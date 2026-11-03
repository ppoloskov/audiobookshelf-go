package contracttest

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestJourneyLibrary covers the library routes the journey does not reach
// (contracts/client-usage.json): a second library and its order, scan, filter
// data, series, narrators, genres, tags, issues and deletion. It runs on its own
// fixture, in order, and records into contracts/shapes/library.json. Record it as
// TestJourney is recorded (see the note in journey_test.go), with the same
// -record, -upstream and -upstream-root flags; the file is written only from a
// full run.
//
// Each library step with an admin-only route is preceded by the listener's 403
// for it. Narrator, genre and tag ids are the client's: base64 of the name, then
// encodeURIComponent (client/plugins/init.client.js $encode).

// libraryShapesFile holds the status, media type and shape each step got from TS
// v2.37.2.
const libraryShapesFile = "../../contracts/shapes/library.json"

// librarySecondName and librarySecondFolder are the second library: its folder is
// created inside the fixture's media root before the run.
const (
	librarySecondName   = "Trial Second Library"
	librarySecondFolder = "second-library"
)

// libraryStep is a journeyStep plus hooks that run before its request and after
// its response, in record and check mode alike. The after hook waits for
// background work the handler started (the scan).
type libraryStep struct {
	journeyStep
	before func(t *testing.T, in *Install, c *journeyCtx)
	after  func(t *testing.T, in *Install, c *journeyCtx)
}

// TestJourneyLibrary runs the library steps against one booted fixture, in order.
func TestJourneyLibrary(t *testing.T) {
	in := journeyInstall(t)
	if err := os.MkdirAll(filepath.Join(in.Dir, "media", librarySecondFolder), 0o755); err != nil {
		t.Fatalf("create second library folder: %v", err)
	}
	c := &journeyCtx{vals: map[string]any{}, tokens: map[string]string{}, wantEvents: true}
	steps := librarySteps(in)
	if *record {
		recordLibrary(t, in, c, steps)
		return
	}
	recorded := readJSON[struct {
		Steps map[string]recordedStep `json:"steps"`
	}](t, libraryShapesFile).Steps
	for _, st := range steps {
		t.Run(st.id, func(t *testing.T) {
			rec, ok := recorded[st.id]
			if !ok {
				t.Fatalf("no recorded shape for step %s in %s", st.id, libraryShapesFile)
			}
			checkJourneyStep(t, rec, runLibraryStep(t, in, c, st))
		})
	}
}

// runLibraryStep sends one step's request, then runs its hook.
func runLibraryStep(t *testing.T, in *Install, c *journeyCtx, st libraryStep) journeyResult {
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

// recordLibrary runs the steps and writes libraryShapesFile. A step that cannot
// run (a missing capture, a failed login) stops the recording.
func recordLibrary(t *testing.T, in *Install, c *journeyCtx, steps []libraryStep) {
	t.Helper()
	meta := readJSON[fixtureMeta](t, filepath.Join(fixtureDir, fixtureFile))
	out := journeyShapesOutput{Steps: map[string]recordedStep{}}
	out.Source.UpstreamCommit = meta.UpstreamCommit
	out.Source.Generator = "go test ./internal/contracttest -run TestJourneyLibrary -record"
	for _, st := range steps {
		if _, dup := out.Steps[st.id]; dup {
			t.Fatalf("duplicate library step %s", st.id)
		}
		out.Steps[st.id] = recordedOf(t, st.id, runLibraryStep(t, in, c, st))
	}
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", libraryShapesFile, err)
	}
	if err := os.WriteFile(libraryShapesFile, append(raw, '\n'), 0o644); err != nil {
		t.Fatalf("write %s: %v", libraryShapesFile, err)
	}
	t.Logf("recorded %d steps to %s", len(steps), libraryShapesFile)
}

// libraryScanWait bounds the wait for a library scan's task_finished event.
const libraryScanWait = 60 * time.Second

// waitForLibraryScan waits for the library-scan task_finished event of libraryID
// on the root socket. The step's before hook cleared earlier finishes, so the
// event seen is this step's scan. A timeout names the step.
func waitForLibraryScan(t *testing.T, c *journeyCtx, stepID, libraryID string) {
	t.Helper()
	if c.events == nil {
		t.Fatalf("step %s: no root socket to wait for the scan of library %s", stepID, libraryID)
	}
	if !c.events.waitScan(libraryID, libraryScanWait) {
		t.Fatalf("step %s: no library-scan task_finished for library %s within %s", stepID, libraryID, libraryScanWait)
	}
}

// libraryNameParam is a name as the client puts it in a path: encodeURIComponent
// of its base64. Base64 has no characters that encodeURIComponent leaves alone
// apart from the ones url.QueryEscape escapes the same way, so it matches.
func libraryNameParam(name string) string {
	return url.QueryEscape(base64.StdEncoding.EncodeToString([]byte(name)))
}

