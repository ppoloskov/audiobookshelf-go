package item_write

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	updatedReq = "socket-outgoing-item-updated"
	updatedInv = "INV-SOCKET-OUTGOING-ITEM-UPDATED"
)

// socketItemUpdatedScenarios covers the emitter side of item_updated that the
// media scenarios do not: rejected writes, a no-op repeat and a failed update.
func socketItemUpdatedScenarios(s *setup) []harness.Scenario {
	return []harness.Scenario{
		scenario(updatedReq, updatedInv, "error", "initial", "item-updated-not-emitted-for-rejected-write",
			"A media patch rejected for permission or validation emits no item_updated to any socket.",
			srcs(updateMediaSrc, podcastUpdateSrc, emitterSrc),
			func(t *testing.T, e *harness.Env) {
				id, podcast := e.IDs["novel"], e.IDs["podcast"]
				a := connect(t, e, map[string]string{"root": e.Token(t, "root"), "listener": e.Token(t, "listener")})
				rows := snapshot(t, e)
				wantStatus(t, "no update permission", patchMedia(t, e, e.Token(t, "listener"), id, map[string]any{"metadata": map[string]any{"subtitle": "Denied"}}), http.StatusForbidden)
				wantStatus(t, "invalid cron", patchMedia(t, e, e.Token(t, "root"), podcast, map[string]any{"autoDownloadSchedule": "not a cron", "metadata": map[string]any{"title": "Never"}}), http.StatusBadRequest)
				wantSnapshot(t, e, rows)
				a.receive(t, "item_updated", nil, nil, []string{"root", "listener"})
			}),
		scenario(updatedReq, updatedInv, "normal", "repeat", "item-updated-repeat-noop-is-silent",
			"Repeating a media patch whose value is already stored answers updated:false, leaves updatedAt unchanged and emits no item_updated.",
			srcs(updateMediaSrc, emitterSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga2"]
				body := map[string]any{"metadata": map[string]any{"subtitle": "Repeated socket subtitle"}}
				a := connect(t, e, map[string]string{"root": e.Token(t, "root")})
				first := patchOK(t, e, id, body)
				if !first.Updated {
					t.Fatal("first patch updated = false")
				}
				a.receive(t, "item_updated", idOf(id), []string{"root"}, nil)
				a.reset()
				before, rows := updatedAt(t, e, id), snapshot(t, e)
				second := patchOK(t, e, id, body)
				if second.Updated {
					t.Fatal("repeat patch updated = true")
				}
				wantUnchanged(t, e, id, before)
				wantSnapshot(t, e, rows)
				a.receive(t, "item_updated", idOf(id), nil, []string{"root"})
			}),
		scenario(updatedReq, updatedInv, "error", "failure", "item-updated-failed-cover-leaves-state",
			"A cover url from a user without the upload permission answers 403, stores neither the cover nor the metadata of the payload and emits no item_updated.",
			srcs(updateMediaSrc, controllerSrc+":196-205", emitterSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["novel"]
				token := s.token(t, e, "noupload")
				a := connect(t, e, map[string]string{"root": e.Token(t, "root")})
				before, rows := updatedAt(t, e, id), snapshot(t, e)
				resp := patchMedia(t, e, token, id, map[string]any{"url": "http://127.0.0.1:1/cover.jpg", "metadata": map[string]any{"subtitle": "No upload"}})
				wantStatus(t, "no upload permission", resp, http.StatusForbidden)
				wantUnchanged(t, e, id, before)
				wantSnapshot(t, e, rows)
				a.receive(t, "item_updated", idOf(id), nil, []string{"root"})
			}),
	}
}
