PARITY_NODE = mise exec node@22 -- node

.PHONY: parity parity-equivalence parity-full ratchet ratchet-update status

parity:
	SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1 $(PARITY_NODE) go-backend/tools/parity/suite-runner.cjs core

parity-full:
	SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1 $(PARITY_NODE) go-backend/tools/parity/suite-runner.cjs full

parity-equivalence:
	SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1 $(PARITY_NODE) go-backend/tools/parity/suite-runner.cjs equivalence

ratchet:
	cd go-backend && go run ./tools/ratchet

ratchet-update:
	cd go-backend && go run ./tools/ratchet -update

status:
	$(PARITY_NODE) go-backend/tools/parity/status.cjs
