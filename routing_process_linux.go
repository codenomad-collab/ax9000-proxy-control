//go:build linux

package main

import (
	"os/exec"
	"syscall"
)

func routingChildLifetime(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
