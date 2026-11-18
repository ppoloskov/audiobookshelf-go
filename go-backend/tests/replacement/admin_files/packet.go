package admin_files

import "audiobookshelf-compat/tests/replacement/harness"

// Packet returns the filesystem replacement scenarios.
func Packet() harness.Packet {
	return harness.Packet{Name: "admin_files", Scenarios: append(filesystemScenarios(), pathExistsScenarios()...)}
}
