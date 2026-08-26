//go:build linux

package webrunner

import (
	"os"
	"syscall"
	"time"
)

// os_Getpid is a seam alias so the janitor compiles identically on every
// platform file pair.
func os_Getpid() int { return os.Getpid() }

// reapOrphanProcess collects the exit status of a killed orphan that was
// reparented to this process. In the Docker container the scraper runs as PID
// 1, so a Chromium orphan killed by the janitor becomes OUR zombie until
// someone waits on it. The wait targets exactly one PID — never -1, which
// would steal exit statuses from os/exec children — and WNOHANG in a short
// loop keeps it non-blocking.
func reapOrphanProcess(pid int32) {
	if os.Getpid() != 1 {
		return
	}

	var status syscall.WaitStatus

	for range 50 {
		reaped, err := syscall.Wait4(int(pid), &status, syscall.WNOHANG, nil)
		if err != nil || reaped == int(pid) {
			return
		}

		if reaped == 0 {
			time.Sleep(20 * time.Millisecond)
		}
	}
}
