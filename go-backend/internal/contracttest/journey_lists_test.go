package contracttest

import (
	"fmt"
	"testing"
)

// TestJourneyLists is the lists and me domain's journey (ROADMAP F8d): the
// collection and playlist writes the web client sends, the playback sessions
// it deletes, the listener's own sessions, progress, continue-listening,
// password and e-reader device routes. It runs on its own fresh install and is
// recorded into contracts/shapes/lists.json, the same way as TestJourneyAdmin
// (journey_admin_test.go). To record it against TS:
//
//	node go-backend/tools/contractfixture/serve-upstream.cjs   # prints ROOT and READY
//	cd go-backend && go test ./internal/contracttest -run '^TestJourneyLists$' -record \
//	    -upstream=<READY URL> -upstream-root=<ROOT>
//
// then stop the serve script.
//
// Collections need update permission (TS CollectionController.middleware), which
// the listener does not have, so the collection writes are root's and the
// listener gets the 403. Playlists are the listener's: TS checks ownership, not
// permission, for them. The sessions the journey deletes are its own: each one
// is a play the journey started (TS remaps a local session's id to a UUID the
// client never sees, so local sessions are not used here).
const listsShapesFile = "../../contracts/shapes/lists.json"

func TestJourneyLists(t *testing.T) {
	runJourney(t, listsShapesFile, nil, journeyListsSteps)
}

