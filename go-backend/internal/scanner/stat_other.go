//go:build !linux && !darwin

package scanner

import (
	"os"
	"time"
)

// statChangeTime falls back to the modification time when the platform stat
// record does not expose a change time.
func statChangeTime(info os.FileInfo) time.Time {
	return info.ModTime()
}
