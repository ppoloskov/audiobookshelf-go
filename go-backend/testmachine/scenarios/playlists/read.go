package playlists

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// revoked is a user whose access to the book library was removed after
// creating a playlist there; created once per server.
type revoked struct {
	token                     string
	bookPlaylist, podPlaylist playlist
}

var revokedByInstall = map[string]revoked{}

// revokedUser creates the user "pl-revoked" with a playlist in each library and
// then limits the user to the podcast library.
func revokedUser(t *testing.T, e *harness.Env) revoked {
	t.Helper()
	userMu.Lock()
	cached, ok := revokedByInstall[e.Install]
	userMu.Unlock()
	if ok {
		return cached
	}
	token := plainUser(t, e, "pl-revoked")
	fx := fixtureOf(e)
	r := revoked{token: token}
	r.bookPlaylist = createAs(t, e, token, "Revoked books", book(fx.novel))
	r.podPlaylist = createPodcastAs(t, e, token, "Revoked casts", episode(fx.podcast, fx.ep1))
	resp := call(t, e, e.Token(t, "root"), http.MethodPatch, "/api/users/"+userID(e, "pl-revoked"), map[string]any{
		"permissions": map[string]any{
			"download": true, "update": false, "delete": false, "accessAllLibraries": false,
			"librariesAccessible": []string{e.IDs["podcastLibrary"]}, "accessAllTags": true, "accessExplicitContent": true,
		},
	})
	wantStatus(t, resp, http.StatusOK)
	userMu.Lock()
	revokedByInstall[e.Install] = r
	userMu.Unlock()
	return r
}

// wantFixtureBookPlaylist asserts the fixture playlist "Commute" of the listener.
func wantFixtureBookPlaylist(t *testing.T, e *harness.Env, p playlist) {
	t.Helper()
	fx := fixtureOf(e)
	row := playlistRow(t, e, e.IDs["playlist"])
	if p.ID != e.IDs["playlist"] || p.Name != "Commute" || p.Description != nil || p.UserID != e.IDs["listener"] || p.LibraryID != e.IDs["library"] ||
		p.LastUpdate != millis(t, row["updatedAt"]) || p.CreatedAt != millis(t, row["createdAt"]) {
		t.Fatalf("fixture playlist payload wrong: %+v", p)
	}
	sameStrings(t, "items", p.refs(), []string{fx.saga2, fx.novel})
	titles := []string{"The Middle", "Single File Novel"}
	authors := []string{"Terry Fixture", "Ada Example"}
	for i, entry := range p.Items {
		if entry.EpisodeID != "" || entry.Episode != nil || entry.LibraryItem.ID != entry.LibraryItemID || entry.LibraryItem.MediaType != "book" || entry.LibraryItem.LibraryID != e.IDs["library"] || entry.LibraryItem.IsMissing == nil || *entry.LibraryItem.IsMissing || entry.LibraryItem.IsInvalid == nil || *entry.LibraryItem.IsInvalid {
			t.Fatalf("book item must carry its expanded library item and no episode: %+v", entry)
		}
		media := entry.LibraryItem.Media
		if media.Metadata.Title != titles[i] || len(media.Metadata.Authors) != 1 || media.Metadata.Authors[0].Name != authors[i] || media.Duration <= 0 || len(media.Tracks) == 0 {
			t.Fatalf("book queue metadata wrong: %+v", media)
		}
		if i == 1 && (media.CoverPath == nil || !strings.HasSuffix(*media.CoverPath, "/metadata/items/"+fx.novel+"/cover.jpg")) {
			t.Fatalf("novel cover path wrong: %v", media.CoverPath)
		}
	}
}

