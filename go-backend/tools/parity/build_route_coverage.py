#!/usr/bin/env python3
"""Build an explicit Go-vs-upstream route coverage ledger from the inventory."""
from __future__ import annotations

import argparse
import hashlib
import json
import re
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
INVENTORY = ROOT / "go-backend/contracts/source-inventory.json"
GO_SERVER = ROOT / "go-backend/internal/server/server.go"
GO_RUNTIME = ROOT / "go-backend/internal/server/runtime.go"
OUTPUT = ROOT / "go-backend/contracts/parity/route-coverage.json"
PINNED = "edb1f7efc92ecacdef51aafb4d9d5054ce10604f"
GO_SOURCES = {
    "adminops": ROOT / "go-backend/internal/adminops/handler.go",
    "audiotools": ROOT / "go-backend/internal/audiotools/tools.go",
    "backup": ROOT / "go-backend/internal/backup/backup.go",
    "catalog": ROOT / "go-backend/internal/catalog/handler.go",
    "emailapi": ROOT / "go-backend/internal/emailapi/handler.go",
    "identity": ROOT / "go-backend/internal/identity/identity.go",
    "notifications": ROOT / "go-backend/internal/notifications/handler.go",
    "podcasts": ROOT / "go-backend/internal/podcasts/podcasts.go",
    "providers": ROOT / "go-backend/internal/providers/handler.go",
    "userapi": ROOT / "go-backend/internal/userapi/userapi.go",
    "playback": ROOT / "go-backend/internal/playback/playback.go",
    "scanner": ROOT / "go-backend/internal/scanner/scanner.go",
    "server": ROOT / "go-backend/internal/server/runtime.go",
    "sharing": ROOT / "go-backend/internal/sharing/handler.go",
}

CASES_FILE = ROOT / "go-backend/contracts/parity/differential-cases.json"
EVIDENCE_FILES = [
    ROOT / "go-backend/contracts/parity/differential-flow-report.json",
    ROOT / "go-backend/contracts/parity/core-flow-report.json",
    ROOT / "go-backend/contracts/parity/mutation-flow-report.json",
    ROOT / "go-backend/contracts/parity/scanner-flow-report.json",
    ROOT / "go-backend/contracts/parity/full-suite-report.json",
]


def owner_for(route: dict) -> str | None:
    p, source = route["route"], route["source"]
    if source == "server/Auth.js":
        return "identity"
    if source == "server/routers/HlsRouter.js":
        return "playback"
    if source == "server/routers/PublicRouter.js":
        return "sharing" if "/share" in p or "/feed" in p else "playback"
    if source == "server/Server.js":
        if p.startswith("/feed/"):
            return "sharing"
        if p == "/init":
            return "identity"
        return "server"
    if source != "server/routers/ApiRouter.js":
        return None
    if p.startswith("/libraries/") and p.endswith("/scan") or p in {"/items/:id/scan", "/items/batch/scan"}:
        return "scanner"
    if p.startswith("/backups"):
        return "backup"
    if p.startswith("/emails"):
        return "emailapi"
    if p.startswith("/notifications"):
        return "notifications"
    if p.startswith("/search") or p.startswith("/custom-metadata-providers"):
        return "providers"
    if p == "/notificationdata":
        return "notifications"
    if p.startswith("/tools"):
        return "audiotools"
    if p.startswith("/tasks") or p in {"/encode-m4b"}:
        return "audiotools"
    if p.startswith("/podcasts"):
        return "podcasts"
    if p.startswith("/authorize") or p == "/init":
        return "identity"
    if p.startswith("/share") or p.startswith("/public/share") or p.startswith("/shares") or p.startswith("/feed"):
        return "sharing"
    if p.startswith("/upload"):
        return "adminops"
    if p in {"/stats/year/:year", "/stats/server", "/logger-data", "/validate-cron",
             "/auth-settings", "/watcher/update", "/sorting-prefixes"} or p.startswith("/tags") or p.startswith("/genres") or p.startswith("/filesystem") or p.startswith("/cache"):
        return "adminops"
    if p.startswith("/public/share") or p.startswith("/shares") or p.startswith("/feed"):
        return "sharing"
    if p.startswith("/me") or p.startswith("/users") or p.startswith("/api-keys") or p.startswith("/collections") or p.startswith("/playlists"):
        return "userapi"
    if p.startswith("/session") or p.startswith("/sessions") or p.startswith("/items/:id/play"):
        return "playback"
    if p.startswith("/libraries") or p.startswith("/items") or p.startswith("/series") or p.startswith("/authors"):
        return "catalog"
    if p.startswith("/settings") or p.startswith("/server") or p.startswith("/users"):
        return "adminops"
    return None


