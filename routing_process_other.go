//go:build !linux

package main

import "os/exec"

func routingChildLifetime(cmd *exec.Cmd) {}
