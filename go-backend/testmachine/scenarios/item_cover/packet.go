// Package item_cover holds the replacement scenarios of the "item_cover"
// packet: the item cover routes of LibraryItemController, the cover subsystem
// (CoverManager, CacheManager) and the socket events this packet owns.
package item_cover

import (
	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// Packet returns the scenarios of the item_cover packet. Reads come first;
// scenarios that replace or delete fixture state run later, and the ones that
// need their own server use harness.Start.
func Packet() harness.Packet {
	s := &setup{}
	var scenarios []harness.Scenario
	for _, group := range [][]harness.Scenario{
		rawCoverScenarios(),
		resizedCoverScenarios(),
		uploadScenarios(s),
		chooseCoverScenarios(s),
		deleteCoverScenarios(s),
		subsystemScenarios(s),
		matchScenarios(s),
		quickMatchScenarios(s),
		socketQuickMatchScenarios(s),
		socketItemsAddedScenarios(s),
		socketItemAddedScenarios(s),
	} {
		scenarios = append(scenarios, group...)
	}
	return harness.Packet{
		Name: "item_cover",
		Options: harness.Options{Env: map[string]string{
			// Cover urls of the scenarios point at httptest servers on 127.0.0.1;
			// upstream filters loopback destinations unless it is whitelisted.
			"SSRF_REQUEST_FILTER_WHITELIST": "127.0.0.1",
		}},
		Scenarios: scenarios,
	}
}
