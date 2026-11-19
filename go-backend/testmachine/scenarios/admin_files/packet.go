package admin_files

import "audiobookshelf-compat/testmachine/scenarios/harness"

// Packet returns the admin filesystem, cache, statistics, upload, watcher and
// logger scenarios. They run in this order on one server: reads first, then the
// cache purges, then the watcher notifications, and the uploads last because
// uploaded files make the library watcher scan.
func Packet() harness.Packet {
	scenarios := filesystemScenarios()
	scenarios = append(scenarios, pathExistsScenarios()...)
	scenarios = append(scenarios, statsScenarios()...)
	scenarios = append(scenarios, cacheScenarios()...)
	scenarios = append(scenarios, loggerScenarios()...)
	scenarios = append(scenarios, watcherScenarios()...)
	scenarios = append(scenarios, uploadScenarios()...)
	return harness.Packet{Name: "admin_files", Scenarios: scenarios}
}
