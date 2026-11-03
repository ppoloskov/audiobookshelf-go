#!/usr/bin/env python3
"""Build docs/specs/planned/parity-finalization-traceability.json (queue row F03).

Read-only over the source inventory, route coverage and capability ledger:
it maps EVERY current capability record, route candidate and Socket.IO event
candidate to recorded evidence and one dispatch task (an approved F-row or a
finite numbered F45/F46/F51 child). Excluded items cite their owner approval.
All counts are read from the current files, never assumed.

Regenerate with python3 after route-coverage or capability changes; the
generator aborts on unmapped IDs so orphaned capabilities cannot slip into
the mapping silently.
"""
from __future__ import annotations

import glob
import json
import re
import subprocess
from pathlib import Path

REPO = Path(__file__).resolve().parents[3]
OUT = REPO / "docs/specs/planned/parity-finalization-traceability.json"
INVENTORY = REPO / "go-backend/contracts/source-inventory.json"
COVERAGE = REPO / "go-backend/contracts/parity/route-coverage.json"
CAPABILITIES_DIR = REPO / "docs/agent-work/capabilities"
BASELINE = "edb1f7efc92ecacdef51aafb4d9d5054ce10604f"

# Runtime mount reconciliation (F03: "resolve aliases/runtime registrations"),
# verified by reading server/Server.js wiring:
#   Server.js:334  app.use(global.RouterBasePath, router)   — base mount
#   Server.js:354  router.use('/api', auth.ifAuthNeeded(authMiddleware), apiRouter.router)
#   Server.js:355  router.use('/hls', hlsRouter.router)
#   Server.js:356  router.use('/public', publicRouter.router)
#   Server.js:359  router.use(express.static(appRoot/static))
#   Auth.js routes init directly on the server router (initAuthRoutes(router))
# Every candidate's registered path is the Express-stripped path; the public
# path template is base + mount + registered path.
REGISTRATION_SITE = {
    "server/routers/ApiRouter.js": {
        "mount": "/api",
        "wiring": "server/Server.js:354",
        "composition": [
            "app: server/Server.js:262,324 request middlewares (security headers/proxy handling)",
            "app: server/Server.js:300 cookieParser",
            "app: server/Server.js:301-313 signed cookie session (MemoryStore 24h TTL)",
            "app: server/Server.js:315-317 passport initialize/session (ifAuthNeeded)",
            "router: server/Server.js:340-348 multipart parser skipping /internal-api",
            "router: server/Server.js:349 express.urlencoded({extended:true,limit:'5mb'})",
            "router: server/Server.js:352 express.json({limit:'10mb'}) skipping /internal-api",
            "mount: server/Server.js:354 auth.ifAuthNeeded(authMiddleware) (passport-jwt bearer/api-key with ignoreExpiration on API keys, LocalAuthStrategy/Strategy per user)",
        ],
    },
    "server/routers/HlsRouter.js": {
        "mount": "/hls",
        "wiring": "server/Server.js:355",
        "composition": ["router: server/Server.js:340-352 multipart/urlencoded/JSON parsers", "mount: server/Server.js:355 (no extra middleware; stream auth resolved inside HlsRouter.streamFileRequest)"],
    },
    "server/routers/PublicRouter.js": {
        "mount": "/public",
        "wiring": "server/Server.js:356",
        "composition": ["router: server/Server.js:340-352 multipart/urlencoded/JSON parsers", "mount: server/Server.js:356 (public share/session URLs)"],
    },
    "server/Auth.js": {
        "mount": "",
        "wiring": "server/Server.js:388 (initAuthRoutes(router) registers on the base router)",
        "composition": ["app: cookieParser + passport session as above", "route-level: authRateLimiter where the registration line includes it"],
    },
    "server/Server.js": {
        "mount": "",
        "wiring": "server/Server.js:334-406 (direct registrations on the base router)",
        "composition": ["app: body parsers/security middlewares as above", "route-level: no per-route middleware unless the registration line includes it"],
    },
}

