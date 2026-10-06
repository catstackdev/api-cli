// Command api is a REST + GraphQL client for the terminal.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"cybercat/api/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Run(ctx, os.Args[1:], cli.StdIO())
	stop()
	os.Exit(code)
}
