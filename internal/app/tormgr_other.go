//go:build !windows

package app

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}
