// Command clientusage inventories the HTTP routes and Socket.IO events that
// clients use and maps each route to the Go handler that serves it (ROADMAP F3).
//
// Sources: the web client (client/ source: $axios calls, URL literals, $socket
// emits and listeners), the iOS app's production-log list (fixed below; the app
// source is not in this workspace) and the mobile steps of the replacement
// trial journey. Upstream routes and their Go handlers come from
// contracts/parity/route-coverage.json.
//
// Regenerate from go-backend:
//
//	go run ./tools/clientusage
//
// The output is deterministic: sorted, input hashes instead of commit ids.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	outputPath        = "go-backend/contracts/client-usage.json"
	routeCoveragePath = "go-backend/contracts/parity/route-coverage.json"
	trialPath         = "go-backend/tools/parity/replacement-trial.cjs"
	clientDir         = "client"

	srcWeb         = "web"
	srcMobileLog   = "mobile-log"
	srcMobileTrial = "mobile-trial"
)

// clientRoots are the client/ directories scanned for calls. players/ is in
// addition to the ROADMAP list: the player sends the playback session requests
// (play, sync, close) and the session track URLs.
var clientRoots = []string{"components", "layouts", "middleware", "mixins", "pages", "players", "plugins", "store"}

// clientSkipDirs are never scanned. cypress holds tests; static and assets hold
// vendored code. A $axios or socket use outside clientRoots is an error.
var clientSkipDirs = []string{".nuxt", "assets", "cypress", "dist", "node_modules", "static"}

var sourceOrder = map[string]int{srcWeb: 0, srcMobileLog: 1, srcMobileTrial: 2}

// axiosPrefixes start a path taken from a $axios call; axiosExact are the bare
// auth and root paths, taken from $axios calls only (they are also page names).
var axiosPrefixes = []string{"/api/", "/auth/", "/public/", "/hls/", "/feed/"}
var axiosExact = []string{"/login", "/logout", "/status", "/init", "/ping", "/healthcheck"}

// socketBuiltins are Socket.IO lifecycle events, not protocol events.
var socketBuiltins = []string{"connect", "connect_error", "disconnect"}

// mobileLog is the fixed list observed in the owner's production logs from the
// iOS app ("METHOD /path"). Path parameters keep the names the app's calls use.
var mobileLog = []string{
	"GET /api/libraries",
	"GET /api/libraries/:id",
	"GET /api/libraries/:id/personalized",
	"GET /api/libraries/:id/items",
	"GET /api/libraries/:id/authors",
	"GET /api/libraries/:id/narrators",
	"GET /api/libraries/:id/series",
	"GET /api/me/listening-stats",
	"GET /api/items/:id",
	"GET /api/items/:id/cover",
	"GET /api/me/item/listening-sessions/:id",
	"GET /api/authors/:id",
	"GET /api/authors/:id/image",
	"GET /api/me/progress/:id",
	"DELETE /api/me/progress/:id",
	"GET /api/me/progress/:id/remove-from-continue-listening",
	"POST /api/session/local",
	"POST /api/authorize",
	"GET /status",
	"GET /auth/openid",
	"GET /auth/openid/mobile-redirect",
	"GET /auth/openid/callback",
}

var (
	// axiosCallRe matches the method part of an axios call; the caller checks
	// that the receiver is an axios instance.
	axiosCallRe = regexp.MustCompile(`\.\s*\$?(get|post|patch|put|delete)\s*\(\s*`)
	// resourceRe finds a resource URL at the start of a literal, or right after
	// a ${...} interpolation (for example ${routerBasePath}/api/items/...).
	resourceRe = regexp.MustCompile("(['\"`}])(/(?:api|public|hls|feed)/)")

	socketEmitRe  = regexp.MustCompile("socket\\.emit\\(\\s*['\"`]([^'\"`\\n]+)['\"`]")
	socketEmitDyn = regexp.MustCompile("socket\\.emit\\(\\s*[^'\"`\\s]")
	socketOnRe    = regexp.MustCompile("socket\\.on\\(\\s*['\"`]([^'\"`\\n]+)['\"`]")
	socketOnDyn   = regexp.MustCompile("socket\\.on\\(\\s*[^'\"`\\s]")

	trialAddRe    = regexp.MustCompile(`^\s*add\('([^']+)',\s*[^,]+,\s*(.*)$`)
	trialMethodRe = regexp.MustCompile(`method: '([A-Z]+)'`)
	trialSessRe   = regexp.MustCompile(`\bepSession\b`)
	contentURLRe  = regexp.MustCompile(`^ctx\.\w+\.audioTracks\[\d+\]\.contentUrl`)
)

