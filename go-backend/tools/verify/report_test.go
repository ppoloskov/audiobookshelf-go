package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

func TestRunGoTestPreservesNonzeroExit(t *testing.T) {
	for _, code := range []int{1, 2} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "go-backend"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "go"), []byte(fmt.Sprintf("#!/bin/sh\nexit %d\n", code)), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", root)
			err := runGoTest(context.Background(), config{root: root, parallel: 1}, filepath.Join(root, "events.jsonl"), nil, allPackets)
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != code {
				t.Fatalf("exit %d was discarded: %v", code, err)
			}
			if ordinaryTestFailure(context.Background(), err) != (code == 1) {
				t.Fatalf("exit %d classified as ordinary test failure incorrectly", code)
			}
			cancelled, cancel := context.WithCancel(context.Background())
			cancel()
			if ordinaryTestFailure(cancelled, err) {
				t.Fatal("cancelled test run classified as ordinary failure")
			}
		})
	}
}

func TestNonzeroExitCannotBecomePassingTarget(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"go-backend/internal/contracttest/testdata/install", "server"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"index.js", "package.json"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	plan := twoScenarios()[:1]
	output := wholePackage(t, plan, nil).String()
	// A subprocess may print a complete passing stream and still exit 1.
	// This fake runner launches no server and exercises the real exit handling.
	script := "#!/bin/sh\n/bin/cat <<'EVENTS'\n" + output + "\nEVENTS\nexit 1\n"
	if err := os.WriteFile(filepath.Join(root, "go"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root)
	result, _, err := runTarget(context.Background(), config{root: root, work: t.TempDir(), parallel: 1}, "upstream", plan, allPackets)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" || len(result.Anomalies) == 0 {
		t.Fatalf("unsuccessful command earned a passing target: %+v", result)
	}
}

const authPackage = packagePrefix + "auth"

// stream builds a `go test -json` stream.
type stream struct {
	t     *testing.T
	lines []string
}

func (s *stream) add(event testEvent) {
	s.t.Helper()
	raw, err := json.Marshal(event)
	if err != nil {
		s.t.Fatal(err)
	}
	s.lines = append(s.lines, string(raw))
}

func (s *stream) raw(line string) { s.lines = append(s.lines, line) }

func (s *stream) String() string { return strings.Join(s.lines, "\n") }

// proofOutput is the log line a passing scenario emits.
func proofOutput(t *testing.T, d harness.Declaration) string {
	t.Helper()
	raw, err := json.Marshal(struct {
		Packet string `json:"packet"`
		harness.Proof
	}{d.Packet, d.Proof})
	if err != nil {
		t.Fatal(err)
	}
	return "    run.go:68: REPLACEMENT_PROOF " + string(raw) + "\n"
}

// passingScenario appends the events of a scenario that passed with a proof.
func (s *stream) passingScenario(pkg string, d harness.Declaration) {
	name := "TestReplacement/" + d.Assertion
	s.add(testEvent{Package: pkg, Action: "run", Test: name})
	s.add(testEvent{Package: pkg, Action: "output", Test: name, Output: proofOutput(s.t, d)})
	s.add(testEvent{Package: pkg, Action: "pass", Test: name})
}

func twoScenarios() []harness.Declaration {
	return []harness.Declaration{declaration("auth", "r", "i", "normal", "stateless", "one"), declaration("auth", "r", "i", "edge", "stateless", "two")}
}