// journeyListsSteps is the lists and me journey, in order. Steps create what
// later steps change or delete, so the order must not change.
func journeyListsSteps(_ *testing.T, in *Install, c *journeyCtx) []journeyStep {
	ids := in.IDs
	saga1, saga2, novel := ids["saga1"], ids["saga2"], ids["novel"]
	library := ids["library"]
	playlistCommute := ids["playlist"]
	const noSuchID = "00000000-0000-4000-8000-000000000000"
	collectionID := func(c *journeyCtx) string { return c.s("collection", "id") }
	playlistID := func(c *journeyCtx) string { return c.s("playlist", "id") }
	listenerID := func(c *journeyCtx) string { return c.listenerID }

	return []journeyStep{
		// Sign in. The finisher is a second regular user, for the step that
		// shows a user cannot change another user's playlist.
		{id: "login.root", method: "POST", route: "/login", body: in.Users["root"], headers: map[string]string{"X-Return-Tokens": "true"}, loginAs: "root"},
		{id: "login.listener", method: "POST", route: "/login", body: in.Users["listener"], headers: map[string]string{"X-Return-Tokens": "true"}, loginAs: "listener"},
		{id: "login.finisher", method: "POST", route: "/login", body: in.Users["finisher"], headers: map[string]string{"X-Return-Tokens": "true"}, loginAs: "finisher"},

		// Collections. The listener has no update or delete permission.
		{id: "forbidden.collections.create", who: "listener", method: "POST", route: "/api/collections", body: map[string]any{
			"name": "Trial Forbidden", "libraryId": library, "books": []any{saga1},
		}},
		{id: "collections.create", who: "root", method: "POST", route: "/api/collections", body: map[string]any{
			"name": "Trial Collection", "libraryId": library, "books": []any{saga1},
		}, capture: "collection"},
		{id: "collections.get", who: "root", routeFn: func(c *journeyCtx) string { return "/api/collections/" + collectionID(c) + "?include=rssfeed" }},
		{id: "forbidden.collections.update", who: "listener", method: "PATCH", routeFn: func(c *journeyCtx) string { return "/api/collections/" + collectionID(c) }, body: map[string]any{"name": "Trial Forbidden"}},
		{id: "forbidden.collections.delete", who: "listener", method: "DELETE", routeFn: func(c *journeyCtx) string { return "/api/collections/" + collectionID(c) }},
		{id: "collections.book.add", who: "root", method: "POST", routeFn: func(c *journeyCtx) string { return "/api/collections/" + collectionID(c) + "/book" }, body: map[string]any{"id": saga2}},
		{id: "collections.book.add.duplicate", who: "root", method: "POST", routeFn: func(c *journeyCtx) string { return "/api/collections/" + collectionID(c) + "/book" }, body: map[string]any{"id": saga2}},
		{id: "collections.update.books", who: "root", method: "PATCH", routeFn: func(c *journeyCtx) string { return "/api/collections/" + collectionID(c) }, body: map[string]any{"books": []any{saga2, saga1}}},
		{id: "collections.update", who: "root", method: "PATCH", routeFn: func(c *journeyCtx) string { return "/api/collections/" + collectionID(c) }, body: map[string]any{
			"name": "Trial Collection Renamed", "description": "Trial description",
		}},
		{id: "collections.batch.add", who: "root", method: "POST", routeFn: func(c *journeyCtx) string { return "/api/collections/" + collectionID(c) + "/batch/add" }, body: map[string]any{"books": []any{novel}}},
		{id: "collections.batch.remove", who: "root", method: "POST", routeFn: func(c *journeyCtx) string { return "/api/collections/" + collectionID(c) + "/batch/remove" }, body: map[string]any{"books": []any{novel}}},
		{id: "collections.book.remove", who: "root", method: "DELETE", routeFn: func(c *journeyCtx) string { return "/api/collections/" + collectionID(c) + "/book/" + saga2 }},
		{id: "playlists.from-collection", who: "listener", method: "POST", routeFn: func(c *journeyCtx) string { return "/api/playlists/collection/" + collectionID(c) }, capture: "collectionPlaylist"},

		// Playlists. The listener owns Commute (the fixture's playlist); the
		// finisher is not its owner and gets 403 for both writes.
		{id: "playlists.create", who: "listener", method: "POST", route: "/api/playlists", body: map[string]any{
			"name": "Trial Playlist", "libraryId": library,
			"items": []any{map[string]any{"libraryItemId": saga1, "episodeId": nil}, map[string]any{"libraryItemId": novel, "episodeId": nil}},
		}, capture: "playlist"},
		{id: "playlists.get", who: "listener", routeFn: func(c *journeyCtx) string { return "/api/playlists/" + playlistID(c) }},
		{id: "forbidden.playlists.update.other", who: "finisher", method: "PATCH", route: "/api/playlists/" + playlistCommute, body: map[string]any{"name": "Trial Stolen"}},
		{id: "forbidden.playlists.delete.other", who: "finisher", method: "DELETE", route: "/api/playlists/" + playlistCommute},
		{id: "playlists.update", who: "listener", method: "PATCH", routeFn: func(c *journeyCtx) string { return "/api/playlists/" + playlistID(c) }, body: map[string]any{
			"name": "Trial Playlist Renamed", "description": "Trial playlist description",
		}},
		{id: "playlists.update.items", who: "listener", method: "PATCH", routeFn: func(c *journeyCtx) string { return "/api/playlists/" + playlistID(c) }, body: map[string]any{
			"items": []any{map[string]any{"libraryItemId": novel, "episodeId": nil}, map[string]any{"libraryItemId": saga1, "episodeId": nil}},
		}},
		{id: "playlists.batch.add", who: "listener", method: "POST", routeFn: func(c *journeyCtx) string { return "/api/playlists/" + playlistID(c) + "/batch/add" }, body: map[string]any{
			"items": []any{map[string]any{"libraryItemId": saga2, "episodeId": nil}},
		}},
		{id: "playlists.batch.remove", who: "listener", method: "POST", routeFn: func(c *journeyCtx) string { return "/api/playlists/" + playlistID(c) + "/batch/remove" }, body: map[string]any{
			"items": []any{map[string]any{"libraryItemId": saga1, "episodeId": nil}},
		}},
		{id: "playlists.delete", who: "listener", method: "DELETE", routeFn: func(c *journeyCtx) string { return "/api/playlists/" + playlistID(c) }},
		{id: "playlists.delete.from-collection", who: "listener", method: "DELETE", routeFn: func(c *journeyCtx) string {
			return "/api/playlists/" + c.s("collectionPlaylist", "id")
		}},
		{id: "collections.delete", who: "root", method: "DELETE", routeFn: func(c *journeyCtx) string { return "/api/collections/" + collectionID(c) }},

		// Playback sessions. Each session the journey deletes is one it played:
		// the first is closed (saved) and deleted by root, the batch deletes a
		// closed session and an open one. The listener may not delete them.
		{id: "series.list", who: "listener", route: "/api/libraries/" + library + "/series?limit=20&page=0&sort=name&desc=0&minified=1", capture: "seriesList"},
		{id: "play.open", who: "listener", method: "POST", route: "/api/items/" + saga1 + "/play", body: webPlayBody(), capture: "openSession"},
		{id: "sessions.open.get", who: "root", route: "/api/sessions/open"},
		{id: "forbidden.sessions.open", who: "listener", route: "/api/sessions/open"},
		{id: "session.close", who: "listener", method: "POST", routeFn: func(c *journeyCtx) string { return "/api/session/" + c.s("openSession", "id") + "/close" }, body: map[string]any{"currentTime": 5, "timeListened": 5}},
		{id: "forbidden.sessions.delete", who: "listener", method: "DELETE", routeFn: func(c *journeyCtx) string { return "/api/sessions/" + c.s("openSession", "id") }},
		{id: "sessions.delete", who: "root", method: "DELETE", routeFn: func(c *journeyCtx) string { return "/api/sessions/" + c.s("openSession", "id") }},
		{id: "play.open.second", who: "listener", method: "POST", route: "/api/items/" + novel + "/play", body: webPlayBody(), capture: "closedSession"},
		{id: "session.close.second", who: "listener", method: "POST", routeFn: func(c *journeyCtx) string { return "/api/session/" + c.s("closedSession", "id") + "/close" }, body: map[string]any{"currentTime": 3, "timeListened": 3}},
		{id: "play.open.third", who: "listener", method: "POST", route: "/api/items/" + saga2 + "/play", body: webPlayBody(), capture: "openSession2"},
		{id: "forbidden.sessions.batch", who: "listener", method: "POST", route: "/api/sessions/batch/delete", bodyFn: func(c *journeyCtx) any {
			return map[string]any{"sessions": []any{c.s("closedSession", "id")}}
		}},
		{id: "sessions.batch.delete", who: "root", method: "POST", route: "/api/sessions/batch/delete", bodyFn: func(c *journeyCtx) any {
			return map[string]any{"sessions": []any{c.s("closedSession", "id"), c.s("openSession2", "id")}}
		}},

		// The listener's own sessions. The second login is the session deleted;
		// the journey's first login stays.
		{id: "login.listener.second", method: "POST", route: "/login", body: in.Users["listener"]},
		{id: "me.sessions.list", who: "listener", route: "/api/me/sessions?page=0&itemsPerPage=10", capture: "mySessions"},
		{id: "me.sessions.delete", who: "listener", method: "DELETE", routeFn: func(c *journeyCtx) string {
			return "/api/me/sessions/" + newestSessionID(c, "mySessions")
		}},
		{id: "me.sessions.delete.again", who: "listener", method: "DELETE", routeFn: func(c *journeyCtx) string {
			return "/api/me/sessions/" + newestSessionID(c, "mySessions")
		}},

		// Progress: write the listener's progress on novel, read it, delete it by
		// its id, then read it again (gone).
		{id: "progress.write", who: "listener", method: "PATCH", route: "/api/me/progress/" + novel, body: map[string]any{"currentTime": 12, "isFinished": false}},
		{id: "progress.read", who: "listener", route: "/api/me/progress/" + novel, capture: "novelProgress"},
		{id: "progress.delete", who: "listener", method: "DELETE", routeFn: func(c *journeyCtx) string { return "/api/me/progress/" + c.s("novelProgress", "id") }},
		{id: "progress.read.after-delete", who: "listener", route: "/api/me/progress/" + novel},

		// Continue listening: hide the series, show it again, and a series that
		// does not exist.
		{id: "series.remove-from-continue", who: "listener", routeFn: func(c *journeyCtx) string {
			return "/api/me/series/" + c.seriesID("Long Saga") + "/remove-from-continue-listening"
		}},
		{id: "series.readd-to-continue", who: "listener", routeFn: func(c *journeyCtx) string {
			return "/api/me/series/" + c.seriesID("Long Saga") + "/readd-to-continue-listening"
		}},
		{id: "series.remove-from-continue.missing", who: "listener", route: "/api/me/series/" + noSuchID + "/remove-from-continue-listening"},

		// Password: a wrong current password, then change and change back. The
		// change ends the listener's sessions, so the journey signs in again.
		{id: "password.wrong", who: "listener", method: "PATCH", route: "/api/me/password", body: map[string]any{
			"password": "trial-not-the-password", "newPassword": "trial-listener-changed",
		}},
		{id: "password.change", who: "listener", method: "PATCH", route: "/api/me/password", body: map[string]any{
			"password": "trial-listener-password", "newPassword": "trial-listener-changed",
		}},
		{id: "login.listener.changed", method: "POST", route: "/login", body: map[string]any{"username": "trial-listener", "password": "trial-listener-changed"}, headers: map[string]string{"X-Return-Tokens": "true"}, loginAs: "listener"},
		{id: "password.change.back", who: "listener", method: "PATCH", route: "/api/me/password", body: map[string]any{
			"password": "trial-listener-changed", "newPassword": "trial-listener-password",
		}},
		{id: "login.listener.restored", method: "POST", route: "/login", body: in.Users["listener"], headers: map[string]string{"X-Return-Tokens": "true"}, loginAs: "listener"},

		// E-reader devices: the user's own device, a duplicate name in one
		// request (the client's error text check), and removal.
		{id: "ereader.user.add", who: "listener", method: "POST", route: "/api/me/ereader-devices", bodyFn: func(c *journeyCtx) any {
			return map[string]any{"ereaderDevices": []any{userEReaderDevice(listenerID(c))}}
		}},
		{id: "ereader.user.duplicate", who: "listener", method: "POST", route: "/api/me/ereader-devices", bodyFn: func(c *journeyCtx) any {
			d := userEReaderDevice(listenerID(c))
			return map[string]any{"ereaderDevices": []any{d, d}}
		}},
		{id: "ereader.user.remove", who: "listener", method: "POST", route: "/api/me/ereader-devices", body: map[string]any{"ereaderDevices": []any{}}},
	}
}

