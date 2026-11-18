package item_write

import (
	"net/http"
	"sync"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const accountPassword = "item-write-password"

// restrictedTag is put on the ebook item for the tag-rule accounts.
const restrictedTag = "iw-restricted"

// account describes a user created through POST /api/users as scenario setup.
type account struct {
	Username  string
	Type      string
	Perms     map[string]bool
	Libraries []string
	Tags      []string
}

// setup creates the accounts and quiets the file watchers once per server.
type setup struct {
	mu     sync.Mutex
	ready  bool
	tokens map[string]string
}

func accountSpecs(e *harness.Env) map[string]account {
	all := func(extra map[string]bool) map[string]bool {
		perms := map[string]bool{"update": true, "delete": true, "upload": true, "download": true, "accessExplicitContent": true}
		for key, value := range extra {
			perms[key] = value
		}
		return perms
	}
	return map[string]account{
		"updater":     {Username: "iw-updater", Type: "user", Perms: map[string]bool{"update": true, "delete": false}},
		"deleter":     {Username: "iw-deleter", Type: "user", Perms: map[string]bool{"update": false, "delete": true}},
		"noupload":    {Username: "iw-noupload", Type: "user", Perms: all(map[string]bool{"upload": false})},
		"podcastonly": {Username: "iw-podcastonly", Type: "user", Perms: all(map[string]bool{"accessAllLibraries": false}), Libraries: []string{e.IDs["podcastLibrary"]}},
		"tagallow":    {Username: "iw-tagallow", Type: "user", Perms: all(map[string]bool{"accessAllTags": false}), Tags: []string{restrictedTag}},
		"tagdeny":     {Username: "iw-tagdeny", Type: "user", Perms: all(map[string]bool{"accessAllTags": false, "selectedTagsNotAccessible": true}), Tags: []string{restrictedTag}},
		"noexplicit":  {Username: "iw-noexplicit", Type: "user", Perms: all(map[string]bool{"accessExplicitContent": false})},
		"plainadmin":  {Username: "iw-admin", Type: "admin"},
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
	return s.tokens[accountSpecs(e)[name].Username]
}

func (s *setup) prepare(t *testing.T, e *harness.Env) {
	t.Helper()
	quietWatchers(t, e)
	root := e.Token(t, "root")
	wantStatus(t, "setup tag", patchMedia(t, e, root, e.IDs["ebook"], map[string]any{"tags": []string{restrictedTag}}), http.StatusOK)
	s.tokens = map[string]string{}
	for _, spec := range accountSpecs(e) {
		perms := map[string]any{}
		for key, value := range spec.Perms {
			perms[key] = value
		}
		body := map[string]any{"username": spec.Username, "password": accountPassword, "type": spec.Type, "isActive": true, "permissions": perms}
		if spec.Libraries != nil {
			body["librariesAccessible"] = spec.Libraries
		}
		if spec.Tags != nil {
			body["itemTagsSelected"] = spec.Tags
		}
		wantStatus(t, "setup user "+spec.Username, e.Post(t, root, "/api/users", body), http.StatusOK)
		s.tokens[spec.Username], _ = e.Login(t, spec.Username, accountPassword)
	}
}

// quietWatchers disables the file watcher of both fixture libraries so that file
// changes of a scenario are never rescanned behind its back.
func quietWatchers(t *testing.T, e *harness.Env) {
	t.Helper()
	for _, key := range []string{"library", "podcastLibrary"} {
		resp := e.Patch(t, e.Token(t, "root"), "/api/libraries/"+e.IDs[key], map[string]any{"settings": map[string]any{"disableWatcher": true}})
		wantStatus(t, "disable watcher", resp, http.StatusOK)
	}
}
