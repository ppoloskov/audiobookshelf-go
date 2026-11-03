-- Media progress as userapi, playback and progressdto read and write it.
-- Column lists are explicit and in the model's order: a database upgraded by
-- an older TS version can store columns in a different physical order, and
-- SELECT * would then map them to the wrong fields. Timestamps are written as
-- dbtime strings by the caller; literal values (1, 0, '[]') are kept as written.

-- name: GetMediaProgressByUserItem :one
SELECT id, mediaItemId, mediaItemType, duration, currentTime, isFinished, hideFromContinueListening, ebookLocation, ebookProgress, finishedAt, extraData, podcastId, createdAt, updatedAt, userId
FROM mediaProgresses
WHERE userId = sqlc.arg(user_id) AND mediaItemId = sqlc.arg(media_item_id)
LIMIT 1;

-- name: GetMediaProgressPosition :one
SELECT currentTime, isFinished
FROM mediaProgresses
WHERE userId = sqlc.arg(user_id) AND mediaItemId = sqlc.arg(media_item_id) AND mediaItemType = sqlc.arg(media_item_type)
LIMIT 1;

-- name: ListMediaProgressByUser :many
SELECT id, mediaItemId, mediaItemType, duration, currentTime, isFinished, hideFromContinueListening, ebookLocation, ebookProgress, finishedAt, extraData, podcastId, createdAt, updatedAt, userId
FROM mediaProgresses
WHERE userId = ?
ORDER BY mediaItemId, isFinished, currentTime;

-- name: ListItemsInProgress :many
SELECT id, mediaItemId, mediaItemType, extraData, updatedAt
FROM mediaProgresses
WHERE userId = ? AND isFinished = 0 AND (currentTime > 0 OR ebookProgress > 0)
ORDER BY updatedAt DESC;

-- name: ListFinishedBookProgressBetween :many
SELECT mp.mediaItemId, b.title, mp.duration, mp.finishedAt, li.id AS libraryItemId, b.coverPath
FROM mediaProgresses mp
JOIN books b ON b.id = mp.mediaItemId
JOIN libraryItems li ON li.mediaId = b.id AND li.mediaType = 'book'
WHERE mp.userId = sqlc.arg(user_id) AND mp.mediaItemType = 'book'
  AND mp.finishedAt >= sqlc.arg(start_at) AND mp.finishedAt < sqlc.arg(end_at);

-- name: InsertMediaProgress :exec
INSERT INTO mediaProgresses (id, mediaItemId, mediaItemType, duration, currentTime, isFinished, hideFromContinueListening, ebookLocation, ebookProgress, finishedAt, extraData, podcastId, userId, createdAt, updatedAt)
VALUES (sqlc.arg(id), sqlc.arg(media_item_id), sqlc.arg(media_item_type), sqlc.arg(duration), sqlc.arg(current_position), sqlc.arg(is_finished), sqlc.arg(hide_from_continue_listening), sqlc.arg(ebook_location), sqlc.arg(ebook_progress), sqlc.arg(finished_at), sqlc.arg(extra_data), sqlc.arg(podcast_id), sqlc.arg(user_id), sqlc.arg(created_at), sqlc.arg(updated_at));

-- name: UpdateMediaProgressState :exec
UPDATE mediaProgresses
SET duration = sqlc.arg(duration), currentTime = sqlc.arg(current_position), isFinished = sqlc.arg(is_finished),
    hideFromContinueListening = sqlc.arg(hide_from_continue_listening), ebookLocation = sqlc.arg(ebook_location),
    ebookProgress = sqlc.arg(ebook_progress), finishedAt = sqlc.arg(finished_at), extraData = sqlc.arg(extra_data),
    updatedAt = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id);

-- name: UpdateMediaProgressUpdatedAt :exec
UPDATE mediaProgresses SET updatedAt = ? WHERE id = ?;

-- name: HideMediaProgressFromContinueListening :execresult
UPDATE mediaProgresses SET hideFromContinueListening = 1, updatedAt = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND userId = sqlc.arg(user_id);

-- name: DeleteMediaProgressForUser :execresult
DELETE FROM mediaProgresses WHERE id = sqlc.arg(id) AND userId = sqlc.arg(user_id);
