> Archived 2026-11-11: completed or superseded report retained as evidence. Current requirements and status are in [ROADMAP.md](../ROADMAP.md) and [ARCHITECTURE.md](../ARCHITECTURE.md).

# Audiobookshelf (`abooks`) resource consumption report

Date: 2026-11-10
Data cutoff: 2026-11-10 16:00 Brisbane (06:00 UTC)
Cluster: `au` context, `https://192.168.10.10:6443`
Namespace / workload: `media/abooks`
Timezone for human-readable times: Australia/Brisbane (UTC+10)

## Summary

- The current image, `ghcr.io/ppoloskov/audiobookshelf-go:sha-b42969b`, started at
  2026-11-10 12:02 Brisbane and has 0 restarts. It runs as pod
  `abooks-86956bccdf-fndnk` on `hyperwombat.au.poloskov.net`.
- The current Go build uses less CPU and memory than both the TypeScript 2.37.2
  build and the previous Go build (`sha-4616244`).
- The largest change is the hourly library scan:

  | Build | Hourly scan |
  |---|---:|
  | TypeScript 2.37.2 | about 1.1 s average |
  | Go `sha-4616244` | about 24 s average (20-32 s typical) |
  | Go `sha-b42969b` | 0-1 s |

- The previous Go build's high average CPU was almost entirely its hourly scan.
  Its idle CPU was already lower than TypeScript's.
- Process memory is now about one third of the last TypeScript build, and peak
  working set is about 5% of the TypeScript peak.
- No Go pod has restarted or been OOM-killed. Earlier TypeScript rolls were
  OOM-killed under 1 GiB and 2 GiB memory limits.

## Data sources and availability

| Source | Where | Retention / coverage |
|---|---|---|
| Container CPU, memory, network, filesystem | VictoriaMetrics `monitoring/victoriametrics`, metrics from cAdvisor | 30 days; earliest samples around 2026-10-11 07:00 UTC |
| Pod/image/restart/limit state | VictoriaMetrics, metrics from kube-state-metrics | 30 days |
| Per-scan application logs | `/config/metadata/logs/scans` on the `abooks-config` PVC | 9,370 files, 2024-12-12 through 2026-11-03 |
| TypeScript daily logs | `/config/backup/metadata/logs/daily` (snapshot from 2026-11-03 09:39) | 2026-10-23 through 2026-11-03 |
| Go daily logs | `/config/metadata/logs/daily` on the `abooks-config` PVC | rolling 7 days, currently 2026-11-04 onward |

The metrics retention is the hard limit for resource history. The scan logs go
back much further, but the library layout and Audiobookshelf version changed
over that period, so early scan durations are not a controlled comparison.

## Observed deployment timeline

Times are when the pod was first/last observed in VictoriaMetrics. The first
TypeScript pod was already running when the 30-day metric window began.

| Brisbane time | Image | Memory limit | Restarts | Notes |
|---|---|---:|---:|---|
| before 2026-10-11 17:00 | TS 2.36.0 | 1 GiB | 2 | Long-running baseline; already running at retention boundary |
| 2026-10-27 19:25 - 2026-10-29 09:50 | TS 2.36.0 | 1 GiB | 13 | OOM-killed; RSS reached about 1.0 GiB |
| 2026-10-29 15:15 - 22:00 | TS 2.37.2 | 2 GiB | 11 | First 2.37.2 roll; last termination reason OOMKilled |
| 2026-10-29 22:05 - 2026-11-03 09:45 | TS 2.37.2 | 4 GiB | 0 | Stable, but working set spiked to about 2.1 GiB |
| 2026-11-03 19:10 - 2026-11-10 12:00 | Go `sha-4616244` | 4 GiB | 0 | Stable; hourly scan 20-32 s |
| 2026-11-10 12:02 - now | Go `sha-b42969b` | 4 GiB | 0 | Current build; hourly scan 0-1 s |

## Resource results

The current deployment requests 100 mCPU and 512 MiB and limits the container
to 1 CPU and 4 GiB. Metrics are from cAdvisor at 1-minute resolution. `mCPU` is
millicores, so 1000 mCPU = 1 CPU core. `RSS` is process resident memory;
`working set` is what Kubernetes accounts to the container and includes page
cache.