// call is one request a client sends, before it is matched to an upstream route.
// An empty Method is inferred from upstream (see urlMethods).
type call struct {
	Method, Path, Caller, Source string
}

// socketSite is one Socket.IO emit or listener.
type socketSite struct {
	Event, Caller string
}

type clientScan struct {
	calls      []call
	emits      []socketSite
	listens    []socketSite
	unresolved []string
}

// upstreamRoute is one route of the pinned upstream server.
type upstreamRoute struct {
	Method, Path, Source, Handler string
	// Exact routes are plain string paths with a real method; only they match.
	Exact bool
}

type usage struct {
	method, path string
	upstream     *upstreamRoute
	sources      map[string]bool
	callers      map[string]bool
}

type fileRef struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type generatedFrom struct {
	RouteCoverage    fileRef  `json:"routeCoverage"`
	ReplacementTrial fileRef  `json:"replacementTrial"`
	ClientRoots      []string `json:"clientRoots"`
	ClientSkipDirs   []string `json:"clientSkipDirs"`
	AxiosPrefixes    []string `json:"axiosPrefixes"`
	AxiosExact       []string `json:"axiosExact"`
	ResourcePrefixes []string `json:"resourcePrefixes"`
	SocketBuiltins   []string `json:"socketBuiltins"`
	Rules            []string `json:"rules"`
}

type routeEntry struct {
	Method    string   `json:"method"`
	Path      string   `json:"path"`
	Sources   []string `json:"sources"`
	Callers   []string `json:"callers"`
	GoHandler string   `json:"goHandler"`
	Upstream  string   `json:"upstream"`
}

type unmatchedEntry struct {
	Method  string   `json:"method"`
	Path    string   `json:"path"`
	Sources []string `json:"sources"`
	Callers []string `json:"callers"`
}

type unusedEntry struct {
	Method    string `json:"method"`
	Path      string `json:"path"`
	GoHandler string `json:"goHandler"`
}

type eventEntry struct {
	Event   string   `json:"event"`
	Callers []string `json:"callers"`
}

type events struct {
	ClientToServer []eventEntry `json:"clientToServer"`
	ServerToClient []eventEntry `json:"serverToClient"`
}

type output struct {
	GeneratedFrom        generatedFrom    `json:"generatedFrom"`
	Routes               []routeEntry     `json:"routes"`
	Events               events           `json:"events"`
	UnusedUpstreamRoutes []unusedEntry    `json:"unusedUpstreamRoutes"`
	Unmatched            []unmatchedEntry `json:"unmatched"`
	// UnresolvedCallSites are requests whose path is a variable or a
	// concatenation, so they are not in routes. A route used only there can
	// appear in unusedUpstreamRoutes; review these sites first.
	UnresolvedCallSites []string `json:"unresolvedCallSites"`
}

// result is the generated file plus what a reviewer needs to check it.
type result struct {
	JSON       []byte
	Unresolved []string
	Routes     int
	Unmatched  int
	Unused     int
}

func main() {
	root, err := repositoryRoot()
	fatalIf(err)
	res, err := generate(root)
	fatalIf(err)
	fatalIf(os.WriteFile(filepath.Join(root, outputPath), res.JSON, 0o644))
	for _, u := range res.Unresolved {
		fmt.Fprintln(os.Stderr, "not resolved (not a literal):", u)
	}
	fmt.Printf("wrote %s: %d routes, %d unmatched, %d unused upstream routes\n", outputPath, res.Routes, res.Unmatched, res.Unused)
}

