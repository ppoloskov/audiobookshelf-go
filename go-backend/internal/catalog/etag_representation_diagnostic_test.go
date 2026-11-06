//go:build tsdiagnostic

package catalog

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/store"
)

func TestTSRepresentationR01Catalog(t *testing.T) {
	response := httptest.NewRecorder()
	writeJSONWithETag(response, httptest.NewRequest(http.MethodGet, "/api/authors/a1", nil), http.StatusOK, map[string]string{"cover": "/metadata/<id&>/cover.png"})
	if got, want := response.Header().Get("ETag"), httputil.ExpressETag(response.Body.Bytes()); got != want {
		t.Fatalf("JSON ETag = %q, want Express %q", got, want)
	}
	db := catalogFixture(t)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE podcasts (id TEXT PRIMARY KEY, title TEXT)`); err != nil {
		t.Fatal(err)
	}
	h := New(db, config.Config{}, nil)
	user := store.User{ID: "u1", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"download":false}`)}
	for _, route := range []string{"/api/items/missing", "/api/items/i1/download", "/api/libraries/l1/items?page=-1"} {
		recorder := httptest.NewRecorder()
		if !h.Handle(recorder, httptest.NewRequest(http.MethodGet, route, nil), user) {
			t.Fatalf("unhandled diagnostic route %s", route)
		}
		if got, want := recorder.Header().Get("ETag"), httputil.ExpressETag(recorder.Body.Bytes()); got != want {
			t.Errorf("%s ETag = %q, want Express %q", route, got, want)
		}
	}
	file := filepath.Join(t.TempDir(), "audio.mp3")
	if err := os.WriteFile(file, []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/file", nil)
	request.Header.Set("Range", "bytes=2-4")
	h.servePath(recorder, request, file)
	want := `W/"` + strconv.FormatInt(info.Size(), 16) + `-` + strconv.FormatInt(info.ModTime().Round(time.Millisecond).UnixMilli(), 16) + `"`
	if got := recorder.Header().Get("ETag"); got != want {
		t.Fatalf("file ETag = %q, want Express %q", got, want)
	}
}

func TestTSRepresentationR04Range(t *testing.T) {
	file := filepath.Join(t.TempDir(), "audio.mp3")
	if err := os.WriteFile(file, []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/file", nil)
	request.Header.Set("Range", "bytes=999999999-")
	(&Handler{}).servePath(response, request, file)
	const want = "<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n<title>Error</title>\n</head>\n<body>\n<pre>Range Not Satisfiable</pre>\n</body>\n</html>\n"
	if response.Body.String() != want {
		t.Fatalf("416 finalhandler bytes = %q, want %q", response.Body.String(), want)
	}
}
