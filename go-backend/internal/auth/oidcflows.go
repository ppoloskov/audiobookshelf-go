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

// put stores value under state and forgets the entries that have expired.
func (f *flows[T]) put(state string, value T) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	for key, entry := range f.items {
		if now.Sub(entry.started) > f.ttl {
			delete(f.items, key)
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
