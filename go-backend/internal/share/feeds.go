package share

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/ids"
)

// listFeeds is RSSFeedController.getAll. Admin only.
func (h *Handler) listFeeds(w http.ResponseWriter, r *http.Request) error {
	var feeds []db.Feed
	var episodes []db.FeedEpisode
	if err := h.db.Tx(r.Context(), func(ctx context.Context) error {
		if err := h.db.Select(ctx, &feeds, `SELECT * FROM feeds ORDER BY rowid`); err != nil {
			return err
		}
		return h.db.Select(ctx, &episodes, `SELECT * FROM feedEpisodes ORDER BY rowid`)
	}); err != nil {
		return fmt.Errorf("list feeds: %w", err)
	}
	byFeed := map[string][]db.FeedEpisode{}
	for _, e := range episodes {
		byFeed[db.Val(e.FeedID)] = append(byFeed[db.Val(e.FeedID)], e)
	}
	out := struct {
		Feeds    []feedView     `json:"feeds"`
		Minified []feedMinified `json:"minified"`
	}{make([]feedView, len(feeds)), make([]feedMinified, len(feeds))}
	for i, feed := range feeds {
		out.Feeds[i], out.Minified[i] = feedViewOf(feed, byFeed[feed.ID]), minified(feed)
	}
	return httpx.JSON(w, r, out)
}

// lenientString is a string; any other JSON value reads as empty (upstream
// takes only strings, and a client may send null or numbers).
type lenientString string

func (s *lenientString) UnmarshalJSON(data []byte) error {
	var v string
	if json.Unmarshal(data, &v) == nil {
		*s = lenientString(v)
	}
	return nil
}

// openRequest is the body of the open routes. PreventIndexing is true unless
// the request says false; empty owner fields are stored as NULL.
type openRequest struct {
	ServerAddress   string `json:"serverAddress"`
	Slug            string `json:"slug"`
	MetadataDetails struct {
		PreventIndexing json.RawMessage `json:"preventIndexing"`
		OwnerName       lenientString   `json:"ownerName"`
		OwnerEmail      lenientString   `json:"ownerEmail"`
	} `json:"metadataDetails"`
}

// openFeed is RSSFeedController.openRSSFeedFor{Item,Collection,Series}. Admin
// only. Checks run in order: the entity, an item's library permission, the
// body, the tracks, the slug.
func (h *Handler) openFeed(w http.ResponseWriter, r *http.Request, kind entityKind) error {
	user, id := auth.User(r.Context()), r.PathValue("id")
	req, decodeErr := httpx.Decode[openRequest](w, r)
	var feed db.Feed
	err := h.db.Tx(r.Context(), func(ctx context.Context) error {
		src, err := h.loadSource(ctx, kind, id)
		if errors.Is(err, errNoEntity) {
			return httpx.NotFound()
		}
		if err != nil {
			return err
		}
		switch {
		case kind == itemFeed && !user.CanAccessLibrary(src.libraryID):
			return httpx.Forbidden()
		case decodeErr != nil:
			return decodeErr
		case req.ServerAddress == "" || req.Slug == "":
			return httpx.BadRequest("Invalid request body")
		case len(src.episodes) == 0:
			return httpx.BadRequest("No audio tracks")
		}
		var taken int
		if err := h.db.Get(ctx, &taken, `SELECT COUNT(*) FROM feeds WHERE slug = ?`, req.Slug); err != nil {
			return fmt.Errorf("count feeds by slug: %w", err)
		}
		if taken > 0 {
			return httpx.BadRequest("Slug already in use")
		}
		feed, err = h.createFeed(ctx, user.ID, kind, id, src, req)
		return err
	})
	if err != nil {
		return err
	}
	h.emit.ToAll("rss_feed_open", minified(feed))
	return httpx.JSON(w, r, struct {
		Feed feedMinified `json:"feed"`
	}{minified(feed)})
}

