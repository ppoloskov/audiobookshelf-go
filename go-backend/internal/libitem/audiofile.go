package libitem

// FileMetadata is the stored FileMetadata of a library or audio file
// (server/objects/metadata/FileMetadata.js). Numbers are float64 so rows with
// fractional values still decode; whole numbers encode without a fraction.
type FileMetadata struct {
	Filename    string  `json:"filename"`
	Ext         string  `json:"ext"`
	Path        string  `json:"path"`
	RelPath     string  `json:"relPath"`
	Size        float64 `json:"size"`
	MtimeMs     float64 `json:"mtimeMs"`
	CtimeMs     float64 `json:"ctimeMs"`
	BirthtimeMs float64 `json:"birthtimeMs"`
}

// AudioChapter is one chapter of an audio file or a book
// (server/objects/files/AudioFile.js chapters, Book.chapters).
type AudioChapter struct {
	ID    int     `json:"id"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Title string  `json:"title"`
}

// AudioFile is one entry of books.audioFiles (server/objects/files/AudioFile.js
// toJSON). A freshly probed file and a stored row are this one type; nullable
// upstream values are pointers so null round-trips as null.
type AudioFile struct {
	Index                int               `json:"index"`
	Ino                  string            `json:"ino"`
	Metadata             FileMetadata      `json:"metadata"`
	AddedAt              int64             `json:"addedAt"`
	UpdatedAt            int64             `json:"updatedAt"`
	TrackNumFromMeta     *int              `json:"trackNumFromMeta"`
	DiscNumFromMeta      *int              `json:"discNumFromMeta"`
	TrackNumFromFilename *int              `json:"trackNumFromFilename"`
	DiscNumFromFilename  *int              `json:"discNumFromFilename"`
	ManuallyVerified     bool              `json:"manuallyVerified"`
	Exclude              bool              `json:"exclude"`
	Error                *string           `json:"error"`
	Format               string            `json:"format"`
	Duration             float64           `json:"duration"`
	BitRate              *int64            `json:"bitRate"`
	Language             *string           `json:"language"`
	Codec                *string           `json:"codec"`
	TimeBase             *string           `json:"timeBase"`
	Channels             *int              `json:"channels"`
	ChannelLayout        *string           `json:"channelLayout"`
	Chapters             []AudioChapter    `json:"chapters"`
	EmbeddedCoverArt     *string           `json:"embeddedCoverArt"`
	MetaTags             map[string]string `json:"metaTags"`
	MimeType             string            `json:"mimeType"`
}
