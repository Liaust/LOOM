// loom-hermes-schedules is an immutable no-argument owner-scoped read helper.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"loom.local/loom/internal/hermesschedules"
)

// Set by the selected host's Nix derivation; never supplied by the caller.
var bindingPath string

func main() {
	// Do not allow runtime environment variables to redirect profile selection or
	// child execution. This helper never executes another program.
	os.Clearenv()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, hermesschedules.ReadTimeout)
	defer cancel()
	// A local filesystem syscall need not cooperate with context cancellation.
	// Bound the entire short-lived process as well as the observer's reads.
	timer := time.AfterFunc(hermesschedules.ReadTimeout, func() { os.Exit(1) })
	defer timer.Stop()
	if err := hermesschedules.ServeFixedHelper(ctx, os.Args[1:], bindingPath, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Hermes schedule observation helper unavailable")
		os.Exit(1)
	}
}