# Env-conditional registration characterizations (resolved from the source).
CONDITIONAL_SITES = {
    "server/Server.js:437": {
        "id": "r014",
        "characterization": "Registration loop over the 21 enumerated dynamic client routes (dynamicRoutes.forEach, DEFAULT Nuxt branch, REACT_CLIENT_PATH unset): each enumerated route serves client/dist/index.html (server/Server.js:437). The pinned Go static-client serves exactly this list (TASK-0010 decision, tested in internal/server) — per-route behavior is verified through cap-sub-static-client.",
        "registeredPaths": [
            "/item/:id", "/author/:id", "/audiobook/:id/chapters", "/audiobook/:id/edit",
            "/audiobook/:id/manage", "/library/:library", "/library/:library/search",
            "/library/:library/bookshelf/:id?", "/library/:library/authors", "/library/:library/narrators",
            "/library/:library/stats", "/library/:library/series/:id?", "/library/:library/podcast/search",
            "/library/:library/podcast/latest", "/library/:library/podcast/download-queue",
            "/config/users/:id", "/config/users/:id/sessions", "/config/item-metadata-utils/:id",
            "/collection/:id", "/playlist/:id", "/share/:slug",
        ],
        "task": "F44",
    },
    "server/Server.js:452": {
        "id": "r015",
        "characterization": "router.all('*') exists ONLY when REACT_CLIENT_PATH is set (Next.js client branch, server/Server.js:437-460). The pinned default deployment (REACT_CLIENT_PATH unset) registers the enumerated Nuxt dynamic client routes (server/Server.js:416-437) and falls through to the Express default 404 HTML error page for everything else; the Go replacement's 501-fallback for unimplemented upstream routes and its unknown-path 404 fidelity are queue row F44's exact scope.",
        "task": "F44",
    },
}

