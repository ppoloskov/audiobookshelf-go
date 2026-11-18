package browse

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	srcUpdatedEmit = "server/controllers/SeriesController.js:84"
	srcAddedEmit   = "server/scanner/Scanner.js:316"
	srcRemovedAPI  = "server/routers/ApiRouter.js:453"
	srcRemovedScan = "server/scanner/BookScanner.js:1006"
)

// seriesEvent is the payload of series_added and series_updated.
type seriesEvent struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	NameIgnore  string  `json:"nameIgnorePrefix"`
	Description *string `json:"description"`
	AddedAt     int64   `json:"addedAt"`
	UpdatedAt   int64   `json:"updatedAt"`
	LibraryID   string  `json:"libraryId"`
}

func decodeSeriesEvent(t *testing.T, raw json.RawMessage) seriesEvent {
	t.Helper()
	var event seriesEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatalf("series event: %v: %s", err, raw)
	}
	return event
}

// removedEvent is the payload of series_removed.
func expectRemovedPayload(t *testing.T, e *harness.Env, raw json.RawMessage, id string) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload) != 2 || payload["id"] != id || payload["libraryId"] != e.IDs[bookLibrary] {
		t.Fatalf("series_removed payload: %s", raw)
	}
}

func (s *suite) updatedEventScenarios() []harness.Scenario {
	const name = "socket-outgoing-series-updated"
	return []harness.Scenario{
		{
			Requirement: name, Invariant: "INV-SOCKET-OUTGOING-SERIES-UPDATED", Case: "normal", State: "initial", Assertion: "series-updated-event-payload-and-audience",
			Sources: []string{srcUpdatedEmit, "server/SocketAuthority.js:57-65"},
			Claim:   "A PATCH that changes a series emits series_updated with the full series object to every authenticated socket (admin, user, library-restricted) and not to a connection that never authenticated.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				id := seriesIDByName(t, e, long)
				audience := s.connect(t, e)
				resp := patchSeries(t, e, e.Token(t, "root"), id, `{"name":"Long Saga Events","description":"first"}`)
				mustOK(t, "patch", resp)
				var reply seriesJSON
				resp.JSON(t, &reply)
				event := decodeSeriesEvent(t, audience.expect(t, "series_updated", idIs(id)))
				// The client replaces its series object with the payload, so it must equal the response.
				if event.Name != reply.Name || event.NameIgnore != reply.NameIgnore || event.NameIgnore != "Long Saga Events" || event.AddedAt != reply.AddedAt || event.UpdatedAt != reply.UpdatedAt || event.LibraryID != reply.LibraryID {
					t.Fatalf("payload %+v differs from the response %+v", event, reply)
				}
				if event.Description == nil || *event.Description != "first" || event.AddedAt <= 0 {
					t.Fatalf("payload: %+v", event)
				}
				listed := getSeries(t, e, e.Token(t, "listener"), detailPath(e, id), nil)
				if listed.AddedAt != event.AddedAt || listed.UpdatedAt != event.UpdatedAt {
					t.Fatalf("event timestamps %d/%d differ from the series read %d/%d", event.AddedAt, event.UpdatedAt, listed.AddedAt, listed.UpdatedAt)
				}
			},
		},
		{
			Requirement: name, Invariant: "INV-SOCKET-OUTGOING-SERIES-UPDATED", Case: "edge", State: "repeat", Assertion: "series-updated-event-not-repeated",
			Sources: []string{srcUpdatedEmit},
			Claim:   "Repeating a PATCH with unchanged values answers 200 but emits no series_updated.",
			Run: func(t *testing.T, e *harness.Env) {
				id := seriesIDByName(t, e, "Long Saga Events")
				audience := s.connect(t, e)
				mustOK(t, "repeat", patchSeries(t, e, e.Token(t, "root"), id, `{"name":"Long Saga Events","description":"first"}`))
				audience.expectNone(t, "series_updated", nil)
			},
		},
		{
			Requirement: name, Invariant: "INV-SOCKET-OUTGOING-SERIES-UPDATED", Case: "normal", State: "changed", Assertion: "series-updated-event-after-further-change",
			Sources: []string{srcUpdatedEmit},
			Claim:   "A later change of only the description emits a new series_updated carrying the unchanged name and the new description.",
			Run: func(t *testing.T, e *harness.Env) {
				id := seriesIDByName(t, e, "Long Saga Events")
				audience := s.connect(t, e)
				mustOK(t, "patch", patchSeries(t, e, e.Token(t, "root"), id, `{"description":"second"}`))
				event := decodeSeriesEvent(t, audience.expect(t, "series_updated", idIs(id)))
				if event.Name != "Long Saga Events" || event.Description == nil || *event.Description != "second" {
					t.Fatalf("payload: %+v", event)
				}
				audience.mark()
				mustOK(t, "restore", patchSeries(t, e, e.Token(t, "root"), id, `{"name":"Long Saga","description":""}`))
				restored := decodeSeriesEvent(t, audience.expect(t, "series_updated", idIs(id)))
				if restored.Name != "Long Saga" || restored.Description == nil || *restored.Description != "" {
					t.Fatalf("restore payload: %+v", restored)
				}
			},
		},
		{
			Requirement: name, Invariant: "INV-SOCKET-OUTGOING-SERIES-UPDATED", Case: "error", State: "failure", Assertion: "series-updated-event-not-sent-on-rejection",
			Sources: []string{srcUpdatedEmit, srcSeriesMiddle},
			Claim:   "A rejected PATCH (forbidden, invalid body, unknown series) emits no series_updated.",
			Run: func(t *testing.T, e *harness.Env) {
				id := seriesIDByName(t, e, long)
				audience := s.connect(t, e)
				expectStatus(t, "forbidden", patchSeries(t, e, e.Token(t, "listener"), id, `{"name":"No"}`), http.StatusForbidden)
				expectStatus(t, "invalid", patchSeries(t, e, e.Token(t, "root"), id, `{"name":1}`), http.StatusBadRequest)
				expectStatus(t, "unknown", patchSeries(t, e, e.Token(t, "root"), "00000000-0000-4000-8000-000000000000", `{"name":"No"}`), http.StatusNotFound)
				audience.expectNone(t, "series_updated", nil)
			},
		},
	}
}

