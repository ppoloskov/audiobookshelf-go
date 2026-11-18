package item_cover

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// matchResult is the body of POST /api/items/:id/match.
type matchResult struct {
	Updated     bool           `json:"updated"`
	LibraryItem map[string]any `json:"libraryItem"`
	Warning     string         `json:"warning"`
}

// match sends POST /api/items/:id/match.
func match(t *testing.T, e *harness.Env, token, itemID string, body any) harness.Response {
	t.Helper()
	return e.Post(t, token, "/api/items/"+itemID+"/match", body)
}

// matchOK sends a match request as root, requires 200 and decodes the result.
func matchOK(t *testing.T, e *harness.Env, itemID string, body any) matchResult {
	t.Helper()
	resp := match(t, e, e.Token(t, "root"), itemID, body)
	wantStatus(t, "match", resp, http.StatusOK)
	var result matchResult
	resp.JSON(t, &result)
	return result
}

// matchScenarios covers POST /api/items/:id/match.
func matchScenarios(s *setup) []harness.Scenario {
	const req, inv = "route-r058", "INV-R058-MATCH-001"
	return []harness.Scenario{
		scenario(req, inv, "error", "initial", "match-auth-and-unknown-item",
			"POST items/:id/match answers 401 without a token, 404 for an unknown item (before the permission checks), 403 for a user without access to the item's library and 403 for a user without the update permission, changing nothing and emitting no item_updated.",
			srcs(controllerSrc+":526-545", middlewareSrc, "server/Auth.js:322"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga2"]
				o := observe(t, e, map[string]string{"root": e.Token(t, "root")})
				before, rows := updatedAt(t, e, id), snapshot(t, e)
				wantStatus(t, "anonymous", match(t, e, "", id, map[string]any{}), http.StatusUnauthorized)
				unknown := "00000000-0000-4000-8000-000000000000"
				wantStatus(t, "unknown as root", match(t, e, e.Token(t, "root"), unknown, map[string]any{}), http.StatusNotFound)
				wantStatus(t, "unknown before permission", match(t, e, e.Token(t, "listener"), unknown, map[string]any{}), http.StatusNotFound)
				wantStatus(t, "no library access", match(t, e, s.token(t, e, "podcastonly"), id, map[string]any{}), http.StatusForbidden)
				wantStatus(t, "no update permission", match(t, e, e.Token(t, "listener"), id, map[string]any{}), http.StatusForbidden)
				wantUnchanged(t, e, id, before)
				wantSnapshot(t, e, rows)
				o.none(t, "item_updated", idOf(id))
			}),
		scenario(req, inv, "normal", "initial", "match-applies-provider-details",
			"An admin match of a book with a custom provider answers 200 {updated:true,libraryItem:expanded item} whose libraryItem reflects the provider values, stores them in the books row, moves updatedAt and emits item_updated.",
			srcs(controllerSrc+":526-545", "server/scanner/Scanner.js:37-135", "server/providers/CustomProviderAdapter.js:24-148"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["novel"]
				stub := newMatchProvider(t, map[string]any{"publisher": "Matched Press", "description": "From the provider"})
				slug := "custom-" + createProvider(t, e, "Match Provider", stub.server.URL)
				o := observe(t, e, map[string]string{"root": e.Token(t, "root"), "anonymous": ""})
				before := updatedAt(t, e, id)
				result := matchOK(t, e, id, map[string]any{"provider": slug})
				if !result.Updated {
					t.Fatal("updated = false")
				}
				requireEqual(t, "response id", result.LibraryItem["id"], id)
				requireEqual(t, "response publisher", harness.Dig(result.LibraryItem, "media", "metadata", "publisher"), "Matched Press")
				requireEqual(t, "response description", harness.Dig(result.LibraryItem, "media", "metadata", "description"), "From the provider")
				requireEqual(t, "books publisher", mediaRow(t, e, id)["publisher"], "Matched Press")
				wantMoved(t, e, id, before)
				o.itemUpdated(t, e, id, []string{"root"}, []string{"anonymous"})
			}),
		scenario(req, inv, "normal", "force", "match-override-replaces-cover-and-details",
			"With overrideCover and overrideDetails a match of a book that already has a cover replaces the cover with the provider image (metadata/items/<id>/cover.png), replaces the title and reports updated true.",
			srcs(controllerSrc+":536-541", "server/scanner/Scanner.js:42-74", coverManagerSrc+":300-345"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["novel"]
				stub := newMatchProvider(t, map[string]any{"title": "Matched Novel", "publisher": "Override Press"})
				slug := "custom-" + createProvider(t, e, "Match Override Provider", stub.server.URL)
				o := observe(t, e, map[string]string{"root": e.Token(t, "root")})
				before := updatedAt(t, e, id)
				result := matchOK(t, e, id, map[string]any{"provider": slug, "overrideCover": true, "overrideDetails": true})
				if !result.Updated {
					t.Fatal("updated = false")
				}
				want := filepath.Join(coverDir(e, id), "cover.png")
				requireEqual(t, "response coverPath", harness.Dig(result.LibraryItem, "media", "coverPath"), want)
				requireEqual(t, "stored cover", string(readDisk(t, want)), string(stub.image))
				requireEqual(t, "books title", mediaRow(t, e, id)["title"], "Matched Novel")
				requireEqual(t, "books publisher", mediaRow(t, e, id)["publisher"], "Override Press")
				wantMoved(t, e, id, before)
				o.itemUpdated(t, e, id, []string{"root"}, nil)
			}),
		scenario(req, inv, "edge", "repeat", "match-repeat-reports-updated-false",
			"Repeating the same match once the item already carries the provider values and cover answers 200 {updated:false,libraryItem:item} without moving updatedAt or emitting item_updated.",
			srcs(controllerSrc+":543-544", "server/scanner/Scanner.js:107-135"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["novel"]
				stub := newMatchProvider(t, map[string]any{"title": "Matched Novel", "publisher": "Override Press"})
				slug := "custom-" + createProvider(t, e, "Match Repeat Provider", stub.server.URL)
				o := observe(t, e, map[string]string{"root": e.Token(t, "root")})
				before := updatedAt(t, e, id)
				result := matchOK(t, e, id, map[string]any{"provider": slug})
				if result.Updated {
					t.Fatal("updated = true on a repeat")
				}
				requireEqual(t, "response libraryItem id", harness.Dig(result.LibraryItem, "id"), id)
				wantUnchanged(t, e, id, before)
				o.none(t, "item_updated", idOf(id))
			}),
		scenario(req, inv, "normal", "changed", "match-reflects-an-earlier-edit",
			"After another user edited the item, a later match answers with a libraryItem that carries both the earlier edit and the new provider values.",
			srcs(controllerSrc+":526-545", "server/scanner/Scanner.js:37-135"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga2"]
				stub := newMatchProvider(t, map[string]any{"publisher": "Changed Press"})
				slug := "custom-" + createProvider(t, e, "Match Changed Provider", stub.server.URL)
				wantStatus(t, "earlier edit", e.Patch(t, e.Token(t, "root"), "/api/items/"+id+"/media", map[string]any{"metadata": map[string]any{"subtitle": "Edited Before Match"}}), http.StatusOK)
				result := matchOK(t, e, id, map[string]any{"provider": slug, "overrideDetails": true})
				if !result.Updated {
					t.Fatal("updated = false")
				}
				requireEqual(t, "response subtitle", harness.Dig(result.LibraryItem, "media", "metadata", "subtitle"), "Edited Before Match")
				requireEqual(t, "response publisher", harness.Dig(result.LibraryItem, "media", "metadata", "publisher"), "Changed Press")
				requireEqual(t, "books subtitle", mediaRow(t, e, id)["subtitle"], "Edited Before Match")
			}),
		scenario(req, inv, "error", "failure", "match-unknown-provider-warns-and-changes-nothing",
			"A match whose provider id does not exist answers 200 with only a warning string, changes no row and emits no item_updated.",
			srcs(controllerSrc+":543-544", "server/finders/BookFinder.js:381-390"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga2"]
				o := observe(t, e, map[string]string{"root": e.Token(t, "root")})
				before, rows := updatedAt(t, e, id), snapshot(t, e)
				result := matchOK(t, e, id, map[string]any{"provider": "custom-00000000-0000-4000-8000-000000000000"})
				if result.Warning == "" || result.Updated || result.LibraryItem != nil {
					t.Fatalf("unknown provider result %+v", result)
				}
				wantUnchanged(t, e, id, before)
				wantSnapshot(t, e, rows)
				o.none(t, "item_updated", idOf(id))
			}),
		scenario(req, inv, "edge", "initial", "match-honours-title-and-author-options",
			"Body options title and author are passed to the provider search instead of the item's stored metadata, and non-string option values are ignored.",
			srcs(controllerSrc+":529-535", "server/finders/BookFinder.js:381-390", "server/providers/CustomProviderAdapter.js:46-70"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["ebook"]
				stub := newMatchProvider(t, map[string]any{"publisher": "Option Press"})
				slug := "custom-" + createProvider(t, e, "Match Options Provider", stub.server.URL)
				result := matchOK(t, e, id, map[string]any{"provider": slug, "title": "Override Title", "author": "Ada Example", "isbn": json.Number("123")})
				if !result.Updated {
					t.Fatal("updated = false")
				}
				query := stub.lastQuery()
				requireEqual(t, "search query", query.Get("query"), "Override Title")
				requireEqual(t, "search author", query.Get("author"), "Ada Example")
				requireEqual(t, "search mediaType", query.Get("mediaType"), "book")
				requireEqual(t, "ignored isbn", query.Get("isbn"), "")
			}),
	}
}
