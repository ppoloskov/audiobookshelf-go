// Package scan holds the replacement scenarios of the "scan" packet: the
// library, batch and item scan routes, the configured metadata precedence and
// sidecar format of the scanner, and the watcher/scheduler that starts scans.
package scan

import "audiobookshelf-compat/testmachine/scenarios/harness"

// Packet returns the scenarios of the scan packet.
func Packet() harness.Packet {
	var scenarios []harness.Scenario
	scenarios = append(scenarios, itemScenarios()...)
	scenarios = append(scenarios, batchScenarios()...)
	scenarios = append(scenarios, libraryScenarios()...)
	scenarios = append(scenarios, precedenceScenarios()...)
	scenarios = append(scenarios, metadataScenarios()...)
	scenarios = append(scenarios, scannerScenarios()...)
	scenarios = append(scenarios, scannerExtraScenarios()...)
	scenarios = append(scenarios, sourceScenarios()...)
	scenarios = append(scenarios, schedulingScenarios()...)
	// Last: the cancel scenario stages a large library and leaves it behind.
	scenarios = append(scenarios, cancelScenarios()...)
	return harness.Packet{Name: "scan", Scenarios: scenarios}
}
