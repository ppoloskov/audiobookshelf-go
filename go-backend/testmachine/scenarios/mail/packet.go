// Package mail holds the replacement scenarios of the "mail" packet: the admin
// email settings, the SMTP test and ereader-device routes, the send-ebook flow
// and the SMTP/MIME contract those flows put on the wire.
//
// Every scenario is deterministic offline: a local SMTP stub on 127.0.0.1
// records the dialog and the message.
package mail

import "audiobookshelf-compat/testmachine/scenarios/harness"

// Packet returns the scenarios in declaration order. Reads come first, then the
// stored settings the later flows need, then device CRUD, then the deliveries
// (which point the settings at a fresh local stub each time).
func Packet() harness.Packet {
	var scenarios []harness.Scenario
	for _, group := range [][]harness.Scenario{
		settingsReadScenarios(),
		settingsWriteScenarios(),
		settingsReflectScenarios(),
		settingsRestartScenarios(),
		deviceScenarios(),
		testMailScenarios(),
		flowScenarios(),
		sendScenarios(),
	} {
		scenarios = append(scenarios, group...)
	}
	return harness.Packet{Name: "mail", Scenarios: scenarios}
}
