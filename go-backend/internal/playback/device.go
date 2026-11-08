package playback

import (
	"audiobookshelf-compat/internal/version"
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"

	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/deviceinfo"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/store"
)

// deviceInfo is the session's device DTO (DeviceInfo.js toJSON): empty values
// are omitted.
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

// clientDevice is the deviceInfo a client sends.
type clientDevice struct {
	DeviceID      string     `json:"deviceId"`
	ClientVersion string     `json:"clientVersion"`
	ClientName    string     `json:"clientName"`
	Manufacturer  string     `json:"manufacturer"`
	Model         string     `json:"model"`
	SDKVersion    sdkVersion `json:"sdkVersion"`
}

// sdkVersion is a JSON number (the Android app) or string, kept as text
// (DeviceInfo.setData, server/objects/DeviceInfo.js:100).
type sdkVersion string

func (v *sdkVersion) UnmarshalJSON(b []byte) error {
	var n json.Number
	if json.Unmarshal(b, &n) != nil {
		_ = json.Unmarshal(b, (*string)(v))
		return nil
	}
	*v = sdkVersion(n)
	return nil
}

func (h *Handler) upsertDevice(ctx context.Context, user store.User, r *http.Request, client clientDevice) (deviceInfo, error) {
	browser, browserVersion, osName, osVersion := deviceinfo.ParseUserAgent(r.UserAgent())
	clientVersion := cmp.Or(client.ClientVersion, version.Server)
	manufacturer, model, sdk := client.Manufacturer, client.Model, string(client.SDKVersion)
	clientName := client.ClientName
	if clientName == "" {
		switch {
		case sdk != "":
			clientName = "Abs Android"
		case model != "":
			clientName = "Abs iOS"
		case browser != "" && osName != "":
			clientName = "Abs Web"
		default:
			clientName = "Unknown"
		}
	}
	deviceName := ""
	// DeviceInfo.js:109/112: `${manufacturer || 'Unknown'} ${model || ''}`, untrimmed.
	switch {
	case sdk != "", model != "":
		deviceName = cmp.Or(manufacturer, "Unknown") + " " + model
	case browser != "":
		deviceName = osName + " " + cmp.Or(osVersion, "N/A") + " " + browser
	}
	ip := r.RemoteAddr
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	dbID, err := ids.New()
	if err != nil {
		return deviceInfo{}, err
	}
	deviceKey := cmp.Or(client.DeviceID, dbID)
	var existing string
	err = h.db.QueryRowContext(ctx, `SELECT id FROM devices WHERE deviceId=? AND userId=? LIMIT 1`, deviceKey, user.ID).Scan(&existing)
	if err == nil {
		dbID = existing
	} else if !errors.Is(err, sql.ErrNoRows) {
		return deviceInfo{}, err
	}
	extraRaw, _ := json.Marshal(struct {
		Manufacturer string `json:"manufacturer,omitempty"`
		Model        string `json:"model,omitempty"`
		OSName       string `json:"osName,omitempty"`
		OSVersion    string `json:"osVersion,omitempty"`
		BrowserName  string `json:"browserName,omitempty"`
	}{manufacturer, model, osName, osVersion, browser})
	version := cmp.Or(sdk, browserVersion)
	now := dbtime.Now()
	if existing == "" {
		_, err = h.db.ExecContext(ctx, `INSERT INTO devices (id,deviceId,clientName,clientVersion,ipAddress,deviceName,deviceVersion,extraData,userId,createdAt,updatedAt) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, dbID, deviceKey, clientName, clientVersion, ip, store.NullIfEmpty(deviceName), store.NullIfEmpty(version), string(extraRaw), user.ID, now, now)
	} else {
		_, err = h.db.ExecContext(ctx, `UPDATE devices SET clientName=?,clientVersion=?,ipAddress=?,deviceName=?,deviceVersion=?,extraData=?,updatedAt=? WHERE id=?`, clientName, clientVersion, ip, store.NullIfEmpty(deviceName), store.NullIfEmpty(version), string(extraRaw), now, dbID)
	}
	if err != nil {
		return deviceInfo{}, err
	}
	// The observed v2.37.2 playback-session device DTO omits deviceName,
	// manufacturer, model and sdkVersion entirely when no value exists
	// (PlaybackSession.js:86-104 / DeviceInfo.js:43-67 fixture behavior).
	return deviceInfo{ID: dbID, UserID: user.ID, DeviceID: deviceKey, IPAddress: ip, ClientVersion: clientVersion, ClientName: clientName, DeviceName: deviceName,
		BrowserName: browser, BrowserVersion: browserVersion, OSName: osName, OSVersion: osVersion, Manufacturer: manufacturer, Model: model, SDKVersion: sdk}, nil
}

func (d deviceInfo) description() string {
	if d.Model != "" {
		if d.SDKVersion != "" {
			return d.Model + " SDK " + d.SDKVersion + " / v" + d.ClientVersion
		}
		return d.Model + " / v" + d.ClientVersion
	}
	return d.OSName + " " + d.OSVersion + " / " + d.BrowserName
}
