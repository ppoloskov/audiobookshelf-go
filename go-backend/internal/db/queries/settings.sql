-- JSON documents kept one per row of the settings table (email-settings,
-- notification-settings). Written as TEXT, never BLOB.

-- name: GetSettingValue :one
SELECT value FROM settings WHERE key = ?;

-- name: UpsertSetting :exec
INSERT INTO settings (key, value, createdAt, updatedAt) VALUES (?, ?, ?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updatedAt = excluded.updatedAt;
