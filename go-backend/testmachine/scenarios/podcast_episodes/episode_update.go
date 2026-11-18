package podcast_episodes

import (
	"encoding/json"
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	episodePatchReq    = "route-r160"
	episodePatchInv    = "INV-ROUTE-R160-CONTRACT"
	episodePatchNoop   = "INV-ROUTE-R160-NOOP-001"
	episodePatchAuth   = "INV-ROUTE-R160-AUTH-001"
	episodePatchSrc    = "server/controllers/PodcastController.js:413-477"
	episodePatchSanSrc = "server/utils/htmlSanitizer.js:1-25"
)

// patchEpisode updates one episode and returns the expanded library item body.
func patchEpisode(t *testing.T, e *harness.Env, itemID, episodeID string, body map[string]any) libraryItemJSON {
	t.Helper()
	resp := e.Patch(t, e.Token(t, "root"), "/api/podcasts/"+itemID+"/episode/"+episodeID, body)
	wantStatus(t, "patch episode", resp, http.StatusOK)
	return decode[libraryItemJSON](t, resp.Body)
}

// itemUpdatedMatcher matches an item_updated event of the item.
func itemUpdatedMatcher(id string) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		if len(args) == 0 {
			return false
		}
		var item struct {
			ID string `json:"id"`
		}
		return json.Unmarshal(args[0], &item) == nil && item.ID == id
	}
}

func episodeUpdateScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: episodePatchReq, Invariant: episodePatchInv, Case: "normal", State: "initial", Assertion: "episode-patch-stores-text-fields-and-publishes",
			Sources: []string{episodePatchSrc, episodeJsonSrc},
			Claim:   "PATCH /api/podcasts/:id/episode/:episodeId stores the supported string fields and a numeric publishedAt, moves the episode's updatedAt strictly forward without touching the library item's updatedAt, emits item_updated to the admin and the listener, and answers the expanded library item.",
			Run: func(t *testing.T, e *harness.Env) {
				item := itemOf(t, e, e.IDs["podcast"])
				before := episodeOf(t, item, e.IDs["ep1"])
				audience := connect(t, e, map[string]string{"admin": e.Token(t, "root"), "listener": e.Token(t, "listener")})
				response := patchEpisode(t, e, e.IDs["podcast"], e.IDs["ep1"], map[string]any{
					"title":       "Pilot Episode Updated",
					"subtitle":    "Fresh subtitle",
					"season":      "4",
					"episode":     "9",
					"episodeType": "bonus",
					"pubDate":     "2025-04-01",
					"publishedAt": 1743465600000,
				})
				updated := episodeOf(t, response, e.IDs["ep1"])
				requireValue(t, "title", updated.Title, "Pilot Episode Updated")
				requireValue(t, "subtitle", updated.Subtitle, "Fresh subtitle")
				requireValue(t, "season", updated.Season, "4")
				requireValue(t, "episode", updated.Episode, "9")
				requireValue(t, "episodeType", updated.EpisodeType, "bonus")
				requireValue(t, "pubDate", updated.PubDate, "2025-04-01")
				if updated.PublishedAt == nil || *updated.PublishedAt != 1743465600000 {
					t.Fatalf("publishedAt = %v, want 1743465600000", updated.PublishedAt)
				}
				if updated.UpdatedAt <= before.UpdatedAt {
					t.Fatalf("episode updatedAt = %d, want after %d", updated.UpdatedAt, before.UpdatedAt)
				}
				if response.UpdatedAt != item.UpdatedAt {
					t.Fatalf("library item updatedAt = %d, want unchanged %d", response.UpdatedAt, item.UpdatedAt)
				}
				stored := episodeRow(t, e, e.IDs["ep1"])
				requireString(t, stored, "title", "Pilot Episode Updated")
				requireString(t, stored, "subtitle", "Fresh subtitle")
				requireString(t, stored, "season", "4")
				requireString(t, stored, "episode", "9")
				requireString(t, stored, "episodeType", "bonus")
				requireString(t, stored, "pubDate", "2025-04-01")
				audience.receive(t, "item_updated", itemUpdatedMatcher(e.IDs["podcast"]), []string{"admin", "listener"}, nil)
			},
		},
		{
			Requirement: episodePatchReq, Invariant: episodePatchInv, Case: "normal", State: "changed", Assertion: "episode-patch-sanitizes-and-sets-enclosure",
			Sources: []string{episodePatchSrc, episodePatchSanSrc},
			Claim:   "PATCH sanitizes description and subtitle HTML to the allowed tags, stores an enclosure object as URL, type and string length, stores valid chapters and answers the stored values.",
			Run: func(t *testing.T, e *harness.Env) {
				stub := newAudioStub(t)
				enclosureURL := stub.url("/manual.mp3")
				response := patchEpisode(t, e, e.IDs["podcast"], e.IDs["ep2"], map[string]any{
					"description": `<p onclick="steal()">New <b>description</b></p><img src="x">`,
					"subtitle":    `<b>Sub</b><img src="y">`,
					"enclosure":   map[string]any{"url": enclosureURL, "type": "audio/x-m4a", "length": 12345},
					"chapters": []map[string]any{
						{"title": "Intro", "start": 1, "end": 10},
						{"title": "Closing", "start": 10, "end": 20},
					},
				})
				updated := episodeOf(t, response, e.IDs["ep2"])
				requireValue(t, "description", updated.Description, "<p>New <b>description</b></p>")
				requireValue(t, "subtitle", updated.Subtitle, "<b>Sub</b>")
				if updated.Enclosure == nil || updated.Enclosure.URL != enclosureURL {
					t.Fatalf("enclosure = %+v", updated.Enclosure)
				}
				requireValue(t, "enclosure type", updated.Enclosure.Type, "audio/x-m4a")
				requireValue(t, "enclosure length", updated.Enclosure.Length, "12345")
				if len(updated.Chapters) != 2 || updated.Chapters[0].Title != "Intro" || updated.Chapters[0].Start != 1 || updated.Chapters[1].Title != "Closing" || updated.Chapters[1].End != 20 {
					t.Fatalf("chapters = %+v", updated.Chapters)
				}
				stored := episodeRow(t, e, e.IDs["ep2"])
				requireString(t, stored, "description", "<p>New <b>description</b></p>")
				requireString(t, stored, "subtitle", "<b>Sub</b>")
				requireString(t, stored, "enclosureURL", enclosureURL)
				requireString(t, stored, "enclosureType", "audio/x-m4a")
				chapters, _ := stored["chapters"].(string)
				if chapters != `[{"end":10,"start":1,"title":"Intro"},{"end":20,"start":10,"title":"Closing"}]` {
					t.Fatalf("stored chapters = %q", chapters)
				}
			},
		},
		{
			Requirement: episodePatchReq, Invariant: episodePatchNoop, Case: "edge", State: "repeat", Assertion: "episode-patch-repeat-is-a-noop",
			Sources: []string{episodePatchSrc},
			Claim:   "Repeating the same PATCH leaves the episode's updatedAt unchanged, emits no item_updated and answers the same stored episode.",
			Run: func(t *testing.T, e *harness.Env) {
				before := episodeOf(t, itemOf(t, e, e.IDs["podcast"]), e.IDs["ep1"])
				socket := e.Socket(t, e.Token(t, "root"))
				mark := socket.Mark()
				body := map[string]any{
					"title":       "Pilot Episode Updated",
					"subtitle":    "Fresh subtitle",
					"season":      "4",
					"episode":     "9",
					"episodeType": "bonus",
					"pubDate":     "2025-04-01",
					"publishedAt": 1743465600000,
				}
				response := patchEpisode(t, e, e.IDs["podcast"], e.IDs["ep1"], body)
				after := episodeOf(t, response, e.IDs["ep1"])
				if after.UpdatedAt != before.UpdatedAt {
					t.Fatalf("repeat changed updatedAt: %d -> %d", before.UpdatedAt, after.UpdatedAt)
				}
				socket.None(t, mark, "item_updated", quietWindow, itemUpdatedMatcher(e.IDs["podcast"]))
			},
		},
		{
			Requirement: episodePatchReq, Invariant: episodePatchInv, Case: "edge", State: "changed", Assertion: "episode-patch-ignores-unsupported-values-and-clears-enclosure",
			Sources: []string{episodePatchSrc},
			Claim:   "PATCH ignores a non-string title, an unsupported index, an unknown key, a string publishedAt and chapters without a truthy start, and a null enclosure clears the stored URL, type and size.",
			Run: func(t *testing.T, e *harness.Env) {
				stub := newAudioStub(t)
				enclosureURL := stub.url("/edge.mp3")
				patchEpisode(t, e, e.IDs["podcast"], e.IDs["ep3"], map[string]any{
					"enclosure": map[string]any{"url": enclosureURL, "type": "audio/mpeg", "length": "7000"},
				})
				before := episodeOf(t, itemOf(t, e, e.IDs["podcast"]), e.IDs["ep3"])
				response := patchEpisode(t, e, e.IDs["podcast"], e.IDs["ep3"], map[string]any{
					"title":       5,
					"index":       3,
					"unknownKey":  "x",
					"publishedAt": "123",
					"chapters":    []map[string]any{{"title": "bad", "start": 0}},
				})
				after := episodeOf(t, response, e.IDs["ep3"])
				requireValue(t, "title", after.Title, "Third Time")
				if after.Index != before.Index {
					t.Fatalf("index = %v, want %v", after.Index, before.Index)
				}
				if after.UpdatedAt != before.UpdatedAt {
					t.Fatalf("ignored values changed updatedAt: %d -> %d", before.UpdatedAt, after.UpdatedAt)
				}
				if after.Enclosure == nil || after.Enclosure.URL != enclosureURL {
					t.Fatalf("enclosure after invalid patch = %+v", after.Enclosure)
				}
				cleared := episodeOf(t, patchEpisode(t, e, e.IDs["podcast"], e.IDs["ep3"], map[string]any{"enclosure": nil}), e.IDs["ep3"])
				if cleared.Enclosure != nil {
					t.Fatalf("enclosure after null = %+v, want null", cleared.Enclosure)
				}
				stored := episodeRow(t, e, e.IDs["ep3"])
				if stored["enclosureURL"] != nil || stored["enclosureType"] != nil || stored["enclosureSize"] != nil {
					t.Fatalf("stored enclosure = %v/%v/%v, want null", stored["enclosureURL"], stored["enclosureType"], stored["enclosureSize"])
				}
			},
		},
		{
			Requirement: episodePatchReq, Invariant: episodePatchAuth, Case: "error", State: "failure", Assertion: "episode-patch-rejections-change-nothing",
			Sources: []string{episodeMidSrc, episodePatchSrc},
			Claim:   "PATCH /api/podcasts/:id/episode/:episodeId answers 401 without a token, 403 for a user without update permission or without access to the library, 404 for an unknown item or episode and 500 for a book item; no rejected request changes a row.",
			Run: func(t *testing.T, e *harness.Env) {
				before := takeSnapshot(t, e)
				episodeBefore := episodeRow(t, e, e.IDs["ep1"])
				restricted := libraryRestrictedToken(t, e, "episode-patch-restricted")
				path := "/api/podcasts/" + e.IDs["podcast"] + "/episode/" + e.IDs["ep1"]
				body := map[string]any{"title": "Rejected change"}
				wantStatus(t, "anonymous", e.Patch(t, "", path, body), http.StatusUnauthorized)
				wantStatus(t, "listener without update", e.Patch(t, e.Token(t, "listener"), path, body), http.StatusForbidden)
				wantStatus(t, "unknown item before permission", e.Patch(t, restricted, "/api/podcasts/not-an-item/episode/"+e.IDs["ep1"], body), http.StatusNotFound)
				wantStatus(t, "restricted", e.Patch(t, restricted, path, body), http.StatusForbidden)
				wantStatus(t, "book item", e.Patch(t, e.Token(t, "root"), "/api/podcasts/"+e.IDs["novel"]+"/episode/"+e.IDs["ep1"], body), http.StatusInternalServerError)
				wantStatus(t, "unknown episode", e.Patch(t, e.Token(t, "root"), "/api/podcasts/"+e.IDs["podcast"]+"/episode/not-an-episode", body), http.StatusNotFound)
				before.requireUnchanged(t, e, "rejected episode updates")
				if after := episodeRow(t, e, e.IDs["ep1"]); after["title"] != episodeBefore["title"] || after["updatedAt"] != episodeBefore["updatedAt"] {
					t.Fatalf("episode changed: %+v, want %+v", after, episodeBefore)
				}
			},
		},
	}
}
