package launch

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

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
