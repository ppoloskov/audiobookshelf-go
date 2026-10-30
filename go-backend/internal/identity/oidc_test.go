package identity

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestOIDCWebFlowValidatesStatePKCENonceIssuerAudienceAndJWKS(t *testing.T) {
	db, handler := identityFixture(t)
	defer db.Close()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const subject = "oidc-subject-1"
	if _, err := db.Exec(`UPDATE users SET extraData = ? WHERE id = ?`, `{"authOpenIDSub":"`+subject+`","seriesHideFromContinueListening":[]}`, "user-1"); err != nil {
		t.Fatal(err)
	}
	var nonce, challenge string
	var provider *httptest.Server
	provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/jwks":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{rsaJWK(&privateKey.PublicKey, "key-1")}})
		case "/token":
			if r.Method != http.MethodPost || r.FormValue("grant_type") != "authorization_code" {
				http.Error(w, "bad token request", 400)
				return
			}
			clientID, secret, ok := r.BasicAuth()
			if !ok || clientID != "abs-client" || secret != "abs-secret" {
				http.Error(w, "bad client auth", 401)
				return
			}
			verifier := r.FormValue("code_verifier")
			digest := sha256.Sum256([]byte(verifier))
			if base64.RawURLEncoding.EncodeToString(digest[:]) != challenge {
				http.Error(w, "bad verifier", 400)
				return
			}
			idToken := signedIDToken(t, privateKey, provider.URL, "abs-client", subject, nonce, time.Now().Add(time.Hour))
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"access_token":"provider-access","id_token":%q,"token_type":"Bearer"}`, idToken)
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer provider-access" {
				http.Error(w, "bad access token", 401)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"sub":"oidc-subject-1","groups":["Admin"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	configureOIDC(t, db, provider.URL, map[string]any{"authOpenIDGroupClaim": "groups"})

	start := httptest.NewRequest(http.MethodGet, "/audiobookshelf/auth/openid?callback=%2Faudiobookshelf%2Flogin", nil)
	start.Host = "abs.example"
	startResponse := httptest.NewRecorder()
	if !handler.Handle(startResponse, start) || startResponse.Code != http.StatusFound {
		t.Fatalf("OIDC start response = %d (%s)", startResponse.Code, startResponse.Body.String())
	}
	location, err := url.Parse(startResponse.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	query := location.Query()
	state := query.Get("state")
	nonce = query.Get("nonce")
	challenge = query.Get("code_challenge")
	if state == "" || nonce == "" || challenge == "" || query.Get("code_challenge_method") != "S256" || query.Get("response_type") != "code" {
		t.Fatalf("incomplete authorization request: %v", query)
	}
	callback := httptest.NewRequest(http.MethodGet, "/audiobookshelf/auth/openid/callback?code=good-code&state="+url.QueryEscape(state), nil)
	callback.Host = "abs.example"
	for _, cookie := range startResponse.Result().Cookies() {
		callback.AddCookie(cookie)
	}
	callbackResponse := httptest.NewRecorder()
	handler.Handle(callbackResponse, callback)
	if callbackResponse.Code != http.StatusFound {
		t.Fatalf("OIDC callback response = %d (%s)", callbackResponse.Code, callbackResponse.Body.String())
	}
	redirect, err := url.Parse(callbackResponse.Header().Get("Location"))
	if err != nil || redirect.Path != "/audiobookshelf/login" || redirect.Query().Get("setToken") == "" || redirect.Query().Get("accessToken") == "" {
		t.Fatalf("unexpected success redirect %q: err=%v", callbackResponse.Header().Get("Location"), err)
	}
	var userType string
	if err := db.QueryRow(`SELECT type FROM users WHERE id = ?`, "user-1").Scan(&userType); err != nil || userType != "admin" {
		t.Fatalf("group mapping produced type=%q err=%v", userType, err)
	}
	var sessions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE userId = ?`, "user-1").Scan(&sessions); err != nil || sessions != 1 {
		t.Fatalf("OIDC callback sessions=%d err=%v", sessions, err)
	}
	var refreshCookie, idTokenCookie bool
	for _, cookie := range callbackResponse.Result().Cookies() {
		if cookie.Name == "refresh_token" && cookie.HttpOnly && cookie.Path == "/" {
			refreshCookie = true
		}
		if cookie.Name == "openid_id_token" && cookie.HttpOnly && cookie.Secure && cookie.SameSite == http.SameSiteStrictMode {
			idTokenCookie = true
		}
	}
	if !refreshCookie || !idTokenCookie {
		t.Fatalf("expected refresh and id-token cookies; refresh=%v id=%v", refreshCookie, idTokenCookie)
	}
}

