package podcasts

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"audiobookshelf-compat/internal/store"
)

type podcastItem struct {
	ID, LibraryID, MediaID, Path, RelPath, FolderID, Ino                      string
	LibraryFiles                                                              sql.NullString
	CreatedAt, UpdatedAt, MTime, CTime, BirthTime, LastScan                   sql.NullString
	ScanVersion                                                               sql.NullString
	Title, Author, ReleaseDate, FeedURL, ImageURL, Description                sql.NullString
	ITunesPageURL, ITunesID, ITunesArtistID, Language, PodcastType, CoverPath sql.NullString
	Explicit, AutoDownload                                                    bool
	AutoSchedule, LastEpisodeCheck                                            sql.NullString
	MaxKeep, MaxNew, NumEpisodes                                              int
	Tags, Genres                                                              sql.NullString
	Episodes                                                                  []podcastEpisode
}

type podcastEpisode struct {
	ID, PodcastID                                                       string
	Index                                                               sql.NullInt64
	Season, Episode, EpisodeType, Title, Subtitle, Description, PubDate sql.NullString
	EnclosureURL, EnclosureType                                         sql.NullString
	EnclosureSize                                                       sql.NullInt64
	PublishedAt, CreatedAt, UpdatedAt                                   sql.NullString
	AudioFile, Chapters, ExtraData                                      sql.NullString
}

func (h *Handler) loadItem(ctx context.Context, id string) (podcastItem, error) {
	var x podcastItem
	err := h.db.QueryRowContext(ctx, `SELECT li.id,li.libraryId,li.mediaId,li.path,li.relPath,li.libraryFolderId,li.ino,li.libraryFiles,li.createdAt,li.updatedAt,li.mtime,li.ctime,li.birthtime,li.lastScan,li.lastScanVersion,p.title,p.author,p.releaseDate,p.feedURL,p.imageURL,p.description,p.itunesPageURL,p.itunesId,p.itunesArtistId,p.language,p.podcastType,COALESCE(p.explicit,0),COALESCE(p.autoDownloadEpisodes,0),p.autoDownloadSchedule,p.lastEpisodeCheck,COALESCE(p.maxEpisodesToKeep,0),COALESCE(p.maxNewEpisodesToDownload,3),p.coverPath,p.tags,p.genres,COALESCE(p.numEpisodes,0) FROM libraryItems li JOIN podcasts p ON p.id=li.mediaId WHERE li.id=? AND li.mediaType='podcast'`, id).Scan(&x.ID, &x.LibraryID, &x.MediaID, &x.Path, &x.RelPath, &x.FolderID, &x.Ino, &x.LibraryFiles, &x.CreatedAt, &x.UpdatedAt, &x.MTime, &x.CTime, &x.BirthTime, &x.LastScan, &x.ScanVersion, &x.Title, &x.Author, &x.ReleaseDate, &x.FeedURL, &x.ImageURL, &x.Description, &x.ITunesPageURL, &x.ITunesID, &x.ITunesArtistID, &x.Language, &x.PodcastType, &x.Explicit, &x.AutoDownload, &x.AutoSchedule, &x.LastEpisodeCheck, &x.MaxKeep, &x.MaxNew, &x.CoverPath, &x.Tags, &x.Genres, &x.NumEpisodes)
	if err != nil {
		return x, err
	}
	rows, err := h.db.QueryContext(ctx, `SELECT id,podcastId,"index",season,episode,episodeType,title,subtitle,description,pubDate,enclosureURL,enclosureSize,enclosureType,publishedAt,audioFile,chapters,extraData,createdAt,updatedAt FROM podcastEpisodes WHERE podcastId=? ORDER BY createdAt ASC`, x.MediaID)
	if err != nil {
		return x, err
	}
	defer rows.Close()
	for rows.Next() {
		var ep podcastEpisode
		if err = rows.Scan(&ep.ID, &ep.PodcastID, &ep.Index, &ep.Season, &ep.Episode, &ep.EpisodeType, &ep.Title, &ep.Subtitle, &ep.Description, &ep.PubDate, &ep.EnclosureURL, &ep.EnclosureSize, &ep.EnclosureType, &ep.PublishedAt, &ep.AudioFile, &ep.Chapters, &ep.ExtraData, &ep.CreatedAt, &ep.UpdatedAt); err != nil {
			return x, err
		}
		x.Episodes = append(x.Episodes, ep)
	}
	if err = rows.Err(); err != nil {
		return x, err
	}
	return x, nil
}

