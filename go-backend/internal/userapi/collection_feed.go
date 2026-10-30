package userapi

import (
	"database/sql"
	"net/http"
)

func (h *Handler) collectionRSSFeed(r *http.Request, collectionID string) (map[string]any, error) {
	var id, slug, userID, entityType, entityID string
	var entityUpdated, cover, serverAddress, feedURL, imageURL, siteURL sql.NullString
	var title, description, author, podcastType, language, ownerName, ownerEmail string
	var explicit, preventIndexing bool
	var created, updated any
	err := h.db.QueryRowContext(r.Context(), `SELECT id,slug,userId,entityType,entityId,entityUpdatedAt,coverPath,serverAddress,feedURL,imageURL,siteURL,title,description,author,podcastType,language,ownerName,ownerEmail,explicit,preventIndexing,createdAt,updatedAt FROM feeds WHERE entityType='collection' AND entityId=? LIMIT 1`, collectionID).Scan(&id, &slug, &userID, &entityType, &entityID, &entityUpdated, &cover, &serverAddress, &feedURL, &imageURL, &siteURL, &title, &description, &author, &podcastType, &language, &ownerName, &ownerEmail, &explicit, &preventIndexing, &created, &updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entityUpdatedAt any
	if entityUpdated.Valid {
		entityUpdatedAt = nullableMillis(entityUpdated.String)
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT id,title,description,enclosureURL,enclosureSize,enclosureType,pubDate,siteURL,author,explicit,duration,season,episode,episodeType,filePath FROM feedEpisodes WHERE feedId=? ORDER BY pubDate DESC`, id)
	if err != nil {
		return nil, err
	}
	episodes := make([]any, 0)
	for rows.Next() {
		var eid, etitle, edescription, enclosureURL, enclosureType, pubDate, link, eauthor, season, episode, episodeType, fullPath sql.NullString
		var enclosureSize, duration sql.NullInt64
		var exp sql.NullBool
		if err := rows.Scan(&eid, &etitle, &edescription, &enclosureURL, &enclosureSize, &enclosureType, &pubDate, &link, &eauthor, &exp, &duration, &season, &episode, &episodeType, &fullPath); err != nil {
			rows.Close()
			return nil, err
		}
		episodes = append(episodes, map[string]any{"id": eid.String, "title": nullableValue(etitle), "description": nullableValue(edescription), "enclosure": map[string]any{"url": nullableValue(enclosureURL), "size": nullableNumber(enclosureSize), "type": nullableValue(enclosureType)}, "pubDate": nullableValue(pubDate), "link": nullableValue(link), "author": nullableValue(eauthor), "explicit": exp.Bool, "duration": nullableNumber(duration), "season": nullableValue(season), "episode": nullableValue(episode), "episodeType": nullableValue(episodeType), "fullPath": nullableValue(fullPath)})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	return map[string]any{"id": id, "slug": slug, "userId": userID, "entityType": entityType, "entityId": entityID, "entityUpdatedAt": entityUpdatedAt, "coverPath": nullableValue(cover), "meta": map[string]any{"title": title, "description": description, "author": author, "imageUrl": nullableValue(imageURL), "feedUrl": nullableValue(feedURL), "link": nullableValue(siteURL), "explicit": explicit, "type": podcastType, "language": language, "preventIndexing": preventIndexing, "ownerName": ownerName, "ownerEmail": ownerEmail}, "serverAddress": serverAddress.String, "feedUrl": feedURL.String, "episodes": episodes, "createdAt": millis(created), "updatedAt": millis(updated)}, nil
}

func nullableNumber(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}
