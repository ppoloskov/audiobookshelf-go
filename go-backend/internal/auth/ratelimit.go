package auth

import (
	"maps"
	"net/http"
	"sync"
	"time"

	"audiobookshelf-compat/internal/httpx"

	"golang.org/x/time/rate"
)

// limiter is a per-IP token bucket: a burst of max requests, refilled at max
// per window (the practical limit of upstream's fixed window). A zero max or
// window disables it.
type limiter struct {
	max    int
	window time.Duration

	mu      sync.Mutex
	clients map[string]*rate.Limiter // guarded by mu
	swept   time.Time                // guarded by mu
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, clients: map[string]*rate.Limiter{}}
}

// allow spends one token of ip's bucket.
func (l *limiter) allow(ip string) bool {
	if l.max <= 0 || l.window <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if time.Since(l.swept) >= l.window {
		l.swept = time.Now()
		// A full bucket is what a new client gets, so forgetting it changes nothing.
		maps.DeleteFunc(l.clients, func(_ string, c *rate.Limiter) bool { return c.Tokens() >= float64(c.Burst()) })
	}
	c := l.clients[ip]
	if c == nil {
		c = rate.NewLimiter(rate.Limit(float64(l.max)/l.window.Seconds()), l.max)
		l.clients[ip] = c
	}
	return c.Allow()
}

// wrap answers 429 {"error": message} to a client over its limit, else runs h.
func (l *limiter) wrap(h httpx.Handler, message string) httpx.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		if !l.allow(httpx.ClientIP(r)) {
			return jsonError(w, r, http.StatusTooManyRequests, message)
		}
		return h(w, r)
	}
}
