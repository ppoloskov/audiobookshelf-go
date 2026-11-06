#!/usr/bin/env python3
"""Build the client x operation table of contracts/mobile-client-audit.md.

Inputs (all committed):
  contracts/clients/clients.json   client repos, pinned SHAs, local checkouts
  contracts/clients/<key>.md       per-client evidence (HTTP table with
                                   file:line citations), extracted from the
                                   pinned checkout
  contracts/client-usage.json      upstream route patterns and Go handlers
  internal/contracttest/journey*_test.go  journey steps (test evidence)

The checkouts are needed only to turn a cited file name into a full path for
the permalink; clone each repo at its SHA to the "checkout" path:
  git clone --depth 1 <repo> <checkout> && git -C <checkout> checkout <sha>

Usage (from go-backend/):
  python3 tools/clientaudit/build.py > contracts/mobile-client-audit.table.md

It prints one Markdown table row per (client, HTTP operation) and, on stderr,
the rows it could not match to an upstream route. It never edits files.
"""
import glob
import json
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))  # go-backend
CLIENTS = json.load(open(os.path.join(ROOT, "contracts/clients/clients.json")))["clients"]
USAGE = json.load(open(os.path.join(ROOT, "contracts/client-usage.json")))

def route_regex(path):
    out = ""
    for seg in path.split("/")[1:]:
        if seg.startswith(":") and seg.endswith("?"):
            out += "(/[^/]+)?"
        elif seg.startswith(":"):
            out += "/[^/]+"
        elif seg.endswith("*"):
            out += "/" + re.escape(seg[:-1]) + ".*"
        else:
            out += "/" + re.escape(seg)
    return re.compile("^" + out + "/?$")

ROUTES = []
for r in USAGE["routes"]:
    ROUTES.append((r["method"], r["path"], True, r.get("goHandler", ""), r.get("sources", [])))
for r in USAGE.get("unusedUpstreamRoutes", []):
    ROUTES.append((r["method"], r["path"], False, r.get("goHandler", ""), []))
ROUTES = [(m, p, used, h, s, route_regex(p)) for m, p, used, h, s in ROUTES if p.startswith("/")]

# Journey sources, for routes the F8 coverage pass did not list as client-used.
JOURNEY = "".join(open(f).read() for f in glob.glob(os.path.join(ROOT, "internal/contracttest/journey*_test.go")))

def normalize_path(raw):
    p = raw.replace("`", "")
    p = re.sub(r"^\{[^}]+\}", "", p)
    p = p.split("?")[0].split("[")[0].split(" ")[0].strip()
    p = re.sub(r"\{[^}]+\}|<[^>]+>|\$\{[^}]+\}|\$[A-Za-z_]+|:[A-Za-z_]+", "X", p)
    if not p.startswith("/"):
        p = "/" + p
    return re.sub(r"^/audiobookshelf(?=/)", "", p)

def match(method, path):
    for m, p, used, handler, sources, rx in ROUTES:
        if (m == method or m == "ALL") and rx.match(path):
            return m, p, used, handler
    return None

FILE_REF = re.compile(r"([A-Za-z0-9_./+-]+\.(?:kt|java|swift|dart|js|vue|ts)):(\d+)(?:-(\d+))?")

def file_index(checkout):
    idx = {}
    for dirpath, dirs, files in os.walk(checkout):
        dirs[:] = [d for d in dirs if d not in (".git", "node_modules", "build", ".dart_tool", "Pods")]
        for f in files:
            full = os.path.relpath(os.path.join(dirpath, f), checkout)
            idx.setdefault(f, []).append(full)
    return idx

def permalink(client, idx, cell):
    for name, start, end in FILE_REF.findall(cell):
        for short, full in client.get("pathPrefixes", {}).items():
            if name.startswith(short):
                name = full + name[len(short):]
        candidates = [name] if os.path.exists(os.path.join(client["checkout"], name)) else \
            [c for c in idx.get(os.path.basename(name), []) if c.endswith(name)]
        if len(candidates) == 1:
            lines = f"L{start}-L{end}" if end else f"L{start}"
            url = f"{client['repo']}/blob/{client['sha']}/{candidates[0]}#{lines}"
            return f"[{os.path.basename(candidates[0])}:{start}{'-' + end if end else ''}]({url})"
    return "UNRESOLVED: " + cell[:60].replace("|", "/")

def priority(path):
    if re.match(r"^/(login|logout|auth/|api/authorize|status|init)", path) or \
       re.search(r"/play|/session|/me/progress|/public/session|/file/|/hls/|bookmark", path):
        return "P0"
    if re.match(r"^/api/(libraries|items|me|series|authors|collections|playlists|podcasts|search)", path):
        return "P1"
    return "P2"

def evidence(route_path, used):
    if used:
        return "TestJourney* (route reached, shape-checked; F8)"
    literal = route_path.split(":")[0].rstrip("/")
    return "TestJourney (path literal present)" if literal and f'"{literal}' in JOURNEY else "MISSING"

def main():
    print("| Client | Operation | Source | Go implementation | Test evidence | Status | Priority |")
    print("|---|---|---|---|---|---|---|")
    for c in CLIENTS:
        idx = file_index(c["checkout"]) if os.path.isdir(c["checkout"]) else {}
        section = None
        for line in open(os.path.join(ROOT, "contracts/clients", c["key"] + ".md")):
            if line.startswith("## "):
                section = line[3:].strip().lower()
                continue
            if not (section and section.startswith("http") and line.startswith("|")):
                continue
            cells = [x.strip() for x in line.strip().strip("|").split("|")]
            if len(cells) < 3 or cells[0].lower() in ("method", "") or set(cells[0]) <= set("-: "):
                continue
            method = cells[0].upper().split("/")[0].strip("` ")
            path = normalize_path(cells[1])
            hit = match(method, path)
            if not hit:
                print(f"unmatched {c['key']}: {method} {cells[1][:80]}", file=sys.stderr)
                continue
            m, rpath, used, handler = hit
            ev = evidence(rpath, used)
            status = "Compatible (route tested)" if ev != "MISSING" else "Implemented, not tested"
            print(f"| {c['name']} @{c['sha'][:7]} | `{m} {rpath}` | {permalink(c, idx, cells[2])} | "
                  f"{handler or 'see route-coverage.json'} | {ev} | {status} | {priority(rpath)} |")

if __name__ == "__main__":
    main()