func (s *suite) addedEventScenarios() []harness.Scenario {
	const name = "socket-outgoing-series-added"
	matchOne := func(series string) []map[string]any {
		return []map[string]any{{"title": "Hard Delete Me", "series": []map[string]string{{"series": series, "sequence": "1"}}}}
	}
	return []harness.Scenario{
		{
			Requirement: name, Invariant: "INV-SOCKET-OUTGOING-SERIES-ADDED", Case: "normal", State: "initial", Assertion: "series-added-event-from-quick-match",
			Sources: []string{srcAddedEmit, "server/controllers/LibraryItemController.js:526-545"},
			Claim:   "A quick match that creates a new series emits series_added with the new series object to every authenticated socket and not to an unauthenticated one.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				provider := s.provider(t, e)
				setBookSeries(t, e, "disposable", `[]`)
				provider.setMatches(matchOne("Matched Saga"))
				audience := s.connect(t, e)
				resp := s.match(t, e, e.Token(t, "root"), "disposable", false)
				mustOK(t, "match", resp)
				event := decodeSeriesEvent(t, audience.expect(t, "series_added", nameIs("Matched Saga")))
				if event.ID == "" || event.NameIgnore != "Matched Saga" || event.LibraryID != e.IDs[bookLibrary] || event.Description != nil {
					t.Fatalf("payload: %+v", event)
				}
				read := getSeries(t, e, e.Token(t, "listener"), detailPath(e, event.ID), nil)
				if event.AddedAt <= 0 || event.AddedAt != read.AddedAt || event.UpdatedAt != read.UpdatedAt || event.NameIgnore != read.NameIgnore {
					t.Fatalf("payload %+v differs from the stored series %+v", event, read)
				}
				expectRow(t, e, event.ID, "Matched Saga", "")
				if id := seriesIDByName(t, e, "Matched Saga"); id != event.ID {
					t.Fatalf("series list id %s, event id %s", id, event.ID)
				}
			},
		},
		{
			Requirement: name, Invariant: "INV-SOCKET-OUTGOING-SERIES-ADDED", Case: "edge", State: "repeat", Assertion: "series-added-event-not-repeated-for-existing-series",
			Sources: []string{srcAddedEmit, "server/scanner/Scanner.js:283-286"},
			Claim:   "Matching the same book again, also with overrideDetails, naming the existing series emits no series_added and creates no second row.",
			Run: func(t *testing.T, e *harness.Env) {
				provider := s.provider(t, e)
				provider.setMatches(matchOne("Matched Saga"))
				audience := s.connect(t, e)
				mustOK(t, "same book again", s.match(t, e, e.Token(t, "root"), "disposable", false))
				audience.mark()
				mustOK(t, "existing series by name with override", s.match(t, e, e.Token(t, "root"), "disposable", true))
				audience.expectNone(t, "series_added", nil)
				if count := e.Count(t, "SELECT COUNT(*) FROM series WHERE name = 'Matched Saga'"); count != 1 {
					t.Fatalf("Matched Saga rows: %d", count)
				}
			},
		},
		{
			Requirement: name, Invariant: "INV-SOCKET-OUTGOING-SERIES-ADDED", Case: "normal", State: "changed", Assertion: "series-added-event-for-another-new-series",
			Sources: []string{srcAddedEmit},
			Claim:   "A second quick match naming another new series emits a second series_added for that series only.",
			Run: func(t *testing.T, e *harness.Env) {
				provider := s.provider(t, e)
				setBookSeries(t, e, "disposable", `[]`)
				provider.setMatches(matchOne("Other Saga"))
				audience := s.connect(t, e)
				mustOK(t, "match", s.match(t, e, e.Token(t, "root"), "disposable", false))
				event := decodeSeriesEvent(t, audience.expect(t, "series_added", nameIs("Other Saga")))
				if event.LibraryID != e.IDs[bookLibrary] {
					t.Fatalf("payload: %+v", event)
				}
				audience.expectNone(t, "series_added", nameIs("Matched Saga"))
			},
		},
		{
			Requirement: name, Invariant: "INV-SOCKET-OUTGOING-SERIES-ADDED", Case: "edge", State: "initial", Assertion: "series-added-event-not-sent-by-media-update",
			Sources: []string{srcAddedEmit},
			Claim:   "Creating a series through the media update route creates the row but emits no series_added.",
			Run: func(t *testing.T, e *harness.Env) {
				audience := s.connect(t, e)
				setBookSeries(t, e, "disposable", `[{"name":"Media Made Series","sequence":"1"}]`)
				if count := e.Count(t, "SELECT COUNT(*) FROM series WHERE name = 'Media Made Series'"); count != 1 {
					t.Fatalf("series not created: %d", count)
				}
				audience.expectNone(t, "series_added", nil)
				setBookSeries(t, e, "disposable", `[{"name":"A Third Saga","sequence":"10"}]`)
			},
		},
		{
			Requirement: name, Invariant: "INV-SOCKET-OUTGOING-SERIES-ADDED", Case: "error", State: "failure", Assertion: "series-added-event-not-sent-on-failed-match",
			Sources: []string{srcAddedEmit, "server/scanner/Scanner.js:55-60", "server/controllers/LibraryItemController.js:1220-1250"},
			Claim:   "A quick match without provider results (warning), a forbidden match and a match of an unknown item emit no series_added.",
			Run: func(t *testing.T, e *harness.Env) {
				provider := s.provider(t, e)
				provider.setMatches(nil)
				audience := s.connect(t, e)
				resp := s.match(t, e, e.Token(t, "root"), "disposable", true)
				mustOK(t, "no results", resp)
				var result struct {
					Warning string `json:"warning"`
				}
				resp.JSON(t, &result)
				if result.Warning == "" {
					t.Fatalf("expected a warning: %s", resp.Body)
				}
				provider.setMatches(matchOne("Forbidden Saga"))
				expectStatus(t, "forbidden", s.match(t, e, e.Token(t, "listener"), "disposable", true), http.StatusForbidden)
				body := `{"provider":` + jsonString(provider.slug) + `}`
				expectStatus(t, "unknown item", postJSON(t, e, e.Token(t, "root"), "/api/items/00000000-0000-4000-8000-000000000000/match", body), http.StatusNotFound)
				audience.expectNone(t, "series_added", nil)
				if count := e.Count(t, "SELECT COUNT(*) FROM series WHERE name = 'Forbidden Saga'"); count != 0 {
					t.Fatalf("rejected match created a series")
				}
			},
		},
	}
}

