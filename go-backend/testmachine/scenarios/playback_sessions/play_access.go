package playback_sessions

import (
	"net/http"
	"slices"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// state is what a refused request must leave unchanged.
type state struct {
	Open     []string
	Devices  int
	Sessions int
	Progress int
}

func snapshot(t *testing.T, e *harness.Env) state {
	t.Helper()
	open := openSessionIDs(t, e)
	slices.Sort(open)
	return state{
		Open:     open,
		Devices:  e.Count(t, "SELECT COUNT(*) FROM devices"),
		Sessions: e.Count(t, "SELECT COUNT(*) FROM playbackSessions"),
		Progress: e.Count(t, "SELECT COUNT(*) FROM mediaProgresses"),
	}
}

// requireRefused posts a play request and requires the status and no change.
func requireRefused(t *testing.T, e *harness.Env, token, path string, status int, what string) {
	t.Helper()
	before := snapshot(t, e)
	resp := e.Post(t, token, path, playBody{"supportedMimeTypes": []string{"audio/mpeg"}, "deviceInfo": map[string]any{"deviceId": "ps-refused"}})
	requireStatus(t, resp, status, what)
	requireEqual(t, what+": state after the refusal", snapshot(t, e), before)
}

// setItemMedia patches an item's media (setup owned by another packet) and
// restores it when the scenario ends.
func setItemMedia(t *testing.T, e *harness.Env, itemID string, body map[string]any, restore map[string]any) {
	t.Helper()
	root := e.Token(t, "root")
	requireStatus(t, e.Patch(t, root, "/api/items/"+itemID+"/media", body), http.StatusOK, "patch item media")
	t.Cleanup(func() { e.Patch(t, root, "/api/items/"+itemID+"/media", restore) })
}

func refusalScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r059", Invariant: "INV-R059-PLAY-REFUSAL-001", Case: "error", State: "failure", Assertion: "play-refusals-change-nothing",
			Sources: []string{"server/routers/ApiRouter.js:117", "server/controllers/LibraryItemController.js:1220-1249", playSource},
			Claim:   "Play is refused with 401 without a valid token, 404 for an unknown item or an item without audio tracks, and 403 for a library the user cannot access (checked before the audio-track 404), and none of them creates a session, device or progress.",
			Run: func(t *testing.T, e *harness.Env) {
				closeAllOpen(t, e)
				listener := fixtureActor(t, e, "listener")
				podcastOnly := createUser(t, e, "ps-podcast-only", map[string]any{
					"permissions": map[string]any{"accessAllLibraries": false}, "librariesAccessible": []string{e.IDs["podcastLibrary"]},
				})
				play := func(id string) string { return "/api/items/" + id + "/play" }
				for _, token := range []string{"", "not-a-token"} {
					requireRefused(t, e, token, play(e.IDs["saga1"]), http.StatusUnauthorized, "unauthenticated play")
				}
				requireRefused(t, e, listener.Token, play("00000000-0000-4000-8000-000000000000"), http.StatusNotFound, "unknown item")
				requireRefused(t, e, listener.Token, play("not-a-uuid"), http.StatusNotFound, "malformed item id")
				requireRefused(t, e, listener.Token, play(e.IDs["ebook"]), http.StatusNotFound, "item without audio tracks")
				requireRefused(t, e, podcastOnly.Token, play(e.IDs["saga1"]), http.StatusForbidden, "library the user cannot access")
				requireRefused(t, e, podcastOnly.Token, play(e.IDs["ebook"]), http.StatusForbidden, "forbidden library is checked before the audio tracks")
				requireRefused(t, e, podcastOnly.Token, play("00000000-0000-4000-8000-000000000000"), http.StatusNotFound, "unknown item stays 404 for a restricted user")
				// The user with access to the podcast library can play there.
				doc := play2(t, e, podcastOnly, e.IDs["podcast"], e.IDs["ep1"])
				closeQuietly(t, e, podcastOnly.Token, doc.ID)
			},
		},
		{
			Requirement: "route-r059", Invariant: "INV-R059-ACCESS-001", Case: "error", State: "initial", Assertion: "play-tag-rules",
			Sources: []string{"server/models/User.js:652-683", "server/controllers/LibraryItemController.js:1225"},
			Claim:   "Item tags decide who may play: an allow-list user needs a matching tag, a deny-list user must not have one, and untagged items are refused to the first and allowed to the second.",
			Run: func(t *testing.T, e *harness.Env) {
				allow := createUser(t, e, "ps-tag-allow", map[string]any{"permissions": map[string]any{"accessAllTags": false, "selectedTagsNotAccessible": false}, "itemTagsSelected": []string{"ps-tag"}})
				deny := createUser(t, e, "ps-tag-deny", map[string]any{"permissions": map[string]any{"accessAllTags": false, "selectedTagsNotAccessible": true}, "itemTagsSelected": []string{"ps-tag"}})
				item := "/api/items/" + e.IDs["saga2"] + "/play"
				requireRefused(t, e, allow.Token, item, http.StatusForbidden, "allow-list user, untagged item")
				mustPlay := func(who actor, device string) {
					doc := play(t, e, who.Token, e.IDs["saga2"], "", playBody{"supportedMimeTypes": []string{"audio/mpeg"}, "deviceInfo": map[string]any{"deviceId": device}})
					closeQuietly(t, e, who.Token, doc.ID)
				}
				mustPlay(deny, "ps-tag-deny-1")
				setItemMedia(t, e, e.IDs["saga2"], map[string]any{"tags": []string{"ps-tag", "other"}}, map[string]any{"tags": []string{}})
				mustPlay(allow, "ps-tag-allow-1")
				requireRefused(t, e, deny.Token, item, http.StatusForbidden, "deny-list user, item with a denied tag")
				setItemMedia(t, e, e.IDs["saga2"], map[string]any{"tags": []string{"other"}}, map[string]any{"tags": []string{}})
				requireRefused(t, e, allow.Token, item, http.StatusForbidden, "allow-list user, item with another tag")
				mustPlay(deny, "ps-tag-deny-2")
				mustPlay(fixtureActor(t, e, "listener"), "ps-tag-listener")
			},
		},
		{
			Requirement: "route-r059", Invariant: "INV-R059-ACCESS-001", Case: "error", State: "changed", Assertion: "play-explicit-rule",
			Sources: []string{"server/models/User.js:675-683", "server/controllers/LibraryItemController.js:1225"},
			Claim:   "An explicit item is refused with 403 to a user without explicit-content access once the item is marked explicit, and stays playable for a user with access.",
			Run: func(t *testing.T, e *harness.Env) {
				finisher, listener := fixtureActor(t, e, "finisher"), fixtureActor(t, e, "listener")
				item := "/api/items/" + e.IDs["saga2"] + "/play"
				ok := play(t, e, finisher.Token, e.IDs["saga2"], "", playBody{"supportedMimeTypes": []string{"audio/mpeg"}, "deviceInfo": map[string]any{"deviceId": "ps-explicit-1"}})
				closeQuietly(t, e, finisher.Token, ok.ID)
				setItemMedia(t, e, e.IDs["saga2"], map[string]any{"metadata": map[string]any{"explicit": true}}, map[string]any{"metadata": map[string]any{"explicit": false}})
				requireRefused(t, e, finisher.Token, item, http.StatusForbidden, "explicit item without explicit access")
				doc := play(t, e, listener.Token, e.IDs["saga2"], "", playBody{"supportedMimeTypes": []string{"audio/mpeg"}, "deviceInfo": map[string]any{"deviceId": "ps-explicit-2"}})
				requireEqual(t, "explicit flag in the session metadata", doc.MediaMetadata["explicit"], true)
				closeQuietly(t, e, listener.Token, doc.ID)
			},
		},
		{
			Requirement: "route-r060", Invariant: "INV-R060-REFUSAL-001", Case: "error", State: "failure", Assertion: "play-episode-refusals-change-nothing",
			Sources: []string{"server/routers/ApiRouter.js:118", episodeSource, "server/controllers/LibraryItemController.js:1220-1249"},
			Claim:   "Play with an episode id is refused with 401 unauthenticated, 404 for an unknown item or episode, 400 for a book, and 403 for a forbidden library before the type check, and changes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				closeAllOpen(t, e)
				listener := fixtureActor(t, e, "listener")
				booksOnly := createUser(t, e, "ps-books-only", map[string]any{
					"permissions": map[string]any{"accessAllLibraries": false}, "librariesAccessible": []string{e.IDs["library"]},
				})
				path := func(item, episode string) string { return "/api/items/" + item + "/play/" + episode }
				unknown := "00000000-0000-4000-8000-000000000000"
				for _, token := range []string{"", "not-a-token"} {
					requireRefused(t, e, token, path(e.IDs["podcast"], e.IDs["ep1"]), http.StatusUnauthorized, "unauthenticated episode play")
				}
				requireRefused(t, e, listener.Token, path(unknown, e.IDs["ep1"]), http.StatusNotFound, "unknown item")
				requireRefused(t, e, listener.Token, path(e.IDs["podcast"], unknown), http.StatusNotFound, "unknown episode")
				requireRefused(t, e, listener.Token, path(e.IDs["podcast"], saga1BookID), http.StatusNotFound, "a book id is no episode")
				requireRefused(t, e, listener.Token, path(e.IDs["saga1"], e.IDs["ep1"]), http.StatusBadRequest, "episode play on a book")
				requireRefused(t, e, booksOnly.Token, path(e.IDs["podcast"], e.IDs["ep1"]), http.StatusForbidden, "library the user cannot access")
				requireRefused(t, e, booksOnly.Token, path(e.IDs["podcast"], unknown), http.StatusForbidden, "forbidden library is checked before the episode")
				requireRefused(t, e, booksOnly.Token, path(unknown, e.IDs["ep1"]), http.StatusNotFound, "unknown item stays 404 for a restricted user")
			},
		},
	}
}

// play2 plays an item or episode of a user with a fresh device id.
func play2(t *testing.T, e *harness.Env, who actor, itemID, episodeID string) sessionDoc {
	t.Helper()
	return play(t, e, who.Token, itemID, episodeID, playBody{"supportedMimeTypes": []string{"audio/mpeg"}, "deviceInfo": map[string]any{"deviceId": "ps-" + who.Name}})
}
