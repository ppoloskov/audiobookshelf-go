#!/usr/bin/env python3
"""Conservative source inventory of the legacy Audiobookshelf Node backend.

This deliberately extracts syntax-level evidence only. It does not resolve
Express mount behavior, conditional registrations, middleware semantics, or
Socket.IO delivery scope. See go-backend/contracts/README.md.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import subprocess
from pathlib import Path
from typing import Any

HTTP_METHODS = {"get", "post", "put", "patch", "delete", "options", "head", "all"}
ROUTE_RE = re.compile(
    r"(?P<receiver>(?:this\.)?router)\s*\.\s*(?P<method>get|post|put|patch|delete|options|head|all)\s*\(",
    re.IGNORECASE,
)
SOCKET_RE = re.compile(r"(?P<receiver>[\w$.]+)\s*\.\s*(?P<method>on|emit)\s*\(")
SOCKET_WRAPPERS = {
    "emitter": (0, "all_authorized_clients"),
    "clientEmitter": (1, "specific_user_clients"),
    "adminEmitter": (0, "admin_clients"),
    "libraryItemEmitter": (0, "clients_with_library_item_access"),
    "libraryItemsEmitter": (0, "clients_with_access_to_any_library_item"),
}
SOCKET_WRAPPER_RE = re.compile(
    r"(?P<receiver>SocketAuthority|this)\s*\.\s*(?P<method>emitter|clientEmitter|adminEmitter|libraryItemEmitter|libraryItemsEmitter)\s*\("
)
STRING_RE = re.compile(r"(?P<quote>['\"`])(?P<value>(?:\\.|(?!\1).)*?)\1", re.DOTALL)


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def line_number(source: str, offset: int) -> int:
    return source.count("\n", 0, offset) + 1


def _first_argument(source: str, open_paren: int) -> tuple[str, str]:
    """Return first argument text and its syntax kind from a call's opening paren."""
    i = open_paren + 1
    start = i
    depth = 0
    quote: str | None = None
    escaped = False
    template_expr_depth = 0
    while i < len(source):
        char = source[i]
        if quote:
            if escaped:
                escaped = False
            elif char == "\\":
                escaped = True
            elif char == quote:
                quote = None
            i += 1
            continue
        if char in "'\"`":
            quote = char
        elif char in "([{":
            depth += 1
        elif char in ")]}":
            if char == ")" and depth == 0:
                break
            depth = max(0, depth - 1)
        elif char == "," and depth == 0:
            break
        i += 1
    expression = source[start:i].strip()
    if not expression:
        return expression, "missing"
    if expression[0] in "'\"`":
        quote_char = expression[0]
        if len(expression) >= 2 and expression[-1] == quote_char:
            value = expression[1:-1]
            kind = "template_literal" if quote_char == "`" else "string_literal"
            if quote_char == "`" and "${" in value:
                return expression, "template_expression"
            return value, kind
    if expression.startswith("/") and expression.rfind("/") > 0:
        return expression, "regex_literal"
    return expression, "dynamic_expression"


def _count_top_level_args(source: str, open_paren: int) -> int:
    """Count top-level arguments (syntax hint only), including multiline calls."""
    i, depth, commas, has_content = open_paren + 1, 0, 0, False
    quote: str | None = None
    escaped = False
    while i < len(source):
        char = source[i]
        if quote:
            if escaped:
                escaped = False
            elif char == "\\":
                escaped = True
            elif char == quote:
                quote = None
            i += 1
            continue
        if char in "'\"`":
            quote = char
            has_content = True
        elif char in "([{":
            depth += 1
            has_content = True
        elif char in ")]}":
            if char == ")" and depth == 0:
                return commas + int(has_content)
            depth = max(0, depth - 1)
        elif char == "," and depth == 0:
            commas += 1
            has_content = False
        elif not char.isspace():
            has_content = True
        i += 1
    return commas + int(has_content)


