package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// statusPayload is the GET /status body the client boot code reads.
type statusPayload struct {
	App           string          `json:"app"`
	ServerVersion string          `json:"serverVersion"`
	IsInit        bool            `json:"isInit"`
	Language      string          `json:"language"`
	AuthMethods   []string        `json:"authMethods"`
	AuthFormData  json.RawMessage `json:"authFormData"`
	ConfigPath    *string         `json:"ConfigPath"`
	MetadataPath  *string         `json:"MetadataPath"`
}

func (s *suite) statusScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r011", Invariant: "INV-ROUTE-R011-ROUTE", Case: "normal", State: "stateless", Assertion: "status-initialized-public-shape",
			Sources: []string{"server/Server.js:384-399", "client/pages/login.vue", "client/layouts/default.vue"},
			Claim:   "GET /audiobookshelf/status without a token answers 200 with app audiobookshelf, version 2.37.2, isInit true, the language and active auth method, and no ConfigPath or MetadataPath, identically on repetition.",
			Run: func(t *testing.T, e *harness.Env) {
				first := e.Get(t, "", "/status")
				second := e.Get(t, "", "/status")
				for _, resp := range []harness.Response{first, second} {
					requireStatus(t, resp, http.StatusOK, "GET /status")
					var status statusPayload
					resp.JSON(t, &status)
					if status.App != "audiobookshelf" || status.ServerVersion != "2.37.2" || !status.IsInit {
						t.Fatalf("status identity = %+v, want initialized app audiobookshelf 2.37.2", status)
					}
					if status.Language != "en-us" || len(status.AuthMethods) != 1 || status.AuthMethods[0] != "local" {
						t.Fatalf("status language/auth = %q %v, want en-us [local]", status.Language, status.AuthMethods)
					}
					if len(status.AuthFormData) == 0 {
						t.Fatal("status authFormData is empty or absent")
					}
					if status.ConfigPath != nil || status.MetadataPath != nil {
						t.Fatalf("initialized status must omit ConfigPath/MetadataPath, got %+v", status)
					}
				}
				if string(first.Body) != string(second.Body) {
					t.Fatalf("repeat status bodies differ: %s vs %s", first.Body, second.Body)
				}
			},
		},
		{
			Requirement: "route-r011", Invariant: "INV-ROUTE-R011-ROUTE", Case: "error", State: "stateless", Assertion: "status-wrong-method-and-credentials-ignored",
			Sources: []string{"server/Server.js:384-399", "server/Server.js:384"},
			Claim:   "GET /status is public: an invalid bearer token still receives the same 200 payload, while POST /status is not a registered route and answers 404.",
			Run: func(t *testing.T, e *harness.Env) {
				valid := e.Get(t, "", "/status")
				requireStatus(t, valid, http.StatusOK, "GET /status")
				sent := e.Do(t, harness.Request{Path: "/status", Token: "not-a-token"})
				requireStatus(t, sent, http.StatusOK, "GET /status with an invalid token")
				if string(sent.Body) != string(valid.Body) {
					t.Fatalf("invalid credentials changed the public status body: %s vs %s", sent.Body, valid.Body)
				}
				requireStatus(t, e.Do(t, harness.Request{Method: "POST", Path: "/status"}), http.StatusNotFound, "POST /status")
			},
		},
		{
			Requirement: "route-r011", Invariant: "INV-ROUTE-R011-ROUTE", Case: "edge", State: "stateless", Assertion: "status-uninitialized-paths",
			Sources: []string{"server/Server.js:390-397", "client/pages/login.vue"},
			Claim:   "On a cold, uninitialised install GET /status answers 200 with isInit false and the actual ConfigPath and MetadataPath directories.",
			Run: func(t *testing.T, e *harness.Env) {
				fresh := harness.Start(t, harness.Options{Fresh: true})
				defer fresh.Stop(t)
				resp := fresh.Get(t, "", "/status")
				requireStatus(t, resp, http.StatusOK, "GET /status on a fresh install")
				var status statusPayload
				resp.JSON(t, &status)
				if status.IsInit || status.App != "audiobookshelf" || status.ServerVersion != "2.37.2" {
					t.Fatalf("fresh status = %+v, want isInit false with app audiobookshelf 2.37.2", status)
				}
				if status.ConfigPath == nil || *status.ConfigPath != fresh.Config {
					t.Fatalf("fresh status ConfigPath = %v, want %q", status.ConfigPath, fresh.Config)
				}
				if status.MetadataPath == nil || *status.MetadataPath != fresh.Metadata {
					t.Fatalf("fresh status MetadataPath = %v, want %q", status.MetadataPath, fresh.Metadata)
				}
				if fresh.Count(t, "SELECT COUNT(*) FROM users") != 0 {
					t.Fatal("the fresh install created a root user")
				}
			},
		},
	}
}

