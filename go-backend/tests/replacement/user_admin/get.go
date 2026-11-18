package user_admin

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

const getSource = "server/controllers/UserController.js:59-105"

// findProgress returns the progress with the library item and episode, or nil.
func findProgress(view userView, itemID string, episodeID string) *progressView {
	for i := range view.MediaProgress {
		progress := &view.MediaProgress[i]
		if progress.LibraryItemID != itemID {
			continue
		}
		if (episodeID == "") != (progress.EpisodeID == nil) {
			continue
		}
		if episodeID == "" || *progress.EpisodeID == episodeID {
			return progress
		}
	}
	return nil
}

func getScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r074", Invariant: "INV-R074-SAMPLED", Case: "normal", State: "stateless", Assertion: "get-user-full-view",
			Sources: []string{"server/routers/ApiRouter.js:136", getSource, "server/models/User.js:353-390"},
			Claim:   "GET /api/users/:id as root returns the full browser view of the listener with its seven media progress entries (display title, subtitle, cover path and media update time added) and its two bookmarks.",
			Run: func(t *testing.T, e *harness.Env) {
				view := getUser(t, e, e.Token(t, "root"), e.IDs["listener"])
				row := userRow(t, e, e.IDs["listener"])
				if view.ID != e.IDs["listener"] || view.Username != "trial-listener" || view.Type != "user" || !view.IsActive || view.IsLocked || view.HasOpenIDLink || view.Email != nil ||
					view.Token != row["token"] || view.CreatedAt <= 0 {
					t.Fatalf("identity fields wrong: %+v", view)
				}
				requireViewPermissions(t, view, map[string]bool{"download": true, "update": false, "delete": false, "upload": false, "createEreader": false, "accessAllLibraries": true, "accessAllTags": true, "accessExplicitContent": true, "selectedTagsNotAccessible": false}, nil, nil)
				if len(view.SeriesHideFromContinueListening) != 0 || view.SeriesHideFromContinueListening == nil {
					t.Fatalf("seriesHideFromContinueListening must be an empty array: %+v", view.SeriesHideFromContinueListening)
				}
				if len(view.MediaProgress) != 7 || len(view.Bookmarks) != 2 {
					t.Fatalf("seven progress entries and two bookmarks expected: %d %d", len(view.MediaProgress), len(view.Bookmarks))
				}
				book := findProgress(view, e.IDs["saga1"], "")
				if book == nil || book.ID != "b95b3028-3461-4306-803b-6e310140cd22" || book.MediaItemType != "book" || book.CurrentTime != 27.5 || book.IsFinished ||
					book.DisplayTitle == nil || *book.DisplayTitle != "The Beginning" || book.CoverPath == nil || *book.CoverPath == "" || book.MediaUpdatedAt == nil || !isISODate(*book.MediaUpdatedAt) {
					t.Fatalf("book progress wrong: %+v", book)
				}
				finished := findProgress(view, e.IDs["novel"], "")
				if finished == nil || !finished.IsFinished || finished.Progress != 1 || finished.DisplayTitle == nil || *finished.DisplayTitle != "Single File Novel" {
					t.Fatalf("finished book progress wrong: %+v", finished)
				}
				episode := findProgress(view, e.IDs["podcast"], e.IDs["ep1"])
				if episode == nil || episode.MediaItemType != "podcastEpisode" || episode.CurrentTime != 10.5 || episode.DisplayTitle == nil || *episode.DisplayTitle != "Pilot Episode" ||
					episode.DisplaySubtitl == nil || *episode.DisplaySubtitl != "Fixture Cast" || episode.MediaUpdatedAt == nil || !isISODate(*episode.MediaUpdatedAt) {
					t.Fatalf("episode progress wrong: %+v", episode)
				}
				if view.Bookmarks[0].LibraryItemID != e.IDs["saga1"] || view.Bookmarks[0].Time != 21 || view.Bookmarks[0].Title != "Good bit" || view.Bookmarks[0].CreatedAt != 1793709987082 ||
					view.Bookmarks[1].LibraryItemID != e.IDs["podcast"] || view.Bookmarks[1].Time != 5 || view.Bookmarks[1].Title != "Cast bookmark" {
					t.Fatalf("bookmarks wrong: %+v", view.Bookmarks)
				}
			},
		},
		{
			Requirement: "route-r074", Invariant: "INV-R074-ROOT-TOKEN-001", Case: "edge", State: "stateless", Assertion: "get-user-root-token-and-empty-lists",
			Sources: []string{getSource, "server/models/User.js:353"},
			Claim:   "GET /api/users/:id of the root user shows its token to root and an empty string to another admin, and a user without progress or bookmarks gets empty arrays.",
			Run: func(t *testing.T, e *harness.Env) {
				asRoot := getUser(t, e, e.Token(t, "root"), rootID)
				if asRoot.Token != userRow(t, e, rootID)["token"] || asRoot.Token == "" || asRoot.Type != "root" {
					t.Fatalf("root sees its own token: %+v", asRoot)
				}
				if asRoot.MediaProgress == nil || len(asRoot.MediaProgress) != 0 || asRoot.Bookmarks == nil || len(asRoot.Bookmarks) != 0 {
					t.Fatalf("empty lists must be arrays: %+v", asRoot)
				}
				admin := adminToken(t, e)
				if hidden := getUser(t, e, admin, rootID); hidden.Token != "" || hidden.ID != rootID {
					t.Fatalf("an admin must not see the root token: %+v", hidden)
				}
				if listener := getUser(t, e, admin, e.IDs["listener"]); listener.Token != userRow(t, e, e.IDs["listener"])["token"] {
					t.Fatalf("an admin sees other users' tokens: %+v", listener)
				}
			},
		},
		{
			Requirement: "route-r074", Invariant: "INV-R074-AUTH-001", Case: "error", State: "stateless", Assertion: "get-user-errors",
			Sources: []string{"server/Server.js:354", getSource, middlewareJS},
			Claim:   "GET /api/users/:id answers 401 without a valid token, 403 to a non-admin for any id including its own, and 404 for an unknown id to an admin.",
			Run: func(t *testing.T, e *harness.Env) {
				requireUnauthenticated(t, e, http.MethodGet, "/api/users/"+e.IDs["listener"])
				listener := e.Token(t, "listener")
				for _, id := range []string{e.IDs["listener"], rootID, unknownID} {
					wantStatus(t, e.Get(t, listener, "/api/users/"+id), http.StatusForbidden, "listener GET "+id)
				}
				for _, token := range []string{e.Token(t, "root"), adminToken(t, e)} {
					wantStatus(t, e.Get(t, token, "/api/users/"+unknownID), http.StatusNotFound, "admin GET unknown id")
				}
			},
		},
	}
}
