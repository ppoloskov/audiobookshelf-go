// Package feeds holds the replacement scenarios of the "feeds" packet: the
// RSS/ATOM feed management routes, the public feed XML, cover and enclosure
// consumers, feed refresh after media changes, feed closing (including the
// entity-deletion call sites) and the rss_feed_open/rss_feed_closed events.
//
// The scenarios run in declaration order on one server and share its state.
// Fixture feeds are read first, then collection/series/item feeds are opened,
// rejected requests are proven silent, refresh writes run before the closes
// they support, events run once the feed state they need exists, the journey
// (which renames the novel item) runs next, and the last scenarios delete
// fixture items or orphan a feed to prove the deletion/boot call sites.
package feeds

import "audiobookshelf-compat/testmachine/scenarios/harness"

// Packet returns the scenarios of the feeds packet.
func Packet() harness.Packet {
	var scenarios []harness.Scenario
	for _, group := range [][]harness.Scenario{
		listScenarios(),
		feedXMLScenarios(),
		coverScenarios(),
		episodeFileScenarios(),
		openCollectionScenarios(),
		openSeriesScenarios(),
		openItemScenarios(),
		{coverErrorScenario()},
		{listEdgeScenario()},
		{seriesChangedScenario()},
		{itemChangedScenario()},
		{collectionChangedScenario()},
		{coverChangedScenario()},
		{feedChangedScenario()},
		{podcastChangedScenario()},
		{restartScenario()},
		closeScenarios(),
		{episodeFileChangedScenario()},
		socketOpenScenarios(),
		socketClosedScenarios(),
		flowScenarios(),
		{chapterTitleScenario()},
		{authorListScenario()},
		{collectionRemovalScenario()},
		{brokenCoverScenario()},
		{emptySeriesScenario()},
		{orphanBootScenario()},
	} {
		scenarios = append(scenarios, group...)
	}
	return harness.Packet{Name: "feeds", Scenarios: scenarios}
}