| Period | Image | Hours | CPU avg | CPU p50 | CPU p95 | CPU max | RSS p50 | RSS p95 | RSS max | WS p50 | WS p95 | WS max | Cache p50 | Cache p95 | Restarts | Limit |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| TS 2.36.0 long run | `audiobookshelf:2.36.0` | 386.4 | 1.35 | 0.38 | 10.13 | 19.8 | 128.1 | 175.1 | 180.6 | 157.5 | 206.1 | 250.1 | 49.7 | 177.0 | 2 | 1 GiB |
| TS 2.36.0 churn | `audiobookshelf:2.36.0` | 38.4 | 2.67 | 0.37 | 13.67 | 215.5 | 101.1 | 174.2 | 1013.2 | 136.1 | 210.9 | 1022.8 | 73.4 | 827.6 | 13 | 1 GiB |
| TS 2.37.2 first roll | `audiobookshelf:2.37.2` | 6.8 | 5.14 | 0.36 | 11.27 | 564.5 | 55.6 | 96.7 | 238.4 | 113.4 | 152.5 | 305.7 | 91.2 | 99.9 | 11 | 2 GiB |
| TS 2.37.2 stable | `audiobookshelf:2.37.2` | 107.7 | 1.66 | 0.29 | 7.58 | 147.1 | 68.4 | 126.1 | 2009.5 | 88.4 | 218.2 | 2163.8 | 142.7 | 241.6 | 0 | 4 GiB |
| Go `sha-4616244` | `audiobookshelf-go:sha-4616244` | 160.6 | 7.30 | 0.15 | 80.72 | 231.3 | 29.5 | 37.3 | 559.8 | 68.4 | 112.3 | 635.7 | 2217.5 | 2369.3 | 0 | 4 GiB |
| Go `sha-b42969b` | `audiobookshelf-go:sha-b42969b` | 3.9 | 0.45 | 0.07 | 3.28 | 5.8 | 21.0 | 27.7 | 77.1 | 61.6 | 71.6 | 103.6 | 44.5 | 70.9 | 0 | 4 GiB |

CPU and memory are in mCPU and MiB respectively. The current Go row covers only
the first four hours after rollout.

### What the numbers mean

- The previous Go build's 7.30 mCPU average is not steady-state usage. Its p50
  was 0.15 mCPU. The hourly scan consumed roughly 20-30 CPU-seconds, which alone
  contributes about 5-7 mCPU to the hourly average.
- The current build's p50 is 0.07 mCPU and its four-hour average is 0.45 mCPU,
  including startup and hourly scans. It is now lower than TypeScript's
  0.29 mCPU p50.
- TypeScript working set was 88 MiB median and 218 MiB p95. The current Go build
  is 62 MiB median and 72 MiB p95.
- TypeScript's 2.1 GiB peak and previous Go's 2.2 GiB page-cache median are both
  gone. The current build holds about 45 MiB of page cache.
- The previous Go build's process RSS was already low (29.5 MiB median), but its
  scanner pulled roughly 2.2 GiB of page cache into the cgroup during its hourly
  scans. The current scanner does not.
- Average network receive was about 4.3 kB/s for TypeScript 2.37.2, 3.3 kB/s for
  the previous Go build and 2.2 kB/s for the current four-hour window. The
  workload is the same order of magnitude, though the current window is a little
  quieter.

## Historical TypeScript scan data

The per-scan logs contain the application's `elapsed` value in milliseconds.
This is a longer and more direct view than the 30-day metric window.

An earlier metric-only reading interpreted the hourly CPU burst as a 7-9 minute
scan. The per-scan logs show the scan itself took about 1.1 s on the last stable
TypeScript pod. The five-minute CPU rate smears that burst across five minutes;
the metric burst is not a direct measurement of scan wall time.

### Last stable TypeScript pod

The final TypeScript 2.37.2 pod (4 GiB limit, 0 restarts) ran 107 scans between
2026-10-29 22:05 and 2026-11-03 09:45 Brisbane:

- average: 1.06 s
- p50: 0.88 s
- p95: 1.15 s
- max: 13.86 s

### Scan duration by TypeScript version

