package share

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/deviceinfo"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/version"
)

// shareClientName is the clientName of devices created by share visits.
const shareClientName = "Abs Web Share"

// deviceInfo is DeviceInfo.toJSON of a share visit: the keys with a value.
type deviceInfo struct {
	ID             string `json:"id"`
	DeviceID       string `json:"deviceId"`
	ClientVersion  string `json:"clientVersion"`
	ClientName     string `json:"clientName"`
	IPAddress      string `json:"ipAddress,omitempty"`
	BrowserName    string `json:"browserName,omitempty"`
	BrowserVersion string `json:"browserVersion,omitempty"`
	OSName         string `json:"osName,omitempty"`
	OSVersion      string `json:"osVersion,omitempty"`
	DeviceName     string `json:"deviceName,omitempty"`
}

// recordDevice writes the devices row of a share visit (PlaybackSessionManager.js:52):
// keyed by the share session id as deviceId, with no user; an earlier row of
// the same id is updated.
func (h *Handler) recordDevice(ctx context.Context, r *http.Request, sessionID string) (deviceInfo, error) {
	ua := deviceinfo.Parse(r.UserAgent())
	deviceName := ""
	if ua.OSName != "" && ua.Browser != "" {
		deviceName = ua.OSName + " " + orDefault(ua.OSVersion, "N/A") + " " + ua.Browser
	}
	ip := httpx.ClientIP(r)
	row := db.Device{
		DeviceID: &sessionID, ClientName: db.Ptr(shareClientName), ClientVersion: db.Ptr(version.Server), IPAddress: &ip,
		DeviceName: nilIfEmpty(deviceName), DeviceVersion: nilIfEmpty(ua.BrowserVersion),
		ExtraData: db.JSON[db.DeviceExtra]{V: db.DeviceExtra{OSName: ua.OSName, OSVersion: ua.OSVersion, BrowserName: ua.Browser}},
		UpdatedAt: db.Now(),
	}
	err := h.db.Tx(ctx, func(ctx context.Context) error {
		err := h.db.Get(ctx, &row.ID, `SELECT id FROM devices WHERE deviceId = ? AND userId IS NULL LIMIT 1`, sessionID)
		switch {
		case err == nil:
			return h.db.Update(ctx, "devices", &row, "clientName", "clientVersion", "ipAddress", "deviceName", "deviceVersion", "extraData", "updatedAt")
		case errors.Is(err, sql.ErrNoRows):
			if row.ID, err = ids.New(); err != nil {
				return fmt.Errorf("new device id: %w", err)
			}
			row.CreatedAt = row.UpdatedAt
			return h.db.Insert(ctx, "devices", &row)
		}
		return fmt.Errorf("find device: %w", err)
	})
	if err != nil {
		return deviceInfo{}, fmt.Errorf("record share device: %w", err)
	}
	return deviceInfo{
		ID: row.ID, DeviceID: sessionID, ClientVersion: version.Server, ClientName: shareClientName, IPAddress: ip,
		BrowserName: ua.Browser, BrowserVersion: ua.BrowserVersion, OSName: ua.OSName, OSVersion: ua.OSVersion, DeviceName: deviceName,
	}, nil
}
