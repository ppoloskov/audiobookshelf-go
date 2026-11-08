package identity

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/jsonvalue"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/serversettings"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/users"
	"github.com/coreos/go-oidc/v3/oidc"
)

const (
	oidcSessionTTL        = 24 * time.Hour
	oidcMobileRedirectTTL = 10 * time.Minute
)

type oidcFlow struct {
	verifier      string
	nonce         string
	redirectURI   string
	sessionCookie string
	mobile        bool
	started       time.Time
}

type mobileFlow struct {
	redirectURI string
	started     time.Time
}

type oidcConfig struct {
	issuer             string
	authorizationURL   string
	tokenURL           string
	userinfoURL        string
	logoutURL          string
	jwksURL            string
	clientID           string
	clientSecret       string
	signingAlgorithm   string
	subfolder          string
	mobileRedirectURIs []string
	groupClaim         string
	advancedPermsClaim string
	matchExistingBy    string
	autoRegister       bool
}

func (h *Handler) oidcAuthorize(w http.ResponseWriter, r *http.Request) error {
	if !h.authMethodEnabled(r.Context(), "openid") {
		return writePlain(w, http.StatusUnauthorized, "Unauthorized")
	}
	if !h.allowAuthRequest(w, r) {
		return nil
	}
	cfg, err := h.readOIDCConfig(r.Context())
	if err != nil {
		return httputil.Internal(err.Error(), err)
	}
	query := r.URL.Query()
	responseType := query.Get("response_type")
	if responseType != "" && responseType != "code" {
		return writeExpressText(w, http.StatusBadRequest, "Invalid response_type, only code supported")
	}
	isMobile := responseType == "code" || query.Get("redirect_uri") != "" || query.Get("code_challenge") != ""
	if !isMobile && query.Has("state") {
		return writeExpressText(w, http.StatusBadRequest, "Invalid state, not allowed on web flow")
	}
	state := query.Get("state")
	if !isMobile || state == "" {
		state, err = randomURLToken(32)
		if err != nil {
			return &httputil.Error{Status: 500, Cause: err}
		}
	}
	redirectURI := ""
	if isMobile {
		redirectURI = query.Get("redirect_uri")
		if !validMobileRedirect(cfg.mobileRedirectURIs, redirectURI) {
			return writeExpressText(w, http.StatusBadRequest, "Invalid redirect_uri")
		}
		if query.Get("code_challenge") == "" {
			return writeExpressText(w, http.StatusBadRequest, "code_challenge required for mobile flow (PKCE)")
		}
		method := query.Get("code_challenge_method")
		if method != "" && method != "S256" {
			return writeExpressText(w, http.StatusBadRequest, "Only S256 code_challenge_method method supported")
		}
	} else {
		callback := query.Get("redirect_uri")
		if callback == "" {
			callback = query.Get("callback")
		}
		if callback == "" {
			return httputil.JSONStatus(w, r, http.StatusBadRequest, map[string]string{"message": "No callback parameter"})
		}
		if !h.validWebCallback(callback, r) {
			return httputil.JSONStatus(w, r, http.StatusBadRequest, map[string]string{"message": "Invalid callback URL - must be same-origin"})
		}
		redirectURI = callback
	}
	verifier := ""
	nonce, err := randomURLToken(32)
	if err != nil {
		return &httputil.Error{Status: 500, Cause: err}
	}
	challenge := query.Get("code_challenge")
	if isMobile {
		if method := query.Get("code_challenge_method"); method == "" {
			challenge = query.Get("code_challenge")
		}
	} else {
		verifier, err = randomURLToken(32)
		if err != nil {
			return &httputil.Error{Status: 500, Cause: err}
		}
		digest := sha256.Sum256([]byte(verifier))
		challenge = base64.RawURLEncoding.EncodeToString(digest[:])
	}
	callbackURL, err := callbackEndpoint(requestOrigin(r), cfg.subfolder, "/auth/openid/callback")
	if err != nil {
		return httputil.Status(400, "Invalid callback URL")
	}
	if isMobile {
		callbackURL, err = callbackEndpoint(requestOrigin(r), cfg.subfolder, "/auth/openid/mobile-redirect")
		if err != nil {
			return httputil.Status(400, "Invalid callback URL")
		}
	}
	if isMobile {
		if err := callbackURLIsSafeProviderReturn(cfg.mobileRedirectURIs, redirectURI); err != nil {
			return writeExpressText(w, http.StatusBadRequest, "Invalid redirect_uri")
		}
	}
	sessionCookie, err := h.sessionCookieValue(r)
	if err != nil {
		return &httputil.Error{Status: 500, Cause: err}
	}
	if !isMobile {
		setTemporaryCookie(w, "auth_cb", redirectURI, 600, false)
		if suppliedState := query.Get("state"); suppliedState != "" {
			setTemporaryCookie(w, "auth_state", suppliedState, 600, false)
		}
		setTemporaryCookie(w, "auth_method", "openid", 10*365*24*60*60, false)
	} else {
		setTemporaryCookie(w, "auth_method", "openid-mobile", 10*365*24*60*60, false)
		h.oidcMu.Lock()
		h.pruneOIDCState(time.Now())
		h.mobileFlows[state] = mobileFlow{redirectURI: redirectURI, started: time.Now()}
		h.oidcMu.Unlock()
	}
	h.oidcMu.Lock()
	h.pruneOIDCState(time.Now())
	h.oidcFlows[state] = oidcFlow{verifier: verifier, nonce: nonce, redirectURI: callbackURL, sessionCookie: sessionCookie, mobile: isMobile, started: time.Now()}
	h.oidcMu.Unlock()
	providerURL, err := authorizationURL(cfg, callbackURL, state, challenge, nonce)
	if err != nil {
		return httputil.Internal(err.Error(), err)
	}
	setExpressCookieValue(w, "connect.sid", sessionCookie, 0, true, false, "")
	return redirectExpress(w, http.StatusFound, providerURL)
}

