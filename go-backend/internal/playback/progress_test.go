package playback

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/dbtest"
	"audiobookshelf-compat/internal/httpx"
)

func TestProgressApplyRules(t *testing.T) {
	d := dbtest.Fixture(t)
	p := NewProgress(d)
	ctx := context.Background()
	var row db.User
	if err := d.Get(ctx, &row, `SELECT * FROM users WHERE id = ?`, rootID); err != nil {
		t.Fatal(err)
	}
	root := auth.NewPrincipal(row)
	apply := func(payload string) ProgressView {
		t.Helper()
		var u Update
		if err := json.Unmarshal([]byte(payload), &u); err != nil {
			t.Fatal(err)
		}
		v, err := p.Apply(ctx, root, u)
		if err != nil {
			t.Fatalf("%s: %v", payload, err)
		}
		return v
	}
	item := `"libraryItemId":"` + novelItem + `",`

	created := apply(`{` + item + `"duration":45,"currentTime":5,"progress":0.11}`)
	if created.CurrentTime != 5 || created.Progress != 0.11 || created.IsFinished || created.EbookProgress == nil || *created.EbookProgress != 0 {
		t.Fatalf("created %+v", created)
	}
	if again := apply(`{` + item + `"currentTime":5}`); *again.LastUpdate != *created.LastUpdate {
		t.Errorf("an unchanged update bumped lastUpdate")
	}
	if v := apply(`{` + item + `"currentTime":20}`); *v.LastUpdate < *created.LastUpdate || v.CurrentTime != 20 {
		t.Errorf("moved %+v", v)
	}
	// Absent leaves a value alone, null clears it.
	if v := apply(`{` + item + `"ebookProgress":0.5}`); *v.EbookProgress != 0.5 {
		t.Errorf("ebookProgress %+v", v)
	}
	if v := apply(`{` + item + `"currentTime":21}`); v.EbookProgress == nil || *v.EbookProgress != 0.5 {
		t.Errorf("an absent ebookProgress was cleared: %+v", v)
	}
	if v := apply(`{` + item + `"ebookProgress":null,"duration":null}`); v.EbookProgress != nil || v.Duration != nil {
		t.Errorf("null did not clear: %+v", v)
	}
	apply(`{` + item + `"duration":45}`)
	// Within the library's 10 seconds of the end: finished; moving back un-finishes.
	if v := apply(`{` + item + `"currentTime":40}`); !v.IsFinished || v.Progress != 1 || v.FinishedAt == nil {
		t.Errorf("near the end %+v", v)
	}
	if v := apply(`{` + item + `"currentTime":10}`); v.IsFinished || v.FinishedAt != nil {
		t.Errorf("moved back %+v", v)
	}
	// Resetting a finished item rewinds it; unhiding follows movement.
	apply(`{` + item + `"isFinished":true}`)
	if v := apply(`{` + item + `"isFinished":false}`); v.IsFinished || v.CurrentTime != 0 || v.Progress != 0 {
		t.Errorf("reset %+v", v)
	}
	apply(`{` + item + `"hideFromContinueListening":true}`)
	if v := apply(`{` + item + `"currentTime":3}`); v.HideFromContinueListening {
		t.Errorf("movement should unhide: %+v", v)
	}
	// lastUpdate becomes updatedAt.
	if v := apply(`{` + item + `"currentTime":4,"lastUpdate":1700000000000}`); *v.LastUpdate != 1700000000000 {
		t.Errorf("lastUpdate %+v", v)
	}
}

func TestProgressApplyRefusals(t *testing.T) {
	d := dbtest.Fixture(t)
	p := NewProgress(d)
	ctx := context.Background()
	if _, err := d.Exec(ctx, `UPDATE users SET permissions = json_set(permissions, '$.accessAllLibraries', json('false')) WHERE id = ?`, finisherID); err != nil {
		t.Fatal(err)
	}
	var row db.User
	if err := d.Get(ctx, &row, `SELECT * FROM users WHERE id = ?`, finisherID); err != nil {
		t.Fatal(err)
	}
	user := auth.NewPrincipal(row)
	for name, tc := range map[string]struct {
		update Update
		status int
	}{
		"unknown item":    {Update{LibraryItemID: "nope"}, http.StatusNotFound},
		"unknown episode": {Update{EpisodeID: "nope"}, http.StatusNotFound},
		"forbidden":       {Update{LibraryItemID: novelItem}, http.StatusForbidden},
		"forbidden ep":    {Update{LibraryItemID: podcast, EpisodeID: episode1}, http.StatusForbidden},
	} {
		_, err := p.Apply(ctx, user, tc.update)
		if got := statusOf(err); err == nil || got != tc.status {
			t.Errorf("%s: err %v status %d, want %d", name, err, got, tc.status)
		}
	}
	// A podcast item without an episode is not a book (after the library check).
	d2 := dbtest.Fixture(t)
	var root db.User
	if err := d2.Get(ctx, &root, `SELECT * FROM users WHERE id = ?`, rootID); err != nil {
		t.Fatal(err)
	}
	if _, err := NewProgress(d2).Apply(ctx, auth.NewPrincipal(root), Update{LibraryItemID: podcast}); statusOf(err) != http.StatusBadRequest {
		t.Errorf("podcast without episode: %v", err)
	}
}

// statusOf is the HTTP status httpx would answer err with.
func statusOf(err error) int {
	w := httptest.NewRecorder()
	httpx.Handler(func(http.ResponseWriter, *http.Request) error { return err }).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	return w.Code
}