# First match wins: method-conditioned and narrow patterns before broad ones.
# Each rule: (package, method or None, route regex, owning task, reason).
ROUTE_RULES = [
    ("identity", None, r"^/auth/openid/(callback|config)$", "F38",
     "remaining OIDC callback/provider branches; reuse the retained 24 selected cases"),
    ("identity", None, r"^/auth/openid", "F37",
     "OIDC session expiry/renewal; 24-hour session TTL and 10-minute mobile redirect map"),
    ("identity", None, r"^/init$", "F04",
     "fresh initialization route belongs with existing-database startup verification"),
    ("identity", None, r"^/(login|logout|auth/refresh|authorize)$", "F05",
     "local login/refresh/logout and session/token effects"),
    ("unassigned", "GET", r"libraries", "F45",
     "ApiCacheManager middleware candidate (TASK-0005 decision; not a route)"),
    ("unassigned", None, r"", "F51", "unassigned inventory candidate (F51 child required)"),
    ("server", None, r"^/(status|ping|healthcheck)$", "F06",
     "unauthenticated health/static responses on the served client path"),
    ("server", None, r"", "F44", "unknown/dynamic path and method fidelity on served prefixes"),
    ("playback", None, r"^/items/:id/play", "F08", "direct play/session start observed with real media"),
    ("playback", None, r"^/:stream/:file", "F08", "direct stream serving with byte ranges"),
    ("playback", None, r"^/session", "F12", "playback-session lifecycle, sync and close effects"),
    ("playback", None, r"^/sessions", "F12", "playback-session reads and management"),
    ("backup", None, r"^/backups/:id/apply$", "F31",
     "restore while requests/jobs are active plus injected restore failure"),
    ("backup", None, r"^/backups", "F30", "backup create/lifecycle and upstream import"),
    ("backup", None, r"^/backups", "F32", "scheduler/rolloff boundary (fallback for path config)"),
    ("scanner", None, r"^/libraries/:id/scan$", "F16", "initial/unchanged repeat import semantics"),
    ("scanner", None, r"/scan", "F17", "changed-input/forced rescan reconciliation"),
    ("catalog", None, r"^/libraries/:id/episode-downloads", "F34", "episode download listing with the queue family"),
    ("catalog", None, r"^/libraries/:id/(recent-episodes|podcast-titles)", "F33", "podcast episode catalog family"),
    ("catalog", None, r"^/libraries/:id/(collections|playlists)", "F25",
     "collection/playlist ownership and mutation lifecycle"),
    ("catalog", None, r"^/libraries/:id/opml", "F33", "podcast OPML export belongs to the feed family"),
    ("catalog", None, r"^/libraries/:id/download", "F28", "library archive download family"),
    ("catalog", None, r"^/libraries/:id/issues", "F29", "issue cleanup alongside destructive cascades"),
    ("catalog", None, r"^/libraries/:id", "F21",
     "library list/detail/queries, filters, ordering, pagination and search"),
    ("catalog", "delete", r"^/items/batch", "F29", "batch item deletion is destructive"),
    ("catalog", None, r"^/items/batch/get", "F21", "batch read family"),
    ("catalog", None, r"^/items/batch/quickmatch", "F36", "batch matching is a provider operation"),
    ("catalog", None, r"^/items/batch/update", "F22", "batch item edits"),
    ("catalog", "delete", r"^/items/:id$", "F29", "item deletion is destructive"),
    ("catalog", None, r"^/items/:id/cover", "F26", "cover/image store and replacement behavior"),
    ("catalog", None, r"/download$", "F28", "file/download family (incl. Apple-mobile/X-Accel variants)"),
    ("catalog", "delete", r"/file/:fileid$", "F29", "library-file deletion is destructive"),
    ("catalog", None, r"^/items/:id/ebook", "F51-01",
     "owner-approved ebook compatibility-stub boundary (TASK-20261030)"),
    ("catalog", None, r"^/items/:id/file", "F28", "library-file access family"),
    ("catalog", None, r"^/items/:id/(match|tracks|chapters|metadata-object|media|ffprobe)", "F22",
     "item edits, metadata writes and inspection reads (ffprobe inspection runs as subprocess here)"),
    ("catalog", None, r"^/items/:id", "F21", "item detail reads"),
    ("catalog", None, r"^/authors/:id/image", "F26", "author image follows cover family"),
    ("catalog", None, r"^/(authors|series)/:id", "F22", "author/series detail and edits"),
    ("catalog", None, r"", "F21", "remaining catalog read/edit families"),
    ("userapi", None, r"^/(users|api-keys)", "F24", "user/credential administration with session effects"),
    ("userapi", None, r"^/me/password", "F24", "credential change lifecycle"),
    ("userapi", None, r"^/me/ereader-devices", "F40", "email device management"),
    ("userapi", None, r"^/(collections|playlists)", "F25", "collection/playlist ownership and mutations"),
    ("userapi", None, r"^/me$", "F05", "authenticated identity read"),
    ("userapi", None, r"^/me", "F12", "per-user progress, bookmarks, sessions and stats"),
    ("userapi", None, r"", "F24", "remaining userapi families"),
    ("emailapi", None, r"", "F39", "email settings/test/delivery variants"),
    ("notifications", None, r"", "F41", "notification settings/CRUD; F42 covers per-ID test/lifecycle events"),
    ("podcasts", None, r"/downloads?", "F34", "download queue lifecycle"),
    ("podcasts", None, r"/match-episodes", "F36", "episode matching is a provider operation"),
    ("podcasts", None, r"", "F33", "RSS ingestion, refresh and episode reads"),
    ("providers", None, r"", "F36", "provider response mapping and failure"),
    ("sharing", None, r"", "F35", "public feed/share access, expiry and revocation"),
    ("audiotools", None, r"", "F43", "audio merge/embed job lifecycle and task manager"),
    ("adminops", None, r"^/settings$|validate-cron|auth-settings|sorting-prefixes", "F45", "server settings/config family"),
    ("adminops", None, r"^/watcher", "F20", "watcher lifecycle"),
    ("adminops", None, r"^/tags|^/genres", "F51-03", "tags/genres admin CRUD (F51 child)"),
    ("adminops", None, r"^/filesystem", "F51-04", "admin filesystem access (F51 child)"),
    ("adminops", None, r"^/cache", "F51-05", "cache maintenance (F51 child; TASK-0005 decision context)"),
    ("adminops", None, r"^/stats|^/logger-data", "F51-06", "server stats and logger data (F51 child)"),
    ("adminops", None, r"^/upload", "F51-07", "upload lifecycle (F51 child)"),
    ("adminops", None, r"", "F51", "unmapped maintenance/admin family (F51 child required)"),
]

