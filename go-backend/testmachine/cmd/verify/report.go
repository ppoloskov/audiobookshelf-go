package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	packagePrefix   = "audiobookshelf-compat/testmachine/scenarios/"
	harnessPackage  = "harness"
	maxFailureLines = 60
	maxEventBytes   = 1 << 20
)

// testEvent is one line of `go test -json`.
type testEvent struct {
	Package     string
	Action      string
	Test        string
	Output      string
	ImportPath  string
	FailedBuild string
}

// failure is one declared scenario that did not earn a proof.
type failure struct {
	Assertion   string   `json:"assertion"`
	Requirement string   `json:"requirement"`
	Invariant   string   `json:"invariant"`
	Case        string   `json:"case"`
	State       string   `json:"state"`
	Output      []string `json:"output"`
}

// packetResult is the outcome of one packet's declared scenarios.
type packetResult struct {
	Declared int       `json:"declared"`
	Passed   int       `json:"passed"`
	Failed   int       `json:"failed"`
	Failures []failure `json:"failures"`
}

// provenAssertion is a passed scenario in the report.
type provenAssertion struct {
	Packet string `json:"packet"`
	harness.Proof
}

// outcome is everything learned from one `go test -json` stream.
type outcome struct {
	Packets   map[string]*packetResult
	Passed    []provenAssertion
	Anomalies []string // problems not attributable to one scenario; any makes the target fail
}

type proofLine struct {
	packet string
	proof  harness.Proof
	valid  bool // emitted by the matching subtest of the matching package
}

type testState struct {
	started bool
	final   string // last pass/fail/skip action; "" while unfinished
	output  []string
	proofs  []proofLine
	invalid string // protocol problem observed for this test
}

type packageState struct {
	started     bool
	invalid     string
	final       string
	failedBuild string
	output      []string
	declares    []declareLine // REPLACEMENT_DECLARE lines, uncapped
	tests       map[string]*testState
}

type declareLine struct{ test, raw string }

func (p *packageState) test(name string) *testState {
	state := p.tests[name]
	if state == nil {
		state = &testState{}
		p.tests[name] = state
	}
	return state
}

// appendCapped appends lines, keeping only the last maxFailureLines.
func appendCapped(lines []string, text string) []string {
	for line := range strings.SplitSeq(strings.TrimRight(text, "\n"), "\n") {
		lines = append(lines, line)
	}
	if len(lines) > maxFailureLines {
		lines = slices.Clone(lines[len(lines)-maxFailureLines:])
	}
	return lines
}

// packetOf maps a package import path to its packet name; ok is false for
// packages outside testmachine/scenarios or nested deeper.
func packetOf(pkg string) (string, bool) {
	name, found := strings.CutPrefix(pkg, packagePrefix)
	return name, found && name != "" && !strings.Contains(name, "/")
}

// readEvents reads a `go test -json` stream tolerantly: lines that are not
// events are returned as stray text instead of aborting.
func readEvents(reader io.Reader) (packages map[string]*packageState, stray []string, err error) {
	packages = make(map[string]*packageState)
	build := make(map[string][]string) // ImportPath -> build output
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxEventBytes)
	for scanner.Scan() {
		var event testEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			stray = appendCapped(stray, scanner.Text())
			continue
		}
		if event.Action == "build-output" {
			build[event.ImportPath] = appendCapped(build[event.ImportPath], event.Output)
			continue
		}
		if event.Package == "" {
			stray = appendCapped(stray, scanner.Text())
			continue
		}
		pkg := packages[event.Package]
		if pkg == nil {
			pkg = &packageState{tests: make(map[string]*testState)}
			packages[event.Package] = pkg
		}
		if !pkg.started && event.Action != "start" {
			pkg.invalid = "event before package start"
		}
		if event.Test == "" {
			if pkg.final != "" {
				pkg.invalid = "event after package completion"
			}
			switch event.Action {
			case "start":
				if pkg.started {
					pkg.invalid = "package started twice"
				}
				pkg.started = true
			case "output":
				pkg.output = appendCapped(pkg.output, event.Output)
			case "pass", "fail", "skip":
				pkg.final = event.Action
				pkg.failedBuild = event.FailedBuild
			}
			continue
		}
		test := pkg.test(event.Test)
		if pkg.final != "" {
			pkg.invalid = "test event after package completion"
		}
		switch event.Action {
		case "run":
			if test.started || test.final != "" {
				test.invalid = "test started twice or after completion"
			}
			test.started = true
			if strings.HasPrefix(event.Test, "TestReplacement/") {
				parent := pkg.tests["TestReplacement"]
				if parent == nil || !parent.started || parent.final != "" {
					test.invalid = "scenario ran outside active TestReplacement"
				}
			}
		case "pass", "fail", "skip":
			if test.final != "" {
				test.invalid = "test finished twice"
			}
			test.final = event.Action
			for name, child := range pkg.tests {
				if strings.HasPrefix(name, event.Test+"/") && child.started && child.final == "" {
					test.invalid = "test finished before its subtest"
				}
			}
		case "output":
			if !test.started || test.final != "" {
				test.invalid = "output outside active test"
			}
			test.output = appendCapped(test.output, event.Output)
			if _, raw, ok := strings.Cut(event.Output, "REPLACEMENT_PROOF "); ok {
				test.proofs = append(test.proofs, decodeProof(raw, event))
			}
			if _, raw, ok := strings.Cut(event.Output, "REPLACEMENT_DECLARE "); ok {
				pkg.declares = append(pkg.declares, declareLine{event.Test, raw})
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("read go test events: %w", err)
	}
	for _, pkg := range packages {
		if pkg.failedBuild != "" {
			pkg.output = append(build[pkg.failedBuild], pkg.output...)
			pkg.output = pkg.output[max(len(pkg.output)-maxFailureLines, 0):]
		}
	}
	return packages, stray, nil
}

