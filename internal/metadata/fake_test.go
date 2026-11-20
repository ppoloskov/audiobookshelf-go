package metadata

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fakeUpstream answers every public API with a recorded-shape body. Paths are
// prefixed by the service: /google, /itunes, /openlibrary, /fantlab, /audible,
// /nexus, /abc, /custom. It logs "path?query" of each request.
type fakeUpstream struct {
	*httptest.Server
	mu  sync.Mutex
	log []string
}

func (f *fakeUpstream) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := slices.Clone(f.log)
	slices.Sort(out)
	return out
}

func newFake(t *testing.T) *fakeUpstream {
	f := &fakeUpstream{}
	write := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.log = append(f.log, r.Method+" "+r.URL.RequestURI())
		f.mu.Unlock()
		p, q := r.URL.Path, r.URL.Query()
		switch {
		case p == "/google/books/v1/volumes":
			if g := q.Get("q"); strings.Contains(g, "intitle:the hobbit") && !strings.Contains(g, "inauthor") {
				write(w, `{"items":[{"id":"1","volumeInfo":{"title":"The Hobbit","authors":["J. R. R. Tolkien"],"description":"<script>x</script><b>b</b>"}}]}`)
				return
			} else if strings.Contains(g, "mp3") || strings.Contains(g, "hobbit") {
				write(w, `{"items":[]}`)
				return
			}
			write(w, `{"items":[
{"id":"g1","volumeInfo":{"title":"Full Book","subtitle":"Sub","authors":["A One","B Two"],"publisher":"Pub","publishedDate":"2020-03-01","description":"<b>Desc</b> text","industryIdentifiers":[{"type":"ISBN_10","identifier":"0000000001"},{"type":"ISBN_13","identifier":"9780000000001"}],"categories":["Fiction","Fantasy"],"imageLinks":{"smallThumbnail":"http://i.test/s.jpg","large":"http://i.test/l.jpg"}}},
{"id":"g2","volumeInfo":{"title":"Sparse","industryIdentifiers":[{"type":"ISBN_10","identifier":"0000000002"}],"imageLinks":{"zzz":"http://i.test/z.jpg"}}},
{"id":"g3","volumeInfo":{"subtitle":"no title"}}]}`)
		case p == "/itunes/search" && q.Get("entity") == "audiobook":
			write(w, `{"results":[
{"collectionId":111,"artistId":222,"collectionName":"iTunes Book","artistName":"X & Y","description":"<p>Hello</p>","releaseDate":"2019-05-06T07:00:00Z","primaryGenreName":"Mystery","artworkUrl100":"https://a.test/100x100bb.jpg","artworkUrl60":"https://a.test/60x60bb.jpg"},
{"collectionId":112,"collectionName":"Plain","artistName":"Solo","artworkUrl600":"https://a.test/600.jpg"}]}`)
		case p == "/itunes/search" && q.Get("entity") == "podcast":
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
		case p == "/abc/cover/bytext/":
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
		case p == "/nexus/books/B000000001":
			write(w, `{"asin":"B000000001","title":"Audible One","subtitle":"Sub","authors":[{"name":"Auth A"},{"name":"Auth B"}],"narrators":[{"name":"Nar"}],"publisherName":"PubCo","releaseDate":"2022-01-01T00:00:00.000Z","runtimeLengthMin":125,"summary":"<p>Sum</p>","image":"https://img.test/a.jpg","isbn":"9781","formatType":"abridged","isAdult":true,"language":"english","region":"us","rating":"4.5","genres":[{"type":"genre","name":"Mystery"},{"type":"genre","name":"Mystery"},{"type":"tag","name":"Classic"},{"type":"tag","name":"Classic"},{"type":"other","name":"x"},{"type":"genre","name":""}],"seriesPrimary":{"name":"Saga","position":"Book 1.5"},"seriesSecondary":{"name":"Other","position":""}}`)
		case p == "/nexus/books/B000000002":
			write(w, `{"asin":"B000000002","title":"Audible Two","authors":[],"runtimeLengthMin":"30.5","region":"uk","formatType":"unabridged"}`)
		case p == "/nexus/authors" && q.Get("name") != "":
			write(w, `[{"asin":"B0FAR00001","name":"Totally Different"},{"asin":"B0AUTHOR01","name":"Jane Doe"}]`)
		case p == "/nexus/authors/B0AUTHOR01":
			write(w, `{"asin":"B0AUTHOR01","name":"Jane Doe","description":"Bio","image":"https://img.test/jane.jpg"}`)
		case p == "/nexus/books/B000000009/chapters":
			write(w, `{"asin":"B000000009","chapters":[{"lengthMs":1000,"startOffsetMs":0,"title":"One"}]}`)
		case p == "/custom/search":
			if r.Header.Get("Authorization") != "Bearer secret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			write(w, `{"matches":[{"title":42,"subtitle":null,"author":["A","B"],"narrator":"N","publisher":7,"publishedYear":2001,"description":"<p>Safe <script>bad</script><a href=\"javascript:bad\">link</a></p>","genres":"Mystery, Mystery, Thriller","tags":[" Classic ","Classic"],"series":[{"series":"Saga","sequence":2},{"series":"Other","sequence":"3"},{"sequence":false},"junk"],"duration":"12.5","cover":"https://img.test/cover.jpg","isbn":"123","asin":["B","C"],"language":"en","id":"dropped","explicit":true,"drop":{"x":1}},{"title":"Second","genres":[1,2],"series":"x","duration":"abc","tags":"","cover":{"a":1}},{"title":"Third","duration":7}]}`)
		case p == "/badcustom/search":
			write(w, `{"nomatches":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f
}
