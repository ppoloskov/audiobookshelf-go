package contracttest

import (
	"encoding/json"
	"image/color"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestJourneyItems is the items domain's journey (ROADMAP F8c): the client
// routes for deleting an item, its chapters, cover, downloads, ebook status,
// ffprobe, library files, metadata object, scans and tracks. It runs on its own
// fixture, in order, and records into contracts/shapes/items.json. Record it the
// way TestJourney is recorded (see the note in journey_test.go), with the same
// -record, -upstream and -upstream-root flags; the file is written only from a
// full run.
//
// The destructive steps act on the disposable item ("Hard Delete Me"). Each
// restricted route is preceded by the listener's refusal for it. Steps mutate
// the fixture, so they must not be reordered.

// itemShapesFile holds the status, media type and shape each step got from TS
// v2.37.2.
const itemShapesFile = "../../contracts/shapes/items.json"

// itemStep is a journeyStep plus hooks that run around its request, in record
// and check mode alike: before sets up a file the request reads or the scan
// finds, after waits for background work the handler started.
type itemStep struct {
	journeyStep
	before func(t *testing.T, in *Install, c *journeyCtx)
	after  func(t *testing.T, in *Install, c *journeyCtx)
}

// TestJourneyItems runs the item steps against one booted fixture, in order.
func TestJourneyItems(t *testing.T) {
	in := journeyInstall(t)
	c := &journeyCtx{vals: map[string]any{}, tokens: map[string]string{}}
	steps := itemSteps(t, in)
	if *record {
		recordItems(t, in, c, steps)
		return
	}
	recorded := readJSON[struct {
		Steps map[string]recordedStep `json:"steps"`
	}](t, itemShapesFile).Steps
	for _, st := range steps {
		t.Run(st.id, func(t *testing.T) {
			rec, ok := recorded[st.id]
			if !ok {
				t.Fatalf("no recorded shape for step %s in %s", st.id, itemShapesFile)
			}
			checkJourneyStep(t, rec, runItemStep(t, in, c, st))
		})
	}
}

// runItemStep runs one step's hooks around its request.
func runItemStep(t *testing.T, in *Install, c *journeyCtx, st itemStep) journeyResult {
	t.Helper()
	if st.before != nil {
		st.before(t, in, c)
	}
	res := runJourneyStep(t, in, c, st.journeyStep, nil)
	if st.after != nil {
		st.after(t, in, c)
	}
	return res
}

// recordItems runs the steps and writes itemShapesFile. A step that cannot run
// (a missing capture, a failed login) stops the recording.
func recordItems(t *testing.T, in *Install, c *journeyCtx, steps []itemStep) {
	t.Helper()
	meta := readJSON[fixtureMeta](t, filepath.Join(fixtureDir, fixtureFile))
	out := journeyShapesOutput{Steps: map[string]recordedStep{}}
	out.Source.UpstreamCommit = meta.UpstreamCommit
	out.Source.Generator = "go test ./internal/contracttest -run TestJourneyItems -record"
	for _, st := range steps {
		if _, dup := out.Steps[st.id]; dup {
			t.Fatalf("duplicate item step %s", st.id)
		}
		out.Steps[st.id] = recordedOf(t, st.id, runItemStep(t, in, c, st))
	}
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", itemShapesFile, err)
	}
	if err := os.WriteFile(itemShapesFile, append(raw, '\n'), 0o644); err != nil {
		t.Fatalf("write %s: %v", itemShapesFile, err)
	}
	t.Logf("recorded %d steps to %s", len(steps), itemShapesFile)
}

