package runtimes

import (
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/maintenance"
)

func TestCloudSnapshotApplicationDataRoot(t *testing.T) {
	for _, kind := range []string{"disabled", "empty", "missing", "symlink", "overlap"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			coverage := runtimeDirectArchiveCoverage(t, root)
			pool := filepath.Join(root, "application-data")
			if kind != "missing" {
				if err := os.Mkdir(pool, 0700); err != nil {
					t.Fatal(err)
				}
			}
			coverage.ApplicationDataRoot = pool
			switch kind {
			case "disabled":
				coverage.ApplicationDataRoot = ""
			case "symlink":
				coverage.ApplicationDataRoot = pool + "-link"
				if err := os.Symlink(pool, coverage.ApplicationDataRoot); err != nil {
					t.Fatal(err)
				}
			case "overlap":
				coverage.ApplicationDataRoot = coverage.MainBoxPath
			}
			r := NewCloudSnapshotUploadRuntime(maintenance.Service{}, root, "main", coverage)
			r.agentsRoot = runtimeDirectArchiveAgentsRoot(root)
			roots, exclusions, err := r.directArchiveRoots()
			if kind != "disabled" && kind != "empty" {
				if err == nil {
					t.Fatal("unsafe root accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, entry := range roots {
				if entry.Name == "application_data" {
					count++
					if entry.Path != pool {
						t.Fatal("root replaced")
					}
				}
			}
			if (kind == "empty" && count != 1) || (kind == "disabled" && count != 0) {
				t.Fatal("pool enrollment wrong", roots)
			}
			for _, exclusion := range exclusions {
				if exclusion.Root == "application_data" {
					t.Fatal("application data excluded")
				}
			}
		})
	}
}
