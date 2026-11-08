package catalog

import (
	"encoding/json"
)

// quickMatchResult is the answer of Scanner.quickMatchLibraryItem
// (server/scanner/Scanner.js:37-137): {warning} when nothing matched, else
// {updated, libraryItem} with the expanded item.
type quickMatchResult struct {
	Warning     *string         `json:"warning,omitempty"`
	Updated     *bool           `json:"updated,omitempty"`
	LibraryItem json.RawMessage `json:"libraryItem,omitempty"`
}

// batchQuickMatchResult is the batch_quickmatch_complete event payload
// (LibraryItemController.js:803-810).
type batchQuickMatchResult struct {
	Success   bool `json:"success"`
	Updates   int  `json:"updates"`
	Unmatched int  `json:"unmatched"`
}

// taskData is the data of a library task.
type taskData struct {
	LibraryID   string           `json:"libraryId"`
	ScanResults *taskScanResults `json:"scanResults,omitempty"`
}

// taskScanResults is LibraryScan.scanResults (LibraryScan.js:71-79).
type taskScanResults struct {
	Added   int    `json:"added"`
	Updated int    `json:"updated"`
	Missing int    `json:"missing"`
	Elapsed int64  `json:"elapsed"`
	Text    string `json:"text"`
}
