package store

import (
	"net/http"
	"reflect"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

func localSession(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{})
	token := e.Token(t, "listener")
	const id = "b5b541c1-d731-47c9-a3da-d05fa0c9f921"
	progressQuery := "SELECT id,currentTime,updatedAt FROM mediaProgresses WHERE userId=? AND mediaItemId=?"
	progressArgs := []any{e.IDs["listener"], "5e33de11-3e94-4c51-87d8-d06d2928ac38"}
	progress := e.Rows(t, progressQuery, progressArgs...)
	if len(progress) != 1 || progress[0]["id"] != "b95b3028-3461-4306-803b-6e310140cd22" || progress[0]["currentTime"] != float64(27.5) || progress[0]["updatedAt"] != "2026-11-03 12:46:26.973 +00:00" {
		t.Fatal("seeded newer progress is missing or changed")
	}
	payload := map[string]any{"id": id, "userId": e.IDs["listener"], "libraryId": e.IDs["library"], "libraryItemId": e.IDs["saga1"], "mediaType": "book", "mediaMetadata": map[string]any{}, "chapters": []any{}, "displayTitle": "The Beginning", "displayAuthor": "Terry Fixture", "coverPath": nil, "duration": 60, "playMethod": 3, "mediaPlayer": "exo-player", "deviceInfo": map[string]any{"deviceId": "store-offline-android", "manufacturer": "Google", "model": "Pixel 8", "sdkVersion": 34, "clientName": "Abs Android", "clientVersion": "0.9.79-beta"}, "serverVersion": "2.37.2", "date": "2023-11-14", "dayOfWeek": "Tuesday", "timeListening": 10, "startTime": 0, "currentTime": 10, "startedAt": int64(1700000000000), "updatedAt": int64(1700000010000)}
	r := e.Post(t, token, "/api/session/local", payload)
	if r.Status != http.StatusOK {
		t.Fatalf("local session status %d", r.Status)
	}
	query := "SELECT id,userId,mediaItemId,mediaItemType,displayTitle,displayAuthor,playMethod,mediaPlayer,startTime,currentTime,timeListening,date,dayOfWeek,createdAt,updatedAt FROM playbackSessions WHERE id=?"
	rows := e.Rows(t, query, id)
	if len(rows) != 1 || rows[0]["userId"] != e.IDs["listener"] || rows[0]["mediaItemId"] != "5e33de11-3e94-4c51-87d8-d06d2928ac38" || rows[0]["mediaItemType"] != "book" || rows[0]["displayTitle"] != "The Beginning" || rows[0]["displayAuthor"] != "Terry Fixture" || rows[0]["playMethod"] != int64(3) || rows[0]["mediaPlayer"] != "exo-player" || rows[0]["startTime"] != float64(0) || rows[0]["date"] != "2023-11-14" || rows[0]["dayOfWeek"] != "Tuesday" || rows[0]["createdAt"] != "2023-11-14 22:13:20.000 +00:00" || rows[0]["updatedAt"] != "2023-11-14 22:13:30.000 +00:00" || rows[0]["currentTime"] != float64(10) || rows[0]["timeListening"] != int64(10) {
		t.Fatalf("local session values: %v", rows)
	}
	if got := e.Rows(t, progressQuery, progressArgs...); !reflect.DeepEqual(got, progress) {
		t.Fatal("older offline session overwrote newer progress")
	}
	history := sessionHistory(t, e, token, id)
	e.Stop(t)
	reopened := harness.Start(t, harness.Options{Install: e.Install, Upstream: true})
	if got := reopened.Rows(t, query, id); !reflect.DeepEqual(got, rows) {
		t.Fatalf("session changed on reopen: %v", got)
	}
	if got := reopened.Rows(t, progressQuery, progressArgs...); !reflect.DeepEqual(got, progress) {
		t.Fatal("progress changed on reopen")
	}
	token = reopened.Token(t, "listener")
	if got := sessionHistory(t, reopened, token, id); !reflect.DeepEqual(got, history) {
		t.Fatalf("session history changed: %v", got)
	}
}

type historySession struct {
	ID            string  `json:"id"`
	LibraryItemID string  `json:"libraryItemId"`
	DisplayTitle  string  `json:"displayTitle"`
	CurrentTime   float64 `json:"currentTime"`
	TimeListening float64 `json:"timeListening"`
}

func sessionHistory(t *testing.T, e *harness.Env, token, id string) historySession {
	t.Helper()
	r := e.Get(t, token, "/api/me/listening-sessions?itemsPerPage=100&page=0")
	if r.Status != http.StatusOK {
		t.Fatalf("history status %d", r.Status)
	}
	var body struct {
		Sessions []historySession `json:"sessions"`
	}
	r.JSON(t, &body)
	for _, s := range body.Sessions {
		if s.ID == id {
			if s.LibraryItemID != e.IDs["saga1"] || s.DisplayTitle != "The Beginning" || s.CurrentTime != 10 || s.TimeListening != 10 {
				t.Fatalf("history session %+v", s)
			}
			return s
		}
	}
	t.Fatal("offline session missing from history")
	return historySession{}
}