func generate(repo string) (result, error) {
	covData, err := os.ReadFile(filepath.Join(repo, routeCoveragePath))
	if err != nil {
		return result{}, err
	}
	upstream, err := loadUpstream(covData)
	if err != nil {
		return result{}, fmt.Errorf("%s: %w", routeCoveragePath, err)
	}
	scan, err := scanClient(repo)
	if err != nil {
		return result{}, err
	}
	trial, err := trialCalls(repo)
	if err != nil {
		return result{}, err
	}
	calls := append(append(scan.calls, mobileLogCalls()...), trial...)

	usages := map[string]*usage{}
	record := func(method, path string, up *upstreamRoute, c call) {
		key := method + " " + path
		u := usages[key]
		if u == nil {
			u = &usage{method: method, path: path, upstream: up, sources: map[string]bool{}, callers: map[string]bool{}}
			usages[key] = u
		}
		u.sources[c.Source] = true
		if c.Caller != "" {
			u.callers[c.Caller] = true
		}
	}
	for _, c := range calls {
		methods := []string{c.Method}
		if c.Method == "" {
			methods = urlMethods(upstream, c.Path)
		}
		for _, m := range methods {
			ups := matchUpstream(upstream, m, c.Path)
			if len(ups) == 0 {
				record(m, c.Path, nil, c)
			}
			for _, up := range ups {
				record(m, up.Path, up, c)
			}
		}
	}

	out := output{Routes: []routeEntry{}, UnusedUpstreamRoutes: []unusedEntry{}, Unmatched: []unmatchedEntry{}, UnresolvedCallSites: scan.unresolved}
	var keys []*usage
	for _, u := range usages {
		keys = append(keys, u)
	}
	sort.Slice(keys, func(i, j int) bool { return lessRoute(keys[i].path, keys[i].method, keys[j].path, keys[j].method) })
	for _, u := range keys {
		if u.upstream != nil {
			out.Routes = append(out.Routes, routeEntry{
				Method: u.method, Path: u.path, Sources: sortedSources(u.sources), Callers: sortedCallers(u.callers),
				GoHandler: u.upstream.Handler, Upstream: u.upstream.Source,
			})
		} else {
			out.Unmatched = append(out.Unmatched, unmatchedEntry{
				Method: u.method, Path: u.path, Sources: sortedSources(u.sources), Callers: sortedCallers(u.callers),
			})
		}
	}

	used := map[string]bool{}
	for key := range usages {
		used[key] = true
	}
	for _, up := range upstream {
		if !used[up.Method+" "+up.Path] {
			out.UnusedUpstreamRoutes = append(out.UnusedUpstreamRoutes, unusedEntry{Method: up.Method, Path: up.Path, GoHandler: up.Handler})
		}
	}
	sort.SliceStable(out.UnusedUpstreamRoutes, func(i, j int) bool {
		a, b := out.UnusedUpstreamRoutes[i], out.UnusedUpstreamRoutes[j]
		return lessRoute(a.Path, a.Method, b.Path, b.Method)
	})

	out.Events.ClientToServer = eventList(scan.emits, nil)
	out.Events.ServerToClient = eventList(scan.listens, socketBuiltins)

	covHash, err := fileSHA256(filepath.Join(repo, routeCoveragePath))
	if err != nil {
		return result{}, err
	}
	trialHash, err := fileSHA256(filepath.Join(repo, trialPath))
	if err != nil {
		return result{}, err
	}
	out.GeneratedFrom = generatedFrom{
		RouteCoverage:    fileRef{Path: routeCoveragePath, SHA256: covHash},
		ReplacementTrial: fileRef{Path: trialPath, SHA256: trialHash},
		ClientRoots:      prefixAll("client/", clientRoots),
		ClientSkipDirs:   clientSkipDirs,
		AxiosPrefixes:    axiosPrefixes,
		AxiosExact:       axiosExact,
		ResourcePrefixes: []string{"/api/", "/public/", "/hls/", "/feed/"},
		SocketBuiltins:   socketBuiltins,
		Rules: []string{
			"web: $axios.get|post|patch|put|delete calls whose first argument is a literal; the method is the call's",
			"web: other literals that start with a resource prefix or follow a ${...} interpolation (covers, downloads, feeds, HLS, variable-assigned paths) take the method from upstream: GET if upstream serves GET for the path, else the methods it serves; no match is GET",
			"web: requests whose path is a variable or a concatenation are listed in unresolvedCallSites, not in routes",
			"web: query strings are dropped; each ${...} becomes :param",
			"mobile-log: fixed list in tools/clientusage/main.go (owner production logs, iOS app); no callers",
			"mobile-trial: journeySteps steps labelled mobile: id contains mobile or android, block references an Android or iOS device, an Android or episode session, or the step sits under a // Mobile or // Android comment; contentUrl steps are /api/items/:param/file/:param",
			"matching: segment by segment; a client :param matches any upstream segment (a literal one only when the path has two literal segments; all tied best matches count as used); upstream :name? is optional; a trailing * matches the rest; ALL, regex and dynamic upstream routes never match",
		},
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return result{}, err
	}
	return result{
		JSON:       append(data, '\n'),
		Unresolved: scan.unresolved,
		Routes:     len(out.Routes),
		Unmatched:  len(out.Unmatched),
		Unused:     len(out.UnusedUpstreamRoutes),
	}, nil
}

