// Package serversettings caches the server-settings row per database handle.
// Save is the only writer of that row and invalidates; whoever closes a handle
// calls Forget, and a restored database is a new handle that starts empty.
package serversettings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	schemaassets "audiobookshelf-compat/contracts/parity"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/jsonvalue"
	"audiobookshelf-compat/internal/textsort"
)

type Settings struct {
	doc      map[string]any
	prefixes []string
}

func parse(raw []byte) Settings {
	doc := map[string]any{}
	if json.Unmarshal(raw, &doc) != nil || doc == nil {
		doc = map[string]any{}
	}
	return Settings{doc: doc, prefixes: textsort.ParsePrefixes(raw)}
}

// Doc decodes a fresh copy of the document; callers may change it freely.
func (s Settings) Doc() map[string]any {
	raw, _ := json.Marshal(s.doc)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}

func (s Settings) Bool(key string) bool { v, _ := s.doc[key].(bool); return v }

func (s Settings) String(key string) string { v, _ := s.doc[key].(string); return v }

// Strings is the string elements of the array setting key.
func (s Settings) Strings(key string) []string { return jsonvalue.Strings(s.doc[key]) }

func (s Settings) SortingPrefixes() []string { return slices.Clone(s.prefixes) }

// IgnorePrefix is sortingIgnorePrefix: sort titles without their prefix.
func (s Settings) IgnorePrefix() bool { return s.Bool("sortingIgnorePrefix") }

// EnvAllowIframe reports ALLOW_IFRAME=1, which forces allowIframe on.
func EnvAllowIframe() bool { return os.Getenv("ALLOW_IFRAME") == "1" }

// EnvBackupPath is BACKUP_PATH, which overrides the stored backupPath.
func EnvBackupPath() string { return os.Getenv("BACKUP_PATH") }

func (s Settings) AllowIframe() bool { return s.Bool("allowIframe") || EnvAllowIframe() }

// DefaultSigningAlgorithm is the OpenID signing algorithm of a fresh server.
const DefaultSigningAlgorithm = "RS256"

// AuthKeys are the authentication settings the auth-settings routes read and
// patch (AuthSettings), in response order. Secret ones are withheld from the
// browser view (ServerSettings.toJSONForBrowser).
var AuthKeys = []string{"authLoginCustomMessage", "authActiveAuthMethods", "authOpenIDIssuerURL", "authOpenIDAuthorizationURL", "authOpenIDTokenURL", "authOpenIDUserInfoURL", "authOpenIDJwksURL", "authOpenIDLogoutURL", "authOpenIDClientID", "authOpenIDClientSecret", "authOpenIDTokenSigningAlgorithm", "authOpenIDButtonText", "authOpenIDAutoLaunch", "authOpenIDAutoRegister", "authOpenIDMatchExistingBy", "authOpenIDMobileRedirectURIs", "authOpenIDGroupClaim", "authOpenIDAdvancedPermsClaim", "authOpenIDSubfolderForRedirectURLs"}

// secretKeys are the settings no browser may see.
var secretKeys = []string{"tokenSecret", "authOpenIDClientID", "authOpenIDClientSecret", "authOpenIDMobileRedirectURIs", "authOpenIDGroupClaim", "authOpenIDAdvancedPermsClaim"}

