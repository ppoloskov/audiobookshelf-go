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
	var data []byte
	var err error
	if o.keys != nil {
		data, err = marshalOrdered(value, o.keys)
	} else {
		data, err = json.Marshal(value)
	}
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

func marshalOrdered(value any, keys []string) ([]byte, error) {
	values, ok := value.(map[string]any)
	if !ok {
		return json.Marshal(value)
	}
	data := make([]byte, 0, 256)
	data = append(data, '{')
	first := true
	for _, key := range keys {
		item, ok := values[key]
		if !ok {
			continue
		}
		if !first {
			data = append(data, ',')
		}
		first = false
		name, _ := json.Marshal(key)
		encoded, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		data = append(data, name...)
		data = append(data, ':')
		data = append(data, encoded...)
	}
	return append(data, '}'), nil
}