TASK_BY_SUBSYSTEM = {
    "sub-authorization-ownership-w5": ["F23"],
    "sub-backup-scheduler": ["F30", "F31", "F32"],
    "sub-covers": ["F26", "F27"],
    "sub-database-schema": ["F04", "F07"],
    "sub-destructive-cascades-w6": ["F29"],
    "sub-downloads": ["F28"],
    "sub-oidc-auth": ["F37", "F38"],
    "sub-podcast-download-queue": ["F33", "F34"],
    "sub-realtime-socketio": ["F14", "F46"],
    "sub-scanner-watcher": ["F16", "F17", "F18", "F19", "F20"],
    "sub-static-client": ["F06", "F45"],
}

EXCLUSIONS = [
    {
        "item": "Ebook full engine support (ebook-only item import and reading)",
        "ownerApproval": "TASK-20261030 Decisions: owner instruction approved compatibility-stub exception; safe grouping/handling only",
        "task": "F51-01",
    },
    {
        "item": "Historical schema upgrade/migration of unverified (non-2.37.2) database versions",
        "ownerApproval": "TASK-20261030 Decisions + CONTRACT: deferred by user instruction; fail clearly instead of migrating",
        "task": "F04",
    },
    {
        "item": "NUSQLITE3 native full-text-search extension runtime",
        "ownerApproval": "CONTRACT + ROADMAP: Go-side equivalent search semantics required and must be tested before any parity claim",
        "task": "F51-02",
    },
    {
        "item": "Upstream in-process LRU response-cache staleness edges (fresh compute used instead)",
        "ownerApproval": "TASK-0005 decision, recorded in cap-route-r016 exceptions",
        "task": "F45",
    },
    {
        "item": "One malformed omitted-urls notification request: upstream process-exit behavior",
        "ownerApproval": "TASK-20261030: reproducible upstream-side divergence intentionally left unverified; isolation oracle required (queue F42)",
        "task": "F42",
    },
]

F51_ROUTE_CHILDREN = [
    {"id": "F51-01", "scope": "Ebook file access/status routes (r069, r070); owner-approved stub boundary"},
    {"id": "F51-02", "scope": "NUSQLITE3 full-text-search equivalent semantics (contract-gated)"},
    {"id": "F51-03", "scope": "Tags/genres admin CRUD: r208-r213"},
    {"id": "F51-04", "scope": "Admin filesystem access: r129, r130"},
    {"id": "F51-05", "scope": "Cache maintenance: r181, r182 (TASK-0005 cache decision context)"},
    {"id": "F51-06", "scope": "Server stats/logger: r197, r198, r218"},
    {"id": "F51-07", "scope": "Upload lifecycle: r203"},
]

