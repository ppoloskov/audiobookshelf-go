package podcasts

import (
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/filekind"
	"audiobookshelf-compat/internal/mediatools"
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/libitem"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/wire"
)

type downloadTask struct {
	id                               string
	item                             libitem.Item
	episode                          Episode
	url, filename                    string
	auto                             bool
	createdAt, startedAt, finishedAt int64
	finished, failed, appendID       bool
	ctx                              context.Context
	cancel                           context.CancelFunc
}

func (d *downloadTask) clientJSON() wire.EpisodeDownload {
	title := d.episode.Title
	libraryID := d.item.LibraryID
	out := wire.EpisodeDownload{
		ID: d.id, EpisodeDisplayTitle: &title, URL: d.url, LibraryItemID: d.item.ID, IsFinished: d.finished, Failed: d.failed,
		AppendRandomID: d.appendID, CreatedAt: d.createdAt, PodcastExplicit: d.item.Podcast.Explicit,
		Season: d.episode.Season, Episode: d.episode.Episode, EpisodeType: cmp.Or(d.episode.EpisodeType, "full"), PublishedAt: d.episode.PublishedAt, GUID: d.episode.GUID,
	}
	if libraryID != "" {
		out.LibraryID = &libraryID
	}
	if d.startedAt != 0 {
		out.StartedAt = &d.startedAt
	}
	if d.finishedAt != 0 {
		out.FinishedAt = &d.finishedAt
	}
	out.PodcastTitle = d.item.Podcast.Title
	return out
}

