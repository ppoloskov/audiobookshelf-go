package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/wire"
)

// upstream answers every provider with a recorded-shape body.
func upstream(t *testing.T, customURL *string) http.Handler {
	write := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, q := r.URL.Path, r.URL.Query()
		switch {
		case p == "/google":
			write(w, `{"items":[
{"id":"g1","volumeInfo":{"title":"Full Book","subtitle":"Sub","authors":["A One","B Two"],"publisher":"Pub","publishedDate":"2020-03-01","description":"<b>Desc</b> text","industryIdentifiers":[{"type":"ISBN_10","identifier":"0000000001"},{"type":"ISBN_13","identifier":"9780000000001"}],"categories":["Fiction","Fantasy"],"imageLinks":{"smallThumbnail":"http://i.test/s.jpg","large":"http://i.test/l.jpg"}}},
{"id":"g2","volumeInfo":{"title":"Sparse","industryIdentifiers":[{"type":"ISBN_10","identifier":"0000000002"}],"imageLinks":{"zzz":"http://i.test/z.jpg"}}},
{"id":"g3","volumeInfo":{"subtitle":"no title"}}]}`)
		case p == "/itunes" && q.Get("entity") == "audiobook":
			write(w, `{"results":[
{"collectionId":111,"artistId":222,"collectionName":"iTunes Book","artistName":"X & Y","description":"<p>Hello</p>","releaseDate":"2019-05-06T07:00:00Z","primaryGenreName":"Mystery","artworkUrl100":"https://a.test/100x100bb.jpg","artworkUrl60":"https://a.test/60x60bb.jpg"},
{"collectionId":112,"collectionName":"Plain","artistName":"Solo","artworkUrl600":"https://a.test/600.jpg"}]}`)
		case p == "/itunes" && q.Get("entity") == "podcast":
			write(w, `{"results":[
{"collectionId":5,"artistId":6,"collectionName":"Pod","artistName":"Host","description":"<i>About</i> it","releaseDate":"2021-01-02T00:00:00Z","genres":["Tech","News"],"artworkUrl600":"https://a.test/p600.jpg","trackCount":42,"feedUrl":"https://feed.test/rss","collectionViewUrl":"https://pod.test/5","trackExplicitness":"explicit"},
{"collectionId":7,"collectionName":"Bare","artworkUrl30":"https://a.test/30x30bb.jpg","artworkUrl100":"https://a.test/100x100bb.jpg"}]}`)
		case p == "/openlibrary/search.json":
			write(w, `{"docs":[
{"title":"The Hobbit","author_name":["Tolkien","Other"],"first_publish_year":1937,"cover_edition_key":"OL1M","key":"/works/OL1W"},
{"title":"Second","key":"/works/OL2W"},
{"title":"nokey"}]}`)
		case p == "/openlibrary/works/OL1W.json":
			write(w, `{"covers":[12,-1,34],"first_publish_date":"1937-09-21","description":{"type":"/type/text","value":"Obj <b>desc</b>"}}`)
		case p == "/openlibrary/works/OL2W.json":
			write(w, `{"first_publish_date":"1999-01","description":"Plain desc"}`)
		case p == "/abc":
			write(w, `[{"versions":{"png":{"original":"https://c.test/1.png"}}},{"versions":{"png":{"original":""}}},{"versions":{"png":{"original":"https://c.test/2.png"}}}]`)
		case p == "/fantlab/search-works":
			write(w, `[{"work_id":12,"work_type_id":1},{"work_id":13,"work_type_id":"1"},{"work_id":14,"work_type_id":7},{"work_type_id":1},{"work_id":15,"work_type_id":1}]`)
		case p == "/fantlab/work/12/extended":
			write(w, `{"work_id":12,"work_name":"Fant Title","work_name_alts":["Alt A","Alt B"],"authors":[{"name":" Writer One "},{"name":""},{"name":"Writer Two"}],"work_year":2021,"work_description":"Fant desc","image":"/img/w.jpg","classificatory":{"genre_group":[{"genre_group_id":2,"genre":[{"label":"Skip"}]},{"genre_group_id":1,"genre":[{"label":"Fantasy","genre":[{"label":"Epic"},{"label":""},{"label":"Heroic"}]},{"label":"ignored"}]}]},"editions_blocks":{"30":{"list":[{"isbn":"111","edition_id":1},{"isbn":"978-5","edition_id":9}]},"10":{"list":[{"isbn":"nope","edition_id":2}]}}}`)
		case p == "/fantlab/edition/9":
			write(w, `{"image":"/img/e.jpg"}`)
		case p == "/fantlab/work/13/extended":
			write(w, `{"work_id":13,"work_name":"Second Fant","authors":[],"work_year":"2005","editions_blocks":{"10":{"list":[{"isbn":"","edition_id":""}]}},"classificatory":[]}`)
		case p == "/fantlab/work/15/extended":
			write(w, `{}`)
		case p == "/audible/1.0/catalog/products":
			write(w, `{"products":[{"asin":"b000000001"},{"asin":"short"},{"asin":"B000000404"},{"asin":"B000000002"}]}`)
		case p == "/nex/books/B000000001", p == "/nex/books/B000000002":
			asin := strings.TrimPrefix(p, "/nex/books/")
			if asin == "B000000001" {
				write(w, `{"asin":"B000000001","title":"Audible One","subtitle":"Sub","authors":[{"name":"Auth A"},{"name":"Auth B"}],"narrators":[{"name":"Nar"}],"publisherName":"PubCo","releaseDate":"2022-01-01T00:00:00.000Z","runtimeLengthMin":125,"summary":"<p>Sum</p>","image":"https://img.test/a.jpg","isbn":"9781","formatType":"abridged","isAdult":true,"language":"english","region":"us","rating":"4.5","genres":[{"type":"genre","name":"Mystery"},{"type":"genre","name":"Mystery"},{"type":"tag","name":"Classic"},{"type":"tag","name":"Classic"},{"type":"other","name":"x"},{"type":"genre","name":""}],"seriesPrimary":{"name":"Saga","position":"Book 1.5"},"seriesSecondary":{"name":"Other","position":""}}`)
			} else {
				write(w, `{"asin":"B000000002","title":"Audible Two","authors":[],"runtimeLengthMin":"30.5","region":"uk","formatType":"unabridged"}`)
			}
		case p == "/nex/authors" && q.Get("name") != "":
			write(w, `[{"asin":"B0FAR00001","name":"Totally Different"},{"asin":"B0AUTHOR01","name":"Jane Doe"}]`)
		case p == "/nex/authors/B0AUTHOR01":
			write(w, `{"asin":"B0AUTHOR01","name":"Jane Doe","description":"Bio","image":"https://img.test/jane.jpg"}`)
		case p == "/nex/books/B000000009/chapters":
			write(w, `{"asin":"B000000009","chapters":[{"lengthMs":1000,"startOffsetMs":0,"title":"One"}]}`)
		case p == "/custom/search":
			write(w, `{"matches":[{"title":42,"subtitle":null,"author":["A","B"],"narrator":"N","publisher":7,"publishedYear":2001,"description":"<p>Safe <script>bad</script><a href=\"javascript:bad\">link</a></p>","genres":"Mystery, Mystery, Thriller","tags":[" Classic ","Classic"],"series":[{"series":"Saga","sequence":2},{"series":"Other","sequence":"3"},{"sequence":false},"junk"],"duration":"12.5","cover":"https://img.test/cover.jpg","isbn":"123","asin":["B","C"],"language":"en","id":"dropped","explicit":true,"drop":{"x":1}},{"title":"Second","genres":[1,2],"series":"x","duration":"abc","tags":"","cover":{"a":1}},{"title":"Third","duration":7}]}`)
		default:
			http.NotFound(w, r)
		}
	})
}

