package realtime

// Packet-local helpers for the realtime scenarios: event waits, audience
// checks, fixture users and API keys. The shared harness offers only what every
// packet needs, so the socket vocabulary of this packet lives here.

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	// socketWait bounds one expected event.
	socketWait = 10 * time.Second
	// socketQuiet is the window in which a socket that must not receive an
	// event stays silent. Socket.None is the harness' only negative check.
	socketQuiet = 500 * time.Millisecond
	// userPassword is the password of every user this packet creates.
	userPassword = "realtime-packet-password"
)

// sessionDoc is the PlaybackSession.toJSONForClient subset a client reads from
// a user's public session field.
type sessionDoc struct {
	ID            string         `json:"id"`
	UserID        string         `json:"userId"`
	LibraryItemID string         `json:"libraryItemId"`
	DisplayTitle  string         `json:"displayTitle"`
	MediaPlayer   string         `json:"mediaPlayer"`
	DeviceInfo    map[string]any `json:"deviceInfo"`
}

// publicUser is User.toJSONForPublic: the payload of user_online and
// user_offline, and one usersOnline entry of init (which adds connections).
type publicUser struct {
	ID          string      `json:"id"`
	Username    string      `json:"username"`
	Type        string      `json:"type"`
	Session     *sessionDoc `json:"session"`
	LastSeen    *int64      `json:"lastSeen"`
	CreatedAt   int64       `json:"createdAt"`
	Connections int         `json:"connections"`
}

// initPayload is the init event. UsersOnline is present for admins only.
type initPayload struct {
	UserID      string       `json:"userId"`
	Username    string       `json:"username"`
	UsersOnline []publicUser `json:"usersOnline"`
}

// requireStatus fails unless the response has the status.
func requireStatus(t *testing.T, resp harness.Response, status int, what string) {
	t.Helper()
	if resp.Status != status {
		t.Fatalf("%s: want %d, got %d %.300s", what, status, resp.Status, resp.Body)
	}
}

// connectAnon opens a socket that never authenticates.
func connectAnon(t *testing.T, e *harness.Env) *harness.Socket {
	t.Helper()
	return e.Socket(t, "")
}

// connectAuth opens a socket authenticated with token and returns it with the
// init payload it received.
func connectAuth(t *testing.T, e *harness.Env, token string) (*harness.Socket, initPayload) {
	t.Helper()
	s := e.Socket(t, "")
	payload := authenticate(t, s, token)
	return s, payload
}

// authenticate emits auth with the token and returns the init payload.
func authenticate(t *testing.T, s *harness.Socket, token string) initPayload {
	t.Helper()
	mark := s.Mark()
	s.Emit(t, "auth", token)
	return decodeArg[initPayload](t, waitEvent(t, s, mark, "init"))
}

// rejectAuth emits auth with the given arguments and returns the auth_failed
// event, requiring that no init followed it.
func rejectAuth(t *testing.T, s *harness.Socket, args ...any) harness.Event {
	t.Helper()
	mark := s.Mark()
	s.Emit(t, "auth", args...)
	event := waitEvent(t, s, mark, "auth_failed")
	requireQuiet(t, s, mark, "init")
	return event
}

// waitEvent waits for the next event with that name.
func waitEvent(t *testing.T, s *harness.Socket, mark int, name string) harness.Event {
	t.Helper()
	return s.Wait(t, mark, name, socketWait, nil)
}

// waitUserEvent waits for the next event with that name whose first argument is
// the public user with the id.
func waitUserEvent(t *testing.T, s *harness.Socket, mark int, name, id string) harness.Event {
	t.Helper()
	return s.Wait(t, mark, name, socketWait, func(args []json.RawMessage) bool {
		return decodeRaw[publicUser](t, args[0]).ID == id
	})
}

// firstArg returns the first argument of an event.
func firstArg(t *testing.T, event harness.Event) json.RawMessage {
	t.Helper()
	if len(event.Args) != 1 {
		t.Fatalf("%s: want exactly one argument, got %d: %s", event.Name, len(event.Args), event.Args)
	}
	return event.Args[0]
}

// requireNoArgs requires an event without arguments.
func requireNoArgs(t *testing.T, event harness.Event) {
	t.Helper()
	if len(event.Args) != 0 {
		t.Fatalf("%s: want no arguments, got %s", event.Name, event.Args)
	}
}

