// Package user_me holds the replacement scenarios of the "user_me" packet: the
// current user's own data routes under /api/me (profile, auth sessions,
// progress, bookmarks, listening sessions and statistics).
package user_me

import "audiobookshelf-compat/tests/replacement/harness"

// Packet returns the scenarios of the user_me packet.
func Packet() harness.Packet {
	var scenarios []harness.Scenario
	scenarios = append(scenarios, meScenarios()...)
	scenarios = append(scenarios, sessionScenarios()...)
	scenarios = append(scenarios, listeningScenarios()...)
	scenarios = append(scenarios, progressScenarios()...)
	scenarios = append(scenarios, continueScenarios()...)
	scenarios = append(scenarios, playedScenarios()...)
	scenarios = append(scenarios, userStateScenarios()...)
	return harness.Packet{Name: "user_me", Scenarios: scenarios}
}
