package contracttest

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestJourneyMisc is the search, podcast, OpenID, public share and session
// journey (ROADMAP F8e). It runs on its own fresh install and is recorded into
// contracts/shapes/misc.json, the same way as TestJourneyAdmin. To record it
// against TS:
//
//	node go-backend/tools/contractfixture/serve-upstream.cjs   # prints ROOT and READY
//	cd go-backend && go test ./internal/contracttest -run '^TestJourneyMisc$' -record \
//	    -upstream=<READY URL> -upstream-root=<ROOT>
//
// then stop the serve script.
//
// The network is local: a test server serves the podcast feeds and is the
// OpenID Connect provider (discovery, authorize, token, userinfo, jwks). The
// search steps that would reach a built-in internet provider (Google Books,
// iTunes, Audible, Audnexus) are not sent; their routes are checked on the
// no-network paths (no title, no ASIN, no query) and with the custom metadata
// provider, which is the local server.
const (
	miscShapesFile = "../../contracts/shapes/misc.json"
	initShapesFile = "../../contracts/shapes/init.json"

	miscClientID     = "abs-client"
	miscClientSecret = "abs-secret"
	miscSubject      = "trial-oidc-listener"
	// miscVerifier is the PKCE verifier of the mobile flow; miscChallenge is its S256 challenge.
	miscVerifier = "trial-pkce-verifier-0123456789-abcdefghijklmnopqrstuvwxyz"
)