func (h *Handler) oidcMobileRedirect(w http.ResponseWriter, r *http.Request) error {
	if !h.authMethodEnabled(r.Context(), "openid") {
		return writePlain(w, http.StatusUnauthorized, "Unauthorized")
	}
	if !h.allowAuthRequest(w, r) {
		return nil
	}
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	h.oidcMu.Lock()
	h.pruneOIDCState(time.Now())
	flow, ok := h.mobileFlows[state]
	if ok {
		delete(h.mobileFlows, state)
	}
	h.oidcMu.Unlock()
	if !ok || state == "" {
		return writeExpressText(w, http.StatusBadRequest, "State parameter mismatch")
	}
	if flow.redirectURI == "" {
		return writeExpressText(w, http.StatusBadRequest, "No redirect URI")
	}
	returnURL, err := url.Parse(flow.redirectURI)
	if err != nil {
		return writeExpressText(w, http.StatusBadRequest, "Invalid redirect URI")
	}
	returnURL.RawQuery = "code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state)
	return redirectExpress(w, http.StatusFound, returnURL.String())
}

func (h *Handler) oidcCallback(w http.ResponseWriter, r *http.Request) error {
	if !h.authMethodEnabled(r.Context(), "openid") {
		return writePlain(w, http.StatusUnauthorized, "Unauthorized")
	}
	if !h.allowAuthRequest(w, r) {
		return nil
	}
	state := r.URL.Query().Get("state")
	h.oidcMu.Lock()
	h.pruneOIDCState(time.Now())
	flow, ok := h.oidcFlows[state]
	if ok {
		delete(h.oidcFlows, state)
	}
	h.oidcMu.Unlock()
	if !ok || state == "" {
		verifiers := r.URL.Query()["code_verifier"]
		hasCodeVerifier := len(verifiers) > 1 || len(verifiers) == 1 && verifiers[0] != ""
		if cookieValue(r, "auth_method") == "openid-mobile" || hasCodeVerifier {
			return writeExpressText(w, http.StatusBadRequest, "No session")
		}
		return h.oidcFailure(w, http.StatusBadRequest, "No sign-in session")
	}
	if providerError := r.URL.Query().Get("error"); providerError != "" {
		logging.Errorf("[OidcAuth] openid callback error: provider returned %q (%s)", providerError, r.URL.Query().Get("error_description"))
		return h.oidcFailure(w, http.StatusUnauthorized, "Unauthorized")
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		return h.oidcFailure(w, http.StatusBadRequest, "Invalid authorization code")
	}
	verifier := flow.verifier
	if supplied := r.URL.Query().Get("code_verifier"); supplied != "" {
		verifier = supplied
	}
	cfg, err := h.readOIDCConfig(r.Context())
	if err != nil {
		logging.RequestError(r, "Error in callback", err)
		return h.oidcFailure(w, http.StatusInternalServerError, "Error in callback")
	}
	userinfo, idToken, err := exchangeAndLoadUserInfo(r.Context(), cfg, code, flow.redirectURI, verifier, flow.nonce)
	if err != nil {
		logging.Errorf("[OidcAuth] openid callback error: %v", err) // OidcAuthStrategy.js:160
		return h.oidcFailure(w, http.StatusUnauthorized, "Unauthorized")
	}
	user, err := h.userFromOIDCInfo(r.Context(), cfg, userinfo)
	if err != nil {
		logging.Errorf("[OidcAuth] openid callback: could not match or create a user: %v", err)
	} else if !user.IsActive {
		logging.Warn("[User] openid: User found but is not active") // User.js:292
	}
	if err != nil || !user.IsActive {
		return h.oidcFailure(w, http.StatusUnauthorized, "Unauthorized")
	}
	access, refresh, err := h.createSession(r.Context(), user, r)
	if err != nil {
		logging.RequestError(r, "Error during login", err)
		return h.oidcFailure(w, http.StatusInternalServerError, "Error during login")
	}
	logging.Infof("[OidcAuth] User %q logged in with OpenID from ip %s", user.Username, clientIP(r))
	setExpressCookie(w, "openid_id_token", idToken, 10*365*24*60*60, true, true, "Strict")
	if flow.mobile || cookieValue(r, "auth_method") == "openid-mobile" || cookieValue(r, "auth_method") == "api" {
		payload, err := h.loginPayload(r.Context(), user)
		if err != nil {
			logging.RequestError(r, "Error during login", err)
			return h.oidcFailure(w, http.StatusInternalServerError, "Error during login")
		}
		userDTO := payload["user"].(map[string]any)
		userDTO["accessToken"] = access
		userDTO["refreshToken"] = refresh
		setExpressCookieValue(w, "connect.sid", flow.sessionCookie, 0, true, false, "")
		httputil.WriteJSON(w, http.StatusOK, payload)
		return nil
	}
	setRefreshCookie(w, r, refresh, int(h.refreshTTL.Seconds()))
	setExpressCookieValue(w, "connect.sid", flow.sessionCookie, 0, true, false, "")
	callback := cookieValue(r, "auth_cb")
	if callback == "" {
		return redirectExpress(w, http.StatusFound, "/login?error=No%20callback%20or%20already%20expired&autoLaunch=0")
	}
	stateQuery := ""
	if userState := cookieValue(r, "auth_state"); userState != "" {
		stateQuery = "&state=" + url.QueryEscape(userState)
	}
	target := callback + "?setToken=" + url.QueryEscape(user.Token) + "&accessToken=" + url.QueryEscape(access) + stateQuery
	return redirectExpress(w, http.StatusFound, target)
}

