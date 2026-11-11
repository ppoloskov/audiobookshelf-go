package auth

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
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

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/db"
)

// idp is a fake identity provider: JWKS, token and userinfo endpoints. Tests
// set what it will say before the callback.
type idp struct {
	*httptest.Server
	key                   *rsa.PrivateKey
	nonce, challenge      string
	issuer, audience, alg string // overrides for the ID token; "" means correct
	userinfo              string
}

func newIDP(t *testing.T) *idp {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &idp{key: key, userinfo: `{"sub":"sub-1","groups":["Admin"]}`}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/jwks":
			e := big.NewInt(int64(key.E)).Bytes()
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256",
				"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(e)}}})
		case "/token":
			id, secret, ok := r.BasicAuth()
			sum := sha256.Sum256([]byte(r.FormValue("code_verifier")))
			if !ok || id != "abs-client" || secret != "abs-secret" || r.FormValue("code") != "good-code" || base64.RawURLEncoding.EncodeToString(sum[:]) != p.challenge {
				http.Error(w, "bad request", 400)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"access_token":"provider-access","token_type":"Bearer","id_token":%q}`, p.idToken())
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer provider-access" {
				http.Error(w, "no", 401)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(p.userinfo))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *idp) idToken() string {
	or := func(v, fallback string) string {
		if v != "" {
			return v
		}
		return fallback
	}
	alg := or(p.alg, "RS256")
	header, _ := json.Marshal(map[string]string{"alg": alg, "kid": "k1", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{"iss": or(p.issuer, p.URL), "aud": or(p.audience, "abs-client"), "sub": "sub-1", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(), "nonce": p.nonce})
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	sum := sha256.Sum256([]byte(unsigned))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// openIDService is a Service whose server settings point at a fresh IdP. extra
// is more JSON for the settings, starting with a comma.
func openIDService(t *testing.T, extra string) (*Service, *db.DB, *idp, http.Handler, *[]string) {
	t.Helper()
	p := newIDP(t)
	events := &[]string{}
	s, d, mux := newService(t, func(c *config.Config, d *db.DB) {
		patch := fmt.Sprintf(`{"authActiveAuthMethods":["local","openid"],"authOpenIDIssuerURL":%q,"authOpenIDAuthorizationURL":%q,"authOpenIDTokenURL":%q,"authOpenIDUserInfoURL":%q,"authOpenIDJwksURL":%q,"authOpenIDClientID":"abs-client","authOpenIDClientSecret":"abs-secret","authOpenIDTokenSigningAlgorithm":"RS256","authOpenIDSubfolderForRedirectURLs":"/sub"%s}`,
			p.URL, p.URL+"/authorize", p.URL+"/token", p.URL+"/userinfo", p.URL+"/jwks", extra)
		mustExec(t, d, `UPDATE settings SET value = json_patch(value, ?) WHERE key = 'server-settings'`, patch)
	})
	s.emit = func(audience, event string, payload any) {
		raw, _ := json.Marshal(payload)
		*events = append(*events, audience+" "+event+" "+string(raw))
	}
	return s, d, p, mux, events
}

// startWeb begins a web sign-in and returns the provider URL query and the
// cookies to send back.
func startWeb(t *testing.T, mux http.Handler, p *idp) (url.Values, []*http.Cookie) {
	t.Helper()
	w := send(mux, "GET", "/auth/openid?callback=%2Flogin", "", nil)
	location, err := url.Parse(w.Header().Get("Location"))
	if w.Code != http.StatusFound || err != nil || !strings.HasPrefix(location.String(), p.URL+"/authorize") {
		t.Fatalf("start = %d %q", w.Code, w.Header().Get("Location"))
	}
	query := location.Query()
	p.nonce, p.challenge = query.Get("nonce"), query.Get("code_challenge")
	if query.Get("state") == "" || p.nonce == "" || p.challenge == "" || query.Get("code_challenge_method") != "S256" || query.Get("response_type") != "code" ||
		query.Get("client_id") != "abs-client" || query.Get("redirect_uri") != "http://abs.example/sub/auth/openid/callback" || query.Get("scope") != "openid profile email groups" && query.Get("scope") != "openid profile email" {
		t.Fatalf("authorization request %v", query)
	}
	return query, w.Result().Cookies()
}

func callbackWith(t *testing.T, mux http.Handler, state string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", "/auth/openid/callback?code=good-code&state="+url.QueryEscape(state), nil)
	r.Host = "abs.example"
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestOIDCWebFlowSignsInAndMapsGroups(t *testing.T) {
	_, d, p, mux, _ := openIDService(t, `,"authOpenIDGroupClaim":"groups"`)
	mustExec(t, d, `UPDATE users SET extraData = '{"authOpenIDSub":"sub-1","seriesHideFromContinueListening":[]}' WHERE id = ?`, listenerID)
	query, cookies := startWeb(t, mux, p)
	w := callbackWith(t, mux, query.Get("state"), cookies)
	target, _ := url.Parse(w.Header().Get("Location"))
	if w.Code != http.StatusFound || target.Path != "/login" || target.Query().Get("setToken") == "" || target.Query().Get("accessToken") == "" {
		t.Fatalf("callback = %d %q", w.Code, w.Header().Get("Location"))
	}
	if kind := count(t, d, `SELECT COUNT(*) FROM users WHERE id = ? AND type = 'admin'`, listenerID); kind != 1 || count(t, d, `SELECT COUNT(*) FROM sessions WHERE userId = ?`, listenerID) != 1 {
		t.Fatal("the Admin group must make the user an admin and open one session")
	}
	var refresh, idToken bool
	for _, c := range w.Result().Cookies() {
		refresh = refresh || (c.Name == "refresh_token" && c.HttpOnly)
		idToken = idToken || (c.Name == "openid_id_token" && c.HttpOnly && c.Secure && c.SameSite == http.SameSiteStrictMode)
	}
	if !refresh || !idToken {
		t.Fatalf("cookies %v", w.Result().Cookies())
	}
	// A state is used once.
	again := callbackWith(t, mux, query.Get("state"), cookies)
	if loc := again.Header().Get("Location"); !strings.Contains(loc, "No%20sign-in%20session") {
		t.Fatalf("replay = %d %q", again.Code, loc)
	}
}

func TestOIDCRejectsBadIDTokens(t *testing.T) {
	for name, tune := range map[string]func(*idp){
		"nonce":    func(p *idp) { p.nonce += "x" },
		"issuer":   func(p *idp) { p.issuer = "https://wrong.example" },
		"audience": func(p *idp) { p.audience = "other-client" },
		"alg":      func(p *idp) { p.alg = "HS256" },
		"subject":  func(p *idp) { p.userinfo = `{"sub":"someone-else","groups":["Admin"]}` },
	} {
		t.Run(name, func(t *testing.T) {
			_, d, p, mux, _ := openIDService(t, `,"authOpenIDGroupClaim":"groups"`)
			mustExec(t, d, `UPDATE users SET extraData = '{"authOpenIDSub":"sub-1"}' WHERE id = ?`, listenerID)
			query, cookies := startWeb(t, mux, p)
			tune(p)
			w := callbackWith(t, mux, query.Get("state"), cookies)
			if !strings.HasPrefix(w.Header().Get("Location"), "/login?error=Unauthorized") || count(t, d, `SELECT COUNT(*) FROM sessions`) != 0 {
				t.Fatalf("callback = %d %q, sessions %d", w.Code, w.Header().Get("Location"), count(t, d, `SELECT COUNT(*) FROM sessions`))
			}
		})
	}
}

func TestOIDCMobileFlow(t *testing.T) {
	_, d, p, mux, events := openIDService(t, `,"authOpenIDMobileRedirectURIs":["audiobookshelf://oauth"],"authOpenIDAutoRegister":true`)
	verifier := "mobile-verifier-0123456789-0123456789-0123456789"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	w := send(mux, "GET", "/auth/openid?response_type=code&redirect_uri=audiobookshelf%3A%2F%2Foauth&state=app-state&code_challenge="+challenge+"&code_challenge_method=S256", "", nil)
	location, _ := url.Parse(w.Header().Get("Location"))
	query := location.Query()
	if w.Code != http.StatusFound || query.Get("state") != "app-state" || query.Get("code_challenge") != challenge || query.Get("redirect_uri") != "http://abs.example/sub/auth/openid/mobile-redirect" {
		t.Fatalf("start = %d %q", w.Code, location)
	}
	p.nonce, p.challenge = query.Get("nonce"), challenge
	p.userinfo = `{"sub":"sub-1","preferred_username":"app-user","email":"a@example.org","email_verified":true}`

	redirect := send(mux, "GET", "/auth/openid/mobile-redirect?code=good-code&state=app-state", "", nil)
	if redirect.Code != http.StatusFound || redirect.Header().Get("Location") != "audiobookshelf://oauth?code=good-code&state=app-state" {
		t.Fatalf("mobile redirect = %d %q", redirect.Code, redirect.Header().Get("Location"))
	}
	if again := send(mux, "GET", "/auth/openid/mobile-redirect?code=good-code&state=app-state", "", nil); again.Code != 400 {
		t.Fatalf("mobile redirect replay = %d", again.Code)
	}
	cb := send(mux, "GET", "/auth/openid/callback?code=good-code&state=app-state&code_verifier="+verifier, "", nil, &http.Cookie{Name: "auth_method", Value: "openid-mobile"})
	var body struct {
		User struct{ Username, AccessToken, RefreshToken string }
	}
	if err := json.Unmarshal(cb.Body.Bytes(), &body); err != nil || cb.Code != 200 || body.User.Username != "app-user" || body.User.AccessToken == "" || body.User.RefreshToken == "" {
		t.Fatalf("callback = %d %s", cb.Code, cb.Body)
	}
	if count(t, d, `SELECT COUNT(*) FROM users WHERE username = 'app-user' AND email = 'a@example.org' AND type = 'user' AND pash IS NULL`) != 1 ||
		len(*events) != 1 || !strings.HasPrefix((*events)[0], "@admins user_added ") {
		t.Fatalf("registration: events %v", *events)
	}
	for path, want := range map[string]int{
		"/auth/openid?response_type=code&redirect_uri=evil%3A%2F%2Fx&code_challenge=abc":                                         400,
		"/auth/openid?response_type=code&redirect_uri=audiobookshelf%3A%2F%2Foauth":                                              400, // no PKCE challenge
		"/auth/openid?response_type=code&redirect_uri=audiobookshelf%3A%2F%2Foauth&code_challenge=a&code_challenge_method=plain": 400,
		"/auth/openid?response_type=token":                    400,
		"/auth/openid?callback=%2Flogin&state=x":              400,
		"/auth/openid":                                        400,
		"/auth/openid?callback=http%3A%2F%2Fevil.example%2Fx": 400,
		"/auth/openid/mobile-redirect?state=unknown&code=c":   400,
		"/auth/openid/callback?state=missing&code_verifier=v": 400,
	} {
		if w := send(mux, "GET", path, "", nil); w.Code != want {
			t.Errorf("%s = %d, want %d", path, w.Code, want)
		}
	}
	if w := send(mux, "GET", "/auth/openid/callback?state=missing", "", nil); w.Code != http.StatusFound || !strings.Contains(w.Header().Get("Location"), "No%20sign-in%20session") {
		t.Fatalf("web callback without a session = %d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestOIDCUserMatching(t *testing.T) {
	t.Run("link by username", func(t *testing.T) {
		s, d, _, _, _ := openIDService(t, `,"authOpenIDMatchExistingBy":"username"`)
		user, err := s.oidcUser(t.Context(), s.settings.Server(), claimSet{"sub": json.RawMessage(`"link-1"`), "preferred_username": json.RawMessage(`"Trial-Listener"`)})
		if err != nil || user.ID != listenerID {
			t.Fatalf("user %v err %v", user.ID, err)
		}
		if count(t, d, `SELECT COUNT(*) FROM users WHERE id = ? AND json_extract(extraData, '$.authOpenIDSub') = 'link-1' AND json_extract(extraData, '$.seriesHideFromContinueListening') IS NOT NULL`, listenerID) != 1 {
			t.Fatal("subject not recorded next to the other extraData keys")
		}
		if _, err := s.oidcUser(t.Context(), s.settings.Server(), claimSet{"sub": json.RawMessage(`"link-2"`), "preferred_username": json.RawMessage(`"trial-listener"`)}); err == nil {
			t.Fatal("a user linked to another subject was linked again")
		}
	})
	t.Run("email must be verified", func(t *testing.T) {
		s, d, _, _, _ := openIDService(t, `,"authOpenIDMatchExistingBy":"email"`)
		mustExec(t, d, `UPDATE users SET email = 'me@example.org' WHERE id = ?`, listenerID)
		claims := claimSet{"sub": json.RawMessage(`"e-1"`), "email": json.RawMessage(`"ME@example.org"`), "email_verified": json.RawMessage(`false`)}
		if _, err := s.oidcUser(t.Context(), s.settings.Server(), claims); err == nil {
			t.Fatal("unverified email matched")
		}
		claims["email_verified"] = json.RawMessage(`true`)
		if user, err := s.oidcUser(t.Context(), s.settings.Server(), claims); err != nil || user.ID != listenerID {
			t.Fatalf("user %v err %v", user.ID, err)
		}
	})
	t.Run("unknown user without auto register", func(t *testing.T) {
		s, _, _, _, _ := openIDService(t, "")
		if _, err := s.oidcUser(t.Context(), s.settings.Server(), claimSet{"sub": json.RawMessage(`"nobody"`)}); err == nil {
			t.Fatal("registered although auto-register is off")
		}
	})
	t.Run("failed claims leave no new user", func(t *testing.T) {
		s, d, _, _, events := openIDService(t, `,"authOpenIDAutoRegister":true,"authOpenIDGroupClaim":"groups"`)
		before := count(t, d, `SELECT COUNT(*) FROM users`)
		claims := claimSet{"sub": json.RawMessage(`"new-1"`), "groups": json.RawMessage(`["nonsense"]`)}
		if _, err := s.oidcUser(t.Context(), s.settings.Server(), claims); err == nil || count(t, d, `SELECT COUNT(*) FROM users`) != before || len(*events) != 0 {
			t.Fatalf("err %v users %d events %v", err, count(t, d, `SELECT COUNT(*) FROM users`), *events)
		}
	})
	t.Run("root keeps its role", func(t *testing.T) {
		s, d, _, _, _ := openIDService(t, `,"authOpenIDGroupClaim":"groups"`)
		mustExec(t, d, `UPDATE users SET extraData = '{"authOpenIDSub":"r-1"}' WHERE type = 'root'`)
		if _, err := s.oidcUser(t.Context(), s.settings.Server(), claimSet{"sub": json.RawMessage(`"r-1"`), "groups": json.RawMessage(`["user"]`)}); err == nil {
			t.Fatal("root was downgraded")
		}
	})
	t.Run("advanced permissions", func(t *testing.T) {
		s, d, _, _, _ := openIDService(t, `,"authOpenIDAutoRegister":true,"authOpenIDAdvancedPermsClaim":"perms"`)
		claims := claimSet{"sub": json.RawMessage(`"p-1"`), "perms": json.RawMessage(`{"canUpdate":true,"canAccessAllLibraries":false,"allowedLibraries":["lib-a"],"tagsAreDenylist":true,"allowedTags":["t"]}`)}
		user, err := s.oidcUser(t.Context(), s.settings.Server(), claims)
		perms := user.Permissions.V
		if err != nil || !perms.Update || perms.AccessAllLibraries || len(perms.LibrariesAccessible) != 1 || !perms.SelectedTagsNotAccessible {
			t.Fatalf("permissions %+v err %v", perms, err)
		}
		claims["sub"], claims["perms"] = json.RawMessage(`"p-2"`), json.RawMessage(`{"canFly":true}`)
		before := count(t, d, `SELECT COUNT(*) FROM users`)
		if _, err := s.oidcUser(t.Context(), s.settings.Server(), claims); err == nil || count(t, d, `SELECT COUNT(*) FROM users`) != before {
			t.Fatalf("unknown permission accepted: %v", err)
		}
	})
}

func TestOIDCRedirectUsesSubfolderSettingVerbatim(t *testing.T) {
	for _, tc := range []struct{ name, subfolder, want string }{
		{"root", "", "http://abs.example/auth/openid/mobile-redirect"},
		{"subfolder", "/audiobookshelf", "http://abs.example/audiobookshelf/auth/openid/mobile-redirect"},
		{"trailing slash", "/a/", "http://abs.example/a/auth/openid/mobile-redirect"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, d, _, _, _ := openIDService(t, `,"authOpenIDMobileRedirectURIs":["audiobookshelf://oauth"]`)
			mustExec(t, d, `UPDATE settings SET value = json_set(value, '$.authOpenIDSubfolderForRedirectURLs', ?)`, tc.subfolder)
			if err := s.settings.Reload(t.Context()); err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", "/", nil)
			r.Host = "abs.example"
			if got := providerRedirect(r, s.settings.Server(), "/auth/openid/mobile-redirect"); got != tc.want {
				t.Fatalf("redirect = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOIDCLogoutURLAndDiscovery(t *testing.T) {
	s, _, p, mux, _ := openIDService(t, `,"authOpenIDLogoutURL":"https://idp.example/logout?existing=1"`)
	s.cfg.RouterBasePath = "/abs"
	w := send(mux, "POST", "/logout", "", nil, &http.Cookie{Name: "auth_method", Value: "openid"}, &http.Cookie{Name: "openid_id_token", Value: "signed.id.token"})
	var body struct {
		RedirectURL string `json:"redirect_url"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	target, err := url.Parse(body.RedirectURL)
	if err != nil || target.Query().Get("id_token_hint") != "signed.id.token" || target.Query().Get("existing") != "1" ||
		target.Query().Get("post_logout_redirect_uri") != "http://abs.example/abs/login" {
		t.Fatalf("logout answer %s", w.Body)
	}
	cleared := map[string]bool{}
	for _, c := range w.Result().Cookies() {
		cleared[c.Name] = c.MaxAge < 0
	}
	if !cleared["refresh_token"] || !cleared["auth_method"] || !cleared["openid_id_token"] {
		t.Fatalf("cookies %v", cleared)
	}
	if mobile := send(mux, "POST", "/logout", "", nil, &http.Cookie{Name: "auth_method", Value: "openid-mobile"}); strings.Contains(mobile.Body.String(), "post_logout_redirect_uri") {
		t.Fatalf("mobile logout must not set a return URL: %s", mobile.Body)
	}

	// Discovery: admins only; fetches the provider's document.
	access, _ := loginTokens(t, mux)
	if w := send(mux, "GET", "/auth/openid/config?issuer="+p.URL, "", map[string]string{"Authorization": "Bearer " + access}); w.Code != 403 {
		t.Fatalf("non-admin discovery = %d", w.Code)
	}
	if w := send(mux, "GET", "/auth/openid/config?issuer="+p.URL, "", nil); w.Code != 401 {
		t.Fatalf("anonymous discovery = %d", w.Code)
	}
	root, _ := s.userByUsername(t.Context(), "trial-root")
	token, _ := s.sessionToken(typeAccess, root.ID, "trial-root", time.Hour)
	if w := send(mux, "GET", "/auth/openid/config", "", map[string]string{"Authorization": "Bearer " + token}); w.Code != 400 {
		t.Fatalf("missing issuer = %d", w.Code)
	}
	if w := send(mux, "GET", "/auth/openid/config?issuer="+p.URL, "", map[string]string{"Authorization": "Bearer " + token}); w.Code != 400 {
		t.Fatalf("issuer without a discovery document = %d", w.Code) // the fake provider has none
	}
}

