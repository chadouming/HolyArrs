//go:build !linux

package transcode

import "syscall"

func childSysProcAttr() *syscall.SysProcAttr {
	return nil
}

func processAlive(_ int) bool {
	return true
}

func lowerChildPriority(_ int) {}
