-- API keys as identity authenticates and administers them. Timestamps are
-- written as dbtime strings by the caller.

-- name: ListApiKeys :many
SELECT * FROM apiKeys;

-- name: GetApiKey :one
SELECT * FROM apiKeys WHERE id = ?;

-- name: InsertApiKey :exec
INSERT INTO apiKeys (id, name, description, expiresAt, lastUsedAt, isActive, permissions, userId, createdByUserId, createdAt, updatedAt)
VALUES (?, ?, NULL, ?, NULL, ?, NULL, ?, ?, ?, ?);

-- name: DeleteApiKey :execresult
DELETE FROM apiKeys WHERE id = ?;

-- name: UpdateApiKeyOwner :exec
UPDATE apiKeys SET userId = sqlc.arg(user_id), isActive = coalesce(sqlc.narg(is_active), isActive), updatedAt = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id);
