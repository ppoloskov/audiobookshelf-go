package user_progress

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

// bookmarkRequest sends one of the three bookmark routes for an item.
type bookmarkRequest struct {
	method string
	path   func(item string) string
	body   map[string]any
}

var (
	createRequest = bookmarkRequest{http.MethodPost, bookmarkPath, map[string]any{"time": 5, "title": "t"}}
	updateRequest = bookmarkRequest{http.MethodPatch, bookmarkPath, map[string]any{"time": 5, "title": "t2"}}
	removeRequest = bookmarkRequest{http.MethodDelete, func(item string) string { return bookmarkPath(item) + "/5" }, nil}
)

func (r bookmarkRequest) do(t *testing.T, e *harness.Env, who actor, item string) harness.Response {
	t.Helper()
	request := harness.Request{Method: r.method, Path: r.path(item), Token: who.token}
	if r.body != nil {
		request.JSON = r.body
	}
	return e.Do(t, request)
}

// requireUnchanged requires that a user's stored bookmarks column is as
// snapshot and that no user_updated event arrived. (updatedAt is not compared:
// connecting a socket updates lastSeen and with it the row.)
func requireUnchanged(t *testing.T, e *harness.Env, who actor, snapshot map[string]any, sockets socketsFor, m marks) {
	t.Helper()
	if got := userRow(t, e, who.id)["bookmarks"]; got != snapshot["bookmarks"] {
		t.Fatalf("bookmarks changed from %v to %v", snapshot["bookmarks"], got)
	}
	sockets.nobody(t, m, "user_updated")
}

// accessScenario builds the scenario that runs every access case against one route.
func accessScenario(requirement, invariant, assertion, claim string, source string, request bookmarkRequest, allowed int, prepare func(t *testing.T, e *harness.Env, c accessCase)) harness.Scenario {
	return harness.Scenario{
		Requirement: requirement, Invariant: invariant, Case: "error", State: "failure", Assertion: assertion,
		Sources: []string{source, "server/models/User.js:646-683"},
		Claim:   claim,
		Run: func(t *testing.T, e *harness.Env) {
			set := accessUsers(t, e)
			for _, c := range accessCases(set) {
				if prepare != nil {
					prepare(t, e, c)
				}
				before := userRow(t, e, c.who.id)
				resp := request.do(t, e, c.who, e.IDs[c.item])
				wantStatus(t, resp, c.statusFor(allowed), c.what)
				if c.denied {
					if got := userRow(t, e, c.who.id)["bookmarks"]; got != before["bookmarks"] {
						t.Fatalf("%s: bookmarks changed from %v to %v", c.what, before["bookmarks"], got)
					}
				}
			}
		},
	}
}

