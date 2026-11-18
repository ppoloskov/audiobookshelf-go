// Package library_data holds the replacement scenarios of the "library_data"
// packet: filter data, stats, authors and narrators of a library, removal of
// items with issues and of metadata files, match-all, and multi-item download.
package library_data

import "audiobookshelf-compat/tests/replacement/harness"

// Packet returns the scenarios of the library_data packet. The order is
// deliberate: reads of the fixture state first, then the enrichment of the
// fixture (one scenario applies it and restarts the server), then writes.
func Packet() harness.Packet {
	s := &suite{}
	filter := filterScenarios(s) // the first four read the fixture state, then the enriched read, then a write
	narrators := narratorScenarios(s)
	var scenarios []harness.Scenario
	scenarios = append(scenarios, filter[:4]...)
	scenarios = append(scenarios, statsScenarios(s)...)
	scenarios = append(scenarios, narrators[:1]...)
	scenarios = append(scenarios, authorScenarios(s)...)
	scenarios = append(scenarios, filter[4:5]...)
	scenarios = append(scenarios, narrators[1:]...)
	scenarios = append(scenarios, filter[5:]...)
	scenarios = append(scenarios, downloadScenarios(s)...)
	scenarios = append(scenarios, metaScenarios(s)...)
	scenarios = append(scenarios, matchScenarios(s)...)
	scenarios = append(scenarios, issuesScenarios(s)...)
	return harness.Packet{Name: "library_data", Scenarios: scenarios}
}