func (x podcastItem) oldJSON() map[string]any {
	var tags, genres []any
	_ = json.Unmarshal([]byte(x.Tags.String), &tags)
	if tags == nil {
		tags = []any{}
	}
	_ = json.Unmarshal([]byte(x.Genres.String), &genres)
	if genres == nil {
		genres = []any{}
	}
	var files []any
	_ = json.Unmarshal([]byte(x.LibraryFiles.String), &files)
	if files == nil {
		files = []any{}
	}
	episodes := make([]map[string]any, 0, len(x.Episodes))
	var total float64
	for _, ep := range x.Episodes {
		old := ep.expandedJSON(x.ID)
		episodes = append(episodes, old)
		var audio map[string]any
		_ = json.Unmarshal([]byte(ep.AudioFile.String), &audio)
		var metadata map[string]any
		_ = json.Unmarshal(mustJSON(audio["metadata"]), &metadata)
		total += number(metadata["size"])
	}
	meta := map[string]any{"title": nullableSQL(x.Title), "author": nullableSQL(x.Author), "description": nullableSQL(x.Description), "releaseDate": nullableSQL(x.ReleaseDate), "genres": genres, "feedUrl": nullableSQL(x.FeedURL), "imageUrl": nullableSQL(x.ImageURL), "itunesPageUrl": nullableSQL(x.ITunesPageURL), "itunesId": nullableSQL(x.ITunesID), "itunesArtistId": nullableSQL(x.ITunesArtistID), "explicit": x.Explicit, "language": nullableSQL(x.Language), "type": nullableSQL(x.PodcastType), "titleIgnorePrefix": titlePrefixAtEnd(x.Title.String)}
	last := sqlMillis(x.LastEpisodeCheck)
	minified := map[string]any{"id": x.MediaID, "metadata": meta, "coverPath": nullableSQL(x.CoverPath), "tags": tags, "numEpisodes": len(episodes), "autoDownloadEpisodes": x.AutoDownload, "autoDownloadSchedule": nullableSQL(x.AutoSchedule), "lastEpisodeCheck": last, "maxEpisodesToKeep": x.MaxKeep, "maxNewEpisodesToDownload": x.MaxNew, "size": int64(total)}
	for i, raw := range files {
		if m, ok := raw.(map[string]any); ok {
			if _, ok = m["fileType"]; !ok {
				m["fileType"] = "audio"
			}
			files[i] = m
		}
	}
	added := sqlMillis(x.CreatedAt)
	updated := sqlMillis(x.UpdatedAt)
	return map[string]any{"id": x.ID, "ino": x.Ino, "oldLibraryItemId": nil, "libraryId": x.LibraryID, "folderId": x.FolderID, "path": x.Path, "relPath": x.RelPath, "isFile": false, "mtimeMs": sqlMillis(x.MTime), "ctimeMs": sqlMillis(x.CTime), "birthtimeMs": sqlMillis(x.BirthTime), "addedAt": added, "updatedAt": updated, "isMissing": false, "isInvalid": false, "mediaType": "podcast", "media": mergeMap(minified, map[string]any{"libraryItemId": x.ID, "episodes": episodes}), "numFiles": len(files), "size": int64(total), "lastScan": sqlMillis(x.LastScan), "scanVersion": nullableSQL(x.ScanVersion), "libraryFiles": files}
}

func (x podcastItem) legacyJSON() map[string]any {
	var tags, genres []any
	_ = json.Unmarshal([]byte(x.Tags.String), &tags)
	if tags == nil {
		tags = []any{}
	}
	_ = json.Unmarshal([]byte(x.Genres.String), &genres)
	if genres == nil {
		genres = []any{}
	}
	episodes := make([]map[string]any, 0, len(x.Episodes))
	for _, ep := range x.Episodes {
		episodes = append(episodes, ep.oldJSON(x.ID))
	}
	metadata := map[string]any{"title": nullableSQL(x.Title), "author": nullableSQL(x.Author), "description": nullableSQL(x.Description), "releaseDate": nullableSQL(x.ReleaseDate), "genres": genres, "feedUrl": nullableSQL(x.FeedURL), "imageUrl": nullableSQL(x.ImageURL), "itunesPageUrl": nullableSQL(x.ITunesPageURL), "itunesId": nullableSQL(x.ITunesID), "itunesArtistId": nullableSQL(x.ITunesArtistID), "explicit": x.Explicit, "language": nullableSQL(x.Language), "type": nullableSQL(x.PodcastType)}
	media := map[string]any{"id": x.MediaID, "libraryItemId": x.ID, "metadata": metadata, "coverPath": nullableSQL(x.CoverPath), "tags": tags, "episodes": episodes, "autoDownloadEpisodes": x.AutoDownload, "autoDownloadSchedule": nullableSQL(x.AutoSchedule), "lastEpisodeCheck": sqlMillis(x.LastEpisodeCheck), "maxEpisodesToKeep": x.MaxKeep, "maxNewEpisodesToDownload": x.MaxNew}
	item := x.oldJSON()
	delete(item, "numFiles")
	delete(item, "size")
	item["media"] = media
	return item
}

