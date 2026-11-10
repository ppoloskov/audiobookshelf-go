// Package feeds is the client JSON of an RSS feed (Feed.toOldJSON). The feeds
// list, the collection pages and anything else that shows a feed build it
// here, so the shape changes in one place.
package feeds

import (
	"context"
	"database/sql"
	"errors"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/store"
)

// Minified is Feed.toOldJSONMinified (server/models/Feed.js:677): the feed of
// an item or series, of POST /api/feeds/*/open, of the rss_feed_open and
// rss_feed_closed events and of the "minified" list of GET /api/feeds. Every
// column is nullable in the feeds table, so every key is a pointer and a NULL
// encodes as null, as Sequelize returns it.
type Minified struct {
	ID         string       `json:"id"`
	EntityType *string      `json:"entityType"`
	EntityID   *string      `json:"entityId"`
	FeedURL    *string      `json:"feedUrl"`
	Meta       MinifiedMeta `json:"meta"`
}

// MinifiedMeta is the meta object of Minified.
type MinifiedMeta struct {
	Title           *string `json:"title"`
	Description     *string `json:"description"`
	PreventIndexing *bool   `json:"preventIndexing"`
	OwnerName       *string `json:"ownerName"`
	OwnerEmail      *string `json:"ownerEmail"`
}

// LoadMinified reads the feed attached to entityType/entityID. An empty
// entityType matches RssFeedManager.findFeedForEntityId
// (server/managers/RssFeedManager.js:59), which filters on entityId only.
func LoadMinified(ctx context.Context, db *sql.DB, entityType, entityID string) (Minified, bool, error) {
	feed, err := sqlc.New(db).GetFeedSummaryByEntity(ctx, sqlc.GetFeedSummaryByEntityParams{EntityType: entityType, EntityID: entityID})
	if errors.Is(err, sql.ErrNoRows) {
		return Minified{}, false, nil
	}
	if err != nil {
		return Minified{}, false, err
	}
	return FromSummary(feed), true, nil
}

// FromSummary is the minified feed of a GetFeedSummaryByEntity row.
func FromSummary(f sqlc.GetFeedSummaryByEntityRow) Minified {
	return Minified{
		ID: f.ID, EntityType: store.NullStringPtr(f.Entitytype), EntityID: store.NullStringPtr(f.Entityid), FeedURL: store.NullStringPtr(f.Feedurl),
		Meta: MinifiedMeta{Title: store.NullStringPtr(f.Title), Description: store.NullStringPtr(f.Description), PreventIndexing: flag(f.Preventindexing), OwnerName: store.NullStringPtr(f.Ownername), OwnerEmail: store.NullStringPtr(f.Owneremail)},
	}
}

// Feed is Feed.toOldJSON (server/models/Feed.js:645). EntityUpdatedAt,
// CreatedAt and UpdatedAt are epoch millis (valueOf).
type Feed struct {
	ID              string    `json:"id"`
	Slug            *string   `json:"slug"`
	UserID          *string   `json:"userId"`
	EntityType      *string   `json:"entityType"`
	EntityID        *string   `json:"entityId"`
	EntityUpdatedAt *int64    `json:"entityUpdatedAt"`
	CoverPath       *string   `json:"coverPath"`
	Meta            Meta      `json:"meta"`
	ServerAddress   *string   `json:"serverAddress"`
	FeedURL         *string   `json:"feedUrl"`
	Episodes        []Episode `json:"episodes"`
	CreatedAt       int64     `json:"createdAt"`
	UpdatedAt       int64     `json:"updatedAt"`
}

// Meta is the meta object of Feed.
type Meta struct {
	Title           *string `json:"title"`
	Description     *string `json:"description"`
	Author          *string `json:"author"`
	ImageURL        *string `json:"imageUrl"`
	FeedURL         *string `json:"feedUrl"`
	Link            *string `json:"link"`
	Explicit        *bool   `json:"explicit"`
	Type            *string `json:"type"`
	Language        *string `json:"language"`
	PreventIndexing *bool   `json:"preventIndexing"`
	OwnerName       *string `json:"ownerName"`
	OwnerEmail      *string `json:"ownerEmail"`
}

