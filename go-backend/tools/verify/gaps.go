package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"audiobookshelf-compat/tests/replacement/harness"
)

type invariant struct {
	ID string `json:"id"`
}

// capability is the part of a canonical requirement record the machine reads.
// Status flags in the records are outputs of a passing run, never inputs here.
type capability struct {
	ID             string            `json:"id"`
	Scope          string            `json:"scope"`
	Cases          map[string]string `json:"cases"`
	Stateful       bool              `json:"stateful"`
	StateScenarios map[string]string `json:"state_scenarios"`
	Invariants     []invariant       `json:"invariants"`
	Exceptions     []string          `json:"exceptions"`
	Path           string            `json:"-"` // file the record was loaded from
}

var (
	caseFacets  = []string{"normal", "error", "edge"}
	stateFacets = []string{"initial", "repeat", "changed", "force", "failure"}
)

// facets lists the case and state names a requirement must cover.
func (c capability) facets() (cases, states []string) {
	if c.Stateful {
		return caseFacets, stateFacets
	}
	return caseFacets, nil
}

// notApplicable reports whether the record marks the facet not_applicable.
func (c capability) notApplicable(facet string) bool {
	return c.Cases[facet] == "not_applicable" || c.StateScenarios[facet] == "not_applicable"
}

// hasReason reports whether an exception explains why the facet is not applicable.
func (c capability) hasReason(facet string) bool {
	return slices.ContainsFunc(c.Exceptions, func(text string) bool {
		reason, ok := strings.CutPrefix(text, facet+" is not applicable:")
		return ok && strings.TrimSpace(reason) != ""
	})
}

// coverageGaps lists what the declared plan does not cover: every in-scope
// invariant needs a scenario, and every case and (for stateful requirements)
// state needs a scenario or an explained not_applicable.
func coverageGaps(records []capability, plan []harness.Declaration) []string {
	bound := make(map[string]bool)
	for _, d := range plan {
		bound[d.Requirement+"/"+d.Invariant] = true
	}
	var gaps []string
	for _, record := range records {
		if record.Scope != "in" {
			continue
		}
		for _, inv := range record.Invariants {
			if key := record.ID + "/" + inv.ID; !bound[key] {
				gaps = append(gaps, key+": no declared scenario")
			}
		}
		cases, states := record.facets()
		for _, facet := range cases {
			gaps = append(gaps, facetGap(record, facet, hasFacet(plan, record.ID, facet, ""))...)
		}
		for _, facet := range states {
			gaps = append(gaps, facetGap(record, facet, hasFacet(plan, record.ID, "", facet))...)
		}
	}
	return gaps
}

func facetGap(record capability, facet string, declared bool) []string {
	switch {
	case declared:
		return nil
	case !record.notApplicable(facet):
		return []string{record.ID + ": no declared " + facet + " scenario"}
	case !record.hasReason(facet):
		return []string{fmt.Sprintf("%s: %s is not_applicable without an exception starting %q", record.ID, facet, facet+" is not applicable:")}
	}
	return nil
}

// hasFacet is a minimum inventory check, never proof of a case/state combination;
// the exact combinations are the packet reviewer's required tuples.
func hasFacet(plan []harness.Declaration, requirement, caseName, state string) bool {
	return slices.ContainsFunc(plan, func(d harness.Declaration) bool {
		return d.Requirement == requirement && (caseName == "" || d.Case == caseName) && (state == "" || d.State == state)
	})
}

// requiredTuple records a source-derived obligation and the specific scenario
// an independent reviewer inspected for it.
type requiredTuple struct {
	Requirement string   `json:"requirement"`
	Invariant   string   `json:"invariant"`
	Case        string   `json:"case"`
	State       string   `json:"state"`
	Assertion   string   `json:"assertion"`
	Sources     []string `json:"sources"`
	Rationale   string   `json:"rationale"`
}

// tupleKey is the exact binding of a required tuple to a declared scenario.
type tupleKey struct{ requirement, invariant, caseName, state, assertion string }

func (t requiredTuple) key() tupleKey {
	return tupleKey{t.Requirement, t.Invariant, t.Case, t.State, t.Assertion}
}

// reviewNotApplicable is one facet the reviewer agreed is not applicable.
type reviewNotApplicable struct {
	Requirement string `json:"requirement"`
	Facet       string `json:"facet"`
	Reason      string `json:"reason"`
}

