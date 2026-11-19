package launch

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// nuxtScriptPattern finds the bundled script the retained client's entry
// document loads (client/dist/index.html script tags, e.g.
// /audiobookshelf/_nuxt/6446b40.js), returning it base-relative.
var nuxtScriptPattern = regexp.MustCompile(`<script[^>]+src="[^"]*(_nuxt/[A-Za-z0-9][A-Za-z0-9._-]*\.js)"`)

// requireEntryDocument asserts a response is the retained client's Nuxt entry
// document and returns the bundled script path it names, so the caller can
// request a real asset instead of accepting any HTML page.
func requireEntryDocument(t *testing.T, resp harness.Response, what string) string {
	t.Helper()
	requireStatus(t, resp, http.StatusOK, what)
	if contentType := resp.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("%s content type = %q, want text/html", what, contentType)
	}
	if !strings.Contains(string(resp.Body), `id="__nuxt"`) {
		t.Fatalf("%s did not serve the Nuxt entry document: %.200q", what, resp.Body)
	}
	match := nuxtScriptPattern.FindStringSubmatch(string(resp.Body))
	if match == nil {
		t.Fatalf("%s entry document names no /_nuxt/<hash>.js script: %.200q", what, resp.Body)
	}
	return "/" + match[1]
}

// requireBundledScript asserts the client asset referenced by the entry
// document is served as JavaScript, not as an HTML fallback page.
func requireBundledScript(t *testing.T, resp harness.Response, what string) {
	t.Helper()
	requireStatus(t, resp, http.StatusOK, what)
	if contentType := resp.Header.Get("Content-Type"); !strings.Contains(contentType, "javascript") {
		t.Fatalf("%s content type = %q, want javascript: %.80q", what, contentType, resp.Body)
	}
	if len(resp.Body) == 0 {
		t.Fatalf("%s served an empty script body", what)
	}
	if strings.Contains(string(resp.Body), `id="__nuxt"`) {
		t.Fatalf("%s served the client entry document instead of a script", what)
	}
}

// launchStatus is the GET /status body the boot code reads (server/Server.js:384-399).
type launchStatus struct {
	App           string          `json:"app"`
	ServerVersion string          `json:"serverVersion"`
	IsInit        bool            `json:"isInit"`
	Language      string          `json:"language"`
	ConfigPath    *string         `json:"ConfigPath"`
	MetadataPath  *string         `json:"MetadataPath"`
	AuthMethods   json.RawMessage `json:"authMethods"`
}

func requireStatus(t *testing.T, resp harness.Response, want int, what string) {
	t.Helper()
	if resp.Status != want {
		t.Fatalf("%s: status %d, want %d: %.200q", what, resp.Status, want, resp.Body)
	}
}

// checkPing asserts the unauthenticated liveness body (server/Server.js:401-404).
func checkPing(t *testing.T, resp harness.Response) {
	t.Helper()
	requireStatus(t, resp, http.StatusOK, "GET /ping")
	var payload struct {
		Success bool `json:"success"`
	}
	resp.JSON(t, &payload)
	if !payload.Success {
		t.Fatalf("/ping success = false: %s", resp.Body)
	}
}

// pingOK performs a direct GET of a ping URL outside the harness client, so a
// scenario can observe a port or base path the harness did not select for Env.
func pingOK(url string) bool {
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close() // fully decoded below
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var payload struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&payload); err != nil {
		return false
	}
	return payload.Success
}

// directGet performs a plain GET outside the harness client, for URLs the
// harness did not select as Env.Base.
func directGet(t *testing.T, url string) harness.Response {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close() // fully read below
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return harness.Response{Status: resp.StatusCode, Header: resp.Header, Body: raw}
}

// tcpRefused reports whether nothing answered at url (a refused TCP port or a
// client error). It is used only after the process has had time to bind, so a
// refusal is evidence that a different address was selected.
func tcpRefused(url string) bool {
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return true
	}
	_ = resp.Body.Close() // the body is not inspected; any answer disproves refusal
	return false
}

// freePort reserves and releases a loopback TCP port for a launch override.
func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release port: %v", err)
	}
	return port
}

// waitPing polls a direct URL until it answers the liveness body.
func waitPing(t *testing.T, what, url string) {
	t.Helper()
	harness.Eventually(t, 20*time.Second, what, func() bool { return pingOK(url) })
}

// dirNames lists the names directly under dir, failing on any read error.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read directory %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// readFile reads a small file under a launch test directory.
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// requireAbsent asserts that path was not created.
func requireAbsent(t *testing.T, path, what string) {
	t.Helper()
	_, err := os.Lstat(path)
	switch {
	case err == nil:
		t.Fatalf("refused boot created %s at %s", what, path)
	case !errors.Is(err, fs.ErrNotExist):
		t.Fatalf("stat %s: %v", path, err)
	}
}
