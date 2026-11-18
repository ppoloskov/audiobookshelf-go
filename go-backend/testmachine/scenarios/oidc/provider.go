package oidc

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

// idTokenMode selects a deliberate defect of the ID token the provider signs.
type idTokenMode int

const (
	idTokenValid idTokenMode = iota
	idTokenBadSignature
	idTokenWrongIssuer
	idTokenWrongAudience
	idTokenExpired
)

// mockGrant is one authorization code issued by the stub provider.
type mockGrant struct {
	// challenge is the S256 code challenge the code was issued for; empty
	// skips PKCE validation (the web flow's verifier is server generated).
	challenge string
	userinfo  map[string]any
}

// mockProvider is an OpenID Connect provider stub on 127.0.0.1. One instance is
// shared by the packet's scenarios: it signs ID tokens with a generated RSA
// key, issues and redeems one code per authorization, and serves the discovery
// document and JWKS the upstream client fetches.
//
// The zero value is not usable; use mockProviderFor. Its handlers never touch
// *testing.T: they answer HTTP on their own goroutine.
type mockProvider struct {
	server     *httptest.Server
	signer     *rsa.PrivateKey
	foreignKey *rsa.PrivateKey

	clientID     string
	clientSecret string

	mu            sync.Mutex
	grants        map[string]mockGrant
	byAccessToken map[string]mockGrant
	mode          idTokenMode
	tokenStatus   int // non-zero: answer the token request with this status and an OAuth error
	lastTokenForm url.Values
	lastTokenAuth string
	tokenCalls    int
	userinfoCalls int
	jwksCalls     int
}

// issuer is the provider identifier, without a trailing slash.
func (p *mockProvider) issuer() string { return p.server.URL }

var sharedProvider struct {
	once sync.Once
	p    *mockProvider
	err  error
}

// mockProviderFor returns the packet-wide provider stub, starting it on first
// use. It is never closed: the test process owns it, and every scenario needs
// the same issuer after the server side cached its client.
func mockProviderFor(t *testing.T) *mockProvider {
	t.Helper()
	sharedProvider.once.Do(func() {
		sharedProvider.p, sharedProvider.err = startMockProvider()
	})
	if sharedProvider.err != nil {
		t.Fatalf("start OIDC provider stub: %v", sharedProvider.err)
	}
	return sharedProvider.p
}

// startMockProvider builds the key material and starts the HTTP server.
func startMockProvider() (*mockProvider, error) {
	signer, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	foreign, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	p := &mockProvider{
		signer:        signer,
		foreignKey:    foreign,
		clientID:      "abs-oidc-client",
		clientSecret:  "abs-oidc-secret",
		grants:        map[string]mockGrant{},
		byAccessToken: map[string]mockGrant{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", p.handleDiscovery)
	mux.HandleFunc("/jwks", p.handleJWKS)
	mux.HandleFunc("/authorize", p.handleAuthorize)
	mux.HandleFunc("/token", p.handleToken)
	mux.HandleFunc("/userinfo", p.handleUserinfo)
	mux.HandleFunc("/logout", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	p.server = httptest.NewServer(mux)
	return p, nil
}

func (p *mockProvider) endpoint(name string) string { return p.issuer() + "/" + name }

// issue creates a code for the given userinfo and S256 challenge (empty for
// the web flow, whose verifier stays inside the server).
func (p *mockProvider) issue(t *testing.T, challenge string, userinfo map[string]any) string {
	t.Helper()
	code, err := randomHex(16)
	if err != nil {
		t.Fatalf("issue code: %v", err)
	}
	p.mu.Lock()
	p.grants[code] = mockGrant{challenge: challenge, userinfo: cloneObject(userinfo)}
	p.mu.Unlock()
	return code
}

// setMode selects the next ID token defect.
func (p *mockProvider) setMode(mode idTokenMode) {
	p.mu.Lock()
	p.mode = mode
	p.mu.Unlock()
}

// failToken makes the next token requests answer with an OAuth error status.
func (p *mockProvider) failToken(status int) {
	p.mu.Lock()
	p.tokenStatus = status
	p.mu.Unlock()
}

// reset returns the provider to a clean, successful state.
func (p *mockProvider) reset() {
	p.mu.Lock()
	p.mode = idTokenValid
	p.tokenStatus = 0
	p.lastTokenForm = nil
	p.lastTokenAuth = ""
	p.mu.Unlock()
}

// tokenForm returns a copy of the last token request form.
func (p *mockProvider) tokenForm() url.Values {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lastTokenForm == nil {
		return nil
	}
	return p.lastTokenForm.Clone()
}

func (p *mockProvider) handleDiscovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                p.issuer(),
		"authorization_endpoint":                p.endpoint("authorize"),
		"token_endpoint":                        p.endpoint("token"),
		"userinfo_endpoint":                     p.endpoint("userinfo"),
		"end_session_endpoint":                  p.endpoint("logout"),
		"jwks_uri":                              p.endpoint("jwks"),
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

func (p *mockProvider) handleJWKS(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	p.jwksCalls++
	p.mu.Unlock()
	public := p.signer.PublicKey
	writeJSON(w, http.StatusOK, map[string]any{
		"keys": []any{map[string]any{
			"kty": "RSA",
			"use": "sig",
			"alg": "RS256",
			"kid": "abs-test-key",
			"n":   base64.RawURLEncoding.EncodeToString(public.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(public.E)).Bytes()),
		}},
	})
}

func (p *mockProvider) handleAuthorize(w http.ResponseWriter, _ *http.Request) {
	// The scenarios take the authorization URL apart instead of following it.
	w.WriteHeader(http.StatusOK)
}

func (p *mockProvider) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.lastTokenForm = r.PostForm.Clone()
	p.lastTokenAuth = r.Header.Get("Authorization")
	status := p.tokenStatus
	p.tokenCalls++
	p.mu.Unlock()
	if status != 0 {
		writeJSON(w, status, map[string]any{"error": "invalid_grant", "error_description": "stub rejected the code"})
		return
	}
	code := r.PostForm.Get("code")
	p.mu.Lock()
	grant, ok := p.grants[code]
	mode := p.mode
	p.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant"})
		return
	}
	if grant.challenge != "" {
		verifier := r.PostForm.Get("code_verifier")
		if verifier == "" || s256(verifier) != grant.challenge {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant", "error_description": "PKCE verification failed"})
			return
		}
	}
	accessToken, err := randomHex(16)
	if err != nil {
		http.Error(w, "entropy", http.StatusInternalServerError)
		return
	}
	p.mu.Lock()
	p.byAccessToken[accessToken] = grant
	delete(p.grants, code)
	p.mu.Unlock()
	idToken := p.signIDToken(grant.userinfo, mode)
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": accessToken,
		"token_type":   "Bearer",
		"expires_in":   3600,
		"id_token":     idToken,
	})
}

