package item_write

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

// setExplicit flags a book as explicit (or not) as root.
func setExplicit(t *testing.T, e *harness.Env, itemID string, explicit bool) {
	t.Helper()
	patchOK(t, e, itemID, map[string]any{"metadata": map[string]any{"explicit": explicit}})
}

func mediaAccessScenarios(s *setup) []harness.Scenario {
	const inv = "INV-ROUTE-R053-CONTRACT"
	// rejected sends the same edit as every listed account and requires 403 with nothing changed.
	rejected := func(t *testing.T, e *harness.Env, itemID string, accounts ...string) {
		t.Helper()
		a := connect(t, e, map[string]string{"root": e.Token(t, "root")})
		before, rows := updatedAt(t, e, itemID), snapshot(t, e)
		for _, name := range accounts {
			resp := patchMedia(t, e, s.token(t, e, name), itemID, map[string]any{"metadata": map[string]any{"title": "Rejected By " + name}, "tags": []string{"rejected"}})
			wantStatus(t, name+" PATCH media", resp, http.StatusForbidden)
		}
		wantUnchanged(t, e, itemID, before)
		wantSnapshot(t, e, rows)
		a.receive(t, "item_updated", idOf(itemID), nil, []string{"root"})
	}
	return []harness.Scenario{
		scenario(r053, inv, "error", "initial", "patch-requires-login",
			"PATCH media without a token answers 401 and changes nothing.",
			srcs("server/Auth.js:322", middlewareSrc),
			func(t *testing.T, e *harness.Env) {
				rows := snapshot(t, e)
				resp := e.Do(t, harness.Request{Method: http.MethodPatch, Path: "/api/items/" + e.IDs["novel"] + "/media", JSON: map[string]any{"tags": []string{"x"}}})
				wantStatus(t, "anonymous PATCH", resp, http.StatusUnauthorized)
				wantSnapshot(t, e, rows)
			}),
		scenario(r053, inv, "error", "initial", "patch-unknown-item-is-404-before-permission",
			"PATCH media of an unknown id answers 404 for an admin and for a user without update permission (404 is checked before 403).",
			srcs(middlewareSrc),
			func(t *testing.T, e *harness.Env) {
				rows := snapshot(t, e)
				missing := "/api/items/00000000-0000-4000-8000-000000000000/media"
				for _, token := range []string{e.Token(t, "root"), e.Token(t, "listener")} {
					wantStatus(t, "missing item", e.Patch(t, token, missing, map[string]any{"tags": []string{"x"}}), http.StatusNotFound)
				}
				wantSnapshot(t, e, rows)
			}),
		scenario(r053, inv, "error", "initial", "patch-requires-update-permission",
			"A user without the update permission (listener, a delete-only user) gets 403 and nothing changes; a user with only update permission succeeds.",
			srcs(middlewareSrc, "client/components/modals/item/tabs/Details.vue:164"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["novel"]
				a := connect(t, e, map[string]string{"root": e.Token(t, "root")})
				before, rows := updatedAt(t, e, id), snapshot(t, e)
				for _, token := range []string{e.Token(t, "listener"), e.Token(t, "finisher"), s.token(t, e, "deleter")} {
					wantStatus(t, "PATCH without update", patchMedia(t, e, token, id, map[string]any{"tags": []string{"denied"}}), http.StatusForbidden)
				}
				wantUnchanged(t, e, id, before)
				wantSnapshot(t, e, rows)
				a.receive(t, "item_updated", idOf(id), nil, []string{"root"})
				resp := patchMedia(t, e, s.token(t, e, "updater"), id, map[string]any{"tags": []string{"by-updater"}})
				wantStatus(t, "update-only user", resp, http.StatusOK)
				requireEqual(t, "tags", column(t, mediaRow(t, e, id), "tags"), []any{"by-updater"})
				wantMoved(t, e, id, before)
			}),
		scenario(r053, inv, "error", "initial", "patch-library-access-rule",
			"A user with update permission but without access to the item's library gets 403, and keeps access to items of the library it may use.",
			srcs(middlewareSrc, "server/models/User.js:646-650"),
			func(t *testing.T, e *harness.Env) {
				rejected(t, e, e.IDs["novel"], "podcastonly")
				resp := patchMedia(t, e, s.token(t, e, "podcastonly"), e.IDs["podcast"], map[string]any{"tags": []string{"podcast-user"}})
				wantStatus(t, "own library", resp, http.StatusOK)
				requireEqual(t, "tags", column(t, mediaRow(t, e, e.IDs["podcast"]), "tags"), []any{"podcast-user"})
			}),
		scenario(r053, inv, "error", "initial", "patch-tag-rules",
			"The item-tag rules gate the edit: an allow-list user cannot edit an untagged item, a deny-list user cannot edit a tagged item, and each can edit the other.",
			srcs(middlewareSrc, "server/models/User.js:658-683"),
			func(t *testing.T, e *harness.Env) {
				rejected(t, e, e.IDs["novel"], "tagallow")
				rejected(t, e, e.IDs["ebook"], "tagdeny")
				allowed := patchMedia(t, e, s.token(t, e, "tagallow"), e.IDs["ebook"], map[string]any{"metadata": map[string]any{"subtitle": "Tag allow edit"}})
				wantStatus(t, "allow-list user on tagged item", allowed, http.StatusOK)
				denied := patchMedia(t, e, s.token(t, e, "tagdeny"), e.IDs["saga2"], map[string]any{"metadata": map[string]any{"subtitle": "Tag deny edit"}})
				wantStatus(t, "deny-list user on untagged item", denied, http.StatusOK)
				if mediaRow(t, e, e.IDs["ebook"])["subtitle"] != "Tag allow edit" || mediaRow(t, e, e.IDs["saga2"])["subtitle"] != "Tag deny edit" {
					t.Fatal("subtitles were not stored")
				}
			}),
		scenario(r053, inv, "error", "initial", "patch-explicit-content-rule",
			"A user without explicit-content access gets 403 for an explicit item and can edit it again once it is no longer explicit.",
			srcs(middlewareSrc, "server/models/User.js:675-683"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga1"]
				setExplicit(t, e, id, true)
				rejected(t, e, id, "noexplicit")
				setExplicit(t, e, id, false)
				resp := patchMedia(t, e, s.token(t, e, "noexplicit"), id, map[string]any{"metadata": map[string]any{"subtitle": "Clean now"}})
				wantStatus(t, "after explicit cleared", resp, http.StatusOK)
				if mediaRow(t, e, id)["subtitle"] != "Clean now" {
					t.Fatal("subtitle not stored")
				}
			}),
	}
}
