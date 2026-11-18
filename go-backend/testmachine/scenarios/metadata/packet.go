// Package metadata holds the slice-A scenarios of the "metadata" packet: the
// built-in provider search routes, the provider catalogue, the custom metadata
// provider CRUD routes and the two Socket.IO events those routes emit. The
// cover-search socket flow is a later slice.
package metadata

import "audiobookshelf-compat/testmachine/scenarios/harness"

// Packet returns the scenarios in declaration order: the fixture reads first,
// then the custom providers every later scenario depends on, then the search
// routes, the deletes and finally the event audiences (destructive last).
func Packet() harness.Packet {
	var scenarios []harness.Scenario
	for _, group := range [][]harness.Scenario{
		providerBaseScenarios(),
		createProviderScenarios(),
		providersMergedScenarios(),
		coverSearchScenarios(),
		bookSearchScenarios(),
		podcastAndAuthorScenarios(),
		chapterScenarios(),
		deleteProviderScenarios(),
		addedEventScenarios(),
		removedEventScenarios(),
	} {
		scenarios = append(scenarios, group...)
	}
	return harness.Packet{
		Name:      "metadata",
		Options:   harness.Options{Env: map[string]string{"RATE_LIMIT_AUTH_MAX": "0"}},
		Scenarios: scenarios,
	}
}
