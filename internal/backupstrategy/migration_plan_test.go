package backupstrategy

import (
	"strings"
	"testing"
	"time"
)

func TestMigrationEligibilityRequiresExactInventoryAndStrictRestoreEvidence(t *testing.T) {
	inventory := migrationInventoryFixture()
	evidence, err := BindStrictRestoreEvidence(StrictRestoreEvidence{
		Schema: StrictRestoreEvidenceSchema, Status: StrictRestoreSucceeded,
		RepositoryID: "repo-id", Archive: "__loom-direct-user-data-loom-main-history",
		DirectArchiveManifestSHA: strings.Repeat("a", 64), OperationalManifestSHA256: strings.Repeat("b", 64),
		ProvenanceManifestSHA256: strings.Repeat("c", 64), AcceptedAt: time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := PlanMigrationEligibility(MigrationEligibilityInput{
		Inventory: inventory, ExpectedInventoryDigest: inventory.Digest, StrictRestore: evidence,
		GeneratedAt: time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != MigrationEligibilityEligible || report.CleanupApplyAllowed || len(report.Blockers) != 0 {
		t.Fatalf("eligibility = %#v", report)
	}
	if len(report.Candidates) != 1 || report.Candidates[0].RelativePath != "legacy-generation" || report.ExpectedReclaimableBytes != 4096 {
		t.Fatalf("candidates = %#v bytes=%d", report.Candidates, report.ExpectedReclaimableBytes)
	}
	if len(report.Protected) != 4 {
		t.Fatalf("protected set = %#v", report.Protected)
	}
	replay, err := PlanMigrationEligibility(MigrationEligibilityInput{
		Inventory: inventory, ExpectedInventoryDigest: inventory.Digest, StrictRestore: evidence,
		GeneratedAt: time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC),
	})
	if err != nil || replay.Digest != report.Digest {
		t.Fatalf("deterministic report digest changed: %q %q err=%v", report.Digest, replay.Digest, err)
	}
}

func TestMigrationEligibilityBlocksTamperUnknownAndAccountingMismatch(t *testing.T) {
	inventory := migrationInventoryFixture()
	inventory.Components = append(inventory.Components, ComponentInventory{
		RelativePath: "surprise", Class: ComponentUnknown, Safe: true, ReclaimPosture: ReclaimBlocked,
	})
	inventory.LocalTotals.PotentialReclaimableBytes++
	evidence, err := BindStrictRestoreEvidence(StrictRestoreEvidence{
		Schema: StrictRestoreEvidenceSchema, Status: StrictRestoreSucceeded,
		RepositoryID: "repo-id", Archive: "archive", DirectArchiveManifestSHA: strings.Repeat("1", 64),
		OperationalManifestSHA256: strings.Repeat("2", 64), ProvenanceManifestSHA256: strings.Repeat("3", 64),
		AcceptedAt: time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	evidence.Archive = "tampered-after-binding"
	report, err := PlanMigrationEligibility(MigrationEligibilityInput{
		Inventory: inventory, ExpectedInventoryDigest: "wrong", StrictRestore: evidence,
		GeneratedAt: time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, blocker := range []string{"inventory_digest_mismatch", "strict_restore_evidence_invalid", "unknown_entry:surprise", "reclaimable_byte_accounting_mismatch"} {
		if !containsMigrationString(report.Blockers, blocker) {
			t.Fatalf("missing blocker %q in %#v", blocker, report.Blockers)
		}
	}
	if report.Status != MigrationEligibilityBlocked || report.CleanupApplyAllowed {
		t.Fatalf("blocked report = %#v", report)
	}
}

func migrationInventoryFixture() Inventory {
	inventory := Inventory{
		SchemaVersion: inventorySchemaVersion, Digest: strings.Repeat("d", 64),
		Components: []ComponentInventory{
			{RelativePath: "legacy-generation", Class: ComponentSuccessfulGeneration, Safe: true, RestorePoint: true, ReclaimPosture: ReclaimCandidate, Accounting: ByteAccounting{AllocatedBytes: 4096, PotentialReclaimableBytes: 4096}},
			{RelativePath: "F9", Class: ComponentProtectedMilestone, Safe: true, Protected: true, RestorePoint: true, ReclaimPosture: ReclaimProtected, Accounting: ByteAccounting{AllocatedBytes: 8192}},
			{RelativePath: "store", Class: ComponentSharedStore, Safe: true, Protected: true, ReclaimPosture: ReclaimProtected, Accounting: ByteAccounting{AllocatedBytes: 16384}},
			{RelativePath: "failed", Class: ComponentImmutableFailedEvidence, Safe: true, Protected: true, ReclaimPosture: ReclaimProtected, Accounting: ByteAccounting{AllocatedBytes: 512}},
			{RelativePath: "operational", Class: ComponentOperationalPackage, Safe: true, Protected: true, ReclaimPosture: ReclaimProtected, Accounting: ByteAccounting{AllocatedBytes: 1024}},
		},
		LocalTotals: ByteAccounting{PotentialReclaimableBytes: 4096},
		Remote:      &RemoteInventory{RepositoryID: "repo-id"},
	}
	digest, err := inventoryDigest(inventory)
	if err != nil {
		panic(err)
	}
	inventory.Digest = digest
	return inventory
}

func containsMigrationString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
