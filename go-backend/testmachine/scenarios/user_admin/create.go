package user_admin

import (
	"encoding/json"
	"net/http"
	"reflect"
	"regexp"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	createSource = "server/controllers/UserController.js:116-194"
	addedSource  = "server/controllers/UserController.js:187"
	quiet        = 500 * time.Millisecond
	waitEvent    = 5 * time.Second
)

var bcryptHash = regexp.MustCompile(`^\$2[aby]\$\d\d\$.{53}$`)

// sockets are the authenticated sockets of the three audiences of the user_*
// events: root and a non-root admin (admin clients) and an ordinary user.
type sockets struct{ root, admin, listener *harness.Socket }

// connectAudiences connects one socket for root, the helper admin and the listener.
func connectAudiences(t *testing.T, e *harness.Env) sockets {
	t.Helper()
	return sockets{
		root:     e.Socket(t, e.Token(t, "root")),
		admin:    e.Socket(t, adminToken(t, e)),
		listener: e.Socket(t, e.Token(t, "listener")),
	}
}

// createResult is the POST /api/users response.
type createResult struct {
	User userView `json:"user"`
}

// postUser posts a create request as token and returns the response.
func postUser(t *testing.T, e *harness.Env, token string, body any) harness.Response {
	t.Helper()
	return e.Post(t, token, "/api/users", body)
}

// userCount counts the users rows.
func userCount(t *testing.T, e *harness.Env) int {
	t.Helper()
	return e.Count(t, "SELECT COUNT(*) FROM users")
}

// requireStoredNewUser checks the persisted row of a user made through POST
// /api/users: bcrypt hash, token, flags and the empty documents.
func requireStoredNewUser(t *testing.T, e *harness.Env, view userView, wantActive int64) map[string]any {
	t.Helper()
	row := userRow(t, e, view.ID)
	if row == nil {
		t.Fatalf("user %s has no row", view.Username)
	}
	pash, _ := row["pash"].(string)
	if row["username"] != view.Username || row["type"] != view.Type || row["isActive"] != wantActive || row["isLocked"] != int64(0) || row["lastSeen"] != nil ||
		!bcryptHash.MatchString(pash) || row["token"] != view.Token || view.Token == "" {
		t.Fatalf("stored row wrong: %v", row)
	}
	if !reflect.DeepEqual(jsonColumn(t, row, "bookmarks"), any([]any{})) || !reflect.DeepEqual(jsonColumn(t, row, "extraData"), map[string]any{"seriesHideFromContinueListening": []any{}}) {
		t.Fatalf("bookmarks and extraData of a new user: %v", row)
	}
	return row
}

// requireSameUserJSON requires an event payload to equal the response's user object.
func requireSameUserJSON(t *testing.T, event harness.Event, response harness.Response) {
	t.Helper()
	var body struct {
		User map[string]any `json:"user"`
	}
	response.JSON(t, &body)
	var payload map[string]any
	if err := json.Unmarshal(event.Args[0], &payload); err != nil {
		t.Fatalf("event payload: %v", err)
	}
	if !reflect.DeepEqual(payload, body.User) {
		t.Fatalf("the %s payload must equal the response user:\n event    %v\n response %v", event.Name, payload, body.User)
	}
}

func createScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r071", Invariant: "INV-R071-SAMPLED", Case: "normal", State: "initial", Assertion: "create-user-defaults",
			Sources: []string{"server/routers/ApiRouter.js:133", createSource, "server/models/User.js:134-148"},
			Claim:   "POST /api/users with only username and password creates an inactive account of type user with the default user permissions, no email, empty lists and a bcrypt password hash, and answers {user} in the browser view.",
			Run: func(t *testing.T, e *harness.Env) {
				before, started := userCount(t, e), time.Now().UnixMilli()-5000
				resp := postUser(t, e, e.Token(t, "root"), map[string]any{"username": "created-default", "password": userPassword})
				wantStatus(t, resp, http.StatusOK, "create")
				var created createResult
				resp.JSON(t, &created)
				view := created.User
				if view.ID == "" || view.Username != "created-default" || view.Type != "user" || view.IsActive || view.IsLocked || view.Email != nil || view.LastSeen != nil || view.HasOpenIDLink || view.CreatedAt < started {
					t.Fatalf("view wrong: %+v", view)
				}
				requireViewPermissions(t, view, defaultPermissions("user"), nil, nil)
				if view.MediaProgress == nil || len(view.MediaProgress) != 0 || view.Bookmarks == nil || len(view.Bookmarks) != 0 || view.SeriesHideFromContinueListening == nil || len(view.SeriesHideFromContinueListening) != 0 {
					t.Fatalf("empty lists must be arrays: %+v", view)
				}
				if userCount(t, e) != before+1 {
					t.Fatalf("exactly one row must be added")
				}
				row := requireStoredNewUser(t, e, view, 0)
				if row["email"] != nil {
					t.Fatalf("no email given: %v", row["email"])
				}
				requireRowPermissions(t, e, view.ID, defaultPermissions("user"), nil, nil)
				if tokenReply := e.Post(t, "", "/login", map[string]string{"username": "created-default", "password": userPassword}); tokenReply.Status != http.StatusUnauthorized {
					t.Fatalf("an inactive new account cannot log in: %d %s", tokenReply.Status, tokenReply.Body)
				}
			},
		},
		{
			Requirement: "route-r071", Invariant: "INV-R071-PERSIST-001", Case: "normal", State: "changed", Assertion: "create-admin-with-access-fields",
			Sources: []string{createSource, "server/models/User.js:353-390"},
			Claim:   "POST /api/users with type admin, email, isActive, permission overrides and library and tag selections (top-level or inside permissions) stores them in the permissions JSON; the new account logs in, is listed and read back by id.",
			Run: func(t *testing.T, e *harness.Env) {
				flags := defaultPermissions("admin")
				flags["download"], flags["accessExplicitContent"], flags["accessAllLibraries"], flags["accessAllTags"], flags["selectedTagsNotAccessible"] = false, false, false, false, true
				resp := postUser(t, e, e.Token(t, "root"), map[string]any{
					"username": "created-admin", "password": userPassword, "type": "admin", "isActive": true, "email": "created-admin@example.com",
					"librariesAccessible": []string{e.IDs["library"]},
					"permissions": map[string]any{
						"download": false, "accessExplicitContent": false, "accessAllLibraries": false, "accessAllTags": false, "selectedTagsNotAccessible": true,
						"itemTagsSelected": []string{"Tag A", "Tag B"},
					},
				})
				wantStatus(t, resp, http.StatusOK, "create")
				var created createResult
				resp.JSON(t, &created)
				view := created.User
				if view.Type != "admin" || !view.IsActive || view.Email == nil || *view.Email != "created-admin@example.com" {
					t.Fatalf("view wrong: %+v", view)
				}
				requireViewPermissions(t, view, flags, []string{e.IDs["library"]}, []string{"Tag A", "Tag B"})
				row := requireStoredNewUser(t, e, view, 1)
				if row["email"] != "created-admin@example.com" {
					t.Fatalf("stored email wrong: %v", row["email"])
				}
				requireRowPermissions(t, e, view.ID, flags, []string{e.IDs["library"]}, []string{"Tag A", "Tag B"})
				token := loginToken(t, e, "created-admin")
				if listed := listUsers(t, e, token, nil); findUser(listed, view.ID) == nil {
					t.Fatalf("the new admin must be an admin: it reads the user list")
				}
				readBack := getUser(t, e, e.Token(t, "root"), view.ID)
				requireViewPermissions(t, readBack, flags, []string{e.IDs["library"]}, []string{"Tag A", "Tag B"})
				if findUser(listUsers(t, e, e.Token(t, "root"), nil), view.ID) == nil {
					t.Fatalf("the new user must be listed")
				}
			},
		},
		{
			Requirement: "route-r071", Invariant: "INV-R071-PERSIST-001", Case: "edge", State: "initial", Assertion: "create-user-ignores-unknown-permission-keys",
			Sources: []string{createSource},
			Claim:   "POST /api/users keeps the type's defaults for permission keys the server does not know and stores no such key in users.permissions.",
			Run: func(t *testing.T, e *harness.Env) {
				resp := postUser(t, e, e.Token(t, "root"), map[string]any{
					"username": "created-unknown-key", "password": userPassword, "type": "guest", "isActive": true,
					"permissions": map[string]any{"bogus": true, "download": false},
				})
				wantStatus(t, resp, http.StatusOK, "create")
				var created createResult
				resp.JSON(t, &created)
				flags := defaultPermissions("guest")
				flags["download"] = false
				requireViewPermissions(t, created.User, flags, nil, nil)
				requireRowPermissions(t, e, created.User.ID, flags, nil, nil)
			},
		},
		{
			Requirement: "route-r071", Invariant: "INV-R071-PERSIST-001", Case: "normal", State: "initial", Assertion: "create-user-flag-matrix",
			Sources: []string{createSource, "client/components/modals/AccountModal.vue:334-337", "server/models/User.js:134-148"},
			Claim:   "POST /api/users with the account modal's full permissions object stores each of the nine flags set to the opposite of its type default, for user, admin and guest, in the response and in users.permissions.",
			Run: func(t *testing.T, e *harness.Env) {
				for _, accountType := range []string{"user", "admin", "guest"} {
					want := flipped(defaultPermissions(accountType))
					body := withCopy(modalBody(want, []string{}, []string{}), map[string]any{"username": "matrix-" + accountType, "password": userPassword, "type": accountType, "isActive": true})
					resp := postUser(t, e, e.Token(t, "root"), body)
					wantStatus(t, resp, http.StatusOK, "create "+accountType)
					var created createResult
					resp.JSON(t, &created)
					requireViewPermissions(t, created.User, want, nil, nil)
					requireRowPermissions(t, e, created.User.ID, want, nil, nil)
					if again := getUser(t, e, e.Token(t, "root"), created.User.ID); !equalValue(again.Permissions, want) {
						t.Fatalf("%s: a read must show the stored flags: %v", accountType, again.Permissions)
					}
				}
			},
		},
		{
			Requirement: "route-r071", Invariant: "INV-R071-PERSIST-001", Case: "edge", State: "initial", Assertion: "create-user-modal-empty-fields",
			Sources: []string{createSource, "client/components/modals/AccountModal.vue:334-337"},
			Claim:   "POST /api/users keeps an empty-string email as the empty string, stores a null email as NULL, and stores empty top-level librariesAccessible and itemTagsSelected as empty lists.",
			Run: func(t *testing.T, e *harness.Env) {
				for name, email := range map[string]any{"created-email-empty": "", "created-email-null": nil} {
					resp := postUser(t, e, e.Token(t, "root"), map[string]any{"username": name, "password": userPassword, "isActive": true, "email": email, "librariesAccessible": []string{}, "itemTagsSelected": []string{}})
					wantStatus(t, resp, http.StatusOK, name)
					var created createResult
					resp.JSON(t, &created)
					row := userRow(t, e, created.User.ID)
					if email == "" {
						if created.User.Email == nil || *created.User.Email != "" || row["email"] != "" {
							t.Fatalf("an empty email is kept as the empty string: view %v row %v", created.User.Email, row["email"])
						}
					} else if created.User.Email != nil || row["email"] != nil {
						t.Fatalf("a null email is stored as NULL: view %v row %v", created.User.Email, row["email"])
					}
					requireViewPermissions(t, created.User, defaultPermissions("user"), nil, nil)
					requireRowPermissions(t, e, created.User.ID, defaultPermissions("user"), nil, nil)
				}
			},
		},
		{
			Requirement: "route-r071", Invariant: "INV-R071-AUTH-001", Case: "edge", State: "initial", Assertion: "create-user-by-non-root-admin",
			Sources: []string{middlewareJS, createSource},
			Claim:   "A non-root admin may create users of every type except root, admins included; type root is rejected with 400.",
			Run: func(t *testing.T, e *harness.Env) {
				admin := adminToken(t, e)
				resp := postUser(t, e, admin, map[string]any{"username": "created-by-admin", "password": userPassword, "type": "admin", "isActive": true})
				wantStatus(t, resp, http.StatusOK, "admin creates admin")
				var created createResult
				resp.JSON(t, &created)
				if created.User.Type != "admin" || userRow(t, e, created.User.ID)["type"] != "admin" {
					t.Fatalf("an admin may create an admin: %+v", created.User)
				}
				before := userCount(t, e)
				wantStatus(t, postUser(t, e, admin, map[string]any{"username": "created-root", "password": userPassword, "type": "root"}), http.StatusBadRequest, "type root")
				wantStatus(t, postUser(t, e, e.Token(t, "root"), map[string]any{"username": "created-root", "password": userPassword, "type": "root"}), http.StatusBadRequest, "type root as root")
				if userCount(t, e) != before {
					t.Fatalf("a rejected request must not add a user")
				}
			},
		},
		{
			Requirement: "route-r071", Invariant: "INV-R071-USERNAME-001", Case: "edge", State: "initial", Assertion: "create-username-is-case-sensitive-and-unicode",
			Sources: []string{createSource, "server/models/User.js:484-491"},
			Claim:   "The username uniqueness check compares the exact text: a name differing only in case is accepted as a different user, and a Unicode name is stored and can log in.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				resp := postUser(t, e, root, map[string]any{"username": "Created-Case", "password": userPassword, "isActive": true})
				wantStatus(t, resp, http.StatusOK, "first")
				wantStatus(t, postUser(t, e, root, map[string]any{"username": "created-case", "password": userPassword, "isActive": true}), http.StatusOK, "differs only in case")
				if e.Count(t, "SELECT COUNT(*) FROM users WHERE lower(username) = 'created-case'") != 2 {
					t.Fatalf("both case variants must be stored")
				}
				wantStatus(t, postUser(t, e, root, map[string]any{"username": "Created-Case", "password": userPassword}), http.StatusBadRequest, "exact duplicate")
				name := "Zoë 用户"
				resp = postUser(t, e, root, map[string]any{"username": name, "password": userPassword, "isActive": true})
				wantStatus(t, resp, http.StatusOK, "unicode")
				if e.Count(t, "SELECT COUNT(*) FROM users WHERE username = ?", name) != 1 {
					t.Fatalf("the Unicode name must be stored as given")
				}
				loginToken(t, e, name)
			},
		},
		{
			Requirement: "route-r071", Invariant: "INV-R071-VALIDATE-001", Case: "error", State: "failure", Assertion: "create-user-rejections-change-nothing",
			Sources: []string{createSource},
			Claim:   "POST /api/users answers 400 for a missing, empty or non-string username or password, an unknown account type and a taken username, and changes no row.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				listenerRow := userRow(t, e, e.IDs["listener"])
				before := userCount(t, e)
				for name, body := range map[string]any{
					"no body fields":      map[string]any{},
					"no username":         map[string]any{"password": "x"},
					"no password":         map[string]any{"username": "rejected-user"},
					"empty username":      map[string]any{"username": "", "password": "x"},
					"empty password":      map[string]any{"username": "rejected-user", "password": ""},
					"numeric username":    map[string]any{"username": 123, "password": "x"},
					"numeric password":    map[string]any{"username": "rejected-user", "password": 123},
					"unknown type":        map[string]any{"username": "rejected-user", "password": "x", "type": "superuser"},
					"username taken":      map[string]any{"username": "trial-listener", "password": "new-password"},
					"taken by a new user": map[string]any{"username": "created-default", "password": "new-password"},
				} {
					wantStatus(t, postUser(t, e, root, body), http.StatusBadRequest, name)
				}
				if userCount(t, e) != before || e.Count(t, "SELECT COUNT(*) FROM users WHERE username = 'rejected-user'") != 0 {
					t.Fatalf("rejected requests must not add users")
				}
				requireRowUnchanged(t, e, e.IDs["listener"], listenerRow)
				if reply := e.Post(t, "", "/login", map[string]string{"username": "trial-listener", "password": "new-password"}); reply.Status != http.StatusUnauthorized {
					t.Fatalf("a taken username must not get the new password: %d", reply.Status)
				}
			},
		},
		{
			Requirement: "route-r071", Invariant: "INV-R071-USERNAME-001", Case: "normal", State: "repeat", Assertion: "create-user-twice",
			Sources: []string{createSource},
			Claim:   "Posting the same new username twice creates it once: the second request answers 400 and the first account keeps its row and password.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				resp := postUser(t, e, root, map[string]any{"username": "created-twice", "password": userPassword, "isActive": true})
				wantStatus(t, resp, http.StatusOK, "first")
				var created createResult
				resp.JSON(t, &created)
				row := userRow(t, e, created.User.ID)
				wantStatus(t, postUser(t, e, root, map[string]any{"username": "created-twice", "password": "other-password", "isActive": true}), http.StatusBadRequest, "second")
				if e.Count(t, "SELECT COUNT(*) FROM users WHERE username = 'created-twice'") != 1 {
					t.Fatalf("the name must exist once")
				}
				requireRowUnchanged(t, e, created.User.ID, row)
				loginToken(t, e, "created-twice")
				if reply := e.Post(t, "", "/login", map[string]string{"username": "created-twice", "password": "other-password"}); reply.Status != http.StatusUnauthorized {
					t.Fatalf("the rejected password must not work: %d", reply.Status)
				}
			},
		},
		{
			Requirement: "route-r071", Invariant: "INV-R071-AUTH-001", Case: "error", State: "initial", Assertion: "create-user-requires-admin",
			Sources: []string{"server/Server.js:354", middlewareJS},
			Claim:   "POST /api/users answers 401 without a valid token and 403 to non-admin users, and creates nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				before := userCount(t, e)
				body := map[string]any{"username": "forbidden-user", "password": userPassword}
				for _, token := range []string{"", "not-a-token"} {
					wantStatus(t, postUser(t, e, token, body), http.StatusUnauthorized, "token "+token)
				}
				for _, who := range []string{"listener", "finisher"} {
					wantStatus(t, postUser(t, e, e.Token(t, who), body), http.StatusForbidden, who)
				}
				guest := createUser(t, e, map[string]any{"username": "created-guest", "type": "guest"})
				wantStatus(t, postUser(t, e, loginToken(t, e, guest.Username), body), http.StatusForbidden, "guest")
				if userCount(t, e) != before+1 || e.Count(t, "SELECT COUNT(*) FROM users WHERE username = 'forbidden-user'") != 0 {
					t.Fatalf("a forbidden request must not create a user")
				}
			},
		},
		{
			Requirement: "socket-outgoing-user-added", Invariant: "INV-SOCKET-OUTGOING-USER-ADDED", Case: "normal", State: "initial", Assertion: "user-added-event-to-admins",
			Sources: []string{addedSource},
			Claim:   "Creating a user emits user_added with the new user's browser view (equal to the response) to every admin socket, root and non-root, and not to a non-admin user's socket.",
			Run: func(t *testing.T, e *harness.Env) {
				audience := connectAudiences(t, e)
				marks := [3]int{audience.root.Mark(), audience.admin.Mark(), audience.listener.Mark()}
				resp := postUser(t, e, e.Token(t, "root"), map[string]any{"username": "event-added", "password": userPassword, "type": "admin", "isActive": true})
				wantStatus(t, resp, http.StatusOK, "create")
				var created createResult
				resp.JSON(t, &created)
				for i, socket := range []*harness.Socket{audience.root, audience.admin} {
					event := socket.Wait(t, marks[i], "user_added", waitEvent, matchUsername("event-added"))
					requireSameUserJSON(t, event, resp)
					if userRow(t, e, eventUser(t, event).ID) == nil {
						t.Fatalf("the event must follow the persisted row")
					}
				}
				audience.listener.None(t, marks[2], "user_added", quiet, nil)
			},
		},
		{
			Requirement: "socket-outgoing-user-added", Invariant: "INV-SOCKET-OUTGOING-USER-ADDED", Case: "edge", State: "repeat", Assertion: "user-added-event-per-creation",
			Sources: []string{addedSource},
			Claim:   "Each created user emits its own user_added event, in creation order, also when a non-root admin does the creating, and the creating admin receives it too.",
			Run: func(t *testing.T, e *harness.Env) {
				audience := connectAudiences(t, e)
				mark := audience.admin.Mark()
				admin := adminToken(t, e)
				var ids []string
				for _, name := range []string{"event-added-1", "event-added-2"} {
					resp := postUser(t, e, admin, map[string]any{"username": name, "password": userPassword})
					wantStatus(t, resp, http.StatusOK, "create "+name)
					var created createResult
					resp.JSON(t, &created)
					ids = append(ids, created.User.ID)
				}
				first := audience.admin.Wait(t, mark, "user_added", waitEvent, matchUserID(ids[0]))
				second := audience.admin.Wait(t, mark, "user_added", waitEvent, matchUserID(ids[1]))
				if !first.At.Before(second.At) && !first.At.Equal(second.At) {
					t.Fatalf("events must arrive in creation order")
				}
				audience.root.Wait(t, 0, "user_added", waitEvent, matchUserID(ids[1]))
			},
		},
		{
			Requirement: "socket-outgoing-user-added", Invariant: "INV-SOCKET-OUTGOING-USER-ADDED", Case: "edge", State: "changed", Assertion: "user-added-event-after-name-freed",
			Sources: []string{addedSource, updateSource},
			Claim:   "After a user is renamed, creating a user with the freed name succeeds and emits user_added for the new account.",
			Run: func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				old := createUser(t, e, map[string]any{"username": "event-reuse"})
				patchOK(t, e, root, old.ID, map[string]any{"username": "event-reuse-renamed"})
				socket := e.Socket(t, root)
				mark := socket.Mark()
				resp := postUser(t, e, root, map[string]any{"username": "event-reuse", "password": userPassword})
				wantStatus(t, resp, http.StatusOK, "create with freed name")
				var created createResult
				resp.JSON(t, &created)
				event := socket.Wait(t, mark, "user_added", waitEvent, matchUserID(created.User.ID))
				if created.User.ID == old.ID || eventUser(t, event).Username != "event-reuse" {
					t.Fatalf("the event must describe the new account: %+v", eventUser(t, event))
				}
			},
		},
		{
			Requirement: "socket-outgoing-user-added", Invariant: "INV-SOCKET-OUTGOING-USER-ADDED", Case: "error", State: "failure", Assertion: "user-added-event-not-sent-on-rejection",
			Sources: []string{addedSource, createSource},
			Claim:   "A rejected create (duplicate username, invalid body, forbidden caller) emits no user_added event to any socket.",
			Run: func(t *testing.T, e *harness.Env) {
				audience := connectAudiences(t, e)
				marks := [3]int{audience.root.Mark(), audience.admin.Mark(), audience.listener.Mark()}
				root := e.Token(t, "root")
				wantStatus(t, postUser(t, e, root, map[string]any{"username": "trial-listener", "password": "x"}), http.StatusBadRequest, "duplicate")
				wantStatus(t, postUser(t, e, root, map[string]any{"username": "event-rejected"}), http.StatusBadRequest, "no password")
				wantStatus(t, postUser(t, e, e.Token(t, "listener"), map[string]any{"username": "event-rejected", "password": "x"}), http.StatusForbidden, "forbidden")
				for i, socket := range []*harness.Socket{audience.root, audience.admin, audience.listener} {
					socket.None(t, marks[i], "user_added", quiet, nil)
				}
			},
		},
	}
}
