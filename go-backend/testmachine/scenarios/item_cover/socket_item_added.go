package item_cover

import (
	"net/http"
	"path/filepath"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// createPodcast posts POST /api/podcasts for a new folder in the podcast library.
func createPodcast(t *testing.T, e *harness.Env, token, folder, path string, media map[string]any) harness.Response {
	t.Helper()
	return e.Post(t, token, "/api/podcasts", map[string]any{
		"libraryId": e.IDs["podcastLibrary"], "folderId": folder, "path": path, "media": media,
	})
}

// socketItemAddedScenarios covers the item_added event of podcast creation.
func socketItemAddedScenarios(s *setup) []harness.Scenario {
	const req, inv = "socket-outgoing-item-added", "INV-SOCKET-OUTGOING-ITEM-ADDED"
	prep := func(t *testing.T, e *harness.Env) (folder, root string) {
		t.Helper()
		s.quietWatchers(t, e)
		row := firstRow(t, e, "SELECT id, path FROM libraryFolders WHERE libraryId = ?", e.IDs["podcastLibrary"])
		return row["id"].(string), row["path"].(string)
	}
	return []harness.Scenario{
		scenario(req, inv, "error", "failure", "item-added-not-emitted-on-rejected-podcast",
			"A podcast creation by a non-admin, without media metadata, or with a path outside the library folder is rejected and emits no item_added.",
			srcs(podcastControllerSrc+":42-104", emitterSrc),
			func(t *testing.T, e *harness.Env) {
				folder, root := prep(t, e)
				o := observe(t, e, map[string]string{"root": e.Token(t, "root"), "listener": e.Token(t, "listener")})
				rows := snapshot(t, e)
				media := map[string]any{"metadata": map[string]any{"title": "Rejected Cast"}}
				wantStatus(t, "non-admin", createPodcast(t, e, e.Token(t, "listener"), folder, filepath.Join(root, "Rejected Cast"), media), http.StatusForbidden)
				wantStatus(t, "no media", createPodcast(t, e, e.Token(t, "root"), folder, filepath.Join(root, "Rejected Cast"), map[string]any{}), http.StatusBadRequest)
				wantStatus(t, "outside the folder", createPodcast(t, e, e.Token(t, "root"), folder, filepath.Join(e.Media, "outside"), media), http.StatusBadRequest)
				wantSnapshot(t, e, rows)
				o.none(t, "item_added", anyMatch)
			}),
		scenario(req, inv, "normal", "initial", "item-added-podcast-created-with-cover",
			"Creating a podcast as an admin emits item_added with the created podcast's expanded document (id, libraryId, mediaType, title, coverPath in the item folder) to users with access to the podcast library and to no unauthenticated socket, after the response body.",
			srcs(podcastControllerSrc+":36-186", emitterSrc, "server/SocketAuthority.js:98-112"),
			func(t *testing.T, e *harness.Env) {
				folder, root := prep(t, e)
				image := pngImage(t, 28, 28)
				stub := imageStub(t, "image/png", image)
				path := filepath.Join(root, "New Cast")
				o := observe(t, e, map[string]string{
					"root": e.Token(t, "root"), "listener": e.Token(t, "listener"),
					"podcastonly": s.token(t, e, "podcastonly"), "anonymous": "",
				})
				media := map[string]any{"metadata": map[string]any{
					"title": "New Cast", "author": "Fixture Author", "imageUrl": stub.URL + "/cover.png",
				}}
				resp := createPodcast(t, e, e.Token(t, "root"), folder, path, media)
				wantStatus(t, "create podcast", resp, http.StatusOK)
				created := resp.Object(t)
				id, _ := created["id"].(string)
				requireEqual(t, "created coverPath", harness.Dig(created, "media", "coverPath"), filepath.Join(path, "cover.png"))
				requireEqual(t, "cover file bytes", string(readDisk(t, filepath.Join(path, "cover.png"))), string(image))
				event := o.expectEvent(t, "root", "item_added", idOf(id))
				got := payload(t, event)
				requireEqual(t, "payload id", got["id"], id)
				requireEqual(t, "payload libraryId", got["libraryId"], e.IDs["podcastLibrary"])
				requireEqual(t, "payload mediaType", got["mediaType"], "podcast")
				requireEqual(t, "payload title", harness.Dig(got, "media", "metadata", "title"), "New Cast")
				requireEqual(t, "payload coverPath", harness.Dig(got, "media", "coverPath"), filepath.Join(path, "cover.png"))
				requireEqual(t, "listener payload", payload(t, o.expectEvent(t, "listener", "item_added", idOf(id))), got)
				requireEqual(t, "podcast library user payload", payload(t, o.expectEvent(t, "podcastonly", "item_added", idOf(id))), got)
				o.sockets["anonymous"].None(t, o.marks["anonymous"], "item_added", absentWindow, idOf(id))
				requireEqual(t, "podcast row title", mediaRow(t, e, id)["title"], "New Cast")
				requireEqual(t, "libraryItems path", itemRow(t, e, id)["path"], path)
			}),
		scenario(req, inv, "normal", "changed", "item-added-podcast-without-a-usable-cover",
			"After the first podcast exists, creating another whose image url is not an image still emits item_added with the created item, coverPath null and no cover file.",
			srcs(podcastControllerSrc+":143-183"),
			func(t *testing.T, e *harness.Env) {
				folder, root := prep(t, e)
				bad := imageStub(t, "text/html", []byte("<html>not an image</html>"))
				path := filepath.Join(root, "Second Cast")
				o := observe(t, e, map[string]string{"root": e.Token(t, "root")})
				media := map[string]any{"metadata": map[string]any{"title": "Second Cast", "imageUrl": bad.URL + "/page"}}
				resp := createPodcast(t, e, e.Token(t, "root"), folder, path, media)
				wantStatus(t, "create podcast", resp, http.StatusOK)
				created := resp.Object(t)
				id, _ := created["id"].(string)
				requireEqual(t, "created coverPath", harness.Dig(created, "media", "coverPath"), nil)
				got := payload(t, o.expectEvent(t, "root", "item_added", idOf(id)))
				requireEqual(t, "payload coverPath", harness.Dig(got, "media", "coverPath"), nil)
				requireEqual(t, "payload title", harness.Dig(got, "media", "metadata", "title"), "Second Cast")
				requireEqual(t, "no cover file", dirNames(t, path), []string{})
				requireEqual(t, "podcast row coverPath", mediaCoverPath(t, e, id), nil)
			}),
		scenario(req, inv, "edge", "initial", "item-added-unicode-title-without-image",
			"A podcast created with a Unicode title and no image url emits item_added with the exact title and a null coverPath.",
			srcs(podcastControllerSrc+":143-183", "server/models/Podcast.js:79-121"),
			func(t *testing.T, e *harness.Env) {
				folder, root := prep(t, e)
				path := filepath.Join(root, "Ünïcode Cast ✓")
				o := observe(t, e, map[string]string{"root": e.Token(t, "root")})
				media := map[string]any{"metadata": map[string]any{"title": "Ünïcode Cast ✓", "author": "Ada Example"}}
				resp := createPodcast(t, e, e.Token(t, "root"), folder, path, media)
				wantStatus(t, "create podcast", resp, http.StatusOK)
				created := resp.Object(t)
				id, _ := created["id"].(string)
				got := payload(t, o.expectEvent(t, "root", "item_added", idOf(id)))
				requireEqual(t, "payload title", harness.Dig(got, "media", "metadata", "title"), "Ünïcode Cast ✓")
				requireEqual(t, "payload coverPath", harness.Dig(got, "media", "coverPath"), nil)
				requireEqual(t, "podcast row title", mediaRow(t, e, id)["title"], "Ünïcode Cast ✓")
			}),
		scenario(req, inv, "edge", "repeat", "item-added-not-repeated-for-an-existing-path",
			"Creating a podcast at a path that already exists answers 400 and emits no second item_added.",
			srcs(podcastControllerSrc+":92-104"),
			func(t *testing.T, e *harness.Env) {
				folder, root := prep(t, e)
				path := filepath.Join(root, "Repeat Cast")
				o := observe(t, e, map[string]string{"root": e.Token(t, "root")})
				media := map[string]any{"metadata": map[string]any{"title": "Repeat Cast"}}
				wantStatus(t, "first", createPodcast(t, e, e.Token(t, "root"), folder, path, media), http.StatusOK)
				o.reset()
				rows := snapshot(t, e)
				wantStatus(t, "repeat", createPodcast(t, e, e.Token(t, "root"), folder, path, media), http.StatusBadRequest)
				wantSnapshot(t, e, rows)
				o.none(t, "item_added", anyMatch)
			}),
	}
}