// closeFeed is RSSFeedController.closeRSSFeed. Admin only.
func (h *Handler) closeFeed(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	var feed db.Feed
	err := h.db.Get(r.Context(), &feed, `SELECT * FROM feeds WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.NotFound()
	}
	if err != nil {
		return fmt.Errorf("load feed: %w", err)
	}
	if _, err := h.db.Exec(r.Context(), `DELETE FROM feeds WHERE id = ?`, id); err != nil { // feedEpisodes cascade
		return fmt.Errorf("delete feed: %w", err)
	}
	h.emit.ToAll("rss_feed_closed", minified(feed))
	return httpx.Status(w, http.StatusOK)
}

// publicFeed serves /feed/{slug}: the RSS document, and under it the episode
// media (/item/{episodeId}/...) and the cover (/cover.*).
func (h *Handler) publicFeed(w http.ResponseWriter, r *http.Request) error {
	slug := r.PathValue("slug")
	tail := r.PathValue("tail")
	first, rest, _ := strings.Cut(tail, "/")
	switch {
	case tail == "":
		return h.feedXML(w, r, slug)
	case first == "item" && rest != "":
		episodeID, _, _ := strings.Cut(rest, "/")
		return h.feedMedia(w, r, slug, episodeID)
	case strings.HasPrefix(first, "cover"):
		return h.feedCover(w, r, slug)
	}
	return httpx.NotFound()
}

// feedXML answers the RSS document of the feed, first refreshing the feed from
// its library item, collection or series when that has changed.
func (h *Handler) feedXML(w http.ResponseWriter, r *http.Request, slug string) error {
	var feed db.Feed
	var episodes []db.FeedEpisode
	err := h.db.Tx(r.Context(), func(ctx context.Context) error {
		if err := h.db.Get(ctx, &feed, `SELECT * FROM feeds WHERE slug = ?`, slug); err != nil {
			return err
		}
		if err := h.db.Select(ctx, &episodes, `SELECT * FROM feedEpisodes WHERE feedId = ? ORDER BY rowid`, feed.ID); err != nil {
			return err
		}
		var err error
		episodes, err = h.refreshFeed(ctx, &feed, episodes)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.NotFound()
	}
	if err != nil {
		return fmt.Errorf("load feed %q: %w", slug, err)
	}
	doc, err := marshalRSS(feed, episodes, hostPrefix(r, h.base), time.Now())
	if err != nil {
		return fmt.Errorf("render feed %q: %w", slug, err)
	}
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	_, _ = w.Write(doc) // a client that went away is not a server error
	return nil
}

// feedPath is the file path (selected AS path) of query, a 404 when there is none.
func (h *Handler) feedPath(ctx context.Context, query string, args ...any) (string, error) {
	var row struct {
		Path *string `db:"path"`
	}
	err := h.db.Get(ctx, &row, query, args...)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && db.Val(row.Path) == "") {
		return "", httpx.NotFound()
	}
	return db.Val(row.Path), err
}

// feedMedia serves the audio file of an episode of the feed.
func (h *Handler) feedMedia(w http.ResponseWriter, r *http.Request, slug, episodeID string) error {
	path, err := h.feedPath(r.Context(), `SELECT fe.filePath AS path FROM feedEpisodes fe JOIN feeds f ON f.id = fe.feedId WHERE f.slug = ? AND fe.id = ?`, slug, episodeID)
	if err != nil {
		return err
	}
	return serveMedia(w, r, path, "")
}

// feedCover serves the cover of the feed as image/<extension>.
func (h *Handler) feedCover(w http.ResponseWriter, r *http.Request, slug string) error {
	path, err := h.feedPath(r.Context(), `SELECT coverPath AS path FROM feeds WHERE slug = ?`, slug)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "image/"+strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")))
	return httpx.ServeFile(w, r, path)
}

// applySource copies the entity-derived columns of src into the feed row
// (Feed.getFeedObjFor*).
func applySource(f *db.Feed, src *source) {
	f.EntityUpdatedAt = src.updatedAt
	f.SiteURL, f.Title, f.Description, f.Author = &src.siteURL, &src.title, src.description, src.author
	f.PodcastType, f.Language, f.Explicit, f.CoverPath = &src.podcastType, src.language, db.Bool(src.explicit), src.coverPath
	f.ImageURL = db.Ptr("/Logo.png")
	if src.coverPath != nil && *src.coverPath != "" {
		image := "/feed/" + db.Val(f.Slug) + "/cover" + filepath.Ext(*src.coverPath)
		if !src.updatedAt.IsZero() {
			image += "?ts=" + strconv.FormatInt(src.updatedAt.Millis(), 10) // cache buster
		}
		f.ImageURL = &image
	}
}

// writeEpisodes replaces the episodes of feed with those of src, inside the
// caller's transaction, and returns them. An episode keeps its id and times
// while its file is still in the entity, so podcast apps do not see it as new.
func (h *Handler) writeEpisodes(ctx context.Context, feed db.Feed, src *source, old []db.FeedEpisode) ([]db.FeedEpisode, error) {
	byPath := map[string]db.FeedEpisode{}
	for _, e := range slices.Backward(old) { // the first of duplicates wins
		byPath[db.Val(e.FilePath)] = e
	}
	if _, err := h.db.Exec(ctx, `DELETE FROM feedEpisodes WHERE feedId = ?`, feed.ID); err != nil {
		return nil, fmt.Errorf("delete feed episodes: %w", err)
	}
	now := db.Now()
	rows := make([]db.FeedEpisode, len(src.episodes))
	for i, ep := range src.episodes {
		row := ep.FeedEpisode
		row.ID, row.CreatedAt, row.UpdatedAt = "", now, now
		if kept, ok := byPath[db.Val(ep.FilePath)]; ok {
			row.ID, row.CreatedAt, row.UpdatedAt = kept.ID, kept.CreatedAt, kept.UpdatedAt
		} else if id, err := ids.New(); err != nil {
			return nil, fmt.Errorf("new feed episode id: %w", err)
		} else {
			row.ID = id
		}
		row.Author, row.SiteURL, row.FeedID = feed.Author, feed.SiteURL, &feed.ID
		row.EnclosureURL = db.Ptr("/feed/" + db.Val(feed.Slug) + "/item/" + row.ID + "/media" + ep.ext) // the id is in the URL
		if err := h.db.Insert(ctx, "feedEpisodes", &row); err != nil {
			return nil, err
		}
		rows[i] = row
	}
	return rows, nil
}

// createFeed is Feed.createFeedFor*: the feed row and its episode rows, inside
// the caller's transaction.
func (h *Handler) createFeed(ctx context.Context, userID string, kind entityKind, entityID string, src *source, req openRequest) (db.Feed, error) {
	id, err := ids.New()
	if err != nil {
		return db.Feed{}, fmt.Errorf("new feed id: %w", err)
	}
	now, details := db.Now(), req.MetadataDetails
	feed := db.Feed{
		ID: id, Slug: &req.Slug, EntityType: db.Ptr(string(kind)), EntityID: &entityID, ServerAddress: &req.ServerAddress,
		FeedURL: db.Ptr("/feed/" + req.Slug), OwnerName: nilIfEmpty(string(details.OwnerName)), OwnerEmail: nilIfEmpty(string(details.OwnerEmail)),
		PreventIndexing: db.Bool(string(details.PreventIndexing) != "false"),
		UserID:          nilIfEmpty(userID), CreatedAt: now, UpdatedAt: now,
	}
	applySource(&feed, src)
	if err := h.db.Insert(ctx, "feeds", &feed); err != nil {
		return feed, err
	}
	_, err = h.writeEpisodes(ctx, feed, src, nil)
	return feed, err
}

// refreshFeed rebuilds a stale feed from its entity (Feed.updateFeedForEntity,
// RssFeedManager.checkFeedRequiresUpdate), inside the caller's transaction, and
// returns the feed's episodes. feed is updated in place. A feed whose entity is
// gone is left as it is.
func (h *Handler) refreshFeed(ctx context.Context, feed *db.Feed, episodes []db.FeedEpisode) ([]db.FeedEpisode, error) {
	src, err := h.loadSource(ctx, entityKind(db.Val(feed.EntityType)), db.Val(feed.EntityID))
	if errors.Is(err, errNoEntity) {
		return episodes, nil
	}
	if err != nil {
		return nil, err
	}
	// Collections and series also go stale when a book gains or loses audio files.
	if !src.updatedAt.After(feed.EntityUpdatedAt.Time) && (db.Val(feed.EntityType) == string(itemFeed) || len(episodes) == src.trackCount) {
		return episodes, nil
	}
	applySource(feed, src)
	feed.UpdatedAt = db.Now()
	if err := h.db.Update(ctx, "feeds", feed, "entityUpdatedAt", "imageURL", "siteURL", "title", "description", "author",
		"podcastType", "language", "explicit", "coverPath", "updatedAt"); err != nil {
		return nil, err
	}
	return h.writeEpisodes(ctx, *feed, src, episodes)
}
