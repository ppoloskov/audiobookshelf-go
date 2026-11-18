// Package server holds the replacement scenarios of the "server" packet: the
// unauthenticated liveness, status and client-serving routes, CORS and base
// path handling, the runtime boot/restart lifecycle, and the backup apply flow
// with its log and backup_applied socket events.
package server

import (
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// Packet returns the scenarios of the server packet in execution order: reads
// first, then the lifecycle servers, the log socket, and finally the backup
// flow and its events, which replace the shared database.
func Packet() harness.Packet {
	s := &suite{}
	var scenarios []harness.Scenario
	scenarios = append(scenarios, s.statusScenarios()...)
	scenarios = append(scenarios, s.pingScenarios()...)
	scenarios = append(scenarios, s.healthScenarios()...)
	scenarios = append(scenarios, s.spaScenarios()...)
	scenarios = append(scenarios, s.unmatchedScenarios()...)
	scenarios = append(scenarios, s.cacheScenarios()...)
	scenarios = append(scenarios, s.corsScenarios()...)
	scenarios = append(scenarios, lifecycleScenarios()...)
	scenarios = append(scenarios, s.logScenarios()...)
	scenarios = append(scenarios, s.flowScenarios()...)
	scenarios = append(scenarios, s.backupEventScenarios()...)
	scenarios = append(scenarios, s.socketEventScenarios()...)
	return harness.Packet{Name: "server", Scenarios: scenarios}
}

// suite carries state the backup scenarios share through the packet's one
// server: the backup they create once and apply repeatedly.
type suite struct {
	backupID string
}

// root returns the fixture root token.
func root(t *testing.T, e *harness.Env) string {
	t.Helper()
	return e.Token(t, "root")
}
