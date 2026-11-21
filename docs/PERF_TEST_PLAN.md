# Performance & Resource Comparison Plan: TS v2.37.2 vs Monstrobook (Go)

This plan compares upstream Audiobookshelf (`ghcr.io/advplyr/audiobookshelf:2.37.2`) against Monstrobook (`ghcr.io/ppoloskov/monstrobook:latest`) using **real `absdatabase.sqlite`, real metadata, and real audiobooks**, while keeping production data untouched.

---

## 1. Environment & Isolation Rules

1. **Never share a live `/config` or `/metadata` volume.** Always clone `/config` into two separate temporary directories (`bench-ts` and `bench-go`).
2. **Mount media read-only (`:ro`).** Both servers run with `-v <audiobooks>:/audiobooks:ro` so neither can modify files.
3. **Match production Kubernetes constraints** (matching the `media/abooks` CUE unit):
   - `--user 1001:1001`
   - `--cpus=1 --memory=4g`
   - `PORT=8080`, `CONFIG_PATH=/config`, `METADATA_PATH=/config/metadata`
   - TS container includes `NODE_OPTIONS=--max-old-space-size=2560`
4. **Control for OS page cache:** Run one quick warmup request pass (handled automatically by [tools/bench-compare.py](../tools/bench-compare.py)) before timing hot API endpoints. For cold scan / cold cover resize benchmarks, purge `metadata/cache/covers` and, if comparing NFS/disk scans back-to-back on Linux, drop caches (`echo 3 | sudo tee /proc/sys/vm/drop_caches`) between TS and Go runs.

---

## 2. Automated Podman Benchmark (Nix / Linux Host)

Run [tools/bench-podman.sh](../tools/bench-podman.sh) on a host with Podman, `python3`, and `sqlite3`:

```sh
./tools/bench-podman.sh \
  --config /path/to/real/config \
  --media /mnt/media/audiobooks \
  --token "$ABS_TOKEN"
```

What `bench-podman.sh` automates:
1. Clones `--config` into `/tmp/abs-bench-XXXXXX/{ts,go}` (reusing `rsync`/`cp -a`).
2. Purges `metadata/cache/covers` in both clones so cover-resize benchmarks start cold.
3. Starts `abs-ts` (`127.0.0.1:18081`) and `abs-go` (`127.0.0.1:18082`) sequentially or side-by-side (`--cpus=1 --memory=4g`, `--user 1001:1001`) and measures **cold boot time to `GET /healthcheck` 200** + **initial RSS** (`podman stats --no-stream`).
4. Runs [tools/bench-compare.py](../tools/bench-compare.py) to measure:
   - **Hot API latency (`p50` / `p95` / `p99`) & RPS** across your real library (`items`, `sort=authorName`, `collapseseries=1`, `personalized`, `search`, `item?expanded=1`).
   - **Cold WebP cover resize** (`GET /api/items/{id}/cover?format=webp&width=400`, `sharp` vs `ffmpeg`).
   - **Audio `Range:` streaming** (`GET /api/items/{id}/file/{ino}`).
   - **Optional full library scan (`--scan`)** (`POST /api/libraries/{id}/scan` wall-clock duration).
5. Measures **post-load RSS** (`podman stats`), sends `SIGTERM` to both containers, records **graceful shutdown time**, and runs `sqlite3 PRAGMA integrity_check;` on both cloned DBs.

You can also run [tools/bench-compare.py](../tools/bench-compare.py) directly against any two already-running instances (for example, two Kubernetes port-forwards):

```sh
python3 tools/bench-compare.py \
  --ts-url http://127.0.0.1:18081 \
  --go-url http://127.0.0.1:18082 \
  --token "$ABS_TOKEN" \
  --concurrency 8 \
  --requests 100
```

---

## 3. Production Kubernetes Telemetry (`abooks-resource-report.py`)

After updating the CUE deployment image to `ghcr.io/ppoloskov/monstrobook:sha-7246e26` (or `:latest`), compare real pod CPU & memory before/after across normal daily usage via VictoriaMetrics:

```sh
kubectl -n monitoring port-forward svc/victoriametrics 18428:8428
python3 tools/abooks-resource-report.py --days 14
```

---

## 4. Target Comparison Scorecard

| Metric | What It Tests | Expected Winner / Notes |
| --- | --- | --- |
| Cold boot to `/healthcheck` | Startup + DB load + route init | Go (no Node/V8/Sequelize boot) |
| Idle & post-load RSS | Memory footprint vs `4Gi` limit | Go (no `2560MiB` V8 heap) |
| Library browse / sort / search `p95` | SQLite queries + JSON serialization | Go (compiled structs, no ORM) |
| Cold WebP cover resize | `sharp` (libvips) vs `ffmpeg` subprocess | Compare `p50`/`p95` (useful to know if `ffmpeg` spawn overhead shows up) |
| Library scan (`--scan`) | NFS `stat`/walk + `ffprobe` + SQLite writes | Compare wall-clock + peak RSS |
| `SIGTERM` shutdown + WAL check | Graceful drain & DB integrity | Must exit `<5s` with `integrity_check: ok` |
