package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/deviceinfo"
	"audiobookshelf-compat/internal/ids"
)

// Device is one row of the devices table as DeviceInfo.setData builds it
// (server/objects/DeviceInfo.js). The row is keyed by DeviceID and UserID;
// an empty UserID is a public-share visit (NULL userId).
type Device struct {
	DeviceID      string // "" takes the new row's id, as DeviceInfo.js does
	UserID        string
	ClientName    string
	ClientVersion string
	IPAddress     string
	DeviceName    string
	DeviceVersion string // sdkVersion, else the browser version
	Manufacturer  string
	Model         string
	Info          deviceinfo.Info
}

// UpsertDevice writes the device row, reusing the existing row of the same
// DeviceID and user, and returns its row id and device id.
func UpsertDevice(ctx context.Context, db *sql.DB, d Device) (id, deviceID string, err error) {
	if id, err = ids.New(); err != nil {
		return "", "", err
	}
	deviceID = d.DeviceID
	if deviceID == "" {
		deviceID = id
	}
	var userID any
	if d.UserID != "" {
		userID = d.UserID
	}
	var existing string
	err = db.QueryRowContext(ctx, `SELECT id FROM devices WHERE deviceId=? AND userId IS ? LIMIT 1`, deviceID, userID).Scan(&existing)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}
	extra, _ := json.Marshal(struct {
		Manufacturer string `json:"manufacturer,omitempty"`
		Model        string `json:"model,omitempty"`
		OSName       string `json:"osName,omitempty"`
		OSVersion    string `json:"osVersion,omitempty"`
		BrowserName  string `json:"browserName,omitempty"`
	}{d.Manufacturer, d.Model, d.Info.OSName, d.Info.OSVersion, d.Info.Browser})
	name, deviceVersion, now := NullIfEmpty(d.DeviceName), NullIfEmpty(d.DeviceVersion), dbtime.Now()
	if existing != "" {
		_, err = db.ExecContext(ctx, `UPDATE devices SET clientName=?,clientVersion=?,ipAddress=?,deviceName=?,deviceVersion=?,extraData=?,updatedAt=? WHERE id=?`,
			d.ClientName, d.ClientVersion, d.IPAddress, name, deviceVersion, string(extra), now, existing)
		return existing, deviceID, err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO devices (id,deviceId,clientName,clientVersion,ipAddress,deviceName,deviceVersion,extraData,userId,createdAt,updatedAt) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		id, deviceID, d.ClientName, d.ClientVersion, d.IPAddress, name, deviceVersion, string(extra), userID, now, now)
	return id, deviceID, err
}
