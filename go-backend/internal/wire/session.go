package wire

// AddPlaybackSessionSpreadFields adds the non-persisted properties of the
// legacy PlaybackSession object (server/objects/PlaybackSession.js:38-44).
// They appear in responses that object-spread a session instance instead of
// calling toJSON, namely sessions with an attached user in
// SessionController.getAllWithUserData (server/controllers/SessionController.js:89)
// and UserController.getListeningSessions (server/controllers/UserController.js:442).
func AddPlaybackSessionSpreadFields(session map[string]any) {
	session["lastSave"] = 0
	session["audioTracks"] = []any{}
	session["stream"] = nil
	session["shareSessionId"] = nil
	session["mediaItemShareId"] = nil
	session["coverAspectRatio"] = nil
}