func TestValidWebCallback(t *testing.T) {
	s := &Service{cfg: config.Config{RouterBasePath: "/abs"}}
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "abs.example"
	for callback, want := range map[string]bool{
		"/abs/login":                    true,
		"http://abs.example/abs/login":  true,
		"https://abs.example/abs/login": false, // another scheme is another origin
		"http://evil.example/abs/login": false,
		"//evil.example/abs/login":      false,
		`/\evil.example`:                false,
		"/other/login":                  false,
		"/abs/%2F%2Fevil":               true,
		"/abs/../../etc":                false,
		"":                              false,
		"relative":                      false,
	} {
		if got := s.validWebCallback(callback, r); got != want {
			t.Errorf("validWebCallback(%q) = %v, want %v", callback, got, want)
		}
	}
}

func TestFlowsExpireAndAreTakenOnce(t *testing.T) {
	f := newFlows[string](time.Hour)
	f.put("a", "x")
	if v, ok := f.take("a"); !ok || v != "x" {
		t.Fatal("stored flow missing")
	}
	if _, ok := f.take("a"); ok {
		t.Fatal("a flow was taken twice")
	}
	f.put("old", "y")
	f.mu.Lock()
	f.items["old"] = flowEntry[string]{"y", time.Now().Add(-2 * time.Hour)}
	f.mu.Unlock()
	if _, ok := f.take("old"); ok {
		t.Fatal("expired flow returned")
	}
	f.put("b", "z")
	f.mu.Lock()
	f.items["stale"] = flowEntry[string]{"s", time.Now().Add(-2 * time.Hour)}
	f.mu.Unlock()
	f.put("c", "w")
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.items["stale"]; ok {
		t.Fatal("put kept an expired entry")
	}
}
