package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/people"
	"audiobookshelf-compat/internal/store"
)

type recordedEvent struct {
	event   string
	payload any
}

// authorRig is the catalog integration fixture; author/series entity tests
// live in internal/people.
type authorRig struct {
	t       *testing.T
	db      *sql.DB
	h       *Handler
	mux     *http.ServeMux
	meta    string
	mu      sync.Mutex
	events  []recordedEvent
	touched []string
}

func newAuthorRig(t *testing.T, finder people.AuthorFinder) *authorRig {
	t.Helper()
	db := catalogFixture(t)
	t.Cleanup(func() { db.Close() })
	rig := &authorRig{t: t, db: db, meta: t.TempDir()}
	emit := func(_, event string, payload any) {
		rig.mu.Lock()
		defer rig.mu.Unlock()
		rig.events = append(rig.events, recordedEvent{event, payload})
	}
	writeMetadata := func(_ context.Context, itemID string) error {
		rig.touched = append(rig.touched, itemID)
		return nil
	}
	rig.h = New(db, config.Config{MetadataPath: rig.meta}, emit, Deps{Filters: filterdata.NewCache()})
	rig.h.deps.ItemChanged = writeMetadata
	rig.mux = http.NewServeMux()
	rig.h.Routes(rig.mux)
	people.New(db, config.Config{MetadataPath: rig.meta}, emit, people.Deps{
		Filters: filterdata.NewCache(), Authors: finder, WriteMetadata: writeMetadata,
		SeriesMatchesFilter: rig.h.SeriesMatchesFilter,
	}).Routes(rig.mux)
	for _, query := range []string{
		`INSERT INTO authors VALUES ('a2','Ben Writer','Writer, Ben',NULL,NULL,NULL,'l1','2024-01-03T00:00:00Z','2024-01-03T00:00:00Z')`,
		`INSERT INTO books VALUES ('b2','Second',NULL,NULL,'2025',NULL,NULL,NULL,NULL,NULL,'en',0,0,NULL,5,'[]','[]','null','[]','[]','[]','2024-01-02T00:00:00Z','2024-01-02T00:00:00Z')`,
		`INSERT INTO libraryItems VALUES ('i2','ino-2','/media/books/Second','Second','b2','book',0,0,0,'2024-01-01T00:00:00Z','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z',10,NULL,NULL,'[]','{}','Second','Second','Ben Writer','Writer, Ben','l1','f1','2024-01-02T00:00:00Z','2024-01-02T00:00:00Z')`,
		`INSERT INTO bookAuthors VALUES ('ba2','b2','a2','2024-01-02T00:00:00Z')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	return rig
}

func (r *authorRig) do(method, target, body string, user store.User) *httptest.ResponseRecorder {
	r.t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req = req.WithContext(store.ContextWithUser(req.Context(), user))
	w := httptest.NewRecorder()
	r.mux.ServeHTTP(w, req)
	return w
}

func (r *authorRig) takeEvents() []recordedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.events
	r.events = nil
	return out
}

func rootUser() store.User {
	return store.User{ID: "u1", Username: "root", Type: "root", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true,"update":true,"delete":true,"upload":true}`)}
}

func readOnlyUser() store.User {
	return store.User{ID: "u2", Username: "reader", Type: "user", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true}`)}
}

func decodeAuthorBody(t *testing.T, w *httptest.ResponseRecorder, into any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), into); err != nil {
		t.Fatalf("body %q: %v", w.Body.String(), err)
	}
}
