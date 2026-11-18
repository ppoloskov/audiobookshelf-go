package item_cover

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// providerStub serves a custom metadata provider search endpoint plus the cover
// image its match points at, both on 127.0.0.1.
type providerStub struct {
	server  *httptest.Server
	fields  map[string]any
	image   []byte
	mu      sync.Mutex
	queries []url.Values
}

// lastQuery returns the query of the last search the stub answered.
func (s *providerStub) lastQuery() url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queries) == 0 {
		return nil
	}
	return s.queries[len(s.queries)-1]
}

// newMatchProvider starts a book provider that answers every search with one
// match built from fields, whose cover url points back at the stub.
func newMatchProvider(t *testing.T, fields map[string]any) *providerStub {
	t.Helper()
	stub := &providerStub{fields: fields, image: pngImage(t, 32, 24)}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			stub.mu.Lock()
			stub.queries = append(stub.queries, r.URL.Query())
			stub.mu.Unlock()
			match := map[string]any{"cover": stub.server.URL + "/cover.png"}
			for key, value := range stub.fields {
				match[key] = value
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{"matches": []any{match}}); err != nil {
				t.Errorf("write provider response: %v", err)
			}
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(stub.image) // a failed write fails the scenario through the download
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

// createProvider stores a custom metadata provider and returns its id.
func createProvider(t *testing.T, e *harness.Env, name, url string) string {
	t.Helper()
	resp := e.Post(t, e.Token(t, "root"), "/api/custom-metadata-providers", map[string]any{
		"name": name, "url": url, "mediaType": "book",
	})
	wantStatus(t, "create provider", resp, http.StatusOK)
	var payload struct {
		Provider struct {
			ID string `json:"id"`
		} `json:"provider"`
	}
	resp.JSON(t, &payload)
	if payload.Provider.ID == "" {
		t.Fatalf("provider id missing: %.200s", resp.Body)
	}
	return payload.Provider.ID
}

// quickMatch posts POST /api/items/batch/quickmatch.
func quickMatch(t *testing.T, e *harness.Env, token string, ids []string, options map[string]any) harness.Response {
	t.Helper()
	body := map[string]any{"libraryItemIds": ids}
	if options != nil {
		body["options"] = options
	}
	return e.Post(t, token, "/api/items/batch/quickmatch", body)
}

// quickMatchResult is the payload of batch_quickmatch_complete.
type quickMatchResult struct {
	Success   bool `json:"success"`
	Updates   int  `json:"updates"`
	Unmatched int  `json:"unmatched"`
}

// waitQuickMatch waits for batch_quickmatch_complete on one socket.
func waitQuickMatch(t *testing.T, o *observer, who string) quickMatchResult {
	t.Helper()
	ev := o.expectEvent(t, who, "batch_quickmatch_complete", anyMatch)
	var result quickMatchResult
	if err := json.Unmarshal(ev.Args[0], &result); err != nil {
		t.Fatalf("batch_quickmatch_complete payload: %v: %s", err, ev.Args[0])
	}
	return result
}

// quickMatchScenarios covers POST /api/items/batch/quickmatch.
func quickMatchScenarios(s *setup) []harness.Scenario {
	const req, inv = "route-r048", "INV-ROUTE-R048-CONTRACT"
	return []harness.Scenario{
		scenario(req, inv, "error", "initial", "quickmatch-requires-admin-and-ids",
			"POST batch/quickmatch answers 403 for a non-admin, 400 for an empty id list and 400 when no listed id exists, without changing an item or emitting batch_quickmatch_complete.",
			srcs(controllerSrc+":763-785", "client/components/modals/BatchQuickMatchModel.vue:109-132", "client/layouts/default.vue:352-363"),
			func(t *testing.T, e *harness.Env) {
				o := observe(t, e, map[string]string{"root": e.Token(t, "root"), "listener": e.Token(t, "listener")})
				rows := snapshot(t, e)
				wantStatus(t, "listener", quickMatch(t, e, e.Token(t, "listener"), []string{e.IDs["saga2"]}, nil), http.StatusForbidden)
				wantStatus(t, "empty ids", quickMatch(t, e, e.Token(t, "root"), nil, nil), http.StatusBadRequest)
				wantStatus(t, "unknown ids", quickMatch(t, e, e.Token(t, "root"), []string{"00000000-0000-4000-8000-000000000000"}, nil), http.StatusBadRequest)
				wantSnapshot(t, e, rows)
				o.none(t, "batch_quickmatch_complete", anyMatch)
			}),
		scenario(req, inv, "normal", "initial", "quickmatch-applies-a-custom-provider-match",
			"For an admin, POST batch/quickmatch of a book with a custom provider answers 200, updates the book title from the provider, downloads the match cover into metadata/items/<id>/cover.png, moves updatedAt, emits item_updated and then batch_quickmatch_complete {success:true,updates:1,unmatched:0} to the caller.",
			srcs(controllerSrc+":786-816", "server/scanner/Scanner.js:37-135", "server/providers/CustomProviderAdapter.js:24-148", coverManagerSrc+":300-345"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["disposable"]
				stub := newMatchProvider(t, map[string]any{
					"publisher": "Quick Press", "description": "Matched description",
					"genres": []string{"Fantasy"},
				})
				slug := "custom-" + createProvider(t, e, "Quick Match Provider", stub.server.URL)
				// The item's folder image is its stored cover after the scans of the
				// subsystem scenarios; clear it so the provider cover is downloaded.
				wantStatus(t, "clear cover", deleteCoverRequest(t, e, e.Token(t, "root"), id), http.StatusOK)
				o := observe(t, e, map[string]string{"root": e.Token(t, "root")})
				before := updatedAt(t, e, id)
				wantStatus(t, "quickmatch", quickMatch(t, e, e.Token(t, "root"), []string{id}, map[string]any{"provider": slug}), http.StatusOK)
				o.itemUpdated(t, e, id, []string{"root"}, nil)
				requireEqual(t, "batch result", waitQuickMatch(t, o, "root"), quickMatchResult{Success: true, Updates: 1})
				want := filepath.Join(coverDir(e, id), "cover.png")
				requireEqual(t, "coverPath", mediaCoverPath(t, e, id), want)
				requireEqual(t, "downloaded cover", string(readDisk(t, want)), string(stub.image))
				wantImageSize(t, "downloaded cover", readDisk(t, want), 32, 24)
				requireEqual(t, "book publisher", mediaRow(t, e, id)["publisher"], "Quick Press")
				requireEqual(t, "book genres", jsonColumn(t, mediaRow(t, e, id), "genres"), []any{"Fantasy"})
				wantMoved(t, e, id, before)
			}),
		scenario(req, inv, "edge", "repeat", "quickmatch-repeat-reports-no-updates",
			"Repeating the same successful quickmatch answers 200 and reports {success:false,updates:0,unmatched:0} without changing the item or emitting item_updated.",
			srcs(controllerSrc+":786-816", "server/scanner/Scanner.js:37-135"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["disposable"]
				stub := newMatchProvider(t, map[string]any{
					"publisher": "Quick Press", "description": "Matched description",
					"genres": []string{"Fantasy"},
				})
				slug := "custom-" + createProvider(t, e, "Repeat Provider", stub.server.URL)
				o := observe(t, e, map[string]string{"root": e.Token(t, "root")})
				before := updatedAt(t, e, id)
				wantStatus(t, "repeat", quickMatch(t, e, e.Token(t, "root"), []string{id}, map[string]any{"provider": slug}), http.StatusOK)
				requireEqual(t, "repeat result", waitQuickMatch(t, o, "root"), quickMatchResult{})
				wantUnchanged(t, e, id, before)
				o.none(t, "item_updated", idOf(id))
			}),
		scenario(req, inv, "normal", "force", "quickmatch-override-replaces-details-and-cover",
			"With overrideCover and overrideDetails a quickmatch of a book that already has a cover and a different title replaces both from the provider and reports one update.",
			srcs(controllerSrc+":768-782", "server/scanner/Scanner.js:42-46", "server/scanner/Scanner.js:63-74"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["novel"]
				stub := newMatchProvider(t, map[string]any{"title": "Overridden Novel", "author": "Ada Example", "publishedYear": "1999"})
				slug := "custom-" + createProvider(t, e, "Override Provider", stub.server.URL)
				o := observe(t, e, map[string]string{"root": e.Token(t, "root")})
				before := updatedAt(t, e, id)
				options := map[string]any{"provider": slug, "overrideCover": true, "overrideDetails": true}
				wantStatus(t, "override", quickMatch(t, e, e.Token(t, "root"), []string{id}, options), http.StatusOK)
				requireEqual(t, "override result", waitQuickMatch(t, o, "root"), quickMatchResult{Success: true, Updates: 1})
				requireEqual(t, "title", mediaRow(t, e, id)["title"], "Overridden Novel")
				requireEqual(t, "coverPath", mediaCoverPath(t, e, id), filepath.Join(coverDir(e, id), "cover.png"))
				requireEqual(t, "cover bytes", string(readDisk(t, filepath.Join(coverDir(e, id), "cover.png"))), string(stub.image))
				wantMoved(t, e, id, before)
			}),
		scenario(req, inv, "edge", "changed", "quickmatch-keeps-an-existing-cover-and-title-without-override",
			"Without overrideCover or overrideDetails, a quickmatch of a book that already has a cover and a title fills only empty fields (here language) and keeps the stored coverPath, cover bytes and title.",
			srcs("server/scanner/Scanner.js:42-46", "server/scanner/Scanner.js:63-74"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["novel"]
				stub := newMatchProvider(t, map[string]any{"title": "Ignored Title", "language": "en"})
				slug := "custom-" + createProvider(t, e, "No Override Provider", stub.server.URL)
				o := observe(t, e, map[string]string{"root": e.Token(t, "root")})
				coverBefore := mediaCoverPath(t, e, id)
				bytesBefore := readDisk(t, coverBefore.(string))
				titleBefore, _ := mediaRow(t, e, id)["title"].(string)
				before := updatedAt(t, e, id)
				wantStatus(t, "no override", quickMatch(t, e, e.Token(t, "root"), []string{id}, map[string]any{"provider": slug}), http.StatusOK)
				requireEqual(t, "result", waitQuickMatch(t, o, "root"), quickMatchResult{Success: true, Updates: 1})
				requireEqual(t, "language", mediaRow(t, e, id)["language"], "en")
				requireEqual(t, "title kept", mediaRow(t, e, id)["title"], titleBefore)
				requireEqual(t, "coverPath", mediaCoverPath(t, e, id), coverBefore)
				requireEqual(t, "cover bytes", string(readDisk(t, coverBefore.(string))), string(bytesBefore))
				wantMoved(t, e, id, before)
			}),
		scenario(req, inv, "edge", "initial", "quickmatch-mixed-ids-process-the-known-item",
			"A batch that lists one known and one unknown id answers 200 and processes the known item only, reporting exactly one updated or unmatched item.",
			srcs(controllerSrc+":786-816"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga2"]
				stub := newMatchProvider(t, map[string]any{"language": "fr"})
				slug := "custom-" + createProvider(t, e, "Mixed Provider", stub.server.URL)
				o := observe(t, e, map[string]string{"root": e.Token(t, "root")})
				wantStatus(t, "mixed", quickMatch(t, e, e.Token(t, "root"), []string{id, "00000000-0000-4000-8000-000000000000"}, map[string]any{"provider": slug}), http.StatusOK)
				requireEqual(t, "mixed batch result", waitQuickMatch(t, o, "root"), quickMatchResult{Success: true, Updates: 1})
				requireEqual(t, "language", mediaRow(t, e, id)["language"], "fr")
			}),
		scenario(req, inv, "error", "failure", "quickmatch-unmatched-provider-changes-nothing",
			"A quickmatch whose provider id does not exist answers 200, leaves the item unchanged and reports {success:false,updates:0,unmatched:1}.",
			srcs(controllerSrc+":794-811", "server/finders/BookFinder.js:381-390"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga2"]
				o := observe(t, e, map[string]string{"root": e.Token(t, "root")})
				before, rows := updatedAt(t, e, id), snapshot(t, e)
				wantStatus(t, "missing provider", quickMatch(t, e, e.Token(t, "root"), []string{id}, map[string]any{"provider": "custom-00000000-0000-4000-8000-000000000000"}), http.StatusOK)
				requireEqual(t, "unmatched result", waitQuickMatch(t, o, "root"), quickMatchResult{Unmatched: 1})
				wantUnchanged(t, e, id, before)
				wantSnapshot(t, e, rows)
			}),
	}
}
