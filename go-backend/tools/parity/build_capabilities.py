#!/usr/bin/env python3
"""Generate the per-capability parity ledger in docs/agent-work/capabilities/.

Each route candidate in contracts/parity/route-coverage.json becomes one
capability record compatible with go-scope-ledger check-ledger.go. A small set
of non-HTTP subsystem capabilities is appended. Regenerate after route-coverage
changes. The mapping from route-coverage status to capability status is
documented in docs/agent-work/capabilities/README.md.

Reviewed evidence (F1 task): records may carry reviewed content this generator
cannot derive — curated evidence/exceptions/invariants, verified cases and
state scenarios, a reviewed status/contract, and reviewed tests/implementation
and source references. Regeneration merges that content additively so a routine
run never erases or downgrades it, and refuses to overwrite a record it cannot
parse. New records are created deterministically; never remove entries here.
Use --out to generate into another (e.g. temporary) directory; the default
output is the live ledger.
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
COVERAGE = ROOT / "go-backend/contracts/parity/route-coverage.json"
OUT = ROOT / "docs/agent-work/capabilities"

CASE_FILES = {
    "core": "go-backend/contracts/parity/core-flow-report.json",
    "mutation": "go-backend/contracts/parity/mutation-flow-report.json",
    "scanner": "go-backend/contracts/parity/scanner-flow-report.json",
    "full": "go-backend/contracts/parity/full-suite-report.json",
}

# Non-HTTP subsystem capabilities: source-side surfaces that are not single
# route candidates. Statuses are deliberately conservative ("implemented"
# means implemented-but-not-fully-verified, never verified parity).
SUBSYSTEMS = [
    {
        "id": "sub-realtime-socketio",
        "parent": "realtime",
        "kind": "subsystem",
        "status": "implemented",
        "contract": "Socket.IO/Engine.IO engine-compatible endpoint on legacy and configured-prefixed paths: connect auth, per-user rooms, event payloads/order/recipients, log listeners, dispatch actions.",
        "source": ["server/SocketAuthority.js", "server/Controller.js"],
        "implementation": ["go-backend/internal/realtime/hub.go"],
        "tests": ["go-backend/internal/realtime/hub_test.go", "go-backend/internal/server/manager_test.go"],
        "evidence": ["go-backend/contracts/parity/core-flow-report.json#socket-connect-prefixed", "go-backend/contracts/parity/core-flow-report.json#socket-connect-legacy"],
        "exceptions": ["Sampled connect handshake compared; per-event recipient and payload variants remain unverified (source-inventory.json lists 165 Socket.IO event candidates)."],
    },
    {
        "id": "sub-database-schema",
        "parent": "database",
        "kind": "data",
        "status": "implemented",
        "contract": "SQLite file absdatabase.sqlite is byte-shape-compatible at the table/row level with the pinned upstream fresh-init schema; existing databases are verified, never migrated or recreated.",
        "source": ["server/Database.js", "server/models/", "server/migrations/"],
        "implementation": ["go-backend/internal/store/store.go", "go-backend/internal/bootstrap/bootstrap.go", "go-backend/internal/server/manager.go"],
        "tests": ["go-backend/internal/store/store_test.go", "go-backend/internal/store/open_write_test.go", "go-backend/internal/bootstrap/bootstrap_test.go"],
        "evidence": ["go-backend/contracts/parity/schema.sql", "go-backend/contracts/library-golden.json"],
        "exceptions": ["Historical schema upgrade/migrations are deferred by user instruction: unverified schema versions must fail clearly."],
    },
    {
        "id": "sub-static-client",
        "parent": "runtime",
        "kind": "subsystem",
        "status": "implemented",
        "contract": "Serves the retained original frontend build at the configured RouterBasePath, preserving index.html fallback and asset paths.",
        "source": ["server/Server.js", "index.js"],
        "implementation": ["go-backend/internal/server/runtime.go"],
        "tests": ["go-backend/internal/server/server_test.go"],
        "evidence": [],
        "exceptions": ["Asset caching header variants not yet differentially verified.", "TASK-0010 fixed the unknown-path divergence: Go now serves assets, directory index.html semantics, the enumerated dynamic-route list, and 404s everything else, matching the upstream default (Nuxt) client branch. router.all('*') catch-all applies only to the Next.js client branch (REACT_CLIENT_PATH), which is a documented env-conditional."],
    },
    {
        "id": "sub-backup-scheduler",
        "parent": "jobs",
        "kind": "job",
        "status": "implemented",
        "contract": "Scheduled and manual backups, restore via graph replacement, backupsToKeep/maxBackupSize/backupSchedule semantics matching upstream DailyBackupManager.",
        "source": ["server/managers/BackupManager.js"],
        "implementation": ["go-backend/internal/backup/backup.go", "go-backend/internal/server/manager.go"],
        "tests": ["go-backend/internal/backup/backup_test.go"],
        "evidence": ["go-backend/tools/parity/test-backup-restore.cjs"],
        "exceptions": ["Executable restore/scheduler/rollback acceptance gates remain to be run as distinct differential scenarios."],
    },
    {
        "id": "sub-scanner-watcher",
        "parent": "scanner",
        "kind": "job",
        "status": "implemented",
        "contract": "Library/item scans, file discovery, sidecar/OPF precedence, watcher add/unlink/rename with scheduled cancellation, metadata writing equivalent to upstream LibraryScan/Scanner",
        "source": ["server/scanner/", "server/managers/TaskManager.js"],
        "implementation": ["go-backend/internal/scanner/scanner.go", "go-backend/internal/scanner/files.go", "go-backend/internal/scanner/metadata.go"],
        "tests": ["go-backend/internal/scanner/scanner_test.go"],
        "evidence": [],
        "exceptions": ["Prior scanner differential report is marked provisional/invalid (pre-isolation shared media) and excluded from evidence counts; a valid re-run is required."],
    },
    {
        "id": "sub-podcast-download-queue",
        "parent": "podcasts",
        "kind": "job",
        "status": "implemented",
        "contract": "Podcast RSS feed ingestion, episode download queue/lifecycle matching upstream PodcastManager episode download task states and files.",
        "source": ["server/managers/PodcastManager.js"],
        "implementation": ["go-backend/internal/podcasts/podcasts.go", "go-backend/internal/podcasts/downloads.go", "go-backend/internal/podcasts/rss.go"],
        "tests": ["go-backend/internal/podcasts/rss_test.go"],
        "evidence": [],
        "exceptions": ["Test TestCreatePodcastValidatesFolderAndPersistsSourceShape currently fails at episode download completion (investigation open)."],
    },
    {
        "id": "sub-oidc-auth",
        "parent": "identity",
        "kind": "behaviour",
        "status": "implemented",
        "contract": "OpenID Connect login/callback/logout/auto-redirect flow parity with upstream OpenIDAuthStrategy, including settings side effects.",
        "source": ["server/auth/OpenIDAuthStrategy.js", "server/controllers/OpenIDController.js"],
        "implementation": ["go-backend/internal/identity/oidc.go", "go-backend/internal/identity/identity.go"],
        "tests": ["go-backend/internal/identity/oidc_test.go", "go-backend/internal/identity/identity_test.go"],
        "evidence": [],
        "exceptions": ["Live-provider OIDC flows not yet differentially verified against a mock identity provider."],
    },
]

STATUS_MAP = {
    "implemented_not_differentially_verified": "implemented",
    "handler_exists_not_integrated": "implemented",
    "not_implemented": "unverified",
}

KIND_MAP = {
    "literal_path_candidate": "operation",
    "dynamic_path_candidate": "behaviour",
    "regex_path_candidate_not_exact": "behaviour",
}


def route_invariant(route: dict, sampled: list) -> list:
    """Evidence-driven invariants for route records.

    A verified invariant is recorded only where a sampled differential variant
    PASSED in a valid run; the invariant text describes exactly what that
    sample covered (status, auth-by-token, sampled request shape) and cites the
    executable harness and report. Routes without sampled variants get no
    invariants (honest unverified state, never invented).
    """
    if not sampled:
        return []
    ids = ", ".join(v["id"] for v in sampled)
    refs = sorted({v["report"] for v in sampled})
    return [
        {
            "id": f"INV-{route['id'].upper()}-SAMPLED",
            "class": "shape",
            "contract": (
                f"Sampled {route['upstream']['method']} {route['upstream']['route']} behavior via differential "
                f"case(s) {ids}: status {sampled[0].get('expectedStatus', 200)}, bearer-token auth path, and "
                f"payload shape/types compare byte-equal (after recorded normalizations) between the pinned "
                f"upstream server and the Go implementation. Covers only the sampled request shape/fixture state; "
                f"query/body/error/permission variants are NOT covered by this invariant."
            ),
            "status": "verified",
            "source": [f"{route['upstream']['source']}:{route['upstream']['line']}"],
            "tests": ["go-backend/tools/parity/differential.cjs"],
            "evidence": [f"go-backend/contracts/parity/{r}#{v['id']}" for r in refs for v in sampled if v["report"] == r],
            "exceptions": [
                "Sampled evidence only; a passing sample does not verify error/edge/permission variants."
            ],
        }
    ]


# List fields whose curated entries must never be erased by regeneration:
# previous entries are kept in their existing order and derived entries are
# appended only when new. This also keeps each run deterministic.
MERGED_LIST_KEYS = (
    "evidence",
    "exceptions",
    "invariants",
    "implementation",
    "source",
    "tests",
)


def write_record(record: dict, outdir: Path) -> bool:
    """Refresh generated fields without erasing manually curated evidence.

    The ledger contains state scenarios, reviewed coverage and evidence that
    this generator cannot derive. When a record already exists, preserve every
    field it does not own, keep reviewed scalars (status, contract) rather than
    overwriting them with freshly derived values, and union the merged list
    fields in previous order. Only when a record is missing is it created from
    the generated content alone. A malformed existing record is left untouched
    and reported so a human can repair it (refuse-overwrite rather than merge).
    """
    path = outdir / f"cap-{record['id']}.json"
    if not path.exists():
        path.write_text(json.dumps(record, indent=1) + "\n")
        return True
    try:
        previous = json.loads(path.read_text())
    except ValueError:
        print(f"refused to overwrite malformed record: {path}", file=sys.stderr)
        return False
    for key, value in previous.items():
        if key not in record:
            record[key] = value
    for key in MERGED_LIST_KEYS:
        if key in previous:
            combined = list(previous[key])
            for value in record.get(key, []):
                if value not in combined:
                    combined.append(value)
            record[key] = combined
    # Cases and stateful describe reviewed coverage beyond the route samples
    # this generator can see. Never downgrade them on regeneration.
    if "cases" in previous:
        generated = record.setdefault("cases", {})
        for case, status in previous["cases"].items():
            if status == "verified" or case not in generated:
                generated[case] = status
    if previous.get("stateful"):
        record["stateful"] = True
    # Reviewer-owned status and contract: regeneration derives these from
    # coverage, but an existing record's reviewed value outranks the derived
    # one in both directions (a curated verified record is not auto-downgraded
    # and a deliberately non-verified record is not auto-promoted).
    if "status" in previous:
        record["status"] = previous["status"]
    if "contract" in previous:
        record["contract"] = previous["contract"]
    path.write_text(json.dumps(record, indent=1) + "\n")
    return True


def main() -> None:
    parser = argparse.ArgumentParser(
        description="Generate the capability parity ledger; reviewed content is preserved, never erased.")
    parser.add_argument(
        "--routes-only", action="store_true",
        help="refresh only route records; subsystem records are left untouched")
    parser.add_argument(
        "--out", type=Path, default=None,
        help="output directory override (tests/temporary runs); "
             "defaults to the live ledger docs/agent-work/capabilities")
    args = parser.parse_args()
    outdir = Path(args.out) if args.out is not None else OUT
    routes_only = args.routes_only
    outdir.mkdir(parents=True, exist_ok=True)
    coverage = json.loads(COVERAGE.read_text())
    evidence_reports = {}
    for name, path in CASE_FILES.items():
        report_path = ROOT / path
        if not report_path.exists():
            continue
        report = json.loads(report_path.read_text())
        for case in report.get("cases", []):
            evidence_reports[case["id"]] = {
                "passed": bool(case.get("passed")),
                "reference": path,
            }

    count = 0
    refusals = 0
    for route in coverage["routes"]:
        go = route["go"]
        upstream = route["upstream"]
        status = STATUS_MAP.get(go["status"], "unverified")
        cases = {"normal": "unverified", "error": "unverified", "edge": "unverified"}
        evidence: list[str] = []
        exceptions: list[str] = []
        tests: list[str] = []
        if go.get("evidence"):
            tests.append(go["evidence"])
        sampled = [v for v in route.get("differentialVariants", []) if v["sampled"]]
        # Evidence lists every sampled variant (pass or FAIL) with its report.
        seen: set[str] = set()
        for v in sampled:
            label = "pass" if v["sampledPass"] else "FAIL"
            entry = f"go-backend/contracts/parity/{v['report']}#{v['id']} (differential {label})"
            if entry not in seen:
                seen.add(entry)
                evidence.append(entry)
        # Map case categories to case verification: 'errors' and 'permissions'
        # (auth/permission-denial responses) samples support error=verified,
        # 'edge-cases' samples support edge=verified, when ALL sampled variants
        # for this route pass.
        all_passing = bool(sampled) and not any(v["sampledPass"] is False for v in sampled)
        if all_passing:
            categories = {v.get("category") for v in sampled}
            cases["normal"] = "verified"
            if categories & {"errors", "permissions"}:
                cases["error"] = "verified"
            if "edge-cases" in categories:
                cases["edge"] = "verified"
        if upstream["routeKind"] != "string_literal":
            exceptions.append(
                f"Synthetic candidate ({upstream['routeKind']}): not a single exact upstream route; "
                "requires runtime-path reconciliation during verification."
            )
        if go["status"] == "handler_exists_not_integrated":
            exceptions.append(
                f"Handler exists in package {go['ownerPackage']} but is not dispatched by internal/server/runtime.go."
            )
        if upstream["method"] == "GET" and upstream["route"] == "/^\\/libraries/":
            # Contract decision (TASK-0005): this candidate is the ApiCacheManager
            # middleware, not a route. Its invalidation hooks clear entries on
            # every model write, so a cache hit always serves the bytes a fresh
            # recompute serves under complete invalidation. Computing responses
            # fresh in Go is observably equivalent in all verified scenarios and
            # avoids cache-staleness reproduction of upstream's own stale edges.
            status = "characterized"
            exceptions.append(
                "Behavioral decision (TASK-0005, go-backend internal equivalent): the middleware is an "
                "in-process LRU response cache keyed by user+URL with full invalidation on model writes. "
                "The Go server computes every response fresh, which is observably equivalent under the "
                "complete-invalidation contract; upstream cache staleness edges are not reproduced. "
                "Documented, not silently patched."
            )
        if go["status"] == "not_implemented" and upstream["routeKind"] == "string_literal":
            exceptions.append("No Go implementation exists yet (explicit 501 fallback upstream-side route).")
        stateful = upstream["method"] in {"POST", "PATCH", "DELETE", "PUT"}
        if all_passing:
            # Status verified ONLY when all three cases are verified (the
            # validator requires no unverified case on a verified record) and
            # the record is non-stateful; stateful records additionally need
            # scenario evidence and stay implemented until then.
            if not stateful and all(v == "verified" for v in cases.values()):
                status = "verified"
        record = {
            "id": f"route-{route['id']}",
            "parent": f"http/{go['ownerPackage'] or 'unassigned'}",
            "kind": KIND_MAP.get(route["candidateClass"], "operation"),
            "scope": "in",
            "status": status,
            "contract": (
                f"{upstream['method']} {upstream['route']} candidate recorded from {upstream['source']}:"
                f"{upstream['line']}. Method, path, query/body variants, auth/permission gates, payload "
                f"shape/types, errors and side effects must match upstream v2.37.2."
            ),
            "source": [f"{upstream['source']}:{upstream['line']}"],
            "implementation": ([go["evidence"]] if go.get("evidence") else []),
            "cases": cases,
            "stateful": upstream["method"] in {"POST", "PATCH", "DELETE", "PUT"},
            "tests": tests,
            "evidence": evidence,
            "exceptions": exceptions,
        }
        # Full-case verification: a route record is upgraded to status verified
        # ONLY when all three cases are verified AND a verified invariant
        # exists. Stateful records additionally need verified state scenarios
        # plus a repeat_operation invariant; until persisted-state evidence is
        # attached per record, they stay implemented (honest gap).
        stateful = upstream["method"] in {"POST", "PATCH", "DELETE", "PUT"}
        if all_passing:
            case_ids = ", ".join(v["id"] for v in sampled)
            refs = sorted({v["report"] for v in sampled})
            record["invariants"] = [
                {
                    "id": f"INV-{route['id'].upper()}-SAMPLED",
                    "class": "shape",
                    "contract": (
                        f"Sampled {upstream['method']} {upstream['route']} via differential case(s) {case_ids}: "
                        f"status, auth path and payload shape/types compare equal (after recorded "
                        f"normalizations) between the pinned upstream server and the Go implementation. "
                        f"Category sample{"s" if len(sampled) != 1 else ""}: "
                        + ", ".join(sorted(str(c) for c in {v.get("category") for v in sampled}))
                        + "."
                    ),
                    "status": "verified",
                    "source": [f"{upstream['source']}:{upstream['line']}"],
                    "tests": ["go-backend/tools/parity/differential.cjs"],
                    "evidence": [f"go-backend/contracts/parity/{r}#{v['id']}" for r in refs for v in sampled if v["report"] == r],
                    "exceptions": [
                        "Sampled evidence; unsampled request-shape variants are separate behaviors."
                    ],
                }
            ]
        if not write_record(record, outdir):
            refusals += 1
        count += 1

    if refusals:
        print(f"refused {refusals} malformed existing record(s); repair or remove them, then rerun", file=sys.stderr)
        raise SystemExit(2)

    if routes_only:
        print(f"wrote {count} route capability records to {outdir} (routes-only; subsystem records untouched)")
        return

    for sub in SUBSYSTEMS:
        record = {
            "id": sub["id"],
            "parent": sub["parent"],
            "kind": sub["kind"],
            "scope": "in",
            "status": sub["status"],
            "contract": sub["contract"],
            "source": sub["source"],
            "implementation": sub["implementation"],
            "cases": {"normal": "unverified", "error": "unverified", "edge": "unverified"},
            "tests": sub["tests"],
            "evidence": sub["evidence"],
            "exceptions": sub["exceptions"],
        }
        if not write_record(record, outdir):
            refusals += 1
        count += 1

    if refusals:
        print(f"refused {refusals} malformed existing record(s); repair or remove them, then rerun", file=sys.stderr)
        raise SystemExit(2)

    print(f"wrote {count} capability records to {outdir}")


if __name__ == "__main__":
    main()
