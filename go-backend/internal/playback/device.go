package playback

import (
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/version"
	"cmp"
	"context"
	"encoding/json"
	"net/http"

	"audiobookshelf-compat/internal/deviceinfo"
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
	ua := deviceinfo.Parse(r.UserAgent())
	clientVersion := cmp.Or(client.ClientVersion, version.Server)
	manufacturer, model, sdk := client.Manufacturer, client.Model, string(client.SDKVersion)
	clientName := client.ClientName
	if clientName == "" {
		switch {
		case sdk != "":
			clientName = "Abs Android"
		case model != "":
			clientName = "Abs iOS"
		case ua.Browser != "" && ua.OSName != "":
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
	case ua.Browser != "":
		deviceName = ua.OSName + " " + cmp.Or(ua.OSVersion, "N/A") + " " + ua.Browser
	}
	ip := httputil.PeerIP(r)
	id, deviceID, err := store.UpsertDevice(ctx, h.db, store.Device{
		DeviceID: client.DeviceID, UserID: user.ID, ClientName: clientName, ClientVersion: clientVersion, IPAddress: ip,
		DeviceName: deviceName, DeviceVersion: cmp.Or(sdk, ua.BrowserVersion), Manufacturer: manufacturer, Model: model, Info: ua,
	})
	if err != nil {
		return deviceInfo{}, err
	}
	// The observed v2.37.2 playback-session device DTO omits deviceName,
	// manufacturer, model and sdkVersion entirely when no value exists
	// (PlaybackSession.js:86-104 / DeviceInfo.js:43-67 fixture behavior).
	return deviceInfo{ID: id, UserID: user.ID, DeviceID: deviceID, IPAddress: ip, ClientVersion: clientVersion, ClientName: clientName, DeviceName: deviceName,
		BrowserName: ua.Browser, BrowserVersion: ua.BrowserVersion, OSName: ua.OSName, OSVersion: ua.OSVersion, Manufacturer: manufacturer, Model: model, SDKVersion: sdk}, nil
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
