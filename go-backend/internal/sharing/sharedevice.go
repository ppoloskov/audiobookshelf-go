package sharing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"

	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/store"
)

const serverVersion = "2.37.2"

// shareDevice is PlaybackSessionManager.getDeviceInfo for a public share
// visit (server/managers/PlaybackSessionManager.js:52): DeviceInfo.setData
// with clientDeviceInfo {clientName: 'Abs Web Share', deviceId: shareSessionId}
// and no user. The devices row is reused by deviceId, otherwise created with a
// NULL userId. The returned map is DeviceInfo.toJSON, which drops null keys.
func (h *Handler) shareDevice(ctx context.Context, r *http.Request, shareSessionID string) (map[string]any, error) {
	browser, browserVersion, osName, osVersion := parseUserAgent(r.UserAgent())
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
	id, err := uuid()
	if err != nil {
		return nil, err
	}
	var existing string
	err = h.db.QueryRowContext(ctx, `SELECT id FROM devices WHERE deviceId=? LIMIT 1`, shareSessionID).Scan(&existing)
	switch {
	case err == nil:
		id = existing
		_, err = h.db.ExecContext(ctx, `UPDATE devices SET clientName=?,clientVersion=?,ipAddress=?,deviceName=?,deviceVersion=?,extraData=?,updatedAt=? WHERE id=?`,
			clientName, serverVersion, ip, store.NullIfEmpty(deviceName), store.NullIfEmpty(browserVersion), string(extraRaw), now, id)
	case errors.Is(err, sql.ErrNoRows):
		_, err = h.db.ExecContext(ctx, `INSERT INTO devices (id,deviceId,clientName,clientVersion,ipAddress,deviceName,deviceVersion,extraData,userId,createdAt,updatedAt) VALUES (?,?,?,?,?,?,?,?,NULL,?,?)`,
			id, shareSessionID, clientName, serverVersion, ip, store.NullIfEmpty(deviceName), store.NullIfEmpty(browserVersion), string(extraRaw), now, now)
	}
	if err != nil {
		return nil, err
	}
	info := map[string]any{"id": id, "deviceId": shareSessionID, "clientVersion": serverVersion, "clientName": clientName}
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

// parseUserAgent recognizes the common browsers and systems. It mirrors the
// parser the playback package uses for its device rows; ua-parser-js
// recognizes far more user agents, which stay unverified.
func parseUserAgent(ua string) (browser, browserVersion, osName, osVersion string) {
	after := func(marker string) string {
		i := strings.Index(ua, marker)
		if i < 0 {
			return ""
		}
		v := ua[i+len(marker):]
		if j := strings.IndexAny(v, " ;)"); j >= 0 {
			v = v[:j]
		}
		return v
	}
	switch {
	case strings.Contains(ua, "Firefox/"):
		browser, browserVersion = "Firefox", after("Firefox/")
	case strings.Contains(ua, "Edg/"):
		browser, browserVersion = "Edge", after("Edg/")
	case strings.Contains(ua, "Chrome/"):
		browser, browserVersion = "Chrome", after("Chrome/")
	case strings.Contains(ua, "Safari/"):
		browser, browserVersion = "Safari", after("Version/")
	}
	switch {
	case strings.Contains(ua, "Windows NT 10"):
		osName, osVersion = "Windows", "10"
	case strings.Contains(ua, "Mac OS X"):
		osName, osVersion = "Mac OS", strings.ReplaceAll(after("Mac OS X "), "_", ".")
	case strings.Contains(ua, "Android"):
		osName, osVersion = "Android", after("Android ")
	case strings.Contains(ua, "iPhone OS "):
		osName, osVersion = "iOS", strings.ReplaceAll(after("iPhone OS "), "_", ".")
	case strings.Contains(ua, "Linux"):
		osName = "Linux"
	}
	return
}
