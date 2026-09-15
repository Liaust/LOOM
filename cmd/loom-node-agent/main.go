package main

import (
	"fmt"
	"os"

	"loom.local/loom/internal/nodeagent"
)

func main() {
	if err := nodeagent.NewRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
