#!/usr/bin/env python3
"""Report per-pod resource use for Audiobookshelf from VictoriaMetrics.

This is a metrics-only helper. It discovers abooks pods from
kube_pod_container_info, queries cAdvisor CPU/memory metrics over each pod's
observed lifetime, and prints a Markdown table (or CSV). Lifetimes are based on
300-second discovery samples, so very short-lived pods and the first minute of a
new pod's startup can be excluded.

Example:
    kubectl -n monitoring port-forward svc/victoriametrics 18428:8428
    python3 go-backend/tools/abooks-resource-report.py --days 30
"""

from __future__ import annotations

import argparse
import csv
import datetime as dt
import json
import math
import sys
import urllib.error
import urllib.parse
import urllib.request
from collections import defaultdict


def api_get(base: str, path: str, params: dict[str, str]) -> dict:
    url = base.rstrip("/") + path + "?" + urllib.parse.urlencode(params)
    try:
        with urllib.request.urlopen(url, timeout=60) as response:
            return json.load(response)
    except urllib.error.HTTPError as exc:
        body = exc.read().decode("utf-8", "replace")
        raise SystemExit(f"VictoriaMetrics returned HTTP {exc.code}: {body}") from exc
    except urllib.error.URLError as exc:
        raise SystemExit(f"cannot reach {url}: {exc}") from exc


def query_range(base: str, query: str, start: int, end: int, step: int) -> list[dict]:
    payload = api_get(base, "/api/v1/query_range", {
        "query": query,
        "start": start,
        "end": end,
        "step": step,
    })
    return payload["data"]["result"]


def query(base: str, expr: str) -> list[dict]:
    payload = api_get(base, "/api/v1/query", {"query": expr})
    return payload["data"]["result"]


def percentile(values: list[float], fraction: float) -> float:
    if not values:
        return 0.0
    values = sorted(values)
    index = min(len(values) - 1, int(len(values) * fraction))
    return values[index]


def local_time(ts: int) -> str:
    return dt.datetime.fromtimestamp(ts).strftime("%Y-%m-%d %H:%M")


def choose_step(start: int, end: int, wanted: int) -> int:
    # VictoriaMetrics rejects more than 30,000 points per series.
    needed = math.ceil((end - start) / 29000) if end > start else wanted
    step = max(wanted, needed)
    return int(math.ceil(step / 60) * 60)


