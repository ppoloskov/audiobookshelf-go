package sharing

import (
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/internal/dbtime"
)

// Expiry must be checked by every cookie-protected route, including direct
// cover/track requests that do not first fetch the share document.
func TestWaveGExpiredShareDirectRoutesRevokeSessionOnce(t *testing.T) {
	paths := []struct {
		name, path, method, body string
	}{
		{"cover", "/cover", http.MethodGet, ""},
		{"track", "/track/0", http.MethodGet, ""},
		{"download", "/download", http.MethodGet, ""},
		{"progress", "/progress", http.MethodPatch, `{"currentTime":3}`},
		{"session-document", "", http.MethodGet, ""},
	}
	for _, tc := range paths {
		t.Run(tc.name, func(t *testing.T) {
			db, h, admin, media, events := recordingFixture(t)
			// Give the cover route a real live target so the expiry assertion
			// distinguishes authorization from a missing-artwork 404.
			if _, err := db.Exec(`UPDATE books SET coverPath=? WHERE id='book-1'`, media); err != nil {
				t.Fatal(err)
			}
			const slug = "wave-g-expiry"
			createShare(t, h, admin, `{"slug":"`+slug+`","expiresAt":0,"mediaItemType":"book","mediaItemId":"book-1","isDownloadable":true}`)
			opened := shareCall(h, http.MethodGet, "/public/share/"+slug, "", nil)
			if opened.Code != http.StatusOK || len(opened.Result().Cookies()) == 0 {
				t.Fatalf("open share status=%d body=%s", opened.Code, opened.Body.String())
			}
			cookie := opened.Result().Cookies()[0]
			live := shareCall(h, tc.method, "/public/share/"+slug+tc.path, tc.body, cookie)
			if live.Code != http.StatusOK && !(tc.name == "progress" && live.Code == http.StatusNoContent) {
				t.Fatalf("live route status=%d body=%s", live.Code, live.Body.String())
			}
			if _, err := db.Exec(`UPDATE mediaItemShares SET expiresAt=? WHERE slug=?`, dbtime.Format(time.Now().Add(-time.Minute)), slug); err != nil {
				t.Fatal(err)
			}
			got := shareCall(h, tc.method, "/public/share/"+slug+tc.path, tc.body, cookie)
			if got.Code != http.StatusNotFound {
				t.Fatalf("expired route status=%d body=%s, want 404", got.Code, got.Body.String())
			}
			if n := countShares(t, db); n != 0 {
				t.Fatalf("expired share still persisted: %d", n)
			}
			closed := 0
			for _, event := range *events {
				if event.event == "share_closed" && event.room == "@admins" {
					closed++
				}
			}
			if closed != 1 {
				t.Fatalf("share_closed count=%d, want exactly one", closed)
			}
			// Repeating the same expired request is idempotent and emits no
			// second close event.
			if again := shareCall(h, tc.method, "/public/share/"+slug+tc.path, tc.body, cookie); again.Code != http.StatusNotFound {
				t.Fatalf("repeated expired route status=%d, want 404", again.Code)
			}
			closed = 0
			for _, event := range *events {
				if event.event == "share_closed" && event.room == "@admins" {
					closed++
				}
			}
			if closed != 1 {
				t.Fatalf("repeated expiry emitted %d share_closed events", closed)
			}
		})
	}
}
