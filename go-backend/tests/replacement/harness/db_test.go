package harness

import (
	"context"
	"path/filepath"
	"testing"
)

func TestRowsAndCountReadFixtureDatabase(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	install := filepath.Join(t.TempDir(), "install")
	if err := copyFixture(context.Background(), root, install); err != nil {
		t.Fatal(err)
	}
	e := &Env{Config: filepath.Join(install, "config")}
	if count := e.Count(t, "SELECT COUNT(*) FROM users WHERE username = ?", "trial-root"); count != 1 {
		t.Fatalf("root user count = %d", count)
	}
	rows := e.Rows(t, "SELECT username, isActive FROM users ORDER BY username")
	if len(rows) < 3 || rows[0]["username"] != "trial-finisher" {
		t.Fatalf("rows: %v", rows)
	}
	if _, ok := rows[0]["username"].(string); !ok {
		t.Fatalf("text column is %T, want string", rows[0]["username"])
	}
}
