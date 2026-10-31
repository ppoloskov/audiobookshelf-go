# Archived parity ledger tools

These Python scripts supported the former hand-curated capability ledger and
strict-finding audit. They are retained as historical evidence, not as active
parity gates. The active source inventory remains generated and checked by
`go-backend/tools/parity/build_route_coverage.py --check`.

The archived regression can still be run from the repository root:

```sh
python3 -m unittest -v docs/agent-work/archive/parity-tools/test_build_capabilities.py
```

The scripts derive the repository root from their archived location and write
to their existing default ledger only when explicitly run without `--out`.
