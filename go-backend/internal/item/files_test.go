package item

import (
	"archive/zip"
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/auth"
)

var (
	uploaderUser = user("u1", "root", `{"accessAllLibraries":true,"accessAllTags":true,"update":true,"upload":true,"download":true}`)
	noRightsUser = user("u9", "user", `{"accessAllLibraries":true,"accessAllTags":true}`)
	otherLibUser = user("u8", "user", `{"librariesAccessible":["lp"],"accessAllTags":true,"update":true,"upload":true,"download":true}`)
)

// libraryFileJSON is a libraryFiles entry (with the audio fields books.audioFiles also reads).
func libraryFileJSON(ino, path string) string {
	return fmt.Sprintf(`{"ino":%q,"metadata":{"filename":%q,"ext":%q,"path":%q,"relPath":%q,"size":10},"addedAt":1,"updatedAt":2,"fileType":"audio","isSupplementary":null}`,
		ino, filepath.Base(path), filepath.Ext(path), path, filepath.Base(path))
}

// mount puts the files into the library folder of i1: the audio file audio-1
// (also its only audio track) and any extra libraryFiles entries.
func (r *rig) mount(extra ...string) (dir string) {
	r.t.Helper()
	dir = r.t.TempDir()
	book := filepath.Join(dir, "The Book")
	audio := filepath.Join(book, "01.mp3")
	mustWrite(r.t, audio, "0123456789")
	files := append([]string{libraryFileJSON("audio-1", audio)}, extra...)
	r.exec(`UPDATE libraryFolders SET path=? WHERE id='f1'`, dir)
	r.exec(`UPDATE libraryItems SET path=?, libraryFiles=? WHERE id='i1'`, book, "["+strings.Join(files, ",")+"]")
	r.exec(`UPDATE books SET audioFiles=? WHERE id='b1'`, "["+strings.Replace(libraryFileJSON("audio-1", audio), `"fileType":"audio",`, `"index":1,"duration":12.5,`, 1)+"]")
	r.takeEvents()
	return book
}

func (r *rig) request(u *auth.Principal, method, target, ctype, body string, header ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req.WithContext(auth.WithUser(req.Context(), u)))
	return rec
}

func TestServeFileHonoursRangeLibraryAndPermissions(t *testing.T) {
	r := newRig(t)
	book := r.mount()
	rec := r.request(readerUser, "GET", "/api/items/i1/file/audio-1", "", "", "Range", "bytes=2-4")
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "234" || rec.Header().Get("Content-Range") != "bytes 2-4/10" || rec.Header().Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("range: %d %v %q", rec.Code, rec.Header(), rec.Body)
	}
	if tag := rec.Header().Get("ETag"); tag == "" || r.request(readerUser, "GET", "/api/items/i1/file/audio-1", "", "", "If-None-Match", tag).Code != http.StatusNotModified {
		t.Fatalf("a matching If-None-Match must answer 304, ETag %q", tag)
	}
	if rec := r.request(readerUser, "GET", "/api/items/i1/file/nope", "", ""); rec.Code != 404 {
		t.Fatalf("unknown inode: %d", rec.Code)
	}
	if rec := r.request(otherLibUser, "GET", "/api/items/i1/file/audio-1", "", ""); rec.Code != 403 {
		t.Fatalf("other library: %d", rec.Code)
	}
	// A file outside the library folders is never served.
	outside := filepath.Join(t.TempDir(), "secret.mp3")
	mustWrite(t, outside, "secret")
	r.exec(`UPDATE libraryItems SET libraryFiles=? WHERE id='i1'`, "["+libraryFileJSON("escape", outside)+"]")
	if rec := r.request(rootUser, "GET", "/api/items/i1/file/escape", "", ""); rec.Code != 404 {
		t.Fatalf("outside file: %d %q", rec.Code, rec.Body)
	}
	_ = book
}

func TestDownloadFileNeedsDownloadAfterFindingFile(t *testing.T) {
	r := newRig(t)
	r.mount()
	if rec := r.request(noRightsUser, "GET", "/api/items/i1/file/nope/download", "", ""); rec.Code != 404 {
		t.Fatalf("unknown file before permission: %d", rec.Code)
	}
	if rec := r.request(noRightsUser, "GET", "/api/items/i1/file/audio-1/download", "", ""); rec.Code != 403 {
		t.Fatalf("no download permission: %d", rec.Code)
	}
	rec := r.request(uploaderUser, "GET", "/api/items/i1/file/audio-1/download", "", "")
	if rec.Code != 200 || rec.Body.String() != "0123456789" || rec.Header().Get("Content-Disposition") != `attachment; filename=01.mp3` {
		t.Fatalf("download: %d %v %q", rec.Code, rec.Header(), rec.Body)
	}
}

