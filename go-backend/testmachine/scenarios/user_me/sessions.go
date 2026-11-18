package user_me

import (
	"net/http"
	"sort"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	listPassword = "trial-listener-password"
	listName     = "trial-listener"
)

// listSessions reads GET /api/me/sessions as token, optionally presenting a refresh token.
func listSessions(t *testing.T, e *harness.Env, token, refreshToken string, query ...string) sessionsPage {
	t.Helper()
	header := map[string]string{}
	if refreshToken != "" {
		header["X-Refresh-Token"] = refreshToken
	}
	return listSessionsWith(t, e, token, header, query...)
}

// listSessionsWith reads GET /api/me/sessions with extra request headers.
func listSessionsWith(t *testing.T, e *harness.Env, token string, header map[string]string, query ...string) sessionsPage {
	t.Helper()
	resp := e.Do(t, harness.Request{Path: "/api/me/sessions", Token: token, Header: header, Query: pageQuery(query...)})
	wantStatus(t, resp, http.StatusOK, "GET /api/me/sessions")
	var page sessionsPage
	resp.JSON(t, &page)
	return page
}

// findSession returns the session with the id, or nil.
func findSession(page sessionsPage, id string) *sessionJSON {
	for i := range page.Sessions {
		if page.Sessions[i].ID == id {
			return &page.Sessions[i]
		}
	}
	return nil
}

