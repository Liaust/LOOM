package backupstrategy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalOperationalRetentionPinsDigestLeaseAndApply(t *testing.T) {
	f := newOperationalFixture(t)
	for i := 0; i < 5; i++ {
		createOperationalFixture(t, f, fmt.Sprintf("operational-%03d", i), time.Date(2026, 10, 13, i, 0, 0, 0, time.UTC), nil)
	}
	ctx := context.Background()
	pins := map[string]string{"operational-000": "rollback"}
	resolve := func() (map[string]string, error) { return pins, nil }
	p, err := PlanLocalOperationalRetention(ctx, f.packagesRoot, pins)
	if err != nil || len(p.Selection.Keep) != 2 || len(p.Selection.Remove) != 3 || p.BoundMet {
		t.Fatalf("plan=%+v err=%v", p, err)
	}
	lease, err := LockOperationalPackages(f.packagesRoot, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyLocalOperationalRetention(ctx, f.packagesRoot, p.PlanHash, true, resolve); err == nil {
		t.Fatal("deleted while producer/consumer was active")
	}
	lease.Close()
	if _, err := ApplyLocalOperationalRetention(ctx, f.packagesRoot, p.PlanHash, false, resolve); err == nil {
		t.Fatal("missing confirmation accepted")
	}
	pins["operational-001"] = "new current checkpoint"
	if _, err := ApplyLocalOperationalRetention(ctx, f.packagesRoot, p.PlanHash, true, resolve); err == nil {
		t.Fatal("stale protected references accepted")
	}
	delete(pins, "operational-001")
	result, err := ApplyLocalOperationalRetention(ctx, f.packagesRoot, p.PlanHash, true, resolve)
	if err != nil || result.Status != "applied" || len(result.Removed) != 3 || result.Removed[0] != "operational-001" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err := os.Stat(result.ReceiptPath + ".result"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.packagesRoot, "operational-000")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.packagesRoot, "operational-001")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err := ApplyLocalOperationalRetention(ctx, f.packagesRoot, p.PlanHash, true, resolve); err == nil {
		t.Fatal("old plan replay accepted")
	}
}

func TestLocalOperationalRetentionHoldsUnknownAndChangedPackages(t *testing.T) {
	f := newOperationalFixture(t)
	for i := 0; i < 4; i++ {
		createOperationalFixture(t, f, fmt.Sprintf("operational-%03d", i), time.Date(2026, 10, 13, i, 0, 0, 0, time.UTC), nil)
	}
	ctx := context.Background()
	resolve := func() (map[string]string, error) { return nil, nil }
	p, err := PlanLocalOperationalRetention(ctx, f.packagesRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.packagesRoot, "operational-000", OperationalManifestFile)
	if err := os.WriteFile(path, []byte("not a package"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyLocalOperationalRetention(ctx, f.packagesRoot, p.PlanHash, true, resolve); err == nil {
		t.Fatal("changed manifest deleted")
	}
	held, err := PlanLocalOperationalRetention(ctx, f.packagesRoot, nil)
	if err != nil || held.BoundMet || len(held.Selection.Findings) == 0 || len(held.Selection.Remove) != 2 {
		t.Fatalf("held=%+v err=%v", held, err)
	}
	if _, err := PlanLocalOperationalRetention(ctx, f.packagesRoot, map[string]string{"operational-missing": "pin"}); err == nil {
		t.Fatal("missing pin accepted")
	}
}

func TestOperationalProducerCanDeferHistoricalVerification(t *testing.T) {
	f := newOperationalFixture(t)
	old := createOperationalFixture(t, f, "operational-old", time.Now().Add(-time.Hour), nil)
	if err := os.WriteFile(filepath.Join(old.PackageDir, OperationalManifestFile), []byte("broken old package"), 0600); err != nil {
		t.Fatal(err)
	}
	next := createOperationalFixture(t, f, "operational-new", time.Now(), func(input *OperationalPackageCreateInput) { input.DeferRetentionPlanning = true })
	if next.RetentionPlan.Status != "operator_review_required" || len(next.RetentionPlan.Keep) != 0 || len(next.RetentionPlan.Findings) != 0 || next.Verification.Status != "succeeded" {
		t.Fatalf("unexpected producer result: %+v", next)
	}
}
