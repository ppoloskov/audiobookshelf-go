# Node 22 runner for the parity harness: mise when installed, else plain node
# (must then be Node 22.x). Override with `make PARITY_NODE=... <target>`.
PARITY_NODE ?= $(shell command -v mise >/dev/null 2>&1 && echo "mise exec node@22 -- node" || echo node)

.PHONY: parity parity-equivalence parity-full parity-replacement browser-check ratchet ratchet-update status

parity:
	SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1 $(PARITY_NODE) go-backend/tools/parity/suite-runner.cjs core

parity-full:
	SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1 $(PARITY_NODE) go-backend/tools/parity/suite-runner.cjs full

parity-equivalence:
	SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1 $(PARITY_NODE) go-backend/tools/parity/suite-runner.cjs equivalence

# In-place replacement trial on a realistic upstream-created installation.
parity-replacement:
	SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1 $(PARITY_NODE) go-backend/tools/parity/replacement-trial.cjs

# Chromium acceptance of the unchanged web client (Playwright), Node vs Go.
# Creates a fresh fixture; does not rewrite the checked-in contract report.
browser-check:
	SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1 $(PARITY_NODE) go-backend/tools/parity/browser-check.cjs

ratchet:
	cd go-backend && go run ./tools/ratchet

ratchet-update:
	cd go-backend && go run ./tools/ratchet -update

status:
	$(PARITY_NODE) go-backend/tools/parity/status.cjs
