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

// statBirthTime falls back to the modification time without a creation time.
func statBirthTime(path string, info os.FileInfo) time.Time {
	return info.ModTime()
}
