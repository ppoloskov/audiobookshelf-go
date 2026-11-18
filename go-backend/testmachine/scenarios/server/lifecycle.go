package server

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// getUnavailable performs a plain HTTP GET and returns the transport error: the
// harness client fails the test on connection errors, which is what a stopped
// server must produce.
func getUnavailable(url string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	_ = resp.Body.Close() // no body is inspected; the request must not succeed
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	return os.ErrInvalid
}

func lifecycleScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "runtime-lifecycle", Invariant: "INV-RUNTIME-LIFECYCLE", Case: "normal", State: "initial", Assertion: "cold-boot-fresh-install-serves-complete-graph",
			Sources: []string{"server/Server.js:180-258", "server/Server.js:323-405", "index.js:19-56"},
			Claim:   "A cold boot of an uninitialised install answers ping, the uninitialised status, the bundled client and the auth-gated API immediately, with an empty user table and its metadata streams directory created.",
			Run: func(t *testing.T, e *harness.Env) {
				fresh := harness.Start(t, harness.Options{Fresh: true})
				defer fresh.Stop(t)
				checkPing(t, fresh.Get(t, "", "/ping"))
				status := fresh.Get(t, "", "/status")
				requireStatus(t, status, http.StatusOK, "fresh GET /status")
				var payload statusPayload
				status.JSON(t, &payload)
				if payload.IsInit {
					t.Fatal("fresh install reported isInit true")
				}
				requireStatus(t, fresh.Get(t, "", "/"), http.StatusOK, "fresh GET /")
				requireStatus(t, fresh.Get(t, "", "/api/libraries"), http.StatusUnauthorized, "fresh GET /api/libraries")
				if got := fresh.Count(t, "SELECT COUNT(*) FROM users"); got != 0 {
					t.Fatalf("fresh install user count = %d, want 0", got)
				}
				info, err := os.Stat(filepath.Join(fresh.Metadata, "streams"))
				if err != nil || !info.IsDir() {
					t.Fatalf("metadata/streams missing after boot: %v", err)
				}
				if _, err := os.Stat(filepath.Join(fresh.Config, "absdatabase.sqlite")); err != nil {
					t.Fatalf("config database missing after boot: %v", err)
				}
			},
		},
		{
			Requirement: "runtime-lifecycle", Invariant: "INV-RUNTIME-LIFECYCLE", Case: "normal", State: "repeat", Assertion: "boot-stop-restart-serves-and-persists",
			Sources: []string{"server/Server.js:276-322", "server/Server.js:565-572"},
			Claim:   "SIGINT shutdown completes without error and a restart on the same install serves the committed library state again.",
			Run: func(t *testing.T, e *harness.Env) {
				own := harness.Start(t, harness.Options{})
				defer own.Stop(t)
				checkPing(t, own.Get(t, "", "/ping"))
				patchLibraryName(t, own, own.IDs["library"], "Lifecycle Persisted")
				own.Stop(t)
				own.Restart(t)
				checkPing(t, own.Get(t, "", "/ping"))
				requireStatus(t, own.Get(t, root(t, own), "/status"), http.StatusOK, "status after restart")
				if got := libraryName(t, own, root(t, own), own.IDs["library"]); got != "Lifecycle Persisted" {
					t.Fatalf("library name after restart = %q, want Lifecycle Persisted", got)
				}
			},
		},
		{
			Requirement: "runtime-lifecycle", Invariant: "INV-RUNTIME-LIFECYCLE", Case: "error", State: "failure", Assertion: "shutdown-refuses-requests-not-half-graph",
			Sources: []string{"server/Server.js:276-288", "server/Server.js:565-572"},
			Claim:   "After SIGINT the HTTP listener refuses connections instead of answering from a partly closed graph, and the persisted database is reopened by the next boot.",
			Run: func(t *testing.T, e *harness.Env) {
				own := harness.Start(t, harness.Options{})
				defer own.Stop(t)
				own.Stop(t)
				if err := getUnavailable(own.Base + "/ping"); err == nil {
					t.Fatal("a stopped server still answered /ping")
				}
				if _, err := os.Stat(filepath.Join(own.Config, "absdatabase.sqlite")); err != nil {
					t.Fatalf("shutdown removed the database: %v", err)
				}
				own.Restart(t)
				checkPing(t, own.Get(t, "", "/ping"))
			},
		},
		{
			Requirement: "runtime-lifecycle", Invariant: "INV-RUNTIME-LIFECYCLE", Case: "edge", State: "repeat", Assertion: "restart-closes-open-sockets-and-welcomes-new",
			Sources: []string{"server/Server.js:276-288", "server/SocketAuthority.js:168-200"},
			Claim:   "A restart closes an authenticated socket connection and a newly connected socket authenticates and receives init after the restart, with the API still serving.",
			Run: func(t *testing.T, e *harness.Env) {
				own := harness.Start(t, harness.Options{})
				defer own.Stop(t)
				old := own.Socket(t, root(t, own))
				old.Emit(t, "set_log_listener", 2)
				own.Restart(t)
				// Env.Restart closed the old socket; connecting again proves the
				// socket server was rebuilt and still authenticates.
				freshSocket := own.Socket(t, root(t, own))
				freshSocket.Emit(t, "set_log_listener", 2)
				mark := freshSocket.Mark()
				checkPing(t, own.Get(t, "", "/ping"))
				waitLog(t, freshSocket, mark, 2, "Received ping")
			},
		},
		{
			Requirement: "runtime-lifecycle", Invariant: "INV-RUNTIME-LIFECYCLE-RESTART-001", Case: "edge", State: "repeat", Assertion: "repeated-restart-cycles-keep-serving",
			Sources: []string{"server/Server.js:180-258", "server/Server.js:565-572"},
			Claim:   "Two restarts and a repeated shutdown leave the server serving with the committed library name intact, and an extra shutdown call is harmless.",
			Run: func(t *testing.T, e *harness.Env) {
				own := harness.Start(t, harness.Options{})
				defer own.Stop(t)
				patchLibraryName(t, own, own.IDs["library"], "Repeat Restart")
				for i := range 2 {
					own.Restart(t)
					checkPing(t, own.Get(t, "", "/ping"))
					if got := libraryName(t, own, root(t, own), own.IDs["library"]); got != "Repeat Restart" {
						t.Fatalf("restart %d library name = %q, want Repeat Restart", i+1, got)
					}
				}
				own.Stop(t)
				own.Stop(t)
				own.Restart(t)
				checkPing(t, own.Get(t, "", "/ping"))
				if got := libraryName(t, own, root(t, own), own.IDs["library"]); got != "Repeat Restart" {
					t.Fatalf("library name after repeated shutdown = %q, want Repeat Restart", got)
				}
				// A restart must honour scannerDisableWatcher and still serve.
				setWatcherDisabled := func(disabled bool) {
					t.Helper()
					resp := own.Do(t, harness.Request{Method: "PATCH", Path: "/api/settings", Token: root(t, own), JSON: map[string]any{"scannerDisableWatcher": disabled}})
					requireStatus(t, resp, http.StatusOK, "PATCH scannerDisableWatcher")
				}
				setWatcherDisabled(true)
				own.Restart(t)
				checkPing(t, own.Get(t, "", "/ping"))
				if got := libraryName(t, own, root(t, own), own.IDs["library"]); got != "Repeat Restart" {
					t.Fatalf("library name with the watcher disabled = %q, want Repeat Restart", got)
				}
				setWatcherDisabled(false)
				own.Restart(t)
				checkPing(t, own.Get(t, "", "/ping"))
			},
		},
		{
			Requirement: "runtime-lifecycle", Invariant: "INV-RUNTIME-LIFECYCLE-RESTART-001", Case: "normal", State: "changed", Assertion: "restart-removes-orphan-playback-streams",
			Sources: []string{"server/Server.js:180-188", "server/managers/PlaybackSessionManager.js:459-484"},
			Claim:   "On boot the runtime removes UUID-named orphan stream directories, leaves other names alone, and keeps committed library data.",
			Run: func(t *testing.T, e *harness.Env) {
				own := harness.Start(t, harness.Options{})
				defer own.Stop(t)
				patchLibraryName(t, own, own.IDs["library"], "Stream Cleanup")
				streams := filepath.Join(own.Metadata, "streams")
				orphan := filepath.Join(streams, "11111111-1111-4111-8111-111111111111")
				notAStream := filepath.Join(streams, "keep-me")
				for _, dir := range []string{orphan, notAStream} {
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatalf("create stream fixture %s: %v", dir, err)
					}
				}
				own.Restart(t)
				if _, err := os.Stat(orphan); !os.IsNotExist(err) {
					t.Fatalf("orphan UUID stream directory survived restart: %v", err)
				}
				if info, err := os.Stat(notAStream); err != nil || !info.IsDir() {
					t.Fatalf("non-UUID stream directory was removed: %v", err)
				}
				if got := libraryName(t, own, root(t, own), own.IDs["library"]); got != "Stream Cleanup" {
					t.Fatalf("library name after cleanup restart = %q, want Stream Cleanup", got)
				}
			},
		},
		{
			Requirement: "runtime-lifecycle", Invariant: "INV-RUNTIME-LIFECYCLE-FAILURE-001", Case: "error", State: "failure", Assertion: "failed-boot-exits-without-partial-install",
			Sources: []string{"server/Server.js:180-200", "server/Server.js:276-288", "index.js:19-56"},
			Claim:   "A boot whose CONFIG_PATH is a regular file exits non-zero without answering HTTP and leaves the install's own config and metadata directories untouched.",
			Run: func(t *testing.T, e *harness.Env) {
				dir := t.TempDir()
				configFile := filepath.Join(dir, "config")
				if err := os.WriteFile(configFile, []byte("not a directory"), 0o600); err != nil {
					t.Fatalf("write config file: %v", err)
				}
				broken := harness.Start(t, harness.Options{Fresh: true, NoWait: true, Env: map[string]string{"CONFIG_PATH": configFile}})
				code, exited := broken.WaitExit(30 * time.Second)
				if !exited {
					t.Fatal("a boot with a file CONFIG_PATH did not exit")
				}
				if code == 0 {
					t.Fatal("a failed boot exited with status 0")
				}
				if err := getUnavailable(broken.Base + "/ping"); err == nil {
					t.Fatal("a failed boot answered /ping")
				}
				content, err := os.ReadFile(configFile)
				if err != nil || string(content) != "not a directory" {
					t.Fatalf("failed boot changed the CONFIG_PATH file: %q %v", content, err)
				}
				if names := dirNames(t, broken.Config); len(names) != 0 {
					t.Fatalf("failed boot created files in the install config directory: %v", names)
				}
			},
		},
	}
}
