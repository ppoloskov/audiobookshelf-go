package sharing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/wire"
)

const feedColumns = `id,slug,entityType,entityId,entityUpdatedAt,serverAddress,feedURL,imageURL,siteURL,title,description,author,podcastType,language,ownerName,ownerEmail,explicit,preventIndexing,coverPath,userId,createdAt,updatedAt`

// feedRow is one feeds row. Every nullable column stays nullable so a NULL
// reaches JSON as null and the RSS builder as a falsy value, like Sequelize.
type feedRow struct {
	ID              string
	Slug            sql.NullString
	EntityType      sql.NullString
	EntityID        sql.NullString
	EntityUpdatedAt time.Time
	ServerAddress   sql.NullString
	FeedURL         sql.NullString
	ImageURL        sql.NullString
	SiteURL         sql.NullString
	Title           sql.NullString
	Description     sql.NullString
	Author          sql.NullString
	PodcastType     sql.NullString
	Language        sql.NullString
	OwnerName       sql.NullString
	OwnerEmail      sql.NullString
	Explicit        sql.NullBool
	PreventIndexing sql.NullBool
	CoverPath       sql.NullString
	UserID          sql.NullString
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type feedEpisodeRow struct {
	ID            string
	Title         sql.NullString
	Author        sql.NullString
	Description   sql.NullString
	SiteURL       sql.NullString
	EnclosureURL  sql.NullString
	EnclosureType sql.NullString
	EnclosureSize sql.NullInt64
	PubDate       sql.NullString
	Season        sql.NullString
	Episode       sql.NullString
	EpisodeType   sql.NullString
	Duration      sql.NullFloat64
	FilePath      sql.NullString
	Explicit      sql.NullBool
}

const feedEpisodeColumns = `id,title,author,description,siteURL,enclosureURL,enclosureType,enclosureSize,pubDate,season,episode,episodeType,duration,filePath,explicit`

type rowScanner interface{ Scan(...any) error }

func scanFeedRow(row rowScanner) (feedRow, error) {
	var f feedRow
	var entityUpdated, created, updated any
	err := row.Scan(&f.ID, &f.Slug, &f.EntityType, &f.EntityID, &entityUpdated, &f.ServerAddress, &f.FeedURL, &f.ImageURL, &f.SiteURL, &f.Title, &f.Description, &f.Author, &f.PodcastType, &f.Language, &f.OwnerName, &f.OwnerEmail, &f.Explicit, &f.PreventIndexing, &f.CoverPath, &f.UserID, &created, &updated)
	f.EntityUpdatedAt, f.CreatedAt, f.UpdatedAt = asTime(entityUpdated), asTime(created), asTime(updated)
	return f, err
}

func (h *Handler) feedBySlug(ctx context.Context, slug string) (feedRow, error) {
	return scanFeedRow(h.db.QueryRowContext(ctx, `SELECT `+feedColumns+` FROM feeds WHERE slug=?`, slug))
}

func (h *Handler) feedEpisodeRows(ctx context.Context, feedID string) ([]feedEpisodeRow, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT `+feedEpisodeColumns+` FROM feedEpisodes WHERE feedId=? ORDER BY rowid`, feedID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []feedEpisodeRow{}
	for rows.Next() {
		var e feedEpisodeRow
		if err := rows.Scan(&e.ID, &e.Title, &e.Author, &e.Description, &e.SiteURL, &e.EnclosureURL, &e.EnclosureType, &e.EnclosureSize, &e.PubDate, &e.Season, &e.Episode, &e.EpisodeType, &e.Duration, &e.FilePath, &e.Explicit); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func strPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

func boolPtr(v sql.NullBool) *bool {
	if !v.Valid {
		return nil
	}
	return &v.Bool
}

func (f feedRow) minified() wire.FeedMinified {
	return wire.FeedMinified{
		ID: f.ID, EntityType: f.EntityType.String, EntityID: f.EntityID.String, FeedURL: strPtr(f.FeedURL),
		Meta: wire.FeedMinifiedMeta{Title: strPtr(f.Title), Description: strPtr(f.Description), PreventIndexing: boolPtr(f.PreventIndexing), OwnerName: strPtr(f.OwnerName), OwnerEmail: strPtr(f.OwnerEmail)},
	}
}

func (f feedRow) old(eps []feedEpisodeRow) wire.Feed {
	out := wire.Feed{
		ID: f.ID, Slug: strPtr(f.Slug), UserID: strPtr(f.UserID), EntityType: strPtr(f.EntityType), EntityID: strPtr(f.EntityID),
		Meta: wire.FeedMeta{
			Title: strPtr(f.Title), Description: strPtr(f.Description), Author: strPtr(f.Author), ImageURL: strPtr(f.ImageURL),
			FeedURL: strPtr(f.FeedURL), Link: strPtr(f.SiteURL), Explicit: boolPtr(f.Explicit), Type: strPtr(f.PodcastType),
			Language: strPtr(f.Language), PreventIndexing: boolPtr(f.PreventIndexing), OwnerName: strPtr(f.OwnerName), OwnerEmail: strPtr(f.OwnerEmail),
		},
		ServerAddress: strPtr(f.ServerAddress), FeedURL: strPtr(f.FeedURL), Episodes: make([]wire.FeedEpisode, 0, len(eps)),
		CreatedAt: f.CreatedAt.UnixMilli(), UpdatedAt: f.UpdatedAt.UnixMilli(),
	}
	// entityUpdatedAt?.valueOf() || null, coverPath || null
	if ms := f.EntityUpdatedAt.UnixMilli(); !f.EntityUpdatedAt.IsZero() && ms != 0 {
		out.EntityUpdatedAt = &ms
	}
	if f.CoverPath.Valid && f.CoverPath.String != "" {
		out.CoverPath = &f.CoverPath.String
	}
	for _, e := range eps {
		var size *int64
		if e.EnclosureSize.Valid {
			size = &e.EnclosureSize.Int64
		}
		var duration *float64
		if e.Duration.Valid {
			duration = &e.Duration.Float64
		}
		out.Episodes = append(out.Episodes, wire.FeedEpisode{
			ID: e.ID, Title: strPtr(e.Title), Description: strPtr(e.Description),
			Enclosure: wire.FeedEpisodeEnclosure{URL: strPtr(e.EnclosureURL), Size: size, Type: strPtr(e.EnclosureType)},
			PubDate:   strPtr(e.PubDate), Link: strPtr(e.SiteURL), Author: strPtr(e.Author), Explicit: boolPtr(e.Explicit),
			Duration: duration, Season: strPtr(e.Season), Episode: strPtr(e.Episode), EpisodeType: strPtr(e.EpisodeType), FullPath: strPtr(e.FilePath),
		})
	}
	return out
}

func (h *Handler) listFeeds(w http.ResponseWriter, r *http.Request, u store.User) {
	if !admin(u) {
		httputil.SendStatus(w, 403, httputil.WithoutETag())
		return
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT `+feedColumns+` FROM feeds`)
	if err != nil {
		logging.RequestError(r, "internal error", err)
		httputil.SendStatus(w, 500, httputil.WithoutETag())
		return
	}
	var feeds []feedRow
	for rows.Next() {
		f, err := scanFeedRow(rows)
		if err != nil {
			_ = rows.Close()
			logging.RequestError(r, "internal error", err)
			httputil.SendStatus(w, 500, httputil.WithoutETag())
			return
		}
		feeds = append(feeds, f)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		logging.RequestError(r, "internal error", err)
		httputil.SendStatus(w, 500, httputil.WithoutETag())
		return
	}
	out := wire.FeedList{Feeds: make([]wire.Feed, 0, len(feeds)), Minified: make([]wire.FeedMinified, 0, len(feeds))}
	for _, f := range feeds {
		eps, err := h.feedEpisodeRows(r.Context(), f.ID)
		if err != nil {
			logging.RequestError(r, "internal error", err)
			httputil.SendStatus(w, 500, httputil.WithoutETag())
			return
		}
		out.Feeds = append(out.Feeds, f.old(eps))
		out.Minified = append(out.Minified, f.minified())
	}
	write(w, out)
}

