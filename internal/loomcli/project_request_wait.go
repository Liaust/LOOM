package loomcli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// This reports only an outstanding request, not an unobserved backend phase.
func startProjectRequestWait(cmd *cobra.Command, opts *options, operation, activity string, delay, interval time.Duration) func() {
	if opts.jsonOutput || opts.plainOutput {
		return func() {}
	}
	stop, done := make(chan struct{}), make(chan struct{})
	started := time.Now()
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		defer close(done)
		timer := time.NewTimer(delay)
		defer timer.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-timer.C:
				fmt.Fprintf(cmd.ErrOrStderr(), "Waiting for %s response (%s elapsed); operation %s. Completion is not yet confirmed; do not resubmit.\n", activity, time.Since(started).Round(time.Second), declarationDisplay(operation))
				timer.Reset(interval)
			}
		}
	}()
	return func() { close(stop); <-done }
}
