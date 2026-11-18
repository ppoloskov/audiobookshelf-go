package auth

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// lifecycleScenarios are the end-to-end token flows (flow-client-auth-token-lifecycle).
func lifecycleScenarios() []harness.Scenario {
	const (
		flow   = "INV-FLOW-CLIENT-AUTH-TOKEN-LIFECYCLE-001"
		rotate = "INV-FLOW-CLIENT-AUTH-TOKEN-LIFECYCLE-002"
		revoke = "INV-FLOW-CLIENT-AUTH-TOKEN-LIFECYCLE-003"
		req    = "flow-client-auth-token-lifecycle"
	)
	return []harness.Scenario{
		{
			Requirement: req, Invariant: flow, Case: "normal", State: "initial", Assertion: "native-client-token-flow",
			Sources: []string{"server/Auth.js:322-357", "server/Auth.js:486-523", "server/auth/TokenManager.js:350-425"},
			Claim:   "A native client logs in with X-Return-Tokens, uses the access token, refreshes with x-refresh-token, uses the new access token and logs out, after which the refresh token is dead and the session row is gone.",
			Run: func(t *testing.T, e *harness.Env) {
				acc := newAccount(t, e, nil)
				s := loginNative(t, e, acc)
				requireAuthorizedAs(t, e.Post(t, s.Access, "/api/authorize", nil), acc.ID)
				next := refreshed(t, "refresh", refreshByHeader(t, e, s.Refresh))
				requireAuthorizedAs(t, e.Post(t, next.User.AccessToken, "/api/authorize", nil), acc.ID)
				requireLoggedOut(t, logoutWith(t, e, *next.User.RefreshToken, ""))
				requireDeniedWithError(t, "refresh after logout", refreshByHeader(t, e, *next.User.RefreshToken))
				if sessionCount(t, e, acc.ID) != 0 {
					t.Fatal("logout must delete the session")
				}
			},
		},
		{
			Requirement: req, Invariant: flow, Case: "normal", State: "repeat", Assertion: "browser-client-cookie-flow",
			Sources: []string{"server/Auth.js:322-357", "server/Auth.js:486-523"},
			Claim:   "A browser logs in, refreshes twice with the rotating refresh cookie, and logs out with the latest cookie, after which neither the latest nor the earlier cookie refreshes.",
			Run: func(t *testing.T, e *harness.Env) {
				acc := newAccount(t, e, nil)
				s := loginBrowser(t, e, acc)
				first := refreshByCookie(t, e, s.Refresh)
				refreshed(t, "first refresh", first)
				cookie1 := cookieValue(first, "refresh_token")
				second := refreshByCookie(t, e, cookie1)
				refreshed(t, "second refresh", second)
				cookie2 := cookieValue(second, "refresh_token")
				if cookie1 == "" || cookie2 == "" || cookie1 == cookie2 || cookie1 == s.Refresh {
					t.Fatal("every refresh must rotate the cookie")
				}
				out := e.Do(t, harness.Request{Method: http.MethodPost, Path: "/logout", Header: map[string]string{"Cookie": "refresh_token=" + cookie2}})
				requireLoggedOut(t, out)
				for _, token := range []string{cookie2, cookie1, s.Refresh} {
					requireDeniedWithError(t, "refresh after logout", refreshByCookie(t, e, token))
				}
			},
		},
		{
			Requirement: req, Invariant: rotate, Case: "edge", State: "repeat", Assertion: "concurrent-refresh-converges-on-one-token",
			Sources: []string{"server/auth/TokenManager.js:372-388", "server/auth/TokenManager.js:210-262"},
			Claim:   "Parallel refreshes with one refresh token all succeed and return one and the same rotated refresh token, which is the token stored in the session row.",
			Run:     checkConcurrentRefresh,
		},
		{
			Requirement: req, Invariant: rotate, Case: "error", State: "changed", Assertion: "access-token-expires-and-refresh-recovers",
			Sources: []string{"server/auth/TokenManager.js:15-23", "server/auth/TokenManager.js:312-317"},
			Claim:   "When the access token has expired the API answers 401 and a refresh with the still valid refresh token yields a working access token.",
			Run:     checkAccessTokenExpiry,
		},
		{
			Requirement: req, Invariant: revoke, Case: "normal", State: "changed", Assertion: "password-change-with-refresh-token-keeps-current-session",
			Sources: []string{"server/controllers/MeController.js:438-474", "server/auth/TokenManager.js:467-509"},
			Claim:   "PATCH /api/me/password with the current refresh token answers new tokens, revokes the user's other sessions and the old refresh token, and keeps the current session on the new tokens.",
			Run:     checkPasswordChangeKeepsCurrent,
		},
		{
			Requirement: req, Invariant: revoke, Case: "normal", State: "changed", Assertion: "password-change-without-refresh-token-revokes-all-sessions",
			Sources: []string{"server/controllers/MeController.js:455-473", "server/auth/TokenManager.js:503-508"},
			Claim:   "PATCH /api/me/password without a refresh token answers 200 and deletes every session of the user; the new password logs in and the old one does not.",
			Run: func(t *testing.T, e *harness.Env) {
				acc := newAccount(t, e, nil)
				s1 := loginNative(t, e, acc)
				s2 := loginNative(t, e, acc)
				resp := e.Patch(t, s1.Access, "/api/me/password", map[string]string{"password": acc.Password, "newPassword": "changed-password-1"})
				requireStatus(t, "change password", resp, http.StatusOK)
				if sessionCount(t, e, acc.ID) != 0 {
					t.Fatal("all sessions must be deleted")
				}
				requireDeniedWithError(t, "first session", refreshByHeader(t, e, s1.Refresh))
				requireDeniedWithError(t, "second session", refreshByHeader(t, e, s2.Refresh))
				requireDenied(t, "old password", loginAs(t, e, acc, nil))
				acc.Password = "changed-password-1"
				requireStatus(t, "new password", loginAs(t, e, acc, nil), http.StatusOK)
			},
		},
		{
			Requirement: req, Invariant: revoke, Case: "edge", State: "changed", Assertion: "password-change-with-unknown-refresh-token-revokes-all-sessions",
			Sources: []string{"server/auth/TokenManager.js:467-509"},
			Claim:   "PATCH /api/me/password with an x-refresh-token that matches no session answers 200 and deletes every session of the user.",
			Run: func(t *testing.T, e *harness.Env) {
				acc := newAccount(t, e, nil)
				s := loginNative(t, e, acc)
				loginNative(t, e, acc)
				resp := e.Do(t, harness.Request{Method: http.MethodPatch, Path: "/api/me/password", Token: s.Access,
					Header: map[string]string{"x-refresh-token": "unknown-token"},
					JSON:   map[string]string{"password": acc.Password, "newPassword": "changed-password-4"}})
				requireStatus(t, "change password", resp, http.StatusOK)
				if sessionCount(t, e, acc.ID) != 0 {
					t.Fatal("all sessions must be deleted")
				}
			},
		},
		{
			Requirement: req, Invariant: revoke, Case: "error", State: "failure", Assertion: "wrong-current-password-keeps-sessions",
			Sources: []string{"server/controllers/MeController.js:449-453", "server/auth/LocalAuthStrategy.js:145-160"},
			Claim:   "PATCH /api/me/password with a wrong current password answers 400 and leaves the password and every session unchanged.",
			Run: func(t *testing.T, e *harness.Env) {
				acc := newAccount(t, e, nil)
				s := loginNative(t, e, acc)
				before := sessionRows(t, e, acc.ID)
				resp := e.Patch(t, s.Access, "/api/me/password", map[string]string{"password": "not-my-password", "newPassword": "changed-password-2"})
				requireStatus(t, "change password", resp, http.StatusBadRequest)
				// A non-root user may not set an empty password either.
				empty := e.Patch(t, s.Access, "/api/me/password", map[string]string{"password": acc.Password, "newPassword": ""})
				requireStatus(t, "empty new password", empty, http.StatusBadRequest)
				after := sessionRows(t, e, acc.ID)
				if len(after) != len(before) || after[0]["refreshToken"] != before[0]["refreshToken"] {
					t.Fatal("a rejected password change must keep the sessions")
				}
				refreshed(t, "session after rejected change", refreshByHeader(t, e, s.Refresh))
				requireStatus(t, "old password", loginAs(t, e, acc, nil), http.StatusOK)
			},
		},
		{
			Requirement: req, Invariant: revoke, Case: "normal", State: "changed", Assertion: "admin-password-reset-revokes-user-sessions",
			Sources: []string{"server/controllers/UserController.js:244-250", "server/controllers/UserController.js:335-348", "server/auth/TokenManager.js:503-508"},
			Claim:   "When an admin sets another user's password through PATCH /api/users/:id all that user's sessions are deleted while the admin's own sessions stay.",
			Run: func(t *testing.T, e *harness.Env) {
				acc := newAccount(t, e, nil)
				s := loginNative(t, e, acc)
				root := rootID(t, e)
				rootBefore := sessionCount(t, e, root)
				patchUser(t, e, acc.ID, map[string]any{"password": "reset-by-admin"})
				if sessionCount(t, e, acc.ID) != 0 {
					t.Fatal("the user's sessions must be deleted")
				}
				requireDeniedWithError(t, "refresh after reset", refreshByHeader(t, e, s.Refresh))
				if sessionCount(t, e, root) != rootBefore {
					t.Fatal("the admin's own sessions must stay")
				}
			},
		},
	}
}

