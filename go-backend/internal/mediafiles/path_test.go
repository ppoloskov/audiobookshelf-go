package mediafiles

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPurgeCoverCacheRemovesOnlyItemVariants(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"i1_400.jpeg", "i1_800.webp", "i10_400.jpeg", "other"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	PurgeCoverCache(dir, "i1")
	for _, name := range []string{"i1_400.jpeg", "i1_800.webp"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s survived purge: %v", name, err)
		}
	}
	for _, name := range []string{"i10_400.jpeg", "other"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s was removed: %v", name, err)
		}
	}
}

func TestWithinAnyRequiresResolvedPathInsideRoot(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "sub", "file.mp3")
	if err := os.MkdirAll(filepath.Dir(inside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.mp3")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !WithinAny(inside, []string{root}) {
		t.Errorf("file inside root was rejected")
	}
	if WithinAny(outside, []string{root}) {
		t.Errorf("file outside root was accepted")
	}
	if WithinAny(filepath.Join(root, "missing.mp3"), []string{root}) {
		t.Errorf("missing target was accepted")
	}
}
