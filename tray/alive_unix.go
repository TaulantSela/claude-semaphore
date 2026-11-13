//go:build !windows

package main

import (
	"errors"
	"syscall"
)

// pidAlive reports whether a process exists. EPERM means it exists but belongs
// to someone else, which still counts.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