var miscChallenge = func() string {
	digest := sha256.Sum256([]byte(miscVerifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}()

func TestJourneyMisc(t *testing.T) {
	runJourney(t, miscShapesFile, nil, journeyMiscSteps)
}

// journeyMiscSteps is the misc journey, in order. Steps create what later steps
// change, so the order must not change. The OpenID steps need the cookie jar
// miscClient sets: the start step's session carries to the callback.
func journeyMiscSteps(t *testing.T, in *Install, c *journeyCtx) []journeyStep {
	ids := in.IDs
	saga1, podcast, podcastLibrary := ids["saga1"], ids["podcast"], ids["podcastLibrary"]
	const noSuchID = "00000000-0000-4000-8000-000000000000"
	server := startMiscServer(t)
	miscClient(t, in, c)
	customSlug := func(c *journeyCtx) string { return "custom-" + c.s("miscProvider", "provider", "id") }
	opmlFolder := func(c *journeyCtx) string { return c.s("podcastLib", "folders", 0, "id") }
	publicURL := "/public/share/misc-share-saga1"

	return []journeyStep{
		// Sign in.
		{id: "login.root", method: "POST", route: "/login", body: in.Users["root"], headers: map[string]string{"X-Return-Tokens": "true"}, loginAs: "root"},
		{id: "login.listener", method: "POST", route: "/login", body: in.Users["listener"], headers: map[string]string{"X-Return-Tokens": "true"}, loginAs: "listener"},

		// Reads the later steps take ids from: the podcast library's folder, and the
		// book's media id (the share needs it).
		{id: "plib.get", who: "root", route: "/api/libraries/" + podcastLibrary, capture: "podcastLib"},
		{id: "item.saga1", who: "root", route: "/api/items/" + saga1 + "?expanded=1", capture: "item_saga1"},

		// Custom metadata providers: a book provider and a podcast provider, both
		// served by the local server (the search adapter calls url + /search).
		{id: "providers.create.book", who: "root", method: "POST", route: "/api/custom-metadata-providers", body: map[string]any{
			"name": "Trial Misc Books", "url": c.fixtureURL, "mediaType": "book", "authHeaderValue": "",
		}, capture: "miscProvider"},
		{id: "providers.create.podcast", who: "root", method: "POST", route: "/api/custom-metadata-providers", body: map[string]any{
			"name": "Trial Misc Podcasts", "url": c.fixtureURL, "mediaType": "podcast", "authHeaderValue": "",
		}, capture: "miscPodcastProvider"},
		{id: "search.providers", who: "root", route: "/api/search/providers"},
		{id: "search.providers.listener", who: "listener", route: "/api/search/providers"},

		// Books. Without a title the search returns nothing and sends no request.
		{id: "search.books.noterm", who: "root", route: "/api/search/books"},
		{id: "search.books.builtin.noterm", who: "root", route: "/api/search/books?" + miscQuery("provider", "google", "title", "")},
		{id: "search.books.anon", routeFn: func(c *journeyCtx) string {
			return "/api/search/books?" + miscQuery("provider", customSlug(c), "title", "Matched Title")
		}},
		{id: "search.books.custom", who: "root", routeFn: func(c *journeyCtx) string {
			return "/api/search/books?" + miscQuery("provider", customSlug(c), "title", "Matched Title", "author", "Terry Fixture")
		}},
		{id: "search.books.custom.listener", who: "listener", routeFn: func(c *journeyCtx) string {
			return "/api/search/books?" + miscQuery("provider", customSlug(c), "title", "Matched Title")
		}},
		{id: "search.books.custom.nothing", who: "root", routeFn: func(c *journeyCtx) string {
			return "/api/search/books?" + miscQuery("provider", customSlug(c), "title", "Nothing Here")
		}},
		{id: "search.books.custom.item", who: "root", routeFn: func(c *journeyCtx) string {
			return "/api/search/books?" + miscQuery("provider", customSlug(c), "title", "Matched Title", "id", saga1)
		}},
		{id: "search.books.custom.item.missing", who: "root", routeFn: func(c *journeyCtx) string {
			return "/api/search/books?" + miscQuery("provider", customSlug(c), "title", "Matched Title", "id", noSuchID)
		}},
		{id: "search.books.custom.unknown", who: "root", routeFn: func(c *journeyCtx) string {
			return "/api/search/books?" + miscQuery("provider", "custom-"+noSuchID, "title", "Matched Title")
		}},

		// Covers. The title is required; the podcast branch checks it before it
		// reaches the network.
		{id: "search.covers.custom", who: "root", routeFn: func(c *journeyCtx) string {
			return "/api/search/covers?" + miscQuery("provider", customSlug(c), "title", "Matched Title", "author", "Terry Fixture")
		}},
		{id: "search.covers.custom.listener", who: "listener", routeFn: func(c *journeyCtx) string {
			return "/api/search/covers?" + miscQuery("provider", customSlug(c), "title", "Matched Title")
		}},
		{id: "search.covers.notitle", who: "root", route: "/api/search/covers?" + miscQuery("provider", "google")},
		{id: "search.covers.podcast.notitle", who: "root", route: "/api/search/covers?" + miscQuery("podcast", "1")},

		// Authors and chapters. Their searches need the internet, so only the
		// no-query and invalid-ASIN paths are sent.
		{id: "search.authors.noq", who: "root", route: "/api/search/authors"},
		{id: "search.chapters.noasin", who: "root", route: "/api/search/chapters"},
		{id: "search.chapters.badasin", who: "root", route: "/api/search/chapters?" + miscQuery("asin", "NOTANASIN")},

		// Podcast episode matching: the podcast's feed is the local RSS feed. The
		// listener is not an admin; the root's match updates the three episodes,
		// the second match finds them already matched.
		{id: "podcast.feed.set", who: "root", method: "PATCH", route: "/api/items/" + podcast + "/media", body: map[string]any{
			"metadata": map[string]any{"feedUrl": server.URL + "/feed.xml"},
		}},
		{id: "podcast.match.listener", who: "listener", method: "POST", route: "/api/podcasts/" + podcast + "/match-episodes"},
		{id: "podcast.match", who: "root", method: "POST", route: "/api/podcasts/" + podcast + "/match-episodes"},
		{id: "podcast.match.again", who: "root", method: "POST", route: "/api/podcasts/" + podcast + "/match-episodes"},
		{id: "podcast.item.after.match", who: "root", route: "/api/items/" + podcast + "?expanded=1"},

		// OPML: parse the file listing the local feed, then create the podcast
		// from the parsed feeds the way the client sends them.
		{id: "opml.parse", who: "root", method: "POST", route: "/api/podcasts/opml/parse", body: map[string]any{"opmlText": miscOPML(server.URL + "/opml-feed.xml")}, capture: "opmlParsed"},
		{id: "opml.create.listener", who: "listener", method: "POST", route: "/api/podcasts/opml/create", bodyFn: func(c *journeyCtx) any {
			return miscOPMLCreate(c, opmlFolder(c), podcastLibrary)
		}},
		{id: "opml.create.invalid", who: "root", method: "POST", route: "/api/podcasts/opml/create", body: map[string]any{
			"feeds": []any{"not a url"}, "libraryId": podcastLibrary, "folderId": "x",
		}},
		{id: "opml.create.nofolder", who: "root", method: "POST", route: "/api/podcasts/opml/create", body: map[string]any{
			"feeds": []any{server.URL + "/opml-feed.xml"}, "libraryId": podcastLibrary,
		}},
		{id: "opml.create.badfolder", who: "root", method: "POST", route: "/api/podcasts/opml/create", body: map[string]any{
			"feeds": []any{server.URL + "/opml-feed.xml"}, "libraryId": podcastLibrary, "folderId": noSuchID,
		}},
		{id: "opml.create", who: "root", method: "POST", route: "/api/podcasts/opml/create", bodyFn: func(c *journeyCtx) any {
			return miscOPMLCreate(c, opmlFolder(c), podcastLibrary)
		}, settle: true},
		{id: "opml.create.items", who: "root", route: "/api/libraries/" + podcastLibrary + "/items?limit=20&page=0&minified=1"},

		// OpenID: the provider's configuration, then the settings that turn the
		// flow on (the client's authentication page sends the whole settings object).
		{id: "openid.config.listener", who: "listener", route: "/auth/openid/config?" + miscQuery("issuer", server.URL)},
		{id: "openid.config.noissuer", who: "root", route: "/auth/openid/config"},
		{id: "openid.config.badissuer", who: "root", route: "/auth/openid/config?" + miscQuery("issuer", "not a url")},
		{id: "openid.config", who: "root", route: "/auth/openid/config?" + miscQuery("issuer", server.URL)},
		{id: "auth.get", who: "root", route: "/api/auth-settings", capture: "authSettings"},
		{id: "openid.settings.patch", who: "root", method: "PATCH", route: "/api/auth-settings", bodyFn: func(c *journeyCtx) any {
			body := copyCaptured(c, "authSettings")
			for k, v := range miscOIDCSettings(server.URL) {
				body[k] = v
			}
			return body
		}},

		// Web flow. The start step is the server's redirect to the provider; the
		// callback is the provider's redirect back, carrying the code it issued.
		{id: "openid.start.nocallback", route: "/auth/openid"},
		{id: "openid.start.evil", route: "/auth/openid?" + miscQuery("callback", "https://evil.example/audiobookshelf/login")},
		{id: "openid.start.web", route: "/auth/openid?" + miscQuery("callback", "/audiobookshelf/login")},
		{id: "openid.callback.web", routeFn: func(c *journeyCtx) string {
			route := server.callbackRoute(t, c.s("authorizeURL"))
			c.vals["oidcWebRoute"] = route
			return route
		}},
		{id: "openid.callback.web.replay", routeFn: func(c *journeyCtx) string { return c.s("oidcWebRoute") }},

		// Mobile flow: PKCE, the redirect URI allow-list, the server's own
		// mobile-redirect route, then the app's callback with its verifier.
		{id: "openid.start.mobile.badredirect", route: "/auth/openid?" + miscQuery("redirect_uri", "audiobookshelf://evil", "code_challenge", miscChallenge, "code_challenge_method", "S256", "response_type", "code")},
		{id: "openid.start.mobile.nochallenge", route: "/auth/openid?" + miscQuery("redirect_uri", "audiobookshelf://oauth", "response_type", "code")},
		{id: "openid.start.mobile", route: "/auth/openid?" + miscQuery("redirect_uri", "audiobookshelf://oauth", "code_challenge", miscChallenge, "code_challenge_method", "S256", "response_type", "code", "state", "trial-mobile-state")},
		{id: "openid.mobile-redirect.badstate", route: "/auth/openid/mobile-redirect?" + miscQuery("code", "trial-code", "state", "trial-unknown-state")},
		{id: "openid.mobile-redirect", routeFn: func(c *journeyCtx) string {
			return server.authorizeAt(t, c.s("authorizeURL")).RequestURI()
		}},
		{id: "openid.callback.mobile", routeFn: func(c *journeyCtx) string {
			back := server.authorizeAt(t, c.s("authorizeURL")).Query()
			return "/auth/openid/callback?" + url.Values{"code": back["code"], "state": back["state"], "code_verifier": {miscVerifier}}.Encode()
		}},

		// No session: a fresh cookie jar has no OpenID state. The mobile client
		// gets 400 "No session"; the web flow is redirected to the login page.
		{id: "openid.callback.nosession.mobile", routeFn: func(c *journeyCtx) string {
			in.client.Jar = newMiscJar(t)
			return "/auth/openid/callback?" + miscQuery("code", "trial-code", "state", "trial-state", "code_verifier", miscVerifier)
		}},
		{id: "openid.callback.nosession.web", route: "/auth/openid/callback?" + miscQuery("code", "trial-code", "state", "trial-state")},

		// Public share: the book, its cover, the playback session its share opens
		// and the progress and download routes the share page calls.
		{id: "share.create", who: "root", method: "POST", route: "/api/share/mediaitem", bodyFn: func(c *journeyCtx) any {
			return map[string]any{"slug": "misc-share-saga1", "mediaItemType": "book", "mediaItemId": bookID(c, "saga1"), "expiresAt": 0, "isDownloadable": true}
		}, capture: "miscShare"},
		{id: "share.public", route: publicURL, capture: "publicShare"},
		{id: "share.public.t", route: publicURL + "?t=7"},
		{id: "share.public.cover", route: publicURL + "/cover", binary: "bytes"},
		// The share's own session is not a playback session: its tracks are served
		// under the share (TS ShareManager), so the public session route answers 404
		// for it. The public session route serves the playback session a play opens.
		{id: "share.public.track", routeFn: func(c *journeyCtx) string {
			return "/public/session/" + c.s("publicShare", "playbackSession", "id") + "/track/1"
		}},
		{id: "play.saga1", who: "root", method: "POST", route: "/api/items/" + saga1 + "/play", body: webPlayBody(), capture: "playSession"},
		{id: "session.public.track", routeFn: func(c *journeyCtx) string {
			return "/public/session/" + c.s("playSession", "id") + "/track/1"
		}, binary: "bytes"},
		{id: "share.public.download", route: publicURL + "/download", binary: "bytes"},
		{id: "share.public.progress", method: "PATCH", route: publicURL + "/progress", body: map[string]any{"currentTime": 12, "timeListened": 4}},
		{id: "share.public.progress.bad", method: "PATCH", route: publicURL + "/progress", body: map[string]any{"currentTime": "soon"}},
		{id: "share.public.missing", route: "/public/share/misc-share-nope/cover"},
	}
}

// miscQuery encodes key and value pairs as a query string.
func miscQuery(kv ...string) string {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return v.Encode()
}

// miscOIDCSettings is the authentication settings the OpenID steps turn on: the
// provider's endpoints, the client, the local mobile redirect, and matching the
// existing user by username (the userinfo's preferred_username).
func miscOIDCSettings(base string) map[string]any {
	return map[string]any{
		"authActiveAuthMethods":              []any{"local", "openid"},
		"authOpenIDIssuerURL":                base,
		"authOpenIDAuthorizationURL":         base + "/authorize",
		"authOpenIDTokenURL":                 base + "/token",
		"authOpenIDUserInfoURL":              base + "/userinfo",
		"authOpenIDJwksURL":                  base + "/jwks",
		"authOpenIDLogoutURL":                base + "/logout",
		"authOpenIDClientID":                 miscClientID,
		"authOpenIDClientSecret":             miscClientSecret,
		"authOpenIDTokenSigningAlgorithm":    "RS256",
		"authOpenIDButtonText":               "Trial Login",
		"authOpenIDAutoLaunch":               false,
		"authOpenIDAutoRegister":             false,
		"authOpenIDMatchExistingBy":          "username",
		"authOpenIDMobileRedirectURIs":       []any{"audiobookshelf://oauth"},
		"authOpenIDSubfolderForRedirectURLs": "",
		"authOpenIDGroupClaim":               "",
		"authOpenIDAdvancedPermsClaim":       "",
	}
}

// miscOPML is an OPML file listing one feed.
func miscOPML(feedURL string) string {
	return `<?xml version="1.0" encoding="UTF-8"?><opml version="2.0"><head><title>Trial</title></head>` +
		`<body><outline text="Trial OPML Show" type="rss" xmlUrl="` + feedURL + `"/></body></opml>`
}

// miscOPMLCreate is the body the OPML dialog sends: the feed URLs of the parse
// the journey captured as opmlParsed.
func miscOPMLCreate(c *journeyCtx, folder, library string) map[string]any {
	feeds := []any{}
	list, _ := c.v("opmlParsed", "feeds").([]any)
	for _, e := range list {
		if m, ok := e.(map[string]any); ok {
			feeds = append(feeds, m["feedUrl"])
		}
	}
	return map[string]any{"feeds": feeds, "libraryId": library, "folderId": folder, "autoDownloadEpisodes": false}
}

// miscClient gives the install's client a cookie jar, so the sessions the flows
// set (the OpenID state, the share session) carry from step to step. Redirects
// are not followed: a step records the redirect it got. The URL of the
// provider's authorize endpoint, which the start steps redirect to, is kept as
// authorizeURL for the steps that finish the flow.
func miscClient(t testing.TB, in *Install, c *journeyCtx) {
	t.Helper()
	in.client.Jar = newMiscJar(t)
	in.client.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
		if req.URL.Path == "/authorize" {
			c.vals["authorizeURL"] = req.URL.String()
		}
		return http.ErrUseLastResponse
	}
}

func newMiscJar(t testing.TB) *cookiejar.Jar {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	return jar
}

// miscServer is a local test server: the podcast feeds the steps read, and an
// OpenID Connect provider with an RSA key, the same flow as
// internal/identity/oidc_test.go (discovery, authorize, token, userinfo, jwks).
type miscServer struct {
	URL string
	key *rsa.PrivateKey

	mu     sync.Mutex
	codes  map[string]miscCode // authorization codes issued and not yet redeemed
	tokens map[string]bool     // access tokens issued
}

// miscCode is what the provider keeps about an authorization request.
type miscCode struct {
	challenge, nonce, redirectURI string
}

func startMiscServer(t testing.TB) *miscServer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("provider key: %v", err)
	}
	s := &miscServer{key: key, codes: map[string]miscCode{}, tokens: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	return s
}

