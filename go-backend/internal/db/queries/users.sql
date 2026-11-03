-- Users as identity and store read and write them today. Literal SQL values
-- (NULL, 'root', '[]') are kept as written so the stored rows are unchanged.
-- Lookups use SELECT * so every row maps to one sqlc model (see store.UserFromRow).

-- name: GetUserByID :one
SELECT * FROM users WHERE id = ? LIMIT 1;

-- name: GetUserByUsername :one
SELECT * FROM users WHERE lower(username) = ? LIMIT 1;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE lower(email) = ? LIMIT 1;

-- name: GetUserByOIDCSub :one
SELECT * FROM users
WHERE (CASE WHEN json_valid(extraData) THEN json_extract(extraData, '$.authOpenIDSub') END) = CAST(sqlc.arg(sub) AS TEXT)
LIMIT 1;

-- name: GetUserIDByOldUserID :one
SELECT id FROM users
WHERE (CASE WHEN json_valid(extraData) THEN json_extract(extraData, '$.oldUserId') END) = CAST(sqlc.arg(old_user_id) AS TEXT)
LIMIT 1;

-- name: GetUserEmail :one
SELECT email FROM users WHERE id = ?;

-- name: CountRootUsers :one
SELECT COUNT(*) FROM users WHERE type = 'root';

-- name: InsertRootUser :exec
INSERT INTO users (id, username, email, pash, type, token, isActive, isLocked, lastSeen, permissions, bookmarks, extraData, createdAt, updatedAt)
VALUES (?, ?, NULL, ?, 'root', ?, 1, 0, NULL, ?, '[]', ?, ?, ?);

-- name: InsertOIDCUser :exec
INSERT INTO users (id, username, email, pash, type, token, isActive, isLocked, lastSeen, permissions, bookmarks, extraData, createdAt, updatedAt)
VALUES (?, ?, ?, NULL, 'user', ?, 1, 0, NULL, ?, '[]', ?, ?, ?);

-- name: UpdateUserType :exec
UPDATE users SET type = ?, updatedAt = ? WHERE id = ?;

-- name: UpdateUserPermissions :exec
UPDATE users SET permissions = ?, updatedAt = ? WHERE id = ?;

-- name: UpdateUserExtraData :exec
UPDATE users SET extraData = ?, updatedAt = ? WHERE id = ?;

-- name: UpdateUserPassword :exec
UPDATE users SET pash = ?, updatedAt = ? WHERE id = ?;

-- name: DeleteUser :exec
DELETE FROM users WHERE id = ?;