var defaults = func() map[string]any {
	var asset struct {
		Rows []struct {
			Key   string         `json:"key"`
			Value map[string]any `json:"value"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(schemaassets.DefaultSettingsJSON, &asset); err != nil {
		panic("serversettings: decode default settings: " + err.Error())
	}
	for _, row := range asset.Rows {
		if row.Key == "server-settings" {
			return row.Value
		}
	}
	panic("serversettings: default settings have no server-settings row")
}()

// withDefaults is the document of a server that has stored nothing: the
// defaults plus the backup folder and signing algorithm the server picks.
func withDefaults(backupsDir string) map[string]any {
	doc := maps.Clone(defaults)
	doc["backupPath"] = backupsDir
	doc["authOpenIDTokenSigningAlgorithm"] = DefaultSigningAlgorithm
	return doc
}

// DefaultMetadataPrecedence is the order in which a book library merges its
// metadata sources when none is stored; a new library stores it too.
func DefaultMetadataPrecedence() []string {
	return []string{"folderStructure", "audioMetatags", "nfoFile", "txtFiles", "opfFile", "absMetadata"}
}

// ApplyEnv lets BACKUP_PATH and ALLOW_IFRAME win over the document's values.
func ApplyEnv(doc map[string]any) {
	if value := EnvBackupPath(); value != "" {
		doc["backupPath"] = value
	}
	if EnvAllowIframe() {
		doc["allowIframe"] = true
	}
}

// FreshDocument is the server-settings row a new database starts with.
func FreshDocument(backupsDir string) map[string]any {
	doc := withDefaults(backupsDir)
	ApplyEnv(doc)
	delete(doc, "tokenSecret")
	return doc
}

// ForBrowser is ServerSettings.toJSONForBrowser: the stored settings over the
// defaults, the environment overrides and the host time zone, without secrets.
func (s Settings) ForBrowser(backupsDir string) map[string]any {
	doc := withDefaults(backupsDir)
	maps.Copy(doc, s.Doc())
	// TS ServerSettings keeps an unset authOpenIDTokenURL as null (`settings.x || null`),
	// and the fresh default row does not store the key.
	if _, ok := doc["authOpenIDTokenURL"]; !ok {
		doc["authOpenIDTokenURL"] = nil
	}
	ApplyEnv(doc)
	doc["timeZone"] = HostTimeZone()
	for _, key := range secretKeys {
		delete(doc, key)
	}
	return doc
}

// HostTimeZone mirrors upstream ServerSettings.getHostTimeZone, which answers
// the process IANA zone name (Intl.DateTimeFormat().resolvedOptions().timeZone)
// and falls back to UTC. Go's time.Local.String() returns the unnamed "Local"
// zone when TZ is unset, so resolve the zone from the environment or the
// /etc/localtime symlink.
func HostTimeZone() string {
	if tz := strings.TrimSpace(os.Getenv("TZ")); tz != "" {
		return icuZoneName(tz)
	}
	if link, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
		if index := strings.LastIndex(link, "zoneinfo/"); index >= 0 {
			return icuZoneName(link[index+len("zoneinfo/"):])
		}
	}
	return "UTC"
}

// icuZoneName returns the zone name Node 22's ICU reports for a TZ value
// (for example Etc/UTC -> UTC, US/Pacific -> America/Los_Angeles). The table
// icuZones is generated by tools/icuzones/generate.cjs from
// TZ=<id> node -e 'Intl.DateTimeFormat().resolvedOptions().timeZone'; names it
// does not list resolve to themselves.
func icuZoneName(zone string) string {
	if canonical, ok := icuZones[zone]; ok {
		return canonical
	}
	return zone
}

func Save(ctx context.Context, db *sql.DB, doc map[string]any) error {
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	res, err := db.ExecContext(ctx, `UPDATE settings SET value=?,updatedAt=? WHERE key='server-settings'`, string(b), dbtime.Now())
	if err != nil {
		return err
	}
	Invalidate(db)
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("serversettings: server-settings row missing")
	}
	return nil
}

type entry struct {
	mu  sync.Mutex
	cur *Settings
	gen uint64
}

var entries sync.Map // *sql.DB -> *entry

func entryOf(db *sql.DB) *entry {
	e, _ := entries.LoadOrStore(db, &entry{})
	return e.(*entry)
}

// Get returns the cached settings, reading the row when it is not cached. A
// read that overlaps an Invalidate is discarded and repeated, so Get never
// returns a document older than the last completed write.
func Get(ctx context.Context, db *sql.DB) (Settings, error) {
	e := entryOf(db)
	for {
		e.mu.Lock()
		if e.cur != nil {
			s := *e.cur
			e.mu.Unlock()
			return s, nil
		}
		gen := e.gen
		e.mu.Unlock()
		var raw []byte
		if err := db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='server-settings'`).Scan(&raw); err != nil {
			return Settings{}, err
		}
		s := parse(raw)
		e.mu.Lock()
		if e.gen == gen {
			e.cur = &s
			e.mu.Unlock()
			return s, nil
		}
		e.mu.Unlock()
	}
}

// Load is Get with defaults (no keys, sortingPrefixes the, a) when the row
// cannot be read.
func Load(ctx context.Context, db *sql.DB) Settings {
	s, err := Get(ctx, db)
	if err != nil {
		return parse(nil)
	}
	return s
}

// Invalidate drops the cached value after the server-settings row is written.
func Invalidate(db *sql.DB) {
	e := entryOf(db)
	e.mu.Lock()
	e.cur, e.gen = nil, e.gen+1
	e.mu.Unlock()
}

// Forget drops the handle's entry; call it when the handle is closed.
func Forget(db *sql.DB) { entries.Delete(db) }
