package catalog

import (
	"context"
	"testing"
	"time"

	"audiobookshelf-compat/internal/item"
)

// Match-all is admin-only, answers at once and quick matches every book of the
// library through Deps.QuickMatch, then finishes its task.
func TestLibraryMatchAllRunsQuickMatchPerBook(t *testing.T) {
	rig := newAuthorRig(t, nil)
	var matched []string
	updated := true
	rig.h.deps.QuickMatch = func(_ context.Context, id string, opts item.MatchOptions) (item.MatchResult, error) {
		matched = append(matched, id+"/"+opts.Provider)
		return item.MatchResult{Updated: &updated}, nil
	}
	if w := rig.do("GET", "/api/libraries/l1/matchall", "", readOnlyUser()); w.Code != 403 {
		t.Fatalf("non-admin %d", w.Code)
	}
	if w := rig.do("GET", "/api/libraries/l1/matchall", "", rootUser()); w.Code != 200 {
		t.Fatalf("admin %d", w.Code)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		finished := false
		rig.mu.Lock()
		for _, e := range rig.events {
			finished = finished || e.event == "task_finished"
		}
		rig.mu.Unlock()
		if finished {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("task did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(matched) != 2 || matched[0] != "i1/google" || matched[1] != "i2/google" {
		t.Fatalf("matched %v", matched)
	}
}
