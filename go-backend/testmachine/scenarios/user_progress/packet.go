// Package user_progress holds the replacement scenarios of the "user_progress"
// packet: the current user's media progress writes (single, batch, delete),
// bookmarks, and the events they and playback sync emit.
package user_progress

import "audiobookshelf-compat/testmachine/scenarios/harness"

// Packet returns the scenarios of the user_progress packet.
func Packet() harness.Packet {
	var scenarios []harness.Scenario
	scenarios = append(scenarios, createScenarios()...)
	scenarios = append(scenarios, updateScenarios()...)
	scenarios = append(scenarios, finishScenarios()...)
	scenarios = append(scenarios, errorScenarios()...)
	scenarios = append(scenarios, batchScenarios()...)
	scenarios = append(scenarios, removeScenarios()...)
	scenarios = append(scenarios, bookmarkScenarios()...)
	scenarios = append(scenarios, bookmarkErrorScenarios()...)
	scenarios = append(scenarios, bookmarkMoreScenarios()...)
	scenarios = append(scenarios, progressEventScenarios()...)
	return harness.Packet{Name: "user_progress", Scenarios: scenarios}
}
