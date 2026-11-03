package wire

// BatchUpdateResult is the body of POST /api/items/batch/update
// (LibraryItemController.js:730-733).
type BatchUpdateResult struct {
	Success bool `json:"success"`
	Updates int  `json:"updates"`
}
