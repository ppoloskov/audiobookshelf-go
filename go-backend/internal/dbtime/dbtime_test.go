package dbtime

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestFormatUsesUTCAndMillisecondPrecision(t *testing.T) {
	value := Format(time.Date(2025, 3, 4, 5, 6, 7, 123456789, time.FixedZone("offset", 2*60*60)))
	if value != "2025-03-04 03:06:07.123 +00:00" {
		t.Fatalf("Format() = %q", value)
	}
}

func TestNowUsesCanonicalLayout(t *testing.T) {
	value := Now()
	if len(value) != len("2006-01-02 15:04:05.000 +00:00") || !strings.HasSuffix(value, " +00:00") {
		t.Fatalf("Now() = %q", value)
	}
	if _, err := time.Parse("2006-01-02 15:04:05.000 -07:00", value); err != nil {
		t.Fatalf("Now() is not parseable: %v", err)
	}
}

func TestSQLNullBindingStaysNullInDatabase(t *testing.T) {
	// Regression: a nullable DATETIME must reach SQLite as SQL NULL, never the
	// empty string (audits/B5-datetime.md NULL-vs-empty section).
	db, err := sql.Open("sqlite", "file:dbtime-null-test?mode=memory")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE timestamps (ts DATETIME)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO timestamps (ts) VALUES (?)`, nil); err != nil {
		t.Fatal(err)
	}
	var stored any
	if err := db.QueryRow(`SELECT ts FROM timestamps`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != nil {
		t.Fatalf("stored NULL DATETIME = %#v (%T), want <nil>", stored, stored)
	}
	// The formatted canonical string round-trips through a DATETIME column.
	formatted := Now()
	if _, err := db.Exec(`INSERT INTO timestamps (ts) VALUES (?)`, formatted); err != nil {
		t.Fatal(err)
	}
	var text string
	if err := db.QueryRow(`SELECT ts FROM timestamps WHERE rowid=2`).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if text != formatted {
		t.Fatalf("stored canonical DATETIME = %q, want %q", text, formatted)
	}
}