// waitForItemFile returns when GET /api/items/<id>?expanded=1 lists a library
// file called filename. A batch scan adds the files it finds; the list is polled,
// and the poll interval only bounds how often it is read.
func waitForItemFile(t *testing.T, in *Install, token, itemID, filename string) {
	t.Helper()
	const limit = 60 * time.Second
	start := time.Now()
	for {
		resp, raw := in.Do(t, http.MethodGet, "/api/items/"+itemID+"?expanded=1", token, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET item while waiting for the scan: status %d: %s", resp.StatusCode, snippet(raw))
		}
		var item struct {
			LibraryFiles []struct {
				Metadata struct {
					Filename string `json:"filename"`
				} `json:"metadata"`
			} `json:"libraryFiles"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			t.Fatalf("GET item: %v: %s", err, snippet(raw))
		}
		for _, f := range item.LibraryFiles {
			if f.Metadata.Filename == filename {
				return
			}
		}
		if time.Since(start) >= limit {
			t.Fatalf("library file %s of item %s not listed after %s", filename, itemID, limit)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// itemSteps is the items journey, in order. Reads come first, then the
// refusals, then the changes. The disposable item is deleted last.
func itemSteps(t *testing.T, in *Install) []itemStep {
	ids := in.IDs
	disposable, ebook := ids["disposable"], ids["ebook"]
	disposableDir := filepath.Join(in.Dir, "media", "audiobooks", "Zed Disposable", "Hard Delete Me")
	audioPath := filepath.Join(disposableDir, "Hard Delete Me.mp3")
	cover := encodeJourneyImage(t, color.RGBA{G: 200, A: 255}, 120, 180, false)
	disposableAudio := func(c *journeyCtx) string { return c.s("disposable", "media", "audioFiles", 0, "ino") }
	ebookFile := func(c *journeyCtx) string { return c.s("ebook", "media", "ebookFile", "ino") }
	ebookStatus := func(c *journeyCtx) string {
		return "/api/items/" + ebook + "/ebook/" + ebookFile(c) + "/status"
	}
	fileRoute := func(c *journeyCtx) string { return "/api/items/" + disposable + "/file/" + disposableAudio(c) }
	ffprobeRoute := func(c *journeyCtx) string { return "/api/items/" + disposable + "/ffprobe/" + disposableAudio(c) }
	chapters := map[string]any{"chapters": []any{
		map[string]any{"id": 0, "title": "Trial Chapter One", "start": 0, "end": 60},
		map[string]any{"id": 1, "title": "Trial Chapter Two", "start": 60, "end": 120},
	}}
	tracks := func(c *journeyCtx) any {
		return map[string]any{"orderedFileData": []any{
			map[string]any{"index": 1, "filename": "Hard Delete Me.mp3", "ino": disposableAudio(c), "exclude": false},
		}}
	}
	uploaded := func(c *journeyCtx) string { return c.s("coverUploaded", "cover") }
	itemRoute := "/api/items/" + disposable
	plain := func(s journeyStep) itemStep { return itemStep{journeyStep: s} }

	return []itemStep{
		plain(journeyStep{id: "login.root", method: http.MethodPost, route: "/login",
			body:    map[string]any{"username": in.Users["root"].Username, "password": in.Users["root"].Password},
			headers: map[string]string{"X-Return-Tokens": "true"}, loginAs: "root"}),
		plain(journeyStep{id: "login.listener", method: http.MethodPost, route: "/login",
			body:    map[string]any{"username": in.Users["listener"].Username, "password": in.Users["listener"].Password},
			headers: map[string]string{"X-Return-Tokens": "true"}, loginAs: "listener"}),

		// Reads the later steps take ids and file inodes from.
		plain(journeyStep{id: "item.get", who: "root", route: itemRoute + "?expanded=1", capture: "disposable"}),
		plain(journeyStep{id: "item.get.ebook", who: "root", route: "/api/items/" + ebook + "?expanded=1", capture: "ebook"}),

		// Restricted routes refuse the listener: deleting, chapters, the cover,
		// tracks, ebook status and files need update or delete; ffprobe, the
		// metadata object and both scans are for admins.
		plain(journeyStep{id: "item.delete.listener", who: "listener", method: http.MethodDelete, route: itemRoute + "?hard=1"}),
		plain(journeyStep{id: "item.chapters.listener", who: "listener", method: http.MethodPost, route: itemRoute + "/chapters", body: chapters}),
		plain(journeyStep{id: "item.cover.upload.listener", who: "listener", method: http.MethodPost, route: itemRoute + "/cover",
			body: multipartForm(t, nil, formFile{field: "cover", name: "trial-cover.jpg", data: cover})}),
		plain(journeyStep{id: "item.cover.patch.listener", who: "listener", method: http.MethodPatch, route: itemRoute + "/cover",
			body: map[string]any{"cover": filepath.Join(disposableDir, "cover.jpg")}}),
		plain(journeyStep{id: "item.cover.delete.listener", who: "listener", method: http.MethodDelete, route: itemRoute + "/cover"}),
		plain(journeyStep{id: "item.tracks.listener", who: "listener", method: http.MethodPatch, route: itemRoute + "/tracks", bodyFn: tracks}),
		plain(journeyStep{id: "item.ebook.status.listener", who: "listener", method: http.MethodPatch, routeFn: ebookStatus}),
		plain(journeyStep{id: "item.file.delete.listener", who: "listener", method: http.MethodDelete, routeFn: fileRoute}),
		plain(journeyStep{id: "item.ffprobe.listener", who: "listener", routeFn: ffprobeRoute}),
		plain(journeyStep{id: "item.metadata-object.listener", who: "listener", route: itemRoute + "/metadata-object"}),
		plain(journeyStep{id: "item.scan.listener", who: "listener", method: http.MethodPost, route: itemRoute + "/scan"}),
		plain(journeyStep{id: "item.batch.scan.listener", who: "listener", method: http.MethodPost, route: "/api/items/batch/scan",
			body: map[string]any{"libraryItemIds": []any{disposable}}}),

		// Reads: the metadata object and ffprobe need the audio file, the
		// downloads stream the folder (zip) and the file.
		plain(journeyStep{id: "item.metadata-object", who: "root", route: itemRoute + "/metadata-object"}),
		plain(journeyStep{id: "item.ffprobe", who: "root", routeFn: ffprobeRoute}),
		plain(journeyStep{id: "item.download", who: "root", route: itemRoute + "/download", tokenQuery: true}),
		plain(journeyStep{id: "item.file.download", who: "root", routeFn: func(c *journeyCtx) string { return fileRoute(c) + "/download" }, tokenQuery: true}),

		// Tracks: keep the one track, as the track editor saves it. Then read
		// the item back.
		plain(journeyStep{id: "item.tracks", who: "root", method: http.MethodPatch, route: itemRoute + "/tracks", bodyFn: tracks}),
		plain(journeyStep{id: "item.get.after-tracks", who: "root", route: itemRoute + "?expanded=1"}),

		// Chapters: set two, then remove them all, as the chapters page does.
		plain(journeyStep{id: "item.chapters.set", who: "root", method: http.MethodPost, route: itemRoute + "/chapters", body: chapters}),
		plain(journeyStep{id: "item.chapters.clear", who: "root", method: http.MethodPost, route: itemRoute + "/chapters",
			body: map[string]any{"chapters": []any{}}}),

		// Cover: the server keeps covers in the item folder, so the upload is
		// scanned in before the cover is set to it. Then the cover is removed.
		plain(journeyStep{id: "item.settings.store-cover", who: "root", method: http.MethodPatch, route: "/api/settings",
			body: map[string]any{"storeCoverWithItem": true}}),
		plain(journeyStep{id: "item.cover.upload", who: "root", method: http.MethodPost, route: itemRoute + "/cover",
			body: multipartForm(t, nil, formFile{field: "cover", name: "trial-cover.jpg", data: cover}), capture: "coverUploaded"}),
		plain(journeyStep{id: "item.scan", who: "root", method: http.MethodPost, route: itemRoute + "/scan"}),
		plain(journeyStep{id: "item.cover.patch", who: "root", method: http.MethodPatch, route: itemRoute + "/cover",
			bodyFn: func(c *journeyCtx) any { return map[string]any{"cover": uploaded(c)} }}),
		plain(journeyStep{id: "item.cover.delete", who: "root", method: http.MethodDelete, route: itemRoute + "/cover"}),

		// Ebook status: the primary ebook becomes supplementary, then primary
		// again. The item is read back after the restore.
		plain(journeyStep{id: "item.ebook.status", who: "root", method: http.MethodPatch, routeFn: ebookStatus}),
		plain(journeyStep{id: "item.ebook.status.restore", who: "root", method: http.MethodPatch, routeFn: ebookStatus}),
		plain(journeyStep{id: "item.get.ebook.after", who: "root", route: "/api/items/" + ebook + "?expanded=1"}),
		// The readers open an ebook by the inode of its library file (the
		// store's ereaderFileId), not the item's primary ebook route.
		plain(journeyStep{id: "item.ebook.file", who: "root", binary: "bytes",
			routeFn: func(c *journeyCtx) string { return "/api/items/" + ebook + "/ebook/" + ebookFile(c) }}),

		// Batch scan: the scan adds a file to the folder, and the step waits
		// until the item lists it.
		{journeyStep: journeyStep{id: "item.batch.scan", who: "root", method: http.MethodPost, route: "/api/items/batch/scan",
			body: map[string]any{"libraryItemIds": []any{disposable}}},
			before: func(t *testing.T, in *Install, c *journeyCtx) {
				if err := os.WriteFile(filepath.Join(disposableDir, "batch-scan.jpg"), cover, 0o644); err != nil {
					t.Fatalf("write the batch scan file: %v", err)
				}
			},
			after: func(t *testing.T, in *Install, c *journeyCtx) {
				waitForItemFile(t, in, c.tokens["root"], disposable, "batch-scan.jpg")
			}},

		// Files and deletion: the audio file goes (the item is then missing),
		// then the item, from the file system too. The scans above can give the
		// file a new inode, so the item is read again for its current one.
		plain(journeyStep{id: "item.get.before-file-delete", who: "root", route: itemRoute + "?expanded=1", capture: "disposableNow"}),
		{journeyStep: journeyStep{id: "item.file.delete", who: "root", method: http.MethodDelete,
			routeFn: func(c *journeyCtx) string {
				return "/api/items/" + disposable + "/file/" + c.s("disposableNow", "media", "audioFiles", 0, "ino")
			}},
			after: func(t *testing.T, in *Install, c *journeyCtx) {
				if _, err := os.Stat(audioPath); !os.IsNotExist(err) {
					t.Errorf("the audio file was not removed from the file system: %v", err)
				}
			}},
		{journeyStep: journeyStep{id: "item.delete", who: "root", method: http.MethodDelete, route: itemRoute + "?hard=1"},
			after: func(t *testing.T, in *Install, c *journeyCtx) {
				if _, err := os.Stat(disposableDir); !os.IsNotExist(err) {
					t.Errorf("the item folder was not removed from the file system: %v", err)
				}
			}},
		plain(journeyStep{id: "item.get.after-delete", who: "root", route: itemRoute + "?expanded=1"}),
	}
}
