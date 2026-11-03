package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"audiobookshelf-compat/internal/wire"
)

// LoadDeviceInfo reads a devices row by primary key and builds the legacy
// device object upstream attaches to stored playback sessions
// (server/models/PlaybackSession.js:97 -> Device.getOldDevice). Every column
// is nullable in the pinned schema. It returns nil, nil for an empty id.
func LoadDeviceInfo(ctx context.Context, db *sql.DB, id string) (*wire.DeviceInfo, error) {
	if id == "" {
		return nil, nil
	}
	var rowID, deviceID, userID, clientName, clientVersion, ip, deviceName, deviceVersion, extraRaw sql.NullString
	err := db.QueryRowContext(ctx, `SELECT id,deviceId,userId,clientName,clientVersion,ipAddress,deviceName,deviceVersion,extraData FROM devices WHERE id=?`, id).
		Scan(&rowID, &deviceID, &userID, &clientName, &clientVersion, &ip, &deviceName, &deviceVersion, &extraRaw)
	if err != nil {
		return nil, err
	}
	var extra struct {
		BrowserName  string `json:"browserName"`
		OSName       string `json:"osName"`
		OSVersion    string `json:"osVersion"`
		Manufacturer string `json:"manufacturer"`
		Model        string `json:"model"`
	}
	_ = json.Unmarshal([]byte(extraRaw.String), &extra)
	ptr := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	info := &wire.DeviceInfo{
		ID: NullStringPtr(rowID), UserID: NullStringPtr(userID), DeviceID: deviceID.String, IPAddress: NullStringPtr(ip),
		BrowserName: ptr(extra.BrowserName), OSName: ptr(extra.OSName), OSVersion: ptr(extra.OSVersion),
		ClientVersion: clientVersion.String, Manufacturer: extra.Manufacturer, Model: extra.Model,
		ClientName: clientName.String, DeviceName: deviceName.String,
	}
	// Device.getOldDevice: Android reports deviceVersion as sdkVersion, every
	// other client as browserVersion; `|| null` maps "" to null.
	if clientName.String == "Abs Android" {
		info.SDKVersion = deviceVersion.String
	} else {
		info.BrowserVersion = ptr(deviceVersion.String)
	}
	return info, nil
}