func checkConcurrentRefresh(t *testing.T, e *harness.Env) {
	acc := newAccount(t, e, nil)
	old := loginNative(t, e, acc).Refresh
	const workers = 5
	type result struct {
		status int
		token  string
		err    error
	}
	results := make([]result, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodPost, e.Base+"/auth/refresh", nil)
			if err != nil {
				results[i].err = err
				return
			}
			req.Header.Set("x-refresh-token", old)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				results[i].err = err
				return
			}
			defer resp.Body.Close() // fully decoded below
			var body loginPayload
			results[i].err = json.NewDecoder(resp.Body).Decode(&body)
			results[i].status = resp.StatusCode
			if body.User.RefreshToken != nil {
				results[i].token = *body.User.RefreshToken
			}
		}()
	}
	wg.Wait()
	rows := sessionRows(t, e, acc.ID)
	if len(rows) != 1 {
		t.Fatalf("want one session row, got %d", len(rows))
	}
	current, _ := rows[0]["refreshToken"].(string)
	for i, r := range results {
		if r.err != nil || r.status != http.StatusOK || r.token != current || current == old {
			t.Fatalf("worker %d: status %d token %q err %v; stored %q", i, r.status, r.token, r.err, current)
		}
	}
}

func checkPasswordChangeKeepsCurrent(t *testing.T, e *harness.Env) {
	acc := newAccount(t, e, nil)
	current := loginNative(t, e, acc)
	other := loginNative(t, e, acc)
	resp := e.Do(t, harness.Request{Method: http.MethodPatch, Path: "/api/me/password", Token: current.Access,
		Header: map[string]string{"x-refresh-token": current.Refresh},
		JSON:   map[string]string{"password": acc.Password, "newPassword": "changed-password-3"}})
	requireStatus(t, "change password", resp, http.StatusOK)
	var answer struct {
		Success bool `json:"success"`
		User    struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
		} `json:"user"`
	}
	resp.JSON(t, &answer)
	if !answer.Success || answer.User.AccessToken == "" || answer.User.RefreshToken == "" || answer.User.RefreshToken == current.Refresh {
		t.Fatalf("answer = %s", resp.Body)
	}
	if sessionCount(t, e, acc.ID) != 1 {
		t.Fatal("only the current session may remain")
	}
	requireDeniedWithError(t, "other session", refreshByHeader(t, e, other.Refresh))
	requireDeniedWithError(t, "old token of the current session", refreshByHeader(t, e, current.Refresh))
	refreshed(t, "new token of the current session", refreshByHeader(t, e, answer.User.RefreshToken))
	requireAuthorizedAs(t, e.Post(t, answer.User.AccessToken, "/api/authorize", nil), acc.ID)
}
