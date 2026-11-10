package podcastread

import (
	"context"
	"database/sql"
	"strconv"

	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/store"
)

func LoadMany(ctx context.Context, db *sql.DB, ids []string, opt Options) (map[string]*Podcast, error) {
	podcasts := make(map[string]*Podcast, len(ids))
	if len(ids) == 0 {
		return podcasts, nil
	}
	err := store.QueryIn(ctx, db, `SELECT id,title,author,releaseDate,feedURL,imageURL,description,itunesPageURL,itunesId,itunesArtistId,language,podcastType,explicit,autoDownloadEpisodes,autoDownloadSchedule,lastEpisodeCheck,maxEpisodesToKeep,maxNewEpisodesToDownload,coverPath,tags,genres,numEpisodes FROM podcasts WHERE id IN `, ``, ids, func(rows *sql.Rows) error {
		var p Podcast
		var id, title, author, release, feed, image, description, itunesPage, itunesID, itunesArtist, language, ptype, schedule, cover sql.NullString
		var explicit, autoDownload sql.NullInt64
		var lastCheck any
		var maxKeep, maxNew, numEpisodes sql.NullInt64
		var tags, genres []byte
		if err := rows.Scan(&id, &title, &author, &release, &feed, &image, &description, &itunesPage, &itunesID, &itunesArtist, &language, &ptype, &explicit, &autoDownload, &schedule, &lastCheck, &maxKeep, &maxNew, &cover, &tags, &genres, &numEpisodes); err != nil {
			return err
		}
		check, err := dbtime.OptionalMillis(lastCheck)
		if err != nil {
			return err
		}
		if check != nil && *check == 0 {
			check = nil
		}
		p = Podcast{ID: id.String, Title: store.NullStringPtr(title), Author: store.NullStringPtr(author), Description: store.NullStringPtr(description), ReleaseDate: store.NullStringPtr(release),
			FeedURL: store.NullStringPtr(feed), ImageURL: store.NullStringPtr(image), ITunesPageURL: store.NullStringPtr(itunesPage), ITunesID: store.NullStringPtr(itunesID), ITunesArtistID: store.NullStringPtr(itunesArtist),
			Language: store.NullStringPtr(language), Type: store.NullStringPtr(ptype), Explicit: explicit.Int64 != 0, AutoDownloadEpisodes: autoDownload.Int64 != 0, AutoDownloadSchedule: store.NullStringPtr(schedule),
			LastEpisodeCheck: check, MaxEpisodesToKeep: maxKeep.Int64, MaxNewEpisodesToDownload: maxNew.Int64, CoverPath: store.NullStringPtr(cover),
			Tags: store.DecodeJSONSlice[string](tags), Genres: store.DecodeJSONSlice[string](genres), NumEpisodes: numEpisodes.Int64}
		if opt.Episodes {
			p.Episodes = []Episode{}
		}
		podcasts[p.ID] = &p
		return nil
	})
	if err != nil || !opt.Episodes {
		return podcasts, err
	}
	column, keys := "podcastId", ids
	if opt.EpisodeIDs != nil {
		column, keys = "id", opt.EpisodeIDs
	}
	err = store.QueryIn(ctx, db, `SELECT podcastId,id,"index",season,episode,episodeType,title,subtitle,description,pubDate,enclosureURL,enclosureSize,enclosureType,publishedAt,audioFile,chapters,extraData,createdAt,updatedAt FROM podcastEpisodes WHERE `+column+` IN `, ` ORDER BY podcastId,createdAt ASC,rowid`, keys, func(rows *sql.Rows) error {
		var e Episode
		var index, enclosureSize sql.NullInt64
		var season, episode, episodeType, title, subtitle, description, pubDate, enclosureURL, enclosureType sql.NullString
		var published, created, updated any
		var audio, chapters, extra []byte
		if err := rows.Scan(&e.PodcastID, &e.ID, &index, &season, &episode, &episodeType, &title, &subtitle, &description, &pubDate, &enclosureURL, &enclosureSize, &enclosureType, &published, &audio, &chapters, &extra, &created, &updated); err != nil {
			return err
		}
		var err error
		if e.PublishedAt, err = dbtime.OptionalMillis(published); err != nil {
			return err
		}
		if e.PublishedAt != nil && *e.PublishedAt == 0 {
			e.PublishedAt = nil
		}
		if e.AddedAt, err = dbtime.OptionalMillis(created); err != nil {
			return err
		}
		if e.UpdatedAt, err = dbtime.OptionalMillis(updated); err != nil {
			return err
		}
		if index.Valid {
			e.Index = &index.Int64
		}
		extras := store.DecodeJSONObject(extra)
		e.OldEpisodeID, _ = extras["oldEpisodeId"].(string)
		e.GUID, _ = extras["guid"].(string)
		e.Season, e.Episode, e.EpisodeType, e.Title, e.Subtitle, e.Description, e.PubDate = store.NullStringPtr(season), store.NullStringPtr(episode), store.NullStringPtr(episodeType), store.NullStringPtr(title), store.NullStringPtr(subtitle), store.NullStringPtr(description), store.NullStringPtr(pubDate)
		if enclosureURL.Valid && enclosureURL.String != "" {
			e.Enclosure = &Enclosure{URL: enclosureURL.String, Type: store.NullStringPtr(enclosureType)}
			if enclosureSize.Valid {
				length := strconv.FormatInt(enclosureSize.Int64, 10)
				e.Enclosure.Length = &length
			}
		}
		e.AudioFile, e.Chapters = store.DecodeJSONObject(audio), store.DecodeJSONSlice[map[string]any](chapters)
		if p := podcasts[e.PodcastID]; p != nil {
			p.Episodes = append(p.Episodes, e)
		}
		return nil
	})
	return podcasts, err
}
