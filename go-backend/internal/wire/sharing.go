package wire

// MediaItemShare is the share object sent by POST /api/share/mediaitem, the
// share_open and share_closed Socket.IO events, and (before its
// playbackSession key) GET /public/share/:slug: MediaItemShare.toJSONForClient
// (server/models/MediaItemShare.js:65) and ShareManager.findBySlug
// (server/managers/ShareManager.js:66), which drop pash, userId and extraData.
//
// Sequelize DATE attributes are Date objects, which JSON.stringify writes as
// ISO strings with milliseconds ("2026-01-02T03:04:05.678Z"), not epoch
// millis. ExpiresAt is null for a permanent share. IsDownloadable is omitted
// when the create request did not carry it (undefined in JS).
type MediaItemShare struct {
	ID             string  `json:"id"`
	MediaItemID    string  `json:"mediaItemId"`
	MediaItemType  string  `json:"mediaItemType"`
	Slug           string  `json:"slug"`
	ExpiresAt      *string `json:"expiresAt"`
	CreatedAt      string  `json:"createdAt"`
	UpdatedAt      string  `json:"updatedAt"`
	IsDownloadable *bool   `json:"isDownloadable,omitempty"`
}

// FeedMinified is Feed.toOldJSONMinified (server/models/Feed.js:677): the
// feed object of POST /api/feeds/*/open, the rss_feed_open and rss_feed_closed
// events, and the "minified" list of GET /api/feeds.
type FeedMinified struct {
	ID         string           `json:"id"`
	EntityType string           `json:"entityType"`
	EntityID   string           `json:"entityId"`
	FeedURL    *string          `json:"feedUrl"`
	Meta       FeedMinifiedMeta `json:"meta"`
}

// FeedMinifiedMeta is the meta object of FeedMinified. Every column is
// nullable in the feeds table, so every key is *T without omitempty.
type FeedMinifiedMeta struct {
	Title           *string `json:"title"`
	Description     *string `json:"description"`
	PreventIndexing *bool   `json:"preventIndexing"`
	OwnerName       *string `json:"ownerName"`
	OwnerEmail      *string `json:"ownerEmail"`
}

// Feed is Feed.toOldJSON (server/models/Feed.js:645), an element of "feeds"
// in GET /api/feeds. EntityUpdatedAt, CreatedAt and UpdatedAt are epoch
// millis here (valueOf), unlike the share dates above.
type Feed struct {
	ID              string        `json:"id"`
	Slug            *string       `json:"slug"`
	UserID          *string       `json:"userId"`
	EntityType      *string       `json:"entityType"`
	EntityID        *string       `json:"entityId"`
	EntityUpdatedAt *int64        `json:"entityUpdatedAt"`
	CoverPath       *string       `json:"coverPath"`
	Meta            FeedMeta      `json:"meta"`
	ServerAddress   *string       `json:"serverAddress"`
	FeedURL         *string       `json:"feedUrl"`
	Episodes        []FeedEpisode `json:"episodes"`
	CreatedAt       int64         `json:"createdAt"`
	UpdatedAt       int64         `json:"updatedAt"`
}

// FeedMeta is the meta object of Feed.
type FeedMeta struct {
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

// FeedEpisode is FeedEpisode.getOldEpisode (server/models/FeedEpisode.js:287).
type FeedEpisode struct {
	ID          string               `json:"id"`
	Title       *string              `json:"title"`
	Description *string              `json:"description"`
	Enclosure   FeedEpisodeEnclosure `json:"enclosure"`
	PubDate     *string              `json:"pubDate"`
	Link        *string              `json:"link"`
	Author      *string              `json:"author"`
	Explicit    *bool                `json:"explicit"`
	Duration    *float64             `json:"duration"`
	Season      *string              `json:"season"`
	Episode     *string              `json:"episode"`
	EpisodeType *string              `json:"episodeType"`
	FullPath    *string              `json:"fullPath"`
}

// FeedEpisodeEnclosure is the enclosure object of FeedEpisode.
type FeedEpisodeEnclosure struct {
	URL  *string `json:"url"`
	Size *int64  `json:"size"`
	Type *string `json:"type"`
}

// FeedList is the GET /api/feeds response (server/controllers/RSSFeedController.js:21).
type FeedList struct {
	Feeds    []Feed         `json:"feeds"`
	Minified []FeedMinified `json:"minified"`
}

// FeedOpened is the POST /api/feeds/{item,collection,series}/:id/open response.
type FeedOpened struct {
	Feed FeedMinified `json:"feed"`
}
