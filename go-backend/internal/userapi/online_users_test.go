package userapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/identity"
	"audiobookshelf-compat/internal/store"
)

func TestOnlineUsersEncodesTypedOpenSessions(t *testing.T) {
	type metadata struct {
		Title string `json:"title"`
	}
	type chapter struct {
		Start float64 `json:"start"`
		Title string  `json:"title"`
	}
	type session struct {
		ID            string    `json:"id"`
		CurrentTime   float64   `json:"currentTime"`
		MediaMetadata *metadata `json:"mediaMetadata"`
		Chapters      []chapter `json:"chapters"`
	}
	type decodedSession struct {
		ID            string          `json:"id"`
		CurrentTime   float64         `json:"currentTime"`
		MediaMetadata json.RawMessage `json:"mediaMetadata"`
		Chapters      []chapter       `json:"chapters"`
	}

	db, user := userFixture(t)
	defer db.Close()
	ident, err := identity.New(db, config.Config{JWTSecret: "online-users-test-secret"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	h, err := New(db, ident, nil, Deps{OpenSessions: func() any {
		return []session{
			{ID: "session-one", CurrentTime: 12.5, MediaMetadata: &metadata{Title: "First"}, Chapters: []chapter{{Start: 1.25, Title: "Opening"}}},
			{ID: "session-two", CurrentTime: 0, MediaMetadata: nil, Chapters: []chapter{}},
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	h.Routes(mux)
	admin := user
	admin.Type = "root"
	request := func(u store.User) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/users/online", nil)
		req = req.WithContext(store.ContextWithUser(req.Context(), u))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	if got := request(user); got.Code != http.StatusForbidden {
		t.Fatalf("reader status = %d, want 403", got.Code)
	}
	w := request(admin)
	if w.Code != http.StatusOK {
		t.Fatalf("admin status = %d body=%s, want 200", w.Code, w.Body.String())
	}
	var got struct {
		UsersOnline  json.RawMessage  `json:"usersOnline"`
		OpenSessions []decodedSession `json:"openSessions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.OpenSessions) != 2 {
		t.Fatalf("openSessions length = %d, want 2", len(got.OpenSessions))
	}
	first, second := got.OpenSessions[0], got.OpenSessions[1]
	var firstMetadata metadata
	if err := json.Unmarshal(first.MediaMetadata, &firstMetadata); err != nil {
		t.Fatalf("first mediaMetadata = %s: %v", first.MediaMetadata, err)
	}
	if first.ID != "session-one" || first.CurrentTime != 12.5 || firstMetadata.Title != "First" ||
		len(first.Chapters) != 1 || first.Chapters[0] != (chapter{Start: 1.25, Title: "Opening"}) {
		t.Fatalf("first open session = %+v, want typed fields and chapter", first)
	}
	if second.ID != "session-two" || second.CurrentTime != 0 || string(second.MediaMetadata) != "null" || second.Chapters == nil || len(second.Chapters) != 0 {
		t.Fatalf("second open session = %+v, want zero time, null metadata, empty chapters", second)
	}
	if string(got.UsersOnline) != "[]" {
		t.Fatalf("usersOnline = %s, want []", got.UsersOnline)
	}
}

func TestOnlineUsersEmptyOpenSessionsRemainsArray(t *testing.T) {
	db, user := userFixture(t)
	defer db.Close()
	ident, err := identity.New(db, config.Config{JWTSecret: "online-users-test-secret"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h, err := New(db, ident, nil, Deps{OpenSessions: func() any { return []struct{}{} }})
	if err != nil {
		t.Fatal(err)
	}
	h.Routes(mux)
	user.Type = "root"
	req := httptest.NewRequest(http.MethodGet, "/api/users/online", nil)
	req = req.WithContext(store.ContextWithUser(req.Context(), user))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", w.Code, w.Body.String())
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if string(got["openSessions"]) != "[]" {
		t.Fatalf("openSessions = %s, want []", got["openSessions"])
	}
}
