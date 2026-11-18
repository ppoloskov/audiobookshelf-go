package launch

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// binaryScenarios prove the standalone executable boots a fresh or existing
// install and that refused launches fail without persisting the override.
func binaryScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "runtime-binary", Invariant: "INV-RUNTIME-BINARY", Case: "normal", State: "stateless", Assertion: "binary-boots-and-serves-boot-contract",
			Sources: []string{"index.js:55-56", "server/Server.js:180-258", "server/Server.js:384-405"},
			Claim:   "The standalone executable boots a fresh install and serves liveness, uninitialised status, the bundled client and the auth-gated API from one process.",
			Run: func(t *testing.T, e *harness.Env) {
				own := harness.Start(t, harness.Options{Fresh: true})
				defer own.Stop(t)
				checkPing(t, own.Get(t, "", "/ping"))
				resp := own.Get(t, "", "/status")
				requireStatus(t, resp, http.StatusOK, "fresh GET /status")
				var payload launchStatus
				resp.JSON(t, &payload)
				if payload.IsInit || payload.App != "audiobookshelf" || payload.ServerVersion != "2.37.2" {
					t.Fatalf("fresh status identity = %+v", payload)
				}
				entry := own.Get(t, "", "/")
				requireStatus(t, entry, http.StatusOK, "fresh GET /")
				if !strings.HasPrefix(entry.Header.Get("Content-Type"), "text/html") || len(entry.Body) == 0 {
					t.Fatalf("client entry = %q body %.80q", entry.Header.Get("Content-Type"), entry.Body)
				}
				requireStatus(t, own.Get(t, "", "/api/libraries"), http.StatusUnauthorized, "fresh GET /api/libraries")
			},
		},
		{
			Requirement: "runtime-binary", Invariant: "INV-RUNTIME-BINARY", Case: "edge", State: "stateless", Assertion: "binary-reopens-existing-install-identity",
			Sources: []string{"index.js:46-55", "server/Server.js:180-220", "server/Server.js:384-400"},
			Claim:   "Reopening an existing install preserves its identity: the same root user, library ids and initialised status are served after a new process starts on the same tree.",
			Run: func(t *testing.T, e *harness.Env) {
				own := harness.Start(t, harness.Options{})
				defer own.Stop(t)
				checkPing(t, own.Get(t, "", "/ping"))
				libraryID := own.IDs["library"]
				if libraryID == "" {
					t.Fatal("fixture library id is missing")
				}
				before := libraryIDs(t, own)
				if !containsID(before, libraryID) {
					t.Fatalf("first boot did not serve fixture library %q: %v", libraryID, before)
				}
				usersBefore := own.Count(t, "SELECT COUNT(*) FROM users")
				own.Stop(t)

				reopened := harness.Start(t, harness.Options{Install: own.Install})
				defer reopened.Stop(t)
				checkPing(t, reopened.Get(t, "", "/ping"))
				resp := reopened.Get(t, "", "/status")
				requireStatus(t, resp, http.StatusOK, "reopened GET /status")
				var payload launchStatus
				resp.JSON(t, &payload)
				if !payload.IsInit {
					t.Fatal("reopened install reported isInit false")
				}
				if got := libraryIDs(t, reopened); !sameIDs(before, got) {
					t.Fatalf("library ids changed across reopen: %v vs %v", before, got)
				}
				if got := reopened.Count(t, "SELECT COUNT(*) FROM users"); got != usersBefore {
					t.Fatalf("user count changed across reopen: %d vs %d", got, usersBefore)
				}
				// A fresh process must re-authenticate the stored root user.
				if token := reopened.Token(t, "root"); token == "" {
					t.Fatal("root login failed after reopen")
				}
			},
		},
		{
			Requirement: "runtime-binary", Invariant: "INV-RUNTIME-BINARY-REFUSED-001", Case: "error", State: "stateless", Assertion: "binary-refuses-corrupt-config",
			Sources: []string{"index.js:46", "server/Server.js:188", "server/Database.js:180-192"},
			Claim:   "A corrupt absdatabase.sqlite makes the boot exit non-zero without serving HTTP and leaves the supplied database bytes unchanged.",
			Run: func(t *testing.T, e *harness.Env) {
				configDir := t.TempDir()
				database := filepath.Join(configDir, "absdatabase.sqlite")
				corrupt := []byte("not-a-sqlite-database")
				if err := os.WriteFile(database, corrupt, 0o600); err != nil {
					t.Fatalf("write corrupt database: %v", err)
				}
				own := harness.Start(t, harness.Options{
					Fresh:  true,
					NoWait: true,
					Env:    map[string]string{"CONFIG_PATH": configDir},
				})
				defer own.Stop(t)
				code, exited := own.WaitExit(30 * time.Second)
				if !exited || code == 0 {
					t.Fatalf("corrupt database: exited=%v code=%d, want non-zero", exited, code)
				}
				if !tcpRefused(own.Base + "/ping") {
					t.Fatal("a refused corrupt-config boot still answered /ping")
				}
				if got := readFile(t, database); string(got) != string(corrupt) {
					t.Fatalf("corrupt database was modified: %q", got)
				}
				if names := dirNames(t, own.Config); len(names) != 0 {
					t.Fatalf("failed boot wrote into the install config dir: %v", names)
				}
			},
		},
		{
			Requirement: "runtime-binary", Invariant: "INV-RUNTIME-BINARY-REFUSED-001", Case: "error", State: "stateless", Assertion: "binary-refuses-bad-config-path",
			Sources: []string{"index.js:46", "server/Server.js:121-126"},
			Claim:   "A CONFIG_PATH that is a regular file exits non-zero without serving HTTP and leaves both the file and the install config directory unchanged.",
			Run: func(t *testing.T, e *harness.Env) {
				configFile := filepath.Join(t.TempDir(), "config")
				content := []byte("not a directory")
				if err := os.WriteFile(configFile, content, 0o600); err != nil {
					t.Fatalf("write config file: %v", err)
				}
				own := harness.Start(t, harness.Options{
					Fresh:  true,
					NoWait: true,
					Env:    map[string]string{"CONFIG_PATH": configFile},
				})
				defer own.Stop(t)
				code, exited := own.WaitExit(30 * time.Second)
				if !exited || code == 0 {
					t.Fatalf("file CONFIG_PATH: exited=%v code=%d, want non-zero", exited, code)
				}
				if !tcpRefused(own.Base + "/ping") {
					t.Fatal("a refused bad-path boot still answered /ping")
				}
				if got := readFile(t, configFile); string(got) != string(content) {
					t.Fatalf("the CONFIG_PATH file was modified: %q", got)
				}
				if names := dirNames(t, own.Config); len(names) != 0 {
					t.Fatalf("failed boot wrote into the install config dir: %v", names)
				}
			},
		},
		{
			Requirement: "runtime-binary", Invariant: "INV-RUNTIME-BINARY-REFUSED-001", Case: "error", State: "stateless", Assertion: "binary-unwritable-metadata-never-ready",
			Sources: []string{"index.js:47", "server/Server.js:124-126", "server/Server.js:198-204"},
			Claim:   "An unwritable METADATA_PATH never becomes HTTP-ready and leaves no files in the metadata directory.",
			Run: func(t *testing.T, e *harness.Env) {
				metadata := filepath.Join(t.TempDir(), "metadata")
				if err := os.Mkdir(metadata, 0o500); err != nil {
					t.Fatalf("create read-only metadata dir: %v", err)
				}
				own := harness.Start(t, harness.Options{
					Fresh:  true,
					NoWait: true,
					Env:    map[string]string{"METADATA_PATH": metadata},
				})
				// Registered after Start, so this runs before Start's Stop cleanup:
				// the SIGINT handler needs a writable metadata tree to shut down.
				t.Cleanup(func() { _ = os.Chmod(metadata, 0o700) })
				if err := own.WaitReady(3 * time.Second); err == nil {
					t.Fatal("an unwritable METADATA_PATH unexpectedly became ready")
				}
				if names := dirNames(t, metadata); len(names) != 0 {
					t.Fatalf("an unwritable METADATA_PATH gained files: %v", names)
				}
			},
		},
	}
}

// libraryIDs reads the library ids the running server exposes.
func libraryIDs(t *testing.T, e *harness.Env) []string {
	t.Helper()
	resp := e.Get(t, e.Token(t, "root"), "/api/libraries")
	requireStatus(t, resp, http.StatusOK, "GET /api/libraries")
	var answer struct {
		Libraries []struct {
			ID string `json:"id"`
		} `json:"libraries"`
	}
	resp.JSON(t, &answer)
	ids := make([]string, 0, len(answer.Libraries))
	for _, library := range answer.Libraries {
		ids = append(ids, library.ID)
	}
	slices.Sort(ids)
	return ids
}

func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func sameIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
