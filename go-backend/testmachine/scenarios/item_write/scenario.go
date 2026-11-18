package item_write

import (
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// scenario builds a harness.Scenario with the shared fields filled in.
func scenario(requirement, invariant, caseName, state, assertion, claim string, sources []string, run func(t *testing.T, e *harness.Env)) harness.Scenario {
	return harness.Scenario{
		Requirement: requirement, Invariant: invariant, Case: caseName, State: state,
		Assertion: assertion, Sources: sources, Claim: claim, Run: run,
	}
}

// srcs is shorthand for a list of upstream references.
func srcs(list ...string) []string { return list }