func TestPruneOIDCStateMatchesSessionAndMobileRedirectLifetimes(t *testing.T) {
	now := time.Now()
	handler := &Handler{
		oidcFlows: map[string]oidcFlow{
			"web-after-mobile-ttl":    {started: now.Add(-oidcMobileRedirectTTL - time.Second)},
			"mobile-after-mobile-ttl": {mobile: true, started: now.Add(-oidcMobileRedirectTTL - time.Second)},
			"session-expired":         {started: now.Add(-oidcSessionTTL - time.Second)},
		},
		mobileFlows: map[string]mobileFlow{
			"redirect-expired": {started: now.Add(-oidcMobileRedirectTTL - time.Second)},
			"redirect-valid":   {started: now.Add(-oidcMobileRedirectTTL + time.Second)},
		},
	}

	handler.pruneOIDCState(now)
	if _, ok := handler.oidcFlows["web-after-mobile-ttl"]; !ok {
		t.Fatal("OIDC session flow was pruned at the shorter mobile redirect TTL")
	}
	if _, ok := handler.oidcFlows["mobile-after-mobile-ttl"]; !ok {
		t.Fatal("mobile callback flow was pruned before the OIDC session TTL")
	}
	if _, ok := handler.oidcFlows["session-expired"]; ok {
		t.Fatal("OIDC session flow outlived the upstream session TTL")
	}
	if _, ok := handler.mobileFlows["redirect-expired"]; ok {
		t.Fatal("mobile redirect mapping outlived the upstream LRU TTL")
	}
	if _, ok := handler.mobileFlows["redirect-valid"]; !ok {
		t.Fatal("mobile redirect mapping expired before the upstream LRU TTL")
	}
}

