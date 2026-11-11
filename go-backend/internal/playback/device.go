package playback

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/deviceinfo"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/version"
)

// deviceInfo is the device a session was started from (DeviceInfo.toJSON): keys
// without a value are left out, as the v2.37.2 session response does.
type deviceInfo struct {
	ID             string `json:"id"`
	UserID         string `json:"userId"`
	DeviceID       string `json:"deviceId"`
	IPAddress      string `json:"ipAddress"`
	ClientVersion  string `json:"clientVersion"`
	ClientName     string `json:"clientName"`
	DeviceName     string `json:"deviceName,omitempty"`
	BrowserName    string `json:"browserName,omitempty"`
	BrowserVersion string `json:"browserVersion,omitempty"`
	OSName         string `json:"osName,omitempty"`
	OSVersion      string `json:"osVersion,omitempty"`
	Manufacturer   string `json:"manufacturer,omitempty"`
	Model          string `json:"model,omitempty"`
	SDKVersion     string `json:"sdkVersion,omitempty"`
}

// description is the device as the server log and progress events name it.
func (d deviceInfo) description() string {
	if d.Model != "" {
		if d.SDKVersion != "" {
			return d.Model + " SDK " + d.SDKVersion + " / v" + d.ClientVersion
		}
		return d.Model + " / v" + d.ClientVersion
	}
	return d.OSName + " " + d.OSVersion + " / " + d.BrowserName
}

// clientDevice is the deviceInfo object a client sends.
type clientDevice struct {
	DeviceID      string     `json:"deviceId"`
	ClientVersion string     `json:"clientVersion"`
	ClientName    string     `json:"clientName"`
	Manufacturer  string     `json:"manufacturer"`
	Model         string     `json:"model"`
	SDKVersion    sdkVersion `json:"sdkVersion"`
}

// sdkVersion is a JSON number (the Android app sends one) or string, kept as
// text (DeviceInfo.setData).
type sdkVersion string

// UnmarshalJSON reads a number or a string; any other type reads as empty.
func (v *sdkVersion) UnmarshalJSON(data []byte) error {
	var n json.Number
	if json.Unmarshal(data, &n) == nil {
		*v = sdkVersion(n)
		return nil
	}
	var text string
	_ = json.Unmarshal(data, &text) // other types read as no version
	*v = sdkVersion(text)
	return nil
}

// upsertDevice writes the user's devices row for this client (created, or
// updated when the same deviceId was seen before) and describes it.
func (h *Handler) upsertDevice(ctx context.Context, r *http.Request, user *auth.Principal, client clientDevice) (deviceInfo, error) {
	ua := deviceinfo.Parse(r.UserAgent())
	sdk := string(client.SDKVersion)
	clientName := client.ClientName
	if clientName == "" {
		switch {
		case sdk != "":
			clientName = "Abs Android"
		case client.Model != "":
			clientName = "Abs iOS"
		case ua.Browser != "" && ua.OSName != "":
			clientName = "Abs Web"
		default:
			clientName = "Unknown"
		}
	}
	deviceName := ""
	switch {
	case sdk != "", client.Model != "":
		deviceName = cmp.Or(client.Manufacturer, "Unknown") + " " + client.Model // DeviceInfo.js: untrimmed
	case ua.Browser != "":
		deviceName = ua.OSName + " " + cmp.Or(ua.OSVersion, "N/A") + " " + ua.Browser
	}
	info := deviceInfo{
		UserID: user.ID, DeviceID: client.DeviceID, IPAddress: httpx.ClientIP(r), ClientVersion: cmp.Or(client.ClientVersion, version.Server),
		ClientName: clientName, DeviceName: deviceName, BrowserName: ua.Browser, BrowserVersion: ua.BrowserVersion,
		OSName: ua.OSName, OSVersion: ua.OSVersion, Manufacturer: client.Manufacturer, Model: client.Model, SDKVersion: sdk,
	}
	row := db.Device{
		ClientName: &info.ClientName, ClientVersion: &info.ClientVersion, IPAddress: &info.IPAddress,
		DeviceName: nilIfEmpty(deviceName), DeviceVersion: nilIfEmpty(cmp.Or(sdk, ua.BrowserVersion)),
		ExtraData: db.JSON[db.DeviceExtra]{V: db.DeviceExtra{Manufacturer: client.Manufacturer, Model: client.Model, OSName: ua.OSName, OSVersion: ua.OSVersion, BrowserName: ua.Browser}},
		UpdatedAt: db.Now(), UserID: &user.ID,
	}
	err := h.db.Tx(ctx, func(ctx context.Context) error {
		newID, err := ids.New()
		if err != nil {
			return fmt.Errorf("new device id: %w", err)
		}
		info.DeviceID = cmp.Or(client.DeviceID, newID) // a client without a deviceId is known by its row id
		row.DeviceID = &info.DeviceID
		err = h.db.Get(ctx, &row.ID, `SELECT id FROM devices WHERE deviceId = ? AND userId = ? LIMIT 1`, info.DeviceID, user.ID)
		switch {
		case err == nil:
			return h.db.Update(ctx, "devices", &row, "clientName", "clientVersion", "ipAddress", "deviceName", "deviceVersion", "extraData", "updatedAt")
		case errors.Is(err, sql.ErrNoRows):
			row.ID, row.CreatedAt = newID, row.UpdatedAt
			return h.db.Insert(ctx, "devices", &row)
		}
		return err
	})
	if err != nil {
		return deviceInfo{}, fmt.Errorf("record device: %w", err)
	}
	info.ID = row.ID
	return info, nil
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// storedDevice is a devices row as the session history shows it
// (Device.getOldDevice through DeviceInfo.toJSON): the text fields are always
// present, the rest only with a value.
type storedDevice struct {
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

func storedDeviceOf(row db.Device) storedDevice {
	extra := row.ExtraData.V
	d := storedDevice{
		ID: &row.ID, UserID: row.UserID, DeviceID: db.Val(row.DeviceID), IPAddress: row.IPAddress,
		BrowserName: nilIfEmpty(extra.BrowserName), OSName: nilIfEmpty(extra.OSName), OSVersion: nilIfEmpty(extra.OSVersion),
		ClientVersion: db.Val(row.ClientVersion), Manufacturer: extra.Manufacturer, Model: extra.Model,
		ClientName: db.Val(row.ClientName), DeviceName: db.Val(row.DeviceName),
	}
	// Device.getOldDevice: Android keeps its SDK version in deviceVersion, everyone else a browser version.
	if d.ClientName == "Abs Android" {
		d.SDKVersion = db.Val(row.DeviceVersion)
	} else {
		d.BrowserVersion = nilIfEmpty(db.Val(row.DeviceVersion))
	}
	return d
}
