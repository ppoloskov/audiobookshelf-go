// Package podcast_episodes holds the replacement scenarios of the
// "podcast_episodes" packet: the episode read, update and delete routes, the
// library recent-episode and download-queue listings, the podcast download
// queue (start, order, clear, failure and restart), automatic download of new
// feed episodes and the five episode socket events.
package podcast_episodes

import "audiobookshelf-compat/testmachine/scenarios/harness"

// Packet returns the scenarios of the podcast_episodes packet. Reads come
// first; the groups that download episodes and add files to the podcast library
// run after them, and episode deletion runs last.
func Packet() harness.Packet {
	var scenarios []harness.Scenario
	for _, group := range [][]harness.Scenario{
		recentScenarios(),
		episodeGetScenarios(),
		episodeUpdateScenarios(),
		libraryDownloadScenarios(),
		podcastDownloadScenarios(),
		clearQueueScenarios(),
		downloadEpisodeScenarios(),
		queueScenarios(),
		episodeGetEdgeScenarios(),
		socketScenarios(),
		episodeDeleteScenarios(),
	} {
		scenarios = append(scenarios, group...)
	}
	return harness.Packet{Name: "podcast_episodes", Scenarios: scenarios}
}
