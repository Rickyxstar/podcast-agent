// Command podcast-agent summarizes podcast transcripts, extracts show notes
// and fact-checks claims, either one file at a time (run) or as an SQS-driven
// worker (worker).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	// SIGTERM is how Kubernetes stops a pod; SIGINT is Ctrl-C.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		stop()
		os.Exit(1)
	}
}
