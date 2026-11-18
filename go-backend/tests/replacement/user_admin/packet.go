// Package user_admin holds the replacement scenarios of the "user_admin"
// packet: the administrator's user management routes under /api/users and the
// user_added, user_updated and user_removed Socket.IO events.
package user_admin

import "audiobookshelf-compat/tests/replacement/harness"

// Packet returns the scenarios of the user_admin packet. Reads come first and
// the destructive scenarios last: they share one server.
func Packet() harness.Packet {
	var scenarios []harness.Scenario
	scenarios = append(scenarios, listScenarios()...)
	scenarios = append(scenarios, onlineScenarios()...)
	scenarios = append(scenarios, getScenarios()...)
	scenarios = append(scenarios, listeningScenarios()...)
	scenarios = append(scenarios, createScenarios()...)
	scenarios = append(scenarios, updateScenarios()...)
	scenarios = append(scenarios, unlinkScenarios()...)
	scenarios = append(scenarios, deleteScenarios()...)
	// The scenarios log in far more than the default limit of 40 auth requests
	// per 10 minutes; the limiter is not under test here.
	return harness.Packet{Name: "user_admin", Options: harness.Options{Env: map[string]string{"RATE_LIMIT_AUTH_MAX": "0"}}, Scenarios: scenarios}
}
