package item_read

import (
	"net/http"
	"sync"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// account describes a user created through POST /api/users as scenario setup.
type account struct {
	Username  string
	Type      string
	Perms     map[string]bool
	Libraries []string // librariesAccessible
	Tags      []string // itemTagsSelected
}

const accountPassword = "item-read-password"

// setup creates users and prepares the items they are restricted against, once
// per packet run. It is owned by the packet that builds it in Packet().
type setup struct {
	mu     sync.Mutex
	tokens map[string]string // username to access token
	ready  bool
}

// restrictedTag is put on the ebook item; explicitItem is flagged explicit.
const restrictedTag = "ir-restricted"

func accountSpecs(e *harness.Env) map[string]account {
	return map[string]account{
		"nodownload":   {Username: "ir-nodownload", Type: "user", Perms: map[string]bool{"download": false}},
		"plain":        {Username: "ir-plain", Type: "user"},
		"admin":        {Username: "ir-admin", Type: "admin"},
		"podcastonly":  {Username: "ir-podcastonly", Type: "user", Perms: map[string]bool{"accessAllLibraries": false, "accessExplicitContent": true}, Libraries: []string{e.IDs["podcastLibrary"]}},
		"limitedadmin": {Username: "ir-limitedadmin", Type: "admin", Perms: map[string]bool{"accessAllLibraries": false}, Libraries: []string{e.IDs["podcastLibrary"]}},
		"tagallow":     {Username: "ir-tagallow", Type: "user", Perms: map[string]bool{"accessAllTags": false, "accessExplicitContent": true}, Tags: []string{restrictedTag}},
		"tagdeny":      {Username: "ir-tagdeny", Type: "user", Perms: map[string]bool{"accessAllTags": false, "selectedTagsNotAccessible": true, "accessExplicitContent": true}, Tags: []string{restrictedTag}},
	}
}

// token returns the access token of a named account, creating all accounts
// and tagging/flagging the restricted items on first use.
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
	root := e.Token(t, "root")
	quietWatcher(t, e)
	for _, target := range []struct {
		item string
		body map[string]any
	}{
		{e.IDs["ebook"], map[string]any{"tags": []string{restrictedTag}}},
		{e.IDs["disposable"], map[string]any{"metadata": map[string]any{"explicit": true}}},
	} {
		resp := e.Patch(t, root, "/api/items/"+target.item+"/media", target.body)
		wantStatus(t, "setup PATCH media", resp, http.StatusOK)
	}
	s.tokens = map[string]string{}
	for _, spec := range accountSpecs(e) {
		perms := map[string]any{}
		for key, value := range spec.Perms {
			perms[key] = value
		}
		body := map[string]any{
			"username": spec.Username, "password": accountPassword, "type": spec.Type, "isActive": true,
			"permissions": perms,
		}
		if spec.Libraries != nil {
			body["librariesAccessible"] = spec.Libraries
		}
		if spec.Tags != nil {
			body["itemTagsSelected"] = spec.Tags
		}
		wantStatus(t, "setup POST users "+spec.Username, e.Post(t, root, "/api/users", body), http.StatusOK)
		s.tokens[spec.Username], _ = e.Login(t, spec.Username, accountPassword)
	}
}
