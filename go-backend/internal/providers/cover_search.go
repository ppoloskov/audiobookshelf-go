package providers

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"time"
)

// coverSearchProviderTimeout bounds one provider's answer to a cover search
// (upstream CoverSearchManager.providerTimeout).
const coverSearchProviderTimeout = 10 * time.Second

// CoverRequest is a cover search: the search_covers socket payload, or the
// /api/search/covers query.
type CoverRequest struct {
	RequestID string
	Title     string
	Author    string
	Provider  string
	Podcast   bool
}

// coverEvent is the payload of a cover_search_* event: the request it answers,
// and the provider's covers, error or total where the event carries them.
type coverEvent struct {
	RequestID string   `json:"requestId"`
	Provider  string   `json:"provider,omitempty"`
	Covers    []string `json:"covers,omitempty"`
	Total     int      `json:"total,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// StartCoverSearch runs req in the background and returns at once. Each
// provider's covers go to reply as cover_search_result; a provider that fails
// sends cover_search_provider_error; cover_search_complete follows once every
// provider has answered, unless the search was cancelled first.
func (h *Handler) StartCoverSearch(req CoverRequest, reply func(event string, payload any)) {
	ctx, cancel := context.WithCancel(context.Background())
	h.coverMu.Lock()
	h.coverSearches[req.RequestID] = cancel
	h.coverMu.Unlock()
	go func() {
		var wg sync.WaitGroup
		for _, provider := range coverProviders(req) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				h.searchCoversFrom(ctx, req, provider, reply)
			}()
		}
		wg.Wait()
		h.coverMu.Lock()
		delete(h.coverSearches, req.RequestID)
		h.coverMu.Unlock()
		if ctx.Err() == nil {
			reply("cover_search_complete", coverEvent{RequestID: req.RequestID})
		}
		cancel()
	}()
}

// CancelCoverSearch stops a running cover search. A cancelled search sends
// nothing more; the cancel itself is answered with cover_search_cancelled.
func (h *Handler) CancelCoverSearch(requestID string, reply func(event string, payload any)) {
	h.coverMu.Lock()
	cancel, ok := h.coverSearches[requestID]
	if ok {
		cancel()
		delete(h.coverSearches, requestID)
	}
	h.coverMu.Unlock()
	if ok {
		reply("cover_search_cancelled", coverEvent{RequestID: requestID})
	}
}

// searchCoversFrom sends one provider's answer. A cancelled search is silent;
// a provider that fails or times out is reported by name.
func (h *Handler) searchCoversFrom(parent context.Context, req CoverRequest, provider string, reply func(string, any)) {
	ctx, cancel := context.WithTimeout(parent, coverSearchProviderTimeout)
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

// coverProviders lists the providers a cover request searches: iTunes for a
// podcast, the named provider for a book, and the sets "all" and "best" expand
// to (the same sets the cover tab offers).
func coverProviders(req CoverRequest) []string {
	switch {
	case req.Podcast:
		return []string{"itunes"}
	case req.Provider == "all":
		return []string{"google", "itunes", "openlibrary", "fantlab", "audiobookcovers", "audible", "audible.ca", "audible.uk", "audible.au", "audible.fr", "audible.de", "audible.jp", "audible.it", "audible.in", "audible.es"}
	case req.Provider == "best":
		return []string{"google", "fantlab", "audible"}
	}
	return []string{req.Provider}
}

func (h *Handler) providerCovers(ctx context.Context, provider string, req CoverRequest) ([]string, error) {
	if req.Podcast {
		query := url.Values{"term": {req.Title}, "entity": {"podcast"}, "media": {"podcast"}}
		var response struct {
			Results []itunesItem `json:"results"`
		}
		if err := h.getJSON(ctx, h.endpoint("itunes")+"?"+query.Encode(), &response); err != nil {
			return nil, err
		}
		var covers []string
		for _, item := range response.Results {
			if cover := item.cover(); cover != "" {
				covers = append(covers, cover)
			}
		}
		return uniqueStrings(covers), nil
	}
	var results []bookMatch
	var err error
	if strings.HasPrefix(provider, "custom-") {
		results, err = h.searchCustom(ctx, provider, "book", req.Title, req.Author, "", coverSearchProviderTimeout)
	} else {
		results, err = h.searchBuiltinBooks(ctx, provider, req.Title, req.Author, "", "")
	}
	if err != nil {
		return nil, err
	}
	var covers []string
	for _, result := range results {
		covers = append(covers, result.Covers...)
		if result.Cover != "" {
			covers = append(covers, string(result.Cover))
		}
	}
	return uniqueStrings(covers), nil
}
