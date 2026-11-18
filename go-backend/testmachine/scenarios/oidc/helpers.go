package oidc

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	mobileRedirectURI = "audiobookshelf://oauth"
	webCallbackPath   = "/auth/openid/callback"
)

// oidcEnv is the environment the packet's servers need: the auth rate limiter
// would otherwise reject the many authentication requests of the flow
// scenarios from the single test address.
func oidcEnv() map[string]string {
	return map[string]string{"RATE_LIMIT_AUTH_MAX": "0"}
}

// defaultUserinfo is a provider answer for an auto-registered user.
func defaultUserinfo(sub, username, email string) map[string]any {
	return map[string]any{
		"sub":                sub,
		"preferred_username": username,
		"name":               username,
		"email":              email,
		"email_verified":     true,
	}
}

// configureOIDC points the server's OpenID settings at the stub provider and
// enables the openid auth method. overrides replace single settings (a nil
// value clears the setting, as the settings endpoint accepts null).
func configureOIDC(t *testing.T, e *harness.Env, p *mockProvider, overrides map[string]any) {
	t.Helper()
	settings := map[string]any{
		"authActiveAuthMethods":              []string{"local", "openid"},
		"authOpenIDIssuerURL":                p.issuer(),
		"authOpenIDAuthorizationURL":         p.endpoint("authorize"),
		"authOpenIDTokenURL":                 p.endpoint("token"),
		"authOpenIDUserInfoURL":              p.endpoint("userinfo"),
		"authOpenIDJwksURL":                  p.endpoint("jwks"),
		"authOpenIDLogoutURL":                p.endpoint("logout"),
		"authOpenIDClientID":                 p.clientID,
		"authOpenIDClientSecret":             p.clientSecret,
		"authOpenIDTokenSigningAlgorithm":    "RS256",
		"authOpenIDButtonText":               "Login with OpenId",
		"authOpenIDAutoLaunch":               false,
		"authOpenIDAutoRegister":             true,
		"authOpenIDMatchExistingBy":          nil,
		"authOpenIDMobileRedirectURIs":       []string{mobileRedirectURI},
		"authOpenIDGroupClaim":               nil,
		"authOpenIDAdvancedPermsClaim":       nil,
		"authOpenIDSubfolderForRedirectURLs": "",
	}
	for key, value := range overrides {
		settings[key] = value
	}
	resp := e.Patch(t, e.Token(t, "root"), "/api/auth-settings", settings)
	requireStatus(t, "configure openid settings", resp, http.StatusOK)
}

// browser holds the cookies one client keeps between the start request and the
// callback: the Express session and the auth_mode/callback cookies upstream
// sets for the flow.
type browser struct {
	Session      string
	AuthMethod   string
	AuthCallback string
	AuthState    string
}

// browserFrom collects the cookies of a start answer.
func browserFrom(resp harness.Response) browser {
	var b browser
	for _, cookie := range resp.Cookies {
		switch cookie.Name {
		case "connect.sid":
			b.Session = cookie.Value
		case "auth_method":
			b.AuthMethod = cookie.Value
		case "auth_cb":
			b.AuthCallback = cookie.Value
		case "auth_state":
			b.AuthState = cookie.Value
		}
	}
	return b
}

// header renders the cookies as a Cookie request header.
func (b browser) header() string {
	var parts []string
	for _, pair := range [][2]string{
		{"connect.sid", b.Session},
		{"auth_method", b.AuthMethod},
		{"auth_cb", b.AuthCallback},
		{"auth_state", b.AuthState},
	} {
		if pair[1] != "" {
			parts = append(parts, pair[0]+"="+pair[1])
		}
	}
	return strings.Join(parts, "; ")
}

