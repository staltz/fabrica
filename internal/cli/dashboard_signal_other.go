//go:build !unix

package cli

import "os"

// freshLinkRequests has nothing to listen for where there's no SIGUSR1: the
// channel never gets a value.
func freshLinkRequests() (<-chan os.Signal, func()) {
	return nil, func() {}
}
