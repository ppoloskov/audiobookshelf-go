// Package library_manage holds the replacement scenarios of the
// "library_manage" packet: creating, listing, reading, updating, deleting and
// ordering libraries, and the library_added/updated/removed events.
package library_manage

import "audiobookshelf-compat/tests/replacement/harness"

// Packet returns the scenarios of the library_manage packet.
func Packet() harness.Packet {
	var scenarios []harness.Scenario
	scenarios = append(scenarios, listScenarios()...)
	scenarios = append(scenarios, getScenarios()...)
	scenarios = append(scenarios, createScenarios()...)
	scenarios = append(scenarios, updateScenarios()...)
	scenarios = append(scenarios, deleteScenarios()...)
	scenarios = append(scenarios, orderScenarios()...)
	scenarios = append(scenarios, updateItemScenarios()...)
	scenarios = append(scenarios, eventScenarios()...)
	scenarios = append(scenarios, checkOrderScenarios()...)
	return harness.Packet{Name: "library_manage", Scenarios: scenarios}
}
