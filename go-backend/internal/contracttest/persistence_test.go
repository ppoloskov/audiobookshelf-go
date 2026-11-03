package contracttest

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// Persistence in both directions (ROADMAP F7). The TS-created fixture is read
// through the Go API and compared with its own rows; then Go writes, and the
// rows Go wrote are checked against the formats TS writes (ARCHITECTURE.md,
// Persistence contract).

var (
	uuidV4Pattern   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	dateTimePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3} \+00:00$`)
	datePattern     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

const (
	legacyRowPrefix = "legacy-null-"
	dateTimeLayout  = "2006-01-02 15:04:05.000 -07:00"
)

// row is one stored row keyed by column name. SQL NULL is nil, TEXT is string,
// INTEGER is int64, REAL is float64 and BLOB is []byte. A JSON column that
// holds []byte was stored as a BLOB, not as text.
type row map[string]any

// rowsOf returns every row of q. The rows are closed before it returns, so
// the caller may query again (no nested queries on the pool).
func rowsOf(t testing.TB, db *sql.DB, q string, args ...any) []row {
	t.Helper()
	rows, err := db.Query(q, args...)
	if err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("columns %q: %v", q, err)
	}
	var out []row
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scan %q: %v", q, err)
		}
		r := row{}
		for i, c := range cols {
			r[c] = vals[i]
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %q: %v", q, err)
	}
	return out
}

// declaredColumns maps each column of table to its declared type and whether
// it is NOT NULL.
type declared struct {
	typ     string
	notNull bool
}

func declaredColumns(t testing.TB, db *sql.DB, table string) map[string]declared {
	t.Helper()
	out := map[string]declared{}
	for _, r := range rowsOf(t, db, `SELECT name, type, "notnull" AS nn FROM pragma_table_info(?)`, table) {
		out[text(r["name"])] = declared{typ: strings.ToUpper(text(r["type"])), notNull: r["nn"] == int64(1)}
	}
	if len(out) == 0 {
		t.Fatalf("table %s has no columns", table)
	}
	return out
}

// selectAll reads whole rows of table as stored. The driver turns a DATETIME
// column into time.Time, which would hide the text format, so DATETIME columns
// are cast back to the text that is on disk.
func selectAll(t testing.TB, db *sql.DB, table, where string, args ...any) []row {
	t.Helper()
	var list []string
	for col, d := range declaredColumns(t, db, table) {
		expr := `"` + col + `"`
		if strings.HasPrefix(d.typ, "DATETIME") {
			expr = "CAST(" + expr + " AS TEXT)"
		}
		list = append(list, expr+` AS "`+col+`"`)
	}
	sort.Strings(list)
	return rowsOf(t, db, "SELECT "+strings.Join(list, ", ")+" FROM "+table+" "+where, args...)
}

func text(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	}
	return ""
}

// asJSONNumber widens a stored INTEGER or REAL to float64, the type a decoded
// JSON number has, so stored values compare with API values.
func asJSONNumber(v any) any {
	if n, ok := v.(int64); ok {
		return float64(n)
	}
	return v
}

// asBool reads a 0/1 boolean column.
func asBool(t testing.TB, v any, column string) bool {
	t.Helper()
	n, ok := v.(int64)
	if !ok || (n != 0 && n != 1) {
		t.Fatalf("%s = %#v, want 0 or 1", column, v)
	}
	return n == 1
}

// epochMillis reads a stored DATETIME the way a client reads the API: Unix
// milliseconds, or nil for SQL NULL.
func epochMillis(t testing.TB, v any) any {
	t.Helper()
	if v == nil {
		return nil
	}
	ts, err := time.Parse(dateTimeLayout, text(v))
	if err != nil {
		t.Fatalf("stored DATETIME %q: %v", text(v), err)
	}
	return float64(ts.UnixMilli())
}

// jsonObject decodes a stored JSON object column; a NULL column is an empty map.
func jsonObject(t testing.TB, v any) map[string]any {
	t.Helper()
	out := map[string]any{}
	if v == nil {
		return out
	}
	if err := json.Unmarshal([]byte(text(v)), &out); err != nil {
		t.Fatalf("stored JSON %q: %v", text(v), err)
	}
	return out
}

// getJSON sends a GET and decodes the body into out.
func getJSON(t testing.TB, in *Install, path, token string, out any) {
	t.Helper()
	resp, body := in.Do(t, http.MethodGet, path, token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d: %s", path, resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, out); err != nil {
		t.Fatalf("GET %s: decode: %v", path, err)
	}
}

func expectStatus(t testing.TB, in *Install, method, path, token string, body any, want int) []byte {
	t.Helper()
	resp, data := in.Do(t, method, path, token, body)
	if resp.StatusCode != want {
		t.Fatalf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, want, data)
	}
	return data
}

// compareField reports a difference between the API value and the stored one.
func compareField(t testing.TB, id, field string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("row %s field %s: API = %#v, stored = %#v", id, field, got, want)
	}
}

// --- (a) read: the fixture rows, through the Go API -------------------------

func TestMeReturnsFixtureProgressAndBookmarks(t *testing.T) {
	in := Boot(t)
	listener := in.Users["listener"]
	token := in.Login(t, listener.Username, listener.Password)
	userID := in.IDs["listener"]

	var me struct {
		MediaProgress []map[string]any `json:"mediaProgress"`
		Bookmarks     []any            `json:"bookmarks"`
	}
	getJSON(t, in, "/api/me", token, &me)

	stored := selectAll(t, in.DB, "mediaProgresses", "WHERE userId = ?", userID)
	if len(me.MediaProgress) != len(stored) {
		t.Fatalf("GET /api/me has %d progress rows, stored %d", len(me.MediaProgress), len(stored))
	}
	byID := map[string]map[string]any{}
	for _, p := range me.MediaProgress {
		byID[text(p["id"])] = p
	}
	for _, r := range stored {
		id := text(r["id"])
		got, ok := byID[id]
		if !ok {
			t.Errorf("stored progress %s is missing from GET /api/me", id)
			continue
		}
		extra := jsonObject(t, r["extraData"])
		progress := any(float64(0))
		if v, ok := extra["progress"]; ok {
			progress = v
		}
		compareField(t, id, "mediaItemId", got["mediaItemId"], text(r["mediaItemId"]))
		compareField(t, id, "mediaItemType", got["mediaItemType"], text(r["mediaItemType"]))
		compareField(t, id, "duration", got["duration"], asJSONNumber(r["duration"]))
		compareField(t, id, "currentTime", got["currentTime"], asJSONNumber(r["currentTime"]))
		compareField(t, id, "ebookProgress", got["ebookProgress"], asJSONNumber(r["ebookProgress"]))
		compareField(t, id, "ebookLocation", got["ebookLocation"], nullableText(r["ebookLocation"]))
		compareField(t, id, "isFinished", got["isFinished"], asBool(t, r["isFinished"], "isFinished"))
		compareField(t, id, "hideFromContinueListening", got["hideFromContinueListening"], asBool(t, r["hideFromContinueListening"], "hideFromContinueListening"))
		compareField(t, id, "progress", got["progress"], progress)
		compareField(t, id, "libraryItemId", got["libraryItemId"], extra["libraryItemId"])
		compareField(t, id, "lastUpdate", got["lastUpdate"], epochMillis(t, r["updatedAt"]))
		compareField(t, id, "startedAt", got["startedAt"], epochMillis(t, r["createdAt"]))
		compareField(t, id, "finishedAt", got["finishedAt"], epochMillis(t, r["finishedAt"]))
	}

	var bookmarks []any
	userRows := rowsOf(t, in.DB, `SELECT bookmarks FROM users WHERE id = ?`, userID)
	if err := json.Unmarshal([]byte(text(userRows[0]["bookmarks"])), &bookmarks); err != nil {
		t.Fatalf("stored bookmarks: %v", err)
	}
	if len(bookmarks) == 0 {
		t.Fatal("fixture listener has no stored bookmarks; the check would prove nothing")
	}
	if !reflect.DeepEqual(me.Bookmarks, bookmarks) {
		t.Errorf("GET /api/me bookmarks = %#v, stored = %#v", me.Bookmarks, bookmarks)
	}
}

func nullableText(v any) any {
	if v == nil {
		return nil
	}
	return text(v)
}

// Read defaults follow upstream objects/PlaybackSession.js construct: a NULL
// displayTitle reads as "" (:151), a NULL currentTime as 0 (:159), a zero or
// NULL timeListening as null. The stored NULL is not changed by the read.
func TestListeningSessionsReturnFixtureRows(t *testing.T) {
	in := Boot(t)
	listener := in.Users["listener"]
	token := in.Login(t, listener.Username, listener.Password)
	userID := in.IDs["listener"]

	var out struct {
		Total    int              `json:"total"`
		Sessions []map[string]any `json:"sessions"`
	}
	getJSON(t, in, "/api/me/listening-sessions?itemsPerPage=100", token, &out)

	stored := selectAll(t, in.DB, "playbackSessions", "WHERE userId = ?", userID)
	if out.Total != len(stored) || len(out.Sessions) != len(stored) {
		t.Fatalf("listening sessions: total %d, returned %d, stored %d", out.Total, len(out.Sessions), len(stored))
	}
	byID := map[string]map[string]any{}
	for _, s := range out.Sessions {
		byID[text(s["id"])] = s
	}
	sawLegacy := false
	for _, r := range stored {
		id := text(r["id"])
		got, ok := byID[id]
		if !ok {
			t.Errorf("stored session %s is missing from GET /api/me/listening-sessions", id)
			continue
		}
		sawLegacy = sawLegacy || strings.HasPrefix(id, legacyRowPrefix)
		extra := jsonObject(t, r["extraData"])
		if text(r["mediaItemType"]) == "podcastEpisode" {
			compareField(t, id, "episodeId", got["episodeId"], text(r["mediaItemId"]))
			compareField(t, id, "mediaType", got["mediaType"], "podcast")
		} else {
			compareField(t, id, "bookId", got["bookId"], text(r["mediaItemId"]))
			compareField(t, id, "mediaType", got["mediaType"], "book")
		}
		compareField(t, id, "libraryId", got["libraryId"], nullableText(r["libraryId"]))
		compareField(t, id, "duration", got["duration"], asJSONNumber(r["duration"]))
		compareField(t, id, "playMethod", got["playMethod"], asJSONNumber(r["playMethod"]))
		compareField(t, id, "mediaPlayer", got["mediaPlayer"], nullableText(r["mediaPlayer"]))
		compareField(t, id, "serverVersion", got["serverVersion"], nullableText(r["serverVersion"]))
		compareField(t, id, "coverPath", got["coverPath"], nullableText(r["coverPath"]))
		compareField(t, id, "startedAt", got["startedAt"], epochMillis(t, r["createdAt"]))
		compareField(t, id, "updatedAt", got["updatedAt"], epochMillis(t, r["updatedAt"]))
		compareField(t, id, "mediaMetadata", got["mediaMetadata"], jsonValue(t, r["mediaMetadata"]))
		if r["displayTitle"] != nil {
			compareField(t, id, "displayTitle", got["displayTitle"], text(r["displayTitle"]))
		} else if got["displayTitle"] != "" {
			t.Errorf("row %s displayTitle NULL: API = %#v, want the legacy default \"\"", id, got["displayTitle"])
		}
		if r["currentTime"] != nil {
			compareField(t, id, "currentTime", got["currentTime"], asJSONNumber(r["currentTime"]))
		} else if got["currentTime"] != float64(0) {
			t.Errorf("row %s currentTime NULL: API = %#v, want the legacy default 0", id, got["currentTime"])
		}
		if r["timeListening"] != nil && r["timeListening"] != int64(0) {
			compareField(t, id, "timeListening", got["timeListening"], asJSONNumber(r["timeListening"]))
		} else if got["timeListening"] != nil {
			t.Errorf("row %s timeListening %#v: API = %#v, want null", id, r["timeListening"], got["timeListening"])
		}
		// Upstream objects/PlaybackSession.js:134 reads li_ and local_ library
		// item ids as null; anything else is returned as stored.
		want := extra["libraryItemId"]
		if s, ok := want.(string); ok && (strings.HasPrefix(s, "li_") || strings.HasPrefix(s, "local_")) {
			want = nil
		}
		compareField(t, id, "libraryItemId", got["libraryItemId"], want)
		if r["date"] != nil {
			compareField(t, id, "date", got["date"], text(r["date"]))
		} else if d, _ := got["date"].(string); !datePattern.MatchString(d) {
			t.Errorf("row %s date NULL: API = %#v, want a derived YYYY-MM-DD", id, got["date"])
		}
	}
	if !sawLegacy {
		t.Fatal("fixture has no legacy-null rows; the NULL checks would prove nothing")
	}
}

func jsonValue(t testing.TB, v any) any {
	t.Helper()
	if v == nil {
		return nil
	}
	var out any
	if err := json.Unmarshal([]byte(text(v)), &out); err != nil {
		t.Fatalf("stored JSON %q: %v", text(v), err)
	}
	return out
}

// --- (a) write: Go writes, then the stored rows are checked -----------------

// tsBaseline is what TS wrote before Go touched the database: the ids of every
// existing row, and per table the columns TS populated in every non-legacy
// row of the same kind (the columns Go must populate too).
type tsBaseline struct {
	ids      map[string]map[string]bool
	required map[string][]string
}

func takeBaseline(t testing.TB, db *sql.DB) tsBaseline {
	t.Helper()
	b := tsBaseline{ids: map[string]map[string]bool{}, required: map[string][]string{}}
	for _, table := range []string{"mediaProgresses", "playbackSessions"} {
		b.ids[table] = map[string]bool{}
		for _, r := range rowsOf(t, db, `SELECT id FROM `+table) {
			b.ids[table][text(r["id"])] = true
		}
		kind := selectAll(t, db, table, `WHERE mediaItemType = 'book' AND id NOT LIKE ?`, legacyRowPrefix+"%")
		if len(kind) == 0 {
			t.Fatalf("%s: no TS book rows to compare against", table)
		}
		b.required[table] = populatedInEvery(kind)
	}
	return b
}

// populatedInEvery lists the columns that are non-NULL in every row.
func populatedInEvery(rows []row) []string {
	var cols []string
	for col := range rows[0] {
		all := true
		for _, r := range rows {
			if r[col] == nil {
				all = false
				break
			}
		}
		if all {
			cols = append(cols, col)
		}
	}
	sort.Strings(cols)
	return cols
}

// checkStoredFormat asserts one row Go wrote uses the stored formats TS uses.
func checkStoredFormat(t testing.TB, db *sql.DB, table string, r row, required []string) {
	t.Helper()
	id := text(r["id"])
	cols := declaredColumns(t, db, table)
	for col, d := range cols {
		v, present := r[col]
		if !present {
			t.Errorf("%s %s: column %s missing from the row", table, id, col)
			continue
		}
		switch {
		case v == nil:
			if d.notNull {
				t.Errorf("%s %s: NOT NULL column %s is NULL", table, id, col)
			}
			continue
		case strings.HasPrefix(d.typ, "DATETIME"):
			if !dateTimePattern.MatchString(text(v)) {
				t.Errorf("%s %s: %s = %q, want DATETIME text YYYY-MM-DD HH:MM:SS.SSS +00:00", table, id, col, text(v))
			}
		case strings.HasPrefix(d.typ, "JSON"):
			if _, isBlob := v.([]byte); isBlob {
				t.Errorf("%s %s: %s is stored as a BLOB, want JSON text", table, id, col)
			} else if !json.Valid([]byte(text(v))) {
				t.Errorf("%s %s: %s = %q is not valid JSON", table, id, col, text(v))
			}
		case strings.HasPrefix(d.typ, "UUID"):
			if !uuidV4Pattern.MatchString(text(v)) {
				t.Errorf("%s %s: %s = %q, want a UUIDv4", table, id, col, text(v))
			}
		case strings.HasPrefix(d.typ, "TINYINT"):
			asBool(t, v, table+"."+col)
		}
	}
	for _, col := range required {
		if r[col] == nil {
			t.Errorf("%s %s: %s is NULL; TS rows of the same kind populate it", table, id, col)
		}
	}
}

func TestGoWritesUseTSStorageFormats(t *testing.T) {
	in := Boot(t)
	listener := in.Users["listener"]
	token := in.Login(t, listener.Username, listener.Password)
	userID := in.IDs["listener"]
	base := takeBaseline(t, in.DB)

	// Go writes: a progress row for a book the listener has none for (insert),
	// an update of an existing row, a bookmark, an offline session upload and a
	// played session that is synced.
	expectStatus(t, in, http.MethodPatch, "/api/me/progress/"+in.IDs["disposable"], token,
		map[string]any{"currentTime": 7.5, "duration": 30}, http.StatusOK)
	expectStatus(t, in, http.MethodPatch, "/api/me/progress/"+in.IDs["saga2"], token,
		map[string]any{"currentTime": 15, "duration": 30}, http.StatusOK)
	expectStatus(t, in, http.MethodPost, "/api/me/item/"+in.IDs["saga1"]+"/bookmark", token,
		map[string]any{"time": 12, "title": "Go write"}, http.StatusOK)
	now := time.Now().UnixMilli()
	expectStatus(t, in, http.MethodPost, "/api/session/local", token, map[string]any{
		"id": "play_local_persist0001", "userId": userID, "libraryId": in.IDs["library"],
		"libraryItemId": in.IDs["saga1"], "mediaType": "book", "mediaMetadata": map[string]any{},
		"chapters": []any{}, "displayTitle": "The Beginning", "displayAuthor": "Terry Fixture",
		"coverPath": nil, "duration": 60, "playMethod": 3, "mediaPlayer": "exo-player",
		"deviceInfo":    map[string]any{"deviceId": "persist-android-0001", "clientName": "Abs Android", "clientVersion": "0.9.79-beta"},
		"serverVersion": "2.37.2", "date": "2026-01-05", "dayOfWeek": "Monday", "timeListening": 10,
		"startTime": 0, "currentTime": 10, "startedAt": now - 10000, "updatedAt": now,
	}, http.StatusOK)
	var played struct {
		ID string `json:"id"`
	}
	data := expectStatus(t, in, http.MethodPost, "/api/items/"+in.IDs["saga1"]+"/play", token,
		map[string]any{"forceDirectPlay": true, "deviceInfo": map[string]any{"deviceId": "persist-web-0001", "clientName": "Abs Web", "clientVersion": "2.37.2"}}, http.StatusOK)
	if err := json.Unmarshal(data, &played); err != nil || played.ID == "" {
		t.Fatalf("play: no session id in %s", data)
	}
	expectStatus(t, in, http.MethodPost, "/api/session/"+played.ID+"/sync", token,
		map[string]any{"currentTime": 9, "timeListened": 9}, http.StatusOK)

	// Progress rows: the new row and the updated row.
	disposableMedia := rowsOf(t, in.DB, `SELECT mediaId FROM libraryItems WHERE id = ?`, in.IDs["disposable"])
	created := selectAll(t, in.DB, "mediaProgresses", "WHERE userId = ? AND mediaItemId = ?", userID, text(disposableMedia[0]["mediaId"]))
	if len(created) != 1 {
		t.Fatalf("PATCH created %d progress rows for the disposable book, want 1", len(created))
	}
	checkStoredFormat(t, in.DB, "mediaProgresses", created[0], base.required["mediaProgresses"])
	saga2Media := rowsOf(t, in.DB, `SELECT mediaId FROM libraryItems WHERE id = ?`, in.IDs["saga2"])
	updated := selectAll(t, in.DB, "mediaProgresses", "WHERE userId = ? AND mediaItemId = ?", userID, text(saga2Media[0]["mediaId"]))
	if len(updated) != 1 {
		t.Fatalf("saga2 progress rows = %d, want 1", len(updated))
	}
	checkStoredFormat(t, in.DB, "mediaProgresses", updated[0], base.required["mediaProgresses"])
	if text(updated[0]["updatedAt"]) == "2026-11-03 12:46:27.379 +00:00" {
		t.Error("PATCH did not move saga2 progress updatedAt")
	}

	// The bookmark list: TEXT JSON with TS entry keys.
	users := selectAll(t, in.DB, "users", "WHERE id = ?", userID)
	checkStoredFormat(t, in.DB, "users", users[0], nil)
	var entries []map[string]any
	if err := json.Unmarshal([]byte(text(users[0]["bookmarks"])), &entries); err != nil {
		t.Fatalf("stored bookmarks: %v", err)
	}
	foundGo := false
	for _, e := range entries {
		if e["title"] != "Go write" {
			continue
		}
		foundGo = true
		keys := make([]string, 0, len(e))
		for k := range e {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if !reflect.DeepEqual(keys, []string{"createdAt", "libraryItemId", "time", "title"}) {
			t.Errorf("bookmark keys = %v, want the TS entry keys", keys)
		}
		if _, ok := e["createdAt"].(float64); !ok || e["createdAt"].(float64) != float64(int64(e["createdAt"].(float64))) {
			t.Errorf("bookmark createdAt = %#v, want integer epoch milliseconds", e["createdAt"])
		}
	}
	if !foundGo {
		t.Fatal("bookmark written by Go is not in users.bookmarks")
	}

	// Playback sessions: every row Go created.
	sessions := 0
	for _, r := range selectAll(t, in.DB, "playbackSessions", "WHERE userId = ?", userID) {
		if base.ids["playbackSessions"][text(r["id"])] {
			continue
		}
		sessions++
		checkStoredFormat(t, in.DB, "playbackSessions", r, base.required["playbackSessions"])
		if !datePattern.MatchString(text(r["date"])) {
			t.Errorf("playbackSessions %s: date = %q, want YYYY-MM-DD", text(r["id"]), text(r["date"]))
		}
	}
	if sessions != 2 {
		t.Fatalf("Go created %d playback sessions, want 2 (local sync and played session)", sessions)
	}
}

func TestExpandedItemsMatchFixtureRows(t *testing.T) {
	in := Boot(t)
	listener := in.Users["listener"]
	token := in.Login(t, listener.Username, listener.Password)

	for _, key := range []string{"saga1", "saga2", "novel"} {
		itemID := in.IDs[key]
		var item struct {
			Media struct {
				Duration float64 `json:"duration"`
				Metadata struct {
					Title       string   `json:"title"`
					Subtitle    *string  `json:"subtitle"`
					Description *string  `json:"description"`
					Narrators   []string `json:"narrators"`
					Authors     []struct {
						Name string `json:"name"`
					} `json:"authors"`
				} `json:"metadata"`
			} `json:"media"`
		}
		getJSON(t, in, "/api/items/"+itemID+"?expanded=1", token, &item)

		li := rowsOf(t, in.DB, `SELECT mediaId, title FROM libraryItems WHERE id = ?`, itemID)
		if len(li) != 1 {
			t.Fatalf("%s: library item not stored", key)
		}
		book := rowsOf(t, in.DB, `SELECT * FROM books WHERE id = ?`, text(li[0]["mediaId"]))
		if len(book) != 1 {
			t.Fatalf("%s: book row not stored", key)
		}
		b := book[0]
		meta := item.Media.Metadata
		compareField(t, key, "libraryItems.title", meta.Title, text(li[0]["title"]))
		compareField(t, key, "books.title", meta.Title, text(b["title"]))
		compareField(t, key, "books.subtitle", meta.Subtitle, nullableStringPtr(b["subtitle"]))
		compareField(t, key, "books.description", meta.Description, nullableStringPtr(b["description"]))
		compareField(t, key, "books.duration", item.Media.Duration, asJSONNumber(b["duration"]))
		narrators, _ := jsonValue(t, b["narrators"]).([]any)
		wantNarrators := []string{}
		for _, n := range narrators {
			wantNarrators = append(wantNarrators, n.(string))
		}
		compareField(t, key, "books.narrators", meta.Narrators, wantNarrators)

		authors := rowsOf(t, in.DB, `SELECT a.name AS name FROM bookAuthors ba JOIN authors a ON a.id = ba.authorId WHERE ba.bookId = ?`, text(b["id"]))
		var wantAuthors, gotAuthors []string
		for _, a := range authors {
			wantAuthors = append(wantAuthors, text(a["name"]))
		}
		for _, a := range meta.Authors {
			gotAuthors = append(gotAuthors, a.Name)
		}
		sort.Strings(wantAuthors)
		sort.Strings(gotAuthors)
		compareField(t, key, "authors", gotAuthors, wantAuthors)
	}
}

// nullableStringPtr is a NULL-or-text column as a JSON string pointer.
func nullableStringPtr(v any) *string {
	if v == nil {
		return nil
	}
	s := text(v)
	return &s
}