def _arguments(source: str, open_paren: int) -> list[str]:
    """Split call arguments at top-level commas, respecting strings and nesting."""
    args: list[str] = []
    start, i, depth = open_paren + 1, open_paren + 1, 0
    quote: str | None = None
    escaped = False
    while i < len(source):
        char = source[i]
        if quote:
            if escaped:
                escaped = False
            elif char == "\\":
                escaped = True
            elif char == quote:
                quote = None
            i += 1
            continue
        if char in "'\"`":
            quote = char
        elif char in "([{":
            depth += 1
        elif char in ")]}":
            if char == ")" and depth == 0:
                last = source[start:i].strip()
                if last:
                    args.append(last)
                return args
            depth = max(0, depth - 1)
        elif char == "," and depth == 0:
            args.append(source[start:i].strip())
            start = i + 1
        i += 1
    return args


def _classify_expression(expression: str) -> tuple[str, str]:
    """Return (reported value, syntax kind) for an already isolated expression."""
    expression = expression.strip()
    if not expression:
        return expression, "missing"
    if expression[0] in "'\"`" and len(expression) >= 2 and expression[-1] == expression[0]:
        quote_char = expression[0]
        value = expression[1:-1]
        if quote_char == "`" and "${" in value:
            return expression, "template_expression"
        return value, "template_literal" if quote_char == "`" else "string_literal"
    if expression.startswith("/") and expression.rfind("/") > 0:
        return expression, "regex_literal"
    return expression, "dynamic_expression"


def _source_record(root: Path, path: Path, source: str) -> dict[str, str]:
    return {"path": path.relative_to(root).as_posix(), "sha256": sha256(source.encode("utf-8"))}


def extract_routes(root: Path, source_roots: tuple[str, ...] = ("server",)) -> tuple[list[dict[str, Any]], dict[str, dict[str, str]]]:
    routes: list[dict[str, Any]] = []
    sources: dict[str, dict[str, str]] = {}
    for source_root in source_roots:
        for path in sorted((root / source_root).rglob("*.js")):
            rel = path.relative_to(root).as_posix()
            if "/libs/" in rel:
                continue
            source = path.read_text(encoding="utf-8")
            if not ROUTE_RE.search(source):
                continue
            sources[rel] = _source_record(root, path, source)
            for match in ROUTE_RE.finditer(source):
                offset = match.end() - 1
                route_value, route_kind = _first_argument(source, offset)
                routes.append({
                    "method": match.group("method").upper(),
                    "route": route_value,
                    "route_kind": route_kind,
                    "receiver": match.group("receiver"),
                    "source": rel,
                    "line": line_number(source, match.start()),
                    "argument_count_syntax_hint": _count_top_level_args(source, offset),
                    "middleware_behavior_resolved": False,
                    "prefix_context": _prefix_context(rel),
                })
    return routes, sources


def _prefix_context(source: str) -> dict[str, Any]:
    if source.endswith("server/routers/ApiRouter.js"):
        return {"known_mount": "/api", "additional_prefix": "global.RouterBasePath (runtime configured)", "fully_resolved": False}
    if source.endswith("server/routers/PublicRouter.js"):
        return {"known_mount": "/public", "additional_prefix": "global.RouterBasePath (runtime configured)", "fully_resolved": False}
    if source.endswith("server/routers/HlsRouter.js"):
        return {"known_mount": "/hls", "additional_prefix": "global.RouterBasePath (runtime configured)", "fully_resolved": False}
    if source.endswith("server/Server.js"):
        return {"known_mount": "router mounted at global.RouterBasePath", "fully_resolved": False}
    return {"known_mount": None, "reason": "mount point not resolved by this source extractor", "fully_resolved": False}


