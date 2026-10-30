package podcasts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"audiobookshelf-compat/internal/dbtime"
)

type downloadTask struct {
	id                               string
	item                             podcastItem
	episode                          map[string]any
	url, filename                    string
	auto                             bool
	createdAt, startedAt, finishedAt int64
	finished, failed, appendID       bool
	ctx                              context.Context
	cancel                           context.CancelFunc
}

func (d *downloadTask) clientJSON() map[string]any {
	title, _ := d.episode["title"].(string)
	season := d.episode["season"]
	episode := d.episode["episode"]
	kind := d.episode["episodeType"]
	if kind == nil || kind == "" {
		kind = "full"
	}
	return map[string]any{"id": d.id, "episodeDisplayTitle": title, "url": d.url, "libraryItemId": d.item.ID, "libraryId": d.item.LibraryID, "isFinished": d.finished, "failed": d.failed, "appendRandomId": d.appendID, "startedAt": nullableInt64(d.startedAt), "createdAt": d.createdAt, "finishedAt": nullableInt64(d.finishedAt), "podcastTitle": nullableSQL(d.item.Title), "podcastExplicit": d.item.Explicit, "season": season, "episode": episode, "episodeType": kind, "publishedAt": d.episode["publishedAt"], "guid": d.episode["guid"]}
}
func nullableInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func (h *Handler) enqueue(item podcastItem, episodes []map[string]any, auto bool) error {
	var added []*downloadTask
	for _, episode := range episodes {
		enc, ok := episode["enclosure"].(map[string]any)
		if !ok {
			return errors.New("Invalid episode: enclosure URL is required")
		}
		raw, _ := enc["url"].(string)
		target := validFeedURL(raw)
		if target == "" {
			return errors.New("Invalid episode enclosure URL")
		}
		title, _ := episode["title"].(string)
		if strings.TrimSpace(title) == "" {
			title = "Episode"
		}
		d := &downloadTask{id: newUUID(), item: item, episode: episode, url: target, filename: episodeFilename(title, target), auto: auto, createdAt: time.Now().UnixMilli()}
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
			h.emit("*", "episode_download_queued", d.clientJSON())
		}
	}
	h.queueMu.Unlock()
	return nil
}

func (h *Handler) downloadsFor(itemID string) []map[string]any {
	h.queueMu.Lock()
	defer h.queueMu.Unlock()
	var out []map[string]any
	for _, d := range h.queue {
		if d.item.ID == itemID {
			out = append(out, d.clientJSON())
		}
	}
	return out
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
		h.emit("*", "episode_download_queue_cleared", itemID)
	}
}

func (h *Handler) startDownload(d *downloadTask) {
	ctx, cancel := context.WithCancel(context.Background())
	d.ctx = ctx
	d.cancel = cancel
	d.startedAt = time.Now().UnixMilli()
	h.emit("*", "episode_download_started", d.clientJSON())
	task := map[string]any{"id": d.id, "action": "download-podcast-episode", "data": map[string]any{"libraryId": d.item.LibraryID, "libraryItemId": d.item.ID}, "title": "Downloading episode", "titleKey": "MessageDownloadingEpisode", "titleSubs": nil, "description": "Downloading episode \"" + episodeTitle(d) + "\".", "descriptionKey": "MessageTaskDownloadingEpisodeDescription", "descriptionSubs": []string{episodeTitle(d)}, "error": nil, "errorKey": nil, "errorSubs": nil, "showSuccess": false, "isFailed": false, "isFinished": false, "startedAt": d.startedAt, "finishedAt": nil}
	h.emit("*", "task_started", task)
	go h.runDownload(d)
}

