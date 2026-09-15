package serviceregistry

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestHelperClientRejectsTrailingAndOversizedResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"trailing json", `printf '%s' '{"operation":"status","success":true,"process_state":"running"} {}'`},
		{"oversized stdout", `dd if=/dev/zero bs=70000 count=1 2>/dev/null | tr '\\000' x`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "helper")
			if err := os.WriteFile(path, []byte("#!/bin/sh\n"+test.body+"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			runner := HelperClientRunner{Path: path, AllowlistPath: filepath.Join(dir, "allowlist.yaml")}
			if _, err := runner.Run(context.Background(), testAllowlistRecord(), ManagerRequest{AllowlistKey: "example", Operation: OperationStatus}); err == nil {
				t.Fatal("unsafe helper response accepted")
			}
		})
	}
}
