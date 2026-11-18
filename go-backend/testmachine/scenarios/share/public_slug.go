package share

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// publicSlugScenarios covers route-r220: GET /public/share/:slug creates or
// reuses the cookie-bound share playback session.
func publicSlugScenarios() []harness.Scenario {
	const req = "route-r220"
	const inv = "INV-R220-CONTRACT-001"
	return []harness.Scenario{
		{
			Requirement: req, Invariant: inv, Case: "normal", State: "initial", Assertion: "public-slug-creates-cookie-session",
			Sources: []string{"server/controllers/ShareController.js:35-136", "server/objects/PlaybackSession.js:59-116", "server/models/MediaItemShare.js:62-74"},
			Claim:   "GET /public/share/trial-share-novel returns the fixture share with no secret fields, a 30-day share_session_id cookie and a new web-share playback session whose tracks, chapters, duration and device binding are the fixture novel's.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				sessionsBefore := e.Count(t, "SELECT COUNT(*) FROM playbackSessions")
				resp := publicGet(t, e, novelSlug, nil, "", nil)
				wantStatus(t, resp, http.StatusOK, "public novel slug")

				var body map[string]any
				resp.JSON(t, &body)
				for _, secret := range []string{"pash", "userId", "extraData"} {
					if _, present := body[secret]; present {
						t.Fatalf("public share must not expose %q: %v", secret, body)
					}
				}
				value := decodeShare(t, resp)
				if value.ID != "d4441526-7cbf-4d91-8683-55259880556a" || value.MediaItemID != "85ab0f32-b17f-4c9a-883e-a61e260f1e46" || value.MediaItemType != "book" || value.Slug != novelSlug {
					t.Fatalf("fixture share identity wrong: %s", summary(value))
				}
				if value.ExpiresAt != nil || value.IsDownloadable == nil || !*value.IsDownloadable {
					t.Fatalf("fixture share must be permanent and downloadable: %s", summary(value))
				}
				if value.CreatedAt == "" || value.CreatedAt != value.UpdatedAt {
					t.Fatalf("fixture share timestamps wrong: created %q updated %q", value.CreatedAt, value.UpdatedAt)
				}

				cookie := shareCookie(t, resp)
				if !uuidV4.MatchString(cookie) {
					t.Fatalf("share_session_id cookie %q is not a UUID v4", cookie)
				}
				if len(resp.Cookies) != 1 || resp.Cookies[0].MaxAge != 60*60*24*30 || !resp.Cookies[0].HttpOnly || resp.Cookies[0].Path != "/" {
					t.Fatalf("share cookie attributes wrong: %+v", resp.Cookies)
				}

				session := value.PlaybackSession
				if !uuidV4.MatchString(session.ID) || session.UserID != nil || session.LibraryID != fx.library || session.LibraryItemID != fx.novel || session.BookID == nil || *session.BookID != "85ab0f32-b17f-4c9a-883e-a61e260f1e46" || session.EpisodeID != nil || session.MediaType != "book" {
					t.Fatalf("session identity wrong: %+v", session)
				}
				if session.DisplayTitle != "Single File Novel" || session.DisplayAuthor != "Ada Example" || session.Duration != 45 || session.CoverPath == nil || *session.CoverPath == "" {
					t.Fatalf("session media fields wrong: %+v", session)
				}
				if session.MediaMetadata.Title != "Single File Novel" || len(session.MediaMetadata.Authors) != 1 || session.MediaMetadata.Authors[0].Name != "Ada Example" || len(session.MediaMetadata.Series) != 0 || len(session.MediaMetadata.Genres) != 1 || session.MediaMetadata.Genres[0] != "Fiction" || session.MediaMetadata.PublishedYear == nil || *session.MediaMetadata.PublishedYear != "2021" {
					t.Fatalf("session mediaMetadata wrong: %+v", session.MediaMetadata)
				}
				if len(session.Chapters) != 3 || session.Chapters[0].Title != "Opening" || session.Chapters[1].Start != 15 || session.Chapters[2].End != 45 {
					t.Fatalf("session chapters wrong: %+v", session.Chapters)
				}
				if session.PlayMethod != 0 || session.MediaPlayer != "web-share" || session.CoverAspectRatio != 1 || session.ServerVersion == "" {
					t.Fatalf("session playback fields wrong: playMethod %d mediaPlayer %q aspect %v version %q", session.PlayMethod, session.MediaPlayer, session.CoverAspectRatio, session.ServerVersion)
				}
				if session.DeviceInfo.DeviceID != cookie || session.DeviceInfo.ClientName != "Abs Web Share" || session.DeviceInfo.ID == "" {
					t.Fatalf("session device must be bound to the cookie: %+v (cookie %s)", session.DeviceInfo, cookie)
				}
				if session.StartTime != 0 || session.CurrentTime != 0 || session.TimeListening != 0 || session.LibraryItem != nil {
					t.Fatalf("new session must start at zero with no expanded library item: %+v", session)
				}
				now := time.Now().UnixMilli()
				if session.StartedAt < now-10_000 || session.StartedAt > now+10_000 || session.UpdatedAt != session.StartedAt {
					t.Fatalf("session startedAt/updatedAt must be the request time: %d/%d (now %d)", session.StartedAt, session.UpdatedAt, now)
				}
				if session.Date != time.Now().UTC().Format("2006-01-02") || session.DayOfWeek == "" {
					t.Fatalf("session date/dayOfWeek wrong: %q %q", session.Date, session.DayOfWeek)
				}
				if len(session.AudioTracks) != 1 {
					t.Fatalf("novel session must expose one audio track: %+v", session.AudioTracks)
				}
				track := session.AudioTracks[0]
				if track.Index != 1 || track.StartOffset != 0 || track.Duration != 45 || track.Title != "Single File Novel.m4b" || track.ContentURL != "/audiobookshelf/public/share/"+novelSlug+"/track/1" || track.MimeType != "audio/mp4" || track.Codec == nil || *track.Codec != "aac" {
					t.Fatalf("audio track wrong: %+v", track)
				}
				if track.Metadata.Filename != "Single File Novel.m4b" || track.Metadata.Path == "" {
					t.Fatalf("audio track metadata wrong: %+v", track.Metadata)
				}
				if got := e.Count(t, "SELECT COUNT(*) FROM playbackSessions"); got != sessionsBefore {
					t.Fatalf("a share session must not persist a playbackSessions row: %d -> %d", sessionsBefore, got)
				}
				devices := e.Rows(t, "SELECT clientName, userId, ipAddress FROM devices WHERE deviceId = ?", cookie)
				if len(devices) != 1 || devices[0]["clientName"] != "Abs Web Share" || devices[0]["userId"] != nil {
					t.Fatalf("share device row wrong: %v", devices)
				}
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "edge", State: "repeat", Assertion: "public-slug-reuses-session-and-time-query",
			Sources: []string{"server/controllers/ShareController.js:46-70", "server/controllers/ShareController.js:107-127"},
			Claim:   "A second GET with the share cookie reuses the same playback session without a new cookie, and ?t overrides currentTime only inside the duration while the existing session keeps its last position for out-of-range or non-numeric t.",
			Run: func(t *testing.T, e *harness.Env) {
				first, cookie := openPublicSession(t, e, novelSlug, nil, "")
				sessionID := first.PlaybackSession.ID
				around := time.Now().UnixMilli()
				resp := publicGet(t, e, novelSlug, nil, cookie, nil)
				wantStatus(t, resp, http.StatusOK, "repeat slug read")
				if got := shareCookie(t, resp); got != "" {
					t.Fatalf("an existing session must not set a new cookie, got %q", got)
				}
				reused := decodeShare(t, resp).PlaybackSession
				if reused.ID != sessionID || reused.DeviceInfo.DeviceID != cookie {
					t.Fatalf("repeat must reuse session %s, got %+v", sessionID, reused)
				}
				if reused.UpdatedAt < around-1000 {
					t.Fatalf("existing session updatedAt moved backwards: %d < %d", reused.UpdatedAt, around)
				}

				seek := publicGet(t, e, novelSlug, url.Values{"t": {"30"}}, cookie, nil)
				wantStatus(t, seek, http.StatusOK, "seek to 30")
				seeked := decodeShare(t, seek).PlaybackSession
				if seeked.ID != sessionID || seeked.CurrentTime != 30 || seeked.StartTime != 0 || seeked.Duration != 45 {
					t.Fatalf("?t=30 must override currentTime of the existing session: %+v", seeked)
				}
				beyond := publicGet(t, e, novelSlug, url.Values{"t": {"99"}}, cookie, nil)
				wantStatus(t, beyond, http.StatusOK, "seek beyond duration")
				if got := decodeShare(t, beyond).PlaybackSession.CurrentTime; got != 30 {
					t.Fatalf("?t beyond the duration must not move the existing session, currentTime = %v", got)
				}
				text := publicGet(t, e, novelSlug, url.Values{"t": {"abc"}}, cookie, nil)
				wantStatus(t, text, http.StatusOK, "non-numeric t")
				if got := decodeShare(t, text).PlaybackSession.CurrentTime; got != 30 {
					t.Fatalf("non-numeric ?t must not move the session, currentTime = %v", got)
				}

				fresh := publicGet(t, e, novelSlug, url.Values{"t": {"99"}}, "", nil)
				wantStatus(t, fresh, http.StatusOK, "new session beyond duration")
				if got := decodeShare(t, fresh).PlaybackSession; got.StartTime != 0 || got.CurrentTime != 0 {
					t.Fatalf("a new session with ?t beyond the duration starts at zero: %+v", got)
				}
				exact := publicGet(t, e, novelSlug, url.Values{"t": {"45"}}, "", nil)
				wantStatus(t, exact, http.StatusOK, "new session at exact duration")
				if got := decodeShare(t, exact).PlaybackSession; got.StartTime != 45 || got.CurrentTime != 45 {
					t.Fatalf("a new session at exactly the duration starts at %v", got.StartTime)
				}
				negative := publicGet(t, e, novelSlug, url.Values{"t": {"-5"}}, "", nil)
				wantStatus(t, negative, http.StatusOK, "negative t")
				if got := decodeShare(t, negative).PlaybackSession.StartTime; got != 0 {
					t.Fatalf("negative ?t must clamp to zero, startTime = %v", got)
				}
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "normal", State: "changed", Assertion: "public-slug-switches-session-between-shares",
			Sources: []string{"server/controllers/ShareController.js:48-68", "server/managers/ShareManager.js:29-47"},
			Claim:   "A cookie bound to one share that opens another share belongs to the new session only: the previous session is closed (its track route denies) and returning to the first share creates a new session on the same cookie.",
			Run: func(t *testing.T, e *harness.Env) {
				first, cookie := openPublicSession(t, e, novelSlug, nil, "")
				firstSession := first.PlaybackSession.ID
				second, _ := openPublicSession(t, e, saga2Slug, url.Values{}, cookie)
				if second.Slug != saga2Slug || second.PlaybackSession.ID == firstSession || second.PlaybackSession.DeviceInfo.DeviceID != cookie {
					t.Fatalf("switching share must create a new session on the same cookie: %+v", second.PlaybackSession)
				}
				if second.PlaybackSession.DisplayTitle != "The Middle" || second.PlaybackSession.Duration != 30 || second.PlaybackSession.MediaPlayer != "web-share" {
					t.Fatalf("saga2 session wrong: %+v", second.PlaybackSession)
				}
				denied := e.Do(t, harness.Request{Path: publicSharePath(novelSlug) + "/track/1", Header: cookieHeader(cookie)})
				wantStatus(t, denied, http.StatusNotFound, "track of the closed session")
				back, _ := openPublicSession(t, e, novelSlug, nil, cookie)
				if back.PlaybackSession.ID == second.PlaybackSession.ID || back.PlaybackSession.ID == firstSession {
					t.Fatalf("returning to the novel share must create another session: %+v", back.PlaybackSession)
				}
				if back.PlaybackSession.CurrentTime != 0 || back.PlaybackSession.DeviceInfo.DeviceID != cookie {
					t.Fatalf("recreated novel session must start at zero on the same device: %+v", back.PlaybackSession)
				}
				track := e.Do(t, harness.Request{Path: publicSharePath(novelSlug) + "/track/1", Header: cookieHeader(cookie)})
				wantStatus(t, track, http.StatusOK, "track after returning to the novel share")
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "error", State: "failure", Assertion: "public-slug-unknown-invalid-cookie-and-expired",
			Sources: []string{"server/controllers/ShareController.js:39-45", "server/controllers/ShareController.js:70-76", "server/managers/ShareManager.js:114-133"},
			Claim:   "An unknown slug returns 404 without a cookie, a malformed cookie is cleared and then reused verbatim as the session id, and a share past its expiry is removed and returns 404 without creating a session.",
			Run: func(t *testing.T, e *harness.Env) {
				missing := publicGet(t, e, "no-such-share", nil, "", nil)
				wantStatus(t, missing, http.StatusNotFound, "unknown slug")
				if got := shareCookie(t, missing); got != "" {
					t.Fatalf("unknown slug must not issue a cookie, got %q", got)
				}
				invalid := publicGet(t, e, novelSlug, nil, "not-a-uuid", nil)
				wantStatus(t, invalid, http.StatusOK, "malformed cookie")
				value := decodeShare(t, invalid)
				cookie := shareCookie(t, invalid)
				// Upstream clears the malformed cookie and then reuses the request
				// cookie value as the new session id, so the device keeps it verbatim.
				if cookie != "not-a-uuid" || value.PlaybackSession.DeviceInfo.DeviceID != cookie {
					t.Fatalf("malformed cookie is reused as the session id: cookie %q device %q", cookie, value.PlaybackSession.DeviceInfo.DeviceID)
				}
				if len(invalid.Cookies) != 2 || invalid.Cookies[0].Value != "" || invalid.Cookies[1].Value != "not-a-uuid" {
					t.Fatalf("malformed cookie must be cleared and re-set: %+v", invalid.Cookies)
				}

				expires := time.Now().Add(3000 * time.Millisecond).UnixMilli()
				created := mustCreateShare(t, e, "share-slug-expiry", mediaIDForItem(t, e, fixtureOf(e).disposable), expires, true)
				session, _ := openPublicSession(t, e, created.Slug, nil, "")
				if session.PlaybackSession.ID == "" {
					t.Fatal("expiring share must serve a session before it expires")
				}
				harness.Eventually(t, 15*time.Second, "expiring share to be removed", func() bool {
					return len(shareRows(t, e, created.ID)) == 0
				})
				gone := publicGet(t, e, created.Slug, nil, "", nil)
				wantStatus(t, gone, http.StatusNotFound, "expired share")
				if got := shareCookie(t, gone); got != "" {
					t.Fatalf("expired share must not issue a cookie, got %q", got)
				}
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "edge", State: "initial", Assertion: "public-slug-podcast-share-has-no-library-item",
			Sources: []string{"server/controllers/ShareController.js:78-82", "server/models/MediaItemShare.js:79-93"},
			Claim:   "A podcastEpisode share can be created but its public slug returns 404 because the share lookup only resolves book library items, and the failed read creates no session or cookie.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				resp := e.Post(t, e.Token(t, "root"), "/api/share/mediaitem", map[string]any{
					"slug": "share-episode", "mediaItemType": "podcastEpisode", "mediaItemId": fx.ep1, "expiresAt": 0, "isDownloadable": false,
				})
				wantStatus(t, resp, http.StatusCreated, "create episode share")
				created := decodeShare(t, resp)
				if created.MediaItemType != "podcastEpisode" || created.MediaItemID != fx.ep1 || created.Slug != "share-episode" || created.ExpiresAt != nil || created.IsDownloadable == nil || *created.IsDownloadable {
					t.Fatalf("episode share wrong: %s", summary(created))
				}
				wantShareRow(t, e, created, rootUserID(t, e))
				missing := publicGet(t, e, "share-episode", nil, "", nil)
				wantStatus(t, missing, http.StatusNotFound, "episode share has no book library item")
				if got := shareCookie(t, missing); got != "" {
					t.Fatalf("failed episode read must not issue a cookie, got %q", got)
				}
				wantStatus(t, removeShare(t, e, created.ID), http.StatusNoContent, "delete episode share")
				noShareRow(t, e, created.ID)
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "normal", State: "changed", Assertion: "public-slug-reloads-persisted-shares",
			Sources: []string{"server/managers/ShareManager.js:90-110", "server/managers/ShareManager.js:114-133"},
			Claim:   "A timed share is reopened and scheduled when the server restarts, and a share whose expiresAt passed while the server was down is deleted on load with its slug answering 404.",
			Run: func(t *testing.T, e *harness.Env) {
				created := mustCreateShare(t, e, "share-slug-restart", mediaIDForItem(t, e, fixtureOf(e).disposable), time.Now().Add(10*time.Minute).UnixMilli(), true)
				e.Restart(t)
				reopened, _ := openPublicSession(t, e, created.Slug, nil, "")
				if reopened.ID != created.ID {
					t.Fatalf("restart must reopen share %s, got %s", created.ID, reopened.ID)
				}
				e.Stop(t)
				if changed := e.Exec(t, "UPDATE mediaItemShares SET expiresAt = ? WHERE id = ?", "2020-01-01 00:00:00.000 +00:00", created.ID); changed != 1 {
					t.Fatalf("prepare expired row: %d rows changed", changed)
				}
				e.Restart(t)
				harness.Eventually(t, 10*time.Second, "expired row removal on load", func() bool {
					return len(shareRows(t, e, created.ID)) == 0
				})
				wantStatus(t, publicGet(t, e, created.Slug, nil, "", nil), http.StatusNotFound, "expired-on-load slug")
			},
		},
	}
}
