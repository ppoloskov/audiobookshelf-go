//go:build linux

package scanner

import (
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// statChangeTime returns the inode change time from the OS stat record.
// Linux exposes it as st.Ctim.
func statChangeTime(info os.FileInfo) time.Time {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return time.Unix(st.Ctim.Sec, st.Ctim.Nsec)
	}
	return info.ModTime()
}

// statBirthTime returns the file creation time (statx btime), which is what
// Node reports as birthtimeMs on Linux. Filesystems without btime report the
// Unix epoch there, so that is returned for them as well.
func statBirthTime(path string, info os.FileInfo) time.Time {
	var sx unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, 0, unix.STATX_BTIME, &sx); err != nil || sx.Mask&unix.STATX_BTIME == 0 {
		return time.Unix(0, 0)
	}
	return time.Unix(sx.Btime.Sec, int64(sx.Btime.Nsec))
}
