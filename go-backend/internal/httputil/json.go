package httputil

import (
	"encoding/json"
	"net/http"
	"strconv"

	"audiobookshelf-compat/internal/logging"
)

const jsonContentType = "application/json; charset=utf-8"

// WriteJSON writes value as an Express res.json style response. It marshals
// first, so a value that cannot be encoded yields a plain 500 instead of a
// truncated body.
func WriteJSON(w http.ResponseWriter, status int, value any, opts ...Option) {
	o := apply(opts)
	if o.streamed {
		w.Header().Set("Content-Type", jsonContentType)
		w.WriteHeader(status)
		if o.head() {
			return
		}
		_ = json.NewEncoder(w).Encode(value)
		return
	}
	data, err := json.Marshal(value)
	if err != nil {
		logging.Errorf("[httputil] WriteJSON: internal server error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", jsonContentType)
	if !o.noContentLength {
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	}
	w.WriteHeader(status)
	if !o.head() {
		_, _ = w.Write(data)
	}
}