func TestOIDCRejectsNonceMismatchAndStateReplay(t *testing.T) {
	db, handler := identityFixture(t)
	defer db.Close()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var nonce, challenge string
	var provider *httptest.Server
	provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/jwks":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{rsaJWK(&privateKey.PublicKey, "key-1")}})
		case "/token":
			verifier := r.FormValue("code_verifier")
			digest := sha256.Sum256([]byte(verifier))
			if base64.RawURLEncoding.EncodeToString(digest[:]) != challenge {
				http.Error(w, "bad verifier", 400)
				return
			}
			badToken := signedIDToken(t, privateKey, provider.URL, "abs-client", "oidc-subject", nonce+"wrong", time.Now().Add(time.Hour))
			fmt.Fprintf(w, `{"access_token":"provider-access","id_token":%q}`, badToken)
		case "/userinfo":
			fmt.Fprint(w, `{"sub":"oidc-subject"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	configureOIDC(t, db, provider.URL, nil)
	start := httptest.NewRequest(http.MethodGet, "/auth/openid?callback=%2Faudiobookshelf%2Flogin", nil)
	start.Host = "abs.example"
	startResponse := httptest.NewRecorder()
	handler.Handle(startResponse, start)
	location, err := url.Parse(startResponse.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	nonce, challenge = location.Query().Get("nonce"), location.Query().Get("code_challenge")
	state := location.Query().Get("state")
	callback := httptest.NewRequest(http.MethodGet, "/auth/openid/callback?code=bad&state="+url.QueryEscape(state), nil)
	callback.Host = "abs.example"
	for _, cookie := range startResponse.Result().Cookies() {
		callback.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.Handle(response, callback)
	if response.Code != http.StatusFound || !strings.HasPrefix(response.Header().Get("Location"), "/login?error=Unauthorized") {
		t.Fatalf("nonce mismatch should reject callback: status=%d location=%q body=%q", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	var sessions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("invalid OIDC token created sessions=%d err=%v", sessions, err)
	}
	// The state is single-use even when the callback token validation fails.
	replay := httptest.NewRecorder()
	handler.Handle(replay, callback)
	replayURL, _ := url.Parse(replay.Header().Get("Location"))
	if replay.Code != http.StatusFound || replayURL.Query().Get("error") != "No sign-in session" {
		t.Fatalf("state replay was not rejected: status=%d location=%q", replay.Code, replay.Header().Get("Location"))
	}
}

func TestOIDCVerifierRejectsWrongIssuerAndAudience(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		issuer string
		aud    string
	}{
		{name: "issuer", issuer: "https://wrong-issuer.example", aud: "abs-client"},
		{name: "audience", aud: "other-client"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				t.Fatal(err)
			}
			nonce := "expected-nonce"
			var provider *httptest.Server
			provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/jwks":
					_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{rsaJWK(&privateKey.PublicKey, "key-1")}})
				case "/token":
					issuer := testCase.issuer
					if issuer == "" {
						issuer = provider.URL
					}
					token := signedIDTokenForClient(t, privateKey, issuer, testCase.aud, "sub-1", nonce, time.Now().Add(time.Hour))
					fmt.Fprintf(w, `{"access_token":"access","id_token":%q}`, token)
				default:
					http.NotFound(w, r)
				}
			}))
			defer provider.Close()
			cfg := oidcConfig{issuer: provider.URL, tokenURL: provider.URL + "/token", userinfoURL: provider.URL + "/userinfo", jwksURL: provider.URL + "/jwks", clientID: "abs-client", clientSecret: "abs-secret", signingAlgorithm: "RS256"}
			_, _, err = exchangeAndLoadUserInfo(t.Context(), cfg, "code", provider.URL+"/callback", "", nonce)
			if err == nil {
				t.Fatal("ID token with mismatched issuer or audience was accepted")
			}
		})
	}
}

func TestOIDCAutoRegisterAndMobileRedirectAllowlist(t *testing.T) {
	db, handler := identityFixture(t)
	defer db.Close()
	var provider *httptest.Server
	provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }))
	defer provider.Close()
	configureOIDC(t, db, provider.URL, nil)
	settings, err := handler.loadSettings(t.Context(), "server-settings")
	if err != nil {
		t.Fatal(err)
	}
	settings["authOpenIDAutoRegister"] = true
	data, _ := json.Marshal(settings)
	if _, err := db.Exec(`UPDATE settings SET value = ? WHERE key = 'server-settings'`, string(data)); err != nil {
		t.Fatal(err)
	}
	cfg, err := handler.readOIDCConfig(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	user, err := handler.userFromOIDCInfo(t.Context(), cfg, map[string]any{"sub": "new-sub", "preferred_username": "new-reader", "email": "new@example.org", "email_verified": true})
	if err != nil || user.Username != "new-reader" || user.Type != "user" || !user.IsActive {
		t.Fatalf("auto-registration user=%#v err=%v", user, err)
	}

	settings["authOpenIDMobileRedirectURIs"] = []any{"audiobookshelf://oauth"}
	data, _ = json.Marshal(settings)
	if _, err := db.Exec(`UPDATE settings SET value = ? WHERE key = 'server-settings'`, string(data)); err != nil {
		t.Fatal(err)
	}
	cfg, err = handler.readOIDCConfig(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	allowed := httptest.NewRecorder()
	allowedRequest := httptest.NewRequest(http.MethodGet, "/auth/openid?response_type=code&redirect_uri=audiobookshelf%3A%2F%2Foauth&code_challenge=some-challenge&code_challenge_method=S256", nil)
	allowedRequest.Host = "abs.example"
	handler.Handle(allowed, allowedRequest)
	if allowed.Code != http.StatusFound {
		t.Fatalf("allowed mobile redirect returned %d: %s", allowed.Code, allowed.Body.String())
	}
	denied := httptest.NewRecorder()
	deniedRequest := httptest.NewRequest(http.MethodGet, "/auth/openid?response_type=code&redirect_uri=evil%3A%2F%2Fapp&code_challenge=some-challenge", nil)
	deniedRequest.Host = "abs.example"
	handler.Handle(denied, deniedRequest)
	if denied.Code != http.StatusBadRequest || !strings.Contains(denied.Body.String(), "Invalid redirect_uri") {
		t.Fatalf("unlisted mobile redirect returned %d: %s", denied.Code, denied.Body.String())
	}
}

func TestOIDCCallbackEmptyCodeVerifierWithoutSessionUsesWebRedirect(t *testing.T) {
	db, handler := identityFixture(t)
	defer db.Close()
	configureOIDC(t, db, "https://provider.example", nil)
	request := httptest.NewRequest(http.MethodGet, "/auth/openid/callback?state=missing-state&code_verifier=", nil)
	request.Host = "abs.example"
	response := httptest.NewRecorder()
	if !handler.Handle(response, request) {
		t.Fatal("OIDC callback route was not handled")
	}
	want := "/login?error=No%20sign-in%20session&autoLaunch=0"
	if response.Code != http.StatusFound || response.Header().Get("Location") != want {
		t.Fatalf("response = %d location %q, want 302 %q", response.Code, response.Header().Get("Location"), want)
	}
}

func TestOIDCCallbackDuplicateEmptyVerifiersWithoutSessionUsesMobileError(t *testing.T) {
	db, handler := identityFixture(t)
	defer db.Close()
	configureOIDC(t, db, "https://provider.example", nil)
	request := httptest.NewRequest(http.MethodGet, "/auth/openid/callback?state=missing-state&code_verifier=&code_verifier=", nil)
	request.Host = "abs.example"
	response := httptest.NewRecorder()
	if !handler.Handle(response, request) {
		t.Fatal("OIDC callback route was not handled")
	}
	body := "No session"
	if response.Code != http.StatusBadRequest || response.Body.String() != body {
		t.Fatalf("response = %d %q, want 400 %q", response.Code, response.Body.String(), body)
	}
	if got := response.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want Express text response", got)
	}
}

func TestOIDCMobileCallbackMissingSessionUsesExpressTextResponse(t *testing.T) {
	db, handler := identityFixture(t)
	defer db.Close()
	configureOIDC(t, db, "https://provider.example", map[string]any{
		"authOpenIDMobileRedirectURIs": []any{"audiobookshelf://oauth"},
	})
	request := httptest.NewRequest(http.MethodGet, "/auth/openid/callback?state=expired-state&code=mobile-code&code_verifier=mobile-verifier", nil)
	request.Host = "abs.example"
	request.AddCookie(&http.Cookie{Name: "auth_method", Value: "openid-mobile"})
	response := httptest.NewRecorder()
	if !handler.Handle(response, request) {
		t.Fatal("OIDC callback route was not handled")
	}
	body := "No session"
	if response.Code != http.StatusBadRequest || response.Body.String() != body {
		t.Fatalf("response = %d %q, want 400 %q", response.Code, response.Body.String(), body)
	}
	if got := response.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want Express text response", got)
	}
	if got := response.Header().Get("Content-Length"); got != fmt.Sprint(len(body)) {
		t.Fatalf("Content-Length = %q, want %d", got, len(body))
	}
	if got := response.Header().Get("ETag"); !strings.HasPrefix(got, `W/"`) {
		t.Fatalf("ETag = %q, want Express weak ETag", got)
	}
}

