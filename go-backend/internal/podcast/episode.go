package podcast

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/filekind"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/item"
	"audiobookshelf-compat/internal/mediatools"
	"audiobookshelf-compat/internal/playlistview"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/textclean"
)

// audioOf maps an ffprobe report of the downloaded file to the audioFile
// document of an episode. It fails when the file has no audio stream.
func audioOf(path string, st os.FileInfo, probe mediatools.Info, now db.Time) (db.AudioFile, error) {
	i := slices.IndexFunc(probe.Streams, func(s mediatools.Stream) bool { return s.Type == "audio" })
	if i < 0 {
		return db.AudioFile{}, errors.New("Invalid media file: no audio stream found")
	}
	stream := probe.Streams[i]
	duration, _ := strconv.ParseFloat(probe.Format.Duration, 64) // unreadable counts as 0
	bitRate, _ := strconv.ParseInt(probe.Format.BitRate, 10, 64)
	if bitRate == 0 {
		bitRate, _ = strconv.ParseInt(stream.BitRate, 10, 64)
	}
	ext, base, ms := filepath.Ext(path), filepath.Base(path), float64(st.ModTime().UnixMilli())
	return db.AudioFile{Index: 1, Ino: inode(st), AddedAt: now.Millis(), UpdatedAt: now.Millis(),
		Metadata: db.FileMetadata{Filename: base, Ext: ext, Path: filepath.ToSlash(path), RelPath: base, Size: st.Size(), MtimeMs: ms, CtimeMs: ms, BirthtimeMs: ms},
		Format:   cmp.Or(probe.Format.LongName, probe.Format.Name), Duration: &duration, BitRate: &bitRate, Codec: &stream.Codec, TimeBase: &stream.TimeBase,
		Channels: db.Ptr(int64(stream.Channels)), ChannelLayout: &stream.ChannelLayout, Chapters: []db.Chapter{}, MimeType: filekind.AudioMime(ext)}, nil
}

