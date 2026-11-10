-- Playback sessions as playback and userapi read and write them. Every column
-- but id, createdAt and updatedAt is nullable in the pinned schema, and real
-- installs hold NULLs, so the generated fields are all sql.Null* (see
-- store.PlaybackSessionFromRow). Column lists are explicit and in the model's
-- order, for the same reason as progress.sql. Timestamps are dbtime strings.

-- name: CountPlaybackSessionsForUser :one
SELECT COUNT(*) FROM playbackSessions WHERE sqlc.narg(user_id) IS NULL OR userId = sqlc.narg(user_id);

-- name: CountPlaybackSessionByID :one
SELECT COUNT(*) FROM playbackSessions WHERE id = ?;

-- name: ListPlaybackSessionsForUser :many
SELECT id, mediaItemId, mediaItemType, displayTitle, displayAuthor, duration, playMethod, mediaPlayer, startTime, currentTime, serverVersion, coverPath, timeListening, mediaMetadata, date, dayOfWeek, extraData, createdAt, updatedAt, userId, deviceId, libraryId
FROM playbackSessions
WHERE userId = sqlc.arg(user_id) AND (sqlc.narg(media_item_id) IS NULL OR mediaItemId = sqlc.narg(media_item_id))
ORDER BY updatedAt DESC;

-- name: ListPlaybackSessionsCreatedBetween :many
SELECT mediaItemType, timeListening, displayTitle, mediaMetadata, createdAt
FROM playbackSessions
WHERE userId = sqlc.arg(user_id) AND createdAt >= sqlc.arg(start_at) AND createdAt < sqlc.arg(end_at);

-- name: UpsertPlaybackSession :exec
INSERT INTO playbackSessions (id, mediaItemId, mediaItemType, displayTitle, displayAuthor, duration, playMethod, mediaPlayer, startTime, currentTime, serverVersion, coverPath, timeListening, mediaMetadata, date, dayOfWeek, extraData, userId, deviceId, libraryId, createdAt, updatedAt)
VALUES (sqlc.arg(id), sqlc.arg(media_item_id), sqlc.arg(media_item_type), sqlc.arg(display_title), sqlc.arg(display_author), sqlc.arg(duration), sqlc.arg(play_method), sqlc.arg(media_player), sqlc.arg(start_time), sqlc.arg(current_position), '2.37.2', sqlc.arg(cover_path), sqlc.arg(time_listening), sqlc.arg(media_metadata), sqlc.arg(date), sqlc.arg(day_of_week), sqlc.arg(extra_data), sqlc.arg(user_id), sqlc.arg(device_id), sqlc.arg(library_id), sqlc.arg(created_at), sqlc.arg(updated_at))
ON CONFLICT(id) DO UPDATE SET currentTime = excluded.currentTime, timeListening = excluded.timeListening, updatedAt = excluded.updatedAt, date = excluded.date, dayOfWeek = excluded.dayOfWeek;

-- name: DeletePlaybackSession :exec
DELETE FROM playbackSessions WHERE id = ?;

-- name: DeletePlaybackSessionsByIDs :execresult
DELETE FROM playbackSessions WHERE id IN (sqlc.slice(ids));

-- name: DetachPlaybackSessionsFromUser :execresult
UPDATE playbackSessions SET userId = NULL WHERE userId = ?;
