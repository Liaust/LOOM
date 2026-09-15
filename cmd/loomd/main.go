package main

import (
	"os"

	"loom.local/loom/internal/loomdapp"
)

func main() {
	if err := loomdapp.NewRootCommand().Execute(); err != nil {
		os.Exit(1)
	}
}