// loadUpstream reads the upstream route list. The full path joins the router
// mount and the route; a runtime-configured base path is not part of it.
func loadUpstream(data []byte) ([]upstreamRoute, error) {
	var cov struct {
		Routes []struct {
			Upstream struct {
				Method    string `json:"method"`
				Route     string `json:"route"`
				RouteKind string `json:"routeKind"`
				Source    string `json:"source"`
				Line      int    `json:"line"`
				Mount     struct {
					KnownMount *string `json:"known_mount"`
				} `json:"mount"`
			} `json:"upstream"`
			Go struct {
				Evidence *string `json:"evidence"`
			} `json:"go"`
		} `json:"routes"`
	}
	if err := json.Unmarshal(data, &cov); err != nil {
		return nil, err
	}
	routes := make([]upstreamRoute, 0, len(cov.Routes))
	for _, r := range cov.Routes {
		u := r.Upstream
		handler := ""
		if r.Go.Evidence != nil {
			handler = *r.Go.Evidence
		}
		routes = append(routes, upstreamRoute{
			Method:  u.Method,
			Path:    mountedPath(u.Mount.KnownMount, u.Route),
			Source:  fmt.Sprintf("%s:%d", u.Source, u.Line),
			Handler: handler,
			Exact:   u.RouteKind == "string_literal" && u.Method != "ALL",
		})
	}
	return routes, nil
}

func mountedPath(mount *string, route string) string {
	prefix := ""
	if mount != nil && strings.HasPrefix(*mount, "/") {
		prefix = *mount
	}
	if route == "/" {
		if prefix == "" {
			return "/"
		}
		return prefix
	}
	return prefix + route
}

// matchUpstream returns the best exact upstream routes for a client request.
// An empty method matches any method. Several routes can tie (a client
// variable can stand for any of them), and all of them are returned, in
// route-coverage order.
func matchUpstream(upstream []upstreamRoute, method, path string) []*upstreamRoute {
	var best []*upstreamRoute
	bestScore := 0
	for i := range upstream {
		up := &upstream[i]
		if !up.Exact || (method != "" && up.Method != method) {
			continue
		}
		score, ok := matchScore(path, up.Path)
		switch {
		case !ok:
		case best == nil || score > bestScore:
			best, bestScore = []*upstreamRoute{up}, score
		case score == bestScore:
			best = append(best, up)
		}
	}
	return best
}

// urlMethods gives the method of a URL literal used outside $axios (an img
// source, a link, a download, or a variable): GET when upstream serves GET for
// the path, otherwise the methods upstream serves there. No match means GET.
func urlMethods(upstream []upstreamRoute, path string) []string {
	cands := matchUpstream(upstream, "", path)
	if len(cands) == 0 {
		return []string{"GET"}
	}
	set := map[string]bool{}
	for _, up := range cands {
		if up.Method == "GET" {
			return []string{"GET"}
		}
		set[up.Method] = true
	}
	methods := make([]string, 0, len(set))
	for m := range set {
		methods = append(methods, m)
	}
	sort.Strings(methods)
	return methods
}