func (s *suite) removedEventScenarios() []harness.Scenario {
	const name = "socket-outgoing-series-removed"
	return []harness.Scenario{
		{
			Requirement: name, Invariant: "INV-SOCKET-OUTGOING-SERIES-REMOVED", Case: "normal", State: "initial", Assertion: "series-removed-event-when-media-update-empties-series",
			Sources: []string{srcRemovedAPI, "server/controllers/LibraryItemController.js:238"},
			Claim:   "Removing a series from its only book through the media update deletes the series and emits series_removed {id, libraryId} to every authenticated socket, not to an unauthenticated one.",
			Run: func(t *testing.T, e *harness.Env) {
				s.enrich(t, e)
				setBookSeries(t, e, "disposable", `[{"name":"Temp Series","sequence":"1"}]`)
				id := seriesIDByName(t, e, "Temp Series")
				audience := s.connect(t, e)
				setBookSeries(t, e, "disposable", `[]`)
				expectRemovedPayload(t, e, audience.expect(t, "series_removed", idIs(id)), id)
				if count := e.Count(t, "SELECT COUNT(*) FROM series WHERE id = ?", id); count != 0 {
					t.Fatalf("series row not deleted")
				}
			},
		},
		{
			Requirement: name, Invariant: "INV-SOCKET-OUTGOING-SERIES-REMOVED", Case: "edge", State: "repeat", Assertion: "series-removed-event-not-repeated",
			Sources: []string{srcRemovedAPI},
			Claim:   "Repeating the update that removes nothing emits no series_removed.",
			Run: func(t *testing.T, e *harness.Env) {
				audience := s.connect(t, e)
				setBookSeries(t, e, "disposable", `[]`)
				audience.expectNone(t, "series_removed", nil)
			},
		},
		{
			Requirement: name, Invariant: "INV-SOCKET-OUTGOING-SERIES-REMOVED", Case: "normal", State: "changed", Assertion: "series-removed-event-only-when-last-book-leaves",
			Sources: []string{srcRemovedAPI, "server/routers/ApiRouter.js:425-428"},
			Claim:   "A series shared by two books survives the first removal without event and is removed, with the event, when the second book leaves it.",
			Run: func(t *testing.T, e *harness.Env) {
				setBookSeries(t, e, "disposable", `[{"name":"Temp Two","sequence":"1"}]`)
				setBookSeries(t, e, "ebook", `[{"name":"A Third Saga","sequence":"2"},{"name":"Temp Two","sequence":"2"}]`)
				id := seriesIDByName(t, e, "Temp Two")
				audience := s.connect(t, e)
				setBookSeries(t, e, "disposable", `[{"name":"A Third Saga","sequence":"10"}]`)
				audience.expectNone(t, "series_removed", nil)
				if count := e.Count(t, "SELECT COUNT(*) FROM series WHERE id = ?", id); count != 1 {
					t.Fatalf("series with a remaining book must stay")
				}
				setBookSeries(t, e, "ebook", `[{"name":"A Third Saga","sequence":"2"}]`)
				expectRemovedPayload(t, e, audience.expect(t, "series_removed", idIs(id)), id)
			},
		},
		{
			Requirement: name, Invariant: "INV-SOCKET-OUTGOING-SERIES-REMOVED", Case: "edge", State: "changed", Assertion: "series-removed-event-from-quick-match-replacement",
			Sources: []string{srcRemovedAPI, "server/scanner/Scanner.js:328-342"},
			Claim:   "A quick match with overrideDetails that replaces a book's only series by an existing one removes the emptied series and emits series_removed, and no series_added.",
			Run: func(t *testing.T, e *harness.Env) {
				provider := s.provider(t, e)
				setBookSeries(t, e, "disposable", `[{"name":"Replaced Saga","sequence":"1"}]`)
				id := seriesIDByName(t, e, "Replaced Saga")
				provider.setMatches([]map[string]any{{"title": "Hard Delete Me", "series": []map[string]string{{"series": "A Third Saga", "sequence": "10"}}}})
				audience := s.connect(t, e)
				mustOK(t, "match", s.match(t, e, e.Token(t, "root"), "disposable", true))
				expectRemovedPayload(t, e, audience.expect(t, "series_removed", idIs(id)), id)
				audience.expectNone(t, "series_added", nil)
			},
		},
		{
			Requirement: name, Invariant: "INV-SOCKET-OUTGOING-SERIES-REMOVED", Case: "error", State: "failure", Assertion: "series-removed-event-not-sent-on-rejection",
			Sources: []string{srcRemovedAPI, "server/controllers/LibraryItemController.js:1220-1250"},
			Claim:   "A forbidden or unknown-item media update leaves the series in place and emits no series_removed.",
			Run: func(t *testing.T, e *harness.Env) {
				setBookSeries(t, e, "disposable", `[{"name":"Keep Me","sequence":"1"}]`)
				id := seriesIDByName(t, e, "Keep Me")
				audience := s.connect(t, e)
				path := "/api/items/" + e.IDs["disposable"] + "/media"
				expectStatus(t, "forbidden", patchJSON(t, e, e.Token(t, "listener"), path, `{"metadata":{"series":[]}}`), http.StatusForbidden)
				expectStatus(t, "unknown", patchJSON(t, e, e.Token(t, "root"), "/api/items/00000000-0000-4000-8000-000000000000/media", `{"metadata":{"series":[]}}`), http.StatusNotFound)
				audience.expectNone(t, "series_removed", nil)
				if count := e.Count(t, "SELECT COUNT(*) FROM series WHERE id = ?", id); count != 1 {
					t.Fatalf("series must stay")
				}
			},
		},
		{
			Requirement: name, Invariant: "INV-SOCKET-OUTGOING-SERIES-REMOVED", Case: "edge", State: "initial", Assertion: "series-removed-event-from-item-scan",
			Sources: []string{srcRemovedScan, "server/scanner/BookScanner.js:290-296", "server/scanner/LibraryItemScanner.js:90"},
			Claim:   "Rescanning a book whose metadata file lists another series removes the emptied old series and emits series_removed (the new series gets no series_added).",
			Run: func(t *testing.T, e *harness.Env) {
				id := seriesIDByName(t, e, "Keep Me")
				file := filepath.Join(e.Metadata, "items", e.IDs["disposable"], "metadata.json")
				raw, err := os.ReadFile(file)
				if err != nil {
					t.Fatalf("metadata file: %v", err)
				}
				var doc map[string]any
				if err := json.Unmarshal(raw, &doc); err != nil {
					t.Fatal(err)
				}
				doc["series"] = []string{"Scan Made Saga #1"} // an empty list would leave the series untouched (abmetadataGenerator.js:83)
				edited, err := json.Marshal(doc)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, edited, 0o644); err != nil {
					t.Fatal(err)
				}
				audience := s.connect(t, e)
				mustOK(t, "scan", e.Post(t, e.Token(t, "root"), "/api/items/"+e.IDs["disposable"]+"/scan", nil))
				expectRemovedPayload(t, e, audience.expect(t, "series_removed", idIs(id)), id)
				audience.expectNone(t, "series_added", nil)
				if count := e.Count(t, "SELECT COUNT(*) FROM series WHERE name = 'Scan Made Saga'"); count != 1 {
					t.Fatalf("the scan must create the series listed in the metadata file")
				}
				if count := e.Count(t, "SELECT COUNT(*) FROM series WHERE id = ?", id); count != 0 {
					t.Fatalf("series row not deleted by scan")
				}
			},
		},
		{
			Requirement: name, Invariant: "INV-SOCKET-OUTGOING-SERIES-REMOVED", Case: "edge", State: "changed", Assertion: "series-removed-event-from-item-delete",
			Sources: []string{srcRemovedAPI, "server/controllers/LibraryItemController.js:145"},
			Claim:   "Deleting the only book of a series removes the series and emits series_removed.",
			Run: func(t *testing.T, e *harness.Env) {
				setBookSeries(t, e, "disposable", `[{"name":"Doomed Series","sequence":"1"}]`)
				id := seriesIDByName(t, e, "Doomed Series")
				audience := s.connect(t, e)
				mustOK(t, "delete", e.Delete(t, e.Token(t, "root"), "/api/items/"+e.IDs["disposable"]+"?hard=1"))
				expectRemovedPayload(t, e, audience.expect(t, "series_removed", idIs(id)), id)
			},
		},
	}
}
