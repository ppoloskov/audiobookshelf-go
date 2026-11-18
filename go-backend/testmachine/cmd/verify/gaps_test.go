package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

func declaration(packet, requirement, invariantID, caseName, state, assertion string) harness.Declaration {
	return harness.Declaration{Packet: packet, Proof: harness.Proof{Requirement: requirement, Invariant: invariantID, Case: caseName, State: state, Assertion: assertion}}
}

func hasGap(gaps []string, text string) bool {
	for _, gap := range gaps {
		if strings.Contains(gap, text) {
			return true
		}
	}
	return false
}

func TestCoverageNeedsDeclaredOrExplainedFacets(t *testing.T) {
	record := capability{
		ID: "r", Scope: "in", Stateful: true,
		Cases:          map[string]string{"normal": "unverified", "error": "unverified", "edge": "not_applicable"},
		StateScenarios: map[string]string{"initial": "unverified", "repeat": "unverified", "changed": "not_applicable", "force": "not_applicable", "failure": "unverified"},
		Invariants:     []invariant{{ID: "i"}, {ID: "unbound"}},
		Exceptions:     []string{"force is not applicable: no force mode."},
	}
	plan := []harness.Declaration{declaration("p", "r", "i", "normal", "initial", "a")}
	gaps := coverageGaps([]capability{record}, plan)
	for _, want := range []string{
		"r/unbound: no declared scenario",
		"no declared error scenario",
		"edge is not_applicable without an exception",
		"no declared repeat scenario",
		"changed is not_applicable without an exception",
		"no declared failure scenario",
	} {
		if !hasGap(gaps, want) {
			t.Errorf("missing gap %q in %v", want, gaps)
		}
	}
	if hasGap(gaps, "force") || hasGap(gaps, "no declared normal") || hasGap(gaps, "no declared initial") {
		t.Errorf("covered or explained facets reported as gaps: %v", gaps)
	}
}

func TestVerifiedFlagsAreOutputsNotInputs(t *testing.T) {
	// No record needs status "verified"; a record with every facet declared has no gaps.
	record := capability{ID: "r", Scope: "in", Cases: map[string]string{"normal": "unverified", "error": "unverified", "edge": "unverified"}, Invariants: []invariant{{ID: "i"}}}
	var plan []harness.Declaration
	for _, name := range []string{"normal", "error", "edge"} {
		plan = append(plan, declaration("p", "r", "i", name, "stateless", name))
	}
	if gaps := coverageGaps([]capability{record}, plan); len(gaps) != 0 {
		t.Fatalf("unexpected gaps: %v", gaps)
	}
}

func TestNotApplicableNeedsAnActualExplanation(t *testing.T) {
	record := capability{ID: "r", Cases: map[string]string{"error": "not_applicable"}, Exceptions: []string{"error is not applicable:  "}}
	if gaps := facetGap(record, "error", false); !hasGap(gaps, "without an exception") {
		t.Fatalf("empty not-applicable explanation accepted: %v", gaps)
	}
}

func TestPacketReceiptCannotCertifyInventoryDiscovery(t *testing.T) {
	f := newReviewFixture(t)
	if gaps := f.gaps(t); len(gaps) != 0 {
		t.Fatalf("expected a fully reviewed packet: %v", gaps)
	}
	current := inputs{Source: "pinned", Requirements: "reviewed", Scenarios: "scenarios"}
	if inventoryReviewGap(f.root, current) == "" {
		t.Fatal("review of a listed packet certified discovery of the whole contract")
	}
	receipt := inventoryReview{Reviewer: "independent reviewer", Pin: sourcePin, Inputs: current, Reconciliation: "Checked client requests, persisted formats, and lifecycle against source.", OpenFindings: []string{}}
	save := func() {
		t.Helper()
		raw, err := json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.root, inventoryReviewPath), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save()
	if gap := inventoryReviewGap(f.root, current); gap != "" {
		t.Fatal(gap)
	}
	receipt.OpenFindings = []string{"unmapped protocol"}
	save()
	if inventoryReviewGap(f.root, current) == "" {
		t.Fatal("accepted open source-to-inventory finding")
	}
	receipt.OpenFindings = nil
	save()
	if inventoryReviewGap(f.root, current) == "" {
		t.Fatal("accepted missing finding disposition")
	}
	receipt.OpenFindings = []string{}
	save()
	current.Requirements = "changed"
	if inventoryReviewGap(f.root, current) == "" {
		t.Fatal("accepted inventory review for different requirements")
	}
}

