package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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

// TestProviderOutputsGolden records every provider's normalized output for the
// recorded upstream bodies above (go test -run Golden with UPDATE_GOLDEN=1
// rewrites testdata/provider_matches.golden.json).
func TestProviderOutputsGolden(t *testing.T) {
	h := characterisationHandler(t)
	got := map[string]any{}
	get := func(name, path string) {
		w := httptest.NewRecorder()
		if !serve(h, w, httptest.NewRequest(http.MethodGet, path, nil), store.User{Type: "admin"}) {
			t.Fatalf("%s: route not handled", name)
		}
		var v any
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatalf("%s: %v: %s", name, err, w.Body.String())
		}
		got[name] = map[string]any{"status": w.Code, "body": v}
	}
	for _, provider := range []string{"google", "itunes", "openlibrary", "audiobookcovers", "fantlab", "audible", "audible.uk", "custom-c1"} {
		get("books/"+provider, "/api/search/books?provider="+provider+"&title=Title&author=Writer")
	}
	get("books/audible-asin", "/api/search/books?provider=audible&title=x&asin=b000000002&region=uk")
	get("books/audible-title-asin", "/api/search/books?provider=audible&title=B000000001")
	get("books/empty-title", "/api/search/books?provider=google")
	get("podcast", "/api/search/podcast?term=pod&country=us")
	get("covers/best", "/api/search/covers?title=Title&author=Writer&provider=all")
	get("covers/podcast", "/api/search/covers?title=Pod&podcast=1")
	get("covers/custom", "/api/search/covers?title=Title&provider=custom-c1")
	get("authors", "/api/search/authors?q=Jane+Doe")
	get("authors/none", "/api/search/authors?q=Zzzzzz")
	get("chapters", "/api/search/chapters?asin=b000000009")
	get("chapters/invalid", "/api/search/chapters?asin=bad")

	finder := map[string]any{}
	for name, q := range map[string]wire.BookSearch{
		"google":      {Provider: "google", Title: "Title", Author: "Writer"},
		"openlibrary": {Provider: "openlibrary", Title: "The Hobbit", Author: "Tolkien"},
		"audible":     {Provider: "audible", Title: "Title", Duration: 1800},
		"fantlab":     {Provider: "fantlab", Title: "Title"},
		"itunes":      {Provider: "itunes", Title: "Title"},
		"custom":      {Provider: "custom-c1", Title: "Title"},
	} {
		finder[name] = h.SearchBooks(context.Background(), q)
	}
	finder["podcasts"] = h.SearchPodcasts(context.Background(), "pod")
	finder["authorByName"] = h.FindAuthorByName(context.Background(), "Jane Doe", "")
	raw, _ := json.Marshal(finder)
	var finderValue any
	_ = json.Unmarshal(raw, &finderValue)
	got["finder"] = finderValue

	const golden = "testdata/provider_matches.golden.json"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		out, _ := json.MarshalIndent(got, "", "  ")
		if err := os.WriteFile(golden, append(out, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	data, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	var gotValue map[string]any
	gotRaw, _ := json.Marshal(got)
	_ = json.Unmarshal(gotRaw, &gotValue)
	for name, w := range want {
		if !reflect.DeepEqual(dropNulls(w), dropNulls(gotValue[name])) {
			wj, _ := json.MarshalIndent(w, "", " ")
			gj, _ := json.MarshalIndent(gotValue[name], "", " ")
			t.Errorf("%s differs\nwant %s\ngot  %s", name, wj, gj)
		}
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
