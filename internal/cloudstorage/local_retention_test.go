package cloudstorage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/backupstrategy"
)

func TestDirectArchiveRefusesOperationalRetentionLease(t *testing.T) {
	root := t.TempDir()
	lease, err := backupstrategy.LockOperationalPackages(root, true)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	input := DirectArchiveInput{}
	input.Request.OperationalPackage.Path = filepath.Join(root, "operational-held")
	_, err = (BorgSnapshotBackend{}).ArchiveCanonicalRoots(context.Background(), input)
	if err == nil || !strings.Contains(err.Error(), "operational packages are in use") {
		t.Fatalf("unexpected result: %v", err)
	}
}
