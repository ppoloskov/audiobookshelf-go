package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/settings"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const tenYears = 10 * 365 * 24 * time.Hour

// oidcSettings returns the server settings with the OpenID provider checked
// to be fully configured.
func (s *Service) oidcSettings() (settings.Server, error) {
	server := s.settings.Server()
	for name, value := range map[string]string{"issuer": string(server.OpenIDIssuerURL), "client ID": string(server.OpenIDClientID),
		"client secret": string(server.OpenIDClientSecret), "authorization URL": string(server.OpenIDAuthorizeURL),
		"token URL": string(server.OpenIDTokenURL), "userinfo URL": string(server.OpenIDUserInfoURL), "JWKS URL": string(server.OpenIDJwksURL)} {
		if value == "" {
			return server, fmt.Errorf("OpenID Connect settings are not valid: missing %s", name)
		}
		if strings.HasSuffix(name, "URL") && !absoluteHTTP(value) {
			return server, fmt.Errorf("OpenID Connect settings are not valid: %s is not an http(s) URL", name)
		}
	}
	return server, nil
}

func absoluteHTTP(value string) bool {
	u, err := url.Parse(value)
	return err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https")
}

// oauthConfig is the OAuth client for the provider; redirect is where the
// provider returns to.
func oauthConfig(server settings.Server, redirect string) *oauth2.Config {
	scopes := []string{"openid", "profile", "email"}
	for _, claim := range []string{server.OpenIDGroupClaim, server.OpenIDAdvancedPermsClaim} {
		if claim != "" {
			scopes = append(scopes, claim)
		}
	}
	return &oauth2.Config{
		ClientID: string(server.OpenIDClientID), ClientSecret: string(server.OpenIDClientSecret), RedirectURL: redirect, Scopes: scopes,
		Endpoint: oauth2.Endpoint{AuthURL: string(server.OpenIDAuthorizeURL), TokenURL: string(server.OpenIDTokenURL), AuthStyle: oauth2.AuthStyleInHeader},
	}
}

// providerRedirect is the URL the provider returns to. The path is the
// admin's authOpenIDSubfolderForRedirectURLs setting verbatim (the site root
// when unset), not the router base path (OidcAuthStrategy.js).
func providerRedirect(r *http.Request, server settings.Server, endpoint string) string {
	u, _ := url.Parse(requestOrigin(r)) // built from a scheme and the request host
	subfolder := ""
	if server.OpenIDSubfolder != nil {
		subfolder = *server.OpenIDSubfolder
	}
	u.Path = path.Join("/", subfolder, endpoint)
	return u.String()
}

