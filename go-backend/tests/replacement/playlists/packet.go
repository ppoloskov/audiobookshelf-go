// Package playlists holds the replacement scenarios of the "playlists" packet:
// the playlist routes, the library playlist list, the playlist_added,
// playlist_updated and playlist_removed events and the ownership flow.
package playlists

import "audiobookshelf-compat/tests/replacement/harness"

// Packet returns the scenarios of the playlists packet. They run in this order
// on one server: reads of the pristine fixture first, then creates and writes
// (each on playlists of its own), then events and the flows.
func Packet() harness.Packet {
	var scenarios []harness.Scenario
	for _, group := range [][]harness.Scenario{readScenarios(), createScenarios(), updateScenarios(), itemScenarios(), batchScenarios(), restrictedScenarios(), rollbackScenarios(), eventScenarios(), flowScenarios()} {
		scenarios = append(scenarios, group...)
	}
	return harness.Packet{Name: "playlists", Scenarios: append(scenarios, restartScenario())}
}
