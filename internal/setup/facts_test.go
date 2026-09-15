package setup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBinaryFactFindsHomeLocalBinFallback(t *testing.T) {
	home := t.TempDir()
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir local bin: %v", err)
	}
	path := filepath.Join(binDir, "loom-test-binary")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}

	fact := binaryFact("loom-test-binary", home)
	if !fact.Found || fact.Path != path {
		t.Fatalf("binary fact = %#v, want %s", fact, path)
	}
}
