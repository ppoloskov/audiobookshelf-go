package contracttest

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/contracttest/shape"
)

// Record mode: TestJourney sends the same steps to a TS v2.37.2 server that
// serve-upstream.cjs started on a copy of the fixture, and writes journey.json
// from the raw responses. Nothing is normalized: a timestamp is a number where
// TS sends a number. The dumps (db.*, fs.*) are read from that copy's database
// and files after the steps, by the same code the check run uses.
var (
	record       = flag.Bool("record", false, "write contracts/shapes/journey.json from the TS server at -upstream, instead of checking Go against it")
	upstream     = flag.String("upstream", "", "base URL of the TS server serve-upstream.cjs printed as READY (with -record)")
	upstreamRoot = flag.String("upstream-root", "", "root directory serve-upstream.cjs printed as ROOT (with -record)")
)

// journeyInstall is the install the steps run against: a Go server booted on
// a copy of the fixture, or with -record the TS server on its copy.
func journeyInstall(t testing.TB) *Install {
	t.Helper()
	switch {
	case *record:
		if *upstream == "" || *upstreamRoot == "" {
			t.Fatal("-record needs -upstream and -upstream-root, as serve-upstream.cjs prints them")
		}
		return upstreamInstall(t, *upstream, *upstreamRoot)
	case *upstream != "" || *upstreamRoot != "":
		t.Fatal("-upstream and -upstream-root need -record")
	}
	return Boot(t)
}

// upstreamInstall is an Install whose requests go to the TS server at baseURL.
// Its database and files are the copy at root, read-only: the dumps read what
// TS wrote there.
func upstreamInstall(t testing.TB, baseURL, root string) *Install {
	t.Helper()
	meta := readJSON[fixtureMeta](t, filepath.Join(fixtureDir, fixtureFile))
	ids := readJSON[map[string]string](t, filepath.Join(fixtureDir, idsFile))
	// TS writes while the dumps and fingerprints read, so wait for its locks
	// (store.OpenReadOnly has no busy timeout). query_only keeps the reads read-only.
	dsn := "file:" + filepath.Join(root, dbRelPath) + "?mode=ro&_pragma=busy_timeout(10000)&_pragma=query_only(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open upstream database: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return &Install{
		BaseURL: baseURL,
		IDs:     ids,
		Users:   meta.Users,
		Dir:     root,
		DB:      db,
		client:  &http.Client{Timeout: requestLimit},
	}
}

// journeyShapesOutput is journey.json: the file format the check run reads.
type journeyShapesOutput struct {
	Source struct {
		UpstreamCommit string `json:"upstreamCommit"`
		Generator      string `json:"generator"`
	} `json:"source"`
	Steps map[string]recordedStep `json:"steps"`
}

// recordJourneyFile runs the steps and the dumps against the install and writes
// shapesFile. A step that fails to run (a capture it needs is missing, a login
// fails) stops the recording: the file is only written from a full run.
func recordJourneyFile(t *testing.T, shapesFile string, in *Install, c *journeyCtx, steps []journeyStep, dumps []string) {
	t.Helper()
	meta := readJSON[fixtureMeta](t, filepath.Join(fixtureDir, fixtureFile))
	out := journeyShapesOutput{Steps: map[string]recordedStep{}}
	out.Source.UpstreamCommit = meta.UpstreamCommit
	out.Source.Generator = "go test ./internal/contracttest -run " + t.Name() + " -record"
	for _, st := range steps {
		if _, dup := out.Steps[st.id]; dup {
			t.Fatalf("duplicate journey step %s", st.id)
		}
		got := runJourneyStep(t, in, c, st, nil)
		rec := recordedOf(t, st.id, got)
		if st.events > 0 {
			events := eventShapesOf(got.events)
			rec.Events = &events
		}
		out.Steps[st.id] = rec
	}
	for _, id := range dumps {
		out.Steps[id] = recordedStep{
			Status:      http.StatusOK,
			ContentType: "application/json",
			Shape:       shape.Infer(dumpBody(t, in, id)),
		}
	}
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", shapesFile, err)
	}
	if err := os.WriteFile(shapesFile, append(raw, '\n'), 0o644); err != nil {
		t.Fatalf("write %s: %v", shapesFile, err)
	}
	t.Logf("recorded %d steps and %d dumps to %s", len(steps), len(dumps), shapesFile)
}

// recordedOf is what journey.json keeps of one response. A body is a shape only
// when its media type is JSON; anything else is checked by status and media
// type.
func recordedOf(t *testing.T, id string, got journeyResult) recordedStep {
	t.Helper()
	rec := recordedStep{Status: got.status, ContentType: got.media}
	if !strings.HasSuffix(got.media, "json") || len(bytes.TrimSpace(got.raw)) == 0 {
		return rec
	}
	var v any
	if err := json.Unmarshal(got.raw, &v); err != nil {
		t.Fatalf("%s: media type %s but the body is not JSON: %v: %s", id, got.media, err, snippet(got.raw))
	}
	rec.Shape = shape.Infer(v)
	return rec
}