func (h *Handler) oidcConfig(w http.ResponseWriter, r *http.Request) error {
	if !h.allowAuthRequest(w, r) {
		return nil
	}
	user, err := h.Authenticate(r)
	if err != nil {
		return writePlain(w, http.StatusUnauthorized, "Unauthorized")
	}
	if user.Type != "root" && user.Type != "admin" {
		return httputil.Status(403, "")
	}
	issuer := r.URL.Query().Get("issuer")
	if issuer == "" {
		return httputil.Status(400, "Invalid request. Query param 'issuer' is required")
	}
	issuer = strings.TrimSuffix(issuer, "/")
	u, err := url.Parse(issuer + "/.well-known/openid-configuration")
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || !strings.HasSuffix(u.Path, "/.well-known/openid-configuration") {
		return httputil.Status(400, "Invalid request. Query param 'issuer' is invalid")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return httputil.Status(400, "Failed to get openid configuration")
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		logging.Errorf("[OidcAuth] Failed to get openid configuration at %q: %v", u.String(), err) // OidcAuthStrategy.js:470
		return httputil.Status(400, "Failed to get openid configuration")
	}
	defer resp.Body.Close()
	var data map[string]any
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&data) != nil {
		return httputil.Status(400, "Failed to get openid configuration")
	}
	result := map[string]any{}
	for _, key := range []string{"issuer", "authorization_endpoint", "token_endpoint", "userinfo_endpoint", "end_session_endpoint", "jwks_uri", "id_token_signing_alg_values_supported"} {
		if value, ok := data[key]; ok {
			result[key] = value
		}
	}
	httputil.WriteJSON(w, http.StatusOK, result)
	return nil
}

