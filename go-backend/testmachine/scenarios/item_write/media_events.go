package item_write

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// itemAudience opens one socket per account kind.
func itemAudience(t *testing.T, e *harness.Env, s *setup) *audience {
	t.Helper()
	return connect(t, e, map[string]string{
		"root": e.Token(t, "root"), "listener": e.Token(t, "listener"), "podcastonly": s.token(t, e, "podcastonly"),
		"tagallow": s.token(t, e, "tagallow"), "tagdeny": s.token(t, e, "tagdeny"), "noexplicit": s.token(t, e, "noexplicit"), "anonymous": "",
	})
}

// wantItemPayload checks an item_updated payload against the stored item and the item document.
func wantItemPayload(t *testing.T, e *harness.Env, got map[string]any, itemID, title string) {
	t.Helper()
	if got["id"] != itemID || got["libraryId"] != e.IDs["library"] || got["mediaType"] != "book" || metadataOf(got)["title"] != title {
		t.Fatalf("item_updated payload id/libraryId/mediaType/title = %v/%v/%v/%v", got["id"], got["libraryId"], got["mediaType"], metadataOf(got)["title"])
	}
	if want := float64(updatedAt(t, e, itemID).UnixMilli()); got["updatedAt"] != want {
		t.Fatalf("payload updatedAt = %v, want %v", got["updatedAt"], want)
	}
	requireEqual(t, "payload equals the expanded item document", got, itemDocument(t, e, itemID))
}

func mediaEventScenarios(s *setup) []harness.Scenario {
	edit := func(t *testing.T, e *harness.Env, itemID, subtitle string) {
		t.Helper()
		wantStatus(t, "edit", patchMedia(t, e, e.Token(t, "root"), itemID, map[string]any{"metadata": map[string]any{"subtitle": subtitle}}), http.StatusOK)
	}
	return []harness.Scenario{
		scenario("socket-outgoing-item-updated", "INV-SOCKET-OUTGOING-ITEM-UPDATED", "normal", "initial", "item-updated-tag-rules",
			"item_updated carries the expanded item and reaches each socket whose user may access the item by library, allow-tag and deny-tag rules, but not an unauthenticated socket.",
			srcs(emitterSrc, updateMediaSrc, "server/models/User.js:658-683"),
			func(t *testing.T, e *harness.Env) {
				a := itemAudience(t, e, s)
				edit(t, e, e.IDs["ebook"], "Event one")
				events := a.receive(t, "item_updated", idOf(e.IDs["ebook"]), []string{"root", "listener", "tagallow", "noexplicit"}, []string{"podcastonly", "tagdeny", "anonymous"})
				wantItemPayload(t, e, payload(t, events["listener"]), e.IDs["ebook"], "Ebook Only")
				requireEqual(t, "same payload for every audience", payload(t, events["root"]), payload(t, events["tagallow"]))
				a.reset()
				edit(t, e, e.IDs["saga2"], "Event two")
				events = a.receive(t, "item_updated", idOf(e.IDs["saga2"]), []string{"root", "listener", "tagdeny", "noexplicit"}, []string{"podcastonly", "tagallow", "anonymous"})
				wantItemPayload(t, e, payload(t, events["tagdeny"]), e.IDs["saga2"], "The Middle")
			}),
		scenario("socket-outgoing-item-updated", "INV-SOCKET-OUTGOING-ITEM-UPDATED", "edge", "changed", "item-updated-explicit-and-library-rules",
			"item_updated of an explicit item skips users without explicit access, and a podcast update reaches users with access to its library and item, including users with access to all libraries.",
			srcs(emitterSrc, "server/models/User.js:675-683"),
			func(t *testing.T, e *harness.Env) {
				a := itemAudience(t, e, s)
				id := e.IDs["saga1"]
				setExplicit(t, e, id, true)
				a.receive(t, "item_updated", idOf(id), []string{"root", "listener", "tagdeny"}, []string{"noexplicit", "tagallow", "podcastonly", "anonymous"})
				a.reset()
				setExplicit(t, e, id, false)
				events := a.receive(t, "item_updated", idOf(id), []string{"root", "noexplicit", "tagdeny"}, []string{"podcastonly", "anonymous"})
				if got := payload(t, events["noexplicit"]); metadataOf(got)["explicit"] != false {
					t.Fatalf("payload explicit = %v", metadataOf(got)["explicit"])
				}
				a.reset()
				wantStatus(t, "podcast edit", patchMedia(t, e, e.Token(t, "root"), e.IDs["podcast"], map[string]any{"metadata": map[string]any{"language": "es"}}), http.StatusOK)
				pod := a.receive(t, "item_updated", idOf(e.IDs["podcast"]), []string{"root", "listener", "podcastonly", "tagdeny"}, []string{"noexplicit", "tagallow", "anonymous"}) // the podcast was made explicit by an earlier scenario
				got := payload(t, pod["podcastonly"])
				if got["mediaType"] != "podcast" || got["libraryId"] != e.IDs["podcastLibrary"] || metadataOf(got)["language"] != "es" {
					t.Fatalf("podcast payload %v", got)
				}
			}),
	}
}