func bookmarkErrorScenarios() []harness.Scenario {
	// Setup for update and remove: the caller needs a bookmark on the accessible item.
	prepare := func(t *testing.T, e *harness.Env, c accessCase) {
		t.Helper()
		if !c.denied {
			e.Post(t, c.who.token, bookmarkPath(e.IDs[c.item]), map[string]any{"time": 5, "title": "seed"})
		}
	}
	return []harness.Scenario{
		accessScenario("route-r113", "INV-R113-ACCESS-001", "bookmark-create-access-rules",
			"POST bookmark answers 403 and stores nothing for an item outside the user's libraries, with a tag rule (allow and deny mode) or explicit content the user may not see, and 200 for the accessible items.",
			bookmarkCreateSource, createRequest, http.StatusOK, nil),
		accessScenario("route-r114", "INV-R114-ACCESS-001", "bookmark-update-access-rules",
			"PATCH bookmark answers 403 and stores nothing under the library, tag (allow and deny) and explicit-content rules, and 200 for an accessible item with a bookmark.",
			bookmarkUpdateSource, updateRequest, http.StatusOK, prepare),
		accessScenario("route-r115", "INV-R115-ACCESS-001", "bookmark-remove-access-rules",
			"DELETE bookmark answers 403 and removes nothing under the library, tag (allow and deny) and explicit-content rules, and 200 for an accessible item with a bookmark.",
			bookmarkRemoveSource, removeRequest, http.StatusOK, prepare),
		{
			Requirement: "route-r113", Invariant: "INV-R113-ERROR-001", Case: "error", State: "failure", Assertion: "bookmark-create-rejections",
			Sources: []string{bookmarkCreateSource, "server/utils/index.js:53-55", "server/Server.js:354", "server/Server.js:160"},
			Claim:   "POST bookmark answers 401 unauthenticated, 404 for an unknown item (before any body check), 400 for a missing, null or non-numeric time and for a missing, empty or non-string title; nothing is stored and no event is sent.",
			Run: func(t *testing.T, e *harness.Env) {
				requireUnauthenticated(t, e, http.MethodPost, bookmarkPath(e.IDs["saga1"]), map[string]any{"time": 1, "title": "t"})
				who := newActor(t, e)
				postBookmark(t, e, who, e.IDs["saga1"], 7, "kept")
				snapshot := userRow(t, e, who.id)
				sockets := connectAudiences(t, e, who)
				mark := sockets.mark()
				wantStatus(t, e.Post(t, who.token, bookmarkPath(unknownID), map[string]any{"time": "x", "title": ""}), http.StatusNotFound, "unknown item with invalid body")
				for _, c := range []struct {
					what string
					body map[string]any
				}{
					{"no time", map[string]any{"title": "t"}},
					{"null time", map[string]any{"time": nil, "title": "t"}},
					{"text time", map[string]any{"time": "abc", "title": "t"}},
					{"no title", map[string]any{"time": 5}},
					{"empty title", map[string]any{"time": 5, "title": ""}},
					{"numeric title", map[string]any{"time": 5, "title": 12}},
					{"null title", map[string]any{"time": 5, "title": nil}},
				} {
					wantStatus(t, e.Post(t, who.token, bookmarkPath(e.IDs["saga1"]), c.body), http.StatusBadRequest, c.what)
				}
				requireUnchanged(t, e, who, snapshot, sockets, mark)
			},
		},
		{
			Requirement: "route-r114", Invariant: "INV-R114-ERROR-001", Case: "error", State: "failure", Assertion: "bookmark-update-rejections",
			Sources: []string{bookmarkUpdateSource, "server/Server.js:354", "server/Server.js:160"},
			Claim:   "PATCH bookmark answers 401 unauthenticated, 404 for an unknown item (before the body check), 400 for invalid time or title (before the bookmark lookup) and 404 when no bookmark exists at that time or on that item; nothing changes and no event is sent.",
			Run: func(t *testing.T, e *harness.Env) {
				requireUnauthenticated(t, e, http.MethodPatch, bookmarkPath(e.IDs["saga1"]), map[string]any{"time": 1, "title": "t"})
				who := newActor(t, e)
				postBookmark(t, e, who, e.IDs["saga1"], 7, "kept")
				snapshot := userRow(t, e, who.id)
				sockets := connectAudiences(t, e, who)
				mark := sockets.mark()
				wantStatus(t, e.Patch(t, who.token, bookmarkPath(unknownID), map[string]any{"time": "x", "title": ""}), http.StatusNotFound, "unknown item with invalid body")
				wantStatus(t, e.Patch(t, who.token, bookmarkPath(e.IDs["saga1"]), map[string]any{"time": "abc", "title": "t"}), http.StatusBadRequest, "text time")
				wantStatus(t, e.Patch(t, who.token, bookmarkPath(e.IDs["saga1"]), map[string]any{"time": nil, "title": "t"}), http.StatusBadRequest, "null time")
				wantStatus(t, e.Patch(t, who.token, bookmarkPath(e.IDs["saga1"]), map[string]any{"time": 99, "title": ""}), http.StatusBadRequest, "empty title, no bookmark at 99")
				wantStatus(t, e.Patch(t, who.token, bookmarkPath(e.IDs["saga1"]), map[string]any{"time": 8, "title": "t"}), http.StatusNotFound, "no bookmark at that time")
				wantStatus(t, e.Patch(t, who.token, bookmarkPath(e.IDs["saga2"]), map[string]any{"time": 7, "title": "t"}), http.StatusNotFound, "bookmark of another item")
				requireUnchanged(t, e, who, snapshot, sockets, mark)
			},
		},
		{
			Requirement: "route-r115", Invariant: "INV-R115-ERROR-001", Case: "error", State: "failure", Assertion: "bookmark-remove-rejections",
			Sources: []string{bookmarkRemoveSource, "server/Server.js:354", "server/Server.js:160"},
			Claim:   "DELETE bookmark answers 401 unauthenticated, 404 for an unknown item (before the time check), 400 for a non-numeric time and 404 when no bookmark exists at that time or on that item; nothing changes and no event is sent.",
			Run: func(t *testing.T, e *harness.Env) {
				requireUnauthenticated(t, e, http.MethodDelete, bookmarkPath(e.IDs["saga1"])+"/7", nil)
				who := newActor(t, e)
				postBookmark(t, e, who, e.IDs["saga1"], 7, "kept")
				snapshot := userRow(t, e, who.id)
				sockets := connectAudiences(t, e, who)
				mark := sockets.mark()
				wantStatus(t, e.Delete(t, who.token, bookmarkPath(unknownID)+"/abc"), http.StatusNotFound, "unknown item with invalid time")
				wantStatus(t, e.Delete(t, who.token, bookmarkPath(e.IDs["saga1"])+"/abc"), http.StatusBadRequest, "text time")
				wantStatus(t, e.Delete(t, who.token, bookmarkPath(e.IDs["saga1"])+"/8"), http.StatusNotFound, "no bookmark at that time")
				wantStatus(t, e.Delete(t, who.token, bookmarkPath(e.IDs["saga2"])+"/7"), http.StatusNotFound, "bookmark of another item")
				requireUnchanged(t, e, who, snapshot, sockets, mark)
			},
		},
	}
}