| Version | Scans | Avg | p50 | p95 | Max | Scans over 10 s | Scans over 60 s |
|---|---:|---:|---:|---:|---:|---:|---:|
| 2.17.5 | 15 | 45.41 s | 4.36 s | 289.52 s | 289.52 s | 5 | 2 |
| 2.17.6 | 2 | 0.43 s | 0.53 s | 0.53 s | 0.53 s | 0 | 0 |
| 2.17.7 | 13 | 0.37 s | 0.36 s | 0.49 s | 0.49 s | 0 | 0 |
| 2.18.1 | 45 | 1.36 s | 0.38 s | 7.56 s | 11.39 s | 2 | 0 |
| 2.19.5 | 13 | 4.21 s | 0.65 s | 39.50 s | 39.50 s | 1 | 0 |
| 2.20.0 | 69 | 0.98 s | 0.60 s | 1.49 s | 9.43 s | 0 | 0 |
| 2.23.0 | 102 | 1.15 s | 0.59 s | 4.35 s | 12.20 s | 2 | 0 |
| 2.29.0 | 37 | 2.71 s | 0.86 s | 1.76 s | 67.68 s | 1 | 1 |
| 2.30.0 | 3066 | 0.68 s | 0.49 s | 1.11 s | 95.48 s | 15 | 1 |
| 2.32.1 | 4295 | 2.01 s | 1.68 s | 3.03 s | 237.39 s | 19 | 1 |
| 2.36.0 | 1599 | 1.54 s | 1.39 s | 2.24 s | 40.10 s | 5 | 0 |
| 2.37.2 | 114 | 1.06 s | 0.88 s | 1.24 s | 13.86 s | 1 | 0 |

The early 2.17.x rows include the initial library setup and are not comparable
to steady state. From 2.30.0 onward the hourly scan is consistently in the
sub-second to low-single-digit seconds range, with rare outliers.

### Monthly scan duration, 2026

The scan cadence changed from daily to hourly around October 2025, so raw scan
counts are not comparable across years. The table below starts in 2026, when
the hourly cadence was stable.

| Month | Scans | Avg | p50 | p95 | Max | Over 10 s | Over 60 s |
|---|---:|---:|---:|---:|---:|---:|---:|
| 2026-01 | 750 | 0.58 s | 0.36 s | 0.60 s | 33.17 s | 5 | 0 |
| 2026-02 | 606 | 1.08 s | 0.65 s | 1.84 s | 95.48 s | 1 | 1 |
| 2026-03 | 747 | 2.03 s | 1.78 s | 2.90 s | 16.01 s | 1 | 0 |
| 2026-04 | 722 | 2.17 s | 1.82 s | 3.18 s | 47.23 s | 3 | 0 |
| 2026-05 | 738 | 2.54 s | 1.78 s | 4.40 s | 237.39 s | 6 | 1 |
| 2026-06 | 717 | 2.08 s | 1.61 s | 3.26 s | 28.88 s | 6 | 0 |
| 2026-07 | 736 | 1.83 s | 1.63 s | 2.90 s | 17.92 s | 2 | 0 |
| 2026-08 | 734 | 1.26 s | 1.17 s | 1.57 s | 28.60 s | 1 | 0 |
| 2026-09 | 719 | 1.46 s | 1.40 s | 1.75 s | 12.06 s | 2 | 0 |
| 2026-10 | 739 | 1.65 s | 1.41 s | 2.79 s | 40.10 s | 4 | 0 |
| 2026-11 (to 3 Nov) | 58 | 0.99 s | 0.88 s | 1.37 s | 4.49 s | 0 | 0 |

## Findings

1. **Memory is substantially lower than TypeScript.** Process RSS is 21.0 MiB
   median versus 68.4 MiB for the last TypeScript build. Working set is 61.6 MiB
   versus 88.4 MiB. The current build's peak working set is 103.6 MiB versus
   2.16 GiB for TypeScript.

2. **The previous Go build's average CPU was misleading.** Its p50 was
   0.15 mCPU, lower than TypeScript. The 7.30 mCPU average came from a 20-32 s
   hourly scan that consumed about one core for that period. The current build
   reduces that scan to 0-1 s, so the average falls to 0.45 mCPU.

