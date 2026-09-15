package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"loom.local/loom/internal/preparation"
)

func main() {
	// The configured unit is the only entry. No alternate root, command,
	// identity, cgroup or fixture flags can promote direct-root execution.
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "preparation: fixed socket activation required")
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	if preparation.RunActivated(ctx) != nil {
		fmt.Fprintln(os.Stderr, "preparation: unavailable or failed; no output receipt")
		os.Exit(1)
	}
}