def case_matches(route: dict, case: dict) -> bool:
    if route["route_kind"] == "regex_literal" or case["method"].upper() != route["method"].upper():
        return False
    source_path = route["route"]
    case_path = case["route"].split("?", 1)[0]
    if case_path.startswith("/api/"):
        case_path = case_path[4:]
    if case_path.startswith("/audiobookshelf/"):
        case_path = case_path[len("/audiobookshelf"):]
    mount = route.get("prefix_context", {}).get("known_mount")
    if mount and mount != "/api":
        if case_path == mount:
            case_path = "/"
        elif case_path.startswith(mount + "/"):
            case_path = case_path[len(mount):]
        else:
            return False
    sample_path = re.sub(r"\{[^{}]+\}", "fixture", case_path)

    def pattern(template: str) -> re.Pattern[str]:
        out = "^"
        for segment in template.strip("/").split("/"):
            if re.fullmatch(r":[A-Za-z0-9_]+\?", segment):
                out += r"(?:/[^/]+)?"
            elif re.fullmatch(r":[A-Za-z0-9_]+", segment) or re.fullmatch(r"\{[^{}]+\}", segment):
                out += r"/[^/]+"
            else:
                out += "/" + re.escape(segment)
        return re.compile(out + "$")

    try:
        return bool(pattern(source_path).match(sample_path))
    except re.error:
        return False


def implementation_for(route: dict, owner: str | None) -> tuple[str, str | None, str | None]:
    method, p, source = route["method"].upper(), route["route"], route["source"]
    module = owner
    prefix = "/api" if source == "server/routers/ApiRouter.js" else ""
    if source == "server/routers/HlsRouter.js":
        prefix = "/hls"
        p = "/:stream/:file"
    elif source == "server/routers/PublicRouter.js" and owner == "sharing":
        prefix = "/public"
    if module is None or module not in GO_SOURCES or not GO_SOURCES[module].exists():
        return "not_implemented", None, None
    go_route = f"{method} {prefix}{p}"
    if module == "server":
        # runtime.go is part of the server package itself, so it is integrated
        # by construction; the import-search heuristic only fits other packages.
        integrated = True
    else:
        integrated = f'"audiobookshelf-compat/internal/{module}"' in GO_RUNTIME.read_text()
    status = "implemented_not_differentially_verified" if integrated else "handler_exists_not_integrated"
    return status, go_route, f"go-backend/internal/{module}/{GO_SOURCES[module].name}"


def cases_for(route: dict, cases: list[dict]) -> list[dict]:
    return [case for case in cases if case_matches(route, case)]


