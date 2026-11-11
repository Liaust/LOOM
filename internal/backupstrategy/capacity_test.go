package backupstrategy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOperationalCapacityExistingMainRoot(t *testing.T) {
	root := os.Getenv("LOOM_TEST_OPERATIONAL_CAPACITY_ROOT")
	if root == "" {
		t.Skip("explicit read-only Main admission check")
	}
	if root != "/var/lib/loom/backups/operational" {
		t.Fatal("unexpected live root")
	}
	if err := CheckOperationalPackageAdmission(context.Background(), root, 96<<30, 50<<30); err != nil {
		t.Fatal(err)
	}
}

func TestOperationalCapacityAdmissionPreservesHeldBytes(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "held-dump")
	if err := os.WriteFile(path, make([]byte, 8192), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkOperationalPackageAdmission(context.Background(), root, 96<<30, 1, 1<<40); err != nil {
		t.Fatal(err)
	}
	if err := checkOperationalPackageAdmission(context.Background(), root, 1, 1, 1<<40); err == nil {
		t.Fatal("over-budget capture admitted")
	}
	if err := checkOperationalPackageAdmission(context.Background(), root, 96<<30, 1<<62, 1<<40); err == nil {
		t.Fatal("free-space floor ignored")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("held payload changed", err)
	}
	if err := os.Symlink(path, filepath.Join(root, "unknown-link")); err != nil {
		t.Fatal(err)
	}
	if err := checkOperationalPackageAdmission(context.Background(), root, 96<<30, 1, 1<<40); err == nil {
		t.Fatal("unknown linked inventory admitted")
	}
}
