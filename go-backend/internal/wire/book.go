package wire

// BookSeries is the legacy series object embedded in book metadata.
type BookSeries struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Sequence *string `json:"sequence"`
}

// BookSeriesRef is the metadata.series object of an item in an author's series
// group: the series with its prefix-free name and the book's sequence.
type BookSeriesRef struct {
	ID               string  `json:"id"`
	Name             *string `json:"name"`
	NameIgnorePrefix *string `json:"nameIgnorePrefix"`
	Sequence         *string `json:"sequence"`
}
