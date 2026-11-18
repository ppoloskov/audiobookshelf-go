package share

import (
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// publicSlugShares lists the audio track titles the public session exposes.
func publicSlugShares(t *testing.T, e *harness.Env, slug string) mediaItemShare {
	t.Helper()
	value, _ := openPublicSession(t, e, slug, nil, "")
	return value
}

// createScenarios covers route-r195: POST /api/share/mediaitem.
func createScenarios() []harness.Scenario {
	const req = "route-r195"
	const inv = "INV-R195-CONTRACT-001"
	return []harness.Scenario{
		{
			Requirement: req, Invariant: inv, Case: "normal", State: "initial", Assertion: "create-share-persists-and-serves",
			Sources: []string{"server/controllers/ShareController.js:319-380", "server/managers/ShareManager.js:136-147", "server/models/MediaItemShare.js:62-74"},
			Claim:   "POST /api/share/mediaitem as admin answers 201 with the client share fields (no expiry, downloadable), persists one owned row with NULL pash/extraData, and the returned slug immediately serves a public session with the item's tracks.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				mediaID := mediaIDForItem(t, e, fx.saga1)
				before := e.Count(t, "SELECT COUNT(*) FROM mediaItemShares")
				started := time.Now().UnixMilli()
				resp := createShare(t, e, e.Token(t, "root"), "share-slug-create", mediaID, 0, true)
				wantStatus(t, resp, http.StatusCreated, "create share")
				value := decodeShare(t, resp)
				if !uuidV4.MatchString(value.ID) || value.MediaItemID != mediaID || value.MediaItemType != "book" || value.Slug != "share-slug-create" {
					t.Fatalf("created share identity wrong: %s", summary(value))
				}
				if value.ExpiresAt != nil || value.IsDownloadable == nil || !*value.IsDownloadable {
					t.Fatalf("created share flags wrong: %s", summary(value))
				}
				created, err := time.Parse(time.RFC3339Nano, value.CreatedAt)
				if err != nil || created.UnixMilli() < started-1000 || created.UnixMilli() > time.Now().UnixMilli()+1000 || value.CreatedAt != value.UpdatedAt {
					t.Fatalf("created share timestamps wrong: created %q updated %q", value.CreatedAt, value.UpdatedAt)
				}
				wantShareRow(t, e, value, rootUserID(t, e))
				if got := e.Count(t, "SELECT COUNT(*) FROM mediaItemShares"); got != before+1 {
					t.Fatalf("mediaItemShares rows = %d, want %d", got, before+1)
				}

				public := publicSlugShares(t, e, value.Slug)
				session := public.PlaybackSession
				if session.DisplayTitle != "The Beginning" || session.DisplayAuthor != "Terry Fixture" || session.Duration != 60.186123 || session.MediaPlayer != "web-share" {
					t.Fatalf("public session of the created share wrong: %+v", session)
				}
				if len(session.AudioTracks) != 3 || session.AudioTracks[0].StartOffset != 0 || session.AudioTracks[1].StartOffset != 20.062041 || session.AudioTracks[2].StartOffset != 40.124082 {
					t.Fatalf("created share tracks wrong: %+v", session.AudioTracks)
				}
				if session.MediaMetadata.Title != "The Beginning" || len(session.MediaMetadata.Series) != 1 || session.MediaMetadata.Series[0].Name != "Long Saga" || session.MediaMetadata.Series[0].Sequence != "1" {
					t.Fatalf("created share mediaMetadata wrong: %+v", session.MediaMetadata)
				}
				wantStatus(t, removeShare(t, e, value.ID), http.StatusNoContent, "cleanup created share")
				noShareRow(t, e, value.ID)
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "edge", State: "repeat", Assertion: "create-share-expiry-and-duplicate",
			Sources: []string{"server/controllers/ShareController.js:334-356", "server/managers/ShareManager.js:114-133"},
			Claim:   "A timed share stores the exact requested expiry and a disabled download flag, a repeated create for the same item answers 409 without a second row, and an omitted isDownloadable is stored as NULL.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				expires := time.Now().Add(10 * time.Minute).UnixMilli()
				resp := createShare(t, e, e.Token(t, "root"), "share-slug-expiring", mediaIDForItem(t, e, fx.ebook), expires, false)
				wantStatus(t, resp, http.StatusCreated, "create timed share")
				timed := decodeShare(t, resp)
				if timed.ExpiresAt == nil || timed.IsDownloadable == nil || *timed.IsDownloadable {
					t.Fatalf("timed share flags wrong: %s", summary(timed))
				}
				parsed, err := time.Parse(time.RFC3339Nano, *timed.ExpiresAt)
				if err != nil || parsed.UnixMilli() != expires {
					t.Fatalf("expiresAt = %q, want %d", *timed.ExpiresAt, expires)
				}
				wantShareRow(t, e, timed, rootUserID(t, e))
				rows := e.Count(t, "SELECT COUNT(*) FROM mediaItemShares")
				repeat := createShare(t, e, e.Token(t, "root"), "share-slug-expiring-again", mediaIDForItem(t, e, fx.ebook), expires, false)
				wantStatus(t, repeat, http.StatusConflict, "repeat create for the same item")
				if got := e.Count(t, "SELECT COUNT(*) FROM mediaItemShares"); got != rows {
					t.Fatalf("rejected repeat changed the row count: %d -> %d", rows, got)
				}
				if found := e.Rows(t, "SELECT id FROM mediaItemShares WHERE slug = ?", "share-slug-expiring-again"); len(found) != 0 {
					t.Fatalf("rejected repeat persisted a share: %v", found)
				}

				episode := e.Post(t, e.Token(t, "root"), "/api/share/mediaitem", map[string]any{
					"slug": "share-slug-null-download", "mediaItemType": "podcastEpisode", "mediaItemId": fx.ep2, "expiresAt": 0,
				})
				wantStatus(t, episode, http.StatusCreated, "create share without isDownloadable")
				open := decodeShare(t, episode)
				if open.IsDownloadable != nil || open.ExpiresAt != nil {
					t.Fatalf("omitted isDownloadable must be NULL: %s", summary(open))
				}
				wantShareRow(t, e, open, rootUserID(t, e))
				wantStatus(t, removeShare(t, e, timed.ID), http.StatusNoContent, "delete timed share")
				wantStatus(t, removeShare(t, e, open.ID), http.StatusNoContent, "delete null-download share")
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "edge", State: "failure", Assertion: "create-share-past-expiry-is-destroyed",
			Sources: []string{"server/managers/ShareManager.js:114-133", "server/controllers/ShareController.js:366-372"},
			Claim:   "A positive expiresAt that is already in the past is accepted with 201 and a share_open, but the share is never opened, its row is removed and the public slug stays 404 without a session.",
			Run: func(t *testing.T, e *harness.Env) {
				admin := e.Socket(t, e.Token(t, "root"))
				mark := admin.Mark()
				expires := time.Now().Add(-time.Second).UnixMilli()
				created := mustCreateShare(t, e, "share-slug-past-expiry", mediaIDForItem(t, e, fixtureOf(e).disposable), expires, true)
				if created.ExpiresAt == nil {
					t.Fatalf("past expiry share must carry expiresAt: %s", summary(created))
				}
				admin.Wait(t, mark, "share_open", eventWait, matchShareID(created.ID))
				harness.Eventually(t, 10*time.Second, "past-expiry share row removal", func() bool {
					return len(shareRows(t, e, created.ID)) == 0
				})
				resp := publicGet(t, e, created.Slug, nil, "", nil)
				wantStatus(t, resp, http.StatusNotFound, "past expiry public slug")
				if got := shareCookie(t, resp); got != "" {
					t.Fatalf("past-expiry share must not open a session, cookie %q", got)
				}
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "normal", State: "changed", Assertion: "create-share-visible-in-item-detail",
			Sources: []string{"server/controllers/ShareController.js:365-372", "server/controllers/LibraryItemController.js:85-89", "server/managers/ShareManager.js:49-63"},
			Claim:   "After an admin creates a share, the admin's expanded item detail with include=share exposes the same client share, a non-admin receives no mediaItemShare, and after deletion the admin's detail has mediaItemShare null.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				created := mustCreateShare(t, e, "share-slug-detail", mediaIDForItem(t, e, fx.disposable), 0, true)
				admin, present := itemShareFromDetail(t, e, e.Token(t, "root"), fx.disposable)
				if !present || admin == nil {
					t.Fatalf("admin item detail must expose mediaItemShare: present=%v value=%v", present, admin)
				}
				if admin["id"] != created.ID || admin["slug"] != created.Slug || admin["mediaItemId"] != created.MediaItemID || admin["isDownloadable"] != true {
					t.Fatalf("item detail share = %v, want %s", admin, summary(created))
				}
				if _, leaked := admin["userId"]; leaked {
					t.Fatalf("item detail share must not expose userId: %v", admin)
				}
				if listener, present := itemShareFromDetail(t, e, e.Token(t, "listener"), fx.disposable); present {
					t.Fatalf("non-admin item detail must not include mediaItemShare: %v", listener)
				}
				wantStatus(t, removeShare(t, e, created.ID), http.StatusNoContent, "delete detail share")
				if gone, present := itemShareFromDetail(t, e, e.Token(t, "root"), fx.disposable); !present || gone != nil {
					t.Fatalf("admin item detail must expose mediaItemShare null after deletion: present=%v value=%v", present, gone)
				}
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "error", State: "initial", Assertion: "create-share-rejects-invalid-input",
			Sources: []string{"server/controllers/ShareController.js:320-352"},
			Claim:   "Share creation rejects non-admin callers with 403 and unauthenticated callers with 401, invalid required fields and expiry with 400, an unknown media item with 404, and a used slug or item with 409, without persisting anything.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				sagaMedia := mediaIDForItem(t, e, fx.saga1)
				novelMedia := mediaIDForItem(t, e, fx.novel)
				before := e.Count(t, "SELECT COUNT(*) FROM mediaItemShares")
				unauthenticated := e.Do(t, harness.Request{Method: http.MethodPost, Path: "/api/share/mediaitem", JSON: map[string]any{
					"slug": "share-slug-auth", "mediaItemType": "book", "mediaItemId": sagaMedia, "expiresAt": 0, "isDownloadable": true,
				}})
				wantStatus(t, unauthenticated, http.StatusUnauthorized, "unauthenticated create")
				listener := createShare(t, e, e.Token(t, "listener"), "share-slug-auth", sagaMedia, 0, true)
				wantStatus(t, listener, http.StatusForbidden, "non-admin create")

				cases := []struct {
					name   string
					body   map[string]any
					status int
				}{
					{"missing slug", map[string]any{"mediaItemType": "book", "mediaItemId": sagaMedia, "expiresAt": 0}, http.StatusBadRequest},
					{"blank slug", map[string]any{"slug": "   ", "mediaItemType": "book", "mediaItemId": sagaMedia, "expiresAt": 0}, http.StatusBadRequest},
					{"numeric slug", map[string]any{"slug": 7, "mediaItemType": "book", "mediaItemId": sagaMedia, "expiresAt": 0}, http.StatusBadRequest},
					{"missing type", map[string]any{"slug": "share-slug-bad", "mediaItemId": sagaMedia, "expiresAt": 0}, http.StatusBadRequest},
					{"invalid type", map[string]any{"slug": "share-slug-bad", "mediaItemType": "author", "mediaItemId": sagaMedia, "expiresAt": 0}, http.StatusBadRequest},
					{"missing media", map[string]any{"slug": "share-slug-bad", "mediaItemType": "book", "expiresAt": 0}, http.StatusBadRequest},
					{"numeric media", map[string]any{"slug": "share-slug-bad", "mediaItemType": "book", "mediaItemId": 7, "expiresAt": 0}, http.StatusBadRequest},
					{"missing expiry", map[string]any{"slug": "share-slug-bad", "mediaItemType": "book", "mediaItemId": sagaMedia}, http.StatusBadRequest},
					{"null expiry", map[string]any{"slug": "share-slug-bad", "mediaItemType": "book", "mediaItemId": sagaMedia, "expiresAt": nil}, http.StatusBadRequest},
					{"negative expiry", map[string]any{"slug": "share-slug-bad", "mediaItemType": "book", "mediaItemId": sagaMedia, "expiresAt": -1}, http.StatusBadRequest},
					{"text expiry", map[string]any{"slug": "share-slug-bad", "mediaItemType": "book", "mediaItemId": sagaMedia, "expiresAt": "soon"}, http.StatusBadRequest},
					{"unknown media", map[string]any{"slug": "share-slug-bad", "mediaItemType": "book", "mediaItemId": "00000000-0000-4000-8000-000000000000", "expiresAt": 0}, http.StatusNotFound},
					{"used slug", map[string]any{"slug": novelSlug, "mediaItemType": "book", "mediaItemId": sagaMedia, "expiresAt": 0}, http.StatusConflict},
					{"used media", map[string]any{"slug": "share-slug-bad", "mediaItemType": "book", "mediaItemId": novelMedia, "expiresAt": 0}, http.StatusConflict},
				}
				for _, test := range cases {
					resp := e.Post(t, e.Token(t, "root"), "/api/share/mediaitem", test.body)
					wantStatus(t, resp, test.status, test.name)
				}
				if got := e.Count(t, "SELECT COUNT(*) FROM mediaItemShares"); got != before {
					t.Fatalf("rejected creates changed the row count: %d -> %d", before, got)
				}
				if found := e.Rows(t, "SELECT id FROM mediaItemShares WHERE slug = ? OR mediaItemId = ?", "share-slug-bad", sagaMedia); len(found) != 0 {
					t.Fatalf("rejected creates persisted a share: %v", found)
				}
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "error", State: "failure", Assertion: "create-share-rejection-is-silent",
			Sources: []string{"server/controllers/ShareController.js:320-352", "server/managers/ShareManager.js:136-143", "server/SocketAuthority.js:81-90"},
			Claim:   "A rejected create changes no row and emits no share_open to an admin or listener socket; the existing share row is byte-identical before and after.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				mediaID := mediaIDForItem(t, e, fx.saga1)
				created := mustCreateShare(t, e, "share-slug-silent", mediaID, 0, true)
				admin := e.Socket(t, e.Token(t, "root"))
				listener := e.Socket(t, e.Token(t, "listener"))
				adminMark, listenerMark := admin.Mark(), listener.Mark()
				before := shareRows(t, e, created.ID)
				rows := e.Count(t, "SELECT COUNT(*) FROM mediaItemShares")

				wantStatus(t, createShare(t, e, e.Token(t, "listener"), "share-slug-denied", mediaID, 0, true), http.StatusForbidden, "listener create")
				wantStatus(t, createShare(t, e, e.Token(t, "root"), "share-slug-dup", mediaID, 0, true), http.StatusConflict, "duplicate media create")
				wantStatus(t, createShare(t, e, e.Token(t, "root"), "share-slug-invalid", mediaID, -1, true), http.StatusBadRequest, "invalid expiry create")

				if after := shareRows(t, e, created.ID); len(after) != 1 || !rowsEqual(before[0], after[0]) {
					t.Fatalf("rejected creates modified the existing row: %v -> %v", before, after)
				}
				if got := e.Count(t, "SELECT COUNT(*) FROM mediaItemShares"); got != rows {
					t.Fatalf("rejected creates changed the row count: %d -> %d", rows, got)
				}
				admin.None(t, adminMark, "share_open", quietWindow, nil)
				listener.None(t, listenerMark, "share_open", quietWindow, nil)
				wantStatus(t, removeShare(t, e, created.ID), http.StatusNoContent, "cleanup silent share")
			},
		},
	}
}

// rowsEqual compares two row maps by every column value.
func rowsEqual(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}
