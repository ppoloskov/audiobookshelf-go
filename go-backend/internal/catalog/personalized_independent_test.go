package catalog

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"testing"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
)

// INDEPENDENT EVIDENCE for the personalized shelves (the golden file is
// recorded from the code). Expectations are derived by hand from the rows in
// seedShelfGolden and the shelf rules in upstream
// LibraryItem.getPersonalizedLibraryItems / libraryItemsBookFilters.js:
// continue-listening = unfinished audio progress, not hidden, newest progress
// first; continue-reading = unfinished ebook-only progress; listen-again =
// finished audio books; permission filters apply to every shelf.
func personalizedShelves(t *testing.T, h *Handler, user store.User, path string) map[string][]string {
	t.Helper()
	w := httptest.NewRecorder()
	if !h.Handle(w, httptest.NewRequest("GET", path, nil), user) || w.Code != 200 {
		t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
	}
	var shelves []struct {
		ID       string `json:"id"`
		Entities []struct {
			ID string `json:"id"`
		} `json:"entities"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &shelves); err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, s := range shelves {
		ids := []string{}
		for _, e := range s.Entities {
			ids = append(ids, e.ID)
		}
		out[s.ID] = ids
	}
	return out
}

func TestPersonalizedShelvesFollowProgressAndPermissionRules(t *testing.T) {
	db := catalogFixture(t)
	defer db.Close()
	seedShelfGolden(t, db)
	h := New(db, config.Config{}, nil, testDeps())
	listener := store.User{ID: "listener", Type: "user", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessExplicitContent":false,"accessAllTags":false,"itemTagsSelected":["secret"],"selectedTagsNotAccessible":true}`)}
	root := store.User{ID: "root", Type: "root", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessExplicitContent":true,"accessAllTags":true}`)}

	l := personalizedShelves(t, h, listener, "/api/libraries/l1/personalized")
	// listener: b10 3s (02-06), b5 10s (02-03) unfinished audio; b4 ebook 50%; b1/b7 finished audio; b9 finished ebook.
	if want := []string{"i10", "i5"}; !slices.Equal(l["continue-listening"], want) {
		t.Errorf("continue-listening %v, want %v", l["continue-listening"], want)
	}
	if want := []string{"i4"}; !slices.Equal(l["continue-reading"], want) {
		t.Errorf("continue-reading %v, want %v", l["continue-reading"], want)
	}
	if want := []string{"i1"}; !slices.Equal(l["listen-again"], want) {
		t.Errorf("listen-again %v, want %v (i7 is finished but tagged secret)", l["listen-again"], want)
	}
	if want := []string{"i9"}; !slices.Equal(l["read-again"], want) {
		t.Errorf("read-again %v, want %v", l["read-again"], want)
	}
	// Invariant: no shelf shows the explicit book (i6) or the "secret"-tagged ones (i7) to this user.
	for id, ids := range l {
		for _, hidden := range []string{"i6", "i7"} {
			if slices.Contains(ids, hidden) {
				t.Errorf("shelf %s leaks %s to a user without access", id, hidden)
			}
		}
	}
	// root hid b5 from continue-listening and has b3 in progress.
	r := personalizedShelves(t, h, root, "/api/libraries/l1/personalized")
	if want := []string{"i3"}; !slices.Equal(r["continue-listening"], want) {
		t.Errorf("root continue-listening %v, want %v", r["continue-listening"], want)
	}
	if !slices.Contains(r["recently-added"], "i6") {
		t.Errorf("root sees explicit content: recently-added %v", r["recently-added"])
	}
	// Finished books are not "recently added" for the user who finished them (i1).
	if slices.Contains(r["recently-added"], "i1") {
		t.Errorf("recently-added %v lists an item old enough to be excluded", r["recently-added"])
	}
	// limit bounds every item shelf (recent series and newest authors have fixed sizes upstream: LibraryItem.js:387).
	for id, ids := range personalizedShelves(t, h, root, "/api/libraries/l1/personalized?limit=2") {
		if id != "recent-series" && id != "newest-authors" && len(ids) > 2 {
			t.Errorf("shelf %s has %d entities with limit=2", id, len(ids))
		}
	}
	// Podcast library: the listener's unfinished episode puts p1 in continue-listening.
	p := personalizedShelves(t, h, listener, "/api/libraries/l2/personalized")
	if want := []string{"ip1"}; !slices.Equal(p["continue-listening"], want) {
		t.Errorf("podcast continue-listening %v, want %v", p["continue-listening"], want)
	}
}
