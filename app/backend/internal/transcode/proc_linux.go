//go:build linux

package transcode

import (
	"errors"
	"syscall"
)

func childSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func lowerChildPriority(pid int) {
	if pid > 0 {
		_ = syscall.Setpriority(syscall.PRIO_PROCESS, pid, 10)
	}
}
