package store

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// settingsFixtureUpdatedAt is the pinned fixture timestamp of the
// server-settings row.
const settingsFixtureUpdatedAt = "2026-11-03 12:46:24.606 +00:00"

func settingsRowQuery() string {
	return "SELECT value, createdAt, updatedAt FROM settings WHERE key='server-settings'"
}

// settingsPatchInitial: the first settings PATCH merges into the document,
// moves updatedAt strictly forward and does not surface the token secret.
func settingsPatchInitial(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{})
	token := e.Token(t, "root")
	query := settingsRowQuery()
	before := e.Rows(t, query)
	if len(before) != 1 || before[0]["updatedAt"] != settingsFixtureUpdatedAt || before[0]["createdAt"] != settingsFixtureUpdatedAt {
		t.Fatalf("fixture settings row: %v", before)
	}
	assertFixtureSettings(t, decodeStoredSettings(t, before[0]["value"]))
	floor := time.Now()
	r := e.Patch(t, token, "/api/settings", map[string]any{"language": "de"})
	if r.Status != http.StatusOK {
		t.Fatalf("settings PATCH status %d", r.Status)
	}
	var body struct {
		ServerSettings map[string]any `json:"serverSettings"`
	}
	r.JSON(t, &body)
	if body.ServerSettings["language"] != "de" {
		t.Fatalf("patch response language: %v", body.ServerSettings["language"])
	}
	if _, ok := body.ServerSettings["tokenSecret"]; ok {
		t.Fatal("patch response surfaced tokenSecret")
	}
	after := e.Rows(t, query)
	if len(after) != 1 || after[0]["createdAt"] != settingsFixtureUpdatedAt {
		t.Fatalf("settings createdAt changed: %v", after)
	}
	stored := decodeStoredSettings(t, after[0]["value"])
	if stored.Language != "de" || stored.ChromecastEnabled || stored.ScannerCoverProvider != "google" || stored.TokenSecret == "" {
		t.Fatalf("patched settings: %+v", stored)
	}
	updated, ok := after[0]["updatedAt"].(string)
	if !ok || updated <= settingsFixtureUpdatedAt {
		t.Fatalf("updatedAt did not move forward: %v", after[0]["updatedAt"])
	}
	assertUTCWindow(t, "settings.updatedAt", after[0]["updatedAt"], floor)
}

// settingsPatchRepeat: repeating the same patch writes nothing, and a patch
// with only non-patchable keys is ignored entirely.
func settingsPatchRepeat(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{})
	token := e.Token(t, "root")
	query := settingsRowQuery()
	r := e.Patch(t, token, "/api/settings", map[string]any{"language": "de"})
	if r.Status != http.StatusOK {
		t.Fatalf("first patch status %d", r.Status)
	}
	written := e.Rows(t, query)
	if len(written) != 1 || written[0]["updatedAt"] == settingsFixtureUpdatedAt {
		t.Fatalf("first patch row: %v", written)
	}
	repeat := e.Patch(t, token, "/api/settings", map[string]any{"language": "de"})
	if repeat.Status != http.StatusOK {
		t.Fatalf("repeat patch status %d", repeat.Status)
	}
	var body struct {
		ServerSettings map[string]any `json:"serverSettings"`
	}
	repeat.JSON(t, &body)
	if body.ServerSettings["language"] != "de" {
		t.Fatalf("repeat patch language: %v", body.ServerSettings["language"])
	}
	if got := e.Rows(t, query); !reflect.DeepEqual(got, written) {
		t.Fatalf("repeat patch rewrote the row: %v != %v", got, written)
	}
	// tokenSecret and unknown keys are not patchable; such a patch writes
	// nothing and answers 200.
	unknown := e.Patch(t, token, "/api/settings", map[string]any{"tokenSecret": "ignored", "notASetting": 1})
	if unknown.Status != http.StatusOK {
		t.Fatalf("unknown-key patch status %d", unknown.Status)
	}
	if got := e.Rows(t, query); !reflect.DeepEqual(got, written) {
		t.Fatalf("unknown-key patch rewrote the row: %v != %v", got, written)
	}
}

