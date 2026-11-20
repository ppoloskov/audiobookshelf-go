# Node 22 runner for the Twin-Server Differential Oracle: plain node inside Nix
# or when node on PATH is already v22.x, else mise when installed, else node.
# Override with `make ORACLE_NODE_CMD=...`.
ORACLE_NODE_CMD ?= $(shell if [ -n "$$IN_NIX_SHELL" ] || node -v 2>/dev/null | grep -q '^v22\.'; then echo node; elif command -v mise >/dev/null 2>&1; then echo "mise exec node@22 -- node"; else echo node; fi)
ORACLE_ARGS ?=

.PHONY: build test upstream check verify oracle openapi oracle-unit oracle-routes client-audit web-e2e nix-shell nix-verify nix-test

build:
	go build -o monstrobook ./cmd/monstrobook

test:
	go test ./... -count=1

upstream:
	$(ORACLE_NODE_CMD) testmachine/oracle/fetch-upstream.mjs

# Authoritative verification gate: live Twin-Server Differential Oracle (pinned
# TS v2.37.2 vs Go across HTTP, 26 SQLite tables, 3-audience Socket.IO, FS,
# stubs, bidirectional rollback, plus TS V8 & Go coverage + client audit).
check: verify

verify: oracle

oracle:
	$(ORACLE_NODE_CMD) testmachine/oracle/run-oracle.mjs $(ORACLE_ARGS)

openapi:
	ORACLE_OPENAPI_OUT=docs/openapi.json $(ORACLE_NODE_CMD) testmachine/oracle/run-oracle.mjs $(ORACLE_ARGS)

# Fast pure-Go unit & mutation tests for the oracle differ (no live servers).
oracle-unit:
	go test -race -count=1 ./testmachine/oracle/...

# Regenerate routes.json and sockets.json directly from upstream server/.
oracle-routes:
	$(ORACLE_NODE_CMD) testmachine/oracle/gen_routes.mjs

# Static cross-check of web client ($axios/Socket.IO) + 6 mobile client audits.
client-audit:
	$(ORACLE_NODE_CMD) testmachine/oracle/client-audit.mjs

# Playwright E2E driving the unchanged bundled Nuxt SPA against TS and Go.
web-e2e:
	$(ORACLE_NODE_CMD) testmachine/oracle/web-e2e.mjs

# Reproducible Nix dev shell and verification helpers.
nix-shell:
	nix --extra-experimental-features 'nix-command flakes' develop

nix-verify:
	nix --extra-experimental-features 'nix-command flakes' develop --command make verify

nix-test:
	nix --extra-experimental-features 'nix-command flakes' develop --command make test
