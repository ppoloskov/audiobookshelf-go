package playback

import (
	"audiobookshelf-compat/internal/mediatools"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/store"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/session_golden.json from the current code")

// recorder normalises what a scenario observed (responses, persisted rows,
// emitted events) so it can be compared by value: ids and tmp paths become
// placeholders and, for server-clocked flows, times do too.
type recorder struct {
	t      *testing.T
	h      *Handler
	ids    []string
	repl   []string
	clock  bool
	base   store.User
	events []any
	out    map[string]any
}

func (r *recorder) register(id string) {
	if id == "" {
		return
	}
	for _, known := range r.ids {
		if known == id {
			return
		}
	}
	r.ids = append(r.ids, id)
	r.repl = append(r.repl, id, "<ID"+string(rune('A'+len(r.ids)-1))+">")
}

func (r *recorder) norm(v any) any {
	b, _ := json.Marshal(v)
	text := strings.NewReplacer(r.repl...).Replace(string(b))
	var out any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		r.t.Fatal(err)
	}
	scrub(out, r.clock)
	return out
}

// scrub replaces server-clock values. Progress and device rows always carry
// them; playback sessions only do when the server clocked the session.
func scrub(v any, clock bool) {
	switch x := v.(type) {
	case map[string]any:
		_, progress := x["mediaItemType"]
		_, device := x["clientName"]
		for k, e := range x {
			switch k {
			case "lastUpdate", "lastSeen", "finishedAt":
				x[k] = "<T>"
			case "startedAt", "updatedAt", "createdAt", "date", "dayOfWeek":
				if clock || ((progress || device) && k != "date" && k != "dayOfWeek") {
					x[k] = "<T>"
				}
			default:
				scrub(e, clock)
			}
		}
	case []any:
		for _, e := range x {
			scrub(e, clock)
		}
	}
}

func (r *recorder) call(name, method, path string, body string, admin bool) map[string]any {
	user := r.user()
	if admin {
		user.ID, user.Type = "admin-one", "admin"
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	w := httptest.NewRecorder()
	serve(r.h, w, req, user)
	var parsed any
	if json.Unmarshal(w.Body.Bytes(), &parsed) != nil {
		parsed = w.Body.String()
	}
	if m, ok := parsed.(map[string]any); ok {
		if id, _ := m["id"].(string); id != "" && w.Code == http.StatusOK && strings.Contains(path, "/play") {
			r.register(id)
		}
		if d, _ := m["deviceInfo"].(map[string]any); d != nil {
			if id, _ := d["id"].(string); id != "" {
				r.register(id)
			}
		}
	}
	res := map[string]any{"status": w.Code, "body": parsed}
	r.out[name] = res
	return res
}

func (r *recorder) user() store.User { return r.base }

func (r *recorder) rows() []any {
	var out []any
	for _, q := range []string{
		`SELECT * FROM playbackSessions ORDER BY displayTitle, mediaPlayer, startTime, timeListening`,
		`SELECT * FROM devices ORDER BY clientName, deviceName, deviceVersion`,
		`SELECT * FROM mediaProgresses ORDER BY mediaItemId`,
	} {
		rows, err := r.h.db.Query(q)
		if err != nil {
			r.t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				r.t.Fatal(err)
			}
			row := map[string]any{}
			for i, c := range cols {
				if b, ok := vals[i].([]byte); ok {
					vals[i] = string(b)
				}
				if s, ok := vals[i].(string); ok && (c == "mediaMetadata" || c == "extraData" || c == "audioFile") {
					var j any
					if json.Unmarshal([]byte(s), &j) == nil {
						vals[i] = j
					}
				}
				row[c] = vals[i]
			}
			out = append(out, row)
		}
		rows.Close()
	}
	return out
}

