package user_progress

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	bookmarkCreateSource = "server/controllers/MeController.js:327-358"
	bookmarkUpdateSource = "server/controllers/MeController.js:360-396"
	bookmarkRemoveSource = "server/controllers/MeController.js:398-429"
	bookmarkModelSource  = "server/models/User.js:841-912"
)

func bookmarkPath(item string) string { return "/api/me/item/" + item + "/bookmark" }

// storedBookmarks reads the users.bookmarks JSON column.
func storedBookmarks(t *testing.T, e *harness.Env, userID string) []bookmarkJSON {
	t.Helper()
	rows := e.Rows(t, "SELECT bookmarks FROM users WHERE id = ?", userID)
	if len(rows) != 1 {
		t.Fatalf("user %s not found", userID)
	}
	raw, _ := text(rows[0], "bookmarks")
	var list []bookmarkJSON
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatalf("users.bookmarks %q: %v", raw, err)
	}
	return list
}

// userRow reads the stored bookmarks text and updatedAt of a user.
func userRow(t *testing.T, e *harness.Env, userID string) map[string]any {
	t.Helper()
	rows := e.Rows(t, "SELECT bookmarks, updatedAt FROM users WHERE id = ?", userID)
	if len(rows) != 1 {
		t.Fatalf("user %s not found", userID)
	}
	return rows[0]
}

// postBookmark creates a bookmark and decodes the 200 answer.
func postBookmark(t *testing.T, e *harness.Env, who actor, item string, time float64, title string) bookmarkJSON {
	t.Helper()
	resp := e.Post(t, who.token, bookmarkPath(item), map[string]any{"time": time, "title": title})
	wantStatus(t, resp, http.StatusOK, "create bookmark")
	var bookmark bookmarkJSON
	resp.JSON(t, &bookmark)
	return bookmark
}

// requireBookmarks compares the stored list and the GET /api/me list with want.
func requireBookmarks(t *testing.T, e *harness.Env, who actor, want []bookmarkJSON) {
	t.Helper()
	got := storedBookmarks(t, e, who.id)
	equal := func(list []bookmarkJSON) bool {
		if len(list) != len(want) {
			return false
		}
		for i := range list {
			if list[i] != want[i] {
				return false
			}
		}
		return true
	}
	if !equal(got) {
		t.Fatalf("stored bookmarks %+v, want %+v", got, want)
	}
	var me userJSON
	resp := e.Get(t, who.token, "/api/me")
	wantStatus(t, resp, http.StatusOK, "GET /api/me")
	resp.JSON(t, &me)
	if !equal(me.Bookmarks) {
		t.Fatalf("GET /api/me bookmarks %+v, want %+v", me.Bookmarks, want)
	}
}

func bookmarkScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r113", Invariant: "INV-R113-SAMPLED", Case: "normal", State: "initial", Assertion: "bookmark-create",
			Sources: []string{"server/routers/ApiRouter.js:187", bookmarkCreateSource, bookmarkModelSource},
			Claim:   "POST /api/me/item/:id/bookmark answers the new bookmark {libraryItemId, time, title, createdAt}, appends it to users.bookmarks (also for a podcast item and time 0), and emits user_updated with the bookmarks to the caller only.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				sockets := connectAudiences(t, e, who)
				mark := sockets.mark()
				before := time.Now()
				first := postBookmark(t, e, who, e.IDs["saga1"], 33.5, "Chapter 2")
				after := time.Now()
				if first.LibraryItemID != e.IDs["saga1"] || first.Time != 33.5 || first.Title != "Chapter 2" || first.CreatedAt < before.UnixMilli() || first.CreatedAt > after.UnixMilli() {
					t.Fatalf("created bookmark wrong: %+v", first)
				}
				ev := sockets.ownerOnly(t, mark, "user_updated", userUpdatedOf(who.id))
				if got := decodeUserUpdated(t, ev).Bookmarks; len(got) != 1 || got[0] != first {
					t.Fatalf("user_updated bookmarks %+v, want [%+v]", got, first)
				}
				requireBookmarks(t, e, who, []bookmarkJSON{first})
				podcast := postBookmark(t, e, who, e.IDs["podcast"], 0, "Start")
				if podcast.Time != 0 || podcast.LibraryItemID != e.IDs["podcast"] {
					t.Fatalf("podcast bookmark wrong: %+v", podcast)
				}
				requireBookmarks(t, e, who, []bookmarkJSON{first, podcast})
			},
		},
		{
			Requirement: "route-r113", Invariant: "INV-R113-SAMPLED", Case: "edge", State: "repeat", Assertion: "bookmark-create-same-time",
			Sources: []string{bookmarkModelSource, bookmarkCreateSource},
			Claim:   "Creating a bookmark at an existing time of the same item answers the existing bookmark (same createdAt) with the new title and stores no second entry; the same title again rewrites nothing; the same time on another item is a separate bookmark.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				first := postBookmark(t, e, who, e.IDs["saga1"], 10, "One")
				nextMillisecond()
				renamed := postBookmark(t, e, who, e.IDs["saga1"], 10, "Two")
				if renamed.CreatedAt != first.CreatedAt || renamed.Title != "Two" || renamed.Time != 10 {
					t.Fatalf("existing bookmark must come back renamed: %+v", renamed)
				}
				requireBookmarks(t, e, who, []bookmarkJSON{renamed})
				stored := userRow(t, e, who.id)
				nextMillisecond()
				again := postBookmark(t, e, who, e.IDs["saga1"], 10, "Two")
				if again != renamed {
					t.Fatalf("repeat answer %+v, want %+v", again, renamed)
				}
				sameRow(t, "user row after repeat", stored, userRow(t, e, who.id))
				other := postBookmark(t, e, who, e.IDs["saga2"], 10, "Other item")
				requireBookmarks(t, e, who, []bookmarkJSON{renamed, other})
			},
		},
		{
			Requirement: "route-r114", Invariant: "INV-R114-SAMPLED", Case: "normal", State: "changed", Assertion: "bookmark-update-title",
			Sources: []string{"server/routers/ApiRouter.js:188", bookmarkUpdateSource, bookmarkModelSource},
			Claim:   "PATCH /api/me/item/:id/bookmark with an existing time answers the bookmark with the new title and unchanged createdAt, rewrites it in users.bookmarks, leaves other bookmarks alone and emits user_updated to the caller only; repeating it answers the same.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				first := postBookmark(t, e, who, e.IDs["saga1"], 21, "Good bit")
				second := postBookmark(t, e, who, e.IDs["saga1"], 40.25, "Later")
				sockets := connectAudiences(t, e, who)
				mark := sockets.mark()
				resp := e.Patch(t, who.token, bookmarkPath(e.IDs["saga1"]), map[string]any{"time": 21, "title": "Best bit"})
				wantStatus(t, resp, http.StatusOK, "update bookmark")
				var updated bookmarkJSON
				resp.JSON(t, &updated)
				want := first
				want.Title = "Best bit"
				if updated != want {
					t.Fatalf("updated bookmark %+v, want %+v", updated, want)
				}
				ev := sockets.ownerOnly(t, mark, "user_updated", userUpdatedOf(who.id))
				if got := decodeUserUpdated(t, ev).Bookmarks; len(got) != 2 || got[0] != want || got[1] != second {
					t.Fatalf("user_updated bookmarks %+v", got)
				}
				requireBookmarks(t, e, who, []bookmarkJSON{want, second})
				wantStatus(t, e.Patch(t, who.token, bookmarkPath(e.IDs["saga1"]), map[string]any{"time": 21, "title": "Best bit"}), http.StatusOK, "repeat")
				requireBookmarks(t, e, who, []bookmarkJSON{want, second})
			},
		},
		{
			Requirement: "route-r115", Invariant: "INV-R115-SAMPLED", Case: "normal", State: "changed", Assertion: "bookmark-remove",
			Sources: []string{"server/routers/ApiRouter.js:189", bookmarkRemoveSource, bookmarkModelSource},
			Claim:   "DELETE /api/me/item/:id/bookmark/:time removes only the caller's bookmark of that item and time (the time is parsed as a number, so 21.50 matches 21.5), keeps other times, items and users' bookmarks, answers 200 and emits user_updated to the caller only; a repeat answers 404.",
			Run: func(t *testing.T, e *harness.Env) {
				who, other := newActor(t, e), newActor(t, e)
				theirs := postBookmark(t, e, other, e.IDs["saga1"], 21.5, "theirs")
				keep10 := postBookmark(t, e, who, e.IDs["saga1"], 10, "ten")
				postBookmark(t, e, who, e.IDs["saga1"], 21.5, "twenty-one and a half")
				podcast10 := postBookmark(t, e, who, e.IDs["podcast"], 10, "podcast ten")
				zero := postBookmark(t, e, who, e.IDs["saga1"], 0, "zero")
				sockets := connectAudiences(t, e, who)
				mark := sockets.mark()
				wantStatus(t, e.Delete(t, who.token, bookmarkPath(e.IDs["saga1"])+"/21.50"), http.StatusOK, "delete 21.50")
				ev := sockets.ownerOnly(t, mark, "user_updated", userUpdatedOf(who.id))
				if got := decodeUserUpdated(t, ev).Bookmarks; len(got) != 3 {
					t.Fatalf("user_updated bookmarks %+v", got)
				}
				requireBookmarks(t, e, who, []bookmarkJSON{keep10, podcast10, zero})
				requireBookmarks(t, e, other, []bookmarkJSON{theirs})
				wantStatus(t, e.Delete(t, who.token, bookmarkPath(e.IDs["saga1"])+"/21.5"), http.StatusNotFound, "repeat")
				wantStatus(t, e.Delete(t, who.token, bookmarkPath(e.IDs["saga1"])+"/0"), http.StatusOK, "delete time 0")
				wantStatus(t, e.Delete(t, who.token, bookmarkPath(e.IDs["saga1"])+"/10"), http.StatusOK, "delete 10")
				requireBookmarks(t, e, who, []bookmarkJSON{podcast10})
			},
		},
	}
}
