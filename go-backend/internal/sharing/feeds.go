package sharing

import (
	"audiobookshelf-compat/internal/access"
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

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/wire"
)

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

// feedFromRow maps a feeds row from the generated queries. The nullable flags
// keep NULL, as the wire and RSS code read them.
func feedFromRow(r sqlc.Feed) feedRow {
	return feedRow{
		ID: r.ID, Slug: r.Slug, EntityType: r.Entitytype, EntityID: r.Entityid, EntityUpdatedAt: asTime(r.Entityupdatedat),
		ServerAddress: r.Serveraddress, FeedURL: r.Feedurl, ImageURL: r.Imageurl, SiteURL: r.Siteurl, Title: r.Title,
		Description: r.Description, Author: r.Author, PodcastType: r.Podcasttype, Language: r.Language,
		OwnerName: r.Ownername, OwnerEmail: r.Owneremail, Explicit: nullBool(r.Explicit), PreventIndexing: nullBool(r.Preventindexing),
		CoverPath: r.Coverpath, UserID: r.Userid, CreatedAt: asTime(r.Createdat), UpdatedAt: asTime(r.Updatedat),
	}
}

// feedResult is feedFromRow for a generated query's (row, error) result.
func feedResult(row sqlc.Feed, err error) (feedRow, error) {
	if err != nil {
		return feedRow{}, err
	}
	return feedFromRow(row), nil
}

func feedEpisodeFromRow(r sqlc.Feedepisode) feedEpisodeRow {
	return feedEpisodeRow{
		ID: r.ID, Title: r.Title, Author: r.Author, Description: r.Description, SiteURL: r.Siteurl,
		EnclosureURL: r.Enclosureurl, EnclosureType: r.Enclosuretype, EnclosureSize: r.Enclosuresize, PubDate: r.Pubdate,
		Season: r.Season, Episode: r.Episode, EpisodeType: r.Episodetype, Duration: r.Duration, FilePath: r.Filepath,
		Explicit: nullBool(r.Explicit),
	}
}

// nullBool reads a nullable TINYINT(1) flag, keeping NULL.
func nullBool(v sql.NullInt64) sql.NullBool { return sql.NullBool{Bool: v.Int64 != 0, Valid: v.Valid} }

func (h *Handler) feedBySlug(ctx context.Context, slug string) (feedRow, error) {
	return feedResult(h.queries().GetFeedBySlug(ctx, slug))
}

func (h *Handler) feedEpisodeRows(ctx context.Context, feedID string) ([]feedEpisodeRow, error) {
	rows, err := h.queries().ListFeedEpisodes(ctx, feedID)
	if err != nil {
		return nil, err
	}
	out := make([]feedEpisodeRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, feedEpisodeFromRow(r))
	}
	return out, nil
}

func boolPtr(v sql.NullBool) *bool {
	if !v.Valid {
		return nil
	}
	return &v.Bool
}

func (f feedRow) minified() wire.FeedMinified {
	return wire.FeedMinified{
		ID: f.ID, EntityType: f.EntityType.String, EntityID: f.EntityID.String, FeedURL: store.NullStringPtr(f.FeedURL),
		Meta: wire.FeedMinifiedMeta{Title: store.NullStringPtr(f.Title), Description: store.NullStringPtr(f.Description), PreventIndexing: boolPtr(f.PreventIndexing), OwnerName: store.NullStringPtr(f.OwnerName), OwnerEmail: store.NullStringPtr(f.OwnerEmail)},
	}
}

