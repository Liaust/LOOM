package loomcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/filesystemlayout"
	"loom.local/loom/internal/filetransfer"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/mainstorage"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/storagecleanup"
	"loom.local/loom/internal/storagedoctor"
	"loom.local/loom/internal/storagefidelity"
	"loom.local/loom/internal/storagemigration"
	"loom.local/loom/internal/storageretention"
	"loom.local/loom/internal/storageview"
)

func TestStorageFilesystemMigrationRequiresReviewedManifestConfirmation(t *testing.T) {
	cmd := newStorageFilesystemMigrateCommand(&options{})
	cmd.SetArgs([]string{"--apply", "--manifest", filepath.Join(t.TempDir(), "manifest.json")})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("apply confirmation error = %v", err)
	}
}

func TestStorageFilesystemManifestRoundTripDoesNotOverwrite(t *testing.T) {
	pathValue := filepath.Join(t.TempDir(), "migration.json")
	manifest := storagemigration.Manifest{SchemaVersion: storagemigration.ManifestSchemaVersion, NodeID: "main", LayoutFingerprint: "layout", GeneratedAt: time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)}
	if err := storagemigration.RefreshManifestHash(&manifest); err != nil {
		t.Fatal(err)
	}
	if err := writeFilesystemManifest(pathValue, manifest); err != nil {
		t.Fatal(err)
	}
	if err := writeFilesystemManifest(pathValue, manifest); err == nil {
		t.Fatal("manifest writer overwrote an existing review artifact")
	}
	got, err := readFilesystemManifest(pathValue)
	if err != nil {
		t.Fatal(err)
	}
	if got.ManifestHash != manifest.ManifestHash || got.NodeID != "main" {
		t.Fatalf("round trip manifest = %#v", got)
	}
}

