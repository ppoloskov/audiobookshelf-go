-- Collections and playlists as the user API and catalog read and write them.
-- Every parameter is named (sqlc.arg): sqlc numbers a mix of named and bare ?
-- placeholders out of step. Columns that are nullable in the schema take
-- CAST(sqlc.arg(...) AS TEXT) or AS INTEGER so the Go parameter is a plain
-- value; a NULL is written as the literal NULL. Timestamps are dbtime strings
-- written by the caller. Reads use SELECT * so each row maps to one sqlc model.

-- name: ListCollectionIDs :many
SELECT id FROM collections ORDER BY createdAt ASC;

-- name: ListLibraryCollectionIDs :many
SELECT id FROM collections WHERE libraryId = CAST(sqlc.arg(library_id) AS TEXT) ORDER BY createdAt ASC;

-- name: GetCollection :one
SELECT * FROM collections WHERE id = sqlc.arg(id);

-- name: GetCollectionLibraryID :one
SELECT libraryId FROM collections WHERE id = sqlc.arg(id);

-- name: InsertCollection :exec
INSERT INTO collections (id, name, description, libraryId, createdAt, updatedAt)
VALUES (sqlc.arg(id), CAST(sqlc.arg(name) AS TEXT), sqlc.arg(description), CAST(sqlc.arg(library_id) AS TEXT), sqlc.arg(createdAt), sqlc.arg(updatedAt));

-- name: UpdateCollection :exec
UPDATE collections
SET name = CAST(sqlc.arg(name) AS TEXT), description = sqlc.arg(description), updatedAt = sqlc.arg(updatedAt)
WHERE id = sqlc.arg(id);

-- name: DeleteCollection :exec
DELETE FROM collections WHERE id = ?;

-- name: DeleteCollectionsByLibraryID :exec
DELETE FROM collections WHERE libraryId = CAST(sqlc.arg(library_id) AS TEXT);

-- name: DeleteCollectionBooksByLibraryID :exec
DELETE FROM collectionBooks
WHERE collectionId IN (SELECT id FROM collections WHERE libraryId = CAST(sqlc.arg(library_id) AS TEXT));

-- name: ListCollectionBookItemIDs :many
SELECT li.id
FROM collectionBooks cb
JOIN libraryItems li ON li.mediaId = cb.bookId AND li.mediaType = 'book'
WHERE cb.collectionId = CAST(sqlc.arg(collection_id) AS TEXT)
ORDER BY cb."order" ASC;

-- name: ListCollectionBookLinks :many
SELECT cb.id AS link_id, li.id AS item_id, cb."order" AS position
FROM collectionBooks cb
JOIN libraryItems li ON li.mediaId = cb.bookId AND li.mediaType = 'book'
WHERE cb.collectionId = CAST(sqlc.arg(collection_id) AS TEXT);

-- name: ListCollectionBookSources :many
SELECT b.id, b.title, b.description, b.coverPath, b.language, b.explicit, b.chapters, b.audioFiles, b.updatedAt,
       li.id AS item_id, li.libraryId AS library_id, li.createdAt AS item_created_at, li.updatedAt AS item_updated_at
FROM collectionBooks cb
JOIN books b ON b.id = cb.bookId
JOIN libraryItems li ON li.mediaId = b.id AND li.mediaType = 'book'
WHERE cb.collectionId = CAST(sqlc.arg(collection_id) AS TEXT)
ORDER BY cb."order" ASC;

-- name: ListCollectionBooks :many
SELECT * FROM collectionBooks WHERE collectionId = CAST(sqlc.arg(collection_id) AS TEXT) ORDER BY "order" ASC;

-- name: ListCollectionBookMediaIDs :many
SELECT bookId FROM collectionBooks WHERE collectionId = CAST(sqlc.arg(collection_id) AS TEXT);

-- name: CountCollectionBooks :one
SELECT COUNT(*) FROM collectionBooks WHERE collectionId = CAST(sqlc.arg(collection_id) AS TEXT);

-- name: CountCollectionBook :one
SELECT COUNT(*) FROM collectionBooks
WHERE collectionId = CAST(sqlc.arg(collection_id) AS TEXT) AND bookId = CAST(sqlc.arg(book_id) AS TEXT);

