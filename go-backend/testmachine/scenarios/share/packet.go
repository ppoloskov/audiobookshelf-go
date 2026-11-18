// Package share holds the replacement scenarios of the "share" packet: the
// media item share management routes, the public share consumer routes, share
// expiry and playback sessions, and the share_open/share_closed events.
//
// The scenarios run in declaration order on one server. Public reads of the two
// fixture shares come first, then creates/deletes that clean up after
// themselves, then the event audiences, then the destructive single-file item.
package share

import "audiobookshelf-compat/testmachine/scenarios/harness"

// Packet returns the scenarios of the share packet.
func Packet() harness.Packet {
	var scenarios []harness.Scenario
	for _, group := range [][]harness.Scenario{
		publicSlugScenarios(),
		publicTrackScenarios(),
		publicCoverScenarios(),
		publicDownloadScenarios(),
		publicProgressScenarios(),
		createScenarios(),
		deleteScenarios(),
		shareOpenScenarios(),
		shareClosedScenarios(),
		acceleratedScenarios(),
		singleFileScenarios(),
	} {
		scenarios = append(scenarios, group...)
	}
	return harness.Packet{Name: "share", Scenarios: scenarios}
}
