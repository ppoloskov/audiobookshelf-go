// Package author holds the replacement scenarios of the "author" packet: the
// author routes (read, update and merge, delete, match, image) and the author
// Socket.IO events.
package author

import "audiobookshelf-compat/testmachine/scenarios/harness"

// Packet returns the scenarios of the author packet.
func Packet() harness.Packet {
	var scenarios []harness.Scenario
	scenarios = append(scenarios, getScenarios()...)
	scenarios = append(scenarios, getRestrictedScenarios()...)
	scenarios = append(scenarios, imageReadScenarios()...)
	scenarios = append(scenarios, imageUploadScenarios()...)
	scenarios = append(scenarios, imageSizeScenarios()...)
	scenarios = append(scenarios, matchScenarios()...)
	scenarios = append(scenarios, updateScenarios()...)
	scenarios = append(scenarios, imageDeleteScenarios()...)
	scenarios = append(scenarios, scanEventScenarios()...)
	scenarios = append(scenarios, itemEventScenarios()...)
	scenarios = append(scenarios, permissionScenarios()...)
	scenarios = append(scenarios, scanUnlinkScenarios()...)
	scenarios = append(scenarios, updateLastFirstScenarios()...)
	scenarios = append(scenarios, authorRouteEventScenarios()...)
	scenarios = append(scenarios, renameScenarios()...)
	scenarios = append(scenarios, mergeVariantScenarios()...)
	scenarios = append(scenarios, seriesSortScenarios()...)
	scenarios = append(scenarios, deleteScenarios()...)
	return harness.Packet{Name: "author", Scenarios: scenarios}
}
