package sharing

import (
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/version"
	"context"
	"net/http"

	"audiobookshelf-compat/internal/deviceinfo"
	"audiobookshelf-compat/internal/store"
)

// shareDevice is PlaybackSessionManager.getDeviceInfo for a public share
// visit (server/managers/PlaybackSessionManager.js:52): DeviceInfo.setData
// with clientDeviceInfo {clientName: 'Abs Web Share', deviceId: shareSessionId}
// and no user. The devices row is reused by deviceId, otherwise created with a
// NULL userId. The returned map is DeviceInfo.toJSON, which drops null keys.
func (h *Handler) shareDevice(ctx context.Context, r *http.Request, shareSessionID string) (map[string]any, error) {
	ua := deviceinfo.Parse(r.UserAgent())
	const clientName = "Abs Web Share"
	deviceName := ""
	if ua.OSName != "" && ua.Browser != "" {
		deviceName = ua.OSName + " " + orDefault(ua.OSVersion, "N/A") + " " + ua.Browser
	}
	ip := httputil.ClientIP(r)
	id, _, err := store.UpsertDevice(ctx, h.db, store.Device{
		DeviceID: shareSessionID, ClientName: clientName, ClientVersion: version.Server, IPAddress: ip,
		DeviceName: deviceName, DeviceVersion: ua.BrowserVersion, Info: ua,
	})
	if err != nil {
		return nil, err
	}
	info := map[string]any{"id": id, "deviceId": shareSessionID, "clientVersion": version.Server, "clientName": clientName}
	for key, value := range map[string]string{"ipAddress": ip, "browserName": ua.Browser, "browserVersion": ua.BrowserVersion, "osName": ua.OSName, "osVersion": ua.OSVersion, "deviceName": deviceName} {
		if value != "" {
			info[key] = value
		}
	}
	return info, nil
}
