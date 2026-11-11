package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	requestTimeout   = 10 * time.Second
	maxResponseBytes = 8 << 20
)

// getter fetches JSON from a provider with one client.
type getter struct{ client *http.Client }

// body GETs endpoint and returns the body of a 2xx answer. A body over
// maxResponseBytes is an error, never a truncated read.
func (g getter) body(ctx context.Context, endpoint string, header http.Header) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	for name, values := range header {
		request.Header[name] = values
	}
	response, err := g.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("provider returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxResponseBytes {
		return nil, errors.New("provider response too large")
	}
	return body, nil
}

// json GETs endpoint into target. A field of another type than target expects
// stays zero, as an absent one does: providers drift, and one odd field must
// not lose the whole answer.
func (g getter) json(ctx context.Context, endpoint string, target any) error {
	body, err := g.body(ctx, endpoint, nil)
	if err != nil {
		return err
	}
	var mistyped *json.UnmarshalTypeError
	if err := json.Unmarshal(body, target); err != nil && !errors.As(err, &mistyped) {
		return err
	}
	return nil
}

// rawText is the text of a JSON string or number, "" for anything else.
func rawText(raw json.RawMessage) string {
	var text looseText
	_ = text.UnmarshalJSON(raw) // never fails
	return string(text)
}

// before is s up to the first separator, or all of s.
func before(s, separator string) string {
	head, _, _ := strings.Cut(s, separator)
	return head
}

// uniqueTrimmed trims values and drops empty and repeated ones, in order.
func uniqueTrimmed(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

// jsonString is s as a JSON string, for an ID field; empty s is no ID.
func jsonString(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	raw, _ := json.Marshal(s) // a string always marshals
	return raw
}