func (h *Handler) enqueue(item libitem.Item, episodes []Episode, auto bool) error {
	var added []*downloadTask
	for _, episode := range episodes {
		if episode.Enclosure.URL == "" {
			return errors.New("Invalid episode: enclosure URL is required")
		}
		target := validFeedURL(episode.Enclosure.URL)
		if target == "" {
			return errors.New("Invalid episode enclosure URL")
		}
		title := cmp.Or(strings.TrimSpace(episode.Title), "Episode")
		taskID, err := ids.New()
		if err != nil {
			return err
		}
		d := &downloadTask{id: taskID, item: item, episode: episode, url: target, filename: episodeFilename(title, target), auto: auto, createdAt: time.Now().UnixMilli()}
		added = append(added, d)
	}
	h.queueMu.Lock()
	for _, d := range added {
		duplicate := false
		if h.current != nil && h.current.item.ID == d.item.ID && h.current.url == d.url {
			duplicate = true
		}
		for _, q := range h.queue {
			if q.item.ID == d.item.ID && q.url == d.url {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		if h.current == nil {
			h.current = d
			h.queueMu.Unlock()
			h.startDownload(d)
			h.queueMu.Lock()
		} else {
			h.queue = append(h.queue, d)
			h.emit.ToAll("episode_download_queued", d.clientJSON())
		}
	}
	h.queueMu.Unlock()
	return nil
}

func (h *Handler) downloadsFor(itemID string) []wire.EpisodeDownload {
	h.queueMu.Lock()
	defer h.queueMu.Unlock()
	out := make([]wire.EpisodeDownload, 0)
	for _, d := range h.queue {
		if d.item.ID == itemID {
			out = append(out, d.clientJSON())
		}
	}
	return out
}

// EpisodeDownloads reports the queued downloads of a library item and the one
// running now, if it belongs to the item (LibraryItemController.findOne,
// server/controllers/LibraryItemController.js:90-95).
func (h *Handler) EpisodeDownloads(libraryItemID string) (queued []wire.EpisodeDownload, current *wire.EpisodeDownload) {
	queued = h.downloadsFor(libraryItemID)
	h.queueMu.Lock()
	defer h.queueMu.Unlock()
	if h.current != nil && h.current.item.ID == libraryItemID {
		download := h.current.clientJSON()
		current = &download
	}
	return queued, current
}

func (h *Handler) clearQueue(itemID string) {
	h.queueMu.Lock()
	before := len(h.queue)
	next := h.queue[:0]
	for _, d := range h.queue {
		if itemID == "" || d.item.ID != itemID {
			next = append(next, d)
		}
	}
	h.queue = next
	changed := len(h.queue) != before
	h.queueMu.Unlock()
	if changed {
		h.emit.ToAll("episode_download_queue_cleared", itemID)
	}
}

// episodeTaskData is the data of the download-podcast-episode task.
type episodeTaskData struct {
	LibraryID     string `json:"libraryId"`
	LibraryItemID string `json:"libraryItemId"`
}

// task is the download's 'download-podcast-episode' task, unfinished.
func (d *downloadTask) task() events.Task {
	title := episodeTitle(d)
	t := events.NewTask(d.id, "download-podcast-episode", episodeTaskData{LibraryID: d.item.LibraryID, LibraryItemID: d.item.ID}).
		Titled("Downloading episode", "MessageDownloadingEpisode", nil).
		Described(`Downloading episode "`+title+`".`, "MessageTaskDownloadingEpisodeDescription", title)
	t.StartedAt = d.startedAt
	return t
}

func (h *Handler) startDownload(d *downloadTask) {
	ctx, cancel := context.WithCancel(context.Background())
	d.ctx = ctx
	d.cancel = cancel
	d.startedAt = time.Now().UnixMilli()
	h.emit.ToAll("episode_download_started", d.clientJSON())
	h.emit.TaskStarted(d.task())
	go h.runDownload(d)
}

func (h *Handler) runDownload(d *downloadTask) {
	path, err := h.fetchEpisode(d)
	if err == nil {
		err = h.registerEpisode(d, path)
	}
	if err != nil {
		logging.Errorf("[PodcastManager] Podcast Episode download failed %q: %v", episodeTitle(d), err) // PodcastManager.js:180
		if path != "" {
			_ = os.Remove(path)
		}
		h.queueMu.Lock()
		d.failed = true
		d.finished = true
		d.finishedAt = time.Now().UnixMilli()
		h.queueMu.Unlock()
	} else {
		logging.Infof("[PodcastManager] Successfully downloaded podcast episode %q", episodeTitle(d)) // PodcastManager.js:163
		h.queueMu.Lock()
		d.finished = true
		d.finishedAt = time.Now().UnixMilli()
		h.queueMu.Unlock()
	}
	if d.cancel != nil {
		d.cancel()
	}
	finished := d.task()
	finished.IsFinished, finished.FinishedAt = true, &d.finishedAt
	if err != nil {
		finished = finished.Failed("Failed", "MessageTaskFailed")
	}
	h.emit.TaskFinished(finished)
	h.emit.ToAll("episode_download_finished", d.clientJSON())
	h.queueMu.Lock()
	if h.current == d {
		h.current = nil
	}
	next := (*downloadTask)(nil)
	if len(h.queue) > 0 {
		next = h.queue[0]
		h.queue = h.queue[1:]
		h.current = next
	}
	h.queueMu.Unlock()
	if next != nil {
		h.startDownload(next)
	}
}

func (h *Handler) fetchEpisode(d *downloadTask) (string, error) {
	u, err := url.Parse(d.url)
	if err != nil {
		return "", err
	}
	itemDir := d.item.Path
	if err = os.MkdirAll(itemDir, 0755); err != nil {
		return "", err
	}
	targetName := d.filename
	target := filepath.Join(itemDir, targetName)
	if _, err = os.Stat(target); err == nil {
		h.queueMu.Lock()
		d.appendID = true
		h.queueMu.Unlock()
		targetName = episodeFilename(episodeTitle(d)+" ("+d.id+")", d.url)
		target = filepath.Join(itemDir, targetName)
	}
	req, err := http.NewRequestWithContext(d.ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", "audiobookshelf (+https://audiobookshelf.org; like iTMS)")
	resp, err := h.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("episode download returned %s", resp.Status)
	}
	tmp, err := os.CreateTemp(itemDir, ".episode-*.download")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	written, err := io.Copy(tmp, io.LimitReader(resp.Body, 8<<30+1))
	if err == nil && written > 8<<30 {
		err = errors.New("episode download exceeds 8 GiB limit")
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err = os.Rename(tmpName, target); err != nil {
		return "", err
	}
	if _, err = h.probeEpisode(d.ctx, target); err != nil {
		_ = os.Remove(target)
		return "", err
	}
	return target, nil
}

func (h *Handler) probeEpisode(ctx context.Context, path string) (mediatools.Info, error) {
	p, err := mediatools.Probe(ctx, path)
	if err != nil {
		return p, fmt.Errorf("ffprobe audio file: %w", err)
	}
	for _, s := range p.Streams {
		if s.Type == "audio" {
			return p, nil
		}
	}
	return p, errors.New("Invalid media file: no audio stream found")
}

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
	fileMeta := map[string]any{"filename": filename, "ext": ext, "path": filepath.ToSlash(path), "relPath": rel, "size": size, "mtimeMs": st.ModTime().UnixMilli(), "ctimeMs": st.ModTime().UnixMilli(), "birthtimeMs": st.ModTime().UnixMilli()}
	audio := map[string]any{"index": 1, "ino": ino, "metadata": fileMeta, "addedAt": now.UnixMilli(), "updatedAt": now.UnixMilli(), "trackNumFromMeta": nil, "discNumFromMeta": nil, "trackNumFromFilename": nil, "discNumFromFilename": nil, "manuallyVerified": false, "exclude": false, "error": nil, "format": format, "duration": duration, "bitRate": bitRate, "language": nil, "codec": stream.Codec, "timeBase": stream.TimeBase, "channels": stream.Channels, "channelLayout": stream.ChannelLayout, "chapters": []any{}, "embeddedCoverArt": nil, "metaTags": map[string]any{}, "mimeType": mime}
	libFile := map[string]any{"ino": ino, "metadata": fileMeta, "isSupplementary": nil, "addedAt": now.UnixMilli(), "updatedAt": now.UnixMilli(), "fileType": "audio"}
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
	var files []map[string]any
	if err = json.Unmarshal([]byte(rawFiles), &files); err != nil {
		return err
	}
	files = append(files, libFile)
	filesJSON, _ := json.Marshal(files)
	var sizeTotal int64
	for _, f := range files {
		m, _ := f["metadata"].(map[string]any)
		if v, ok := m["size"].(float64); ok {
			sizeTotal += int64(v)
		} else if v, ok := m["size"].(int64); ok {
			sizeTotal += v
		}
	}
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
			libitem.EpisodeExpanded
			LibraryItem libitem.Expanded `json:"libraryItem"`
		}{episode.Expanded(d.item.ID), expanded})
	}
	return nil
}

