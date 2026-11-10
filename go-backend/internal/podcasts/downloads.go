package podcasts

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/mediatools"
	"audiobookshelf-compat/internal/wire"
)

type downloadTask struct {
	id                               string
	item                             items.Item
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

func (h *Handler) enqueue(item items.Item, episodes []Episode, auto bool) error {
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
	queued, _ := h.EpisodeDownloads(itemID)
	return queued
}

// EpisodeDownloads reports the queued downloads of a library item and the one
// running now, if it belongs to the item (LibraryItemController.findOne,
// server/controllers/LibraryItemController.js:90-95).
func (h *Handler) EpisodeDownloads(libraryItemID string) (queued []wire.EpisodeDownload, current *wire.EpisodeDownload) {
	h.queueMu.Lock()
	defer h.queueMu.Unlock()
	queued = make([]wire.EpisodeDownload, 0)
	for _, d := range h.queue {
		if d.item.ID == libraryItemID {
			queued = append(queued, d.clientJSON())
		}
	}
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
	h.queueMu.Lock() // pollers and Close read these fields under queueMu
	d.ctx = ctx
	d.cancel = cancel
	d.startedAt = time.Now().UnixMilli()
	started := d.clientJSON()
	h.queueMu.Unlock()
	h.emit.ToAll("episode_download_started", started)
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
	} else {
		logging.Infof("[PodcastManager] Successfully downloaded podcast episode %q", episodeTitle(d)) // PodcastManager.js:163
	}
	h.queueMu.Lock()
	d.failed = err != nil
	d.finished = true
	d.finishedAt = time.Now().UnixMilli()
	h.queueMu.Unlock()
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