3. **The current Go scanner is faster than TypeScript's.** TypeScript's last
   stable pod averaged 1.06 s per scan. The current Go build logs 0-1 s. The
   previous Go build averaged about 24 s.

4. **The current build avoids the old scanner's page-cache pressure.** The
   previous Go build held about 2.2 GiB of page cache in the container cgroup
   (median; 2.37 GiB p95). The current build holds about 45 MiB median and
   71 MiB p95. This matters for node memory pressure even though it does not
   show up as process RSS.

5. **Go has been stable.** The previous and current Go pods have 0 restarts and
   0 OOM events. The TypeScript 2.36.0 churn pod was OOM-killed under 1 GiB
   (13 restarts), and the first 2.37.2 roll had 11 restarts under 2 GiB. The
   final TypeScript 2.37.2 roll at 4 GiB was stable but still spiked to about
   2.1 GiB working set.

## Caveats

- The current Go pod is only about four hours old. Memory may still settle, and
  a full day of scans has not yet been observed.
- CPU values use a five-minute rate, so sub-minute scan bursts are smeared over
  five minutes. Use the scan log `elapsed` values for scan wall time.
- TypeScript metrics are limited to the 30-day VictoriaMetrics retention. Scan
  logs go back to 2024, but library size, layout and Audiobookshelf version
  changed over that period.
- The first 2.37.2 roll's last termination reason is OOMKilled, but the sampled
  working set peaked at only 306 MiB under a 2 GiB limit. The exact spike may
  have occurred between scrapes, or the OOM may have been node-level pressure.
  Treat the restart count as reliable and the cause as unproven.
- `working set` includes page cache; `RSS` is the better number for process
  memory. The current `kubectl top` value can jump transiently after a directory
  walk because page cache is charged to the cgroup.
- The scan log `elapsed` is the application's own measurement. Go daily logs
  round scan times to whole seconds.
- The current build's 0-1 s scan is consistent with the stat-only skip path for
  unchanged files. This report did not re-run a live add/rename/delete
  change-detection test; the scanner's own tests and a one-off real-file check
  cover that separately.

## How to get data for future comparison

### 1. Port-forward VictoriaMetrics

```sh
kubectl --context au -n monitoring port-forward svc/victoriametrics 18428:8428
```

VictoriaMetrics is served under the `/vm` path prefix. All API calls below use:

```sh
VM=http://127.0.0.1:18428/vm
```

### 2. List pods, images and observed lifetimes

```sh
curl -s "$VM/api/v1/query_range" \
  --data-urlencode 'query=max by (pod,image) (kube_pod_container_info{namespace="media",pod=~"abooks-.*",container="abooks"})' \
  --data-urlencode "start=$(date -v-30d +%s)" \
  --data-urlencode "end=$(date +%s)" \
  --data-urlencode 'step=300'
```

For a quick current view:

```sh
kubectl -n media get deploy abooks -o jsonpath='{.spec.template.spec.containers[0].image}{"\n"}'
kubectl -n media get pods -l app=abooks -o wide
kubectl -n media top pod -l app=abooks
```

### 3. CPU

Average CPU rate per pod:

```promql
max by (pod) (
  rate(container_cpu_usage_seconds_total{
    namespace="media",pod=~"abooks-.*",container="abooks"
  }[5m])
)
```

Use `query_range` with `step=60` for windows up to about 20 days. For a full
30-day window, use `step=120` or `step=300`; VictoriaMetrics rejects more than
30,000 points per series.

For a single pod:

```sh
curl -s "$VM/api/v1/query_range" \
  --data-urlencode 'query=max by (pod) (rate(container_cpu_usage_seconds_total{namespace="media",pod="abooks-86956bccdf-fndnk",container="abooks"}[5m]))' \
  --data-urlencode "start=$(date -v-1d +%s)" \
  --data-urlencode "end=$(date +%s)" \
  --data-urlencode 'step=60'
```

### 4. Memory

Process RSS:

```promql
max by (pod) (
  container_memory_rss{
    namespace="media",pod=~"abooks-.*",container="abooks"
  }
)
```

Kubernetes working set:

```promql
max by (pod) (
  container_memory_working_set_bytes{
    namespace="media",pod=~"abooks-.*",container="abooks"
  }
)
```