// reviewFixture builds a minimal repository tree with one packet and a receipt.
type reviewFixture struct {
	root    string
	records []capability
	ids     []string
	plan    []harness.Declaration
	receipt reviewReceipt
}

func newReviewFixture(t *testing.T) *reviewFixture {
	t.Helper()
	root := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("server/Auth.js", "x")
	write("go-backend/testmachine/scenarios/p/packet.go", "package p")
	for _, path := range []string{
		"go-backend/testmachine/scenarios/harness/run.go",
		"go-backend/internal/contracttest/testdata/install/ids.json",
		"go-backend/testmachine/contracts/clients/official-app.md", "go-backend/testmachine/contracts/client-usage.json", "go-backend/testmachine/contracts/mobile-client-audit.md",
		"go-backend/testmachine/register/CONTRACT.md", "go-backend/ARCHITECTURE.md",
		"go-backend/go.mod", "go-backend/go.sum",
	} {
		write(path, "reviewed input")
	}
	recordPath := write("go-backend/testmachine/register/capabilities/cap-r.json", "{}")
	f := &reviewFixture{
		root: root,
		records: []capability{{ID: "r", Scope: "in", Path: recordPath, Cases: map[string]string{"normal": "unverified", "error": "not_applicable", "edge": "unverified"},
			Invariants: []invariant{{ID: "i"}}, Exceptions: []string{"error is not applicable: no error path"}}},
		ids:  []string{"r"},
		plan: []harness.Declaration{declaration("p", "r", "i", "normal", "stateless", "a"), declaration("p", "r", "i", "edge", "stateless", "b")},
	}
	scenarios, err := scenarioDigest(root, "p")
	if err != nil {
		t.Fatal(err)
	}
	requirements, err := requirementsDigest(f.records, f.ids)
	if err != nil {
		t.Fatal(err)
	}
	f.receipt = reviewReceipt{
		Packet: "p", Reviewer: "independent", ScenarioDigest: scenarios, RequirementsDigest: requirements, OpenFindings: []json.RawMessage{},
		Required: []requiredTuple{
			{Requirement: "r", Invariant: "i", Case: "normal", State: "stateless", Assertion: "a", Sources: []string{"server/Auth.js:1"}, Rationale: "why"},
			{Requirement: "r", Invariant: "i", Case: "edge", State: "stateless", Assertion: "b", Sources: []string{"server/Auth.js:2"}, Rationale: "why"},
		},
		NotApplicable: []reviewNotApplicable{{Requirement: "r", Facet: "error", Reason: "no error path"}},
	}
	return f
}

func TestReviewDigestBindsSharedInputs(t *testing.T) {
	for _, path := range []string{
		"go-backend/testmachine/scenarios/harness/run.go",
		"go-backend/internal/contracttest/testdata/install/ids.json",
		"go-backend/testmachine/contracts/clients/official-app.md", "go-backend/testmachine/contracts/client-usage.json", "go-backend/testmachine/contracts/mobile-client-audit.md",
		"go-backend/testmachine/register/CONTRACT.md", "go-backend/ARCHITECTURE.md",
		"go-backend/go.mod", "go-backend/go.sum",
	} {
		t.Run(path, func(t *testing.T) {
			f := newReviewFixture(t)
			if err := os.WriteFile(filepath.Join(f.root, path), []byte("changed after review"), 0600); err != nil {
				t.Fatal(err)
			}
			if gaps := f.gaps(t); !hasGap(gaps, "scenario_digest") {
				t.Fatalf("changed reviewed input retained receipt: %v", gaps)
			}
		})
	}
}

