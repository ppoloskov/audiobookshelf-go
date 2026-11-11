package podcast

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/item"
	"audiobookshelf-compat/internal/wire"
)

// maxEpisodeBytes bounds one downloaded episode.
const maxEpisodeBytes = 8 << 30

// episodeExts are the audio extensions kept in a download's file name; any
// other becomes .mp3.
var episodeExts = []string{".mp3", ".m4a", ".m4b", ".aac", ".wav", ".ogg", ".oga", ".flac", ".opus", ".mp4"}

// download is one episode in the queue. Handler.mu guards its fields once it
// is queued.
type download struct {
	id, url, filename                string
	item                             *item.Item
	episode                          feedEpisode
	auto                             bool
	createdAt, startedAt, finishedAt int64
	finished, failed, appendID       bool
}

func (d *download) title() string {
	return cmp.Or(strings.TrimSpace(d.episode.Title), "Episode")
}

// view is the download as clients see it (PodcastEpisodeDownload.toJSONForClient).
func (d *download) view() wire.EpisodeDownload {
	title, podcast := d.title(), d.item.Podcast
	out := wire.EpisodeDownload{ID: d.id, EpisodeDisplayTitle: &title, URL: d.url, LibraryItemID: d.item.ID, IsFinished: d.finished, Failed: d.failed,
		AppendRandomID: d.appendID, CreatedAt: d.createdAt, PodcastTitle: podcast.Title, PodcastExplicit: bool(podcast.Explicit), Season: d.episode.Season,
		Episode: d.episode.Episode, EpisodeType: cmp.Or(d.episode.EpisodeType, "full"), PublishedAt: d.episode.PublishedAt, GUID: d.episode.GUID,
		LibraryID: d.item.LibraryID}
	if d.startedAt != 0 {
		out.StartedAt = &d.startedAt
	}
	if d.finishedAt != 0 {
		out.FinishedAt = &d.finishedAt
	}
	return out
}

// task is the download's 'download-podcast-episode' task record, unfinished.
func (d *download) task() events.Task {
	title := d.title()
	t := events.NewTask(d.id, "download-podcast-episode", struct {
		LibraryID     string `json:"libraryId"`
		LibraryItemID string `json:"libraryItemId"`
	}{LibraryID: db.Val(d.item.LibraryID), LibraryItemID: d.item.ID}).
		Titled("Downloading episode", "MessageDownloadingEpisode", nil).
		Described(`Downloading episode "`+title+`".`, "MessageTaskDownloadingEpisodeDescription", title)
	t.StartedAt = d.startedAt
	return t
}

