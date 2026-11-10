package httputil

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"audiobookshelf-compat/internal/logging"
)

const jsonContentType = "application/json; charset=utf-8"

// WriteJSON writes value as an Express res.json style response for code that
// is not an error-returning Handler (the 401 paths, /ping). It marshals first,
// so a value that cannot be encoded yields a plain 500 instead of a truncated
// body. Handlers use JSON, which adds the ETag and 304.
func WriteJSON(w http.ResponseWriter, r *http.Request, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		logging.Errorf("[httputil] WriteJSON: internal server error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	sendJSON(w, status, data, r.Method == http.MethodHead)
}

// DecodeObjectFields is express.json for a handler that reads named keys: a
// request whose content type is not JSON has an empty body object, and a body
// that is not a JSON object has no keys.
func DecodeObjectFields(r *http.Request) map[string]json.RawMessage {
	body := map[string]json.RawMessage{}
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/json" && !strings.HasSuffix(media, "+json") {
		return body
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
	if err != nil {
		return body
	}
	var parsed map[string]json.RawMessage
	if json.Unmarshal(raw, &parsed) == nil && parsed != nil {
		return parsed
	}
	return body
}

// sendJSON is the one place a marshalled JSON body is framed.
func sendJSON(w http.ResponseWriter, status int, data []byte, head bool) {
	w.Header().Set("Content-Type", jsonContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(status)
	if !head {
		_, _ = w.Write(data)
	}
}

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