// decodeProof parses a proof line; it is valid only from the matching subtest
// "TestReplacement/<assertion>" of package testmachine/scenarios/<packet>.
func decodeProof(raw string, event testEvent) proofLine {
	var line struct {
		Packet string `json:"packet"`
		harness.Proof
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &line); err != nil {
		return proofLine{}
	}
	packet, ok := packetOf(event.Package)
	return proofLine{packet: line.Packet, proof: line.Proof, valid: ok && packet == line.Packet && event.Action == "output" && event.Test == "TestReplacement/"+line.Assertion}
}

// parseResults grades every declared scenario from a `go test -json` stream.
// A scenario passes only when its subtest TestReplacement/<assertion> of its
// packet's package finished with pass and emitted exactly one valid proof that
// equals the declaration. Everything else is a failure with the scenario's
// output (last 60 lines), never an abort.
func parseResults(reader io.Reader, plan []harness.Declaration) (*outcome, error) {
	packages, stray, err := readEvents(reader)
	if err != nil {
		return nil, err
	}
	result := &outcome{Packets: make(map[string]*packetResult), Passed: []provenAssertion{}}
	if len(packages) == 0 {
		result.Anomalies = append(result.Anomalies, "go test produced no test events")
	}
	for _, line := range stray {
		result.Anomalies = append(result.Anomalies, "unexpected go test output: "+line)
	}
	declared := make(map[string]bool)
	for _, d := range plan {
		declared[d.Packet+"/"+d.Assertion] = true
		packet := result.Packets[d.Packet]
		if packet == nil {
			packet = &packetResult{Failures: []failure{}}
			result.Packets[d.Packet] = packet
		}
		packet.Declared++
		if lines := gradeScenario(packages[packagePrefix+d.Packet], d); lines == nil {
			packet.Passed++
			result.Passed = append(result.Passed, provenAssertion{Packet: d.Packet, Proof: d.Proof})
		} else {
			packet.Failed++
			packet.Failures = append(packet.Failures, failure{Assertion: d.Assertion, Requirement: d.Requirement, Invariant: d.Invariant, Case: d.Case, State: d.State, Output: lines})
		}
	}
	names := make([]string, 0, len(packages))
	for name := range packages {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		pkg := packages[name]
		packet, isPacket := packetOf(name)
		if pkg.invalid != "" || (pkg.final != "fail" && (!pkg.started || pkg.final != "pass")) {
			result.Anomalies = append(result.Anomalies, fmt.Sprintf("package %s has incomplete or invalid lifecycle: %s", name, pkg.invalid))
		}
		if pkg.final == "fail" && !(isPacket && result.Packets[packet] != nil && result.Packets[packet].Failed > 0) {
			result.Anomalies = append(result.Anomalies, fmt.Sprintf("package %s failed outside declared scenarios: %s", name, strings.Join(lastLines(append(slices.Clone(pkg.output), failedTestOutput(pkg)...), 10), " | ")))
		}
		for testName, test := range pkg.tests {
			for _, proof := range test.proofs {
				if !proof.valid {
					result.Anomalies = append(result.Anomalies, fmt.Sprintf("misattributed proof from %s in %s", testName, name))
				} else if !declared[proof.packet+"/"+proof.proof.Assertion] {
					result.Anomalies = append(result.Anomalies, fmt.Sprintf("proof for undeclared scenario %s/%s", proof.packet, proof.proof.Assertion))
				}
			}
		}
	}
	slices.Sort(result.Anomalies)
	return result, nil
}

