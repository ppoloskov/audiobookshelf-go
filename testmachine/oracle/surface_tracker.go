package oracle

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// SocketManifest holds the Socket.IO event inventory extracted from server/.
type SocketManifest struct {
	GeneratedFrom string   `json:"generated_from"`
	Inbound       []string `json:"inbound"`
	Outbound      []string `json:"outbound"`
}

// SurfaceReport summarizes live TS HTTP route and Socket.IO event hits.
type SurfaceReport struct {
	RoutesTotal         int      `json:"routes_total"`
	RoutesInScope       int      `json:"routes_in_scope"`
	RoutesHappyHit      int      `json:"routes_happy_hit"`
	RoutesErrorHit      int      `json:"routes_error_hit"`
	RoutesAnyHit        int      `json:"routes_any_hit"`
	UnhitHappyRoutes    []string `json:"unhit_happy_routes"`
	UnhitErrorRoutes    []string `json:"unhit_error_routes"`
	SocketInboundTotal  int      `json:"socket_inbound_total"`
	SocketInboundHit    []string `json:"socket_inbound_hit"`
	SocketInboundUnhit  []string `json:"socket_inbound_unhit"`
	SocketOutboundTotal int      `json:"socket_outbound_total"`
	SocketOutboundHit   []string `json:"socket_outbound_hit"`
	SocketOutboundUnhit []string `json:"socket_outbound_unhit"`
}

type httpCallKey struct {
	method string
	path   string
}

type hitKinds struct {
	happy bool
	err   bool
}

type tracker struct {
	mu             sync.Mutex
	http           map[httpCallKey]hitKinds
	socketInbound  map[string]bool
	socketOutbound map[string]bool
	openapi        *OpenAPIRecorder
}

var surfaceTracker = &tracker{
	http:           make(map[httpCallKey]hitKinds),
	socketInbound:  make(map[string]bool),
	socketOutbound: make(map[string]bool),
	openapi:        NewOpenAPIRecorder(),
}

// RecordHTTP records one HTTP response from a target when isTS is true.
func (t *tracker) RecordHTTP(isTS bool, method, path string, status int) {
	if !isTS || status <= 0 {
		return
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		method = "GET"
	}
	key := httpCallKey{method: method, path: normalizeTrackedPath(path)}
	t.mu.Lock()
	prev := t.http[key]
	prev.happy = prev.happy || (status >= 200 && status < 400)
	prev.err = prev.err || status >= 400
	t.http[key] = prev
	t.mu.Unlock()
}

// RecordHTTPDetail records one HTTP status hit and OpenAPI request/response observation.
func (t *tracker) RecordHTTPDetail(obs HTTPObservation) {
	t.RecordHTTP(obs.IsTS, obs.Method, obs.Path, obs.Status)
	if t.openapi != nil {
		t.openapi.Record(obs)
	}
}

// RecordSocketInbound records an inbound Socket.IO event sent to TS.
func (t *tracker) RecordSocketInbound(isTS bool, name string) {
	if !isTS || name == "" {
		return
	}
	t.mu.Lock()
	t.socketInbound[name] = true
	t.mu.Unlock()
}

// RecordSocketOutbound records an outbound Socket.IO event received from TS.
func (t *tracker) RecordSocketOutbound(isTS bool, name string) {
	if !isTS || name == "" {
		return
	}
	t.mu.Lock()
	t.socketOutbound[name] = true
	t.mu.Unlock()
}

// FlushSurfaceReport matches recorded TS traffic against routesPath and socketsPath
// and writes the JSON summary report to outPath.
func FlushSurfaceReport(outPath, routesPath, socketsPath string) error {
	manifest, err := LoadRouteManifest(routesPath)
	if err != nil {
		return err
	}
	sockets, err := loadSocketManifest(socketsPath)
	if err != nil {
		return err
	}

	surfaceTracker.mu.Lock()
	httpSnap := make(map[httpCallKey]hitKinds, len(surfaceTracker.http))
	for k, v := range surfaceTracker.http {
		httpSnap[k] = v
	}
	inSnap := make(map[string]bool, len(surfaceTracker.socketInbound))
	for k, v := range surfaceTracker.socketInbound {
		inSnap[k] = v
	}
	outSnap := make(map[string]bool, len(surfaceTracker.socketOutbound))
	for k, v := range surfaceTracker.socketOutbound {
		outSnap[k] = v
	}
	surfaceTracker.mu.Unlock()

	routeHits := make([]hitKinds, len(manifest.Routes))
	for key, hits := range httpSnap {
		if idx := matchBestRoute(manifest.Routes, key.method, key.path); idx >= 0 {
			routeHits[idx].happy = routeHits[idx].happy || hits.happy
			routeHits[idx].err = routeHits[idx].err || hits.err
		}
	}

	report := SurfaceReport{
		RoutesTotal:         len(manifest.Routes),
		UnhitHappyRoutes:    make([]string, 0),
		UnhitErrorRoutes:    make([]string, 0),
		SocketInboundTotal:  len(sockets.Inbound),
		SocketOutboundTotal: len(sockets.Outbound),
	}
	for i, route := range manifest.Routes {
		if route.Skip {
			continue
		}
		report.RoutesInScope++
		h := routeHits[i]
		label := route.Method + " " + route.Path
		if h.happy {
			report.RoutesHappyHit++
		} else {
			report.UnhitHappyRoutes = append(report.UnhitHappyRoutes, label)
		}
		if h.err {
			report.RoutesErrorHit++
		} else {
			report.UnhitErrorRoutes = append(report.UnhitErrorRoutes, label)
		}
		if h.happy || h.err {
			report.RoutesAnyHit++
		}
	}
	report.SocketInboundHit, report.SocketInboundUnhit = partitionSocketEvents(sockets.Inbound, inSnap)
	report.SocketOutboundHit, report.SocketOutboundUnhit = partitionSocketEvents(sockets.Outbound, outSnap)

	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode surface report: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		return fmt.Errorf("create surface report dir: %w", err)
	}
	if err := os.WriteFile(outPath, append(encoded, '\n'), 0644); err != nil {
		return fmt.Errorf("write surface report %s: %w", outPath, err)
	}
	return nil
}

