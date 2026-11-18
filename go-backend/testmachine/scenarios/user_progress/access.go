package user_progress

import (
	"net/http"
	"sync"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// accessSet is the users and item state of the access rules.
type accessSet struct {
	restricted actor // only the podcast library
	tagIn      actor // only items tagged x
	tagOut     actor // everything except items tagged x
	noExplicit actor // no explicit content
}

var (
	accessMu    sync.Mutex
	accessByEnv = map[*harness.Env]accessSet{}
)

// accessUsers creates, once per server, users with each access rule and tags
// saga2 with "x" and marks the disposable item explicit (state no API call
// produces here; see README "Database"). The server restarts once.
func accessUsers(t *testing.T, e *harness.Env) accessSet {
	t.Helper()
	accessMu.Lock()
	defer accessMu.Unlock()
	if set, ok := accessByEnv[e]; ok {
		return set
	}
	set := accessSet{
		restricted: newActorWith(t, e, map[string]any{"type": "user", "permissions": map[string]any{"accessAllLibraries": false}, "librariesAccessible": []string{e.IDs["podcastLibrary"]}}),
		tagIn: newActorWith(t, e, map[string]any{"type": "user", "itemTagsSelected": []string{"x"},
			"permissions": map[string]any{"accessAllTags": false, "selectedTagsNotAccessible": false}}),
		tagOut: newActorWith(t, e, map[string]any{"type": "user", "itemTagsSelected": []string{"x"},
			"permissions": map[string]any{"accessAllTags": false, "selectedTagsNotAccessible": true}}),
		noExplicit: newActorWith(t, e, map[string]any{"type": "user", "permissions": map[string]any{"accessExplicitContent": false}}),
	}
	e.Stop(t)
	e.Exec(t, `UPDATE books SET tags = '["x"]' WHERE id = ?`, saga2Book)
	e.Exec(t, "UPDATE books SET explicit = 1 WHERE id = ?", explicitBk)
	e.Restart(t)
	accessByEnv[e] = set
	return set
}

// accessCase is one caller and item with the status a bookmark route answers.
type accessCase struct {
	what   string
	who    actor
	item   string // key of Env.IDs
	denied bool
}

// accessCases lists the library, tag (allow and deny mode) and explicit rules.
func accessCases(set accessSet) []accessCase {
	return []accessCase{
		{"library: user of the podcast library only, book item", set.restricted, "saga1", true},
		{"library: user of the podcast library only, podcast item", set.restricted, "podcast", false},
		{"tags allow list: item tagged x", set.tagIn, "saga2", false},
		{"tags allow list: untagged item", set.tagIn, "saga1", true},
		{"tags deny list: item tagged x", set.tagOut, "saga2", true},
		{"tags deny list: untagged item", set.tagOut, "saga1", false},
		{"explicit: user without explicit access, explicit item", set.noExplicit, "disposable", true},
		{"explicit: user without explicit access, other item", set.noExplicit, "saga1", false},
	}
}

// statusFor is the status a route answers for an access case when the request
// is otherwise accepted.
func (c accessCase) statusFor(allowed int) int {
	if c.denied {
		return http.StatusForbidden
	}
	return allowed
}
