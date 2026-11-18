package tools

import (
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

func wantStatus(t *testing.T, response harness.Response, status int) {
	t.Helper()
	if response.Status != status {
		t.Fatalf("HTTP status %d, want %d: %s", response.Status, status, response.Body)
	}
}

func createUser(t *testing.T, e *harness.Env, name, kind string, libraries []string) (id, token string) {
	t.Helper()
	const password = "embed-queue-scenario-password"
	request := struct {
		Username            string   `json:"username"`
		Password            string   `json:"password"`
		Type                string   `json:"type"`
		IsActive            bool     `json:"isActive"`
		LibrariesAccessible []string `json:"librariesAccessible"`
		Permissions         struct {
			AccessAllLibraries    bool `json:"accessAllLibraries"`
			AccessExplicitContent bool `json:"accessExplicitContent"`
		} `json:"permissions"`
	}{Username: name, Password: password, Type: kind, IsActive: true, LibrariesAccessible: libraries}
	request.Permissions.AccessAllLibraries = kind == "admin"
	request.Permissions.AccessExplicitContent = true
	response := e.Post(t, e.Token(t, "root"), "/api/users", request)
	wantStatus(t, response, 200)
	var body struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	response.JSON(t, &body)
	if body.User.ID == "" {
		t.Fatal("created user response has no ID")
	}
	token, _ = e.Login(t, name, password)
	return body.User.ID, token
}

func connectAudiences(t *testing.T, e *harness.Env, suffix string) *audience {
	t.Helper()
	_, admin := createUser(t, e, "embed-admin-"+suffix, "admin", []string{})
	_, restricted := createUser(t, e, "embed-restricted-"+suffix, "user", []string{e.IDs["podcastLibrary"]})
	revokedID, revoked := createUser(t, e, "embed-revoked-"+suffix, "user", []string{e.IDs["library"]})
	wantStatus(t, e.Get(t, revoked, "/api/items/"+e.IDs["saga1"]), 200)
	a := &audience{sockets: make(map[string]*harness.Socket), marks: make(map[string]int)}
	for name, token := range map[string]string{"root": e.Token(t, "root"), "admin": admin, "listener": e.Token(t, "listener"), "restricted": restricted, "revoked": revoked, "anonymous": ""} {
		a.sockets[name] = e.Socket(t, token)
	}
	// Revoke library access after the socket authenticated. Dequeue uses the
	// unfiltered all-authenticated emitter even for this connected user.
	wantStatus(t, e.Patch(t, e.Token(t, "root"), "/api/users/"+revokedID, struct {
		LibrariesAccessible []string `json:"librariesAccessible"`
		Permissions         struct {
			AccessAllLibraries bool `json:"accessAllLibraries"`
		} `json:"permissions"`
	}{LibrariesAccessible: []string{e.IDs["podcastLibrary"]}}), 200)
	for _, token := range []string{restricted, revoked} {
		wantStatus(t, e.Get(t, token, "/api/items/"+e.IDs["saga1"]), 403)
	}
	a.barrier(t)
	for name, socket := range a.sockets {
		a.marks[name] = socket.Mark()
	}
	return a
}