// enqueue queues the episodes of it, one at a time. An episode already running
// or queued for the item is skipped; the first download starts at once and is
// announced as started, later ones as queued.
func (h *Handler) enqueue(it *item.Item, episodes []feedEpisode, auto bool) error {
	added := make([]*download, 0, len(episodes))
	for _, episode := range episodes {
		if episode.Enclosure.URL == "" {
			return errors.New("Invalid episode: enclosure URL is required")
		}
		link := httpURL(episode.Enclosure.URL)
		if link == "" {
			return errors.New("Invalid episode enclosure URL")
		}
		d := &download{id: ids.Must(), item: it, episode: episode, url: link, auto: auto, createdAt: time.Now().UnixMilli()}
		d.filename = episodeFilename(d.title(), link)
		added = append(added, d)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	for _, d := range added {
		duplicate := func(o *download) bool { return o != nil && o.item.ID == d.item.ID && o.url == d.url }
		switch {
		case duplicate(h.current), slices.ContainsFunc(h.queue, duplicate):
		case h.current == nil:
			h.start(d)
			h.wg.Add(1)
			go h.work(d)
		default:
			h.queue = append(h.queue, d)
			h.emit.ToAll("episode_download_queued", d.view())
		}
	}
	return nil
}

// start makes d the running download and announces it. h.mu is held.
func (h *Handler) start(d *download) {
	h.current = d
	d.startedAt = time.Now().UnixMilli()
	h.emit.ToAll("episode_download_started", d.view())
	h.emit.TaskStarted(d.task())
}

// work downloads d, then each download that was queued behind it.
func (h *Handler) work(d *download) {
	defer h.wg.Done()
	for d != nil {
		path, err := h.fetch(d)
		if err == nil {
			err = h.register(d, path)
		}
		if err != nil {
			slog.Error("podcast: episode download failed", "episode", d.title(), "error", err)
			if path != "" {
				_ = os.Remove(path) // the file is not in the library; best effort
			}
		} else {
			slog.Info("podcast: downloaded episode", "episode", d.title())
		}
		d = h.finish(d, err)
	}
}

// finish records the outcome of d and starts the next queued download, which
// it returns.
func (h *Handler) finish(d *download, runErr error) *download {
	h.mu.Lock()
	defer h.mu.Unlock()
	d.failed, d.finished, d.finishedAt = runErr != nil, true, time.Now().UnixMilli()
	t := d.task()
	t.IsFinished, t.FinishedAt = true, &d.finishedAt
	if runErr != nil {
		t = t.Failed("Failed", "MessageTaskFailed")
	}
	h.emit.TaskFinished(t)
	h.emit.ToAll("episode_download_finished", d.view())
	h.current = nil
	if h.closed || len(h.queue) == 0 {
		return nil
	}
	next := h.queue[0]
	h.queue = h.queue[1:]
	h.start(next)
	return next
}

// fetch downloads the episode into the item folder through a temporary file
// and returns its final path. A name already taken gets " (<download id>)".
func (h *Handler) fetch(d *download) (string, error) {
	dir := d.item.Path
	if dir == nil {
		return "", errors.New("item has no path")
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		return "", err
	}
	target := filepath.Join(*dir, d.filename)
	if _, err := os.Stat(target); err == nil {
		h.mu.Lock()
		d.appendID = true
		h.mu.Unlock()
		target = filepath.Join(*dir, episodeFilename(d.title()+" ("+d.id+")", d.url))
	}
	req, err := http.NewRequestWithContext(h.ctx, http.MethodGet, d.url, nil)
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
	tmp, err := os.CreateTemp(*dir, ".episode-*.download")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name()) // gone after the rename; removes a failed download
	written, err := io.Copy(tmp, io.LimitReader(resp.Body, maxEpisodeBytes+1))
	if err == nil && written > maxEpisodeBytes {
		err = errors.New("episode download exceeds 8 GiB limit")
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	return target, os.Rename(tmp.Name(), target)
}

// episodeFilename is the download's file name: the title without characters
// that are unsafe in file names, and an audio extension from the URL.
func episodeFilename(title, rawURL string) string {
	ext := ".mp3"
	if u, err := url.Parse(rawURL); err == nil {
		if e := strings.ToLower(filepath.Ext(u.Path)); slices.Contains(episodeExts, e) {
			ext = e
		}
	}
	name := strings.TrimSpace(strings.NewReplacer("/", " ", "\\", " ", "?", "", ":", " - ", "*", "", "|", "", "\"", "", "<", "", ">", "").Replace(strings.TrimSpace(title)))
	return cmp.Or(name, "Episode") + ext
}

// downloadsOf reports the queued downloads of an item and its running one.
func (h *Handler) downloadsOf(match func(*item.Item) bool) (queued []wire.EpisodeDownload, current *wire.EpisodeDownload) {
	h.mu.Lock()
	defer h.mu.Unlock()
	queued = []wire.EpisodeDownload{}
	for _, d := range h.queue {
		if match(d.item) {
			queued = append(queued, d.view())
		}
	}
	if h.current != nil && match(h.current.item) {
		view := h.current.view()
		current = &view
	}
	return queued, current
}

// EpisodeDownloads reports the queued downloads of a library item and the one
// running now, if it belongs to the item (LibraryItemController.findOne).
func (h *Handler) EpisodeDownloads(libraryItemID string) (queued []wire.EpisodeDownload, current *wire.EpisodeDownload) {
	return h.downloadsOf(func(it *item.Item) bool { return it.ID == libraryItemID })
}

// clear drops the queued downloads of itemID and tells the clients. The running
// download finishes.
func (h *Handler) clear(itemID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	before := len(h.queue)
	h.queue = slices.DeleteFunc(h.queue, func(d *download) bool { return d.item.ID == itemID })
	if len(h.queue) != before {
		h.emit.ToAll("episode_download_queue_cleared", itemID)
	}
}
