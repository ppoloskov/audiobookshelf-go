package db_test

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/dbtest"
)

// roundTrip decodes text into a T, encodes it again and compares by value.
// Unknown keys fail the decode, so a key upstream writes cannot go unnoticed.
func roundTrip[T any](t *testing.T, name, text string) {
	t.Helper()
	var doc T
	dec := json.NewDecoder(bytes.NewReader([]byte(text)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		t.Errorf("%s: decode: %v", name, err)
		return
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Errorf("%s: encode: %v", name, err)
		return
	}
	var want, got any
	if err := json.Unmarshal([]byte(text), &want); err != nil {
		t.Fatalf("%s: bad test text: %v", name, err)
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Errorf("%s: round trip changed the document\n in:  %s\n out: %s", name, text, out)
	}
}

func TestDocumentRoundTrips(t *testing.T) {
	file := `{"filename":"a.m4b","ext":".m4b","path":"/a/a.m4b","relPath":"a.m4b","size":10,"mtimeMs":1793709979586.1235,"ctimeMs":1,"birthtimeMs":2}`

	roundTrip[db.LibraryFile](t, "library file", `{"ino":"1","metadata":`+file+`,"isSupplementary":false,"addedAt":1,"updatedAt":2,"fileType":"audio"}`)
	roundTrip[db.LibraryFile](t, "library file, null supplementary", `{"ino":"1","metadata":`+file+`,"isSupplementary":null,"addedAt":1,"updatedAt":2,"fileType":"audio"}`)

	// The two shapes the scanner writes for books.ebookFile.
	roundTrip[db.EbookFile](t, "ebook from library file", `{"ino":"1","metadata":`+file+`,"isSupplementary":null,"addedAt":1,"updatedAt":2,"fileType":"ebook","ebookFormat":"epub"}`)
	roundTrip[db.EbookFile](t, "ebook with supplementary false", `{"ino":"1","metadata":`+file+`,"isSupplementary":false,"addedAt":1,"updatedAt":2,"fileType":"ebook","ebookFormat":"epub"}`)
	roundTrip[db.EbookFile](t, "ebook from EBookFile.toJSON", `{"ino":"1","metadata":`+file+`,"ebookFormat":"epub","addedAt":1,"updatedAt":2}`)

	audio := `{"index":1,"ino":"1","metadata":` + file + `,"addedAt":1,"updatedAt":2,"trackNumFromMeta":3,"discNumFromMeta":null,"trackNumFromFilename":null,"discNumFromFilename":1,` +
		`"manuallyVerified":true,"exclude":false,"error":"x","format":"MP3","duration":1.5,"bitRate":64000,"language":null,"codec":"mp3","timeBase":"1/1","channels":2,` +
		`"channelLayout":"stereo","chapters":[{"id":0,"start":0,"end":1.5,"title":"T"}],"embeddedCoverArt":null,"metaTags":{"tagASIN":"B0","tagMusicBrainzArtistId":"x"},"mimeType":"audio/mpeg"}`
	roundTrip[db.AudioFile](t, "audio file", audio)
	roundTrip[db.AudioFile](t, "audio file, empty tags and null duration", `{"index":1,"ino":"1","metadata":`+file+`,"addedAt":1,"updatedAt":2,"trackNumFromMeta":null,"discNumFromMeta":null,"trackNumFromFilename":null,"discNumFromFilename":null,"manuallyVerified":false,"exclude":false,"error":null,"format":"","duration":null,"bitRate":null,"language":null,"codec":null,"timeBase":null,"channels":null,"channelLayout":null,"chapters":[],"embeddedCoverArt":null,"metaTags":{},"mimeType":""}`)

	perms := `{"download":true,"update":false,"delete":false,"upload":true,"createEreader":false,"accessAllLibraries":false,"accessAllTags":true,"accessExplicitContent":true,"selectedTagsNotAccessible":true,"librariesAccessible":["l1"],"itemTagsSelected":[]}`
	roundTrip[db.Permissions](t, "permissions", perms)
	roundTrip[db.Bookmark](t, "bookmark", `{"libraryItemId":"li","time":12.5,"title":"t","createdAt":1793709987082}`)

	roundTrip[db.UserExtra](t, "user extra", `{"seriesHideFromContinueListening":[]}`)
	roundTrip[db.UserExtra](t, "user extra, openid", `{"authOpenIDSub":"s","seriesHideFromContinueListening":["a"]}`)
	roundTrip[db.UserExtra](t, "user extra, migrated", `{"seriesHideFromContinueListening":[],"oldUserId":"usr_1"}`)
	roundTrip[db.UserExtra](t, "user extra, empty", `{}`)

	roundTrip[db.LibrarySettings](t, "book library settings", `{"coverAspectRatio":1,"disableWatcher":false,"autoScanCronExpression":"0 * * * *","skipMatchingMediaWithAsin":true,"skipMatchingMediaWithIsbn":false,"audiobooksOnly":false,"epubsAllowScriptedContent":false,"hideSingleBookSeries":false,"onlyShowLaterBooksInContinueSeries":false,"metadataPrecedence":["folderStructure"],"markAsFinishedPercentComplete":null,"markAsFinishedTimeRemaining":10}`)
	roundTrip[db.LibrarySettings](t, "podcast library settings", `{"coverAspectRatio":1,"disableWatcher":false,"autoScanCronExpression":null,"podcastSearchRegion":"us","markAsFinishedPercentComplete":95.5,"markAsFinishedTimeRemaining":null}`)
	roundTrip[db.LibrarySettings](t, "book library, empty precedence", `{"coverAspectRatio":0,"disableWatcher":true,"autoScanCronExpression":null,"metadataPrecedence":[],"markAsFinishedPercentComplete":null,"markAsFinishedTimeRemaining":10}`)
	roundTrip[db.LibraryExtra](t, "library extra", `{"lastScanMetadataPrecedence":["a"],"oldLibraryId":"lib_1"}`)
	roundTrip[db.LibraryItemExtra](t, "item extra", `{}`)
	roundTrip[db.LibraryItemExtra](t, "item extra, migrated", `{"oldLibraryItemId":"li_1"}`)
	roundTrip[db.EpisodeExtra](t, "episode extra", `{"guid":"g","oldEpisodeId":"ep_1"}`)
	roundTrip[db.ProgressExtra](t, "progress extra", `{"libraryItemId":"li","progress":0}`)
	roundTrip[db.SessionExtra](t, "session extra", `{"libraryItemId":"li"}`)
	roundTrip[db.DeviceExtra](t, "device extra", `{"manufacturer":"Google","model":"Pixel 8","osName":"Android","osVersion":"14","browserName":"Chrome"}`)
	roundTrip[db.DeviceExtra](t, "device extra, empty", `{}`)

	roundTrip[db.SessionBookMetadata](t, "book session metadata", `{"title":"T","subtitle":null,"authors":[{"id":"a","name":"A"}],"narrators":[],"series":[{"id":"s","name":"S","sequence":"1"}],"genres":[],"publishedYear":null,"publishedDate":null,"publisher":null,"description":null,"isbn":null,"asin":null,"language":null,"explicit":false,"abridged":false}`)
	roundTrip[db.SessionPodcastMetadata](t, "podcast session metadata", `{"title":"T","author":"A","description":null,"releaseDate":null,"genres":["Technology"],"feedUrl":null,"imageUrl":null,"itunesPageUrl":null,"itunesId":null,"itunesArtistId":null,"explicit":false,"language":null,"type":"episodic"}`)
}

// TestJSONValueKeepsHTMLCharacters: upstream filters search JSON columns with
// LIKE, and JSON.stringify does not escape <, > or &.
func TestJSONValueKeepsHTMLCharacters(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	book := db.Book{ID: "b1", CreatedAt: db.Now(), UpdatedAt: db.Now(), Genres: db.JSON[[]string]{V: []string{"Rock & Roll", "<b>"}}}
	if err := d.Insert(ctx, "books", book); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := d.Get(ctx, &stored, "SELECT genres FROM books"); err != nil || stored != `["Rock & Roll","<b>"]` {
		t.Fatalf("stored genres = %q, %v", stored, err)
	}
}
