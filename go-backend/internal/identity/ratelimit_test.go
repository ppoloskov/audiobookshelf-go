package identity

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/internal/config"
)

// rateTestHandler builds the handler from production config with the given
// login rate limit; the tests drive it through POST /login.
func rateTestHandler(t *testing.T, limit int, window time.Duration) *Handler {
	t.Helper()
	auth := config.DefaultAuth()
	auth.RateMax, auth.RateWindow = limit, window
	db, h := identityFixtureWith(t, config.Config{Auth: auth})
	t.Cleanup(func() { db.Close() })
	return h
}

// attempt posts a login for an unknown user from remote and returns the response; a
// wrong password is 401 when admitted, so any 429 comes from the limiter only.
func attempt(h *Handler, remote string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"nobody","password":"x"}`))
	r.RemoteAddr = remote + ":5555"
	w := httptest.NewRecorder()
	h.Handle(w, r)
	return w
}

func TestAuthRateLimitTripsAndResets(t *testing.T) {
	h := rateTestHandler(t, 4, 300*time.Millisecond)
	for i := range 4 {
		if w := attempt(h, "10.0.0.1"); w.Code != http.StatusUnauthorized {
			t.Fatalf("request %d from a fresh client: status %d, want 401 (admitted)", i+1, w.Code)
		}
	}
	w := attempt(h, "10.0.0.1")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("fifth request in the burst: status %d, want 429", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["error"] != config.DefaultAuth().RateMessage || len(body) != 1 {
		t.Fatalf("body = %q, want {\"error\":%q}", w.Body.String(), config.DefaultAuth().RateMessage)
	}
	if got := w.Header().Get("RateLimit-Policy"); got != "4;w=0" {
		t.Fatalf("RateLimit-Policy = %q", got)
	}
	if w := attempt(h, "10.0.0.2"); w.Code != http.StatusUnauthorized {
		t.Fatalf("another client was limited by the first client's bucket: %d", w.Code)
	}
	// The limiter runs on the wall clock, so the window can only be waited out.
	time.Sleep(350 * time.Millisecond)
	for i := range 4 {
		if w := attempt(h, "10.0.0.1"); w.Code != http.StatusUnauthorized {
			t.Fatalf("request %d after the window: status %d, want admitted", i+1, w.Code)
		}
	}
	if w := attempt(h, "10.0.0.1"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("bucket admitted more than the limit after the window: %d", w.Code)
	}
}

// TS's fixed window admits up to 2x the limit across a boundary; the bucket
// stays within that and admits at least the limit at once.
func TestAuthRateLimitNeverExceedsLimitInAWindow(t *testing.T) {
	const limit = 10
	window := 500 * time.Millisecond
	h := rateTestHandler(t, limit, window)
	admitted := 0
	deadline := time.Now().Add(window * 8 / 10)
	for time.Now().Before(deadline) {
		if attempt(h, "10.0.0.9").Code != http.StatusTooManyRequests {
			admitted++
		}
	}
	if admitted > 2*limit {
		t.Fatalf("admitted %d requests in one window, more than 2x the limit %d", admitted, limit)
	}
	if admitted < limit {
		t.Fatalf("admitted %d requests, want at least the limit %d", admitted, limit)
	}
}

func TestAuthRateLimitDisabledAdmitsEverything(t *testing.T) {
	h := rateTestHandler(t, 0, time.Minute)
	for range 100 {
		if w := attempt(h, "10.0.0.3"); w.Code != http.StatusUnauthorized {
			t.Fatalf("limited with the limiter disabled: %d", w.Code)
		}
	}
}

// Idle clients are forgotten so the table cannot grow without bound. Nothing
// observable depends on it (a forgotten client gets a fresh full bucket), so
// this one test reads the table.
func TestAuthRateLimitPrunesIdleClients(t *testing.T) {
	h := rateTestHandler(t, 4, 100*time.Millisecond)
	for _, remote := range []string{"10.1.0.1", "10.1.0.2", "10.1.0.3"} {
		attempt(h, remote)
	}
	time.Sleep(150 * time.Millisecond)
	attempt(h, "10.1.0.4")
	h.rateMu.Lock()
	defer h.rateMu.Unlock()
	if len(h.rateLimiters) != 1 {
		t.Fatalf("tracked clients = %d after idle pruning, want 1", len(h.rateLimiters))
	}
	if _, ok := h.rateLimiters["10.1.0.4"]; !ok {
		t.Fatal("the active client was pruned")
	}
}