// matchScore compares a client path with an upstream path. Literal segments
// must be equal; a client variable (:param) matches any upstream segment. A
// variable standing for an upstream literal costs a little, and counts only
// when the client path has two literal segments (so /api/:param matches
// nothing). Upstream :name? is optional, and a trailing * matches the rest.
// A higher score is more specific.
func matchScore(client, upstream string) (int, bool) {
	cs, us := segments(client), segments(upstream)
	literalClient := 0
	for _, c := range cs {
		if !strings.HasPrefix(c, ":") {
			literalClient++
		}
	}
	score := 0
	for i, u := range us {
		if u == "*" && i == len(us)-1 {
			return score - 5, true
		}
		if i >= len(cs) {
			if strings.HasSuffix(u, "?") {
				continue
			}
			return 0, false
		}
		c := cs[i]
		switch {
		case strings.HasPrefix(u, ":"):
		case strings.HasSuffix(u, "*"):
			if i != len(us)-1 || strings.HasPrefix(c, ":") || !strings.HasPrefix(c, strings.TrimSuffix(u, "*")) {
				return 0, false
			}
			score += 5
		case strings.HasPrefix(c, ":"):
			if literalClient < 2 {
				return 0, false
			}
			score -= 3
		case c != u:
			return 0, false
		default:
			score += 10
		}
	}
	if len(cs) > len(us) {
		return 0, false
	}
	return score, true
}