// reviewReceipt is go-backend/contracts/verification-review/<packet>.json,
// written by an independent reviewer. A passing run never produces it.
type reviewReceipt struct {
	Packet             string                `json:"packet"`
	Reviewer           string                `json:"reviewer"`
	ScenarioDigest     string                `json:"scenario_digest"`
	RequirementsDigest string                `json:"requirements_digest"`
	Required           []requiredTuple       `json:"required_tuples"`
	NotApplicable      []reviewNotApplicable `json:"not_applicable"`
	OpenFindings       []json.RawMessage     `json:"open_findings"`
}

const inventoryReviewPath = "go-backend/contracts/verification-inventory-review.json"

// inventoryReview records the independent cross-packet source and client audit.
// A complete set of packet receipts only accounts for requirements already listed.
type inventoryReview struct {
	Reviewer       string   `json:"reviewer"`
	Pin            string   `json:"source_pin"`
	Inputs         inputs   `json:"inputs"`
	Reconciliation string   `json:"reconciliation"`
	OpenFindings   []string `json:"open_findings"`
}

func inventoryReviewGap(root string, current inputs) string {
	raw, err := readMetadata(filepath.Join(root, inventoryReviewPath))
	if err != nil {
		return "independent whole-contract inventory review unavailable: " + inventoryReviewPath
	}
	var review inventoryReview
	if json.Unmarshal(raw, &review) != nil || review.Pin != sourcePin || review.Inputs != current ||
		strings.TrimSpace(review.Reviewer) == "" || strings.TrimSpace(review.Reconciliation) == "" ||
		review.OpenFindings == nil || len(review.OpenFindings) != 0 {
		return "independent whole-contract inventory review is stale, incomplete or has open findings"
	}
	return ""
}

// scenarioDigest binds review to the packet, shared harness, fixture and owner
// contracts. The source pin binds the upstream/client tree, which inputDigests
// independently requires to match that exact pin before certification.
func scenarioDigest(root, packet string) (string, error) {
	digest, err := digestPaths(root,
		"go-backend/tests/replacement/"+packet,
		"go-backend/tests/replacement/harness",
		"go-backend/internal/contracttest/testdata/install",
		"go-backend/contracts/clients", "go-backend/contracts/client-usage.json", "go-backend/contracts/mobile-client-audit.md",
		"docs/agent-work/CONTRACT.md", "go-backend/ARCHITECTURE.md",
		"go-backend/go.mod", "go-backend/go.sum")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(sourcePin+"\x00"+digest))), nil
}

// requirementsDigest hashes the capability files of the packet's requirements
// in sorted order of file name.
func requirementsDigest(records []capability, ids []string) (string, error) {
	byID := make(map[string]capability, len(records))
	for _, record := range records {
		byID[record.ID] = record
	}
	var files []string
	for _, id := range ids {
		record, ok := byID[id]
		if !ok {
			return "", fmt.Errorf("packet requirement %s has no capability record", id)
		}
		files = append(files, filepath.Base(record.Path))
	}
	slices.Sort(files)
	if len(files) == 0 {
		return "", fmt.Errorf("packet owns no requirements")
	}
	return digestPaths(filepath.Dir(byID[ids[0]].Path), files...)
}

// reviewGaps checks every packet's independent review receipt.
func reviewGaps(root string, records []capability, packets map[string][]string, plan []harness.Declaration) (gaps []string, accepted int) {
	owners := make(map[string]string)
	known := make(map[string]bool)
	for _, record := range records {
		known[record.ID] = record.Scope == "in"
	}
	for packet, ids := range packets {
		for _, id := range ids {
			if !known[id] {
				gaps = append(gaps, packet+": owns unknown or out-of-scope requirement "+id)
			}
			if prior, ok := owners[id]; ok {
				gaps = append(gaps, fmt.Sprintf("%s: requirement %s has duplicate ownership by %s", packet, id, prior))
			}
			owners[id] = packet
		}
	}
	for _, record := range records {
		if record.Scope == "in" && owners[record.ID] == "" {
			gaps = append(gaps, record.ID+": in-scope requirement has no packet owner")
		}
	}
	names := make([]string, 0, len(packets))
	for name := range packets {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		found := packetReviewGaps(root, name, records, packets[name], plan)
		if len(found) == 0 {
			accepted++
		}
		gaps = append(gaps, found...)
	}
	return gaps, accepted
}