func TestOIDCCallbackProviderFailureUsesLoginRedirect(t *testing.T) {
	db, h := identityFixture(t)
	defer db.Close()

	response := httptest.NewRecorder()
	h.oidcFailure(response, http.StatusUnauthorized, "Unauthorized")

	if response.Code != http.StatusFound || response.Header().Get("Location") != "/login?error=Unauthorized&autoLaunch=0" {
		t.Fatalf("mobile callback failure = status %d, location %q; want upstream login redirect", response.Code, response.Header().Get("Location"))
	}
}

func TestOIDCMobileRedirectStateMismatchUsesExpressTextResponse(t *testing.T) {
	db, handler := identityFixture(t)
	defer db.Close()
	configureOIDC(t, db, "https://provider.example", map[string]any{
		"authOpenIDMobileRedirectURIs": []any{"audiobookshelf://oauth"},
	})
	request := httptest.NewRequest(http.MethodGet, "/auth/openid/mobile-redirect?state=unknown&code=test", nil)
	request.Host = "abs.example"
	response := httptest.NewRecorder()
	if !handler.Handle(response, request) {
		t.Fatal("mobile redirect route was not handled")
	}
	body := "State parameter mismatch"
	if response.Code != http.StatusBadRequest || response.Body.String() != body {
		t.Fatalf("response = %d %q, want 400 %q", response.Code, response.Body.String(), body)
	}
	if got := response.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want Express text response", got)
	}
	if got := response.Header().Get("Content-Length"); got != fmt.Sprint(len(body)) {
		t.Fatalf("Content-Length = %q, want %d", got, len(body))
	}
	if got := response.Header().Get("ETag"); !strings.HasPrefix(got, `W/"`) {
		t.Fatalf("ETag = %q, want Express weak ETag", got)
	}

	handler.oidcMu.Lock()
	handler.mobileFlows["known-state"] = mobileFlow{redirectURI: "audiobookshelf://oauth", started: time.Now()}
	handler.oidcMu.Unlock()
	validRequest := httptest.NewRequest(http.MethodGet, "/auth/openid/mobile-redirect?state=known-state&code=mobile-code", nil)
	validRequest.Host = "abs.example"
	validResponse := httptest.NewRecorder()
	if !handler.Handle(validResponse, validRequest) {
		t.Fatal("mobile redirect route was not handled")
	}
	target := "audiobookshelf://oauth?code=mobile-code&state=known-state"
	redirectBody := "Found. Redirecting to " + target
	if validResponse.Code != http.StatusFound || validResponse.Header().Get("Location") != target || validResponse.Body.String() != redirectBody {
		t.Fatalf("redirect = %d location=%q body=%q", validResponse.Code, validResponse.Header().Get("Location"), validResponse.Body.String())
	}
	if validResponse.Header().Get("Content-Type") != "text/plain; charset=utf-8" || validResponse.Header().Get("Content-Length") != fmt.Sprint(len(redirectBody)) {
		t.Fatalf("redirect headers = %#v", validResponse.Header())
	}
}