// openFeedRequest is the body of the three open routes. The fields stay raw
// because upstream checks their JavaScript types (typeof === 'string', !== false).
type openFeedRequest struct {
	ServerAddress   json.RawMessage `json:"serverAddress"`
	Slug            json.RawMessage `json:"slug"`
	MetadataDetails json.RawMessage `json:"metadataDetails"`
}

type metadataDetails struct {
	PreventIndexing json.RawMessage `json:"preventIndexing"`
	OwnerName       json.RawMessage `json:"ownerName"`
	OwnerEmail      json.RawMessage `json:"ownerEmail"`
}

// jsonString is the value of a raw JSON string, ok false for any other type.
func jsonString(raw json.RawMessage) (string, bool) {
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// feedOptions is RssFeedManager.getFeedOptionsFromReqOptions: preventIndexing
// is true unless the request says exactly false; owner fields are non-empty
// strings or NULL.
type feedOptions struct {
	preventIndexing bool
	ownerName       sql.NullString
	ownerEmail      sql.NullString
}

func (req openFeedRequest) options() feedOptions {
	var d metadataDetails
	_ = json.Unmarshal(req.MetadataDetails, &d)
	opts := feedOptions{preventIndexing: string(d.PreventIndexing) != "false"}
	if s, ok := jsonString(d.OwnerName); ok && s != "" {
		opts.ownerName = sql.NullString{String: s, Valid: true}
	}
	if s, ok := jsonString(d.OwnerEmail); ok && s != "" {
		opts.ownerEmail = sql.NullString{String: s, Valid: true}
	}
	return opts
}

func (req openFeedRequest) valid() (serverAddress, slug string, ok bool) {
	serverAddress, ok1 := jsonString(req.ServerAddress)
	slug, ok2 := jsonString(req.Slug)
	return serverAddress, slug, ok1 && ok2 && serverAddress != "" && slug != ""
}

// openFeed is RSSFeedController.openRSSFeedForItem / ForCollection / ForSeries
// (server/controllers/RSSFeedController.js:38-170). The checks run in the
// order upstream runs them, which differs between the item route and the
// collection and series routes.
func (h *Handler) openFeed(w http.ResponseWriter, r *http.Request, u store.User, entityType string) {
	if !admin(u) {
		httputil.SendStatus(w, 403, httputil.WithoutETag())
		return
	}
	ctx := r.Context()
	id := routeID(r.URL.Path, "open")
	var req openFeedRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	var src *feedSource
	var err error
	noTracks := "Item has no audio tracks"
	switch entityType {
	case "libraryItem":
		if src, err = h.loadItemSource(ctx, id); err != nil {
			h.feedEntityError(w, err)
			return
		}
		if !libraryAccess(u, src.LibraryID) {
			httputil.SendStatus(w, 403, httputil.WithoutETag())
			return
		}
	case "collection":
		noTracks = "Collection has no audio tracks"
	case "series":
		noTracks = "Series has no audio tracks"
	}
	serverAddress, slug, ok := req.valid()
	if !ok {
		httputil.SendText(w, 400, "Invalid request body", httputil.WithoutETag())
		return
	}
	if entityType == "libraryItem" {
		if len(src.Episodes) == 0 {
			httputil.SendText(w, 400, noTracks, httputil.WithoutETag())
			return
		}
		if h.slugInUse(ctx, w, slug) {
			return
		}
	} else {
		if h.slugInUse(ctx, w, slug) {
			return
		}
		if src, err = h.loadAggregateSource(ctx, entityType, id); err != nil {
			h.feedEntityError(w, err)
			return
		}
		if len(src.Episodes) == 0 {
			httputil.SendText(w, 400, noTracks, httputil.WithoutETag())
			return
		}
	}
	feed, err := h.createFeed(ctx, u.ID, entityType, id, src, serverAddress, slug, req.options())
	if err != nil {
		logging.RequestError(r, "Failed to open RSS feed", err)
		httputil.SendText(w, 500, "Failed to open RSS feed", httputil.WithoutETag())
		return
	}
	minified := feed.minified()
	logging.Infof("[RssFeedManager] Opened RSS feed %q", feedURLText(minified)) // RssFeedManager.js:301
	if h.emit != nil {
		h.emit("*", "rss_feed_open", minified)
	}
	write(w, wire.FeedOpened{Feed: minified})
}

func feedURLText(f wire.FeedMinified) string {
	if f.FeedURL == nil {
		return ""
	}
	return *f.FeedURL
}

func (h *Handler) feedEntityError(w http.ResponseWriter, err error) {
	if errors.Is(err, errFeedEntityMissing) {
		httputil.SendStatus(w, 404, httputil.WithoutETag())
		return
	}
	logging.Errorf("[sharing] RSS feed entity lookup failed: %v", err)
	httputil.SendStatus(w, 500, httputil.WithoutETag())
}

// slugInUse answers 400 and reports true when a feed already has the slug.
func (h *Handler) slugInUse(ctx context.Context, w http.ResponseWriter, slug string) bool {
	var count int
	if err := h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM feeds WHERE slug=?`, slug).Scan(&count); err != nil {
		logging.Errorf("[sharing] slugInUse: internal error: %v", err)
		httputil.SendStatus(w, 500, httputil.WithoutETag())
		return true
	}
	if count > 0 {
		httputil.SendText(w, 400, "Slug already in use", httputil.WithoutETag())
		return true
	}
	return false
}

func feedImageURL(slug string, coverPath sql.NullString, entityUpdatedAt time.Time) string {
	if !coverPath.Valid || coverPath.String == "" {
		return "/Logo.png"
	}
	buster := ""
	if !entityUpdatedAt.IsZero() {
		buster = "?ts=" + strconv.FormatInt(entityUpdatedAt.UnixMilli(), 10)
	}
	return "/feed/" + slug + "/cover" + nodeExt(coverPath.String) + buster
}

func boolInt64(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func nullBoolInt(v sql.NullBool) any {
	if !v.Valid {
		return nil
	}
	return boolInt64(v.Bool)
}

// newFeedRow is Feed.getFeedObjFor* plus the options and a new id.
func newFeedRow(id, userID, entityType, entityID string, src *feedSource, serverAddress, slug string, opts feedOptions, now time.Time) feedRow {
	return feedRow{
		ID: id, Slug: sql.NullString{String: slug, Valid: true}, EntityType: sql.NullString{String: entityType, Valid: true},
		EntityID: sql.NullString{String: entityID, Valid: true}, EntityUpdatedAt: src.EntityUpdatedAt,
		ServerAddress: sql.NullString{String: serverAddress, Valid: true}, FeedURL: sql.NullString{String: "/feed/" + slug, Valid: true},
		ImageURL: sql.NullString{String: feedImageURL(slug, src.CoverPath, src.EntityUpdatedAt), Valid: true},
		SiteURL:  sql.NullString{String: src.SiteURL, Valid: true}, Title: sql.NullString{String: src.Title, Valid: true},
		Description: src.Description, Author: src.Author, PodcastType: sql.NullString{String: src.PodcastType, Valid: true},
		Language: src.Language, OwnerName: opts.ownerName, OwnerEmail: opts.ownerEmail,
		Explicit: sql.NullBool{Bool: src.Explicit, Valid: true}, PreventIndexing: sql.NullBool{Bool: opts.preventIndexing, Valid: true},
		CoverPath: src.CoverPath, UserID: sql.NullString{String: userID, Valid: userID != ""}, CreatedAt: now, UpdatedAt: now,
	}
}

func (f feedRow) insertArgs() []any {
	return []any{f.ID, f.Slug, f.EntityType, f.EntityID, dbtime.Format(f.EntityUpdatedAt), f.ServerAddress, f.FeedURL, f.ImageURL, f.SiteURL, f.Title, f.Description, f.Author, f.PodcastType, f.Language, f.OwnerName, f.OwnerEmail, nullBoolInt(f.Explicit), nullBoolInt(f.PreventIndexing), f.CoverPath, f.UserID, dbtime.Format(f.CreatedAt), dbtime.Format(f.UpdatedAt)}
}

// episodeRow is FeedEpisode.getFeedEpisodeObj*: the episode id is part of the
// enclosure URL.
func episodeRow(id string, feed feedRow, ep feedEpisodeSource) feedEpisodeRow {
	return feedEpisodeRow{
		ID: id, Title: sql.NullString{String: ep.Title, Valid: true}, Author: feed.Author, Description: ep.Description,
		SiteURL:       feed.SiteURL,
		EnclosureURL:  sql.NullString{String: "/feed/" + feed.Slug.String + "/item/" + id + "/media" + nodeExt(ep.Filename), Valid: true},
		EnclosureType: ep.EnclosureType, EnclosureSize: ep.EnclosureSize, PubDate: ep.PubDate, Season: ep.Season, Episode: ep.Episode,
		EpisodeType: ep.EpisodeType, Duration: ep.Duration, FilePath: ep.FilePath, Explicit: sql.NullBool{Bool: ep.Explicit, Valid: true},
	}
}

func (e feedEpisodeRow) args() []any {
	return []any{e.ID, e.Title, e.Author, e.Description, e.SiteURL, e.EnclosureURL, e.EnclosureType, e.EnclosureSize, e.PubDate, e.Season, e.Episode, e.EpisodeType, e.Duration, e.FilePath, nullBoolInt(e.Explicit)}
}

// createFeed is Feed.createFeedFor* (server/models/Feed.js:157): the feed row
// and its episode rows in one transaction.
func (h *Handler) createFeed(ctx context.Context, userID, entityType, entityID string, src *feedSource, serverAddress, slug string, opts feedOptions) (feedRow, error) {
	id, err := uuid()
	if err != nil {
		return feedRow{}, err
	}
	now := time.Now().UTC()
	feed := newFeedRow(id, userID, entityType, entityID, src, serverAddress, slug, opts, now)
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return feedRow{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `INSERT INTO feeds(`+feedColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, feed.insertArgs()...); err != nil {
		return feedRow{}, err
	}
	for _, ep := range src.Episodes {
		epID, err := uuid()
		if err != nil {
			return feedRow{}, err
		}
		args := append(episodeRow(epID, feed, ep).args(), dbtime.Format(now), dbtime.Format(now), id)
		if _, err = tx.ExecContext(ctx, `INSERT INTO feedEpisodes(`+feedEpisodeColumns+`,createdAt,updatedAt,feedId) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, args...); err != nil {
			return feedRow{}, err
		}
	}
	return feed, tx.Commit()
}

