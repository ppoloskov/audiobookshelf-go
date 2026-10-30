#!/usr/bin/env python3
"""F01 regression: capability regeneration must never erase reviewed evidence.

The generator (build_capabilities.py) refreshes generated fields from
route-coverage.json, but the ledger also carries reviewed content it cannot
derive: curated evidence/exceptions/invariants, verified cases, state
scenarios, a reviewed record status, reviewed tests/implementation references,
and manually classified stateful GET records.

Every run here writes into a temporary directory passed with --out; the live
ledger at docs/agent-work/capabilities is never touched. For each run this
test asserts:

1. create-missing: every route and subsystem record exists after the first run;
2. preservation: enriched route and subsystem seed records keep their reviewed
   status, stateful classification, cases, state scenarios, invariants,
   evidence, exceptions and tests/implementation references;
3. determinism: the second run over the same directory is byte-identical to
   the first (curated merging and new-record creation are idempotent).
"""

from __future__ import annotations

import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

REPO = Path(__file__).resolve().parents[3]
SCRIPT = REPO / "go-backend/tools/parity/build_capabilities.py"
COVERAGE = REPO / "go-backend/contracts/parity/route-coverage.json"
LIVE_LEDGER = REPO / "docs/agent-work/capabilities"

spec = importlib.util.spec_from_file_location("build_capabilities_f01", SCRIPT)
GENERATOR_MODULE = importlib.util.module_from_spec(spec)
spec.loader.exec_module(GENERATOR_MODULE)

# The generator's default output must remain the live ledger; tests always
# pass --out and never generate into LIVE_LEDGER.
assert GENERATOR_MODULE.OUT == LIVE_LEDGER

REVIEWED_ROUTE_INVARIANT = {
    "id": "INV-F01-REVIEWED-001",
    "class": "shape",
    "contract": "Reviewed invariant attached outside the generator's report inputs.",
    "status": "verified",
    "source": ["server/routers/ApiRouter.js:70"],
    "tests": ["go-backend/tools/parity/oidc-differential.cjs"],
    "evidence": ["go-backend/contracts/parity/reviewed-report.json#seeded-case"],
    "exceptions": ["Reviewed invariant exception note."],
}

F01_SUBSYSTEM_ID = "sub-static-client"


def load_routes() -> list[dict]:
    return json.loads(COVERAGE.read_text())["routes"]


def select_reviewed_get_route(routes: list[dict]) -> dict:
    """Pick a GET candidate that regeneration cannot itself mark verified, so
    a seeded reviewed `verified` status exercises the no-downgrade floor."""
    fails_sampling = lambda route: (  # noqa: E731
        route["upstream"]["method"] == "GET"
        and route["upstream"]["routeKind"] == "string_literal"
        and any(v["sampledPass"] is False for v in route.get("differentialVariants", []))
    )
    unverified = lambda route: (  # noqa: E731
        route["upstream"]["method"] == "GET"
        and route["upstream"]["routeKind"] == "string_literal"
        and not route.get("differentialVariants")
    )
    for pick in (fails_sampling, unverified):
        for route in routes:
            if pick(route):
                return route
    raise AssertionError("no eligible reviewed GET route found in route-coverage.json; "
                         "regenerate coverage and review the F01 seed assumptions")


def seed_route_record(route: dict) -> dict:
    rid = route["id"]
    upstream = route["upstream"]
    return {
        "id": f"route-{rid}",
        "parent": "http/http-reviewed",
        "kind": "operation",
        "scope": "in",
        "status": "verified",
        "contract": (
            f"{upstream['method']} {upstream['route']} candidate recorded from "
            f"{upstream['source']}:{upstream['line']}. Reviewed behavior wording: "
            "method, query variants, session/cookie effects, provider redirect, errors "
            "and state transitions must match upstream v2.37.2."
        ),
        "source": [f"{upstream['source']}:{upstream['line']}", "server/ReviewedSourceOverlay.js:1-9"],
        "implementation": [
            "go-backend/internal/catalog/handler.go",
            "go-backend/tools/parity/reviewed-impl-reference.go",
        ],
        "cases": {"normal": "verified", "error": "verified", "edge": "verified"},
        # F01: stateful GET classification is a manual reviewer override of the
        # method-derived default; regeneration must keep it.
        "stateful": True,
        "tests": [
            "go-backend/internal/identity/identity_test.go",
            "go-backend/tools/parity/oidc-differential.cjs",
        ],
        "evidence": ["go-backend/contracts/parity/reviewed-report.json#seeded-case"],
        "invariants": [REVIEWED_ROUTE_INVARIANT],
        "state_scenarios": {
            "initial": "verified",
            "repeat": "unverified",
            "changed": "unverified",
            "force": "not_applicable",
            "failure": "verified",
        },
        "exceptions": ["Reviewed exception note from a human capability review."],
    }


def seed_subsystem_record() -> dict:
    return {
        "id": F01_SUBSYSTEM_ID,
        "parent": "runtime",
        "kind": "subsystem",
        "scope": "in",
        "status": "verified",
        "contract": "Seed contract note (regeneration refreshes the generated wording).",
        "source": ["server/Server.js", "index.js"],
        "implementation": [
            "go-backend/internal/server/runtime.go",
            "go-backend/tools/parity/browser-check.cjs",
        ],
        "cases": {"normal": "verified", "error": "verified", "edge": "verified"},
        "tests": [
            "go-backend/tools/parity/browser-check.cjs",
            "go-backend/tools/parity/client-check.cjs",
        ],
        "evidence": ["go-backend/contracts/parity/browser-acceptance-report.json"],
        "invariants": [
            {
                "id": "INV-F01-SUBSYSTEM-REVIEWED-001",
                "class": "shape",
                "contract": "Reviewed subsystem invariant the generator cannot derive.",
                "status": "verified",
                "source": ["server/Server.js"],
                "tests": ["go-backend/tools/parity/browser-check.cjs"],
                "evidence": ["go-backend/contracts/parity/browser-acceptance-report.json"],
                "exceptions": [],
            }
        ],
        "exceptions": ["Reviewed subsystem exception note."],
        "stateful": False,
    }