Cgroup page cache:

```promql
max by (pod) (
  container_memory_cache{
    namespace="media",pod=~"abooks-.*",container="abooks"
  }
)
```

### 5. Restarts, limits and termination reasons

```promql
max_over_time(
  kube_pod_container_status_restarts_total{
    namespace="media",pod=~"abooks-.*",container="abooks"
  }[30d]
)
```

```promql
max by (pod,resource) (
  max_over_time(
    kube_pod_container_resource_limits{
      namespace="media",pod=~"abooks-.*",container="abooks"
    }[30d]
  )
)
```

```promql
max by (pod,reason) (
  max_over_time(
    kube_pod_container_status_last_terminated_reason{
      namespace="media",pod=~"abooks-.*"
    }[30d]
  )
)
```

### 6. Scan durations

The application writes one file per scan. The first line is a JSON object with
`library.lastScanVersion` and `elapsed` in milliseconds. Extract the first line
of every file:

```sh
kubectl -n media exec deploy/abooks -- sh -c '
  for f in /config/metadata/logs/scans/*; do
    printf "%s " "$(basename "$f")";
    head -1 "$f";
  done
' > /tmp/abooks_scans.txt
```

Parse locally (Python 3, standard library only):

```python
import json, re, collections

rows = []
for line in open("/tmp/abooks_scans.txt", encoding="utf-8", errors="replace"):
    name, _, body = line.partition(" ")
    m = re.match(r"(\d{4}-\d{2}-\d{2})_", name)
    if not m:
        continue
    try:
        d = json.loads(body)
    except json.JSONDecodeError:
        continue
    if "elapsed" not in d:
        continue
    rows.append((
        m.group(1),
        d.get("library", {}).get("lastScanVersion", "?"),
        float(d["elapsed"]) / 1000,
    ))

by_version = collections.defaultdict(list)
for _, version, seconds in rows:
    by_version[version].append(seconds)

for version, values in sorted(by_version.items()):
    values.sort()
    p50 = values[len(values) // 2]
    p95 = values[min(len(values) - 1, int(len(values) * 0.95))]
    print(version, len(values), round(sum(values) / len(values), 2),
          round(p50, 2), round(p95, 2), round(max(values), 2))
```

Go scans after 2026-11-03 are in the daily logs:

```sh
kubectl -n media exec deploy/abooks -- sh -c '
  grep -hE "Library scan .* completed in" \
    /config/metadata/logs/daily/$(date +%Y-%m-%d).txt
'
```

TypeScript daily logs from the final rollout are in the backup snapshot:

```sh
kubectl -n media exec deploy/abooks -- sh -c '
  grep -hE "Library scan .* completed in" \
    /config/backup/metadata/logs/daily/2026-11-02.txt
'
```

The backup is a snapshot from 2026-11-03 09:39 and may be replaced; do not rely
on it as a permanent archive.

### 7. Reusable report script

A metrics-only helper is included at:

```sh
go-backend/tools/abooks-resource-report.py
```

Usage:

```sh
python3 go-backend/tools/abooks-resource-report.py --days 30
```

To freeze a report at a specific instant, use `--end` with an ISO 8601 time:

```sh
python3 go-backend/tools/abooks-resource-report.py \
  --days 30 --end 2026-11-10T06:00:00Z
```

It expects the port-forward above and prints a Markdown table with per-pod CPU,
RSS, working set, cache, restarts and limits. It uses local time for display,
discovers each pod's observed lifetime, and filters every metric query to that
pod. Run it after each meaningful rollout and keep the output with the
deployment record.

### 8. What to compare after the next rollout

At minimum, compare these for the old and new pod over comparable windows:

- CPU p50 and p95 over a quiet period, not only the average.
- CPU average over a full day, to capture the hourly scan.
- Scan wall time from the app logs, not from the container CPU graph.
- RSS p50/p95 and peak working set.
- Page cache median, because the scanner can inflate it without affecting RSS.
- Restart count, OOM events and memory limit.
- Network receive rate, to confirm the workload was comparable.

Do not run `du`, `find` or another directory walk inside the `abooks` container
before sampling memory. Those commands pull directory metadata into the cgroup
page cache and can make the working set look much larger for several minutes.
