package httputil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"
)

// filterPolicy is the SSRF exemption TS applies to every axios request
// (server/Server.js, DisableSsrfRequestFilter): the filter is off entirely, or
// the host is named in SSRF_REQUEST_FILTER_WHITELIST. The environment is read
// once per client (or per SSRFFilterDisabled call), never per request.
type filterPolicy struct {
	off       bool
	whitelist []string
}

func policyFromEnv() filterPolicy {
	p := filterPolicy{off: os.Getenv("DISABLE_SSRF_REQUEST_FILTER") == "1" || os.Getenv("EXP_PROXY_SUPPORT") == "1"}
	for _, host := range strings.Split(os.Getenv("SSRF_REQUEST_FILTER_WHITELIST"), ",") {
		if host = strings.TrimSpace(host); host != "" {
			p.whitelist = append(p.whitelist, host)
		}
	}
	return p
}

func (p filterPolicy) exempt(host string) bool {
	if p.off {
		return true
	}
	for _, allowed := range p.whitelist {
		if allowed == host {
			return true
		}
	}
	return false
}

// SSRFFilterDisabled reports whether outbound requests to host skip the private
// address filter. Prefer SafeClient, which applies it for you.
func SSRFFilterDisabled(host string) bool { return policyFromEnv().exempt(host) }

var blockedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, raw := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"2001:db8::/32", "2001:2::/48", "2001:3::/32", "2001:4:112::/48", "2001:10::/28",
		"2001:20::/28", "64:ff9b::/96", "::ffff:0:0/96", "2002::/16", "2001::/32",
	} {
		out = append(out, netip.MustParsePrefix(raw))
	}
	return out
}()

// PublicAddress is the one rule for what an outbound request may reach: global
// unicast, not private, loopback, link-local or in a reserved range.
func PublicAddress(ip netip.Addr) bool {
	if !ip.IsValid() || ip.Zone() != "" || ip.Is4In6() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

// SafeClient is the only outbound HTTP client for user-supplied URLs. The
// public-address rule runs when each connection is dialed, so it covers the
// first request and every redirect target (a redirect to a new host dials
// again and is judged on its own host). It follows at most 5 redirects, like
// axios. It has no overall timeout: callers bound a request with its context.
func SafeClient() *http.Client {
	policy := policyFromEnv()
	dialer := &net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		ResponseHeaderTimeout: 30 * time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			if policy.exempt(host) {
				return dialer.DialContext(ctx, network, address)
			}
			ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
			lastErr := errors.New("host resolved to no addresses")
			for _, ip := range ips {
				if !PublicAddress(ip) {
					lastErr = fmt.Errorf("call to %s is blocked", ip)
					continue
				}
				conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if dialErr == nil {
					return conn, nil
				}
				lastErr = dialErr
			}
			return nil, lastErr
		},
	}
	return &http.Client{Transport: transport, CheckRedirect: func(_ *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return errors.New("stopped after 5 redirects")
		}
		return nil
	}}
}

// DownloadImage fetches an http(s) image through SafeClient into dest (30
// second budget), accepting a 2xx image/* response that is not SVG. A failed
// download leaves no file behind.
func DownloadImage(ctx context.Context, rawURL, dest string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("Invalid URL")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return errors.New("Invalid URL")
	}
	req.Header.Set("User-Agent", "audiobookshelf (+https://audiobookshelf.org)")
	resp, err := SafeClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Request failed with status code %d", resp.StatusCode)
	}
	contentType := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "image/") || contentType == "image/svg+xml" {
		return fmt.Errorf("Invalid content type %q", contentType)
	}
	file, err := os.Create(dest)
	if err != nil {
		return err
	}
	_, err = io.Copy(file, resp.Body)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(dest)
	}
	return err
}