func (s *miscServer) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/feed.xml":
		writeMiscFeed(w, s.URL, "Fixture Cast", "Pilot Episode", "Second Outing", "Third Time")
	case "/opml-feed.xml":
		writeMiscFeed(w, s.URL, "Trial OPML Show", "OPML Pilot")
	case "/.well-known/openid-configuration":
		writeMiscJSON(w, map[string]any{
			"issuer": s.URL, "authorization_endpoint": s.URL + "/authorize", "token_endpoint": s.URL + "/token",
			"userinfo_endpoint": s.URL + "/userinfo", "jwks_uri": s.URL + "/jwks", "end_session_endpoint": s.URL + "/logout",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	case "/authorize":
		s.authorize(w, r)
	case "/token":
		s.token(w, r)
	case "/userinfo":
		s.userinfo(w, r)
	case "/jwks":
		s.jwks(w)
	default:
		http.NotFound(w, r)
	}
}

// authorize issues a code for the request and redirects back with it and the
// state, the way a provider redirects after the user approves.
func (s *miscServer) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	back, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || back.Scheme == "" {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	code := miscRandom()
	s.mu.Lock()
	s.codes[code] = miscCode{challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), redirectURI: q.Get("redirect_uri")}
	s.mu.Unlock()
	back.RawQuery = url.Values{"code": {code}, "state": {q.Get("state")}}.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

