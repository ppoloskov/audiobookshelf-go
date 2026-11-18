// Package collections holds the replacement scenarios of the "collections"
// packet: the collection routes, the library collection list and the
// collection_added, collection_updated and collection_removed events.
package collections

import "audiobookshelf-compat/testmachine/scenarios/harness"

// Packet returns the scenarios of the collections packet. They run in this
// order on one server: reads of the pristine fixture first, then creates and
// writes (each on its own collection), then events.
func Packet() harness.Packet {
	var scenarios []harness.Scenario
	for _, group := range [][]harness.Scenario{readScenarios(), createScenarios(), updateScenarios(), bookScenarios(), eventScenarios(), restrictedScenarios(), pagingScenarios()} {
		scenarios = append(scenarios, group...)
	}
	return harness.Packet{Name: "collections", Scenarios: scenarios}
}
