//go:build oracle

package oracle

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
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type mockOIDCServer struct {
	srv   *httptest.Server
	key   *rsa.PrivateKey
	mu    sync.Mutex
	codes map[string][2]string // code -> [nonce, code_challenge]
}

func startMockOIDCProvider(t *testing.T) *mockOIDCServer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	m := &mockOIDCServer{key: key, codes: make(map[string][2]string)}
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v) // test mock response write
	}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeJSON(w, map[string]any{
				"issuer": m.srv.URL, "authorization_endpoint": m.srv.URL + "/authorize",
				"token_endpoint": m.srv.URL + "/token", "userinfo_endpoint": m.srv.URL + "/userinfo",
				"end_session_endpoint": m.srv.URL + "/logout", "jwks_uri": m.srv.URL + "/jwks",
				"id_token_signing_alg_values_supported": []string{"RS256"},
				"code_challenge_methods_supported":      []string{"S256"},
			})
		case "/jwks":
			writeJSON(w, map[string]any{"keys": []map[string]any{{
				"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "oracle-deep-oidc-key",
				"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
			}}})
		case "/token":
			if err := r.ParseForm(); err != nil {
				http.Error(w, "invalid form", http.StatusBadRequest)
				return
			}
			code := r.FormValue("code")
			m.mu.Lock()
			pair, ok := m.codes[code]
			m.mu.Unlock()
			if !ok {
				http.Error(w, "unknown code", http.StatusBadRequest)
				return
			}
			if pair[1] != "" {
				sum := sha256.Sum256([]byte(r.FormValue("code_verifier")))
				if base64.RawURLEncoding.EncodeToString(sum[:]) != pair[1] {
					http.Error(w, "pkce mismatch", http.StatusBadRequest)
					return
				}
			}
			idToken, err := m.signIDToken(pair[0])
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, map[string]any{"access_token": "oidc-access-" + code, "token_type": "Bearer", "id_token": idToken})
		case "/userinfo":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer oidc-access-") {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			writeJSON(w, map[string]any{
				"sub": "oidc-user-sub-1", "email": "oidc@example.test",
				"email_verified": true, "preferred_username": "oidc-alice", "groups": []string{"user"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockOIDCServer) registerCode(code, nonce, challenge string) {
	m.mu.Lock()
	m.codes[code] = [2]string{nonce, challenge}
	m.mu.Unlock()
}

