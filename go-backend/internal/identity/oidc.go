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

	"audiobookshelf-compat/internal/store"
	"github.com/coreos/go-oidc/v3/oidc"
)

const oidcFlowTTL = 10 * time.Minute

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

func (h *Handler) oidcAuthorize(w http.ResponseWriter, r *http.Request) {
	if !h.authMethodEnabled(r.Context(), "openid") {
		writePlain(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if !h.allowAuthRequest(w, r) {
		return
	}
	cfg, err := h.readOIDCConfig(r.Context())
	if err != nil {
		writePlain(w, http.StatusInternalServerError, err.Error())
		return
	}
	query := r.URL.Query()
	responseType := query.Get("response_type")
	if responseType != "" && responseType != "code" {
		writeExpressText(w, http.StatusBadRequest, "Invalid response_type, only code supported")
		return
	}
	isMobile := responseType == "code" || query.Get("redirect_uri") != "" || query.Get("code_challenge") != ""
	if !isMobile && query.Has("state") {
		writeExpressText(w, http.StatusBadRequest, "Invalid state, not allowed on web flow")
		return
	}
	state := query.Get("state")
	if !isMobile || state == "" {
		state, err = randomURLToken(32)
		if err != nil {
			writePlain(w, http.StatusInternalServerError, "Internal Server Error")
			return
		}
	}
	redirectURI := ""
	if isMobile {
		redirectURI = query.Get("redirect_uri")
		if !validMobileRedirect(cfg.mobileRedirectURIs, redirectURI) {
			writeExpressText(w, http.StatusBadRequest, "Invalid redirect_uri")
			return
		}
		if query.Get("code_challenge") == "" {
			writeExpressText(w, http.StatusBadRequest, "code_challenge required for mobile flow (PKCE)")
			return
		}
		method := query.Get("code_challenge_method")
		if method != "" && method != "S256" {
			writeExpressText(w, http.StatusBadRequest, "Only S256 code_challenge_method method supported")
			return
		}
	} else {
		callback := query.Get("redirect_uri")
		if callback == "" {
			callback = query.Get("callback")
		}
		if callback == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "No callback parameter"})
			return
		}
		if !h.validWebCallback(callback, r) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Invalid callback URL - must be same-origin"})
			return
		}
		redirectURI = callback
	}
	verifier := ""
	nonce, err := randomURLToken(32)
	if err != nil {
		writePlain(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	challenge := query.Get("code_challenge")
	if isMobile {
		if method := query.Get("code_challenge_method"); method == "" {
			challenge = query.Get("code_challenge")
		}
	} else {
		verifier, err = randomURLToken(32)
		if err != nil {
			writePlain(w, http.StatusInternalServerError, "Internal Server Error")
			return
		}
		digest := sha256.Sum256([]byte(verifier))
		challenge = base64.RawURLEncoding.EncodeToString(digest[:])
	}
	callbackURL, err := callbackEndpoint(requestOrigin(r), cfg.subfolder, "/auth/openid/callback")
	if err != nil {
		writePlain(w, http.StatusBadRequest, "Invalid callback URL")
		return
	}
	if isMobile {
		callbackURL, err = callbackEndpoint(requestOrigin(r), cfg.subfolder, "/auth/openid/mobile-redirect")
		if err != nil {
			writePlain(w, http.StatusBadRequest, "Invalid callback URL")
			return
		}
	}
	if isMobile {
		if err := callbackURLIsSafeProviderReturn(cfg.mobileRedirectURIs, redirectURI); err != nil {
			writeExpressText(w, http.StatusBadRequest, "Invalid redirect_uri")
			return
		}
	}
	sessionCookie, err := h.sessionCookieValue(r)
	if err != nil {
		writePlain(w, http.StatusInternalServerError, "Internal Server Error")
		return
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
		writePlain(w, http.StatusInternalServerError, err.Error())
		return
	}
	setExpressCookieValue(w, "connect.sid", sessionCookie, 0, true, false, "")
	redirectExpress(w, http.StatusFound, providerURL)
}

func (h *Handler) oidcMobileRedirect(w http.ResponseWriter, r *http.Request) {
	if !h.authMethodEnabled(r.Context(), "openid") {
		writePlain(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if !h.allowAuthRequest(w, r) {
		return
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
		writeExpressText(w, http.StatusBadRequest, "State parameter mismatch")
		return
	}
	if flow.redirectURI == "" {
		writeExpressText(w, http.StatusBadRequest, "No redirect URI")
		return
	}
	returnURL, err := url.Parse(flow.redirectURI)
	if err != nil {
		writeExpressText(w, http.StatusBadRequest, "Invalid redirect URI")
		return
	}
	returnURL.RawQuery = "code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state)
	redirectExpress(w, http.StatusFound, returnURL.String())
}

func (h *Handler) oidcCallback(w http.ResponseWriter, r *http.Request) {
	if !h.authMethodEnabled(r.Context(), "openid") {
		writePlain(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if !h.allowAuthRequest(w, r) {
		return
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
			writeExpressText(w, http.StatusBadRequest, "No session")
			return
		}
		h.oidcFailure(w, http.StatusBadRequest, "No sign-in session")
		return
	}
	if providerError := r.URL.Query().Get("error"); providerError != "" {
		h.oidcFailure(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		h.oidcFailure(w, http.StatusBadRequest, "Invalid authorization code")
		return
	}
	verifier := flow.verifier
	if supplied := r.URL.Query().Get("code_verifier"); supplied != "" {
		verifier = supplied
	}
	cfg, err := h.readOIDCConfig(r.Context())
	if err != nil {
		h.oidcFailure(w, http.StatusInternalServerError, "Error in callback")
		return
	}
	userinfo, idToken, err := exchangeAndLoadUserInfo(r.Context(), cfg, code, flow.redirectURI, verifier, flow.nonce)
	if err != nil {
		h.oidcFailure(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	user, err := h.userFromOIDCInfo(r.Context(), cfg, userinfo)
	if err != nil || !user.IsActive {
		h.oidcFailure(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	access, refresh, err := h.createSession(r.Context(), user, r)
	if err != nil {
		h.oidcFailure(w, http.StatusInternalServerError, "Error during login")
		return
	}
	setExpressCookie(w, "openid_id_token", idToken, 10*365*24*60*60, true, true, "Strict")
	if flow.mobile || cookieValue(r, "auth_method") == "openid-mobile" || cookieValue(r, "auth_method") == "api" {
		payload, err := h.loginPayload(r.Context(), user)
		if err != nil {
			h.oidcFailure(w, http.StatusInternalServerError, "Error during login")
			return
		}
		userDTO := payload["user"].(map[string]any)
		userDTO["accessToken"] = access
		userDTO["refreshToken"] = refresh
		setExpressCookieValue(w, "connect.sid", flow.sessionCookie, 0, true, false, "")
		writeJSON(w, http.StatusOK, payload)
		return
	}
	setRefreshCookie(w, r, refresh, int(h.refreshTTL.Seconds()))
	setExpressCookieValue(w, "connect.sid", flow.sessionCookie, 0, true, false, "")
	callback := cookieValue(r, "auth_cb")
	if callback == "" {
		redirectExpress(w, http.StatusFound, "/login?error=No%20callback%20or%20already%20expired&autoLaunch=0")
		return
	}
	stateQuery := ""
	if userState := cookieValue(r, "auth_state"); userState != "" {
		stateQuery = "&state=" + url.QueryEscape(userState)
	}
	target := callback + "?setToken=" + url.QueryEscape(user.Token) + "&accessToken=" + url.QueryEscape(access) + stateQuery
	redirectExpress(w, http.StatusFound, target)
}

func (h *Handler) oidcConfig(w http.ResponseWriter, r *http.Request) {
	if !h.allowAuthRequest(w, r) {
		return
	}
	user, err := h.Authenticate(r)
	if err != nil {
		writePlain(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if user.Type != "root" && user.Type != "admin" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	issuer := r.URL.Query().Get("issuer")
	if issuer == "" {
		writePlain(w, http.StatusBadRequest, "Invalid request. Query param 'issuer' is required")
		return
	}
	issuer = strings.TrimSuffix(issuer, "/")
	u, err := url.Parse(issuer + "/.well-known/openid-configuration")
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || !strings.HasSuffix(u.Path, "/.well-known/openid-configuration") {
		writePlain(w, http.StatusBadRequest, "Invalid request. Query param 'issuer' is invalid")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		writePlain(w, http.StatusBadRequest, "Failed to get openid configuration")
		return
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		writePlain(w, http.StatusBadRequest, "Failed to get openid configuration")
		return
	}
	defer resp.Body.Close()
	var data map[string]any
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&data) != nil {
		writePlain(w, http.StatusBadRequest, "Failed to get openid configuration")
		return
	}
	result := map[string]any{}
	for _, key := range []string{"issuer", "authorization_endpoint", "token_endpoint", "userinfo_endpoint", "end_session_endpoint", "jwks_uri", "id_token_signing_alg_values_supported"} {
		if value, ok := data[key]; ok {
			result[key] = value
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) openIDLogoutURL(r *http.Request, idToken, authMethod string) any {
	settings, err := h.loadSettings(r.Context(), "server-settings")
	if err != nil {
		return nil
	}
	endpoint := getString(settings, "authOpenIDLogoutURL")
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

func (h *Handler) oidcFailure(w http.ResponseWriter, status int, message string) {
	// The pinned Passport callback routes failures based on
	// `req.session[sessionKey].mobile === true`, but the OIDC strategy stores
	// the mobile redirect URI string there. A valid mobile flow therefore uses
	// the web login redirect on callback failure, regardless of auth_method.
	if status == http.StatusInternalServerError {
		message = "Error in callback"
	} else if status == http.StatusBadRequest && message == "No sign-in session" {
		message = "No sign-in session"
	} else {
		message = "Unauthorized"
	}
	redirectError := strings.ReplaceAll(url.QueryEscape(message), "+", "%20")
	redirectExpress(w, http.StatusFound, "/login?error="+redirectError+"&autoLaunch=0")
}

func (h *Handler) readOIDCConfig(ctx context.Context) (oidcConfig, error) {
	settings, err := h.loadSettings(ctx, "server-settings")
	if err != nil {
		return oidcConfig{}, err
	}
	cfg := oidcConfig{
		issuer:             getString(settings, "authOpenIDIssuerURL"),
		authorizationURL:   getString(settings, "authOpenIDAuthorizationURL"),
		tokenURL:           getString(settings, "authOpenIDTokenURL"),
		userinfoURL:        getString(settings, "authOpenIDUserInfoURL"),
		logoutURL:          getString(settings, "authOpenIDLogoutURL"),
		jwksURL:            getString(settings, "authOpenIDJwksURL"),
		clientID:           getString(settings, "authOpenIDClientID"),
		clientSecret:       getString(settings, "authOpenIDClientSecret"),
		signingAlgorithm:   getString(settings, "authOpenIDTokenSigningAlgorithm"),
		subfolder:          getString(settings, "authOpenIDSubfolderForRedirectURLs"),
		groupClaim:         getString(settings, "authOpenIDGroupClaim"),
		advancedPermsClaim: getString(settings, "authOpenIDAdvancedPermsClaim"),
		matchExistingBy:    getString(settings, "authOpenIDMatchExistingBy"),
	}
	if cfg.signingAlgorithm == "" {
		cfg.signingAlgorithm = "RS256"
	}
	cfg.mobileRedirectURIs = stringSlice(settings["authOpenIDMobileRedirectURIs"])
	cfg.autoRegister, _ = settings["authOpenIDAutoRegister"].(bool)
	if cfg.subfolder == "" {
		cfg.subfolder = h.cfg.RouterBasePath
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
	if cfg.groupClaim != "" && !truthy(info[cfg.groupClaim]) {
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
			extra := decodeMap(user.ExtraData)
			if previous, _ := extra["authOpenIDSub"].(string); previous != "" {
				return store.User{}, errors.New("user already linked to a different OpenID subject")
			}
			if !user.IsActive {
				return user, nil
			}
			encoded, err := setOrderedJSONField(user.ExtraData, "authOpenIDSub", sub)
			if err != nil {
				return store.User{}, err
			}
			if _, err := h.db.ExecContext(ctx, `UPDATE users SET extraData = ?, updatedAt = ? WHERE id = ?`, string(encoded), sqlDate(time.Now()), user.ID); err != nil {
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
			h.userAddedEmitter(UserBrowser(user, false, false))
		}
	}
	if !user.IsActive {
		return user, errors.New("user not active or not found")
	}
	if cfg.groupClaim != "" {
		if err := h.applyOIDCGroups(ctx, cfg, user, info); err != nil {
			if created {
				_, _ = h.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, user.ID)
			}
			return store.User{}, err
		}
		user, _ = h.userByID(ctx, user.ID)
	}
	if cfg.advancedPermsClaim != "" && user.Type != "admin" && user.Type != "root" {
		if err := h.applyOIDCPermissions(ctx, cfg, user, info); err != nil {
			if created {
				_, _ = h.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, user.ID)
			}
			return store.User{}, err
		}
		user, _ = h.userByID(ctx, user.ID)
	}
	return user, nil
}

// setOrderedJSONField updates one object member while retaining the source
// order of every existing member. Upstream mutates the parsed Sequelize JSON
// object and serializes it in insertion order; Go map marshaling sorts keys.
func setOrderedJSONField(data []byte, name string, value any) ([]byte, error) {
	if len(bytes.TrimSpace(data)) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		data = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return nil, errors.New("identity: OIDC extraData must be a JSON object")
	}
	type field struct {
		name  string
		value json.RawMessage
	}
	fields := make([]field, 0, 8)
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	found := false
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, errors.New("identity: invalid OIDC extraData key")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, err
		}
		if key == name {
			raw = encoded
			found = true
		}
		fields = append(fields, field{name: key, value: raw})
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("identity: trailing data after OIDC extraData object")
		}
		return nil, err
	}
	if !found {
		fields = append(fields, field{name: name, value: encoded})
	}
	var output bytes.Buffer
	output.WriteByte('{')
	for i, item := range fields {
		if i > 0 {
			output.WriteByte(',')
		}
		key, err := json.Marshal(item.name)
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(item.value)
		if err != nil {
			return nil, err
		}
		output.Write(key)
		output.WriteByte(':')
		output.Write(value)
	}
	output.WriteByte('}')
	return output.Bytes(), nil
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
	rows, err := h.db.QueryContext(ctx, `SELECT id, extraData FROM users`)
	if err != nil {
		return store.User{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return store.User{}, err
		}
		var extra map[string]any
		if json.Unmarshal(raw, &extra) == nil && extra["authOpenIDSub"] == sub {
			return h.userByID(ctx, id)
		}
	}
	if err := rows.Err(); err != nil {
		return store.User{}, err
	}
	return store.User{}, sql.ErrNoRows
}

func (h *Handler) userByEmail(ctx context.Context, email string) (store.User, error) {
	return h.userQuery(ctx, `SELECT id, username, email, pash, type, token, isActive, isLocked, lastSeen, permissions, bookmarks, extraData, createdAt, updatedAt FROM users WHERE lower(email) = ? LIMIT 1`, strings.ToLower(email))
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
	var email any
	if verified, _ := info["email_verified"].(bool); verified {
		email, _ = info["email"].(string)
	}
	id, err := randomUUID()
	if err != nil {
		return store.User{}, err
	}
	token, err := h.issueLegacyToken(id, username)
	if err != nil {
		return store.User{}, err
	}
	permissions := map[string]any{"download": true, "update": false, "delete": false, "upload": false, "createEreader": false, "accessAllLibraries": true, "accessAllTags": true, "accessExplicitContent": false, "selectedTagsNotAccessible": false, "librariesAccessible": []string{}, "itemTagsSelected": []string{}}
	extra := map[string]any{"authOpenIDSub": sub, "seriesHideFromContinueListening": []string{}}
	now := sqlDate(time.Now())
	_, err = h.db.ExecContext(ctx, `INSERT INTO users (id,username,email,pash,type,token,isActive,isLocked,lastSeen,permissions,bookmarks,extraData,createdAt,updatedAt) VALUES (?,?,?,NULL,'user',?,1,0,NULL,?,'[]',?,?,?)`, id, username, email, token, mustJSON(permissions), mustJSON(extra), now, now)
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
		_, err := h.db.ExecContext(ctx, `UPDATE users SET type = ?, updatedAt = ? WHERE id = ?`, role, sqlDate(time.Now()), user.ID)
		return err
	}
	return nil
}

func (h *Handler) applyOIDCPermissions(ctx context.Context, cfg oidcConfig, user store.User, info map[string]any) error {
	claim, ok := info[cfg.advancedPermsClaim].(map[string]any)
	if !ok {
		return errors.New("Advanced permissions claim is missing or invalid")
	}
	permissions := decodeMap(user.Permissions)
	mapping := map[string]string{"canDownload": "download", "canUpload": "upload", "canDelete": "delete", "canUpdate": "update", "canAccessExplicitContent": "accessExplicitContent", "canAccessAllLibraries": "accessAllLibraries", "canAccessAllTags": "accessAllTags", "canCreateEReader": "createEreader", "tagsAreDenylist": "selectedTagsNotAccessible", "allowedLibraries": "librariesAccessible", "allowedTags": "itemTagsSelected"}
	for key, value := range claim {
		mapped, exists := mapping[key]
		if !exists {
			return fmt.Errorf("Unexpected permission property: %s", key)
		}
		if mapped == "librariesAccessible" || mapped == "itemTagsSelected" {
			continue
		}
		permissions[mapped] = truthy(value)
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
	_, err = h.db.ExecContext(ctx, `UPDATE users SET permissions = ?, updatedAt = ? WHERE id = ?`, string(encoded), sqlDate(time.Now()), user.ID)
	return err
}

func (h *Handler) pruneOIDCState(now time.Time) {
	for state, flow := range h.oidcFlows {
		if now.Sub(flow.started) > oidcFlowTTL {
			delete(h.oidcFlows, state)
		}
	}
	for state, flow := range h.mobileFlows {
		if now.Sub(flow.started) > oidcFlowTTL {
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

func getString(value map[string]any, key string) string {
	result, _ := value[key].(string)
	return result
}

func truthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case float64:
		return v != 0
	default:
		return true
	}
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
	settings, err := h.loadSettings(ctx, "server-settings")
	if err != nil {
		return false
	}
	methods, _ := settings["authActiveAuthMethods"].([]any)
	for _, value := range methods {
		if value == method {
			return true
		}
	}
	return false
}
