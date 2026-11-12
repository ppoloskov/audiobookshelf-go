package playback

import (
	"cmp"
	"context"
	"database/sql"
	"slices"
	"strings"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/item"
	"audiobookshelf-compat/internal/media"
	"audiobookshelf-compat/internal/version"
)

// sessionMedia is what a session needs of a library item, or of one episode of a
// podcast item.
type sessionMedia struct {
	item      item.Item
	title     string
	author    string
	coverPath *string
	duration  float64
	metadata  any // item.BookMetadataFull or item.PodcastMetadataFull
	chapters  []db.Chapter
	files     []db.AudioFile // the playable files, with their MIME type filled in
}

// loadMedia reads the item and, for episodeID, that episode. It returns
// sql.ErrNoRows for a missing, invalid or unplayable target.
func (h *Handler) loadMedia(ctx context.Context, itemID, episodeID string) (*sessionMedia, error) {
	it, err := item.Get(ctx, h.db, itemID, item.Options{Episodes: true})
	if err != nil {
		return nil, err
	}
	if it.IsMissing || it.IsInvalid {
		return nil, sql.ErrNoRows
	}
	m := &sessionMedia{item: *it, chapters: []db.Chapter{}}
	if b := it.Book; b != nil {
		m.title, m.author, m.coverPath, m.duration = db.Val(b.Title), b.AuthorName(), b.CoverPath, db.Val(b.Duration)
		m.chapters, m.metadata = b.Chapters.V, b.Full(it.ID).Metadata
		for _, file := range b.AudioFiles.V {
			if !file.Exclude {
				m.files = append(m.files, playable(file))
			}
		}
	} else {
		p := it.Podcast
		m.title, m.author, m.coverPath, m.metadata = db.Val(p.Title), db.Val(p.Author), p.CoverPath, p.Full(it.ID).Metadata
	}
	if episodeID == "" {
		return m, nil
	}
	if it.Podcast == nil {
		return nil, sql.ErrNoRows
	}
	i := slices.IndexFunc(it.Podcast.Episodes, func(e item.Episode) bool { return e.ID == episodeID })
	if i < 0 {
		return nil, sql.ErrNoRows
	}
	episode := it.Podcast.Episodes[i]
	file := playable(episode.AudioFile.V)
	file.Index = 1
	m.files, m.duration, m.chapters, m.title = []db.AudioFile{file}, db.Val(file.Duration), episode.Chapters.V, db.Val(episode.Title)
	return m, nil
}

// playable sets the file's MIME type from its extension and a duration of at
// least zero.
func playable(file db.AudioFile) db.AudioFile {
	ext := strings.TrimPrefix(strings.ToLower(file.Metadata.Ext), ".")
	file.MimeType = media.AudioMime("." + cmp.Or(ext, file.Format))
	file.Duration = db.Ptr(max(db.Val(file.Duration), 0))
	return file
}

// newSession is an open session of userID on the media with the fields every
// start path shares; callers set the rest.
func (m *sessionMedia) newSession(id, userID, episodeID string) *session {
	s := &session{mediaID: m.item.MediaRowID(), files: m.files, Session: Session{
		ID: id, UserID: userID, LibraryID: db.Val(m.item.LibraryID), LibraryItemID: m.item.ID, MediaType: db.Val(m.item.MediaType),
		MediaMetadata: m.metadata, Chapters: m.chapters, DisplayTitle: m.title, DisplayAuthor: m.author, CoverPath: m.coverPath,
		Duration: m.duration, ServerVersion: version.Server,
	}}
	if episodeID != "" {
		s.EpisodeID = &episodeID
	} else {
		s.BookID = &s.mediaID
	}
	return s
}