// closeFeed is RSSFeedController.closeRSSFeed and RssFeedManager.handleCloseFeed.
func (h *Handler) closeFeed(w http.ResponseWriter, r *http.Request, u store.User) {
	if !admin(u) {
		httputil.SendStatus(w, 403, httputil.WithoutETag())
		return
	}
	id := routeID(r.URL.Path, "close")
	feed, err := scanFeedRow(h.db.QueryRowContext(r.Context(), `SELECT `+feedColumns+` FROM feeds WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		httputil.SendStatus(w, 404, httputil.WithoutETag())
		return
	}
	if err != nil {
		logging.RequestError(r, "internal error", err)
		httputil.SendStatus(w, 500, httputil.WithoutETag())
		return
	}
	if _, err = h.db.ExecContext(r.Context(), `DELETE FROM feeds WHERE id=?`, id); err != nil {
		logging.RequestError(r, "internal error", err)
		httputil.SendStatus(w, 500, httputil.WithoutETag())
		return
	}
	logging.Infof("[RssFeedManager] Closed RSS feed %q", feedURLText(feed.minified())) // RssFeedManager.js:359
	if h.emit != nil {
		h.emit("*", "rss_feed_closed", feed.minified())
	}
	httputil.SendStatus(w, 200, httputil.WithoutETag())
}

// publicFeed is GET /feed/:slug, /feed/:slug/item/:episodeId/* and
// /feed/:slug/cover* (RssFeedManager.getFeed, getFeedItem, getFeedCover).
func (h *Handler) publicFeed(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 {
		httputil.SendStatus(w, 404, httputil.WithoutETag())
		return
	}
	slug := parts[1]
	if len(parts) >= 4 && parts[2] == "item" {
		h.feedItem(w, r, slug, parts[3])
		return
	}
	if len(parts) >= 3 && strings.HasPrefix(parts[2], "cover") {
		h.feedCover(w, r, slug)
		return
	}
	if len(parts) != 2 {
		httputil.SendStatus(w, 404, httputil.WithoutETag())
		return
	}
	ctx := r.Context()
	feed, err := h.feedBySlug(ctx, slug)
	if errors.Is(err, sql.ErrNoRows) {
		httputil.SendStatus(w, 404, httputil.WithoutETag())
		return
	}
	var eps []feedEpisodeRow
	if err == nil {
		eps, err = h.feedEpisodeRows(ctx, feed.ID)
	}
	if err == nil {
		feed, eps, err = h.refreshFeedIfStale(ctx, feed, eps)
	}
	if err != nil {
		logging.RequestError(r, "internal error", err)
		httputil.SendStatus(w, 500, httputil.WithoutETag())
		return
	}
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	_, _ = io.WriteString(w, feedXML(feed, eps, hostPrefix(r, h.cfg.RouterBasePath), time.Now()))
}

// feedRequiresUpdate is RssFeedManager.checkFeedRequiresUpdate
// (server/managers/RssFeedManager.js:68).
func feedRequiresUpdate(feed feedRow, episodeCount int, src *feedSource) bool {
	if feed.EntityType.String == "libraryItem" {
		return src.EntityUpdatedAt.After(feed.EntityUpdatedAt)
	}
	if episodeCount != src.BookTrackCount {
		return true
	}
	return newerOf(src.OwnUpdatedAt, src.ChildrenUpdatedAt).After(feed.EntityUpdatedAt)
}

// refreshFeedIfStale is checkFeedRequiresUpdate followed by
// Feed.updateFeedForEntity (server/models/Feed.js:481). Episodes keep their id
// when their file is still in the entity; the response of the refreshing
// request lists the episodes in the entity's order.
func (h *Handler) refreshFeedIfStale(ctx context.Context, feed feedRow, eps []feedEpisodeRow) (feedRow, []feedEpisodeRow, error) {
	if feed.EntityType.String != "libraryItem" && feed.EntityType.String != "collection" && feed.EntityType.String != "series" {
		return feed, eps, errors.New("invalid feed entity type")
	}
	src, err := h.loadFeedSource(ctx, feed.EntityType.String, feed.EntityID.String)
	if err != nil {
		return feed, eps, err
	}
	if !feedRequiresUpdate(feed, len(eps), src) {
		return feed, eps, nil
	}
	now := time.Now().UTC()
	next := newFeedRow(feed.ID, feed.UserID.String, feed.EntityType.String, feed.EntityID.String, src, feed.ServerAddress.String, feed.Slug.String, feedOptions{}, now)
	next.UserID, next.OwnerName, next.OwnerEmail, next.PreventIndexing, next.CreatedAt = feed.UserID, feed.OwnerName, feed.OwnerEmail, feed.PreventIndexing, feed.CreatedAt
	next.UpdatedAt = feed.UpdatedAt
	if feedColumnsChanged(feed, next) {
		next.UpdatedAt = now
	}
	byPath := map[string]string{}
	for _, e := range eps {
		if _, dup := byPath[e.FilePath.String]; !dup {
			byPath[e.FilePath.String] = e.ID
		}
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return feed, eps, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `UPDATE feeds SET entityUpdatedAt=?,imageURL=?,siteURL=?,title=?,description=?,author=?,podcastType=?,language=?,explicit=?,coverPath=?,updatedAt=? WHERE id=?`,
		dbtime.Format(next.EntityUpdatedAt), next.ImageURL, next.SiteURL, next.Title, next.Description, next.Author, next.PodcastType, next.Language, nullBoolInt(next.Explicit), next.CoverPath, dbtime.Format(next.UpdatedAt), feed.ID); err != nil {
		return feed, eps, err
	}
	kept := map[string]bool{}
	fresh := make([]feedEpisodeRow, 0, len(src.Episodes))
	for _, ep := range src.Episodes {
		epID := byPath[ep.FilePath.String]
		if epID == "" {
			if epID, err = uuid(); err != nil {
				return feed, eps, err
			}
		}
		row := episodeRow(epID, next, ep)
		kept[epID] = true
		fresh = append(fresh, row)
		// bulkCreate with updateOnDuplicate leaves createdAt and updatedAt of
		// existing rows alone (server/models/FeedEpisode.js updateOnDuplicate list).
		args := append(row.args(), dbtime.Format(now), dbtime.Format(now), feed.ID)
		if _, err = tx.ExecContext(ctx, `INSERT INTO feedEpisodes(`+feedEpisodeColumns+`,createdAt,updatedAt,feedId) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET title=excluded.title,author=excluded.author,description=excluded.description,siteURL=excluded.siteURL,enclosureURL=excluded.enclosureURL,enclosureType=excluded.enclosureType,enclosureSize=excluded.enclosureSize,pubDate=excluded.pubDate,season=excluded.season,episode=excluded.episode,episodeType=excluded.episodeType,duration=excluded.duration,filePath=excluded.filePath,explicit=excluded.explicit`, args...); err != nil {
			return feed, eps, err
		}
	}
	for _, e := range eps {
		if kept[e.ID] {
			continue
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM feedEpisodes WHERE id=?`, e.ID); err != nil {
			return feed, eps, err
		}
	}
	if err = tx.Commit(); err != nil {
		return feed, eps, err
	}
	return next, fresh, nil
}