func TestOIDCLogoutBuildsEndSessionRedirectAndClearsCookies(t *testing.T) {
	db, handler := identityFixture(t)
	defer db.Close()
	settings, err := handler.loadSettings(t.Context(), "server-settings")
	if err != nil {
		t.Fatal(err)
	}
	settings["authOpenIDLogoutURL"] = "https://issuer.example/logout?existing=1"
	encoded, _ := json.Marshal(settings)
	if _, err := db.Exec(`UPDATE settings SET value = ? WHERE key = 'server-settings'`, string(encoded)); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/logout", nil)
	r.Host = "abs.example"
	sessionCookie, err := handler.sessionCookieValue(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatal(err)
	}
	r.AddCookie(&http.Cookie{Name: "connect.sid", Value: sessionCookie})
	r.AddCookie(&http.Cookie{Name: "auth_method", Value: "openid"})
	r.AddCookie(&http.Cookie{Name: "openid_id_token", Value: "signed.id.token"})
	w := httptest.NewRecorder()
	handler.Handle(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("logout status=%d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	redirect, err := url.Parse(body["redirect_url"].(string))
	if err != nil || redirect.Query().Get("id_token_hint") != "signed.id.token" || redirect.Query().Get("post_logout_redirect_uri") != "http://abs.example/audiobookshelf/login" || redirect.Query().Get("existing") != "1" {
		t.Fatalf("unexpected end-session redirect %#v err=%v", body["redirect_url"], err)
	}
	cleared := map[string]bool{}
	for _, cookie := range w.Result().Cookies() {
		if cookie.Expires.Year() == 1970 && cookie.MaxAge == 0 {
			cleared[cookie.Name] = true
		}
		if cookie.Name == "connect.sid" && cookie.Value != sessionCookie {
			t.Fatalf("logout changed the signed session cookie: got %q want %q", cookie.Value, sessionCookie)
		}
	}
	if !cleared["refresh_token"] || !cleared["auth_method"] || !cleared["openid_id_token"] {
		t.Fatalf("logout did not expire expected cookies: %#v", cleared)
	}
	if got := w.Header().Get("Content-Length"); got != fmt.Sprint(w.Body.Len()) {
		t.Fatalf("Content-Length = %q, want %d", got, w.Body.Len())
	}
	if got := w.Header().Get("ETag"); !strings.HasPrefix(got, `W/"`) {
		t.Fatalf("ETag = %q, want Express weak ETag", got)
	}
}

func TestOIDCVerifierRejectsDisallowedSigningAlgorithm(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	nonce := "expected-nonce"
	var provider *httptest.Server
	provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/jwks" {
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{rsaJWK(&privateKey.PublicKey, "key-1")}})
			return
		}
		if r.URL.Path == "/token" {
			token := signedIDTokenWithAlgorithm(t, privateKey, provider.URL, "abs-client", "sub-1", nonce, time.Now().Add(time.Hour), "HS256")
			fmt.Fprintf(w, `{"access_token":"access","id_token":%q}`, token)
			return
		}
		http.NotFound(w, r)
	}))
	defer provider.Close()
	cfg := oidcConfig{issuer: provider.URL, tokenURL: provider.URL + "/token", userinfoURL: provider.URL + "/userinfo", jwksURL: provider.URL + "/jwks", clientID: "abs-client", clientSecret: "abs-secret", signingAlgorithm: "RS256"}
	if _, _, err = exchangeAndLoadUserInfo(t.Context(), cfg, "code", provider.URL+"/callback", "", nonce); err == nil {
		t.Fatal("HS256 ID token accepted while RS256 configured")
	}
}

