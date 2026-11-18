package harness

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	_ "modernc.org/sqlite"
)

// sourcePin is the upstream commit the fixture install was created with.
const sourcePin = "edb1f7efc92ecacdef51aafb4d9d5054ce10604f"

// maxFixtureMetaBytes bounds fixture.json and ids.json reads.
const maxFixtureMetaBytes = 1 << 20

// fixtureTemplate returns the read-only fixture install under the repository root.
func fixtureTemplate(root string) string {
	return filepath.Join(root, "go-backend/internal/contracttest/testdata/install")
}

// copyTree copies the regular files and directories of source into dest.
// A symlink is an error: the fixture must be self-contained.
func copyTree(ctx context.Context, source, dest string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("fixture symlink is not allowed: %s", rel)
		}
		if entry.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close() // fixture inputs are read-only; no write errors to flush
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

// freshInstall creates empty config and metadata directories and copies only the
// fixture media, so the server starts uninitialised over the same library files.
func freshInstall(ctx context.Context, root, dest string) error {
	for _, name := range []string{"config", "metadata"} {
		if err := os.MkdirAll(filepath.Join(dest, name), 0755); err != nil {
			return fmt.Errorf("fresh install: %w", err)
		}
	}
	if err := copyTree(ctx, filepath.Join(fixtureTemplate(root), "media"), filepath.Join(dest, "media")); err != nil {
		return fmt.Errorf("fresh install media: %w", err)
	}
	return nil
}

// treeDigest hashes relative names and contents of every file under dir.
func treeDigest(dir string) (string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	slices.Sort(files)
	hash := sha256.New()
	for _, file := range files {
		rel, err := filepath.Rel(dir, file)
		if err != nil {
			return "", err
		}
		if _, err := io.WriteString(hash, filepath.ToSlash(rel)+"\x00"); err != nil {
			return "", err
		}
		in, err := os.Open(file)
		if err != nil {
			return "", err
		}
		_, copyErr := io.Copy(hash, in)
		closeErr := in.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// copyFixture owns only a fresh temporary destination, never a live installation.
// It copies the fixture install and rewrites the seed paths stored in the database.
func copyFixture(ctx context.Context, root, dest string) (err error) {
	source := fixtureTemplate(root)
	if err := copyTree(ctx, source, dest); err != nil {
		return fmt.Errorf("copy fixture: %w", err)
	}
	sourceDigest, err := treeDigest(source)
	if err != nil {
		return err
	}
	copiedDigest, err := treeDigest(dest)
	if err != nil {
		return err
	}
	if sourceDigest != copiedDigest {
		return fmt.Errorf("fixture copy differs before path rewriting")
	}
	raw, err := readSmallFile(filepath.Join(dest, "fixture.json"))
	if err != nil {
		return err
	}
	var meta struct {
		Pin   string `json:"upstreamCommit"`
		Roots struct {
			Config   string `json:"config"`
			Metadata string `json:"metadata"`
			Media    string `json:"media"`
		} `json:"seedRoots"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return err
	}
	if meta.Pin != sourcePin {
		return fmt.Errorf("starting fixture source pin differs")
	}
	pairs := [][2]string{{meta.Roots.Config, filepath.Join(dest, "config")}, {meta.Roots.Metadata, filepath.Join(dest, "metadata")}, {meta.Roots.Media, filepath.Join(dest, "media")}}
	slices.SortFunc(pairs, func(a, b [2]string) int { return len(b[0]) - len(a[0]) })
	data, err := sql.Open("sqlite", filepath.Join(dest, "config/absdatabase.sqlite"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, data.Close()) }()
	tables, err := data.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND substr(name,1,7)<>'sqlite_'")
	if err != nil {
		return err
	}
	var names []string
	for tables.Next() {
		var name string
		if err := tables.Scan(&name); err != nil {
			_ = tables.Close()
			return err
		}
		names = append(names, name)
	}
	rowsErr := tables.Err()
	closeErr := tables.Close()
	if rowsErr != nil {
		return rowsErr
	}
	if closeErr != nil {
		return closeErr
	}
	type column struct{ table, name string }
	var columns []column
	for _, table := range names {
		rows, err := data.QueryContext(ctx, "PRAGMA table_info("+quoteIdentifier(table)+")")
		if err != nil {
			return err
		}
		for rows.Next() {
			var cid, notNull, pk int
			var name, kind string
			var fallback sql.NullString
			if err := rows.Scan(&cid, &name, &kind, &notNull, &fallback, &pk); err != nil {
				_ = rows.Close()
				return err
			}
			upper := strings.ToUpper(kind)
			if upper == "" || strings.Contains(upper, "TEXT") || strings.Contains(upper, "CHAR") || strings.Contains(upper, "CLOB") || strings.Contains(upper, "JSON") {
				columns = append(columns, column{table, name})
			}
		}
		readErr := rows.Err()
		closeErr := rows.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	tx, err := data.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, rollbackErr)
		}
	}()
	for _, pair := range pairs {
		if pair[0] == "" {
			return fmt.Errorf("empty source fixture path")
		}
		for _, col := range columns {
			q := "UPDATE " + quoteIdentifier(col.table) + " SET " + quoteIdentifier(col.name) + "=replace(" + quoteIdentifier(col.name) + ",?,?) WHERE instr(" + quoteIdentifier(col.name) + ",?)>0"
			if _, err := tx.ExecContext(ctx, q, pair[0], pair[1], pair[0]); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func quoteIdentifier(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }

// readSmallFile reads a fixture metadata file, refusing anything over maxFixtureMetaBytes.
func readSmallFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxFixtureMetaBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, maxFixtureMetaBytes)
	}
	return os.ReadFile(path)
}

// fixtureUser is one login of fixture.json.
type fixtureUser struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// loadInstallMeta reads ids.json and the users of fixture.json from an install.
// Both are absent for a Fresh install, which yields an empty result.
func loadInstallMeta(install string) (ids map[string]string, users map[string]fixtureUser, err error) {
	ids = map[string]string{}
	users = map[string]fixtureUser{}
	raw, err := readSmallFile(filepath.Join(install, "ids.json"))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, nil, err
	default:
		if err := json.Unmarshal(raw, &ids); err != nil {
			return nil, nil, fmt.Errorf("decode ids.json: %w", err)
		}
	}
	raw, err = readSmallFile(filepath.Join(install, "fixture.json"))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, nil, err
	default:
		var meta struct {
			Users map[string]fixtureUser `json:"users"`
		}
		if err := json.Unmarshal(raw, &meta); err != nil {
			return nil, nil, fmt.Errorf("decode fixture.json: %w", err)
		}
		users = meta.Users
	}
	return ids, users, nil
}