func segments(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// scanClient reads every .vue and .js file under client/. Files outside
// clientRoots must not use $axios or sockets, so a new directory cannot be
// missed silently.
func scanClient(repo string) (clientScan, error) {
	var scan clientScan
	err := filepath.WalkDir(filepath.Join(repo, clientDir), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if contains(clientSkipDirs, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if ext := filepath.Ext(path); ext != ".vue" && ext != ".js" {
			return nil
		}
		rel, err := filepath.Rel(repo, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(data)
		if !underClientRoots(rel) {
			if strings.Contains(text, "$axios") || strings.Contains(text, "socket.emit(") || strings.Contains(text, "socket.on(") {
				return fmt.Errorf("%s uses $axios or a socket outside clientRoots; add its directory to clientRoots", rel)
			}
			return nil
		}
		scanFile(rel, text, &scan)
		return nil
	})
	return scan, err
}

func underClientRoots(rel string) bool {
	parts := strings.Split(rel, "/")
	return len(parts) >= 3 && parts[0] == clientDir && contains(clientRoots, parts[1])
}

func scanFile(rel, text string, scan *clientScan) {
	caller := func(off int) string { return fmt.Sprintf("%s:%d", rel, lineOf(text, off)) }

	// Literal starts already taken by an axios call, so the URL pass skips them.
	consumed := map[int]bool{}
	for _, m := range axiosCallRe.FindAllStringSubmatchIndex(text, -1) {
		if !strings.HasSuffix(strings.TrimRight(text[:m[0]], " \t\r\n"), "axios") {
			continue
		}
		method := strings.ToUpper(text[m[2]:m[3]])
		start := m[1]
		raw, end, ok := readLiteral(text, start)
		if !ok || strings.HasPrefix(strings.TrimLeft(text[end:], " \t\r\n"), "+") {
			scan.unresolved = append(scan.unresolved, fmt.Sprintf("%s $axios.%s(...)", caller(m[0]), strings.ToLower(method)))
			continue
		}
		consumed[start+1] = true
		path := normalize(raw)
		if !hasAxiosPrefix(path) {
			continue
		}
		scan.calls = append(scan.calls, call{Method: method, Path: path, Caller: caller(start), Source: srcWeb})
	}

	for _, m := range resourceRe.FindAllStringSubmatchIndex(text, -1) {
		start := m[4]
		if consumed[start] {
			continue
		}
		scan.calls = append(scan.calls, call{Path: normalize(scanTail(text, start)), Caller: caller(start), Source: srcWeb})
	}

	for _, m := range socketEmitRe.FindAllStringSubmatchIndex(text, -1) {
		scan.emits = append(scan.emits, socketSite{Event: text[m[2]:m[3]], Caller: caller(m[0])})
	}
	for _, m := range socketEmitDyn.FindAllStringIndex(text, -1) {
		scan.unresolved = append(scan.unresolved, fmt.Sprintf("%s socket.emit(...)", caller(m[0])))
	}
	for _, m := range socketOnRe.FindAllStringSubmatchIndex(text, -1) {
		scan.listens = append(scan.listens, socketSite{Event: text[m[2]:m[3]], Caller: caller(m[0])})
	}
	for _, m := range socketOnDyn.FindAllStringIndex(text, -1) {
		scan.unresolved = append(scan.unresolved, fmt.Sprintf("%s socket.on(...)", caller(m[0])))
	}
}

func hasAxiosPrefix(p string) bool {
	for _, prefix := range axiosPrefixes {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return contains(axiosExact, p)
}

// trialCalls reads the journeySteps function of the replacement trial and
// returns the requests of its mobile steps.
func trialCalls(repo string) ([]call, error) {
	data, err := os.ReadFile(filepath.Join(repo, trialPath))
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	start, end := -1, -1
	for i, l := range lines {
		if start < 0 && strings.HasPrefix(l, "function journeySteps(") {
			start = i
		} else if start >= 0 && l == "}" {
			end = i
			break
		}
	}
	if start < 0 || end < 0 {
		return nil, fmt.Errorf("%s: function journeySteps not found", trialPath)
	}
	var calls []call
	mobileSection := false
	for i := start; i < end; i++ {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(t, "//") {
			mobileSection = strings.HasPrefix(t, "// Mobile") || strings.HasPrefix(t, "// Android")
			continue
		}
		m := trialAddRe.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		block := stepBlock(lines, i, end)
		if !mobileSection && !isMobileStep(m[1], block) {
			continue
		}
		method := "GET"
		if mm := trialMethodRe.FindStringSubmatch(block); mm != nil {
			method = mm[1]
		}
		path, err := trialPathOf(m[2], block)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", trialPath, i+1, err)
		}
		calls = append(calls, call{Method: method, Path: path, Caller: fmt.Sprintf("%s:%d", trialPath, i+1), Source: srcMobileTrial})
	}
	return calls, nil
}

func isMobileStep(id, block string) bool {
	text := strings.ToLower(id + " " + block)
	return strings.Contains(text, "mobile") || strings.Contains(text, "android") ||
		strings.Contains(text, "ios_device") || trialSessRe.MatchString(block)
}

// stepBlock is the text of one add(...) step: its line and the lines after it
// up to the next step, comment or statement.
func stepBlock(lines []string, i, end int) string {
	parts := []string{lines[i]}
	for j := i + 1; j < end; j++ {
		t := strings.TrimSpace(lines[j])
		if t == "" || strings.HasPrefix(t, "//") || strings.HasPrefix(t, "add(") || strings.HasPrefix(t, "}") ||
			strings.HasPrefix(t, "for ") || strings.HasPrefix(t, "const ") || strings.HasPrefix(t, "if ") {
			break
		}
		parts = append(parts, lines[j])
	}
	return strings.Join(parts, "\n")
}

// trialPathOf returns the normalized route of a step. A contentUrl step streams
// a direct-play track; its URL is the AudioTrack contentUrl
// (server/objects/files/AudioTrack.js:32), which the journey does not record.
func trialPathOf(rest, block string) (string, error) {
	expr := strings.TrimSpace(rest)
	if strings.HasPrefix(expr, "ctx =>") {
		expr = strings.TrimSpace(strings.TrimPrefix(expr, "ctx =>"))
		if contentURLRe.MatchString(expr) {
			if !strings.Contains(block, "binary: 'bytes'") {
				return "", errors.New("contentUrl step is not a byte stream")
			}
			return "/api/items/:param/file/:param", nil
		}
	}
	if expr == "" || (expr[0] != '`' && expr[0] != '\'') {
		return "", fmt.Errorf("unsupported route expression %q", expr)
	}
	raw, _, ok := readLiteral(expr, 0)
	if !ok {
		return "", errors.New("unterminated route literal")
	}
	if strings.Contains(raw, "${BASE}") {
		return "", errors.New("mobile step uses the test router base")
	}
	return normalize(raw), nil
}

func mobileLogCalls() []call {
	calls := make([]call, 0, len(mobileLog))
	for _, entry := range mobileLog {
		method, path, _ := strings.Cut(entry, " ")
		calls = append(calls, call{Method: method, Path: path, Source: srcMobileLog})
	}
	return calls
}

// eventList groups socket sites by event name, dropping skipped names.
func eventList(sites []socketSite, skip []string) []eventEntry {
	callers := map[string][]string{}
	for _, s := range sites {
		if contains(skip, s.Event) {
			continue
		}
		callers[s.Event] = append(callers[s.Event], s.Caller)
	}
	names := make([]string, 0, len(callers))
	for name := range callers {
		names = append(names, name)
	}
	sort.Strings(names)
	list := make([]eventEntry, 0, len(names))
	for _, name := range names {
		list = append(list, eventEntry{Event: name, Callers: sortedUnique(callers[name])})
	}
	return list
}

// normalize turns a literal into a path: each ${...} becomes :param and the
// query or fragment is dropped.
func normalize(raw string) string {
	var b strings.Builder
	for i := 0; i < len(raw); i++ {
		if raw[i] == '$' && i+1 < len(raw) && raw[i+1] == '{' {
			b.WriteString(":param")
			i = skipInterp(raw, i+1) - 1
			continue
		}
		b.WriteByte(raw[i])
	}
	p := b.String()
	if k := strings.IndexAny(p, "?#"); k >= 0 {
		p = p[:k]
	}
	return p
}

// readLiteral returns the raw text of the string or template literal that
// opens at text[i], and the index just past its closing quote.
func readLiteral(text string, i int) (string, int, bool) {
	if i >= len(text) {
		return "", 0, false
	}
	q := text[i]
	if q != '\'' && q != '"' && q != '`' {
		return "", 0, false
	}
	for j := i + 1; j < len(text); j++ {
		c := text[j]
		switch {
		case c == '\\':
			j++
		case c == q:
			return text[i+1 : j], j + 1, true
		case c == '\n' && q != '`':
			return "", 0, false
		case c == '$' && q == '`' && j+1 < len(text) && text[j+1] == '{':
			j = skipInterp(text, j+1) - 1
		}
	}
	return "", 0, false
}

// scanTail returns the text from i up to the next quote or newline outside
// an interpolation. It is used when the literal's opening quote is not in
// the match.
func scanTail(text string, i int) string {
	for j := i; j < len(text); j++ {
		c := text[j]
		switch {
		case c == '\\':
			j++
		case c == '`' || c == '\'' || c == '"' || c == '\n':
			return text[i:j]
		case c == '$' && j+1 < len(text) && text[j+1] == '{':
			j = skipInterp(text, j+1) - 1
		}
	}
	return text[i:]
}

// skipInterp returns the index just past the brace that closes the one at open.
func skipInterp(text string, open int) int {
	depth := 0
	for k := open; k < len(text); k++ {
		switch text[k] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return k + 1
			}
		}
	}
	return len(text)
}

