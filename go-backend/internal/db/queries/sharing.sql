-- Media item shares, RSS feeds and feed episodes as the sharing package and the
-- catalog read and write them. Every parameter is named (sqlc.arg) when the
-- statement has more than one: sqlc numbers a mix of named and bare ?
-- placeholders out of step. Nullable columns take CAST(sqlc.arg(...) AS TEXT)
-- or AS INTEGER so the Go parameter is a plain value, or sqlc.arg with a
-- nullable Go type where the caller passes NULL. NULL literals stay as written.
-- Timestamps are dbtime strings written by the caller. Reads use SELECT * so
-- each row maps to one sqlc model.

-- name: GetShareMediaIDBySlugOrMedia :one
SELECT mediaItemId FROM mediaItemShares
WHERE slug = CAST(sqlc.arg(slug) AS TEXT) OR mediaItemId = CAST(sqlc.arg(media_id) AS TEXT)
LIMIT 1;

-- name: GetMediaItemShare :one
SELECT * FROM mediaItemShares WHERE id = ?;

-- name: GetMediaItemShareBySlug :one
SELECT * FROM mediaItemShares WHERE slug = CAST(sqlc.arg(slug) AS TEXT);

-- name: GetMediaItemShareSummaryByMediaID :one
SELECT id, mediaItemId, mediaItemType, slug, expiresAt, isDownloadable, createdAt, updatedAt
FROM mediaItemShares WHERE mediaItemId = CAST(sqlc.arg(media_id) AS TEXT) LIMIT 1;

-- name: InsertMediaItemShare :exec
INSERT INTO mediaItemShares (id, mediaItemId, mediaItemType, slug, pash, expiresAt, extraData, isDownloadable, createdAt, updatedAt, userId)
VALUES (sqlc.arg(id), CAST(sqlc.arg(media_id) AS TEXT), CAST(sqlc.arg(media_type) AS TEXT), CAST(sqlc.arg(slug) AS TEXT), NULL, sqlc.arg(expiresAt), NULL, sqlc.arg(isDownloadable), sqlc.arg(createdAt), sqlc.arg(updatedAt), CAST(sqlc.arg(user_id) AS TEXT));

-- name: DeleteMediaItemShare :exec
DELETE FROM mediaItemShares WHERE id = ?;

-- name: DeleteBookShares :exec
DELETE FROM mediaItemShares WHERE mediaItemType = 'book' AND mediaItemId = CAST(sqlc.arg(media_id) AS TEXT);

-- name: DeleteBookSharesByMediaIDs :exec
DELETE FROM mediaItemShares WHERE mediaItemType = 'book' AND mediaItemId IN (sqlc.slice(media_ids));

-- name: DeletePodcastEpisodeSharesByMediaIDs :exec
DELETE FROM mediaItemShares WHERE mediaItemType = 'podcastEpisode' AND mediaItemId IN (sqlc.slice(media_ids));

-- name: GetFeed :one
SELECT * FROM feeds WHERE id = ?;

-- name: GetFeedBySlug :one
SELECT * FROM feeds WHERE slug = CAST(sqlc.arg(slug) AS TEXT);

-- name: GetFeedCoverPathBySlug :one
SELECT coverPath FROM feeds WHERE slug = CAST(sqlc.arg(slug) AS TEXT);

-- name: GetFeedByEntity :one
SELECT * FROM feeds
WHERE entityType = CAST(sqlc.arg(entity_type) AS TEXT) AND entityId = CAST(sqlc.arg(entity_id) AS TEXT)
LIMIT 1;

-- name: GetFeedSummaryByEntity :one
-- An empty entity_type matches any type (RssFeedManager.findFeedForEntityId
-- filters on entityId alone). Lists only the columns the catalog reads.
SELECT id, entityType, entityId, feedURL, title, description, preventIndexing, ownerName, ownerEmail
FROM feeds
WHERE (CAST(sqlc.arg(entity_type) AS TEXT) = '' OR entityType = CAST(sqlc.arg(entity_type) AS TEXT))
  AND entityId = CAST(sqlc.arg(entity_id) AS TEXT)
LIMIT 1;

-- name: ListFeeds :many
SELECT * FROM feeds;

-- name: CountFeedsBySlug :one
SELECT COUNT(*) FROM feeds WHERE slug = CAST(sqlc.arg(slug) AS TEXT);

