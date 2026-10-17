//go:build unix

package cli

import (
	"os"
	"os/signal"
	"syscall"
)

// freshLinkRequests gets a value each time this process is sent SIGUSR1,
// which is how a program that runs `fabrica dashboard` asks it for a fresh
// link. Call the function it returns to stop listening.
func freshLinkRequests() (<-chan os.Signal, func()) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGUSR1)
	return sigs, func() { signal.Stop(sigs) }
}
