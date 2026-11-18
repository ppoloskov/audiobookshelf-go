package harness

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// helperPacket is the packet the Run subprocess helper executes.
func helperPacket(mode string) Packet {
	scenario := Scenario{
		Requirement: "route-r000", Invariant: "INV-X", Case: "normal", State: "stateless", Assertion: "helper-scenario",
		Sources: []string{"server/Auth.js:1"}, Claim: "helper claim",
		Run: func(t *testing.T, _ *Env) {
			switch mode {
			case "fail":
				t.Fatal("deliberate failure")
			case "error":
				t.Error("deliberate non-fatal failure")
			case "skip":
				t.Skip("deliberate skip")
			case "skip-after-error":
				t.Error("failed first")
				t.Skip("then skipped")
			}
		},
	}
	return Packet{Name: "helper", NoServer: mode != "plan", Scenarios: []Scenario{scenario}}
}

// TestRunHelper is the subprocess body for TestRunEmitsProofsOnlyForPasses.
func TestRunHelper(t *testing.T) {
	mode := os.Getenv("HARNESS_RUN_HELPER")
	if mode == "" {
		return
	}
	run(t, helperPacket(mode))
}

func TestRunEmitsProofsOnlyForPasses(t *testing.T) {
	for _, tc := range []struct {
		mode      string
		wantFail  bool
		wantProof bool
	}{
		{"pass", false, true},
		{"fail", true, false},
		{"error", true, false},
		{"skip", true, false},
		{"skip-after-error", true, false},
		{"plan", false, false},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestRunHelper$", "-test.v")
			cmd.Env = append(cleanEnv(), "HARNESS_RUN_HELPER="+tc.mode)
			if tc.mode == "plan" {
				cmd.Env = append(cmd.Env, "REPLACEMENT_PLAN_ONLY=1")
			}
			out, err := cmd.CombinedOutput()
			if failed := err != nil; failed != tc.wantFail {
				t.Fatalf("helper failed=%v, want %v: %v\n%s", failed, tc.wantFail, err, out)
			}
			text := string(out)
			if !strings.Contains(text, `REPLACEMENT_DECLARE {"packet":"helper","requirement":"route-r000","invariant":"INV-X","case":"normal","state":"stateless","assertion":"helper-scenario","sources":["server/Auth.js:1"],"claim":"helper claim"}`) {
				t.Fatalf("declaration missing or malformed:\n%s", text)
			}
			if got := strings.Contains(text, `REPLACEMENT_PROOF {"packet":"helper","requirement":"route-r000","invariant":"INV-X","case":"normal","state":"stateless","assertion":"helper-scenario"}`); got != tc.wantProof {
				t.Fatalf("proof emitted=%v, want %v:\n%s", got, tc.wantProof, text)
			}
		})
	}
}

// cleanEnv is the parent environment without any REPLACEMENT_ variable.
func cleanEnv() []string {
	var env []string
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "REPLACEMENT_") {
			env = append(env, entry)
		}
	}
	return env
}

func TestPacketValidation(t *testing.T) {
	good := helperPacket("pass")
	mutate := func(change func(*Packet)) Packet {
		p := good
		p.Scenarios = append([]Scenario(nil), good.Scenarios...)
		change(&p)
		return p
	}
	if err := good.validate(); err != nil {
		t.Fatal(err)
	}
	if err := mutate(func(p *Packet) { p.Name = "library_manage" }).validate(); err != nil {
		t.Fatalf("canonical packet name rejected: %v", err)
	}
	for name, p := range map[string]Packet{
		"bad packet name":   mutate(func(p *Packet) { p.Name = "Bad_Name" }),
		"no scenarios":      mutate(func(p *Packet) { p.Scenarios = nil }),
		"bad assertion":     mutate(func(p *Packet) { p.Scenarios[0].Assertion = "Has Space" }),
		"empty requirement": mutate(func(p *Packet) { p.Scenarios[0].Requirement = "" }),
		"empty invariant":   mutate(func(p *Packet) { p.Scenarios[0].Invariant = "" }),
		"unknown case":      mutate(func(p *Packet) { p.Scenarios[0].Case = "happy" }),
		"unknown state":     mutate(func(p *Packet) { p.Scenarios[0].State = "later" }),
		"no sources":        mutate(func(p *Packet) { p.Scenarios[0].Sources = nil }),
		"empty source":      mutate(func(p *Packet) { p.Scenarios[0].Sources = []string{""} }),
		"no claim":          mutate(func(p *Packet) { p.Scenarios[0].Claim = "" }),
		"nil run":           mutate(func(p *Packet) { p.Scenarios[0].Run = nil }),
		"duplicate assertion": mutate(func(p *Packet) {
			p.Scenarios = append(p.Scenarios, p.Scenarios[0])
		}),
	} {
		t.Run(name, func(t *testing.T) {
			if err := p.validate(); err == nil {
				t.Fatal("invalid packet accepted")
			}
		})
	}
}