F04_F06_PACKETS = {
    "F04": {
        "taskID": "F04",
        "outcome": "one behavior family: Go startup on representative pinned existing v2.37.2 databases including recorded partial-state cases; unsupported historical schemas refused without mutation. Compare startup outcomes, logical schema/rows and files before/after; no implicit migration or recreation.",
        "requirement": "CONTRACT owner direction 2026-10-30 (existing-database deferment lifted) + DATABASE treatment row; capabilities sub-database-schema (F04/F07), route-r010.",
        "alreadyVerified": "existing-db-matrix.cjs baseline cases (2.37.2 boots, stale/newer maxVersion refusal, missing migrationsMeta refusal) remain applicable; reuse them, extend with partial-state and file-effects variants.",
        "ownedFiles": "go-backend/internal/bootstrap/ + go-backend/internal/store/ tests; harness variant additions in tools/parity/existing-db-matrix.cjs only if a new observation is needed.",
        "inputs": "fresh fixture manifest via create-fixture.cjs; partial-state seeds documented in existing-db-matrix.cjs; exact Node v22.23.4 path from handover gotchas; ports distinct per run.",
        "expectedOutputs": "startup exit codes/messages; schema row/file comparison; refusal messages; no migration rows written.",
        "scenarios": "normal boot; partial upgrade states within pinned release; stale and newer versions; repeat boot; restart after failed boot.",
        "nonGoals": "playback, browsing, scanner; no migration implementation.",
        "commands": "node go-backend/tools/parity/existing-db-matrix.cjs variants; CGO_ENABLED=0 go test ./internal/bootstrap ./internal/store; record N/M/S + exits",
    },
    "F05": {
        "taskID": "F05",
        "outcome": "one behavior family: local login, refresh, logout and inactive/restricted user observations needed by the retained client; verify cookie/token usability and persisted revocation; fill only missing local-session cases.",
        "requirement": "CONTRACT tokens/sessions row; server/auth/LocalAuthStrategy.js; TokenManager; capabilities route-r000/r001/r006/r099 identity rows.",
        "alreadyVerified": "login/reader-login/full-suite and OIDC local-auth-selected cases; reuse unchanged.",
        "ownedFiles": "verification-first: existing differential harness case families. Any repair is separate per-transition packet in internal/identity.",
        "inputs": "fresh fixture copies per side per rule; distinct ports; refresh token lifetime fixtures.",
        "expectedOutputs": "status/headers/Set-Cookie/JSON per transition; persisted session rows before/after.",
        "scenarios": "normal, wrong-password, inactive user, restricted library access, repeated refresh, post-logout reuse, concurrent refresh grace.",
        "nonGoals": "OIDC variants (F37/F38), user CRUD (F24), progress (F12).",
        "commands": "differential runner local-auth + refresh/logout families on fresh fixture copies; record N/M/S + exits",
    },
    "F06": {
        "taskID": "F06",
        "outcome": "one behavior family: real-browser login → library → book detail through the configured prefix with retained client assets; compare restricted-user visibility; fresh-DB initialization validated as separate fixture variant.",
        "requirement": "CONTRACT static-client parity; cap-sub-static-client; browser-check.cjs; server/routes static handling.",
        "alreadyVerified": "browser-check.cjs direct-play+seek passes and browser-acceptance-report screenshots; reuse the retained runner, add prefixed-path and restricted-user variants.",
        "ownedFiles": "verification only (tools/parity/browser-check.cjs variants). Divergences found are separate packets (internal/server runtime paths).",
        "inputs": "isolated fixture copies per side; libwebp-capable ffmpeg for cover renders (suite pins the default-ffmpeg 500 equality separately); Chromium install.",
        "expectedOutputs": "browser steps PASS/FAIL per side, screenshots, console/page errors, API call statuses.",
        "scenarios": "root path launch; configured RouterBasePath launch; prefixed assets; restricted user library visibility; fresh-DB initialization variant.",
        "nonGoals": "playback/HLS, socket reconnect, cover size boundaries.",
        "commands": "node go-backend/tools/parity/browser-check.cjs variants; record N/M/S + exits",
    },
}


def normalize_route(route: str) -> str:
    """Normalize a coverage route expression for rule matching."""
    s = route.replace("\\/", "/")
    if s.startswith("^"):
        s = s[1:]
    if not s.startswith("/"):
        s = "/" + s
    return s