func (p *mockProvider) handleUserinfo(w http.ResponseWriter, r *http.Request) {
	token := ""
	if header := r.Header.Get("Authorization"); len(header) > len("Bearer ") && header[:7] == "Bearer " {
		token = header[7:]
	}
	p.mu.Lock()
	p.userinfoCalls++
	grant, ok := p.byAccessToken[token]
	p.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid_token"})
		return
	}
	writeJSON(w, http.StatusOK, grant.userinfo)
}

// signIDToken builds the compact JWT the client verifies against the JWKS.
func (p *mockProvider) signIDToken(userinfo map[string]any, mode idTokenMode) string {
	now := time.Now().Unix()
	claims := map[string]any{
		"iss": p.issuer(),
		"aud": p.clientID,
		"sub": "",
		"iat": now - 1,
		"exp": now + 300,
	}
	for key, value := range userinfo {
		claims[key] = value
	}
	switch mode {
	case idTokenWrongIssuer:
		claims["iss"] = p.issuer() + "/other"
	case idTokenWrongAudience:
		claims["aud"] = "another-client"
	case idTokenExpired:
		claims["exp"] = now - 3600
		claims["iat"] = now - 7200
	}
	header := map[string]any{"alg": "RS256", "typ": "JWT", "kid": "abs-test-key"}
	payload := encodeSegment(claims)
	signingInput := encodeSegment(header) + "." + payload
	digest := sha256.Sum256([]byte(signingInput))
	key := p.signer
	if mode == idTokenBadSignature {
		key = p.foreignKey
	}
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		// rsa.SignPKCS1v15 only fails on bad parameters, which are fixed here.
		return ""
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func encodeSegment(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		// The values are maps of strings, numbers and booleans.
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value) // the response is a test stub; a write error ends the request
}

// s256 is the PKCE S256 challenge of a verifier, base64url without padding.
func s256(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

// randomVerifier returns a 43-character PKCE verifier (32 random bytes, hex).
func randomVerifier() string {
	// 16 bytes hex is 32 characters; the RFC allows 43-128, and the official
	// app uses 42 random bytes (84 hex characters), so use 32 bytes.
	value, err := randomHex(32)
	if err != nil {
		return "fallback-verifier-0123456789abcdef0123456789abcdef"
	}
	return value
}

func randomHex(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func cloneObject(value map[string]any) map[string]any {
	clone := make(map[string]any, len(value))
	for key, item := range value {
		clone[key] = item
	}
	return clone
}