// beginWeb performs the web start and returns the provider URL and the client
// cookies. It requires the upstream 302 to the configured provider.
func beginWeb(t *testing.T, e *harness.Env, callback string) (*url.URL, browser) {
	t.Helper()
	resp := e.Do(t, harness.Request{Path: "/auth/openid", Query: url.Values{"callback": {callback}}})
	requireStatus(t, "web start", resp, http.StatusFound)
	authorize := location(t, resp)
	b := browserFrom(resp)
	if b.Session == "" {
		t.Fatalf("web start must set a session cookie: %v", resp.Header.Values("Set-Cookie"))
	}
	return authorize, b
}

// beginMobile performs a native start for the given verifier and returns the
// provider URL, the client cookies, the state and the S256 challenge.
func beginMobile(t *testing.T, e *harness.Env, verifier string) (*url.URL, browser, string, string) {
	t.Helper()
	challenge := s256(verifier)
	resp := e.Do(t, harness.Request{Path: "/auth/openid", Query: url.Values{
		"response_type":         {"code"},
		"redirect_uri":          {mobileRedirectURI},
		"client_id":             {"Audiobookshelf-App"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}})
	requireStatus(t, "mobile start", resp, http.StatusFound)
	authorize := location(t, resp)
	b := browserFrom(resp)
	if b.Session == "" {
		t.Fatalf("mobile start must set a session cookie: %v", resp.Header.Values("Set-Cookie"))
	}
	return authorize, b, authorize.Query().Get("state"), challenge
}

// providerRedirect emulates the provider sending the browser back to a route of
// the server: the request goes to the origin, exactly as the provider's
// redirect_uri does, and not through the router base path prefix.
func providerRedirect(t *testing.T, e *harness.Env, params url.Values, cookie string) harness.Response {
	t.Helper()
	req := harness.Request{Raw: true, Path: webCallbackPath}
	if len(params) > 0 {
		req.Path += "?" + params.Encode()
	}
	if cookie != "" {
		req.Header = map[string]string{"Cookie": cookie}
	}
	return e.Do(t, req)
}

// mobileRedirect performs the provider's redirect to the route the server saved
// as its own redirect_uri for the native flow.
func mobileRedirect(t *testing.T, e *harness.Env, state, code string) harness.Response {
	t.Helper()
	return e.Do(t, harness.Request{Path: "/auth/openid/mobile-redirect", Query: url.Values{"state": {state}, "code": {code}}})
}

// webLogin is one complete web authorization: start, provider code, callback.
type webLogin struct {
	Authorize *url.URL
	Start     browser
	State     string
	Code      string
	Response  harness.Response
	Location  *url.URL
}

// loginWeb runs the web flow and requires the successful redirect.
func loginWeb(t *testing.T, e *harness.Env, p *mockProvider, callback string, userinfo map[string]any) webLogin {
	t.Helper()
	authorize, b := beginWeb(t, e, callback)
	state := authorize.Query().Get("state")
	code := p.issue(t, "", userinfo)
	resp := providerRedirect(t, e, url.Values{"state": {state}, "code": {code}}, b.header())
	requireStatus(t, "web callback", resp, http.StatusFound)
	return webLogin{Authorize: authorize, Start: b, State: state, Code: code, Response: resp, Location: location(t, resp)}
}

// mobileLogin is one complete native authorization: start, mobile redirect,
// callback.
type mobileLogin struct {
	Authorize *url.URL
	Start     browser
	Verifier  string
	Challenge string
	State     string
	Code      string
	Redirect  harness.Response
	AppLink   *url.URL
	Callback  harness.Response
}

// loginMobile runs the native flow with the caller's verifier and requires the
// app link redirect and the JSON login answer.
func loginMobile(t *testing.T, e *harness.Env, p *mockProvider, verifier string, userinfo map[string]any) mobileLogin {
	t.Helper()
	authorize, b, state, challenge := beginMobile(t, e, verifier)
	code := p.issue(t, challenge, userinfo)
	redirect := mobileRedirect(t, e, state, code)
	requireStatus(t, "mobile redirect", redirect, http.StatusFound)
	appLink := location(t, redirect)
	callback := providerRedirect(t, e, url.Values{
		"state":         {state},
		"code":          {code},
		"code_verifier": {verifier},
	}, b.header())
	requireStatus(t, "mobile callback", callback, http.StatusOK)
	return mobileLogin{
		Authorize: authorize, Start: b, Verifier: verifier, Challenge: challenge,
		State: state, Code: code, Redirect: redirect, AppLink: appLink, Callback: callback,
	}
}

// location parses the Location header of a redirect answer.
func location(t *testing.T, resp harness.Response) *url.URL {
	t.Helper()
	raw := resp.Header.Get("Location")
	if raw == "" {
		t.Fatalf("answer %d has no Location header: %s", resp.Status, resp.Body)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("Location %q: %v", raw, err)
	}
	return parsed
}

// requireStatus fails unless resp has the wanted status.
func requireStatus(t *testing.T, what string, resp harness.Response, want int) {
	t.Helper()
	if resp.Status != want {
		t.Fatalf("%s: status %d, want %d: %s", what, resp.Status, want, resp.Body)
	}
}

// requireNoLocation fails when the answer redirects.
func requireNoLocation(t *testing.T, what string, resp harness.Response) {
	t.Helper()
	if raw := resp.Header.Get("Location"); raw != "" {
		t.Fatalf("%s: unexpected Location %q", what, raw)
	}
}

// requireNoTokenMaterial fails when a rejected answer carries a JWT or token
// fields; non-admins must not learn credentials from an error.
func requireNoTokenMaterial(t *testing.T, what string, resp harness.Response) {
	t.Helper()
	text := string(resp.Body)
	for _, marker := range []string{"eyJ", "accessToken", "refreshToken", "id_token"} {
		if strings.Contains(text, marker) {
			t.Fatalf("%s: answer carries token material %q: %s", what, marker, text)
		}
	}
}

// cookieValue returns the value of the named response cookie, "" when absent.
func cookieValue(resp harness.Response, name string) string {
	for _, cookie := range resp.Cookies {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

// cookie returns the named response cookie and whether it exists.
func cookie(resp harness.Response, name string) (*http.Cookie, bool) {
	for _, item := range resp.Cookies {
		if item.Name == name {
			return item, true
		}
	}
	return nil, false
}

// decodeJWT decodes the claims of a compact JWT without verifying it.
func decodeJWT(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token is not a compact JWT: %q", token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode JWT payload: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("decode JWT claims: %v", err)
	}
	return claims
}

// loginEnvelope is the part of a login answer the clients read.
type loginEnvelope struct {
	User struct {
		ID            string         `json:"id"`
		Username      string         `json:"username"`
		Email         *string        `json:"email"`
		Type          string         `json:"type"`
		Token         string         `json:"token"`
		AccessToken   string         `json:"accessToken"`
		RefreshToken  *string        `json:"refreshToken"`
		IsActive      bool           `json:"isActive"`
		HasOpenIDLink bool           `json:"hasOpenIDLink"`
		Permissions   map[string]any `json:"permissions"`
	} `json:"user"`
	UserDefaultLibraryID *string `json:"userDefaultLibraryId"`
	ServerSettings       struct {
		Version  string `json:"version"`
		Language string `json:"language"`
	} `json:"serverSettings"`
	EreaderDevices []any  `json:"ereaderDevices"`
	Source         string `json:"Source"`
}

// requireWebLoginAnswer checks the redirect target and the two cookies a
// successful web callback returns.
func requireWebLoginAnswer(t *testing.T, e *harness.Env, login webLogin, callback string) (string, string) {
	t.Helper()
	if got := login.Location.Scheme + "://" + login.Location.Host + login.Location.Path; got != callback {
		t.Fatalf("web callback redirect = %q, want %q", got, callback)
	}
	accessToken := login.Location.Query().Get("accessToken")
	legacyToken := login.Location.Query().Get("setToken")
	if accessToken == "" || legacyToken == "" {
		t.Fatalf("web callback redirect lacks setToken/accessToken: %s", login.Location)
	}
	refresh, ok := cookie(login.Response, "refresh_token")
	if !ok || refresh.Value == "" || !refresh.HttpOnly || refresh.Path != "/" {
		t.Fatalf("web callback must set an HttpOnly refresh_token cookie: %v", login.Response.Header.Values("Set-Cookie"))
	}
	idTokenCookie, ok := cookie(login.Response, "openid_id_token")
	if !ok || idTokenCookie.Value == "" || !idTokenCookie.Secure || idTokenCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("web callback must set a Secure SameSite=Strict openid_id_token cookie: %v", login.Response.Header.Values("Set-Cookie"))
	}
	return accessToken, refresh.Value
}

// userRowBySub returns the users row linked to the provider subject, or nil.
func userRowBySub(t *testing.T, e *harness.Env, sub string) map[string]any {
	t.Helper()
	rows := e.Rows(t, "SELECT id, username, email, type, isActive, token, extraData, permissions FROM users WHERE instr(extraData, ?) > 0", `"authOpenIDSub":"`+sub+`"`)
	if len(rows) == 0 {
		return nil
	}
	if len(rows) > 1 {
		t.Fatalf("subject %q is linked to %d users", sub, len(rows))
	}
	return rows[0]
}

// userRow returns one users row by id, or nil.
func userRow(t *testing.T, e *harness.Env, id string) map[string]any {
	t.Helper()
	rows := e.Rows(t, "SELECT id, username, email, type, isActive, token, extraData, permissions FROM users WHERE id = ?", id)
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}

// userCount returns how many rows the users table holds.
func userCount(t *testing.T, e *harness.Env) int {
	t.Helper()
	return e.Count(t, "SELECT COUNT(*) FROM users")
}

// sessionsOf returns the sessions rows of a user.
func sessionsOf(t *testing.T, e *harness.Env, userID string) []map[string]any {
	t.Helper()
	return e.Rows(t, "SELECT id, refreshToken, expiresAt FROM sessions WHERE userId = ?", userID)
}

// columnJSON decodes a JSON text column, failing the test when it is not JSON.
func columnJSON(t *testing.T, row map[string]any, name string) map[string]any {
	t.Helper()
	text, ok := row[name].(string)
	if !ok {
		t.Fatalf("column %s is %T, want JSON text", name, row[name])
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		t.Fatalf("column %s is not JSON: %v: %s", name, err, text)
	}
	return decoded
}

// snapshot captures the rows a rejected OIDC request must not change.
type snapshot struct {
	Users      int
	Sessions   int
	MaxUpdated string
}

// snapshotOf reads the user and session counts and the newest user timestamp.
func snapshotOf(t *testing.T, e *harness.Env) snapshot {
	t.Helper()
	var s snapshot
	s.Users = userCount(t, e)
	s.Sessions = e.Count(t, "SELECT COUNT(*) FROM sessions")
	rows := e.Rows(t, "SELECT COALESCE(MAX(updatedAt), '') AS newest FROM users")
	if len(rows) == 1 {
		s.MaxUpdated, _ = rows[0]["newest"].(string)
	}
	return s
}

// requireUnchanged fails when the snapshot changed.
func requireUnchanged(t *testing.T, e *harness.Env, before snapshot, what string) {
	t.Helper()
	after := snapshotOf(t, e)
	if after != before {
		t.Fatalf("%s changed state: before %+v, after %+v", what, before, after)
	}
}

// createUser creates an active user through the admin API and returns its id.
func createUser(t *testing.T, e *harness.Env, body map[string]any) string {
	t.Helper()
	resp := e.Post(t, e.Token(t, "root"), "/api/users", body)
	requireStatus(t, "create user", resp, http.StatusOK)
	var answer struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	resp.JSON(t, &answer)
	if answer.User.ID == "" {
		t.Fatalf("create user: no id: %s", resp.Body)
	}
	return answer.User.ID
}
