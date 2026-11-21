#!/usr/bin/env bash
# Isolated side-by-side Podman benchmark comparing upstream TS v2.37.2 vs
# Monstrobook (Go) against cloned real /config and read-only /audiobooks.
set -euo pipefail

TS_IMAGE="${TS_IMAGE:-ghcr.io/advplyr/audiobookshelf:2.37.2}"
GO_IMAGE="${GO_IMAGE:-ghcr.io/ppoloskov/monstrobook:latest}"
TS_PORT="${TS_PORT:-18081}"
GO_PORT="${GO_PORT:-18082}"
CONCURRENCY="${CONCURRENCY:-8}"
REQUESTS="${REQUESTS:-100}"

CONFIG_SRC=""
MEDIA_SRC=""
TOKEN=""
USERNAME=""
PASSWORD=""
RUN_SCAN=0
KEEP_TMP=0

usage() {
  cat <<'EOF'
Usage: tools/bench-podman.sh --config <dir> --media <dir> (--token <jwt> | --username <u> --password <p>) [options]

Options:
  --scan          Also time POST /api/libraries/<id>/scan
  --concurrency N HTTP concurrency (default: 8)
  --requests N    Requests per benchmark (default: 100)
  --ts-image IMG  Override TS image (default: ghcr.io/advplyr/audiobookshelf:2.37.2)
  --go-image IMG  Override Go image (default: ghcr.io/ppoloskov/monstrobook:latest)
  --keep          Keep temporary cloned config dirs after exit
EOF
  exit 1
}

while [ $# -gt 0 ]; do
  case "$1" in
    --config) CONFIG_SRC="$2"; shift 2 ;;
    --media) MEDIA_SRC="$2"; shift 2 ;;
    --token) TOKEN="$2"; shift 2 ;;
    --username) USERNAME="$2"; shift 2 ;;
    --password) PASSWORD="$2"; shift 2 ;;
    --concurrency) CONCURRENCY="$2"; shift 2 ;;
    --requests) REQUESTS="$2"; shift 2 ;;
    --ts-image) TS_IMAGE="$2"; shift 2 ;;
    --go-image) GO_IMAGE="$2"; shift 2 ;;
    --scan) RUN_SCAN=1; shift ;;
    --keep) KEEP_TMP=1; shift ;;
    *) usage ;;
  esac
done

[ -n "$CONFIG_SRC" ] && [ -n "$MEDIA_SRC" ] || usage
[ -d "$CONFIG_SRC" ] || { echo "missing --config dir: $CONFIG_SRC" >&2; exit 1; }
[ -d "$MEDIA_SRC" ] || { echo "missing --media dir: $MEDIA_SRC" >&2; exit 1; }

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TMP_DIR="$(mktemp -d /tmp/abs-bench-XXXXXX)"
cleanup() {
  podman rm -f abs-bench-ts abs-bench-go >/dev/null 2>&1 || true
  if [ "$KEEP_TMP" -eq 0 ]; then
    rm -rf "$TMP_DIR"
  else
    echo "[bench] kept temp dir: $TMP_DIR"
  fi
}
trap cleanup EXIT

echo "[bench] cloning $CONFIG_SRC -> $TMP_DIR/{ts,go}"
cp -a "$CONFIG_SRC" "$TMP_DIR/ts"
cp -a "$CONFIG_SRC" "$TMP_DIR/go"
# Purge cover caches so cold WebP resize starts equally empty.
rm -rf "$TMP_DIR/ts/metadata/cache/covers" "$TMP_DIR/go/metadata/cache/covers"

start_container() {
  local name="$1" image="$2" port="$3" cfg="$4" extra_env=("${@:5}")
  podman rm -f "$name" >/dev/null 2>&1 || true
  local t0
  t0="$(python3 -c 'import time; print(time.perf_counter())')"
  podman run -d --name "$name" \
    --user 1001:1001 --cpus=1 --memory=4g \
    -p "127.0.0.1:${port}:8080" \
    -e PORT=8080 -e CONFIG_PATH=/config -e METADATA_PATH=/config/metadata \
    "${extra_env[@]}" \
    -v "${cfg}:/config:Z" \
    -v "${MEDIA_SRC}:/audiobooks:ro" \
    "$image" >/dev/null

  for _ in $(seq 1 120); do
    if curl -sf "http://127.0.0.1:${port}/healthcheck" >/dev/null 2>&1; then
      python3 -c "import time; print(f'{time.perf_counter() - $t0:.2f}')"
      return 0
    fi
    sleep 0.25
  done
  echo "container $name failed healthcheck" >&2
  podman logs "$name" >&2 || true
  return 1
}

echo "[bench] starting TS ($TS_IMAGE)..."
TS_BOOT="$(start_container abs-bench-ts "$TS_IMAGE" "$TS_PORT" "$TMP_DIR/ts" -e NODE_OPTIONS=--max-old-space-size=2560)"
echo "[bench] starting Go ($GO_IMAGE)..."
GO_BOOT="$(start_container abs-bench-go "$GO_IMAGE" "$GO_PORT" "$TMP_DIR/go")"

echo ""
echo "### Container Boot & Memory"
echo "- **TS cold boot:** ${TS_BOOT}s | **Go cold boot:** ${GO_BOOT}s"
echo "- **Initial stats:**"
podman stats --no-stream --format "  - {{.Name}}: CPU={{.CPUPerc}} MEM={{.MemUsage}}" abs-bench-ts abs-bench-go

CMD=(python3 "$SCRIPT_DIR/bench-compare.py"
  --ts-url "http://127.0.0.1:${TS_PORT}"
  --go-url "http://127.0.0.1:${GO_PORT}"
  --concurrency "$CONCURRENCY"
  --requests "$REQUESTS")
if [ -n "$TOKEN" ]; then
  CMD+=(--token "$TOKEN")
else
  CMD+=(--username "$USERNAME" --password "$PASSWORD")
fi
if [ "$RUN_SCAN" -eq 1 ]; then
  CMD+=(--scan)
fi

echo ""
echo "### HTTP, Cover Resize & Streaming"
"${CMD[@]}"

echo ""
echo "### Post-Load Memory & Graceful Shutdown"
podman stats --no-stream --format "  - {{.Name}}: CPU={{.CPUPerc}} MEM={{.MemUsage}}" abs-bench-ts abs-bench-go

stop_timed() {
  local name="$1"
  local t0
  t0="$(python3 -c 'import time; print(time.perf_counter())')"
  podman stop -t 10 "$name" >/dev/null
  python3 -c "import time; print(f'{time.perf_counter() - $t0:.2f}')"
}

TS_STOP="$(stop_timed abs-bench-ts)"
GO_STOP="$(stop_timed abs-bench-go)"
echo "- **SIGTERM stop time:** TS=${TS_STOP}s | Go=${GO_STOP}s"

if command -v sqlite3 >/dev/null 2>&1; then
  echo "- **SQLite integrity_check (TS):** $(sqlite3 "$TMP_DIR/ts/absdatabase.sqlite" 'PRAGMA integrity_check;')"
  echo "- **SQLite integrity_check (Go):** $(sqlite3 "$TMP_DIR/go/absdatabase.sqlite" 'PRAGMA integrity_check;')"
fi