func lineOf(text string, off int) int {
	return strings.Count(text[:off], "\n") + 1
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func prefixAll(prefix string, list []string) []string {
	out := make([]string, 0, len(list))
	for _, v := range list {
		out = append(out, prefix+v)
	}
	return out
}

func sortedSources(set map[string]bool) []string {
	list := make([]string, 0, len(set))
	for s := range set {
		list = append(list, s)
	}
	sort.Slice(list, func(i, j int) bool { return sourceOrder[list[i]] < sourceOrder[list[j]] })
	return list
}

func sortedCallers(set map[string]bool) []string {
	list := make([]string, 0, len(set))
	for c := range set {
		list = append(list, c)
	}
	return sortedCallerList(list)
}

// sortedUnique returns the values sorted by file, then line number, without duplicates.
func sortedUnique(values []string) []string {
	seen := map[string]bool{}
	var list []string
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			list = append(list, v)
		}
	}
	return sortedCallerList(list)
}

func sortedCallerList(list []string) []string {
	if list == nil {
		list = []string{}
	}
	sort.Slice(list, func(i, j int) bool {
		fi, li := splitCaller(list[i])
		fj, lj := splitCaller(list[j])
		if fi != fj {
			return fi < fj
		}
		return li < lj
	})
	return list
}

func splitCaller(c string) (string, int) {
	k := strings.LastIndex(c, ":")
	if k < 0 {
		return c, 0
	}
	n, err := strconv.Atoi(c[k+1:])
	if err != nil {
		return c, 0
	}
	return c[:k], n
}

func lessRoute(pathA, methodA, pathB, methodB string) bool {
	if pathA != pathB {
		return pathA < pathB
	}
	return methodA < methodB
}

func fileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func repositoryRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go-backend", "go.mod")); err == nil {
			return dir, nil
		}
		if filepath.Dir(dir) == dir {
			return "", fmt.Errorf("could not locate repository root from %s", wd)
		}
	}
}

func fatalIf(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
