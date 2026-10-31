package realtime

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/identity"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/wire"

	_ "modernc.org/sqlite"
)

func testDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "realtime.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE users (id TEXT PRIMARY KEY, username TEXT, email TEXT, pash TEXT, type TEXT, token TEXT, isActive INTEGER, isLocked INTEGER, lastSeen DATETIME, permissions TEXT, bookmarks TEXT, extraData TEXT, createdAt DATETIME, updatedAt DATETIME)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, username, email, pash, type, token, isActive, isLocked, lastSeen, permissions, bookmarks, extraData, createdAt, updatedAt) VALUES ('user-1', 'alice', '', '', 'user', '', 1, 0, '1970-01-01 00:00:00.000 +00:00', '{"librariesAccessible":["lib-1"],"itemTagsSelected":["mystery"]}', '[]', '{}', '2020-01-01 00:00:00.000 +00:00', '1970-01-01 00:00:00.000 +00:00'), ('admin-1', 'root', '', '', 'admin', '', 1, 0, '1970-01-01 00:00:00.000 +00:00', '{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true}', '[]', '{}', '2020-01-01 00:00:00.000 +00:00', '1970-01-01 00:00:00.000 +00:00'), ('limited-1', 'bob', '', '', 'user', '', 1, 0, '1970-01-01 00:00:00.000 +00:00', '{"librariesAccessible":["lib-2"],"itemTagsSelected":["mystery"]}', '[]', '{}', '2020-01-01 00:00:00.000 +00:00', '1970-01-01 00:00:00.000 +00:00')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE libraries (id TEXT, name TEXT, displayOrder INTEGER, icon TEXT, mediaType TEXT, provider TEXT, lastScan DATETIME, lastScanVersion TEXT, settings TEXT, extraData TEXT, createdAt DATETIME, updatedAt DATETIME)`,
		`CREATE TABLE libraryFolders (id TEXT, path TEXT, libraryId TEXT, createdAt DATETIME, updatedAt DATETIME)`,
		`CREATE TABLE settings (key TEXT, value TEXT)`,
		`CREATE TABLE sessions (id TEXT, ipAddress TEXT, userAgent TEXT, refreshToken TEXT, expiresAt DATETIME, createdAt DATETIME, updatedAt DATETIME, userId TEXT, lastRefreshToken TEXT, lastRefreshTokenExpiresAt DATETIME)`,
		`CREATE TABLE apiKeys (id TEXT, isActive INTEGER, expiresAt DATETIME, lastUsedAt DATETIME, userId TEXT, updatedAt DATETIME)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

func TestNewRegistersLegacyAndPrefixedSocketPaths(t *testing.T) {
	db, _ := testDB(t)
	hub, err := New(db, config.Config{RouterBasePath: "/abs"}, func(*http.Request) (store.User, error) {
		return store.User{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	if got, want := strings.Join(hub.Paths(), ","), "/socket.io,/abs/socket.io"; got != want {
		t.Fatalf("Paths() = %q, want %q", got, want)
	}
	response := httptest.NewRecorder()
	hub.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/elsewhere", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("unknown path status = %d, want 404", response.Code)
	}
}

func TestPayloadAccessibleAppliesLibraryAndItemPermissionsWhenPresent(t *testing.T) {
	user := store.User{Permissions: []byte(`{"librariesAccessible":["lib-1"],"itemTagsSelected":["mystery"]}`)}
	allowed := map[string]any{"libraryId": "lib-1", "media": map[string]any{"tags": []any{"mystery"}}}
	wrongLibrary := map[string]any{"libraryId": "lib-2", "media": map[string]any{"tags": []any{"mystery"}}}
	wrongTag := map[string]any{"libraryId": "lib-1", "media": map[string]any{"tags": []any{"romance"}}}
	if !payloadAccessible(user, allowed) {
		t.Fatal("expected allowed library and tag payload to pass")
	}
	if payloadAccessible(user, wrongLibrary) {
		t.Fatal("payload from inaccessible library passed")
	}
	if payloadAccessible(user, wrongTag) {
		t.Fatal("payload with inaccessible tag passed")
	}
	if !payloadAccessible(user, map[string]any{"message": "global"}) {
		t.Fatal("global payload without item metadata should pass")
	}
	typedAllowed := map[string]any{
		"libraryId": "lib-1",
		"media": wire.BookExpanded{
			ID:       "book-1",
			Tags:     []string{"mystery"},
			Metadata: wire.BookMetadataExpanded{BookMetadataFull: wire.BookMetadataFull{Explicit: false}},
		},
	}
	if !payloadAccessible(user, typedAllowed) {
		t.Fatal("typed book media with an accessible tag should pass")
	}
	typedExplicit := map[string]any{
		"libraryId": "lib-1",
		"media": wire.BookExpanded{
			ID:       "book-2",
			Tags:     []string{"mystery"},
			Metadata: wire.BookMetadataExpanded{BookMetadataFull: wire.BookMetadataFull{Explicit: true}},
		},
	}
	if payloadAccessible(user, typedExplicit) {
		t.Fatal("typed explicit book media passed without explicit-content permission")
	}
	if payloadAccessible(user, map[string]any{"libraryId": "lib-1", "media": struct{}{}}) {
		t.Fatal("unrecognized media must fail closed")
	}
	collection := map[string]any{"id": "collection-1", "libraryId": "lib-1", "name": "Queue", "books": []any{}}
	if !payloadAccessible(user, collection) {
		t.Fatal("collection event with library access should be delivered")
	}
	libraryOnly := store.User{Permissions: []byte(`{"librariesAccessible":["lib-1"]}`)}
	if !payloadAccessible(libraryOnly, collection) {
		t.Fatal("collection event should not require item/tag permissions")
	}
	if payloadAccessible(user, map[string]any{"id": "collection-2", "libraryId": "lib-2", "name": "Other", "books": []any{}}) {
		t.Fatal("collection event without library access passed")
	}
	noLibrary := store.User{Permissions: []byte(`{"librariesAccessible":["lib-2"]}`)}
	if payloadAccessible(noLibrary, collection) {
		t.Fatal("collection event was delivered to a user without library access")
	}
	if _, send := payloadForUser(user, collection); !send {
		t.Fatal("collection broadcast was not sent to a user with library access")
	}
	if _, send := payloadForUser(noLibrary, collection); send {
		t.Fatal("collection broadcast was sent to a user without library access")
	}
	filtered, send := payloadForUser(user, []any{allowed, wrongLibrary})
	items, ok := filtered.([]any)
	if !send || !ok || len(items) != 1 {
		t.Fatalf("item-list broadcast filter = %#v, send=%v", filtered, send)
	}
}

func TestUsersOnlineCallsPlaybackLookupOutsideHubLock(t *testing.T) {
	db, _ := testDB(t)
	hub, err := New(db, config.Config{}, func(*http.Request) (store.User, error) { return store.User{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	hub.mu.Lock()
	hub.clients["socket-a"] = &client{user: &store.User{ID: "user-1", Username: "alice", Type: "user"}}
	hub.clients["socket-b"] = &client{user: &store.User{ID: "user-1", Username: "alice", Type: "user"}}
	hub.mu.Unlock()
	hub.SetSessionLookup(func(userID string) any {
		if userID != "user-1" {
			t.Errorf("session lookup user=%q", userID)
		}
		// A playback lookup may synchronously mutate hub state as it emits a
		// stream update. This would deadlock if usersOnline held Hub.mu.
		hub.SetSessionLookup(nil)
		return map[string]any{"id": "session-1"}
	})
	done := make(chan []map[string]any, 1)
	go func() { done <- hub.UsersOnline() }()
	select {
	case users := <-done:
		if len(users) != 1 || users[0]["connections"] != 2 || users[0]["session"].(map[string]any)["id"] != "session-1" {
			t.Fatalf("online users=%v", users)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("playback lookup deadlocked under Hub.mu")
	}
}

func TestSocketIO4PollingUpgradeAuthAndPrefixedPath(t *testing.T) {
	if err := exec.Command("node", "-e", "require('socket.io-client')").Run(); err != nil {
		t.Skip("socket.io-client@4 is not installed; set NODE_PATH to a directory containing it")
	}
	db, dbPath := testDB(t)
	secret := "realtime-integration-secret"
	authHandler, err := identity.New(db, config.Config{JWTSecret: secret})
	if err != nil {
		t.Fatal(err)
	}
	hub, err := New(db, config.Config{RouterBasePath: "/abs"}, authHandler.Authenticate)
	if err != nil {
		t.Fatal(err)
	}
	hub.SetDispatcher(func(_ context.Context, action Action) error {
		if action.Event == "cancel_scan" {
			hub.Emit("*", "probe_item", map[string]any{
				"libraryId": "lib-1", "media": map[string]any{"tags": []string{"mystery"}},
			})
			if action.Ack != nil {
				action.Ack([]any{"scan_cancelled"}, nil)
			}
		}
		return nil
	})
	defer hub.Close()
	server := httptest.NewServer(hub)
	defer server.Close()
	const probe = `
