// Package metadata searches outside services for book, podcast, author, chapter
// and cover metadata: Google Books, iTunes, Open Library, FantLab, Audible
// (through Audnexus), AudiobookCovers and the custom providers an admin
// registers. It serves the /api/search and /api/custom-metadata-providers
// routes and the socket cover search.
package metadata

import (
	"context"
	"net/http"
	"strings"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/httpx"
)

// Endpoints are the base URLs of the public APIs; tests point them at fake
// servers.
type Endpoints struct {
	Google, ITunes, OpenLibrary, FantLab, Audible, Audnexus, AudiobookCovers string
}

// DefaultEndpoints are the public API URLs.
func DefaultEndpoints() Endpoints {
	return Endpoints{
		Google:          "https://www.googleapis.com/books/v1/volumes",
		ITunes:          "https://itunes.apple.com/search",
		OpenLibrary:     "https://openlibrary.org",
		FantLab:         "https://api.fantlab.ru",
		Audible:         "https://api.audible.com/1.0/catalog/products",
		Audnexus:        "https://api.audnex.us",
		AudiobookCovers: "https://api.audiobookcovers.com/cover/bytext/",
	}
}

// bookSearcher is a provider that finds books. A provider that fails answers
// with no results rather than an error: one broken service must not break a
// search across several.
type bookSearcher interface {
	searchBooks(ctx context.Context, q BookQuery) ([]BookResult, error)
}

// services are the built-in providers over one set of endpoints.
type services struct {
	google          google
	itunes          itunes
	openLibrary     openLibrary
	fantLab         fantLab
	audiobookCovers audiobookCovers
	audible         audible
	audnexus        audnexus
}

func newServices(get getter, e Endpoints) services {
	nexus := audnexus{get, e.Audnexus}
	return services{
		google: google{get, e.Google}, itunes: itunes{get, e.ITunes}, openLibrary: openLibrary{get, e.OpenLibrary},
		fantLab: fantLab{get, strings.TrimRight(e.FantLab, "/")}, audiobookCovers: audiobookCovers{get, e.AudiobookCovers},
		audible: audible{get, e.Audible, nexus}, audnexus: nexus,
	}
}

// bookProvider is the built-in provider named by value. Any unknown name is
// Google Books, the default of the search tab.
func (s services) bookProvider(value string) bookSearcher {
	switch {
	case value == "itunes":
		return s.itunes
	case value == "openlibrary":
		return s.openLibrary
	case value == "audiobookcovers":
		return s.audiobookCovers
	case value == "fantlab":
		return s.fantLab
	case strings.HasPrefix(value, "audible"):
		return s.audible
	}
	return s.google
}

// Handler serves the metadata routes and runs searches.
type Handler struct {
	d      *db.DB
	emit   events.Emitter
	custom getter // custom providers: admin-set URLs, so SSRF-guarded
	svc    services
	covers coverSearches
}

// New returns a Handler over the public APIs. Custom providers are reached
// through httpx.NewSafeClient.
func New(d *db.DB, emit events.Emitter) *Handler {
	return &Handler{
		d: d, emit: emit,
		custom: getter{httpx.NewSafeClient()},
		svc:    newServices(getter{&http.Client{}}, DefaultEndpoints()),
	}
}

// Routes registers the search routes (any signed-in user) and the custom
// provider routes (admin only) on mux.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.Handle("GET /api/search/providers", httpx.Handler(h.providers))
	mux.Handle("GET /api/search/books", httpx.Handler(h.searchBooksRoute))
	mux.Handle("GET /api/search/covers", httpx.Handler(h.searchCoversRoute))
	mux.Handle("GET /api/search/podcast", httpx.Handler(h.searchPodcastsRoute))
	mux.Handle("GET /api/search/authors", httpx.Handler(h.searchAuthorsRoute))
	mux.Handle("GET /api/search/chapters", httpx.Handler(h.searchChaptersRoute))
	mux.Handle("GET /api/custom-metadata-providers", auth.Admin(httpx.Handler(h.listCustom)))
	mux.Handle("POST /api/custom-metadata-providers", auth.Admin(httpx.Handler(h.createCustom)))
	mux.Handle("DELETE /api/custom-metadata-providers/{id}", auth.Admin(httpx.Handler(h.deleteCustom)))
}