// settingsConcurrentMerge: two writers racing disjoint patches both persist.
func settingsConcurrentMerge(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{})
	token := e.Token(t, "root")
	query := settingsRowQuery()
	assertFixtureSettings(t, decodeStoredSettings(t, e.Rows(t, query)[0]["value"]))
	// Each writer repeats its disjoint patch many rounds so the two real
	// writes interleave with sustained concurrent request load.
	const rounds = 90
	writers := []struct {
		key   string
		value any
	}{{key: "language", value: "fr"}, {key: "chromecastEnabled", value: true}}
	failures := make([]error, len(writers))
	var wg sync.WaitGroup
	for index, writer := range writers {
		wg.Add(1)
		go func(index int, writer struct {
			key   string
			value any
		}) {
			defer wg.Done()
			payload, err := json.Marshal(map[string]any{writer.key: writer.value})
			if err != nil {
				failures[index] = err
				return
			}
			for round := 0; round < rounds; round++ {
				status, err := rawJSONRequest(http.MethodPatch, e.Base+"/api/settings", token, payload)
				if err != nil {
					failures[index] = err
					return
				}
				if status != http.StatusOK {
					failures[index] = fmt.Errorf("round %d: status %d", round, status)
					return
				}
			}
		}(index, writer)
	}
	wg.Wait()
	for index, err := range failures {
		if err != nil {
			t.Fatalf("writer %d (%s): %v", index, writers[index].key, err)
		}
	}
	stored := decodeStoredSettings(t, e.Rows(t, query)[0]["value"])
	if stored.Language != "fr" || !stored.ChromecastEnabled || stored.ScannerCoverProvider != "google" {
		t.Fatalf("merged settings: %+v", stored)
	}
	auth := e.Post(t, token, "/api/authorize", nil)
	if auth.Status != http.StatusOK {
		t.Fatalf("authorize status %d", auth.Status)
	}
	surfaced := auth.Object(t)["serverSettings"]
	visible, ok := surfaced.(map[string]any)
	if !ok || visible["language"] != "fr" || visible["chromecastEnabled"] != true {
		t.Fatalf("surfaced merged settings: %v", surfaced)
	}
}

// settingsFailureTrigger: a settings upsert that fails in the database exits
// the server, leaves the persisted document and timestamp intact, and the
// installation loads the original settings after the trigger is removed.
func settingsFailureTrigger(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{})
	token := e.Token(t, "root")
	query := settingsRowQuery()
	before := e.Rows(t, query)
	if len(before) != 1 {
		t.Fatalf("settings rows: %d", len(before))
	}
	assertFixtureSettings(t, decodeStoredSettings(t, before[0]["value"]))
	db := openRW(t, e)
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	}()
	drop := abortTriggers(t, db, "settings")
	payload, err := json.Marshal(map[string]any{"language": "de"})
	if err != nil {
		t.Fatal(err)
	}
	// The aborted upsert exits the server before it can answer; the closed
	// connection is the client-visible failure.
	_, _ = rawJSONRequest(http.MethodPatch, e.Base+"/api/settings", token, payload)
	code, exited := e.WaitExit(15 * time.Second)
	if !exited || code != 1 {
		t.Fatalf("aborted settings upsert: exited=%v code=%d", exited, code)
	}
	if got := e.Rows(t, query); !reflect.DeepEqual(got, before) {
		t.Fatalf("failed upsert changed the settings row: %v != %v", got, before)
	}
	e.Stop(t)
	for _, statement := range drop {
		// The triggers only existed for this scenario; dropping them restores
		// the install for the reopen.
		_ = e.Exec(t, statement)
	}
	reopened := harness.Start(t, harness.Options{Install: e.Install, Upstream: true})
	if got := reopened.Rows(t, query); !reflect.DeepEqual(got, before) {
		t.Fatalf("reopened settings row changed: %v != %v", got, before)
	}
	token = reopened.Token(t, "root")
	auth := reopened.Post(t, token, "/api/authorize", nil)
	if auth.Status != http.StatusOK {
		t.Fatalf("authorize status %d", auth.Status)
	}
	if visible, ok := auth.Object(t)["serverSettings"].(map[string]any); !ok || visible["language"] != "en-us" {
		t.Fatalf("reopened settings language: %v", auth.Object(t)["serverSettings"])
	}
	// Settings are writable again once the failure trigger is gone.
	r := reopened.Patch(t, token, "/api/settings", map[string]any{"language": "de"})
	if r.Status != http.StatusOK {
		t.Fatalf("recovery patch status %d", r.Status)
	}
	if stored := decodeStoredSettings(t, reopened.Rows(t, query)[0]["value"]); stored.Language != "de" {
		t.Fatalf("recovery patch language: %+v", stored)
	}
}

