package sharing

import "audiobookshelf-compat/internal/feeds"

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

// FeedList is the GET /api/feeds response (server/controllers/RSSFeedController.js:21).
type FeedList struct {
	Feeds    []feeds.Feed     `json:"feeds"`
	Minified []feeds.Minified `json:"minified"`
}

// FeedOpened is the POST /api/feeds/{item,collection,series}/:id/open response.
type FeedOpened struct {
	Feed feeds.Minified `json:"feed"`
}