// feedColumnsChanged reports whether Feed.update(feedObj) would change a
// column; Sequelize skips the save, and the updatedAt bump, when none did.
func feedColumnsChanged(a, b feedRow) bool {
	return !a.EntityUpdatedAt.Equal(b.EntityUpdatedAt) || a.ImageURL != b.ImageURL || a.SiteURL != b.SiteURL || a.Title != b.Title ||
		a.Description != b.Description || a.Author != b.Author || a.PodcastType != b.PodcastType || a.Language != b.Language ||
		a.Explicit != b.Explicit || a.CoverPath != b.CoverPath || a.FeedURL != b.FeedURL
}

func (h *Handler) feedItem(w http.ResponseWriter, r *http.Request, slug, episodeID string) {
	var path sql.NullString
	err := h.db.QueryRowContext(r.Context(), `SELECT fe.filePath FROM feedEpisodes fe JOIN feeds f ON f.id=fe.feedId WHERE f.slug=? AND fe.id=?`, slug, episodeID).Scan(&path)
	if err != nil || path.String == "" {
		httputil.SendStatus(w, 404, httputil.WithoutETag())
		return
	}
	serveMedia(w, r, path.String)
}

// feedCover streams the cover with Content-Type image/<extension> like
// RssFeedManager.getFeedCover (res.type + createReadStream, no ranges).
func (h *Handler) feedCover(w http.ResponseWriter, r *http.Request, slug string) {
	var path sql.NullString
	if err := h.db.QueryRowContext(r.Context(), `SELECT coverPath FROM feeds WHERE slug=?`, slug).Scan(&path); err != nil || path.String == "" {
		httputil.SendStatus(w, 404, httputil.WithoutETag())
		return
	}
	f, err := os.Open(path.String)
	if err != nil {
		httputil.SendStatus(w, 404, httputil.WithoutETag())
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "image/"+strings.ToLower(strings.TrimPrefix(nodeExt(path.String), ".")))
	_, _ = io.Copy(w, f)
}

