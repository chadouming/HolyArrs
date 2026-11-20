//go:build !linux

package builtin

import "syscall"

func childAttr() *syscall.SysProcAttr { return nil }