func (h *Handler) trimOldestEpisode(ctx context.Context, item libitem.Item, justAdded string) error {
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
		var audio map[string]any
		_ = json.Unmarshal([]byte(audioRaw.String), &audio)
		metadata, _ := audio["metadata"].(map[string]any)
		path, _ := metadata["path"].(string)
		ino := fmt.Sprint(audio["ino"])
		if path != "" {
			candidate = &old{id, path, ino}
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
	files := []map[string]any{}
	for _, f := range h.rawLibraryFiles(ctx, item.ID) {
		if f["ino"] != candidate.ino {
			files = append(files, f)
		}
	}
	encoded, _ := json.Marshal(files)
	now := dbtime.Now()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = sqlc.New(tx).DeletePlaylistMediaItemsByMediaID(ctx, candidate.id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM mediaProgresses WHERE mediaItemId=?`, candidate.id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM podcastEpisodes WHERE id=?`, candidate.id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE libraryItems SET libraryFiles=?,updatedAt=? WHERE id=?`, string(encoded), now, item.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE podcasts SET numEpisodes=(SELECT COUNT(*) FROM podcastEpisodes WHERE podcastId=?),updatedAt=? WHERE id=?`, item.MediaID, now, item.MediaID); err != nil {
		return err
	}
	return tx.Commit()
}

func episodeTitle(d *downloadTask) string {
	if strings.TrimSpace(d.episode.Title) != "" {
		return d.episode.Title
	}
	return "Episode"
}
func episodeFilename(title, rawURL string) string {
	ext := ""
	if u, e := url.Parse(rawURL); e == nil {
		ext = strings.ToLower(filepath.Ext(u.Path))
	}
	if !map[string]bool{".mp3": true, ".m4a": true, ".m4b": true, ".aac": true, ".wav": true, ".ogg": true, ".oga": true, ".flac": true, ".opus": true, ".mp4": true}[ext] {
		ext = ".mp3"
	}
	title = strings.TrimSpace(title)
	title = strings.NewReplacer("/", " ", "\\", " ", "?", "", ":", " - ", "*", "", "|", "", "\"", "", "<", "", ">", "").Replace(title)
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Episode"
	}
	return title + ext
}
