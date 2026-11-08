// Package filekind is the one table of file extensions the server knows:
// the library-file kind of each (server/utils/globals.js:2-6) and the MIME
// type of audio files.
package filekind

import "strings"

// The kinds LibraryFile.fileType takes.
const (
	Audio    = "audio"
	Image    = "image"
	Ebook    = "ebook"
	Metadata = "metadata"
	Text     = "text"
	Unknown  = "unknown"
)

type entry struct{ kind, mime string }

// An audio entry without a MIME type is served as audio/mpeg.
var table = map[string]entry{
	".mp3": {Audio, ""}, ".wav": {Audio, ""},
	".m4b": {Audio, "audio/mp4"}, ".m4a": {Audio, "audio/mp4"}, ".mp4": {Audio, "audio/mp4"},
	".ogg": {Audio, "audio/ogg"}, ".oga": {Audio, "audio/ogg"}, ".opus": {Audio, "audio/ogg"},
	".aac": {Audio, "audio/aac"}, ".flac": {Audio, "audio/flac"}, ".wma": {Audio, "audio/x-ms-wma"},
	".aiff": {Audio, "audio/x-aiff"}, ".aif": {Audio, "audio/x-aiff"},
	".webm": {Audio, "audio/webm"}, ".webma": {Audio, "audio/webm"},
	".mka": {Audio, "audio/x-matroska"}, ".awb": {Audio, "audio/amr-wb"}, ".caf": {Audio, "audio/x-caf"},
	".mpg": {Audio, ""}, ".mpeg": {Audio, ""},
	".jpg": {Image, ""}, ".jpeg": {Image, ""}, ".png": {Image, ""}, ".webp": {Image, ""},
	".epub": {Ebook, ""}, ".pdf": {Ebook, ""}, ".mobi": {Ebook, ""}, ".azw3": {Ebook, ""}, ".cbz": {Ebook, ""}, ".cbr": {Ebook, ""},
	".opf": {Metadata, ""}, ".abs": {Metadata, ""}, ".xml": {Metadata, ""}, ".json": {Metadata, ""},
	".txt": {Text, ""}, ".nfo": {Text, ""},
}

// Of is the kind of a file extension, with its dot, in any case.
func Of(ext string) string {
	if e, ok := table[strings.ToLower(ext)]; ok {
		return e.kind
	}
	return Unknown
}

func IsAudio(ext string) bool { return Of(ext) == Audio }
func IsEbook(ext string) bool { return Of(ext) == Ebook }

// IsCompanion reports whether a scan keeps a file beside the media: images,
// metadata and text.
func IsCompanion(ext string) bool {
	switch Of(ext) {
	case Image, Metadata, Text:
		return true
	}
	return false
}

// AudioMime is the MIME type of an audio file extension.
func AudioMime(ext string) string {
	if e := table[strings.ToLower(ext)]; e.kind == Audio && e.mime != "" {
		return e.mime
	}
	return "audio/mpeg"
}
