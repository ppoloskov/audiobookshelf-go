package user_misc

import (
	"slices"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// Packet returns the scenarios of the user_misc package. The shared server runs
// without the authentication rate limit so scenarios may create users and log
// in freely; scenarios that write change shared packet state in declaration
// order (reads first, destructive deletes last).
func Packet() harness.Packet {
	return harness.Packet{
		Name:    "user_misc",
		Options: harness.Options{Env: map[string]string{"RATE_LIMIT_AUTH_MAX": "0"}},
		Scenarios: slices.Concat(
			itemsInProgressScenarios(),
			yearStatsScenarios(),
			seriesScenarios(),
			passwordScenarios(),
			ereaderScenarios(),
			apiKeyScenarios(),
		),
	}
}