func grade(t *testing.T, s *stream, plan []harness.Declaration) *outcome {
	t.Helper()
	result, err := parseResults(strings.NewReader(s.String()), plan)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func wholePackage(t *testing.T, plan []harness.Declaration, tweak func(*stream)) *stream {
	s := &stream{t: t}
	s.add(testEvent{Package: authPackage, Action: "start"})
	s.add(testEvent{Package: authPackage, Action: "run", Test: "TestReplacement"})
	for _, d := range plan {
		s.passingScenario(authPackage, d)
	}
	if tweak != nil {
		tweak(s)
	}
	s.add(testEvent{Package: authPackage, Action: "pass", Test: "TestReplacement"})
	s.add(testEvent{Package: authPackage, Action: "pass"})
	return s
}

func TestEveryPassingScenarioEarnsAProof(t *testing.T) {
	plan := twoScenarios()
	result := grade(t, wholePackage(t, plan, nil), plan)
	packet := result.Packets["auth"]
	if packet.Declared != 2 || packet.Passed != 2 || packet.Failed != 0 || len(result.Passed) != 2 || len(result.Anomalies) != 0 {
		t.Fatalf("unexpected outcome: %+v %+v", packet, result.Anomalies)
	}
}

func TestFailuresAreCollectedNotAborted(t *testing.T) {
	plan := twoScenarios()
	s := &stream{t: t}
	s.add(testEvent{Package: authPackage, Action: "start"})
	s.add(testEvent{Package: authPackage, Action: "run", Test: "TestReplacement"})
	s.passingScenario(authPackage, plan[0])
	s.add(testEvent{Package: authPackage, Action: "run", Test: "TestReplacement/two"})
	for i := range 100 {
		s.add(testEvent{Package: authPackage, Action: "output", Test: "TestReplacement/two", Output: fmt.Sprintf("line %d\n", i)})
	}
	s.add(testEvent{Package: authPackage, Action: "fail", Test: "TestReplacement/two"})
	s.add(testEvent{Package: authPackage, Action: "fail", Test: "TestReplacement"})
	s.add(testEvent{Package: authPackage, Action: "fail"})
	result := grade(t, s, plan)
	packet := result.Packets["auth"]
	if packet.Passed != 1 || packet.Failed != 1 || len(packet.Failures) != 1 {
		t.Fatalf("outcome: %+v", packet)
	}
	failure := packet.Failures[0]
	if failure.Assertion != "two" || len(failure.Output) != maxFailureLines || failure.Output[len(failure.Output)-1] != "line 99" {
		t.Fatalf("failure output not capped to the last %d lines: %d lines, last %q", maxFailureLines, len(failure.Output), failure.Output[len(failure.Output)-1])
	}
	if len(result.Anomalies) != 0 {
		t.Fatalf("a failing scenario is not an anomaly: %v", result.Anomalies)
	}
}

func TestIncompleteRunsFailClosed(t *testing.T) {
	plan := twoScenarios()
	for name, tc := range map[string]struct {
		build func() *stream
		want  string // text of the first scenario's failure output
	}{
		"empty stream": {func() *stream { return &stream{t: t} }, "package produced no test events"},
		"build failure": {func() *stream {
			s := &stream{t: t}
			s.add(testEvent{Package: authPackage, Action: "output", Output: "FAIL\t" + authPackage + " [build failed]\n"})
			s.add(testEvent{Package: authPackage, Action: "fail"})
			return s
		}, "package failed before the scenario finished"},
		"panic mid-run": {func() *stream {
			s := &stream{t: t}
			s.add(testEvent{Package: authPackage, Action: "run", Test: "TestReplacement"})
			s.add(testEvent{Package: authPackage, Action: "run", Test: "TestReplacement/one"})
			s.add(testEvent{Package: authPackage, Action: "output", Test: "TestReplacement/one", Output: "panic: boom\n"})
			s.add(testEvent{Package: authPackage, Action: "fail"})
			return s
		}, "panic: boom"},
		"timeout": {func() *stream {
			s := &stream{t: t}
			s.add(testEvent{Package: authPackage, Action: "output", Output: "panic: test timed out after 2h0m0s\n"})
			s.add(testEvent{Package: authPackage, Action: "fail"})
			return s
		}, "panic: test timed out"},
		"scenarios never ran": {func() *stream {
			s := &stream{t: t}
			s.add(testEvent{Package: authPackage, Action: "run", Test: "TestReplacement"})
			s.add(testEvent{Package: authPackage, Action: "pass", Test: "TestReplacement"})
			s.add(testEvent{Package: authPackage, Action: "pass"})
			return s
		}, "scenario did not run"},
	} {
		t.Run(name, func(t *testing.T) {
			result := grade(t, tc.build(), plan)
			packet := result.Packets["auth"]
			if packet.Passed != 0 || packet.Failed != 2 {
				t.Fatalf("outcome: %+v", packet)
			}
			if !strings.Contains(strings.Join(packet.Failures[0].Output, "\n"), tc.want) {
				t.Fatalf("failure output %q lacks %q", packet.Failures[0].Output, tc.want)
			}
		})
	}
}

func TestSkippedScenarioIsNotAProof(t *testing.T) {
	plan := twoScenarios()[:1]
	s := &stream{t: t}
	s.add(testEvent{Package: authPackage, Action: "run", Test: "TestReplacement/one"})
	s.add(testEvent{Package: authPackage, Action: "skip", Test: "TestReplacement/one"})
	if packet := grade(t, s, plan).Packets["auth"]; packet.Passed != 0 || packet.Failed != 1 {
		t.Fatalf("skip earned credit: %+v", packet)
	}
}

func TestProofMustBeValidAndExactlyOnce(t *testing.T) {
	plan := twoScenarios()[:1]
	d := plan[0]
	name := "TestReplacement/one"
	for testName, tc := range map[string]struct {
		events    func(s *stream)
		anomalies bool
	}{
		"pass without proof": {func(s *stream) {
			s.add(testEvent{Package: authPackage, Action: "run", Test: name})
			s.add(testEvent{Package: authPackage, Action: "pass", Test: name})
		}, false},
		"duplicate proof": {func(s *stream) {
			s.add(testEvent{Package: authPackage, Action: "run", Test: name})
			s.add(testEvent{Package: authPackage, Action: "output", Test: name, Output: proofOutput(t, d)})
			s.add(testEvent{Package: authPackage, Action: "output", Test: name, Output: proofOutput(t, d)})
			s.add(testEvent{Package: authPackage, Action: "pass", Test: name})
		}, false},
		"proof from the parent test": {func(s *stream) {
			s.add(testEvent{Package: authPackage, Action: "output", Test: "TestReplacement", Output: proofOutput(t, d)})
			s.add(testEvent{Package: authPackage, Action: "run", Test: name})
			s.add(testEvent{Package: authPackage, Action: "pass", Test: name})
		}, true},
		"proof from another subtest": {func(s *stream) {
			s.add(testEvent{Package: authPackage, Action: "run", Test: name})
			s.add(testEvent{Package: authPackage, Action: "output", Test: "TestReplacement/other", Output: proofOutput(t, d)})
			s.add(testEvent{Package: authPackage, Action: "pass", Test: name})
		}, true},
		"proof from another package": {func(s *stream) {
			s.add(testEvent{Package: authPackage, Action: "run", Test: name})
			s.add(testEvent{Package: packagePrefix + "server", Action: "output", Test: name, Output: proofOutput(t, d)})
			s.add(testEvent{Package: authPackage, Action: "pass", Test: name})
		}, true},
		"proof on the run action": {func(s *stream) {
			s.add(testEvent{Package: authPackage, Action: "run", Test: name, Output: proofOutput(t, d)})
			s.add(testEvent{Package: authPackage, Action: "pass", Test: name})
		}, false},
		"proof for another assertion": {func(s *stream) {
			other := d
			other.Assertion = "two"
			s.add(testEvent{Package: authPackage, Action: "run", Test: name})
			s.add(testEvent{Package: authPackage, Action: "output", Test: name, Output: proofOutput(t, other)})
			s.add(testEvent{Package: authPackage, Action: "pass", Test: name})
		}, true},
		"proof with a different case": {func(s *stream) {
			other := d
			other.Case = "edge"
			s.add(testEvent{Package: authPackage, Action: "run", Test: name})
			s.add(testEvent{Package: authPackage, Action: "output", Test: name, Output: proofOutput(t, other)})
			s.add(testEvent{Package: authPackage, Action: "pass", Test: name})
		}, false},
		"scenario finished twice": {func(s *stream) {
			s.passingScenario(authPackage, d)
			s.add(testEvent{Package: authPackage, Action: "pass", Test: name})
		}, false},
		"malformed proof": {func(s *stream) {
			s.add(testEvent{Package: authPackage, Action: "run", Test: name})
			s.add(testEvent{Package: authPackage, Action: "output", Test: name, Output: "REPLACEMENT_PROOF {oops\n"})
			s.add(testEvent{Package: authPackage, Action: "pass", Test: name})
		}, true},
	} {
		t.Run(testName, func(t *testing.T) {
			s := &stream{t: t}
			s.add(testEvent{Package: authPackage, Action: "start"})
			s.add(testEvent{Package: authPackage, Action: "run", Test: "TestReplacement"})
			tc.events(s)
			s.add(testEvent{Package: authPackage, Action: "pass", Test: "TestReplacement"})
			s.add(testEvent{Package: authPackage, Action: "pass"})
			result := grade(t, s, plan)
			if packet := result.Packets["auth"]; packet.Passed != 0 || packet.Failed != 1 {
				t.Fatalf("invalid proof earned credit: %+v", packet)
			}
			if got := len(result.Anomalies) != 0; got != tc.anomalies {
				t.Fatalf("anomalies = %v, want present=%v", result.Anomalies, tc.anomalies)
			}
		})
	}
}

func TestProofForUndeclaredScenarioAndStrayLinesAreAnomalies(t *testing.T) {
	plan := twoScenarios()[:1]
	extra := declaration("auth", "r", "i", "edge", "stateless", "extra")
	s := wholePackage(t, plan, func(s *stream) { s.passingScenario(authPackage, extra) })
	s.raw("not json at all")
	result := grade(t, s, plan)
	if result.Packets["auth"].Passed != 1 {
		t.Fatalf("declared scenario lost: %+v", result.Packets["auth"])
	}
	text := strings.Join(result.Anomalies, "\n")
	if !strings.Contains(text, "undeclared scenario auth/extra") || !strings.Contains(text, "not json at all") {
		t.Fatalf("anomalies: %v", result.Anomalies)
	}
}

func TestHarnessFailureIsAnAnomaly(t *testing.T) {
	plan := twoScenarios()
	s := wholePackage(t, plan, nil)
	harnessPkg := packagePrefix + "harness"
	s.add(testEvent{Package: harnessPkg, Action: "start"})
	s.add(testEvent{Package: harnessPkg, Action: "run", Test: "TestX"})
	s.add(testEvent{Package: harnessPkg, Action: "output", Test: "TestX", Output: "x_test.go:1: broken\n"})
	s.add(testEvent{Package: harnessPkg, Action: "fail", Test: "TestX"})
	s.add(testEvent{Package: harnessPkg, Action: "fail"})
	result := grade(t, s, plan)
	if len(result.Anomalies) != 1 || !strings.Contains(result.Anomalies[0], "harness failed") || !strings.Contains(result.Anomalies[0], "broken") {
		t.Fatalf("anomalies: %v", result.Anomalies)
	}
}

func TestPackageFailureAfterPassingScenariosIsAnAnomaly(t *testing.T) {
	plan := twoScenarios()
	s := &stream{t: t}
	s.add(testEvent{Package: authPackage, Action: "start"})
	s.add(testEvent{Package: authPackage, Action: "run", Test: "TestReplacement"})
	for _, d := range plan {
		s.passingScenario(authPackage, d)
	}
	s.add(testEvent{Package: authPackage, Action: "output", Test: "TestReplacement", Output: "server.go:1: stop server: boom\n"})
	s.add(testEvent{Package: authPackage, Action: "fail", Test: "TestReplacement"})
	s.add(testEvent{Package: authPackage, Action: "fail"})
	result := grade(t, s, plan)
	if result.Packets["auth"].Passed != 2 || len(result.Anomalies) != 1 {
		t.Fatalf("outcome: %+v %v", result.Packets["auth"], result.Anomalies)
	}
}

func TestParseDeclarationsKeepsEveryDeclaration(t *testing.T) {
	s := &stream{t: t}
	s.add(testEvent{Package: authPackage, Action: "start"})
	s.add(testEvent{Package: authPackage, Action: "run", Test: "TestReplacement"})
	for i := range 150 {
		d := harness.Declaration{Packet: "auth", Proof: harness.Proof{Requirement: "r", Invariant: "i", Case: "normal", State: "stateless", Assertion: fmt.Sprintf("a%d", i)}, Sources: []string{"server/x.js:1"}, Claim: "c"}
		raw, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		s.add(testEvent{Package: authPackage, Action: "output", Test: "TestReplacement", Output: "    run.go:1: REPLACEMENT_DECLARE " + string(raw) + "\n"})
	}
	s.add(testEvent{Package: authPackage, Action: "pass", Test: "TestReplacement"})
	s.add(testEvent{Package: authPackage, Action: "pass"})
	plan, err := parseDeclarations(strings.NewReader(s.String()))
	if err != nil || len(plan) != 150 || plan[149].Assertion != "a149" || plan[0].Claim != "c" {
		t.Fatalf("plan of %d: %v", len(plan), err)
	}
}

func TestParseDeclarationsRejectsBrokenPackagesAndForgeries(t *testing.T) {
	declare := func(packet string) string {
		raw, err := json.Marshal(harness.Declaration{Packet: packet, Proof: harness.Proof{Assertion: "x"}})
		if err != nil {
			t.Fatal(err)
		}
		return "REPLACEMENT_DECLARE " + string(raw) + "\n"
	}
	for name, build := range map[string]func(*stream){
		"failed package": func(s *stream) { s.add(testEvent{Package: authPackage, Action: "fail"}) },
		"other packet's name": func(s *stream) {
			s.add(testEvent{Package: authPackage, Action: "output", Test: "TestReplacement", Output: declare("server")})
		},
		"declared outside TestReplacement": func(s *stream) {
			s.add(testEvent{Package: authPackage, Action: "output", Test: "TestOther", Output: declare("auth")})
		},
		"declared by harness": func(s *stream) {
			s.add(testEvent{Package: packagePrefix + "harness", Action: "output", Test: "TestReplacement", Output: declare("harness")})
		},
		"malformed": func(s *stream) {
			s.add(testEvent{Package: authPackage, Action: "output", Test: "TestReplacement", Output: "REPLACEMENT_DECLARE {\n"})
		},
		"stray output": func(s *stream) { s.raw("# build failed") },
	} {
		t.Run(name, func(t *testing.T) {
			s := &stream{t: t}
			build(s)
			if _, err := parseDeclarations(strings.NewReader(s.String())); err == nil {
				t.Fatal("broken plan accepted")
			}
		})
	}
}

func TestPassingProofRequiresCompleteActiveLifecycle(t *testing.T) {
	plan := twoScenarios()[:1]
	for name, change := range map[string]func([]string) []string{
		"missing subtest run":   func(lines []string) []string { return append(lines[:2], lines[3:]...) },
		"missing package start": func(lines []string) []string { return lines[1:] },
		"late package start": func(lines []string) []string {
			return []string{lines[1], lines[2], lines[3], lines[0], lines[4], lines[5], lines[6]}
		},
		"missing parent run": func(lines []string) []string { return append(lines[:1], lines[2:]...) },
		"proof after pass":   func(lines []string) []string { lines[3], lines[4] = lines[4], lines[3]; return lines },
		"truncated package":  func(lines []string) []string { return lines[:5] },
		"skipped package": func(lines []string) []string {
			lines[6] = strings.Replace(lines[6], `"pass"`, `"skip"`, 1)
			return lines
		},
		"package finished twice": func(lines []string) []string { return append(lines, lines[6]) },
		"extra nonmatching proof": func(lines []string) []string {
			other := plan[0]
			other.Case = "error"
			raw, err := json.Marshal(testEvent{Package: authPackage, Action: "output", Test: "TestReplacement/one", Output: proofOutput(t, other)})
			if err != nil {
				t.Fatal(err)
			}
			return append(lines[:4], append([]string{string(raw)}, lines[4:]...)...)
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := wholePackage(t, plan, nil)
			s.lines = change(s.lines)
			result := grade(t, s, plan)
			if result.Packets["auth"].Passed != 0 {
				t.Fatalf("invalid lifecycle earned credit: %+v", result)
			}
		})
	}
}

func TestPlanRequiresSuccessfulPackageAndDeclaringTest(t *testing.T) {
	d := twoScenarios()[0]
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"", "skip", "fail"} {
		t.Run("package-"+action, func(t *testing.T) {
			s := &stream{t: t}
			s.add(testEvent{Package: authPackage, Action: "start"})
			s.add(testEvent{Package: authPackage, Action: "run", Test: "TestReplacement"})
			s.add(testEvent{Package: authPackage, Action: "output", Test: "TestReplacement", Output: "REPLACEMENT_DECLARE " + string(raw)})
			s.add(testEvent{Package: authPackage, Action: "pass", Test: "TestReplacement"})
			if action != "" {
				s.add(testEvent{Package: authPackage, Action: action})
			}
			if _, err := parseDeclarations(strings.NewReader(s.String())); err == nil {
				t.Fatal("incomplete or unsuccessful plan accepted")
			}
		})
	}
	if _, err := parseDeclarations(strings.NewReader("")); err == nil {
		t.Fatal("empty plan accepted")
	}
}
