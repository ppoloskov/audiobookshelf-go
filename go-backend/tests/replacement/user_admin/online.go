package user_admin

import (
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/tests/replacement/harness"
)

const onlineSource = "server/controllers/UserController.js:484-493"

// onlineUser is one entry of usersOnline.
type onlineUser struct {
	ID          string          `json:"id"`
	Username    string          `json:"username"`
	Type        string          `json:"type"`
	Session     *map[string]any `json:"session"`
	LastSeen    *int64          `json:"lastSeen"`
	CreatedAt   int64           `json:"createdAt"`
	Connections int             `json:"connections"`
}

// onlineBody is the GET /api/users/online response.
type onlineBody struct {
	UsersOnline  []onlineUser     `json:"usersOnline"`
	OpenSessions []map[string]any `json:"openSessions"`
}

// getOnline reads GET /api/users/online as token.
func getOnline(t *testing.T, e *harness.Env, token string) onlineBody {
	t.Helper()
	resp := e.Get(t, token, "/api/users/online")
	wantStatus(t, resp, http.StatusOK, "GET /api/users/online")
	var body onlineBody
	resp.JSON(t, &body)
	if body.UsersOnline == nil || body.OpenSessions == nil {
		t.Fatalf("usersOnline and openSessions must be arrays: %s", resp.Body)
	}
	return body
}

// onlineEntry returns the entry of a user id, or nil.
func (b onlineBody) onlineEntry(id string) *onlineUser {
	for i := range b.UsersOnline {
		if b.UsersOnline[i].ID == id {
			return &b.UsersOnline[i]
		}
	}
	return nil
}

// waitOnline polls GET /api/users/online until the condition holds.
func waitOnline(t *testing.T, e *harness.Env, token, what string, condition func(onlineBody) bool) onlineBody {
	t.Helper()
	var last onlineBody
	harness.Eventually(t, 10*time.Second, what, func() bool {
		last = getOnline(t, e, token)
		return condition(last)
	})
	return last
}

func onlineScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r073", Invariant: "INV-R073-ONLINE-001", Case: "normal", State: "stateless", Assertion: "online-users-count-connections",
			Sources: []string{"server/routers/ApiRouter.js:135", onlineSource, "server/SocketAuthority.js:27-42", "server/models/User.js:328-339"},
			Claim:   "GET /api/users/online lists each user with an authenticated socket once with the public user fields and the number of connections, and omits users without a socket.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				e.Socket(t, root)
				listener := e.Token(t, "listener")
				e.Socket(t, listener)
				e.Socket(t, listener)
				e.Socket(t, "") // connected but never authenticated: not a user
				started := time.Now().UnixMilli() - 5000
				body := waitOnline(t, e, root, "root once and listener twice online", func(b onlineBody) bool {
					l := b.onlineEntry(e.IDs["listener"])
					return l != nil && l.Connections == 2 && len(b.UsersOnline) == 2
				})
				rootEntry, listenerEntry := body.onlineEntry(rootID), body.onlineEntry(e.IDs["listener"])
				if rootEntry == nil || rootEntry.Connections != 1 || rootEntry.Username != "trial-root" || rootEntry.Type != "root" {
					t.Fatalf("root must be online once: %+v", rootEntry)
				}
				if listenerEntry.Username != "trial-listener" || listenerEntry.Type != "user" || listenerEntry.Session != nil ||
					listenerEntry.LastSeen == nil || *listenerEntry.LastSeen < started || listenerEntry.CreatedAt <= 0 {
					t.Fatalf("listener entry fields wrong: %+v", listenerEntry)
				}
				if body.onlineEntry(finisherID) != nil {
					t.Fatalf("a user without a socket must not be online: %+v", body.UsersOnline)
				}
				if len(body.UsersOnline) != 2 {
					t.Fatalf("only root and the listener are online: %+v", body.UsersOnline)
				}
			},
		},
		{
			Requirement: "route-r073", Invariant: "INV-R073-ONLINE-001", Case: "edge", State: "stateless", Assertion: "online-open-session-and-disconnect",
			Sources: []string{onlineSource, "server/models/User.js:328-339", "server/SocketAuthority.js:149-157"},
			Claim:   "A user with an open playback session shows that session in usersOnline and openSessions, and leaves usersOnline when the last socket disconnects.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				user := createUser(t, e, map[string]any{"username": "online-player", "type": "user"})
				token := loginToken(t, e, user.Username)
				socket := e.Socket(t, token)
				play := e.Post(t, token, "/api/items/"+e.IDs["novel"]+"/play", map[string]any{
					"deviceInfo": map[string]any{"clientName": "admin-packet", "deviceId": "admin-packet-online"}, "mediaPlayer": "admin-packet", "forceDirectPlay": true, "supportedMimeTypes": []string{},
				})
				wantStatus(t, play, http.StatusOK, "start playback")
				var started struct {
					ID string `json:"id"`
				}
				play.JSON(t, &started)
				body := waitOnline(t, e, root, "the player online", func(b onlineBody) bool { return b.onlineEntry(user.ID) != nil })
				entry := body.onlineEntry(user.ID)
				session := ""
				if entry.Session != nil {
					session, _ = (*entry.Session)["id"].(string)
				}
				if entry.Connections != 1 || session != started.ID {
					t.Fatalf("the online entry must carry the open session %s: %+v", started.ID, entry)
				}
				var open map[string]any
				for _, candidate := range body.OpenSessions {
					if candidate["id"] == started.ID {
						open = candidate
					}
				}
				if open == nil || open["userId"] != user.ID {
					t.Fatalf("openSessions must list the playback session: %+v", body.OpenSessions)
				}
				for name, object := range map[string]map[string]any{"openSessions entry": open, "usersOnline session": *entry.Session} {
					device, _ := object["deviceInfo"].(map[string]any)
					if object["id"] != started.ID || object["libraryItemId"] != e.IDs["novel"] || object["displayTitle"] != "Single File Novel" || object["mediaType"] != "book" ||
						object["mediaPlayer"] != "admin-packet" || object["playMethod"] != float64(0) || object["currentTime"] != float64(0) ||
						device["deviceId"] != "admin-packet-online" || device["clientName"] != "admin-packet" {
						t.Fatalf("%s fields wrong: %+v", name, object)
					}
				}
				wantStatus(t, e.Post(t, token, "/api/session/"+started.ID+"/close", map[string]any{"currentTime": 1, "timeListened": 1}), http.StatusOK, "close playback")
				socket.Close()
				waitOnline(t, e, root, "the player offline and the session closed", func(b onlineBody) bool {
					for _, candidate := range b.OpenSessions {
						if candidate["id"] == started.ID {
							return false
						}
					}
					return b.onlineEntry(user.ID) == nil
				})
			},
		},
		{
			Requirement: "route-r073", Invariant: "INV-R073-AUTH-001", Case: "error", State: "stateless", Assertion: "online-users-requires-admin",
			Sources: []string{"server/Server.js:354", onlineSource},
			Claim:   "GET /api/users/online answers 401 without a valid token and 403 to a non-admin user, and 200 to a non-root admin.",
			Run: func(t *testing.T, e *harness.Env) {
				requireUnauthenticated(t, e, http.MethodGet, "/api/users/online")
				wantStatus(t, e.Get(t, e.Token(t, "listener"), "/api/users/online"), http.StatusForbidden, "listener")
				getOnline(t, e, adminToken(t, e))
			},
		},
	}
}
