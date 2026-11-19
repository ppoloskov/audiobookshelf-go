// Package metadata holds the metadata packet: the built-in provider search
// routes, the provider catalogue, the custom metadata provider CRUD routes, the
// two Socket.IO events those routes emit, and the streaming cover-search socket
// flow (search results, completion, refusals, provider failures and
// cancellation).
package metadata

import "audiobookshelf-compat/testmachine/scenarios/harness"

// Packet returns the scenarios in declaration order: the fixture reads first,
// then the custom providers every later scenario depends on, then the search
// routes, the deletes, the provider event audiences and finally the cover-search
// socket flow, whose scenarios create and delete their own providers.
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
		coverSearchResultScenarios(),
		coverSearchCompleteScenarios(),
		coverSearchErrorScenarios(),
		coverProviderErrorScenarios(),
		coverSearchCancelScenarios(),
	} {
		scenarios = append(scenarios, group...)
	}
	return harness.Packet{
		Name:      "metadata",
		Options:   harness.Options{Env: map[string]string{"RATE_LIMIT_AUTH_MAX": "0"}},
		Scenarios: scenarios,
	}
}
