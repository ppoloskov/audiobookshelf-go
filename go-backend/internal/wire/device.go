package wire

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
