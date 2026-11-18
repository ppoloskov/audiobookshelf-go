package user_progress

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/tests/replacement/harness"
)

// progressUpdated is the payload of user_item_progress_updated.
type progressUpdated struct {
	ID                string       `json:"id"`
	SessionID         string       `json:"sessionId"`
	DeviceDescription string       `json:"deviceDescription"`
	Data              progressJSON `json:"data"`
}

// decodeProgressUpdated decodes the single argument of user_item_progress_updated.
func decodeProgressUpdated(t *testing.T, ev harness.Event) progressUpdated {
	t.Helper()
	var payload progressUpdated
	if len(ev.Args) != 1 {
		t.Fatalf("user_item_progress_updated takes one argument, got %d", len(ev.Args))
	}
	if err := json.Unmarshal(ev.Args[0], &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	return payload
}

// progressUpdatedWhere matches a user_item_progress_updated event by its data.
func progressUpdatedWhere(match func(progressUpdated) bool) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		var payload progressUpdated
		return len(args) == 1 && json.Unmarshal(args[0], &payload) == nil && match(payload)
	}
}

const progressEvent = "user_item_progress_updated"

// session is a started playback session.
type session struct {
	ID       string  `json:"id"`
	Duration float64 `json:"duration"`
}

// deviceDescription is what the device below is described as.
const deviceDescription = "Pixel SDK 33 / v1.2.3"

func device(id string) map[string]any {
	return map[string]any{"deviceId": id, "model": "Pixel", "sdkVersion": "33", "clientVersion": "1.2.3", "clientName": "progress-packet"}
}

// startSession starts a direct-play session (setup through the playback
// routes, owned by another packet).
func startSession(t *testing.T, e *harness.Env, who actor, item, episode, deviceID string) session {
	t.Helper()
	path := "/api/items/" + item + "/play"
	if episode != "" {
		path += "/" + episode
	}
	resp := e.Post(t, who.token, path, map[string]any{"deviceInfo": device(deviceID), "mediaPlayer": "progress-packet", "forceDirectPlay": true, "supportedMimeTypes": []string{}})
	wantStatus(t, resp, http.StatusOK, "start playback")
	var started session
	resp.JSON(t, &started)
	if started.ID == "" {
		t.Fatalf("session has no id: %s", resp.Body)
	}
	return started
}

func progressEventScenarios() []harness.Scenario {
	return append(patchEventScenarios(), syncEventScenarios()...)
}

func patchEventScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r111", Invariant: "INV-R111-EVENT-001", Case: "normal", State: "initial", Assertion: "patch-emits-user-updated",
			Sources: []string{patchSource, "server/models/User.js:603-638"},
			Claim:   "A successful PATCH emits user_updated with the caller's user (id, username, every media progress object including the written one) to the caller's sockets only, on create and on update, and never user_item_progress_updated.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				sockets := connectAudiences(t, e, who)
				mark := sockets.mark()
				patchOK(t, e, who, e.IDs["saga1"], "", map[string]any{"currentTime": 12, "duration": 60, "progress": 0.2})
				user := decodeUserUpdated(t, sockets.ownerOnly(t, mark, "user_updated", userUpdatedOf(who.id)))
				row := progressRow(t, e, who.id, saga1Book)
				if user.Username != who.name || len(user.MediaProgress) != 1 {
					t.Fatalf("user_updated user wrong: %+v", user)
				}
				requireAPIMatchesRow(t, user.MediaProgress[0], row, who.id, e.IDs["saga1"], "")
				sockets.nobody(t, mark, progressEvent)

				mark = sockets.mark()
				patchOK(t, e, who, e.IDs["podcast"], e.IDs["ep1"], map[string]any{"currentTime": 4, "duration": 24, "progress": 4.0 / 24})
				patchOK(t, e, who, e.IDs["saga1"], "", map[string]any{"currentTime": 30, "duration": 60, "progress": 0.5})
				sockets.owner.Wait(t, mark.owner, "user_updated", 5*time.Second, func(args []json.RawMessage) bool {
					var got userJSON
					if len(args) != 1 || json.Unmarshal(args[0], &got) != nil || got.ID != who.id || len(got.MediaProgress) != 2 {
						return false
					}
					byItem := progressByItem(got.MediaProgress)
					return byItem[saga1Book].CurrentTime == 30 && byItem[e.IDs["ep1"]].CurrentTime == 4
				})
				sockets.nobody(t, mark, progressEvent)
			},
		},
	}
}

