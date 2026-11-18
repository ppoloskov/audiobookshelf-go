// Package backup holds the replacement scenarios of the "backup" packet: the
// archive format, the backup CRUD and upload routes, path updates, the archive
// download, the pre-swap apply validation and the backup scheduler/retention.
package backup

import "audiobookshelf-compat/testmachine/scenarios/harness"

// Packet returns the scenarios of the backup packet in execution order: the
// empty read first, then creation, the archive format, deletes, downloads,
// applies, uploads, path changes and finally the scheduler scenarios, which
// wait for real cron ticks.
func Packet() harness.Packet {
	var scenarios []harness.Scenario
	for _, group := range [][]harness.Scenario{
		initialScenarios(),
		archiveFailureScenarios(),
		archiveDirScenarios(),
		listScenarios(),
		createScenarios(),
		formatScenarios(),
		deleteScenarios(),
		downloadScenarios(),
		applyScenarios(),
		uploadScenarios(),
		pathScenarios(),
		schedulerScenarios(),
	} {
		scenarios = append(scenarios, group...)
	}
	return harness.Packet{Name: "backup", Scenarios: scenarios}
}
