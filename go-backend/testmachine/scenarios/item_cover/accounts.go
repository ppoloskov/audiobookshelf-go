package item_cover

import (
	"net/http"
	"sync"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const accountPassword = "item-cover-password"

// account describes a user created through POST /api/users as scenario setup.
type account struct {
	Username    string
	Permissions map[string]any
	Libraries   []string
}

// setup creates the accounts and quiets the file watchers once per server.
type setup struct {
	mu     sync.Mutex
	ready  bool
	tokens map[string]string
}

// perms builds a complete permission object: every permission is false unless
// listed, so a test user never gains a capability by accident.
func (s *setup) perms(e *harness.Env, allowed ...string) map[string]any {
	out := map[string]any{
		"download": false, "update": false, "delete": false, "upload": false, "createEreader": false,
		"accessAllLibraries": true, "accessAllTags": true, "accessExplicitContent": true,
		"selectedTagsNotAccessible": false, "librariesAccessible": []string{}, "itemTagsSelected": []string{},
	}
	for _, name := range allowed {
		switch name {
		case "allLibraries":
			out["librariesAccessible"] = []string{}
		default:
			out[name] = true
		}
	}
	return out
}

// accountSpecs names the accounts this packet needs.
func (s *setup) accountSpecs(e *harness.Env) map[string]account {
	return map[string]account{
		"uploader": {Username: "ic-uploader", Permissions: s.perms(e, "update", "upload")},
		"noupload": {Username: "ic-noupload", Permissions: s.perms(e, "update")},
		"noupdate": {Username: "ic-noupdate", Permissions: s.perms(e, "upload")},
		"deleter":  {Username: "ic-deleter", Permissions: s.perms(e, "delete")},
		"fulluser": {Username: "ic-full", Permissions: s.perms(e, "update", "upload", "delete")},
		"podcastonly": {
			Username: "ic-podcastonly", Permissions: s.perms(e, "update", "upload", "delete"),
			Libraries: []string{e.IDs["podcastLibrary"]},
		},
	}
}

// token returns the access token of a named account, preparing the install on first use.
func (s *setup) token(t *testing.T, e *harness.Env, name string) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ready {
		s.prepare(t, e)
		s.ready = true
	}
	return s.tokens[s.accountSpecs(e)[name].Username]
}

// prepare creates the accounts and turns off both library watchers.
func (s *setup) prepare(t *testing.T, e *harness.Env) {
	t.Helper()
	s.quietWatchers(t, e)
	root := e.Token(t, "root")
	s.tokens = map[string]string{}
	for _, spec := range s.accountSpecs(e) {
		body := map[string]any{
			"username": spec.Username, "password": accountPassword, "type": "user",
			"isActive": true, "permissions": spec.Permissions,
		}
		if spec.Libraries != nil {
			body["librariesAccessible"] = spec.Libraries
			spec.Permissions["accessAllLibraries"] = false
		}
		wantStatus(t, "setup user "+spec.Username, e.Post(t, root, "/api/users", body), http.StatusOK)
		token, _ := e.Login(t, spec.Username, accountPassword)
		s.tokens[spec.Username] = token
	}
}

// quietWatchers disables the file watcher of both fixture libraries so that a
// cover write can never be followed by a rescan triggered behind the test.
func (s *setup) quietWatchers(t *testing.T, e *harness.Env) {
	t.Helper()
	for _, key := range []string{"library", "podcastLibrary"} {
		resp := e.Patch(t, e.Token(t, "root"), "/api/libraries/"+e.IDs[key], map[string]any{"settings": map[string]any{"disableWatcher": true}})
		wantStatus(t, "disable watcher "+key, resp, http.StatusOK)
	}
}