func (s *suite) pingScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r012", Invariant: "INV-ROUTE-R012-ROUTE", Case: "normal", State: "stateless", Assertion: "ping-value-and-repeat",
			Sources: []string{"server/Server.js:401-404"},
			Claim:   "GET /audiobookshelf/ping answers 200 {success:true} without authentication, every time.",
			Run: func(t *testing.T, e *harness.Env) {
				for range 2 {
					checkPing(t, e.Get(t, "", "/ping"))
				}
			},
		},
		{
			Requirement: "route-r012", Invariant: "INV-ROUTE-R012-ROUTE", Case: "error", State: "stateless", Assertion: "ping-wrong-method-is-404",
			Sources: []string{"server/Server.js:401"},
			Claim:   "POST /ping is not a registered route and answers 404 rather than the success body.",
			Run: func(t *testing.T, e *harness.Env) {
				requireStatus(t, e.Do(t, harness.Request{Method: "POST", Path: "/ping"}), http.StatusNotFound, "POST /ping")
				checkPing(t, e.Get(t, "", "/ping"))
			},
		},
		{
			Requirement: "route-r012", Invariant: "INV-ROUTE-R012-ROUTE", Case: "edge", State: "stateless", Assertion: "ping-basepath-and-legacy",
			Sources: []string{"server/Server.js:323-334", "server/Server.js:401"},
			Claim:   "GET /ping without the router base path is rewritten to the base path and answers like /audiobookshelf/ping.",
			Run: func(t *testing.T, e *harness.Env) {
				checkPing(t, e.Get(t, "", "/ping"))
				checkPing(t, e.Do(t, harness.Request{Path: "/ping", Raw: true}))
			},
		},
	}
}

func (s *suite) healthScenarios() []harness.Scenario {
	return []harness.Scenario{
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
			Requirement: "route-r013", Invariant: "INV-ROUTE-R013-ROUTE", Case: "error", State: "stateless", Assertion: "healthcheck-wrong-method-is-404",
			Sources: []string{"server/Server.js:405"},
			Claim:   "POST /healthcheck is not a registered route and answers 404.",
			Run: func(t *testing.T, e *harness.Env) {
				requireStatus(t, e.Do(t, harness.Request{Method: "POST", Path: "/healthcheck"}), http.StatusNotFound, "POST /healthcheck")
			},
		},
		{
			Requirement: "route-r013", Invariant: "INV-ROUTE-R013-ROUTE", Case: "edge", State: "stateless", Assertion: "healthcheck-head-and-query",
			Sources: []string{"server/Server.js:405"},
			Claim:   "HEAD /healthcheck answers 200 with an empty body and a query string does not change the plain OK answer.",
			Run: func(t *testing.T, e *harness.Env) {
				head := e.Do(t, harness.Request{Method: "HEAD", Path: "/healthcheck"})
				requireStatus(t, head, http.StatusOK, "HEAD /healthcheck")
				if len(head.Body) != 0 {
					t.Fatalf("HEAD /healthcheck body = %q, want empty", head.Body)
				}
				withQuery := e.Do(t, harness.Request{Path: "/healthcheck", Query: map[string][]string{"probe": {"1"}}})
				if withQuery.Status != 200 || string(withQuery.Body) != "OK" {
					t.Fatalf("GET /healthcheck?probe=1 = %d %q, want 200 OK", withQuery.Status, withQuery.Body)
				}
			},
		},
	}
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