// webPlayBody is the play request the web client sends for a book.
func webPlayBody() map[string]any {
	return map[string]any{
		"deviceInfo": map[string]any{"clientName": "Abs Web", "clientVersion": "2.37.2"}, "mediaPlayer": "html5",
		"forceDirectPlay": false, "forceTranscode": false,
		"supportedMimeTypes": []any{"audio/flac", "audio/mpeg", "audio/mp4", "audio/ogg", "audio/aac", "audio/webm"},
	}
}

// userEReaderDevice is the device the account modal sends for the user's own
// e-reader: specificUsers with only that user.
func userEReaderDevice(userID string) map[string]any {
	return map[string]any{
		"name": "trial-listener-kindle", "email": "trial-listener@example.com",
		"availabilityOption": "specificUsers", "users": []any{userID},
	}
}

// newestSessionID is the id of the session with the latest createdAt in the
// listing captured as name: the one the journey logged in last.
func newestSessionID(c *journeyCtx, name string) string {
	list, _ := c.v(name, "sessions").([]any)
	best, bestAt := "", -1.0
	for _, e := range list {
		m, _ := e.(map[string]any)
		at, _ := m["createdAt"].(float64)
		if at > bestAt {
			best, bestAt = fmt.Sprint(m["id"]), at
		}
	}
	return best
}
