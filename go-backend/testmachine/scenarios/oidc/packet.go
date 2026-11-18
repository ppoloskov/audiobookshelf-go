// Package oidc holds the replacement scenarios of the "oidc" packet: the
// OpenID Connect start, mobile redirect, callback and discovery routes, and the
// native PKCE and account-linking flows clients drive through them.
package oidc

import (
	"slices"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// Packet returns the scenarios of the oidc packet. The shared server runs with
// the authentication rate limiter disabled: the flow scenarios issue many
// authentication requests from one address, and the limiter has no contract for
// clients that never reach it.
func Packet() harness.Packet {
	return harness.Packet{
		Name:    "oidc",
		Options: harness.Options{Env: oidcEnv()},
		Scenarios: slices.Concat(
			configScenarios(),
			startScenarios(),
			mobileRedirectScenarios(),
			callbackScenarios(),
			mobilePKCEScenarios(),
			accountScenarios(),
			groupScenarios(),
			lifecycleScenarios(),
			flowScenarios(),
		),
	}
}