def registration_middleware(source: str, line: int) -> list[str]:
    """Extract middleware identifiers from the route's registration line(s)."""
    path = REPO / source
    lines = path.read_text().splitlines()
    text = lines[line - 1] if line - 1 < len(lines) else ""
    if line < len(lines) and ".bind(this)" not in text:
        text += " " + (lines[line] or "")  # registration may continue on the next line
    names: list[str] = []
    tokens = re.findall(
        r"\b([A-Za-z_$][\w$]*(?:Controller|Router|Auth)?\.middleware)\b|"
        r"\b(authRateLimiter)\b|\bpassport\.authenticate\('(\w+)'",
        text,
    )
    for tok in tokens:
        name = tok[0] or tok[1] or f"passport.authenticate({tok[2]})"
        if name and name not in names:
            names.append(name)
    return names


def route_task(pkg: str, method: str, route: str) -> tuple[str, str]:
    for rule in ROUTE_RULES:
        p, m, rx, task, reason = rule
        if p != pkg:
            continue
        if m is not None and m.upper() != method.upper():
            continue
        if re.search(rx, route):
            return task, reason
    return "F51", "unmapped maintenance/admin family (F51 child required)"


def f46_children(events: list[dict]) -> list[dict]:
    """Children of F46, one per at most three related events (same source file)."""
    buckets: dict[str, list[dict]] = {}
    for ev in events:
        buckets.setdefault(ev["source"], []).append(ev)
    children = []
    for src in sorted(buckets):
        batch = sorted(buckets[src], key=lambda e: (e["line"], e["event"]))
        for i in range(0, len(batch), 3):
            chunk = batch[i:i + 3]
            events = [{"event": e["event"], "line": e["line"],
                       "operation": e["operation"], "receiver": e["receiver"]}
                      for e in chunk]
            children.append({
                "id": f"F46-{len(children) + 1:02d}",
                "source": src,
                "events": events,
                "capability": "sub-realtime-socketio",
                "proof": "both paths/transports, acknowledgements, payload/order/recipients and auth lifecycle",
            })
    return children


def f51_model_children(models: list[dict]) -> list[dict]:
    children = []
    for i in range(0, len(models), 3):
        batch = models[i:i + 3]
        children.append({
            "id": f"F51-M{len(children) + 1:02d}",
            "models": [m["source"].rsplit('/', 1)[-1].removesuffix('.js') for m in batch],
            "sources": [m["source"] for m in batch],
            "capability": "sub-database-schema",
        })
    return children


def f45_children() -> list[dict]:
    """One child per upstream option family found in index.js/server/Server.js."""
    families: list[str] = []
    for src in ("index.js", "server/Server.js"):
        text = (REPO / src).read_text()
        for m in re.finditer(r"process\.env\.([A-Z0-9_]+)", text):
            if m.group(1) not in families:
                families.append(m.group(1))
    return [{"id": f"F45-ENV-{i + 1:02d}", "family": name} for i, name in enumerate(families)]


