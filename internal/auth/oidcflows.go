package auth

import (
	"sync"
	"time"
)

// webFlow is a started OpenID sign-in, kept between the redirect to the
// provider and the callback. verifier is the PKCE verifier the server keeps
// for web clients (mobile clients hold their own); redirectURI is the callback
// URL given to the provider; mobile marks the app flow.
type webFlow struct {
	verifier, nonce, redirectURI string
	mobile                       bool
}

// flows holds pending values by OpenID state for a limited time, in memory:
// a restart abandons sign-ins in progress. A state is taken once.
type flows[T any] struct {
	ttl time.Duration

	mu    sync.Mutex
	items map[string]flowEntry[T] // guarded by mu
}

type flowEntry[T any] struct {
	value   T
	started time.Time
}

func newFlows[T any](ttl time.Duration) *flows[T] {
	return &flows[T]{ttl: ttl, items: map[string]flowEntry[T]{}}
}

const maxOIDCFlows = 1024

// put stores value under state, forgets the entries that have expired, and
// evicts the earliest-expiring entry if the map is still at capacity.
func (f *flows[T]) put(state string, value T) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	for key, entry := range f.items {
		if now.Sub(entry.started) > f.ttl {
			delete(f.items, key)
		}
	}
	if _, exists := f.items[state]; !exists && len(f.items) >= maxOIDCFlows {
		var oldestKey string
		var oldestTime time.Time
		first := true
		for key, entry := range f.items {
			if first || entry.started.Before(oldestTime) {
				oldestKey = key
				oldestTime = entry.started
				first = false
			}
		}
		if !first {
			delete(f.items, oldestKey)
		}
	}
	f.items[state] = flowEntry[T]{value, now}
}

// take removes and returns the value under state, if it has not expired.
func (f *flows[T]) take(state string) (value T, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	entry, found := f.items[state]
	delete(f.items, state)
	return entry.value, found && time.Since(entry.started) <= f.ttl
}
