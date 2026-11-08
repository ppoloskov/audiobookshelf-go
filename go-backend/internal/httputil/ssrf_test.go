package httputil

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

func TestPublicAddress(t *testing.T) {
	for ip, want := range map[string]bool{
		"8.8.8.8": true, "127.0.0.1": false, "10.0.0.1": false, "100.64.0.1": false,
		"0.0.1.1": false, "169.254.1.1": false, "::1": false, "2606:4700::1": true,
	} {
		if got := PublicAddress(netip.MustParseAddr(ip)); got != want {
			t.Errorf("PublicAddress(%s) = %v, want %v", ip, got, want)
		}
	}
}

func TestSafeClientBlocksLoopbackUnlessWhitelisted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png"))
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "img")
	t.Setenv("SSRF_REQUEST_FILTER_WHITELIST", "")
	if err := DownloadImage(context.Background(), srv.URL, dest); err == nil {
		t.Fatal("loopback download was allowed")
	}
	if _, err := os.Stat(dest); err == nil {
		t.Fatal("failed download left a file")
	}
	t.Setenv("SSRF_REQUEST_FILTER_WHITELIST", "127.0.0.1")
	if err := DownloadImage(context.Background(), srv.URL, dest); err != nil {
		t.Fatal(err)
	}
}