def extract_socket_events(root: Path, source_roots: tuple[str, ...] = ("server",)) -> tuple[list[dict[str, Any]], dict[str, dict[str, str]]]:
    events: list[dict[str, Any]] = []
    sources: dict[str, dict[str, str]] = {}
    for source_root in source_roots:
        for path in sorted((root / source_root).rglob("*.js")):
            rel = path.relative_to(root).as_posix()
            if "/libs/" in rel:
                continue
            source = path.read_text(encoding="utf-8")
            direct_matches = [m for m in SOCKET_RE.finditer(source) if "socket" in m.group("receiver").lower() or "io" in m.group("receiver").lower()]
            wrapper_matches = list(SOCKET_WRAPPER_RE.finditer(source))
            if not direct_matches and not wrapper_matches:
                continue
            sources[rel] = _source_record(root, path, source)
            for match in direct_matches:
                value, kind = _first_argument(source, match.end() - 1)
                events.append({
                    "direction": "incoming_registration" if match.group("method") == "on" else "emission",
                    "operation": match.group("method"),
                    "receiver": match.group("receiver"),
                    "event": value,
                    "event_kind": kind,
                    "source": rel,
                    "line": line_number(source, match.start()),
                    "scope_resolved": False,
                })
            for match in wrapper_matches:
                wrapper = match.group("method")
                event_arg_index, scope = SOCKET_WRAPPERS[wrapper]
                args = _arguments(source, match.end() - 1)
                if event_arg_index < len(args):
                    value, kind = _classify_expression(args[event_arg_index])
                else:
                    value, kind = "", "missing"
                events.append({
                    "direction": "emission_via_wrapper",
                    "operation": "emit",
                    "receiver": match.group("receiver"),
                    "wrapper": wrapper,
                    "wrapper_event_argument_index": event_arg_index,
                    "event": value,
                    "event_kind": kind,
                    "source": rel,
                    "line": line_number(source, match.start()),
                    "scope_classification": scope,
                    "scope_resolved": False,
                })
    return events, sources


def extract_models(root: Path) -> tuple[list[dict[str, Any]], dict[str, dict[str, str]]]:
    models_dir = root / "server" / "models"
    models: list[dict[str, Any]] = []
    sources: dict[str, dict[str, str]] = {}
    if models_dir.is_dir():
        for path in sorted(models_dir.glob("*.js")):
            rel = path.relative_to(root).as_posix()
            source = path.read_text(encoding="utf-8")
            models.append({"filename": path.name, "source": rel, "line": 1})
            sources[rel] = _source_record(root, path, source)
    return models, sources


def git_revision(root: Path) -> str | None:
    try:
        return subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True, stderr=subprocess.DEVNULL).strip()
    except (OSError, subprocess.CalledProcessError):
        return None


def build_inventory(root: Path) -> dict[str, Any]:
    routes, route_sources = extract_routes(root)
    socket_events, socket_sources = extract_socket_events(root)
    models, model_sources = extract_models(root)
    sources = {**route_sources, **socket_sources, **model_sources}
    return {
        "schema_version": 1,
        "inventory_kind": "conservative_source_syntax_inventory",
        "source_revision": git_revision(root),
        "source_tree": "server/",
        "limitations": [
            "This is source extraction, not a complete API or behavior specification.",
            "Express mount prefixes are partially annotated and remain runtime/configuration dependent.",
            "Middleware, authorization, conditional registration, request/response schemas, and handler behavior are not interpreted.",
            "Only syntax forms represented by this extractor are inventoried; dynamic expressions are retained as unresolved text.",
            "Socket.IO wrapper emissions are inventoried from known wrapper signatures, but recipient scope and runtime event flow are not resolved.",
            "The syntax scanner reads JavaScript .js files only and may count registrations in comments or strings; TypeScript sources are not scanned.",
            "Model inventory lists filenames only; it does not describe database schema, migrations, or serialized metadata formats.",
        ],
        "counts": {"routes": len(routes), "socket_events": len(socket_events), "model_files": len(models), "source_files": len(sources)},
        "routes": routes,
        "socket_events": socket_events,
        "model_files": models,
        "sources": dict(sorted(sources.items())),
    }


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[2], help="repository root")
    parser.add_argument("--output", type=Path, default=None, help="output JSON path (default: stdout)")
    args = parser.parse_args()
    inventory = build_inventory(args.root.resolve())
    rendered = json.dumps(inventory, indent=2, sort_keys=True) + "\n"
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(rendered, encoding="utf-8")
    else:
        print(rendered, end="")


if __name__ == "__main__":
    main()
