// Package podcast_feed holds the replacement scenarios of the "podcast_feed"
// packet: the podcast RSS fetch and parse route, the OPML preview and import,
// podcast creation, the new-episode check, episode search and episode matching,
// and the library podcast title and OPML listings.
package podcast_feed

import "audiobookshelf-compat/testmachine/scenarios/harness"

// Packet returns the scenarios of the podcast_feed packet. Reads come first;
// the groups that change the fixture podcast (feed URL, episode enclosures and a
// real episode download) run last.
func Packet() harness.Packet {
	var scenarios []harness.Scenario
	for _, group := range [][]harness.Scenario{
		feedScenarios(),
		opmlParseScenarios(),
		createScenarios(),
		opmlCreateScenarios(),
		titlesScenarios(),
		libraryOPMLScenarios(),
		checkNewScenarios(),
		searchScenarios(),
		matchScenarios(),
	} {
		scenarios = append(scenarios, group...)
	}
	return harness.Packet{Name: "podcast_feed", Scenarios: scenarios}
}
