package main

import (
	"os"
	"path/filepath"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

func TestDigestIncludesNamesAndMissingInputsFail(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a"), []byte("same"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := digestPaths(root, ".")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "a"), filepath.Join(root, "b")); err != nil {
		t.Fatal(err)
	}
	after, err := digestPaths(root, ".")
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("renamed source input was omitted from digest")
	}
	if _, err := digestPaths(root, "missing"); err == nil {
		t.Fatal("missing input accepted")
	}
}

func TestDependencyProvenanceIncludesInstalledFilesAndLocalBinLinks(t *testing.T) {
	root := t.TempDir()
	modules := filepath.Join(root, "node_modules")
	if err := os.MkdirAll(filepath.Join(modules, ".bin"), 0700); err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(modules, "tool.js")
	if err := os.WriteFile(tool, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(modules, ".bin", "tool")
	if err := os.Symlink("../tool.js", link); err != nil {
		t.Fatal(err)
	}
	first, err := digestPaths(root, "node_modules")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tool, []byte("different"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := digestPaths(root, "node_modules")
	if err != nil || first == second {
		t.Fatalf("changed installed code did not change digest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "external"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../external", filepath.Join(modules, ".bin", "outside")); err != nil {
		t.Fatal(err)
	}
	if _, err := digestPaths(root, "node_modules"); err == nil {
		t.Fatal("accepted linked code outside the measured install")
	}
	if err := os.Remove(filepath.Join(modules, ".bin", "outside")); err != nil {
		t.Fatal(err)
	}
	// A link elsewhere than node_modules/.bin is refused even when it stays inside.
	if err := os.Symlink("tool.js", filepath.Join(modules, "alias.js")); err != nil {
		t.Fatal(err)
	}
	if _, err := digestPaths(root, "node_modules"); err == nil {
		t.Fatal("accepted a symlink outside node_modules/.bin")
	}
	if err := os.Remove(filepath.Join(modules, "alias.js")); err != nil {
		t.Fatal(err)
	}
	// Repointing a .bin link changes the digest even if file contents stay equal.
	third, err := digestPaths(root, "node_modules")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modules, "other.js"), []byte("different"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../other.js", link); err != nil {
		t.Fatal(err)
	}
	fourth, err := digestPaths(root, "node_modules")
	if err != nil || fourth == third {
		t.Fatalf("repointed link did not change digest: %v", err)
	}
}

func TestInputDigestsMeasureInstalledDependencyCode(t *testing.T) {
	root := t.TempDir()
	// Isolate the provenance measurement from the independently enforced upstream
	// Git pin: this fixture contains only the files needed by inputDigests.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	for _, path := range []string{
		"server/source.js", "test/source.js", "index.js", "client/dist/index.html",
		"dist-server/server/source.js", "dist-server/test/source.js", "dist-server/index.js",
		"docs/agent-work/capabilities/requirement.json", "docs/agent-work/CONTRACT.md", "go-backend/ARCHITECTURE.md",
		"go-backend/contracts/clients/audit.md", "go-backend/contracts/client-usage.json", "go-backend/contracts/mobile-client-audit.md",
		"go-backend/tests/replacement/scenario.go", "go-backend/internal/contracttest/testdata/install/fixture.json",
		"go-backend/tools/verify/main.go", "go-backend/tools/source-inventory.mjs", "go-backend/tools/replacement-contract-map.mjs", "go-backend/tools/parity/check.cjs",
		"go-backend/go.mod", "go-backend/go.sum", "go-backend/cmd/main.go",
		"package-lock.json", "node_modules/.package-lock.json", "node_modules/dependency/index.js",
	} {
		file := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("original"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	before, err := inputDigests(root, os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "node_modules/dependency/index.js"), []byte("changed without touching the lock"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := inputDigests(root, os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if before.DependencyInstall == after.DependencyInstall {
		t.Fatal("report dependency provenance ignored changed installed JavaScript")
	}
}

func TestDigestRejectsScenarioDirectoryAlias(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "library_manage"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("library_manage", filepath.Join(root, "library-manage")); err != nil {
		t.Fatal(err)
	}
	if _, err := digestPaths(root, "."); err == nil {
		t.Fatal("accepted a scenario directory alias instead of one canonical packet path")
	}
}

func TestPlanRejectsUnknownAndInventedFacets(t *testing.T) {
	record := capability{ID: "r", Scope: "in", Cases: map[string]string{"normal": "unverified"}, Invariants: []invariant{{ID: "i"}}}
	packets := map[string][]string{"p": {"r"}, "q": {"other"}}
	good := harness.Declaration{Packet: "p", Proof: harness.Proof{Requirement: "r", Invariant: "i", Case: "normal", State: "stateless", Assertion: "a"}}
	if err := validatePlan([]capability{record}, packets, []harness.Declaration{good}); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*harness.Declaration){
		"unknown requirement":            func(d *harness.Declaration) { d.Requirement = "missing" },
		"unknown invariant":              func(d *harness.Declaration) { d.Invariant = "missing" },
		"unknown case":                   func(d *harness.Declaration) { d.Case = "error" },
		"lifecycle on stateless":         func(d *harness.Declaration) { d.State = "repeat" },
		"unknown packet":                 func(d *harness.Declaration) { d.Packet = "nope" },
		"requirement of an other packet": func(d *harness.Declaration) { d.Packet = "q" },
		"empty assertion":                func(d *harness.Declaration) { d.Assertion = "" },
	} {
		t.Run(name, func(t *testing.T) {
			d := good
			mutate(&d)
			if err := validatePlan([]capability{record}, packets, []harness.Declaration{d}); err == nil {
				t.Fatal("invalid declaration accepted")
			}
		})
	}
	if err := validatePlan([]capability{record}, packets, []harness.Declaration{good, good}); err == nil {
		t.Fatal("duplicate assertion in one packet accepted")
	}
}
