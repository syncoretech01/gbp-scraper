//go:build !linux

package webrunner

import "os"

func os_Getpid() int { return os.Getpid() }

// reapOrphanProcess is a no-op off Linux: Windows has no zombie processes to
// reap, and the scraper is never PID 1 outside the Linux container.
func reapOrphanProcess(int32) {}