-- name: InsertCollectionBook :exec
INSERT INTO collectionBooks (id, collectionId, bookId, "order", createdAt)
VALUES (sqlc.arg(id), CAST(sqlc.arg(collection_id) AS TEXT), CAST(sqlc.arg(book_id) AS TEXT), CAST(sqlc.arg(position) AS INTEGER), sqlc.arg(createdAt));

-- name: UpdateCollectionBookOrder :exec
UPDATE collectionBooks SET "order" = CAST(sqlc.arg(position) AS INTEGER) WHERE id = sqlc.arg(id);

-- name: DeleteCollectionBook :exec
DELETE FROM collectionBooks WHERE id = ?;

-- name: DeleteCollectionBookByMediaID :execresult
DELETE FROM collectionBooks
WHERE collectionId = CAST(sqlc.arg(collection_id) AS TEXT) AND bookId = CAST(sqlc.arg(book_id) AS TEXT);

-- name: ListUserPlaylistIDs :many
SELECT id FROM playlists WHERE userId = CAST(sqlc.arg(user_id) AS TEXT);

-- name: ListUserLibraryPlaylistIDs :many
SELECT id FROM playlists
WHERE userId = CAST(sqlc.arg(user_id) AS TEXT) AND libraryId = CAST(sqlc.arg(library_id) AS TEXT);

-- name: CountUserLibraryPlaylists :one
SELECT COUNT(*) FROM playlists
WHERE userId = CAST(sqlc.arg(user_id) AS TEXT) AND libraryId = CAST(sqlc.arg(library_id) AS TEXT);

-- name: GetPlaylist :one
SELECT * FROM playlists WHERE id = sqlc.arg(id);

-- name: InsertPlaylist :exec
INSERT INTO playlists (id, name, description, libraryId, userId, createdAt, updatedAt)
VALUES (sqlc.arg(id), CAST(sqlc.arg(name) AS TEXT), sqlc.arg(description), CAST(sqlc.arg(library_id) AS TEXT), CAST(sqlc.arg(user_id) AS TEXT), sqlc.arg(createdAt), sqlc.arg(updatedAt));

-- name: UpdatePlaylist :exec
UPDATE playlists
SET name = CAST(sqlc.arg(name) AS TEXT), description = sqlc.arg(description), updatedAt = sqlc.arg(updatedAt)
WHERE id = sqlc.arg(id);

-- name: DeletePlaylist :exec
DELETE FROM playlists WHERE id = ?;

-- name: DeleteUserPlaylists :exec
DELETE FROM playlists WHERE userId = CAST(sqlc.arg(user_id) AS TEXT);

-- name: ListPlaylistMediaItems :many
SELECT * FROM playlistMediaItems WHERE playlistId = CAST(sqlc.arg(playlist_id) AS TEXT) ORDER BY "order" ASC;

-- name: ListPlaylistMediaItemIDs :many
SELECT id FROM playlistMediaItems WHERE playlistId = CAST(sqlc.arg(playlist_id) AS TEXT) ORDER BY "order";

-- name: CountPlaylistMediaItems :one
SELECT COUNT(*) FROM playlistMediaItems WHERE playlistId = CAST(sqlc.arg(playlist_id) AS TEXT);

-- name: CountPlaylistMediaItem :one
SELECT COUNT(*) FROM playlistMediaItems
WHERE playlistId = CAST(sqlc.arg(playlist_id) AS TEXT) AND mediaItemId = CAST(sqlc.arg(media_id) AS TEXT);

-- name: InsertPlaylistMediaItem :exec
INSERT INTO playlistMediaItems (id, mediaItemId, mediaItemType, "order", playlistId, createdAt)
VALUES (sqlc.arg(id), CAST(sqlc.arg(media_id) AS TEXT), CAST(sqlc.arg(media_type) AS TEXT), CAST(sqlc.arg(position) AS INTEGER), CAST(sqlc.arg(playlist_id) AS TEXT), sqlc.arg(createdAt));

-- name: UpdatePlaylistMediaItemOrder :exec
UPDATE playlistMediaItems SET "order" = CAST(sqlc.arg(position) AS INTEGER) WHERE id = sqlc.arg(id);

-- name: DeletePlaylistMediaItem :exec
DELETE FROM playlistMediaItems WHERE id = ?;

-- name: DeletePlaylistMediaItems :exec
DELETE FROM playlistMediaItems WHERE playlistId = CAST(sqlc.arg(playlist_id) AS TEXT);