def run_generator(outdir: Path, extra: list[str] | None = None) -> subprocess.CompletedProcess:
    return subprocess.run(
        [sys.executable, str(SCRIPT), "--out", str(outdir), *(extra or [])],
        capture_output=True,
        text=True,
        cwd=REPO,
    )


def ledger_files(outdir: Path) -> dict[str, bytes]:
    return {p.name: p.read_bytes() for p in sorted(outdir.glob("cap-*.json"))}


def load_record(outdir: Path, record_id: str) -> dict:
    return json.loads((outdir / f"cap-{record_id}.json").read_text())


class RegenerationPreservationTest(unittest.TestCase):
    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory(prefix="f01-capabilities-")
        self.outdir = Path(self._tmp.name) / "caps"

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def seed_ledger(self) -> dict[str, dict]:
        self.outdir.mkdir(parents=True)
        routes = load_routes()
        route = select_reviewed_get_route(routes)
        seeds = {f"cap-route-{route['id']}.json": seed_route_record(route),
                 f"cap-{F01_SUBSYSTEM_ID}.json": seed_subsystem_record()}
        for name, record in seeds.items():
            (self.outdir / name).write_text(json.dumps(record, indent=1) + "\n")
        self.seeds = seeds
        self.routes = routes
        self.route_record_id = f"route-{route['id']}"
        return seeds

    def test_regeneration_preserves_reviewed_records_and_is_idempotent(self) -> None:
        self.seed_ledger()

        first = run_generator(self.outdir)
        self.assertEqual(first.returncode, 0, first.stderr)
        self.assertEqual(first.stderr, "", first.stderr)

        expected_count = len(self.routes) + len(GENERATOR_MODULE.SUBSYSTEMS)
        files = ledger_files(self.outdir)
        self.assertEqual(len(files), expected_count)
        for route in self.routes:
            self.assertIn(f"cap-route-{route['id']}.json", files)
        for sub in GENERATOR_MODULE.SUBSYSTEMS:
            self.assertIn(f"cap-{sub['id']}.json", files)

        # Preservation after the first run.
        route_seed = self.seeds[f"cap-{self.route_record_id}.json"]
        route_record = load_record(self.outdir, self.route_record_id)
        self.assertEqual(route_record["status"], "verified")
        self.assertTrue(route_record["stateful"])
        # Reviewed scalars survive regeneration even where the generator
        # derives a weaker/different value.
        self.assertEqual(route_record["contract"], route_seed["contract"])
        for case, _status in route_seed["cases"].items():
            self.assertEqual(route_record["cases"][case], "verified")
        self.assertEqual(route_record["state_scenarios"], route_seed["state_scenarios"])
        self.assertIn(REVIEWED_ROUTE_INVARIANT, route_record["invariants"])
        for key in ("evidence", "exceptions"):
            for value in route_seed[key]:
                self.assertIn(value, route_record[key])
        for key in ("tests", "implementation", "source"):
            for value in route_seed[key]:
                self.assertIn(value, route_record[key], route_record[key])

        subsystem_seed = self.seeds[f"cap-{F01_SUBSYSTEM_ID}.json"]
        subsystem_record = load_record(self.outdir, F01_SUBSYSTEM_ID)
        self.assertEqual(subsystem_record["status"], "verified")
        self.assertEqual(subsystem_record["contract"], subsystem_seed["contract"])
        self.assertFalse(subsystem_record["stateful"])
        for case in ("normal", "error", "edge"):
            self.assertEqual(subsystem_record["cases"][case], "verified")
        self.assertIn("INV-F01-SUBSYSTEM-REVIEWED-001",
                      [inv["id"] for inv in subsystem_record["invariants"]])
        for value in subsystem_seed["tests"]:
            self.assertIn(value, subsystem_record["tests"], subsystem_record["tests"])
        for value in subsystem_seed["evidence"]:
            self.assertIn(value, subsystem_record["evidence"], subsystem_record["evidence"])
        for key in ("exceptions", "implementation", "source"):
            for value in subsystem_seed[key]:
                self.assertIn(value, subsystem_record[key])

        # Second run: byte-identical output, including the seeded records
        # (deterministic preservation) and created records (create-missing).
        after_first = ledger_files(self.outdir)
        second = run_generator(self.outdir)
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(ledger_files(self.outdir), after_first)

        route_record = load_record(self.outdir, self.route_record_id)
        self.assertEqual(route_record["status"], "verified")
        self.assertTrue(route_record["stateful"])

    def test_routes_only_leaves_subsystem_records_untouched(self) -> None:
        self.seed_ledger()
        subsystem_bytes = ledger_files(self.outdir)[f"cap-{F01_SUBSYSTEM_ID}.json"]

        result = run_generator(self.outdir, ["--routes-only"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(ledger_files(self.outdir)[f"cap-{F01_SUBSYSTEM_ID}.json"],
                         subsystem_bytes)

    def test_refuses_to_overwrite_malformed_record(self) -> None:
        self.seed_ledger()
        malformed = self.outdir / f"cap-route-{self.routes[0]['id']}.json"
        malformed.write_text("{ not json")
        malformed_bytes = malformed.read_bytes()

        result = run_generator(self.outdir)

        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn(str(malformed), result.stderr)
        self.assertEqual(malformed.read_bytes(), malformed_bytes)


if __name__ == "__main__":
    unittest.main()