func TestDownloadItemStreamsAZipOfTheFolder(t *testing.T) {
	r := newRig(t)
	book := r.mount()
	mustWrite(t, filepath.Join(book, "notes", "a.txt"), "hello")
	rec := r.request(uploaderUser, "GET", "/api/items/i1/download", "", "")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/zip" || rec.Header().Get("Content-Disposition") != `attachment; filename="The Book.zip"` {
		t.Fatalf("zip: %d %v", rec.Code, rec.Header())
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
		if f.Method != zip.Store {
			t.Errorf("%s is compressed", f.Name)
		}
	}
	if got := strings.Join(names, ","); got != "01.mp3,notes/a.txt" {
		t.Fatalf("entries %s", got)
	}
	if rec := r.request(noRightsUser, "GET", "/api/items/i1/download", "", ""); rec.Code != 403 {
		t.Fatalf("no download permission: %d", rec.Code)
	}
	if rec := r.request(otherLibUser, "GET", "/api/items/i1/download", "", ""); rec.Code != 403 {
		t.Fatalf("other library: %d", rec.Code)
	}
	if err := os.RemoveAll(book); err != nil {
		t.Fatal(err)
	}
	if rec := r.request(uploaderUser, "GET", "/api/items/i1/download", "", ""); rec.Code != 404 {
		t.Fatalf("missing folder: %d", rec.Code)
	}
}

func TestCoverRawIsPublicAndCached(t *testing.T) {
	r := newRig(t)
	dir := t.TempDir()
	cover := filepath.Join(dir, "cover.png")
	mustWrite(t, cover, "raw-cover-bytes")
	r.exec(`UPDATE libraryFolders SET path=? WHERE id='f1'`, dir)
	r.exec(`UPDATE books SET coverPath=? WHERE id='b1'`, cover)
	rec := r.do(nil, "GET", "/api/items/i1/cover?raw=1&ts=123", "")
	if rec.Code != 200 || rec.Body.String() != "raw-cover-bytes" || rec.Header().Get("Cache-Control") != "private, max-age=86400" || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("raw cover: %d %v %q", rec.Code, rec.Header(), rec.Body)
	}
	if rec := r.do(nil, "GET", "/api/items/i1/cover?raw=1", ""); rec.Header().Get("Cache-Control") != "public, max-age=0" {
		t.Fatalf("Cache-Control %q", rec.Header().Get("Cache-Control"))
	}
	if rec := r.do(nil, "GET", "/api/items/i1/cover?format=gif", ""); rec.Code != 400 {
		t.Fatalf("unsupported format: %d", rec.Code)
	}
	if rec := r.do(nil, "GET", "/api/items/i2/cover?raw=1", ""); rec.Code != 404 {
		t.Fatalf("item without cover: %d", rec.Code)
	}
	r.exec(`UPDATE books SET coverPath='/etc/hosts' WHERE id='b1'`)
	if rec := r.do(nil, "GET", "/api/items/i1/cover?raw=1", ""); rec.Code != 404 {
		t.Fatalf("cover outside library and metadata: %d", rec.Code)
	}
}

func coverForm(t *testing.T, filename, content string) (string, string) {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("cover", filename)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte(content))
	_ = form.Close()
	return form.FormDataContentType(), body.String()
}

