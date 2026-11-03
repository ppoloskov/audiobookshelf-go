package userapi

import (
	"database/sql"
	"net/http"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/store"
)

func (h *Handler) collectionRSSFeed(r *http.Request, collectionID string) (map[string]any, error) {
	feed, err := h.queries().GetFeedByEntity(r.Context(), sqlc.GetFeedByEntityParams{EntityType: "collection", EntityID: collectionID})
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	eps, err := h.queries().ListFeedEpisodesNewestFirst(r.Context(), feed.ID)
	if err != nil {
		return nil, err
	}
	episodes := make([]any, 0, len(eps))
	for _, e := range eps {
		episodes = append(episodes, map[string]any{"id": e.ID, "title": store.NullString(e.Title), "description": store.NullString(e.Description), "enclosure": map[string]any{"url": store.NullString(e.Enclosureurl), "size": store.NullInt64(e.Enclosuresize), "type": store.NullString(e.Enclosuretype)}, "pubDate": store.NullString(e.Pubdate), "link": store.NullString(e.Siteurl), "author": store.NullString(e.Author), "explicit": e.Explicit.Int64 != 0, "duration": nullFloat(e.Duration), "season": store.NullString(e.Season), "episode": store.NullString(e.Episode), "episodeType": store.NullString(e.Episodetype), "fullPath": store.NullString(e.Filepath)})
	}
	return map[string]any{"id": feed.ID, "slug": feed.Slug.String, "userId": feed.Userid.String, "entityType": feed.Entitytype.String, "entityId": feed.Entityid.String, "entityUpdatedAt": dbtime.Millis(feed.Entityupdatedat), "coverPath": store.NullString(feed.Coverpath), "meta": map[string]any{"title": feed.Title.String, "description": feed.Description.String, "author": feed.Author.String, "imageUrl": store.NullString(feed.Imageurl), "feedUrl": store.NullString(feed.Feedurl), "link": store.NullString(feed.Siteurl), "explicit": feed.Explicit.Int64 != 0, "type": feed.Podcasttype.String, "language": feed.Language.String, "preventIndexing": feed.Preventindexing.Int64 != 0, "ownerName": feed.Ownername.String, "ownerEmail": feed.Owneremail.String}, "serverAddress": feed.Serveraddress.String, "feedUrl": feed.Feedurl.String, "episodes": episodes, "createdAt": dbtime.Millis(feed.Createdat), "updatedAt": dbtime.Millis(feed.Updatedat)}, nil
}

// nullFloat is a nullable FLOAT column as a JSON value: NULL becomes nil.
func nullFloat(v sql.NullFloat64) any {
	if !v.Valid {
		return nil
	}
	return v.Float64
}
