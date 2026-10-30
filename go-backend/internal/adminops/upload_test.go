package adminops

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
)

func uploadRequest(t *testing.T, fields map[string]string, filename string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for k, v := range fields {
		_ = w.WriteField(k, v)
	}
	if filename != "" {
		p, err := w.CreateFormFile("file", filename)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = p.Write([]byte("upload data"))
	}
	_ = w.Close()
	r := httptest.NewRequest(http.MethodPost, "/api/upload", &body)
	r.Header.Set("Content-Type", w.FormDataContentType())
	return r
}

func TestUploadWritesSanitizedFileAndRejectsInvalidRequests(t *testing.T) {
	db := adminopsDB(t)
	root := t.TempDir()
	if _, err := db.Exec(`CREATE TABLE libraries(id TEXT PRIMARY KEY,mediaType TEXT); CREATE TABLE libraryFolders(id TEXT PRIMARY KEY,path TEXT,libraryId TEXT); INSERT INTO libraries VALUES('lib','book'); INSERT INTO libraryFolders VALUES('folder',?,'lib')`, root); err != nil {
		t.Fatal(err)
	}
	h := New(db, config.Config{}, nil)
	u := store.User{IsActive: true, Permissions: []byte(`{"upload":true,"accessAllLibraries":true}`)}
	w := httptest.NewRecorder()
	h.Handle(w, uploadRequest(t, map[string]string{"title": "Title", "library": "lib", "folder": "folder", "author": "A/B"}, "a:b?.mp3"), u)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(root, "AB", "Title", "a - b.mp3"))
	if err != nil || string(data) != "upload data" {
		t.Fatalf("file=%q err=%v", data, err)
	}

	for _, tc := range []struct {
		name   string
		req    *http.Request
		user   store.User
		status int
	}{
		{"denied", uploadRequest(t, map[string]string{}, "x"), store.User{IsActive: true}, 403},
		{"no file", uploadRequest(t, map[string]string{"title": "x"}, ""), u, 400},
		{"missing fields", uploadRequest(t, map[string]string{}, "x"), u, 400},
		{"bad library", uploadRequest(t, map[string]string{"title": "x", "library": "absent", "folder": "folder"}, "x"), u, 404},
		{"bad folder", uploadRequest(t, map[string]string{"title": "x", "library": "lib", "folder": "absent"}, "x"), u, 404},
		{"no library access", uploadRequest(t, map[string]string{"title": "x", "library": "lib", "folder": "folder"}, "x"), store.User{IsActive: true, Permissions: []byte(`{"upload":true}`)}, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := httptest.NewRecorder()
			h.Handle(got, tc.req, tc.user)
			if got.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", got.Code, tc.status, strings.TrimSpace(got.Body.String()))
			}
		})
	}
	bad := httptest.NewRecorder()
	h.Handle(bad, httptest.NewRequest(http.MethodPost, "/api/upload", strings.NewReader("not multipart")), u)
	if bad.Code != 400 {
		t.Fatalf("invalid multipart status=%d", bad.Code)
	}
}