func (h *Handler) openIDLogoutURL(r *http.Request, idToken, authMethod string) any {
	settings, err := serversettings.Get(r.Context(), h.db)
	if err != nil {
		return nil
	}
	endpoint := settings.String("authOpenIDLogoutURL")
	if endpoint == "" {
		return nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil
	}
	q := u.Query()
	if idToken != "" {
		q.Set("id_token_hint", idToken)
	} else {
		q.Del("id_token_hint")
	}
	if authMethod == "openid" {
		q.Set("post_logout_redirect_uri", requestOrigin(r)+h.cfg.RouterBasePath+"/login")
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func (h *Handler) oidcFailure(w http.ResponseWriter, status int, message string) error {
	// The pinned Passport callback routes failures based on
	// `req.session[sessionKey].mobile === true`, but the OIDC strategy stores
	// the mobile redirect URI string there. A valid mobile flow therefore uses
	// the web login redirect on callback failure, regardless of auth_method.
	if status == http.StatusInternalServerError {
		logging.Errorf("[Auth] OpenID callback failed with status %d: %s", status, message)
		message = "Error in callback"
	} else if status == http.StatusBadRequest && message == "No sign-in session" {
		message = "No sign-in session"
	} else {
		message = "Unauthorized"
	}
	redirectError := strings.ReplaceAll(url.QueryEscape(message), "+", "%20")
	return redirectExpress(w, http.StatusFound, "/login?error="+redirectError+"&autoLaunch=0")
}

func (h *Handler) readOIDCConfig(ctx context.Context) (oidcConfig, error) {
	settings, err := serversettings.Get(ctx, h.db)
	if err != nil {
		return oidcConfig{}, err
	}
	doc := settings.Doc()
	cfg := oidcConfig{
		issuer:             settings.String("authOpenIDIssuerURL"),
		authorizationURL:   settings.String("authOpenIDAuthorizationURL"),
		tokenURL:           settings.String("authOpenIDTokenURL"),
		userinfoURL:        settings.String("authOpenIDUserInfoURL"),
		logoutURL:          settings.String("authOpenIDLogoutURL"),
		jwksURL:            settings.String("authOpenIDJwksURL"),
		clientID:           settings.String("authOpenIDClientID"),
		clientSecret:       settings.String("authOpenIDClientSecret"),
		signingAlgorithm:   settings.String("authOpenIDTokenSigningAlgorithm"),
		subfolder:          settings.String("authOpenIDSubfolderForRedirectURLs"),
		groupClaim:         settings.String("authOpenIDGroupClaim"),
		advancedPermsClaim: settings.String("authOpenIDAdvancedPermsClaim"),
		matchExistingBy:    settings.String("authOpenIDMatchExistingBy"),
	}
	if cfg.signingAlgorithm == "" {
		cfg.signingAlgorithm = "RS256"
	}
	cfg.mobileRedirectURIs = stringSlice(doc["authOpenIDMobileRedirectURIs"])
	cfg.autoRegister = settings.Bool("authOpenIDAutoRegister")
	// The redirect URLs use authOpenIDSubfolderForRedirectURLs verbatim
	// (OidcAuthStrategy.js:330-332): "" means the site root, and an unset key
	// interpolates as "undefined" in the template literal. It is never the
	// router base path.
	switch v, present := doc["authOpenIDSubfolderForRedirectURLs"]; {
	case !present:
		cfg.subfolder = "undefined"
	case v == nil:
		cfg.subfolder = "null"
	}
	for name, value := range map[string]string{"issuer": cfg.issuer, "authorization URL": cfg.authorizationURL, "token URL": cfg.tokenURL, "userinfo URL": cfg.userinfoURL, "JWKS URL": cfg.jwksURL, "client ID": cfg.clientID, "client secret": cfg.clientSecret} {
		if value == "" {
			return oidcConfig{}, fmt.Errorf("OpenID Connect settings are not valid: missing %s", name)
		}
	}
	for _, value := range []string{cfg.authorizationURL, cfg.tokenURL, cfg.userinfoURL, cfg.jwksURL} {
		if !absoluteHTTPURL(value) {
			return oidcConfig{}, errors.New("OpenID Connect settings are not valid: endpoint URL is invalid")
		}
	}
	return cfg, nil
}

func authorizationURL(cfg oidcConfig, redirectURI, state, challenge, nonce string) (string, error) {
	u, err := url.Parse(cfg.authorizationURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("invalid OpenID authorization endpoint")
	}
	scope := "openid profile email"
	if cfg.groupClaim != "" {
		scope += " " + cfg.groupClaim
	}
	if cfg.advancedPermsClaim != "" {
		scope += " " + cfg.advancedPermsClaim
	}
	q := u.Query()
	q.Set("client_id", cfg.clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", scope)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("nonce", nonce)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func exchangeAndLoadUserInfo(ctx context.Context, cfg oidcConfig, code, redirectURI, verifier, nonce string) (map[string]any, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI}}
	if verifier != "" {
		form.Set("code_verifier", verifier)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(cfg.clientID, cfg.clientSecret)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	var tokens struct {
		AccessToken string `json:"access_token"`
		IDToken     string `json:"id_token"`
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&tokens) != nil || tokens.AccessToken == "" || tokens.IDToken == "" {
		return nil, "", errors.New("OIDC token exchange failed")
	}
	keySet := oidc.NewRemoteKeySet(ctx, cfg.jwksURL)
	verifierOIDC := oidc.NewVerifier(cfg.issuer, keySet, &oidc.Config{ClientID: cfg.clientID, SupportedSigningAlgs: []string{cfg.signingAlgorithm}})
	idToken, err := verifierOIDC.Verify(ctx, tokens.IDToken)
	if err != nil {
		return nil, "", fmt.Errorf("verify OIDC id token: %w", err)
	}
	var idClaims struct {
		Subject string `json:"sub"`
		Nonce   string `json:"nonce"`
	}
	if err := idToken.Claims(&idClaims); err != nil || idClaims.Subject == "" || idClaims.Nonce != nonce {
		return nil, "", errors.New("OIDC ID token has no subject")
	}
	userInfoRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.userinfoURL, nil)
	if err != nil {
		return nil, "", err
	}
	userInfoRequest.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	userInfoRequest.Header.Set("Accept", "application/json")
	userInfoResponse, err := (&http.Client{Timeout: 10 * time.Second}).Do(userInfoRequest)
	if err != nil {
		return nil, "", err
	}
	defer userInfoResponse.Body.Close()
	var userinfo map[string]any
	if userInfoResponse.StatusCode < 200 || userInfoResponse.StatusCode >= 300 || json.NewDecoder(io.LimitReader(userInfoResponse.Body, 2<<20)).Decode(&userinfo) != nil {
		return nil, "", errors.New("OIDC userinfo request failed")
	}
	if userinfo["sub"] != idClaims.Subject {
		return nil, "", errors.New("OIDC subject does not match userinfo")
	}
	return userinfo, tokens.IDToken, nil
}

func (h *Handler) userFromOIDCInfo(ctx context.Context, cfg oidcConfig, info map[string]any) (store.User, error) {
	sub, _ := info["sub"].(string)
	if sub == "" {
		return store.User{}, errors.New("invalid userinfo, no sub")
	}
	if cfg.groupClaim != "" && !jsonvalue.Truthy(info[cfg.groupClaim]) {
		return store.User{}, errors.New("configured group claim is missing")
	}
	user, err := h.userByOIDCSub(ctx, sub)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return store.User{}, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		user, err = h.matchOIDCUser(ctx, cfg, info)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return store.User{}, err
		}
		if errors.Is(err, sql.ErrNoRows) {
			user = store.User{}
			err = nil
		}
		if user.ID != "" {
			extra := store.DecodeJSONObject(user.ExtraData)
			if previous, _ := extra["authOpenIDSub"].(string); previous != "" {
				return store.User{}, errors.New("user already linked to a different OpenID subject")
			}
			if !user.IsActive {
				return user, nil
			}
			encoded, err := setJSONField(user.ExtraData, "authOpenIDSub", sub)
			if err != nil {
				return store.User{}, err
			}
			if err := h.queries().UpdateUserExtraData(ctx, sqlc.UpdateUserExtraDataParams{Extradata: store.Text(string(encoded)), Updatedat: dbtime.Format(time.Now()), ID: user.ID}); err != nil {
				return store.User{}, err
			}
			user.ExtraData = encoded
		}
	}
	created := false
	if user.ID == "" {
		if !cfg.autoRegister {
			return store.User{}, errors.New("user not found and auto-register is disabled")
		}
		user, err = h.createOIDCUser(ctx, info)
		if err != nil {
			return store.User{}, err
		}
		created = true
		if h.userAddedEmitter != nil {
			h.userAddedEmitter(users.UserDTO(user, nil))
		}
	}
	if !user.IsActive {
		return user, errors.New("user not active or not found")
	}
	if cfg.groupClaim != "" {
		if err := h.applyOIDCGroups(ctx, cfg, user, info); err != nil {
			if created {
				_ = h.queries().DeleteUser(ctx, user.ID)
			}
			return store.User{}, err
		}
		user, _ = h.userByID(ctx, user.ID)
	}
	if cfg.advancedPermsClaim != "" && user.Type != "admin" && user.Type != "root" {
		if err := h.applyOIDCPermissions(ctx, cfg, user, info); err != nil {
			if created {
				_ = h.queries().DeleteUser(ctx, user.ID)
			}
			return store.User{}, err
		}
		user, _ = h.userByID(ctx, user.ID)
	}
	return user, nil
}

// setJSONField sets one member of the JSON object in data (null or empty
// data is an empty object), leaving the other members' values untouched.
func setJSONField(data []byte, name string, value any) ([]byte, error) {
	fields := map[string]json.RawMessage{}
	if trimmed := bytes.TrimSpace(data); len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		if err := json.Unmarshal(trimmed, &fields); err != nil {
			return nil, errors.New("identity: OIDC extraData must be a JSON object")
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	fields[name] = encoded
	return json.Marshal(fields)
}

func (h *Handler) matchOIDCUser(ctx context.Context, cfg oidcConfig, info map[string]any) (store.User, error) {
	switch cfg.matchExistingBy {
	case "email":
		email, _ := info["email"].(string)
		if email == "" {
			return store.User{}, errors.New("No email in userinfo")
		}
		if verified, ok := info["email_verified"].(bool); ok && !verified {
			return store.User{}, errors.New("Email not verified")
		}
		return h.userByEmail(ctx, email)
	case "username":
		username, _ := info["preferred_username"].(string)
		if username == "" {
			username, _ = info["username"].(string)
		}
		if username == "" {
			return store.User{}, errors.New("No username in userinfo")
		}
		return h.userByUsername(ctx, strings.ToLower(username))
	default:
		return store.User{}, nil
	}
}

func (h *Handler) userByOIDCSub(ctx context.Context, sub string) (store.User, error) {
	return userResult(h.queries().GetUserByOIDCSub(ctx, sub))
}

func (h *Handler) userByEmail(ctx context.Context, email string) (store.User, error) {
	return userResult(h.queries().GetUserByEmail(ctx, store.Text(strings.ToLower(email))))
}

func (h *Handler) createOIDCUser(ctx context.Context, info map[string]any) (store.User, error) {
	sub, _ := info["sub"].(string)
	username, _ := info["preferred_username"].(string)
	if username == "" {
		username, _ = info["name"].(string)
	}
	if username == "" {
		username = sub
	}
	if username == "" {
		return store.User{}, errors.New("OIDC username is missing")
	}
	var email sql.NullString // NULL unless the email is verified, as before
	if verified, _ := info["email_verified"].(bool); verified {
		address, _ := info["email"].(string)
		email = store.TextOrNull(address)
	}
	id, err := ids.New()
	if err != nil {
		return store.User{}, err
	}
	token, err := h.IssueLegacyToken(id, username)
	if err != nil {
		return store.User{}, err
	}
	now := dbtime.Format(time.Now())
	err = h.queries().InsertUser(ctx, sqlc.InsertUserParams{
		ID: id, Username: store.Text(username), Email: email, Type: store.Text("user"), Token: store.Text(token), Isactive: sql.NullInt64{Int64: 1, Valid: true},
		Permissions: store.Text(encodeJSON(access.Defaults("user"))), Extradata: store.Text(users.NewUserExtraData(sub)), Createdat: now, Updatedat: now,
	})
	if err != nil {
		return store.User{}, err
	}
	return h.userByID(ctx, id)
}

func (h *Handler) applyOIDCGroups(ctx context.Context, cfg oidcConfig, user store.User, info map[string]any) error {
	groups, ok := info[cfg.groupClaim].([]any)
	if !ok {
		return errors.New("OpenID group claim is invalid")
	}
	roles := map[string]bool{}
	for _, group := range groups {
		name, ok := group.(string)
		if !ok {
			return errors.New("OpenID group claim contains a non-string value")
		}
		roles[strings.ToLower(name)] = true
	}
	role := ""
	for _, candidate := range []string{"admin", "user", "guest"} {
		if roles[candidate] {
			role = candidate
			break
		}
	}
	if role == "" {
		return errors.New("No valid group found in userinfo")
	}
	if user.Type == "root" {
		if role != "admin" {
			return errors.New("Root user cannot be downgraded")
		}
		return nil
	}
	if user.Type != role {
		return h.queries().UpdateUserType(ctx, sqlc.UpdateUserTypeParams{Type: store.Text(role), Updatedat: dbtime.Format(time.Now()), ID: user.ID})
	}
	return nil
}

func (h *Handler) applyOIDCPermissions(ctx context.Context, cfg oidcConfig, user store.User, info map[string]any) error {
	claim, ok := info[cfg.advancedPermsClaim].(map[string]any)
	if !ok {
		return errors.New("Advanced permissions claim is missing or invalid")
	}
	permissions := store.DecodeJSONObject(user.Permissions)
	mapping := map[string]string{"canDownload": "download", "canUpload": "upload", "canDelete": "delete", "canUpdate": "update", "canAccessExplicitContent": "accessExplicitContent", "canAccessAllLibraries": "accessAllLibraries", "canAccessAllTags": "accessAllTags", "canCreateEReader": "createEreader", "tagsAreDenylist": "selectedTagsNotAccessible", "allowedLibraries": "librariesAccessible", "allowedTags": "itemTagsSelected"}
	for key, value := range claim {
		mapped, exists := mapping[key]
		if !exists {
			return fmt.Errorf("Unexpected permission property: %s", key)
		}
		if mapped == "librariesAccessible" || mapped == "itemTagsSelected" {
			continue
		}
		permissions[mapped] = jsonvalue.Truthy(value)
	}
	if permissions["accessAllLibraries"] == true {
		permissions["librariesAccessible"] = []string{}
	} else if values, ok := claim["allowedLibraries"].([]any); ok && len(values) > 0 {
		ids, err := checkedStringArray(values)
		if err != nil {
			return errors.New(`Invalid permission property "allowedLibraries", expecting array of strings`)
		}
		permissions["librariesAccessible"] = ids
	}
	if permissions["accessAllTags"] == true {
		permissions["itemTagsSelected"] = []string{}
	} else if values, ok := claim["allowedTags"].([]any); ok && len(values) > 0 {
		tags, err := checkedStringArray(values)
		if err != nil {
			return errors.New(`Invalid permission property "allowedTags", expecting array of strings`)
		}
		permissions["itemTagsSelected"] = tags
	}
	encoded, err := json.Marshal(permissions)
	if err != nil {
		return err
	}
	return h.queries().UpdateUserPermissions(ctx, sqlc.UpdateUserPermissionsParams{Permissions: store.Text(string(encoded)), Updatedat: dbtime.Format(time.Now()), ID: user.ID})
}

func (h *Handler) pruneOIDCState(now time.Time) {
	for state, flow := range h.oidcFlows {
		if now.Sub(flow.started) > oidcSessionTTL {
			delete(h.oidcFlows, state)
		}
	}
	for state, flow := range h.mobileFlows {
		if now.Sub(flow.started) > oidcMobileRedirectTTL {
			delete(h.mobileFlows, state)
		}
	}
}

func (h *Handler) validWebCallback(callback string, r *http.Request) bool {
	if callback == "" || strings.HasPrefix(callback, "//") || strings.HasPrefix(callback, "/\\") {
		return false
	}
	origin := requestOrigin(r)
	server, err := url.Parse(origin)
	if err != nil {
		return false
	}
	var target *url.URL
	if strings.HasPrefix(callback, "/") {
		target, err = server.Parse(callback)
	} else {
		target, err = url.Parse(callback)
	}
	if err != nil || target.Host != server.Host || target.Scheme != server.Scheme {
		return false
	}
	decodedPath, err := url.PathUnescape(target.EscapedPath())
	if err != nil || strings.HasPrefix(decodedPath, "//") || strings.HasPrefix(decodedPath, "/\\") {
		return false
	}
	base := strings.TrimSuffix(h.cfg.RouterBasePath, "/")
	return strings.HasPrefix(target.Path, base+"/")
}

func callbackEndpoint(origin, subfolder, endpoint string) (string, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return "", errors.New("invalid request origin")
	}
	base := strings.TrimSuffix(subfolder, "/")
	if !strings.HasPrefix(base, "/") {
		base = "/" + base
	}
	u.Path = path.Clean(base + endpoint)
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func requestOrigin(r *http.Request) string {
	protocol := "http"
	if requestSecure(r) {
		protocol = "https"
	}
	return protocol + "://" + r.Host
}

func validMobileRedirect(allowed []string, redirect string) bool {
	if redirect == "" {
		return false
	}
	if len(allowed) == 1 && allowed[0] == "*" {
		return true
	}
	for _, value := range allowed {
		if value == redirect {
			return true
		}
	}
	return false
}

func callbackURLIsSafeProviderReturn(allowed []string, redirect string) error {
	if !validMobileRedirect(allowed, redirect) {
		return errors.New("invalid redirect URI")
	}
	return nil
}

func randomURLToken(byteLength int) (string, error) {
	b := make([]byte, byteLength)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func absoluteHTTPURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https")
}

func checkedStringArray(values []any) ([]string, error) {
	result := make([]string, len(values))
	for i, value := range values {
		s, ok := value.(string)
		if !ok {
			return nil, errors.New("expected string array")
		}
		result[i] = s
	}
	return result, nil
}

func setTemporaryCookie(w http.ResponseWriter, name, value string, maxAge int, secure bool) {
	setExpressCookie(w, name, value, maxAge, true, secure, "")
}

func cookieValue(r *http.Request, name string) string {
	cookie, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	value, err := url.QueryUnescape(cookie.Value)
	if err != nil {
		return cookie.Value
	}
	return value
}

func (h *Handler) authMethodEnabled(ctx context.Context, method string) bool {
	settings, err := serversettings.Get(ctx, h.db)
	if err != nil {
		return false
	}
	methods, _ := settings.Doc()["authActiveAuthMethods"].([]any)
	for _, value := range methods {
		if value == method {
			return true
		}
	}
	return false
}
