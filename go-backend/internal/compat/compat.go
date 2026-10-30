// Package compat provides a deliberately small, read-only HTTP differential
// harness. It compares endpoint responses; it does not claim backend coverage.
package compat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const DefaultBodyLimit int64 = 4 << 20

var DefaultCompareHeaders = []string{"content-type", "location", "etag", "cache-control"}

type Case struct {
	Name               string            `json:"name"`
	Method             string            `json:"method"`
	Path               string            `json:"path"`
	Headers            map[string]string `json:"headers,omitempty"`
	CompareHeaders     []string          `json:"compare_headers,omitempty"`
	IgnoreJSONPointers []string          `json:"ignore_json_pointers,omitempty"`
}

type Result struct {
	Name   string
	Equal  bool
	Detail string
}

type Options struct {
	LeftURL, RightURL     *url.URL
	LeftToken, RightToken string
	Timeout               time.Duration
	BodyLimit             int64
}

type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

func Run(ctx context.Context, c Case, o Options) (Result, error) {
	name := c.Name
	if name == "" {
		name = c.Method + " " + c.Path
	}
	method := strings.ToUpper(c.Method)
	if method != http.MethodGet && method != http.MethodHead {
		return Result{}, fmt.Errorf("case %q: method %q is not allowed (only GET and HEAD)", name, c.Method)
	}
	leftURL, err := endpointURL(o.LeftURL, c.Path)
	if err != nil {
		return Result{}, fmt.Errorf("case %q: left URL: %w", name, err)
	}
	rightURL, err := endpointURL(o.RightURL, c.Path)
	if err != nil {
		return Result{}, fmt.Errorf("case %q: right URL: %w", name, err)
	}
	limit := o.BodyLimit
	if limit <= 0 {
		limit = DefaultBodyLimit
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	left, err := fetch(ctx, method, leftURL, c.Headers, o.LeftToken, timeout, limit)
	if err != nil {
		return Result{}, fmt.Errorf("case %q: left: %w", name, err)
	}
	right, err := fetch(ctx, method, rightURL, c.Headers, o.RightToken, timeout, limit)
	if err != nil {
		return Result{}, fmt.Errorf("case %q: right: %w", name, err)
	}
	if left.StatusCode != right.StatusCode {
		return Result{Name: name, Detail: fmt.Sprintf("status differs: left %d, right %d", left.StatusCode, right.StatusCode)}, nil
	}
	headers := c.CompareHeaders
	if headers == nil {
		headers = DefaultCompareHeaders
	}
	for _, h := range headers {
		lh, rh := left.Header.Values(h), right.Header.Values(h)
		if !equalStrings(lh, rh) {
			return Result{Name: name, Detail: fmt.Sprintf("header %q differs: left %q, right %q", http.CanonicalHeaderKey(h), lh, rh)}, nil
		}
	}
	leftJSON, rightJSON := isJSON(left.Header), isJSON(right.Header)
	if leftJSON || rightJSON {
		if !leftJSON || !rightJSON {
			return Result{Name: name, Detail: "body media type differs between JSON and non-JSON"}, nil
		}
		var lv, rv any
		ld := json.NewDecoder(bytes.NewReader(left.Body))
		ld.UseNumber()
		rd := json.NewDecoder(bytes.NewReader(right.Body))
		rd.UseNumber()
		if err := ld.Decode(&lv); err != nil {
			return Result{}, fmt.Errorf("case %q: left body declares JSON but cannot be decoded: %w", name, err)
		}
		if err := rd.Decode(&rv); err != nil {
			return Result{}, fmt.Errorf("case %q: right body declares JSON but cannot be decoded: %w", name, err)
		}
		if hasExtraJSONValue(ld) || hasExtraJSONValue(rd) {
			return Result{}, fmt.Errorf("case %q: JSON response contains trailing data", name)
		}
		ignored, err := compilePointers(c.IgnoreJSONPointers)
		if err != nil {
			return Result{}, fmt.Errorf("case %q: %w", name, err)
		}
		if !jsonEqual(lv, rv, nil, ignored) {
			return Result{Name: name, Detail: "JSON body differs (object key order is ignored; array order and JSON types are preserved; numeric values compare by value)"}, nil
		}
	} else if !bytes.Equal(left.Body, right.Body) {
		return Result{Name: name, Detail: "non-JSON body bytes differ"}, nil
	}
	return Result{Name: name, Equal: true, Detail: "responses match"}, nil
}

func endpointURL(base *url.URL, path string) (*url.URL, error) {
	if base == nil || base.Scheme == "" || base.Host == "" {
		return nil, errors.New("server URL must be absolute")
	}
	if base.Path != "" && base.Path != "/" {
		return nil, errors.New("server URL must not include a path; case paths are resolved from the origin root")
	}
	u, err := url.Parse(path)
	if err != nil {
		return nil, err
	}
	if u.IsAbs() || u.Host != "" || u.Fragment != "" || !strings.HasPrefix(path, "/") {
		return nil, errors.New("case path must be an absolute-path reference beginning with /, not a URL or fragment")
	}
	// Case paths address the origin root; the base URL path is restricted above.
	baseCopy := *base
	baseCopy.Path = strings.TrimRight(baseCopy.Path, "/") + "/"
	return baseCopy.ResolveReference(u), nil
}

func fetch(ctx context.Context, method string, target *url.URL, headers map[string]string, token string, timeout time.Duration, limit int64) (*Response, error) {
	if limit == int64(^uint64(0)>>1) {
		return nil, errors.New("response body limit is too large")
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, method, target.String(), nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		if strings.EqualFold(k, "authorization") || strings.EqualFold(k, "cookie") || strings.EqualFold(k, "host") {
			return nil, fmt.Errorf("request header %q is forbidden; credentials come only from token environment variables", k)
		}
		req.Header.Set(k, v)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	origin := target
	client := &http.Client{Timeout: timeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if !sameOrigin(origin, req.URL) {
			return errors.New("refused cross-origin redirect")
		}
		// Preserve the redirect response so status and Location differences remain
		// observable. CheckRedirect runs before a redirected request is sent.
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("response body exceeded %d byte limit", limit)
	}
	return &Response{StatusCode: resp.StatusCode, Header: resp.Header.Clone(), Body: body}, nil
}

func sameOrigin(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if u.Port() != "" {
			return u.Port()
		}
		if strings.EqualFold(u.Scheme, "https") {
			return "443"
		}
		if strings.EqualFold(u.Scheme, "http") {
			return "80"
		}
		return ""
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

func isJSON(h http.Header) bool {
	v := h.Get("Content-Type")
	media, _, err := mime.ParseMediaType(v)
	if err != nil {
		return false
	}
	media = strings.ToLower(media)
	return media == "application/json" || strings.HasSuffix(media, "+json")
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func hasExtraJSONValue(d *json.Decoder) bool {
	var extra any
	err := d.Decode(&extra)
	return err == nil || err != io.EOF
}

type pointerNode struct {
	terminal bool
	children map[string]*pointerNode
}

func compilePointers(paths []string) (*pointerNode, error) {
	root := &pointerNode{children: map[string]*pointerNode{}}
	for _, p := range paths {
		if p == "" {
			root.terminal = true
			continue
		}
		if !strings.HasPrefix(p, "/") {
			return nil, fmt.Errorf("invalid JSON pointer %q (must be empty or begin with /)", p)
		}
		n := root
		for _, raw := range strings.Split(p[1:], "/") {
			var b strings.Builder
			for i := 0; i < len(raw); i++ {
				if raw[i] == '~' {
					if i+1 >= len(raw) || (raw[i+1] != '0' && raw[i+1] != '1') {
						return nil, fmt.Errorf("invalid escape in JSON pointer %q", p)
					}
					i++
					if raw[i] == '0' {
						b.WriteByte('~')
					} else {
						b.WriteByte('/')
					}
				} else {
					b.WriteByte(raw[i])
				}
			}
			key := b.String()
			if n.children[key] == nil {
				n.children[key] = &pointerNode{children: map[string]*pointerNode{}}
			}
			n = n.children[key]
		}
		n.terminal = true
	}
	return root, nil
}

func jsonEqual(a, b any, path []string, ignored *pointerNode) bool {
	n := ignored
	for _, p := range path {
		if n == nil {
			break
		}
		n = n.children[p]
	}
	if n != nil && n.terminal {
		return true
	}
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, x := range av {
			y, ok := bv[k]
			if !ok || !jsonEqual(x, y, appendPath(path, k), ignored) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i, x := range av {
			if !jsonEqual(x, bv[i], appendPath(path, strconv.Itoa(i)), ignored) {
				return false
			}
		}
		return true
	default:
		if an, ok := a.(json.Number); ok {
			bn, ok := b.(json.Number)
			if !ok {
				return false
			}
			ar, aok := new(big.Rat).SetString(an.String())
			br, bok := new(big.Rat).SetString(bn.String())
			return aok && bok && ar.Cmp(br) == 0
		}
		return fmt.Sprintf("%T:%v", a, a) == fmt.Sprintf("%T:%v", b, b)
	}
}
func appendPath(path []string, s string) []string {
	p := make([]string, len(path)+1)
	copy(p, path)
	p[len(path)] = s
	return p
}