func TestSessionGolden(t *testing.T) {
	if !mediatools.FFmpegAvailable() {
		t.Skip("ffmpeg is unavailable")
	}
	got := map[string]any{}
	t.Run("book", func(t *testing.T) { got["book"] = bookScenario(t) })
	t.Run("podcast", func(t *testing.T) { got["podcast"] = podcastScenario(t) })
	t.Run("local", func(t *testing.T) { got["local"] = localScenario(t) })
	raw, _ := json.Marshal(got)
	var normalized any
	_ = json.Unmarshal(raw, &normalized)
	const file = "testdata/session_golden.json"
	if *updateGolden {
		pretty, _ := json.MarshalIndent(normalized, "", "  ")
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, append(pretty, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var wantValue any
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(normalized, wantValue) {
		gotPretty, _ := json.MarshalIndent(normalized, "", "  ")
		_ = os.WriteFile(os.TempDir()+"/session_golden.got.json", gotPretty, 0o644)
		t.Fatalf("session responses, rows or events differ from %s (got written to %s/session_golden.got.json)", file, os.TempDir())
	}
}

func newRecorder(t *testing.T, clock bool) *recorder {
	h, user, itemID, mediaPath := fixture(t)
	_ = itemID
	user.IsActive = true
	r := &recorder{t: t, h: h, base: user, clock: clock, out: map[string]any{}}
	// Fixed item timestamps so the embedded libraryItem is comparable.
	for _, q := range []string{
		`UPDATE libraryItems SET mtime='2026-01-01T00:00:00.000Z',ctime='2026-01-01T00:00:00.000Z',birthtime='2026-01-01T00:00:00.000Z',createdAt='2026-01-01T00:00:00.000Z',updatedAt='2026-01-01T00:00:00.000Z'`,
		`UPDATE bookAuthors SET createdAt='2026-01-01T00:00:00.000Z'`,
	} {
		if _, err := h.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	r.repl = append(r.repl, mediaPath, "<MEDIA>", h.cfg.MetadataPath, "<META>")
	h.emit = func(userID, event string, payload any) {
		b, _ := json.Marshal(payload)
		var raw any
		_ = json.Unmarshal(b, &raw)
		r.events = append(r.events, map[string]any{"to": userID, "event": event, "payload": raw})
	}
	return r
}

func (r *recorder) finish() any {
	for _, q := range []string{`SELECT id FROM playbackSessions ORDER BY displayTitle, mediaPlayer, startTime, timeListening`, `SELECT id FROM devices ORDER BY clientName, deviceName, deviceVersion`, `SELECT id FROM mediaProgresses ORDER BY mediaItemId`} {
		rows, err := r.h.db.Query(q)
		if err != nil {
			r.t.Fatal(err)
		}
		for rows.Next() {
			var id string
			_ = rows.Scan(&id)
			r.register(id)
		}
		rows.Close()
	}
	r.out["rows"] = r.rows()
	r.out["events"] = r.events
	r.out["open"] = r.h.OpenSessions()
	return r.norm(r.out)
}

func sid(res map[string]any) string {
	body, _ := res["body"].(map[string]any)
	id, _ := body["id"].(string)
	return id
}

func bookScenario(t *testing.T) any {
	r := newRecorder(t, true)
	android := r.call("playAndroid", "POST", "/api/items/item-one/play", `{"deviceInfo":{"deviceId":"dev-1","manufacturer":"Google","model":"Pixel 8","sdkVersion":34,"clientName":"Abs Android","clientVersion":"0.9.79","bogus":"x"},"mediaPlayer":"exo-player","forceDirectPlay":true,"supportedMimeTypes":["audio/mpeg"]}`, false)
	id := sid(android)
	r.call("get", "GET", "/api/session/"+id, "", false)
	r.call("getForeign", "GET", "/api/session/"+id, "", true)
	r.call("syncBad", "POST", "/api/session/"+id+"/sync", `{"currentTime":-1,"timeListened":1}`, false)
	r.call("syncBad2", "POST", "/api/session/"+id+"/sync", `{"currentTime":1}`, false)
	r.call("sync", "POST", "/api/session/"+id+"/sync", `{"currentTime":1.5,"timeListened":2,"duration":3}`, false)
	r.call("history", "GET", "/api/sessions?sort=createdAt&desc=1", "", true)
	r.call("close", "POST", "/api/session/"+id+"/close", `{"currentTime":2,"timeListened":1}`, false)
	web := r.call("playWeb", "POST", "/api/items/item-one/play", `{"supportedMimeTypes":["audio/ogg"],"forceDirectPlay":true}`, false)
	r.call("closeEmpty", "POST", "/api/session/"+sid(web)+"/close", `{}`, false)
	r.call("playNoBody", "POST", "/api/items/item-one/play", `{"deviceInfo":{"deviceId":"dev-2","clientName":"Abs iOS","model":"iPhone"},"forceDirectPlay":true}`, false)
	// Session left open: shows up in OpenSessions.
	return r.finish()
}

func podcastScenario(t *testing.T) any {
	r := newRecorder(t, true)
	h := r.h
	if _, err := h.db.Exec(`UPDATE libraryItems SET mediaType='podcast',mediaId='podcast-one'`); err != nil {
		t.Fatal(err)
	}
	now := "2026-01-01T00:00:00.000Z"
	if _, err := h.db.Exec(`INSERT INTO podcasts (id,title,author,feedURL,imageURL,description,language,podcastType,explicit,autoDownloadEpisodes,maxEpisodesToKeep,maxNewEpisodesToDownload,tags,genres,numEpisodes,createdAt,updatedAt) VALUES ('podcast-one','Pod','Host','https://f.example/rss','https://f.example/i.png','Desc','en','episodic',1,0,0,3,'["t"]','["g1","g2"]',2,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	var path string
	_ = h.db.QueryRow(`SELECT path FROM libraryItems`).Scan(&path)
	for i, id := range []string{"episode-one", "episode-two"} {
		af, _ := json.Marshal(map[string]any{"index": 1, "ino": "ep-" + id, "metadata": map[string]any{"filename": id + ".mp3", "ext": ".mp3", "path": path, "size": 1000}, "duration": 3 + i, "bitRate": 64000, "codec": "mp3", "customKey": "kept", "exclude": false})
		if _, err := h.db.Exec(`INSERT INTO podcastEpisodes (id,podcastId,title,audioFile,chapters,createdAt,updatedAt) VALUES (?,'podcast-one',?,?,'[{"id":0,"start":0,"end":1,"title":"c"}]',?,?)`, id, "Title "+id, string(af), now, now); err != nil {
			t.Fatal(err)
		}
	}
	res := r.call("playEpisode", "POST", "/api/items/item-one/play/episode-two", `{"forceDirectPlay":true,"mediaPlayer":"html5","deviceInfo":{"deviceId":"web-1","clientName":"Abs Web","clientVersion":"2.37.2"}}`, false)
	id := sid(res)
	r.call("syncEpisode", "POST", "/api/session/"+id+"/sync", `{"currentTime":2,"timeListened":2,"duration":4}`, false)
	r.call("closeEpisode", "POST", "/api/session/"+id+"/close", `{"currentTime":3,"timeListened":1}`, false)
	r.call("playMissingEpisode", "POST", "/api/items/item-one/play/nope", `{"deviceInfo":{"deviceId":"web-1"}}`, false)
	hls := r.call("playEpisodeHLS", "POST", "/api/items/item-one/play/episode-one", `{"deviceInfo":{"deviceId":"web-1"}}`, false)
	r.call("closeHLS", "POST", "/api/session/"+sid(hls)+"/close", `{}`, false)
	return r.finish()
}

func localScenario(t *testing.T) any {
	r := newRecorder(t, false)
	device := `"deviceInfo":{"deviceId":"dev-local","manufacturer":"Google","model":"Pixel","sdkVersion":33,"clientName":"Abs Android","clientVersion":"0.9"}`
	single := func(extra string) string {
		return `{"id":"play_local_one","libraryItemId":"item-one","currentTime":20,"timeListening":5,"startedAt":1767600000000,"updatedAt":1767600010000,"mediaPlayer":"offline",` + device + extra + `}`
	}
	r.call("local", "POST", "/api/session/local", single(`,"date":"2026-01-05","dayOfWeek":"Monday","coverPath":"/c.jpg","displayTitle":"Mine","displayAuthor":"Me","playMethod":3,"startTime":2,"mediaMetadata":{"title":"Client title","extra":"kept"}`), false)
	// Same local id again: updates the same row, recomputes the day.
	r.call("localAgain", "POST", "/api/session/local", single(`,"date":"2026-01-05","dayOfWeek":"Monday"`), false)
	r.call("localBadBody", "POST", "/api/session/local", `[`, false)
	r.call("localMissing", "POST", "/api/session/local", `{"id":"x","libraryItemId":"nope"}`, false)
	all := `{` + device + `,"sessions":[
		{"id":"play_local_two","libraryItemId":"item-one","currentTime":"7","timeListening":3.5,"startedAt":"1767600000000","updatedAt":"2026-01-05T10:00:00.000Z"},
		"junk",
		{"id":"play_local_two","libraryItemId":"item-one","currentTime":8,"timeListening":4,"updatedAt":1767600020000,"playMethod":0,"displayTitle":"T2"},
		{"id":"fixed-id-1","libraryItemId":"nope","displayTitle":"gone"},
		{"libraryItemId":"item-one","currentTime":1,"timeListening":1,"updatedAt":1767600030000,"startedAt":1767600029000},
		{"id":"play_local_old","libraryItemId":"item-one","currentTime":1,"timeListening":1,"updatedAt":1000}
	]}`
	r.call("localAll", "POST", "/api/session/local-all", all, false)
	return r.finish()
}