// register adds the downloaded file at path to the podcast as an episode, then
// trims old episodes (auto downloads only) and announces the change.
func (h *Handler) register(d *download, path string) error {
	ctx := h.ctx
	probe, err := mediatools.Probe(ctx, path)
	if err != nil {
		return fmt.Errorf("ffprobe audio file: %w", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	now := db.Now()
	audio, err := audioOf(path, st, probe, now)
	if err != nil {
		return err
	}
	ep, enc := d.episode, d.episode.Enclosure
	extra := db.EpisodeExtra{}
	if ep.GUID != nil && *ep.GUID != "" {
		extra.GUID = ep.GUID
	}
	row := db.PodcastEpisode{ID: ids.Must(), Season: ptrIfSet(ep.Season), Episode: ptrIfSet(ep.Episode), EpisodeType: db.Ptr(cmp.Or(ep.EpisodeType, "full")),
		Title: db.Ptr(d.title()), Subtitle: ptrIfSet(ep.Subtitle), Description: ptrIfSet(ep.Description), PubDate: ptrIfSet(ep.PubDate),
		EnclosureURL: ptrIfSet(enc.URL), EnclosureType: ptrIfSet(enc.Type), AudioFile: db.JSON[db.AudioFile]{V: audio},
		Chapters: db.JSON[[]db.Chapter]{V: []db.Chapter{}}, ExtraData: db.JSON[*db.EpisodeExtra]{V: &extra}, CreatedAt: now, UpdatedAt: now, PodcastID: &d.item.Podcast.ID}
	if size, err := strconv.ParseInt(enc.Length, 10, 64); err == nil {
		row.EnclosureSize = &size
	}
	if ms := ep.PublishedAt; ms != nil && *ms != 0 {
		row.PublishedAt = db.FromMillis(*ms)
	}
	err = h.db.Tx(ctx, func(ctx context.Context) error {
		if err := h.db.Insert(ctx, "podcastEpisodes", &row); err != nil {
			return err
		}
		var li db.LibraryItem
		if err := h.db.Get(ctx, &li, `SELECT * FROM libraryItems WHERE id=?`, d.item.ID); err != nil {
			return err
		}
		size := audio.Metadata.Size
		for _, f := range li.LibraryFiles.V {
			size += f.Metadata.Size
		}
		li.Size, li.UpdatedAt = &size, now
		li.LibraryFiles.V = append(li.LibraryFiles.V, db.LibraryFile{Ino: audio.Ino, Metadata: audio.Metadata, AddedAt: now.Millis(), UpdatedAt: now.Millis(), FileType: "audio"})
		if err := h.db.Update(ctx, "libraryItems", &li, "libraryFiles", "size", "updatedAt"); err != nil {
			return err
		}
		return h.recount(ctx, d.item.Podcast.ID, now)
	})
	if err != nil {
		return err
	}
	updated, err := h.load(ctx, d.item.ID)
	if err != nil {
		return err
	}
	if d.auto && db.Val(updated.Podcast.MaxEpisodesToKeep) > 0 {
		if err := h.trimOldest(ctx, updated, row.ID); err != nil {
			return err
		}
		if updated, err = h.load(ctx, d.item.ID); err != nil {
			return err
		}
	}
	view := h.expanded(updated)
	h.emit.ToAdmins("item_updated", view)
	i := slices.IndexFunc(updated.Podcast.Episodes, func(e item.Episode) bool { return e.ID == row.ID })
	if i < 0 {
		return nil
	}
	h.emit.ToAll("episode_added", struct {
		item.EpisodeExpanded
		LibraryItem item.Expanded `json:"libraryItem"`
	}{updated.Podcast.Episodes[i].Expanded(d.item.ID), view})
	if d.auto { // PodcastManager.js:257: notifications only for auto downloaded episodes
		h.notifyEpisodeDownloaded(updated, updated.Podcast.Episodes[i])
	}
	return nil
}

// recount stores the episode count of the podcast.
func (h *Handler) recount(ctx context.Context, podcastID string, now db.Time) error {
	_, err := h.db.Exec(ctx, `UPDATE podcasts SET numEpisodes=(SELECT COUNT(*) FROM podcastEpisodes WHERE podcastId=?),updatedAt=? WHERE id=?`, podcastID, now, podcastID)
	return err
}

// trimOldest deletes the oldest episode (and its file) once the podcast has more
// dated episodes than maxEpisodesToKeep, never the one just added
// (PodcastManager.removeOldestEpisode). A file that cannot be removed keeps its
// episode.
func (h *Handler) trimOldest(ctx context.Context, it *item.Item, justAdded string) error {
	var dated []item.Episode
	for _, ep := range it.Podcast.Episodes {
		if !ep.PublishedAt.IsZero() {
			dated = append(dated, ep)
		}
	}
	if int64(len(dated)) <= db.Val(it.Podcast.MaxEpisodesToKeep) {
		return nil
	}
	slices.SortStableFunc(dated, func(a, b item.Episode) int { return a.PublishedAt.Compare(b.PublishedAt.Time) })
	for _, ep := range dated {
		file := ep.AudioFile.V
		if ep.ID == justAdded || file.Metadata.Path == "" {
			continue
		}
		if err := os.Remove(file.Metadata.Path); err != nil {
			slog.Warn("podcast: remove oldest episode file", "path", file.Metadata.Path, "error", err)
			return nil
		}
		return h.deleteEpisode(ctx, it, ep.ID, file.Ino)
	}
	return nil
}

// deleteEpisode removes an episode row with its playlist entries and progress,
// drops its audio file from the item's libraryFiles and recounts numEpisodes.
// It runs on a *sql.Tx because store.DeleteEpisodes (shared with the item
// delete paths, until they move) takes one.
func (h *Handler) deleteEpisode(ctx context.Context, it *item.Item, episodeID, ino string) error {
	tx, err := h.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // a no-op after Commit; the error to report is the one returned
	changes, err := store.DeleteEpisodes(ctx, tx, []string{episodeID})
	if err != nil {
		return err
	}
	var stored db.JSON[[]db.LibraryFile]
	if err := tx.QueryRowContext(ctx, `SELECT libraryFiles FROM libraryItems WHERE id=?`, it.ID).Scan(&stored); err != nil {
		return err
	}
	files := slices.DeleteFunc(stored.V, func(f db.LibraryFile) bool { return f.Ino == ino })
	encoded, err := db.JSON[[]db.LibraryFile]{V: nonNilFiles(files)}.Value()
	if err != nil {
		return err
	}
	now := db.Now()
	if _, err := tx.ExecContext(ctx, `UPDATE libraryItems SET libraryFiles=?,updatedAt=? WHERE id=?`, encoded, now, it.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE podcasts SET numEpisodes=(SELECT COUNT(*) FROM podcastEpisodes WHERE podcastId=?),updatedAt=? WHERE id=?`, it.Podcast.ID, now, it.Podcast.ID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	playlistview.Announce(ctx, h.db.SQL(), h.emit, changes)
	return nil
}

func nonNilFiles(files []db.LibraryFile) []db.LibraryFile {
	if files == nil {
		return []db.LibraryFile{}
	}
	return files
}

// notifyEpisodeDownloaded fires onPodcastEpisodeDownloaded
// (NotificationManager.js:21-46); delivery is not awaited, as upstream.
func (h *Handler) notifyEpisodeDownloaded(it *item.Item, ep item.Episode) {
	p := it.Podcast
	var library db.Library
	_ = h.db.Get(h.ctx, &library, `SELECT * FROM libraries WHERE id=?`, db.Val(it.LibraryID)) // a missing library is named "Unknown"
	libraryName := cmp.Or(db.Val(library.Name), "Unknown")
	h.notifyAsync("onPodcastEpisodeDownloaded", map[string]any{
		"libraryItemId": it.ID, "libraryId": db.Val(it.LibraryID), "libraryName": libraryName,
		"mediaTags": strings.Join(p.Tags.V, ", "), "podcastTitle": db.Val(p.Title), "podcastAuthor": db.Val(p.Author),
		"podcastDescription": db.Val(p.Description), "podcastGenres": strings.Join(p.Genres.V, ", "),
		"episodeId": ep.ID, "episodeTitle": db.Val(ep.Title), "episodeSubtitle": db.Val(ep.Subtitle), "episodeDescription": db.Val(ep.Description),
	})
}

// notifyFeedFailure fires onRSSFeedDisabled or onRSSFeedFailed
// (NotificationManager.js:81-121, PodcastManager.js:346-351).
func (h *Handler) notifyFeedFailure(event string, it *item.Item, numFailed int) {
	h.notifyAsync(event, map[string]any{"feedUrl": db.Val(it.Podcast.FeedURL), "numFailed": numFailed, "title": cmp.Or(db.Val(it.Podcast.Title), "Unknown Title")})
}

func (h *Handler) notifyAsync(event string, data map[string]any) {
	if h.notify == nil {
		return
	}
	h.spawn(func() {
		if err := h.notify(h.ctx, event, data); err != nil {
			slog.Error("podcast: notification failed", "event", event, "error", err)
		}
	})
}

// episodeOf finds an episode of the loaded item.
func episodeOf(it *item.Item, id string) (item.Episode, bool) {
	i := slices.IndexFunc(it.Podcast.Episodes, func(e item.Episode) bool { return e.ID == id })
	if i < 0 {
		return item.Episode{}, false
	}
	return it.Podcast.Episodes[i], true
}

func (h *Handler) getEpisode(w http.ResponseWriter, r *http.Request, it *item.Item) error {
	ep, ok := episodeOf(it, r.PathValue("episodeId"))
	if !ok {
		return httpx.NotFound()
	}
	return httpx.JSON(w, r, ep.Full(it.ID))
}

// nullable tells an absent JSON key from an explicit null.
type nullable[T any] struct {
	Set bool
	V   *T
}

func (n *nullable[T]) UnmarshalJSON(data []byte) error {
	n.Set = true
	if string(data) == "null" {
		return nil
	}
	n.V = new(T)
	return json.Unmarshal(data, n.V)
}

// episodePatch is the body of PATCH /api/podcasts/{id}/episode/{episodeId}; a
// nil field is absent. Enclosure null clears the enclosure.
type episodePatch struct {
	Title       *string                  `json:"title"`
	Subtitle    *string                  `json:"subtitle"`
	Description *string                  `json:"description"`
	PubDate     *string                  `json:"pubDate"`
	Episode     *string                  `json:"episode"`
	Season      *string                  `json:"season"`
	EpisodeType *string                  `json:"episodeType"`
	Enclosure   nullable[enclosurePatch] `json:"enclosure"`
	Chapters    *[]db.Chapter            `json:"chapters"`
	PublishedAt *int64                   `json:"publishedAt"`
}

// enclosurePatch replaces the enclosure when it has a url.
type enclosurePatch struct {
	URL    *string `json:"url"`
	Type   string  `json:"type"`
	Length *int64  `json:"length"`
}

// patchedColumns are the columns apply may change.
var patchedColumns = []string{"season", "episode", "episodeType", "title", "subtitle", "description", "pubDate", "enclosureURL", "enclosureSize",
	"enclosureType", "publishedAt", "chapters", "updatedAt"}

// apply writes the present fields of p to row. Description and subtitle are
// sanitized HTML (PodcastController.js:430-445). Chapters are ignored unless
// every one has a title and a non-zero start (js:451).
func (p episodePatch) apply(row *db.PodcastEpisode) {
	set := func(dst **string, v *string) {
		if v != nil && db.Val(*dst) != *v {
			*dst = v
		}
	}
	clean := func(v *string) *string {
		if v == nil || *v == "" {
			return v
		}
		return db.Ptr(textclean.Sanitize(*v))
	}
	set(&row.Title, p.Title)
	set(&row.Subtitle, clean(p.Subtitle))
	set(&row.Description, clean(p.Description))
	set(&row.PubDate, p.PubDate)
	set(&row.Episode, p.Episode)
	set(&row.Season, p.Season)
	set(&row.EpisodeType, p.EpisodeType)
	if p.Enclosure.Set {
		if e := p.Enclosure.V; e == nil {
			row.EnclosureURL, row.EnclosureSize, row.EnclosureType = nil, nil, nil
		} else if e.URL != nil {
			row.EnclosureURL, row.EnclosureSize, row.EnclosureType = e.URL, e.Length, ptrIfSet(e.Type)
		}
	}
	if c := p.Chapters; c != nil && !slices.ContainsFunc(*c, func(ch db.Chapter) bool { return ch.Title == "" || ch.Start == 0 }) {
		row.Chapters.V = *c
	}
	if ms := p.PublishedAt; ms != nil && (row.PublishedAt.IsZero() || row.PublishedAt.Millis() != *ms) {
		row.PublishedAt = db.FromMillis(*ms)
	}
}

func (h *Handler) updateEpisode(w http.ResponseWriter, r *http.Request, it *item.Item) error {
	id := r.PathValue("episodeId")
	if _, ok := episodeOf(it, id); !ok {
		return httpx.Error(http.StatusNotFound, "Episode not found")
	}
	patch, err := httpx.DecodeLimit[episodePatch](w, r, 2<<20)
	if err != nil {
		return err
	}
	changed := false
	err = h.db.Tx(r.Context(), func(ctx context.Context) error {
		var row db.PodcastEpisode
		if err := h.db.Get(ctx, &row, `SELECT * FROM podcastEpisodes WHERE id=?`, id); err != nil {
			return err
		}
		before := row
		patch.apply(&row)
		if changed = !reflect.DeepEqual(before, row); !changed {
			return nil
		}
		row.UpdatedAt = db.Now()
		return h.db.Update(ctx, "podcastEpisodes", &row, patchedColumns...)
	})
	if err != nil {
		return httpx.Internal("database write failed", err)
	}
	if changed {
		if updated := h.announce(r.Context(), it.ID); updated != nil {
			it = updated
		}
	}
	return httpx.JSON(w, r, h.expanded(it))
}

func (h *Handler) removeEpisode(w http.ResponseWriter, r *http.Request, it *item.Item) error {
	id := r.PathValue("episodeId")
	ep, ok := episodeOf(it, id)
	if !ok {
		return httpx.NotFound()
	}
	file := ep.AudioFile.V
	if r.URL.Query().Get("hard") == "1" && file.Metadata.Path != "" {
		if err := os.Remove(file.Metadata.Path); err != nil {
			slog.Warn("podcast: hard delete episode file", "path", file.Metadata.Path, "error", err)
		}
	}
	if err := h.deleteEpisode(r.Context(), it, id, file.Ino); err != nil {
		return httpx.Internal("database write failed", err)
	}
	updated := h.announce(r.Context(), it.ID)
	if updated == nil {
		return httpx.Internal("database read failed", errors.New("reload item after episode delete"))
	}
	return httpx.JSON(w, r, updated.Full())
}