func (h *Handler) runDownload(d *downloadTask) {
	path, err := h.fetchEpisode(d)
	if err == nil {
		err = h.registerEpisode(d, path)
	}
	if err != nil {
		if path != "" {
			_ = os.Remove(path)
		}
		h.queueMu.Lock()
		d.failed = true
		d.finished = true
		d.finishedAt = time.Now().UnixMilli()
		h.queueMu.Unlock()
	} else {
		h.queueMu.Lock()
		d.finished = true
		d.finishedAt = time.Now().UnixMilli()
		h.queueMu.Unlock()
	}
	if d.cancel != nil {
		d.cancel()
	}
	taskError := any(nil)
	errorKey := any(nil)
	if err != nil {
		taskError = "Failed"
		errorKey = "MessageTaskFailed"
	}
	task := map[string]any{"id": d.id, "action": "download-podcast-episode", "data": map[string]any{"libraryId": d.item.LibraryID, "libraryItemId": d.item.ID}, "title": "Downloading episode", "titleKey": "MessageDownloadingEpisode", "titleSubs": nil, "description": "Downloading episode \"" + episodeTitle(d) + "\".", "descriptionKey": "MessageTaskDownloadingEpisodeDescription", "descriptionSubs": []string{episodeTitle(d)}, "error": taskError, "errorKey": errorKey, "errorSubs": nil, "showSuccess": false, "isFailed": err != nil, "isFinished": true, "startedAt": d.startedAt, "finishedAt": d.finishedAt}
	h.emit("*", "task_finished", task)
	h.emit("*", "episode_download_finished", d.clientJSON())
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
	if !h.allowPrivateNetwork {
		if err = rejectPrivateHost(d.ctx, d.url); err != nil {
			return "", err
		}
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

type probeOutput struct {
	Format struct {
		Name, FormatName, FormatLongName, Duration, Size, BitRate string            `json:"-"`
		NameTag                                                   string            `json:"name"`
		FormatNameTag                                             string            `json:"format_name"`
		FormatLongNameTag                                         string            `json:"format_long_name"`
		DurationTag                                               string            `json:"duration"`
		SizeTag                                                   string            `json:"size"`
		BitRateTag                                                string            `json:"bit_rate"`
		Tags                                                      map[string]string `json:"tags"`
	} `json:"format"`
	Streams []struct {
		CodecType     string            `json:"codec_type"`
		CodecName     string            `json:"codec_name"`
		TimeBase      string            `json:"time_base"`
		Channels      int               `json:"channels"`
		ChannelLayout string            `json:"channel_layout"`
		BitRate       string            `json:"bit_rate"`
		Tags          map[string]string `json:"tags"`
	} `json:"streams"`
	Chapters []struct {
		StartTime, EndTime string
		Tags               map[string]string `json:"tags"`
	} `json:"chapters"`
}

func (h *Handler) probeEpisode(ctx context.Context, path string) (probeOutput, error) {
	bin := strings.TrimSpace(os.Getenv("FFPROBE_PATH"))
	if bin == "" {
		bin = "ffprobe"
	}
	cmd := exec.CommandContext(ctx, bin, "-v", "error", "-show_format", "-show_streams", "-show_chapters", "-of", "json", path)
	raw, err := cmd.Output()
	if err != nil {
		return probeOutput{}, fmt.Errorf("ffprobe audio file: %w", err)
	}
	var p probeOutput
	if err = json.Unmarshal(raw, &p); err != nil {
		return p, err
	}
	for _, s := range p.Streams {
		if s.CodecType == "audio" {
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
	duration, _ := strconv.ParseFloat(probe.Format.DurationTag, 64)
	bitRate, _ := strconv.ParseInt(probe.Format.BitRateTag, 10, 64)
	size := st.Size()
	var stream struct {
		CodecName, TimeBase, ChannelLayout, BitRate string
		Channels                                    int
	}
	for _, s := range probe.Streams {
		if s.CodecType == "audio" {
			stream.CodecName, stream.TimeBase, stream.ChannelLayout, stream.Channels, stream.BitRate = s.CodecName, s.TimeBase, s.ChannelLayout, s.Channels, s.BitRate
			break
		}
	}
	if bitRate == 0 {
		bitRate, _ = strconv.ParseInt(stream.BitRate, 10, 64)
	}
	format := probe.Format.FormatLongNameTag
	if format == "" {
		format = probe.Format.FormatNameTag
	}
	mime := mimeForExt(ext)
	fileMeta := map[string]any{"filename": filename, "ext": ext, "path": filepath.ToSlash(path), "relPath": rel, "size": size, "mtimeMs": st.ModTime().UnixMilli(), "ctimeMs": st.ModTime().UnixMilli(), "birthtimeMs": st.ModTime().UnixMilli()}
	audio := map[string]any{"index": 1, "ino": ino, "metadata": fileMeta, "addedAt": now.UnixMilli(), "updatedAt": now.UnixMilli(), "trackNumFromMeta": nil, "discNumFromMeta": nil, "trackNumFromFilename": nil, "discNumFromFilename": nil, "manuallyVerified": false, "exclude": false, "error": nil, "format": format, "duration": duration, "bitRate": bitRate, "language": nil, "codec": stream.CodecName, "timeBase": stream.TimeBase, "channels": stream.Channels, "channelLayout": stream.ChannelLayout, "chapters": []any{}, "embeddedCoverArt": nil, "metaTags": map[string]any{}, "mimeType": mime}
	libFile := map[string]any{"ino": ino, "metadata": fileMeta, "isSupplementary": nil, "addedAt": now.UnixMilli(), "updatedAt": now.UnixMilli(), "fileType": "audio"}
	episodeID := newUUID()
	extra := map[string]any{}
	if guid, ok := d.episode["guid"].(string); ok && guid != "" {
		extra["guid"] = guid
	}
	extraJSON, _ := json.Marshal(extra)
	audioJSON, _ := json.Marshal(audio)
	chaptersJSON, _ := json.Marshal([]any{})
	var enclosure map[string]any
	enclosure, _ = d.episode["enclosure"].(map[string]any)
	enclosureURL, _ := enclosure["url"].(string)
	enclosureType, _ := enclosure["type"].(string)
	var enclosureSize any
	if n, ok := enclosure["length"].(string); ok {
		if i, e := strconv.ParseInt(n, 10, 64); e == nil {
			enclosureSize = i
		}
	} else if n, ok := enclosure["length"].(float64); ok {
		enclosureSize = int64(n)
	}
	title := episodeTitle(d)
	subtitle, _ := d.episode["subtitle"].(string)
	description, _ := d.episode["description"].(string)
	pubDate, _ := d.episode["pubDate"].(string)
	season, _ := d.episode["season"].(string)
	episodeNo, _ := d.episode["episode"].(string)
	kind, _ := d.episode["episodeType"].(string)
	if kind == "" {
		kind = "full"
	}
	var published any
	if n, ok := d.episode["publishedAt"].(int64); ok && n != 0 {
		published = time.UnixMilli(n).UTC().Format(time.RFC3339Nano)
	} else if n, ok := d.episode["publishedAt"].(float64); ok && n != 0 {
		published = time.UnixMilli(int64(n)).UTC().Format(time.RFC3339Nano)
	}
	tx, err := h.db.BeginTx(d.ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(d.ctx, `INSERT INTO podcastEpisodes (id,"index",season,episode,episodeType,title,subtitle,description,pubDate,enclosureURL,enclosureSize,enclosureType,publishedAt,audioFile,chapters,extraData,createdAt,updatedAt,podcastId) VALUES (?,NULL,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, episodeID, nullableAny(season), nullableAny(episodeNo), kind, title, nullableAny(subtitle), nullableAny(description), nullableAny(pubDate), nullableAny(enclosureURL), enclosureSize, nullableAny(enclosureType), published, string(audioJSON), string(chaptersJSON), string(extraJSON), stamp, stamp, d.item.MediaID)
	if err != nil {
		return err
	}
	var rawFiles string
	if err = tx.QueryRowContext(d.ctx, `SELECT libraryFiles FROM libraryItems WHERE id=?`, d.item.ID).Scan(&rawFiles); err != nil {
		return err
	}
	var files []map[string]any
	_ = json.Unmarshal([]byte(rawFiles), &files)
	files = append(files, libFile)
	filesJSON, _ := json.Marshal(files)
	var sizeTotal int64
	for _, f := range files {
		var m map[string]any
		_ = json.Unmarshal(mustJSON(f["metadata"]), &m)
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
	if d.auto && updated.MaxKeep > 0 {
		if err = h.trimOldestEpisode(d.ctx, updated, episodeID); err != nil {
			return err
		}
		updated, err = h.loadItem(d.ctx, d.item.ID)
		if err != nil {
			return err
		}
	}
	h.emit("@admins", "item_updated", updated.oldJSON())
	episode, ok := updated.episode(episodeID)
	if ok {
		event := episode.expandedJSON(d.item.ID)
		event["libraryItem"] = updated.oldJSON()
		h.emit("*", "episode_added", event)
	}
	return nil
}

func (h *Handler) trimOldestEpisode(ctx context.Context, item podcastItem, justAdded string) error {
	var published int
	_ = h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM podcastEpisodes WHERE podcastId=? AND publishedAt IS NOT NULL`, item.MediaID).Scan(&published)
	if published <= item.MaxKeep {
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
		var metadata map[string]any
		_ = json.Unmarshal(mustJSON(audio["metadata"]), &metadata)
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
	var existing []map[string]any
	_ = json.Unmarshal([]byte(item.LibraryFiles.String), &existing)
	for _, f := range existing {
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
	if _, err = tx.ExecContext(ctx, `DELETE FROM playlistMediaItems WHERE mediaItemId=?`, candidate.id); err != nil {
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
	if s, ok := d.episode["title"].(string); ok && strings.TrimSpace(s) != "" {
		return s
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
func mimeForExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".m4a", ".m4b", ".mp4":
		return "audio/mp4"
	case ".wav":
		return "audio/wav"
	case ".ogg", ".oga", ".opus":
		return "audio/ogg"
	case ".flac":
		return "audio/flac"
	case ".aac":
		return "audio/aac"
	default:
		return "audio/mpeg"
	}
}