// token redeems a code once: the client's credentials, the PKCE verifier and
// the redirect URI must match what the authorize request sent.
func (s *miscServer) token(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "form", http.StatusBadRequest)
		return
	}
	clientID, secret, ok := r.BasicAuth()
	if !ok || clientID != miscClientID || secret != miscClientSecret {
		writeMiscStatus(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	s.mu.Lock()
	info, found := s.codes[r.PostForm.Get("code")]
	delete(s.codes, r.PostForm.Get("code"))
	s.mu.Unlock()
	digest := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !found || r.PostForm.Get("code_verifier") == "" || base64.RawURLEncoding.EncodeToString(digest[:]) != info.challenge ||
		r.PostForm.Get("redirect_uri") != info.redirectURI {
		writeMiscStatus(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	access := miscRandom()
	s.mu.Lock()
	s.tokens[access] = true
	s.mu.Unlock()
	writeMiscJSON(w, map[string]any{
		"access_token": access, "token_type": "Bearer", "expires_in": 3600, "id_token": s.idToken(info.nonce),
	})
}

func (s *miscServer) userinfo(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	ok := s.tokens[token]
	s.mu.Unlock()
	if !ok {
		writeMiscStatus(w, http.StatusUnauthorized, "invalid_token")
		return
	}
	writeMiscJSON(w, map[string]any{
		"sub": miscSubject, "preferred_username": "trial-listener", "email": "trial-listener@example.com",
		"email_verified": true, "name": "Trial Listener",
	})
}

func (s *miscServer) jwks(w http.ResponseWriter) {
	writeMiscJSON(w, map[string]any{"keys": []any{map[string]string{
		"kty": "RSA", "kid": "key-1", "alg": "RS256", "use": "sig",
		"n": base64.RawURLEncoding.EncodeToString(s.key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(s.key.E)).Bytes()),
	}}})
}

// idToken is an RS256 ID token for the test subject, with the request's nonce
// when it sent one.
func (s *miscServer) idToken(nonce string) string {
	now := time.Now()
	claims := map[string]any{"iss": s.URL, "aud": miscClientID, "sub": miscSubject, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()}
	if nonce != "" {
		claims["nonce"] = nonce
	}
	header, err := json.Marshal(map[string]string{"alg": "RS256", "kid": "key-1", "typ": "JWT"})
	if err != nil {
		return ""
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return ""
	}
	signing := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, digest[:])
	if err != nil {
		return ""
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// authorizeAt sends the browser's request to the provider's authorize URL (the
// one the server redirected to) and returns the URL the provider redirects back to.
func (s *miscServer) authorizeAt(t testing.TB, authorizeURL string) *url.URL {
	t.Helper()
	if authorizeURL == "" {
		t.Fatal("no authorize URL: the start step did not redirect to the provider")
	}
	client := &http.Client{Timeout: requestLimit, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, authorizeURL, nil)
	if err != nil {
		t.Fatalf("authorize request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize: status %d, want a redirect", resp.StatusCode)
	}
	back, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("authorize redirect: %v", err)
	}
	return back
}

// callbackRoute is the route the provider's redirect back to the server carries
// the code to, for the web callback.
func (s *miscServer) callbackRoute(t testing.TB, authorizeURL string) string {
	t.Helper()
	return s.authorizeAt(t, authorizeURL).RequestURI()
}

func miscRandom() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "trial-random-unavailable"
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func writeMiscJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeMiscStatus(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"error":%q}`, code)
}

// writeMiscFeed writes an RSS feed whose items have the given titles and
// enclosures on the server's own address.
func writeMiscFeed(w http.ResponseWriter, base, title string, items ...string) {
	var b strings.Builder
	fmt.Fprintf(&b, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><title>%s</title><link>%s</link><description>Trial feed</description>`, title, base)
	for i, item := range items {
		fmt.Fprintf(&b, `<item><title>%s</title><guid>trial-%d</guid><pubDate>Mon, 01 Jan 2024 00:00:00 GMT</pubDate>`+
			`<enclosure url="%s/audio/%d.mp3" length="1024" type="audio/mpeg"/></item>`, item, i, base, i)
	}
	b.WriteString(`</channel></rss>`)
	w.Header().Set("Content-Type", "application/rss+xml")
	_, _ = w.Write([]byte(b.String()))
}

