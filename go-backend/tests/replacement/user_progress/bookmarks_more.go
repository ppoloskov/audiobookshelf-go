package user_progress

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

func bookmarkMoreScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r112", Invariant: "INV-R112-CONTRACT-001", Case: "error", State: "repeat", Assertion: "remove-progress-twice",
			Sources: []string{removeSource},
			Claim:   "Deleting the same progress id a second time answers 404, emits nothing and leaves the user's other rows alone.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				seedBook(t, e, who)
				patchOK(t, e, who, e.IDs["novel"], "", map[string]any{"currentTime": 3, "duration": 45, "progress": 3.0 / 45})
				id, _ := text(progressRow(t, e, who.id, saga1Book), "id")
				wantStatus(t, e.Delete(t, who.token, "/api/me/progress/"+id), http.StatusOK, "first delete")
				sockets := connectAudiences(t, e, who)
				mark := sockets.mark()
				wantStatus(t, e.Delete(t, who.token, "/api/me/progress/"+id), http.StatusNotFound, "second delete")
				sockets.nobody(t, mark, "user_updated")
				if rows := progressRows(t, e, who.id); len(rows) != 1 || rows[0]["mediaItemId"] != novelBook {
					t.Fatalf("only the novel row must remain: %v", rows)
				}
			},
		},
		{
			Requirement: "route-r113", Invariant: "INV-R113-SAMPLED", Case: "edge", State: "changed", Assertion: "bookmark-create-after-remove",
			Sources: []string{bookmarkCreateSource, bookmarkModelSource},
			Claim:   "A bookmark created at a time whose earlier bookmark was removed is a new entry with a new createdAt and the new title.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				first := postBookmark(t, e, who, e.IDs["saga1"], 12.5, "first")
				wantStatus(t, e.Delete(t, who.token, bookmarkPath(e.IDs["saga1"])+"/12.5"), http.StatusOK, "delete")
				requireBookmarks(t, e, who, []bookmarkJSON{})
				nextMillisecond()
				second := postBookmark(t, e, who, e.IDs["saga1"], 12.5, "second")
				if second.CreatedAt <= first.CreatedAt || second.Title != "second" {
					t.Fatalf("recreated bookmark must be new: %+v after %+v", second, first)
				}
				requireBookmarks(t, e, who, []bookmarkJSON{second})
			},
		},
		{
			Requirement: "route-r114", Invariant: "INV-R114-SAMPLED", Case: "normal", State: "initial", Assertion: "bookmark-update-seeded",
			Sources: []string{bookmarkUpdateSource, bookmarkModelSource},
			Claim:   "On the fixture state PATCH retitles the listener's seeded bookmark, keeps its createdAt and its other bookmark, and answers the bookmark.",
			Run: func(t *testing.T, e *harness.Env) {
				token := actor{name: "trial-listener", id: e.IDs["listener"], token: e.Token(t, "listener")}
				want := bookmarkJSON{LibraryItemID: e.IDs["saga1"], Time: 21, Title: "Renamed bit", CreatedAt: 1793709987082}
				resp := e.Patch(t, token.token, bookmarkPath(e.IDs["saga1"]), map[string]any{"time": 21, "title": "Renamed bit"})
				wantStatus(t, resp, http.StatusOK, "update seeded bookmark")
				var got bookmarkJSON
				resp.JSON(t, &got)
				if got != want {
					t.Fatalf("answer %+v, want %+v", got, want)
				}
				requireBookmarks(t, e, token, []bookmarkJSON{want, {LibraryItemID: e.IDs["podcast"], Time: 5, Title: "Cast bookmark", CreatedAt: 1793709988272}})
			},
		},
		{
			Requirement: "route-r114", Invariant: "INV-R114-SAMPLED", Case: "edge", State: "repeat", Assertion: "bookmark-update-unicode-and-zero",
			Sources: []string{bookmarkUpdateSource, bookmarkModelSource},
			Claim:   "A bookmark at time 0 can be retitled with a non-ASCII title, stored and answered as sent, and repeating the same update answers the same bookmark.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				created := postBookmark(t, e, who, e.IDs["saga1"], 0, "start")
				want := created
				want.Title = "Kapitel Ünïcode 章 \U0001F4D6"
				for i := 0; i < 2; i++ {
					resp := e.Patch(t, who.token, bookmarkPath(e.IDs["saga1"]), map[string]any{"time": 0, "title": want.Title})
					wantStatus(t, resp, http.StatusOK, "update")
					var got bookmarkJSON
					resp.JSON(t, &got)
					if got != want {
						t.Fatalf("answer %d: %+v, want %+v", i, got, want)
					}
					requireBookmarks(t, e, who, []bookmarkJSON{want})
				}
			},
		},
		{
			Requirement: "route-r115", Invariant: "INV-R115-SAMPLED", Case: "normal", State: "initial", Assertion: "bookmark-remove-seeded",
			Sources: []string{bookmarkRemoveSource, bookmarkModelSource},
			Claim:   "On the fixture state DELETE removes the listener's seeded podcast bookmark and keeps the other seeded bookmark.",
			Run: func(t *testing.T, e *harness.Env) {
				token := actor{name: "trial-listener", id: e.IDs["listener"], token: e.Token(t, "listener")}
				before := storedBookmarks(t, e, token.id)
				if len(before) != 2 || before[1].LibraryItemID != e.IDs["podcast"] || before[1].Time != 5 || before[1].Title != "Cast bookmark" || before[1].CreatedAt != 1793709988272 {
					t.Fatalf("seeded bookmarks wrong: %+v", before)
				}
				wantStatus(t, e.Delete(t, token.token, bookmarkPath(e.IDs["podcast"])+"/5"), http.StatusOK, "delete seeded bookmark")
				requireBookmarks(t, e, token, before[:1])
			},
		},
		{
			Requirement: "route-r115", Invariant: "INV-R115-SAMPLED", Case: "edge", State: "repeat", Assertion: "bookmark-remove-numeric-forms",
			Sources: []string{bookmarkRemoveSource, bookmarkModelSource},
			Claim:   "A negative or fractional time is matched numerically (-3, 7.250 for 7.25), the second delete of the same time answers 404 and the other bookmarks stay.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				postBookmark(t, e, who, e.IDs["saga1"], -3, "negative")
				quarter := postBookmark(t, e, who, e.IDs["saga1"], 7.25, "quarter")
				keep := postBookmark(t, e, who, e.IDs["saga1"], 99, "keep")
				wantStatus(t, e.Delete(t, who.token, bookmarkPath(e.IDs["saga1"])+"/-3"), http.StatusOK, "delete -3")
				wantStatus(t, e.Delete(t, who.token, bookmarkPath(e.IDs["saga1"])+"/-3"), http.StatusNotFound, "delete -3 again")
				requireBookmarks(t, e, who, []bookmarkJSON{quarter, keep})
				wantStatus(t, e.Delete(t, who.token, bookmarkPath(e.IDs["saga1"])+"/7.250"), http.StatusOK, "delete 7.250")
				wantStatus(t, e.Delete(t, who.token, bookmarkPath(e.IDs["saga1"])+"/7.25"), http.StatusNotFound, "delete 7.25 again")
				requireBookmarks(t, e, who, []bookmarkJSON{keep})
			},
		},
	}
}
