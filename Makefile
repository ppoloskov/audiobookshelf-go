# Node 22 runner for the Twin-Server Differential Oracle: mise when installed,
# else plain node (must then be Node 22.x). Override with `make ORACLE_NODE_CMD=...`.
ORACLE_NODE_CMD ?= $(shell command -v mise >/dev/null 2>&1 && echo "mise exec node@22 -- node" || echo node)
ORACLE_ARGS ?=

.PHONY: check verify oracle oracle-unit oracle-routes client-audit web-e2e

# Authoritative verification gate: live Twin-Server Differential Oracle (pinned
# TS v2.37.2 vs Go across HTTP, 26 SQLite tables, 3-audience Socket.IO, FS,
# stubs, bidirectional rollback, plus TS V8 & Go coverage + client audit).
check: verify

verify: oracle

oracle:
	$(ORACLE_NODE_CMD) go-backend/testmachine/oracle/run-oracle.mjs $(ORACLE_ARGS)

# Fast pure-Go unit & mutation tests for the oracle differ (no live servers).
oracle-unit:
	cd go-backend && go test -race ./testmachine/oracle/...

# Regenerate routes.json and sockets.json directly from upstream server/.
oracle-routes:
	$(ORACLE_NODE_CMD) go-backend/testmachine/oracle/gen_routes.mjs

# Static cross-check of web client ($axios/Socket.IO) + 6 mobile client audits.
client-audit:
	$(ORACLE_NODE_CMD) go-backend/testmachine/oracle/client-audit.mjs

# Playwright E2E driving the unchanged bundled Nuxt SPA against TS and Go.
web-e2e:
	$(ORACLE_NODE_CMD) go-backend/testmachine/oracle/web-e2e.mjs
