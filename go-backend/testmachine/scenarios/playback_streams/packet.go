// Package playback_streams holds the replacement scenarios of the
// "playback_streams" packet: the HLS file route, the public session track route
// and the Socket.IO stream lifecycle events of a transcoded playback session.
package playback_streams

import "audiobookshelf-compat/testmachine/scenarios/harness"

// Packet returns the scenarios of the playback_streams packet. Reads come
// first; the scenarios that add and corrupt media files come last.
func Packet() harness.Packet {
	var scenarios []harness.Scenario
	for _, group := range [][]harness.Scenario{
		hlsScenarios(),
		trackScenarios(),
		openScenarios(),
		progressScenarios(),
		readyScenarios(),
		resetScenarios(),
		closedScenarios(),
		errorScenarios(),
	} {
		scenarios = append(scenarios, group...)
	}
	return harness.Packet{
		Name:      "playback_streams",
		Options:   harness.Options{Env: map[string]string{"RATE_LIMIT_AUTH_MAX": "0"}},
		Scenarios: scenarios,
	}
}
