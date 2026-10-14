//go:build !unix

package provider

import "os/exec"

// detach does nothing where there are no process sessions; the command
// still outlives Fabrica, since nothing waits for it.
func detach(*exec.Cmd) {}