def sha(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def build() -> dict:
    inventory = json.loads(INVENTORY.read_text())
    cases = json.loads(CASES_FILE.read_text())["cases"] if CASES_FILE.exists() else []
    evidence = {}
    evidence_files = []
    for report_path in EVIDENCE_FILES:
        if not report_path.exists():
            continue
        evidence_files.append(str(report_path.relative_to(ROOT)))
        try:
            report = json.loads(report_path.read_text())
        except json.JSONDecodeError:
            continue
        if report.get("validForCompatibilityCounts") is False:
            continue
        for case in report.get("cases", []):
            if case.get("id"):
                evidence[case["id"]] = {"passed": bool(case.get("passed")), "report": report_path.name}
    routes = []
    status_counts = {}
    route_kind_counts = {}
    mapped_cases = set()
    for index, route in enumerate(inventory["routes"]):
        method = route["method"].upper()
        upstream_path = route["route"]
        owner = owner_for(route)
        implementation, go_route, owner_evidence = implementation_for(route, owner)
        route_cases = cases_for(route, cases)
        mapped_cases.update(case["id"] for case in route_cases)
        variants = []
        for case in route_cases:
            sampled = evidence.get(case["id"])
            variants.append({
                "id": case["id"],
                "category": case.get("category"),
                "sampled": sampled is not None,
                "sampledPass": sampled["passed"] if sampled else None,
                "report": sampled["report"] if sampled else None,
            })
        status_counts[implementation] = status_counts.get(implementation, 0) + 1
        route_kind_counts[route["route_kind"]] = route_kind_counts.get(route["route_kind"], 0) + 1
        classification = {
            "string_literal": "literal_path_candidate",
            "dynamic_expression": "dynamic_path_candidate",
            "regex_literal": "regex_path_candidate_not_exact",
        }.get(route["route_kind"], "unknown_syntax_candidate")
        routes.append({
            "id": f"r{index:03d}",
            "candidateClass": classification,
            "upstream": {
                "method": method,
                "route": upstream_path,
                "source": route["source"],
                "line": route["line"],
                "routeKind": route["route_kind"],
                "mount": route["prefix_context"],
                "middlewareArgumentCountHint": route["argument_count_syntax_hint"],
                "middlewareBehaviorResolved": route.get("middleware_behavior_resolved", False),
            },
            "go": {"ownerPackage": owner, "status": implementation, "route": go_route, "evidence": owner_evidence},
            "differentialVariants": variants,
            "differentialVariantCount": len(variants),
            "sampledVariantCount": sum(variant["sampled"] for variant in variants),
            "sampledPassingVariantCount": sum(variant["sampledPass"] is True for variant in variants),
            "sampledFailingVariantCount": sum(variant["sampledPass"] is False for variant in variants),
            "compatibilityStatus": "unverified",
        })
    return {
        "schemaVersion": 1,
        "baseline": PINNED,
        "inventorySourceRevision": inventory["source_revision"],
        "inventorySha256": sha(INVENTORY),
        "goSourceHashesAtGeneration": {
            "go-backend/internal/server/server.go": sha(GO_SERVER),
            "go-backend/internal/server/runtime.go": sha(GO_RUNTIME),
        } | {
            f"go-backend/internal/{name}/{source.name}": sha(source)
            for name, source in GO_SOURCES.items() if source.exists()
        },
        "limitations": [
            "All 226 entries are source-syntax candidates, not resolved Express routes; mount paths and middleware behavior are not resolved.",
            "A string literal entry records a candidate method and path only. Regex and dynamic-expression entries are clearly marked and are not exact-path coverage.",
            "Middleware argument counts are syntax hints only; they do not establish authentication, authorization, validation, or response behavior.",
            "A generic Go 501 fallback is not evidence that the upstream route returns 501 or that the route is handled correctly.",
            "A passing executable case samples only its listed request shape and fixture state. CompatibilityStatus remains unverified for every route until explicit review of relevant variants.",
            "Query, body, permission, content-type, image-cache, and error variants are separate behaviors and are not implied by a path match.",
        ],
        "falsePositiveInventory": [
            {"source": "Express mount paths", "risk": "ApiRouter, PublicRouter, HlsRouter and Auth mount prefixes can be nested or runtime-configured; the extractor does not resolve them."},
            {"source": "Middleware chain", "risk": "The middleware argument count does not reveal permission checks or whether a handler returns early."},
            {"source": "Regex route candidate", "risk": "A regex path can match many concrete paths and is not one exact route."},
            {"source": "Dynamic route expression", "risk": "A computed path may be incomplete or may expand into multiple routes."},
            {"source": "Go catch-all 501", "risk": "The experimental server's fallback can hide missing handler integration during manual probing; compare only against source-executed upstream responses."},
            {"source": "Cache and image variants", "risk": "One cover path can vary by raw mode, format, dimensions, cache headers and response bytes."},
            {"source": "External providers", "risk": "Network-backed provider routes need separate mock-backed and live-network test categories; neither implies the other."},
        ],
        "evidenceFiles": evidence_files,
        "counts": {
            "inventoryRoutes": len(routes),
            "compatibilityVerified": 0,
            "unverified": len(routes),
            "handlerCandidates": sum(r["go"]["status"] != "not_implemented" for r in routes),
            "goStatusCounts": status_counts,
            "syntaxCandidateCounts": route_kind_counts,
            "routesWithDifferentialVariants": sum(r["differentialVariantCount"] > 0 for r in routes),
            "declaredDifferentialVariants": len(mapped_cases),
            "sampledDifferentialVariants": len(evidence),
            "sampledPassingVariants": sum(value["passed"] for value in evidence.values()),
            "sampledFailingVariants": sum(not value["passed"] for value in evidence.values()),
        },
        "routes": routes,
    }


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--check", action="store_true", help="fail if checked-in ledger is stale")
    parser.add_argument("--output", type=Path, default=OUTPUT)
    args = parser.parse_args()
    generated = build()
    encoded = json.dumps(generated, indent=2) + "\n"
    if args.check:
        if not args.output.exists() or args.output.read_text() != encoded:
            raise SystemExit("route coverage ledger is stale; regenerate with build_route_coverage.py")
        print(f"route coverage: {generated['counts']['inventoryRoutes']} routes, all compatibility status unverified")
    else:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(encoded)
        print(f"wrote {args.output} ({generated['counts']['inventoryRoutes']} routes)")


if __name__ == "__main__":
    main()
