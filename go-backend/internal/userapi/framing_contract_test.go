package userapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestBatchMutationResponsesPreserveJSONValues(t *testing.T) {
	t.Run("playlist add", func(t *testing.T) {
		db, h, user := playlistOrderFixture(t)
		seedListReadValues(t, db)

		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/playlists/list-read/batch/add", strings.NewReader(`{"items":[{"libraryItemId":"item-book-second"}]}`))
		req.Header.Set("Content-Type", "application/json")
		if !h.Handle(recorder, req, user) {
			t.Fatal("playlist batch add route not handled")
		}
		payload := assertBatchJSONResponse(t, recorder, []string{"id", "name", "libraryId", "userId", "description", "lastUpdate", "createdAt", "items"})
		if payload["id"] != "list-read" || payload["name"] != "Mixed Queue" || payload["libraryId"] != "lib1" || payload["userId"] != user.ID || payload["description"] != nil {
			t.Fatalf("playlist identity/nullable fields = %#v", payload)
		}
		wantTime := float64(time.Date(2024, time.January, 2, 3, 4, 5, 0, time.UTC).UnixMilli())
		if payload["lastUpdate"] != wantTime || payload["createdAt"] != wantTime {
			t.Fatalf("playlist timestamps = %v/%v, want %v", payload["lastUpdate"], payload["createdAt"], wantTime)
		}
		items, ok := payload["items"].([]any)
		if !ok || len(items) != 3 {
			t.Fatalf("playlist items = %#v, want three ordered items", payload["items"])
		}
		for i, wantID := range []string{"item-book-first", "item-podcast", "item-book-second"} {
			entry, ok := items[i].(map[string]any)
			if !ok {
				t.Fatalf("playlist item %d = %#v", i, items[i])
			}
			gotID, _ := entry["libraryItemId"].(string)
			if gotID != wantID {
				t.Fatalf("playlist item %d ID = %q, want %q", i, gotID, wantID)
			}
			if i != 1 {
				libraryItem, ok := entry["libraryItem"].(map[string]any)
				if !ok || libraryItem["id"] != wantID {
					t.Fatalf("playlist item %d libraryItem = %#v, want ID %q", i, entry["libraryItem"], wantID)
				}
				media, ok := libraryItem["media"].(map[string]any)
				wantBookID, wantTitle := "book-first", "First Shelf Book"
				if i == 2 {
					wantBookID, wantTitle = "book-second", "Second Shelf Book"
				}
				metadata, metadataOK := media["metadata"].(map[string]any)
				if !ok || media["id"] != wantBookID || !metadataOK || metadata["title"] != wantTitle {
					t.Fatalf("playlist item %d book media = %#v, want %s/%s", i, libraryItem["media"], wantBookID, wantTitle)
				}
			} else {
				episode, ok := entry["episode"].(map[string]any)
				parent, parentOK := entry["libraryItem"].(map[string]any)
				if entry["episodeId"] != "episode-one" || !ok || episode["id"] != "episode-one" || episode["title"] != "Pilot Episode" || !parentOK || parent["id"] != "item-podcast" {
					t.Fatalf("podcast playlist item = %#v", entry)
				}
			}
		}
	})

	t.Run("collection batch add", func(t *testing.T) {
		db, h, user := playlistOrderFixture(t)
		seedListReadValues(t, db)
		user.Permissions = json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":false,"update":true}`)

		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/collections/collection-read/batch/add", strings.NewReader(`{"books":["item-book-second"]}`))
		req.Header.Set("Content-Type", "application/json")
		if !h.Handle(recorder, req, user) {
			t.Fatal("collection batch add route not handled")
		}
		payload := assertBatchJSONResponse(t, recorder, []string{"id", "libraryId", "name", "description", "books", "lastUpdate", "createdAt"})
		if payload["id"] != "collection-read" || payload["libraryId"] != "lib1" || payload["name"] != "Reading List" || payload["description"] != nil {
			t.Fatalf("collection identity/nullable fields = %#v", payload)
		}
		wantTime := float64(time.Date(2024, time.January, 2, 3, 4, 5, 0, time.UTC).UnixMilli())
		if payload["lastUpdate"] != wantTime || payload["createdAt"] != wantTime {
			t.Fatalf("collection timestamps = %v/%v, want %v", payload["lastUpdate"], payload["createdAt"], wantTime)
		}
		books, ok := payload["books"].([]any)
		if !ok || len(books) != 2 {
			t.Fatalf("collection books = %#v, want two visible books", payload["books"])
		}
		for i, wantID := range []string{"item-book-second", "item-book-first"} {
			item, ok := books[i].(map[string]any)
			if !ok || item["id"] != wantID {
				t.Fatalf("collection book %d = %#v, want ID %q", i, books[i], wantID)
			}
			media, ok := item["media"].(map[string]any)
			if !ok || media["id"] != strings.TrimPrefix(wantID, "item-") {
				t.Fatalf("collection book %d media = %#v", i, item["media"])
			}
			metadata, ok := media["metadata"].(map[string]any)
			if !ok || metadata["title"] != map[string]string{"item-book-second": "Second Shelf Book", "item-book-first": "First Shelf Book"}[wantID] {
				t.Fatalf("collection book %d metadata = %#v", i, media["metadata"])
			}
		}
	})
}