func TestStorageFilesystemLargeManifestArtifactIsBoundedReadableAndApplicable(t *testing.T) {
	pathValue := filepath.Join(t.TempDir(), "migration-set")
	root := t.TempDir()
	oldRoot := filepath.Join(root, "old")
	newRoot := filepath.Join(root, "new")
	if err := os.MkdirAll(oldRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(oldRoot, "payload")
	if err := os.WriteFile(oldPath, []byte("bounded artifact\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	details := make([]storagecatalog.EntryDetail, 0, maxFilesystemManifestRecords*2+1)
	for index := 0; index < maxFilesystemManifestRecords*2+1; index++ {
		detail := storagecatalog.EntryDetail{Entry: storagecatalog.Entry{StorageEntryID: fmt.Sprintf("entry-%06d", index), OriginalSourcePath: oldPath}}
		if index == 0 {
			detail.PhysicalRefs = []storagecatalog.PhysicalRef{{StoragePhysicalRefID: "ref-payload", StorageEntryID: detail.Entry.StorageEntryID, RefKind: storagecatalog.PhysicalRefKindLaneFile, URI: oldPath, Status: storagecatalog.PhysicalRefStatusAvailable}}
		}
		details = append(details, detail)
	}
	manifest, err := storagemigration.Plan(storagemigration.PlanInput{NodeID: "main", LayoutFingerprint: "layout", Now: time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC), Roots: []storagemigration.RootMapping{{Name: "lane", OldRoot: oldRoot, NewRoot: newRoot}}, Details: details})
	if err != nil {
		t.Fatal(err)
	}
	set, err := writeFilesystemManifestArtifact(pathValue, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Batches) < 3 {
		t.Fatalf("batch count = %d, want at least 3 bounded children", len(set.Batches))
	}
	for _, batch := range set.Batches {
		if batch.ActionCount > maxFilesystemManifestRecords || batch.EncodedBytes > maxFilesystemManifestBatchBytes {
			t.Fatalf("unreadable batch descriptor: %#v", batch)
		}
	}
	indexPath := filepath.Join(pathValue, "index.json")
	payload, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	var reviewed storagemigration.ManifestSet
	if err := json.Unmarshal(payload, &reviewed); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 27, 13, 0, 0, 0, time.UTC)
	reviewed.Review = storagemigration.Review{ReviewedBy: "integrator", ReviewedAt: &now}
	payload, err = json.MarshalIndent(reviewed, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, append(payload, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	completeFilesystemRootMove(t, oldRoot, newRoot)
	artifact, err := readFilesystemManifestArtifact(pathValue)
	if err != nil {
		t.Fatalf("read artifact produced by writer: %v", err)
	}
	result, err := applyFilesystemManifestArtifact(context.Background(), acceptingFilesystemRebinder{}, artifact, "main", "layout")
	if err != nil {
		t.Fatalf("apply artifact produced by dry-run writer: %v", err)
	}
	if result.BatchesApplied != len(set.Batches) {
		t.Fatalf("apply result = %#v", result)
	}
}

type acceptingFilesystemRebinder struct{}

func (acceptingFilesystemRebinder) RebindPaths(_ context.Context, input storagecatalog.RebindPathsInput) (storagecatalog.RebindPathsResult, error) {
	return storagecatalog.RebindPathsResult{PhysicalRefsUpdated: len(input.PhysicalRefs), EntriesUpdated: len(input.Entries)}, nil
}

func (acceptingFilesystemRebinder) RebindPathBatches(_ context.Context, batchCount int, load storagecatalog.RebindPathsBatchLoader) (storagecatalog.RebindPathsResult, error) {
	result := storagecatalog.RebindPathsResult{}
	for index := 0; index < batchCount; index++ {
		input, err := load(index)
		if err != nil {
			return storagecatalog.RebindPathsResult{}, err
		}
		result.PhysicalRefsUpdated += len(input.PhysicalRefs)
		result.EntriesUpdated += len(input.Entries)
	}
	return result, nil
}

func TestStorageFilesystemRollbackCommandWritesUnreviewedExactInverseAndRestoresCatalog(t *testing.T) {
	top := t.TempDir()
	oldRoot := filepath.Join(top, "old")
	newRoot := filepath.Join(top, "new")
	if err := os.MkdirAll(oldRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(oldRoot, "payload")
	newPath := filepath.Join(newRoot, "payload")
	if err := os.WriteFile(oldPath, []byte("rollback CLI fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	detail := storagecatalog.EntryDetail{Entry: storagecatalog.Entry{StorageEntryID: "entry"}, PhysicalRefs: []storagecatalog.PhysicalRef{{StoragePhysicalRefID: "ref", StorageEntryID: "entry", RefKind: storagecatalog.PhysicalRefKindLaneFile, URI: oldPath, Status: storagecatalog.PhysicalRefStatusAvailable}}}
	forward, err := storagemigration.Plan(storagemigration.PlanInput{NodeID: "main", LayoutFingerprint: "pre-cutover-layout", Roots: []storagemigration.RootMapping{{Name: "lane", OldRoot: oldRoot, NewRoot: newRoot}}, Details: []storagecatalog.EntryDetail{detail}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	forward.Review = storagemigration.Review{ReviewedBy: "integrator", ReviewedAt: &now}
	forwardPath := filepath.Join(top, "forward.json")
	rollbackPath := filepath.Join(top, "rollback.json")
	if err := writeFilesystemManifest(forwardPath, forward); err != nil {
		t.Fatal(err)
	}
	cmd := newStorageFilesystemMigrateCommand(&options{})
	cmd.SetArgs([]string{"--rollback-from", forwardPath, "--manifest", rollbackPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("rollback generation command: %v", err)
	}
	rollback, err := readFilesystemManifest(rollbackPath)
	if err != nil {
		t.Fatal(err)
	}
	if rollback.Review.ReviewedBy != "" || rollback.Review.ReviewedAt != nil || rollback.LayoutFingerprint != forward.LayoutFingerprint || len(rollback.Actions) != 1 {
		t.Fatalf("generated rollback = %#v", rollback)
	}
	if rollback.Roots[0].OldRoot != newRoot || rollback.Roots[0].NewRoot != oldRoot || rollback.Actions[0].OldURI != newPath || rollback.Actions[0].NewURI != oldPath {
		t.Fatalf("rollback is not exact inverse: %#v", rollback)
	}
	cmd = newStorageFilesystemMigrateCommand(&options{})
	cmd.SetArgs([]string{"--rollback-from", forwardPath, "--manifest", rollbackPath})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "without overwrite") {
		t.Fatalf("rollback overwrite error = %v", err)
	}
	completeFilesystemRootMove(t, oldRoot, newRoot)
	catalog := &rollbackCatalogFixture{uri: oldPath}
	if _, err := storagemigration.Apply(context.Background(), catalog, storagemigration.ApplyInput{Manifest: forward, NodeID: "main", LayoutFingerprint: "pre-cutover-layout", LoadedFromFile: true}); err != nil {
		t.Fatal(err)
	}
	completeFilesystemRootMove(t, newRoot, oldRoot)
	if _, err := storagemigration.Apply(context.Background(), catalog, storagemigration.ApplyInput{Manifest: rollback, NodeID: "main", LayoutFingerprint: "pre-cutover-layout", LoadedFromFile: true}); err == nil || !strings.Contains(err.Error(), "not reviewed") {
		t.Fatalf("unreviewed rollback apply error = %v", err)
	}
	rollback.Review = storagemigration.Review{ReviewedBy: "integrator", ReviewedAt: &now}
	if _, err := storagemigration.Apply(context.Background(), catalog, storagemigration.ApplyInput{Manifest: rollback, NodeID: "main", LayoutFingerprint: "pre-cutover-layout", LoadedFromFile: true}); err != nil {
		t.Fatal(err)
	}
	if catalog.uri != oldPath {
		t.Fatalf("catalog URI after exact rollback = %q, want %q", catalog.uri, oldPath)
	}
}

func TestStorageFilesystemRollbackCommandRejectsUnreviewedAndConflictedForwardArtifacts(t *testing.T) {
	root := t.TempDir()
	oldRoot := filepath.Join(root, "old")
	newRoot := filepath.Join(root, "new")
	if err := os.MkdirAll(oldRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest, err := storagemigration.Plan(storagemigration.PlanInput{NodeID: "main", LayoutFingerprint: "layout", Roots: []storagemigration.RootMapping{{Name: "empty", OldRoot: oldRoot, NewRoot: newRoot}}})
	if err != nil {
		t.Fatal(err)
	}
	unreviewedPath := filepath.Join(root, "unreviewed.json")
	if err := writeFilesystemManifest(unreviewedPath, manifest); err != nil {
		t.Fatal(err)
	}
	cmd := newStorageFilesystemMigrateCommand(&options{})
	cmd.SetArgs([]string{"--rollback-from", unreviewedPath, "--manifest", filepath.Join(root, "rollback-unreviewed.json")})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "not reviewed") {
		t.Fatalf("unreviewed forward error = %v", err)
	}
	now := time.Now().UTC()
	manifest.Review = storagemigration.Review{ReviewedBy: "integrator", ReviewedAt: &now}
	manifest.Conflicts = []storagemigration.Conflict{{Code: "operator_block", Path: oldRoot, Detail: "requires disposition"}}
	if err := storagemigration.RefreshManifestHash(&manifest); err != nil {
		t.Fatal(err)
	}
	conflictedPath := filepath.Join(root, "conflicted.json")
	if err := writeFilesystemManifest(conflictedPath, manifest); err != nil {
		t.Fatal(err)
	}
	cmd = newStorageFilesystemMigrateCommand(&options{})
	cmd.SetArgs([]string{"--rollback-from", conflictedPath, "--manifest", filepath.Join(root, "rollback-conflicted.json")})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "open conflicts") {
		t.Fatalf("conflicted forward error = %v", err)
	}
}

func TestStorageFilesystemRollbackCommandStreamsManifestSetToBoundedInverse(t *testing.T) {
	top := t.TempDir()
	oldRoot := filepath.Join(top, "old")
	newRoot := filepath.Join(top, "new")
	if err := os.MkdirAll(oldRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(oldRoot, "payload")
	if err := os.WriteFile(oldPath, []byte("bounded rollback set"), 0o600); err != nil {
		t.Fatal(err)
	}
	details := make([]storagecatalog.EntryDetail, 0, maxFilesystemManifestRecords+1)
	for index := 0; index < maxFilesystemManifestRecords+1; index++ {
		detail := storagecatalog.EntryDetail{Entry: storagecatalog.Entry{StorageEntryID: fmt.Sprintf("entry-%06d", index), OriginalSourcePath: oldPath}}
		if index == 0 {
			detail.PhysicalRefs = []storagecatalog.PhysicalRef{{StoragePhysicalRefID: "ref", StorageEntryID: detail.Entry.StorageEntryID, RefKind: storagecatalog.PhysicalRefKindLaneFile, URI: oldPath, Status: storagecatalog.PhysicalRefStatusAvailable}}
		}
		details = append(details, detail)
	}
	forward, err := storagemigration.Plan(storagemigration.PlanInput{NodeID: "main", LayoutFingerprint: "pre-cutover-layout", Roots: []storagemigration.RootMapping{{Name: "lane", OldRoot: oldRoot, NewRoot: newRoot}}, Details: details})
	if err != nil {
		t.Fatal(err)
	}
	forwardPath := filepath.Join(top, "forward-set")
	forwardSet, err := writeFilesystemManifestArtifact(forwardPath, forward)
	if err != nil {
		t.Fatal(err)
	}
	if len(forwardSet.Batches) < 2 {
		t.Fatalf("forward set has %d batches", len(forwardSet.Batches))
	}
	now := time.Now().UTC()
	forwardSet.Review = storagemigration.Review{ReviewedBy: "integrator", ReviewedAt: &now}
	indexPayload, err := json.MarshalIndent(forwardSet, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(forwardPath, "index.json"), append(indexPayload, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	rollbackPath := filepath.Join(top, "rollback-set")
	cmd := newStorageFilesystemMigrateCommand(&options{})
	cmd.SetArgs([]string{"--rollback-from", forwardPath, "--manifest", rollbackPath})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	artifact, err := readFilesystemManifestArtifact(rollbackPath)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Set == nil || len(artifact.Set.Batches) != len(forwardSet.Batches) || artifact.Set.Review.ReviewedBy != "" || artifact.Set.Review.ReviewedAt != nil {
		t.Fatalf("rollback set = %#v", artifact.Set)
	}
	if artifact.Set.Roots[0].OldRoot != newRoot || artifact.Set.Roots[0].NewRoot != oldRoot || artifact.Set.LayoutFingerprint != forwardSet.LayoutFingerprint {
		t.Fatalf("rollback set roots/fingerprint = %#v", artifact.Set)
	}
	if _, err := applyFilesystemManifestArtifact(context.Background(), acceptingFilesystemRebinder{}, artifact, "main", "pre-cutover-layout"); err == nil || !strings.Contains(err.Error(), "not reviewed") {
		t.Fatalf("unreviewed rollback set apply error = %v", err)
	}
	for _, descriptor := range artifact.Set.Batches {
		batch, err := loadFilesystemManifestBatch(artifact.Path, *artifact.Set, descriptor)
		if err != nil {
			t.Fatal(err)
		}
		if batch.Review.ReviewedBy != "" || batch.Review.ReviewedAt != nil || batch.Roots[0].OldRoot != newRoot || batch.Roots[0].NewRoot != oldRoot || descriptor.EncodedBytes > maxFilesystemManifestBatchBytes {
			t.Fatalf("rollback child %s = %#v", descriptor.File, batch)
		}
	}

	forwardArtifact, err := readFilesystemManifestArtifact(forwardPath)
	if err != nil {
		t.Fatal(err)
	}
	completeFilesystemRootMove(t, oldRoot, newRoot)
	forwardResult, err := applyFilesystemManifestArtifact(context.Background(), acceptingFilesystemRebinder{}, forwardArtifact, "main", "pre-cutover-layout")
	if err != nil {
		t.Fatalf("apply reviewed forward manifest set: %v", err)
	}
	if forwardResult.BatchesApplied != len(forwardSet.Batches) {
		t.Fatalf("forward set apply result = %#v", forwardResult)
	}

	completeFilesystemRootMove(t, newRoot, oldRoot)
	artifact.Set.Review = storagemigration.Review{ReviewedBy: "integrator", ReviewedAt: &now}
	rollbackResult, err := applyFilesystemManifestArtifact(context.Background(), acceptingFilesystemRebinder{}, artifact, "main", "pre-cutover-layout")
	if err != nil {
		t.Fatalf("apply independently reviewed rollback manifest set: %v", err)
	}
	if rollbackResult.BatchesApplied != len(artifact.Set.Batches) || rollbackResult.Catalog.PhysicalRefsUpdated != 1 || rollbackResult.Catalog.EntriesUpdated != len(details) {
		t.Fatalf("rollback set apply result = %#v", rollbackResult)
	}
}

type rollbackCatalogFixture struct {
	uri string
}

func (r *rollbackCatalogFixture) RebindPaths(_ context.Context, input storagecatalog.RebindPathsInput) (storagecatalog.RebindPathsResult, error) {
	result := storagecatalog.RebindPathsResult{}
	for _, ref := range input.PhysicalRefs {
		if r.uri == ref.NewURI {
			result.AlreadyApplied++
			continue
		}
		if r.uri != ref.ExpectedURI {
			return result, fmt.Errorf("catalog URI %q does not match expected %q", r.uri, ref.ExpectedURI)
		}
		r.uri = ref.NewURI
		result.PhysicalRefsUpdated++
	}
	return result, nil
}

func TestStorageFilesystemManifestSummaryAndVerifyJSONStayBoundedAndFailClosed(t *testing.T) {
	cmd := &cobra.Command{}
	var output bytes.Buffer
	cmd.SetOut(&output)
	summary := filesystemManifestSummary{SchemaVersion: storagemigration.ManifestSetSchemaVersion, ManifestPath: "/tmp/review", ManifestSetHash: strings.Repeat("a", 64), BatchCount: 250, ActionCount: 500000, ReviewRequired: true}
	if err := renderStorageFilesystemManifest(cmd, &options{jsonOutput: true}, summary); err != nil {
		t.Fatal(err)
	}
	if output.Len() > 2048 || strings.Contains(output.String(), "\"actions\"") {
		t.Fatalf("summary unexpectedly expanded manifest payload: bytes=%d output=%s", output.Len(), output.String())
	}
	output.Reset()
	result := storagemigration.VerifyResult{CheckedAvailableRefs: 1, Findings: []storagemigration.VerifyFinding{{Code: "root_escape", Path: "/outside", Detail: "outside canonical root"}}, OK: false}
	err := renderStorageFilesystemVerify(cmd, &options{jsonOutput: true}, result)
	if err == nil {
		t.Fatal("JSON verification findings returned success")
	}
	var decoded storagemigration.VerifyResult
	if decodeErr := json.Unmarshal(output.Bytes(), &decoded); decodeErr != nil || decoded.OK || len(decoded.Findings) != 1 {
		t.Fatalf("structured findings were not emitted before failure: decoded=%#v err=%v output=%s", decoded, decodeErr, output.String())
	}
	output.Reset()
	if err := renderStorageFilesystemVerify(cmd, &options{}, result); err == nil {
		t.Fatal("human verification findings returned success")
	}
	if !strings.Contains(output.String(), "root_escape") || !strings.Contains(output.String(), "Findings: 1") {
		t.Fatalf("human findings missing before failure: %s", output.String())
	}
}

func TestStorageFilesystemManifestSetRejectsChildInventoryDifferentFromIndex(t *testing.T) {
	artifactPath := filepath.Join(t.TempDir(), "migration-set")
	root := t.TempDir()
	oldRoot := filepath.Join(root, "old")
	newRoot := filepath.Join(root, "new")
	if err := os.MkdirAll(oldRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(oldRoot, "payload")
	if err := os.WriteFile(oldPath, []byte("inventory-bound child"), 0o600); err != nil {
		t.Fatal(err)
	}
	detail := storagecatalog.EntryDetail{Entry: storagecatalog.Entry{StorageEntryID: "entry"}, PhysicalRefs: []storagecatalog.PhysicalRef{{StoragePhysicalRefID: "ref", StorageEntryID: "entry", RefKind: storagecatalog.PhysicalRefKindLaneFile, URI: oldPath, Status: storagecatalog.PhysicalRefStatusAvailable}}}
	manifest, err := storagemigration.Plan(storagemigration.PlanInput{NodeID: "main", LayoutFingerprint: "layout", Roots: []storagemigration.RootMapping{{Name: "lane", OldRoot: oldRoot, NewRoot: newRoot}}, Details: []storagecatalog.EntryDetail{detail}})
	if err != nil {
		t.Fatal(err)
	}
	set, err := writeFilesystemManifestArtifact(artifactPath, manifest)
	if err != nil {
		t.Fatal(err)
	}
	batchPath := filepath.Join(artifactPath, set.Batches[0].File)
	var batch storagemigration.Manifest
	if err := readBoundedJSON(batchPath, maxFilesystemManifestBatchBytes, &batch); err != nil {
		t.Fatal(err)
	}
	batch.RootInventory[0].InternalMarkerCount++
	if err := storagemigration.RefreshManifestHash(&batch); err != nil {
		t.Fatal(err)
	}
	batchPayload, err := json.MarshalIndent(batch, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	batchPayload = append(batchPayload, '\n')
	if err := os.WriteFile(batchPath, batchPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	set.Batches[0].ManifestHash = batch.ManifestHash
	set.Batches[0].EncodedBytes = int64(len(batchPayload))
	if err := storagemigration.RefreshManifestSetHash(&set); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	set.Review = storagemigration.Review{ReviewedBy: "integrator", ReviewedAt: &now}
	indexPayload, err := json.MarshalIndent(set, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactPath, "index.json"), append(indexPayload, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	artifact, err := readFilesystemManifestArtifact(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	completeFilesystemRootMove(t, oldRoot, newRoot)
	if _, err := applyFilesystemManifestArtifact(context.Background(), acceptingFilesystemRebinder{}, artifact, "main", "layout"); err == nil || !strings.Contains(err.Error(), "different root inventory") {
		t.Fatalf("child/set inventory mismatch error = %v", err)
	}
}

func completeFilesystemRootMove(t *testing.T, sourceRoot, destinationRoot string) {
	t.Helper()
	if err := os.MkdirAll(destinationRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if err := os.Rename(filepath.Join(sourceRoot, entry.Name()), filepath.Join(destinationRoot, entry.Name())); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(sourceRoot); err != nil {
		t.Fatal(err)
	}
}

func TestStorageFilesystemRootSpecsRejectDuplicateNestedAndCollidingMappings(t *testing.T) {
	layout := filesystemlayout.Layout{DataRoot: "/unreadable/data", ImportsRoot: "/srv/imports", ArchiveRoot: "/srv/archive", BoxRoot: "/srv/box"}
	tests := [][]string{
		{"one=/old:/new/a", "two=/old/child:/new/b"},
		{"one=/old/a:/new", "two=/old/b:/new/child"},
		{"one=/migration:/migration/new"},
	}
	for _, specs := range tests {
		if _, err := filesystemMigrationRoots(layout, specs); err == nil {
			t.Fatalf("ambiguous CLI roots accepted: %#v", specs)
		}
	}
}

func TestStorageFilesystemExplicitRootsReplaceDefaults(t *testing.T) {
	layout := filesystemlayout.Layout{
		DataRoot:                    "/var/lib/loom",
		ImportsRoot:                 "/var/lib/loom/lane/accepted",
		ArchiveRoot:                 "/var/lib/loom/storage-archive",
		DeprecatedMainDocumentsRoot: "/var/lib/loom/main-documents",
		BoxRoot:                     "/home/loomadmin/loom-box",
	}
	specs := []string{
		"box=/home/loomadmin/loom-box:/srv/loom/box",
		"archive=/var/lib/loom/storage-archive:/srv/loom/storage/archive",
	}
	got, err := filesystemMigrationRoots(layout, specs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "archive" || got[1].Name != "box" {
		t.Fatalf("explicit roots were not authoritative: %#v", got)
	}
}

func TestParseStorageFilesystemRootSpec(t *testing.T) {
	got, err := parseFilesystemRootSpec("lane=/var/lib/loom/lane/accepted:/srv/loom/storage/imports")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "lane" || got.OldRoot != "/var/lib/loom/lane/accepted" || got.NewRoot != "/srv/loom/storage/imports" {
		t.Fatalf("root mapping = %#v", got)
	}
	if _, err := parseFilesystemRootSpec("broken"); err == nil {
		t.Fatal("invalid root mapping was accepted")
	}
}

func TestStorageTreeCommandHumanAndJSON(t *testing.T) {
	entry := storageCommandEntryFixture(t)
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/storage/entries" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
		response.WriteJSON(w, http.StatusOK, response.Success("corr_test", []storagecatalog.Entry{entry}))
	})
	defer shutdown()

	stdout, stderr, err := executeRootCommand("--socket", socketPath, "storage", "tree")
	if err != nil {
		t.Fatalf("storage tree returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{"LOOM storage catalog paths", "macbook/Backups/Documents/current/report.md", entry.StorageEntryID} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("human output missing %q:\n%s", want, stdout)
		}
	}

	stdout, stderr, err = executeRootCommand("--socket", socketPath, "--json", "storage", "tree")
	if err != nil {
		t.Fatalf("storage tree json returned error: %v stderr=%s", err, stderr)
	}
	var envelope response.Envelope[[]storagecatalog.Entry]
	if decodeErr := json.Unmarshal([]byte(stdout), &envelope); decodeErr != nil {
		t.Fatalf("decode json output: %v output=%s", decodeErr, stdout)
	}
	if !envelope.OK || len(envelope.Data) != 1 || envelope.Data[0].StorageEntryID != entry.StorageEntryID {
		t.Fatalf("unexpected json envelope: %#v", envelope)
	}
}

func TestStorageListRender(t *testing.T) {
	entry := storageCommandEntryFixture(t)
	cmd := NewRootCommand()
	var output strings.Builder
	cmd.SetOut(&output)
	renderStorageEntryList(cmd, []storagecatalog.Entry{entry})
	if !strings.Contains(output.String(), "ENTRY") || !strings.Contains(output.String(), entry.StorageEntryID) || !strings.Contains(output.String(), entry.LogicalPath) {
		t.Fatalf("rendered list missing expected fields:\n%s", output.String())
	}
}

func TestStorageExportCommands(t *testing.T) {
	status := storagedoctor.ExportCompatibilityInfo{
		Deprecated: true,
		Active:     false,
		LegacyPath: "/tmp/loom-export",
		Message:    "The generated filesystem export is retired and is not active operational state.",
		ReplacementCommands: []string{
			"loom storage filesystem status",
			"loom storage filesystem verify",
		},
	}
	requests := 0
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/storage/export/status" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
		requests++
		response.WriteJSON(w, http.StatusOK, response.Success("corr_test", status))
	})
	defer shutdown()

	stdout, stderr, err := executeRootCommand("--socket", socketPath, "storage", "export", "status")
	if err != nil {
		t.Fatalf("storage export status returned error: %v stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "LOOM generated storage export is retired") || !strings.Contains(stdout, "/tmp/loom-export") {
		t.Fatalf("status output missing expected fields:\n%s", stdout)
	}

	for _, args := range [][]string{
		{"storage", "export", "refresh"},
		{"storage", "export", "refresh", "status"},
		{"storage", "export", "rebuild", "--dry-run"},
	} {
		allArgs := append([]string{"--socket", socketPath}, args...)
		stdout, stderr, err = executeRootCommand(allArgs...)
		if err != nil {
			t.Fatalf("deprecated diagnostic %v returned error: %v stderr=%s", args, err, stderr)
		}
		if !strings.Contains(stdout, "generated storage export is retired") || strings.Contains(stdout, "requested: true") {
			t.Fatalf("deprecated diagnostic %v was not inert:\n%s", args, stdout)
		}
	}
	if requests != 4 {
		t.Fatalf("compatibility diagnostics made %d requests, want four read-only status requests", requests)
	}
}

func TestStorageDoctorAndRepairCommands(t *testing.T) {
	entry := storageCommandEntryFixture(t)
	ref := storagecatalog.PhysicalRef{
		StoragePhysicalRefID: ids.NewStoragePhysicalRefID(),
		StorageEntryID:       entry.StorageEntryID,
		RefKind:              storagecatalog.PhysicalRefKindBackupArtifact,
		URI:                  "/var/lib/loom/private-backups/report.md",
		Status:               storagecatalog.PhysicalRefStatusAvailable,
		CreatedAt:            time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC),
		UpdatedAt:            time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC),
	}
	rebuildAt := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	retentionStatus := storagecatalog.RetentionStatus{Entries: 1, Retained: 1, GeneratedAt: rebuildAt}
	mainStatus := mainstorage.Status{BackingRoot: "/tmp/main-documents", Exists: true, GeneratedAt: rebuildAt}
	filesystemStatus := storagedoctor.FilesystemStatus{SchemaVersion: "v0.7", Status: storagedoctor.StatusOK, Catalog: storagedoctor.CatalogStatus{QueryLimit: 5000, Returned: 1}, GeneratedAt: rebuildAt}
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/storage/entries":
			response.WriteJSON(w, http.StatusOK, response.Success("corr_test", []storagecatalog.Entry{entry}))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/storage/filesystem/status":
			response.WriteJSON(w, http.StatusOK, response.Success("corr_test", filesystemStatus))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/storage/retention/status":
			response.WriteJSON(w, http.StatusOK, response.Success("corr_test", retentionStatus))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/storage/main-documents/status":
			response.WriteJSON(w, http.StatusOK, response.Success("corr_test", mainStatus))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/storage/entries/"):
			response.WriteJSON(w, http.StatusOK, response.Success("corr_test", storagecatalog.EntryDetail{Entry: entry, PhysicalRefs: []storagecatalog.PhysicalRef{ref}}))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/projects":
			response.WriteJSON(w, http.StatusOK, response.Success("corr_test", []map[string]any{}))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	})
	defer shutdown()

	stdout, stderr, err := executeRootCommand("--socket", socketPath, "storage", "doctor", "--limit", "20", "--inspect-limit", "20")
	if err != nil {
		t.Fatalf("storage doctor returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{"LOOM storage doctor: ok", "catalog.physical_refs", "project.runtime_archives"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, stdout)
		}
	}

	stdout, stderr, err = executeRootCommand("--socket", socketPath, "--json", "storage", "doctor", "--include-fidelity", "--limit", "20", "--inspect-limit", "20")
	if err != nil {
		t.Fatalf("storage doctor --include-fidelity returned error: %v stderr=%s", err, stderr)
	}
	var doctorReport storagedoctor.Report
	if err := json.Unmarshal([]byte(stdout), &doctorReport); err != nil {
		t.Fatalf("decode doctor report: %v output=%s", err, stdout)
	}
	foundFidelity := false
	for _, check := range doctorReport.Checks {
		if check.ID == "storage.fidelity" {
			foundFidelity = true
			break
		}
	}
	if !foundFidelity {
		t.Fatalf("doctor report missing storage.fidelity check: %#v", doctorReport.Checks)
	}

	stdout, stderr, err = executeRootCommand("--socket", socketPath, "storage", "repair", "export", "--dry-run")
	if err == nil {
		t.Fatalf("retired storage repair export unexpectedly succeeded: %s", stdout)
	}

	stdout, stderr, err = executeRootCommand("storage", "repair", "catalog", "--source", "dropzone")
	if err != nil {
		t.Fatalf("storage repair catalog returned error: %v stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "Storage catalog repair: warning") || !strings.Contains(stdout, "dropzone accept/import worker") {
		t.Fatalf("repair catalog output missing expected plan:\n%s", stdout)
	}
}

func TestStorageMountStatusCommand(t *testing.T) {
	stdout, stderr, err := executeRootCommand("storage", "mount-status", "--doctor", "--mount-path", t.TempDir(), "--smb-host", "127.0.0.1")
	if err != nil {
		t.Fatalf("storage mount-status returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{"LOOM Main mount status", "Protocol: smb", "SMB host: 127.0.0.1", "tcp_445"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("mount-status output missing %q:\n%s", want, stdout)
		}
	}

	stdout, stderr, err = executeRootCommand("storage", "mount-status", "--protocol", "rclone", "--mount-path", t.TempDir(), "--rclone-config", filepath.Join(t.TempDir(), "rclone.conf"))
	if err != nil {
		t.Fatalf("storage mount-status --protocol rclone returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{"Protocol: rclone", "Rclone config:", "rclone_config"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("rclone mount-status output missing %q:\n%s", want, stdout)
		}
	}

	stdout, stderr, err = executeRootCommand("--json", "storage", "mount-status", "--mount-path", t.TempDir(), "--smb-host", "127.0.0.1", "--smb-share", "LOOM-Main", "--smb-user", "loomshare")
	if err != nil {
		t.Fatalf("storage mount-status --json returned error: %v stderr=%s", err, stderr)
	}
	var status struct {
		Protocol string `json:"protocol"`
		SMBHost  string `json:"smb_host"`
		SMBShare string `json:"smb_share"`
		SMBUser  string `json:"smb_user"`
	}
	if err := json.Unmarshal([]byte(stdout), &status); err != nil {
		t.Fatalf("decode mount status JSON: %v\n%s", err, stdout)
	}
	if status.Protocol != "smb" || status.SMBHost != "127.0.0.1" || status.SMBShare != "LOOM-Main" || status.SMBUser != "loomshare" {
		t.Fatalf("unexpected JSON mount status: %#v", status)
	}

	stdout, stderr, err = executeRootCommand("storage", "mount-status", "--protocol", "nfs")
	if err == nil {
		t.Fatalf("storage mount-status --protocol nfs succeeded unexpectedly stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "Unsupported LOOM Main mount protocol") {
		t.Fatalf("invalid protocol stderr missing useful error:\n%s", stderr)
	}
}

func TestStorageFidelityReportCommand(t *testing.T) {
	size := int64(64)
	entry := storagecatalog.Entry{
		StorageEntryID: ids.NewStorageEntryID(), StorageClass: storagecatalog.StorageClassPrivateBackup,
		SourceArea: storagecatalog.SourceAreaDocuments, OriginNodeKey: "macbook", LogicalPath: "scripts/run.sh",
		FileClass: storagecatalog.FileClassCode, ProcessingState: storagecatalog.ProcessingStateBackupOnly,
		AvailabilityState: storagecatalog.AvailabilityStateAvailable, SizeBytes: &size,
		Metadata: json.RawMessage(`{"filesystem_observation":{"executable":true,"source_mode":493}}`),
	}
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/storage/entries" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
		if r.URL.Query().Get("node") != "macbook" {
			t.Fatalf("expected node filter to be sent, got query %s", r.URL.RawQuery)
		}
		response.WriteJSON(w, http.StatusOK, response.Success("corr_test", []storagecatalog.Entry{entry}))
	})
	defer shutdown()

	stdout, stderr, err := executeRootCommand("--socket", socketPath, "storage", "fidelity", "report", "--node", "macbook", "--prefix", "macbook/Backups/Documents")
	if err != nil {
		t.Fatalf("storage fidelity report returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{"Storage fidelity report", "warnings=1", "executable_mode_not_restored", "partially_safe"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("fidelity report output missing %q:\n%s", want, stdout)
		}
	}
}

func TestStorageSafeDeleteCheckCommand(t *testing.T) {
	result := storageretention.SafeToDeleteResult{
		Ref:      "macbook/Backups/Documents/current/secret.md",
		Decision: storageretention.DecisionNotSafe,
		Safe:     false,
		Blockers: []string{"permission_denied: source path could not be inspected because of permissions"},
	}
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/storage/safe-to-delete" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
		var input storageretention.SafeToDeleteInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode input: %v", err)
		}
		if input.Ref != result.Ref {
			t.Fatalf("unexpected ref %#v", input)
		}
		response.WriteJSON(w, http.StatusOK, response.Success("corr_test", result))
	})
	defer shutdown()

	stdout, stderr, err := executeRootCommand("--socket", socketPath, "storage", "safe-delete", "check", result.Ref)
	if err != nil {
		t.Fatalf("storage safe-delete check returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{"Safe to delete: not_safe", "permission_denied"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("safe-delete output missing %q:\n%s", want, stdout)
		}
	}
}

func TestStorageFidelityBackfillCommand(t *testing.T) {
	result := storagefidelity.BackfillResult{
		Source:                 storagefidelity.BackfillSourceWatchedRoots,
		DryRun:                 true,
		Scanned:                1,
		Observed:               1,
		FindingsPlanned:        1,
		PayloadRewrites:        0,
		ExportRefreshRequested: false,
		Items: []storagefidelity.BackfillItem{{
			StorageEntryID: ids.NewStorageEntryID(),
			SourceArea:     storagecatalog.SourceAreaDocuments,
			NodeKey:        "macbook",
			LogicalPath:    "scripts/run.sh",
			ViewPath:       "macbook/Backups/Documents/current/scripts/run.sh",
			ObjectKind:     "regular_file",
			Observed:       true,
			Findings: []storagefidelity.Finding{{
				Severity: "warning",
				Kind:     "executable_mode_not_restored",
				Summary:  "safe view strips executable bits",
			}},
		}},
		Notes:       []string{"retained payload bytes are not rewritten"},
		GeneratedAt: time.Now().UTC(),
	}
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/storage/fidelity/backfill" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
		var input storagefidelity.BackfillInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode input: %v", err)
		}
		if input.Source != storagefidelity.BackfillSourceWatchedRoots || !input.DryRun || input.Apply || input.Yes {
			t.Fatalf("unexpected backfill input: %#v", input)
		}
		response.WriteJSON(w, http.StatusOK, response.Success("corr_test", result))
	})
	defer shutdown()

	stdout, stderr, err := executeRootCommand("--socket", socketPath, "storage", "fidelity", "backfill", "--source", "watched-roots", "--dry-run")
	if err != nil {
		t.Fatalf("storage fidelity backfill returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{"Storage fidelity backfill: dry-run", "Source: watched-roots", "findings_planned=1", "Payload rewrites: 0", "executable_mode_not_restored"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("fidelity backfill output missing %q:\n%s", want, stdout)
		}
	}
}

func TestStorageInventoryAndCleanupCommands(t *testing.T) {
	root := t.TempDir()
	mainDoc := filepath.Join(root, "Documents", "v0-6-2-slice-02-smoke.txt")
	if err := os.MkdirAll(filepath.Dir(mainDoc), 0o755); err != nil {
		t.Fatalf("mkdir main Documents fixture: %v", err)
	}
	if err := os.WriteFile(mainDoc, []byte("main owned"), 0o644); err != nil {
		t.Fatalf("write main Documents fixture: %v", err)
	}
	generated := filepath.Join(root, "macbook", "Backups", "Documents", "current", ".loom-acceptance", "v0-6-2-slice-02-smoke.txt")
	if err := os.MkdirAll(filepath.Dir(generated), 0o755); err != nil {
		t.Fatalf("mkdir generated fixture: %v", err)
	}
	if err := os.WriteFile(generated, []byte("generated view"), 0o644); err != nil {
		t.Fatalf("write generated fixture: %v", err)
	}

	stdout, stderr, err := executeRootCommand("--json", "storage", "inventory", "--root", root, "--production", "--production-override")
	if err != nil {
		t.Fatalf("storage inventory returned error: %v stderr=%s", err, stderr)
	}
	var inventory storagecleanup.InventoryResult
	if err := json.Unmarshal([]byte(stdout), &inventory); err != nil {
		t.Fatalf("decode inventory: %v output=%s", err, stdout)
	}
	if inventory.Summary.Candidates != 2 || inventory.Summary.AutoApply != 1 || inventory.Summary.ManualReview != 1 {
		t.Fatalf("unexpected inventory summary: %#v items=%#v", inventory.Summary, inventory.Items)
	}

	planPath := filepath.Join(root, "cleanup-plan.json")
	stdout, stderr, err = executeRootCommand("storage", "cleanup", "plan", "--root", root, "--production", "--production-override", "--out", planPath)
	if err != nil {
		t.Fatalf("storage cleanup plan returned error: %v stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "LOOM storage cleanup plan") || !strings.Contains(stdout, "Plan file: "+planPath) {
		t.Fatalf("cleanup plan output missing expected fields:\n%s", stdout)
	}
	if _, err := os.Stat(planPath); err != nil {
		t.Fatalf("cleanup plan file missing: %v", err)
	}

	stdout, stderr, err = executeRootCommand("--json", "storage", "cleanup", "apply", "--plan", planPath, "--dry-run")
	if err != nil {
		t.Fatalf("storage cleanup apply dry-run returned error: %v stderr=%s", err, stderr)
	}
	var dryRun storagecleanup.ApplyResult
	if err := json.Unmarshal([]byte(stdout), &dryRun); err != nil {
		t.Fatalf("decode cleanup dry-run: %v output=%s", err, stdout)
	}
	if dryRun.Summary.WouldQuarantine != 1 || dryRun.Summary.ManualReview != 1 {
		t.Fatalf("unexpected dry-run summary: %#v changes=%#v", dryRun.Summary, dryRun.Changes)
	}
	if _, err := os.Stat(generated); err != nil {
		t.Fatalf("dry-run should preserve generated fixture: %v", err)
	}

	stdout, stderr, err = executeRootCommand("storage", "cleanup", "apply", "--plan", planPath, "--yes")
	if err != nil {
		t.Fatalf("storage cleanup apply returned error: %v stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "LOOM storage cleanup applied") || !strings.Contains(stdout, "quarantined=1") {
		t.Fatalf("cleanup apply output missing expected fields:\n%s", stdout)
	}
	if _, err := os.Stat(generated); !os.IsNotExist(err) {
		t.Fatalf("generated fixture should be moved to quarantine, err=%v", err)
	}
	if _, err := os.Stat(mainDoc); err != nil {
		t.Fatalf("main Documents fixture should be preserved: %v", err)
	}
}

func TestStorageMainDocumentsStatusCommand(t *testing.T) {
	status := mainstorage.Status{
		BackingRoot:           "/tmp/main-documents",
		Exists:                true,
		StableWindowSeconds:   15,
		FilesDiscovered:       1,
		FilesAccepted:         1,
		FilesMissingCataloged: 1,
		GeneratedAt:           time.Date(2026, 6, 5, 17, 0, 0, 0, time.UTC),
		Imports: []mainstorage.FileStatus{{
			RelativePath:   "Reports/report.md",
			State:          mainstorage.StateAccepted,
			StorageEntryID: ids.NewStorageEntryID(),
		}, {
			RelativePath:  "Reports/._report.md",
			State:         mainstorage.StateIgnored,
			IgnoredReason: "AppleDouble sidecar",
		}},
	}
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/storage/main-documents/status" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
		response.WriteJSON(w, http.StatusOK, response.Success("corr_test", status))
	})
	defer shutdown()

	stdout, stderr, err := executeRootCommand("--socket", socketPath, "storage", "main-documents", "status")
	if err != nil {
		t.Fatalf("storage main-documents status returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{"Main Documents import", "/tmp/main-documents", "accepted", "Reports/report.md", "ignored", "AppleDouble sidecar", "Missing cataloged files are deferred"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("status output missing %q:\n%s", want, stdout)
		}
	}
}

func TestStorageMainDocumentsReconcileCommand(t *testing.T) {
	result := mainstorage.ReconcileResult{
		BackingRoot:      "/srv/loom/box/Documents",
		LegacyRoot:       "/var/lib/loom/main-documents",
		DryRun:           true,
		FilesPresent:     1,
		ActiveCataloged:  2,
		MissingCataloged: 1,
		Migration: mainstorage.DocumentsMigrationPlan{
			Status:          mainstorage.DocumentsMigrationReady,
			LegacyRoot:      "/var/lib/loom/main-documents",
			CanonicalRoot:   "/srv/loom/box/Documents",
			LegacyExists:    true,
			CanonicalExists: true,
			LegacyOnly:      1,
			Equivalent:      2,
			Items: []mainstorage.DocumentsMigrationItem{{
				RelativePath: "Legacy/report.md",
				State:        mainstorage.DocumentsMigrationLegacyOnly,
			}},
		},
		Items: []mainstorage.FileStatus{{
			RelativePath:   "Missing/report.md",
			State:          mainstorage.StateMissingDeferred,
			StorageEntryID: ids.NewStorageEntryID(),
		}},
	}
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/storage/main-documents/reconcile" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
		var input mainstorage.ReconcileInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode input: %v", err)
		}
		if !input.DryRun || input.Yes || !input.CompareLegacy {
			t.Fatalf("expected dry-run reconcile input, got %#v", input)
		}
		response.WriteJSON(w, http.StatusOK, response.Success("corr_test", result))
	})
	defer shutdown()

	stdout, stderr, err := executeRootCommand("--socket", socketPath, "storage", "main-documents", "reconcile", "--dry-run")
	if err != nil {
		t.Fatalf("storage main-documents reconcile returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{"Main Documents reconciliation: dry-run", "Canonical Box Documents: /srv/loom/box/Documents", "Legacy Documents migration: ready", "legacy_only=1", "No files were moved", "Missing cataloged: 1", "Missing/report.md", mainstorage.StateMissingDeferred, "No tombstones were written"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("reconcile output missing %q:\n%s", want, stdout)
		}
	}
}

func TestStorageMainDocumentsReconcileCommandDoesNotAdvertiseExportRefresh(t *testing.T) {
	result := mainstorage.ReconcileResult{
		BackingRoot:      "/srv/loom/box/Documents",
		DryRun:           false,
		ActiveCataloged:  1,
		MissingCataloged: 1,
		Tombstoned:       1,
		Migration: mainstorage.DocumentsMigrationPlan{
			Status:        mainstorage.DocumentsMigrationNotNeeded,
			LegacyRoot:    "/var/lib/loom/main-documents",
			CanonicalRoot: "/srv/loom/box/Documents",
		},
	}
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/storage/main-documents/reconcile" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
		var input mainstorage.ReconcileInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode input: %v", err)
		}
		if input.DryRun || !input.Yes {
			t.Fatalf("expected applied reconcile input, got %#v", input)
		}
		response.WriteJSON(w, http.StatusOK, response.Success("corr_test", result))
	})
	defer shutdown()

	stdout, stderr, err := executeRootCommand("--socket", socketPath, "storage", "main-documents", "reconcile", "--yes")
	if err != nil {
		t.Fatalf("storage main-documents reconcile returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{"Main Documents reconciliation: applied", "Canonical Box Documents: /srv/loom/box/Documents", "Tombstoned: 1"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("reconcile output missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(strings.ToLower(stdout), "export") {
		t.Fatalf("reconcile output still advertises storage export work:\n%s", stdout)
	}
}

func TestStorageMainDocumentsReconcileCommandFailsOnMigrationConflict(t *testing.T) {
	result := mainstorage.ReconcileResult{
		BackingRoot: "/srv/loom/box/Documents",
		DryRun:      true,
		Migration: mainstorage.DocumentsMigrationPlan{
			Status:        mainstorage.DocumentsMigrationBlocked,
			LegacyRoot:    "/var/lib/loom/main-documents",
			CanonicalRoot: "/srv/loom/box/Documents",
			Conflicts:     1,
			Items: []mainstorage.DocumentsMigrationItem{{
				RelativePath: "conflict.md",
				State:        mainstorage.DocumentsMigrationConflict,
				Reason:       "file checksums differ",
			}},
		},
		CatalogMutationBlocked:     true,
		CatalogMutationBlockReason: "legacy and canonical Documents contain differing collisions",
	}
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/storage/main-documents/reconcile" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
		response.WriteJSON(w, http.StatusOK, response.Success("corr_test", result))
	})
	defer shutdown()

	stdout, _, err := executeRootCommand("--socket", socketPath, "storage", "main-documents", "reconcile", "--dry-run")
	if err == nil {
		t.Fatalf("storage main-documents reconcile returned nil error: stdout=%s", stdout)
	}
	for _, want := range []string{"Legacy Documents migration: blocked", "conflicts=1", "conflict.md", "file checksums differ", "Catalog mutation blocked"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("reconcile output missing %q:\n%s", want, stdout)
		}
	}
}

