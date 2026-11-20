package oracle

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	maxManifestBytes = 16 << 20
	missingUUID      = "00000000-0000-4000-8000-000000000000"
)

// RouteSpec describes one HTTP route entry in a generated route manifest.
type RouteSpec struct {
	Method     string          `json:"method"`
	Path       string          `json:"path"`
	Auth       string          `json:"auth"`
	Body       json.RawMessage `json:"body,omitempty"`
	Mutates    bool            `json:"mutates,omitempty"`
	Skip       bool            `json:"skip,omitempty"`
	SkipReason string          `json:"skip_reason,omitempty"`
}

// RouteManifest holds the route inventory generated from the pinned TS server.
type RouteManifest struct {
	GeneratedFrom string      `json:"generated_from"`
	Routes        []RouteSpec `json:"routes"`
}

// MatrixReport summarizes the results of probing a RouteManifest against Twin.
type MatrixReport struct {
	TotalRoutes       int       `json:"total_routes"`
	ProbedRoutes      int       `json:"probed_routes"`
	TotalChecks       int       `json:"total_checks"`
	UnexpectedDiffs   int       `json:"unexpected_diffs"`
	AcknowledgedDiffs int       `json:"acknowledged_diffs"`
	Failures          []Finding `json:"failures,omitempty"`
}

type matrixProbe struct {
	kind    string
	role    string
	token   string
	missing bool
	badJSON bool
}

// LoadRouteManifest reads, size-checks, and decodes a RouteManifest JSON file.
func LoadRouteManifest(filePath string) (RouteManifest, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return RouteManifest{}, fmt.Errorf("stat route manifest %s: %w", filePath, err)
	}
	if info.Size() > maxManifestBytes {
		return RouteManifest{}, fmt.Errorf("route manifest %s exceeds %d bytes", filePath, maxManifestBytes)
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return RouteManifest{}, fmt.Errorf("read route manifest %s: %w", filePath, err)
	}
	var manifest RouteManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return RouteManifest{}, fmt.Errorf("decode route manifest %s: %w", filePath, err)
	}
	if len(manifest.Routes) == 0 {
		return RouteManifest{}, fmt.Errorf("route manifest %s: routes must not be empty", filePath)
	}
	return manifest, nil
}

// RunRouteMatrix executes the auth, missing-ID, and malformed-JSON probe matrix
// across every non-skipped route in manifest against both TS and Go servers.
func RunRouteMatrix(t *testing.T, tw *Twin, manifest RouteManifest) MatrixReport {
	t.Helper()
	report := MatrixReport{TotalRoutes: len(manifest.Routes)}
	goDBPath := filepath.Join(tw.Go.Config, "absdatabase.sqlite")

	// Pre-warm fixture tokens before any probe snapshots the database, so lazy
	// /login calls do not insert a sessions row during a rejected-probe check.
	_ = tw.TS.Token(t, "root")
	_ = tw.TS.Token(t, "listener")
	_ = tw.Go.Token(t, "root")
	_ = tw.Go.Token(t, "listener")

	for _, route := range manifest.Routes {
		if route.Skip {
			continue
		}
		probes := buildRouteProbes(route)
		if len(probes) == 0 {
			continue
		}
		report.ProbedRoutes++
		subName := route.Method + " " + route.Path
		t.Run(subName, func(t *testing.T) {
			for _, probe := range probes {
				report.TotalChecks++
				stepName := fmt.Sprintf("%s %s [%s]", route.Method, route.Path, probe.kind)
				findings := executeMatrixProbe(t, tw, goDBPath, route, probe, stepName)
				unexpected, acknowledged := tw.Diff.Filter(findings)
				report.AcknowledgedDiffs += len(acknowledged)
				if len(unexpected) > 0 {
					report.UnexpectedDiffs += len(unexpected)
					report.Failures = append(report.Failures, unexpected...)
					t.Errorf("probe %s unexpected divergences (%d):\n%s", stepName, len(unexpected), FormatFindings(unexpected))
				}
			}
		})
	}
	return report
}

func buildRouteProbes(route RouteSpec) []matrixProbe {
	var probes []matrixProbe
	if route.Auth != "public" {
		probes = append(probes,
			matrixProbe{kind: "anon"},
			matrixProbe{kind: "bad_token", token: "invalid.jwt.token"},
		)
	}
	if route.Auth == "admin" {
		probes = append(probes, matrixProbe{kind: "listener", role: "listener"})
	}
	if hasIDParam(route.Path) && (route.Method == "GET" || route.Method == "PATCH" || route.Method == "DELETE" || !route.Mutates) {
		probes = append(probes, matrixProbe{kind: "missing_id", role: "root", missing: true})
	}
	if route.Method == "POST" || route.Method == "PATCH" || route.Method == "PUT" {
		probes = append(probes, matrixProbe{kind: "bad_json", role: "root", badJSON: true})
	}
	return probes
}

