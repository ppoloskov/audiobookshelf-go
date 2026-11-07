package providers

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"audiobookshelf-compat/internal/textclean"
)

// matchCore is what a custom provider may return for a book; every field
// decodes leniently (see text, num, list, seriesList) and a field it could not
// read is left out.
type matchCore struct {
	Title         text       `json:"title,omitempty"`
	Subtitle      text       `json:"subtitle,omitempty"`
	Author        text       `json:"author,omitempty"`
	Narrator      text       `json:"narrator,omitempty"`
	Publisher     text       `json:"publisher,omitempty"`
	PublishedYear year       `json:"publishedYear,omitzero"`
	Description   text       `json:"description,omitempty"`
	Cover         text       `json:"cover,omitempty"`
	ISBN          text       `json:"isbn,omitempty"`
	ASIN          text       `json:"asin,omitempty"`
	Genres        list       `json:"genres,omitempty"`
	Tags          list       `json:"tags,omitempty"`
	Series        seriesList `json:"series,omitempty"`
	Language      text       `json:"language,omitempty"`
	Duration      num        `json:"duration,omitzero"`
}

// bookMatch is one book search result of any provider: matchCore plus what
// the built-in providers add. An empty field is left out of the JSON.
type bookMatch struct {
	matchCore
	DescriptionPlain string   `json:"descriptionPlain,omitempty"`
	ID               any      `json:"id,omitempty"`
	ArtistID         any      `json:"artistId,omitempty"`
	Key              string   `json:"key,omitempty"`
	Edition          string   `json:"edition,omitempty"`
	Covers           []string `json:"covers,omitempty"`
	FirstPublishDate string   `json:"first_publish_date,omitempty"`
	Region           string   `json:"region,omitempty"`
	Rating           string   `json:"rating,omitempty"`
	Abridged         *bool    `json:"abridged,omitempty"`
	Explicit         *bool    `json:"explicit,omitempty"`
}

// sanitize cleans the description and derives its plain-text form.
func (m *bookMatch) sanitize() {
	if m.Description != "" {
		clean := textclean.Sanitize(string(m.Description))
		m.Description, m.DescriptionPlain = text(clean), textclean.Strip(clean)
	}
}

type seriesEntry struct {
	Series   string `json:"series"`
	Sequence text   `json:"sequence,omitempty"`
}

// text is a string that reads a number as its decimal form and a list of
// scalars as their comma join (a JSON object is read by its "value").
type text string

type list []string

type seriesList []seriesEntry

// year is a published year: a string, or the number a provider (FantLab) sends.
type year struct{ v any }

func (y year) IsZero() bool                 { return y.v == nil || y.v == "" }
func (y year) MarshalJSON() ([]byte, error) { return json.Marshal(y.v) }
func (y *year) UnmarshalJSON(b []byte) error {
	var t text
	_ = t.UnmarshalJSON(b)
	if t != "" {
		y.v = string(t)
	}
	return nil
}

// num is a number that also reads a numeric string; the zero value is absent.
type num struct {
	v  float64
	ok bool
}

func (n num) IsZero() bool                 { return !n.ok }
func (n num) MarshalJSON() ([]byte, error) { return json.Marshal(n.v) }

func stringValue(v any) (string, bool) {
	switch value := v.(type) {
	case string:
		return value, true
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64), true
	case map[string]any:
		return stringValue(value["value"])
	case []any:
		parts := make([]string, len(value))
		for i, item := range value {
			part, ok := stringValue(item)
			if !ok {
				return "", false
			}
			parts[i] = part
		}
		return strings.Join(parts, ","), true
	}
	return "", false
}

// The unmarshalers below never fail: a value of the wrong shape is left out.

func (t *text) UnmarshalJSON(b []byte) error {
	var v any
	_ = json.Unmarshal(b, &v)
	s, _ := stringValue(v)
	*t = text(s)
	return nil
}

func (n *num) UnmarshalJSON(b []byte) error {
	var v any
	_ = json.Unmarshal(b, &v)
	switch v := v.(type) {
	case float64:
		*n = num{v, true}
	case string:
		if f, err := strconv.ParseFloat(v, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			*n = num{f, true}
		}
	}
	return nil
}

// UnmarshalJSON reads a comma separated string or an array of strings, trimmed and unique.
func (l *list) UnmarshalJSON(b []byte) error {
	var v any
	_ = json.Unmarshal(b, &v)
	var parts []string
	switch v := v.(type) {
	case string:
		parts = strings.Split(v, ",")
	case []any:
		for _, part := range v {
			s, ok := part.(string)
			if !ok {
				return nil
			}
			parts = append(parts, s)
		}
	}
	*l = uniqueStrings(parts)
	return nil
}

// UnmarshalJSON keeps the entries that name a series.
func (s *seriesList) UnmarshalJSON(b []byte) error {
	var entries []seriesEntry
	_ = json.Unmarshal(b, &entries)
	for _, entry := range entries {
		if entry.Series != "" {
			*s = append(*s, entry)
		}
	}
	return nil
}
