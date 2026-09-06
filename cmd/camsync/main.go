// Command camsync shifts photo timestamps per camera using XMP sidecars.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/sollniss/camsync/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Main(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
