package podcasts

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/filekind"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/mediafiles"
	"audiobookshelf-compat/internal/mediatools"
	"audiobookshelf-compat/internal/podcasts/read"
	"audiobookshelf-compat/internal/store"
)

func (h *Handler) registerEpisode(d *downloadTask, path string) error {
	probe, err := h.probeEpisode(d.ctx, path)
	if err != nil {
		return err
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	stamp := dbtime.Format(now)
	ino := strconv.FormatInt(st.ModTime().UnixNano(), 10)
	if raw := st.Sys(); raw != nil {
		if info, ok := raw.(*syscall.Stat_t); ok {
			ino = strconv.FormatUint(info.Ino, 10)
		}
	}
	ext := filepath.Ext(path)
	filename := filepath.Base(path)
	rel := filename
	duration, _ := strconv.ParseFloat(probe.Format.Duration, 64)
	bitRate, _ := strconv.ParseInt(probe.Format.BitRate, 10, 64)
	size := st.Size()
	var stream mediatools.Stream
	for _, s := range probe.Streams {
		if s.Type == "audio" {
			stream = s
			break
		}
	}
	if bitRate == 0 {
		bitRate, _ = strconv.ParseInt(stream.BitRate, 10, 64)
	}
	format := probe.Format.LongName
	if format == "" {
		format = probe.Format.Name
	}
	mime := filekind.AudioMime(ext)
	mtime := float64(st.ModTime().UnixMilli())
	fileMeta := mediafiles.FileMetadata{Filename: filename, Ext: ext, Path: filepath.ToSlash(path), RelPath: rel, Size: float64(size), MtimeMs: mtime, CtimeMs: mtime, BirthtimeMs: mtime}
	audio := mediafiles.AudioFile{Index: 1, Ino: ino, Metadata: fileMeta, AddedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli(), Format: format, Duration: duration, BitRate: &bitRate,
		Codec: &stream.Codec, TimeBase: &stream.TimeBase, Channels: &stream.Channels, ChannelLayout: &stream.ChannelLayout, Chapters: []mediafiles.AudioChapter{}, MetaTags: map[string]string{}, MimeType: mime}
	libFile := mediafiles.File{"ino": ino, "metadata": fileMeta, "isSupplementary": nil, "addedAt": now.UnixMilli(), "updatedAt": now.UnixMilli(), "fileType": "audio"}
	episodeID, err := ids.New()
	if err != nil {
		return err
	}
	extra := map[string]any{}
	if g := d.episode.GUID; g != nil && *g != "" {
		extra["guid"] = *g
	}
	extraJSON, _ := json.Marshal(extra)
	audioJSON, _ := json.Marshal(audio)
	chaptersJSON, _ := json.Marshal([]any{})
	enclosureURL, enclosureType := d.episode.Enclosure.URL, d.episode.Enclosure.Type
	var enclosureSize any
	if i, e := strconv.ParseInt(string(d.episode.Enclosure.Length), 10, 64); e == nil {
		enclosureSize = i
	}
	title := episodeTitle(d)
	subtitle, description, pubDate, season, episodeNo := d.episode.Subtitle, d.episode.Description, d.episode.PubDate, d.episode.Season, d.episode.Episode
	kind := cmp.Or(d.episode.EpisodeType, "full")
	var published any
	if n := d.episode.PublishedAt; n != nil && *n != 0 {
		published = time.UnixMilli(*n).UTC().Format(time.RFC3339Nano)
	}
	tx, err := h.db.BeginTx(d.ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(d.ctx, `INSERT INTO podcastEpisodes (id,"index",season,episode,episodeType,title,subtitle,description,pubDate,enclosureURL,enclosureSize,enclosureType,publishedAt,audioFile,chapters,extraData,createdAt,updatedAt,podcastId) VALUES (?,NULL,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, episodeID, store.NullIfEmpty(season), store.NullIfEmpty(episodeNo), kind, title, store.NullIfEmpty(subtitle), store.NullIfEmpty(description), store.NullIfEmpty(pubDate), store.NullIfEmpty(enclosureURL), enclosureSize, store.NullIfEmpty(enclosureType), published, string(audioJSON), string(chaptersJSON), string(extraJSON), stamp, stamp, d.item.MediaID)
	if err != nil {
		return err
	}
	var rawFiles string
	if err = tx.QueryRowContext(d.ctx, `SELECT libraryFiles FROM libraryItems WHERE id=?`, d.item.ID).Scan(&rawFiles); err != nil {
		return err
	}
	var files []mediafiles.File
	if err = json.Unmarshal([]byte(rawFiles), &files); err != nil {
		return err
	}
	sizeTotal := size
	for _, f := range files {
		sizeTotal += f.Size()
	}
	files = append(files, libFile)
	filesJSON, _ := json.Marshal(files)
	_, err = tx.ExecContext(d.ctx, `UPDATE libraryItems SET libraryFiles=?,size=?,updatedAt=? WHERE id=?`, string(filesJSON), sizeTotal, stamp, d.item.ID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(d.ctx, `UPDATE podcasts SET numEpisodes=(SELECT COUNT(*) FROM podcastEpisodes WHERE podcastId=?),updatedAt=? WHERE id=?`, d.item.MediaID, stamp, d.item.MediaID)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	updated, err := h.loadItem(d.ctx, d.item.ID)
	if err != nil {
		return err
	}
	if d.auto && updated.Podcast.MaxEpisodesToKeep > 0 {
		if err = h.trimOldestEpisode(d.ctx, updated, episodeID); err != nil {
			return err
		}
		updated, err = h.loadItem(d.ctx, d.item.ID)
		if err != nil {
			return err
		}
	}
	expanded := h.expanded(d.ctx, updated)
	h.emit.ToAdmins("item_updated", expanded)
	if episode, ok := episodeOf(updated, episodeID); ok {
		h.emit.ToAll("episode_added", struct {
			podcastread.EpisodeExpanded
			LibraryItem items.Expanded `json:"libraryItem"`
		}{episode.Expanded(d.item.ID), expanded})
		if d.auto { // PodcastManager.js:257: notifications only for auto downloaded episodes
			h.notifyEpisodeDownloaded(updated, episode)
		}
	}
	return nil
}

// notifyEpisodeDownloaded fires onPodcastEpisodeDownloaded
// (NotificationManager.js:21-46); delivery is not awaited, as upstream.
func (h *Handler) notifyEpisodeDownloaded(item items.Item, ep podcastread.Episode) {
	p := item.Podcast
	libraryName := "Unknown"
	_ = h.db.QueryRow(`SELECT name FROM libraries WHERE id=?`, item.LibraryID).Scan(&libraryName)
	h.notifyAsync("onPodcastEpisodeDownloaded", map[string]any{
		"libraryItemId": item.ID, "libraryId": item.LibraryID, "libraryName": libraryName,
		"mediaTags": strings.Join(p.Tags, ", "), "podcastTitle": str(p.Title), "podcastAuthor": str(p.Author),
		"podcastDescription": str(p.Description), "podcastGenres": strings.Join(p.Genres, ", "),
		"episodeId": ep.ID, "episodeTitle": str(ep.Title), "episodeSubtitle": str(ep.Subtitle), "episodeDescription": str(ep.Description),
	})
}

// notifyFeedFailure fires onRSSFeedDisabled or onRSSFeedFailed
// (NotificationManager.js:81-121, PodcastManager.js:346-351).
func (h *Handler) notifyFeedFailure(event string, item items.Item, numFailed int) {
	title := str(item.Podcast.Title)
	if title == "" {
		title = "Unknown Title"
	}
	h.notifyAsync(event, map[string]any{"feedUrl": str(item.Podcast.FeedURL), "numFailed": numFailed, "title": title})
}

func (h *Handler) notifyAsync(event string, data map[string]any) {
	if h.notify == nil {
		return
	}
	go func() {
		if err := h.notify(context.Background(), event, data); err != nil {
			logging.Errorf("[NotificationManager] %s: %v", event, err)
		}
	}()
}

func (h *Handler) trimOldestEpisode(ctx context.Context, item items.Item, justAdded string) error {
	var published int
	if err := h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM podcastEpisodes WHERE podcastId=? AND publishedAt IS NOT NULL`, item.MediaID).Scan(&published); err != nil {
		return err
	}
	if int64(published) <= item.Podcast.MaxEpisodesToKeep {
		return nil
	}
	rows, err := h.db.QueryContext(ctx, `SELECT id,audioFile FROM podcastEpisodes WHERE podcastId=? AND publishedAt IS NOT NULL ORDER BY publishedAt ASC`, item.MediaID)
	if err != nil {
		return err
	}
	type old struct{ id, path, ino string }
	var candidate *old
	for rows.Next() {
		var id string
		var audioRaw sql.NullString
		if err = rows.Scan(&id, &audioRaw); err != nil {
			rows.Close()
			return err
		}
		if id == justAdded {
			continue
		}
		audio := mediafiles.File(store.DecodeJSONObject([]byte(audioRaw.String)))
		if path := audio.Path(); path != "" {
			candidate = &old{id, path, audio.Ino()}
			break
		}
	}
	_ = rows.Close()
	if candidate == nil {
		return nil
	}
	if err = os.Remove(candidate.path); err != nil {
		return nil
	}
	return h.deleteEpisode(ctx, item, candidate.id, candidate.ino)
}