def format_gib(value: float) -> str:
    if value <= 0:
        return "-"
    return f"{value / 2**30:.1f} GiB"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--vm-url", default="http://127.0.0.1:18428/vm",
                        help="VictoriaMetrics base URL including path prefix")
    parser.add_argument("--days", type=int, default=30,
                        help="lookback window in days (default: 30)")
    parser.add_argument("--namespace", default="media")
    parser.add_argument("--pod-regex", default="abooks-.*")
    parser.add_argument("--container", default="abooks")
    parser.add_argument("--step", type=int, default=60,
                        help="preferred query step in seconds (default: 60)")
    parser.add_argument("--end",
                        help="end of the window as ISO 8601 (default: now); "
                             "use this to freeze a report")
    parser.add_argument("--format", choices=("markdown", "csv"), default="markdown")
    args = parser.parse_args()

    if args.end:
        end = dt.datetime.fromisoformat(args.end.replace("Z", "+00:00"))
        if end.tzinfo is None:
            end = end.replace(tzinfo=dt.timezone.utc)
        now = int(end.timestamp())
    else:
        now = int(dt.datetime.now(dt.timezone.utc).timestamp())
    start = now - args.days * 86400
    selector = (
        f'namespace="{args.namespace}",pod=~"{args.pod_regex}",'
        f'container="{args.container}"'
    )

    # Discover pods and their observed lifetimes. A 300 s step is enough for
    # kube-state-metrics series that are present for at least a few minutes.
    info = query_range(
        args.vm_url,
        f"max by (pod,image) (kube_pod_container_info{{{selector}}})",
        start,
        now,
        300,
    )
    pod_image: dict[str, str] = {}
    lifetimes: dict[str, tuple[int, int]] = {}
    for series in info:
        pod = series["metric"].get("pod", "")
        image = series["metric"].get("image", "?")
        timestamps = [int(float(ts)) for ts, value in series["values"] if float(value) > 0]
        if not pod or not timestamps:
            continue
        pod_image[pod] = image.rsplit("/", 1)[-1]
        lifetimes[pod] = (min(timestamps), max(timestamps))

    if not lifetimes:
        raise SystemExit("no matching pods found in the requested window")

    restarts: dict[str, int] = {}
    for series in query(
        args.vm_url,
        f"max by (pod) (max_over_time("
        f'kube_pod_container_status_restarts_total{{{selector}}}[{args.days}d]))',
    ):
        restarts[series["metric"].get("pod", "")] = int(float(series["value"][1]))

    limits: dict[str, dict[str, int]] = defaultdict(dict)
    for series in query(
        args.vm_url,
        f"max by (pod,resource) (max_over_time("
        f'kube_pod_container_resource_limits{{{selector}}}[{args.days}d]))',
    ):
        pod = series["metric"].get("pod", "")
        resource = series["metric"].get("resource", "")
        limits[pod][resource] = int(float(series["value"][1]))

    rows = []
    for pod, (first, last) in sorted(lifetimes.items(), key=lambda item: item[1][0]):
        step = choose_step(first, last, args.step)
        pod_selector = (
            f'namespace="{args.namespace}",pod="{pod}",'
            f'container="{args.container}"'
        )
        metric_queries = {
            "cpu": f"max by (pod) (rate(container_cpu_usage_seconds_total{{{pod_selector}}}[5m]))",
            "rss": f"max by (pod) (container_memory_rss{{{pod_selector}}})",
            "ws": f"max by (pod) (container_memory_working_set_bytes{{{pod_selector}}})",
            "cache": f"max by (pod) (container_memory_cache{{{pod_selector}}})",
        }
        values: dict[str, list[float]] = {}
        for name, expr in metric_queries.items():
            result = query_range(args.vm_url, expr, first, last, step)
            points = [
                float(value)
                for series in result
                for _, value in series["values"]
                if float(value) > 0
            ]
            values[name] = points

        cpu = values["cpu"]
        rss = values["rss"]
        ws = values["ws"]
        cache = values["cache"]
        hours = (last - first) / 3600
        rows.append({
            "pod": pod,
            "image": pod_image.get(pod, "?"),
            "first": local_time(first),
            "last": local_time(last),
            "hours": hours,
            "cpu_avg": (sum(cpu) / len(cpu) * 1000) if cpu else 0.0,
            "cpu_p50": percentile(cpu, 0.50) * 1000,
            "cpu_p95": percentile(cpu, 0.95) * 1000,
            "cpu_max": (max(cpu) * 1000) if cpu else 0.0,
            "rss_p50": percentile(rss, 0.50) / 2**20,
            "rss_p95": percentile(rss, 0.95) / 2**20,
            "rss_max": (max(rss) / 2**20) if rss else 0.0,
            "ws_p50": percentile(ws, 0.50) / 2**20,
            "ws_p95": percentile(ws, 0.95) / 2**20,
            "ws_max": (max(ws) / 2**20) if ws else 0.0,
            "cache_p50": percentile(cache, 0.50) / 2**20,
            "cache_p95": percentile(cache, 0.95) / 2**20,
            "cache_max": (max(cache) / 2**20) if cache else 0.0,
            "restarts": restarts.get(pod, 0),
            "limit": limits.get(pod, {}).get("memory", 0),
        })

    if args.format == "csv":
        fields = [
            "pod", "image", "first", "last", "hours",
            "cpu_avg", "cpu_p50", "cpu_p95", "cpu_max",
            "rss_p50", "rss_p95", "rss_max",
            "ws_p50", "ws_p95", "ws_max",
            "cache_p50", "cache_p95", "cache_max",
            "restarts", "limit",
        ]
        writer = csv.DictWriter(sys.stdout, fieldnames=fields)
        writer.writeheader()
        writer.writerows(rows)
        return 0

    print(f"# abooks resource report")
    print()
    print(f"- VictoriaMetrics: `{args.vm_url}`")
    print(f"- Window: last {args.days} days, ending {local_time(now)}")
    print(f"- Selector: `{selector}`")
    print(f"- Times: local ({dt.datetime.now().astimezone().tzname()})")
    print()
    print("| Pod | Image | First | Hours | CPU avg/p50/p95 (mCPU) | CPU max (mCPU) | RSS p50/p95 (MiB) | RSS max (MiB) | WS p50/p95 (MiB) | WS max (MiB) | Cache p50/p95 (MiB) | Cache max (MiB) | Restarts | Limit |")
    print("|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
    for row in rows:
        print(
            f"| `{row['pod']}` | `{row['image']}` | {row['first']} | {row['hours']:.1f} | "
            f"{row['cpu_avg']:.2f}/{row['cpu_p50']:.2f}/{row['cpu_p95']:.2f} | {row['cpu_max']:.1f} | "
            f"{row['rss_p50']:.1f}/{row['rss_p95']:.1f} | {row['rss_max']:.1f} | "
            f"{row['ws_p50']:.1f}/{row['ws_p95']:.1f} | {row['ws_max']:.1f} | "
            f"{row['cache_p50']:.1f}/{row['cache_p95']:.1f} | {row['cache_max']:.1f} | "
            f"{row['restarts']} | {format_gib(row['limit'])} |"
        )

    reasons = query(
        args.vm_url,
        f"max by (pod,reason) (max_over_time("
        f'kube_pod_container_status_last_terminated_reason{{{selector}}}[{args.days}d]))',
    )
    if reasons:
        print()
        print("## Last termination reasons")
        print()
        print("| Pod | Reason |")
        print("|---|---|")
        for series in sorted(reasons, key=lambda item: item["metric"].get("pod", "")):
            pod = series["metric"].get("pod", "?")
            reason = series["metric"].get("reason", "?")
            print(f"| `{pod}` | `{reason}` |")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
