PARITY_NODE = mise exec node@22 -- node

.PHONY: parity parity-equivalence parity-full parity-replacement ratchet ratchet-update status

parity:
	SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1 $(PARITY_NODE) go-backend/tools/parity/suite-runner.cjs core

parity-full:
	SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1 $(PARITY_NODE) go-backend/tools/parity/suite-runner.cjs full

parity-equivalence:
	SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1 $(PARITY_NODE) go-backend/tools/parity/suite-runner.cjs equivalence

# In-place replacement trial on a realistic upstream-created installation.
parity-replacement:
	SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1 $(PARITY_NODE) go-backend/tools/parity/replacement-trial.cjs

ratchet:
	cd go-backend && go run ./tools/ratchet

ratchet-update:
	cd go-backend && go run ./tools/ratchet -update

status:
	$(PARITY_NODE) go-backend/tools/parity/status.cjs
