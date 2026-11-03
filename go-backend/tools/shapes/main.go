// Command shapes turns the replacement trial's recorded upstream journey into
// the committed response-shape contract, contracts/shapes/journey.json.
//
// For each journey step it records the HTTP status, the content type (media
// type without parameters) and, for JSON bodies only, the shape inferred by
// internal/contracttest/shape. Non-JSON bodies are checked by status and
// content type alone. A step that recorded an error keeps the error text.
//
// Regenerate (from the repository root; needs the trial's work directory):
//
//	node go-backend/tools/parity/replacement-trial.cjs --work /tmp/abs-shapes-work
//	cd go-backend && go run ./tools/shapes \
//	    -journey /tmp/abs-shapes-work/journey-upstream.json \
//	    -out contracts/shapes/journey.json
//
// The trial needs Node 22, `npm ci && npm run build:server` and
// SSRF_REQUEST_FILTER_WHITELIST=127.0.0.1; see AGENTS.md. The trial writes
// journey-upstream.json only after the upstream run, so a fresh run is needed
// whenever the journey changes. journey-upstream.json is not committed.
//
// What the trial records per step (replacement-trial.cjs, runJourney):
//   - JSON responses: the parsed body only (no headers). These are recorded
//     here as application/json.
//   - Plain-text responses: the body string. Recorded as text/plain, which is
//     an inference: the trial does not keep the header.
//   - Binary or text steps: an object with the header "type" and the metadata
//     key(s) of that kind (bytes, meta, zip, xml, text). The media type comes
//     from that header, and no shape is written.
//   - A 204 with no body: recorded with an empty content type.
//
// Keys of the output are sorted (encoding/json sorts map keys).
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"audiobookshelf-compat/internal/contracttest/shape"
)

// upstreamCommit is the pinned TS reference (go-backend/AGENTS.md).
const upstreamCommit = "edb1f7efc92ecacdef51aafb4d9d5054ce10604f"

type journeyStep struct {
	Status int    `json:"status"`
	Error  string `json:"error"`
	Body   any    `json:"body"`
}

type output struct {
	Source source          `json:"source"`
	Steps  map[string]step `json:"steps"`
}

type source struct {
	UpstreamCommit string `json:"upstreamCommit"`
	Generator      string `json:"generator"`
}

type step struct {
	Status      int             `json:"status"`
	ContentType string          `json:"contentType"`
	Shape       *shape.Skeleton `json:"shape,omitempty"`
	Error       string          `json:"error,omitempty"`
}

// binaryKeys are the body keys the trial writes for non-JSON steps.
var binaryKeys = map[string]bool{
	"type": true, "len": true, "sha": true, "range": true, "nonEmpty": true,
	"disposition": true, "entries": true, "text": true, "status": true,
}

// isBinaryBody reports whether a recorded body is one of the trial's non-JSON
// records: a header "type" plus at least one kind marker, and no other keys.
func isBinaryBody(m map[string]any) bool {
	if _, ok := m["type"]; !ok {
		return false
	}
	marked := false
	for k := range m {
		if !binaryKeys[k] {
			return false
		}
		switch k {
		case "len", "nonEmpty", "disposition", "text":
			marked = true
		}
	}
	return marked
}

func mediaType(s any) string {
	str, _ := s.(string)
	media, _, _ := strings.Cut(str, ";")
	return strings.ToLower(strings.TrimSpace(media))
}

// generate builds the contract file from the trial's journey JSON.
func generate(journey []byte) ([]byte, output, error) {
	var steps map[string]journeyStep
	if err := json.Unmarshal(journey, &steps); err != nil {
		return nil, output{}, fmt.Errorf("parse journey: %w", err)
	}
	out := output{
		Source: source{UpstreamCommit: upstreamCommit, Generator: "tools/shapes"},
		Steps:  make(map[string]step, len(steps)),
	}
	for id, s := range steps {
		if s.Error != "" {
			out.Steps[id] = step{Status: s.Status, Error: s.Error}
			continue
		}
		var e step
		e.Status = s.Status
		switch body := s.Body.(type) {
		case map[string]any:
			if isBinaryBody(body) {
				e.ContentType = mediaType(body["type"])
				break
			}
			e.ContentType = "application/json"
			e.Shape = shape.Infer(body)
		case string:
			e.ContentType = "text/plain"
		case nil:
			if s.Status == 204 {
				break
			}
			e.ContentType = "application/json"
			e.Shape = shape.Infer(nil)
		default:
			e.ContentType = "application/json"
			e.Shape = shape.Infer(body)
		}
		out.Steps[id] = e
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return nil, output{}, err
	}
	return buf.Bytes(), out, nil
}

func main() {
	journeyPath := flag.String("journey", "", "journey-upstream.json written by replacement-trial.cjs")
	outPath := flag.String("out", "", "contract file to write (for example contracts/shapes/journey.json)")
	flag.Parse()
	if *journeyPath == "" || *outPath == "" {
		fmt.Fprintln(os.Stderr, "usage: go run ./tools/shapes -journey <journey-upstream.json> -out <journey.json>")
		os.Exit(2)
	}
	raw, err := os.ReadFile(*journeyPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	data, out, err := generate(raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(*outPath, data, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	summarize(out)
}

func summarize(out output) {
	var jsonSteps, textSteps, bodiless, errs int
	for _, s := range out.Steps {
		switch {
		case s.Error != "":
			errs++
		case s.Shape != nil:
			jsonSteps++
		case s.ContentType == "":
			bodiless++
		default:
			textSteps++
		}
	}
	fmt.Fprintf(os.Stderr, "shapes: %d steps (%d JSON, %d non-JSON, %d without body, %d with error)\n",
		len(out.Steps), jsonSteps, textSteps, bodiless, errs)
}
