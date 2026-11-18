package item_write

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

const (
	r051          = "route-r051"
	deleteItemSrc = controllerSrc + ":103-149"
)

// freshServer starts a server of its own with quiet file watchers and its own account setup.
func freshServer(t *testing.T) (*harness.Env, *setup) {
	t.Helper()
	e := harness.Start(t, harness.Options{})
	quietWatchers(t, e)
	return e, &setup{}
}

// removalAudience opens the sockets that observe a removal: every kind of account and an unauthenticated one.
func removalAudience(t *testing.T, e *harness.Env, s *setup) *audience {
	t.Helper()
	return connect(t, e, map[string]string{
		"root": e.Token(t, "root"), "listener": e.Token(t, "listener"), "podcastonly": s.token(t, e, "podcastonly"),
		"tagallow": s.token(t, e, "tagallow"), "anonymous": "",
	})
}

// wantItemRemoved requires item_removed {id, libraryId} on each authenticated socket and none on the unauthenticated one.
func wantItemRemoved(t *testing.T, a *audience, e *harness.Env, itemID, libraryID string) {
	t.Helper()
	events := a.receive(t, "item_removed", idOf(itemID), []string{"root", "listener", "podcastonly", "tagallow"}, []string{"anonymous"})
	for name, ev := range events {
		got := payload(t, ev)
		requireEqual(t, name+" item_removed payload", got, map[string]any{"id": itemID, "libraryId": libraryID})
	}
}

// tableDump returns every row of the tables a delete must leave alone or change, by table name.
func tableDump(t *testing.T, e *harness.Env) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, table := range e.Rows(t, "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name") {
		name := table["name"].(string)
		switch name {
		case "sessions", "settings", "users", "devices", "apiKeys": // login state changes with the setup requests
			continue
		case "authors", "series": // the orphan cleanup of a soft delete races with the book destroy (LibraryItem.js:836-842)
			continue
		}
		var parts []string
		for _, row := range e.Rows(t, "SELECT * FROM `"+name+"` ORDER BY 1") {
			parts = append(parts, fmtRow(row))
		}
		out[name] = strings.Join(parts, "\n")
	}
	return out
}

func fmtRow(row map[string]any) string {
	return strings.TrimSpace(strings.ReplaceAll(sprint(row), "\n", " "))
}

