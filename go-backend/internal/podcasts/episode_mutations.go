package podcasts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/mediafiles"
	"audiobookshelf-compat/internal/playlistview"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/textclean"
)

func (h *Handler) updateEpisode(w http.ResponseWriter, r *http.Request, item items.Item) error {
	episodeID := r.PathValue("episodeId")
	ep, ok := episodeOf(item, episodeID)
	if !ok {
		return httputil.Status(404, "Episode not found")
	}
	// PodcastController.js:423-455 matches exact keys and ignores wrong types.
	var body map[string]any
	if json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&body) != nil {
		return httputil.Status(400, "Invalid request body")
	}
	fields := map[string]any{}
	for _, field := range []struct {
		key string
		old *string
	}{
		{"title", ep.Title}, {"subtitle", ep.Subtitle}, {"description", ep.Description},
		{"pubDate", ep.PubDate}, {"episode", ep.Episode}, {"season", ep.Season}, {"episodeType", ep.EpisodeType},
	} {
		if value, ok := body[field.key].(string); ok {
			if (field.key == "description" || field.key == "subtitle") && value != "" {
				value = textclean.Sanitize(value)
			}
			if store.StringPtrValue(field.old) != value {
				fields[field.key] = value
			}
		}
	}
	if raw, exists := body["enclosure"]; exists {
		if raw == nil {
			if ep.Enclosure != nil {
				fields["enclosureURL"] = nil
				fields["enclosureSize"] = nil
				fields["enclosureType"] = nil
			}
		} else if enclosure, ok := raw.(map[string]any); ok {
			if u, ok := enclosure["url"].(string); ok {
				size := any(nil)
				if n, ok := enclosure["length"].(float64); ok {
					size = int64(n)
				}
				typ, _ := enclosure["type"].(string)
				enc := ep.Enclosure
				if enc == nil || enc.URL != u {
					fields["enclosureURL"] = u
				}
				if enc == nil || enc.Length == nil || size == nil || *enc.Length != strconv.FormatInt(size.(int64), 10) {
					fields["enclosureSize"] = size
				}
				if enc == nil || enc.Type == nil || *enc.Type != typ {
					fields["enclosureType"] = store.NullIfEmpty(typ)
				}
			}
		}
	}
	if raw, exists := body["chapters"]; exists {
		if list, ok := raw.([]any); ok {
			valid := true
			for _, value := range list {
				chapter, yes := value.(map[string]any)
				if !yes || chapter["title"] == nil || chapter["start"] == nil || chapter["start"] == float64(0) {
					valid = false
					break
				}
			}
			if valid {
				encoded, _ := json.Marshal(list)
				old, _ := json.Marshal(ep.Chapters)
				if string(encoded) != string(old) {
					fields["chapters"] = string(encoded)
				}
			}
		}
	}
	if n, ok := body["publishedAt"].(float64); ok {
		millis := int64(n)
		if ep.PublishedAt == nil || *ep.PublishedAt != millis {
			fields["publishedAt"] = time.UnixMilli(millis).UTC().Format(time.RFC3339Nano)
		}
	}
	if len(fields) > 0 {
		keys := make([]string, 0, len(fields))
		for k := range fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		set := make([]string, 0, len(keys))
		args := make([]any, 0, len(keys)+2)
		for _, k := range keys {
			set = append(set, k+"=?")
			args = append(args, fields[k])
		}
		args = append(args, dbtime.Now(), episodeID)
		set = append(set, "updatedAt=?")
		if _, err := h.db.ExecContext(r.Context(), `UPDATE podcastEpisodes SET `+strings.Join(set, ",")+` WHERE id=?`, args...); err != nil {
			return httputil.Internal("database write failed", err)
		}
		updated, err := h.loadItem(r.Context(), item.ID)
		if err == nil {
			h.emit.ToAdmins("item_updated", h.expanded(r.Context(), updated))
			item = updated
		}
	}
	return httputil.JSON(w, r, h.expanded(r.Context(), item))
}

func (h *Handler) removeEpisode(w http.ResponseWriter, r *http.Request, item items.Item) error {
	episodeID := r.PathValue("episodeId")
	ep, ok := episodeOf(item, episodeID)
	if !ok {
		return httputil.Status(404, "")
	}
	if r.URL.Query().Get("hard") == "1" {
		if path := ep.AudioFile.Path(); path != "" {
			_ = os.Remove(path)
		}
	}
	if err := h.deleteEpisode(r.Context(), item, episodeID, ep.AudioFile.Ino()); err != nil {
		return httputil.Internal("database write failed", err)
	}
	updated, err := h.loadItem(r.Context(), item.ID)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	h.emit.ToAdmins("item_updated", h.expanded(r.Context(), updated))
	return httputil.JSON(w, r, updated.Full())
}

func (h *Handler) deleteEpisode(ctx context.Context, item items.Item, episodeID, ino string) error {
	files := []mediafiles.File{}
	for _, f := range h.rawLibraryFiles(ctx, item.ID) {
		if f.Ino() != ino {
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
	playlistChanges, err := store.DeleteEpisodes(ctx, tx, []string{episodeID})
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE libraryItems SET libraryFiles=?,updatedAt=? WHERE id=?`, string(encoded), now, item.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE podcasts SET numEpisodes=(SELECT COUNT(*) FROM podcastEpisodes WHERE podcastId=?),updatedAt=? WHERE id=?`, item.MediaID, now, item.MediaID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	playlistview.Announce(ctx, h.db, h.emit, playlistChanges)
	return nil
}

// rawLibraryFiles reads the stored libraryFiles without the derived fileType.
func (h *Handler) rawLibraryFiles(ctx context.Context, itemID string) []mediafiles.File {
	var raw []byte
	if err := h.db.QueryRowContext(ctx, `SELECT libraryFiles FROM libraryItems WHERE id=?`, itemID).Scan(&raw); err != nil {
		logging.Errorf("[PodcastManager] read libraryFiles of %s: %v", itemID, err)
	}
	return store.DecodeJSONSlice[mediafiles.File](raw)
}
