package contracttest

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"audiobookshelf-compat/internal/access"
)

// createUser makes an active user with all-access permissions and returns its
// id and token.
func createUser(t *testing.T, in *Install, root, name string) (string, string) {
	t.Helper()
	body := map[string]any{"username": name, "password": name + "-password", "type": "user", "isActive": true,
		"permissions": map[string]any{"accessAllLibraries": true, "accessAllTags": true, "accessExplicitContent": true}}
	resp, raw := in.Do(t, http.MethodPost, "/api/users", root, body)
	var created struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(raw, &created); resp.StatusCode != http.StatusOK || err != nil || created.User.ID == "" {
		t.Fatalf("create user: %d %s", resp.StatusCode, raw)
	}
	return created.User.ID, in.Login(t, name, name+"-password")
}

// TestRequestDecodesPermissionsOnce proves the runtime decodes a request user's
// permissions once, however many checks the handlers make.
func TestRequestDecodesPermissionsOnce(t *testing.T) {
	in := Boot(t)
	root := in.Login(t, in.Users["root"].Username, in.Users["root"].Password)
	_, token := createUser(t, in, root, "decode-once")
	lib := "/api/libraries/" + in.IDs["library"]
	for _, path := range []string{lib + "/items?limit=5", "/api/items/" + in.IDs["novel"] + "?expanded=1", lib + "/personalized?minified=1"} {
		before := access.Decodes()
		if resp, raw := in.Do(t, http.MethodGet, path, token, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, resp.StatusCode, raw)
		}
		if got := access.Decodes() - before; got != 1 {
			t.Errorf("GET %s decoded permissions %d times, want 1", path, got)
		}
	}
}

// itemSocket is an authenticated socket counting item_updated events.
type itemSocket struct {
	client  *sioClient
	updated atomic.Int64
}

func connectUser(t *testing.T, in *Install, token string) *itemSocket {
	t.Helper()
	s := &itemSocket{}
	ready := make(chan struct{}, 1)
	client, err := dialSocketIO(in.BaseURL, journeySocketPath, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client.onEvent = func(name string, _ any) {
		switch name {
		case "init":
			ready <- struct{}{}
		case "item_updated":
			s.updated.Add(1)
		}
	}
	s.client = client
	t.Cleanup(client.close)
	if err := client.emit("auth", token); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("no init within 10s")
	}
	return s
}

// touch edits the novel as root and reports whether the socket got item_updated.
func (s *itemSocket) sees(t *testing.T, in *Install, root, title string) bool {
	t.Helper()
	before := s.updated.Load()
	path := "/api/items/" + in.IDs["novel"] + "/media"
	if resp, raw := in.Do(t, http.MethodPatch, path, root, map[string]any{"metadata": map[string]any{"title": title}}); resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH %s: %d %s", path, resp.StatusCode, raw)
	}
	time.Sleep(400 * time.Millisecond)
	return s.updated.Load() > before
}

func (s *itemSocket) disconnected() bool {
	select {
	case <-s.client.done:
		return true
	case <-time.After(3 * time.Second):
		return false
	}
}

// TestSocketFollowsUserUpdates: upstream keeps the user object loaded at socket
// auth (UserController.js:347 emits to the actor only), so a demoted user keeps
// receiving item events. Go refreshes the connection from the updated row.
func TestSocketFollowsUserUpdates(t *testing.T) {
	in := Boot(t)
	root := in.Login(t, in.Users["root"].Username, in.Users["root"].Password)
	id, token := createUser(t, in, root, "watcher")
	s := connectUser(t, in, token)
	patch := func(body map[string]any) {
		t.Helper()
		if resp, raw := in.Do(t, http.MethodPatch, "/api/users/"+id, root, body); resp.StatusCode != http.StatusOK {
			t.Fatalf("PATCH user: %d %s", resp.StatusCode, raw)
		}
	}
	if !s.sees(t, in, root, "one") {
		t.Fatal("baseline: socket should receive item_updated")
	}
	patch(map[string]any{"permissions": map[string]any{"accessAllLibraries": false}, "librariesAccessible": []string{"other-library"}})
	if s.sees(t, in, root, "two") {
		t.Error("library access removed: socket still receives the item")
	}
	patch(map[string]any{"permissions": map[string]any{"accessAllLibraries": true}})
	if !s.sees(t, in, root, "three") {
		t.Error("library access restored: socket should receive the item again")
	}
	patch(map[string]any{"permissions": map[string]any{"accessAllTags": false}, "itemTagsSelected": []string{"no-such-tag"}})
	if s.sees(t, in, root, "four") {
		t.Error("tag access removed: socket still receives the item")
	}
	patch(map[string]any{"permissions": map[string]any{"accessAllTags": true}})
	if !s.sees(t, in, root, "five") {
		t.Error("tag access restored: socket should receive the item again")
	}
	patch(map[string]any{"permissions": map[string]any{"accessExplicitContent": false}, "isActive": false})
	if !s.disconnected() {
		t.Error("deactivated user's socket stays connected")
	}
}

func TestSocketClosesWhenUserDeleted(t *testing.T) {
	in := Boot(t)
	root := in.Login(t, in.Users["root"].Username, in.Users["root"].Password)
	id, token := createUser(t, in, root, "doomed")
	s := connectUser(t, in, token)
	if resp, raw := in.Do(t, http.MethodDelete, "/api/users/"+id, root, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE user: %d %s", resp.StatusCode, raw)
	}
	if !s.disconnected() {
		t.Error("deleted user's socket stays connected")
	}
}
