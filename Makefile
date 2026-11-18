# Node 22 runner for the parity harness: mise when installed, else plain node
# (must then be Node 22.x). Override with `make PARITY_NODE=... <target>`.
PARITY_NODE ?= $(shell command -v mise >/dev/null 2>&1 && echo "mise exec node@22 -- node" || echo node)
VERIFY_NODE ?= $(shell command -v mise >/dev/null 2>&1 && mise exec node@22 -- node -p 'process.execPath' || command -v node)
VERIFY_ARGS ?=

.PHONY: verify-packet parity parity-equivalence parity-full parity-replacement parity-scan route-probe browser-check bench client-usage status check verify

# One authoritative verdict: machine health, upstream proof, then the same Go
# assertions. Missing upstream proof is a nonzero result, not advisory success.
check: verify

verify:
	cd go-backend && go run ./tools/verify -node "$(VERIFY_NODE)" $(VERIFY_ARGS)

# Develop one owner packet against pinned upstream: make verify-packet PACKET=auth [RUN=regex]
verify-packet:
	cd go-backend && REPLACEMENT_TARGET=upstream REPLACEMENT_NODE="$(VERIFY_NODE)" go test -tags=replacement ./tests/replacement/$(PACKET) -count=1 -timeout 30m -v $(if $(RUN),-run 'TestReplacement/$(RUN)',)

parity:
	SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1 $(PARITY_NODE) go-backend/tools/parity/suite-runner.cjs core

parity-full:
	SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1 $(PARITY_NODE) go-backend/tools/parity/suite-runner.cjs full

parity-equivalence:
	SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1 $(PARITY_NODE) go-backend/tools/parity/suite-runner.cjs equivalence

# In-place replacement trial on a realistic upstream-created installation.
parity-replacement:
	SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1 $(PARITY_NODE) go-backend/tools/parity/replacement-trial.cjs

# New content and library maintenance (scan, rename, delete, metadata edit, watcher) trial.
parity-scan:
	SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1 $(PARITY_NODE) go-backend/tools/parity/scan-trial.cjs

# Every route pinned upstream registers must be handled by Go (no 501
# "not implemented" fallback). ROUTE_PROBE_ARGS="--work DIR" reuses a seeded
# replacement-trial work dir instead of seeding a fresh installation.
route-probe:
	SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1 $(PARITY_NODE) go-backend/tools/parity/route-probe.cjs $(ROUTE_PROBE_ARGS)

# Chromium acceptance of the unchanged web client (Playwright), Node vs Go.
# Creates a fresh fixture; does not rewrite the checked-in contract report.
browser-check:
	SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1 $(PARITY_NODE) go-backend/tools/parity/browser-check.cjs

# Large-library benchmark (2000 books): upstream vs Go cold start, RSS and
# p50/p95 latency of the hot client calls. BENCH_ARGS="--books 500 --runs 10".
# Work dir and report: BENCH_ARGS="--work DIR" (default: a fresh temp dir).
bench:
	SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1 $(PARITY_NODE) go-backend/tools/parity/bench.cjs $(BENCH_ARGS)

# Client routes and Socket.IO events, mapped to Go handlers (contracts/client-usage.json).
client-usage:
	cd go-backend && go run ./tools/clientusage

status:
	$(PARITY_NODE) go-backend/tools/parity/status.cjs
