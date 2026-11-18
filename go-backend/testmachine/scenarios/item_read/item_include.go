package item_read

import (
	"net/http"
	"net/url"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const includeSource = "server/controllers/LibraryItemController.js:70-100"

// progressDoc is MediaProgress.getOldMediaProgress for a mediaProgresses row.
func progressDoc(t *testing.T, row map[string]any) object {
	t.Helper()
	extra, _ := dbJSON(t, row["extraData"]).(map[string]any)
	var episodeID any
	if row["mediaItemType"] == "podcastEpisode" {
		episodeID = row["mediaItemId"]
	}
	progress := extra["progress"]
	if progress == nil {
		progress = 0
	}
	return object{
		"id": row["id"], "userId": row["userId"], "libraryItemId": extra["libraryItemId"], "episodeId": episodeID,
		"mediaItemId": row["mediaItemId"], "mediaItemType": row["mediaItemType"], "duration": row["duration"],
		"progress": progress, "currentTime": row["currentTime"], "isFinished": dbBool(row["isFinished"]),
		"hideFromContinueListening": dbBool(row["hideFromContinueListening"]), "ebookLocation": row["ebookLocation"],
		"ebookProgress": row["ebookProgress"], "lastUpdate": dbTime(t, row["updatedAt"]), "startedAt": dbTime(t, row["createdAt"]),
		"finishedAt": dbTime(t, row["finishedAt"]),
	}
}

// progressRow loads the media progress of a user for a library item (and episode).
func progressRow(t *testing.T, e *harness.Env, userID, itemID, mediaItemID string) map[string]any {
	t.Helper()
	return firstRow(t, e, "SELECT * FROM mediaProgresses WHERE userId = ? AND json_extract(extraData, '$.libraryItemId') = ? AND mediaItemId = ?", userID, itemID, mediaItemID)
}

// expandedWith requests the expanded document with extra query parameters.
func expandedWith(t *testing.T, e *harness.Env, token, id string, query url.Values) object {
	t.Helper()
	query.Set("expanded", "1")
	resp := e.Do(t, harness.Request{Path: "/api/items/" + id, Token: token, Query: query})
	wantStatus(t, "GET expanded item", resp, http.StatusOK)
	return resp.Object(t)
}

// itemIncludeScenarios covers ?include= and ?episode= on the expanded document.
func itemIncludeScenarios(s *setup) []harness.Scenario {
	scenario := func(assertion, caseName, claim string, run func(t *testing.T, e *harness.Env)) harness.Scenario {
		return harness.Scenario{
			Requirement: "route-r050", Invariant: "INV-R050-INCLUDE-001", Case: caseName, State: "stateless", Assertion: assertion,
			Sources: []string{includeSource, "server/models/User.js:716-722", "server/models/MediaProgress.js:155-176"}, Claim: claim, Run: run,
		}
	}
	return []harness.Scenario{
		scenario("include-progress-book", "normal",
			"include=progress adds userMediaProgress with the caller's stored progress of the book (id, currentTime, progress, isFinished, ebook fields, timestamps as numbers).",
			func(t *testing.T, e *harness.Env) {
				listener := e.IDs["listener"]
				for _, key := range []string{"saga1", "novel", "ebook", "saga2"} {
					id := e.IDs[key]
					doc := expandedWith(t, e, e.Token(t, "listener"), id, url.Values{"include": {"progress"}})
					row := firstRow(t, e, "SELECT * FROM mediaProgresses WHERE userId = ? AND json_extract(extraData, '$.libraryItemId') = ?", listener, id)
					requireSame(t, key+" userMediaProgress", progressDoc(t, row), doc["userMediaProgress"])
				}
			}),
		scenario("include-progress-episode", "normal",
			"include=progress&episode=<id> on the podcast adds the caller's progress of exactly that episode, with episodeId set and finishedAt a number for a finished episode.",
			func(t *testing.T, e *harness.Env) {
				for _, key := range []string{"ep1", "ep2", "ep3"} {
					doc := expandedWith(t, e, e.Token(t, "listener"), e.IDs["podcast"], url.Values{"include": {"progress"}, "episode": {e.IDs[key]}})
					row := progressRow(t, e, e.IDs["listener"], e.IDs["podcast"], e.IDs[key])
					requireSame(t, key+" userMediaProgress", progressDoc(t, row), doc["userMediaProgress"])
				}
			}),
		scenario("include-progress-none", "edge",
			"include=progress gives userMediaProgress null when the caller has no progress for the item, when the episode id does not match, and for a book with an episode id.",
			func(t *testing.T, e *harness.Env) {
				cases := []struct {
					name, token, item string
					query             url.Values
				}{
					{"root without progress", e.Token(t, "root"), e.IDs["saga1"], url.Values{"include": {"progress"}}},
					{"unknown episode on podcast", e.Token(t, "listener"), e.IDs["podcast"], url.Values{"include": {"progress"}, "episode": {"no-such-episode"}}},
					{"episode id on a book", e.Token(t, "listener"), e.IDs["saga1"], url.Values{"include": {"progress"}, "episode": {e.IDs["ep1"]}}},
					{"item without progress", e.Token(t, "listener"), e.IDs["disposable"], url.Values{"include": {"progress"}}},
				}
				for _, c := range cases {
					doc := expandedWith(t, e, c.token, c.item, c.query)
					value, present := doc["userMediaProgress"]
					if !present || value != nil {
						t.Fatalf("%s: userMediaProgress = %v (present %v), want null", c.name, value, present)
					}
				}
			}),
		scenario("include-progress-podcast-without-episode", "edge",
			"include=progress on a podcast without episode returns one of the caller's episode progress records of that podcast.",
			func(t *testing.T, e *harness.Env) {
				doc := expandedWith(t, e, e.Token(t, "listener"), e.IDs["podcast"], url.Values{"include": {"progress"}})
				progress, _ := doc["userMediaProgress"].(map[string]any)
				if progress == nil || progress["libraryItemId"] != e.IDs["podcast"] || progress["userId"] != e.IDs["listener"] {
					t.Fatalf("userMediaProgress %v is not a progress record of the listener for the podcast", doc["userMediaProgress"])
				}
				row := progressRow(t, e, e.IDs["listener"], e.IDs["podcast"], progress["episodeId"].(string))
				requireSame(t, "userMediaProgress", progressDoc(t, row), progress)
			}),
		scenario("include-rssfeed", "normal",
			"include=rssfeed adds the item's open feed as {id, entityType, entityId, feedUrl, meta{title, description, preventIndexing, ownerName, ownerEmail}} for any user, and rssFeed null when there is no feed.",
			func(t *testing.T, e *harness.Env) {
				for _, token := range []string{e.Token(t, "root"), e.Token(t, "listener")} {
					for _, key := range []string{"saga1", "novel", "podcast"} {
						id := e.IDs[key]
						feed := firstRow(t, e, "SELECT * FROM feeds WHERE entityId = ?", id)
						want := object{
							"id": feed["id"], "entityType": feed["entityType"], "entityId": id, "feedUrl": feed["feedURL"],
							"meta": object{"title": feed["title"], "description": feed["description"], "preventIndexing": dbBool(feed["preventIndexing"]), "ownerName": feed["ownerName"], "ownerEmail": feed["ownerEmail"]},
						}
						doc := expandedWith(t, e, token, id, url.Values{"include": {"rssfeed"}})
						requireSame(t, key+" rssFeed", want, doc["rssFeed"])
					}
					doc := expandedWith(t, e, token, e.IDs["saga2"], url.Values{"include": {"rssfeed"}})
					if value, present := doc["rssFeed"]; !present || value != nil {
						t.Fatalf("item without a feed: rssFeed = %v (present %v), want null", value, present)
					}
				}
			}),
		scenario("include-share-admin", "normal",
			"include=share adds mediaItemShare of a book for an admin (id, mediaItemId, mediaItemType, slug, expiresAt, isDownloadable, createdAt, updatedAt) without pash, userId or extraData.",
			func(t *testing.T, e *harness.Env) {
				for _, key := range []string{"saga2", "novel"} {
					id := e.IDs[key]
					book := firstRow(t, e, "SELECT mediaId FROM libraryItems WHERE id = ?", id)["mediaId"]
					share := firstRow(t, e, "SELECT * FROM mediaItemShares WHERE mediaItemId = ?", book)
					for _, token := range []string{e.Token(t, "root"), s.token(t, e, "admin")} {
						doc := expandedWith(t, e, token, id, url.Values{"include": {"share"}})
						got, _ := doc["mediaItemShare"].(map[string]any)
						if got == nil {
							t.Fatalf("%s: no mediaItemShare in %v", key, doc["mediaItemShare"])
						}
						for _, secret := range []string{"pash", "userId", "extraData"} {
							if _, leaked := got[secret]; leaked {
								t.Fatalf("%s: mediaItemShare exposes %s", key, secret)
							}
						}
						for field, want := range map[string]any{
							"id": share["id"], "mediaItemId": share["mediaItemId"], "mediaItemType": share["mediaItemType"],
							"slug": share["slug"], "expiresAt": nil, "isDownloadable": dbBool(share["isDownloadable"]),
						} {
							if got[field] != want {
								t.Fatalf("%s: mediaItemShare.%s = %v, want %v", key, field, got[field], want)
							}
						}
						for _, field := range []string{"createdAt", "updatedAt"} {
							if text, _ := got[field].(string); text == "" {
								t.Fatalf("%s: mediaItemShare.%s = %v, want a timestamp", key, field, got[field])
							}
						}
					}
				}
			}),
		scenario("include-share-restricted", "edge",
			"An administrator gets mediaItemShare:null for an unshared book. Non-admins and podcasts omit it, and books omit download queue fields.",
			func(t *testing.T, e *harness.Env) {
				unshared := expandedWith(t, e, e.Token(t, "root"), e.IDs["saga1"], url.Values{"include": {"share"}})
				if share, present := unshared["mediaItemShare"]; !present || share != nil {
					t.Fatalf("unshared book: mediaItemShare must be present and null")
				}
				cases := []struct {
					name, token, item, include string
				}{
					{"non-admin user on a shared book", e.Token(t, "listener"), e.IDs["saga2"], "share"},
					{"admin on a podcast", e.Token(t, "root"), e.IDs["podcast"], "share"},
					{"book with downloads", e.Token(t, "root"), e.IDs["saga1"], "downloads"},
				}
				for _, c := range cases {
					doc := expandedWith(t, e, c.token, c.item, url.Values{"include": {c.include}})
					for _, key := range []string{"mediaItemShare", "episodeDownloadsQueued", "episodesDownloading"} {
						if _, present := doc[key]; present {
							t.Fatalf("%s: unexpected key %s = %v", c.name, key, doc[key])
						}
					}
				}
			}),
		scenario("include-downloads-podcast", "normal",
			"include=downloads on an idle podcast adds episodeDownloadsQueued as an empty array and no episodesDownloading.",
			func(t *testing.T, e *harness.Env) {
				doc := expandedWith(t, e, e.Token(t, "listener"), e.IDs["podcast"], url.Values{"include": {"downloads"}})
				queued, ok := doc["episodeDownloadsQueued"].([]any)
				if !ok || len(queued) != 0 {
					t.Fatalf("episodeDownloadsQueued = %v, want []", doc["episodeDownloadsQueued"])
				}
				if _, present := doc["episodesDownloading"]; present {
					t.Fatalf("episodesDownloading = %v on an idle podcast", doc["episodesDownloading"])
				}
			}),
		scenario("include-combined-and-unknown", "edge",
			"include accepts a comma list in any order, ignores unknown names, and every requested part appears alongside the unchanged expanded document.",
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["podcast"]
				doc := expandedWith(t, e, e.Token(t, "root"), id, url.Values{"include": {"bogus,downloads,rssfeed,progress,share"}})
				feed := firstRow(t, e, "SELECT id FROM feeds WHERE entityId = ?", id)
				if Dig(doc, "rssFeed", "id") != feed["id"] || doc["userMediaProgress"] != nil || doc["episodeDownloadsQueued"] == nil {
					t.Fatalf("combined include: rssFeed %v userMediaProgress %v episodeDownloadsQueued %v", doc["rssFeed"], doc["userMediaProgress"], doc["episodeDownloadsQueued"])
				}
				for _, extra := range []string{"rssFeed", "userMediaProgress", "episodeDownloadsQueued"} {
					delete(doc, extra)
				}
				requireItem(t, "expanded document with includes", expandedItem(t, e, id), doc)
			}),
		scenario("include-ignored-without-expanded", "edge",
			"include and episode are ignored unless expanded=1: the plain document is returned without userMediaProgress, rssFeed or mediaItemShare, and expanded=0 gives the plain document.",
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga2"]
				for _, query := range []url.Values{
					{"include": {"progress,rssfeed,share,downloads"}},
					{"expanded": {"0"}, "include": {"progress,share"}},
				} {
					resp := e.Do(t, harness.Request{Path: "/api/items/" + id, Token: e.Token(t, "root"), Query: query})
					wantStatus(t, "GET item with include", resp, http.StatusOK)
					requireItem(t, "plain document", plainItem(t, e, id), resp.Object(t))
				}
			}),
	}
}

// Dig is a local alias to keep scenario bodies short.
var Dig = harness.Dig