func (e podcastEpisode) oldJSON(libraryItemID string) map[string]any {
	var audio map[string]any
	_ = json.Unmarshal([]byte(e.AudioFile.String), &audio)
	if audio == nil {
		audio = map[string]any{}
	}
	var chapters any
	_ = json.Unmarshal([]byte(e.Chapters.String), &chapters)
	if chapters == nil {
		chapters = []any{}
	}
	var extra map[string]any
	_ = json.Unmarshal([]byte(e.ExtraData.String), &extra)
	if extra == nil {
		extra = map[string]any{}
	}
	var enclosure any
	if e.EnclosureURL.Valid {
		length := any(nil)
		if e.EnclosureSize.Valid {
			length = fmt.Sprint(e.EnclosureSize.Int64)
		}
		enclosure = map[string]any{"url": e.EnclosureURL.String, "type": nullableSQL(e.EnclosureType), "length": length}
	}
	var index any
	if e.Index.Valid {
		index = e.Index.Int64
	}
	base := map[string]any{"libraryItemId": libraryItemID, "podcastId": e.PodcastID, "id": e.ID, "oldEpisodeId": extra["oldEpisodeId"], "index": index, "season": nullableSQL(e.Season), "episode": nullableSQL(e.Episode), "episodeType": nullableSQL(e.EpisodeType), "title": nullableSQL(e.Title), "subtitle": nullableSQL(e.Subtitle), "description": nullableSQL(e.Description), "enclosure": enclosure, "guid": extra["guid"], "pubDate": nullableSQL(e.PubDate), "chapters": chapters, "audioFile": audio, "publishedAt": sqlMillis(e.PublishedAt), "addedAt": sqlMillis(e.CreatedAt), "updatedAt": sqlMillis(e.UpdatedAt)}
	return base
}

func (e podcastEpisode) expandedJSON(libraryItemID string) map[string]any {
	base := e.oldJSON(libraryItemID)
	var audio map[string]any
	_ = json.Unmarshal([]byte(e.AudioFile.String), &audio)
	if audio == nil {
		audio = map[string]any{}
	}
	var metadata map[string]any
	_ = json.Unmarshal(mustJSON(audio["metadata"]), &metadata)
	track := map[string]any{}
	for k, v := range audio {
		track[k] = v
	}
	track["startOffset"] = 0
	track["index"] = 1
	track["title"] = metadata["filename"]
	track["contentUrl"] = "/api/items/" + libraryItemID + "/file/" + fmt.Sprint(audio["ino"])
	base["audioTrack"] = track
	base["duration"] = number(audio["duration"])
	base["size"] = number(metadata["size"])
	return base
}

func canAccessPodcast(u store.User, x podcastItem) bool {
	if u.Type == "admin" || u.Type == "root" {
		return true
	}
	var p map[string]any
	if json.Unmarshal(u.Permissions, &p) != nil {
		return false
	}
	if p["accessAllLibraries"] != true {
		ok := false
		if ids, yes := p["librariesAccessible"].([]any); yes {
			for _, id := range ids {
				if id == x.LibraryID {
					ok = true
				}
			}
		}
		if !ok {
			return false
		}
	}
	if p["accessAllTags"] == true {
		return !x.Explicit || p["accessExplicitContent"] == true
	}
	if x.Explicit && p["accessExplicitContent"] != true {
		return false
	}
	var tags []string
	_ = json.Unmarshal([]byte(x.Tags.String), &tags)
	selected, _ := p["itemTagsSelected"].([]any)
	if p["selectedTagsNotAccessible"] == true {
		if len(tags) == 0 {
			return true
		}
		for _, tag := range tags {
			for _, s := range selected {
				if s == tag {
					return false
				}
			}
		}
		return true
	}
	for _, tag := range tags {
		for _, s := range selected {
			if s == tag {
				return true
			}
		}
	}
	return false
}
func titlePrefixAtEnd(s string) string {
	lower := strings.ToLower(s)
	for _, prefix := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(lower, prefix) {
			return s[len(prefix):] + ", " + strings.TrimSpace(s[:len(prefix)])
		}
	}
	return s
}
func nullableSQL(v sql.NullString) any {
	if !v.Valid {
		return nil
	}
	return v.String
}
func sqlMillis(v sql.NullString) any {
	if !v.Valid || v.String == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999 -0700 MST", "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, v.String); err == nil {
			return t.UnixMilli()
		}
	}
	if n, err := time.Parse("2006-01-02T15:04:05.000Z", v.String); err == nil {
		return n.UnixMilli()
	}
	return nil
}
func mergeMap(a, b map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func number(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case string:
		var x float64
		_, _ = fmt.Sscan(n, &x)
		return x
	}
	return 0
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
