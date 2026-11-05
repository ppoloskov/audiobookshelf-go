package httputil

import (
	"encoding/json"
	"errors"
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

// DecodeObjectRequest decodes one JSON object from a request body.
func DecodeObjectRequest(r *http.Request) (map[string]any, error) {
	defer r.Body.Close()
	var out map[string]any
	d := json.NewDecoder(r.Body)
	d.UseNumber()
	if err := d.Decode(&out); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, errors.New("body must be an object")
	}
	return out, nil
}