func TestListWritesPersistAndEmit(t *testing.T) {
	t.Run("playlist metadata reorder add remove delete", func(t *testing.T) {
		db, h, user := playlistOrderFixture(t)
		defer db.Close()
		seedListReadValues(t, db)
		var recipients, events []string
		h.emit = func(userID, event string, _ any) {
			recipients = append(recipients, userID)
			events = append(events, event)
		}
		request := func(method, path, body string) {
			t.Helper()
			w := httptest.NewRecorder()
			if !h.Handle(w, httptest.NewRequest(method, path, strings.NewReader(body)), user) || w.Code != http.StatusOK {
				t.Fatalf("%s %s: status=%d body=%s", method, path, w.Code, w.Body.String())
			}
		}
		request(http.MethodPatch, "/api/playlists/list-read", `{"name":"Updated Queue","description":"Notes","items":[{"libraryItemId":"item-podcast","episodeId":"episode-one"},{"libraryItemId":"item-book-first"}]}`)
		var name, description string
		if err := db.QueryRow(`SELECT name,description FROM playlists WHERE id='list-read'`).Scan(&name, &description); err != nil || name != "Updated Queue" || description != "Notes" {
			t.Fatalf("persisted playlist metadata=(%q,%q), err=%v", name, description, err)
		}
		assertPlaylistOrder := func(want []string) {
			t.Helper()
			rows, err := db.Query(`SELECT mediaItemId FROM playlistMediaItems WHERE playlistId='list-read' ORDER BY "order"`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			for i, id := range want {
				var got string
				if !rows.Next() || rows.Scan(&got) != nil || got != id {
					t.Fatalf("playlist item %d=%q, want %q", i, got, id)
				}
			}
			if rows.Next() {
				t.Fatal("unexpected extra playlist item")
			}
		}
		assertPlaylistOrder([]string{"episode-one", "book-first"})
		request(http.MethodPost, "/api/playlists/list-read/batch/add", `{"items":[{"libraryItemId":"item-book-second"}]}`)
		assertPlaylistOrder([]string{"episode-one", "book-first", "book-second"})
		request(http.MethodDelete, "/api/playlists/list-read/item/item-book-second", "")
		assertPlaylistOrder([]string{"episode-one", "book-first"})
		request(http.MethodDelete, "/api/playlists/list-read", "")
		var playlists, entries int
		if err := db.QueryRow(`SELECT COUNT(*) FROM playlists WHERE id='list-read'`).Scan(&playlists); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM playlistMediaItems WHERE playlistId='list-read'`).Scan(&entries); err != nil {
			t.Fatal(err)
		}
		if playlists != 0 || entries != 0 {
			t.Fatalf("deleted playlist remains: playlists=%d entries=%d", playlists, entries)
		}
		if !slices.Equal(events, []string{"playlist_updated", "playlist_updated", "playlist_updated", "playlist_removed"}) || !slices.Equal(recipients, []string{user.ID, user.ID, user.ID, user.ID}) {
			t.Fatalf("playlist events=%v recipients=%v", events, recipients)
		}
	})

	t.Run("collection metadata and membership delete", func(t *testing.T) {
		db, h, user := playlistOrderFixture(t)
		defer db.Close()
		seedListReadValues(t, db)
		user.Permissions = json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":false,"update":true}`)
		var recipients, events []string
		h.emit = func(userID, event string, _ any) {
			recipients = append(recipients, userID)
			events = append(events, event)
		}
		request := func(method, path, body string) {
			t.Helper()
			w := httptest.NewRecorder()
			if !h.Handle(w, httptest.NewRequest(method, path, strings.NewReader(body)), user) || w.Code != http.StatusOK {
				t.Fatalf("%s %s: status=%d body=%s", method, path, w.Code, w.Body.String())
			}
		}
		request(http.MethodPatch, "/api/collections/collection-read", `{"name":"Updated Reading","description":"Notes"}`)
		var name, description string
		if err := db.QueryRow(`SELECT name,description FROM collections WHERE id='collection-read'`).Scan(&name, &description); err != nil || name != "Updated Reading" || description != "Notes" {
			t.Fatalf("persisted collection metadata=(%q,%q), err=%v", name, description, err)
		}
		request(http.MethodDelete, "/api/collections/collection-read/book/item-book-second", "")
		rows, err := db.Query(`SELECT bookId,"order" FROM collectionBooks WHERE collectionId='collection-read' ORDER BY "order"`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		want := []string{"book-first", "book-denied"}
		for i, id := range want {
			var got string
			var order int
			if !rows.Next() || rows.Scan(&got, &order) != nil || got != id || order != i+1 {
				t.Fatalf("collection entry %d=(%q,%d), want (%q,%d)", i, got, order, id, i+1)
			}
		}
		if rows.Next() {
			t.Fatal("unexpected extra collection entry")
		}
		if !slices.Equal(events, []string{"collection_updated", "collection_updated"}) || !slices.Equal(recipients, []string{"*", "*"}) {
			t.Fatalf("collection events=%v recipients=%v", events, recipients)
		}
	})
}

func assertBatchJSONResponse(t *testing.T, recorder *httptest.ResponseRecorder, fields []string) map[string]any {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("mutation status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := recorder.Header().Get("Content-Length"); got != fmt.Sprint(recorder.Body.Len()) {
		t.Fatalf("Content-Length = %q, want %d", got, recorder.Body.Len())
	}
	if got := recorder.Header().Get("ETag"); got != "" {
		t.Fatalf("mutation unexpectedly set ETag %q", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode mutation response: %v; body=%s", err, recorder.Body.String())
	}
	gotFields := make([]string, 0, len(payload))
	for field := range payload {
		gotFields = append(gotFields, field)
	}
	slices.Sort(gotFields)
	slices.Sort(fields)
	if !slices.Equal(gotFields, fields) {
		t.Fatalf("JSON fields = %v, want exactly %v", gotFields, fields)
	}
	return payload
}
