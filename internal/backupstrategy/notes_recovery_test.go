package backupstrategy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func notesFixture(t *testing.T, root, id string) NotesRecoveryPublication {
	t.Helper()
	fixture := newOperationalFixture(t)
	created := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	result := createOperationalFixture(t, fixture, "operational-"+id, created, nil)
	destination := filepath.Join(root, id, "operational-package")
	if err := os.MkdirAll(destination, 0700); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(result.PackageDir)
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(result.PackageDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		mustWriteOperational(t, filepath.Join(destination, entry.Name()), raw)
	}
	tar := []byte("owned tiny archive fixture")
	mustWriteOperational(t, filepath.Join(root, id, "main-state.tar.gz"), tar)
	manifest, _ := os.ReadFile(filepath.Join(destination, "manifest.json"))
	captured, err := time.Parse("20060102T150405Z", strings.Split(id, "-")[1])
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"schema": "loom.notes_recovery.v1", "id": id, "captured_at": captured,
		"package_id": result.Verification.PackageID, "manifest_sha256": notesRecoveryHash(manifest), "main_state_sha256": notesRecoveryHash(tar)})
	mustWriteOperational(t, filepath.Join(root, id, "cohort.json"), raw)
	pub, err := RecordNotesRecoveryPublication(context.Background(), root, id, filepath.Join(filepath.Dir(root), "publications"), true)
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

func protectNotesFixture(t *testing.T, root string, pub NotesRecoveryPublication) NotesRecoveryProtection {
	t.Helper()
	raw, _ := json.Marshal(pub)
	proof := NotesRecoveryProtection{Schema: NotesRecoveryProtectionSchema, Publication: pub, PublicationSHA256: notesRecoveryHash(raw),
		Repository: "fixture-repository", Archive: "history-" + pub.ID, ArchiveRef: "fixture-reference", ArchiveManifestSHA256: strings.Repeat("a", 64),
		VerifiedAt: time.Now().UTC(), VerificationBasis: "stable_publication_and_authenticated_archive_metadata_v1"}
	if err := WriteNotesRecoveryRecord(root, pub.ID, proof, 0600); err != nil {
		t.Fatal(err)
	}
	return proof
}

func TestNotesRecoveryPublicationAndMutation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cohorts")
	id := "notes-20261019T020000Z-12345678"
	pub := notesFixture(t, root, id)
	records := filepath.Join(filepath.Dir(root), "publications")
	got, hash, err := ReadNotesRecoveryPublication(records, id)
	if err != nil || hash == "" || got.Identity != pub.Identity {
		t.Fatalf("publication %v %v", got, err)
	}
	file := filepath.Join(root, id, "main-state.tar.gz")
	before, _ := os.Stat(file)
	if err := os.WriteFile(file, []byte("changed but same byte size"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	current, err := InspectNotesRecoveryPublication(context.Background(), root, id)
	if err != nil || current.Identity == pub.Identity {
		t.Fatalf("mutation not detected: %v", err)
	}
	if _, err := RecordNotesRecoveryPublication(context.Background(), root, id, records, true); err == nil {
		t.Fatal("incorrect payload digest accepted")
	}
	if err := os.Symlink(file, filepath.Join(root, id, "unexpected")); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectNotesRecoveryPublication(context.Background(), root, id); err == nil {
		t.Fatal("linked artifact accepted")
	}
}

func TestNotesRecoveryPublicationVerifiesFreshInventory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cohorts")
	id := "notes-20261019T020000Z-12345678"
	notesFixture(t, root, id)
	path := filepath.Join(root, id, "cohort.json")
	raw, _ := os.ReadFile(path)
	var cohort map[string]any
	if err := json.Unmarshal(raw, &cohort); err != nil {
		t.Fatal(err)
	}
	inventory := []byte(`{"schema_version":"notes_workspace.recovery_enrollment.v1","collections":[]}`)
	cohort["recovery_inventory_sha256"], cohort["enrollment_inventory_sha256"] = notesRecoveryHash(inventory), notesRecoveryHash(inventory)
	raw, _ = json.Marshal(cohort)
	mustWriteOperational(t, path, raw)
	inventoryPath := filepath.Join(root, id, "recovery-enrollment.json")
	mustWriteOperational(t, inventoryPath, inventory)
	if _, err := InspectNotesRecoveryPublication(context.Background(), root, id); err != nil {
		t.Fatal(err)
	}
	mustWriteOperational(t, inventoryPath, []byte(`{"schema_version":"changed"}`))
	if _, err := InspectNotesRecoveryPublication(context.Background(), root, id); err == nil {
		t.Fatal("changed recovery membership accepted")
	}
}