// TestJourneyInit is the first-run journey (ROADMAP F8e): an install with no
// root user answers /status with isInit false, POST /init creates the root user,
// and /status and /login then accept it. It runs on an empty install
// (BootEmpty, or with -record a TS server started with serve-upstream.cjs
// --empty) and is recorded into contracts/shapes/init.json.
func TestJourneyInit(t *testing.T) {
	in := initInstall(t)
	c := &journeyCtx{vals: map[string]any{}, tokens: map[string]string{}}
	steps := journeyInitSteps()
	if *record {
		recordJourneyFile(t, initShapesFile, in, c, steps, nil)
		return
	}
	recorded := readJSON[struct {
		Steps map[string]recordedStep `json:"steps"`
	}](t, initShapesFile).Steps
	for _, st := range steps {
		t.Run(st.id, func(t *testing.T) {
			rec, ok := recorded[st.id]
			if !ok {
				t.Fatalf("no recorded shape for step %s in %s", st.id, initShapesFile)
			}
			checkJourneyStep(t, rec, runJourneyStep(t, in, c, st))
		})
	}
}

// initInstall is the empty install the init steps run against: BootEmpty, or
// with -record the empty TS server at -upstream.
func initInstall(t testing.TB) *Install {
	t.Helper()
	switch {
	case *record:
		if *upstream == "" || *upstreamRoot == "" {
			t.Fatal("-record needs -upstream and -upstream-root, as serve-upstream.cjs --empty prints them")
		}
		return upstreamInstall(t, *upstream, *upstreamRoot)
	case *upstream != "" || *upstreamRoot != "":
		t.Fatal("-upstream and -upstream-root need -record")
	}
	return BootEmpty(t)
}

// journeyInitSteps is the first-run journey, in order.
func journeyInitSteps() []journeyStep {
	root := map[string]any{"username": "trial-init-root", "password": "trial-init-password"}
	return []journeyStep{
		{id: "status.before", route: "/status"},
		{id: "init.create", method: "POST", route: "/init", body: map[string]any{"newRoot": root}},
		{id: "status.after", route: "/status"},
		{id: "init.again", method: "POST", route: "/init", body: map[string]any{"newRoot": root}},
		{id: "login.root", method: "POST", route: "/login", body: root, headers: map[string]string{"X-Return-Tokens": "true"}},
		{id: "login.wrong", method: "POST", route: "/login", body: map[string]any{"username": "trial-init-root", "password": "trial-init-nope"}},
	}
}