// decodeArg decodes the first argument of an event.
func decodeArg[T any](t *testing.T, event harness.Event) T {
	t.Helper()
	return decodeRaw[T](t, firstArg(t, event))
}

// argObject decodes the first argument of an event as an object.
func argObject(t *testing.T, event harness.Event) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(firstArg(t, event), &object); err != nil || object == nil {
		t.Fatalf("%s: argument is not a JSON object: %s (%v)", event.Name, firstArg(t, event), err)
	}
	return object
}

// decodeRaw decodes one JSON value of an event.
func decodeRaw[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return value
}

// requireExactKeys requires the object to have exactly these keys.
func requireExactKeys(t *testing.T, what string, object map[string]any, keys ...string) {
	t.Helper()
	got, want := slices.Sorted(maps.Keys(object)), slices.Clone(keys)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("%s: want keys %v, got %v (%v)", what, want, got, object)
	}
}

// requireQuiet requires that none of the names is among the events since mark.
// The caller first waits for a later positive effect (or flushes the actor), so
// the window is a negative check, never a sleep to wait for an effect.
func requireQuiet(t *testing.T, s *harness.Socket, mark int, names ...string) {
	t.Helper()
	for i, event := range s.Events() {
		if i < mark || !slices.Contains(names, event.Name) {
			continue
		}
		t.Fatalf("unexpected %q event since %d: %s", event.Name, mark, event.Args)
	}
}

// countEvents counts the events with that name since mark.
func countEvents(s *harness.Socket, mark int, name string) int {
	count := 0
	for i, event := range s.Events() {
		if i >= mark && event.Name == name {
			count++
		}
	}
	return count
}

// userEvents returns the events with that name since mark whose first argument
// is the public user with the id.
func userEvents(t *testing.T, s *harness.Socket, mark int, name, id string) []harness.Event {
	t.Helper()
	var found []harness.Event
	for i, event := range s.Events() {
		if i < mark || event.Name != name || len(event.Args) == 0 {
			continue
		}
		if decodeRaw[publicUser](t, event.Args[0]).ID == id {
			found = append(found, event)
		}
	}
	return found
}

// waitUserEvents waits until count events with that name for the user id have
// arrived and returns exactly those.
func waitUserEvents(t *testing.T, s *harness.Socket, mark int, name, id string, count int) []harness.Event {
	t.Helper()
	var found []harness.Event
	harness.Eventually(t, socketWait, fmt.Sprintf("%d %s events for %s", count, name, id), func() bool {
		found = userEvents(t, s, mark, name, id)
		return len(found) >= count
	})
	return found[:count]
}

// flush waits until the server has handled every event already sent from s by
// sending the application ping and waiting for its pong.
func flush(t *testing.T, s *harness.Socket) {
	t.Helper()
	mark := s.Mark()
	s.Emit(t, "ping")
	s.Wait(t, mark, "pong", socketWait, nil)
}

// initEvent returns the init event a socket received; e.Socket already
// consumed it, so a scenario reads it back from the recorded events.
func initEvent(t *testing.T, s *harness.Socket) harness.Event {
	t.Helper()
	for _, event := range s.Events() {
		if event.Name == "init" {
			return event
		}
	}
	t.Fatal("the socket received no init event")
	return harness.Event{}
}

// onlineUsers reads GET /api/users/online as root.
func onlineUsers(t *testing.T, e *harness.Env) []publicUser {
	t.Helper()
	resp := e.Get(t, e.Token(t, "root"), "/api/users/online")
	requireStatus(t, resp, http.StatusOK, "GET /api/users/online")
	var body struct {
		UsersOnline []publicUser `json:"usersOnline"`
	}
	resp.JSON(t, &body)
	return body.UsersOnline
}

// onlineEntry returns the usersOnline entry of a user id, or nil.
func onlineEntry(t *testing.T, e *harness.Env, id string) *publicUser {
	t.Helper()
	for _, user := range onlineUsers(t, e) {
		if user.ID == id {
			return &user
		}
	}
	return nil
}

