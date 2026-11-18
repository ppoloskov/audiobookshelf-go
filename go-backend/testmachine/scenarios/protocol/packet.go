// Package protocol holds the replacement scenarios of the "protocol" packet:
// the HTTP protocol obligations shared by the file- and JSON-bearing routes
// (byte ranges, conditional reads, MIME and disposition, HEAD, denials), the
// mobile conditional-cache contract, JS-derived text ordering, the daily log
// file format and the outgoing log socket event.
//
// Scenarios run in declaration order on one server. Reads come first; the
// scenarios that change a file, revoke a share or restart the server with
// seeded log files come after them.
package protocol

import "audiobookshelf-compat/testmachine/scenarios/harness"

// Packet returns the scenarios of the protocol packet.
func Packet() harness.Packet {
	var scenarios []harness.Scenario
	for _, group := range [][]harness.Scenario{
		fileByteScenarios(),
		fileRangeScenarios(),
		fileHeadScenarios(),
		fileConditionalScenarios(),
		fileDeniedScenarios(),
		mobileETagScenarios(),
		mobileCoverScenarios(),
		textOrderingScenarios(),
		logEventScenarios(),
		dailyLogScenarios(),
		dailyLogLifecycleScenarios(),
	} {
		scenarios = append(scenarios, group...)
	}
	return harness.Packet{
		Name:      "protocol",
		Options:   harness.Options{Env: map[string]string{"RATE_LIMIT_AUTH_MAX": "0"}},
		Scenarios: scenarios,
	}
}