func packetReviewGaps(root, packet string, records []capability, ids []string, plan []harness.Declaration) []string {
	raw, err := readMetadata(filepath.Join(root, "go-backend/contracts/verification-review", packet+".json"))
	if err != nil {
		return []string{packet + ": independent review receipt is absent"}
	}
	var review reviewReceipt
	if err := json.Unmarshal(raw, &review); err != nil {
		return []string{packet + ": review receipt is not valid: " + err.Error()}
	}
	var gaps []string
	add := func(format string, args ...any) { gaps = append(gaps, packet+": "+fmt.Sprintf(format, args...)) }
	if review.Packet != packet {
		add("review receipt names packet %q", review.Packet)
	}
	if strings.TrimSpace(review.Reviewer) == "" {
		add("review receipt has no reviewer")
	}
	if current, err := scenarioDigest(root, packet); err != nil || current != review.ScenarioDigest {
		add("review receipt is stale: scenario_digest does not match the packet directory")
	}
	if current, err := requirementsDigest(records, ids); err != nil || current != review.RequirementsDigest {
		add("review receipt is stale: requirements_digest does not match the packet's capability records")
	}
	if review.OpenFindings == nil {
		add("review receipt must explicitly record open_findings")
	} else if len(review.OpenFindings) != 0 {
		add("review receipt has %d open findings", len(review.OpenFindings))
	}
	declared := make(map[tupleKey]bool)
	for _, d := range plan {
		if d.Packet == packet {
			declared[tupleKey{d.Requirement, d.Invariant, d.Case, d.State, d.Assertion}] = true
		}
	}
	reviewed := make(map[tupleKey]bool)
	assertions := make(map[string]bool)
	for _, tuple := range review.Required {
		where := tuple.Requirement + "/" + tuple.Invariant + "/" + tuple.Assertion
		if len(tuple.Sources) == 0 || strings.TrimSpace(tuple.Rationale) == "" {
			add("%s: required tuple lacks sources or rationale", where)
		}
		for _, source := range tuple.Sources {
			if problem := sourceProblem(root, source); problem != "" {
				add("%s: %s", where, problem)
			}
		}
		if assertions[tuple.Assertion] {
			add("%s: review reused assertion %s", where, tuple.Assertion)
		}
		assertions[tuple.Assertion] = true
		key := tuple.key()
		if !declared[key] {
			add("%s: required tuple case=%s state=%s has no exactly matching declared scenario", where, tuple.Case, tuple.State)
		}
		reviewed[key] = true
	}
	for tuple := range declared {
		if !reviewed[tuple] {
			add("%s: scenario not reviewed", tuple.assertion)
		}
	}
	byID := make(map[string]capability, len(records))
	for _, record := range records {
		byID[record.ID] = record
	}
	for _, id := range ids {
		record, ok := byID[id]
		if !ok {
			add("packets.json lists unknown requirement %s", id)
			continue
		}
		var reviewedPlan []harness.Declaration
		for _, tuple := range review.Required {
			if tuple.Requirement == id {
				reviewedPlan = append(reviewedPlan, harness.Declaration{Proof: harness.Proof{Requirement: tuple.Requirement, Invariant: tuple.Invariant, Case: tuple.Case, State: tuple.State, Assertion: tuple.Assertion}})
			}
		}
		for _, problem := range coverageGaps([]capability{record}, reviewedPlan) {
			add("review incomplete: %s", problem)
		}
		cases, states := record.facets()
		for _, facet := range append(slices.Clone(cases), states...) {
			if !record.notApplicable(facet) {
				continue
			}
			agreed := slices.ContainsFunc(review.NotApplicable, func(n reviewNotApplicable) bool {
				return n.Requirement == id && n.Facet == facet && strings.TrimSpace(n.Reason) != ""
			})
			if !agreed {
				add("%s: not_applicable %s is not recorded with a reason in the review", id, facet)
			}
		}
	}
	return gaps
}

// sourceProblem returns why a review source is not acceptable provenance, or "".
func sourceProblem(root, source string) string {
	file, _, _ := strings.Cut(source, ":")
	file = filepath.ToSlash(filepath.Clean(file))
	if !strings.HasPrefix(file, "server/") && !strings.HasPrefix(file, "client/") && !strings.HasPrefix(file, "go-backend/contracts/clients/") && file != "index.js" && file != "docs/agent-work/CONTRACT.md" && file != "go-backend/contracts/client-usage.json" && file != "go-backend/contracts/mobile-client-audit.md" {
		return "source " + source + " lacks upstream/client/owner contract provenance"
	}
	if info, err := os.Lstat(filepath.Join(root, file)); err != nil || !info.Mode().IsRegular() {
		return "source " + source + " is missing or is not a regular file"
	}
	return ""
}
