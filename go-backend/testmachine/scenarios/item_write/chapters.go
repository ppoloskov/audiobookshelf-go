package item_write

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	r064        = "route-r064"
	chaptersSrc = controllerSrc + ":904-960"
)

func chapterBody(chapters ...any) map[string]any {
	if chapters == nil {
		chapters = []any{}
	}
	return map[string]any{"chapters": chapters}
}

func chapter(title string, start, end float64) map[string]any {
	return map[string]any{"title": title, "start": start, "end": end}
}

func postChapters(t *testing.T, e *harness.Env, token, itemID string, body any) harness.Response {
	t.Helper()
	return e.Post(t, token, "/api/items/"+itemID+"/chapters", body)
}

// chapterResult is the body of POST /api/items/:id/chapters.
type chapterResult struct {
	Success bool `json:"success"`
	Updated bool `json:"updated"`
}

func chaptersOK(t *testing.T, e *harness.Env, itemID string, body any) chapterResult {
	t.Helper()
	resp := postChapters(t, e, e.Token(t, "root"), itemID, body)
	wantStatus(t, "POST chapters", resp, http.StatusOK)
	var result chapterResult
	resp.JSON(t, &result)
	if !result.Success {
		t.Fatalf("success = false: %s", resp.Body)
	}
	return result
}

