package playback_streams

import (
	"encoding/json"
	"net/http"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// Fixture facts (go-backend/internal/contracttest/testdata/install). Item ids
// come from Env.IDs; durations and titles are the seeded rows.
const (
	saga1Duration = 60.186123 // "The Beginning", three mp3 files of 20.062041 s
	novelDuration = 45.0      // "Single File Novel", one m4b file
	segmentLength = 6
	saga1Segments = 11 // ceil(60.186123 / 6)
	novelSegments = 8  // ceil(45 / 6)
	userPassword  = "streams-packet-password"
)

// Scenario sources.
const (
	hlsRouterSrc     = "server/routers/HlsRouter.js:48-96"
	hlsStreamSrc     = "server/objects/Stream.js:125-145"
	publicRouterSrc  = "server/routers/PublicRouter.js:13-22"
	getTrackSrc      = "server/controllers/SessionController.js:279-336"
	sessionManager   = "server/managers/PlaybackSessionManager.js:313-364"
	sessionCloseSrc  = "server/managers/PlaybackSessionManager.js:421-431"
	sessionRemoveSrc = "server/managers/PlaybackSessionManager.js:441-456"
	listeningTimeSrc = "server/managers/PlaybackSessionManager.js:387-390"
	streamStartSrc   = "server/objects/Stream.js:234-356"
	streamCheckSrc   = "server/objects/Stream.js:153-232"
	streamCloseSrc   = "server/objects/Stream.js:358-379"
	streamResetSrc   = "server/objects/Stream.js:397-414"
	socketClientSrc  = "server/SocketAuthority.js:51-78"
	webSocketSrc     = "client/layouts/default.vue:156-174,419-424"
	webPlayerSrc     = "client/components/app/MediaPlayerContainer.vue:419-466"
	audioTrackSrc    = "client/players/AudioTrack.js:1-36"
	hlsPlaylistGen   = "server/utils/generators/hlsPlaylistGenerator.js:9-18"
	playMethodSrc    = "server/managers/PlaybackSessionManager.js:321-322"
	segmentParseSrc  = "server/routers/HlsRouter.js:23-27"
	streamPayloadSrc = "server/objects/Stream.js:110-123"
	serverRoutesSrc  = "server/Server.js:353-356"
)

// caseDoc is a playback session as the play route returns it.
type caseDoc struct {
	ID          string   `json:"id"`
	UserID      string   `json:"userId"`
	PlayMethod  *int     `json:"playMethod"`
	StartTime   float64  `json:"startTime"`
	Duration    *float64 `json:"duration"`
	AudioTracks []track  `json:"audioTracks"`
}

// track is one audio track of a session.
type track struct {
	Index    int     `json:"index"`
	Duration float64 `json:"duration"`
	Title    string  `json:"title"`
	MimeType string  `json:"mimeType"`
	Codec    *string `json:"codec"`
	URL      string  `json:"contentUrl"`
	Metadata *struct {
		Filename string `json:"filename"`
		Path     string `json:"path"`
	} `json:"metadata"`
}

// streamOpenDoc is the stream_open payload (Stream.toJSON).
type streamOpenDoc struct {
	ID          string  `json:"id"`
	UserID      string  `json:"userId"`
	LibraryItem itemDoc `json:"libraryItem"`
	Episode     *struct {
		ID string `json:"id"`
	} `json:"episode"`
	SegmentLength       int     `json:"segmentLength"`
	PlaylistPath        string  `json:"playlistPath"`
	ClientPlaylistURI   string  `json:"clientPlaylistUri"`
	StartTime           float64 `json:"startTime"`
	SegmentStartNumber  int     `json:"segmentStartNumber"`
	IsTranscodeComplete bool    `json:"isTranscodeComplete"`
}

// itemDoc is the expanded library item the stream payload carries.
type itemDoc struct {
	ID        string `json:"id"`
	LibraryID string `json:"libraryId"`
	MediaType string `json:"mediaType"`
	Media     struct {
		ID       string `json:"id"`
		Metadata struct {
			Title string `json:"title"`
		} `json:"metadata"`
	} `json:"media"`
}

// progressDoc is the stream_progress payload. Chunks holds a number for a
// single created segment and a "first-last" string for a run of segments, as
// the client's setChunksReady handles both (PlayerUi.vue:290-301).
type progressDoc struct {
	Stream      string `json:"stream"`
	Percent     string `json:"percent"`
	Chunks      []any  `json:"chunks"`
	NumSegments int    `json:"numSegments"`
}

// resetDoc is the stream_reset payload.
type resetDoc struct {
	StartTime float64 `json:"startTime"`
	StreamID  string  `json:"streamId"`
}

// errorDoc is the stream_error payload.
type errorDoc struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

// actor is a user with its token and id.
type actor struct {
	Name  string
	Token string
	ID    string
}

// fixtureActor returns a fixture user of fixture.json.
func fixtureActor(t *testing.T, e *harness.Env, name string) actor {
	t.Helper()
	token := e.Token(t, name)
	resp := e.Get(t, token, "/api/me")
	requireStatus(t, resp, http.StatusOK, "GET /api/me")
	var me struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	}
	resp.JSON(t, &me)
	return actor{Name: me.Username, Token: token, ID: me.ID}
}

