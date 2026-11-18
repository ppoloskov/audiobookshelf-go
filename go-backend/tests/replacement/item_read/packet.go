// Package item_read holds the replacement scenarios of the "item_read" packet:
// the item document, batch get, item and file downloads, ebook serving, the
// metadata object and ffprobe routes of LibraryItemController.
package item_read

import (
	"audiobookshelf-compat/tests/replacement/harness"
)

// Packet returns the scenarios of the item_read packet. Reads come first;
// the scenarios that change the install (new library files, a deleted file)
// come last.
func Packet() harness.Packet {
	s := &setup{}
	var scenarios []harness.Scenario
	for _, group := range [][]harness.Scenario{
		itemDocScenarios(s),
		factScenarios(s),
		itemIncludeScenarios(s),
		reviewScenarios(s),
		itemAccessScenarios(s),
		batchScenarios(s),
		metadataObjectScenarios(s),
		ffprobeScenarios(s),
		fileScenarios(s),
		fileDownloadScenarios(s),
		ebookScenarios(s),
		itemDownloadScenarios(s),
		xAccelScenarios(s),
		changedScenarios(s),
	} {
		scenarios = append(scenarios, group...)
	}
	return harness.Packet{Name: "item_read", Scenarios: scenarios}
}
