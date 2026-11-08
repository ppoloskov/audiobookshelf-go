package playback

import "audiobookshelf-compat/internal/wire"

// ProgressUpdatedEvent is the user_item_progress_updated socket payload
// (PlaybackSessionManager.js:402): the stored progress with the session and
// device that produced it.
type ProgressUpdatedEvent struct {
	ID                string             `json:"id"`
	SessionID         string             `json:"sessionId"`
	DeviceDescription string             `json:"deviceDescription"`
	Data              wire.MediaProgress `json:"data"`
}
