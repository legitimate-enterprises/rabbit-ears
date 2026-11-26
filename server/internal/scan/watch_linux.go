package scan

import (
	"os"
	"strconv"
	"strings"
)

// watchBudget is half the inotify watches the user may hold, leaving the
// rest to the user's other programs. Watches hold no descriptors.
// shortcut: Sync charges a root an entry each, as kqueue costs, where
// inotify takes a watch per folder only; charge folders alone if large
// libraries go unwatched on Linux.
func watchBudget() int {
	b, err := os.ReadFile("/proc/sys/fs/inotify/max_user_watches")
	n, perr := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || perr != nil {
		n = 8192 // the kernel's floor
	}
	return n / 2
}
