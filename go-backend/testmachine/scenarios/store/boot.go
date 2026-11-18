// The boot scenarios cover the sub-database-schema facets beyond the pinned
// repeat boot: boot tolerating a missing settings row and boot refusing an
// unreadable database without mutating it.
package store

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// bootMissingSettingsRow: a boot without the server-settings row recreates it
// from the defaults with the generated token secret; login keeps working.
func bootMissingSettingsRow(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{})
	if got := e.Count(t, "SELECT COUNT(*) FROM settings WHERE key = 'server-settings'"); got != 1 {
		t.Fatalf("fixture settings rows: %d", got)
	}
	e.Stop(t)
	if n := e.Exec(t, "DELETE FROM settings WHERE key='server-settings'"); n != 1 {
		t.Fatalf("settings delete: %d", n)
	}
	floor := time.Now()
	e.Restart(t)
	rows := e.Rows(t, "SELECT value, createdAt, updatedAt FROM settings WHERE key='server-settings'")
	if len(rows) != 1 {
		t.Fatalf("recreated settings rows: %d", len(rows))
	}
	assertFixtureSettings(t, decodeStoredSettings(t, rows[0]["value"]))
	assertUTCWindow(t, "settings.createdAt", rows[0]["createdAt"], floor)
	assertUTCWindow(t, "settings.updatedAt", rows[0]["updatedAt"], floor)
	if e.Token(t, "root") == "" {
		t.Fatal("root login after boot without settings row")
	}
}

// bootCorruptRefused: upstream refuses to boot on an unreadable database and
// exits with status 1 without mutating the file or creating sidecars.
func bootCorruptRefused(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{})
	e.Stop(t)
	dbPath := filepath.Join(e.Config, "absdatabase.sqlite")
	corrupt := bytes.Repeat([]byte("not a sqlite database file"), 200)
	if err := os.WriteFile(dbPath, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	// A clean shutdown leaves no write-ahead sidecars; remove stale ones so
	// the refused boot starts from the corrupt file alone.
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if err := os.Remove(dbPath + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	second := harness.Start(t, harness.Options{Install: e.Install, NoWait: true})
	code, exited := second.WaitExit(45 * time.Second)
	if !exited || code != 1 {
		t.Fatalf("refused boot: exited=%v code=%d", exited, code)
	}
	if got := readFile(t, dbPath); !bytes.Equal(got, corrupt) {
		t.Fatalf("corrupt database file changed: %d bytes", len(got))
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(dbPath + suffix); err == nil {
			t.Fatalf("refused boot created %s", suffix)
		}
	}
}

// storedSettings is the subset of the persisted server-settings document the
// scenarios pin.
type storedSettings struct {
	Language             string   `json:"language"`
	ChromecastEnabled    bool     `json:"chromecastEnabled"`
	ScannerCoverProvider string   `json:"scannerCoverProvider"`
	AllowIframe          bool     `json:"allowIframe"`
	SortingPrefixes      []string `json:"sortingPrefixes"`
	BackupsToKeep        int      `json:"backupsToKeep"`
	MaxBackupSize        int      `json:"maxBackupSize"`
	Version              string   `json:"version"`
	TokenSecret          string   `json:"tokenSecret"`
}

// decodeStoredSettings decodes the settings TEXT document.
func decodeStoredSettings(t *testing.T, raw any) storedSettings {
	t.Helper()
	text, ok := raw.(string)
	if !ok {
		t.Fatalf("settings value is %T, not text", raw)
	}
	var value storedSettings
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	return value
}

// assertFixtureSettings fails unless the document holds the pinned defaults;
// the token secret is only checked for presence, never printed or compared.
func assertFixtureSettings(t *testing.T, stored storedSettings) {
	t.Helper()
	if stored.Language != "en-us" || stored.ChromecastEnabled || stored.ScannerCoverProvider != "google" ||
		stored.AllowIframe || stored.BackupsToKeep != 2 || stored.MaxBackupSize != 1 || stored.Version != "2.37.2" ||
		stored.TokenSecret == "" || !reflect.DeepEqual(stored.SortingPrefixes, []string{"the", "a"}) {
		t.Fatalf("settings document: %+v", stored)
	}
}
