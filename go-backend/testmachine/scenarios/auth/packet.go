// Package auth holds the replacement scenarios of the "auth" packet: login,
// refresh, logout, first-run initialisation, API authorisation, the token
// lifecycle and the permission and ownership rules.
package auth

import (
	"slices"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	listenerName     = "trial-listener"
	listenerPassword = "trial-listener-password"
)

// Packet returns the scenarios of the auth packet. The shared server runs
// without the authentication rate limit so that scenarios may log in freely;
// the rate limit has its own server.
func Packet() harness.Packet {
	return harness.Packet{
		Name:    "auth",
		Options: harness.Options{Env: map[string]string{"RATE_LIMIT_AUTH_MAX": "0"}},
		Scenarios: slices.Concat(
			loginScenarios(),
			refreshScenarios(),
			logoutScenarios(),
			initScenarios(),
			authorizeScenarios(),
			lifecycleScenarios(),
			tokenScenarios(),
			ownershipScenarios(),
			permissionScenarios(),
			aclItemScenarios(),
			aclUserScenarios(),
		),
	}
}
