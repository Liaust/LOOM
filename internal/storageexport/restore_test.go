package storageexport

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/storagecatalog"
)

func TestRestorePlanAndApplyRemainAvailableWithoutMaterializer(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.txt")
	target := filepath.Join(root, "restore", "target.txt")
	if err := os.WriteFile(source, []byte("retained"), 0o640); err != nil {
		t.Fatal(err)
	}
	entry := storagecatalog.Entry{StorageEntryID: "storage_entry_restore", FileClass: storagecatalog.FileClassText, AvailabilityState: storagecatalog.AvailabilityStateAvailable}
	plan, err := PlanRestore(RestorePlanInput{
		Ref: "storage_entry_restore", Mode: RestoreModeSafe, TargetPath: target,
		EntryDetail: storagecatalog.EntryDetail{Entry: entry, PhysicalRefs: []storagecatalog.PhysicalRef{{RefKind: storagecatalog.PhysicalRefKindLocalPath, URI: source, Status: storagecatalog.PhysicalRefStatusAvailable}}},
	})
	if err != nil {
		t.Fatalf("PlanRestore: %v", err)
	}
	result, err := ApplyRestorePlan(context.Background(), plan, true)
	if err != nil {
		t.Fatalf("ApplyRestorePlan: %v", err)
	}
	if result.Refused || result.TargetPath != target {
		t.Fatalf("unexpected result: %#v", result)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "retained" {
		t.Fatalf("restored payload = %q err=%v", got, err)
	}
}

func TestRestorePlanRejectsMissingLocalEvidence(t *testing.T) {
	plan, err := PlanRestore(RestorePlanInput{
		Ref: "storage_entry_missing", Mode: RestoreModeSafe, TargetPath: filepath.Join(t.TempDir(), "target"),
		EntryDetail: storagecatalog.EntryDetail{Entry: storagecatalog.Entry{StorageEntryID: "storage_entry_missing", FileClass: storagecatalog.FileClassText, AvailabilityState: storagecatalog.AvailabilityStateAvailable}},
	})
	if err != nil {
		t.Fatalf("PlanRestore: %v", err)
	}
	if plan.CanApply || len(plan.Steps) == 0 || plan.Steps[len(plan.Steps)-1].Status != "missing" {
		t.Fatalf("missing physical-ref evidence did not fail closed: %#v", plan)
	}
}
