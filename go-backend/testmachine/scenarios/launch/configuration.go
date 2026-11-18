package launch

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// configurationScenarios prove that launch-time flags and environment values
// select the installation and listener the server actually uses, and that bad
// startup input is refused without persisting the override.
func configurationScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "runtime-configuration", Invariant: "INV-RUNTIME-CONFIGURATION", Case: "normal", State: "initial", Assertion: "port-env-overrides-default",
			Sources: []string{"index.js:44", "server/Server.js:475-478"},
			Claim:   "The PORT environment value selects the listening TCP port over the built-in 3333 default, observed by /ping answering on that port.",
			Run: func(t *testing.T, e *harness.Env) {
				parsed, err := url.Parse(e.URL)
				if err != nil || parsed.Port() == "" {
					t.Fatalf("parse launch URL %q: %v", e.URL, err)
				}
				if parsed.Port() == "3333" {
					t.Fatal("launch used the built-in 3333 port instead of the environment port")
				}
				checkPing(t, e.Get(t, "", "/ping"))
			},
		},
		{
			Requirement: "runtime-configuration", Invariant: "INV-RUNTIME-CONFIGURATION", Case: "edge", State: "changed", Assertion: "flag-port-overrides-env",
			Sources: []string{"index.js:44", "server/Server.js:475-478"},
			Claim:   "A --port flag overrides the PORT environment value: the flag-selected TCP port serves /ping even though the environment names a different port.",
			Run: func(t *testing.T, e *harness.Env) {
				flagPort := freePort(t)
				own := harness.Start(t, harness.Options{
					NoWait: true,
					Args:   []string{"--port", strconv.Itoa(flagPort)},
				})
				defer own.Stop(t)
				waitPing(t, "flag-selected port", fmt.Sprintf("http://127.0.0.1:%d/audiobookshelf/ping", flagPort))
				// The harness environment port is a different one. When the ports
				// happen to coincide, the stronger refusal check is meaningless.
				if envPort(t, own) != flagPort && !tcpRefused(own.Base+"/ping") {
					t.Fatal("the environment port also served /ping after --port was given")
				}
			},
		},
		{
			Requirement: "runtime-configuration", Invariant: "INV-RUNTIME-CONFIGURATION", Case: "edge", State: "changed", Assertion: "flag-paths-override-env-shown-in-status",
			Sources: []string{"index.js:41-42", "index.js:46-47", "server/Server.js:83-84", "server/Server.js:395-397"},
			Claim:   "The --config and --metadata flags override CONFIG_PATH and METADATA_PATH, and the fresh /status body reports the flag-selected directories instead of the environment ones.",
			Run: func(t *testing.T, e *harness.Env) {
				flagConfig := filepath.Join(t.TempDir(), "flag-config")
				flagMetadata := filepath.Join(t.TempDir(), "flag-metadata")
				own := harness.Start(t, harness.Options{
					Fresh: true,
					Args:  []string{"--config", flagConfig, "--metadata", flagMetadata},
				})
				defer own.Stop(t)
				resp := own.Get(t, "", "/status")
				requireStatus(t, resp, 200, "fresh GET /status with path flags")
				var payload launchStatus
				resp.JSON(t, &payload)
				if payload.ConfigPath == nil || *payload.ConfigPath != flagConfig {
					t.Fatalf("/status ConfigPath = %v, want flag path %q", payload.ConfigPath, flagConfig)
				}
				if payload.MetadataPath == nil || *payload.MetadataPath != flagMetadata {
					t.Fatalf("/status MetadataPath = %v, want flag path %q", payload.MetadataPath, flagMetadata)
				}
				if payload.ConfigPath != nil && *payload.ConfigPath == own.Config {
					t.Fatal("fresh /status still reported the environment CONFIG_PATH")
				}
			},
		},
		{
			Requirement: "runtime-configuration", Invariant: "INV-RUNTIME-CONFIGURATION", Case: "edge", State: "changed", Assertion: "host-flag-overrides-invalid-env",
			Sources: []string{"index.js:45", "server/Server.js:475-478"},
			Claim:   "A --host flag overrides an invalid HOST environment value: the flag-selected loopback address serves /ping while the environment value on its own leaves no listener.",
			Run: func(t *testing.T, e *harness.Env) {
				const invalidHost = "999.999.999.999"
				own := harness.Start(t, harness.Options{
					NoWait: true,
					Env:    map[string]string{"HOST": invalidHost},
					Args:   []string{"--host", "127.0.0.1"},
				})
				defer own.Stop(t)
				waitPing(t, "flag-selected host", own.Base+"/ping")

				envOnly := harness.Start(t, harness.Options{
					NoWait: true,
					Env:    map[string]string{"HOST": invalidHost},
				})
				defer envOnly.Stop(t)
				code, exited := envOnly.WaitExit(30 * time.Second)
				if !exited || code == 0 {
					t.Fatalf("invalid HOST environment alone: exited=%v code=%d, want a non-zero exit", exited, code)
				}
				if !tcpRefused(envOnly.Base + "/ping") {
					t.Fatal("invalid HOST environment alone still answered /ping")
				}
			},
		},
		{
			Requirement: "runtime-configuration", Invariant: "INV-RUNTIME-CONFIGURATION-BASEPATH-001", Case: "edge", State: "initial", Assertion: "router-base-path-from-env",
			Sources: []string{"index.js:50", "server/Server.js:55-63", "server/Server.js:323-334", "server/Server.js:401"},
			Claim:   "ROUTER_BASE_PATH from the environment moves /ping and /status to the configured prefix and away from the /audiobookshelf default.",
			Run: func(t *testing.T, e *harness.Env) {
				const custom = "/launch-prefix"
				own := harness.Start(t, harness.Options{
					NoWait: true,
					Env:    map[string]string{"ROUTER_BASE_PATH": custom},
				})
				defer own.Stop(t)
				prefixed := own.URL + custom
				waitPing(t, "prefixed ping", prefixed+"/ping")
				resp := directGet(t, prefixed+"/status")
				requireStatus(t, resp, 200, "GET prefixed /status")
				var payload launchStatus
				resp.JSON(t, &payload)
				if payload.App != "audiobookshelf" || payload.ServerVersion != "2.37.2" {
					t.Fatalf("prefixed /status identity = %+v", payload)
				}
				if pingOK(own.Base + "/ping") {
					t.Fatal("the default /audiobookshelf/ping still answered after ROUTER_BASE_PATH was changed")
				}
			},
		},
		{
			Requirement: "runtime-configuration", Invariant: "INV-RUNTIME-CONFIGURATION-REJECT-001", Case: "error", State: "failure", Assertion: "port-out-of-range-refused",
			Sources: []string{"index.js:44", "server/Server.js:475-478"},
			Claim:   "An out-of-range PORT exits non-zero without serving HTTP and leaves the install reusable by a later normal boot.",
			Run: func(t *testing.T, e *harness.Env) {
				own := harness.Start(t, harness.Options{
					Fresh:  true,
					NoWait: true,
					Env:    map[string]string{"PORT": "99999"},
				})
				defer own.Stop(t)
				code, exited := own.WaitExit(30 * time.Second)
				if !exited || code == 0 {
					t.Fatalf("out-of-range PORT: exited=%v code=%d, want non-zero", exited, code)
				}
				if !tcpRefused(own.Base + "/ping") {
					t.Fatal("a refused launch still answered /ping")
				}
				retry := harness.Start(t, harness.Options{Install: own.Install})
				defer retry.Stop(t)
				checkPing(t, retry.Get(t, "", "/ping"))
			},
		},
		{
			Requirement: "runtime-configuration", Invariant: "INV-RUNTIME-CONFIGURATION-NONNUM-001", Case: "error", State: "failure", Assertion: "port-nonnumeric-has-no-tcp-listener",
			Sources: []string{"index.js:44", "server/Server.js:475-478"},
			Claim:   "A non-numeric PORT leaves no TCP liveness listener on the configured port.",
			Run: func(t *testing.T, e *harness.Env) {
				socketPath := filepath.Join(t.TempDir(), "launch.sock")
				own := harness.Start(t, harness.Options{
					NoWait: true,
					Env:    map[string]string{"PORT": socketPath},
				})
				defer own.Stop(t)
				if err := own.WaitReady(3 * time.Second); err == nil {
					t.Fatal("a non-numeric PORT unexpectedly served TCP /ping")
				}
			},
		},
		{
			Requirement: "runtime-configuration", Invariant: "INV-RUNTIME-CONFIGURATION", Case: "normal", State: "repeat", Assertion: "config-restart-preserves-boot-contract",
			Sources: []string{"index.js:44-50", "server/Server.js:384-404"},
			Claim:   "Restarting with the same launch configuration keeps /status and /ping answering the same boot contract.",
			Run: func(t *testing.T, e *harness.Env) {
				own := harness.Start(t, harness.Options{})
				defer own.Stop(t)
				first := own.Get(t, "", "/status")
				requireStatus(t, first, 200, "first GET /status")
				checkPing(t, own.Get(t, "", "/ping"))
				own.Restart(t)
				second := own.Get(t, "", "/status")
				requireStatus(t, second, 200, "second GET /status")
				if string(first.Body) != string(second.Body) {
					t.Fatalf("status changed across restart: %s vs %s", first.Body, second.Body)
				}
				checkPing(t, own.Get(t, "", "/ping"))
			},
		},
	}
}

// envPort returns the loopback TCP port the harness selected for the process.
func envPort(t *testing.T, e *harness.Env) int {
	t.Helper()
	parsed, err := url.Parse(e.URL)
	if err != nil {
		t.Fatalf("parse launch URL %q: %v", e.URL, err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("launch URL has no port: %q", e.URL)
	}
	return port
}