// oidcStart is GET /auth/openid. Web clients pass ?callback= (a same-origin
// page that receives the tokens) and the server keeps the PKCE verifier.
// Mobile apps pass response_type=code, redirect_uri (on the allow-list),
// code_challenge (S256) and optionally state, and finish the code exchange
// themselves through /auth/openid/callback.
func (s *Service) oidcStart(w http.ResponseWriter, r *http.Request) error {
	if !s.methodEnabled("openid") {
		return errUnauthorized
	}
	server, err := s.oidcSettings()
	if err != nil {
		return httpx.Internal("", err)
	}
	query := r.URL.Query()
	if kind := query.Get("response_type"); kind != "" && kind != "code" {
		return httpx.BadRequest("Invalid response_type, only code supported")
	}
	mobile := query.Get("response_type") == "code" || query.Get("redirect_uri") != "" || query.Get("code_challenge") != ""
	if !mobile && query.Has("state") {
		return httpx.BadRequest("Invalid state, not allowed on web flow")
	}
	state, err := query.Get("state"), error(nil)
	if !mobile || state == "" {
		state, err = randomToken(32)
	}
	nonce, nonceErr := randomToken(32)
	if err = errors.Join(err, nonceErr); err != nil {
		return httpx.Internal("", err)
	}
	flow := webFlow{nonce: nonce, mobile: mobile}
	options := []oauth2.AuthCodeOption{oauth2.SetAuthURLParam("nonce", nonce)}
	if mobile {
		returnURI, challenge := query.Get("redirect_uri"), query.Get("code_challenge")
		switch {
		case !allowedMobileRedirect(server.OpenIDMobileRedirectURIs, returnURI):
			return httpx.BadRequest("Invalid redirect_uri")
		case challenge == "":
			return httpx.BadRequest("code_challenge required for mobile flow (PKCE)")
		case query.Get("code_challenge_method") != "" && query.Get("code_challenge_method") != "S256":
			return httpx.BadRequest("Only S256 code_challenge_method method supported")
		}
		options = append(options, oauth2.SetAuthURLParam("code_challenge", challenge), oauth2.SetAuthURLParam("code_challenge_method", "S256"))
		flow.redirectURI = providerRedirect(r, server, "/auth/openid/mobile-redirect")
		s.mobile.put(state, returnURI)
		setCookie(w, "auth_method", "openid-mobile", tenYears)
	} else {
		callback := query.Get("callback")
		switch {
		case callback == "":
			return jsonMessage(w, r, "No callback parameter")
		case !s.validWebCallback(callback, r):
			return jsonMessage(w, r, "Invalid callback URL - must be same-origin")
		}
		flow.verifier = oauth2.GenerateVerifier()
		options = append(options, oauth2.S256ChallengeOption(flow.verifier))
		flow.redirectURI = providerRedirect(r, server, "/auth/openid/callback")
		setCookie(w, "auth_cb", callback, 10*time.Minute)
		setCookie(w, "auth_method", "openid", tenYears)
	}
	s.web.put(state, flow)
	http.Redirect(w, r, oauthConfig(server, flow.redirectURI).AuthCodeURL(state, options...), http.StatusFound)
	return nil
}

// oidcMobileRedirect is GET /auth/openid/mobile-redirect, the redirect URI the
// provider knows for apps: it forwards code and state to the app's own
// redirect URI.
func (s *Service) oidcMobileRedirect(w http.ResponseWriter, r *http.Request) error {
	if !s.methodEnabled("openid") {
		return errUnauthorized
	}
	state := r.URL.Query().Get("state")
	returnURI, ok := s.mobile.take(state)
	if !ok || state == "" {
		return httpx.BadRequest("State parameter mismatch")
	}
	target, err := url.Parse(returnURI)
	if err != nil {
		return httpx.BadRequest("Invalid redirect URI")
	}
	target.RawQuery = url.Values{"code": {r.URL.Query().Get("code")}, "state": {state}}.Encode()
	http.Redirect(w, r, target.String(), http.StatusFound)
	return nil
}

// jsonMessage answers 400 {"message": message}, the body of a bad web start.
func jsonMessage(w http.ResponseWriter, r *http.Request, message string) error {
	return httpx.JSONStatus(w, r, http.StatusBadRequest, struct {
		Message string `json:"message"`
	}{message})
}

// loginFailure sends the browser back to the login page with an error.
func loginFailure(w http.ResponseWriter, r *http.Request, message string) error {
	http.Redirect(w, r, "/login?error="+url.PathEscape(message)+"&autoLaunch=0", http.StatusFound)
	return nil
}