const assert = require('node:assert/strict');
const { io } = require('socket.io-client');
const base = process.env.ABS_PROBE_URL;
const once = (socket, name) => new Promise((resolve, reject) => {
  const finish = (fn, value) => { clearTimeout(timer); socket.off(name, onValue); socket.off('connect_error', onError); fn(value); };
  const timer = setTimeout(() => finish(reject, new Error('timeout waiting for ' + name)), 5000);
  const onValue = value => finish(resolve, value);
  const onError = error => finish(reject, error);
  socket.once(name, onValue);
  socket.once('connect_error', onError);
});
async function connect(path, token, opts, expectUpgrade, id, username) {
  const client = io(base, { path, timeout: 4000, ...opts });
  const seen = [];
  const diagnostics = [];
  client.onAny(event => seen.push(event));
  client.on('disconnect', reason => diagnostics.push('disconnect: ' + reason));
  client.on('connect_error', error => diagnostics.push('connect_error: ' + error.message));
  await once(client, 'connect');
  console.log('connected', path, client.io.engine.transport.name);
  const initPromise = once(client, 'init');
  let authFailure;
  client.once('auth_failed', value => { authFailure = value; });
		client.emit('auth', token);
  const init = await initPromise;
  assert.equal(authFailure, undefined, 'authentication failed: ' + JSON.stringify(authFailure));
  console.log('inited', path, JSON.stringify(init));
  assert.equal(init.userId, id);
  assert.equal(init.username, username);
  if (id === 'admin-1') assert(Array.isArray(init.usersOnline));
  const pong = once(client, 'pong');
  client.emit('ping');
  await pong.catch(error => {
    const engine = client.io.engine;
    throw new Error(error.message + '; path=' + path + '; user=' + id + '; connected=' + client.connected + '; transport=' + engine?.transport?.name + '; readyState=' + engine?.readyState + '; events=' + JSON.stringify(seen) + '; diagnostics=' + JSON.stringify(diagnostics));
  });
  console.log('pong', path);
  if (expectUpgrade) {
    const deadline = Date.now() + 3000;
    while (client.io.engine.transport.name !== 'websocket' && Date.now() < deadline) {
      await new Promise(resolve => setTimeout(resolve, 20));
    }
    assert.equal(client.io.engine.transport.name, 'websocket', 'polling connection did not upgrade');
  } else {
    assert.equal(client.io.engine.transport.name, 'polling');
  }
  return { client, seen, diagnostics };
}
(async () => {
  const admin = await connect('/socket.io', process.env.ABS_ADMIN_TOKEN, { transports: ['polling', 'websocket'], upgrade: true }, true, 'admin-1', 'root');
  const alice = await connect('/abs/socket.io', process.env.ABS_USER_TOKEN, { transports: ['polling'], upgrade: false }, false, 'user-1', 'alice');
  const restricted = await connect('/socket.io', process.env.ABS_LIMITED_TOKEN, { transports: ['polling'], upgrade: false }, false, 'limited-1', 'bob');
  await new Promise(resolve => setTimeout(resolve, 100));
  assert(admin.seen.some(event => event === 'user_online'), 'admin did not receive user_online');
  assert(!alice.seen.includes('user_online'), 'non-admin received admin-only user_online');
  const allowedItem = once(alice.client, 'probe_item');
  const acknowledgement = new Promise(resolve => admin.client.emit('cancel_scan', 'lib-1', resolve));
  await allowedItem;
  assert.equal(await acknowledgement, 'scan_cancelled');
  await new Promise(resolve => setTimeout(resolve, 200));
  assert(!restricted.seen.includes('probe_item'), 'broadcast crossed library permission boundary');
  admin.client.disconnect(); alice.client.disconnect(); restricted.client.disconnect();
  console.log('Socket.IO4 protocol, auth, recipient scope, and permission fanout passed');
  process.exit(0);
})().catch(error => { console.error(error); process.exit(1); });
`
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "-e", probe)
	cmd.Env = append(cmd.Environ(),
		"ABS_PROBE_URL="+server.URL,
		"ABS_ADMIN_TOKEN="+signedToken(t, secret, map[string]any{"type": "access", "userId": "admin-1", "username": "root", "jti": "admin-jti"}),
		"ABS_USER_TOKEN="+signedToken(t, secret, map[string]any{"type": "access", "userId": "user-1", "username": "alice", "jti": "user-jti"}),
		"ABS_LIMITED_TOKEN="+signedToken(t, secret, map[string]any{"type": "access", "userId": "limited-1", "username": "bob", "jti": "limited-jti"}),
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Socket.IO client probe failed: %v (ctx error: %v)\n%s", err, ctx.Err(), output)
	}
	var lastSeen, updatedAt string
	if err := db.QueryRow(`SELECT lastSeen, updatedAt FROM users WHERE id='user-1'`).Scan(&lastSeen, &updatedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := time.Parse("2006-01-02 15:04:05.000 -07:00", lastSeen); err != nil {
		t.Fatalf("Socket.IO auth wrote a non-Sequelize lastSeen value %q: %v", lastSeen, err)
	}
	if _, err := time.Parse("2006-01-02 15:04:05.000 -07:00", updatedAt); err != nil {
		t.Fatalf("Socket.IO auth wrote a non-Sequelize updatedAt value %q: %v", updatedAt, err)
	}
	repo, err := store.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("identity-facing read-only store could not reopen after socket auth: %v", err)
	}
	defer repo.Close()
	user, err := repo.UserByID(context.Background(), "user-1")
	if err != nil || user.LastSeen == nil || user.UpdatedAt == nil {
		t.Fatalf("identity-facing user read after socket auth: user=%+v err=%v", user, err)
	}
}

func signedToken(t *testing.T, secret string, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	encode := base64.RawURLEncoding.EncodeToString
	unsigned := encode(header) + "." + encode(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(unsigned))
	return unsigned + "." + encode(mac.Sum(nil))
}