def main() -> None:
    inventory = json.loads(INVENTORY.read_text())
    coverage = json.loads(COVERAGE.read_text())
    capabilities = {}
    capability_files = {}
    for path in sorted(glob.glob(str(CAPABILITIES_DIR / "cap-*.json"))):
        record = json.loads(Path(path).read_text())
        if record["id"] in capabilities:
            raise SystemExit(f"duplicate capability id {record['id']} in {path}")
        capabilities[record["id"]] = record
        capability_files[record["id"]] = Path(path).name
    assert len(capability_files) == len(list(glob.glob(str(CAPABILITIES_DIR / 'cap-*.json'))))

    f45 = f45_children()
    f46 = f46_children(inventory["socket_events"])
    f51_models = f51_model_children(inventory["model_files"])
    valid_tasks = ({f"F{n:02d}" for n in range(1, 52)}
                   | {c["id"] for c in F51_ROUTE_CHILDREN}
                   | {c["id"] for c in f51_models}
                   | {c["id"] for c in f45})

    cap_entries = []
    for cid, record in sorted(capabilities.items()):
        if cid in TASK_BY_SUBSYSTEM:
            tasks = TASK_BY_SUBSYSTEM[cid]
        else:
            rid = cid.removeprefix("route-")
            coverage_route = next(r for r in coverage["routes"] if r["id"] == rid)
            task, _reason = route_task(
                coverage_route["go"]["ownerPackage"] or "unassigned",
                coverage_route["upstream"]["method"],
                normalize_route(coverage_route["upstream"]["route"]))
            tasks = [task]
        cap_entries.append({
            "id": cid,
            "file": f"docs/agent-work/capabilities/{capability_files[cid]}",
            "kind": record.get("kind"),
            "parent": record.get("parent"),
            "status": record.get("status"),
            "stateful": record.get("stateful"),
            "stateScenarios": record.get("state_scenarios"),
            "evidence": record.get("evidence", []),
            "verifiedInvariantIds": [i["id"] for i in record.get("invariants", []) if i.get("status") == "verified"],
            "tasks": tasks,
        })

    route_entries = []
    for r in coverage["routes"]:
        pkg = r["go"]["ownerPackage"] or "unassigned"
        raw = normalize_route(r["upstream"]["route"])
        task, reason = route_task(pkg, r["upstream"]["method"], raw)
        if task == "F51":
            raise SystemExit(f"bare F51 assignment for {r['id']} {r['upstream']['method']} "
                             f"{r['upstream']['route']}: extend ROUTE_RULES with a specific F51 child")
        site = REGISTRATION_SITE[raw_registration_source := r["upstream"]["source"]]
        registered = r["upstream"]["route"].removeprefix("^")
        variants = r.get("differentialVariants", [])
        sampled = [v for v in variants if v.get("sampled")]
        conditional = CONDITIONAL_SITES.get(f"{r['upstream']['source']}:{r['upstream']['line']}")
        public_path = f"{{base}}{site['mount']}{registered}" if r["upstream"]["routeKind"] == "string_literal" else None
        route_entries.append({
            "id": r["id"],
            "method": r["upstream"]["method"],
            "route": r["upstream"]["route"],
            "routeKind": r["upstream"]["routeKind"],
            "source": f"{r['upstream']['source']}:{r['upstream']['line']}",
            "runtime": {
                "mount": site["mount"],
                "wiring": site["wiring"],
                "publicPathTemplate": public_path,
                "middlewareMatcher": registered if r["upstream"]["routeKind"] == "regex_literal" else None,
                "publicPathTemplates": [f"{{base}}{site['mount']}{p}" for p in (conditional or {}).get("registeredPaths", [])],
                "composition": site["composition"],
                "routeMiddleware": registration_middleware(r["upstream"]["source"], r["upstream"]["line"]),
                "resolved": True,
                "conditional": conditional and {
                    "characterization": conditional["characterization"],
                    "task": conditional["task"],
                    "registeredPaths": conditional.get("registeredPaths", []),
                },
            },
            "goStatus": r["go"]["status"],
            "capabilityId": f"route-{r['id']}",
            "declaredVariants": len(variants),
            "sampledVariants": len(sampled),
            "sampledPassing": sum(1 for v in sampled if v.get("sampledPass")),
            "task": task,
            "taskReason": reason,
        })

    f46_child_by_key = {}
    for child in f46:
        for e in child["events"]:
            f46_child_by_key[(child["source"], e["line"], e["event"])] = child["id"]
    event_entries = []
    for ev in inventory["socket_events"]:
        key = (ev["source"], ev["line"], ev["event"])
        event_entries.append({
            "event": ev["event"],
            "kind": ev["event_kind"],
            "operation": ev["operation"],
            "direction": ev["direction"],
            "receiver": ev["receiver"],
            "source": f"{ev['source']}:{ev['line']}",
            "capabilityId": "sub-realtime-socketio",
            "f46Child": f46_child_by_key[key],
        })

    model_child_by_file = {}
    for batch in f51_models:
        for src in batch["sources"]:
            model_child_by_file[src] = batch["id"]
    model_entries = [{"file": m["source"],
                      "capabilityId": "sub-database-schema",
                      "f51Child": model_child_by_file[m["source"]]}
                     for m in inventory["model_files"]]

    counts = {
        "sourceInventory": inventory["counts"],
        "routeCoverage": coverage["counts"],
        "capabilityRecords": len(cap_entries),
        "routeSubsystemRecords": len([c for c in cap_entries if not c['id'].startswith('route-')]),
        "routeCandidates": len(route_entries),
        "eventCandidates": len(event_entries),
        "modelFiles": len(model_entries),
        "f46Children": len(f46),
        "f45Children": len(f45),
        "f51RouteChildren": len(F51_ROUTE_CHILDREN),
        "f51ModelChildren": len(f51_models),
        "exclusions": len(EXCLUSIONS),
    }

    doc = {
        "schemaVersion": 1,
        "title": "Parity finalization traceability (queue row F03)",
        "generatedAt": "2026-10-30",
        "sourceRevision": BASELINE,
        "inputs": {
            "sourceInventory": "go-backend/contracts/source-inventory.json",
            "routeCoverage": "go-backend/contracts/parity/route-coverage.json",
            "capabilityLedger": "docs/agent-work/capabilities/",
            "generator": "docs/specs/planned/build-parity-finalization-traceability.py",
        },
        "limitations": [
            "Runtime mounts are reconciled per candidate from the verified Server.js wiring (REGISTRATION_SITE table); the composition lists record the statically registered middleware chain. Response/error/permission behavior per variant remains the owning dispatch packet's executable work.",
            "Event candidates are emission-site syntax inventories; recipient scope, transport and payload behavior are verified only via F46 child packets; unresolved event aliases surface as F46 packet work.",
            "Mapping assigns owning queue rows at family granularity; owning packets split related variants (maximum three per child) at dispatch.",
            "Counts are exact as of generation-time; regenerate after coverage or capability changes.",
            "F45-ENV children enumerate option families read from index.js/server/Server.js only; families read elsewhere (e.g. NUSQLITE3_PATH in server/Database.js) are tracked by exclusions/F51 children.",
            "Ledger filename/id drift: record sub-authorization-ownership-w5 lives in cap-sub-authorization-ownership.json (coordinator follow-up rename).",
        ],
        "counts": counts,
        "capabilities": cap_entries,
        "routeCandidates": route_entries,
        "eventCandidates": event_entries,
        "modelFiles": model_entries,
        "f51RouteChildren": F51_ROUTE_CHILDREN,
        "f51ModelChildren": f51_models,
        "f45Children": f45,
        "f46Children": f46,
        "exclusions": EXCLUSIONS,
        "dispatchPackets": F04_F06_PACKETS,
    }

    # No-orphan validation: every referenced task resolves; every mapped
    # candidate references an existing capability; every family has a task.
    for cap in cap_entries:
        assert cap["tasks"], f"capability {cap['id']} has no task"
        for t in cap["tasks"]:
            assert t in valid_tasks, f"orphan task {t!r} on {cap['id']}"
        assert (REPO / "docs/agent-work/capabilities" / capability_files[cap["id"]]).exists()
    for r in route_entries:
        assert r["capabilityId"] in capabilities, f"orphan capability {r['capabilityId']}"
        assert r["task"] in valid_tasks, f"orphan task {r['task']!r} on route {r['id']}"
    for e in event_entries:
        assert e["f46Child"].startswith("F46-"), f"unresolved event {e['event']}"
    for m in model_entries:
        assert m["f51Child"].startswith("F51-M"), f"unresolved model {m['file']}"

    OUT.write_text(json.dumps(doc, indent=1) + "\n")
    print(f"wrote {OUT}: {counts['capabilityRecords']} capabilities, {counts['routeCandidates']} routes, "
          f"{counts['eventCandidates']} events, {counts['modelFiles']} models; "
          f"F46 children {len(f46)}, F45 children {len(f45)}, F51 route children {len(F51_ROUTE_CHILDREN)}, "
          f"F51 model children {len(f51_models)}")


if __name__ == "__main__":
    main()