// failedTestOutput is the output of the package's failed tests.
func failedTestOutput(pkg *packageState) []string {
	var lines []string
	for _, test := range pkg.tests {
		if test.final == "fail" {
			lines = append(lines, test.output...)
		}
	}
	return lines
}

func lastLines(lines []string, n int) []string {
	return lines[max(len(lines)-n, 0):]
}

// gradeScenario returns nil when the declared scenario passed, else the output
// explaining why not (never empty).
func gradeScenario(pkg *packageState, d harness.Declaration) []string {
	if pkg == nil {
		return []string{"package produced no test events"}
	}
	test := pkg.tests["TestReplacement/"+d.Assertion]
	if test == nil || test.final == "" {
		reason := "scenario did not run"
		if pkg.final == "fail" {
			reason = "package failed before the scenario finished"
		}
		lines := []string{reason + ":"}
		if test != nil {
			lines = append(lines, test.output...)
		}
		return lastLines(append(lines, pkg.output...), maxFailureLines)
	}
	if !pkg.started || (pkg.final != "pass" && pkg.final != "fail") || pkg.invalid != "" {
		return append([]string{"package lifecycle incomplete or invalid: " + pkg.invalid}, test.output...)
	}
	parent := pkg.tests["TestReplacement"]
	if parent == nil || !parent.started || (parent.final != "pass" && parent.final != "fail") || parent.invalid != "" {
		return append([]string{"TestReplacement lifecycle incomplete or invalid"}, test.output...)
	}
	switch {
	case test.final == "skip":
		return append([]string{"scenario skipped"}, test.output...)
	case test.final != "pass":
		return nonEmpty(test.output, "scenario failed")
	case test.invalid != "":
		return append([]string{test.invalid}, test.output...)
	case !test.started:
		return append([]string{"scenario passed without a run event"}, test.output...)
	}
	matching := 0
	for _, proof := range test.proofs {
		if proof.valid && proof.packet == d.Packet && proof.proof == d.Proof {
			matching++
		}
	}
	if matching != 1 || len(test.proofs) != 1 {
		return append([]string{fmt.Sprintf("scenario passed but emitted %d REPLACEMENT_PROOF lines (%d matching valid), want exactly 1 matching proof", len(test.proofs), matching)}, test.output...)
	}
	return nil
}

func nonEmpty(lines []string, fallback string) []string {
	if len(lines) == 0 {
		return []string{fallback}
	}
	return lines
}

// parseDeclarations reads the plan from a plan-only `go test -json` stream. A
// package that failed to build or run makes the plan unknowable: that is an
// error naming the packages.
func parseDeclarations(reader io.Reader) ([]harness.Declaration, error) {
	packages, stray, err := readEvents(reader)
	if err != nil {
		return nil, err
	}
	var plan []harness.Declaration
	var problems []string
	names := make([]string, 0, len(packages))
	for name := range packages {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		pkg := packages[name]
		if !pkg.started || pkg.final != "pass" || pkg.invalid != "" {
			problems = append(problems, fmt.Sprintf("package %s failed or incomplete during plan discovery: %s %s", name, pkg.invalid, strings.Join(lastLines(append(slices.Clone(pkg.output), failedTestOutput(pkg)...), 10), " | ")))
		}
		packet, isPacket := packetOf(name)
		for _, line := range pkg.declares {
			declaringTest := pkg.tests[line.test]
			if declaringTest == nil || !declaringTest.started || declaringTest.final != "pass" || declaringTest.invalid != "" {
				problems = append(problems, fmt.Sprintf("declaration outside successful active test in %s", name))
				continue
			}
			var d harness.Declaration
			if err := json.Unmarshal([]byte(strings.TrimSpace(line.raw)), &d); err != nil {
				problems = append(problems, fmt.Sprintf("malformed declaration in %s: %v", name, err))
				continue
			}
			if !isPacket || d.Packet != packet || packet == harnessPackage || line.test != "TestReplacement" {
				problems = append(problems, fmt.Sprintf("declaration of packet %q by %q in package %s", d.Packet, line.test, name))
				continue
			}
			plan = append(plan, d)
		}
	}
	if len(plan) == 0 {
		problems = append(problems, "no scenario declarations discovered")
	}
	for _, line := range stray {
		problems = append(problems, "unexpected go test output: "+line)
	}
	if len(problems) != 0 {
		return nil, fmt.Errorf("plan discovery: %s", strings.Join(problems, "; "))
	}
	return plan, nil
}
