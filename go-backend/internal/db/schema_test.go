package db

import (
	"os"
	"strings"
	"testing"

	schemaassets "audiobookshelf-compat/contracts/parity"
)

// schemaMarker ends the header of schema.sql; everything after it must be the
// pinned contract, byte for byte.
const schemaMarker = "-- ---8<--- copied from contracts/parity/schema.sql below ---8<---\n"

// TestSchemaIsPinnedContract fails when schema.sql drifts from the TS v2.37.2
// schema that contracts/parity/schema.sql (embedded by schemaassets) holds.
func TestSchemaIsPinnedContract(t *testing.T) {
	data, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, body, found := strings.Cut(string(data), schemaMarker)
	if !found {
		t.Fatal("schema.sql is missing its copy marker")
	}
	if body != schemaassets.SchemaSQL {
		t.Fatal("schema.sql body differs from contracts/parity/schema.sql; copy the contract file again")
	}
}

// TestSQLCSchemaIsDerived fails when sqlc/schema.sql is not what `make sqlc`
// derives from schema.sql. sqlc cannot parse the trigger bodies or backtick
// quoting, so the derived copy drops triggers and unquotes names; the types
// sqlc generates do not depend on either.
func TestSQLCSchemaIsDerived(t *testing.T) {
	src, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	derived, err := os.ReadFile("sqlc/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if string(derived) != deriveSQLCSchema(string(src)) {
		t.Fatal("sqlc/schema.sql is stale; run `make sqlc`")
	}
}

// deriveSQLCSchema mirrors the awk filter in the Makefile sqlc target: drop each
// CREATE TRIGGER statement through its END; line, and remove backticks.
func deriveSQLCSchema(src string) string {
	var out strings.Builder
	inTrigger := false
	for _, line := range strings.SplitAfter(src, "\n") {
		if strings.HasPrefix(line, "CREATE TRIGGER") {
			inTrigger = true
		}
		if inTrigger {
			if strings.HasSuffix(strings.TrimSpace(line), "END;") {
				inTrigger = false
			}
			continue
		}
		out.WriteString(strings.ReplaceAll(line, "`", ""))
	}
	return out.String()
}
