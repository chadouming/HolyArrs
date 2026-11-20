//go:build !linux

package transcribe

import "syscall"

func childAttr() *syscall.SysProcAttr { return nil }
