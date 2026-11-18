package user_admin

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

const unlinkSource = "server/controllers/UserController.js:414-426"

// linkOpenID gives the users an OpenID subject by writing the database while
// the server is stopped: no API sets authOpenIDSub without a live OpenID
// provider (setup that the harness allows for state no route can produce).
func linkOpenID(t *testing.T, e *harness.Env, subject string, ids ...string) {
	t.Helper()
	e.Stop(t)
	for _, id := range ids {
		if e.Exec(t, "UPDATE users SET extraData = json_set(extraData, '$.authOpenIDSub', ?) WHERE id = ?", subject+"-"+id, id) != 1 {
			t.Fatalf("user %s not linked", id)
		}
	}
	e.Restart(t)
}

// requireLinked requires the stored subject and the view flag of a user.
func requireLinked(t *testing.T, e *harness.Env, id string, linked bool) {
	t.Helper()
	extra, _ := jsonColumn(t, userRow(t, e, id), "extraData").(map[string]any)
	subject, present := extra["authOpenIDSub"]
	if linked != (subject != nil && subject != "") {
		t.Fatalf("stored authOpenIDSub of %s: %v (present %v), want linked=%v", id, subject, present, linked)
	}
	if view := getUser(t, e, e.Token(t, "root"), id); view.HasOpenIDLink != linked {
		t.Fatalf("hasOpenIDLink of %s must be %v", id, linked)
	}
}

// unlink sends PATCH /api/users/:id/openid-unlink.
func unlink(t *testing.T, e *harness.Env, token, id string) harness.Response {
	t.Helper()
	return e.Patch(t, token, "/api/users/"+id+"/openid-unlink", nil)
}

func unlinkScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r077", Invariant: "INV-R077-CONTRACT-001", Case: "normal", State: "initial", Assertion: "unlink-clears-subject-and-emits",
			Sources: []string{"server/routers/ApiRouter.js:139", unlinkSource, "server/models/User.js:171-173"},
			Claim:   "PATCH /api/users/:id/openid-unlink on a linked user answers 200, sets extraData.authOpenIDSub to null keeping the other extraData, changes no other column but the update time, and emits user_updated with hasOpenIDLink false to the acting admin only.",
			Run: func(t *testing.T, e *harness.Env) {
				target := createUser(t, e, map[string]any{"username": "unlink-one"})
				linkOpenID(t, e, "subject", target.ID)
				requireLinked(t, e, target.ID, true)
				before := userRow(t, e, target.ID)
				audience := connectAudiences(t, e)
				marks := [3]int{audience.root.Mark(), audience.admin.Mark(), audience.listener.Mark()}
				wantStatus(t, unlink(t, e, e.Token(t, "root"), target.ID), http.StatusOK, "unlink")
				requireLinked(t, e, target.ID, false)
				row := userRow(t, e, target.ID)
				extra, _ := jsonColumn(t, row, "extraData").(map[string]any)
				if v, ok := extra["authOpenIDSub"]; !ok || v != nil || jsonString(extra["seriesHideFromContinueListening"]) != "[]" {
					t.Fatalf("extraData must keep the other keys and null the subject: %v", extra)
				}
				for _, column := range []string{"pash", "token", "username", "type", "permissions", "isActive", "bookmarks", "createdAt"} {
					if row[column] != before[column] {
						t.Fatalf("column %s must not change: %v -> %v", column, before[column], row[column])
					}
				}
				event := audience.root.Wait(t, marks[0], "user_updated", waitEvent, matchUserID(target.ID))
				if eventUser(t, event).HasOpenIDLink {
					t.Fatalf("the event must show the link removed")
				}
				audience.admin.None(t, marks[1], "user_updated", quiet, nil)
				audience.listener.None(t, marks[2], "user_updated", quiet, nil)
			},
		},
		{
			Requirement: "route-r077", Invariant: "INV-R077-CONTRACT-001", Case: "normal", State: "repeat", Assertion: "unlink-twice-and-unlinked-user",
			Sources: []string{unlinkSource},
			Claim:   "Unlinking a user that has no OpenID link, including a second unlink, answers 200 and changes nothing and emits no event; root's own record may be targeted by any admin the same way.",
			Run: func(t *testing.T, e *harness.Env) {
				target := createUser(t, e, map[string]any{"username": "unlink-twice"})
				linkOpenID(t, e, "twice", target.ID)
				root := e.Token(t, "root")
				wantStatus(t, unlink(t, e, root, target.ID), http.StatusOK, "first unlink")
				row := userRow(t, e, target.ID)
				socket := e.Socket(t, root)
				mark := socket.Mark()
				wantStatus(t, unlink(t, e, root, target.ID), http.StatusOK, "second unlink")
				wantStatus(t, unlink(t, e, adminToken(t, e), rootID), http.StatusOK, "unlink without a link")
				wantStatus(t, unlink(t, e, root, e.IDs["listener"]), http.StatusOK, "never linked")
				requireRowUnchanged(t, e, target.ID, row)
				socket.None(t, mark, "user_updated", quiet, nil)
			},
		},
		{
			Requirement: "route-r077", Invariant: "INV-R077-CONTRACT-001", Case: "edge", State: "changed", Assertion: "unlink-by-non-root-admin",
			Sources: []string{middlewareJS, unlinkSource},
			Claim:   "A non-root admin may unlink a user; the event goes to that admin's sockets and the link is gone for later reads.",
			Run: func(t *testing.T, e *harness.Env) {
				target := createUser(t, e, map[string]any{"username": "unlink-admin"})
				linkOpenID(t, e, "admin", target.ID)
				audience := connectAudiences(t, e)
				marks := [2]int{audience.root.Mark(), audience.admin.Mark()}
				wantStatus(t, unlink(t, e, adminToken(t, e), target.ID), http.StatusOK, "unlink")
				audience.admin.Wait(t, marks[1], "user_updated", waitEvent, matchUserID(target.ID))
				audience.root.None(t, marks[0], "user_updated", quiet, nil)
				requireLinked(t, e, target.ID, false)
			},
		},
		{
			Requirement: "route-r077", Invariant: "INV-R077-AUTH-001", Case: "error", State: "failure", Assertion: "unlink-rejections-keep-the-link",
			Sources: []string{"server/Server.js:354", middlewareJS, unlinkSource},
			Claim:   "PATCH openid-unlink answers 401 unauthenticated, 403 to non-admins (also for their own id) and 404 for an unknown id, leaving a linked user linked and emitting no event.",
			Run: func(t *testing.T, e *harness.Env) {
				target := createUser(t, e, map[string]any{"username": "unlink-rejected"})
				linkOpenID(t, e, "rejected", target.ID)
				row := userRow(t, e, target.ID)
				root := e.Token(t, "root")
				socket := e.Socket(t, root)
				mark := socket.Mark()
				for _, token := range []string{"", "not-a-token"} {
					wantStatus(t, unlink(t, e, token, target.ID), http.StatusUnauthorized, "token "+token)
				}
				listener := e.Token(t, "listener")
				for _, id := range []string{target.ID, e.IDs["listener"]} {
					wantStatus(t, unlink(t, e, listener, id), http.StatusForbidden, "listener unlinks "+id)
				}
				wantStatus(t, unlink(t, e, root, unknownID), http.StatusNotFound, "unknown id")
				requireRowUnchanged(t, e, target.ID, row)
				requireLinked(t, e, target.ID, true)
				socket.None(t, mark, "user_updated", quiet, nil)
			},
		},
	}
}
