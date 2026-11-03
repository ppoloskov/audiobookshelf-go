package identity

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func rateTestHandler(t *testing.T, limit int, window time.Duration) *Handler {
	t.Helper()
	db, h := identityFixture(t)
	t.Cleanup(func() { db.Close() })
	h.rateMax = limit
	h.rateWindow = window
	return h
}

func authRequest(remote string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	r.RemoteAddr = remote + ":5555"
	return r
}

func TestAuthRateLimitTripsAndResets(t *testing.T) {
	h := rateTestHandler(t, 4, 300*time.Millisecond)
	hit := func(remote string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		if h.allowAuthRequest(w, authRequest(remote)) {
			return nil
		}
		return w
	}
	for i := range 4 {
		if w := hit("10.0.0.1"); w != nil {
			t.Fatalf("request %d from a fresh client was limited", i+1)
		}
	}
	w := hit("10.0.0.1")
	if w == nil {
		t.Fatal("fifth request in the burst was admitted")
	}
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["error"] != h.rateMessage || len(body) != 1 {
		t.Fatalf("body = %q, want {\"error\":%q}", w.Body.String(), h.rateMessage)
	}
	if got := w.Header().Get("RateLimit-Policy"); got != "4;w=0" {
		t.Fatalf("RateLimit-Policy = %q", got)
	}
	if hit("10.0.0.2") != nil {
		t.Fatal("another client was limited by the first client's bucket")
	}
	time.Sleep(350 * time.Millisecond)
	for i := range 4 {
		if hit("10.0.0.1") != nil {
			t.Fatalf("request %d after the window was limited", i+1)
		}
	}
	if hit("10.0.0.1") == nil {
		t.Fatal("bucket admitted more than the limit after the window")
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
		if h.allowAuthRequest(httptest.NewRecorder(), authRequest("10.0.0.9")) {
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
		if !h.allowAuthRequest(httptest.NewRecorder(), authRequest("10.0.0.3")) {
			t.Fatal("limited with the limiter disabled")
		}
	}
}

func TestAuthRateLimitPrunesIdleClients(t *testing.T) {
	h := rateTestHandler(t, 4, 100*time.Millisecond)
	for _, remote := range []string{"10.1.0.1", "10.1.0.2", "10.1.0.3"} {
		h.allowAuthRequest(httptest.NewRecorder(), authRequest(remote))
	}
	time.Sleep(150 * time.Millisecond)
	h.allowAuthRequest(httptest.NewRecorder(), authRequest("10.1.0.4"))
	h.rateMu.Lock()
	defer h.rateMu.Unlock()
	if len(h.rateLimiters) != 1 {
		t.Fatalf("tracked clients = %d after idle pruning, want 1", len(h.rateLimiters))
	}
	if _, ok := h.rateLimiters["10.1.0.4"]; !ok {
		t.Fatal("the active client was pruned")
	}
}
