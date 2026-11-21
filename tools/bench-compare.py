#!/usr/bin/env python3
"""Compare HTTP API, cover-resize, streaming, and scan performance between
upstream Audiobookshelf (TS) and Monstrobook (Go).

Uses Python standard library only.
"""

from __future__ import annotations

import argparse
import concurrent.futures
import json
import math
import statistics
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass


@dataclass
class BenchResult:
    name: str
    total: int
    errors: int
    wall_s: float
    latencies_ms: list[float]
    bytes_read: int = 0

    @property
    def rps(self) -> float:
        return self.total / self.wall_s if self.wall_s > 0 else 0.0

    def pct(self, p: float) -> float:
        if not self.latencies_ms:
            return 0.0
        s = sorted(self.latencies_ms)
        idx = min(len(s) - 1, max(0, math.ceil(len(s) * p) - 1))
        return s[idx]


def http_req(
    base: str,
    path: str,
    token: str | None = None,
    method: str = "GET",
    body: dict | None = None,
    headers: dict[str, str] | None = None,
    timeout: float = 60.0,
) -> tuple[int, bytes, float]:
    url = base.rstrip("/") + path
    hdrs = dict(headers or {})
    if token:
        hdrs["Authorization"] = f"Bearer {token}"
    data = None
    if body is not None:
        data = json.dumps(body).encode("utf-8")
        hdrs.setdefault("Content-Type", "application/json")
    req = urllib.request.Request(url, data=data, headers=hdrs, method=method)
    t0 = time.perf_counter()
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            payload = resp.read()
            return resp.status, payload, (time.perf_counter() - t0) * 1000.0
    except urllib.error.HTTPError as exc:
        payload = exc.read()
        return exc.code, payload, (time.perf_counter() - t0) * 1000.0


def login(base: str, username: str, password: str) -> str:
    status, payload, _ = http_req(
        base, "/login", method="POST", body={"username": username, "password": password}
    )
    if status != 200:
        raise SystemExit(f"login failed on {base}: HTTP {status}: {payload[:200]!r}")
    return json.loads(payload)["user"]["token"]


def discover_targets(base: str, token: str) -> dict:
    status, payload, _ = http_req(base, "/api/libraries", token=token)
    if status != 200:
        raise SystemExit(f"GET /api/libraries failed on {base}: HTTP {status}")
    libs = json.loads(payload).get("libraries", [])
    if not libs:
        raise SystemExit(f"no libraries found on {base}")

    book_lib = next((l for l in libs if l.get("mediaType") == "book"), libs[0])
    lib_id = book_lib["id"]

    status, payload, _ = http_req(
        base, f"/api/libraries/{lib_id}/items?limit=60&page=0", token=token
    )
    if status != 200:
        raise SystemExit(f"GET /api/libraries/{lib_id}/items failed: HTTP {status}")
    items = json.loads(payload).get("results", [])
    if not items:
        raise SystemExit(f"library {lib_id} has no items")

    item_id = items[0]["id"]
    cover_ids = [it["id"] for it in items if it.get("media", {}).get("coverPath")][:30]
    if not cover_ids:
        cover_ids = [it["id"] for it in items[:20]]

    status, payload, _ = http_req(base, f"/api/items/{item_id}?expanded=1", token=token)
    audio_ino = None
    if status == 200:
        af = json.loads(payload).get("media", {}).get("audioFiles", [])
        if af:
            audio_ino = af[0].get("ino")

    return {
        "library_id": lib_id,
        "item_id": item_id,
        "cover_ids": cover_ids,
        "audio_ino": audio_ino,
    }


def run_load(
    name: str,
    base: str,
    paths: list[str],
    token: str,
    requests: int,
    concurrency: int,
    headers: dict[str, str] | None = None,
) -> BenchResult:
    # Warmup (2 requests)
    for p in paths[:2]:
        http_req(base, p, token=token, headers=headers)

    latencies: list[float] = []
    errors = 0
    bytes_read = 0
    t_start = time.perf_counter()

    def one(i: int) -> tuple[bool, float, int]:
        p = paths[i % len(paths)]
        st, body, ms = http_req(base, p, token=token, headers=headers)
        return (200 <= st < 400), ms, len(body)

    with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as pool:
        for ok, ms, n in pool.map(one, range(requests)):
            if not ok:
                errors += 1
            latencies.append(ms)
            bytes_read += n

    return BenchResult(
        name=name,
        total=requests,
        errors=errors,
        wall_s=time.perf_counter() - t_start,
        latencies_ms=latencies,
        bytes_read=bytes_read,
    )


def run_cold_covers(
    base: str, token: str, cover_ids: list[str], concurrency: int
) -> BenchResult:
    paths = [f"/api/items/{cid}/cover?format=webp&width=400" for cid in cover_ids]
    latencies: list[float] = []
    errors = 0
    bytes_read = 0
    t_start = time.perf_counter()

    def one(p: str) -> tuple[bool, float, int]:
        st, body, ms = http_req(base, p, token=token)
        return (200 <= st < 400), ms, len(body)

    with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as pool:
        for ok, ms, n in pool.map(one, paths):
            if not ok:
                errors += 1
            latencies.append(ms)
            bytes_read += n

    return BenchResult(
        name="Cold WebP cover resize (400px)",
        total=len(paths),
        errors=errors,
        wall_s=time.perf_counter() - t_start,
        latencies_ms=latencies,
        bytes_read=bytes_read,
    )


