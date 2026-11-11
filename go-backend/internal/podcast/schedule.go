package podcast

import (
	"context"
	"log/slog"
	"time"

	"audiobookshelf-compat/internal/cronspec"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/item"
)

// Reschedule makes the item's auto-download cron entry follow its podcast: one
// entry while auto-download is on with a valid schedule, none otherwise
// (CronManager.checkUpdatePodcastCron, CronManager.js:231).
func (h *Handler) Reschedule(ctx context.Context, itemID string) {
	schedule := ""
	it, err := item.Get(ctx, h.db, itemID, item.Options{})
	if err == nil && it.Podcast != nil && bool(it.Podcast.AutoDownloadEpisodes) && cronspec.Valid(db.Val(it.Podcast.AutoDownloadSchedule)) {
		schedule = db.Val(it.Podcast.AutoDownloadSchedule)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	if entry, ok := h.entries[itemID]; ok {
		h.cron.Remove(entry)
		delete(h.entries, itemID)
	}
	if schedule == "" {
		return
	}
	entry, err := h.cron.AddFunc(schedule, func() { h.runScheduled(itemID) })
	if err != nil {
		slog.Error("podcast: schedule auto download", "item", itemID, "schedule", schedule, "error", err) // CronManager.js:175
		return
	}
	h.entries[itemID] = entry
	slog.Info("podcast: added to auto download cron", "podcast", db.Val(it.Podcast.Title), "schedule", schedule) // CronManager.js:249
}

// runScheduled checks the feed of an auto-download podcast and queues the
// episodes published since its last check (PodcastManager.runEpisodeCheck).
func (h *Handler) runScheduled(itemID string) {
	ctx := h.ctx
	it, err := h.load(ctx, itemID)
	if err != nil || it.Podcast == nil || it.Podcast.FeedURL == nil || !bool(it.Podcast.AutoDownloadEpisodes) {
		return
	}
	title, feedURL := db.Val(it.Podcast.Title), *it.Podcast.FeedURL
	after := lastCheck(it)
	slog.Info("podcast: runEpisodeCheck", "podcast", title, "lastCheck", time.UnixMilli(after).UTC().Format(time.RFC3339)) // PodcastManager.js:329
	parsed, err := h.fetchFeed(ctx, feedURL)
	if err != nil {
		slog.Error("podcast: failed to get feed", "podcast", title, "feed", feedURL, "error", err)
		h.feedFailed(ctx, it)
		return
	}
	h.mu.Lock()
	delete(h.failed, itemID)
	h.mu.Unlock()
	for _, ep := range it.Podcast.Episodes {
		after = max(after, ep.PublishedAt.Millis())
	}
	if found := filterNew(parsed.Episodes, it, after, int(db.Val(it.Podcast.MaxNewEpisodesToDownload))); len(found) > 0 {
		if err := h.enqueue(it, found, true); err != nil {
			slog.Error("podcast: queue new episodes", "item", itemID, "error", err)
		}
	}
	if err := h.markChecked(ctx, it); err != nil {
		slog.Error("podcast: save episode check", "item", itemID, "error", err)
	}
}

// feedFailed counts a failed scheduled check. At maxFailedChecks in a row it
// turns auto-download off (PodcastManager.js:336-351).
func (h *Handler) feedFailed(ctx context.Context, it *item.Item) {
	h.mu.Lock()
	h.failed[it.ID]++
	failed := h.failed[it.ID]
	disable := h.maxFailedChecks > 0 && failed >= h.maxFailedChecks
	if disable {
		delete(h.failed, it.ID)
	}
	h.mu.Unlock()
	if !disable {
		h.notifyFeedFailure("onRSSFeedFailed", it, failed)
		if err := h.markChecked(ctx, it); err != nil {
			slog.Error("podcast: save episode check", "item", it.ID, "error", err)
		}
		return
	}
	slog.Error("podcast: too many failed checks, disabling auto download", "podcast", db.Val(it.Podcast.Title), "failed", failed) // PodcastManager.js:343
	h.notifyFeedFailure("onRSSFeedDisabled", it, failed)
	now := db.Now()
	if _, err := h.db.Exec(ctx, `UPDATE podcasts SET autoDownloadEpisodes=0,lastEpisodeCheck=?,updatedAt=? WHERE id=?`, now, now, it.Podcast.ID); err != nil {
		slog.Error("podcast: disable auto download", "item", it.ID, "error", err)
	}
	h.Reschedule(ctx, it.ID)
	h.announce(ctx, it.ID)
}

// markChecked stores now as the podcast's last episode check.
func (h *Handler) markChecked(ctx context.Context, it *item.Item) error {
	now := db.Now()
	_, err := h.db.Exec(ctx, `UPDATE podcasts SET lastEpisodeCheck=?,updatedAt=? WHERE id=?`, now, now, it.Podcast.ID)
	return err
}

// lastCheck is the podcast's last episode check in milliseconds, 0 when never.
func lastCheck(it *item.Item) int64 {
	if t := it.Podcast.LastEpisodeCheck; !t.IsZero() {
		return t.Millis()
	}
	return 0
}

// filterNew picks the feed episodes published after the cutoff whose guid and
// enclosure URL the podcast does not have yet, at most limit of them (0 is no
// limit).
func filterNew(all []feedEpisode, it *item.Item, after int64, limit int) []feedEpisode {
	guids, urls := map[string]bool{}, map[string]bool{}
	for _, ep := range it.Podcast.Episodes {
		if extra := ep.ExtraData.V; extra != nil && db.Val(extra.GUID) != "" {
			guids[*extra.GUID] = true
		}
		urls[db.Val(ep.EnclosureURL)] = true
	}
	found := []feedEpisode{}
	for _, ep := range all {
		if ep.PublishedAt == nil || *ep.PublishedAt <= after || (ep.GUID != nil && guids[*ep.GUID]) || urls[ep.Enclosure.URL] {
			continue
		}
		if found = append(found, ep); limit > 0 && len(found) >= limit {
			break
		}
	}
	return found
}
