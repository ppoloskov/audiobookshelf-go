package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// LoadDeviceInfo reads a devices row by primary key and builds the legacy
// device object upstream attaches to stored playback sessions
// (server/models/PlaybackSession.js:97 -> Device.getOldDevice). Every column
// is nullable in the pinned schema. It returns nil, nil for an empty id.
func LoadDeviceInfo(ctx context.Context, db *sql.DB, id string) (*DeviceInfo, error) {
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
	info := &DeviceInfo{
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

// DeviceInfo is the legacy device object attached to stored playback
// sessions: upstream Device.getOldDevice (server/models/Device.js:149)
// wrapped in objects/DeviceInfo and serialized by DeviceInfo.toJSON
// (server/objects/DeviceInfo.js:43).
//
// Two upstream quirks decide the shape:
//   - DeviceInfo.construct passes the string fields through stripAllTags,
//     which turns null into "" (server/utils/htmlSanitizer.js:29), so those
//     keys are always present.
//   - toJSON deletes every other key whose value is null or undefined.
//
// Pointer fields with omitempty model the deleted keys; plain strings model
// the stripAllTags fields. Field order matches toJSON.
type DeviceInfo struct {
	ID             *string `json:"id,omitempty"`
	UserID         *string `json:"userId,omitempty"`
	DeviceID       string  `json:"deviceId"`
	IPAddress      *string `json:"ipAddress,omitempty"`
	BrowserName    *string `json:"browserName,omitempty"`
	BrowserVersion *string `json:"browserVersion,omitempty"`
	OSName         *string `json:"osName,omitempty"`
	OSVersion      *string `json:"osVersion,omitempty"`
	ClientVersion  string  `json:"clientVersion"`
	Manufacturer   string  `json:"manufacturer"`
	Model          string  `json:"model"`
	SDKVersion     string  `json:"sdkVersion"`
	ClientName     string  `json:"clientName"`
	DeviceName     string  `json:"deviceName"`
}