func TestStorageMainDocumentsRetentionBackfillCommand(t *testing.T) {
	result := mainstorage.RetentionBackfillResult{
		BackingRoot:     "/tmp/main-documents",
		RetentionRoot:   "/tmp/storage-retention",
		DryRun:          true,
		Scanned:         2,
		AlreadyRetained: 1,
		WouldCreate:     1,
		Items: []mainstorage.RetentionBackfillItem{{
			RelativePath:   "Report.md",
			StorageEntryID: ids.NewStorageEntryID(),
			State:          "would_create_retention_payload",
			RetentionPath:  "/tmp/storage-retention/main-documents/by-sha256/aaa",
		}},
	}
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/storage/main-documents/retention/backfill" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
		var input mainstorage.RetentionBackfillInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode input: %v", err)
		}
		if !input.DryRun || input.Yes {
			t.Fatalf("expected dry-run backfill input, got %#v", input)
		}
		response.WriteJSON(w, http.StatusOK, response.Success("corr_test", result))
	})
	defer shutdown()

	stdout, stderr, err := executeRootCommand("--socket", socketPath, "storage", "main-documents", "retention", "backfill")
	if err != nil {
		t.Fatalf("storage main-documents retention backfill returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{"Main Documents retention backfill: dry-run", "would_create=1", "Report.md", "would_create_retention_payload"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("retention backfill output missing %q:\n%s", want, stdout)
		}
	}
}

