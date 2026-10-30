//go:build linux

package scanner

import (
	"os"
	"syscall"
	"time"
)

// statChangeTime returns the inode change time from the OS stat record.
// Linux exposes it as st.Ctim.
func statChangeTime(info os.FileInfo) time.Time {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return time.Unix(st.Ctim.Sec, st.Ctim.Nsec)
	}
	return info.ModTime()
}