// oidcCallback is GET /auth/openid/callback. For a web client it signs the
// user in and redirects to the page named at the start with the tokens in the
// query; for an app (which sends the code and its PKCE verifier) it answers the
// login JSON with both tokens.
func (s *Service) oidcCallback(w http.ResponseWriter, r *http.Request) error {
	if !s.methodEnabled("openid") {
		return errUnauthorized
	}
	query := r.URL.Query()
	flow, ok := s.web.take(query.Get("state"))
	if !ok || query.Get("state") == "" {
		if cookieValue(r, "auth_method") == "openid-mobile" || query.Get("code_verifier") != "" {
			return httpx.BadRequest("No session")
		}
		return loginFailure(w, r, "No sign-in session")
	}
	if providerError := query.Get("error"); providerError != "" {
		slog.Error("openid callback: provider returned an error", "error", providerError, "description", query.Get("error_description"))
		return loginFailure(w, r, "Unauthorized")
	}
	server, err := s.oidcSettings()
	if err != nil {
		slog.Error("openid callback", "error", err)
		return loginFailure(w, r, "Error in callback")
	}
	verifier := flow.verifier
	if supplied := query.Get("code_verifier"); supplied != "" {
		verifier = supplied
	}
	claims, idToken, err := s.exchange(r.Context(), server, flow, query.Get("code"), verifier)
	var user db.User
	if err == nil {
		user, err = s.oidcUser(r.Context(), server, claims)
	}
	if err != nil {
		slog.Error("openid callback", "error", err)
		return loginFailure(w, r, "Unauthorized")
	}
	access, refresh, err := s.createSession(r.Context(), user, r)
	if err != nil {
		slog.Error("openid login", "error", err)
		return loginFailure(w, r, "Error in callback")
	}
	slog.Info("user logged in with OpenID", "username", db.Val(user.Username), "ip", httpx.ClientIP(r))
	http.SetCookie(w, &http.Cookie{Name: "openid_id_token", Value: url.QueryEscape(idToken), Path: "/", MaxAge: int(tenYears.Seconds()),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	method := cookieValue(r, "auth_method")
	if flow.mobile || method == "openid-mobile" || method == "api" {
		payload, err := s.payloadFor(r.Context(), user)
		if err != nil {
			slog.Error("openid login", "error", err)
			return loginFailure(w, r, "Error in callback")
		}
		return httpx.JSON(w, r, signedIn(payload, access, &refresh))
	}
	s.setRefreshCookie(w, r, refresh)
	callback := cookieValue(r, "auth_cb")
	if callback == "" {
		return loginFailure(w, r, "No callback or already expired")
	}
	http.Redirect(w, r, callback+"?setToken="+url.QueryEscape(db.Val(user.Token))+"&accessToken="+url.QueryEscape(access), http.StatusFound)
	return nil
}

// exchange trades the authorization code for tokens and verifies the ID token
// (signature, issuer, audience, signing algorithm, nonce), then reads the
// userinfo, whose subject must be the ID token's.
func (s *Service) exchange(ctx context.Context, server settings.Server, flow webFlow, code, verifier string) (claimSet, string, error) {
	if code == "" {
		return nil, "", errors.New("no authorization code")
	}
	ctx, cancel := context.WithTimeout(oidc.ClientContext(ctx, s.client), 10*time.Second)
	defer cancel()
	var options []oauth2.AuthCodeOption
	if verifier != "" {
		options = append(options, oauth2.VerifierOption(verifier))
	}
	token, err := oauthConfig(server, flow.redirectURI).Exchange(ctx, code, options...)
	if err != nil {
		return nil, "", fmt.Errorf("token exchange: %w", err)
	}
	rawID, _ := token.Extra("id_token").(string) // absent means no ID token
	if rawID == "" {
		return nil, "", errors.New("token response has no id_token")
	}
	algs := []string{server.OpenIDSigningAlg}
	provider := (&oidc.ProviderConfig{
		IssuerURL: string(server.OpenIDIssuerURL), AuthURL: string(server.OpenIDAuthorizeURL), TokenURL: string(server.OpenIDTokenURL),
		UserInfoURL: string(server.OpenIDUserInfoURL), JWKSURL: string(server.OpenIDJwksURL), Algorithms: algs,
	}).NewProvider(ctx)
	idToken, err := provider.Verifier(&oidc.Config{ClientID: string(server.OpenIDClientID), SupportedSigningAlgs: algs}).Verify(ctx, rawID)
	if err != nil {
		return nil, "", fmt.Errorf("verify id token: %w", err)
	}
	if idToken.Subject == "" || idToken.Nonce != flow.nonce {
		return nil, "", errors.New("id token has no subject or the wrong nonce")
	}
	info, err := provider.UserInfo(ctx, oauth2.StaticTokenSource(token))
	if err != nil {
		return nil, "", fmt.Errorf("userinfo: %w", err)
	}
	var claims claimSet
	if err := info.Claims(&claims); err != nil || info.Subject != idToken.Subject {
		return nil, "", errors.New("userinfo subject does not match the id token")
	}
	return claims, rawID, nil
}

// oidcDiscovery is GET /auth/openid/config?issuer=: an admin asks the server
// to fetch the provider's discovery document so the settings form can fill in
// the endpoints.
func (s *Service) oidcDiscovery(w http.ResponseWriter, r *http.Request) error {
	principal, err := s.Authenticate(r)
	if err != nil {
		return errUnauthorized
	}
	if !principal.IsAdmin() {
		return httpx.Forbidden()
	}
	issuer := strings.TrimSuffix(r.URL.Query().Get("issuer"), "/")
	if issuer == "" {
		return httpx.BadRequest("Invalid request. Query param 'issuer' is required")
	}
	target, err := url.Parse(issuer + "/.well-known/openid-configuration")
	if err != nil || !absoluteHTTP(target.String()) || !strings.HasSuffix(target.Path, "/.well-known/openid-configuration") {
		return httpx.BadRequest("Invalid request. Query param 'issuer' is invalid")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return httpx.BadRequest("Failed to get openid configuration")
	}
	var doc struct {
		Issuer                string   `json:"issuer,omitempty"`
		AuthorizationEndpoint string   `json:"authorization_endpoint,omitempty"`
		TokenEndpoint         string   `json:"token_endpoint,omitempty"`
		UserinfoEndpoint      string   `json:"userinfo_endpoint,omitempty"`
		EndSessionEndpoint    string   `json:"end_session_endpoint,omitempty"`
		JWKSURI               string   `json:"jwks_uri,omitempty"`
		SigningAlgs           []string `json:"id_token_signing_alg_values_supported,omitempty"`
	}
	response, err := s.client.Do(request)
	if err == nil {
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			err = fmt.Errorf("status %d", response.StatusCode)
		} else {
			err = json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&doc)
		}
	}
	if err != nil {
		slog.Error("openid discovery failed", "url", target.String(), "error", err)
		return httpx.BadRequest("Failed to get openid configuration")
	}
	return httpx.JSON(w, r, doc)
}