def run_scan(base: str, token: str, lib_id: str, timeout_s: float = 1800.0) -> float:
    t0 = time.perf_counter()
    st, payload, _ = http_req(
        base, f"/api/libraries/{lib_id}/scan", token=token, method="POST"
    )
    if st not in (200, 202):
        raise RuntimeError(f"POST scan failed on {base}: HTTP {st} {payload[:200]!r}")
    deadline = t0 + timeout_s
    while time.perf_counter() < deadline:
        st, payload, _ = http_req(base, "/api/tasks", token=token)
        if st == 200:
            tasks = json.loads(payload).get("tasks", [])
            active = [
                t
                for t in tasks
                if t.get("action") == "library-scan"
                and t.get("data", {}).get("libraryId") == lib_id
                and not t.get("isFinished")
            ]
            if not active:
                return time.perf_counter() - t0
        time.sleep(0.5)
    raise TimeoutError(f"scan did not complete within {timeout_s}s on {base}")


def bench_suite(base: str, token: str, args: argparse.Namespace) -> dict:
    tgt = discover_targets(base, token)
    lib_id = tgt["library_id"]
    item_id = tgt["item_id"]

    cases = [
        (
            "List items (50, sort=authorName)",
            [f"/api/libraries/{lib_id}/items?limit=50&page=0&sort=media.metadata.authorName"],
            None,
        ),
        (
            "List items (collapseseries=1)",
            [f"/api/libraries/{lib_id}/items?collapseseries=1&limit=50&page=0"],
            None,
        ),
        (
            "Personalized shelves",
            [f"/api/libraries/{lib_id}/personalized"],
            None,
        ),
        (
            "Library search (?q=the)",
            [f"/api/libraries/{lib_id}/search?q=the"],
            None,
        ),
        (
            "Item detail (?expanded=1)",
            [f"/api/items/{item_id}?expanded=1"],
            None,
        ),
    ]
    if tgt["audio_ino"]:
        cases.append(
            (
                "Audio Range 64KiB",
                [f"/api/items/{item_id}/file/{tgt['audio_ino']}"],
                {"Range": "bytes=0-65535"},
            )
        )

    results = []
    for name, paths, hdrs in cases:
        results.append(
            run_load(name, base, paths, token, args.requests, args.concurrency, hdrs)
        )

    results.append(run_cold_covers(base, token, tgt["cover_ids"], min(args.concurrency, 4)))

    scan_s = None
    if args.scan:
        scan_s = run_scan(base, token, lib_id)

    return {"target": tgt, "results": results, "scan_s": scan_s}


def print_markdown(ts_out: dict, go_out: dict) -> None:
    print("| Benchmark | TS RPS | Go RPS | Speedup | TS p50 | Go p50 | TS p95 | Go p95 | Errors (TS/Go) |")
    print("| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | :---: |")
    for ts_r, go_r in zip(ts_out["results"], go_out["results"]):
        speedup = (go_r.rps / ts_r.rps) if ts_r.rps > 0 else 0.0
        print(
            f"| {ts_r.name} | {ts_r.rps:.1f} | {go_r.rps:.1f} | {speedup:.2f}x | "
            f"{ts_r.pct(0.50):.1f}ms | {go_r.pct(0.50):.1f}ms | "
            f"{ts_r.pct(0.95):.1f}ms | {go_r.pct(0.95):.1f}ms | "
            f"{ts_r.errors}/{go_r.errors} |"
        )
    if ts_out["scan_s"] is not None and go_out["scan_s"] is not None:
        ratio = ts_out["scan_s"] / go_out["scan_s"] if go_out["scan_s"] > 0 else 0.0
        print(
            f"| Library scan (wall time) | — | — | {ratio:.2f}x faster | "
            f"{ts_out['scan_s']:.2f}s | {go_out['scan_s']:.2f}s | — | — | 0/0 |"
        )


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--ts-url", required=True, help="TS server base URL")
    ap.add_argument("--go-url", required=True, help="Go server base URL")
    ap.add_argument("--token", help="Bearer JWT (valid on both cloned DBs)")
    ap.add_argument("--username", help="Username if not passing --token")
    ap.add_argument("--password", help="Password if not passing --token")
    ap.add_argument("--concurrency", type=int, default=8)
    ap.add_argument("--requests", type=int, default=100)
    ap.add_argument("--scan", action="store_true", help="Also benchmark library scan")
    ap.add_argument("--json", action="store_true", help="Emit JSON summary as well")
    args = ap.parse_args()

    ts_tok = args.token
    go_tok = args.token
    if not ts_tok:
        if not (args.username and args.password):
            ap.error("provide --token or both --username and --password")
        ts_tok = login(args.ts_url, args.username, args.password)
        go_tok = login(args.go_url, args.username, args.password)

    print(f"[bench] running against TS={args.ts_url} and Go={args.go_url} (c={args.concurrency}, n={args.requests})", file=sys.stderr)
    ts_out = bench_suite(args.ts_url, ts_tok, args)
    go_out = bench_suite(args.go_url, go_tok, args)

    print()
    print_markdown(ts_out, go_out)

    if args.json:
        summary = {
            "ts": [vars(r) | {"p50": r.pct(0.50), "p95": r.pct(0.95), "rps": r.rps, "mean": statistics.mean(r.latencies_ms)} for r in ts_out["results"]],
            "go": [vars(r) | {"p50": r.pct(0.50), "p95": r.pct(0.95), "rps": r.rps, "mean": statistics.mean(r.latencies_ms)} for r in go_out["results"]],
            "ts_scan_s": ts_out["scan_s"],
            "go_scan_s": go_out["scan_s"],
        }
        print("\n```json")
        print(json.dumps(summary, indent=2))
        print("```")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