func (m *mockOIDCServer) signIDToken(nonce string) (string, error) {
	hdr, err := json.Marshal(map[string]string{"alg": "RS256", "kid": "oracle-deep-oidc-key", "typ": "JWT"})
	if err != nil {
		return "", fmt.Errorf("marshal jwt header: %w", err)
	}
	now := time.Now()
	claims, err := json.Marshal(map[string]any{
		"iss": m.srv.URL, "sub": "oidc-user-sub-1", "aud": "oracle-oidc-client",
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "nonce": nonce,
		"email": "oidc@example.test", "email_verified": true,
		"preferred_username": "oidc-alice", "groups": []string{"user"},
	})
	if err != nil {
		return "", fmt.Errorf("marshal jwt claims: %w", err)
	}
	unsigned := base64.RawURLEncoding.EncodeToString(hdr) + "." + base64.RawURLEncoding.EncodeToString(claims)
	sum := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, m.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("sign id_token: %w", err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func cookieHeader(cookies []*http.Cookie) string {
	parts := make([]string, 0, len(cookies))
	for _, c := range cookies {
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; ")
}

// TestOracleDeepOIDCCallbackAndMobilePKCE exercises the full OIDC callback
// exchange, user auto-creation, group claim mapping, and mobile PKCE redirect.
func TestOracleDeepOIDCCallbackAndMobilePKCE(t *testing.T) {
	idp := startMockOIDCProvider(t)
	tw := StartTwin(t, Options{NoSockets: true})
	tsRoot, goRoot := tw.TS.Token(t, "root"), tw.Go.Token(t, "root")

	authSettings := map[string]any{
		"authActiveAuthMethods": []string{"local", "openid"}, "authOpenIDIssuerURL": idp.srv.URL,
		"authOpenIDAuthorizationURL": idp.srv.URL + "/authorize", "authOpenIDTokenURL": idp.srv.URL + "/token",
		"authOpenIDUserInfoURL": idp.srv.URL + "/userinfo", "authOpenIDJwksURL": idp.srv.URL + "/jwks",
		"authOpenIDClientID": "oracle-oidc-client", "authOpenIDClientSecret": "oracle-oidc-secret",
		"authOpenIDTokenSigningAlgorithm": "RS256", "authOpenIDAutoRegister": true,
		"authOpenIDMatchExistingBy": "email", "authOpenIDGroupClaim": "groups",
		"authOpenIDMobileRedirectURIs":       []string{"audiobookshelf://oauth"},
		"authOpenIDSubfolderForRedirectURLs": "/audiobookshelf",
	}
	assertNoUnexpected(t, tw, "oidc-deep-patch-settings", tw.Diff.DiffHTTP(
		"oidc-deep-patch-settings",
		tw.TS.Patch(t, tsRoot, "/api/auth-settings", authSettings),
		tw.Go.Patch(t, goRoot, "/api/auth-settings", authSettings),
	))

	tsAuth := tw.TS.Do(t, Request{Path: "/auth/openid", Query: url.Values{"callback": {"/audiobookshelf/"}}})
	goAuth := tw.Go.Do(t, Request{Path: "/auth/openid", Query: url.Values{"callback": {"/audiobookshelf/"}}})
	if tsAuth.Status != http.StatusFound || goAuth.Status != http.StatusFound {
		t.Fatalf("web authorize statuses: ts=%d go=%d, want 302", tsAuth.Status, goAuth.Status)
	}
	parseLoc := func(side string, resp Response) *url.URL {
		u, err := url.Parse(resp.Header.Get("Location"))
		if err != nil {
			t.Fatalf("%s parse Location %q: %v", side, resp.Header.Get("Location"), err)
		}
		return u
	}
	tsQ, goQ := parseLoc("ts", tsAuth).Query(), parseLoc("go", goAuth).Query()
	idp.registerCode("mock-code-ts", tsQ.Get("nonce"), tsQ.Get("code_challenge"))
	idp.registerCode("mock-code-go", goQ.Get("nonce"), goQ.Get("code_challenge"))

	tsCB := tw.TS.Do(t, Request{Path: "/auth/openid/callback", Query: url.Values{"code": {"mock-code-ts"}, "state": {tsQ.Get("state")}}, Header: map[string]string{"Cookie": cookieHeader(tsAuth.Cookies)}})
	goCB := tw.Go.Do(t, Request{Path: "/auth/openid/callback", Query: url.Values{"code": {"mock-code-go"}, "state": {goQ.Get("state")}}, Header: map[string]string{"Cookie": cookieHeader(goAuth.Cookies)}})
	if tsCB.Status != http.StatusFound || goCB.Status != http.StatusFound {
		t.Fatalf("web callback statuses: ts=%d (%s) go=%d (%s)", tsCB.Status, tsCB.Header.Get("Location"), goCB.Status, goCB.Header.Get("Location"))
	}
	extractToken := func(side string, resp Response) string {
		loc := parseLoc(side+"-cb", resp)
		tok := loc.Query().Get("accessToken")
		if loc.Path != "/audiobookshelf/" || tok == "" {
			t.Fatalf("%s unexpected callback redirect %q", side, loc.String())
		}
		return tok
	}
	tsMe := tw.TS.Get(t, extractToken("ts", tsCB), "/api/me")
	goMe := tw.Go.Get(t, extractToken("go", goCB), "/api/me")
	tsUserID, _ := tsMe.Object(t)["id"].(string)
	goUserID, _ := goMe.Object(t)["id"].(string)
	if tsUserID == "" || goUserID == "" {
		t.Fatalf("missing auto-created OIDC user id: ts=%s go=%s", excerpt(tsMe.Body), excerpt(goMe.Body))
	}
	tw.Diff.Corr.BindID(tsUserID, goUserID)
	assertNoUnexpected(t, tw, "oidc-web-me", tw.Diff.DiffHTTP("oidc-web-me", tsMe, goMe))
	if got := tsMe.Object(t)["username"]; got != "oidc-alice" {
		t.Fatalf("created OIDC username = %v, want oidc-alice", got)
	}
	assertDBEqual(t, tw, "oidc-web-users-db")

	sum := sha256.Sum256([]byte("oracle-mobile-pkce-verifier-0123456789-abcdef"))
	mobQuery := url.Values{
		"response_type": {"code"}, "redirect_uri": {"audiobookshelf://oauth"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}, "state": {"mobstate1"},
	}
	tsMob := tw.TS.Do(t, Request{Path: "/auth/openid", Query: mobQuery})
	goMob := tw.Go.Do(t, Request{Path: "/auth/openid", Query: mobQuery})
	if tsMob.Status != http.StatusFound || goMob.Status != http.StatusFound {
		t.Fatalf("mobile authorize statuses: ts=%d go=%d, want 302", tsMob.Status, goMob.Status)
	}
	tsMobRedir := tw.TS.Do(t, Request{Path: "/auth/openid/mobile-redirect", Query: url.Values{"code": {"mob-code-1"}, "state": {"mobstate1"}}})
	goMobRedir := tw.Go.Do(t, Request{Path: "/auth/openid/mobile-redirect", Query: url.Values{"code": {"mob-code-1"}, "state": {"mobstate1"}}})
	assertNoUnexpected(t, tw, "oidc-mobile-redirect", tw.Diff.DiffHTTP("oidc-mobile-redirect", tsMobRedir, goMobRedir))
	if want := "audiobookshelf://oauth?code=mob-code-1&state=mobstate1"; tsMobRedir.Header.Get("Location") != want {
		t.Fatalf("mobile redirect Location = %q, want %q", tsMobRedir.Header.Get("Location"), want)
	}
}

// TestOracleDeepBackupUploadAndApply exercises creating, downloading, deleting,
// multipart uploading, and applying a backup archive on both TS v2.37.2 and Go.
func TestOracleDeepBackupUploadAndApply(t *testing.T) {
	tw := StartTwin(t, Options{NoSockets: true})
	tsRoot, goRoot := tw.TS.Token(t, "root"), tw.Go.Token(t, "root")

	extractBackupID := func(side string, resp Response) string {
		arr, _ := resp.Object(t)["backups"].([]any)
		if len(arr) == 0 {
			t.Fatalf("%s backups empty: %s", side, excerpt(resp.Body))
		}
		first, _ := arr[0].(map[string]any)
		id, _ := first["id"].(string)
		if id == "" {
			t.Fatalf("%s backup missing id: %s", side, excerpt(resp.Body))
		}
		return id
	}
	tsCreate := tw.TS.Post(t, tsRoot, "/api/backups", nil)
	goCreate := tw.Go.Post(t, goRoot, "/api/backups", nil)
	tsBackupID, goBackupID := extractBackupID("ts", tsCreate), extractBackupID("go", goCreate)
	tw.Diff.Corr.BindID(tsBackupID, goBackupID)
	assertNoUnexpected(t, tw, "backup-create", tw.Diff.DiffHTTP("backup-create", tsCreate, goCreate))

	tsDL := tw.TS.Get(t, tsRoot, "/api/backups/"+tsBackupID+"/download")
	goDL := tw.Go.Get(t, goRoot, "/api/backups/"+goBackupID+"/download")
	if tsDL.Status != http.StatusOK || goDL.Status != http.StatusOK || len(tsDL.Body) == 0 || len(goDL.Body) == 0 {
		t.Fatalf("backup download failed: ts=(%d,%dB) go=(%d,%dB)", tsDL.Status, len(tsDL.Body), goDL.Status, len(goDL.Body))
	}
	assertNoUnexpected(t, tw, "backup-delete", tw.Diff.DiffHTTP("backup-delete", tw.TS.Delete(t, tsRoot, "/api/backups/"+tsBackupID), tw.Go.Delete(t, goRoot, "/api/backups/"+goBackupID)))

	tsBody, tsCT := Multipart(nil, File{Field: "file", Name: "backup.audiobookshelf", Content: tsDL.Body})
	goBody, goCT := Multipart(nil, File{Field: "file", Name: "backup.audiobookshelf", Content: goDL.Body})
	tsUpload := tw.TS.Do(t, Request{Method: http.MethodPost, Path: "/api/backups/upload", Token: tsRoot, Header: map[string]string{"Content-Type": tsCT}, Body: tsBody})
	goUpload := tw.Go.Do(t, Request{Method: http.MethodPost, Path: "/api/backups/upload", Token: goRoot, Header: map[string]string{"Content-Type": goCT}, Body: goBody})
	tsUploadedID, goUploadedID := extractBackupID("ts-upload", tsUpload), extractBackupID("go-upload", goUpload)
	tw.Diff.Corr.BindID(tsUploadedID, goUploadedID)
	assertNoUnexpected(t, tw, "backup-upload", tw.Diff.DiffHTTP("backup-upload", tsUpload, goUpload))

	assertNoUnexpected(t, tw, "backup-apply", tw.Diff.DiffHTTP("backup-apply", tw.TS.Get(t, tsRoot, "/api/backups/"+tsUploadedID+"/apply"), tw.Go.Get(t, goRoot, "/api/backups/"+goUploadedID+"/apply")))
	if err := tw.TS.WaitReady(readyTimeout); err != nil {
		t.Fatalf("ts not ready after backup apply: %v", err)
	}
	if err := tw.Go.WaitReady(readyTimeout); err != nil {
		t.Fatalf("go not ready after backup apply: %v", err)
	}
	assertNoUnexpected(t, tw, "backup-apply-me", tw.Diff.DiffHTTP("backup-apply-me", tw.TS.Get(t, tsRoot, "/api/me"), tw.Go.Get(t, goRoot, "/api/me")))
	assertNoUnexpected(t, tw, "backup-apply-libraries", tw.Diff.DiffHTTP("backup-apply-libraries", tw.TS.Get(t, tsRoot, "/api/libraries"), tw.Go.Get(t, goRoot, "/api/libraries")))
}

// TestOracleDeepPodcastOPMLAndNewFolderScan exercises scanning a new podcast
// folder on disk, OPML bulk podcast creation, and checking a feed for new episodes.
func TestOracleDeepPodcastOPMLAndNewFolderScan(t *testing.T) {
	mp3Bytes := generateSineMP3(t, filepath.Join(t.TempDir(), "ep01.mp3"), "Disk Podcast Ep 1", "Oracle Disk Podcast")
	tw := StartTwin(t, Options{})
	tsRoot, goRoot := tw.TS.Token(t, "root"), tw.Go.Token(t, "root")
	podLib, podFolder, podcastID := tw.TS.IDs["podcastLibrary"], tw.TS.IDs["podcastFolder1"], tw.TS.IDs["podcast"]

	for _, mediaRoot := range []string{tw.TS.Media, tw.Go.Media} {
		writeFileMkdir(t, filepath.Join(mediaRoot, "podcasts", "Oracle Disk Podcast", "ep01.mp3"), mp3Bytes)
	}
	tw.Diff.Corr.RegisterInodes(tw.TS.Media, tw.Go.Media, tw.TS.Metadata, tw.Go.Metadata)

	tsMark, goMark := tw.WatchTS.Socket("root").Mark(), tw.WatchGo.Socket("root").Mark()
	assertNoUnexpected(t, tw, "podcast-folder-scan", tw.Diff.DiffHTTP("podcast-folder-scan", tw.TS.Post(t, tsRoot, "/api/libraries/"+podLib+"/scan", nil), tw.Go.Post(t, goRoot, "/api/libraries/"+podLib+"/scan", nil)))
	matchPodLib := func(data map[string]any) bool { return data["libraryId"] == podLib }
	waitTaskFinished(t, tw.WatchTS.Socket("root"), tsMark, "library-scan", matchPodLib)
	waitTaskFinished(t, tw.WatchGo.Socket("root"), goMark, "library-scan", matchPodLib)
	tw.Diff.Corr.RegisterInodes(tw.TS.Media, tw.Go.Media, tw.TS.Metadata, tw.Go.Metadata)

	tw.Stubs.SetRoute("/ep.mp3", 200, "audio/mpeg", mp3Bytes)
	rssXML := `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd"><channel><title>Oracle OPML Podcast</title><description>OPML feed</description><itunes:author>OPML Host</itunes:author><item><title>OPML Episode 1</title><description>Fresh episode</description><pubDate>Thu, 01 Jan 2026 00:00:00 GMT</pubDate><guid isPermaLink="false">oracle-opml-ep-1</guid><enclosure url="` + tw.Stubs.HTTPURL + `/ep.mp3" length="4096" type="audio/mpeg"/></item></channel></rss>`
	tw.Stubs.SetRoute("/opml-pod.xml", 200, "application/xml", []byte(rssXML))

	tsOpmlMark, goOpmlMark := tw.WatchTS.Socket("root").Mark(), tw.WatchGo.Socket("root").Mark()
	opmlReq := map[string]any{"feeds": []string{tw.Stubs.HTTPURL + "/opml-pod.xml"}, "libraryId": podLib, "folderId": podFolder, "autoDownloadEpisodes": false}
	assertNoUnexpected(t, tw, "podcast-opml-create", tw.Diff.DiffHTTP("podcast-opml-create", tw.TS.Post(t, tsRoot, "/api/podcasts/opml/create", opmlReq), tw.Go.Post(t, goRoot, "/api/podcasts/opml/create", opmlReq)))
	waitTaskFinished(t, tw.WatchTS.Socket("root"), tsOpmlMark, "opml-import", nil)
	waitTaskFinished(t, tw.WatchGo.Socket("root"), goOpmlMark, "opml-import", nil)

	feedPatch := map[string]any{"metadata": map[string]any{"feedUrl": tw.Stubs.HTTPURL + "/opml-pod.xml"}}
	assertNoUnexpected(t, tw, "podcast-set-feed-url", tw.Diff.DiffHTTP("podcast-set-feed-url", tw.TS.Patch(t, tsRoot, "/api/items/"+podcastID+"/media", feedPatch), tw.Go.Patch(t, goRoot, "/api/items/"+podcastID+"/media", feedPatch)))

	tsEpMark, goEpMark := tw.WatchTS.Socket("root").Mark(), tw.WatchGo.Socket("root").Mark()
	assertNoUnexpected(t, tw, "podcast-checknew", tw.Diff.DiffHTTP("podcast-checknew", tw.TS.Get(t, tsRoot, "/api/podcasts/"+podcastID+"/checknew"), tw.Go.Get(t, goRoot, "/api/podcasts/"+podcastID+"/checknew")))
	waitEp := func(sock *Socket, from int) {
		sock.Wait(t, from, "episode_download_finished", complexTaskTimeout, func(args []json.RawMessage) bool {
			var ev map[string]any
			return len(args) > 0 && json.Unmarshal(args[0], &ev) == nil && ev["libraryItemId"] == podcastID && ev["isFinished"] == true
		})
	}
	waitEp(tw.WatchTS.Socket("root"), tsEpMark)
	waitEp(tw.WatchGo.Socket("root"), goEpMark)
}

// TestOracleDeepScanAuthorSeriesCleanupAndCancel exercises clearing authors and
// series on a book item, triggering watcher scanFilesChanged, and canceling a
// running library scan over Socket.IO.
func TestOracleDeepScanAuthorSeriesCleanupAndCancel(t *testing.T) {
	mp3Bytes := generateSineMP3(t, filepath.Join(t.TempDir(), "Oracle Watcher Book.mp3"), "Oracle Watcher Book", "Oracle Watcher Author")
	tw := StartTwin(t, Options{})
	tsRoot, goRoot := tw.TS.Token(t, "root"), tw.Go.Token(t, "root")
	bookLib, saga2 := tw.TS.IDs["library"], tw.TS.IDs["saga2"]

	clearBody := map[string]any{"metadata": map[string]any{"authors": []any{}, "series": []any{}}}
	assertNoUnexpected(t, tw, "saga2-clear-authors-series", tw.Diff.DiffHTTP("saga2-clear-authors-series", tw.TS.Patch(t, tsRoot, "/api/items/"+saga2+"/media", clearBody), tw.Go.Patch(t, goRoot, "/api/items/"+saga2+"/media", clearBody)))
	assertDBEqual(t, tw, "saga2-clear-authors-series-db")

	tsRel := filepath.Join(tw.TS.Media, "audiobooks", "Oracle Watcher Book", "Oracle Watcher Book.mp3")
	goRel := filepath.Join(tw.Go.Media, "audiobooks", "Oracle Watcher Book", "Oracle Watcher Book.mp3")
	writeFileMkdir(t, tsRel, mp3Bytes)
	writeFileMkdir(t, goRel, mp3Bytes)
	tw.Diff.Corr.RegisterInodes(tw.TS.Media, tw.Go.Media, tw.TS.Metadata, tw.Go.Metadata)

	tsWatchMark, goWatchMark := tw.WatchTS.Socket("root").Mark(), tw.WatchGo.Socket("root").Mark()
	assertNoUnexpected(t, tw, "watcher-update-add", tw.Diff.DiffHTTP(
		"watcher-update-add",
		tw.TS.Post(t, tsRoot, "/api/watcher/update", map[string]any{"libraryId": bookLib, "path": tsRel, "type": "add"}),
		tw.Go.Post(t, goRoot, "/api/watcher/update", map[string]any{"libraryId": bookLib, "path": goRel, "type": "add"}),
	))
	waitTaskFinished(t, tw.WatchTS.Socket("root"), tsWatchMark, "watcher-scan", nil)
	waitTaskFinished(t, tw.WatchGo.Socket("root"), goWatchMark, "watcher-scan", nil)

	tsScanMark, goScanMark := tw.WatchTS.Socket("root").Mark(), tw.WatchGo.Socket("root").Mark()
	assertNoUnexpected(t, tw, "scan-before-cancel", tw.Diff.DiffHTTP("scan-before-cancel", tw.TS.Post(t, tsRoot, "/api/libraries/"+bookLib+"/scan?force=1", nil), tw.Go.Post(t, goRoot, "/api/libraries/"+bookLib+"/scan?force=1", nil)))
	tw.WatchTS.Socket("root").Emit(t, "cancel_scan", bookLib)
	tw.WatchGo.Socket("root").Emit(t, "cancel_scan", bookLib)
	matchBookLib := func(data map[string]any) bool { return data["libraryId"] == bookLib }
	waitTaskFinished(t, tw.WatchTS.Socket("root"), tsScanMark, "library-scan", matchBookLib)
	waitTaskFinished(t, tw.WatchGo.Socket("root"), goScanMark, "library-scan", matchBookLib)
}
