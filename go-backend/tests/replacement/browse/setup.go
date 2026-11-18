package browse

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/tests/replacement/harness"
)

// suite holds the state the scenarios of one packet run share: the
// enrichment of the fixture (done once, by the first scenario that needs it)
// and the tokens of the restricted users it creates.
type suite struct {
	enriched     bool
	podcastReady bool
	seriesFeed   bool
	metadata     *metadataProvider
	matches      int
	plain        bool
	tokens       map[string]string
}

// enrichment patches the fixture metadata so filters, sorts and series have
// distinct values. The order is deliberate: series lastBookUpdated follows it.
var enrichment = []struct{ key, body string }{
	{"novel", `{"metadata":{"subtitle":"A Sub","publisher":"Pub One","publishedYear":"1995","language":"English","abridged":true,"narrators":["Nora Narrator","Vera Voice"],"genres":["Fiction","Mystery"],"asin":"B000TEST01","isbn":"9780000000001","series":[{"name":"Solo Series","sequence":"1"}]},"tags":["alpha","beta"]}`},
	{"saga1", `{"metadata":{"publisher":"Pub Two","publishedYear":"1985","language":"English","genres":["Fantasy"]},"tags":["alpha"]}`},
	{"saga2", `{"metadata":{"publisher":"Pub One","publishedYear":"2010","genres":["Fantasy","Mystery"],"asin":"B000TEST02"}}`},
	{"ebook", `{"metadata":{"explicit":true,"publishedYear":"2005","subtitle":"Epub","genres":["Mystery"],"series":[{"name":"A Third Saga","sequence":"2"}]},"tags":["beta"]}`},
	{"disposable", `{"metadata":{"series":[{"name":"A Third Saga","sequence":"10"}]},"tags":["gamma"]}`},
	{"podcast", `{"metadata":{"language":"English"},"tags":["pod-tag"]}`},
}

// enrich applies the enrichment once.
func (s *suite) enrich(t *testing.T, e *harness.Env) {
	t.Helper()
	if s.enriched {
		return
	}
	s.prepareDates(t, e)
	root := e.Token(t, "root")
	for _, step := range enrichment {
		mustOK(t, "enrich "+step.key, patchJSON(t, e, root, "/api/items/"+e.IDs[step.key]+"/media", step.body))
	}
	s.enriched = true
}

// restrictedUsers are created on demand: name -> permission JSON fragment.
var restrictedUsers = map[string]string{
	"browse-tagged":   `"permissions":{"accessAllTags":false,"accessExplicitContent":true},"itemTagsSelected":["alpha"]`,
	"browse-notag":    `"permissions":{"accessAllTags":false,"selectedTagsNotAccessible":true,"accessExplicitContent":true},"itemTagsSelected":["beta"]`,
	"browse-podtag":   `"permissions":{"accessAllTags":false,"accessExplicitContent":true},"itemTagsSelected":["pod-tag"]`,
	"browse-podnotag": `"permissions":{"accessAllTags":false,"selectedTagsNotAccessible":true,"accessExplicitContent":true},"itemTagsSelected":["pod-tag"]`,
	"browse-podonly":  `"permissions":{"accessAllLibraries":false,"accessExplicitContent":true},"librariesAccessible":["PODCAST"]`,
}

// restrictedToken creates (once) and logs in a restricted user.
func (s *suite) restrictedToken(t *testing.T, e *harness.Env, name string) string {
	t.Helper()
	if token, ok := s.tokens[name]; ok {
		return token
	}
	if s.tokens == nil {
		s.tokens = map[string]string{}
	}
	fragment := restrictedUsers[name]
	fragment = strings.Replace(fragment, "PODCAST", e.IDs[podcastLibrary], 1)
	body := `{"username":"` + name + `","password":"browse-pass-1","type":"user","isActive":true,` + fragment + `}`
	resp := postJSON(t, e, e.Token(t, "root"), "/api/users", body)
	expectStatus(t, "create user "+name, resp, http.StatusOK)
	token, _ := e.Login(t, name, "browse-pass-1")
	s.tokens[name] = token
	return token
}

// prepareDates sets the creation dates the 60-day shelves and the recent
// filter depend on, relative to now, keeping the fixture's creation order
// (disposable, novel, ebook, saga2, saga1). The disposable book and its author
// are 90 days old, so they are outside the window; everything else is inside.
// The server is stopped around the update (harness Exec).
func (s *suite) prepareDates(t *testing.T, e *harness.Env) {
	t.Helper()
	e.Stop(t)
	stamp := func(daysAgo int) string {
		return time.Now().UTC().Add(-time.Duration(daysAgo) * 24 * time.Hour).Format("2006-01-02 15:04:05.000 +00:00")
	}
	for key, days := range map[string]int{"disposable": 90, "novel": 4, "ebook": 3, "saga2": 2, "saga1": 1, "podcast": 2} {
		e.Exec(t, "UPDATE libraryItems SET createdAt = ? WHERE id = ?", stamp(days), e.IDs[key])
		e.Exec(t, "UPDATE books SET createdAt = ? WHERE id = (SELECT mediaId FROM libraryItems WHERE id = ?)", stamp(days), e.IDs[key])
	}
	for key, days := range map[string]int{"ep1": 3, "ep2": 2, "ep3": 1} {
		e.Exec(t, "UPDATE podcastEpisodes SET createdAt = ? WHERE id = ?", stamp(days), e.IDs[key])
	}
	for name, days := range map[string]int{"Zed Disposable": 90, "Ada Example": 4, "Terry Fixture": 1} {
		e.Exec(t, "UPDATE authors SET createdAt = ? WHERE name = ?", stamp(days), name)
	}
	e.Exec(t, "UPDATE series SET createdAt = ? WHERE name = 'Long Saga'", stamp(5))
	e.Restart(t)
}
