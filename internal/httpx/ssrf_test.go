package httpx

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestPublicAddress(t *testing.T) {
	for ip, want := range map[string]bool{
		"8.8.8.8": true, "127.0.0.1": false, "10.0.0.1": false, "100.64.0.1": false,
		"0.0.1.1": false, "169.254.1.1": false, "::1": false, "2606:4700::1": true,
	} {
		if got := publicAddress(netip.MustParseAddr(ip)); got != want {
			t.Errorf("publicAddress(%s) = %v, want %v", ip, got, want)
		}
	}
}

func TestSafeClientBlocksLoopbackUnlessWhitelisted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("png"))
	}))
	defer srv.Close()
	fetch := func() error {
		resp, err := NewSafeClient().Get(srv.URL)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		_, err = io.Copy(io.Discard, resp.Body)
		return err
	}
	t.Setenv("SSRF_REQUEST_FILTER_WHITELIST", "")
	if err := fetch(); err == nil {
		t.Fatal("loopback request was allowed")
	}
	t.Setenv("SSRF_REQUEST_FILTER_WHITELIST", "127.0.0.1")
	if err := fetch(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSRF_REQUEST_FILTER_WHITELIST", "")
	t.Setenv("DISABLE_SSRF_REQUEST_FILTER", "1")
	if err := fetch(); err != nil {
		t.Fatalf("filter disabled but request failed: %v", err)
	}
}
