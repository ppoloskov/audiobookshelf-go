package user_misc

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	passwordSource  = "server/controllers/MeController.js:426-474"
	localAuthSource = "server/auth/LocalAuthStrategy.js:145-183"
	tokenSource     = "server/auth/TokenManager.js:467-504"
	changeClient    = "client/pages/account.vue:234-252"
)

// The password sequence of the shared packet user: the initial password is set
// at creation, then each scenario moves it forward deterministically.
const (
	passwordInitial = "misc-password-0"
	passwordAfter1  = "misc-password-1"
	passwordAfter2  = "misc-password-2"
)

// passwordResponses decodes the token payload of a password change.
type passwordTokens struct {
	Success bool `json:"success"`
	User    struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
	} `json:"user"`
}

func passwordScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r116", Invariant: "INV-R116-TOKENS-001", Case: "normal", State: "initial", Assertion: "password-change-rotates-tokens",
			Sources: []string{"server/routers/ApiRouter.js:190", passwordSource, tokenSource, changeClient},
			Claim:   "PATCH /api/me/password with the current password and the x-refresh-token of one of two mobile sessions changes the stored hash, returns new access and refresh tokens for that session, keeps only that session and never returns the hash.",
			Run: func(t *testing.T, e *harness.Env) {
				userID, _ := newUser(t, e, map[string]any{"username": "misc-pw-rotate", "type": "user"}, passwordInitial)
				access, refresh, sessionID := mobileLogin(t, e, "misc-pw-rotate", passwordInitial)
				mobileLogin(t, e, "misc-pw-rotate", passwordInitial)
				if got := sessionIDs(t, e, userID); len(got) != 3 {
					t.Fatalf("the create login and two mobile logins must store three sessions: %v", got)
				}
				oldHash := pashHash(t, e, userID)

				resp := e.Do(t, harness.Request{
					Method: http.MethodPatch, Path: "/api/me/password", Token: access,
					JSON:   map[string]any{"password": passwordInitial, "newPassword": passwordAfter1},
					Header: map[string]string{"X-Refresh-Token": refresh},
				})
				wantStatus(t, resp, http.StatusOK, "change password")
				var tokens passwordTokens
				resp.JSON(t, &tokens)
				if !tokens.Success || tokens.User.AccessToken == "" || tokens.User.RefreshToken == "" {
					t.Fatalf("password change must return new tokens: %s", resp.Body)
				}
				if strings.Contains(string(resp.Body), "$2a$") || strings.Contains(string(resp.Body), oldHash) {
					t.Fatalf("password change response leaks the stored hash: %s", resp.Body)
				}

				if got := sessionIDs(t, e, userID); len(got) != 1 || got[0] != sessionID {
					t.Fatalf("only the rotated session must remain: got %v, want [%s]", got, sessionID)
				}
				newHash := pashHash(t, e, userID)
				if newHash == "" || newHash == oldHash {
					t.Fatalf("stored hash must change: old %q new %q", oldHash, newHash)
				}

				me := e.Get(t, tokens.User.AccessToken, "/api/me")
				wantStatus(t, me, http.StatusOK, "new access token")
				var view struct {
					ID string `json:"id"`
				}
				me.JSON(t, &view)
				if view.ID != userID {
					t.Fatalf("new access token belongs to %q, want %q", view.ID, userID)
				}
				refreshed := e.Do(t, harness.Request{Method: http.MethodPost, Path: "/auth/refresh", Header: map[string]string{"X-Refresh-Token": tokens.User.RefreshToken}})
				wantStatus(t, refreshed, http.StatusOK, "refresh with the new token")
				var refreshedBody struct {
					User struct {
						AccessToken string `json:"accessToken"`
					} `json:"user"`
				}
				refreshed.JSON(t, &refreshedBody)
				if refreshedBody.User.AccessToken == "" {
					t.Fatalf("refresh must return an access token: %s", refreshed.Body)
				}
			},
		},
		{
			Requirement: "route-r116", Invariant: "INV-R116-HASH-001", Case: "normal", State: "repeat", Assertion: "password-change-repeat-updates-hash",
			Sources: []string{passwordSource, localAuthSource},
			Claim:   "PATCH /api/me/password can change the password again with the previous new password: it rotates the session, stores a different non-empty hash and answers 200 without a hash in the body.",
			Run: func(t *testing.T, e *harness.Env) {
				access, refresh, _ := mobileLogin(t, e, "misc-pw-rotate", passwordAfter1)
				before := pashHash(t, e, userIDOf(t, e, "misc-pw-rotate"))
				resp := e.Do(t, harness.Request{
					Method: http.MethodPatch, Path: "/api/me/password", Token: access,
					JSON:   map[string]any{"password": passwordAfter1, "newPassword": passwordAfter2},
					Header: map[string]string{"X-Refresh-Token": refresh},
				})
				wantStatus(t, resp, http.StatusOK, "second password change")
				if bytes.Contains(resp.Body, []byte("$2a$")) {
					t.Fatalf("second change leaks a hash: %s", resp.Body)
				}
				after := pashHash(t, e, userIDOf(t, e, "misc-pw-rotate"))
				if after == "" || after == before {
					t.Fatalf("hash must change on the repeat change: before %q after %q", before, after)
				}
			},
		},
		{
			Requirement: "route-r116", Invariant: "INV-R116-CONTRACT-001", Case: "edge", State: "changed", Assertion: "password-change-old-login-rejected",
			Sources: []string{passwordSource, localAuthSource},
			Claim:   "After two password changes the previous password no longer logs in (401) and the current one logs in.",
			Run: func(t *testing.T, e *harness.Env) {
				for _, password := range []string{passwordInitial, passwordAfter1} {
					resp := e.Do(t, harness.Request{Method: http.MethodPost, Path: "/login", JSON: map[string]string{"username": "misc-pw-rotate", "password": password}})
					wantStatus(t, resp, http.StatusUnauthorized, "login with the superseded password")
				}
				access, _, _ := mobileLogin(t, e, "misc-pw-rotate", passwordAfter2)
				getJSON(t, e, access, "/api/me", nil)
			},
		},
		{
			Requirement: "route-r116", Invariant: "INV-R116-TOKENS-001", Case: "edge", State: "changed", Assertion: "password-change-without-refresh-destroys-sessions",
			Sources: []string{passwordSource, tokenSource},
			Claim:   "PATCH /api/me/password without an x-refresh-token answers 200 and destroys every session of the user; the new password logs in.",
			Run: func(t *testing.T, e *harness.Env) {
				userID, _ := newUser(t, e, map[string]any{"username": "misc-pw-norefresh", "type": "user"}, passwordInitial)
				access, _, _ := mobileLogin(t, e, "misc-pw-norefresh", passwordInitial)
				mobileLogin(t, e, "misc-pw-norefresh", passwordInitial)
				resp := e.Patch(t, access, "/api/me/password", map[string]any{"password": passwordInitial, "newPassword": passwordAfter1})
				wantStatus(t, resp, http.StatusOK, "change password without refresh token")
				if got := sessionIDs(t, e, userID); len(got) != 0 {
					t.Fatalf("all sessions must be destroyed: %v", got)
				}
				mobileLogin(t, e, "misc-pw-norefresh", passwordAfter1)
			},
		},
		{
			Requirement: "route-r116", Invariant: "INV-R116-TOKENS-001", Case: "edge", State: "changed", Assertion: "password-change-unknown-refresh-destroys-sessions",
			Sources: []string{passwordSource, tokenSource},
			Claim:   "PATCH /api/me/password with an x-refresh-token that matches no session answers 200 without tokens and destroys all sessions of the user.",
			Run: func(t *testing.T, e *harness.Env) {
				userID, _ := newUser(t, e, map[string]any{"username": "misc-pw-bogus", "type": "user"}, passwordInitial)
				access, _, _ := mobileLogin(t, e, "misc-pw-bogus", passwordInitial)
				resp := e.Do(t, harness.Request{
					Method: http.MethodPatch, Path: "/api/me/password", Token: access,
					JSON:   map[string]any{"password": passwordInitial, "newPassword": passwordAfter1},
					Header: map[string]string{"X-Refresh-Token": "not-a-session-token"},
				})
				wantStatus(t, resp, http.StatusOK, "change password with an unknown refresh token")
				if bytes.Contains(resp.Body, []byte("accessToken")) {
					t.Fatalf("no tokens may be returned without a matching session: %s", resp.Body)
				}
				if got := sessionIDs(t, e, userID); len(got) != 0 {
					t.Fatalf("all sessions must be destroyed: %v", got)
				}
			},
		},
		{
			Requirement: "route-r116", Invariant: "INV-R116-CONTRACT-001", Case: "error", State: "failure", Assertion: "password-change-rejects-without-side-effects",
			Sources: []string{passwordSource, localAuthSource, "server/routers/ApiRouter.js:190"},
			Claim:   "PATCH /api/me/password answers 400 for a wrong current password and for missing or non-string passwords, 403 for a guest and 401 without a token, and none of these changes the stored hash or the sessions.",
			Run: func(t *testing.T, e *harness.Env) {
				userID, _ := newUser(t, e, map[string]any{"username": "misc-pw-reject", "type": "user"}, passwordInitial)
				access, _, _ := mobileLogin(t, e, "misc-pw-reject", passwordInitial)
				hash := pashHash(t, e, userID)
				sessions := sessionIDs(t, e, userID)

				for _, body := range []any{
					map[string]any{"password": "wrong", "newPassword": passwordAfter1},
					map[string]any{"password": passwordInitial},
					map[string]any{"password": passwordInitial, "newPassword": nil},
					map[string]any{"password": nil, "newPassword": passwordAfter1},
					map[string]any{"password": passwordInitial, "newPassword": 42},
				} {
					resp := e.Patch(t, access, "/api/me/password", body)
					wantStatus(t, resp, http.StatusBadRequest, "rejected password change")
					if got := pashHash(t, e, userID); got != hash {
						t.Fatalf("rejected change altered the hash: %q -> %q", hash, got)
					}
					equalJSON(t, "sessions after a rejected change", sessionIDs(t, e, userID), sessions)
				}

				guestID, guestToken := newUser(t, e, map[string]any{"username": "misc-pw-guest", "type": "guest"}, "misc-guest-password")
				guestHash := pashHash(t, e, guestID)
				wantStatus(t, e.Patch(t, guestToken, "/api/me/password", map[string]any{"password": "misc-guest-password", "newPassword": "other"}), http.StatusForbidden, "guest password change")
				wantStatus(t, e.Patch(t, guestToken, "/api/me/password", map[string]any{}), http.StatusForbidden, "the guest check runs before payload validation")
				if got := pashHash(t, e, guestID); got != guestHash {
					t.Fatal("guest password change altered the stored hash")
				}

				requireUnauthenticated(t, e, http.MethodPatch, "/api/me/password")
				if got := pashHash(t, e, userID); got != hash {
					t.Fatalf("rejected requests altered the hash: %q -> %q", hash, got)
				}
				equalJSON(t, "sessions after rejected requests", sessionIDs(t, e, userID), sessions)
			},
		},
		{
			Requirement: "route-r116", Invariant: "INV-R116-ROOT-001", Case: "edge", State: "initial", Assertion: "password-change-root-empty-password",
			Sources: []string{localAuthSource, passwordSource},
			Claim:   "On its own server PATCH /api/me/password lets root set an empty new password (stored hash empty, 200) and rejects an empty new password for a non-root user with 400.",
			Run: func(t *testing.T, _ *harness.Env) {
				e := harness.Start(t, harness.Options{Env: map[string]string{"RATE_LIMIT_AUTH_MAX": "0"}})
				rootToken := e.Token(t, "root")
				rootIDLocal := userIDOf(t, e, "trial-root")
				nonRootID, nonRootToken := newUser(t, e, map[string]any{"username": "misc-pw-empty", "type": "user"}, passwordInitial)

				wantStatus(t, e.Patch(t, nonRootToken, "/api/me/password", map[string]any{"password": passwordInitial, "newPassword": ""}), http.StatusBadRequest, "empty new password for a non-root user")
				if got := pashHash(t, e, nonRootID); got == "" {
					t.Fatal("rejected empty password must keep the stored hash")
				}

				resp := e.Patch(t, rootToken, "/api/me/password", map[string]any{"password": "trial-root-password", "newPassword": ""})
				wantStatus(t, resp, http.StatusOK, "root empty new password")
				if got := pashHash(t, e, rootIDLocal); got != "" {
					t.Fatalf("root empty password must store an empty hash, got %q", got)
				}
				for _, password := range []string{"", "trial-root-password"} {
					login := e.Do(t, harness.Request{Method: http.MethodPost, Path: "/login", JSON: map[string]string{"username": "trial-root", "password": password}})
					if password == "" {
						wantStatus(t, login, http.StatusOK, "login with the empty root password")
					} else {
						wantStatus(t, login, http.StatusUnauthorized, "login with the superseded root password")
					}
				}
			},
		},
	}
}
