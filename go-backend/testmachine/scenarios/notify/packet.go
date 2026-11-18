// Package notify holds the replacement scenarios of the "notify" packet: the
// admin notification settings routes, the Apprise delivery flow per admitted
// trigger and the notifications_updated socket event.
//
// Every delivery is proven against a local Apprise stub on 127.0.0.1 and the
// real scheduler trigger against a local RSS feed and episode audio, so the
// packet is deterministic offline.
package notify

import "audiobookshelf-compat/testmachine/scenarios/harness"

// Packet returns the scenarios in declaration order: the reads first, then the
// stored settings the later flows use, the record CRUD, the two test routes, the
// event and socket contract, and last the delivery flow whose real scheduler
// trigger leaves a cron running until the scenario cleans it up.
func Packet() harness.Packet {
	var scenarios []harness.Scenario
	for _, group := range [][]harness.Scenario{
		settingsReadScenarios(),
		dataScenarios(),
		settingsWriteScenarios(),
		createScenarios(),
		deleteScenarios(),
		updateRecordScenarios(),
		triggerScenarios(),
		recordTestScenarios(),
		eventScenarios(),
		socketScenarios(),
		flowScenarios(),
	} {
		scenarios = append(scenarios, group...)
	}
	return harness.Packet{Name: "notify", Scenarios: scenarios}
}