// libraryNewBody is the body the web client posts to create a library
// (getNewLibraryData in client/components/modals/libraries/EditModal.vue).
func libraryNewBody(folder string) map[string]any {
	return map[string]any{
		"name":      librarySecondName,
		"provider":  "google",
		"folders":   []any{map[string]any{"fullPath": folder}},
		"icon":      "database",
		"mediaType": "book",
		"settings": map[string]any{
			"coverAspectRatio":                   1,
			"disableWatcher":                     false,
			"skipMatchingMediaWithAsin":          false,
			"skipMatchingMediaWithIsbn":          false,
			"autoScanCronExpression":             nil,
			"hideSingleBookSeries":               false,
			"onlyShowLaterBooksInContinueSeries": false,
			"metadataPrecedence":                 []any{"folderStructure", "audioMetatags", "nfoFile", "txtFiles", "opfFile", "absMetadata"},
			"markAsFinishedPercentComplete":      nil,
			"markAsFinishedTimeRemaining":        10,
		},
	}
}

// librarySteps is the library journey, in order. Steps mutate the fixture, so
// they must not be reordered. The seed step gives the novel a genre, tags and a
// narrator the library steps rename and remove.
func librarySteps(in *Install) []libraryStep {
	lib := in.IDs["library"]
	folder := filepath.Join(in.Dir, "media", librarySecondFolder)
	newLibrary := func(c *journeyCtx) string { return c.s("newLibrary", "id") }
	libraryPath := func(suffix string) func(*journeyCtx) string {
		return func(c *journeyCtx) string { return "/api/libraries/" + newLibrary(c) + suffix }
	}
	plain := func(s journeyStep) libraryStep { return libraryStep{journeyStep: s} }
	withWait := func(s journeyStep) libraryStep {
		return libraryStep{journeyStep: s,
			before: func(t *testing.T, in *Install, c *journeyCtx) {
				if c.events != nil {
					c.events.forgetScan(lib)
				}
			},
			after: func(t *testing.T, in *Install, c *journeyCtx) {
				waitForLibraryScan(t, c, s.id, lib)
			}}
	}
	return []libraryStep{
		plain(journeyStep{id: "login.root", method: http.MethodPost, route: "/login",
			body:    map[string]any{"username": in.Users["root"].Username, "password": in.Users["root"].Password},
			headers: map[string]string{"X-Return-Tokens": "true"}, loginAs: "root"}),
		plain(journeyStep{id: "login.listener", method: http.MethodPost, route: "/login",
			body:    map[string]any{"username": in.Users["listener"].Username, "password": in.Users["listener"].Password},
			headers: map[string]string{"X-Return-Tokens": "true"}, loginAs: "listener"}),

		// Create and list.
		plain(journeyStep{id: "library.create.listener", who: "listener", method: http.MethodPost, route: "/api/libraries",
			body: libraryNewBody(folder)}),
		plain(journeyStep{id: "library.create", who: "root", method: http.MethodPost, route: "/api/libraries",
			body: libraryNewBody(folder), capture: "newLibrary"}),
		plain(journeyStep{id: "library.list", who: "root", route: "/api/libraries"}),

		// Order: the new library goes first.
		plain(journeyStep{id: "library.order.listener", who: "listener", method: http.MethodPost, route: "/api/libraries/order",
			bodyFn: libraryOrderBody(lib, newLibrary)}),
		plain(journeyStep{id: "library.order", who: "root", method: http.MethodPost, route: "/api/libraries/order",
			bodyFn: libraryOrderBody(lib, newLibrary)}),

		// Seed the novel with a genre, tags and a narrator for the steps below.
		plain(journeyStep{id: "library.seed", who: "root", method: http.MethodPost, route: "/api/items/batch/update", settle: true,
			body: []any{map[string]any{"id": in.IDs["novel"], "mediaPayload": map[string]any{
				"metadata": map[string]any{"genres": []any{"Fiction", "Trial Genre", "Trial Genre Two"}, "narrators": []any{"Trial Narrator"}},
				"tags":     []any{"Trial Tag", "Trial Tag Two"},
			}}}}),

		// Scan the main library and wait for its task.
		plain(journeyStep{id: "library.scan.listener", who: "listener", method: http.MethodPost, route: "/api/libraries/" + lib + "/scan?force=0"}),
		withWait(journeyStep{id: "library.scan", who: "root", method: http.MethodPost, route: "/api/libraries/" + lib + "/scan?force=0"}),

		// Filter data and series.
		plain(journeyStep{id: "library.filterdata", who: "root", route: "/api/libraries/" + lib + "/filterdata"}),
		plain(journeyStep{id: "library.series.list", who: "root", route: "/api/libraries/" + lib + "/series?limit=20&page=0&sort=name&desc=0&minified=1", capture: "seriesList"}),
		plain(journeyStep{id: "library.series.get", who: "root", routeFn: func(c *journeyCtx) string {
			return "/api/libraries/" + lib + "/series/" + c.seriesID("Long Saga") + "?include=progress,rssfeed"
		}}),

		// Narrators: the client's id for "Trial Narrator" is its base64 name.
		plain(journeyStep{id: "library.narrator.rename.listener", who: "listener", method: http.MethodPatch,
			route: "/api/libraries/" + lib + "/narrators/" + libraryNameParam("Trial Narrator"), body: map[string]any{"name": "Trial Narrator Renamed"}}),
		plain(journeyStep{id: "library.narrator.rename", who: "root", method: http.MethodPatch,
			route: "/api/libraries/" + lib + "/narrators/" + libraryNameParam("Trial Narrator"), body: map[string]any{"name": "Trial Narrator Renamed"}}),
		plain(journeyStep{id: "library.narrator.delete.listener", who: "listener", method: http.MethodDelete,
			route: "/api/libraries/" + lib + "/narrators/" + libraryNameParam("Trial Narrator Renamed")}),
		plain(journeyStep{id: "library.narrator.delete", who: "root", method: http.MethodDelete,
			route: "/api/libraries/" + lib + "/narrators/" + libraryNameParam("Trial Narrator Renamed")}),

		// Genres and tags are server-wide and admin-only.
		plain(journeyStep{id: "genres.list.listener", who: "listener", route: "/api/genres"}),
		plain(journeyStep{id: "genres.list", who: "root", route: "/api/genres"}),
		plain(journeyStep{id: "genres.rename.listener", who: "listener", method: http.MethodPost, route: "/api/genres/rename",
			body: map[string]any{"genre": "Trial Genre", "newGenre": "Trial Genre Renamed"}}),
		plain(journeyStep{id: "genres.rename", who: "root", method: http.MethodPost, route: "/api/genres/rename",
			body: map[string]any{"genre": "Trial Genre", "newGenre": "Trial Genre Renamed"}}),
		plain(journeyStep{id: "genres.delete.listener", who: "listener", method: http.MethodDelete,
			route: "/api/genres/" + libraryNameParam("Trial Genre Two")}),
		plain(journeyStep{id: "genres.delete", who: "root", method: http.MethodDelete,
			route: "/api/genres/" + libraryNameParam("Trial Genre Two")}),
		plain(journeyStep{id: "tags.list.listener", who: "listener", route: "/api/tags"}),
		plain(journeyStep{id: "tags.list", who: "root", route: "/api/tags"}),
		plain(journeyStep{id: "tags.rename.listener", who: "listener", method: http.MethodPost, route: "/api/tags/rename",
			body: map[string]any{"tag": "Trial Tag", "newTag": "Trial Tag Renamed"}}),
		plain(journeyStep{id: "tags.rename", who: "root", method: http.MethodPost, route: "/api/tags/rename",
			body: map[string]any{"tag": "Trial Tag", "newTag": "Trial Tag Renamed"}}),
		plain(journeyStep{id: "tags.delete.listener", who: "listener", method: http.MethodDelete,
			route: "/api/tags/" + libraryNameParam("Trial Tag Two")}),
		plain(journeyStep{id: "tags.delete", who: "root", method: http.MethodDelete,
			route: "/api/tags/" + libraryNameParam("Trial Tag Two")}),

		// Issues, then the second library, which is deleted last.
		plain(journeyStep{id: "library.issues.listener", who: "listener", method: http.MethodDelete, route: "/api/libraries/" + lib + "/issues"}),
		plain(journeyStep{id: "library.issues", who: "root", method: http.MethodDelete, route: "/api/libraries/" + lib + "/issues"}),
		plain(journeyStep{id: "library.delete.listener", who: "listener", method: http.MethodDelete, routeFn: libraryPath("")}),
		plain(journeyStep{id: "library.delete", who: "root", method: http.MethodDelete, routeFn: libraryPath("")}),
	}
}

// libraryOrderBody is the reorder body the client sends (LibrariesTable.vue):
// the second library first, the main library second.
func libraryOrderBody(lib string, newLibrary func(*journeyCtx) string) func(*journeyCtx) any {
	return func(c *journeyCtx) any {
		return []any{
			map[string]any{"newOrder": 1, "oldOrder": c.v("newLibrary", "displayOrder"), "id": newLibrary(c)},
			map[string]any{"newOrder": 2, "oldOrder": 1, "id": lib},
		}
	}
}
