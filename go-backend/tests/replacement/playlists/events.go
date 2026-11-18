package playlists

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"audiobookshelf-compat/tests/replacement/harness"
)

const (
	eventWait   = 5 * time.Second
	quietWindow = 300 * time.Millisecond
)

// watch is a set of named sockets with the mark each had when the scenario acted.
type watch struct {
	names   []string
	sockets map[string][]*harness.Socket
	marks   map[*harness.Socket]int
}

// listen connects the given users' tokens (name -> token); the owner gets two
// sockets to show every client of the user receives the event. It also adds an
// unauthenticated socket named "anonymous".
func listen(t *testing.T, e *harness.Env, tokens map[string]string, twice string) *watch {
	t.Helper()
	w := &watch{sockets: map[string][]*harness.Socket{}, marks: map[*harness.Socket]int{}}
	for name, token := range tokens {
		w.names = append(w.names, name)
		w.sockets[name] = append(w.sockets[name], e.Socket(t, token))
		if name == twice {
			w.sockets[name] = append(w.sockets[name], e.Socket(t, token))
		}
	}
	w.names = append(w.names, "anonymous")
	w.sockets["anonymous"] = []*harness.Socket{e.Socket(t, "")}
	w.reset()
	return w
}

// reset marks the current position of every socket.
func (w *watch) reset() {
	for _, list := range w.sockets {
		for _, s := range list {
			w.marks[s] = s.Mark()
		}
	}
}

func matchID(id string) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		var p playlist
		return len(args) == 1 && json.Unmarshal(args[0], &p) == nil && p.ID == id
	}
}

// receive waits for the event on every socket of the named user and returns the payload.
func (w *watch) receive(t *testing.T, name, event, id string) playlist {
	t.Helper()
	var first playlist
	for i, s := range w.sockets[name] {
		ev := s.Wait(t, w.marks[s], event, eventWait, matchID(id))
		var p playlist
		if err := json.Unmarshal(ev.Args[0], &p); err != nil {
			t.Fatalf("%s socket: %s payload: %v", name, event, err)
		}
		if i == 0 {
			first = p
		} else if !reflect.DeepEqual(p, first) {
			t.Fatalf("%s sockets got different payloads: %+v and %+v", name, first, p)
		}
	}
	return first
}

// silent asserts the named user's sockets got no such event since the mark.
func (w *watch) silent(t *testing.T, name, event string) {
	t.Helper()
	for _, s := range w.sockets[name] {
		s.None(t, w.marks[s], event, quietWindow, nil)
	}
}

// silentExcept asserts every socket but the named user's got no such event.
func (w *watch) silentExcept(t *testing.T, owner, event string) {
	t.Helper()
	for _, name := range w.names {
		if name != owner {
			w.silent(t, name, event)
		}
	}
}

// silentEverywhere asserts no socket got the event.
func (w *watch) silentEverywhere(t *testing.T, event string) {
	t.Helper()
	w.silentExcept(t, "", event)
}

// count returns how many events of the name the user's first socket got since the mark.
func (w *watch) count(name, event string) int {
	s := w.sockets[name][0]
	n := 0
	for _, ev := range s.Events()[w.marks[s]:] {
		if ev.Name == event {
			n++
		}
	}
	return n
}

// wantPayload asserts an event payload equals the playlist the HTTP route returned.
func wantPayload(t *testing.T, got, want playlist) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("event payload %+v differs from response %+v", got, want)
	}
	for _, entry := range got.Items {
		if entry.LibraryItem.ID == "" || entry.LibraryItem.MediaType == "" {
			t.Fatalf("event items must be expanded: %+v", entry)
		}
	}
}

// scene is an owner with a second user and an administrator, all watched.
type scene struct {
	token string
	w     *watch
}

func newScene(t *testing.T, e *harness.Env, owner string) scene {
	t.Helper()
	token := plainUser(t, e, owner)
	w := listen(t, e, map[string]string{
		owner:    token,
		"other":  plainUser(t, e, "pl-bystander"),
		"root":   e.Token(t, "root"),
		"listen": e.Token(t, "listener"),
	}, owner)
	return scene{token: token, w: w}
}

func eventScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "socket-outgoing-playlist-added", Invariant: "INV-SOCKET-OUTGOING-PLAYLIST-ADDED", Case: "normal", State: "initial", Assertion: "event-added-on-create-reaches-owner-sockets-only",
			Sources: []string{"server/controllers/PlaylistController.js:118", "server/SocketAuthority.js:68-78"},
			Claim:   "Create emits playlist_added with the expanded playlist to every socket of the owner and to no other user, administrator or unauthenticated socket.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				s := newScene(t, e, "pl-ev-add")
				p := createAs(t, e, s.token, "Announced", book(fx.saga1), book(fx.novel))
				wantPayload(t, s.w.receive(t, "pl-ev-add", "playlist_added", p.ID), p)
				s.w.silentExcept(t, "pl-ev-add", "playlist_added")
				s.w.reset()
				podcast := createPodcastAs(t, e, s.token, "Announced episode", episode(fx.podcast, fx.ep1))
				wantPayload(t, s.w.receive(t, "pl-ev-add", "playlist_added", podcast.ID), podcast)
				if len(podcast.Items) != 1 || podcast.Items[0].Episode == nil || podcast.Items[0].Episode.Title != "Pilot Episode" || podcast.Items[0].Episode.Duration <= 0 || podcast.Items[0].Episode.AudioFile == nil || podcast.Items[0].LibraryItem.Media.Metadata.Title != "Fixture Cast" {
					t.Fatalf("podcast event fixture payload wrong: %+v", podcast.Items)
				}
				s.w.silentExcept(t, "pl-ev-add", "playlist_added")
				s.w.silentEverywhere(t, "playlist_updated")
				s.w.silentEverywhere(t, "playlist_removed")
			},
		},
		{
			Requirement: "socket-outgoing-playlist-added", Invariant: "INV-SOCKET-OUTGOING-PLAYLIST-ADDED", Case: "normal", State: "changed", Assertion: "event-added-from-collection",
			Sources: []string{"server/controllers/PlaylistController.js:560"},
			Claim:   "Creating a playlist from a collection emits playlist_added to the caller's sockets only, with the playlist the route returned.",
			Run: func(t *testing.T, e *harness.Env) {
				s := newScene(t, e, "pl-ev-col")
				p := decode(t, call(t, e, s.token, http.MethodPost, "/api/playlists/collection/"+e.IDs["collection"], nil))
				wantPayload(t, s.w.receive(t, "pl-ev-col", "playlist_added", p.ID), p)
				s.w.silentExcept(t, "pl-ev-col", "playlist_added")
			},
		},
		{
			Requirement: "socket-outgoing-playlist-added", Invariant: "INV-SOCKET-OUTGOING-PLAYLIST-ADDED", Case: "edge", State: "repeat", Assertion: "event-added-repeat-and-late-socket",
			Sources: []string{"server/controllers/PlaylistController.js:118", "server/SocketAuthority.js:68-78"},
			Claim:   "Two creates emit two playlist_added events in order, and a socket that connects afterwards gets no replay.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				s := newScene(t, e, "pl-ev-twice")
				first := createAs(t, e, s.token, "Same", book(fx.novel))
				second := createAs(t, e, s.token, "Same", book(fx.novel))
				wantPayload(t, s.w.receive(t, "pl-ev-twice", "playlist_added", first.ID), first)
				wantPayload(t, s.w.receive(t, "pl-ev-twice", "playlist_added", second.ID), second)
				if n := s.w.count("pl-ev-twice", "playlist_added"); n != 2 {
					t.Fatalf("owner got %d playlist_added, want 2", n)
				}
				for _, socket := range s.w.sockets["pl-ev-twice"] {
					var ids []string
					for _, event := range socket.Events()[s.w.marks[socket]:] {
						if event.Name == "playlist_added" {
							var payload playlist
							if len(event.Args) != 1 || json.Unmarshal(event.Args[0], &payload) != nil {
								t.Fatal("invalid playlist_added payload")
							}
							ids = append(ids, payload.ID)
						}
					}
					sameStrings(t, "playlist_added order", ids, []string{first.ID, second.ID})
				}
				late := e.Socket(t, s.token)
				late.None(t, 0, "playlist_added", quietWindow, nil)
			},
		},
		{
			Requirement: "socket-outgoing-playlist-added", Invariant: "INV-SOCKET-OUTGOING-PLAYLIST-ADDED", Case: "error", State: "failure", Assertion: "event-added-not-sent-for-rejected-create",
			Sources: []string{"server/controllers/PlaylistController.js:33-84", "server/controllers/PlaylistController.js:538-540"},
			Claim:   "Rejected creates (invalid body, unknown item, missing token or unknown collection) emit no playlist_added to any socket.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				s := newScene(t, e, "pl-ev-bad")
				wantStatus(t, call(t, e, s.token, http.MethodPost, "/api/playlists", createBody(e, "")), http.StatusBadRequest)
				wantStatus(t, call(t, e, s.token, http.MethodPost, "/api/playlists", createBody(e, "x", book("no-such-item"))), http.StatusBadRequest)
				wantStatus(t, call(t, e, "", http.MethodPost, "/api/playlists", createBody(e, "x", book(fx.novel))), http.StatusUnauthorized)
				wantStatus(t, call(t, e, s.token, http.MethodPost, "/api/playlists/collection/no-such-collection", nil), http.StatusNotFound)
				s.w.silentEverywhere(t, "playlist_added")
			},
		},
		{
			Requirement: "socket-outgoing-playlist-updated", Invariant: "INV-SOCKET-OUTGOING-PLAYLIST-UPDATED", Case: "normal", State: "initial", Assertion: "event-updated-on-patch-add-remove",
			Sources: []string{"server/controllers/PlaylistController.js:253", "server/controllers/PlaylistController.js:336", "server/controllers/PlaylistController.js:381"},
			Claim:   "PATCH, add item and remove item each emit playlist_updated with the route's playlist to the owner's sockets only.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				s := newScene(t, e, "pl-ev-upd")
				p := createAs(t, e, s.token, "Watched", book(fx.saga1), book(fx.novel))
				s.w.reset()
				renamed := decode(t, update(t, e, s.token, p.ID, map[string]any{"name": "Renamed"}))
				wantPayload(t, s.w.receive(t, "pl-ev-upd", "playlist_updated", p.ID), renamed)
				s.w.silentExcept(t, "pl-ev-upd", "playlist_updated")
				s.w.reset()
				added := decode(t, addItem(t, e, s.token, p.ID, book(fx.ebook)))
				wantPayload(t, s.w.receive(t, "pl-ev-upd", "playlist_updated", p.ID), added)
				s.w.silentExcept(t, "pl-ev-upd", "playlist_updated")
				s.w.reset()
				removed := decode(t, removeItem(t, e, s.token, p.ID, fx.saga1, ""))
				wantPayload(t, s.w.receive(t, "pl-ev-upd", "playlist_updated", p.ID), removed)
				s.w.silentExcept(t, "pl-ev-upd", "playlist_updated")
				s.w.silentEverywhere(t, "playlist_removed")
			},
		},
		{
			Requirement: "socket-outgoing-playlist-updated", Invariant: "INV-SOCKET-OUTGOING-PLAYLIST-UPDATED", Case: "normal", State: "changed", Assertion: "event-updated-on-reorder-and-batches",
			Sources: []string{"server/controllers/PlaylistController.js:252-254", "server/controllers/PlaylistController.js:450", "server/controllers/PlaylistController.js:498"},
			Claim:   "A reorder, a batch add and a batch remove that leaves items each emit playlist_updated to the owner with the new order.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				s := newScene(t, e, "pl-ev-batch")
				p := createAs(t, e, s.token, "Batch watched", book(fx.saga1), book(fx.novel))
				s.w.reset()
				reordered := decode(t, update(t, e, s.token, p.ID, map[string]any{"items": []any{book(fx.novel), book(fx.saga1)}}))
				wantPayload(t, s.w.receive(t, "pl-ev-batch", "playlist_updated", p.ID), reordered)
				s.w.silentExcept(t, "pl-ev-batch", "playlist_updated")
				s.w.reset()
				added := decode(t, batchAdd(t, e, s.token, p.ID, book(fx.ebook), book(fx.saga2)))
				wantPayload(t, s.w.receive(t, "pl-ev-batch", "playlist_updated", p.ID), added)
				s.w.silentExcept(t, "pl-ev-batch", "playlist_updated")
				s.w.reset()
				removed := decode(t, batchRemove(t, e, s.token, p.ID, book(fx.novel)))
				wantPayload(t, s.w.receive(t, "pl-ev-batch", "playlist_updated", p.ID), removed)
				s.w.silentExcept(t, "pl-ev-batch", "playlist_updated")
			},
		},
		{
			Requirement: "socket-outgoing-playlist-updated", Invariant: "INV-SOCKET-OUTGOING-PLAYLIST-UPDATED", Case: "edge", State: "repeat", Assertion: "event-updated-not-sent-when-nothing-changed",
			Sources: []string{"server/controllers/PlaylistController.js:192-201", "server/controllers/PlaylistController.js:252-254", "server/controllers/PlaylistController.js:447-451", "server/controllers/PlaylistController.js:491"},
			Claim:   "A PATCH with the current values, a batch add of existing items and a batch remove of absent items answer 200 and emit no playlist_updated.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				s := newScene(t, e, "pl-ev-noop")
				p := createAs(t, e, s.token, "Quiet", book(fx.saga1), book(fx.novel))
				s.w.reset()
				wantStatus(t, update(t, e, s.token, p.ID, map[string]any{"name": "Quiet", "items": []any{book(fx.saga1), book(fx.novel)}}), http.StatusOK)
				wantStatus(t, batchAdd(t, e, s.token, p.ID, book(fx.saga1)), http.StatusOK)
				wantStatus(t, batchRemove(t, e, s.token, p.ID, book(fx.ebook)), http.StatusOK)
				s.w.silentEverywhere(t, "playlist_updated")
				s.w.silentEverywhere(t, "playlist_removed")
			},
		},
		{
			Requirement: "socket-outgoing-playlist-updated", Invariant: "INV-SOCKET-OUTGOING-PLAYLIST-UPDATED", Case: "error", State: "failure", Assertion: "event-updated-not-sent-for-rejected-requests",
			Sources: []string{"server/controllers/PlaylistController.js:575-593", "server/controllers/PlaylistController.js:286-308"},
			Claim:   "Rejected edits (another user, invalid body, duplicate item, unknown item) emit no playlist_updated to anyone, including the owner.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				s := newScene(t, e, "pl-ev-rej")
				p := createAs(t, e, s.token, "Guarded", book(fx.saga1))
				s.w.reset()
				other := plainUser(t, e, "pl-bystander")
				wantStatus(t, update(t, e, other, p.ID, map[string]any{"name": "Mine now"}), http.StatusForbidden)
				wantStatus(t, addItem(t, e, other, p.ID, book(fx.novel)), http.StatusForbidden)
				wantStatus(t, removeItem(t, e, other, p.ID, fx.saga1, ""), http.StatusForbidden)
				wantStatus(t, update(t, e, s.token, p.ID, map[string]any{"name": 5}), http.StatusBadRequest)
				wantStatus(t, addItem(t, e, s.token, p.ID, book(fx.saga1)), http.StatusBadRequest)
				wantStatus(t, addItem(t, e, s.token, p.ID, book("no-such-item")), http.StatusBadRequest)
				wantStatus(t, removeItem(t, e, s.token, p.ID, fx.novel, ""), http.StatusNotFound)
				s.w.silentEverywhere(t, "playlist_updated")
				s.w.silentEverywhere(t, "playlist_removed")
			},
		},
		{
			Requirement: "socket-outgoing-playlist-updated", Invariant: "INV-SOCKET-OUTGOING-PLAYLIST-UPDATED", Case: "edge", State: "changed", Assertion: "event-updated-when-library-item-deleted",
			Sources: []string{"server/routers/ApiRouter.js:383-384", "server/models/Playlist.js:121-158"},
			Claim:   "Deleting a library item that is in a playlist with other items removes it from the playlist and emits playlist_updated, without that item, to the owner only.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				s := newScene(t, e, "pl-ev-item")
				p := createAs(t, e, s.token, "Loses an item", book(fx.novel), book(fx.disposable), book(fx.ebook))
				s.w.reset()
				wantStatus(t, call(t, e, e.Token(t, "root"), http.MethodDelete, "/api/items/"+fx.disposable, nil), http.StatusOK)
				got := s.w.receive(t, "pl-ev-item", "playlist_updated", p.ID)
				sameStrings(t, "event items", got.refs(), []string{fx.novel, fx.ebook})
				s.w.silentExcept(t, "pl-ev-item", "playlist_updated")
				wantBookLinks(t, e, p.ID, fx.novel, fx.ebook)
			},
		},
		{
			Requirement: "socket-outgoing-playlist-removed", Invariant: "INV-SOCKET-OUTGOING-PLAYLIST-REMOVED", Case: "normal", State: "initial", Assertion: "event-removed-on-delete",
			Sources: []string{"server/controllers/PlaylistController.js:270"},
			Claim:   "DELETE emits playlist_removed with the playlist as it was, items included, to the owner's sockets only.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				s := newScene(t, e, "pl-ev-del")
				p := createAs(t, e, s.token, "Going away", book(fx.saga1), book(fx.novel))
				s.w.reset()
				wantStatus(t, call(t, e, s.token, http.MethodDelete, "/api/playlists/"+p.ID, nil), http.StatusOK)
				wantPayload(t, s.w.receive(t, "pl-ev-del", "playlist_removed", p.ID), p)
				s.w.silentExcept(t, "pl-ev-del", "playlist_removed")
				s.w.silentEverywhere(t, "playlist_updated")
			},
		},
		{
			Requirement: "socket-outgoing-playlist-removed", Invariant: "INV-SOCKET-OUTGOING-PLAYLIST-REMOVED", Case: "edge", State: "changed", Assertion: "event-removed-when-last-item-removed",
			Sources: []string{"server/controllers/PlaylistController.js:376-380", "server/controllers/PlaylistController.js:493-497"},
			Claim:   "Removing the last item (single or batch) emits playlist_removed, and no playlist_updated, to the owner.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				s := newScene(t, e, "pl-ev-last")
				single := createAs(t, e, s.token, "Single last", book(fx.novel))
				multi := createAs(t, e, s.token, "Batch last", book(fx.saga1), book(fx.ebook))
				s.w.reset()
				gone := decode(t, removeItem(t, e, s.token, single.ID, fx.novel, ""))
				if got := s.w.receive(t, "pl-ev-last", "playlist_removed", single.ID); got.UserID != single.UserID || len(got.Items) != 0 {
					t.Fatalf("removed payload wrong: %+v (response %+v)", got, gone)
				}
				decode(t, batchRemove(t, e, s.token, multi.ID, book(fx.saga1), book(fx.ebook)))
				s.w.receive(t, "pl-ev-last", "playlist_removed", multi.ID)
				s.w.silentExcept(t, "pl-ev-last", "playlist_removed")
				s.w.silentEverywhere(t, "playlist_updated")
			},
		},
		{
			Requirement: "socket-outgoing-playlist-removed", Invariant: "INV-SOCKET-OUTGOING-PLAYLIST-REMOVED", Case: "edge", State: "repeat", Assertion: "event-removed-when-library-item-deleted",
			Sources: []string{"server/routers/ApiRouter.js:383-384", "server/models/Playlist.js:121-158"},
			Claim:   "Deleting the only library item of a playlist deletes the playlist and emits playlist_removed to its owner only.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				s := newScene(t, e, "pl-ev-gone")
				p := createAs(t, e, s.token, "Only the doomed", book(fx.ebook))
				s.w.reset()
				wantStatus(t, call(t, e, e.Token(t, "root"), http.MethodDelete, "/api/items/"+fx.ebook, nil), http.StatusOK)
				got := s.w.receive(t, "pl-ev-gone", "playlist_removed", p.ID)
				if got.UserID != p.UserID || len(got.Items) != 0 {
					t.Fatalf("removed payload wrong: %+v", got)
				}
				s.w.silentExcept(t, "pl-ev-gone", "playlist_removed")
				noPlaylistRow(t, e, p.ID)
			},
		},
		{
			Requirement: "socket-outgoing-playlist-removed", Invariant: "INV-SOCKET-OUTGOING-PLAYLIST-REMOVED", Case: "error", State: "failure", Assertion: "event-removed-not-sent-for-rejected-delete",
			Sources: []string{"server/controllers/PlaylistController.js:575-593"},
			Claim:   "A delete by another user, an administrator or without a token, and a delete of an unknown playlist, emit no playlist_removed.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				s := newScene(t, e, "pl-ev-keep")
				p := createAs(t, e, s.token, "Stays", book(fx.novel))
				s.w.reset()
				path := "/api/playlists/" + p.ID
				wantStatus(t, call(t, e, plainUser(t, e, "pl-bystander"), http.MethodDelete, path, nil), http.StatusForbidden)
				wantStatus(t, call(t, e, e.Token(t, "root"), http.MethodDelete, path, nil), http.StatusForbidden)
				wantStatus(t, call(t, e, "", http.MethodDelete, path, nil), http.StatusUnauthorized)
				wantStatus(t, call(t, e, s.token, http.MethodDelete, "/api/playlists/no-such-playlist", nil), http.StatusNotFound)
				s.w.silentEverywhere(t, "playlist_removed")
				wantRow(t, e, p)
			},
		},
	}
}
