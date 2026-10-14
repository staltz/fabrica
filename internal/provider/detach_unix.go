//go:build unix

package provider

import (
	"os/exec"
	"syscall"
)

// detach starts cmd in a session of its own, so it keeps running after
// Fabrica exits, and a Ctrl-C meant for Fabrica doesn't reach it.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
