// Package playback_sessions holds the replacement scenarios of the
// "playback_sessions" packet: starting playback sessions, syncing and closing
// them, offline (local) session upload, and the admin session routes.
package playback_sessions

import "audiobookshelf-compat/testmachine/scenarios/harness"

// Packet returns the scenarios of the playback_sessions packet.
func Packet() harness.Packet {
	var scenarios []harness.Scenario
	scenarios = append(scenarios, playScenarios()...)
	scenarios = append(scenarios, deviceScenarios()...)
	scenarios = append(scenarios, episodeScenarios()...)
	scenarios = append(scenarios, refusalScenarios()...)
	scenarios = append(scenarios, syncScenarios()...)
	scenarios = append(scenarios, closeScenarios()...)
	scenarios = append(scenarios, openScenarios()...)
	return harness.Packet{
		Name:      "playback_sessions",
		Options:   harness.Options{Env: map[string]string{"RATE_LIMIT_AUTH_MAX": "0"}},
		Scenarios: scenarios,
	}
}
