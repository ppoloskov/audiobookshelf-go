package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// sampleJourney has one step of each recorded kind. It is inline, so the
// generator is tested without the trial's journey-upstream.json.
const sampleJourney = `{
  "auth": {"status": 200, "body": {"user": {"id": "u1", "email": null}, "tags": []}},
  "text.ok": {"status": 200, "body": "OK"},
  "error.text": {"status": 404, "body": "Not Found"},
  "bytes": {"status": 206, "body": {"len": 4, "sha": "abc", "type": "audio/mp4; charset=x", "range": "bytes 0-3/9"}},
  "meta": {"status": 200, "body": {"nonEmpty": true, "type": "image/jpeg"}},
  "xml": {"status": 200, "body": {"status": 200, "text": "<rss/>", "type": "application/xml"}},
  "nobody": {"status": 204, "body": null},
  "broken": {"status": 0, "error": "connect ECONNREFUSED"}
}`

func TestGenerateFromInlineJourney(t *testing.T) {
	data, out, err := generate([]byte(sampleJourney))
	if err != nil {
		t.Fatal(err)
	}
	if out.Source.UpstreamCommit != upstreamCommit || out.Source.Generator != "tools/shapes" {
		t.Fatalf("source = %+v", out.Source)
	}
	if got := out.Steps["auth"]; got.Status != 200 || got.ContentType != "application/json" || got.Shape == nil {
		t.Fatalf("auth = %+v", got)
	}
	if got := out.Steps["auth"].Shape.Keys["user"].Keys["email"]; got == nil || got.Kind != "null" || got.Nullable {
		t.Fatalf("email = %+v, want plain null (one sample cannot make it nullable)", got)
	}
	if got := out.Steps["text.ok"]; got.ContentType != "text/plain" || got.Shape != nil {
		t.Fatalf("text.ok = %+v", got)
	}
	if got := out.Steps["bytes"]; got.ContentType != "audio/mp4" || got.Shape != nil {
		t.Fatalf("bytes = %+v, want media type without parameters and no shape", got)
	}
	if got := out.Steps["meta"]; got.ContentType != "image/jpeg" || got.Shape != nil {
		t.Fatalf("meta = %+v", got)
	}
	if got := out.Steps["xml"]; got.ContentType != "application/xml" || got.Shape != nil || got.Status != 200 {
		t.Fatalf("xml = %+v", got)
	}
	if got := out.Steps["nobody"]; got.ContentType != "" || got.Shape != nil {
		t.Fatalf("nobody = %+v, want no content type and no shape", got)
	}
	if got := out.Steps["broken"]; got.Error != "connect ECONNREFUSED" || got.ContentType != "" {
		t.Fatalf("broken = %+v", got)
	}
	if !strings.HasSuffix(string(data), "}\n") {
		t.Fatalf("output does not end with a newline")
	}
	if strings.Index(string(data), `"auth"`) > strings.Index(string(data), `"bytes"`) {
		t.Fatalf("step keys are not sorted")
	}
}

func TestIsBinaryBodyRejectsJSON(t *testing.T) {
	for _, body := range []map[string]any{
		{"user": map[string]any{}},
		{"type": "x"},
		{"text": "x"},
	} {
		if isBinaryBody(body) {
			t.Fatalf("%v read as a binary record", body)
		}
	}
}

// TestCommittedContractIsWellFormed reads contracts/shapes/journey.json. It does
// not regenerate it: that needs the trial's journey-upstream.json.
func TestCommittedContractIsWellFormed(t *testing.T) {
	raw, err := os.ReadFile("../../contracts/shapes/journey.json")
	if err != nil {
		t.Fatal(err)
	}
	var c output
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if c.Source.UpstreamCommit != upstreamCommit {
		t.Fatalf("upstream commit = %s", c.Source.UpstreamCommit)
	}
	if len(c.Steps) == 0 {
		t.Fatal("no steps")
	}
	for id, s := range c.Steps {
		if s.Status == 0 || s.Error != "" {
			t.Errorf("%s: status %d error %q", id, s.Status, s.Error)
		}
		if s.Shape != nil && s.ContentType != "application/json" {
			t.Errorf("%s: shape on non-JSON content type %q", id, s.ContentType)
		}
	}
}