func deleteItemScenarios(s *setup) []harness.Scenario {
	const inv = "INV-R051-DELETE-001"
	const errInv = "INV-ROUTE-R051-CONTRACT"
	const removedInv = "INV-SOCKET-OUTGOING-ITEM-REMOVED"
	const w6Inv = "INV-W6-CASCADE-DB-001"
	const w6 = "sub-destructive-cascades-w6"
	const removed = "socket-outgoing-item-removed"
	return []harness.Scenario{
		scenario(r051, errInv, "error", "failure", "delete-authorisation",
			"DELETE item answers 401 without a token, 404 for an unknown id (also for users without delete permission) and 403 for users without delete permission or access to the item, hard or soft; no row, file or event changes.",
			srcs(middlewareSrc, deleteItemSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["novel"]
				a := removalAudience(t, e, s)
				rows, folder := snapshot(t, e), directoryList(t, itemPath(t, e, id))
				wantStatus(t, "anonymous", e.Do(t, harness.Request{Method: http.MethodDelete, Path: "/api/items/" + id}), http.StatusUnauthorized)
				wantStatus(t, "unknown", e.Delete(t, e.Token(t, "listener"), "/api/items/00000000-0000-4000-8000-000000000000"), http.StatusNotFound)
				for _, query := range []string{"", "?hard=1"} {
					for _, token := range []string{e.Token(t, "listener"), s.token(t, e, "updater"), s.token(t, e, "podcastonly"), s.token(t, e, "tagallow")} {
						wantStatus(t, "forbidden"+query, e.Delete(t, token, "/api/items/"+id+query), http.StatusForbidden)
					}
				}
				requireEqual(t, "item folder", directoryList(t, itemPath(t, e, id)), folder)
				wantSnapshot(t, e, rows)
				a.receive(t, "item_removed", nil, nil, []string{"root", "listener", "podcastonly", "tagallow"})
			}),
		scenario(removed, removedInv, "error", "failure", "item-removed-not-emitted-for-rejected-delete",
			"A rejected delete (403) emits no item_removed to any socket.",
			srcs(deleteItemSrc, "server/routers/ApiRouter.js:403"),
			func(t *testing.T, e *harness.Env) {
				a := removalAudience(t, e, s)
				wantStatus(t, "forbidden", e.Delete(t, e.Token(t, "listener"), "/api/items/"+e.IDs["saga2"]), http.StatusForbidden)
				wantStatus(t, "unknown", e.Delete(t, e.Token(t, "root"), "/api/items/00000000-0000-4000-8000-000000000000"), http.StatusNotFound)
				a.receive(t, "item_removed", nil, nil, []string{"root", "listener", "podcastonly", "tagallow"})
			}),
		scenario(r051, inv, "normal", "initial", "delete-soft-cascade",
			"A soft delete by a user with only delete permission answers 200 and removes the item, book, progress of every user, playlist entries (emptied playlist removed, other playlist renumbered), collection entries, the RSS feed and the metadata folder, while the media files stay.",
			srcs(deleteItemSrc, handleDeleteSrc, removeSeriesSrc, removeAuthorSrc, "client/components/modals/item/tabs/Details.vue"),
			func(t *testing.T, _ *harness.Env) {
				e, s := freshServer(t)
				c := buildCascade(t, e, "soft")
				folder := itemPath(t, e, c.ItemID)
				media := directoryList(t, folder)
				facts := factsOf(t, e, c, folder)
				requireEqual(t, "facts before", facts, cascadeFacts{
					Item: 1, Book: 1, Author: 1, Series: 1, BookAuthor: 1, BookSeries: 1, Progress: 2, PlaylistItems: 2, SoloPlaylists: 1, SharedPlaylists: 1,
					Collections: 1, CollectionBooks: 1, Feeds: 1, Shares: 1, Sessions: 1, MetadataDir: true, ItemFolder: true,
				})
				resp := e.Delete(t, s.token(t, e, "deleter"), "/api/items/"+c.ItemID)
				wantStatus(t, "delete", resp, http.StatusOK)
				settle(t, e, c)
				// Author and Series are not asserted for a soft delete: upstream's unawaited book destroy races with
				// the orphan cleanup (LibraryItem.js:836-842); the hard delete scenarios assert the cleanup.
				after := factsOf(t, e, c, folder)
				after.Author, after.Series = 0, 0
				requireEqual(t, "facts after", after, cascadeFacts{
					SharedPlaylists: 1, Collections: 1, Shares: 1, Sessions: 1, ItemFolder: true,
				})
				requireEqual(t, "media files", directoryList(t, folder), media)
				wantStatus(t, "GET deleted item", e.Get(t, e.Token(t, "root"), "/api/items/"+c.ItemID), http.StatusNotFound)
				requireEqual(t, "shared playlist", playlistOrder(t, e, c.SharedPlaylist), []string{"5e33de11-3e94-4c51-87d8-d06d2928ac38:1"})
				collection := e.Get(t, e.Token(t, "root"), "/api/collections/"+c.Collection)
				wantStatus(t, "collection", collection, http.StatusOK)
				if books, _ := collection.Object(t)["books"].([]any); len(books) != 1 || harness.Dig(books, 0, "id") != e.IDs["novel"] {
					t.Fatalf("collection books %v", books)
				}
			}),
		scenario(removed, removedInv, "normal", "initial", "item-removed-audience-and-cascade-events",
			"A delete emits item_removed {id, libraryId} to every authenticated socket (also users without access to the item), then author_removed and series_removed for the emptied author and series, rss_feed_closed to every authenticated socket, playlist_updated and playlist_removed to the playlist owner only; an unauthenticated socket receives nothing.",
			srcs(deleteItemSrc, handleDeleteSrc, removeSeriesSrc, removeAuthorSrc, "server/routers/ApiRouter.js:403", "server/managers/RssFeedManager.js:355-361", "server/models/Playlist.js:172-208"),
			func(t *testing.T, _ *harness.Env) {
				e, s := freshServer(t)
				c := buildCascade(t, e, "events")
				a := removalAudience(t, e, s)
				filter := e.Get(t, e.Token(t, "root"), "/api/libraries/"+e.IDs["library"]+"?include=filterdata")
				wantStatus(t, "filterdata", filter, http.StatusOK)
				if !strings.Contains(string(filter.Body), "Zed Disposable") || !strings.Contains(string(filter.Body), "Disposable Series") {
					t.Fatal("filter data lacks the author or series before the delete")
				}
				// hard delete: the unawaited book destroy of a soft delete races with the author and series
				// cleanup upstream (LibraryItem.js:836-842), the file removal gives it time to finish
				wantStatus(t, "delete", e.Delete(t, e.Token(t, "root"), "/api/items/"+c.ItemID+"?hard=1"), http.StatusOK)
				wantItemRemoved(t, a, e, c.ItemID, e.IDs["library"])
				everyone, none := []string{"root", "listener", "podcastonly", "tagallow"}, []string{"anonymous"}
				author := a.receive(t, "author_removed", idOf(c.AuthorID), everyone, none)
				series := a.receive(t, "series_removed", idOf(c.SeriesID), everyone, none)
				requireEqual(t, "author_removed payload", payload(t, author["root"]), map[string]any{"id": c.AuthorID, "libraryId": e.IDs["library"]})
				requireEqual(t, "series_removed payload", payload(t, series["root"]), map[string]any{"id": c.SeriesID, "libraryId": e.IDs["library"]})
				feed := a.receive(t, "rss_feed_closed", idOf(c.Feed), everyone, none)
				if got := payload(t, feed["podcastonly"]); got["entityId"] != c.ItemID || got["entityType"] != "libraryItem" {
					t.Fatalf("rss_feed_closed payload %v", got)
				}
				updated := a.receive(t, "playlist_updated", idOf(c.SharedPlaylist), []string{"listener"}, []string{"root", "podcastonly", "tagallow", "anonymous"})
				if items, _ := payload(t, updated["listener"])["items"].([]any); len(items) != 1 || harness.Dig(items, 0, "libraryItemId") != e.IDs["saga1"] {
					t.Fatalf("playlist_updated items %v", items)
				}
				gone := a.receive(t, "playlist_removed", idOf(c.SoloPlaylist), []string{"listener"}, []string{"root", "podcastonly", "tagallow", "anonymous"})
				if payload(t, gone["listener"])["userId"] != e.IDs["listener"] {
					t.Fatalf("playlist_removed payload %v", payload(t, gone["listener"]))
				}
				after := e.Get(t, e.Token(t, "root"), "/api/libraries/"+e.IDs["library"]+"?include=filterdata")
				if strings.Contains(string(after.Body), "Zed Disposable") || strings.Contains(string(after.Body), "Disposable Series") {
					t.Fatal("filter data still lists the removed author or series")
				}
				// upstream order: item_removed first, the author and series cleanup after it
				events := a.sockets["root"].Events()
				position := func(name string) int {
					for i := a.marks["root"]; i < len(events); i++ {
						if events[i].Name == name {
							return i
						}
					}
					t.Fatalf("no %s event", name)
					return -1
				}
				if position("item_removed") > position("series_removed") || position("item_removed") > position("author_removed") {
					t.Fatal("item_removed must precede the author and series cleanup events")
				}
			}),
		scenario(r051, inv, "normal", "force", "delete-hard-removes-files",
			"DELETE ?hard=1 additionally removes the item folder with its media files from disk (the parent folder stays) and answers 200.",
			srcs(deleteItemSrc),
			func(t *testing.T, _ *harness.Env) {
				e, _ := freshServer(t)
				id := e.IDs["disposable"]
				folder := itemPath(t, e, id)
				if !strings.HasSuffix(folder, "Zed Disposable/Hard Delete Me") || !pathExists(filepath.Join(folder, "Hard Delete Me.mp3")) {
					t.Fatalf("fixture folder %s", folder)
				}
				a := connect(t, e, map[string]string{"root": e.Token(t, "root"), "listener": e.Token(t, "listener")})
				wantStatus(t, "hard delete", e.Delete(t, e.Token(t, "root"), "/api/items/"+id+"?hard=1"), http.StatusOK)
				if pathExists(folder) {
					t.Fatal("item folder still on disk")
				}
				if !pathExists(filepath.Dir(folder)) {
					t.Fatal("the parent folder was removed")
				}
				if e.Count(t, "SELECT COUNT(*) FROM libraryItems WHERE id = ?", id) != 0 || e.Count(t, "SELECT COUNT(*) FROM books") != 4 {
					t.Fatal("rows remain or other books were removed")
				}
				a.receive(t, "item_removed", idOf(id), []string{"root", "listener"}, nil)
				if _, err := os.Stat(filepath.Join(e.Metadata, "items", id)); err == nil {
					t.Fatal("metadata folder remains")
				}
			}),
		scenario(r051, inv, "normal", "repeat", "delete-repeat-is-404",
			"Deleting an already deleted item answers 404 and changes no row, file or event.",
			srcs(middlewareSrc, deleteItemSrc),
			func(t *testing.T, _ *harness.Env) {
				e, _ := freshServer(t)
				id := e.IDs["disposable"]
				wantStatus(t, "first", e.Delete(t, e.Token(t, "root"), "/api/items/"+id), http.StatusOK)
				a := connect(t, e, map[string]string{"root": e.Token(t, "root")})
				rows, folder := snapshot(t, e), directoryList(t, itemPath2(e, id))
				wantStatus(t, "repeat", e.Delete(t, e.Token(t, "root"), "/api/items/"+id), http.StatusNotFound)
				wantStatus(t, "repeat hard", e.Delete(t, e.Token(t, "root"), "/api/items/"+id+"?hard=1"), http.StatusNotFound)
				wantSnapshot(t, e, rows)
				requireEqual(t, "folder", directoryList(t, itemPath2(e, id)), folder)
				a.receive(t, "item_removed", nil, nil, []string{"root"})
			}),
		scenario(r051, inv, "edge", "changed", "delete-keeps-shared-author-series-and-closes-fixture-feed",
			"Deleting a book whose author and series have other books keeps them (no author_removed or series_removed), removes the fixture progress of two users, the collection entry and the open feed, and renumbers nothing else.",
			srcs(deleteItemSrc, removeSeriesSrc, removeAuthorSrc),
			func(t *testing.T, _ *harness.Env) {
				e, s := freshServer(t)
				id := e.IDs["saga1"]
				book := itemRow(t, e, id)["mediaId"].(string)
				if book != "5e33de11-3e94-4c51-87d8-d06d2928ac38" || e.Count(t, "SELECT COUNT(*) FROM mediaProgresses WHERE mediaItemId = ?", book) != 2 ||
					e.Count(t, "SELECT COUNT(*) FROM feeds WHERE entityId = ?", id) != 1 || e.Count(t, "SELECT COUNT(*) FROM collectionBooks WHERE bookId = ?", book) != 1 {
					t.Fatal("fixture facts of saga1 differ")
				}
				a := removalAudience(t, e, s)
				wantStatus(t, "delete", e.Delete(t, e.Token(t, "root"), "/api/items/"+id), http.StatusOK)
				wantItemRemoved(t, a, e, id, e.IDs["library"])
				a.receive(t, "rss_feed_closed", nil, []string{"root", "podcastonly"}, []string{"anonymous"})
				for _, name := range []string{"author_removed", "series_removed"} {
					a.sockets["root"].None(t, a.marks["root"], name, absentWindow, nil)
				}
				if e.Count(t, "SELECT COUNT(*) FROM series WHERE id = 'e987a322-9111-4f79-8cb9-c25ee714e0d6'") != 1 || authorRow(t, e, "Terry Fixture") == nil {
					t.Fatal("shared series or author was removed")
				}
				if e.Count(t, "SELECT COUNT(*) FROM mediaProgresses WHERE mediaItemId = ?", book) != 0 || e.Count(t, "SELECT COUNT(*) FROM feeds WHERE entityId = ?", id) != 0 ||
					e.Count(t, "SELECT COUNT(*) FROM collectionBooks WHERE bookId = ?", book) != 0 || e.Count(t, "SELECT COUNT(*) FROM collectionBooks") != 1 {
					t.Fatal("cascade rows remain")
				}
				if e.Count(t, "SELECT COUNT(*) FROM playlists") != 2 || e.Count(t, "SELECT COUNT(*) FROM playlistMediaItems") != 4 {
					t.Fatal("unrelated playlists changed")
				}
			}),
		scenario(r051, inv, "edge", "initial", "delete-podcast-cascade",
			"Deleting a podcast removes its episodes, their progress and playlist entries (the emptied playlist is removed), the open feed and the metadata folder.",
			srcs(deleteItemSrc, handleDeleteSrc),
			func(t *testing.T, _ *harness.Env) {
				e, s := freshServer(t)
				id := e.IDs["podcast"]
				a := removalAudience(t, e, s)
				if e.Count(t, "SELECT COUNT(*) FROM podcastEpisodes") != 3 || e.Count(t, "SELECT COUNT(*) FROM mediaProgresses WHERE mediaItemId IN (?, ?, ?)", e.IDs["ep1"], e.IDs["ep2"], e.IDs["ep3"]) != 3 {
					t.Fatal("fixture episodes or progress differ")
				}
				wantStatus(t, "delete", e.Delete(t, e.Token(t, "root"), "/api/items/"+id), http.StatusOK)
				for query, want := range map[string]int{
					"SELECT COUNT(*) FROM podcastEpisodes": 0, "SELECT COUNT(*) FROM podcasts": 0, "SELECT COUNT(*) FROM mediaProgresses WHERE mediaItemType = 'podcastEpisode'": 0,
					"SELECT COUNT(*) FROM playlists WHERE id = '" + e.IDs["podcastPlaylist"] + "'": 0, "SELECT COUNT(*) FROM feeds WHERE entityId = '" + id + "'": 0,
					"SELECT COUNT(*) FROM libraryItems": 5, "SELECT COUNT(*) FROM playlists": 1,
				} {
					if got := e.Count(t, query); got != want {
						t.Fatalf("%s = %d, want %d", query, got, want)
					}
				}
				if pathExists(metadataDir(e, id)) {
					t.Fatal("metadata folder remains")
				}
				a.receive(t, "item_removed", idOf(id), []string{"root", "listener", "podcastonly", "tagallow"}, []string{"anonymous"})
				if got := payload(t, a.receive(t, "item_removed", idOf(id), []string{"root"}, nil)["root"]); got["libraryId"] != e.IDs["podcastLibrary"] {
					t.Fatalf("payload %v", got)
				}
				a.receive(t, "playlist_removed", idOf(e.IDs["podcastPlaylist"]), []string{"listener"}, []string{"root", "anonymous"})
			}),
		scenario(w6, w6Inv, "normal", "initial", "w6-item-delete-table-diff",
			"A soft delete changes only the item, book, author, series, link, progress, playlist, collection-link and feed tables; every other table (shares, playback sessions, collections, other items) is row for row unchanged.",
			srcs(deleteItemSrc, handleDeleteSrc, "server/controllers/LibraryItemController.js:114-149"),
			func(t *testing.T, _ *harness.Env) {
				e, _ := freshServer(t)
				c := buildCascade(t, e, "diff")
				before := tableDump(t, e)
				wantStatus(t, "delete", e.Delete(t, e.Token(t, "root"), "/api/items/"+c.ItemID), http.StatusOK)
				settle(t, e, c)
				after := tableDump(t, e)
				changed := map[string]bool{"libraryItems": true, "books": true, "authors": true, "series": true, "bookAuthors": true, "bookSeries": true, "mediaProgresses": true,
					"playlists": true, "playlistMediaItems": true, "collectionBooks": true, "feeds": true, "feedEpisodes": true}
				for table, rows := range before {
					if (rows != after[table]) != changed[table] {
						t.Fatalf("table %s: changed=%v, want %v", table, rows != after[table], changed[table])
					}
				}
				if len(after) != len(before) {
					t.Fatal("tables appeared or disappeared")
				}
			}),
		scenario(w6, w6Inv, "normal", "repeat", "w6-item-delete-repeat-outcome",
			"The repeat of an item delete answers 404 and leaves every table row for row as after the first delete.",
			srcs(middlewareSrc, "server/controllers/LibraryItemController.js:114-149"),
			func(t *testing.T, _ *harness.Env) {
				e, _ := freshServer(t)
				c := buildCascade(t, e, "repeat")
				wantStatus(t, "first", e.Delete(t, e.Token(t, "root"), "/api/items/"+c.ItemID), http.StatusOK)
				settle(t, e, c)
				after := tableDump(t, e)
				wantStatus(t, "repeat", e.Delete(t, e.Token(t, "root"), "/api/items/"+c.ItemID), http.StatusNotFound)
				wantTables(t, tableDump(t, e), after)
			}),
		scenario(w6, w6Inv, "error", "failure", "w6-rejected-delete-leaves-all-tables",
			"A delete rejected for permission or access leaves every table and the item folder unchanged.",
			srcs(middlewareSrc, "server/controllers/LibraryItemController.js:114-149"),
			func(t *testing.T, _ *harness.Env) {
				e, s := freshServer(t)
				c := buildCascade(t, e, "reject")
				tokens := []string{e.Token(t, "listener"), s.token(t, e, "updater"), s.token(t, e, "podcastonly")} // the first use prepares the accounts
				before, folder := tableDump(t, e), directoryList(t, itemPath(t, e, c.ItemID))
				for _, token := range tokens {
					wantStatus(t, "rejected", e.Delete(t, e.Token(t, "listener"), "/api/items/"+c.ItemID+"?hard=1"), http.StatusForbidden)
					wantStatus(t, "rejected", e.Delete(t, token, "/api/items/"+c.ItemID), http.StatusForbidden)
				}
				wantTables(t, tableDump(t, e), before)
				requireEqual(t, "folder", directoryList(t, itemPath(t, e, c.ItemID)), folder)
			}),
	}
}

// settle waits until the book of a deleted item is gone: upstream destroys it after the response path
// without awaiting (LibraryItem.js:836-842), and its links cascade with it.
func settle(t *testing.T, e *harness.Env, c cascade) {
	t.Helper()
	harness.Eventually(t, eventWait, "the book row to be destroyed", func() bool {
		return e.Count(t, "SELECT COUNT(*) FROM books WHERE id = ?", c.BookID) == 0
	})
}

// wantTables fails the test naming each table whose rows differ.
func wantTables(t *testing.T, got, want map[string]string) {
	t.Helper()
	for table, rows := range want {
		if got[table] != rows {
			t.Errorf("table %s changed", table)
		}
	}
	if len(got) != len(want) {
		t.Errorf("table sets differ")
	}
	if t.Failed() {
		t.FailNow()
	}
}
