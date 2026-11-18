// Package admin_settings holds the replacement scenarios of the
// "admin_settings" packet: server settings, sorting prefixes, tags, genres,
// cron validation and authentication settings.
package admin_settings

import "audiobookshelf-compat/tests/replacement/harness"

// Packet returns the scenarios of the admin_settings packet.
func Packet() harness.Packet {
	var scenarios []harness.Scenario
	scenarios = append(scenarios, cronScenarios()...)
	scenarios = append(scenarios, settingsScenarios()...)
	scenarios = append(scenarios, prefixScenarios()...)
	scenarios = append(scenarios, tagKind.listScenarios()...)
	scenarios = append(scenarios, genreKind.listScenarios()...)
	scenarios = append(scenarios, tagKind.editScenarios()...)
	scenarios = append(scenarios, genreKind.editScenarios()...)
	scenarios = append(scenarios, authReadScenarios()...)
	scenarios = append(scenarios, authPatchScenarios()...)
	scenarios = append(scenarios, lifecycleScenarios()...)
	return harness.Packet{Name: "admin_settings", Scenarios: scenarios}
}
