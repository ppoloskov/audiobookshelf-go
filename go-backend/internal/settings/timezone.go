package settings

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// hostTimeZone is the IANA name of the host zone, shown in the browser view
// (upstream getHostTimeZone). It comes from TZ or the /etc/localtime link and
// falls back to UTC. Unlike upstream it does not map aliases such as Etc/UTC
// to Node's canonical names. Computed once per process.
var hostTimeZone = sync.OnceValue(func() string {
	if tz := strings.TrimSpace(os.Getenv("TZ")); tz != "" {
		return tz
	}
	if link, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
		if _, name, found := strings.Cut(link, "zoneinfo/"); found && name != "" {
			return name
		}
	}
	return "UTC"
})
