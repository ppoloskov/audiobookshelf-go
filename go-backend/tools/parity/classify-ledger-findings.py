#!/usr/bin/env python3
"""Classify strict-ledger diagnostics without changing capability records."""

import collections
import json
import re
import subprocess
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[3]
VALIDATOR = Path.home() / ".agents/skills/go-scope-ledger/scripts/check-ledger.go"
TASKS = ROOT / "docs/agent-work/tasks"
CAPABILITIES = ROOT / "docs/agent-work/capabilities"


def main() -> int:
    command = [
        "go", "run", str(VALIDATOR), "-tasks", str(TASKS), "-capabilities",
        str(CAPABILITIES), "-require-verified", "-require-invariants",
    ]
    result = subprocess.run(command, cwd=ROOT, text=True, stdout=subprocess.PIPE,
                            stderr=subprocess.STDOUT, check=False)
    diagnostics = []
    record_causes = collections.defaultdict(collections.Counter)
    parent_counts = collections.Counter()
    for line in result.stdout.splitlines():
        match = re.match(r"ERROR: .*/capabilities/([^:]+): (.*)", line)
        if not match:
            continue
        filename, message = match.groups()
        path = CAPABILITIES / filename
        try:
            capability = json.loads(path.read_text())
        except (OSError, json.JSONDecodeError):
            capability = {"id": filename, "parent": "(unreadable)"}

        if "in-scope capability is not verified" in message:
            cause = "unverified_capability_status"
        elif "at least one source-backed invariant required" in message:
            cause = "missing_invariant_record"
        elif "at least one invariant must be verified" in message:
            cause = "no_verified_invariant"
        elif "repeat_operation invariant" in message:
            cause = "missing_repeat_operation_invariant"
        elif "explicitly classify stateful" in message:
            cause = "missing_stateful_classification"
        elif "stateful initial scenario must be verified" in message:
            cause = "initial_scenario_not_verified"
        elif "stateful scenario " in message:
            scenario = re.search(r"stateful scenario (\w+)", message)
            cause = "missing_or_unverified_scenario_" + (scenario.group(1) if scenario else "unknown")
        else:
            cause = "other"

        record_causes[filename][cause] += 1
        parent_counts[capability.get("parent", "(none)")] += 1
        diagnostics.append({"file": filename, "id": capability.get("id"),
                            "parent": capability.get("parent"), "cause": cause,
                            "diagnostic": message})

    records = []
    for filename, counts in sorted(record_causes.items()):
        cap = json.loads((CAPABILITIES / filename).read_text())
        records.append({"file": filename, "id": cap.get("id"),
                        "parent": cap.get("parent"), "kind": cap.get("kind"),
                        "scope": cap.get("scope"), "status": cap.get("status"),
                        "findingCount": sum(counts.values()),
                        "causes": dict(sorted(counts.items()))})

    summary = collections.Counter(d["cause"] for d in diagnostics)
    output = {
        "schemaVersion": 1,
        "validator": "check-ledger.go -require-verified -require-invariants",
        "validatorExitCode": result.returncode,
        "diagnosticCount": len(diagnostics),
        "distinctCapabilitiesWithFindings": len(records),
        "capabilityStatusNotVerified": sum(r["causes"].get("unverified_capability_status", 0) > 0 for r in records),
        "causeCounts": dict(sorted(summary.items())),
        "parentDiagnosticCounts": dict(sorted(parent_counts.items())),
        "records": records,
        "diagnostics": diagnostics,
        "interpretation": {
            "validatorFindingsAreNotBehavioralMismatches": True,
            "behavioralMismatchCount": None,
            "behavioralMismatchNote": "Requires differential evidence; do not infer from missing ledger evidence.",
        },
    }
    json.dump(output, sys.stdout, indent=2)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
