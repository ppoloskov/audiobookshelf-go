package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Run declares the packet's plan, starts its shared server and runs each
// scenario in declaration order as a subtest named after its assertion.
// A proof line is logged only when the scenario returned without failing or
// skipping. With REPLACEMENT_PLAN_ONLY=1 it stops after the declarations.
// Call it from the packet's TestReplacement; the packet name must equal the
// package directory name.
func Run(t *testing.T, p Packet) {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(dir) != p.Name {
		t.Fatalf("packet name %q must equal its package directory %q", p.Name, filepath.Base(dir))
	}
	run(t, p)
}

// run is Run without the directory check, so self-tests can use any name.
func run(t *testing.T, p Packet) {
	t.Helper()
	if err := p.validate(); err != nil {
		t.Fatal(err)
	}
	for _, s := range p.Scenarios {
		declare := Declaration{Packet: p.Name, Proof: s.proof(), Sources: s.Sources, Claim: s.Claim}
		raw, err := json.Marshal(declare)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("REPLACEMENT_DECLARE %s", raw)
	}
	if os.Getenv("REPLACEMENT_PLAN_ONLY") == "1" {
		return
	}
	var e *Env
	if !p.NoServer {
		e = Start(t, p.Options)
	}
	for _, s := range p.Scenarios {
		t.Run(s.Assertion, func(t *testing.T) {
			defer func() {
				// Skip ends the test with Goexit, so this runs for it too.
				if t.Skipped() && !t.Failed() {
					t.Errorf("scenario %s skipped; a skip is not a pass", s.Assertion)
				}
			}()
			s.Run(t, e)
			if t.Failed() {
				return
			}
			raw, err := json.Marshal(struct {
				Packet string `json:"packet"`
				Proof
			}{p.Name, s.proof()})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("REPLACEMENT_PROOF %s", raw)
		})
	}
}
