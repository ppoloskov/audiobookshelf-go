// Package item_write holds the replacement scenarios of the "item_write" packet:
// the item write routes of LibraryItemController and their cascades and events.
package item_write

import (
	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// Packet returns the scenarios of the item_write packet. Metadata edits come first;
// scenarios that remove items or files run on servers of their own or last.
func Packet() harness.Packet {
	s := &setup{}
	var scenarios []harness.Scenario
	for _, group := range [][]harness.Scenario{
		mediaBookScenarios(s),
		mediaAuthorScenarios(s),
		mediaSeriesScenarios(s),
		mediaPodcastScenarios(s),
		mediaAccessScenarios(s),
		mediaCoverScenarios(s),
		mediaEventScenarios(s),
		batchUpdateScenarios(s),
		tracksScenarios(s),
		chaptersScenarios(s),
		ebookStatusScenarios(s),
		deleteFileScenarios(s),
		deleteItemScenarios(s),
		batchDeleteScenarios(s),
	} {
		scenarios = append(scenarios, group...)
	}
	return harness.Packet{Name: "item_write", Scenarios: scenarios}
}
