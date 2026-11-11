package item

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/internal/wire"
)

func TestGetFullAndExpanded(t *testing.T) {
	r := newRig(t)
	full := decode[map[string]any](t, r.do(rootUser, "GET", "/api/items/i1", ""))
	media := full["media"].(map[string]any)
	if full["mediaType"] != "book" || full["libraryId"] != "l1" || media["metadata"].(map[string]any)["title"] != "The Book" {
		t.Fatalf("full item = %v", full)
	}
	if _, ok := full["numFiles"]; ok {
		t.Error("full item carries expanded keys")
	}
	expanded := decode[map[string]any](t, r.do(rootUser, "GET", "/api/items/i1?expanded=1", ""))
	for _, key := range []string{"lastScan", "libraryFiles", "scanVersion", "numFiles"} {
		if _, ok := expanded[key]; !ok {
			t.Errorf("expanded item lacks %q", key)
		}
	}
	em := expanded["media"].(map[string]any)
	for _, key := range []string{"chapters", "audioFiles", "ebookFile", "tracks"} {
		if _, ok := em[key]; !ok {
			t.Errorf("expanded media lacks %q", key)
		}
	}
	if em["numAudioFiles"] != float64(1) {
		t.Errorf("expanded media numAudioFiles = %v", em["numAudioFiles"])
	}
}

func TestGetStatuses(t *testing.T) {
	r := newRig(t)
	for _, id := range []string{"missing", "00000000-0000-0000-0000-000000000000"} {
		rec := r.do(rootUser, "GET", "/api/items/"+id, "")
		if rec.Code != http.StatusNotFound || rec.Body.String() != "Not Found" || rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Errorf("%s: %d %q %v", id, rec.Code, rec.Body, rec.Header())
		}
	}
	if rec := r.do(podUser, "GET", "/api/items/i1", ""); rec.Code != http.StatusForbidden {
		t.Errorf("other library: %d", rec.Code)
	}
}

func TestGetEnforcesExplicitAndTagPermissions(t *testing.T) {
	r := newRig(t)
	r.exec(`UPDATE books SET explicit=1,tags='["tag-x"]' WHERE id='b1'`)
	for _, tc := range []struct {
		name, permissions string
		want              int
	}{
		{"explicit denied despite matching tag", `{"accessAllLibraries":true,"accessExplicitContent":false,"itemTagsSelected":["tag-x"]}`, http.StatusForbidden},
		{"non-overlapping tag denied", `{"accessAllLibraries":true,"accessExplicitContent":true,"itemTagsSelected":["tag-a"]}`, http.StatusForbidden},
		{"matching tag and explicit allowed", `{"accessAllLibraries":true,"accessExplicitContent":true,"itemTagsSelected":["tag-x"]}`, http.StatusOK},
	} {
		if rec := r.do(user("u9", "user", tc.permissions), "GET", "/api/items/i1", ""); rec.Code != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, rec.Code, tc.want)
		}
	}
}

func TestGetPodcastSerializesEpisodeAndTrack(t *testing.T) {
	r := newRig(t)
	item := decode[map[string]any](t, r.do(rootUser, "GET", "/api/items/ip?expanded=1", ""))
	episodes := item["media"].(map[string]any)["episodes"].([]any)
	if len(episodes) != 1 {
		t.Fatalf("episodes = %v", episodes)
	}
	ep := episodes[0].(map[string]any)
	track := ep["audioTrack"].(map[string]any)
	if ep["guid"] != "guid-e1" || track["contentUrl"] != "/api/items/ip/file/ep-audio" || track["index"] != float64(1) {
		t.Fatalf("episode = %v", ep)
	}
}

type queue struct{}

func (queue) EpisodeDownloads(string) ([]wire.EpisodeDownload, *wire.EpisodeDownload) {
	return []wire.EpisodeDownload{{ID: "q1"}}, &wire.EpisodeDownload{ID: "now"}
}

func TestGetIncludes(t *testing.T) {
	r := newRig(t)
	r.h.deps.Downloads = queue{}
	r.exec(`INSERT INTO feeds(id,slug,entityType,entityId,feedURL,title,preventIndexing,ownerName,createdAt,updatedAt) VALUES('feed1','f','libraryItem','i1','/feed/f','Alpha',1,'Owner',?,?)`, ts, ts)
	r.exec(`INSERT INTO mediaItemShares(id,mediaItemId,mediaItemType,slug,isDownloadable,createdAt,updatedAt) VALUES('sh1','b1','book','one',1,?,?),('sh2','b2','book','two',0,?,?)`, ts, ts, ts, ts)
	r.exec(`UPDATE mediaItemShares SET expiresAt='2000-01-01 00:00:00.000 +00:00' WHERE id='sh2'`)
	r.exec(`INSERT INTO mediaProgresses(id,mediaItemId,mediaItemType,duration,currentTime,isFinished,extraData,userId,createdAt,updatedAt) VALUES('mp1','b1','book',10,5,0,'{"libraryItemId":"i1","progress":0.5}','u1',?,?)`, ts, ts)
	get := func(user_ string, id, query string) map[string]any {
		t.Helper()
		u := rootUser
		if user_ == "admin" {
			u = adminUser
		}
		rec := r.do(u, "GET", "/api/items/"+id+"?expanded=1&include="+query, "")
		if rec.Code != 200 {
			t.Fatalf("%s %s: %d", id, query, rec.Code)
		}
		return decode[map[string]any](t, rec)
	}
	i1 := get("root", "i1", "progress,rssfeed,share")
	if p := i1["userMediaProgress"].(map[string]any); p["libraryItemId"] != "i1" || p["progress"] != 0.5 || p["currentTime"] != float64(5) {
		t.Errorf("progress = %v", p)
	}
	if f := i1["rssFeed"].(map[string]any); f["feedUrl"] != "/feed/f" || f["meta"].(map[string]any)["preventIndexing"] != true {
		t.Errorf("rssFeed = %v", f)
	}
	if s := i1["mediaItemShare"].(map[string]any); s["slug"] != "one" || s["isDownloadable"] != true {
		t.Errorf("share = %v", s)
	}
	// Without a feed, a progress row, or an open share the keys are explicit nulls.
	i2 := get("root", "i2", "progress,rssfeed,share")
	for _, key := range []string{"userMediaProgress", "rssFeed", "mediaItemShare"} {
		if v, ok := i2[key]; !ok || v != nil {
			t.Errorf("i2 %s = %v (present %v), want null", key, v, ok)
		}
	}
	// Only admins see shares; names are case-sensitive and split on commas.
	if _, ok := decode[map[string]any](t, r.do(readerUser, "GET", "/api/items/i1?expanded=1&include=share", ""))["mediaItemShare"]; ok {
		t.Error("non-admin item carries mediaItemShare")
	}
	if _, ok := get("root", "i1", "RSSFEED")["rssFeed"]; ok {
		t.Error("include names are case-sensitive")
	}
	pod := get("root", "ip", "downloads")
	if q := pod["episodeDownloadsQueued"].([]any); len(q) != 1 {
		t.Errorf("queued = %v", q)
	}
	if d := pod["episodesDownloading"].([]any); len(d) != 1 {
		t.Errorf("downloading = %v", d)
	}
}
