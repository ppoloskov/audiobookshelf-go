package catalog

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/store"
)

// includesFixture extends the sub-series fixture with an RSS feed on item A
// (entityType 'libraryItem', entityId the library item id, as
// Feed.js:416-421 associates it), an open permanent share of book B and an
// expired share of book C. All items were added just now so the
// recently-added shelf contains them.
func includesFixture(t *testing.T) *Handler {
	t.Helper()
	h, _ := subseriesFixture(t)
	now := dbtime.Format(time.Now())
	past := dbtime.Format(time.Now().Add(-time.Hour))
	for _, q := range []string{
		`UPDATE libraryItems SET createdAt='` + now + `'`,
		`INSERT INTO feeds(id,slug,entityType,entityId,feedURL,title,description,preventIndexing,ownerName,ownerEmail,createdAt,updatedAt) VALUES('feed1','a-feed','libraryItem','liA','/feed/a-feed','Alpha',NULL,1,'Owner','o@example.com','` + now + `','` + now + `')`,
		`INSERT INTO mediaItemShares(id,mediaItemId,mediaItemType,slug,isDownloadable,createdAt,updatedAt) VALUES('share1','B','book','b-share',1,'` + now + `','` + now + `')`,
		`INSERT INTO mediaItemShares(id,mediaItemId,mediaItemType,slug,expiresAt,isDownloadable,createdAt,updatedAt) VALUES('share2','C','book','c-share','` + past + `',0,'` + now + `','` + now + `')`,
	} {
		if _, err := h.db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	return h
}

type includedItem struct {
	ID        string          `json:"id"`
	RSSFeed   json.RawMessage `json:"rssFeed"`
	ItemShare json.RawMessage `json:"mediaItemShare"`
}

func includesOf(t *testing.T, items []includedItem) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, it := range items {
		mark := ""
		if len(it.RSSFeed) > 0 {
			mark += "F"
		}
		if len(it.ItemShare) > 0 {
			mark += "S"
		}
		out[it.ID] = mark
	}
	return out
}

// TestIncludeRSSFeedAndShareOnItemsAndShelves covers libraryItemsBookFilters'
// include handling: the feed is found by library item id (the page used to
// look for entityType "book" and the media id and never found one), the share
// is only attached for admins and only while open, and the shelves apply the
// same include as the library page.
func TestIncludeRSSFeedAndShareOnItemsAndShelves(t *testing.T) {
	h := includesFixture(t)
	admin := store.User{ID: "u1", Type: "admin", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":true}`)}
	member := admin
	member.Type = "user"
	get := func(user store.User, path string) []byte {
		t.Helper()
		w := httptest.NewRecorder()
		if !h.Handle(w, httptest.NewRequest(http.MethodGet, path, nil), user) || w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	page := func(user store.User, query string) map[string]string {
		var body struct {
			Results []includedItem `json:"results"`
		}
		if err := json.Unmarshal(get(user, "/api/libraries/lib1/items?minified=1"+query), &body); err != nil {
			t.Fatal(err)
		}
		return includesOf(t, body.Results)
	}
	if got := page(admin, "&include=rssfeed,share"); got["liA"] != "F" || got["liB"] != "S" || got["liC"] != "" || got["liD"] != "" {
		t.Errorf("admin page includes = %v", got)
	}
	if got := page(member, "&include=rssfeed,share"); got["liA"] != "F" || got["liB"] != "" {
		t.Errorf("member page includes = %v", got)
	}
	if got := page(admin, ""); got["liA"] != "" || got["liB"] != "" {
		t.Errorf("no include = %v", got)
	}

	var shelves []struct {
		ID       string         `json:"id"`
		Entities []includedItem `json:"entities"`
	}
	if err := json.Unmarshal(get(admin, "/api/libraries/lib1/personalized?minified=1&include=rssfeed,share"), &shelves); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, shelf := range shelves {
		if shelf.ID != "recently-added" {
			continue
		}
		found = true
		if got := includesOf(t, shelf.Entities); got["liA"] != "F" || got["liB"] != "S" || got["liC"] != "" {
			t.Errorf("recently-added includes = %v", got)
		}
	}
	if !found {
		t.Fatalf("no recently-added shelf in %s", get(admin, "/api/libraries/lib1/personalized?minified=1"))
	}

	// GET /api/items/:id?expanded=1&include=share: null without a share.
	var item map[string]json.RawMessage
	if err := json.Unmarshal(get(admin, "/api/items/liB?expanded=1&include=share"), &item); err != nil || len(item["mediaItemShare"]) < 3 {
		t.Errorf("item with share: %v %s", err, item["mediaItemShare"])
	}
	if err := json.Unmarshal(get(admin, "/api/items/liD?expanded=1&include=share"), &item); err != nil || string(item["mediaItemShare"]) != "null" {
		t.Errorf("item without share: %v %s", err, item["mediaItemShare"])
	}
	var memberItem map[string]json.RawMessage
	if err := json.Unmarshal(get(member, "/api/items/liB?expanded=1&include=share"), &memberItem); err != nil {
		t.Fatal(err)
	}
	if _, present := memberItem["mediaItemShare"]; present {
		t.Errorf("non-admin item carries mediaItemShare")
	}
}