func executeMatrixProbe(t *testing.T, tw *Twin, goDBPath string, route RouteSpec, probe matrixProbe, stepName string) []Finding {
	t.Helper()
	goBefore, err := SnapshotDB(goDBPath)
	if err != nil {
		t.Fatalf("%s: snapshot go db before probe: %v", stepName, err)
	}

	tsReq := buildMatrixRequest(t, tw.TS, route, probe)
	goReq := buildMatrixRequest(t, tw.Go, route, probe)
	tsResp := tw.TS.Do(t, tsReq)
	goResp := tw.Go.Do(t, goReq)

	findings := tw.Diff.DiffHTTP(stepName, tsResp, goResp)
	if tsResp.Status >= 400 && goResp.Status >= 400 {
		goAfter, err := SnapshotDB(goDBPath)
		if err != nil {
			t.Fatalf("%s: snapshot go db after probe: %v", stepName, err)
		}
		findings = append(findings, DiffDBUnchanged(stepName, goBefore, goAfter)...)
	}
	return findings
}

func buildMatrixRequest(t *testing.T, tgt *Target, route RouteSpec, probe matrixProbe) Request {
	t.Helper()
	tok := probe.token
	if probe.role != "" {
		tok = tgt.Token(t, probe.role)
	}
	req := Request{
		Method: route.Method,
		Path:   resolveMatrixPath(route.Path, tgt.IDs, probe.missing),
		Token:  tok,
	}
	if probe.badJSON {
		req.Body = []byte("{invalid json")
		req.Header = map[string]string{"Content-Type": "application/json"}
	} else if len(route.Body) > 0 {
		req.Body = route.Body
		req.Header = map[string]string{"Content-Type": "application/json"}
	}
	return req
}

func hasIDParam(routePath string) bool {
	for _, seg := range strings.Split(routePath, "/") {
		if strings.HasPrefix(seg, ":") {
			name := strings.TrimSuffix(strings.TrimPrefix(seg, ":"), "?")
			switch name {
			case "year", "time", "index", "tag", "genre", "stream", "file":
				continue
			default:
				return true
			}
		}
	}
	return false
}

func resolveMatrixPath(routePath string, ids map[string]string, useMissing bool) string {
	segs := strings.Split(routePath, "/")
	out := make([]string, 0, len(segs))
	for i, seg := range segs {
		if seg == "" {
			continue
		}
		if seg == "*" {
			out = append(out, "test")
			continue
		}
		if strings.HasSuffix(seg, "*") && !strings.HasPrefix(seg, ":") {
			out = append(out, strings.TrimSuffix(seg, "*"))
			continue
		}
		if !strings.HasPrefix(seg, ":") {
			out = append(out, seg)
			continue
		}
		optional := strings.HasSuffix(seg, "?")
		param := strings.TrimSuffix(strings.TrimPrefix(seg, ":"), "?")
		if optional && useMissing {
			continue
		}
		val := resolveMatrixParam(param, prevSegment(segs, i), ids, useMissing)
		out = append(out, val)
	}
	return "/" + strings.Join(out, "/")
}

func prevSegment(segs []string, idx int) string {
	if idx <= 0 {
		return ""
	}
	return segs[idx-1]
}

func resolveMatrixParam(param, prev string, ids map[string]string, useMissing bool) string {
	switch param {
	case "year":
		if useMissing {
			return "1999"
		}
		return "2026"
	case "time":
		if useMissing {
			return "999999"
		}
		return "10"
	case "index":
		if useMissing {
			return "999999"
		}
		return "0"
	case "tag":
		if useMissing {
			return "missing-tag-0000"
		}
		return "nonexistent-tag"
	case "genre":
		if useMissing {
			return "missing-genre-0000"
		}
		return "nonexistent-genre"
	}
	if useMissing {
		return missingUUID
	}
	switch param {
	case "libraryId":
		return lookupID(ids, "library")
	case "itemId", "libraryItemId":
		return lookupID(ids, "novel")
	case "episodeId":
		return lookupID(ids, "ep1")
	case "collectionId":
		return lookupID(ids, "collection")
	case "playlistId":
		return lookupID(ids, "playlist")
	case "userId":
		return lookupID(ids, "listener")
	case "id":
		switch prev {
		case "libraries":
			return lookupID(ids, "library")
		case "items", "item":
			return lookupID(ids, "novel")
		case "collections":
			return lookupID(ids, "collection")
		case "playlists":
			return lookupID(ids, "playlist")
		case "podcasts":
			return lookupID(ids, "podcast")
		case "users":
			return lookupID(ids, "listener")
		}
	}
	return missingUUID
}

func lookupID(ids map[string]string, key string) string {
	if v := ids[key]; v != "" {
		return v
	}
	return missingUUID
}