// Episode is FeedEpisode.getOldEpisode (server/models/FeedEpisode.js:287).
type Episode struct {
	ID          string    `json:"id"`
	Title       *string   `json:"title"`
	Description *string   `json:"description"`
	Enclosure   Enclosure `json:"enclosure"`
	PubDate     *string   `json:"pubDate"`
	Link        *string   `json:"link"`
	Author      *string   `json:"author"`
	Explicit    *bool     `json:"explicit"`
	Duration    *float64  `json:"duration"`
	Season      *string   `json:"season"`
	Episode     *string   `json:"episode"`
	EpisodeType *string   `json:"episodeType"`
	FullPath    *string   `json:"fullPath"`
}

// Enclosure is the enclosure object of Episode.
type Enclosure struct {
	URL  *string `json:"url"`
	Size *int64  `json:"size"`
	Type *string `json:"type"`
}

func flag(v sql.NullInt64) *bool {
	if !v.Valid {
		return nil
	}
	b := v.Int64 != 0
	return &b
}

// Old maps a feeds row and its episodes, in the order given, to Feed.toOldJSON:
// entityUpdatedAt and coverPath are null when zero or empty (Feed.js:652-653).
func Old(f sqlc.Feed, eps []sqlc.Feedepisode) Feed {
	out := Feed{
		ID: f.ID, Slug: store.NullStringPtr(f.Slug), UserID: store.NullStringPtr(f.Userid), EntityType: store.NullStringPtr(f.Entitytype), EntityID: store.NullStringPtr(f.Entityid),
		Meta: Meta{
			Title: store.NullStringPtr(f.Title), Description: store.NullStringPtr(f.Description), Author: store.NullStringPtr(f.Author), ImageURL: store.NullStringPtr(f.Imageurl),
			FeedURL: store.NullStringPtr(f.Feedurl), Link: store.NullStringPtr(f.Siteurl), Explicit: flag(f.Explicit), Type: store.NullStringPtr(f.Podcasttype),
			Language: store.NullStringPtr(f.Language), PreventIndexing: flag(f.Preventindexing), OwnerName: store.NullStringPtr(f.Ownername), OwnerEmail: store.NullStringPtr(f.Owneremail),
		},
		ServerAddress: store.NullStringPtr(f.Serveraddress), FeedURL: store.NullStringPtr(f.Feedurl), Episodes: make([]Episode, 0, len(eps)),
		CreatedAt: dbtime.Time(f.Createdat).UnixMilli(), UpdatedAt: dbtime.Time(f.Updatedat).UnixMilli(),
	}
	if t := dbtime.Time(f.Entityupdatedat); !t.IsZero() && t.UnixMilli() != 0 {
		ms := t.UnixMilli()
		out.EntityUpdatedAt = &ms
	}
	if f.Coverpath.Valid && f.Coverpath.String != "" {
		out.CoverPath = &f.Coverpath.String
	}
	for _, e := range eps {
		var size *int64
		if e.Enclosuresize.Valid {
			size = &e.Enclosuresize.Int64
		}
		var duration *float64
		if e.Duration.Valid {
			duration = &e.Duration.Float64
		}
		out.Episodes = append(out.Episodes, Episode{
			ID: e.ID, Title: store.NullStringPtr(e.Title), Description: store.NullStringPtr(e.Description),
			Enclosure: Enclosure{URL: store.NullStringPtr(e.Enclosureurl), Size: size, Type: store.NullStringPtr(e.Enclosuretype)},
			PubDate:   store.NullStringPtr(e.Pubdate), Link: store.NullStringPtr(e.Siteurl), Author: store.NullStringPtr(e.Author), Explicit: flag(e.Explicit),
			Duration: duration, Season: store.NullStringPtr(e.Season), Episode: store.NullStringPtr(e.Episode), EpisodeType: store.NullStringPtr(e.Episodetype), FullPath: store.NullStringPtr(e.Filepath),
		})
	}
	return out
}

// OfEntity is the feed of a library item, collection or series without its
// episodes, or nil when the entity has none. Collections load their feed
// without feedEpisodes (Collection.js:44-47), so their JSON carries [].
func OfEntity(ctx context.Context, db sqlc.DBTX, entityType, entityID string) (*Feed, error) {
	row, err := sqlc.New(db).GetFeedByEntity(ctx, sqlc.GetFeedByEntityParams{EntityType: entityType, EntityID: entityID})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	feed := Old(row, nil)
	return &feed, nil
}
