//go:build darwin

package scanner

import (
	"os"
	"syscall"
	"time"
)

// statChangeTime returns the inode change time from the OS stat record.
// Darwin exposes it as st.Ctimespec.
func statChangeTime(info os.FileInfo) time.Time {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return time.Unix(st.Ctimespec.Sec, st.Ctimespec.Nsec)
	}
	return info.ModTime()
}