func TestNotesRecoveryRetentionProtectionAndApply(t *testing.T) {
	base := t.TempDir()
	root, proofs := filepath.Join(base, "cohorts"), filepath.Join(base, "protection")
	old := notesFixture(t, root, "notes-20261017T020000Z-12345678")
	newest := notesFixture(t, root, "notes-20261018T020000Z-12345678")
	pending := notesFixture(t, root, "notes-20261019T020000Z-12345678")
	protectNotesFixture(t, proofs, old)
	protectNotesFixture(t, proofs, newest)
	input := NotesRecoveryRetentionInput{Root: root, PublicationRoot: filepath.Join(base, "publications"), ProtectionRoot: proofs, Repository: "fixture-repository"}
	plan, err := PlanNotesRecoveryRetention(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]string{}
	for _, entry := range plan.Entries {
		actions[entry.ID] = entry.Action
	}
	if actions[old.ID] != "candidate" || actions[newest.ID] != "keep" || actions[pending.ID] != "hold" {
		t.Fatalf("%v", actions)
	}
	again, _ := PlanNotesRecoveryRetention(context.Background(), input)
	if plan.PlanHash != again.PlanHash {
		t.Fatal("unstable plan")
	}
	operational := filepath.Join(base, "packages")
	if err := os.Mkdir(operational, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyNotesRecoveryRetention(context.Background(), operational, input, plan.PlanHash, true,
		func(context.Context, NotesRecoveryProtection) error { return fmt.Errorf("offline") }); err == nil {
		t.Fatal("offline protection accepted")
	}
	if _, err := os.Stat(filepath.Join(root, old.ID)); err != nil {
		t.Fatal("payload removed after refused protection")
	}
	verified := 0
	result, err := ApplyNotesRecoveryRetention(context.Background(), operational, input, plan.PlanHash, true,
		func(_ context.Context, p NotesRecoveryProtection) error {
			verified++
			if p.Publication.ID != old.ID {
				t.Fatal("wrong candidate")
			}
			return nil
		})
	if err != nil || result.Status != "applied" || len(result.Removed) != 1 || verified != 1 {
		t.Fatalf("%+v %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(root, old.ID)); !os.IsNotExist(err) {
		t.Fatal("retired payload remains")
	}
	if _, _, err := ReadNotesRecoveryPublication(input.PublicationRoot, old.ID); err != nil {
		t.Fatal("publication history lost")
	}
	if _, err := ReadNotesRecoveryProtection(proofs, old.ID); err != nil {
		t.Fatal("cloud history pointer lost")
	}
	if _, err := os.Stat(filepath.Join(result.ReceiptRoot, old.ID+".json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, pending.ID)); err != nil {
		t.Fatal("pending capture removed")
	}
}

func TestNotesRecoveryRetentionPinsAndUnverified(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "cohorts")
	proofs := filepath.Join(base, "protection")
	old := notesFixture(t, root, "notes-20261017T020000Z-12345678")
	newest := notesFixture(t, root, "notes-20261018T020000Z-12345678")
	protectNotesFixture(t, proofs, old)
	protectNotesFixture(t, proofs, newest)
	input := NotesRecoveryRetentionInput{Root: root, PublicationRoot: filepath.Join(base, "publications"), ProtectionRoot: proofs, Repository: "fixture-repository", Protected: map[string]string{old.ID: "operator"}}
	plan, err := PlanNotesRecoveryRetention(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range plan.Entries {
		if entry.Action != "keep" {
			t.Fatalf("pinned old replaced newest protected: %+v", entry)
		}
	}
	if err := os.Remove(filepath.Join(input.PublicationRoot, old.ID+".json")); err != nil {
		t.Fatal(err)
	}
	plan, err = PlanNotesRecoveryRetention(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range plan.Entries {
		if entry.ID == old.ID && entry.Action != "hold" {
			t.Fatal("missing prior verification promoted")
		}
	}
	if _, err := os.Stat(filepath.Join(input.PublicationRoot, old.ID+".json")); !os.IsNotExist(err) {
		t.Fatal("planner minted verification")
	}
	input.Protected["notes-20261016T020000Z-12345678"] = "missing"
	if _, err := PlanNotesRecoveryRetention(context.Background(), input); err == nil {
		t.Fatal("missing pin accepted")
	}
}

func TestNotesRecoveryResumeExactRetirement(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "cohorts")
	pub := notesFixture(t, root, "notes-20261017T020000Z-12345678")
	proof := protectNotesFixture(t, filepath.Join(base, "protection"), pub)
	receipt := NotesRecoveryRetirement{Schema: "loom.notes_recovery.retirement.v1", ID: pub.ID, Status: "started", PlanHash: "reviewed", StartedAt: time.Now().UTC(), Protection: proof}
	if err := WriteNotesRecoveryRecord(root+".retirement-receipts", pub.ID, receipt, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, pub.ID), filepath.Join(root, ".retired-"+pub.ID)); err != nil {
		t.Fatal(err)
	}
	// Simulate interruption after some files were already removed.
	if err := os.Remove(filepath.Join(root, ".retired-"+pub.ID, "main-state.tar.gz")); err != nil {
		t.Fatal(err)
	}
	operational := filepath.Join(base, "packages")
	if err := os.Mkdir(operational, 0700); err != nil {
		t.Fatal(err)
	}
	verify := func(context.Context, NotesRecoveryProtection) error { return nil }
	got, err := ResumeNotesRecoveryRetirement(context.Background(), operational, root, pub.ID, verify)
	if err != nil || got.Status != "local_retired" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".retired-"+pub.ID)); !os.IsNotExist(err) {
		t.Fatal("staging remains")
	}
	if _, err := ResumeNotesRecoveryRetirement(context.Background(), operational, root, pub.ID, verify); err != nil {
		t.Fatal(err)
	}
	if _, err := ResumeNotesRecoveryRetirement(context.Background(), operational, root, "notes-20261016T020000Z-12345678", verify); err == nil {
		t.Fatal("unrecorded retirement resumed")
	}
}

func TestNotesRecoveryStatusReadsOnlySavedSummary(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "cohorts")
	pub := notesFixture(t, root, "notes-20261019T020000Z-12345678")
	proofs := filepath.Join(base, "protection")
	protectNotesFixture(t, proofs, pub)
	if err := os.MkdirAll(filepath.Join(base, "backups", "operational"), 0700); err != nil {
		t.Fatal(err)
	}
	input := NotesRecoveryRetentionInput{Root: root, PublicationRoot: filepath.Join(base, "publications"), ProtectionRoot: proofs, Repository: "fixture-repository"}
	if err := PublishRecoveryInventorySummary(context.Background(), base, input); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(base, "backups", "operational")); err != nil {
		t.Fatal(err)
	}
	saved, err := ReadRecoveryInventorySummary(base)
	if err != nil || saved.NotesCaptures != 1 || saved.NotesProtected != 1 || !saved.NotesAvailable || !saved.NotesAccountingComplete {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
	raw, _ := json.Marshal(saved)
	if strings.Contains(string(raw), pub.ID) || strings.Contains(string(raw), root) {
		t.Fatal("summary disclosed private inventory")
	}
}