func TestStorageRepairCatalogMainDocumentsCommand(t *testing.T) {
	result := mainstorage.ReconcileResult{
		BackingRoot:      "/tmp/main-documents",
		DryRun:           true,
		MissingCataloged: 1,
	}
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/storage/main-documents/reconcile" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
		var input mainstorage.ReconcileInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode input: %v", err)
		}
		if !input.DryRun || input.Yes || input.Reason != "unit-test" || input.CreatedBy != "cli-test" {
			t.Fatalf("unexpected repair reconcile input: %#v", input)
		}
		response.WriteJSON(w, http.StatusOK, response.Success("corr_test", result))
	})
	defer shutdown()

	stdout, stderr, err := executeRootCommand("--socket", socketPath, "storage", "repair", "catalog", "--source", "main-documents", "--dry-run", "--reason", "unit-test", "--created-by", "cli-test")
	if err != nil {
		t.Fatalf("storage repair catalog main-documents returned error: %v stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "Main Documents reconciliation: dry-run") || !strings.Contains(stdout, "Missing cataloged: 1") {
		t.Fatalf("repair main-documents output missing expected summary:\n%s", stdout)
	}
}

func TestStorageStatusAndVerifyCommands(t *testing.T) {
	size := int64(11)
	entry := storagecatalog.Entry{
		StorageEntryID:     "storage_entry_test",
		StorageClass:       storagecatalog.StorageClassPrivateBackup,
		SourceArea:         storagecatalog.SourceAreaDocuments,
		OriginNodeKey:      "macbook",
		LogicalPath:        "report.md",
		CurrentViewPath:    "main/Documents/report.md",
		SizeBytes:          &size,
		FileClass:          storagecatalog.FileClassMarkdown,
		ProcessingState:    storagecatalog.ProcessingStateBackupOnly,
		AvailabilityState:  storagecatalog.AvailabilityStateAvailable,
		RetentionState:     storagecatalog.RetentionStateRetained,
		ChecksumAlgorithm:  filetransfer.ChecksumSHA256,
		ChecksumHex:        filetransfer.SHA256Hex([]byte("hello world")),
		OriginalSourcePath: "Documents/report.md",
	}
	ref := storagecatalog.PhysicalRef{
		StoragePhysicalRefID: ids.NewStoragePhysicalRefID(),
		StorageEntryID:       entry.StorageEntryID,
		RefKind:              storagecatalog.PhysicalRefKindBackupArtifact,
		URI:                  "/var/lib/loom/private-backups/report.tar",
		Status:               storagecatalog.PhysicalRefStatusAvailable,
	}
	detail := storagecatalog.EntryDetail{Entry: entry, PhysicalRefs: []storagecatalog.PhysicalRef{ref}}
	resolved := storageview.ResolveResult{
		Path: "main/Documents/report.md",
		ViewEntry: storageview.ViewEntry{
			ViewPath:       "main/Documents/report.md",
			EntryKind:      storageview.EntryKindFile,
			StorageEntryID: entry.StorageEntryID,
			Permissions:    storageview.PermissionWritable,
			Writable:       true,
		},
		EntryDetail: &detail,
	}
	safe := storageretention.SafeToDeleteResult{
		Ref:      entry.StorageEntryID,
		Decision: storageretention.DecisionSafe,
		Safe:     true,
		Reasons:  []string{"accepted by main"},
	}
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/storage/resolve":
			if r.URL.Query().Get("path") != "main/Documents/report.md" {
				t.Fatalf("unexpected resolve path: %s", r.URL.RawQuery)
			}
			response.WriteJSON(w, http.StatusOK, response.Success("corr_test", resolved))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/storage/entries/storage_entry_test":
			response.WriteJSON(w, http.StatusOK, response.Success("corr_test", detail))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/storage/safe-to-delete":
			response.WriteJSON(w, http.StatusOK, response.Success("corr_test", safe))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	})
	defer shutdown()

	stdout, stderr, err := executeRootCommand("--socket", socketPath, "storage", "status", "/Volumes/LOOM-Main/main/Documents/report.md")
	if err != nil {
		t.Fatalf("storage status returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{"Storage status: safe_to_delete", "main/Documents/report.md", "Storage entry: storage_entry_test", "Safe to delete: safe"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("storage status output missing %q:\n%s", want, stdout)
		}
	}

	stdout, stderr, err = executeRootCommand("--socket", socketPath, "--json", "storage", "verify", "storage_entry_test")
	if err != nil {
		t.Fatalf("storage verify returned error: %v stderr=%s", err, stderr)
	}
	var verification storageVerificationReport
	if err := json.Unmarshal([]byte(stdout), &verification); err != nil {
		t.Fatalf("decode verification: %v output=%s", err, stdout)
	}
	if !verification.Verified || verification.Status.State != storageHumanStateSafeToDelete {
		t.Fatalf("unexpected verification report: %#v", verification)
	}
}

