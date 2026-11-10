package podcasts

import (
	"context"
	"time"

	"audiobookshelf-compat/internal/cronspec"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/logging"
)

func (h *Handler) startSchedules() {
	if h.db == nil {
		return
	}
	h.scheduler = cronspec.New()
	rows, err := h.db.Query(`SELECT li.id FROM libraryItems li JOIN podcasts p ON p.id=li.mediaId WHERE li.mediaType='podcast' AND p.autoDownloadEpisodes=1`)
	if err != nil {
		h.scheduler = nil
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	_ = rows.Close()
	for _, id := range ids {
		h.addSchedule(id)
	}
	h.scheduler.Start()
}

// Reschedule re-evaluates an item's auto-download cron entry after its podcast
// changed (CronManager.checkUpdatePodcastCron, CronManager.js:231).
func (h *Handler) Reschedule(_ context.Context, itemID string) {
	h.removeSchedule(itemID)
	h.addSchedule(itemID)
}

func (h *Handler) removeSchedule(itemID string) {
	h.queueMu.Lock()
	defer h.queueMu.Unlock()
	if entry, ok := h.scheduleIDs[itemID]; ok && h.scheduler != nil {
		h.scheduler.Remove(entry)
		delete(h.scheduleIDs, itemID)
	}
}

func (h *Handler) addSchedule(itemID string) {
	if h.scheduler == nil {
		return
	}
	item, err := h.loadItem(context.Background(), itemID)
	if err != nil || !item.Podcast.AutoDownloadEpisodes || !cronspec.Valid(str(item.Podcast.AutoDownloadSchedule)) {
		return
	}
	schedule := str(item.Podcast.AutoDownloadSchedule)
	entry, err := h.scheduler.AddFunc(schedule, func() { h.runScheduled(itemID) })
	if err == nil {
		h.queueMu.Lock()
		h.scheduleIDs[itemID] = entry
		h.queueMu.Unlock()
		logging.Infof("[CronManager] Added podcast %q to auto dl episode cron %q", str(item.Podcast.Title), schedule) // CronManager.js:249
	} else {
		logging.Errorf("[PodcastManager] Failed to schedule podcast cron %s: %v", schedule, err) // CronManager.js:175
	}
}

func (h *Handler) runScheduled(itemID string) {
	item, err := h.loadItem(context.Background(), itemID)
	if err != nil || item.Podcast.FeedURL == nil || !item.Podcast.AutoDownloadEpisodes {
		return
	}
	after := int64(0)
	if v := item.Podcast.LastEpisodeCheck; v != nil {
		after = *v
	}
	logging.Infof("[PodcastManager] runEpisodeCheck: %q | Last check: %v", str(item.Podcast.Title), time.UnixMilli(after).UTC().Format(time.RFC3339)) // PodcastManager.js:329
	feed, err := h.getFeed(context.Background(), str(item.Podcast.FeedURL))
	if err != nil {
		logging.Errorf("[PodcastManager] Failed to get feed for podcast %q (%s): %v", str(item.Podcast.Title), str(item.Podcast.FeedURL), err)
		h.queueMu.Lock()
		h.failedCheckMap[itemID]++
		failed := h.failedCheckMap[itemID]
		disable := h.maxFailedChecks > 0 && failed >= h.maxFailedChecks
		if disable {
			delete(h.failedCheckMap, itemID)
		}
		h.queueMu.Unlock()
		if disable {
			logging.Errorf("[PodcastManager] runEpisodeCheck %d failed attempts at checking episodes for %q - disabling auto download", failed, str(item.Podcast.Title)) // PodcastManager.js:343
			h.notifyFeedFailure("onRSSFeedDisabled", item, failed)
			now := dbtime.Now()
			if _, err := h.db.Exec(`UPDATE podcasts SET autoDownloadEpisodes=0,lastEpisodeCheck=?,updatedAt=? WHERE id=?`, now, now, item.MediaID); err != nil {
				logging.Errorf("[podcasts] auto-download disable update failed for %s: %v", item.MediaID, err)
			}
			h.removeSchedule(itemID)
			if updated, loadErr := h.loadItem(context.Background(), itemID); loadErr == nil {
				h.emit.ToAdmins("item_updated", h.expanded(context.Background(), updated))
			}
		} else {
			h.notifyFeedFailure("onRSSFeedFailed", item, failed)
			now := dbtime.Now()
			if _, err := h.db.Exec(`UPDATE podcasts SET lastEpisodeCheck=?,updatedAt=? WHERE id=?`, now, now, item.MediaID); err != nil {
				logging.Errorf("[podcasts] episode check bookkeeping update failed for %s: %v", item.MediaID, err)
			}
		}
		return
	}
	h.queueMu.Lock()
	delete(h.failedCheckMap, itemID)
	h.queueMu.Unlock()
	for _, ep := range item.Podcast.Episodes {
		if v := ep.PublishedAt; v != nil && *v > after {
			after = *v
		}
	}
	found := filterNew(feed.Episodes, item, after, int(item.Podcast.MaxNewEpisodesToDownload))
	if len(found) > 0 {
		if err := h.enqueue(item, found, true); err != nil {
			logging.Errorf("[podcasts] scheduled episode enqueue failed for %s: %v", item.MediaID, err)
		}
	}
	now := dbtime.Now()
	if _, err := h.db.Exec(`UPDATE podcasts SET lastEpisodeCheck=?,updatedAt=? WHERE id=?`, now, now, item.MediaID); err != nil {
		logging.Errorf("[podcasts] episode check bookkeeping update failed for %s: %v", item.MediaID, err)
	}
}

func filterNew(all []Episode, item items.Item, after int64, limit int) []Episode {
	known := knownEpisodes(item)
	found := []Episode{}
	for _, ep := range all {
		if (ep.PublishedAt == nil || *ep.PublishedAt <= after) || (ep.GUID != nil && known["g:"+*ep.GUID]) || known["u:"+ep.Enclosure.URL] {
			continue
		}
		found = append(found, ep)
		if limit > 0 && len(found) >= limit {
			break
		}
	}
	return found
}
