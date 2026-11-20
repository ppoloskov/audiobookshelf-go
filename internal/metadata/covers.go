package metadata

import (
	"context"
	"sync"
	"time"
)

// coverProviderTimeout bounds one provider's answer to a cover search.
const coverProviderTimeout = 10 * time.Second

// allCoverProviders and bestCoverProviders are the sets the cover tab's "all"
// and "best" choices search.
var (
	allCoverProviders  = []string{"google", "itunes", "openlibrary", "fantlab", "audiobookcovers", "audible", "audible.ca", "audible.uk", "audible.au", "audible.fr", "audible.de", "audible.jp", "audible.it", "audible.in", "audible.es"}
	bestCoverProviders = []string{"google", "fantlab", "audible"}
)

// CoverRequest is a cover search: the search_covers socket payload.
type CoverRequest struct {
	RequestID string
	Title     string
	Author    string
	Provider  string
	Podcast   bool
}

// coverEvent is the payload of a cover_search_* event.
type coverEvent struct {
	RequestID string   `json:"requestId"`
	Provider  string   `json:"provider,omitempty"`
	Covers    []string `json:"covers,omitempty"`
	Total     int      `json:"total,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// coverSearches are the running socket cover searches by request id. The zero
// value is ready to use.
type coverSearches struct {
	mu      sync.Mutex
	running map[string]context.CancelFunc
}

func (c *coverSearches) start(requestID string, cancel context.CancelFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running == nil {
		c.running = map[string]context.CancelFunc{}
	}
	c.running[requestID] = cancel
}

// forget drops a finished search without cancelling it.
func (c *coverSearches) forget(requestID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.running, requestID)
}

// stop cancels and forgets a search and reports whether it was running.
func (c *coverSearches) stop(requestID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	cancel, ok := c.running[requestID]
	if ok {
		cancel()
		delete(c.running, requestID)
	}
	return ok
}

// StartCoverSearch runs req in the background and returns at once. Each
// provider's covers go to reply as cover_search_result; a provider that fails
// sends cover_search_provider_error; cover_search_complete follows when every
// provider has answered, unless the search was cancelled first.
func (h *Handler) StartCoverSearch(req CoverRequest, reply func(event string, payload any)) {
	ctx, cancel := context.WithCancel(context.Background())
	h.covers.start(req.RequestID, cancel)
	go func() {
		defer cancel()
		var wg sync.WaitGroup
		for _, provider := range coverProviders(req) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				h.replyCovers(ctx, req, provider, reply)
			}()
		}
		wg.Wait()
		h.covers.forget(req.RequestID)
		if ctx.Err() == nil {
			reply("cover_search_complete", coverEvent{RequestID: req.RequestID})
		}
	}()
}

// CancelCoverSearch stops a running cover search. It sends nothing more; the
// cancel itself is answered with cover_search_cancelled.
func (h *Handler) CancelCoverSearch(requestID string, reply func(event string, payload any)) {
	if h.covers.stop(requestID) {
		reply("cover_search_cancelled", coverEvent{RequestID: requestID})
	}
}

// replyCovers sends one provider's answer. A cancelled search is silent; a
// provider that fails or times out is reported by name.
func (h *Handler) replyCovers(parent context.Context, req CoverRequest, provider string, reply func(string, any)) {
	ctx, cancel := context.WithTimeout(parent, coverProviderTimeout)
	defer cancel()
	covers, err := h.providerCovers(ctx, provider, req)
	if parent.Err() != nil {
		return
	}
	if err != nil {
		reply("cover_search_provider_error", coverEvent{RequestID: req.RequestID, Provider: provider, Error: err.Error()})
		return
	}
	if len(covers) > 0 {
		reply("cover_search_result", coverEvent{RequestID: req.RequestID, Provider: provider, Covers: covers, Total: len(covers)})
	}
}

// coverProviders lists the providers a request searches: iTunes for a
// podcast, the named provider for a book, and the sets "all" and "best".
func coverProviders(req CoverRequest) []string {
	switch {
	case req.Podcast:
		return []string{"itunes"}
	case req.Provider == "all":
		return allCoverProviders
	case req.Provider == "best":
		return bestCoverProviders
	}
	return []string{req.Provider}
}

// providerCovers are the distinct covers one provider finds for req.
func (h *Handler) providerCovers(ctx context.Context, provider string, req CoverRequest) ([]string, error) {
	if req.Podcast {
		return h.svc.itunes.podcastCovers(ctx, req.Title)
	}
	q := BookQuery{Provider: provider, Title: req.Title, Author: req.Author}
	var results []BookResult
	if isCustom(provider) {
		var err error
		if results, err = h.searchCustom(ctx, provider, "book", q); err != nil {
			return nil, err
		}
	} else {
		results = h.searchBuiltin(ctx, q)
	}
	var covers []string
	for _, result := range results {
		covers = append(covers, result.Covers...)
		covers = append(covers, result.Cover)
	}
	return uniqueTrimmed(covers), nil
}