func characterisationHandler(t *testing.T) *Handler {
	srv := httptest.NewServer(upstream(t, nil))
	t.Cleanup(srv.Close)
	db := providersTestDB(t)
	now := dbNow()
	if _, err := db.Exec(`INSERT INTO customMetadataProviders(id,name,mediaType,url,createdAt,updatedAt) VALUES('c1','Local','book',?,?,?)`, srv.URL+"/custom", now, now); err != nil {
		t.Fatal(err)
	}
	h, err := New(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.SetEndpoint("google", srv.URL+"/google")
	h.SetEndpoint("itunes", srv.URL+"/itunes")
	h.SetEndpoint("openlibrary", srv.URL+"/openlibrary")
	h.SetEndpoint("audiobookcovers", srv.URL+"/abc/")
	h.SetEndpoint("fantlab", srv.URL+"/fantlab")
	h.SetEndpoint("audible", srv.URL+"/audible/1.0/catalog/products")
	h.SetEndpoint("audnexus", srv.URL+"/nex")
	return h
}

// TestProviderOutputs checks every provider's normalized output for the
// recorded upstream bodies in upstream. The expected values are written out by
// hand from those bodies and the TS providers (server/providers/*.js); nothing
// is regenerated from the code under test. One subtest per provider call.
func TestProviderOutputs(t *testing.T) {
	h := characterisationHandler(t)
	for _, c := range []struct {
		name, path string
		status     int
		want       string
	}{
		{"books/google", "/api/search/books?provider=google&title=Title&author=Writer", 200, `[{"author":"A One, B Two","cover":"https://i.test/l.jpg","description":"<b>Desc</b> text","descriptionPlain":"Desc text","genres":["Fiction","Fantasy"],"id":"g1","isbn":"9780000000001","publishedYear":"2020","publisher":"Pub","subtitle":"Sub","title":"Full Book"},{"cover":"https://i.test/z.jpg","id":"g2","isbn":"0000000002","title":"Sparse"}]`},
		{"books/itunes", "/api/search/books?provider=itunes&title=Title&author=Writer", 200, `[{"artistId":222,"author":"X, Y","cover":"https://a.test/600x600bb.jpg","description":"<p>Hello</p>","descriptionPlain":"Hello","genres":["Mystery"],"id":111,"publishedYear":"2019","title":"iTunes Book"},{"author":"Solo","cover":"https://a.test/600.jpg","id":112,"title":"Plain"}]`},
		{"books/openlibrary", "/api/search/books?provider=openlibrary&title=Title&author=Writer", 200, `[{"author":"Tolkien, Other","cover":"https://covers.openlibrary.org/b/OLID/OL1M-L.jpg","covers":["https://covers.openlibrary.org/b/id/12-L.jpg","https://covers.openlibrary.org/b/id/34-L.jpg"],"description":"Obj <b>desc</b>","descriptionPlain":"Obj desc","edition":"OL1M","first_publish_date":"1937-09-21","id":"OL1W","key":"/works/OL1W","publishedYear":"1937","title":"The Hobbit"},{"description":"Plain desc","descriptionPlain":"Plain desc","first_publish_date":"1999-01","id":"OL2W","key":"/works/OL2W","publishedYear":"1999","title":"Second"}]`},
		{"books/audiobookcovers", "/api/search/books?provider=audiobookcovers&title=Title&author=Writer", 200, `[{"cover":"https://c.test/1.png"},{"cover":"https://c.test/2.png"}]`},
		{"books/fantlab", "/api/search/books?provider=fantlab&title=Title&author=Writer", 200, `[{"author":"Writer One, Writer Two","cover":"https://fantlab.ru/img/e.jpg","description":"Fant desc","descriptionPlain":"Fant desc","genres":["Fantasy","Epic","Heroic"],"id":12,"isbn":"978-5","publishedYear":2021,"subtitle":"Alt A","title":"Fant Title"},{"id":13,"publishedYear":"2005","title":"Second Fant"}]`},
		{"books/audible", "/api/search/books?provider=audible&title=Title&author=Writer", 200, `[{"abridged":true,"asin":"B000000001","author":"Auth A, Auth B","cover":"https://img.test/a.jpg","description":"<p>Sum</p>","descriptionPlain":"Sum","duration":125,"explicit":true,"genres":["Mystery"],"isbn":"9781","language":"English","narrator":"Nar","publishedYear":"2022","publisher":"PubCo","rating":"4.5","region":"us","series":[{"sequence":"1.5","series":"Saga"},{"series":"Other"}],"subtitle":"Sub","tags":["Classic"],"title":"Audible One"},{"abridged":false,"asin":"B000000002","duration":30.5,"explicit":false,"region":"uk","title":"Audible Two"}]`},
		{"books/audible.uk", "/api/search/books?provider=audible.uk&title=Title&author=Writer", 200, `[{"abridged":true,"asin":"B000000001","author":"Auth A, Auth B","cover":"https://img.test/a.jpg","description":"<p>Sum</p>","descriptionPlain":"Sum","duration":125,"explicit":true,"genres":["Mystery"],"isbn":"9781","language":"English","narrator":"Nar","publishedYear":"2022","publisher":"PubCo","rating":"4.5","region":"us","series":[{"sequence":"1.5","series":"Saga"},{"series":"Other"}],"subtitle":"Sub","tags":["Classic"],"title":"Audible One"},{"abridged":false,"asin":"B000000002","duration":30.5,"explicit":false,"region":"uk","title":"Audible Two"}]`},
		{"books/custom-c1", "/api/search/books?provider=custom-c1&title=Title&author=Writer", 200, `[{"asin":"B,C","author":"A,B","cover":"https://img.test/cover.jpg","description":"<p>Safe <a>link</a></p>","descriptionPlain":"Safe link","duration":12.5,"genres":["Mystery","Thriller"],"isbn":"123","language":"en","narrator":"N","publishedYear":"2001","publisher":"7","series":[{"sequence":"2","series":"Saga"},{"sequence":"3","series":"Other"}],"tags":["Classic"],"title":"42"},{"title":"Second"},{"duration":7,"title":"Third"}]`},
		{"books/audible-asin", "/api/search/books?provider=audible&title=x&asin=b000000002&region=uk", 200, `[{"abridged":false,"asin":"B000000002","duration":30.5,"explicit":false,"region":"uk","title":"Audible Two"}]`},
		{"books/audible-title-asin", "/api/search/books?provider=audible&title=B000000001", 200, `[{"abridged":true,"asin":"B000000001","author":"Auth A, Auth B","cover":"https://img.test/a.jpg","description":"<p>Sum</p>","descriptionPlain":"Sum","duration":125,"explicit":true,"genres":["Mystery"],"isbn":"9781","language":"English","narrator":"Nar","publishedYear":"2022","publisher":"PubCo","rating":"4.5","region":"us","series":[{"sequence":"1.5","series":"Saga"},{"series":"Other"}],"subtitle":"Sub","tags":["Classic"],"title":"Audible One"}]`},
		{"books/empty-title", "/api/search/books?provider=google", 200, `[]`},
		{"podcast", "/api/search/podcast?term=pod&country=us", 200, `[{"artistId":6,"artistName":"Host","cover":"https://a.test/p600.jpg","description":"<i>About</i> it","descriptionPlain":"About it","explicit":true,"feedUrl":"https://feed.test/rss","genres":["Tech","News"],"id":5,"pageUrl":"https://pod.test/5","releaseDate":"2021-01-02T00:00:00Z","title":"Pod","trackCount":42},{"artistId":null,"artistName":null,"cover":"https://a.test/600x600bb.jpg","description":"","descriptionPlain":"","explicit":false,"feedUrl":null,"genres":[],"id":7,"pageUrl":null,"releaseDate":null,"title":"Bare","trackCount":null}]`},
		{"covers/best", "/api/search/covers?title=Title&author=Writer&provider=all", 200, `{"results":["https://i.test/l.jpg","https://i.test/z.jpg","https://a.test/600x600bb.jpg","https://a.test/600.jpg","https://covers.openlibrary.org/b/id/12-L.jpg","https://covers.openlibrary.org/b/id/34-L.jpg","https://covers.openlibrary.org/b/OLID/OL1M-L.jpg","https://fantlab.ru/img/e.jpg","https://c.test/1.png","https://c.test/2.png","https://img.test/a.jpg"]}`},
		{"covers/podcast", "/api/search/covers?title=Pod&podcast=1", 200, `{"results":["https://a.test/p600.jpg","https://a.test/600x600bb.jpg"]}`},
		{"covers/custom", "/api/search/covers?title=Title&provider=custom-c1", 200, `{"results":["https://img.test/cover.jpg"]}`},
		{"authors", "/api/search/authors?q=Jane+Doe", 200, `{"asin":"B0AUTHOR01","description":"Bio","image":"https://img.test/jane.jpg","name":"Jane Doe"}`},
		{"authors/none", "/api/search/authors?q=Zzzzzz", 200, `null`},
		{"chapters", "/api/search/chapters?asin=b000000009", 200, `{"asin":"B000000009","chapters":[{"lengthMs":1000,"startOffsetMs":0,"title":"One"}]}`},
		{"chapters/invalid", "/api/search/chapters?asin=bad", 200, `{"error":"Invalid ASIN","stringKey":"MessageInvalidAsin"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			if !serve(h, w, httptest.NewRequest(http.MethodGet, c.path, nil), store.User{Type: "admin"}) {
				t.Fatal("route not handled")
			}
			if w.Code != c.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, c.status, w.Body)
			}
			var got, want any
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatalf("%v: %s", err, w.Body)
			}
			if err := json.Unmarshal([]byte(c.want), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(dropNulls(want), dropNulls(got)) {
				gj, _ := json.MarshalIndent(got, "", " ")
				t.Errorf("body differs\nwant %s\ngot  %s", c.want, gj)
			}
		})
	}
}

// The quick-match finders used by the scanner return the same normalized
// matches as the routes, in the provider's order.
func TestFindersReturnProviderMatches(t *testing.T) {
	h := characterisationHandler(t)
	ctx := context.Background()
	titles := func(m []wire.BookMatch) []string {
		out := []string{}
		for _, b := range m {
			out = append(out, strings.Trim(string(b.Title), `"`))
		}
		return out
	}
	for provider, want := range map[string][]string{
		"google":      {"Full Book", "Sparse"},
		"openlibrary": {"The Hobbit"}, // "Second" has no author, so the author filter drops it
		"fantlab":     {"Fant Title", "Second Fant"},
		"itunes":      {"iTunes Book", "Plain"},
		"custom-c1":   {"42", "Second", "Third"},
	} {
		q := wire.BookSearch{Provider: provider, Title: "Title", Author: "Writer"}
		if provider == "openlibrary" {
			q.Title, q.Author = "The Hobbit", "Tolkien"
		}
		got := titles(h.SearchBooks(ctx, q))
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s titles = %v, want %v", provider, got, want)
		}
	}
	if m := h.SearchPodcasts(ctx, "pod"); len(m) != 2 || string(m[0].FeedURL) != `"https://feed.test/rss"` {
		t.Errorf("podcast matches = %+v", m)
	}
	if a := h.FindAuthorByName(ctx, "Jane Doe", ""); a == nil || a.ASIN != "B0AUTHOR01" {
		t.Errorf("author by name = %+v", a)
	}
}

// dropNulls removes object keys holding null: a built-in provider leaves out
// the fields it has no value for, where it used to send null.
func dropNulls(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, value := range v {
			if value != nil {
				out[key] = dropNulls(value)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, value := range v {
			out[i] = dropNulls(value)
		}
		return out
	}
	return v
}