func loadSocketManifest(filePath string) (SocketManifest, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return SocketManifest{}, fmt.Errorf("stat socket manifest %s: %w", filePath, err)
	}
	if info.Size() > maxManifestBytes {
		return SocketManifest{}, fmt.Errorf("socket manifest %s exceeds %d bytes", filePath, maxManifestBytes)
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return SocketManifest{}, fmt.Errorf("read socket manifest %s: %w", filePath, err)
	}
	var manifest SocketManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return SocketManifest{}, fmt.Errorf("decode socket manifest %s: %w", filePath, err)
	}
	return manifest, nil
}

func partitionSocketEvents(names []string, seen map[string]bool) ([]string, []string) {
	hit := make([]string, 0)
	unhit := make([]string, 0)
	for _, name := range names {
		if seen[name] {
			hit = append(hit, name)
		} else {
			unhit = append(unhit, name)
		}
	}
	slices.Sort(hit)
	slices.Sort(unhit)
	return hit, unhit
}

func normalizeTrackedPath(raw string) string {
	if idx := strings.IndexAny(raw, "?#"); idx >= 0 {
		raw = raw[:idx]
	}
	if raw == "/audiobookshelf" {
		raw = "/"
	} else if strings.HasPrefix(raw, "/audiobookshelf/") {
		raw = strings.TrimPrefix(raw, "/audiobookshelf")
	}
	if !strings.HasPrefix(raw, "/") {
		raw = "/" + raw
	}
	if len(raw) > 1 {
		raw = strings.TrimSuffix(raw, "/")
	}
	return raw
}

type routeScore struct {
	exactMethod bool
	literals    int
	params      int
	wildcards   int
	nonSkip     bool
}

func (a routeScore) betterThan(b routeScore) bool {
	if a.exactMethod != b.exactMethod {
		return a.exactMethod
	}
	if a.wildcards != b.wildcards {
		return a.wildcards < b.wildcards
	}
	if a.literals != b.literals {
		return a.literals > b.literals
	}
	if a.params != b.params {
		return a.params > b.params
	}
	return a.nonSkip && !b.nonSkip
}

func matchBestRoute(routes []RouteSpec, method, reqPath string) int {
	reqSegs := splitRouteSegments(reqPath)
	bestIdx := -1
	var best routeScore
	for i, route := range routes {
		if route.Method != method && route.Method != "ALL" {
			continue
		}
		patSegs := splitRouteSegments(route.Path)
		lit, par, wild, ok := matchSegments(patSegs, reqSegs, 0, 0)
		if !ok {
			continue
		}
		score := routeScore{
			exactMethod: route.Method == method,
			literals:    lit,
			params:      par,
			wildcards:   wild,
			nonSkip:     !route.Skip,
		}
		if bestIdx < 0 || score.betterThan(best) {
			bestIdx = i
			best = score
		}
	}
	return bestIdx
}

func splitRouteSegments(p string) []string {
	trimmed := strings.Trim(p, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func matchSegments(pat, req []string, pi, ri int) (int, int, int, bool) {
	if pi == len(pat) && ri == len(req) {
		return 0, 0, 0, true
	}
	if pi == len(pat) {
		return 0, 0, 0, false
	}
	seg := pat[pi]
	if seg == "*" && pi == len(pat)-1 {
		return 0, 0, 1, true
	}
	if strings.HasSuffix(seg, "*") && pi == len(pat)-1 {
		if ri < len(req) && strings.HasPrefix(req[ri], strings.TrimSuffix(seg, "*")) {
			return 0, 0, 1, true
		}
		return 0, 0, 0, false
	}
	if strings.HasPrefix(seg, ":") && strings.HasSuffix(seg, "?") {
		if ri < len(req) && req[ri] != "" {
			if l, p, w, ok := matchSegments(pat, req, pi+1, ri+1); ok {
				return l, p + 1, w, true
			}
		}
		return matchSegments(pat, req, pi+1, ri)
	}
	if strings.HasPrefix(seg, ":") {
		if ri < len(req) && req[ri] != "" {
			if l, p, w, ok := matchSegments(pat, req, pi+1, ri+1); ok {
				return l, p + 1, w, true
			}
		}
		return 0, 0, 0, false
	}
	if ri < len(req) && req[ri] == seg {
		if l, p, w, ok := matchSegments(pat, req, pi+1, ri+1); ok {
			return l + 1, p, w, true
		}
	}
	return 0, 0, 0, false
}
