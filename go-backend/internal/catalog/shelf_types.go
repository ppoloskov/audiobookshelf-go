package catalog

// batchUpdateResult is the body of POST /api/items/batch/update
// (LibraryItemController.js:730-733).
type batchUpdateResult struct {
	Success bool `json:"success"`
	Updates int  `json:"updates"`
}
