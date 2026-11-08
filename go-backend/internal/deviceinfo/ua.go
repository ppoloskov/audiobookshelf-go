package deviceinfo

import "strings"

// Info is what a User-Agent tells about its client; unrecognised parts are "".
type Info struct {
	Browser, BrowserVersion, OSName, OSVersion, DeviceType string
}

// Parse extracts the browser, operating system and device type shared by
// device records and the session list. Recognition is intentionally narrow.
func Parse(ua string) (info Info) {
	browser, browserVersion, osName, osVersion := &info.Browser, &info.BrowserVersion, &info.OSName, &info.OSVersion
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
		*browser, *browserVersion = "Firefox", after("Firefox/")
	case strings.Contains(ua, "Edg/"):
		*browser, *browserVersion = "Edge", after("Edg/")
	case strings.Contains(ua, "Chrome/"):
		*browser, *browserVersion = "Chrome", after("Chrome/")
	case strings.Contains(ua, "Safari/"):
		*browser, *browserVersion = "Safari", after("Version/")
	}
	switch {
	case strings.Contains(ua, "Windows NT 10"):
		*osName, *osVersion = "Windows", "10"
	case strings.Contains(ua, "Mac OS X"):
		*osName, *osVersion = "Mac OS", strings.ReplaceAll(after("Mac OS X "), "_", ".")
	case strings.Contains(ua, "Android"):
		*osName, *osVersion = "Android", after("Android ")
	case strings.Contains(ua, "iPhone OS "):
		*osName, *osVersion = "iOS", strings.ReplaceAll(after("iPhone OS "), "_", ".")
	case strings.Contains(ua, "Linux"):
		*osName = "Linux"
	}
	switch {
	case strings.Contains(ua, "iPad"), strings.Contains(ua, "Tablet"), strings.Contains(ua, "Android") && !strings.Contains(ua, "Mobile"):
		info.DeviceType = "tablet"
	case strings.Contains(ua, "Mobile"), strings.Contains(ua, "iPhone"):
		info.DeviceType = "mobile"
	}
	return info
}
