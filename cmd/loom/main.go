package main

import (
	"os"

	"loom.local/loom/internal/loomcli"
)

func main() {
	if err := loomcli.Execute(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		os.Exit(1)
	}
}