-- name: InsertFeed :exec
INSERT INTO feeds (id, slug, entityType, entityId, entityUpdatedAt, serverAddress, feedURL, imageURL, siteURL, title, description, author, podcastType, language, ownerName, ownerEmail, explicit, preventIndexing, coverPath, userId, createdAt, updatedAt)
VALUES (sqlc.arg(id), sqlc.arg(slug), sqlc.arg(entity_type), sqlc.arg(entity_id), sqlc.arg(entityUpdatedAt), sqlc.arg(server_address), sqlc.arg(feed_url), sqlc.arg(image_url), sqlc.arg(site_url), sqlc.arg(title), sqlc.arg(description), sqlc.arg(author), sqlc.arg(podcast_type), sqlc.arg(language), sqlc.arg(owner_name), sqlc.arg(owner_email), sqlc.arg(explicit), sqlc.arg(prevent_indexing), sqlc.arg(cover_path), sqlc.arg(user_id), sqlc.arg(createdAt), sqlc.arg(updatedAt));

-- name: UpdateFeedFromEntity :exec
UPDATE feeds
SET entityUpdatedAt = sqlc.arg(entityUpdatedAt), imageURL = sqlc.arg(image_url), siteURL = sqlc.arg(site_url), title = sqlc.arg(title),
    description = sqlc.arg(description), author = sqlc.arg(author), podcastType = sqlc.arg(podcast_type),
    language = sqlc.arg(language), explicit = sqlc.arg(explicit), coverPath = sqlc.arg(cover_path), updatedAt = sqlc.arg(updatedAt)
WHERE id = sqlc.arg(id);

-- name: DeleteFeed :exec
DELETE FROM feeds WHERE id = ?;

-- name: DeleteLibraryItemFeeds :exec
DELETE FROM feeds WHERE entityType = 'libraryItem' AND entityId = CAST(sqlc.arg(item_id) AS TEXT);

-- name: DeleteLibraryItemFeedsByItemIDs :exec
DELETE FROM feeds WHERE entityType = 'libraryItem' AND entityId IN (sqlc.slice(item_ids));

-- name: ListFeedEpisodes :many
SELECT * FROM feedEpisodes WHERE feedId = CAST(sqlc.arg(feed_id) AS TEXT) ORDER BY rowid;

-- name: GetFeedEpisodeFilePath :one
SELECT fe.filePath FROM feedEpisodes fe
JOIN feeds f ON f.id = fe.feedId
WHERE f.slug = CAST(sqlc.arg(slug) AS TEXT) AND fe.id = CAST(sqlc.arg(episode_id) AS TEXT);

-- name: UpsertFeedEpisode :exec
-- New episodes get fresh ids, so the conflict clause only refreshes rows that
-- already exist; their createdAt and updatedAt stay as they were.
INSERT INTO feedEpisodes (id, title, author, description, siteURL, enclosureURL, enclosureType, enclosureSize, pubDate, season, episode, episodeType, duration, filePath, explicit, createdAt, updatedAt, feedId)
VALUES (sqlc.arg(id), sqlc.arg(title), sqlc.arg(author), sqlc.arg(description), sqlc.arg(site_url), sqlc.arg(enclosure_url), sqlc.arg(enclosure_type), sqlc.arg(enclosure_size), sqlc.arg(pub_date), sqlc.arg(season), sqlc.arg(episode), sqlc.arg(episode_type), sqlc.arg(duration), sqlc.arg(file_path), sqlc.arg(explicit), sqlc.arg(createdAt), sqlc.arg(updatedAt), CAST(sqlc.arg(feed_id) AS TEXT))
ON CONFLICT(id) DO UPDATE SET title = excluded.title, author = excluded.author, description = excluded.description,
    siteURL = excluded.siteURL, enclosureURL = excluded.enclosureURL, enclosureType = excluded.enclosureType,
    enclosureSize = excluded.enclosureSize, pubDate = excluded.pubDate, season = excluded.season,
    episode = excluded.episode, episodeType = excluded.episodeType, duration = excluded.duration,
    filePath = excluded.filePath, explicit = excluded.explicit;

-- name: DeleteFeedEpisode :exec
DELETE FROM feedEpisodes WHERE id = ?;
