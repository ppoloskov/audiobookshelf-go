//go:build oracle

package oracle

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestMain runs the oracle test suite and flushes the TS surface coverage
// report and OpenAPI specification when their output env vars are set.
func TestMain(m *testing.M) {
	code := m.Run()
	if outPath := os.Getenv("ORACLE_SURFACE_OUT"); outPath != "" {
		if err := FlushSurfaceReport(outPath, "routes.json", "sockets.json"); err != nil {
			fmt.Fprintf(os.Stderr, "flush surface report: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
	}
	if outPath := os.Getenv("ORACLE_OPENAPI_OUT"); outPath != "" {
		if !filepath.IsAbs(outPath) {
			if root, err := RepoRoot(); err == nil {
				outPath = filepath.Join(root, outPath)
			}
		}
		if err := FlushOpenAPI(outPath, "routes.json"); err != nil {
			fmt.Fprintf(os.Stderr, "flush openapi spec: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
	}
	os.Exit(code)
}

// TestOracleRouteMatrix probes every non-skipped upstream route in routes.json
// across unauthenticated, invalid-token, non-admin, missing-ID, and bad-JSON
// requests against live TS v2.37.2 and Go in parallel.
func TestOracleRouteMatrix(t *testing.T) {
	manifest, err := LoadRouteManifest("routes.json")
	if err != nil {
		t.Fatalf("load routes.json: %v", err)
	}
	tw := StartTwin(t, Options{NoSockets: true})
	report := RunRouteMatrix(t, tw, manifest)
	t.Logf("route matrix: total=%d probed=%d checks=%d acknowledged=%d unexpected=%d",
		report.TotalRoutes, report.ProbedRoutes, report.TotalChecks,
		report.AcknowledgedDiffs, report.UnexpectedDiffs)
}

// TestOracleFlows discovers and runs every declarative JSON flow cassette under
// flows/*.json against live TS v2.37.2 and Go in parallel.
func TestOracleFlows(t *testing.T) {
	entries, err := os.ReadDir("flows")
	if err != nil {
		t.Fatalf("read flows dir: %v", err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			files = append(files, filepath.Join("flows", e.Name()))
		}
	}
	slices.Sort(files)
	if len(files) == 0 {
		t.Fatal("no flow files found under flows/")
	}
	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), ".json")
		t.Run(name, func(t *testing.T) {
			RunFlowFile(t, file)
		})
	}
}