func (f feedRow) old(eps []feedEpisodeRow) wire.Feed {
	out := wire.Feed{
		ID: f.ID, Slug: store.NullStringPtr(f.Slug), UserID: store.NullStringPtr(f.UserID), EntityType: store.NullStringPtr(f.EntityType), EntityID: store.NullStringPtr(f.EntityID),
		Meta: wire.FeedMeta{
			Title: store.NullStringPtr(f.Title), Description: store.NullStringPtr(f.Description), Author: store.NullStringPtr(f.Author), ImageURL: store.NullStringPtr(f.ImageURL),
			FeedURL: store.NullStringPtr(f.FeedURL), Link: store.NullStringPtr(f.SiteURL), Explicit: boolPtr(f.Explicit), Type: store.NullStringPtr(f.PodcastType),
			Language: store.NullStringPtr(f.Language), PreventIndexing: boolPtr(f.PreventIndexing), OwnerName: store.NullStringPtr(f.OwnerName), OwnerEmail: store.NullStringPtr(f.OwnerEmail),
		},
		ServerAddress: store.NullStringPtr(f.ServerAddress), FeedURL: store.NullStringPtr(f.FeedURL), Episodes: make([]wire.FeedEpisode, 0, len(eps)),
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
			ID: e.ID, Title: store.NullStringPtr(e.Title), Description: store.NullStringPtr(e.Description),
			Enclosure: wire.FeedEpisodeEnclosure{URL: store.NullStringPtr(e.EnclosureURL), Size: size, Type: store.NullStringPtr(e.EnclosureType)},
			PubDate:   store.NullStringPtr(e.PubDate), Link: store.NullStringPtr(e.SiteURL), Author: store.NullStringPtr(e.Author), Explicit: boolPtr(e.Explicit),
			Duration: duration, Season: store.NullStringPtr(e.Season), Episode: store.NullStringPtr(e.Episode), EpisodeType: store.NullStringPtr(e.EpisodeType), FullPath: store.NullStringPtr(e.FilePath),
		})
	}
	return out
}

