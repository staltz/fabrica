// Command fabrica lets you write down how your AI agents build software,
// once, and run that same setup on many AI coding tools.
//
// See the README, or run `fabrica --help`.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/staltz/fabrica/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Main(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
