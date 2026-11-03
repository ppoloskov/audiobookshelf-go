package contracttest

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
)

// journeyCtx is what the journey's earlier steps left behind: responses kept
// under a capture name, the login tokens, and the two local addresses the
// steps need (the image source and a closed port).
type journeyCtx struct {
	vals            map[string]any
	missing         []string
	tokens          map[string]string
	listenerID      string
	listenerRefresh string
	fixtureURL      string
	deadURL         string
}

// v walks the value captured under name: string elements index objects, int
// elements index arrays. A capture no step stored is recorded in c.missing, so
// the step that needs it fails with a message naming it.
func (c *journeyCtx) v(name string, path ...any) any {
	val, ok := c.vals[name]
	if !ok {
		c.missing = append(c.missing, name)
		return nil
	}
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, _ := val.(map[string]any)
			val = m[k]
		case int:
			a, _ := val.([]any)
			if k < len(a) {
				val = a[k]
			} else {
				val = nil
			}
		}
	}
	return val
}

// s is v as a string: the form route paths and ids need. A null is "".
func (c *journeyCtx) s(name string, path ...any) string {
	switch x := c.v(name, path...).(type) {
	case nil:
		return ""
	case string:
		return x
	default:
		return fmt.Sprint(x)
	}
}

// find returns idField of the first element of the list captured under name
// at listKey whose matchField equals value.
func (c *journeyCtx) find(name, listKey, matchField, value, idField string) string {
	list, _ := c.v(name, listKey).([]any)
	for _, e := range list {
		m, _ := e.(map[string]any)
		if m[matchField] == value {
			return fmt.Sprint(m[idField])
		}
	}
	c.missing = append(c.missing, fmt.Sprintf("%s.%s with %s=%q", name, listKey, matchField, value))
	return ""
}

func (c *journeyCtx) authorID(name string) string {
	return c.find("authorList", "authors", "name", name, "id")
}

func (c *journeyCtx) seriesID(name string) string {
	return c.find("seriesList", "results", "name", name, "id")
}

// seededFeedID is the id of the feed the seed opened for entityID.
func (c *journeyCtx) seededFeedID(entityID string) string {
	return c.find("feedsSeeded", "feeds", "entityId", entityID, "id")
}

// contentRoute is a stream URL the play response of session name handed out.
func contentRoute(name string, track int) func(*journeyCtx) string {
	return func(c *journeyCtx) string { return c.s(name, "audioTracks", track, "contentUrl") }
}

// sessionRoute is /api/session/<id of the playback session name><suffix>.
func sessionRoute(name, suffix string) func(*journeyCtx) string {
	return func(c *journeyCtx) string { return "/api/session/" + c.s(name, "id") + suffix }
}

// authorRoute is /api/authors/<id of the author called name><suffix>.
func authorRoute(name, suffix string) func(*journeyCtx) string {
	return func(c *journeyCtx) string { return "/api/authors/" + c.authorID(name) + suffix }
}

// seriesRoute is /api/series/<id of the series called name><suffix>.
func seriesRoute(name, suffix string) func(*journeyCtx) string {
	return func(c *journeyCtx) string { return "/api/series/" + c.seriesID(name) + suffix }
}

// subseriesRoute is a library item page filtered to one series. The filter is
// series.<base64 of the series id>, escaped as encodeURIComponent escapes it.
func subseriesRoute(libraryID, name, query string) func(*journeyCtx) string {
	return func(c *journeyCtx) string {
		filter := "series." + base64.StdEncoding.EncodeToString([]byte(c.seriesID(name)))
		return "/api/libraries/" + libraryID + "/items?minified=1&collapseseries=1&filter=" + url.QueryEscape(filter) + query
	}
}

// fixtureBody posts the URL of a file on the image source.
func fixtureBody(file string) func(*journeyCtx) any {
	return func(c *journeyCtx) any { return map[string]any{"url": c.fixtureURL + "/" + file} }
}

// matchBody is a quick-match request for the custom provider the journey
// created, plus extra fields.
func matchBody(extra map[string]any) func(*journeyCtx) any {
	return func(c *journeyCtx) any {
		body := map[string]any{"provider": "custom-" + c.s("matchProvider", "provider", "id")}
		for k, v := range extra {
			body[k] = v
		}
		return body
	}
}

// bookID is the media id of the item the journey fetched as item_<key>.
func bookID(c *journeyCtx, key string) string {
	return c.s("item_"+key, "media", "id")
}

// shareBody is a media item share of the book key, with its slug derived from
// key unless extra replaces it.
func shareBody(key string, extra map[string]any) func(*journeyCtx) any {
	return func(c *journeyCtx) any {
		body := map[string]any{"slug": "life-share-" + key, "mediaItemType": "book", "mediaItemId": bookID(c, key), "expiresAt": 0}
		for k, v := range extra {
			body[k] = v
		}
		return body
	}
}

// androidDevice is the deviceInfo the trial's Android client sends.
func androidDevice() map[string]any {
	return map[string]any{"deviceId": "trial-android-0001", "manufacturer": "Google", "model": "Pixel 8", "sdkVersion": 34,
		"clientName": "Abs Android", "clientVersion": "0.9.79-beta"}
}

// mediaType is the media type of a Content-Type header: the part before any
// parameters, lower-cased.
func mediaType(header string) string {
	media, _, _ := strings.Cut(header, ";")
	return strings.ToLower(strings.TrimSpace(media))
}
