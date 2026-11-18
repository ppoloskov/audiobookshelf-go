package item_write

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

// cascade holds the ids of the state built around the disposable item.
type cascade struct {
	ItemID, BookID, AuthorID, SeriesID string
	SoloPlaylist, SharedPlaylist       string // the first holds only the item, the second also the saga1 book
	Collection, Feed, FeedSlug, Share  string
	Session                            string
	ListenerID                         string
	RootProgress, ListenerProgress     string
}

// objectID reads the "id" of a response object or of a nested key.
func objectID(t *testing.T, resp harness.Response, key string) string {
	t.Helper()
	object := resp.Object(t)
	if key != "" {
		object, _ = object[key].(map[string]any)
	}
	id, _ := object["id"].(string)
	if id == "" {
		t.Fatalf("response has no id: %.300s", resp.Body)
	}
	return id
}

// buildCascade creates the rows that deleting the disposable item must remove or keep: a series,
// progress of two users, two playlists, a collection, an RSS feed, a share and a playback session.
func buildCascade(t *testing.T, e *harness.Env, slugPrefix string) cascade {
	t.Helper()
	root, listener := e.Token(t, "root"), e.Token(t, "listener")
	c := cascade{ItemID: e.IDs["disposable"], ListenerID: e.IDs["listener"]}
	c.BookID, _ = itemRow(t, e, c.ItemID)["mediaId"].(string)
	wantStatus(t, "series", patchMedia(t, e, root, c.ItemID, seriesBody(seriesEntry("Disposable Series", "1"))), http.StatusOK)
	c.SeriesID, _ = seriesRow(t, e, "Disposable Series")["id"].(string)
	c.AuthorID, _ = authorRow(t, e, "Zed Disposable")["id"].(string)
	progress := map[string]any{"duration": 20.0, "currentTime": 5.0, "progress": 0.25, "isFinished": false}
	wantStatus(t, "root progress", e.Patch(t, root, "/api/me/progress/"+c.ItemID, progress), http.StatusOK)
	wantStatus(t, "listener progress", e.Patch(t, listener, "/api/me/progress/"+c.ItemID, progress), http.StatusOK)
	lib := e.IDs["library"]
	c.SoloPlaylist = objectID(t, e.Post(t, listener, "/api/playlists", map[string]any{"libraryId": lib, "name": "Only disposable", "items": []any{map[string]any{"libraryItemId": c.ItemID}}}), "")
	c.SharedPlaylist = objectID(t, e.Post(t, listener, "/api/playlists", map[string]any{"libraryId": lib, "name": "Disposable and saga", "items": []any{map[string]any{"libraryItemId": c.ItemID}, map[string]any{"libraryItemId": e.IDs["saga1"]}}}), "")
	c.Collection = objectID(t, e.Post(t, root, "/api/collections", map[string]any{"libraryId": lib, "name": "Disposable collection", "books": []string{c.ItemID, e.IDs["novel"]}}), "")
	c.FeedSlug = slugPrefix + "-feed"
	c.Feed = objectID(t, e.Post(t, root, "/api/feeds/item/"+c.ItemID+"/open", map[string]any{"serverAddress": "http://127.0.0.1:1", "slug": c.FeedSlug, "metadataDetails": map[string]any{"preventIndexing": true}}), "feed")
	c.Share = objectID(t, e.Post(t, root, "/api/share/mediaitem", map[string]any{"slug": slugPrefix + "-share", "mediaItemType": "book", "mediaItemId": c.BookID, "expiresAt": 0}), "")
	session := e.Post(t, listener, "/api/items/"+c.ItemID+"/play", map[string]any{"deviceInfo": map[string]any{"clientName": "item-write"}, "supportedMimeTypes": []string{"audio/mpeg"}, "mediaPlayer": "html5", "forceDirectPlay": true})
	wantStatus(t, "play", session, http.StatusOK)
	c.Session = objectID(t, session, "")
	wantStatus(t, "close session", e.Post(t, listener, "/api/session/"+c.Session+"/close", map[string]any{"currentTime": 3.0, "timeListened": 3.0}), http.StatusOK)
	return c
}

// cascadeFacts counts the rows that reference the disposable item.
type cascadeFacts struct {
	Item, Book, Author, Series, BookAuthor, BookSeries, Progress, PlaylistItems, SoloPlaylists, SharedPlaylists int
	Collections, CollectionBooks, Feeds, Shares, Sessions                                                       int
	MetadataDir, ItemFolder                                                                                     bool
}

func factsOf(t *testing.T, e *harness.Env, c cascade, folder string) cascadeFacts {
	t.Helper()
	count := func(query string, args ...any) int { return e.Count(t, query, args...) }
	return cascadeFacts{
		Item: count("SELECT COUNT(*) FROM libraryItems WHERE id = ?", c.ItemID), Book: count("SELECT COUNT(*) FROM books WHERE id = ?", c.BookID),
		Author: count("SELECT COUNT(*) FROM authors WHERE id = ?", c.AuthorID), Series: count("SELECT COUNT(*) FROM series WHERE id = ?", c.SeriesID),
		BookAuthor: count("SELECT COUNT(*) FROM bookAuthors WHERE bookId = ?", c.BookID), BookSeries: count("SELECT COUNT(*) FROM bookSeries WHERE bookId = ?", c.BookID),
		Progress:      count("SELECT COUNT(*) FROM mediaProgresses WHERE mediaItemId = ?", c.BookID),
		PlaylistItems: count("SELECT COUNT(*) FROM playlistMediaItems WHERE mediaItemId = ?", c.BookID),
		SoloPlaylists: count("SELECT COUNT(*) FROM playlists WHERE id = ?", c.SoloPlaylist), SharedPlaylists: count("SELECT COUNT(*) FROM playlists WHERE id = ?", c.SharedPlaylist),
		Collections: count("SELECT COUNT(*) FROM collections WHERE id = ?", c.Collection), CollectionBooks: count("SELECT COUNT(*) FROM collectionBooks WHERE bookId = ?", c.BookID),
		Feeds: count("SELECT COUNT(*) FROM feeds WHERE id = ?", c.Feed), Shares: count("SELECT COUNT(*) FROM mediaItemShares WHERE id = ?", c.Share),
		Sessions:    count("SELECT COUNT(*) FROM playbackSessions WHERE id = ?", c.Session),
		MetadataDir: pathExists(metadataDir(e, c.ItemID)), ItemFolder: pathExists(folder),
	}
}

func metadataDir(e *harness.Env, itemID string) string { return e.Metadata + "/items/" + itemID }
