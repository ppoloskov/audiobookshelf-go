// Package sharing implements the pinned RSS feed and public media-share routes.
package sharing

import (
	"archive/zip"
	"audiobookshelf-compat/internal/dbtime"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
)

type Handler struct {
	db       *sql.DB
	cfg      config.Config
	emit     func(string, string, any)
	mu       sync.Mutex
	sessions map[string]*shareSession
}
type shareSession struct {
	ID          string
	ShareID     string
	Slug        string
	ItemID      string
	LibraryID   string
	Title       string
	Author      string
	CoverPath   string
	Duration    float64
	Current     float64
	Start       float64
	Updated     int64
	Tracks      []map[string]any
	Metadata    map[string]any
	Chapters    []any
	CoverAspect any
}

func New(db *sql.DB, cfg config.Config, emit func(string, string, any)) *Handler {
	return &Handler{db: db, cfg: cfg, emit: emit, sessions: map[string]*shareSession{}}
}
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request, u store.User) bool {
	p := r.URL.Path
	switch {
	case p == "/api/feeds" && r.Method == http.MethodGet:
		h.listFeeds(w, r, u)
	case strings.HasPrefix(p, "/api/feeds/item/") && strings.HasSuffix(p, "/open") && r.Method == http.MethodPost:
		h.openFeed(w, r, u, "libraryItem")
	case strings.HasPrefix(p, "/api/feeds/collection/") && strings.HasSuffix(p, "/open") && r.Method == http.MethodPost:
		h.openFeed(w, r, u, "collection")
	case strings.HasPrefix(p, "/api/feeds/series/") && strings.HasSuffix(p, "/open") && r.Method == http.MethodPost:
		h.openFeed(w, r, u, "series")
	case strings.HasPrefix(p, "/api/feeds/") && strings.HasSuffix(p, "/close") && r.Method == http.MethodPost:
		h.closeFeed(w, r, u)
	case p == "/api/share/mediaitem" && r.Method == http.MethodPost:
		h.createShare(w, r, u)
	case strings.HasPrefix(p, "/api/share/mediaitem/") && r.Method == http.MethodDelete:
		h.deleteShare(w, r, u)
	case strings.HasPrefix(p, "/feed/") && r.Method == http.MethodGet:
		h.publicFeed(w, r)
	case strings.HasPrefix(p, "/public/share/") && r.Method == http.MethodGet:
		if len(strings.Split(strings.Trim(p, "/"), "/")) == 3 {
			h.publicShare(w, r)
		} else {
			h.publicShareFile(w, r)
		}
	case strings.HasPrefix(p, "/public/share/") && r.Method == http.MethodPatch:
		h.shareProgress(w, r)
	default:
		return false
	}
	return true
}
func admin(u store.User) bool { return u.Type == "root" || u.Type == "admin" }
func (h *Handler) listFeeds(w http.ResponseWriter, r *http.Request, u store.User) {
	if !admin(u) {
		w.WriteHeader(403)
		return
	}
	rows, e := h.db.QueryContext(r.Context(), `SELECT id,slug,entityType,entityId,entityUpdatedAt,serverAddress,feedURL,imageURL,siteURL,title,description,author,podcastType,language,ownerName,ownerEmail,explicit,preventIndexing,coverPath,userId,createdAt,updatedAt FROM feeds`)
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	snapshots := []map[string]any{}
	for rows.Next() {
		f, e := scanFeed(rows)
		if e != nil {
			_ = rows.Close()
			http.Error(w, "database error", 500)
			return
		}
		snapshots = append(snapshots, f)
	}
	if e = rows.Err(); e != nil {
		_ = rows.Close()
		http.Error(w, "database error", 500)
		return
	}
	_ = rows.Close()
	feeds := []map[string]any{}
	mini := []map[string]any{}
	for _, f := range snapshots {
		eps, e := h.feedEpisodes(r.Context(), f["id"].(string))
		if e != nil {
			http.Error(w, "database error", 500)
			return
		}
		f["episodes"] = eps
		feeds = append(feeds, oldFeed(f))
		mini = append(mini, minifiedFeed(f))
	}
	write(w, map[string]any{"feeds": feeds, "minified": mini})
}
func (h *Handler) openFeed(w http.ResponseWriter, r *http.Request, u store.User, entityType string) {
	if !admin(u) {
		w.WriteHeader(403)
		return
	}
	id := routeID(r.URL.Path, "open")
	var p map[string]any
	if json.NewDecoder(r.Body).Decode(&p) != nil {
		p = map[string]any{}
	}
	serverAddress, _ := p["serverAddress"].(string)
	slug, _ := p["slug"].(string)
	if serverAddress == "" || slug == "" {
		http.Error(w, "Invalid request body", 400)
		return
	}
	var entity map[string]any
	var err error
	switch entityType {
	case "libraryItem":
		entity, err = h.itemInfo(r.Context(), id)
	case "collection":
		entity, err = h.collectionInfo(r.Context(), id)
	case "series":
		entity, err = h.seriesInfo(r.Context(), id)
	}
	if errors.Is(err, sql.ErrNoRows) {
		w.WriteHeader(404)
		return
	}
	if err != nil {
		http.Error(w, "Internal server error", 500)
		return
	}
	if entityType == "libraryItem" {
		if !libraryAccess(u, entity["libraryId"].(string)) {
			w.WriteHeader(403)
			return
		}
		if len(entity["episodes"].([]map[string]any)) == 0 {
			http.Error(w, "Item has no audio tracks", 400)
			return
		}
	} else if len(entity["episodes"].([]map[string]any)) == 0 {
		msg := "Collection has no audio tracks"
		if entityType == "series" {
			msg = "Series has no audio tracks"
		}
		http.Error(w, msg, 400)
		return
	}
	var count int
	if e := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM feeds WHERE slug=?`, slug).Scan(&count); e != nil {
		http.Error(w, "database error", 500)
		return
	}
	if count > 0 {
		http.Error(w, "Slug already in use", 400)
		return
	}
	now := time.Now().UTC()
	feedID, _ := uuid()
	title := stringValue(entity["title"])
	author := stringValue(entity["author"])
	desc := stringValue(entity["description"])
	cover := stringValue(entity["coverPath"])
	updated := entity["updatedAt"]
	imageURL := "/Logo.png"
	if cover != "" {
		imageURL = "/feed/" + slug + "/cover" + filepath.Ext(cover)
		if updated != nil {
			imageURL += "?ts=" + fmt.Sprint(millis(updated))
		}
	}
	feedURL := "/feed/" + slug
	siteURL := stringValue(entity["siteURL"])
	podcastType := stringValue(entity["podcastType"])
	if podcastType == "" {
		podcastType = "serial"
	}
	lang := stringValue(entity["language"])
	if lang == "" {
		lang = "en"
	}
	prevent := true
	if details, ok := p["metadataDetails"].(map[string]any); ok {
		if details["preventIndexing"] == false {
			prevent = false
		}
	}
	tx, e := h.db.BeginTx(r.Context(), nil)
	if e != nil {
		http.Error(w, "Internal server error", 500)
		return
	}
	_, e = tx.ExecContext(r.Context(), `INSERT INTO feeds(id,slug,entityType,entityId,entityUpdatedAt,serverAddress,feedURL,imageURL,siteURL,title,description,author,podcastType,language,ownerName,ownerEmail,explicit,preventIndexing,coverPath,createdAt,updatedAt,userId) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, feedID, slug, entityType, id, updated, serverAddress, feedURL, imageURL, siteURL, title, desc, author, podcastType, lang, nullableString(detailsValue(p, "ownerName")), nullableString(detailsValue(p, "ownerEmail")), boolInt(entity["explicit"]), boolInt(prevent), nullableString(cover), sqlDate(now), sqlDate(now), u.ID)
	if e == nil {
		for i, ep := range entity["episodes"].([]map[string]any) {
			episodeID, _ := uuid()
			ep["id"] = episodeID
			ep["feedId"] = feedID
			ep["author"] = author
			ep["siteURL"] = siteURL
			if stringValue(ep["pubDate"]) == "" {
				ep["pubDate"] = now.Add(time.Duration(i) * time.Minute).Format("Mon, 02 Jan 2006 15:04:05 GMT")
			}
			if stringValue(ep["enclosureType"]) == "" {
				ep["enclosureType"] = mimeForPath(stringValue(ep["filePath"]))
			}
			ep["enclosureURL"] = feedURL + "/item/" + episodeID + "/media" + filepath.Ext(stringValue(ep["filePath"]))
			_, e = tx.ExecContext(r.Context(), `INSERT INTO feedEpisodes(id,title,author,description,siteURL,enclosureURL,enclosureType,enclosureSize,pubDate,season,episode,episodeType,duration,filePath,explicit,createdAt,updatedAt,feedId) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, episodeID, ep["title"], author, ep["description"], siteURL, ep["enclosureURL"], ep["enclosureType"], ep["enclosureSize"], ep["pubDate"], ep["season"], ep["episode"], ep["episodeType"], ep["duration"], ep["filePath"], ep["explicit"], sqlDate(now), sqlDate(now), feedID)
			if e != nil {
				break
			}
		}
	}
	if e != nil {
		_ = tx.Rollback()
		http.Error(w, "Failed to open RSS feed", 500)
		return
	}
	if e = tx.Commit(); e != nil {
		http.Error(w, "Failed to open RSS feed", 500)
		return
	}
	feed := map[string]any{"id": feedID, "entityType": entityType, "entityId": id, "feedUrl": feedURL, "meta": map[string]any{"title": title, "description": desc, "preventIndexing": prevent, "ownerName": nullableString(detailsValue(p, "ownerName")), "ownerEmail": nullableString(detailsValue(p, "ownerEmail"))}}
	if h.emit != nil {
		h.emit("*", "rss_feed_open", feed)
	}
	write(w, map[string]any{"feed": feed})
}
func (h *Handler) closeFeed(w http.ResponseWriter, r *http.Request, u store.User) {
	if !admin(u) {
		w.WriteHeader(403)
		return
	}
	id := routeID(r.URL.Path, "close")
	var f map[string]any
	row := h.db.QueryRowContext(r.Context(), `SELECT id,entityType,entityId,feedURL,title,description,preventIndexing,ownerName,ownerEmail FROM feeds WHERE id=?`, id)
	var fid, typ, entity, feedURL, title, desc sql.NullString
	var prevent int
	var owner, ownerEmail sql.NullString
	if e := row.Scan(&fid, &typ, &entity, &feedURL, &title, &desc, &prevent, &owner, &ownerEmail); e != nil {
		w.WriteHeader(404)
		return
	}
	f = map[string]any{"id": fid.String, "entityType": typ.String, "entityId": entity.String, "feedUrl": feedURL.String, "meta": map[string]any{"title": title.String, "description": desc.String, "preventIndexing": prevent != 0, "ownerName": nullable(owner), "ownerEmail": nullable(ownerEmail)}}
	_, e := h.db.ExecContext(r.Context(), `DELETE FROM feeds WHERE id=?`, id)
	if e != nil {
		http.Error(w, "Internal server error", 500)
		return
	}
	if h.emit != nil {
		h.emit("*", "rss_feed_closed", f)
	}
	w.WriteHeader(200)
}

func (h *Handler) itemInfo(ctx context.Context, id string) (map[string]any, error) {
	var libraryID, mediaID, mediaType, title, authorNames, relPath, itemPath, cover, description, language, author, podcastType sql.NullString
	var updated, created any
	var explicit int
	var chapters, tags, genres, audio []byte
	var duration sql.NullFloat64
	err := h.db.QueryRowContext(ctx, `SELECT li.libraryId,li.mediaId,li.mediaType,li.path,li.relPath,li.updatedAt,li.createdAt,li.authorNamesFirstLast FROM libraryItems li WHERE li.id=?`, id).Scan(&libraryID, &mediaID, &mediaType, &itemPath, &relPath, &updated, &created, &authorNames)
	if err != nil {
		return nil, err
	}
	entity := map[string]any{"id": id, "libraryId": libraryID.String, "updatedAt": updated, "createdAt": created, "siteURL": "/item/" + id}
	out := []map[string]any{}
	if mediaType.String == "book" {
		err = h.db.QueryRowContext(ctx, `SELECT title,description,coverPath,language,explicit,duration,chapters,audioFiles,tags,genres FROM books WHERE id=?`, mediaID.String).Scan(&title, &description, &cover, &language, &explicit, &duration, &chapters, &audio, &tags, &genres)
		if err != nil {
			return nil, err
		}
		var mediaUpdated any
		if e := h.db.QueryRowContext(ctx, `SELECT updatedAt FROM books WHERE id=?`, mediaID.String).Scan(&mediaUpdated); e == nil && parseTime(mediaUpdated).After(parseTime(updated)) {
			updated = mediaUpdated
		}
		out = bookEpisodes(id, title.String, authorNames.String, description.String, created, audio, chapters, explicit)
		entity["title"], entity["author"], entity["description"], entity["coverPath"], entity["language"], entity["explicit"], entity["podcastType"], entity["episodes"] = title.String, authorNames.String, description.String, cover.String, language.String, explicit != 0, "serial", out
	} else if mediaType.String == "podcast" {
		err = h.db.QueryRowContext(ctx, `SELECT title,author,description,coverPath,language,podcastType,explicit FROM podcasts WHERE id=?`, mediaID.String).Scan(&title, &author, &description, &cover, &language, &podcastType, &explicit)
		if err != nil {
			return nil, err
		}
		rows, e := h.db.QueryContext(ctx, `SELECT id,title,description,siteURL,enclosureURL,enclosureType,enclosureSize,pubDate,season,episode,episodeType,duration,audioFile,explicit FROM podcastEpisodes WHERE podcastId=? ORDER BY publishedAt`, mediaID.String)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var epID, epTitle, epDesc, site, enclosure, mimeType, pub, season, episode, typ sql.NullString
			var size sql.NullInt64
			var dur sql.NullFloat64
			var af []byte
			var exp int
			if rows.Scan(&epID, &epTitle, &epDesc, &site, &enclosure, &mimeType, &size, &pub, &season, &episode, &typ, &dur, &af, &exp) != nil {
				continue
			}
			var file struct {
				Metadata map[string]any `json:"metadata"`
			}
			_ = json.Unmarshal(af, &file)
			path := stringValue(file.Metadata["path"])
			filename := stringValue(file.Metadata["filename"])
			if path == "" {
				continue
			}
			fileSize := file.Metadata["size"]
			fileMime := mimeType.String
			if fileMime == "" {
				fileMime = mimeForPath(path)
			}
			out = append(out, map[string]any{"id": epID.String, "title": epTitle.String, "author": author.String, "description": epDesc.String, "siteURL": site.String, "enclosureType": fileMime, "enclosureSize": fileSize, "pubDate": pub.String, "season": season.String, "episode": episode.String, "episodeType": typ.String, "duration": dur.Float64, "filePath": path, "metadata": file.Metadata, "filename": filename, "explicit": exp != 0})
		}
		rows.Close()
		var latestEpisodeUpdated any
		if e := h.db.QueryRowContext(ctx, `SELECT updatedAt FROM podcastEpisodes WHERE podcastId=? ORDER BY updatedAt DESC LIMIT 1`, mediaID.String).Scan(&latestEpisodeUpdated); e == nil && parseTime(latestEpisodeUpdated).After(parseTime(updated)) {
			updated = latestEpisodeUpdated
		}
		if podcastType.String == "episodic" {
			sort.SliceStable(out, func(i, j int) bool { return stringValue(out[i]["pubDate"]) > stringValue(out[j]["pubDate"]) })
		}
		entity["title"], entity["author"], entity["description"], entity["coverPath"], entity["language"], entity["explicit"], entity["podcastType"], entity["episodes"] = title.String, author.String, description.String, cover.String, language.String, explicit != 0, podcastType.String, out
	} else {
		return nil, sql.ErrNoRows
	}
	entity["libraryItemPath"], entity["relPath"], entity["mediaType"] = itemPath.String, relPath.String, mediaType.String
	return entity, nil
}
func bookEpisodes(itemID, title, author, description string, created any, audioRaw, chaptersRaw []byte, explicit int) []map[string]any {
	files := decodeObjects(audioRaw)
	tracks := []map[string]any{}
	included := 0
	for _, f := range files {
		if f["exclude"] != true {
			included++
		}
	}
	offset := 0.0
	for _, f := range files {
		if f["exclude"] == true {
			continue
		}
		meta, _ := f["metadata"].(map[string]any)
		path := stringValue(meta["path"])
		filename := stringValue(meta["filename"])
		if path == "" {
			continue
		}
		dur := floatValue(f["duration"])
		trackTitle := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
		if included == 1 {
			trackTitle = title
		}
		tracks = append(tracks, map[string]any{"title": trackTitle, "author": author, "description": description, "duration": dur, "filePath": path, "filename": filename, "enclosureType": stringValue(f["mimeType"]), "enclosureSize": meta["size"], "explicit": explicit != 0, "startOffset": offset})
		offset += dur
	}
	var chapters []map[string]any
	_ = json.Unmarshal(chaptersRaw, &chapters)
	if len(tracks) == len(chapters) && len(tracks) > 1 {
		for i, ch := range chapters {
			if abs(floatValue(ch["start"])-floatValue(tracks[i]["startOffset"])) < 1 && stringValue(ch["title"]) != "" {
				tracks[i]["title"] = ch["title"]
			}
		}
	}
	base := parseTime(created)
	for i, t := range tracks {
		t["pubDate"] = base.Add(time.Duration(i) * time.Minute).UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT")
	}
	return tracks
}
func (h *Handler) collectionInfo(ctx context.Context, id string) (map[string]any, error) {
	var name, desc sql.NullString
	var updated any
	if e := h.db.QueryRowContext(ctx, `SELECT name,description,updatedAt FROM collections WHERE id=?`, id).Scan(&name, &desc, &updated); e != nil {
		return nil, e
	}
	rows, e := h.db.QueryContext(ctx, `SELECT b.id,b.title,b.description,b.coverPath,b.explicit,b.duration,b.chapters,b.audioFiles,b.createdAt,b.updatedAt,li.id,li.libraryId,li.updatedAt,li.authorNamesFirstLast FROM collectionBooks cb JOIN books b ON b.id=cb.bookId JOIN libraryItems li ON li.mediaId=b.id AND li.mediaType='book' WHERE cb.collectionId=? ORDER BY cb."order"`, id)
	if e != nil {
		return nil, e
	}
	return h.aggregateBookEntity(ctx, id, "collection", name.String, desc.String, updated, rows)
}
func (h *Handler) seriesInfo(ctx context.Context, id string) (map[string]any, error) {
	var name, desc sql.NullString
	var updated any
	if e := h.db.QueryRowContext(ctx, `SELECT name,description,updatedAt FROM series WHERE id=?`, id).Scan(&name, &desc, &updated); e != nil {
		return nil, e
	}
	rows, e := h.db.QueryContext(ctx, `SELECT b.id,b.title,b.description,b.coverPath,b.explicit,b.duration,b.chapters,b.audioFiles,b.createdAt,b.updatedAt,li.id,li.libraryId,li.updatedAt,li.authorNamesFirstLast FROM bookSeries bs JOIN books b ON b.id=bs.bookId JOIN libraryItems li ON li.mediaId=b.id AND li.mediaType='book' WHERE bs.seriesId=? ORDER BY bs.sequence`, id)
	if e != nil {
		return nil, e
	}
	return h.aggregateBookEntity(ctx, id, "series", name.String, desc.String, updated, rows)
}
func (h *Handler) aggregateBookEntity(ctx context.Context, id, typ, name, desc string, updated any, rows *sql.Rows) (map[string]any, error) {
	defer rows.Close()
	books := []map[string]any{}
	tracks := []map[string]any{}
	authors := []string{}
	cover := ""
	explicit := false
	firstLibrary := ""
	var latest time.Time
	earliest := time.Now()
	for rows.Next() {
		var bid, title, bdesc, bcover, liID, libraryID, author sql.NullString
		var exp int
		var dur sql.NullFloat64
		var chapters, audio []byte
		var created, bookUpdated, itemUpdated any
		if e := rows.Scan(&bid, &title, &bdesc, &bcover, &exp, &dur, &chapters, &audio, &created, &bookUpdated, &liID, &libraryID, &itemUpdated, &author); e != nil {
			return nil, e
		}
		ep := bookEpisodes(liID.String, title.String, author.String, bdesc.String, created, audio, chapters, exp)
		if len(ep) == 0 {
			continue
		}
		books = append(books, map[string]any{"id": bid.String, "author": author.String, "episodes": ep, "createdAt": created, "updatedAt": itemUpdated})
		tracks = append(tracks, ep...)
		if cover == "" && bcover.Valid {
			cover = bcover.String
		}
		if exp != 0 {
			explicit = true
		}
		if firstLibrary == "" {
			firstLibrary = libraryID.String
		}
		for _, a := range strings.Split(author.String, ", ") {
			a = strings.TrimSpace(a)
			if a != "" && !contains(authors, a) {
				authors = append(authors, a)
			}
		}
		if t := parseTime(created); !t.IsZero() && t.Before(earliest) {
			earliest = t
		}
		if t := parseTime(itemUpdated); t.After(latest) {
			latest = t
		}
		if t := parseTime(bookUpdated); t.After(latest) {
			latest = t
		}
	}
	if e := rows.Err(); e != nil {
		return nil, e
	}
	if !earliest.IsZero() {
		for i, ep := range tracks {
			ep["pubDate"] = earliest.Add(time.Duration(i) * time.Minute).UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT")
		}
	}
	if earliest.After(latest) {
		latest = earliest
	}
	if t := parseTime(updated); t.After(latest) {
		latest = t
	}
	author := ""
	for i, a := range authors {
		if i == 3 {
			author += " & more"
			break
		}
		if i > 0 {
			author += ", "
		}
		author += a
	}

	site := "/collection/" + id
	if typ == "series" {
		site = "/library/" + firstLibrary + "/series/" + id
	}
	return map[string]any{"title": name, "author": author, "description": desc, "coverPath": cover, "explicit": explicit, "podcastType": "serial", "updatedAt": latest, "siteURL": site, "episodes": tracks, "books": books}, nil
}

func (h *Handler) feedEpisodes(ctx context.Context, id string) ([]map[string]any, error) {
	rows, e := h.db.QueryContext(ctx, `SELECT id,title,author,description,siteURL,enclosureURL,enclosureType,enclosureSize,pubDate,season,episode,episodeType,duration,filePath,explicit FROM feedEpisodes WHERE feedId=?`, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, title, author, desc, site, enclosure, mimeType, pub, season, episode, typ, path sql.NullString
		var size sql.NullInt64
		var duration sql.NullFloat64
		var explicit int
		if e := rows.Scan(&id, &title, &author, &desc, &site, &enclosure, &mimeType, &size, &pub, &season, &episode, &typ, &duration, &path, &explicit); e != nil {
			return nil, e
		}
		out = append(out, map[string]any{"id": id.String, "title": title.String, "author": author.String, "description": desc.String, "siteURL": site.String, "enclosureURL": enclosure.String, "enclosureType": mimeType.String, "enclosureSize": size.Int64, "pubDate": pub.String, "season": season.String, "episode": episode.String, "episodeType": typ.String, "duration": duration.Float64, "filePath": path.String, "explicit": explicit != 0})
	}
	return out, rows.Err()
}
func scanFeed(row interface{ Scan(...any) error }) (map[string]any, error) {
	var id, slug, typ, entity, server, feedURL, image, site, title, desc, author, podcastType, language, owner, ownerEmail, cover, user sql.NullString
	var entityUpdated, created, updated any
	var explicit, prevent int
	e := row.Scan(&id, &slug, &typ, &entity, &entityUpdated, &server, &feedURL, &image, &site, &title, &desc, &author, &podcastType, &language, &owner, &ownerEmail, &explicit, &prevent, &cover, &user, &created, &updated)
	if e != nil {
		return nil, e
	}
	return map[string]any{"id": id.String, "slug": slug.String, "entityType": typ.String, "entityId": entity.String, "entityUpdatedAt": millis(entityUpdated), "serverAddress": server.String, "feedURL": feedURL.String, "imageURL": image.String, "siteURL": site.String, "title": title.String, "description": desc.String, "author": author.String, "podcastType": podcastType.String, "language": language.String, "ownerName": nullable(owner), "ownerEmail": nullable(ownerEmail), "explicit": explicit != 0, "preventIndexing": prevent != 0, "coverPath": nullable(cover), "userId": user.String, "createdAt": millis(created), "updatedAt": millis(updated)}, nil
}
func oldFeed(f map[string]any) map[string]any {
	eps := []any{}
	for _, ep := range f["episodes"].([]map[string]any) {
		eps = append(eps, map[string]any{"id": ep["id"], "title": ep["title"], "description": ep["description"], "enclosure": map[string]any{"url": ep["enclosureURL"], "size": ep["enclosureSize"], "type": ep["enclosureType"]}, "pubDate": ep["pubDate"], "link": ep["siteURL"], "author": ep["author"], "explicit": ep["explicit"], "duration": ep["duration"], "season": ep["season"], "episode": ep["episode"], "episodeType": ep["episodeType"], "fullPath": ep["filePath"]})
	}
	return map[string]any{"id": f["id"], "slug": f["slug"], "userId": f["userId"], "entityType": f["entityType"], "entityId": f["entityId"], "entityUpdatedAt": f["entityUpdatedAt"], "coverPath": f["coverPath"], "meta": metaFeed(f), "serverAddress": f["serverAddress"], "feedUrl": f["feedURL"], "episodes": eps, "createdAt": f["createdAt"], "updatedAt": f["updatedAt"]}
}
func minifiedFeed(f map[string]any) map[string]any {
	return map[string]any{"id": f["id"], "entityType": f["entityType"], "entityId": f["entityId"], "feedUrl": f["feedURL"], "meta": map[string]any{"title": f["title"], "description": f["description"], "preventIndexing": f["preventIndexing"], "ownerName": f["ownerName"], "ownerEmail": f["ownerEmail"]}}
}
func metaFeed(f map[string]any) map[string]any {
	return map[string]any{"title": f["title"], "description": f["description"], "author": f["author"], "imageUrl": f["imageURL"], "feedUrl": f["feedURL"], "link": f["siteURL"], "explicit": f["explicit"], "type": f["podcastType"], "language": f["language"], "preventIndexing": f["preventIndexing"], "ownerName": f["ownerName"], "ownerEmail": f["ownerEmail"]}
}
func (h *Handler) publicFeed(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 {
		w.WriteHeader(404)
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
		w.WriteHeader(404)
		return
	}
	row := h.db.QueryRowContext(r.Context(), `SELECT id,slug,entityType,entityId,entityUpdatedAt,serverAddress,feedURL,imageURL,siteURL,title,description,author,podcastType,language,ownerName,ownerEmail,explicit,preventIndexing,coverPath,userId,createdAt,updatedAt FROM feeds WHERE slug=?`, slug)
	f, e := scanFeed(row)
	if e != nil {
		w.WriteHeader(404)
		return
	}
	if e := h.refreshFeedIfStale(r.Context(), f); e != nil {
		http.Error(w, "Internal server error", 500)
		return
	}
	row = h.db.QueryRowContext(r.Context(), `SELECT id,slug,entityType,entityId,entityUpdatedAt,serverAddress,feedURL,imageURL,siteURL,title,description,author,podcastType,language,ownerName,ownerEmail,explicit,preventIndexing,coverPath,userId,createdAt,updatedAt FROM feeds WHERE slug=?`, slug)
	f, e = scanFeed(row)
	if e != nil {
		w.WriteHeader(404)
		return
	}
	eps, e := h.feedEpisodes(r.Context(), f["id"].(string))
	if e != nil {
		http.Error(w, "Internal server error", 500)
		return
	}
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	_, _ = io.WriteString(w, feedXML(f, eps, hostPrefix(r, h.cfg.RouterBasePath)))
}

// refreshFeedIfStale mirrors RssFeedManager.checkFeedRequiresUpdate followed by
// Feed.updateFeedForEntity. Episode IDs are retained when their underlying file
// remains in the entity, so subscribed feed URLs keep resolving after metadata changes.
func (h *Handler) refreshFeedIfStale(ctx context.Context, f map[string]any) error {
	var entity map[string]any
	var err error
	switch f["entityType"] {
	case "libraryItem":
		entity, err = h.itemInfo(ctx, stringValue(f["entityId"]))
	case "collection":
		entity, err = h.collectionInfo(ctx, stringValue(f["entityId"]))
	case "series":
		entity, err = h.seriesInfo(ctx, stringValue(f["entityId"]))
	default:
		return errors.New("invalid feed entity type")
	}
	if err != nil {
		return err
	}
	oldUpdated := parseTime(f["entityUpdatedAt"])
	newUpdated := parseTime(entity["updatedAt"])
	oldEpisodes, err := h.feedEpisodes(ctx, stringValue(f["id"]))
	if err != nil {
		return err
	}
	want := entity["episodes"].([]map[string]any)
	stale := newUpdated.After(oldUpdated) || len(oldEpisodes) != len(want)
	if !stale {
		return nil
	}
	byPath := make(map[string]string, len(oldEpisodes))
	for _, ep := range oldEpisodes {
		byPath[stringValue(ep["filePath"])] = stringValue(ep["id"])
	}
	now := time.Now().UTC()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	feedID, slug := stringValue(f["id"]), stringValue(f["slug"])
	for i, ep := range want {
		path := stringValue(ep["filePath"])
		id := byPath[path]
		if id == "" {
			id, err = uuid()
			if err != nil {
				return err
			}
		}
		ep["id"], ep["feedId"] = id, feedID
		ep["author"], ep["siteURL"] = entity["author"], entity["siteURL"]
		if stringValue(ep["pubDate"]) == "" {
			ep["pubDate"] = now.Add(time.Duration(i) * time.Minute).Format("Mon, 02 Jan 2006 15:04:05 GMT")
		}
		if stringValue(ep["enclosureType"]) == "" {
			ep["enclosureType"] = mimeForPath(path)
		}
		ep["enclosureURL"] = "/feed/" + slug + "/item/" + id + "/media" + filepath.Ext(path)
		_, err = tx.ExecContext(ctx, `INSERT INTO feedEpisodes(id,title,author,description,siteURL,enclosureURL,enclosureType,enclosureSize,pubDate,season,episode,episodeType,duration,filePath,explicit,createdAt,updatedAt,feedId) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET title=excluded.title,author=excluded.author,description=excluded.description,siteURL=excluded.siteURL,enclosureURL=excluded.enclosureURL,enclosureType=excluded.enclosureType,enclosureSize=excluded.enclosureSize,pubDate=excluded.pubDate,season=excluded.season,episode=excluded.episode,episodeType=excluded.episodeType,duration=excluded.duration,filePath=excluded.filePath,explicit=excluded.explicit,updatedAt=excluded.updatedAt`, id, ep["title"], ep["author"], ep["description"], ep["siteURL"], ep["enclosureURL"], ep["enclosureType"], ep["enclosureSize"], ep["pubDate"], ep["season"], ep["episode"], ep["episodeType"], ep["duration"], path, ep["explicit"], sqlDate(now), sqlDate(now), feedID)
		if err != nil {
			return err
		}
		delete(byPath, path)
	}
	for _, id := range byPath {
		if _, err = tx.ExecContext(ctx, `DELETE FROM feedEpisodes WHERE id=?`, id); err != nil {
			return err
		}
	}
	title, desc, author := stringValue(entity["title"]), stringValue(entity["description"]), stringValue(entity["author"])
	cover := stringValue(entity["coverPath"])
	imageURL := "/Logo.png"
	if cover != "" {
		imageURL = "/feed/" + slug + "/cover" + filepath.Ext(cover)
		if !newUpdated.IsZero() {
			imageURL += "?ts=" + fmt.Sprint(newUpdated.UnixMilli())
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE feeds SET entityUpdatedAt=?,imageURL=?,siteURL=?,title=?,description=?,author=?,podcastType=?,language=?,explicit=?,coverPath=?,updatedAt=? WHERE id=?`, entity["updatedAt"], imageURL, entity["siteURL"], title, desc, author, defaultString(entity["podcastType"], "serial"), defaultString(entity["language"], "en"), boolInt(entity["explicit"]), nullableString(cover), sqlDate(now), feedID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (h *Handler) feedItem(w http.ResponseWriter, r *http.Request, slug, episodeID string) {
	var path string
	e := h.db.QueryRowContext(r.Context(), `SELECT fe.filePath FROM feedEpisodes fe JOIN feeds f ON f.id=fe.feedId WHERE f.slug=? AND fe.id=?`, slug, episodeID).Scan(&path)
	if e != nil || path == "" {
		w.WriteHeader(404)
		return
	}
	serveMedia(w, r, path)
}
func (h *Handler) feedCover(w http.ResponseWriter, r *http.Request, slug string) {
	var path sql.NullString
	if e := h.db.QueryRowContext(r.Context(), `SELECT coverPath FROM feeds WHERE slug=?`, slug).Scan(&path); e != nil || !path.Valid || path.String == "" {
		w.WriteHeader(404)
		return
	}
	serveMedia(w, r, path.String)
}
func feedXML(f map[string]any, episodes []map[string]any, host string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n<rss version=\"2.0\" xmlns:atom=\"http://www.w3.org/2005/Atom\" xmlns:itunes=\"http://www.itunes.com/dtds/podcast-1.0.dtd\" xmlns:podcast=\"https://podcastindex.org/namespace/1.0\" xmlns:googleplay=\"http://www.google.com/schemas/play-podcasts/1.0\"><channel>")
	elem := func(k, v string) { b.WriteString("<" + k + ">" + xmlEscape(v) + "</" + k + ">") }
	elem("title", stringValue(f["title"]))
	elem("description", stringValue(f["description"]))
	elem("generator", "Audiobookshelf")
	elem("link", host+stringValue(f["siteURL"]))
	elem("language", defaultString(f["language"], "en"))
	elem("itunes:author", defaultString(f["author"], "advplyr"))
	elem("itunes:type", defaultString(f["podcastType"], "serial"))
	b.WriteString("<itunes:image href=\"" + xmlEscape(host+stringValue(f["imageURL"])) + "\"/>")
	if f["explicit"] == true {
		elem("itunes:explicit", "true")
	} else {
		elem("itunes:explicit", "false")
	}
	if f["preventIndexing"] == true {
		elem("itunes:block", "yes")
		elem("googleplay:block", "yes")
	}
	if stringValue(f["ownerName"]) != "" || stringValue(f["ownerEmail"]) != "" {
		b.WriteString("<itunes:owner>")
		if stringValue(f["ownerName"]) != "" {
			elem("itunes:name", stringValue(f["ownerName"]))
		}
		if stringValue(f["ownerEmail"]) != "" {
			elem("itunes:email", stringValue(f["ownerEmail"]))
		}
		b.WriteString("</itunes:owner>")
	}
	b.WriteString("<atom:link href=\"" + xmlEscape(host+stringValue(f["feedURL"])) + "\" rel=\"self\" type=\"application/rss+xml\"/>")
	for _, ep := range episodes {
		b.WriteString("<item>")
		elem("title", stringValue(ep["title"]))
		elem("description", stringValue(ep["description"]))
		elem("link", host+stringValue(ep["siteURL"]))
		url := host + stringValue(ep["enclosureURL"])
		b.WriteString("<guid>" + xmlEscape(url) + "</guid><enclosure url=\"" + xmlEscape(url) + "\" type=\"" + xmlEscape(stringValue(ep["enclosureType"])) + "\" length=\"" + fmt.Sprint(ep["enclosureSize"]) + "\"/>")
		elem("pubDate", stringValue(ep["pubDate"]))
		if stringValue(ep["author"]) != "" {
			elem("itunes:author", stringValue(ep["author"]))
		}
		elem("itunes:duration", fmt.Sprint(int(floatValue(ep["duration"])+0.5)))
		if stringValue(ep["episodeType"]) != "" {
			elem("itunes:episodeType", stringValue(ep["episodeType"]))
		}
		if stringValue(ep["season"]) != "" {
			elem("itunes:season", stringValue(ep["season"]))
		}
		if stringValue(ep["episode"]) != "" {
			elem("itunes:episode", stringValue(ep["episode"]))
		}
		elem("itunes:explicit", fmt.Sprint(ep["explicit"] == true))
		b.WriteString("</item>")
	}
	b.WriteString("</channel></rss>")
	return b.String()
}
func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func (h *Handler) createShare(w http.ResponseWriter, r *http.Request, u store.User) {
	if !admin(u) {
		w.WriteHeader(403)
		return
	}
	var p struct {
		Slug           string   `json:"slug"`
		ExpiresAt      *float64 `json:"expiresAt"`
		MediaItemType  string   `json:"mediaItemType"`
		MediaItemID    string   `json:"mediaItemId"`
		IsDownloadable bool     `json:"isDownloadable"`
	}
	if json.NewDecoder(r.Body).Decode(&p) != nil {
		http.Error(w, "Missing or invalid required fields", 400)
		return
	}
	if strings.TrimSpace(p.Slug) == "" || p.MediaItemType == "" || p.MediaItemID == "" {
		http.Error(w, "Missing or invalid required fields", 400)
		return
	}
	if p.ExpiresAt == nil || *p.ExpiresAt < 0 {
		http.Error(w, "Invalid expiration date", 400)
		return
	}
	if p.MediaItemType != "book" && p.MediaItemType != "podcastEpisode" {
		http.Error(w, "Invalid media item type", 400)
		return
	}
	var existingID, existingMedia sql.NullString
	e := h.db.QueryRowContext(r.Context(), `SELECT id,mediaItemId FROM mediaItemShares WHERE slug=? OR mediaItemId=? LIMIT 1`, p.Slug, p.MediaItemID).Scan(&existingID, &existingMedia)
	if e == nil {
		if existingMedia.String == p.MediaItemID {
			http.Error(w, "Item is already shared", 409)
		} else {
			http.Error(w, "Slug is already in use", 409)
		}
		return
	}
	if !errors.Is(e, sql.ErrNoRows) {
		http.Error(w, "Internal server error", 500)
		return
	}
	table := "books"
	if p.MediaItemType == "podcastEpisode" {
		table = "podcastEpisodes"
	}
	var exists int
	if e = h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM `+table+` WHERE id=?`, p.MediaItemID).Scan(&exists); e != nil {
		http.Error(w, "Internal server error", 500)
		return
	}
	if exists == 0 {
		http.Error(w, "Media item not found", 404)
		return
	}
	id, e := uuid()
	if e != nil {
		http.Error(w, "Internal server error", 500)
		return
	}
	var expires any
	if *p.ExpiresAt > 0 {
		expires = time.UnixMilli(int64(*p.ExpiresAt)).UTC()
	}
	now := time.Now().UTC()
	res, e := h.db.ExecContext(r.Context(), `INSERT INTO mediaItemShares(id,mediaItemId,mediaItemType,slug,pash,expiresAt,extraData,isDownloadable,createdAt,updatedAt,userId) VALUES(?,?,?,?,NULL,?,NULL,?,?,?,?)`, id, p.MediaItemID, p.MediaItemType, p.Slug, expires, boolInt(p.IsDownloadable), sqlDate(now), sqlDate(now), u.ID)
	if e != nil {
		http.Error(w, "Internal server error", 500)
		return
	}
	_ = res
	share := map[string]any{"id": id, "mediaItemId": p.MediaItemID, "mediaItemType": p.MediaItemType, "slug": p.Slug, "expiresAt": expires, "createdAt": now.UnixMilli(), "updatedAt": now.UnixMilli(), "isDownloadable": p.IsDownloadable}
	if h.emit != nil {
		h.emit("@admins", "share_open", share)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(201)
	_ = json.NewEncoder(w).Encode(share)
}
func (h *Handler) deleteShare(w http.ResponseWriter, r *http.Request, u store.User) {
	if !admin(u) {
		w.WriteHeader(403)
		return
	}
	id := routeID(r.URL.Path, "")
	var share map[string]any
	var sid, mediaID, typ, slug sql.NullString
	var expires, created, updated any
	var download int
	e := h.db.QueryRowContext(r.Context(), `SELECT id,mediaItemId,mediaItemType,slug,expiresAt,createdAt,updatedAt,isDownloadable FROM mediaItemShares WHERE id=?`, id).Scan(&sid, &mediaID, &typ, &slug, &expires, &created, &updated, &download)
	if e != nil {
		http.Error(w, "Media item share not found", 404)
		return
	}
	share = map[string]any{"id": sid.String, "mediaItemId": mediaID.String, "mediaItemType": typ.String, "slug": slug.String, "expiresAt": millis(expires), "createdAt": millis(created), "updatedAt": millis(updated), "isDownloadable": download != 0}
	if _, e = h.db.ExecContext(r.Context(), `DELETE FROM mediaItemShares WHERE id=?`, id); e != nil {
		http.Error(w, "Internal server error", 500)
		return
	}
	h.mu.Lock()
	for key, s := range h.sessions {
		if s.ShareID == id {
			delete(h.sessions, key)
		}
	}
	h.mu.Unlock()
	if h.emit != nil {
		h.emit("@admins", "share_closed", share)
	}
	w.WriteHeader(204)
}
func (h *Handler) loadShare(ctx context.Context, slug string) (map[string]any, error) {
	var id, mediaID, typ, shareSlug sql.NullString
	var pash sql.NullString
	var expires, created, updated any
	var download int
	e := h.db.QueryRowContext(ctx, `SELECT id,mediaItemId,mediaItemType,slug,pash,expiresAt,createdAt,updatedAt,isDownloadable FROM mediaItemShares WHERE slug=?`, slug).Scan(&id, &mediaID, &typ, &shareSlug, &pash, &expires, &created, &updated, &download)
	if e != nil {
		return nil, e
	}
	exp := millis(expires)
	if exp != nil {
		if n, ok := exp.(int64); ok && n < time.Now().UnixMilli() {
			_, _ = h.db.ExecContext(ctx, `DELETE FROM mediaItemShares WHERE id=?`, id.String)
			h.mu.Lock()
			for k, s := range h.sessions {
				if s.ShareID == id.String {
					delete(h.sessions, k)
				}
			}
			h.mu.Unlock()
			return nil, sql.ErrNoRows
		}
	}
	return map[string]any{"id": id.String, "mediaItemId": mediaID.String, "mediaItemType": typ.String, "slug": shareSlug.String, "pash": pash.String, "expiresAt": exp, "createdAt": millis(created), "updatedAt": millis(updated), "isDownloadable": download != 0}, nil
}
func shareClient(s map[string]any) map[string]any {
	return map[string]any{"id": s["id"], "mediaItemId": s["mediaItemId"], "mediaItemType": s["mediaItemType"], "slug": s["slug"], "expiresAt": s["expiresAt"], "createdAt": s["createdAt"], "updatedAt": s["updatedAt"], "isDownloadable": s["isDownloadable"]}
}
func (h *Handler) publicShare(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 {
		w.WriteHeader(404)
		return
	}
	slug := parts[2]
	share, e := h.loadShare(r.Context(), slug)
	if e != nil {
		http.Error(w, "Media item share not found", 404)
		return
	}
	start := 0.0
	if raw := r.URL.Query().Get("t"); raw != "" {
		if n, e := strconv.ParseFloat(raw, 64); e == nil && n >= 0 {
			start = float64(int64(n))
		}
	}
	cookie, _ := r.Cookie("share_session_id")
	sessionID := ""
	if cookie != nil {
		sessionID = cookie.Value
	}
	if sessionID != "" {
		h.mu.Lock()
		old := h.sessions[sessionID]
		if old != nil && old.ShareID == share["id"] {
			if start > 0 && start < old.Duration {
				old.Current = start
			}
			payload := shareClient(share)
			payload["playbackSession"] = old.json()
			h.mu.Unlock()
			write(w, payload)
			return
		}
		if old != nil {
			delete(h.sessions, sessionID)
		}
		h.mu.Unlock()
	}
	if share["mediaItemType"] != "book" {
		http.Error(w, "Media item not found", 404)
		return
	}
	var itemID, libraryID, itemPath, relPath sql.NullString
	err := h.db.QueryRowContext(r.Context(), `SELECT id,libraryId,path,relPath FROM libraryItems WHERE mediaId=? AND mediaType='book' LIMIT 1`, share["mediaItemId"]).Scan(&itemID, &libraryID, &itemPath, &relPath)
	if err != nil {
		http.Error(w, "Media item not found", 404)
		return
	}
	entity, err := h.itemInfo(r.Context(), itemID.String)
	if err != nil {
		http.Error(w, "Media item not found", 404)
		return
	}
	tracks := []map[string]any{}
	total := 0.0
	for i, ep := range entity["episodes"].([]map[string]any) {
		meta := map[string]any{"path": ep["filePath"], "filename": ep["filename"], "size": ep["enclosureSize"]}
		ext := strings.ToLower(filepath.Ext(stringValue(ep["filename"])))
		track := map[string]any{"index": i, "startOffset": total, "duration": ep["duration"], "title": stringValue(ep["filename"]), "contentUrl": h.cfg.RouterBasePath + "/public/share/" + slug + "/track/" + strconv.Itoa(i), "mimeType": ep["enclosureType"], "codec": nil, "metadata": meta}
		tracks = append(tracks, track)
		total += floatValue(ep["duration"])
		_ = ext
	}
	if start > total {
		start = 0
	}
	sid := sessionID
	if sid == "" {
		sid, _ = uuid()
	}
	var chapters []any
	var chapterRaw []byte
	_ = h.db.QueryRowContext(r.Context(), `SELECT chapters FROM books WHERE id=?`, share["mediaItemId"]).Scan(&chapterRaw)
	_ = json.Unmarshal(chapterRaw, &chapters)
	metadata := h.oldBookMetadata(r.Context(), stringValue(share["mediaItemId"]))
	now := time.Now().UnixMilli()
	s := &shareSession{ID: sid, ShareID: share["id"].(string), Slug: slug, ItemID: itemID.String, LibraryID: libraryID.String, Title: stringValue(entity["title"]), Author: stringValue(entity["author"]), CoverPath: stringValue(entity["coverPath"]), Duration: total, Current: start, Start: start, Updated: now, Tracks: tracks, Metadata: metadata, Chapters: chapters}
	var librarySettings []byte
	_ = h.db.QueryRowContext(r.Context(), `SELECT settings FROM libraries WHERE id=?`, libraryID.String).Scan(&librarySettings)
	var ls map[string]any
	_ = json.Unmarshal(librarySettings, &ls)
	s.CoverAspect = ls["coverAspectRatio"]
	h.mu.Lock()
	h.sessions[sid] = s
	h.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "share_session_id", Value: sid, Path: "/", HttpOnly: true, MaxAge: 30 * 24 * 60 * 60})
	payload := shareClient(share)
	payload["playbackSession"] = s.json()
	write(w, payload)
}
func (h *Handler) oldBookMetadata(ctx context.Context, id string) map[string]any {
	var title, subtitle, year, date, publisher, desc, isbn, asin, language sql.NullString
	var exp, abr int
	var narr, genres []byte
	_ = h.db.QueryRowContext(ctx, `SELECT title,subtitle,publishedYear,publishedDate,publisher,description,isbn,asin,language,explicit,abridged,narrators,genres FROM books WHERE id=?`, id).Scan(&title, &subtitle, &year, &date, &publisher, &desc, &isbn, &asin, &language, &exp, &abr, &narr, &genres)
	var narrators, genreList []string
	_ = json.Unmarshal(narr, &narrators)
	_ = json.Unmarshal(genres, &genreList)
	authors := []map[string]any{}
	rows, e := h.db.QueryContext(ctx, `SELECT a.id,a.name FROM authors a JOIN bookAuthors ba ON ba.authorId=a.id WHERE ba.bookId=?`, id)
	if e == nil {
		for rows.Next() {
			var i, n string
			if rows.Scan(&i, &n) == nil {
				authors = append(authors, map[string]any{"id": i, "name": n})
			}
		}
		rows.Close()
	}
	series := []map[string]any{}
	rows, e = h.db.QueryContext(ctx, `SELECT s.id,s.name,bs.sequence FROM series s JOIN bookSeries bs ON bs.seriesId=s.id WHERE bs.bookId=?`, id)
	if e == nil {
		for rows.Next() {
			var i, n string
			var seq sql.NullString
			if rows.Scan(&i, &n, &seq) == nil {
				series = append(series, map[string]any{"id": i, "name": n, "sequence": seq.String})
			}
		}
		rows.Close()
	}
	return map[string]any{"id": id, "title": title.String, "subtitle": nullable(subtitle), "authors": authors, "narrators": narrators, "series": series, "genres": genreList, "publishedYear": nullable(year), "publishedDate": nullable(date), "publisher": nullable(publisher), "description": desc.String, "isbn": nullable(isbn), "asin": nullable(asin), "language": nullable(language), "explicit": exp != 0, "abridged": abr != 0}
}
func (s *shareSession) json() map[string]any {
	return map[string]any{"id": s.ID, "userId": nil, "libraryId": s.LibraryID, "libraryItemId": s.ItemID, "bookId": s.Metadata["id"], "episodeId": nil, "mediaType": "book", "mediaMetadata": s.Metadata, "chapters": s.Chapters, "displayTitle": s.Title, "displayAuthor": s.Author, "coverPath": nullableString(s.CoverPath), "duration": s.Duration, "playMethod": 0, "mediaPlayer": "web-share", "deviceInfo": map[string]any{"clientName": "Abs Web Share", "deviceId": s.ID}, "serverVersion": "2.37.2", "date": time.UnixMilli(s.Updated).UTC().Format("2006-01-02"), "dayOfWeek": time.UnixMilli(s.Updated).UTC().Weekday().String(), "timeListening": 0, "startTime": s.Start, "currentTime": s.Current, "startedAt": s.Updated, "updatedAt": s.Updated, "audioTracks": s.Tracks, "libraryItem": nil, "coverAspectRatio": s.CoverAspect}
}
func (h *Handler) shareProgress(w http.ResponseWriter, r *http.Request) {
	slug := routeID(r.URL.Path, "progress")
	cookie, e := r.Cookie("share_session_id")
	if e != nil || cookie.Value == "" {
		http.Error(w, "Share session not set", 404)
		return
	}
	var p struct {
		CurrentTime *float64 `json:"currentTime"`
	}
	if json.NewDecoder(r.Body).Decode(&p) != nil || p.CurrentTime == nil || *p.CurrentTime < 0 {
		http.Error(w, "Invalid current time", 400)
		return
	}
	share, e := h.loadShare(r.Context(), slug)
	if e != nil {
		w.WriteHeader(404)
		return
	}
	h.mu.Lock()
	s := h.sessions[cookie.Value]
	if s == nil || s.ShareID != share["id"] {
		h.mu.Unlock()
		http.Error(w, "Share session not found", 404)
		return
	}
	s.Current = *p.CurrentTime
	if s.Current > s.Duration {
		s.Current = s.Duration
	}
	s.Updated = time.Now().UnixMilli()
	h.mu.Unlock()
	w.WriteHeader(204)
}
func (h *Handler) publicShareFile(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 {
		w.WriteHeader(404)
		return
	}
	slug := parts[2]
	cookie, e := r.Cookie("share_session_id")
	if e != nil || cookie.Value == "" {
		http.Error(w, "Share session not set", 404)
		return
	}
	h.mu.Lock()
	s := h.sessions[cookie.Value]
	if s == nil || s.Slug != slug {
		h.mu.Unlock()
		http.Error(w, "Share session not found", 404)
		return
	}
	copySession := *s
	h.mu.Unlock()
	switch parts[3] {
	case "cover":
		if copySession.CoverPath == "" {
			http.Error(w, "Cover image not found", 404)
			return
		}
		serveMedia(w, r, copySession.CoverPath)
	case "track":
		if len(parts) < 5 {
			http.Error(w, "Track not found", 404)
			return
		}
		index, _ := strconv.Atoi(parts[4])
		for _, track := range copySession.Tracks {
			if intValue(track["index"]) == index {
				serveMedia(w, r, stringValue(track["metadata"].(map[string]any)["path"]))
				return
			}
		}
		http.Error(w, "Track not found", 404)
	case "download":
		share, e := h.loadShare(r.Context(), slug)
		if e != nil {
			w.WriteHeader(404)
			return
		}
		if share["isDownloadable"] != true {
			http.Error(w, "Download is not allowed for this item", 403)
			return
		}
		var itemPath, relPath sql.NullString
		var isFile int
		e = h.db.QueryRowContext(r.Context(), `SELECT path,relPath,isFile FROM libraryItems WHERE id=?`, copySession.ItemID).Scan(&itemPath, &relPath, &isFile)
		if e != nil {
			http.Error(w, "Library item not found", 404)
			return
		}
		if isFile != 0 {
			w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(relPath.String)+`"`)
			serveMedia(w, r, itemPath.String)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="`+safeFilename(copySession.Title)+`.zip"`)
		if err := zipDirectory(w, itemPath.String); err != nil {
			http.Error(w, "Failed to download the item", 500)
		}
	default:
		w.WriteHeader(404)
	}
}

func write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
func routeID(path, suffix string) string {
	p := strings.Split(strings.Trim(path, "/"), "/")
	if suffix != "" {
		for i, x := range p {
			if x == suffix && i > 0 {
				return p[i-1]
			}
		}
	}
	if len(p) > 0 {
		return p[len(p)-1]
	}
	return ""
}
func uuid() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	s := hex.EncodeToString(b)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
func libraryAccess(u store.User, id string) bool {
	var p map[string]any
	if json.Unmarshal(u.Permissions, &p) != nil {
		return false
	}
	if p["accessAllLibraries"] == true {
		return true
	}
	a, _ := p["librariesAccessible"].([]any)
	for _, v := range a {
		if v == id {
			return true
		}
	}
	return false
}
func stringValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case sql.NullString:
		return x.String
	case []byte:
		return string(x)
	case fmt.Stringer:
		return x.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}
func nullable(v sql.NullString) any {
	if !v.Valid {
		return nil
	}
	return v.String
}
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func floatValue(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case json.Number:
		n, _ := x.Float64()
		return n
	case string:
		n, _ := strconv.ParseFloat(x, 64)
		return n
	}
	return 0
}
func intValue(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(x)
		return n
	}
	return 0
}
func boolInt(v any) int {
	if v == true {
		return 1
	}
	return 0
}
func decodeObjects(raw []byte) []map[string]any {
	var x []map[string]any
	_ = json.Unmarshal(raw, &x)
	return x
}
func contains(a []string, s string) bool {
	for _, x := range a {
		if x == s {
			return true
		}
	}
	return false
}
func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
func parseTime(v any) time.Time {
	switch x := v.(type) {
	case time.Time:
		return x
	case string:
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999 -07:00", "2006-01-02 15:04:05.000 -07:00", "2006-01-02 15:04:05"} {
			if t, e := time.Parse(layout, x); e == nil {
				return t
			}
		}
	case []byte:
		return parseTime(string(x))
	case int64:
		return time.UnixMilli(x)
	}
	return time.Time{}
}
func sqlDate(t time.Time) string { return dbtime.Format(t) }
func millis(v any) any {
	if v == nil {
		return nil
	}
	switch x := v.(type) {
	case time.Time:
		return x.UnixMilli()
	case int64:
		return x
	case float64:
		return int64(x)
	case string:
		if t := parseTime(x); !t.IsZero() {
			return t.UnixMilli()
		}
	case []byte:
		return millis(string(x))
	}
	return nil
}
func defaultString(v any, d string) string {
	if s := stringValue(v); s != "" {
		return s
	}
	return d
}
func hostPrefix(r *http.Request, base string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if x := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); x != "" {
		scheme = x
	}
	return scheme + "://" + r.Host + base
}
func serveMedia(w http.ResponseWriter, r *http.Request, p string) {
	ext := strings.ToLower(filepath.Ext(p))
	mt := map[string]string{".m4b": "audio/mp4", ".m4a": "audio/mp4", ".mp3": "audio/mpeg", ".ogg": "audio/ogg", ".opus": "audio/ogg", ".flac": "audio/flac", ".wav": "audio/wav", ".aac": "audio/aac", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".webp": "image/webp", ".gif": "image/gif"}[ext]
	if mt == "" {
		mt = mime.TypeByExtension(ext)
	}
	if mt != "" {
		w.Header().Set("Content-Type", mt)
	}
	http.ServeFile(w, r, p)
}
func safeFilename(s string) string {
	s = filepath.Base(s)
	s = strings.ReplaceAll(s, "\"", "_")
	if s == "." || s == "/" || s == "" {
		return "download"
	}
	return s
}
func zipDirectory(w io.Writer, root string) error {
	z := zip.NewWriter(w)
	err := filepath.Walk(root, func(path string, info os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		if info.IsDir() {
			return nil
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		header, e := zip.FileInfoHeader(info)
		if e != nil {
			return e
		}
		header.Name = filepath.ToSlash(rel)
		header.Method = zip.Deflate
		out, e := z.CreateHeader(header)
		if e != nil {
			return e
		}
		f, e := os.Open(path)
		if e != nil {
			return e
		}
		_, e = io.Copy(out, f)
		ce := f.Close()
		if e != nil {
			return e
		}
		return ce
	})
	closeErr := z.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func detailsValue(p map[string]any, k string) string {
	d, _ := p["metadataDetails"].(map[string]any)
	return stringValue(d[k])
}
func mimeForPath(p string) string {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".m4b", ".m4a", ".mp4":
		return "audio/mp4"
	case ".mp3":
		return "audio/mpeg"
	case ".flac":
		return "audio/flac"
	case ".ogg", ".opus":
		return "audio/ogg"
	case ".wav":
		return "audio/wav"
	case ".aac":
		return "audio/aac"
	}
	return "application/octet-stream"
}
