package library

import (
	"encoding/json"
	"net/http"
	"reflect"
	"sort"
	"testing"
)

func TestSeriesFiltersAndIncludesReturnBooksProgressAndFeed(t *testing.T) {
	rig := newSeriesRig(t)
	const now = "2024-01-01 00:00:00.000 +00:00"
	if _, err := rig.db.Exec(`INSERT INTO feeds(id,entityType,entityId,feedURL,title,description,preventIndexing,ownerName,ownerEmail,createdAt,updatedAt) VALUES('feed-s','series','s1','https://example.test/rss','Feed','Description',0,'Owner','owner@example.test','` + now + `','` + now + `')`); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.db.Exec(`INSERT INTO mediaProgresses(id,userId,mediaItemId,isFinished,currentTime,ebookProgress,updatedAt,createdAt) VALUES('mp1','u1','b1',0,1,0,'2024-01-03 00:00:00.000 +00:00','2024-01-03 00:00:00.000 +00:00')`); err != nil {
		t.Fatal(err)
	}
	user := seriesRootUser()
	user.ID = "u1"
	w := rig.do("GET", "/api/libraries/l1/series?filter=genres.ZmljdGlvbg==&include=rssfeed", "", user)
	if w.Code != http.StatusOK {
		t.Fatalf("series status=%d body=%s", w.Code, w.Body.String())
	}
	var list map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	entry := list["results"].([]any)[0].(map[string]any)
	if entry["rssFeed"].(map[string]any)["feedUrl"] != "https://example.test/rss" {
		t.Fatalf("series include=%s", w.Body.String())
	}
	w = rig.do("GET", "/api/libraries/l1/series/s1?include=progress,rssfeed", "", user)
	if w.Code != http.StatusOK {
		t.Fatalf("series detail status=%d body=%s", w.Code, w.Body.String())
	}
	var detail map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	progress := detail["progress"].(map[string]any)
	if progress["isFinished"] != false || len(progress["libraryItemIds"].([]any)) != 2 || len(progress["libraryItemIdsFinished"].([]any)) != 0 {
		t.Fatalf("series progress=%#v", progress)
	}
}

func TestSeriesBooksUseMinifiedBookMediaShape(t *testing.T) {
	rig := newSeriesRig(t)
	user := seriesRootUser()
	user.ID = "u1"
	w := rig.do(http.MethodGet, "/api/libraries/l1/series", "", user)
	if w.Code != http.StatusOK {
		t.Fatalf("series status=%d body=%s", w.Code, w.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	series := payload["results"].([]any)[0].(map[string]any)
	item := series["books"].([]any)[0].(map[string]any)
	media := item["media"].(map[string]any)
	wantMediaKeys := []string{"coverPath", "duration", "id", "metadata", "numAudioFiles", "numChapters", "numTracks", "size", "tags"}
	gotMediaKeys := make([]string, 0, len(media))
	for key := range media {
		gotMediaKeys = append(gotMediaKeys, key)
	}
	sort.Strings(gotMediaKeys)
	if !reflect.DeepEqual(gotMediaKeys, wantMediaKeys) {
		t.Fatalf("series book media keys=%v want %v", gotMediaKeys, wantMediaKeys)
	}
	if _, ok := media["ebookFormat"]; ok {
		t.Error("series book includes ebookFormat when its ebookFile is null")
	}
	metadata := media["metadata"].(map[string]any)
	if _, ok := metadata["descriptionPlain"]; ok {
		t.Error("series book minified metadata contains descriptionPlain")
	}
	if metadata["description"] != "<p>A story</p>" {
		t.Errorf("minified description=%#v", metadata["description"])
	}
	for _, key := range []string{"titleIgnorePrefix", "authorName", "authorNameLF", "narratorName", "seriesName", "genres"} {
		if _, ok := metadata[key]; !ok {
			t.Errorf("series book minified metadata missing %q", key)
		}
	}
	if _, ok := media["libraryItemId"]; ok {
		t.Error("series book minified media contains libraryItemId")
	}
	if _, ok := media["tracks"]; ok {
		t.Error("series book minified media contains tracks")
	}
	for _, key := range []string{"audioFiles", "chapters", "ebookFile"} {
		if _, ok := media[key]; ok {
			t.Errorf("series book minified media contains %q", key)
		}
	}
}

func TestSeriesPaginationUsesPageThenLimit(t *testing.T) {
	rig := newSeriesRig(t)
	const now = "2024-01-01 00:00:00.000 +00:00"
	for _, q := range []string{
		`INSERT INTO series(id,name,nameIgnorePrefix,description,libraryId,createdAt,updatedAt) VALUES('s2','Second Series','Second Series',NULL,'l1','` + now + `','` + now + `')`,
		`INSERT INTO bookSeries(id,sequence,bookId,seriesId,createdAt) VALUES('bs3','2','b1','s2','` + now + `')`,
		`INSERT INTO series(id,name,nameIgnorePrefix,description,libraryId,createdAt,updatedAt) VALUES('s3','Third Series','Third Series',NULL,'l1','` + now + `','` + now + `')`,
		`INSERT INTO bookSeries(id,sequence,bookId,seriesId,createdAt) VALUES('bs4','3','b1','s3','` + now + `')`,
		`INSERT INTO series(id,name,nameIgnorePrefix,description,libraryId,createdAt,updatedAt) VALUES('s4','Fourth Series','Fourth Series',NULL,'l1','` + now + `','` + now + `')`,
		`INSERT INTO bookSeries(id,sequence,bookId,seriesId,createdAt) VALUES('bs5','4','b1','s4','` + now + `')`,
	} {
		if _, err := rig.db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	w := rig.do(http.MethodGet, "/api/libraries/l1/series?page=1&limit=2", "", seriesRootUser())
	if w.Code != http.StatusOK {
		t.Fatalf("series status=%d body=%s", w.Code, w.Body.String())
	}
	var payload struct {
		Results []map[string]any `json:"results"`
		Total   int              `json:"total"`
		Page    int              `json:"page"`
		Limit   int              `json:"limit"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Total != 4 || payload.Page != 1 || payload.Limit != 2 || len(payload.Results) != 2 || payload.Results[0]["id"] != "s3" || payload.Results[1]["id"] != "s4" {
		t.Fatalf("unexpected pagination response: %+v", payload)
	}
}