func TestStorageStatusExplainsMissingMainDocumentsPath(t *testing.T) {
	mainStatus := mainstorage.Status{
		BackingRoot:           "/var/lib/loom/main-documents",
		Exists:                true,
		FilesMissingCataloged: 1,
		GeneratedAt:           time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC),
	}
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/storage/resolve":
			if r.URL.Query().Get("path") != "main/Documents/Documents sent from Mac/Morgan" {
				t.Fatalf("unexpected resolve path: %s", r.URL.RawQuery)
			}
			http.NotFound(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/storage/main-documents/status":
			response.WriteJSON(w, http.StatusOK, response.Success("corr_test", mainStatus))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	})
	defer shutdown()

	stdout, stderr, err := executeRootCommand("--socket", socketPath, "storage", "status", "/Volumes/LOOM-Main/main/Documents/Documents sent from Mac/Morgan")
	if err != nil {
		t.Fatalf("storage status returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{
		"Storage status: unknown",
		"No accepted catalog entry matched this main/Documents path.",
		"missing active catalog row",
		"loom storage main-documents reconcile --dry-run",
		"loom storage main-documents reconcile --yes",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("storage status output missing %q:\n%s", want, stdout)
		}
	}
}

func TestStorageFailuresAndTransfersCommands(t *testing.T) {
	failedEntry := storagecatalog.Entry{
		StorageEntryID:    "storage_entry_failed",
		StorageClass:      storagecatalog.StorageClassPrivateBackup,
		SourceArea:        storagecatalog.SourceAreaDocuments,
		OriginNodeKey:     "macbook",
		LogicalPath:       "broken.bin",
		CurrentViewPath:   "macbook/Backups/Documents/current/broken.bin",
		FileClass:         storagecatalog.FileClassBinary,
		ProcessingState:   storagecatalog.ProcessingStateFailed,
		AvailabilityState: storagecatalog.AvailabilityStateFailed,
		RetentionState:    storagecatalog.RetentionStateNone,
		UpdatedAt:         time.Date(2026, 6, 7, 10, 0, 0, 0, time.UTC),
	}
	acceptedDropzone := storagecatalog.Entry{
		StorageEntryID:     "storage_entry_dropzone",
		StorageClass:       storagecatalog.StorageClassDropzoneCustody,
		SourceArea:         storagecatalog.SourceAreaDropzone,
		OriginNodeKey:      "macbook",
		DropzoneTransferID: "drop_video",
		LogicalPath:        "video.mov",
		CurrentViewPath:    "macbook/Dropzone/video.mov",
		FileClass:          storagecatalog.FileClassVideo,
		ProcessingState:    storagecatalog.ProcessingStateMetadataOnly,
		AvailabilityState:  storagecatalog.AvailabilityStateAvailable,
	}
	mainStatus := mainstorage.Status{
		BackingRoot: "/var/lib/loom/main-documents",
		Exists:      true,
		GeneratedAt: time.Date(2026, 6, 7, 10, 0, 0, 0, time.UTC),
		Imports: []mainstorage.FileStatus{{
			RelativePath: "partial.mov",
			State:        mainstorage.StatePendingImport,
			DelayReason:  "waiting for stable file",
		}, {
			RelativePath:  "._sidecar",
			State:         mainstorage.StateIgnored,
			IgnoredReason: "AppleDouble sidecar",
		}, {
			RelativePath: "Removed/report.md",
			State:        mainstorage.StateMissingDeferred,
			DelayReason:  "missing source deferred",
		}},
	}
	transfers := []filetransfer.Status{{
		Manifest: filetransfer.Manifest{
			TransferID:         "file_transfer_failed",
			SourceNodeKey:      "macbook",
			SourceRootKey:      "documents",
			SourceRelativePath: "large.bin",
			TransferKind:       filetransfer.KindWatchedRootBackup,
			Status:             filetransfer.StatusFailed,
			LastErrorMessage:   "network timeout",
			ChunkCount:         4,
		},
		AcceptedChunks: 1,
	}}
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/storage/entries":
			response.WriteJSON(w, http.StatusOK, response.Success("corr_test", []storagecatalog.Entry{failedEntry, acceptedDropzone}))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/storage/main-documents/status":
			response.WriteJSON(w, http.StatusOK, response.Success("corr_test", mainStatus))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/storage/filesystem/status":
			response.WriteJSON(w, http.StatusOK, response.Success("corr_test", storagedoctor.FilesystemStatus{
				SchemaVersion: "v0.7",
				Status:        storagedoctor.StatusWarning,
				Roots: []storagedoctor.PhysicalRootStatus{{
					Key: "imports", Path: "/srv/loom/storage/imports", Status: storagedoctor.StatusWarning, Message: "root does not exist",
				}},
			}))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/file-transfers":
			response.WriteJSON(w, http.StatusOK, response.Success("corr_test", transfers))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	})
	defer shutdown()

	stdout, stderr, err := executeRootCommand("--socket", socketPath, "storage", "failures")
	if err != nil {
		t.Fatalf("storage failures returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{"LOOM storage failures", "storage_entry_failed", "Hidden routine ignored files: 1", mainstorage.StateMissingDeferred, "physical_root", "file_transfer_failed"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("storage failures output missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "AppleDouble sidecar") {
		t.Fatalf("storage failures should hide routine sidecar noise:\n%s", stdout)
	}

	stdout, stderr, err = executeRootCommand("--socket", socketPath, "storage", "transfers")
	if err != nil {
		t.Fatalf("storage transfers returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{"LOOM storage transfers", "file_transfer_failed", "partial.mov", "storage_entry_dropzone"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("storage transfers output missing %q:\n%s", want, stdout)
		}
	}
}

