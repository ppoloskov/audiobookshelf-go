// Package harness is the black-box toolkit for replacement scenarios. It starts
// the server under test (pinned upstream or the Go rewrite, chosen by the
// coordinator), talks to it over HTTP and Socket.IO, reads its database, and
// emits the declarations and proofs the coordinator verifies. It imports
// nothing from the application packages.
package harness

import (
	"fmt"
	"regexp"
	"slices"
	"testing"
)

// Proof identifies one executed assertion.
type Proof struct {
	Requirement string `json:"requirement"`
	Invariant   string `json:"invariant"`
	Case        string `json:"case"`
	State       string `json:"state"`
	Assertion   string `json:"assertion"`
}

// Declaration is the plan entry a packet publishes for one scenario
// (REPLACEMENT_DECLARE lines). Its JSON shape is read by testmachine/cmd/verify.
type Declaration struct {
	Packet string `json:"packet"`
	Proof
	Sources []string `json:"sources"`
	Claim   string   `json:"claim"`
}

// Scenario is one black-box assertion about the server.
type Scenario struct {
	Requirement string   // capability id, e.g. "route-r000"
	Invariant   string   // invariant id inside that capability
	Case        string   // "normal" | "error" | "edge"
	State       string   // "stateless" | "initial" | "repeat" | "changed" | "force" | "failure"
	Assertion   string   // unique within the packet, [a-z0-9-]+
	Sources     []string // upstream refs "server/...js:LINE" the expectation was derived from (>=1)
	Claim       string   // one sentence: the observable behaviour asserted
	// Run performs the checks. e is nil when the packet has NoServer.
	// A scenario that skips is a failure; it earns no proof.
	Run func(t *testing.T, e *Env)
}

// Packet is the scenario set of one owner packet (one Go package).
type Packet struct {
	Name      string  // must equal the package directory name and a key of packets.json
	Options   Options // how the packet's shared server is started
	NoServer  bool    // true: no shared server; every scenario starts its own (e is nil)
	Scenarios []Scenario
}

var (
	packetPattern = regexp.MustCompile(`^[a-z0-9]+([_-][a-z0-9]+)*$`)
	namePattern   = regexp.MustCompile(`^[a-z0-9-]+$`)
	caseNames     = []string{"normal", "error", "edge"}
	stateNames    = []string{"stateless", "initial", "repeat", "changed", "force", "failure"}
)

// validate reports the first structural problem of the packet.
func (p Packet) validate() error {
	if !packetPattern.MatchString(p.Name) {
		return fmt.Errorf("packet name %q must match %s", p.Name, packetPattern)
	}
	if len(p.Scenarios) == 0 {
		return fmt.Errorf("packet %s declares no scenarios", p.Name)
	}
	seen := make(map[string]bool)
	for i, s := range p.Scenarios {
		where := fmt.Sprintf("packet %s scenario #%d (%q)", p.Name, i, s.Assertion)
		switch {
		case !namePattern.MatchString(s.Assertion):
			return fmt.Errorf("%s: assertion must match %s", where, namePattern)
		case seen[s.Assertion]:
			return fmt.Errorf("%s: duplicate assertion", where)
		case s.Requirement == "" || s.Invariant == "":
			return fmt.Errorf("%s: requirement and invariant are required", where)
		case !slices.Contains(caseNames, s.Case):
			return fmt.Errorf("%s: case %q is not one of %v", where, s.Case, caseNames)
		case !slices.Contains(stateNames, s.State):
			return fmt.Errorf("%s: state %q is not one of %v", where, s.State, stateNames)
		case len(s.Sources) == 0 || slices.Contains(s.Sources, ""):
			return fmt.Errorf("%s: at least one non-empty source is required", where)
		case s.Claim == "":
			return fmt.Errorf("%s: claim is required", where)
		case s.Run == nil:
			return fmt.Errorf("%s: Run is required", where)
		}
		seen[s.Assertion] = true
	}
	return nil
}

func (s Scenario) proof() Proof {
	return Proof{Requirement: s.Requirement, Invariant: s.Invariant, Case: s.Case, State: s.State, Assertion: s.Assertion}
}
