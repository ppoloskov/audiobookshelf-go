// kinds.go: the one table of file extensions the server knows:
// the library-file kind of each (server/utils/globals.js:2-6) and the MIME
// type of audio files.

package media

import "strings"

// The kinds LibraryFile.fileType takes.
const (
	KindAudio    = "audio"
	KindImage    = "image"
	KindEbook    = "ebook"
	KindMetadata = "metadata"
	KindText     = "text"
	KindUnknown  = "unknown"
)

type entry struct{ kind, mime string }

// An audio entry without a MIME type is not in TS AudioMimeType
// (server/utils/constants.js:36-58); AudioMime still reports audio/mpeg for it.
var table = map[string]entry{
	".mp3": {KindAudio, "audio/mpeg"}, ".wav": {KindAudio, ""},
	".m4b": {KindAudio, "audio/mp4"}, ".m4a": {KindAudio, "audio/mp4"}, ".mp4": {KindAudio, "audio/mp4"},
	".ogg": {KindAudio, "audio/ogg"}, ".oga": {KindAudio, "audio/ogg"}, ".opus": {KindAudio, "audio/ogg"},
	".aac": {KindAudio, "audio/aac"}, ".flac": {KindAudio, "audio/flac"}, ".wma": {KindAudio, "audio/x-ms-wma"},
	".aiff": {KindAudio, "audio/x-aiff"}, ".aif": {KindAudio, "audio/x-aiff"},
	".webm": {KindAudio, "audio/webm"}, ".webma": {KindAudio, "audio/webm"},
	".mka": {KindAudio, "audio/x-matroska"}, ".awb": {KindAudio, "audio/amr-wb"}, ".caf": {KindAudio, "audio/x-caf"},
	".mpg": {KindAudio, "audio/mpeg"}, ".mpeg": {KindAudio, "audio/mpeg"},
	".jpg": {KindImage, ""}, ".jpeg": {KindImage, ""}, ".png": {KindImage, ""}, ".webp": {KindImage, ""},
	".epub": {KindEbook, ""}, ".pdf": {KindEbook, ""}, ".mobi": {KindEbook, ""}, ".azw3": {KindEbook, ""}, ".cbz": {KindEbook, ""}, ".cbr": {KindEbook, ""},
	".opf": {KindMetadata, ""}, ".abs": {KindMetadata, ""}, ".xml": {KindMetadata, ""}, ".json": {KindMetadata, ""},
	".txt": {KindText, ""}, ".nfo": {KindText, ""},
}

// KindOf is the kind of a file extension, with its dot, in any case.
func KindOf(ext string) string {
	if e, ok := table[strings.ToLower(ext)]; ok {
		return e.kind
	}
	return KindUnknown
}

// IsAudio reports whether the extension is an audio file.
func IsAudio(ext string) bool { return KindOf(ext) == KindAudio }

// IsEbook reports whether the extension is an ebook.
func IsEbook(ext string) bool { return KindOf(ext) == KindEbook }

// IsCompanion reports whether a scan keeps a file beside the media: images,
// metadata and text.
func IsCompanion(ext string) bool {
	switch KindOf(ext) {
	case KindImage, KindMetadata, KindText:
		return true
	}
	return false
}

// AudioMime is the MIME type served for an audio extension; audio/mpeg when none is known.
func AudioMime(ext string) string {
	if e := table[strings.ToLower(ext)]; e.kind == KindAudio && e.mime != "" {
		return e.mime
	}
	return "audio/mpeg"
}

// KnownAudioMime is getAudioMimeTypeFromExtname (fileUtils.js:424-431): the
// MIME type of an audio extension TS names, "" for any other file.
func KnownAudioMime(ext string) string { return table[strings.ToLower(ext)].mime }