// settingsReopen: sequential disjoint settings writes merge into one document
// and survive pinned upstream reopen.
func settingsReopen(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{})
	token := e.Token(t, "root")
	for _, payload := range []map[string]any{{"language": "fr"}, {"chromecastEnabled": true}, {"backupSchedule": "0 4 * * *"}} {
		r := e.Patch(t, token, "/api/settings", payload)
		if r.Status != http.StatusOK {
			t.Fatalf("settings PATCH status %d", r.Status)
		}
	}
	query := "SELECT value,updatedAt FROM settings WHERE key='server-settings'"
	before := e.Rows(t, query)
	assertSettings(t, before)
	auth := e.Post(t, token, "/api/authorize", nil)
	if auth.Status != http.StatusOK {
		t.Fatalf("authorize status %d", auth.Status)
	}
	surfaced := auth.Object(t)["serverSettings"]
	assertSurfaced(t, surfaced)

	e.Stop(t)
	reopened := harness.Start(t, harness.Options{Install: e.Install, Upstream: true})
	after := reopened.Rows(t, query)
	if !reflect.DeepEqual(after, before) {
		t.Fatal("settings row or timestamp changed on reopen")
	}
	assertSettings(t, after)
	token = reopened.Token(t, "root")
	auth = reopened.Post(t, token, "/api/authorize", nil)
	if auth.Status != http.StatusOK || !reflect.DeepEqual(auth.Object(t)["serverSettings"], surfaced) {
		t.Fatal("reopened settings differ")
	}
}
func assertSettings(t *testing.T, rows []map[string]any) {
	t.Helper()
	if len(rows) != 1 {
		t.Fatal("missing settings row")
	}
	raw, ok := rows[0]["value"].(string)
	if !ok {
		t.Fatal("settings value is not text")
	}
	var value struct {
		Language          string `json:"language"`
		ChromecastEnabled bool   `json:"chromecastEnabled"`
		BackupSchedule    any    `json:"backupSchedule"`
	}
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatal("invalid settings JSON")
	}
	if value.Language != "fr" || !value.ChromecastEnabled || value.BackupSchedule != "0 4 * * *" {
		t.Fatalf("settings merge: language=%s chromecast=%v backupSchedule=%v", value.Language, value.ChromecastEnabled, value.BackupSchedule)
	}
}

func assertSurfaced(t *testing.T, value any) {
	t.Helper()
	settings, ok := value.(map[string]any)
	if !ok || settings["language"] != "fr" || settings["chromecastEnabled"] != true || settings["backupSchedule"] != "0 4 * * *" {
		t.Fatal("surfaced settings do not contain all changes")
	}
}

// settingsIframeEnv: with ALLOW_IFRAME enabled in the environment, a patch
// that disables iframe embedding is rejected before any write.
func settingsIframeEnv(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{Env: map[string]string{"ALLOW_IFRAME": "1"}})
	token := e.Token(t, "root")
	query := settingsRowQuery()
	before := e.Rows(t, query)
	if len(before) != 1 {
		t.Fatal("missing settings row")
	}
	r := e.Patch(t, token, "/api/settings", map[string]any{"allowIframe": false, "language": "de"})
	if r.Status != http.StatusBadRequest {
		t.Fatalf("iframe rejection status %d", r.Status)
	}
	if got := e.Rows(t, query); !reflect.DeepEqual(got, before) {
		t.Fatal("iframe rejection changed the settings row")
	}
	auth := e.Post(t, token, "/api/authorize", nil)
	if auth.Status != http.StatusOK {
		t.Fatalf("authorize status %d", auth.Status)
	}
	// The environment override turns the in-memory setting on at boot
	// (server/objects/settings/ServerSettings.js:224-228) without persisting
	// it; the stored row keeps allowIframe false.
	if visible, ok := auth.Object(t)["serverSettings"].(map[string]any); !ok || visible["language"] != "en-us" || visible["allowIframe"] != true {
		t.Fatalf("surfaced settings after rejection: %v", auth.Object(t)["serverSettings"])
	}
}

// settingsRejection: an invalid allowedOrigins update with another supplied
// field is rejected before changing stored or surfaced settings, as is a body
// that is not an object.
func settingsRejection(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{})
	token := e.Token(t, "root")
	query := "SELECT value,updatedAt FROM settings WHERE key='server-settings'"
	before := e.Rows(t, query)
	if len(before) != 1 {
		t.Fatal("missing settings row")
	}
	raw, ok := before[0]["value"].(string)
	if !ok || raw == "" {
		t.Fatal("missing stored settings JSON")
	}
	auth := e.Post(t, token, "/api/authorize", nil)
	if auth.Status != http.StatusOK {
		t.Fatalf("authorize status %d", auth.Status)
	}
	surfaced := auth.Object(t)["serverSettings"]
	visible, ok := surfaced.(map[string]any)
	if !ok || visible["language"] != "en-us" {
		t.Fatal("fixture surfaced language differs")
	}
	// Upstream keeps req.body an object for every parsed request, so only the
	// allowedOrigins check rejects here; see the coverage note in the packet
	// report for the unreachable non-object branch.
	r := e.Patch(t, token, "/api/settings", map[string]any{"allowedOrigins": "invalid", "language": "de"})
	if r.Status != http.StatusBadRequest {
		t.Fatalf("invalid settings status %d", r.Status)
	}
	// The route is admin-only: a non-admin patch is rejected before any
	// write, without touching the stored or surfaced settings.
	listener := e.Token(t, "listener")
	forbidden := e.Patch(t, listener, "/api/settings", map[string]any{"language": "de"})
	if forbidden.Status != http.StatusForbidden {
		t.Fatalf("non-admin settings PATCH status %d", forbidden.Status)
	}
	if got := e.Rows(t, query); !reflect.DeepEqual(got, before) {
		t.Fatal("rejection changed settings row or timestamp")
	}
	auth = e.Post(t, token, "/api/authorize", nil)
	if auth.Status != http.StatusOK || !reflect.DeepEqual(auth.Object(t)["serverSettings"], surfaced) {
		t.Fatal("rejection changed surfaced settings")
	}
}