func sessionScenarios() []harness.Scenario {
	const (
		listSource = "server/controllers/MeController.js:37-77"
		delSource  = "server/controllers/MeController.js:80-105"
	)
	return []harness.Scenario{
		{
			Requirement: "route-r100", Invariant: "INV-R100-SAMPLED", Case: "normal", State: "stateless", Assertion: "sessions-list-fields-and-current",
			Sources: []string{"server/routers/ApiRouter.js:174", listSource, "server/utils/parsers/parseUserAgent.js:9"},
			Claim:   "GET /api/me/sessions lists the caller's unexpired auth sessions newest update first with id, ip, user agent, parsed deviceInfo, ms timestamps and current true only for the session whose refresh token is presented.",
			Run: func(t *testing.T, e *harness.Env) {
				keepSeededSessionsValid(t, e)
				before := time.Now().UnixMilli() - 5000
				token, refresh, id := loginSession(t, e, listName, listPassword)
				page := listSessions(t, e, token, refresh)
				if page.Total != sessionCount(t, e, e.IDs["listener"]) || page.Total < 2 || page.Page != 0 || page.ItemsPerPage != 10 || page.NumPages != 1 || len(page.Sessions) != page.Total {
					t.Fatalf("paging fields wrong: %+v", page)
				}
				fresh := findSession(page, id)
				if fresh == nil || page.Sessions[0].ID != id {
					t.Fatalf("the new session must be listed first: %+v", page.Sessions)
				}
				if fresh.IPAddress == "" || fresh.UserAgent != chromeAgent || !fresh.Current || fresh.CreatedAt < before || fresh.UpdatedAt < fresh.CreatedAt {
					t.Fatalf("new session fields wrong: %+v", *fresh)
				}
				for key, want := range map[string]string{"browserName": "Chrome", "browserVersion": "120.0.0.0", "osName": "Linux"} {
					if fresh.DeviceInfo[key] != want {
						t.Fatalf("deviceInfo.%s must be %q: %v", key, want, fresh.DeviceInfo)
					}
				}
				seeded := findSession(page, listenerSessionID)
				if seeded == nil || seeded.IPAddress != "127.0.0.1" || seeded.UserAgent != "node" || seeded.DeviceInfo != nil || seeded.CreatedAt != 1793709986851 || seeded.UpdatedAt != 1793709986851 || seeded.Current {
					t.Fatalf("seeded session wrong: %+v", seeded)
				}
				for _, session := range page.Sessions {
					if session.Current && session.ID != id {
						t.Fatalf("only the presented refresh token marks a session current: %+v", session)
					}
				}
				if !sort.SliceIsSorted(page.Sessions, func(i, j int) bool { return page.Sessions[i].UpdatedAt > page.Sessions[j].UpdatedAt }) {
					t.Fatalf("sessions must be ordered by updatedAt descending: %+v", page.Sessions)
				}
				for _, session := range listSessions(t, e, token, "").Sessions {
					if session.Current {
						t.Fatalf("no refresh token presented: nothing is current: %+v", session)
					}
				}
			},
		},
		{
			Requirement: "route-r100", Invariant: "INV-R100-SAMPLED", Case: "edge", State: "stateless", Assertion: "sessions-list-excludes-expired",
			Sources: []string{"server/controllers/MeController.js:47-57"},
			Claim:   "GET /api/me/sessions lists only sessions whose expiresAt lies in the future: a session expired in the database is absent from the list and from total while its row still exists, and a session set to expire later is listed.",
			Run: func(t *testing.T, e *harness.Env) {
				// expire one session a few seconds ahead: the server purges rows that are already
				// expired when it starts, so it must pass while the server runs
				_, _, expiring := loginSession(t, e, listName, listPassword)
				soon := time.Now().UTC().Add(12 * time.Second).Format("2006-01-02 15:04:05.000 +00:00")
				editDatabase(t, e,
					[]any{"UPDATE sessions SET expiresAt = ? WHERE id = ?", soon, expiring},
					[]any{"UPDATE sessions SET expiresAt = ? WHERE id = ?", farFuture, listenerSessionID})
				token := e.Token(t, "listener")
				if findSession(listSessions(t, e, token, ""), expiring) == nil {
					t.Fatal("a session that has not expired yet must be listed")
				}
				harness.Eventually(t, 30*time.Second, "the expired session to leave the list", func() bool {
					return findSession(listSessions(t, e, token, ""), expiring) == nil
				})
				page := listSessions(t, e, token, "")
				unexpired := e.Count(t, "SELECT COUNT(*) FROM sessions WHERE userId = ? AND expiresAt > ?", e.IDs["listener"], time.Now().UTC().Format("2006-01-02 15:04:05.000 +00:00"))
				if page.Total != unexpired || findSession(page, listenerSessionID) == nil || e.Count(t, "SELECT COUNT(*) FROM sessions WHERE id = ?", expiring) != 1 {
					t.Fatalf("total %d must count unexpired rows only (%d), keep the others listed, and the expired row stays", page.Total, unexpired)
				}
			},
		},
		{
			Requirement: "route-r100", Invariant: "INV-R100-SAMPLED", Case: "normal", State: "stateless", Assertion: "sessions-current-by-cookie-and-last-refresh-token",
			Sources: []string{"server/controllers/MeController.js:45", "server/controllers/MeController.js:69", "server/Auth.js:331-358"},
			Claim:   "GET /api/me/sessions marks a session current when the refresh_token cookie (the web client) matches its refreshToken or, after a refresh rotated it, its lastRefreshToken; an unrelated cookie marks none.",
			Run: func(t *testing.T, e *harness.Env) {
				token, first, id := loginSession(t, e, listName, listPassword)
				resp := e.Do(t, harness.Request{Method: http.MethodPost, Path: "/auth/refresh", Header: map[string]string{"X-Refresh-Token": first}})
				wantStatus(t, resp, http.StatusOK, "refresh")
				var refreshed struct {
					User struct {
						RefreshToken string `json:"refreshToken"`
					} `json:"user"`
				}
				resp.JSON(t, &refreshed)
				second := refreshed.User.RefreshToken
				if second == "" || second == first {
					t.Fatalf("refresh must rotate the refresh token: %s", resp.Body)
				}
				rows := e.Rows(t, "SELECT refreshToken, lastRefreshToken FROM sessions WHERE id = ?", id)
				if len(rows) != 1 || rows[0]["refreshToken"] != second || rows[0]["lastRefreshToken"] != first {
					t.Fatalf("session must hold the new token and remember the previous one: %v", rows)
				}
				for cookie, want := range map[string]bool{first: true, second: true, "unrelated": false} {
					page := listSessionsWith(t, e, token, map[string]string{"Cookie": "refresh_token=" + cookie})
					for _, session := range page.Sessions {
						if session.Current != (want && session.ID == id) {
							t.Fatalf("cookie %.12s...: session %s current=%v, want %v", cookie, session.ID, session.Current, want && session.ID == id)
						}
					}
				}
			},
		},
		{
			Requirement: "route-r100", Invariant: "INV-R100-SAMPLED", Case: "edge", State: "stateless", Assertion: "sessions-list-paging-parameters",
			Sources: []string{"server/controllers/MeController.js:38-39", "server/controllers/MeController.js:48-51"},
			Claim:   "GET /api/me/sessions pages by itemsPerPage and page (default 10 and 0), clamps a negative page to 0 and itemsPerPage 0 to 1, and returns an empty page past the end with the unchanged total.",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "listener")
				loginSession(t, e, listName, listPassword) // guarantees at least two sessions
				total := listSessions(t, e, token, "").Total
				first := listSessions(t, e, token, "", "itemsPerPage", "1", "page", "0")
				second := listSessions(t, e, token, "", "itemsPerPage", "1", "page", "1")
				if first.Total != total || first.NumPages != total || first.ItemsPerPage != 1 || len(first.Sessions) != 1 ||
					second.Page != 1 || len(second.Sessions) != 1 || first.Sessions[0].ID == second.Sessions[0].ID {
					t.Fatalf("paging by one wrong: %+v %+v", first, second)
				}
				clamped := listSessions(t, e, token, "", "itemsPerPage", "0", "page", "-3")
				if clamped.Page != 0 || clamped.ItemsPerPage != 1 || len(clamped.Sessions) != 1 || clamped.Sessions[0].ID != first.Sessions[0].ID {
					t.Fatalf("clamping wrong: %+v", clamped)
				}
				past := listSessions(t, e, token, "", "page", "50")
				if past.Page != 50 || past.Total != total || len(past.Sessions) != 0 {
					t.Fatalf("page past the end wrong: %+v", past)
				}
			},
		},
		{
			Requirement: "route-r100", Invariant: "INV-R100-GUEST-001", Case: "edge", State: "stateless", Assertion: "sessions-list-guest-is-empty",
			Sources: []string{"server/controllers/MeController.js:41-43"},
			Claim:   "GET /api/me/sessions for a guest answers 200 with no sessions, total 0, numPages 0 and the requested paging values.",
			Run: func(t *testing.T, e *harness.Env) {
				guest := guestUser(t, e, "me-guest-sessions")
				page := listSessions(t, e, guest, "", "page", "2", "itemsPerPage", "5")
				equalJSON(t, "guest page", page, sessionsPage{Total: 0, NumPages: 0, Page: 2, ItemsPerPage: 5, Sessions: []sessionJSON{}})
			},
		},
		{
			Requirement: "route-r100", Invariant: "INV-R100-SAMPLED", Case: "error", State: "stateless", Assertion: "sessions-list-requires-authentication",
			Sources: []string{"server/Server.js:354", "server/routers/ApiRouter.js:174"},
			Claim:   "GET /api/me/sessions answers 401 without a valid token.",
			Run: func(t *testing.T, e *harness.Env) {
				requireUnauthenticated(t, e, http.MethodGet, "/api/me/sessions")
			},
		},
		{
			Requirement: "route-r101", Invariant: "INV-R101-SAMPLED", Case: "normal", State: "initial", Assertion: "session-delete-own",
			Sources: []string{"server/routers/ApiRouter.js:175", delSource},
			Claim:   "DELETE /api/me/sessions/:id removes the caller's session row and answers 200, leaving the caller's other sessions in place.",
			Run: func(t *testing.T, e *harness.Env) {
				token, _, id := loginSession(t, e, listName, listPassword)
				before := sessionCount(t, e, e.IDs["listener"])
				wantStatus(t, e.Delete(t, token, "/api/me/sessions/"+id), http.StatusOK, "delete own session")
				if got := e.Count(t, "SELECT COUNT(*) FROM sessions WHERE id = ?", id); got != 0 {
					t.Fatalf("session row must be gone, %d left", got)
				}
				if after := sessionCount(t, e, e.IDs["listener"]); after != before-1 {
					t.Fatalf("exactly one row must be removed: %d -> %d", before, after)
				}
				if e.Count(t, "SELECT COUNT(*) FROM sessions WHERE id = ?", listenerSessionID) != 1 {
					t.Fatal("the other seeded session must stay")
				}
			},
		},
		{
			Requirement: "route-r101", Invariant: "INV-R101-SAMPLED", Case: "normal", State: "changed", Assertion: "session-delete-invalidates-refresh-and-list",
			Sources: []string{delSource, "server/Auth.js:331-352"},
			Claim:   "After DELETE /api/me/sessions/:id the session no longer appears in GET /api/me/sessions and its refresh token no longer refreshes (401).",
			Run: func(t *testing.T, e *harness.Env) {
				token, refresh, id := loginSession(t, e, listName, listPassword)
				other, _, _ := loginSession(t, e, listName, listPassword)
				if findSession(listSessions(t, e, other, ""), id) == nil {
					t.Fatal("the session must be listed before deletion")
				}
				wantStatus(t, e.Delete(t, other, "/api/me/sessions/"+id), http.StatusOK, "delete session of another device")
				if findSession(listSessions(t, e, other, ""), id) != nil {
					t.Fatal("the deleted session must not be listed")
				}
				resp := e.Do(t, harness.Request{Method: http.MethodPost, Path: "/auth/refresh", Header: map[string]string{"X-Refresh-Token": refresh}})
				wantStatus(t, resp, http.StatusUnauthorized, "refresh with the deleted session's token")
				_ = token
			},
		},
		{
			Requirement: "route-r101", Invariant: "INV-R101-SAMPLED", Case: "error", State: "repeat", Assertion: "session-delete-repeat-not-found",
			Sources: []string{delSource},
			Claim:   "Deleting the same session id again answers 404 and changes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				token, _, id := loginSession(t, e, listName, listPassword)
				other, _, _ := loginSession(t, e, listName, listPassword)
				wantStatus(t, e.Delete(t, other, "/api/me/sessions/"+id), http.StatusOK, "first delete")
				before := e.Count(t, "SELECT COUNT(*) FROM sessions")
				wantStatus(t, e.Delete(t, other, "/api/me/sessions/"+id), http.StatusNotFound, "second delete")
				if after := e.Count(t, "SELECT COUNT(*) FROM sessions"); after != before {
					t.Fatalf("a repeated delete must change nothing: %d -> %d", before, after)
				}
				_ = token
			},
		},
		{
			Requirement: "route-r101", Invariant: "INV-R101-SAMPLED", Case: "edge", State: "initial", Assertion: "session-delete-current-session",
			Sources: []string{delSource},
			Claim:   "A caller may delete the session it is currently using: 200 and the row is gone.",
			Run: func(t *testing.T, e *harness.Env) {
				token, _, id := loginSession(t, e, listName, listPassword)
				wantStatus(t, e.Delete(t, token, "/api/me/sessions/"+id), http.StatusOK, "delete current session")
				if e.Count(t, "SELECT COUNT(*) FROM sessions WHERE id = ?", id) != 0 {
					t.Fatal("session row must be gone")
				}
			},
		},
		{
			Requirement: "route-r101", Invariant: "INV-R101-SAMPLED", Case: "error", State: "initial", Assertion: "session-delete-rejections",
			Sources: []string{delSource, "server/utils/index.js:249"},
			Claim:   "DELETE /api/me/sessions/:id answers 401 without a token, 400 for an id that is not a UUID, 404 for an unknown UUID and 404 for another user's session, and 403 for a guest even before the id is checked.",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "listener")
				requireUnauthenticated(t, e, http.MethodDelete, "/api/me/sessions/"+listenerSessionID)
				wantStatus(t, e.Delete(t, token, "/api/me/sessions/not-a-uuid"), http.StatusBadRequest, "not a uuid")
				wantStatus(t, e.Delete(t, token, "/api/me/sessions/00000000-0000-4000-8000-000000000000"), http.StatusNotFound, "unknown uuid")
				wantStatus(t, e.Delete(t, token, "/api/me/sessions/"+rootSessionID), http.StatusNotFound, "another user's session")
				guest := guestUser(t, e, "me-guest-delete")
				wantStatus(t, e.Delete(t, guest, "/api/me/sessions/not-a-uuid"), http.StatusForbidden, "guest")
				wantStatus(t, e.Delete(t, guest, "/api/me/sessions/"+listenerSessionID), http.StatusForbidden, "guest with a real session id")
			},
		},
		{
			Requirement: "route-r101", Invariant: "INV-R101-SAMPLED", Case: "error", State: "failure", Assertion: "session-delete-rejections-change-nothing",
			Sources: []string{delSource},
			Claim:   "Rejected session deletes (another user's session, unknown, malformed) leave every session row, including the other user's, untouched.",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "listener")
				snapshot := func() string {
					rows := e.Rows(t, "SELECT id, userId, refreshToken, updatedAt FROM sessions ORDER BY id")
					encoded, err := jsonOf(rows)
					if err != nil {
						t.Fatal(err)
					}
					return encoded
				}
				before := snapshot()
				e.Delete(t, token, "/api/me/sessions/"+rootSessionID)
				e.Delete(t, token, "/api/me/sessions/00000000-0000-4000-8000-000000000000")
				e.Delete(t, token, "/api/me/sessions/zzz")
				if snapshot() != before {
					t.Fatal("rejected deletes must not change the sessions table")
				}
				if e.Count(t, "SELECT COUNT(*) FROM sessions WHERE id = ?", rootSessionID) != 1 {
					t.Fatal("root's session must still exist")
				}
			},
		},
	}
}
