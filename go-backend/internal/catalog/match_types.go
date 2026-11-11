package catalog

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
