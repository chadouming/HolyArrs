package builtin

import "syscall"

// childAttr makes the kernel stop the runner when HolyArrs dies, however it dies.
func childAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