func TestEveryRequirementNeedsExactlyOnePacketOwner(t *testing.T) {
	f := newReviewFixture(t)
	for name, packets := range map[string]map[string][]string{
		"unowned":   {},
		"duplicate": {"p": {"r"}, "q": {"r"}},
		"unknown":   {"p": {"r", "invented"}},
	} {
		t.Run(name, func(t *testing.T) {
			gaps, _ := reviewGaps(f.root, f.records, packets, f.plan)
			want := map[string]string{"unowned": "no packet owner", "duplicate": "duplicate ownership", "unknown": "unknown or out-of-scope"}[name]
			if !hasGap(gaps, want) {
				t.Fatalf("ownership problem missing %q: %v", want, gaps)
			}
		})
	}
}

func TestPacketReviewMustDisposeEveryOwnedInvariant(t *testing.T) {
	f := newReviewFixture(t)
	f.records[0].Invariants = append(f.records[0].Invariants, invariant{ID: "unreviewed"})
	if gaps := f.gaps(t); !hasGap(gaps, "review incomplete: r/unreviewed") {
		t.Fatalf("receipt accepted with omitted canonical invariant: %v", gaps)
	}
}

func (f *reviewFixture) gaps(t *testing.T) []string {
	t.Helper()
	raw, err := json.Marshal(f.receipt)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.root, "go-backend/testmachine/contracts/verification-review/p.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return packetReviewGaps(f.root, "p", f.records, f.ids, f.plan)
}

func TestReviewReceiptAcceptedAndRejected(t *testing.T) {
	if gaps := newReviewFixture(t).gaps(t); len(gaps) != 0 {
		t.Fatalf("valid receipt rejected: %v", gaps)
	}
	if gaps := packetReviewGaps(t.TempDir(), "p", nil, nil, nil); !hasGap(gaps, "receipt is absent") {
		t.Fatalf("absent receipt accepted: %v", gaps)
	}
	for name, tc := range map[string]struct {
		change func(*reviewFixture)
		want   string
	}{
		"no reviewer":        {func(f *reviewFixture) { f.receipt.Reviewer = " " }, "no reviewer"},
		"missing findings":   {func(f *reviewFixture) { f.receipt.OpenFindings = nil }, "open_findings"},
		"stale scenarios":    {func(f *reviewFixture) { f.receipt.ScenarioDigest = "0" }, "scenario_digest"},
		"stale requirements": {func(f *reviewFixture) { f.receipt.RequirementsDigest = "0" }, "requirements_digest"},
		"open finding":       {func(f *reviewFixture) { f.receipt.OpenFindings = []json.RawMessage{json.RawMessage(`"x"`)} }, "open findings"},
		"no rationale":       {func(f *reviewFixture) { f.receipt.Required[0].Rationale = "" }, "lacks sources or rationale"},
		"no sources":         {func(f *reviewFixture) { f.receipt.Required[0].Sources = nil }, "lacks sources or rationale"},
		"bad provenance":     {func(f *reviewFixture) { f.receipt.Required[0].Sources = []string{"README.md:1"} }, "provenance"},
		"missing source":     {func(f *reviewFixture) { f.receipt.Required[0].Sources = []string{"server/Gone.js:1"} }, "missing"},
		"unreviewed":         {func(f *reviewFixture) { f.receipt.Required = f.receipt.Required[:1] }, "scenario not reviewed"},
		"invented tuple": {func(f *reviewFixture) {
			f.receipt.Required[1].Case = "normal"
		}, "no exactly matching declared scenario"},
		"reused assertion":   {func(f *reviewFixture) { f.receipt.Required[1].Assertion = "a" }, "reused assertion"},
		"missing N/A":        {func(f *reviewFixture) { f.receipt.NotApplicable = nil }, "not_applicable error"},
		"N/A without reason": {func(f *reviewFixture) { f.receipt.NotApplicable[0].Reason = "" }, "not_applicable error"},
		"wrong packet":       {func(f *reviewFixture) { f.receipt.Packet = "q" }, "names packet"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newReviewFixture(t)
			tc.change(f)
			if gaps := f.gaps(t); !hasGap(gaps, tc.want) {
				t.Fatalf("want a gap containing %q, got %v", tc.want, gaps)
			}
		})
	}
}
