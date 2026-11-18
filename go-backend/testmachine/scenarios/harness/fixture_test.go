package harness

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFixtureCopyRewritesPathsWithoutChangingTemplate(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	source := fixtureTemplate(root)
	before, err := treeDigest(source)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "install")
	if err := copyFixture(context.Background(), root, dest); err != nil {
		t.Fatal(err)
	}
	data, err := sql.Open("sqlite", filepath.Join(dest, "config/absdatabase.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	var path string
	if err := data.QueryRow("SELECT path FROM libraryFolders LIMIT 1").Scan(&path); err != nil {
		t.Fatal(err)
	}
	if err := data.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, filepath.Join(dest, "media")) {
		t.Fatal("fixture retained a seed media path")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("rewritten fixture path is not usable:", err)
	}
	after, err := treeDigest(source)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("fixture template was mutated")
	}
	ids, users, err := loadInstallMeta(dest)
	if err != nil {
		t.Fatal(err)
	}
	if ids["library"] == "" || users["root"].Username == "" {
		t.Fatalf("install metadata incomplete: %v %v", ids, users)
	}
}

func TestFreshInstallHasEmptyConfigAndMediaOnly(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "install")
	if err := freshInstall(context.Background(), root, dest); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config", "metadata"} {
		entries, err := os.ReadDir(filepath.Join(dest, name))
		if err != nil || len(entries) != 0 {
			t.Fatalf("%s must exist and be empty: %v %v", name, entries, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "media")); err != nil {
		t.Fatal(err)
	}
	ids, _, err := loadInstallMeta(dest)
	if err != nil || len(ids) != 0 {
		t.Fatalf("fresh install must have no ids: %v %v", ids, err)
	}
}