// createUser creates a normal user through the admin API and logs it in.
func createUser(t *testing.T, e *harness.Env, username string) actor {
	t.Helper()
	body := map[string]any{"username": username, "password": userPassword, "isActive": true, "type": "user"}
	requireStatus(t, e.Post(t, e.Token(t, "root"), "/api/users", body), http.StatusOK, "create user "+username)
	token, user := e.Login(t, username, userPassword)
	id, _ := user["id"].(string)
	if token == "" || id == "" {
		t.Fatalf("login of %s returned no token or id", username)
	}
	return actor{Name: username, Token: token, ID: id}
}

// setProgress writes the user's resume position of an item through the API.
func setProgress(t *testing.T, e *harness.Env, who actor, itemID string, currentTime, duration float64) {
	t.Helper()
	body := map[string]any{"currentTime": currentTime, "duration": duration}
	requireStatus(t, e.Patch(t, who.Token, "/api/me/progress/"+itemID, body), http.StatusOK, "set progress")
}

// playBody is the body of a play request.
type playBody map[string]any

// androidDevice is a client device description of the official Android app.
func androidDevice(deviceID string) map[string]any {
	return map[string]any{"deviceId": deviceID, "clientVersion": "0.10.0", "manufacturer": "Google", "model": "Pixel 8", "sdkVersion": 34}
}

// transcodeBody forces a transcode for a player that supports mp3.
func transcodeBody(deviceID string) playBody {
	return playBody{
		"deviceInfo":         androidDevice(deviceID),
		"mediaPlayer":        "exo-player",
		"forceTranscode":     true,
		"supportedMimeTypes": []string{"audio/mpeg"},
	}
}

// directBody requests direct play for a player that supports the mime types.
func directBody(deviceID string, mimeTypes ...string) playBody {
	return playBody{
		"deviceInfo":         androidDevice(deviceID),
		"mediaPlayer":        "exo-player",
		"supportedMimeTypes": mimeTypes,
	}
}

// play starts a session and decodes the answer.
func play(t *testing.T, e *harness.Env, token, itemID, episodeID string, body playBody) caseDoc {
	t.Helper()
	path := "/api/items/" + itemID + "/play"
	if episodeID != "" {
		path += "/" + episodeID
	}
	resp := e.Post(t, token, path, body)
	requireStatus(t, resp, http.StatusOK, "POST "+path)
	var doc caseDoc
	resp.JSON(t, &doc)
	if doc.ID == "" {
		t.Fatalf("play returned no session id: %.300s", resp.Body)
	}
	return doc
}

// startStream starts a transcoded session and fails unless the server chose the
// HLS track (playMethod 2, one contentUrl track).
func startStream(t *testing.T, e *harness.Env, who actor, itemID, episodeID, device string) caseDoc {
	t.Helper()
	doc := play(t, e, who.Token, itemID, episodeID, transcodeBody(device))
	if doc.PlayMethod == nil || *doc.PlayMethod != 2 {
		t.Fatalf("%s: playMethod %v, want 2 (transcode)", device, doc.PlayMethod)
	}
	if len(doc.AudioTracks) != 1 || doc.AudioTracks[0].URL != hlsPath(doc.ID, "output.m3u8") {
		t.Fatalf("%s: transcode track %+v", device, doc.AudioTracks)
	}
	return doc
}

// startDirect starts a direct-play session and fails unless the server chose
// one track per audio file.
func startDirect(t *testing.T, e *harness.Env, who actor, itemID, episodeID, device string, mimeTypes ...string) caseDoc {
	t.Helper()
	doc := play(t, e, who.Token, itemID, episodeID, directBody(device, mimeTypes...))
	if doc.PlayMethod == nil || *doc.PlayMethod != 0 {
		t.Fatalf("%s: playMethod %v, want 0 (direct play)", device, doc.PlayMethod)
	}
	if len(doc.AudioTracks) == 0 {
		t.Fatalf("%s: direct play lists no tracks", device)
	}
	return doc
}

// closeSession closes a session through the API.
func closeSession(t *testing.T, e *harness.Env, token, id string) {
	t.Helper()
	requireStatus(t, e.Post(t, token, "/api/session/"+id+"/close", map[string]any{}), http.StatusOK, "close "+id)
}

// closeQuietly closes a session at the end of a scenario.
func closeQuietly(t *testing.T, e *harness.Env, token, id string) {
	t.Helper()
	closeSession(t, e, token, id)
}

// requireStatus fails unless the response has the status.
func requireStatus(t *testing.T, resp harness.Response, status int, what string) {
	t.Helper()
	if resp.Status != status {
		t.Fatalf("%s: want %d, got %d %.300s", what, status, resp.Status, resp.Body)
	}
}

// requireEqual compares two values by their JSON encoding.
func requireEqual(t *testing.T, what string, got, want any) {
	t.Helper()
	g, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("%s: encode got: %v", what, err)
	}
	w, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("%s: encode want: %v", what, err)
	}
	if string(g) != string(w) {
		t.Fatalf("%s:\n got %s\nwant %s", what, g, w)
	}
}

// scenario is the constructor used by the group tables.
func scenario(requirement, invariant, caseName, state, assertion string, sources []string, claim string, run func(t *testing.T, e *harness.Env)) harness.Scenario {
	return harness.Scenario{
		Requirement: requirement,
		Invariant:   invariant,
		Case:        caseName,
		State:       state,
		Assertion:   assertion,
		Sources:     sources,
		Claim:       claim,
		Run:         run,
	}
}