// userID returns the id of a fixture user by username, read through the admin
// users list (ids.json only names the library, items and the listener).
func userID(t *testing.T, e *harness.Env, username string) string {
	t.Helper()
	resp := e.Get(t, e.Token(t, "root"), "/api/users")
	requireStatus(t, resp, http.StatusOK, "GET /api/users")
	var body struct {
		Users []struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"users"`
	}
	resp.JSON(t, &body)
	for _, user := range body.Users {
		if user.Username == username {
			return user.ID
		}
	}
	t.Fatalf("fixture user %s not found", username)
	return ""
}

// createUser creates an active non-admin user and logs it in.
func createUser(t *testing.T, e *harness.Env, username string) (id, token string) {
	t.Helper()
	resp := e.Post(t, e.Token(t, "root"), "/api/users", map[string]any{
		"username": username, "password": userPassword, "type": "user", "isActive": true,
	})
	requireStatus(t, resp, http.StatusOK, "create user "+username)
	var created struct {
		User struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"user"`
	}
	resp.JSON(t, &created)
	if created.User.ID == "" || created.User.Username != username {
		t.Fatalf("create user %s: bad answer %.300s", username, resp.Body)
	}
	token, _ = e.Login(t, username, userPassword)
	return created.User.ID, token
}

// userRow reads the users row of an id, or nil.
func userRow(t *testing.T, e *harness.Env, id string) map[string]any {
	t.Helper()
	rows := e.Rows(t, "SELECT * FROM users WHERE id = ?", id)
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}

// requireRowUnchanged requires a users row to be identical to the value read
// before the rejected request.
func requireRowUnchanged(t *testing.T, e *harness.Env, id string, before map[string]any) {
	t.Helper()
	if after := userRow(t, e, id); !reflect.DeepEqual(after, before) {
		t.Fatalf("a rejected socket request must not change the users row:\n before %v\n after  %v", before, after)
	}
}

// apiKey is one POST /api/api-keys answer: the JWT is only returned at creation.
type apiKey struct {
	ID    string `json:"id"`
	Key   string `json:"apiKey"`
	KeyID string `json:"keyId"`
}

// createAPIKey creates an API key for a user as root. expiresIn is passed only
// when positive.
func createAPIKey(t *testing.T, e *harness.Env, name, userID string, active bool, expiresIn int) apiKey {
	t.Helper()
	body := map[string]any{"name": name, "userId": userID, "isActive": active}
	if expiresIn > 0 {
		body["expiresIn"] = expiresIn
	}
	resp := e.Post(t, e.Token(t, "root"), "/api/api-keys", body)
	requireStatus(t, resp, http.StatusOK, "create API key "+name)
	var answer struct {
		APIKey struct {
			APIKey string `json:"apiKey"`
			KeyID  string `json:"id"`
			Name   string `json:"name"`
		} `json:"apiKey"`
	}
	resp.JSON(t, &answer)
	if answer.APIKey.APIKey == "" || answer.APIKey.KeyID == "" {
		t.Fatalf("create API key %s: no key or id: %.300s", name, resp.Body)
	}
	return apiKey{ID: answer.APIKey.KeyID, Key: answer.APIKey.APIKey}
}

// play starts a playback session for the item and returns its id, so a user has
// an open session in usersOnline and in the user_online payload.
func play(t *testing.T, e *harness.Env, token, itemID, deviceID string) string {
	t.Helper()
	resp := e.Post(t, token, "/api/items/"+itemID+"/play", map[string]any{
		"deviceInfo":         map[string]any{"deviceId": deviceID, "clientName": "realtime-packet"},
		"mediaPlayer":        "realtime-packet",
		"forceDirectPlay":    true,
		"supportedMimeTypes": []string{},
	})
	requireStatus(t, resp, http.StatusOK, "start playback")
	var session struct {
		ID string `json:"id"`
	}
	resp.JSON(t, &session)
	if session.ID == "" {
		t.Fatalf("play returned no session id: %.300s", resp.Body)
	}
	return session.ID
}

// closePlayback closes a session this packet started.
func closePlayback(t *testing.T, e *harness.Env, token, id string) {
	t.Helper()
	requireStatus(t, e.Post(t, token, "/api/session/"+id+"/close", map[string]any{}), http.StatusOK, "close playback")
}

// dbMillis parses a stored Sequelize DATE column to epoch milliseconds.
func dbMillis(t *testing.T, value any) int64 {
	t.Helper()
	text, _ := value.(string)
	parsed, err := time.Parse("2006-01-02 15:04:05.000 -07:00", text)
	if err != nil {
		t.Fatalf("stored time %v: %v", value, err)
	}
	return parsed.UnixMilli()
}
