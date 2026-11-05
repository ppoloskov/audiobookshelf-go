package deviceinfo

import "strings"

// ParseUserAgent extracts the browser and operating system fields shared by
// playback and public-share device records. Recognition is intentionally narrow.
func ParseUserAgent(ua string) (browser, browserVersion, osName, osVersion string) {
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
		browser, browserVersion = "Firefox", after("Firefox/")
	case strings.Contains(ua, "Edg/"):
		browser, browserVersion = "Edge", after("Edg/")
	case strings.Contains(ua, "Chrome/"):
		browser, browserVersion = "Chrome", after("Chrome/")
	case strings.Contains(ua, "Safari/"):
		browser, browserVersion = "Safari", after("Version/")
	}
	switch {
	case strings.Contains(ua, "Windows NT 10"):
		osName, osVersion = "Windows", "10"
	case strings.Contains(ua, "Mac OS X"):
		osName, osVersion = "Mac OS", strings.ReplaceAll(after("Mac OS X "), "_", ".")
	case strings.Contains(ua, "Android"):
		osName, osVersion = "Android", after("Android ")
	case strings.Contains(ua, "iPhone OS "):
		osName, osVersion = "iOS", strings.ReplaceAll(after("iPhone OS "), "_", ".")
	case strings.Contains(ua, "Linux"):
		osName = "Linux"
	}
	return
}