func (h *Handler) listFeeds(w http.ResponseWriter, r *http.Request, u store.User) error {
	if !access.AdminUser(u) {
		return httputil.Status(403, "")
	}
	rows, err := h.queries().ListFeeds(r.Context())
	if err != nil {
		return &httputil.Error{Status: 500, Cause: err}
	}
	out := wire.FeedList{Feeds: make([]wire.Feed, 0, len(rows)), Minified: make([]wire.FeedMinified, 0, len(rows))}
	for _, row := range rows {
		f := feedFromRow(row)
		eps, err := h.feedEpisodeRows(r.Context(), f.ID)
		if err != nil {
			return &httputil.Error{Status: 500, Cause: err}
		}
		out.Feeds = append(out.Feeds, f.old(eps))
		out.Minified = append(out.Minified, f.minified())
	}
	write(w, out)
	return nil
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
func (h *Handler) openFeed(w http.ResponseWriter, r *http.Request, u store.User, entityType string) error {
	if !access.AdminUser(u) {
		return httputil.Status(403, "")
	}
	ctx := r.Context()
	id := r.PathValue("id")
	var req openFeedRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	var src *feedSource
	var err error
	noTracks := "Item has no audio tracks"
	switch entityType {
	case "libraryItem":
		if src, err = h.loadItemSource(ctx, id); err != nil {
			return feedEntityError(err)
		}
		if !access.Of(u).Library(src.LibraryID) {
			return httputil.Status(403, "")
		}
	case "collection":
		noTracks = "Collection has no audio tracks"
	case "series":
		noTracks = "Series has no audio tracks"
	}
	serverAddress, slug, ok := req.valid()
	if !ok {
		return httputil.Status(400, "Invalid request body")
	}
	if entityType == "libraryItem" {
		if len(src.Episodes) == 0 {
			return httputil.Status(400, noTracks)
		}
		if err := h.slugInUse(ctx, slug); err != nil {
			return err
		}
	} else {
		if err := h.slugInUse(ctx, slug); err != nil {
			return err
		}
		if src, err = h.loadAggregateSource(ctx, entityType, id); err != nil {
			return feedEntityError(err)
		}
		if len(src.Episodes) == 0 {
			return httputil.Status(400, noTracks)
		}
	}
	feed, err := h.createFeed(ctx, u.ID, entityType, id, src, serverAddress, slug, req.options())
	if err != nil {
		return httputil.Internal("Failed to open RSS feed", err)
	}
	minified := feed.minified()
	logging.Infof("[RssFeedManager] Opened RSS feed %q", feedURLText(minified)) // RssFeedManager.js:301
	if h.emit != nil {
		h.emit("*", "rss_feed_open", minified)
	}
	write(w, wire.FeedOpened{Feed: minified})
	return nil
}

func feedURLText(f wire.FeedMinified) string {
	if f.FeedURL == nil {
		return ""
	}
	return *f.FeedURL
}

func feedEntityError(err error) error {
	if errors.Is(err, errFeedEntityMissing) {
		return httputil.Status(404, "")
	}
	return &httputil.Error{Status: 500, Cause: err}
}

// slugInUse is the 400 an open request gets when a feed already has the slug.
func (h *Handler) slugInUse(ctx context.Context, slug string) error {
	count, err := h.queries().CountFeedsBySlug(ctx, slug)
	if err != nil {
		return &httputil.Error{Status: 500, Cause: err}
	}
	if count > 0 {
		return httputil.Status(400, "Slug already in use")
	}
	return nil
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

func nullBoolInt(v sql.NullBool) sql.NullInt64 {
	return sql.NullInt64{Int64: boolInt64(v.Bool), Valid: v.Valid}
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

func (f feedRow) insertParams() sqlc.InsertFeedParams {
	return sqlc.InsertFeedParams{
		ID: f.ID, Slug: f.Slug, EntityType: f.EntityType, EntityID: f.EntityID, Entityupdatedat: dbtime.Format(f.EntityUpdatedAt),
		ServerAddress: f.ServerAddress, FeedUrl: f.FeedURL, ImageUrl: f.ImageURL, SiteUrl: f.SiteURL, Title: f.Title,
		Description: f.Description, Author: f.Author, PodcastType: f.PodcastType, Language: f.Language,
		OwnerName: f.OwnerName, OwnerEmail: f.OwnerEmail, Explicit: nullBoolInt(f.Explicit), PreventIndexing: nullBoolInt(f.PreventIndexing),
		CoverPath: f.CoverPath, UserID: f.UserID, Createdat: dbtime.Format(f.CreatedAt), Updatedat: dbtime.Format(f.UpdatedAt),
	}
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

// params is the feedEpisodes upsert for this episode in feedID, with createdAt
// and updatedAt both set to at (a new row; an existing row keeps its own).
func (e feedEpisodeRow) params(feedID, at string) sqlc.UpsertFeedEpisodeParams {
	return sqlc.UpsertFeedEpisodeParams{
		ID: e.ID, Title: e.Title, Author: e.Author, Description: e.Description, SiteUrl: e.SiteURL,
		EnclosureUrl: e.EnclosureURL, EnclosureType: e.EnclosureType, EnclosureSize: e.EnclosureSize, PubDate: e.PubDate,
		Season: e.Season, Episode: e.Episode, EpisodeType: e.EpisodeType, Duration: e.Duration, FilePath: e.FilePath,
		Explicit: nullBoolInt(e.Explicit), Createdat: at, Updatedat: at, FeedID: feedID,
	}
}

// createFeed is Feed.createFeedFor* (server/models/Feed.js:157): the feed row
// and its episode rows in one transaction.
func (h *Handler) createFeed(ctx context.Context, userID, entityType, entityID string, src *feedSource, serverAddress, slug string, opts feedOptions) (feedRow, error) {
	id, err := ids.New()
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
	q := sqlc.New(tx)
	if err = q.InsertFeed(ctx, feed.insertParams()); err != nil {
		return feedRow{}, err
	}
	for _, ep := range src.Episodes {
		epID, err := ids.New()
		if err != nil {
			return feedRow{}, err
		}
		if err = q.UpsertFeedEpisode(ctx, episodeRow(epID, feed, ep).params(id, dbtime.Format(now))); err != nil {
			return feedRow{}, err
		}
	}
	return feed, tx.Commit()
}

// closeFeed is RSSFeedController.closeRSSFeed and RssFeedManager.handleCloseFeed.
func (h *Handler) closeFeed(w http.ResponseWriter, r *http.Request, u store.User) error {
	if !access.AdminUser(u) {
		return httputil.Status(403, "")
	}
	id := r.PathValue("id")
	feed, err := feedResult(h.queries().GetFeed(r.Context(), id))
	if errors.Is(err, sql.ErrNoRows) {
		return httputil.Status(404, "")
	}
	if err != nil {
		return &httputil.Error{Status: 500, Cause: err}
	}
	if err = h.queries().DeleteFeed(r.Context(), id); err != nil {
		return &httputil.Error{Status: 500, Cause: err}
	}
	logging.Infof("[RssFeedManager] Closed RSS feed %q", feedURLText(feed.minified())) // RssFeedManager.js:359
	if h.emit != nil {
		h.emit("*", "rss_feed_closed", feed.minified())
	}
	return httputil.OK(w, r)
}

// publicFeed is GET /feed/:slug, /feed/:slug/item/:episodeId/* and
// /feed/:slug/cover* (RssFeedManager.getFeed, getFeedItem, getFeedCover).
func (h *Handler) publicFeed(w http.ResponseWriter, r *http.Request) error {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	slug := r.PathValue("slug")
	if len(parts) >= 4 && parts[2] == "item" {
		h.feedItem(w, r, slug, parts[3])
		return nil
	}
	if len(parts) >= 3 && strings.HasPrefix(parts[2], "cover") {
		h.feedCover(w, r, slug)
		return nil
	}
	if len(parts) != 2 {
		return httputil.Status(404, "")
	}
	ctx := r.Context()
	feed, err := h.feedBySlug(ctx, slug)
	if errors.Is(err, sql.ErrNoRows) {
		return httputil.Status(404, "")
	}
	var eps []feedEpisodeRow
	if err == nil {
		eps, err = h.feedEpisodeRows(ctx, feed.ID)
	}
	if err == nil {
		feed, eps, err = h.refreshFeedIfStale(ctx, feed, eps)
	}
	if err != nil {
		return &httputil.Error{Status: 500, Cause: err}
	}
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	_, _ = io.WriteString(w, feedXML(feed, eps, hostPrefix(r, h.cfg.RouterBasePath), time.Now()))
	return nil
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
	q := sqlc.New(tx)
	if err = q.UpdateFeedFromEntity(ctx, sqlc.UpdateFeedFromEntityParams{
		Entityupdatedat: dbtime.Format(next.EntityUpdatedAt), ImageUrl: next.ImageURL, SiteUrl: next.SiteURL, Title: next.Title,
		Description: next.Description, Author: next.Author, PodcastType: next.PodcastType, Language: next.Language,
		Explicit: nullBoolInt(next.Explicit), CoverPath: next.CoverPath, Updatedat: dbtime.Format(next.UpdatedAt), ID: feed.ID,
	}); err != nil {
		return feed, eps, err
	}
	kept := map[string]bool{}
	fresh := make([]feedEpisodeRow, 0, len(src.Episodes))
	for _, ep := range src.Episodes {
		epID := byPath[ep.FilePath.String]
		if epID == "" {
			if epID, err = ids.New(); err != nil {
				return feed, eps, err
			}
		}
		row := episodeRow(epID, next, ep)
		kept[epID] = true
		fresh = append(fresh, row)
		// bulkCreate with updateOnDuplicate leaves createdAt and updatedAt of
		// existing rows alone (server/models/FeedEpisode.js updateOnDuplicate list).
		if err = q.UpsertFeedEpisode(ctx, row.params(feed.ID, dbtime.Format(now))); err != nil {
			return feed, eps, err
		}
	}
	for _, e := range eps {
		if kept[e.ID] {
			continue
		}
		if err = q.DeleteFeedEpisode(ctx, e.ID); err != nil {
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

func (h *Handler) feedItem(w http.ResponseWriter, r *http.Request, slug, episodeID string) error {
	path, err := h.queries().GetFeedEpisodeFilePath(r.Context(), sqlc.GetFeedEpisodeFilePathParams{Slug: slug, EpisodeID: episodeID})
	if err != nil || path.String == "" {
		return httputil.Status(404, "")
	}
	serveMedia(w, r, path.String)
	return nil
}

// feedCover streams the cover with Content-Type image/<extension> like
// RssFeedManager.getFeedCover (res.type + createReadStream, no ranges).
func (h *Handler) feedCover(w http.ResponseWriter, r *http.Request, slug string) error {
	path, err := h.queries().GetFeedCoverPathBySlug(r.Context(), slug)
	if err != nil || path.String == "" {
		return httputil.Status(404, "")
	}
	f, err := os.Open(path.String)
	if err != nil {
		return httputil.Status(404, "")
	}
	defer f.Close()
	w.Header().Set("Content-Type", "image/"+strings.ToLower(strings.TrimPrefix(nodeExt(path.String), ".")))
	_, _ = io.Copy(w, f)
	return nil
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
