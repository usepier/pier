package main

import (
	"syscall"
)

// childAttr runs an agent program in a process group of its own (so its
// tools end with it), and ends it should the supervisor die first.
func childAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
}

func killTree(pid int) {
	syscall.Kill(-pid, syscall.SIGKILL)
}
