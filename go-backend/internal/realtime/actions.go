package realtime

import (
	"encoding/json"
	"log/slog"
)

// Reply sends an event back to the socket that made a request, possibly later
// and more than once (cover search streams results).
type Reply func(event string, payload any)

// CoverSearch is the payload of search_covers.
type CoverSearch struct {
	RequestID string `json:"requestId"`
	Title     string `json:"title"`
	Author    string `json:"author"`
	Provider  string `json:"provider"`
	Podcast   bool   `json:"podcast"`
}

// Actions is what the server does for the events clients send. The hub checks
// authentication, administrator rights and the shape of each payload first.
type Actions interface {
	// CancelScan stops the scan of a library.
	CancelScan(libraryID string)
	// SetLogListener streams log entries at or above level (0..6) to the socket.
	SetLogListener(socketID string, level int)
	// RemoveLogListener stops the log stream; it also runs for every closed socket.
	RemoveLogListener(socketID string)
	// SearchCovers starts a cover search and answers through reply.
	SearchCovers(search CoverSearch, reply Reply)
	// CancelCoverSearch stops the cover search of a request.
	CancelCoverSearch(requestID string, reply Reply)
}

// decode reads the first argument of a client event as T. A missing, null or
// mismatched argument is ignored with a debug log: clients cannot be told
// anything useful about a payload they should not have sent.
func decode[T any](socketID, event string, args []any) (T, bool) {
	var value T
	raw := first(args)
	if raw == nil {
		slog.Debug("socket event without payload ignored", "socket", socketID, "event", event)
		return value, false
	}
	body, err := json.Marshal(raw) // raw came from JSON, so this cannot fail
	if err == nil {
		err = json.Unmarshal(body, &value)
	}
	if err != nil {
		slog.Debug("socket event with malformed payload ignored", "socket", socketID, "event", event, "error", err)
		return value, false
	}
	return value, true
}
