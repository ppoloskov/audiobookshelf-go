package library

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestLibraryViewKeepsNullScanFieldsPresent(t *testing.T) {
	rig := newSeriesRig(t)
	user := seriesRootUser()
	get := func() map[string]any {
		w := rig.do(http.MethodGet, "/api/libraries/l1", "", user)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var fields map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil {
			t.Fatal(err)
		}
		return fields
	}
	fields := get()
	if value, ok := fields["lastScanVersion"]; !ok || value != nil {
		t.Fatalf("lastScanVersion should be present as null: %v", fields)
	}
	if value, ok := fields["lastScan"]; !ok || value != nil {
		t.Fatalf("lastScan must remain present as null: %v", fields)
	}
	if _, err := rig.db.Exec(`UPDATE libraries SET lastScanVersion='2.37.2' WHERE id='l1'`); err != nil {
		t.Fatal(err)
	}
	if got := get()["lastScanVersion"]; got != "2.37.2" {
		t.Fatalf("lastScanVersion=%v", got)
	}
}

func TestLibraryStatsUseActualBookQueries(t *testing.T) {
	rig := newSeriesRig(t)
	if _, err := StatsValue(t.Context(), rig.db, "l1", "book"); err != nil {
		t.Fatalf("stats implementation: %v", err)
	}
	w := rig.do(http.MethodGet, "/api/libraries/l1/stats", "", seriesRootUser())
	if w.Code != http.StatusOK {
		t.Fatalf("stats status=%d body=%s", w.Code, w.Body.String())
	}
	var stats map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &stats); err != nil {
		t.Fatal(err)
	}
	if stats["totalItems"] != float64(2) || stats["totalAuthors"] != float64(1) || stats["totalGenres"] != float64(1) {
		t.Fatalf("stats=%#v", stats)
	}
}

func TestLibraryFilterDataRoute(t *testing.T) {
	rig := newSeriesRig(t)
	w := rig.do(http.MethodGet, "/api/libraries/l1/filterdata", "", seriesRootUser())
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["bookCount"] != float64(2) || got["numIssues"] != float64(0) {
		t.Fatalf("filterdata=%s", w.Body.String())
	}
}
