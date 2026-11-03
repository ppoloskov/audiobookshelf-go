-- Refresh-token sessions as identity reads and writes them. Timestamps are
-- written as dbtime strings by the caller; NULL literals are kept as written.

-- name: InsertSession :exec
INSERT INTO sessions (id, ipAddress, userAgent, refreshToken, expiresAt, createdAt, updatedAt, userId, lastRefreshToken, lastRefreshTokenExpiresAt)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL);

-- name: RotateSession :execresult
UPDATE sessions
SET refreshToken = sqlc.arg(new_refresh_token),
    expiresAt = sqlc.arg(expires_at),
    lastRefreshToken = sqlc.arg(last_refresh_token),
    lastRefreshTokenExpiresAt = sqlc.arg(last_refresh_token_expires_at),
    updatedAt = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND refreshToken = sqlc.arg(current_refresh_token);

-- name: GetSessionRefreshToken :one
SELECT refreshToken FROM sessions WHERE id = ?;

-- name: GetSessionByToken :one
SELECT id, userId, refreshToken, expiresAt, lastRefreshToken, lastRefreshTokenExpiresAt
FROM sessions
WHERE refreshToken = sqlc.arg(token) OR lastRefreshToken = sqlc.arg(token)
LIMIT 1;

-- name: GetSessionUserIDByToken :one
SELECT userId FROM sessions
WHERE refreshToken = sqlc.arg(token) OR lastRefreshToken = sqlc.arg(token)
LIMIT 1;

-- name: DeleteSessionByRefreshToken :exec
DELETE FROM sessions WHERE refreshToken = ?;

-- name: DeleteSessionByID :exec
DELETE FROM sessions WHERE id = ?;

-- name: DeleteSessionsByUserID :exec
DELETE FROM sessions WHERE userId = ?;

-- name: DeleteOtherSessionsByUserID :exec
DELETE FROM sessions WHERE userId = ? AND id <> ?;
