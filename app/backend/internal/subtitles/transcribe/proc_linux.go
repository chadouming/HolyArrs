package transcribe

import "syscall"

// childAttr makes the kernel stop a child when HolyArrs dies, however it dies.
func childAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