// endSessionURL is the provider's logout URL for the signed-in user, or nil
// when none is configured. Web clients also say where to return to.
func (s *Service) endSessionURL(r *http.Request, idToken, method string) *string {
	logout := string(s.settings.Server().OpenIDLogoutURL)
	u, err := url.Parse(logout)
	if logout == "" || err != nil || !absoluteHTTP(logout) {
		return nil
	}
	query := u.Query()
	query.Del("id_token_hint")
	if idToken != "" {
		query.Set("id_token_hint", idToken)
	}
	if method == "openid" {
		query.Set("post_logout_redirect_uri", requestOrigin(r)+s.cfg.RouterBasePath+"/login")
	}
	u.RawQuery = query.Encode()
	target := u.String()
	return &target
}

// allowedMobileRedirect reports whether an app may be sent to uri: it is on
// the allow-list, which may be the single entry "*".
func allowedMobileRedirect(allowed []string, uri string) bool {
	return uri != "" && (slices.Contains(allowed, uri) || (len(allowed) == 1 && allowed[0] == "*"))
}

// validWebCallback reports whether callback is a page of this server under
// the router base path, so tokens are never sent to another origin.
func (s *Service) validWebCallback(callback string, r *http.Request) bool {
	if strings.HasPrefix(callback, "//") || strings.HasPrefix(callback, `/\`) {
		return false
	}
	server, err := url.Parse(requestOrigin(r))
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
	decoded, err := url.PathUnescape(target.EscapedPath())
	return err == nil && !strings.HasPrefix(decoded, "//") && !strings.HasPrefix(decoded, `/\`) &&
		strings.HasPrefix(target.Path, strings.TrimSuffix(s.cfg.RouterBasePath, "/")+"/")
}
