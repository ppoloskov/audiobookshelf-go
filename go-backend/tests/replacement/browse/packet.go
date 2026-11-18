package browse

import (
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

// Packet checks the library item list against the seeded book and podcast libraries.
func Packet() harness.Packet {
	s := &suite{}
	scenarios := []harness.Scenario{{
		Requirement: "route-r022", Invariant: "INV-R022-SAMPLED", Case: "normal", State: "stateless", Assertion: "book-library-membership",
		Sources: []string{"server/controllers/LibraryController.js:604-639"},
		Claim:   "The minified book library lists exactly its five fixture items and reports their total.",
		Run: func(t *testing.T, e *harness.Env) {
			page := getItems(t, e, e.Token(t, "root"), bookLibrary, query("minified", "1"))
			want := fixtureIDs(e, "saga1", "saga2", "novel", "ebook", "disposable")
			expectSet(t, "book library membership", page.ids(), want)
			expectInt(t, "total", page.Total, len(want))
			if page.MediaType != "book" || !page.Minified {
				t.Fatalf("unexpected item list mode: mediaType=%q minified=%v", page.MediaType, page.Minified)
			}
		},
	}}
	scenarios = append(scenarios, s.itemScenarios()...)
	scenarios = append(scenarios, s.sortScenarios()...)
	scenarios = append(scenarios, s.filterScenarios()...)
	scenarios = append(scenarios, s.includeCollapseScenarios()...)
	scenarios = append(scenarios, s.podcastScenarios()...)
	scenarios = append(scenarios, s.restrictionScenarios()...)
	scenarios = append(scenarios, s.seriesListScenarios()...)
	scenarios = append(scenarios, s.seriesDetailScenarios()...)
	scenarios = append(scenarios, s.seriesByIDScenarios()...)
	personalized := s.personalizedScenarios()
	scenarios = append(scenarios, personalized[:len(personalized)-1]...)
	scenarios = append(scenarios, s.shelfDetailScenarios()...)
	scenarios = append(scenarios, personalized[len(personalized)-1])
	scenarios = append(scenarios, s.searchScenarios()...)
	scenarios = append(scenarios, s.seriesPatchScenarios()...)
	scenarios = append(scenarios, s.updatedEventScenarios()...)
	scenarios = append(scenarios, s.addedEventScenarios()...)
	scenarios = append(scenarios, s.removedEventScenarios()...)
	scenarios = append(scenarios, s.reviewScenarios()...)
	return harness.Packet{Name: "browse", Scenarios: scenarios}
}