func chaptersScenarios(s *setup) []harness.Scenario {
	const inv = "INV-R064-CHAPTERS-001"
	const errInv = "INV-ROUTE-R064-CONTRACT"
	return []harness.Scenario{
		scenario(r064, inv, "normal", "initial", "chapters-replace-list",
			"POST chapters with a different number of chapters replaces the list with ids 0..n-1, stores it in books.chapters and metadata.json, answers {success,updated:true} and emits item_updated with the chapters.",
			srcs(chaptersSrc, "client/pages/audiobook/_id/chapters.vue:700-720"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga1"]
				requireEqual(t, "fixture chapters", column(t, mediaRow(t, e, id), "chapters"), []any{
					map[string]any{"id": float64(0), "start": float64(0), "end": 20.062041, "title": "Part 1"},
					map[string]any{"id": float64(1), "start": 20.062041, "end": 40.124082, "title": "Part 2"},
					map[string]any{"id": float64(2), "start": 40.124082, "end": 60.186123, "title": "Part 3"},
				})
				a := itemAudience(t, e, s)
				mediaBefore := mediaUpdatedAt(t, e, id)
				result := chaptersOK(t, e, id, chapterBody(chapter("Intro", 0, 10.5), chapter("Main äö", 10.5, 60.186123)))
				if !result.Updated {
					t.Fatal("updated = false")
				}
				want := []any{
					map[string]any{"id": float64(0), "start": 0.0, "end": 10.5, "title": "Intro"},
					map[string]any{"id": float64(1), "start": 10.5, "end": 60.186123, "title": "Main äö"},
				}
				requireEqual(t, "books.chapters", column(t, mediaRow(t, e, id), "chapters"), want)
				requireEqual(t, "metadata.json chapters", readMetadataFile(t, e, id)["chapters"], want)
				requireEqual(t, "item document chapters", harness.Dig(itemDocument(t, e, id), "media", "chapters"), want)
				if !mediaUpdatedAt(t, e, id).After(mediaBefore) {
					t.Fatal("books.updatedAt did not move")
				}
				events := a.receive(t, "item_updated", idOf(id), []string{"root", "listener"}, []string{"podcastonly", "anonymous"})
				requireEqual(t, "payload chapters", harness.Dig(payload(t, events["root"]), "media", "chapters"), want)
			}),
		scenario(r064, inv, "normal", "changed", "chapters-edit-same-count",
			"With the same number of chapters only changed titles and times are stored, the ids stay, and an unchanged repeat answers updated:false with no event and no row change.",
			srcs(chaptersSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga1"]
				a := itemAudience(t, e, s)
				result := chaptersOK(t, e, id, chapterBody(chapter("Intro", 0, 12), chapter("Main äö", 12, 60.186123)))
				if !result.Updated {
					t.Fatal("updated = false")
				}
				want := []any{
					map[string]any{"id": float64(0), "start": 0.0, "end": 12.0, "title": "Intro"},
					map[string]any{"id": float64(1), "start": 12.0, "end": 60.186123, "title": "Main äö"},
				}
				requireEqual(t, "books.chapters", column(t, mediaRow(t, e, id), "chapters"), want)
				a.receive(t, "item_updated", idOf(id), []string{"root"}, nil)
				a.reset()
				rows := snapshot(t, e)
				if repeat := chaptersOK(t, e, id, chapterBody(chapter("Intro", 0, 12), chapter("Main äö", 12, 60.186123))); repeat.Updated {
					t.Fatal("repeat updated = true")
				}
				wantSnapshot(t, e, rows)
				a.receive(t, "item_updated", idOf(id), nil, []string{"root"})
			}),
		scenario(r064, inv, "edge", "changed", "chapters-empty-list-clears",
			"An empty chapter list clears the stored chapters and metadata.json chapters.",
			srcs(chaptersSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga1"]
				if result := chaptersOK(t, e, id, chapterBody()); !result.Updated {
					t.Fatal("updated = false")
				}
				requireEqual(t, "books.chapters", column(t, mediaRow(t, e, id), "chapters"), []any{})
				requireEqual(t, "metadata.json chapters", readMetadataFile(t, e, id)["chapters"], []any{})
			}),
		scenario(r064, errInv, "error", "failure", "chapters-invalid-payloads-are-400",
			"A body without a chapters array, or a chapter without a title string, numeric start or numeric end answers 400 and changes nothing.",
			srcs(chaptersSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga1"]
				rows := snapshot(t, e)
				for name, body := range map[string]any{
					"no chapters": map[string]any{}, "not a list": map[string]any{"chapters": "x"},
					"empty title":  chapterBody(map[string]any{"title": "", "start": 0, "end": 1}),
					"no title":     chapterBody(map[string]any{"start": 0, "end": 1}),
					"string start": chapterBody(map[string]any{"title": "A", "start": "0", "end": 1}),
					"no end":       chapterBody(map[string]any{"title": "A", "start": 0}),
					"second bad":   chapterBody(chapter("A", 0, 1), map[string]any{"title": "B", "start": 1, "end": nil}),
				} {
					wantStatus(t, name, postChapters(t, e, e.Token(t, "root"), id, body), http.StatusBadRequest)
				}
				wantSnapshot(t, e, rows)
			}),
		scenario(r064, errInv, "error", "initial", "chapters-unsupported-items-are-500",
			"A podcast and a book without audio tracks answer 500 (checked before the payload, so an invalid payload also answers 500) and change nothing.",
			srcs(chaptersSrc),
			func(t *testing.T, e *harness.Env) {
				rows := snapshot(t, e)
				for _, id := range []string{e.IDs["podcast"], e.IDs["ebook"]} {
					wantStatus(t, "valid payload", postChapters(t, e, e.Token(t, "root"), id, chapterBody(chapter("A", 0, 1))), http.StatusInternalServerError)
					wantStatus(t, "invalid payload", postChapters(t, e, e.Token(t, "root"), id, map[string]any{}), http.StatusInternalServerError)
				}
				wantSnapshot(t, e, rows)
			}),
		scenario(r064, errInv, "error", "initial", "chapters-authorisation",
			"POST chapters answers 401 without a token, 404 for an unknown item, 403 for a user without update permission or access to the item, and changes nothing.",
			srcs(middlewareSrc, chaptersSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["saga1"]
				rows := snapshot(t, e)
				body := chapterBody(chapter("Denied", 0, 1))
				wantStatus(t, "anonymous", e.Do(t, harness.Request{Method: http.MethodPost, Path: "/api/items/" + id + "/chapters", JSON: body}), http.StatusUnauthorized)
				wantStatus(t, "unknown", postChapters(t, e, e.Token(t, "listener"), "00000000-0000-4000-8000-000000000000", body), http.StatusNotFound)
				for _, token := range []string{e.Token(t, "listener"), s.token(t, e, "deleter"), s.token(t, e, "podcastonly"), s.token(t, e, "tagallow")} {
					wantStatus(t, "forbidden", postChapters(t, e, token, id, body), http.StatusForbidden)
				}
				wantSnapshot(t, e, rows)
			}),
	}
}
