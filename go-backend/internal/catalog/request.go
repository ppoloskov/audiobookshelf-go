package catalog

import (
	"audiobookshelf-compat/internal/jsonvalue"
	"audiobookshelf-compat/internal/store"
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

func requestUser(r *http.Request) store.User {
	user, _ := store.UserFromContext(r.Context())
	return user
}

func sliceHas(list []string, v string) bool {
	for _, e := range list {
		if e == v {
			return true
		}
	}
	return false
}

// jsonBodyObject is express.json for a handler that reads named keys: a
// request whose content type is not JSON has an empty body object, and a body
// that is not a JSON object has no keys.
func jsonBodyObject(r *http.Request) map[string]json.RawMessage {
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

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

var uuidPattern = regexp.MustCompile(`(?i)^(?:[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}|00000000-0000-0000-0000-000000000000|ffffffff-ffff-ffff-ffff-ffffffffffff)$`)

// isUUID is utils.isUUID, uuid.validate.
func isUUID(s string) bool { return uuidPattern.MatchString(s) }

func requestString(data map[string]json.RawMessage, key string) (any, bool) {
	raw, ok := data[key]
	if !ok {
		return nil, false
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String(), true
	}
	return nil, false
}

func requestBool(data map[string]json.RawMessage, key string) (bool, bool) {
	raw, ok := data[key]
	if !ok {
		return false, false
	}
	var b bool
	if json.Unmarshal(raw, &b) != nil {
		return false, false
	}
	return b, true
}

func requestStringArray(data map[string]json.RawMessage, key string) ([]string, bool) {
	raw, ok := data[key]
	if !ok {
		return nil, false
	}
	var values []string
	if json.Unmarshal(raw, &values) != nil {
		return nil, false
	}
	if values == nil {
		return nil, false
	}
	return values, true
}

func jsonValue(value any) string { raw, _ := json.Marshal(value); return string(raw) }

// jsonBodyArray is express.json for a handler that expects a JSON array.
func jsonBodyArray(r *http.Request) ([]json.RawMessage, bool) {
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/json" && !strings.HasSuffix(media, "+json") {
		return nil, false
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
	if err != nil {
		return nil, false
	}
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil {
		return nil, false
	}
	return values, true
}

// bodyStringList reads a body value that is an array, keeping its string
// entries; ok is false for anything that is not a non-empty array
// (`!ids?.length || !Array.isArray(ids)`).
func bodyStringList(raw json.RawMessage) ([]string, bool) {
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil || len(values) == 0 {
		return nil, false
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		var s string
		if json.Unmarshal(value, &s) == nil {
			out = append(out, s)
		}
	}
	return out, true
}

// looselyOne is `value == 1` for a query string (LibraryItemController.js:
// 565, req.query.hard == 1): the string converts to a number.
func looselyOne(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	n, err := strconv.ParseFloat(trimmed, 64)
	return err == nil && n == 1
}

// jsTruthyRaw is JavaScript truthiness of a JSON value (absent counts as undefined).
func jsTruthyRaw(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var v any
	return json.Unmarshal(raw, &v) == nil && jsonvalue.Truthy(v)
}

// matchString is a match field as a string: strings as they are, numbers as JS prints them.
func matchString(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	if v == nil {
		return ""
	}
	return jsonvalue.Text(v)
}

// matchList is `Array.isArray(v) ? [...v] : v.split(',').map(trim).filter(Boolean)`.
func matchList(raw json.RawMessage) []string {
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var out []string
	for _, part := range strings.Split(matchString(raw), ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