// wantFixturePodcastPlaylist asserts the fixture playlist "Cast queue" of the listener.
func wantFixturePodcastPlaylist(t *testing.T, e *harness.Env, p playlist) {
	t.Helper()
	fx := fixtureOf(e)
	if p.ID != e.IDs["podcastPlaylist"] || p.Name != "Cast queue" || p.Description != nil || p.UserID != e.IDs["listener"] || p.LibraryID != e.IDs["podcastLibrary"] {
		t.Fatalf("fixture podcast playlist payload wrong: %+v", p)
	}
	sameStrings(t, "items", p.refs(), []string{ref(fx.podcast, fx.ep3), ref(fx.podcast, fx.ep1)})
	titles := []string{"Third Time", "Pilot Episode"}
	for i, entry := range p.Items {
		if entry.Episode == nil || entry.Episode.ID != entry.EpisodeID || entry.Episode.Title != titles[i] || entry.Episode.Duration <= 0 || entry.Episode.AudioFile == nil || entry.LibraryItem.ID != fx.podcast || entry.LibraryItem.LibraryID != e.IDs["podcastLibrary"] || entry.LibraryItem.IsMissing == nil || *entry.LibraryItem.IsMissing || entry.LibraryItem.IsInvalid == nil || *entry.LibraryItem.IsInvalid || entry.LibraryItem.MediaType != "podcast" || entry.LibraryItem.Media.Metadata.Title != "Fixture Cast" {
			t.Fatalf("episode item must carry the episode and its podcast library item: %+v", entry)
		}
	}
}

func readScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r091", Invariant: "INV-R091-SAMPLED", Case: "edge", State: "stateless", Assertion: "get-playlist-preserves-unplayable-item-flags",
			Sources: []string{"server/models/Playlist.js:297-319", "server/models/LibraryItem.js:1019-1020", "server/controllers/PlaylistController.js:252-255"},
			Claim:   "A playlist read and update event preserve missing and invalid flags and a cover path for its expanded book items.",
			Run: func(t *testing.T, _ *harness.Env) {
				e := harness.Start(t, harness.Options{})
				fx := fixtureOf(e)
				e.Stop(t)
				if e.Exec(t, "UPDATE libraryItems SET isMissing = 1 WHERE id = ?", fx.saga2) != 1 || e.Exec(t, "UPDATE libraryItems SET isInvalid = 1 WHERE id = ?", fx.novel) != 1 {
					t.Fatal("failed to seed missing and invalid fixture items")
				}
				e.Restart(t)
				p := fetchAs(t, e, e.Token(t, "listener"), e.IDs["playlist"])
				sameStrings(t, "fixture items", p.refs(), []string{fx.saga2, fx.novel})
				if p.Items[0].LibraryItem.IsMissing == nil || !*p.Items[0].LibraryItem.IsMissing || p.Items[0].LibraryItem.IsInvalid == nil || *p.Items[0].LibraryItem.IsInvalid || p.Items[1].LibraryItem.IsMissing == nil || *p.Items[1].LibraryItem.IsMissing || p.Items[1].LibraryItem.IsInvalid == nil || !*p.Items[1].LibraryItem.IsInvalid {
					t.Fatalf("missing/invalid flags wrong: %+v", p.Items)
				}
				cover := p.Items[1].LibraryItem.Media.CoverPath
				if cover == nil || !strings.HasSuffix(*cover, "/metadata/items/"+fx.novel+"/cover.jpg") {
					t.Fatalf("novel cover path wrong: %v", cover)
				}
				token := e.Token(t, "listener")
				socket := e.Socket(t, token)
				mark := socket.Mark()
				updated := decode(t, update(t, e, token, p.ID, map[string]any{"name": "Flagged playlist"}))
				event := socket.Wait(t, mark, "playlist_updated", eventWait, matchID(p.ID))
				var emitted playlist
				if err := json.Unmarshal(event.Args[0], &emitted); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(emitted, updated) || !reflect.DeepEqual(emitted.Items, p.Items) {
					t.Fatalf("event lost flagged items or cover: %+v", emitted)
				}
			},
		},
		{
			Requirement: "route-r091", Invariant: "INV-R091-SAMPLED", Case: "normal", State: "stateless", Assertion: "get-owner-reads-book-playlist",
			Sources: []string{"server/controllers/PlaylistController.js:152-155", "server/models/Playlist.js:297-319"},
			Claim:   "The owner reads a book playlist: id, name, null description, owner, library, timestamps equal to the row, and items in persisted order each with libraryItemId and the expanded library item.",
			Run: func(t *testing.T, e *harness.Env) {
				resp := e.Get(t, e.Token(t, "listener"), "/api/playlists/"+e.IDs["playlist"])
				wantFixtureBookPlaylist(t, e, decode(t, resp))
			},
		},
		{
			Requirement: "route-r091", Invariant: "INV-R091-SAMPLED", Case: "edge", State: "stateless", Assertion: "get-podcast-playlist-episode-items",
			Sources: []string{"server/models/Playlist.js:297-319", "server/controllers/PlaylistController.js:152-155"},
			Claim:   "A podcast playlist is read with one item per episode carrying episodeId, the expanded episode and the podcast's library item, in persisted order.",
			Run: func(t *testing.T, e *harness.Env) {
				wantFixturePodcastPlaylist(t, e, fetchAs(t, e, e.Token(t, "listener"), e.IDs["podcastPlaylist"]))
			},
		},
		{
			Requirement: "route-r091", Invariant: "INV-R091-ACCESS", Case: "error", State: "stateless", Assertion: "get-rejects-unknown-foreign-and-anonymous",
			Sources: []string{"server/controllers/PlaylistController.js:575-593", "server/routers/ApiRouter.js:161"},
			Claim:   "GET /api/playlists/:id answers 401 without a token, 404 for an unknown id, and 403 for a user who does not own the playlist, administrators included.",
			Run: func(t *testing.T, e *harness.Env) {
				path := "/api/playlists/" + e.IDs["playlist"]
				wantStatus(t, e.Get(t, "", path), http.StatusUnauthorized)
				wantStatus(t, e.Get(t, e.Token(t, "listener"), "/api/playlists/no-such-playlist"), http.StatusNotFound)
				wantStatus(t, e.Get(t, e.Token(t, "finisher"), path), http.StatusForbidden)
				wantStatus(t, e.Get(t, e.Token(t, "root"), path), http.StatusForbidden)
			},
		},
		{
			Requirement: "route-r091", Invariant: "INV-R091-ACCESS", Case: "edge", State: "stateless", Assertion: "get-hidden-when-library-access-revoked",
			Sources: []string{"server/controllers/PlaylistController.js:585-588"},
			Claim:   "After the owner loses access to the playlist's library, GET answers 404 for that playlist while a playlist in a library the owner still sees is readable.",
			Run: func(t *testing.T, e *harness.Env) {
				r := revokedUser(t, e)
				wantStatus(t, e.Get(t, r.token, "/api/playlists/"+r.bookPlaylist.ID), http.StatusNotFound)
				got := fetchAs(t, e, r.token, r.podPlaylist.ID)
				if got.ID != r.podPlaylist.ID || got.Name != "Revoked casts" {
					t.Fatalf("accessible playlist wrong: %+v", got)
				}
			},
		},
		{
			Requirement: "route-r090", Invariant: "INV-R090-SAMPLED", Case: "normal", State: "stateless", Assertion: "list-owner-sees-own-sorted-by-name",
			Sources: []string{"server/controllers/PlaylistController.js:138-144", "server/models/Playlist.js:27-70"},
			Claim:   "GET /api/playlists returns {playlists} with the caller's playlists of all accessible libraries, expanded, sorted by name.",
			Run: func(t *testing.T, e *harness.Env) {
				got := list(t, e, e.Token(t, "listener"))
				if len(got) != 2 {
					t.Fatalf("listener must see exactly its two fixture playlists, got %d", len(got))
				}
				wantFixturePodcastPlaylist(t, e, got[0])
				wantFixtureBookPlaylist(t, e, got[1])
			},
		},
		{
			Requirement: "route-r090", Invariant: "INV-R090-SAMPLED", Case: "edge", State: "stateless", Assertion: "list-sorts-names-by-node-collation",
			Sources: []string{"server/models/Playlist.js:84-87", "server/controllers/PlaylistController.js:138-143", "go-backend/testmachine/contracts/clients/absorb.md:110"},
			Claim:   "Both playlist lists return the caller's playlists in JavaScript name collation order (case- and accent-insensitive, not byte order), across accessible libraries and within one library.",
			Run: func(t *testing.T, e *harness.Env) {
				// Node 22, locale en-US: ["Zulu","alpha","Émile","Bravo"].sort((a,b) => a.localeCompare(b))
				// gives ["alpha","Bravo","Émile","Zulu"], while byte order would start with "Bravo".
				fx := fixtureOf(e)
				token := plainUser(t, e, "pl-sorter")
				for _, name := range []string{"Zulu", "alpha", "Émile"} {
					createAs(t, e, token, name, book(fx.novel))
				}
				createPodcastAs(t, e, token, "Bravo", episode(fx.podcast, fx.ep1))
				names := func(list []playlist) []string {
					out := make([]string, 0, len(list))
					for _, p := range list {
						out = append(out, p.Name)
					}
					return out
				}
				sameStrings(t, "GET /api/playlists order", names(list(t, e, token)), []string{"alpha", "Bravo", "Émile", "Zulu"})
				sameStrings(t, "book library order", names(libraryPlaylists(t, e, token, e.IDs["library"])), []string{"alpha", "Émile", "Zulu"})
				sameStrings(t, "podcast library order", names(libraryPlaylists(t, e, token, e.IDs["podcastLibrary"])), []string{"Bravo"})
			},
		},
		{
			Requirement: "route-r090", Invariant: "INV-R090-SAMPLED", Case: "edge", State: "stateless", Assertion: "list-other-users-get-empty-array",
			Sources: []string{"server/controllers/PlaylistController.js:138-144"},
			Claim:   "A user without playlists, administrators included, gets {playlists: []} (an empty array, not null) and never sees another user's playlists.",
			Run: func(t *testing.T, e *harness.Env) {
				for _, who := range []string{"root", "finisher"} {
					resp := e.Get(t, e.Token(t, who), "/api/playlists")
					wantStatus(t, resp, http.StatusOK)
					if got, ok := resp.Object(t)["playlists"].([]any); !ok || len(got) != 0 {
						t.Fatalf("%s must get an empty playlists array: %s", who, resp.Body)
					}
				}
			},
		},
		{
			Requirement: "route-r090", Invariant: "INV-R090-SAMPLED", Case: "error", State: "stateless", Assertion: "list-requires-token",
			Sources: []string{"server/routers/ApiRouter.js:160"},
			Claim:   "GET /api/playlists without a token answers 401.",
			Run: func(t *testing.T, e *harness.Env) {
				wantStatus(t, e.Get(t, "", "/api/playlists"), http.StatusUnauthorized)
				wantStatus(t, e.Get(t, "not-a-token", "/api/playlists"), http.StatusUnauthorized)
			},
		},
		{
			Requirement: "route-r090", Invariant: "INV-R090-SAMPLED", Case: "edge", State: "stateless", Assertion: "list-hides-inaccessible-library-playlists",
			Sources: []string{"server/controllers/PlaylistController.js:139-140"},
			Claim:   "GET /api/playlists omits the playlists of libraries the user can no longer access and keeps the others.",
			Run: func(t *testing.T, e *harness.Env) {
				r := revokedUser(t, e)
				got := list(t, e, r.token)
				if hasPlaylist(got, r.bookPlaylist.ID) || !hasPlaylist(got, r.podPlaylist.ID) {
					t.Fatalf("revoked user's list wrong: %d playlists", len(got))
				}
			},
		},
		{
			Requirement: "route-r028", Invariant: "INV-ROUTE-R028-CONTRACT", Case: "normal", State: "stateless", Assertion: "library-list-fixture-owner",
			Sources: []string{"server/controllers/LibraryController.js:853-870", "server/models/Playlist.js:27-70"},
			Claim:   "GET /api/libraries/:id/playlists returns {results,total,limit,page} with only the caller's playlists of that library, expanded; limit and page default to 0.",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "listener")
				var body struct {
					Results []playlist `json:"results"`
					Total   int        `json:"total"`
					Limit   int        `json:"limit"`
					Page    int        `json:"page"`
				}
				resp := e.Get(t, token, "/api/libraries/"+e.IDs["library"]+"/playlists")
				wantStatus(t, resp, http.StatusOK)
				resp.JSON(t, &body)
				if body.Total != 1 || body.Limit != 0 || body.Page != 0 || len(body.Results) != 1 {
					t.Fatalf("book library list wrong: %s", resp.Body)
				}
				wantFixtureBookPlaylist(t, e, body.Results[0])
				resp = e.Get(t, token, "/api/libraries/"+e.IDs["podcastLibrary"]+"/playlists")
				wantStatus(t, resp, http.StatusOK)
				resp.JSON(t, &body)
				if body.Total != 1 || len(body.Results) != 1 {
					t.Fatalf("podcast library list wrong: %s", resp.Body)
				}
				wantFixturePodcastPlaylist(t, e, body.Results[0])
			},
		},
		{
			Requirement: "route-r028", Invariant: "INV-ROUTE-R028-CONTRACT", Case: "edge", State: "stateless", Assertion: "library-list-empty-and-paginated",
			Sources: []string{"server/controllers/LibraryController.js:853-870", "server/controllers/LibraryController.js:1487-1495"},
			Claim:   "A user without playlists gets total 0 and an empty results array; limit and page slice the name-sorted results while total stays the full count, and a page without a limit or a zero limit returns every result.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Get(t, e.Token(t, "root"), "/api/libraries/"+e.IDs["library"]+"/playlists")
				wantStatus(t, root, http.StatusOK)
				if results, ok := root.Object(t)["results"].([]any); !ok || len(results) != 0 || root.Object(t)["total"] != float64(0) {
					t.Fatalf("root must get empty results: %s", root.Body)
				}
				token := plainUser(t, e, "pl-pager")
				fx := fixtureOf(e)
				for _, name := range []string{"C pager", "A pager", "B pager"} {
					createAs(t, e, token, name, book(fx.novel))
				}
				page := func(query string) ([]string, int) {
					resp := e.Get(t, token, "/api/libraries/"+e.IDs["library"]+"/playlists"+query)
					wantStatus(t, resp, http.StatusOK)
					var body struct {
						Results []playlist `json:"results"`
						Total   int        `json:"total"`
					}
					resp.JSON(t, &body)
					var names []string
					for _, p := range body.Results {
						names = append(names, p.Name)
					}
					return names, body.Total
				}
				for _, tc := range []struct {
					query string
					want  []string
				}{
					{"", []string{"A pager", "B pager", "C pager"}},
					{"?limit=0", []string{"A pager", "B pager", "C pager"}},
					{"?page=1", []string{"A pager", "B pager", "C pager"}},
					{"?limit=2&page=0", []string{"A pager", "B pager"}},
					{"?limit=2&page=1", []string{"C pager"}},
					{"?limit=2&page=5", nil},
				} {
					names, total := page(tc.query)
					sameStrings(t, "names for "+tc.query, names, tc.want)
					if total != 3 {
						t.Fatalf("total for %q = %d, want 3", tc.query, total)
					}
				}
			},
		},
		{
			Requirement: "route-r028", Invariant: "INV-ROUTE-R028-CONTRACT", Case: "error", State: "stateless", Assertion: "library-list-rejections",
			Sources: []string{"server/controllers/LibraryController.js:1475-1498", "server/routers/ApiRouter.js:81"},
			Claim:   "The library playlist list answers 401 without a token, 404 for an unknown library, 403 for a library the user may not access, and 400 for a negative or fractional limit or page.",
			Run: func(t *testing.T, e *harness.Env) {
				path := "/api/libraries/" + e.IDs["library"] + "/playlists"
				token := e.Token(t, "listener")
				wantStatus(t, e.Get(t, "", path), http.StatusUnauthorized)
				wantStatus(t, e.Get(t, token, "/api/libraries/no-such-library/playlists"), http.StatusNotFound)
				r := revokedUser(t, e)
				wantStatus(t, e.Get(t, r.token, path), http.StatusForbidden)
				for _, query := range []string{"?limit=-1", "?page=-1", "?limit=1.5", "?page=0.5"} {
					resp := e.Get(t, token, path+query)
					if resp.Status != http.StatusBadRequest {
						t.Fatalf("%s: status %d, want 400", strings.TrimPrefix(query, "?"), resp.Status)
					}
				}
			},
		},
	}
}
