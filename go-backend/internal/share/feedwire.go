package share

import "audiobookshelf-compat/internal/db"

// feedView is Feed.toOldJSON (GET /api/feeds); feedMinified is
// Feed.toOldJSONMinified (open responses, rss_feed_* events).
type feedView struct {
	ID              string        `json:"id"`
	Slug            *string       `json:"slug"`
	UserID          *string       `json:"userId"`
	EntityType      *string       `json:"entityType"`
	EntityID        *string       `json:"entityId"`
	EntityUpdatedAt *int64        `json:"entityUpdatedAt"`
	CoverPath       *string       `json:"coverPath"`
	Meta            feedMeta      `json:"meta"`
	ServerAddress   *string       `json:"serverAddress"`
	FeedURL         *string       `json:"feedUrl"`
	Episodes        []episodeView `json:"episodes"`
	CreatedAt       db.Time       `json:"createdAt"`
	UpdatedAt       db.Time       `json:"updatedAt"`
}

type feedMinified struct {
	ID         string  `json:"id"`
	EntityType *string `json:"entityType"`
	EntityID   *string `json:"entityId"`
	FeedURL    *string `json:"feedUrl"`
	Meta       struct {
		Title           *string `json:"title"`
		Description     *string `json:"description"`
		PreventIndexing bool    `json:"preventIndexing"`
		OwnerName       *string `json:"ownerName"`
		OwnerEmail      *string `json:"ownerEmail"`
	} `json:"meta"`
}

type feedMeta struct {
	Title           *string `json:"title"`
	Description     *string `json:"description"`
	Author          *string `json:"author"`
	ImageURL        *string `json:"imageUrl"`
	FeedURL         *string `json:"feedUrl"`
	Link            *string `json:"link"`
	Explicit        bool    `json:"explicit"`
	Type            *string `json:"type"`
	Language        *string `json:"language"`
	PreventIndexing bool    `json:"preventIndexing"`
	OwnerName       *string `json:"ownerName"`
	OwnerEmail      *string `json:"ownerEmail"`
}

// episodeView is FeedEpisode.getOldEpisode.
type episodeView struct {
	ID          string  `json:"id"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Enclosure   struct {
		URL  *string `json:"url"`
		Size *int64  `json:"size"`
		Type *string `json:"type"`
	} `json:"enclosure"`
	PubDate     *string  `json:"pubDate"`
	Link        *string  `json:"link"`
	Author      *string  `json:"author"`
	Explicit    bool     `json:"explicit"`
	Duration    *float64 `json:"duration"`
	Season      *string  `json:"season"`
	Episode     *string  `json:"episode"`
	EpisodeType *string  `json:"episodeType"`
	FullPath    *string  `json:"fullPath"`
}

func metaOf(f db.Feed) feedMeta {
	return feedMeta{
		Title: f.Title, Description: f.Description, Author: f.Author, ImageURL: f.ImageURL, FeedURL: f.FeedURL, Link: f.SiteURL,
		Explicit: bool(f.Explicit), Type: f.PodcastType, Language: f.Language, PreventIndexing: bool(f.PreventIndexing),
		OwnerName: f.OwnerName, OwnerEmail: f.OwnerEmail,
	}
}

func minified(f db.Feed) feedMinified {
	m := feedMinified{ID: f.ID, EntityType: f.EntityType, EntityID: f.EntityID, FeedURL: f.FeedURL}
	m.Meta.Title, m.Meta.Description, m.Meta.PreventIndexing, m.Meta.OwnerName, m.Meta.OwnerEmail = f.Title, f.Description, bool(f.PreventIndexing), f.OwnerName, f.OwnerEmail
	return m
}

func feedViewOf(f db.Feed, episodes []db.FeedEpisode) feedView {
	v := feedView{
		ID: f.ID, Slug: f.Slug, UserID: f.UserID, EntityType: f.EntityType, EntityID: f.EntityID, ServerAddress: f.ServerAddress, FeedURL: f.FeedURL,
		Meta: metaOf(f), Episodes: make([]episodeView, len(episodes)), CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt,
	}
	if !f.EntityUpdatedAt.IsZero() && f.EntityUpdatedAt.Millis() != 0 { // a stored 0 is "never"
		v.EntityUpdatedAt = db.Ptr(f.EntityUpdatedAt.Millis())
	}
	if f.CoverPath != nil && *f.CoverPath != "" {
		v.CoverPath = f.CoverPath
	}
	for i, e := range episodes {
		v.Episodes[i] = episodeView{ID: e.ID, Title: e.Title, Description: e.Description, PubDate: e.PubDate, Link: e.SiteURL, Author: e.Author,
			Explicit: bool(e.Explicit), Duration: e.Duration, Season: e.Season, Episode: e.Episode, EpisodeType: e.EpisodeType, FullPath: e.FilePath}
		v.Episodes[i].Enclosure.URL, v.Episodes[i].Enclosure.Size, v.Episodes[i].Enclosure.Type = e.EnclosureURL, e.EnclosureSize, e.EnclosureType
	}
	return v
}
