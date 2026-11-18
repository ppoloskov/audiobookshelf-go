package playback_sessions

import (
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	playSource   = "server/controllers/LibraryItemController.js:437-472"
	startSource  = "server/managers/PlaybackSessionManager.js:281-364"
	setDataRef   = "server/objects/PlaybackSession.js:196-224"
	deviceSource = "server/objects/DeviceInfo.js:78-123"
)

func playScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r059", Invariant: "INV-R059-PLAY-SESSION-001", Case: "normal", State: "initial", Assertion: "play-book-direct-session",
			Sources: []string{"server/routers/ApiRouter.js:117", playSource, startSource, setDataRef},
			Claim:   "POST /api/items/:id/play with supported mime types starts a direct-play session whose document carries the checked book fields, the resume position, the device and one track per audio file, and holds it in memory only.",
			Run: func(t *testing.T, e *harness.Env) {
				listener := fixtureActor(t, e, "listener")
				requested := time.Now()
				doc := play(t, e, listener.Token, e.IDs["saga1"], "", androidBody("ps-android-1", "audio/mpeg"))
				requireSaga1Session(t, e, doc, listener.ID, 27.5, 0, "exo-player")
				requireNewSessionTimes(t, doc, requested)
				requireDirectTracks(t, e, doc.AudioTracks)
				device := deviceRow(t, e, "ps-android-1")
				requireEqual(t, "deviceInfo", doc.DeviceInfo, map[string]any{
					"id": device["id"], "userId": listener.ID, "deviceId": "ps-android-1", "ipAddress": "127.0.0.1", "clientVersion": "0.10.0",
					"manufacturer": "Google", "model": "Pixel 8", "sdkVersion": "34", "clientName": "Abs Android", "deviceName": "Google Pixel 8",
				})
				if row := sessionRow(t, e, doc.ID); row != nil {
					t.Fatalf("a session without listening time must not be stored yet: %v", row)
				}
				if !contains(openSessionIDs(t, e), doc.ID) {
					t.Fatal("started session is not open")
				}
				closeQuietly(t, e, listener.Token, doc.ID)
			},
		},
		{
			Requirement: "route-r059", Invariant: "INV-R059-METHOD-001", Case: "normal", State: "initial", Assertion: "play-method-direct-or-transcode",
			Sources: []string{startSource, "server/models/Book.js:278-284", "go-backend/testmachine/contracts/clients/official-app.md:139"},
			Claim:   "The play method is direct play when every audio file mime type is supported or forceDirectPlay is truthy, otherwise a single HLS track; forceDirectPlay wins over forceTranscode and the iOS string flags work.",
			Run: func(t *testing.T, e *harness.Env) {
				listener := fixtureActor(t, e, "listener")
				cases := []struct {
					name      string
					body      playBody
					transcode bool
				}{
					{"supported", playBody{"supportedMimeTypes": []string{"audio/mpeg", "audio/ogg"}}, false},
					{"unsupported", playBody{"supportedMimeTypes": []string{"audio/ogg"}}, true},
					{"empty-list", playBody{"supportedMimeTypes": []string{}}, true},
					{"no-list", playBody{"mediaPlayer": "html5"}, true},
					{"force-direct-unsupported", playBody{"supportedMimeTypes": []string{"audio/ogg"}, "forceDirectPlay": true}, false},
					{"force-direct-no-list", playBody{"forceDirectPlay": true}, false},
					{"force-transcode-supported", playBody{"supportedMimeTypes": []string{"audio/mpeg"}, "forceTranscode": true}, true},
					{"both-forced", playBody{"forceDirectPlay": true, "forceTranscode": true}, false},
					{"ios-direct-strings", playBody{"forceDirectPlay": "1", "forceTranscode": "", "mediaPlayer": "AVPlayer"}, false},
					{"ios-transcode-strings", playBody{"forceDirectPlay": "", "forceTranscode": "1", "supportedMimeTypes": []string{"audio/mpeg"}}, true},
				}
				for _, c := range cases {
					body := c.body
					body["deviceInfo"] = map[string]any{"deviceId": "ps-method-" + c.name}
					doc := play(t, e, listener.Token, e.IDs["saga1"], "", body)
					if c.transcode {
						if doc.PlayMethod == nil || *doc.PlayMethod != 2 {
							t.Fatalf("%s: playMethod %v, want 2 (transcode)", c.name, doc.PlayMethod)
						}
						requireHLSTrack(t, doc, "The Beginning", saga1Duration)
					} else {
						if doc.PlayMethod == nil || *doc.PlayMethod != 0 {
							t.Fatalf("%s: playMethod %v, want 0 (direct play)", c.name, doc.PlayMethod)
						}
						requireDirectTracks(t, e, doc.AudioTracks)
					}
					wantPlayer := "unknown"
					if player, ok := body["mediaPlayer"].(string); ok {
						wantPlayer = player
					}
					if doc.MediaPlayer == nil || *doc.MediaPlayer != wantPlayer {
						t.Fatalf("%s: mediaPlayer %v, want %q", c.name, doc.MediaPlayer, wantPlayer)
					}
					requireNear(t, c.name+" startTime", doc.StartTime, 27.5)
					closeQuietly(t, e, listener.Token, doc.ID)
				}
			},
		},
		{
			Requirement: "route-r059", Invariant: "INV-R059-START-001", Case: "normal", State: "changed", Assertion: "play-start-time-follows-progress",
			Sources: []string{startSource, "server/managers/PlaybackSessionManager.js:293-304"},
			Claim:   "A new session starts at the current progress time, at zero for a user without progress and at zero for a finished item, and follows later progress changes.",
			Run: func(t *testing.T, e *harness.Env) {
				user := createUser(t, e, "ps-resume", map[string]any{})
				item := e.IDs["saga1"]
				start := func(device string) float64 {
					doc := play(t, e, user.Token, item, "", playBody{"supportedMimeTypes": []string{"audio/mpeg"}, "deviceInfo": map[string]any{"deviceId": device}})
					closeQuietly(t, e, user.Token, doc.ID)
					return doc.StartTime
				}
				patch := func(body map[string]any) {
					requireStatus(t, e.Patch(t, user.Token, "/api/me/progress/"+item, body), http.StatusOK, "set progress")
				}
				if got := start("ps-resume-1"); got != 0 {
					t.Fatalf("no progress: startTime %v, want 0", got)
				}
				patch(map[string]any{"currentTime": 30.25, "duration": saga1Duration})
				if got := start("ps-resume-1"); got != 30.25 {
					t.Fatalf("progress 30.25: startTime %v", got)
				}
				patch(map[string]any{"currentTime": 59, "duration": saga1Duration, "isFinished": true})
				if row := progressRow(t, e, user.ID, saga1BookID); row == nil || row["isFinished"].(int64) != 1 || number(t, row["currentTime"]) != 59 {
					t.Fatalf("setup: finished progress with currentTime 59 expected, got %v", row)
				}
				if got := start("ps-resume-1"); got != 0 {
					t.Fatalf("finished item: startTime %v, want 0 although currentTime is 59", got)
				}
				patch(map[string]any{"isFinished": false})
				patch(map[string]any{"currentTime": 42})
				if got := start("ps-resume-1"); got != 42 {
					t.Fatalf("restarted item at 42: startTime %v", got)
				}
				listener := fixtureActor(t, e, "listener")
				if got := start2(t, e, listener, e.IDs["saga2"], "ps-resume-2"); got != 9 {
					t.Fatalf("listener saga2 seeded progress 9: startTime %v", got)
				}
				finisher := fixtureActor(t, e, "finisher")
				if got := start2(t, e, finisher, e.IDs["saga1"], "ps-resume-3"); got != 0 {
					t.Fatalf("finisher saga1 seeded finished progress: startTime %v, want 0", got)
				}
			},
		},
		{
			Requirement: "route-r059", Invariant: "INV-R059-DEVICE-001", Case: "normal", State: "initial", Assertion: "play-device-info-variants",
			Sources: []string{startSource, deviceSource, "server/models/Device.js:35-70"},
			Claim:   "The session deviceInfo and the devices row follow the client description: Android, iOS, web by user agent, explicit client name, sanitised strings and a missing device id.",
			Run: func(t *testing.T, e *harness.Env) {
				listener := fixtureActor(t, e, "listener")
				start := func(userAgent string, deviceInfo map[string]any) (sessionDoc, map[string]any) {
					body := map[string]any{"supportedMimeTypes": []string{"audio/mpeg"}}
					if deviceInfo != nil {
						body["deviceInfo"] = deviceInfo
					}
					resp := e.Do(t, harness.Request{Method: http.MethodPost, Path: "/api/items/" + e.IDs["saga2"] + "/play", Token: listener.Token, JSON: body, Header: map[string]string{"User-Agent": userAgent}})
					requireStatus(t, resp, http.StatusOK, "play")
					var doc sessionDoc
					resp.JSON(t, &doc)
					closeQuietly(t, e, listener.Token, doc.ID)
					id, _ := doc.DeviceInfo["id"].(string)
					rows := e.Rows(t, "SELECT * FROM devices WHERE id = ?", id)
					if len(rows) != 1 {
						t.Fatalf("session device %q has no devices row", id)
					}
					return doc, rows[0]
				}
				// iOS: model without sdkVersion.
				doc, row := start("", map[string]any{"deviceId": "ps-ios-1", "clientVersion": "1.4.0", "manufacturer": "Apple", "model": "iPhone15,2"})
				requireEqual(t, "ios deviceInfo", doc.DeviceInfo, map[string]any{
					"id": row["id"], "userId": listener.ID, "deviceId": "ps-ios-1", "ipAddress": "127.0.0.1", "clientVersion": "1.4.0",
					"manufacturer": "Apple", "model": "iPhone15,2", "clientName": "Abs iOS", "deviceName": "Apple iPhone15,2",
				})
				requireEqual(t, "ios row", map[string]any{"clientName": row["clientName"], "clientVersion": row["clientVersion"], "deviceName": row["deviceName"], "deviceVersion": row["deviceVersion"], "extraData": row["extraData"], "userId": row["userId"], "ipAddress": row["ipAddress"]},
					map[string]any{"clientName": "Abs iOS", "clientVersion": "1.4.0", "deviceName": "Apple iPhone15,2", "deviceVersion": nil, "extraData": `{"manufacturer":"Apple","model":"iPhone15,2"}`, "userId": listener.ID, "ipAddress": "127.0.0.1"})
				// Web: no deviceInfo; fields come from the user agent and the device id is the generated id.
				before := e.Count(t, "SELECT COUNT(*) FROM devices")
				doc, row = start(chromeAgent, nil)
				id, _ := doc.DeviceInfo["id"].(string)
				requireEqual(t, "web deviceInfo", doc.DeviceInfo, map[string]any{
					"id": id, "userId": listener.ID, "deviceId": id, "ipAddress": "127.0.0.1", "browserName": "Chrome", "browserVersion": "120.0.0.0",
					"osName": "Linux", "osVersion": "x86_64", "clientVersion": serverVersion, "clientName": "Abs Web", "deviceName": "Linux x86_64 Chrome",
				})
				requireEqual(t, "web row", map[string]any{"deviceId": row["deviceId"], "clientName": row["clientName"], "clientVersion": row["clientVersion"], "deviceName": row["deviceName"], "deviceVersion": row["deviceVersion"], "extraData": row["extraData"]},
					map[string]any{"deviceId": id, "clientName": "Abs Web", "clientVersion": serverVersion, "deviceName": "Linux x86_64 Chrome", "deviceVersion": "120.0.0.0", "extraData": `{"osName":"Linux","osVersion":"x86_64","browserName":"Chrome"}`})
				doc2, _ := start(chromeAgent, nil)
				if doc2.DeviceInfo["id"] == id || e.Count(t, "SELECT COUNT(*) FROM devices") != before+2 {
					t.Fatalf("a client without deviceId must get a new device on every play")
				}
				// Explicit client name and tag stripping.
				doc, _ = start("", map[string]any{"deviceId": "ps-named-1", "clientName": "Absorb", "clientVersion": "<b>2.0</b>", "manufacturer": "Samsung", "model": "SM-<i>S9</i>08", "sdkVersion": "33"})
				requireEqual(t, "named deviceInfo", doc.DeviceInfo, map[string]any{
					"id": doc.DeviceInfo["id"], "userId": listener.ID, "deviceId": "ps-named-1", "ipAddress": "127.0.0.1", "clientVersion": "2.0",
					"manufacturer": "Samsung", "model": "SM-S908", "sdkVersion": "33", "clientName": "Absorb", "deviceName": "Samsung SM-S908",
				})
			},
		},
		{
			Requirement: "route-r059", Invariant: "INV-R059-DEVICE-001", Case: "edge", State: "repeat", Assertion: "play-known-device-updates-row",
			Sources: []string{startSource, "server/objects/DeviceInfo.js:125-150"},
			Claim:   "Playing again with a known client deviceId reuses the devices row and rewrites its description to the new client data instead of creating a second device.",
			Run: func(t *testing.T, e *harness.Env) {
				listener := fixtureActor(t, e, "listener")
				first := play(t, e, listener.Token, e.IDs["saga2"], "", androidBody("ps-device-known", "audio/mpeg"))
				closeQuietly(t, e, listener.Token, first.ID)
				before := deviceRow(t, e, "ps-device-known")
				count := e.Count(t, "SELECT COUNT(*) FROM devices")
				body := playBody{"supportedMimeTypes": []string{"audio/mpeg"}, "deviceInfo": map[string]any{"deviceId": "ps-device-known", "clientVersion": "0.11.0", "manufacturer": "Google", "model": "Pixel 9", "sdkVersion": 35}}
				second := play(t, e, listener.Token, e.IDs["saga2"], "", body)
				closeQuietly(t, e, listener.Token, second.ID)
				after := deviceRow(t, e, "ps-device-known")
				if after["id"] != before["id"] || second.DeviceInfo["id"] != before["id"] || e.Count(t, "SELECT COUNT(*) FROM devices") != count {
					t.Fatalf("device row must be reused: before %v after %v session %v", before["id"], after["id"], second.DeviceInfo["id"])
				}
				requireEqual(t, "updated device", second.DeviceInfo, map[string]any{
					"id": before["id"], "userId": listener.ID, "deviceId": "ps-device-known", "ipAddress": "127.0.0.1", "clientVersion": "0.11.0",
					"manufacturer": "Google", "model": "Pixel 9", "sdkVersion": "35", "clientName": "Abs Android", "deviceName": "Google Pixel 9",
				})
				requireEqual(t, "updated row", map[string]any{"clientVersion": after["clientVersion"], "deviceName": after["deviceName"], "deviceVersion": after["deviceVersion"], "extraData": after["extraData"]},
					map[string]any{"clientVersion": "0.11.0", "deviceName": "Google Pixel 9", "deviceVersion": "35", "extraData": `{"manufacturer":"Google","model":"Pixel 9"}`})
			},
		},
	}
}

// start2 plays an item as an actor on a device, closes the session and returns its start time.
func start2(t *testing.T, e *harness.Env, who actor, itemID, device string) float64 {
	t.Helper()
	doc := play(t, e, who.Token, itemID, "", playBody{"supportedMimeTypes": []string{"audio/mpeg"}, "deviceInfo": map[string]any{"deviceId": device}})
	closeQuietly(t, e, who.Token, doc.ID)
	return doc.StartTime
}