// --- RSS XML (server/libs/rss, server/libs/xml, Feed.buildXml, FeedEpisode.getRSSData)

func xmlEsc(s string) string {
	return strings.NewReplacer("&", "&amp;", `"`, "&quot;", "'", "&apos;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// xmlCData is an element with a _cdata value: an empty value is falsy in
// node-xml, which then writes a self-closed element.
func xmlCData(name, value string) string {
	if value == "" {
		return "<" + name + "/>"
	}
	return "<" + name + "><![CDATA[" + strings.ReplaceAll(value, "]]>", "]]]]><![CDATA[>") + "]]></" + name + ">"
}

func xmlText(name, value string) string { return "<" + name + ">" + xmlEsc(value) + "</" + name + ">" }

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func jsBool(b bool) string { return strconv.FormatBool(b) }

// jsGMT is new Date(value).toGMTString().
func jsGMT(value string) string {
	t, ok := parseJSDate(value)
	if !ok {
		return "Invalid Date"
	}
	return t.UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT")
}

func mimeLookup(url string) string {
	switch strings.ToLower(nodeExt(url)) {
	case ".mp3":
		return "audio/mpeg"
	case ".m4a", ".m4b", ".mp4":
		return "audio/mp4"
	case ".flac":
		return "audio/flac"
	case ".ogg", ".oga", ".opus":
		return "audio/ogg"
	case ".wav":
		return "audio/wav"
	case ".aac":
		return "audio/aac"
	}
	return "false"
}

// feedXML is Feed.buildXml: node-rss output, compact, with the channel and
// item order of server/libs/rss/index.js generateXML.
func feedXML(f feedRow, eps []feedEpisodeRow, host string, now time.Time) string {
	title := orDefault(f.Title.String, "Untitled RSS Feed")
	siteURL := host + f.SiteURL.String
	imageURL := host + f.ImageURL.String
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><rss xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:content="http://purl.org/rss/1.0/modules/content/" xmlns:atom="http://www.w3.org/2005/Atom" version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd" xmlns:podcast="https://podcastindex.org/namespace/1.0" xmlns:googleplay="http://www.google.com/schemas/play-podcasts/1.0"><channel>`)
	b.WriteString(xmlCData("title", title))
	b.WriteString(xmlCData("description", orDefault(f.Description.String, title)))
	b.WriteString(xmlText("link", siteURL))
	b.WriteString("<image>" + xmlText("url", imageURL) + xmlText("title", title) + xmlText("link", siteURL) + "</image>")
	b.WriteString("<generator>Audiobookshelf</generator>")
	b.WriteString(xmlText("lastBuildDate", now.UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT")))
	b.WriteString(`<atom:link href="` + xmlEsc(host+f.FeedURL.String) + `" rel="self" type="application/rss+xml"/>`)
	author := orDefault(f.Author.String, "advplyr")
	b.WriteString(xmlText("language", orDefault(f.Language.String, "en")))
	b.WriteString(xmlText("author", author))
	b.WriteString(xmlText("itunes:author", author))
	b.WriteString(xmlText("itunes:type", orDefault(f.PodcastType.String, "serial")))
	b.WriteString(`<itunes:image href="` + xmlEsc(imageURL) + `"/>`)
	b.WriteString(xmlText("itunes:explicit", jsBool(f.Explicit.Bool)))
	if f.Description.String != "" {
		b.WriteString(xmlCData("itunes:summary", f.Description.String))
	}
	if f.OwnerName.String != "" || f.Author.String != "" {
		b.WriteString("<itunes:owner>" + xmlText("itunes:name", orDefault(f.OwnerName.String, f.Author.String)))
		if f.OwnerEmail.String != "" {
			b.WriteString(xmlText("itunes:email", f.OwnerEmail.String))
		}
		b.WriteString("</itunes:owner>")
	} else if f.OwnerEmail.String != "" {
		b.WriteString("<itunes:owner>" + xmlText("itunes:email", f.OwnerEmail.String) + "</itunes:owner>")
	}
	if f.PreventIndexing.Bool {
		b.WriteString(xmlText("itunes:block", "yes") + xmlText("googleplay:block", "yes"))
	}
	for _, ep := range eps {
		writeFeedItem(&b, ep, host)
	}
	b.WriteString("</channel></rss>")
	return b.String()
}

func writeFeedItem(b *strings.Builder, ep feedEpisodeRow, host string) {
	b.WriteString("<item>")
	b.WriteString(xmlCData("title", orDefault(ep.Title.String, "No title")))
	if ep.Description.String != "" {
		b.WriteString(xmlCData("description", ep.Description.String))
	}
	b.WriteString(xmlText("link", host+ep.SiteURL.String))
	guid := host + ep.EnclosureURL.String
	b.WriteString(`<guid isPermaLink="false">` + xmlEsc(guid) + `</guid>`)
	if ep.Author.String != "" {
		b.WriteString(xmlCData("dc:creator", ep.Author.String))
	}
	if ep.PubDate.String != "" {
		b.WriteString(xmlText("pubDate", jsGMT(ep.PubDate.String)))
	}
	size := ep.EnclosureSize.Int64
	typ := ep.EnclosureType.String
	if typ == "" {
		typ = mimeLookup(guid)
	}
	b.WriteString(`<enclosure url="` + xmlEsc(guid) + `" length="` + strconv.FormatInt(size, 10) + `" type="` + xmlEsc(typ) + `"/>`)
	if ep.Author.String != "" {
		b.WriteString(xmlText("itunes:author", ep.Author.String))
	}
	b.WriteString(xmlText("itunes:duration", strconv.FormatInt(int64(math.Floor(ep.Duration.Float64+0.5)), 10)))
	b.WriteString(xmlText("itunes:explicit", jsBool(ep.Explicit.Bool)))
	if ep.EpisodeType.String != "" {
		b.WriteString(xmlText("itunes:episodeType", ep.EpisodeType.String))
	}
	if ep.Season.String != "" {
		b.WriteString(xmlText("itunes:season", ep.Season.String))
	}
	if ep.Episode.String != "" {
		b.WriteString(xmlText("itunes:episode", ep.Episode.String))
	}
	if ep.Description.String != "" {
		b.WriteString(xmlCData("itunes:summary", ep.Description.String))
	}
	b.WriteString("</item>")
}
