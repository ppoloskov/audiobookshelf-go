package sharing

import (
	"audiobookshelf-compat/internal/version"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"

	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/deviceinfo"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/store"
)

// shareDevice is PlaybackSessionManager.getDeviceInfo for a public share
// visit (server/managers/PlaybackSessionManager.js:52): DeviceInfo.setData
// with clientDeviceInfo {clientName: 'Abs Web Share', deviceId: shareSessionId}
// and no user. The devices row is reused by deviceId, otherwise created with a
// NULL userId. The returned map is DeviceInfo.toJSON, which drops null keys.
func (h *Handler) shareDevice(ctx context.Context, r *http.Request, shareSessionID string) (map[string]any, error) {
	browser, browserVersion, osName, osVersion := deviceinfo.ParseUserAgent(r.UserAgent())
	const clientName = "Abs Web Share"
	deviceName := ""
	if osName != "" && browser != "" {
		deviceName = osName + " " + orDefault(osVersion, "N/A") + " " + browser
	}
	ip := clientIP(r)
	extra := map[string]string{}
	for key, value := range map[string]string{"osName": osName, "osVersion": osVersion, "browserName": browser} {
		if value != "" {
			extra[key] = value
		}
	}
	extraRaw, _ := json.Marshal(extra)
	now := dbtime.Now()
	id, err := ids.New()
	if err != nil {
		return nil, err
	}
	var existing string
	err = h.db.QueryRowContext(ctx, `SELECT id FROM devices WHERE deviceId=? LIMIT 1`, shareSessionID).Scan(&existing)
	switch {
	case err == nil:
		id = existing
		_, err = h.db.ExecContext(ctx, `UPDATE devices SET clientName=?,clientVersion=?,ipAddress=?,deviceName=?,deviceVersion=?,extraData=?,updatedAt=? WHERE id=?`,
			clientName, version.Server, ip, store.NullIfEmpty(deviceName), store.NullIfEmpty(browserVersion), string(extraRaw), now, id)
	case errors.Is(err, sql.ErrNoRows):
		_, err = h.db.ExecContext(ctx, `INSERT INTO devices (id,deviceId,clientName,clientVersion,ipAddress,deviceName,deviceVersion,extraData,userId,createdAt,updatedAt) VALUES (?,?,?,?,?,?,?,?,NULL,?,?)`,
			id, shareSessionID, clientName, version.Server, ip, store.NullIfEmpty(deviceName), store.NullIfEmpty(browserVersion), string(extraRaw), now, now)
	}
	if err != nil {
		return nil, err
	}
	info := map[string]any{"id": id, "deviceId": shareSessionID, "clientVersion": version.Server, "clientName": clientName}
	for key, value := range map[string]string{"ipAddress": ip, "browserName": browser, "browserVersion": browserVersion, "osName": osName, "osVersion": osVersion, "deviceName": deviceName} {
		if value != "" {
			info[key] = value
		}
	}
	return info, nil
}

// clientIP approximates request-ip's getClientIp: the first X-Forwarded-For
// address, else the socket peer.
func clientIP(r *http.Request) string {
	if xff := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); xff != "" {
		return xff
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
