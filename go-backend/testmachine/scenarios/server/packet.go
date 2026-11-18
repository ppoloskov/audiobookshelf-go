// Package server holds the replacement scenarios of the "server" packet:
// unauthenticated liveness and status routes.
package server

import (
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// Packet returns the scenarios of the server packet.
func Packet() harness.Packet {
	return harness.Packet{Name: "server", Scenarios: []harness.Scenario{
		{
			Requirement: "route-r012", Invariant: "INV-ROUTE-R012-ROUTE", Case: "normal", State: "stateless", Assertion: "ping-value-and-repeat",
			Sources: []string{"server/Server.js:401"},
			Claim:   "GET /audiobookshelf/ping answers 200 {success:true} without authentication, every time.",
			Run: func(t *testing.T, e *harness.Env) {
				for range 2 {
					checkPing(t, e.Get(t, "", "/ping"))
				}
			},
		},
		{
			Requirement: "route-r012", Invariant: "INV-ROUTE-R012-ROUTE", Case: "edge", State: "stateless", Assertion: "ping-basepath-and-legacy",
			Sources: []string{"server/Server.js:323-334"},
			Claim:   "GET /ping without the router base path is rewritten to the base path and answers like /audiobookshelf/ping.",
			Run: func(t *testing.T, e *harness.Env) {
				checkPing(t, e.Get(t, "", "/ping"))
				checkPing(t, e.Do(t, harness.Request{Path: "/ping", Raw: true}))
			},
		},
		{
			Requirement: "route-r013", Invariant: "INV-ROUTE-R013-ROUTE", Case: "normal", State: "stateless", Assertion: "health-value-and-repeat",
			Sources: []string{"server/Server.js:405"},
			Claim:   "GET /audiobookshelf/healthcheck answers 200 OK without authentication, every time.",
			Run: func(t *testing.T, e *harness.Env) {
				for range 2 {
					resp := e.Get(t, "", "/healthcheck")
					if resp.Status != 200 || string(resp.Body) != "OK" {
						t.Fatalf("healthcheck must return 200 and OK without authentication, got %d %q", resp.Status, resp.Body)
					}
				}
			},
		},
		{
			Requirement: "route-r011", Invariant: "INV-ROUTE-R011-ROUTE", Case: "normal", State: "stateless", Assertion: "initialized-status-safe-fields",
			Sources: []string{"server/Server.js:384-399"},
			Claim:   "GET /audiobookshelf/status on an initialized server reports app, version 2.37.2 and isInit true, and omits the config and metadata paths.",
			Run: func(t *testing.T, e *harness.Env) {
				resp := e.Get(t, "", "/status")
				var status struct {
					App          string  `json:"app"`
					Version      string  `json:"serverVersion"`
					Initialized  bool    `json:"isInit"`
					ConfigPath   *string `json:"ConfigPath"`
					MetadataPath *string `json:"MetadataPath"`
				}
				resp.JSON(t, &status)
				if resp.Status != 200 || status.App != "audiobookshelf" || status.Version != "2.37.2" || !status.Initialized || status.ConfigPath != nil || status.MetadataPath != nil {
					t.Fatalf("initialized status must expose the pinned app/version and no config/metadata paths: %d %s", resp.Status, resp.Body)
				}
			},
		},
	}}
}

func checkPing(t *testing.T, resp harness.Response) {
	t.Helper()
	var value struct {
		Success bool `json:"success"`
	}
	resp.JSON(t, &value)
	if resp.Status != 200 || !value.Success {
		t.Fatalf("ping must return 200 and success:true without authentication, got %d %s", resp.Status, resp.Body)
	}
}