func TestOIDCLinksExistingUserByConfiguredUsername(t *testing.T) {
	db, h := identityFixture(t)
	defer db.Close()
	if _, err := db.Exec(`UPDATE users SET extraData = ? WHERE id = ?`, `{"seriesHideFromContinueListening":[]}`, "user-1"); err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer provider.Close()
	configureOIDC(t, db, provider.URL, nil)
	cfg, err := h.readOIDCConfig(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cfg.matchExistingBy = "username"
	user, err := h.userFromOIDCInfo(t.Context(), cfg, map[string]any{"sub": "linked-sub", "preferred_username": "Reader"})
	if err != nil || user.ID != "user-1" {
		t.Fatalf("matched user=%#v err=%v", user, err)
	}
	linked, err := h.userByOIDCSub(t.Context(), "linked-sub")
	if err != nil || linked.ID != "user-1" {
		t.Fatalf("linked user=%#v err=%v", linked, err)
	}
	var persisted string
	if err := db.QueryRow(`SELECT extraData FROM users WHERE id = ?`, "user-1").Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if want := `{"seriesHideFromContinueListening":[],"authOpenIDSub":"linked-sub"}`; persisted != want {
		t.Fatalf("linked extraData = %s, want source field order %s", persisted, want)
	}
}

func TestLocalLoginDisabledWhenNotActive(t *testing.T) {
	db, h := identityFixture(t)
	defer db.Close()
	_, err := db.Exec(`UPDATE settings SET value = ? WHERE key = 'server-settings'`, `{"id":"server-settings","tokenSecret":"test-secret","authActiveAuthMethods":["openid"]}`)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"reader","password":"shelf"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Handle(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("disabled local auth returned %d", w.Code)
	}
}

func configureOIDC(t *testing.T, db *sql.DB, issuer string, override map[string]any) {
	t.Helper()
	var raw []byte
	if err := db.QueryRow(`SELECT value FROM settings WHERE key = 'server-settings'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]any{
		"authActiveAuthMethods":              []any{"local", "openid"},
		"authOpenIDIssuerURL":                issuer,
		"authOpenIDAuthorizationURL":         issuer + "/authorize",
		"authOpenIDTokenURL":                 issuer + "/token",
		"authOpenIDUserInfoURL":              issuer + "/userinfo",
		"authOpenIDJwksURL":                  issuer + "/jwks",
		"authOpenIDClientID":                 "abs-client",
		"authOpenIDClientSecret":             "abs-secret",
		"authOpenIDTokenSigningAlgorithm":    "RS256",
		"authOpenIDSubfolderForRedirectURLs": "/audiobookshelf",
	} {
		settings[key] = value
	}
	for key, value := range override {
		settings[key] = value
	}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE settings SET value = ? WHERE key = 'server-settings'`, string(data)); err != nil {
		t.Fatal(err)
	}
}

func signedIDToken(t *testing.T, key *rsa.PrivateKey, issuer, audience, subject, nonce string, expires time.Time) string {
	return signedIDTokenForClient(t, key, issuer, audience, subject, nonce, expires)
}

func signedIDTokenForClient(t *testing.T, key *rsa.PrivateKey, issuer, audience, subject, nonce string, expires time.Time) string {
	t.Helper()
	return signedIDTokenWithAlgorithm(t, key, issuer, audience, subject, nonce, expires, "RS256")
}

func signedIDTokenWithAlgorithm(t *testing.T, key *rsa.PrivateKey, issuer, audience, subject, nonce string, expires time.Time, algorithm string) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": algorithm, "kid": "key-1", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{"iss": issuer, "aud": audience, "sub": subject, "iat": time.Now().Unix(), "exp": expires.Unix(), "nonce": nonce})
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func rsaJWK(key *rsa.PublicKey, kid string) map[string]string {
	e := big.NewInt(int64(key.E)).Bytes()
	return map[string]string{"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(e)}
}