func syncEventScenarios() []harness.Scenario {
	const requirement = "socket-outgoing-user-item-progress-updated"
	syncSources := []string{"server/managers/PlaybackSessionManager.js:372-410", "server/SocketAuthority.js:68-78"}
	return []harness.Scenario{
		{
			Requirement: requirement, Invariant: "INV-SOCKET-OUTGOING-USER-ITEM-PROGRESS-UPDATED", Case: "normal", State: "initial", Assertion: "session-sync-emits-progress-updated",
			Sources: syncSources,
			Claim:   "POST /api/session/:id/sync emits user_item_progress_updated {id, sessionId, deviceDescription, data} to the session owner's sockets only, with data equal to the stored progress, and no user_updated.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				started := startSession(t, e, who, e.IDs["saga1"], "", "progress-dev-1")
				sockets := connectAudiences(t, e, who)
				mark := sockets.mark()
				wantStatus(t, e.Post(t, who.token, "/api/session/"+started.ID+"/sync", map[string]any{"currentTime": 20, "timeListened": 5}), http.StatusOK, "sync")
				payload := decodeProgressUpdated(t, sockets.ownerOnly(t, mark, progressEvent, nil))
				row := progressRow(t, e, who.id, saga1Book)
				id, _ := text(row, "id")
				if payload.ID != id || payload.SessionID != started.ID || payload.DeviceDescription != deviceDescription {
					t.Fatalf("payload wrong: %+v (row %s, session %s)", payload, id, started.ID)
				}
				if payload.Data.CurrentTime != 20 || payload.Data.Duration != started.Duration || payload.Data.MediaItemType != "book" {
					t.Fatalf("payload data wrong: %+v", payload.Data)
				}
				requireAPIMatchesRow(t, payload.Data, row, who.id, e.IDs["saga1"], "")
				sockets.owner.None(t, mark.owner, "user_updated", 400*time.Millisecond, nil)
			},
		},
		{
			Requirement: requirement, Invariant: "INV-SOCKET-OUTGOING-USER-ITEM-PROGRESS-UPDATED", Case: "edge", State: "repeat", Assertion: "session-sync-emits-on-every-sync",
			Sources: syncSources,
			Claim:   "Every sync emits again, with the same progress id and session id and the newer position, also when the position did not change.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				started := startSession(t, e, who, e.IDs["saga1"], "", "progress-dev-2")
				sockets := connectAudiences(t, e, who)
				var ids []string
				for _, position := range []float64{10, 25, 25} {
					mark := sockets.mark()
					wantStatus(t, e.Post(t, who.token, "/api/session/"+started.ID+"/sync", map[string]any{"currentTime": position, "timeListened": 1}), http.StatusOK, "sync")
					payload := decodeProgressUpdated(t, sockets.ownerOnly(t, mark, progressEvent, nil))
					if payload.SessionID != started.ID || payload.Data.CurrentTime != position {
						t.Fatalf("payload at %v wrong: %+v", position, payload)
					}
					ids = append(ids, payload.ID)
				}
				if ids[0] != ids[1] || ids[1] != ids[2] {
					t.Fatalf("progress id must stay the same: %v", ids)
				}
			},
		},
		{
			Requirement: requirement, Invariant: "INV-SOCKET-OUTGOING-USER-ITEM-PROGRESS-UPDATED", Case: "edge", State: "changed", Assertion: "session-sync-episode-and-close",
			Sources: append([]string{"server/managers/PlaybackSessionManager.js:299-302", "server/managers/PlaybackSessionManager.js:421-430"}, syncSources...),
			Claim:   "A podcast episode session emits data with episodeId and mediaItemType podcastEpisode; closing with sync data emits too, closing without a body does not.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				started := startSession(t, e, who, e.IDs["podcast"], e.IDs["ep1"], "progress-dev-3")
				sockets := connectAudiences(t, e, who)
				mark := sockets.mark()
				wantStatus(t, e.Post(t, who.token, "/api/session/"+started.ID+"/sync", map[string]any{"currentTime": 6, "timeListened": 2}), http.StatusOK, "sync")
				payload := decodeProgressUpdated(t, sockets.ownerOnly(t, mark, progressEvent, nil))
				row := progressRow(t, e, who.id, e.IDs["ep1"])
				requireAPIMatchesRow(t, payload.Data, row, who.id, e.IDs["podcast"], e.IDs["ep1"])
				if payload.Data.MediaItemType != "podcastEpisode" || payload.Data.CurrentTime != 6 || payload.SessionID != started.ID {
					t.Fatalf("episode payload wrong: %+v", payload)
				}
				mark = sockets.mark()
				wantStatus(t, e.Post(t, who.token, "/api/session/"+started.ID+"/close", map[string]any{"currentTime": 9, "timeListened": 1}), http.StatusOK, "close with data")
				closed := decodeProgressUpdated(t, sockets.ownerOnly(t, mark, progressEvent, nil))
				if closed.Data.CurrentTime != 9 || closed.SessionID != started.ID {
					t.Fatalf("close payload wrong: %+v", closed)
				}
				second := startSession(t, e, who, e.IDs["saga1"], "", "progress-dev-4")
				mark = sockets.mark()
				wantStatus(t, e.Post(t, who.token, "/api/session/"+second.ID+"/close", nil), http.StatusOK, "close without data")
				sockets.nobody(t, mark, progressEvent)
			},
		},
		{
			Requirement: requirement, Invariant: "INV-SOCKET-OUTGOING-USER-ITEM-PROGRESS-UPDATED", Case: "error", State: "failure", Assertion: "session-sync-rejected-emits-nothing",
			Sources: []string{"server/controllers/SessionController.js:338-349", "server/managers/PlaybackSessionManager.js:372-410"},
			Claim:   "A sync of an unknown session (404) or of another user's session (403) stores no progress and emits no user_item_progress_updated to anyone.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				intruder := newActor(t, e)
				started := startSession(t, e, who, e.IDs["saga1"], "", "progress-dev-5")
				sockets := connectAudiences(t, e, who)
				mark := sockets.mark()
				intruderSocket := e.Socket(t, intruder.token)
				intruderMark := intruderSocket.Mark()
				wantStatus(t, e.Post(t, who.token, "/api/session/"+unknownID+"/sync", map[string]any{"currentTime": 20}), http.StatusNotFound, "unknown session")
				wantStatus(t, e.Post(t, intruder.token, "/api/session/"+started.ID+"/sync", map[string]any{"currentTime": 20}), http.StatusForbidden, "another user's session")
				requireNoRows(t, e, who.id)
				requireNoRows(t, e, intruder.id)
				sockets.nobody(t, mark, progressEvent)
				intruderSocket.None(t, intruderMark, progressEvent, 400*time.Millisecond, nil)
			},
		},
		{
			Requirement: requirement, Invariant: "INV-SOCKET-OUTGOING-USER-ITEM-PROGRESS-UPDATED", Case: "normal", State: "changed", Assertion: "local-session-sync-emits-only-fresh-progress",
			Sources: []string{"server/managers/PlaybackSessionManager.js:120-270", "server/managers/PlaybackSessionManager.js:281-290", "server/controllers/SessionController.js:254-267"},
			Claim:   "POST /api/session/local-all emits user_item_progress_updated (sessionId = the pushed id, or the uuid a play_local_ id maps to) when it creates or updates progress, applies the session's updatedAt as lastUpdate on update, and emits nothing, with progressSynced false, for a session older than the stored progress; an unknown item answers success false (local-all) or 500 (local) and emits nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				who := newActor(t, e)
				sockets := connectAudiences(t, e, who)
				now := time.Now().UnixMilli()
				local := func(id string, item string, current float64, updatedAt int64) map[string]any {
					return map[string]any{
						"id": id, "libraryItemId": item, "mediaType": "book", "displayTitle": "The Beginning", "displayAuthor": "A",
						"duration": 60.186123, "playMethod": 3, "mediaPlayer": "progress-packet", "startTime": 0, "currentTime": current,
						"timeListening": 5, "startedAt": now - 60000, "updatedAt": updatedAt, "date": "2026-11-15", "dayOfWeek": "Sunday",
					}
				}
				push := func(sessions ...map[string]any) []map[string]any {
					resp := e.Post(t, who.token, "/api/session/local-all", map[string]any{"deviceInfo": device("progress-dev-local"), "sessions": sessions})
					wantStatus(t, resp, http.StatusOK, "local-all")
					var body struct {
						Results []map[string]any `json:"results"`
					}
					resp.JSON(t, &body)
					if len(body.Results) != len(sessions) {
						t.Fatalf("want %d results, got %s", len(sessions), resp.Body)
					}
					return body.Results
				}
				synced := func(result map[string]any) bool { v, _ := result["progressSynced"].(bool); return v }

				// New progress: created, event carries the new row.
				mark := sockets.mark()
				results := push(local("play_local_progress-packet", e.IDs["saga1"], 15, now), local("00000000-0000-4000-8000-0000000000a2", unknownID, 5, now))
				if !synced(results[0]) || results[0]["success"] != true || results[1]["success"] != false || synced(results[1]) {
					t.Fatalf("results wrong: %v", results)
				}
				payload := decodeProgressUpdated(t, sockets.ownerOnly(t, mark, progressEvent, nil))
				row := progressRow(t, e, who.id, saga1Book)
				if payload.SessionID == "" || payload.DeviceDescription != deviceDescription || payload.Data.CurrentTime != 15 {
					t.Fatalf("created payload wrong: %+v", payload)
				}
				requireAPIMatchesRow(t, payload.Data, row, who.id, e.IDs["saga1"], "")
				if n := e.Count(t, "SELECT COUNT(*) FROM mediaProgresses WHERE userId = ?", who.id); n != 1 {
					t.Fatalf("the unknown item must store nothing, rows: %d", n)
				}

				// Older than the stored progress: not applied.
				mark = sockets.mark()
				results = push(local("00000000-0000-4000-8000-0000000000a3", e.IDs["saga1"], 40, now-3600000))
				if results[0]["success"] != true || synced(results[0]) {
					t.Fatalf("stale session must not sync: %v", results)
				}
				sockets.nobody(t, mark, progressEvent)
				sameRow(t, "stale session", row, progressRow(t, e, who.id, saga1Book))

				// Newer, same session through the single-session route: applied, updatedAt is the session's.
				mark = sockets.mark()
				newer := now + 3600000
				single := local("play_local_progress-packet", e.IDs["saga1"], 40, newer)
				single["timeListening"] = 12
				single["deviceInfo"] = device("progress-dev-local")
				wantStatus(t, e.Post(t, who.token, "/api/session/local", single), http.StatusOK, "session/local")
				updated := decodeProgressUpdated(t, sockets.ownerOnly(t, mark, progressEvent, nil))
				if updated.Data.CurrentTime != 40 || updated.Data.LastUpdate != newer || updated.SessionID != payload.SessionID {
					t.Fatalf("updated payload wrong: %+v (want lastUpdate %d, session %s)", updated, newer, payload.SessionID)
				}
				requireAPIMatchesRow(t, updated.Data, progressRow(t, e, who.id, saga1Book), who.id, e.IDs["saga1"], "")
				mark = sockets.mark()
				missing := local("00000000-0000-4000-8000-0000000000a9", unknownID, 5, newer)
				missing["deviceInfo"] = device("progress-dev-local")
				wantStatus(t, e.Post(t, who.token, "/api/session/local", missing), http.StatusInternalServerError, "session/local with an unknown item")
				sockets.nobody(t, mark, progressEvent)
				rows := e.Rows(t, "SELECT currentTime, timeListening, updatedAt, date, dayOfWeek, userId FROM playbackSessions WHERE id = ?", payload.SessionID)
				if len(rows) != 1 {
					t.Fatalf("the pushed session must be stored once under the mapped id, got %d rows", len(rows))
				}
				day := time.UnixMilli(newer).UTC()
				if num(t, rows[0], "currentTime") != 40 || num(t, rows[0], "timeListening") != 12 || stamp(t, rows[0], "updatedAt").UnixMilli() != newer ||
					rows[0]["date"] != day.Format("2006-01-02") || rows[0]["dayOfWeek"] != day.Weekday().String() || rows[0]["userId"] != who.id {
					t.Fatalf("stored session wrong: %v", rows[0])
				}
			},
		},
	}
}