func TestStorageRetentionCommands(t *testing.T) {
	status := storagecatalog.RetentionStatus{Entries: 2, Snapshots: 1, Pending: 1}
	safe := storageretention.SafeToDeleteResult{
		Ref:      "storage_entry_test",
		Decision: storageretention.DecisionSafe,
		Safe:     true,
		Reasons:  []string{"accepted"},
	}
	fetch := storageretention.FetchResult{
		Ref:             "storage_entry_test",
		DestinationPath: "/tmp/fetched.txt",
		BytesWritten:    8,
		Entry:           storagecatalog.Entry{StorageEntryID: "storage_entry_test"},
	}
	restore := storageretention.RestoreResult{FetchResult: fetch}
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/storage/retention/status":
			response.WriteJSON(w, http.StatusOK, response.Success("corr_test", status))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/storage/safe-to-delete":
			response.WriteJSON(w, http.StatusOK, response.Success("corr_test", safe))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/storage/fetch":
			response.WriteJSON(w, http.StatusOK, response.Success("corr_test", fetch))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/storage/restore":
			response.WriteJSON(w, http.StatusOK, response.Success("corr_test", restore))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	})
	defer shutdown()

	stdout, stderr, err := executeRootCommand("--socket", socketPath, "storage", "retention", "status")
	if err != nil {
		t.Fatalf("storage retention status returned error: %v stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "Storage retention") || !strings.Contains(stdout, "Entries: 2") {
		t.Fatalf("retention output missing expected fields:\n%s", stdout)
	}

	stdout, stderr, err = executeRootCommand("--socket", socketPath, "storage", "safe-to-delete", "storage_entry_test")
	if err != nil {
		t.Fatalf("storage safe-to-delete returned error: %v stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "Safe to delete: safe") || !strings.Contains(stdout, "accepted") {
		t.Fatalf("safe-to-delete output missing expected fields:\n%s", stdout)
	}

	stdout, stderr, err = executeRootCommand("--socket", socketPath, "storage", "fetch", "storage_entry_test", "--to", "/tmp/fetched.txt")
	if err != nil {
		t.Fatalf("storage fetch returned error: %v stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "Storage file fetched") || !strings.Contains(stdout, "/tmp/fetched.txt") {
		t.Fatalf("fetch output missing expected fields:\n%s", stdout)
	}

	stdout, stderr, err = executeRootCommand("--socket", socketPath, "storage", "restore", "storage_entry_test", "--to", "/tmp/fetched.txt")
	if err != nil {
		t.Fatalf("storage restore returned error: %v stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "Storage file restored") || !strings.Contains(stdout, "/tmp/fetched.txt") {
		t.Fatalf("restore output missing expected fields:\n%s", stdout)
	}
}

func TestStorageArchiveCommand(t *testing.T) {
	result := storagearchive.ArchiveResult{
		ArchiveManifest: storagecatalog.ArchiveManifest{
			ArchiveManifestID: "storage_archive_manifest_test",
			ArchiveKey:        "taxes-2026",
			ArchiveKind:       "document_archive",
			Status:            "complete",
		},
		Manifest: storagearchive.ManifestDocument{
			ArchiveManifestID: "storage_archive_manifest_test",
			ArchiveKey:        "taxes-2026",
			ArchiveKind:       "document_archive",
			SourceRef:         "macbook/Backups/Documents/current/Taxes",
			TargetPath:        "main/Archive/Documents/Taxes-2026",
			OwnerNodeKey:      "main",
		},
		ManifestPath: "/var/lib/loom/storage-archive/manifests/storage_archive_manifest_test/manifest.json",
		Entries: []storagearchive.ArchivedEntry{{
			SourceStorageEntryID:  "storage_entry_source",
			ArchiveStorageEntryID: "storage_entry_archive",
			SourceViewPath:        "macbook/Backups/Documents/current/Taxes/receipt.pdf",
			ArchiveViewPath:       "main/Archive/Documents/Taxes-2026/receipt.pdf",
		}},
	}
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/storage/archive" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
		var input storagearchive.ArchiveInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode input: %v", err)
		}
		if input.SourceRef != "macbook/Backups/Documents/current/Taxes" || input.TargetPath != "main/Archive/Documents/Taxes-2026" || !input.MarkSourceArchived {
			t.Fatalf("unexpected archive input: %#v", input)
		}
		response.WriteJSON(w, http.StatusOK, response.Success("corr_test", result))
	})
	defer shutdown()

	stdout, stderr, err := executeRootCommand("--socket", socketPath, "storage", "archive", "macbook/Backups/Documents/current/Taxes", "--to", "main/Archive/Documents/Taxes-2026", "--mark-source-archived")
	if err != nil {
		t.Fatalf("storage archive returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{"Storage archived", "storage_archive_manifest_test", "Taxes-2026", "receipt.pdf"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("archive output missing %q:\n%s", want, stdout)
		}
	}
}

func storageCommandEntryFixture(t *testing.T) storagecatalog.Entry {
	t.Helper()
	size := int64(10)
	return storagecatalog.Entry{
		StorageEntryID:    ids.NewStorageEntryID(),
		StorageClass:      storagecatalog.StorageClassPrivateBackup,
		SourceArea:        storagecatalog.SourceAreaDocuments,
		OriginNodeKey:     "macbook",
		LogicalPath:       "report.md",
		SizeBytes:         &size,
		FileClass:         storagecatalog.FileClassMarkdown,
		ProcessingState:   storagecatalog.ProcessingStateBackupOnly,
		AvailabilityState: storagecatalog.AvailabilityStateAvailable,
		RetentionState:    storagecatalog.RetentionStateNone,
	}
}

func startStorageCommandServer(t *testing.T, handler http.HandlerFunc) (string, func()) {
	t.Helper()
	socketPath := filepath.Join(os.TempDir(), ids.NewStorageEntryID()+".sock")
	_ = os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	server := &http.Server{Handler: handler}
	done := make(chan struct{})
	go func() {
		_ = server.Serve(listener)
		close(done)
	}()
	return socketPath, func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		<-done
		_ = os.Remove(socketPath)
	}
}
