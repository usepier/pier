//go:build !linux

package main

import "syscall"

// childAttr: the supervisor runs on Linux; elsewhere (its tests) children
// only get a process group.
func childAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

func killTree(pid int) { syscall.Kill(-pid, syscall.SIGKILL) }