// Uploading a cover replaces the item's earlier cover.* images but touches
// nothing else in the folder (CoverManager.removeOldCovers removes only an
// explicit list of image extensions).
func TestUploadCoverRemovesOnlyOlderCoverImages(t *testing.T) {
	r := newRig(t)
	r.mount()
	dir := filepath.Join(r.metaDir, "i1")
	for _, name := range []string{"cover.svg", "cover.foo", "cover.png", "cover.JIFF", "other.jpg", "COVER.webp", "cover.jpg"} {
		mustWrite(t, filepath.Join(dir, name), name)
	}
	mustWrite(t, filepath.Join(r.cacheDir, "i1_400.jpeg"), "cached")
	ctype, body := coverForm(t, "new.jpg", "\xff\xd8\xffnew-cover")
	rec := r.request(uploaderUser, "POST", "/api/items/i1/cover", ctype, body)
	want := filepath.Join(dir, "cover.jpg")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"cover":`+fmt.Sprintf("%q", want)) {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	if got, err := os.ReadFile(want); err != nil || string(got) != "\xff\xd8\xffnew-cover" {
		t.Errorf("cover.jpg = %q, %v", got, err)
	}
	for _, name := range []string{"cover.svg", "cover.foo", "other.jpg"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("the upload removed unrelated file %q: %v", name, err)
		}
	}
	for _, name := range []string{"cover.png", "cover.JIFF", "COVER.webp"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("the old image %q survived, stat error = %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(r.cacheDir, "i1_400.jpeg")); !os.IsNotExist(err) {
		t.Errorf("the cached resize survived: %v", err)
	}
	if got := r.values(`SELECT coverPath FROM books WHERE id='b1'`); got != want {
		t.Errorf("coverPath %q", got)
	}
	if got := r.takeEventNames(); got != "item_updated" {
		t.Errorf("events %q", got)
	}
}

func TestUploadCoverChecksPermissionsAndInput(t *testing.T) {
	r := newRig(t)
	r.mount()
	jpg, jpgBody := coverForm(t, "new.jpg", "\xff\xd8\xff")
	svg, svgBody := coverForm(t, "new.svg", "<svg/>")
	for name, c := range map[string]struct {
		user         *auth.Principal
		target, kind string
		body         string
		status       int
		message      string
	}{
		"no upload permission": {readerUser, "/api/items/i1/cover", jpg, jpgBody, 403, ""},
		"unknown item":         {uploaderUser, "/api/items/zz/cover", jpg, jpgBody, 404, ""},
		"other library":        {otherLibUser, "/api/items/i1/cover", jpg, jpgBody, 403, ""},
		"unsupported type":     {uploaderUser, "/api/items/i1/cover", svg, svgBody, 400, "Invalid image type .svg (Supported: png,jpg,jpeg,webp)"},
		"no file or url":       {uploaderUser, "/api/items/i1/cover", "application/json", `{}`, 400, "Invalid request no file or url"},
		"malformed body":       {uploaderUser, "/api/items/i1/cover", "application/json", `{`, 400, "Invalid request no file or url"},
		"private url":          {uploaderUser, "/api/items/i1/cover", "application/json", `{"url":"http://127.0.0.1:1/c.jpg"}`, 400, "Failed to download image from url"},
		"not a url":            {uploaderUser, "/api/items/i1/cover", "application/json", `{"url":"ftp://x/c.jpg"}`, 400, "Failed to download image from url"},
	} {
		rec := r.request(c.user, "POST", c.target, c.kind, c.body)
		if rec.Code != c.status || (c.message != "" && rec.Body.String() != c.message) {
			t.Errorf("%s: %d %q", name, rec.Code, rec.Body)
		}
	}
}

func TestChooseCoverCopiesALibraryImage(t *testing.T) {
	r := newRig(t)
	book := r.mount()
	jpg, txt := filepath.Join(book, "front.jpg"), filepath.Join(book, "readme.txt")
	mustWrite(t, jpg, "\xff\xd8\xff\xe0 image")
	mustWrite(t, txt, "plain text")
	r.exec(`UPDATE libraryItems SET libraryFiles=? WHERE id='i1'`, "["+libraryFileJSON("img", jpg)+","+libraryFileJSON("txt", txt)+"]")
	patch := func(u *auth.Principal, cover string) *httptest.ResponseRecorder {
		return r.request(u, "PATCH", "/api/items/i1/cover", "application/json", fmt.Sprintf(`{"cover":%q}`, cover))
	}
	for name, c := range map[string]struct {
		user   *auth.Principal
		cover  string
		status int
		text   string
	}{
		"read only":     {readerUser, jpg, 403, ""},
		"not a path":    {rootUser, "", 400, "Invalid request no cover path"},
		"not our file":  {rootUser, "/etc/hosts", 500, "Invalid cover path"},
		"a url":         {rootUser, "https://example.test/c.jpg", 500, "Invalid cover path"},
		"not an image":  {rootUser, txt, 500, "Invalid image"},
		"missing":       {rootUser, filepath.Join(book, "gone.jpg"), 500, "Invalid cover path"},
		"other library": {podUser, jpg, 403, ""},
	} {
		if rec := patch(c.user, c.cover); rec.Code != c.status || (c.text != "" && rec.Body.String() != c.text) {
			t.Errorf("%s: %d %q", name, rec.Code, rec.Body)
		}
	}
	rec := patch(rootUser, jpg)
	stored := filepath.Join(r.metaDir, "i1", "cover.jpg")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), stored) {
		t.Fatalf("choose: %d %s", rec.Code, rec.Body)
	}
	if got := r.values(`SELECT coverPath FROM books WHERE id='b1'`); got != stored {
		t.Errorf("coverPath %q", got)
	}
	r.takeEvents()
	if rec := patch(rootUser, stored); rec.Code != 500 { // not one of the item's library files
		t.Errorf("a stored copy is not a library file: %d", rec.Code)
	}
	// Removing the cover keeps the file and announces once.
	if rec := r.request(rootUser, "DELETE", "/api/items/i1/cover", "", ""); rec.Code != 200 {
		t.Fatalf("remove: %d", rec.Code)
	}
	if rec := r.request(rootUser, "DELETE", "/api/items/i1/cover", "", ""); rec.Code != 200 {
		t.Fatalf("remove again: %d", rec.Code)
	}
	if _, err := os.Stat(stored); err != nil {
		t.Errorf("remove deleted the file: %v", err)
	}
	if got, names := r.values(`SELECT COALESCE(coverPath,'') FROM books WHERE id='b1'`), r.takeEventNames(); got != "" || names != "item_updated" {
		t.Errorf("coverPath %q events %q", got, names)
	}
	if rec := r.request(readerUser, "DELETE", "/api/items/i1/cover", "", ""); rec.Code != 403 {
		t.Errorf("read only remove: %d", rec.Code)
	}
}

func TestChapters(t *testing.T) {
	r := newRig(t)
	r.mount()
	post := func(u *auth.Principal, id, body string) *httptest.ResponseRecorder {
		return r.request(u, "POST", "/api/items/"+id+"/chapters", "application/json", body)
	}
	body := `{"chapters":[{"title":"Opening","start":0,"end":12.5}]}`
	if rec := post(rootUser, "i1", body); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"success":true,"updated":true}` {
		t.Fatalf("chapters: %d %s", rec.Code, rec.Body)
	}
	if got := r.values(`SELECT chapters FROM books WHERE id='b1'`); got != `[{"id":0,"start":0,"end":12.5,"title":"Opening"}]` {
		t.Fatalf("stored chapters %s", got)
	}
	if got := r.takeEventNames(); got != "item_updated" || len(r.touched) != 1 {
		t.Errorf("events %q, metadata writes %v", got, r.touched)
	}
	if rec := post(rootUser, "i1", body); !strings.Contains(rec.Body.String(), `"updated":false`) {
		t.Errorf("same chapters: %s", rec.Body)
	}
	for name, c := range map[string]struct {
		user   *auth.Principal
		id     string
		body   string
		status int
	}{
		"read only":     {readerUser, "i1", body, 403},
		"unknown item":  {rootUser, "zz", body, 404},
		"podcast":       {rootUser, "ip", body, 500},
		"no audio":      {rootUser, "i2", body, 500},
		"null":          {rootUser, "i1", `{"chapters":null}`, 400},
		"absent":        {rootUser, "i1", `{}`, 400},
		"not a list":    {rootUser, "i1", `{"chapters":{}}`, 400},
		"untitled":      {rootUser, "i1", `{"chapters":[{"title":"","start":0,"end":1}]}`, 400},
		"malformed":     {rootUser, "i1", `{`, 400},
		"empty list ok": {rootUser, "i1", `{"chapters":[]}`, 200},
	} {
		if rec := post(c.user, c.id, c.body); rec.Code != c.status {
			t.Errorf("%s: %d %q", name, rec.Code, rec.Body)
		}
	}
	if got := r.values(`SELECT chapters FROM books WHERE id='b1'`); got != `[]` {
		t.Errorf("an empty list clears the chapters, stored %s", got)
	}
}

func TestTracksReplaceAndOrderTheAudioFiles(t *testing.T) {
	r := newRig(t)
	r.mount()
	patch := func(u *auth.Principal, id, body string) *httptest.ResponseRecorder {
		return r.request(u, "PATCH", "/api/items/"+id+"/tracks", "application/json", body)
	}
	for name, c := range map[string]struct {
		user   *auth.Principal
		id     string
		body   string
		status int
	}{
		"read only":   {readerUser, "i1", `{"orderedFileData":[{"ino":"audio-1"}]}`, 403},
		"podcast":     {rootUser, "ip", `{"orderedFileData":[{"ino":"audio-1"}]}`, 400},
		"empty":       {rootUser, "i1", `{"orderedFileData":[]}`, 400},
		"unknown ino": {rootUser, "i1", `{"orderedFileData":[{"ino":"nope"}]}`, 400},
		"malformed":   {rootUser, "i1", `[`, 400},
	} {
		if rec := patch(c.user, c.id, c.body); rec.Code != c.status {
			t.Errorf("%s: %d %q", name, rec.Code, rec.Body)
		}
	}
	rec := patch(rootUser, "i1", `{"orderedFileData":[{"ino":"audio-1","exclude":true}]}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"libraryFiles"`) {
		t.Fatalf("tracks: %d %s", rec.Code, rec.Body)
	}
	if got := r.values(`SELECT json_extract(audioFiles,'$[0].exclude')||'/'||json_extract(audioFiles,'$[0].index')||'/'||json_extract(audioFiles,'$[0].manuallyVerified') FROM books WHERE id='b1'`); got != "1/-1/1" {
		t.Errorf("stored track %s", got)
	}
	if got := r.takeEventNames(); got != "item_updated" || len(r.touched) != 0 {
		t.Errorf("events %q, metadata writes %v", got, r.touched)
	}
}

func TestEbookStatusTogglesThePrimaryEbook(t *testing.T) {
	r := newRig(t)
	book := r.mount()
	epub := strings.Replace(libraryFileJSON("ebook-1", filepath.Join(book, "story.EPUB")), `"fileType":"audio","isSupplementary":null`, `"fileType":"ebook","isSupplementary":true`, 1)
	mustWrite(t, filepath.Join(book, "story.EPUB"), "epub")
	r.exec(`UPDATE libraryItems SET libraryFiles=json_insert(libraryFiles,'$[#]',json(?)) WHERE id='i1'`, epub)
	patch := func(u *auth.Principal, target string) *httptest.ResponseRecorder {
		return r.request(u, "PATCH", target, "", "")
	}
	if rec := patch(rootUser, "/api/items/i1/ebook/ebook-1/status"); rec.Code != 200 {
		t.Fatalf("status: %d %s", rec.Code, rec.Body)
	}
	if got := r.values(`SELECT json_extract(ebookFile,'$.ino')||'/'||json_extract(ebookFile,'$.ebookFormat')||'/'||COALESCE(json_extract(ebookFile,'$.isSupplementary'),'none') FROM books WHERE id='b1'`); got != "ebook-1/epub/none" {
		t.Errorf("ebookFile %s", got)
	}
	if got := r.values(`SELECT json_extract(j.value,'$.isSupplementary') FROM libraryItems, json_each(libraryFiles) j WHERE libraryItems.id='i1' AND json_extract(j.value,'$.ino')='ebook-1'`); got != "0" {
		t.Errorf("the primary ebook is not supplementary: %s", got)
	}
	if got := r.takeEventNames(); got != "item_updated" {
		t.Errorf("events %q", got)
	}
	if rec := patch(rootUser, "/api/items/i1/ebook/ebook-1/status"); rec.Code != 200 { // toggles back
		t.Fatalf("toggle: %d", rec.Code)
	}
	if got := r.values(`SELECT COALESCE(ebookFile,'none') FROM books WHERE id='b1'`); got != "none" {
		t.Errorf("ebookFile after demotion %s", got)
	}
	for name, c := range map[string]struct {
		user   *auth.Principal
		target string
		status int
	}{
		"read only":     {readerUser, "/api/items/i1/ebook/ebook-1/status", 403},
		"not an ebook":  {rootUser, "/api/items/i1/ebook/audio-1/status", 400},
		"unknown inode": {rootUser, "/api/items/i1/ebook/nope/status", 400},
		"podcast":       {rootUser, "/api/items/ip/ebook/ebook-1/status", 400},
		"unknown item":  {rootUser, "/api/items/zz/ebook/ebook-1/status", 404},
	} {
		if rec := patch(c.user, c.target); rec.Code != c.status {
			t.Errorf("%s: %d %q", name, rec.Code, rec.Body)
		}
	}
	// The ebook routes serve the files.
	if rec := r.request(readerUser, "GET", "/api/items/i1/ebook/ebook-1", "", ""); rec.Code != 200 || rec.Body.String() != "epub" {
		t.Errorf("ebook by id: %d %q", rec.Code, rec.Body)
	}
	if rec := r.request(readerUser, "GET", "/api/items/i1/ebook/audio-1", "", ""); rec.Code != 400 {
		t.Errorf("an audio inode is not an ebook: %d", rec.Code)
	}
	if rec := r.request(readerUser, "GET", "/api/items/i1/ebook", "", ""); rec.Code != 404 {
		t.Errorf("no primary ebook: %d", rec.Code)
	}
}

func TestMetadataObjectIsAdminOnlyAndBuildsAudioTags(t *testing.T) {
	r := newRig(t)
	rec := r.request(adminUser, "GET", "/api/items/i1/metadata-object", "", "")
	if rec.Code != 200 {
		t.Fatalf("metadata object: %d %s", rec.Code, rec.Body)
	}
	got := decode[map[string]string](t, rec)
	want := map[string]string{"title": "The Book", "artist": "Ada Author", "album_artist": "Ada Author", "album": "The Book", "date": "2024", "genre": "fiction",
		"composer": "Reader", "comment": "<p>A story</p>", "description": "<p>A story</p>", "grouping": "First Series #1", "TRACKTOTAL": "1"}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %q, want %q", key, got[key], value)
		}
	}
	if _, ok := got["publisher"]; ok || len(got) != len(want) {
		t.Errorf("empty tags must be left out: %v", got)
	}
	for name, c := range map[string]struct {
		user   *auth.Principal
		id     string
		status int
	}{"reader": {readerUser, "i1", 403}, "unknown": {adminUser, "zz", 404}, "no audio": {adminUser, "i2", 400}, "podcast": {adminUser, "ip", 400}} {
		if rec := r.request(c.user, "GET", "/api/items/"+c.id+"/metadata-object", "", ""); rec.Code != c.status {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	if rec := r.request(readerUser, "GET", "/api/items/zz/ffprobe/1", "", ""); rec.Code != 403 {
		t.Errorf("ffprobe is admin only, even for an unknown item: %d", rec.Code)
	}
}

func TestDeleteFileRemovesItFromTheItemAndMarksAnEmptyBookMissing(t *testing.T) {
	r := newRig(t)
	book := r.mount()
	audio := filepath.Join(book, "01.mp3")
	del := func(u *auth.Principal, id, ino string) *httptest.ResponseRecorder {
		return r.request(u, "DELETE", "/api/items/"+id+"/file/"+ino, "", "")
	}
	for name, c := range map[string]struct {
		user    *auth.Principal
		id, ino string
		status  int
	}{"read only": {readerUser, "i1", "audio-1", 403}, "unknown item": {rootUser, "zz", "audio-1", 404}, "unknown file": {rootUser, "i1", "nope", 404}, "other library": {otherLibUser, "i1", "audio-1", 403}} {
		if rec := del(c.user, c.id, c.ino); rec.Code != c.status {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	if _, err := os.Stat(audio); err != nil {
		t.Fatalf("a refused delete removed the file: %v", err)
	}
	if rec := del(rootUser, "i1", "audio-1"); rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(audio); !os.IsNotExist(err) {
		t.Errorf("file still exists: %v", err)
	}
	if got := r.values(`SELECT isMissing||'/'||libraryFiles||'/'||(SELECT audioFiles FROM books WHERE id='b1') FROM libraryItems WHERE id='i1'`); got != "1/[]/[]" {
		t.Errorf("rows %s", got)
	}
	if got := r.takeEventNames(); got != "item_updated" {
		t.Errorf("events %q", got)
	}
}

func TestDeleteFileOfAnEpisodeRemovesTheEpisode(t *testing.T) {
	r := newRig(t)
	dir := t.TempDir()
	audio := filepath.Join(dir, "episode.mp3")
	mustWrite(t, audio, "episode")
	r.exec(`INSERT INTO libraryFolders(id,path,libraryId,createdAt,updatedAt) VALUES('fp',?,'lp',?,?)`, dir, ts, ts)
	r.exec(`UPDATE libraryItems SET libraryFiles=? WHERE id='ip'`, "["+libraryFileJSON("ep-audio", audio)+"]")
	r.exec(`UPDATE podcastEpisodes SET audioFile=replace(audioFile,'/nonexistent/podcasts/episode.mp3',?) WHERE id='e1'`, audio)
	if rec := r.request(podUser, "DELETE", "/api/items/ip/file/ep-audio", "", ""); rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if got := r.values(`SELECT count(*) FROM podcastEpisodes WHERE podcastId='p1'`); got != "0" {
		t.Errorf("episodes left: %s", got)
	}
	if _, err := os.Stat(audio); !os.IsNotExist(err) {
		t.Errorf("file still exists: %v", err)
	}
}
