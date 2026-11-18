// Package launch holds the replacement scenarios of the "launch" packet: the
// standalone executable's launch contract (flag/environment precedence, config
// and metadata paths, router base path, refused boots and invalid startup
// input), the boot-boundary device flow, and the retained static client.
package launch

import (
	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// Packet returns the launch scenarios in execution order. Scenarios that only
// read the fixture-backed shared server run first; scenarios that need a fresh
// install, a special environment or a launch failure start their own server.
func Packet() harness.Packet {
	var scenarios []harness.Scenario
	scenarios = append(scenarios, staticScenarios()...)
	scenarios = append(scenarios, deviceScenarios()...)
	scenarios = append(scenarios, configurationScenarios()...)
	scenarios = append(scenarios, binaryScenarios()...)
	return harness.Packet{Name: "launch", Scenarios: scenarios}
}
