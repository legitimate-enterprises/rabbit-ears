package scan

import "golang.org/x/sys/unix"

// watchBudget is half the descriptors the process may open, leaving the
// rest to media, the database, ffmpeg pipes and sockets: fsnotify's kqueue
// backend holds one for every entry it watches.
func watchBudget() int {
	var lim unix.Rlimit
	_ = unix.Getrlimit(unix.RLIMIT_NOFILE, &lim)
	// The kernel holds a process to kern.maxfilesperproc whatever its
	// limit says, and the limit may be unlimited.
	m, _ := unix.SysctlUint32("kern.maxfilesperproc")
	if lim.Cur < uint64(m) {
		m = uint32(lim.Cur) //nolint:gosec // below m, a uint32
	}
	return int(m / 2)
}
