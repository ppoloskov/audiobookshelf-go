package browse

import (
	"context"
	"slices"
	"time"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/item"
)

// feedMinified is Feed.toOldJSONMinified: the feed of an item or series. Every
// feeds column is nullable, so every key is a pointer.
type feedMinified struct {
	ID         string  `json:"id"`
	EntityType *string `json:"entityType"`
	EntityID   *string `json:"entityId"`
	FeedURL    *string `json:"feedUrl"`
	Meta       struct {
		Title           *string `json:"title"`
		Description     *string `json:"description"`
		PreventIndexing *bool   `json:"preventIndexing"`
		OwnerName       *string `json:"ownerName"`
		OwnerEmail      *string `json:"ownerEmail"`
	} `json:"meta"`
}

// feedRow is a feedMinified as selected: the flat columns.
type feedRow struct {
	ID              string  `db:"id"`
	EntityType      *string `db:"entityType"`
	EntityID        *string `db:"entityId"`
	FeedURL         *string `db:"feedURL"`
	Title           *string `db:"title"`
	Description     *string `db:"description"`
	PreventIndexing *bool   `db:"preventIndexing"`
	OwnerName       *string `db:"ownerName"`
	OwnerEmail      *string `db:"ownerEmail"`
}

// feedsOf returns the open feed of each entity id, keyed by entity id. An empty
// entityType matches any type (RssFeedManager.findFeedForEntityId filters on
// entityId alone); of several feeds the oldest wins.
func (h *Handler) feedsOf(ctx context.Context, entityType string, entityIDs []string) (map[string]*feedMinified, error) {
	if len(entityIDs) == 0 {
		return nil, nil
	}
	var rows []feedRow
	err := h.db.Select(ctx, &rows, `SELECT id, entityType, entityId, feedURL, title, description, preventIndexing, ownerName, ownerEmail
		FROM feeds WHERE entityId IN (?) AND (? = '' OR entityType = ?) ORDER BY rowid`, entityIDs, entityType, entityType)
	if err != nil {
		return nil, err
	}
	feeds := make(map[string]*feedMinified, len(rows))
	for _, row := range rows {
		id := db.Val(row.EntityID)
		if feeds[id] != nil {
			continue
		}
		feed := &feedMinified{ID: row.ID, EntityType: row.EntityType, EntityID: row.EntityID, FeedURL: row.FeedURL}
		feed.Meta.Title, feed.Meta.Description, feed.Meta.PreventIndexing = row.Title, row.Description, row.PreventIndexing
		feed.Meta.OwnerName, feed.Meta.OwnerEmail = row.OwnerName, row.OwnerEmail
		feeds[id] = feed
	}
	return feeds, nil
}

// shareView is libraryItem.mediaItemShare, the open share of a book
// (ShareManager.findByMediaItemId): its dates are ISO strings.
type shareView struct {
	ID             string  `json:"id"`
	MediaItemID    string  `json:"mediaItemId"`
	MediaItemType  *string `json:"mediaItemType"`
	Slug           *string `json:"slug"`
	ExpiresAt      *string `json:"expiresAt"`
	IsDownloadable bool    `json:"isDownloadable"`
	CreatedAt      string  `json:"createdAt"`
	UpdatedAt      string  `json:"updatedAt"`
}

func isoTime(t db.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// sharesOf returns the open (unexpired) share of each media item id, keyed by
// media id. Of several shares the oldest is the only candidate, and if it has
// expired the item has none.
func (h *Handler) sharesOf(ctx context.Context, mediaIDs []string) (map[string]*shareView, error) {
	if len(mediaIDs) == 0 {
		return nil, nil
	}
	var rows []db.MediaItemShare
	if err := h.db.Select(ctx, &rows, `SELECT * FROM mediaItemShares WHERE mediaItemId IN (?) ORDER BY rowid`, mediaIDs); err != nil {
		return nil, err
	}
	shares := make(map[string]*shareView, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		mediaID := db.Val(row.MediaItemID)
		if seen[mediaID] {
			continue
		}
		seen[mediaID] = true
		if !row.ExpiresAt.IsZero() && row.ExpiresAt.Millis() <= time.Now().UnixMilli() {
			continue
		}
		view := &shareView{ID: row.ID, MediaItemID: mediaID, MediaItemType: row.MediaItemType, Slug: row.Slug,
			IsDownloadable: bool(row.IsDownloadable), CreatedAt: isoTime(row.CreatedAt), UpdatedAt: isoTime(row.UpdatedAt)}
		if !row.ExpiresAt.IsZero() {
			view.ExpiresAt = db.Ptr(isoTime(row.ExpiresAt))
		}
		shares[mediaID] = view
	}
	return shares, nil
}

// itemExtras says which include= entities to attach to rows.
type itemExtras struct {
	rssFeed, share, numEpisodesIncomplete bool
}

// extrasOf reads include=. mediaItemShare is for admins only.
func extrasOf(include []string, admin bool) itemExtras {
	return itemExtras{rssFeed: slices.Contains(include, "rssfeed"), share: admin && slices.Contains(include, "share"),
		numEpisodesIncomplete: slices.Contains(include, "numepisodesincomplete")}
}

// attach sets rssFeed, mediaItemShare (books only) and numEpisodesIncomplete
// (podcasts) on rows, which render items one for one, with one query for each.
func (h *Handler) attach(ctx context.Context, userID string, want itemExtras, items []item.Item, rows []item.Minified) error {
	var itemIDs, bookIDs, podcastIDs []string
	for _, it := range items {
		itemIDs = append(itemIDs, it.ID)
		if it.Book != nil {
			bookIDs = append(bookIDs, it.Book.ID)
		} else {
			podcastIDs = append(podcastIDs, it.Podcast.ID)
		}
	}
	var feeds map[string]*feedMinified
	var shares map[string]*shareView
	var err error
	if want.rssFeed {
		if feeds, err = h.feedsOf(ctx, "libraryItem", itemIDs); err != nil {
			return err
		}
	}
	if want.share {
		if shares, err = h.sharesOf(ctx, bookIDs); err != nil {
			return err
		}
	}
	var finished []struct {
		PodcastID string `db:"podcastId"`
		N         int64  `db:"n"`
	}
	if want.numEpisodesIncomplete && len(podcastIDs) > 0 {
		err = h.db.Select(ctx, &finished, `SELECT podcastId, COUNT(*) AS n FROM mediaProgresses WHERE userId = ? AND podcastId IN (?) AND isFinished = 1 GROUP BY podcastId`, userID, podcastIDs)
		if err != nil {
			return err
		}
	}
	for i, it := range items {
		row := &rows[i]
		if feed := feeds[it.ID]; feed != nil {
			row.RSSFeed = feed
		}
		if it.Book != nil {
			if share := shares[it.Book.ID]; share != nil {
				row.MediaItemShare = share
			}
		} else if want.numEpisodesIncomplete {
			// numEpisodes minus the user's finished episodes; absent when 0.
			row.NumEpisodesIncomplete = db.Val(it.Podcast.NumEpisodes)
			for _, f := range finished {
				if f.PodcastID == it.Podcast.ID {
					row.NumEpisodesIncomplete -= f.N
				}
			}
		}
	}
	return nil
}

// minify renders items in the minified shape.
func minify(items []item.Item, prefixes []string) []item.Minified {
	rows := make([]item.Minified, len(items))
	for i, it := range items {
		rows[i] = it.Minified(prefixes)
	}
	return rows
}
