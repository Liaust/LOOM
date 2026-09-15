package storagemigration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"loom.local/loom/internal/storagecatalog"
)

func TestFixturePlanMoveRebindVerifyAndRollback(t *testing.T) {
	top := t.TempDir()
	oldRoot := filepath.Join(top, "old")
	newRoot := filepath.Join(top, "new")
	if err := os.MkdirAll(filepath.Join(oldRoot, "node", "batch"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(oldRoot, "node", "batch", "report.md ")
	newPath := filepath.Join(newRoot, "node", "batch", "report.md ")
	if err := os.WriteFile(oldPath, []byte("canonical fixture\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	detail := storagecatalog.EntryDetail{Entry: storagecatalog.Entry{StorageEntryID: "entry-1", OriginalSourcePath: oldPath, CurrentViewPath: oldPath}, PhysicalRefs: []storagecatalog.PhysicalRef{{StoragePhysicalRefID: "ref-1", StorageEntryID: "entry-1", RefKind: storagecatalog.PhysicalRefKindLaneFile, URI: oldPath, Status: storagecatalog.PhysicalRefStatusAvailable}}}
	fingerprint := LayoutFingerprint("main", map[string]string{"imports": newRoot})
	manifest, err := Plan(PlanInput{NodeID: "main", LayoutFingerprint: fingerprint, Roots: []RootMapping{{Name: "imports", OldRoot: oldRoot, NewRoot: newRoot}}, Details: []storagecatalog.EntryDetail{detail}, Now: time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(manifest.Actions) != 2 || len(manifest.Conflicts) != 0 {
		t.Fatalf("manifest = %#v", manifest)
	}
	var physicalAction PathAction
	for _, action := range manifest.Actions {
		if action.PhysicalRefID == "ref-1" {
			physicalAction = action
		}
	}
	if physicalAction.Expected.ChecksumType != "sha256" || physicalAction.Expected.ChecksumHex == "" || manifest.Roots[0].OldFilesystemID == "" {
		t.Fatalf("manifest lacks evidence: %#v", manifest)
	}
	moveReviewedRoot(t, oldRoot, newRoot)
	reviewerTime := time.Date(2026, 8, 27, 12, 30, 0, 0, time.UTC)
	manifest.Review = Review{ReviewedBy: "integrator", ReviewedAt: &reviewerTime}
	catalog := &memoryRebinder{refURI: oldPath, original: oldPath, view: oldPath}
	result, err := Apply(context.Background(), catalog, ApplyInput{Manifest: manifest, NodeID: "main", LayoutFingerprint: fingerprint, LoadedFromFile: true})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.VerifiedDestinations != 1 || catalog.refURI != newPath || catalog.original != newPath || catalog.view != newPath {
		t.Fatalf("apply result=%#v catalog=%#v", result, catalog)
	}
	verification := Verify(VerifyInput{Details: []storagecatalog.EntryDetail{{Entry: detail.Entry, PhysicalRefs: []storagecatalog.PhysicalRef{{StoragePhysicalRefID: "ref-1", StorageEntryID: "entry-1", RefKind: storagecatalog.PhysicalRefKindLaneFile, URI: newPath, Status: storagecatalog.PhysicalRefStatusAvailable}}}}, CanonicalRoots: []string{newRoot}, ClassRoots: map[string][]string{storagecatalog.PhysicalRefClassCanonicalCustody: {newRoot}}})
	if !verification.OK {
		t.Fatalf("Verify = %#v", verification)
	}

	rollback, err := RollbackManifest(manifest, time.Date(2026, 8, 27, 13, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	moveReviewedRoot(t, newRoot, oldRoot)
	rollback.Review = Review{ReviewedBy: "integrator", ReviewedAt: &reviewerTime}
	if _, err := Apply(context.Background(), catalog, ApplyInput{Manifest: rollback, NodeID: "main", LayoutFingerprint: fingerprint, LoadedFromFile: true}); err != nil {
		t.Fatalf("rollback Apply: %v", err)
	}
	if catalog.refURI != oldPath || catalog.original != oldPath || catalog.view != oldPath {
		t.Fatalf("rollback did not reproduce prior refs exactly: %#v", catalog)
	}
}

func TestPreCutoverWatchedBackupKeepsCanonicalLogicalIdentityAfterPhysicalRebind(t *testing.T) {
	root := t.TempDir()
	oldRoot := filepath.Join(root, "private-backups")
	newRoot := filepath.Join(root, "storage", "backups")
	relative := filepath.Join("macbook", "loom_box__documents", "batch-42", "payload", "Reports", "report.pdf")
	oldPath := filepath.Join(oldRoot, relative)
	newPath := filepath.Join(newRoot, relative)
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, []byte("pre-cutover watched backup"), 0o640); err != nil {
		t.Fatal(err)
	}
	viewPath := "backups/macbook/loom_box__documents/batch-42/payload/Reports/report.pdf"
	detail := storagecatalog.EntryDetail{Entry: storagecatalog.Entry{StorageEntryID: "entry", CurrentViewPath: viewPath}, PhysicalRefs: []storagecatalog.PhysicalRef{{StoragePhysicalRefID: "ref", StorageEntryID: "entry", RefKind: storagecatalog.PhysicalRefKindLocalPath, URI: oldPath, Status: storagecatalog.PhysicalRefStatusAvailable}}}
	manifest, err := Plan(PlanInput{NodeID: "main", LayoutFingerprint: "layout", Roots: []RootMapping{{Name: "user_backups", OldRoot: oldRoot, NewRoot: newRoot}}, Details: []storagecatalog.EntryDetail{detail}})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Conflicts) != 0 || len(manifest.Actions) != 1 || manifest.Actions[0].OldView != "" || manifest.Actions[0].NewView != "" {
		t.Fatalf("pre-cutover watched-backup plan = %#v", manifest)
	}
	moveReviewedRoot(t, oldRoot, newRoot)
	now := time.Now().UTC()
	manifest.Review = Review{ReviewedBy: "integrator", ReviewedAt: &now}
	catalog := &memoryRebinder{refURI: oldPath, view: viewPath}
	if _, err := Apply(context.Background(), catalog, ApplyInput{Manifest: manifest, NodeID: "main", LayoutFingerprint: "layout", LoadedFromFile: true}); err != nil {
		t.Fatal(err)
	}
	if catalog.refURI != newPath || catalog.view != viewPath {
		t.Fatalf("post-cutover catalog identity = ref %q view %q", catalog.refURI, catalog.view)
	}
}

func TestApplyRejectsRuntimeUnreviewedAndChangedEvidence(t *testing.T) {
	root := t.TempDir()
	oldRoot := filepath.Join(root, "old")
	newRoot := filepath.Join(root, "new")
	_ = os.Mkdir(oldRoot, 0o755)
	_ = os.Mkdir(newRoot, 0o755)
	oldPath := filepath.Join(oldRoot, "payload")
	newPath := filepath.Join(newRoot, "payload")
	_ = os.WriteFile(oldPath, []byte("before"), 0o600)
	fp := LayoutFingerprint("main", map[string]string{"new": newRoot})
	manifest, err := Plan(PlanInput{NodeID: "main", LayoutFingerprint: fp, Roots: []RootMapping{{Name: "new", OldRoot: oldRoot, NewRoot: newRoot}}, Details: []storagecatalog.EntryDetail{{Entry: storagecatalog.Entry{StorageEntryID: "entry"}, PhysicalRefs: []storagecatalog.PhysicalRef{{StoragePhysicalRefID: "ref", StorageEntryID: "entry", RefKind: storagecatalog.PhysicalRefKindLocalPath, URI: oldPath, Status: storagecatalog.PhysicalRefStatusAvailable}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(context.Background(), &memoryRebinder{}, ApplyInput{Manifest: manifest, NodeID: "main", LayoutFingerprint: fp}); err == nil || !strings.Contains(err.Error(), "generated-at-runtime") {
		t.Fatalf("runtime plan error = %v", err)
	}
	if _, err := Apply(context.Background(), &memoryRebinder{}, ApplyInput{Manifest: manifest, NodeID: "main", LayoutFingerprint: fp, LoadedFromFile: true}); err == nil || !strings.Contains(err.Error(), "not reviewed") {
		t.Fatalf("unreviewed error = %v", err)
	}
	moveReviewedRoot(t, oldRoot, newRoot)
	if err := os.WriteFile(newPath, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifest.Review = Review{ReviewedBy: "integrator", ReviewedAt: &now}
	if _, err := Apply(context.Background(), &memoryRebinder{}, ApplyInput{Manifest: manifest, NodeID: "main", LayoutFingerprint: fp, LoadedFromFile: true}); err == nil || (!strings.Contains(err.Error(), "evidence changed") && !strings.Contains(err.Error(), "physical inventory changed")) {
		t.Fatalf("changed evidence error = %v", err)
	}
}

func TestVerifyFindsDanglingDuplicateEscapeAndClassMismatch(t *testing.T) {
	root := t.TempDir()
	canonical := filepath.Join(root, "canonical")
	_ = os.Mkdir(canonical, 0o755)
	outside := filepath.Join(root, "outside")
	_ = os.WriteFile(outside, []byte("x"), 0o600)
	details := []storagecatalog.EntryDetail{
		{Entry: storagecatalog.Entry{StorageEntryID: "one"}, PhysicalRefs: []storagecatalog.PhysicalRef{{StoragePhysicalRefID: "one", RefKind: storagecatalog.PhysicalRefKindLaneFile, URI: outside, Status: storagecatalog.PhysicalRefStatusAvailable}}},
		{Entry: storagecatalog.Entry{StorageEntryID: "two"}, PhysicalRefs: []storagecatalog.PhysicalRef{{StoragePhysicalRefID: "two", RefKind: storagecatalog.PhysicalRefKindLaneFile, URI: outside, Status: storagecatalog.PhysicalRefStatusAvailable}, {StoragePhysicalRefID: "missing", RefKind: storagecatalog.PhysicalRefKindRetentionPayload, URI: filepath.Join(root, "missing"), Status: storagecatalog.PhysicalRefStatusAvailable}}},
	}
	result := Verify(VerifyInput{Details: details, CanonicalRoots: []string{canonical}, ClassRoots: map[string][]string{storagecatalog.PhysicalRefClassRetentionCopy: {filepath.Join(root, "retention")}}})
	if result.OK || len(result.Findings) < 4 {
		t.Fatalf("Verify = %#v", result)
	}
}

func TestValidateRootMappingsRejectsAmbiguityBeforePlanning(t *testing.T) {
	tests := []struct {
		name  string
		roots []RootMapping
		want  string
	}{
		{name: "duplicate name", roots: []RootMapping{{Name: "lane", OldRoot: "/old/a", NewRoot: "/new/a"}, {Name: "lane", OldRoot: "/old/b", NewRoot: "/new/b"}}, want: "duplicate migration root name"},
		{name: "nested old", roots: []RootMapping{{Name: "parent", OldRoot: "/old", NewRoot: "/new/a"}, {Name: "child", OldRoot: "/old/child", NewRoot: "/new/b"}}, want: "old migration roots"},
		{name: "nested new", roots: []RootMapping{{Name: "parent", OldRoot: "/old/a", NewRoot: "/new"}, {Name: "child", OldRoot: "/old/b", NewRoot: "/new/child"}}, want: "new migration roots"},
		{name: "old inside new", roots: []RootMapping{{Name: "first", OldRoot: "/migration/source", NewRoot: "/migration"}}, want: "unsafe migration nesting"},
		{name: "new inside old", roots: []RootMapping{{Name: "first", OldRoot: "/migration", NewRoot: "/migration/destination"}}, want: "unsafe migration nesting"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ValidateRootMappings(test.roots); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateRootMappings error = %v, want %q", err, test.want)
			}
			// The detail path would otherwise become an evidence conflict. A
			// mapping error must be returned before that path is inspected.
			_, err := Plan(PlanInput{NodeID: "main", LayoutFingerprint: "layout", Roots: test.roots, Details: []storagecatalog.EntryDetail{{Entry: storagecatalog.Entry{StorageEntryID: "never-inventoried", OriginalSourcePath: "/old/missing"}}}})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Plan error = %v, want central validation %q", err, test.want)
			}
		})
	}
}

func TestSplitManifestBoundsEveryProducedBatch(t *testing.T) {
	source := Manifest{
		SchemaVersion:     ManifestSchemaVersion,
		GeneratedAt:       time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC),
		NodeID:            "main",
		LayoutFingerprint: "layout",
		Roots:             []RootMapping{{Name: "lane", OldRoot: "/old", NewRoot: "/new"}},
	}
	for index := 0; index < 17; index++ {
		source.Actions = append(source.Actions, PathAction{ActionID: fmt.Sprintf("action-%02d", index), StorageEntryID: fmt.Sprintf("entry-%02d", index), OldURI: "/old/file", NewURI: "/new/file"})
	}
	if err := RefreshManifestHash(&source); err != nil {
		t.Fatal(err)
	}
	set, batches, err := SplitManifest(source, 4, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 5 || set.ActionCount != 17 || set.ConflictCount != 0 {
		t.Fatalf("split set=%#v batches=%d", set, len(batches))
	}
	for index, batch := range batches {
		payload, err := json.MarshalIndent(batch, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if len(batch.Actions) > 4 || len(payload)+1 > 4096 {
			t.Fatalf("batch %d is outside bounds: actions=%d bytes=%d", index, len(batch.Actions), len(payload)+1)
		}
		if err := ValidateManifestHash(batch); err != nil {
			t.Fatalf("batch %d hash: %v", index, err)
		}
	}
	if err := ValidateManifestSetHash(set); err != nil {
		t.Fatal(err)
	}
}

func TestSplitManifestKeepsOneStorageEntryTransactional(t *testing.T) {
	source := Manifest{
		SchemaVersion:     ManifestSchemaVersion,
		GeneratedAt:       time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC),
		NodeID:            "main",
		LayoutFingerprint: "layout",
		Roots:             []RootMapping{{Name: "lane", OldRoot: "/old", NewRoot: "/new"}},
	}
	for _, entryID := range []string{"entry-a", "entry-b"} {
		for index := 0; index < 4; index++ {
			source.Actions = append(source.Actions, PathAction{
				ActionID:       fmt.Sprintf("%s-action-%02d", entryID, index),
				StorageEntryID: entryID,
				OldURI:         "/old/file",
				NewURI:         "/new/file",
			})
		}
	}
	if err := RefreshManifestHash(&source); err != nil {
		t.Fatal(err)
	}
	_, batches, err := SplitManifest(source, 5, 8192)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 2 {
		t.Fatalf("batch count = %d, want 2", len(batches))
	}
	seen := map[string]int{}
	for batchIndex, batch := range batches {
		for _, action := range batch.Actions {
			if previous, exists := seen[action.StorageEntryID]; exists && previous != batchIndex {
				t.Fatalf("entry %s was split across batches %d and %d", action.StorageEntryID, previous, batchIndex)
			}
			seen[action.StorageEntryID] = batchIndex
		}
	}
}

func TestSplitManifestRejectsEntryLargerThanTransactionalLimit(t *testing.T) {
	source := Manifest{SchemaVersion: ManifestSchemaVersion, GeneratedAt: time.Now().UTC(), NodeID: "main", LayoutFingerprint: "layout", Roots: []RootMapping{{Name: "lane", OldRoot: "/old", NewRoot: "/new"}}}
	for index := 0; index < 3; index++ {
		source.Actions = append(source.Actions, PathAction{ActionID: fmt.Sprintf("action-%d", index), StorageEntryID: "one-entry"})
	}
	if err := RefreshManifestHash(&source); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SplitManifest(source, 2, 8192); err == nil || !strings.Contains(err.Error(), "transactional batch limit") {
		t.Fatalf("oversized entry error = %v", err)
	}
}

func TestSplitManifestScalesToProductionShapedActionCounts(t *testing.T) {
	const actionCount = 100_001
	source := Manifest{
		SchemaVersion:     ManifestSchemaVersion,
		GeneratedAt:       time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC),
		NodeID:            "main",
		LayoutFingerprint: "layout",
		Roots:             []RootMapping{{Name: "lane", OldRoot: "/old", NewRoot: "/new"}},
		Actions:           make([]PathAction, 0, actionCount),
	}
	for index := 0; index < actionCount; index++ {
		source.Actions = append(source.Actions, PathAction{ActionID: fmt.Sprintf("action-%06d", index), StorageEntryID: fmt.Sprintf("entry-%06d", index)})
	}
	if err := RefreshManifestHash(&source); err != nil {
		t.Fatal(err)
	}
	set, batches, err := SplitManifest(source, 2000, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if set.ActionCount != actionCount || len(batches) < 51 {
		t.Fatalf("production-shaped split totals: set=%#v batches=%d", set, len(batches))
	}
	counted := 0
	for index, batch := range batches {
		if len(batch.Actions) > 2000 {
			t.Fatalf("batch %d has %d actions", index, len(batch.Actions))
		}
		counted += len(batch.Actions)
	}
	if counted != actionCount {
		t.Fatalf("counted %d actions, want %d", counted, actionCount)
	}
}

func TestPlanRejectsIntermediateSymlinkEscapes(t *testing.T) {
	top := t.TempDir()
	oldRoot := filepath.Join(top, "old")
	newRoot := filepath.Join(top, "new")
	outside := filepath.Join(top, "outside")
	for _, root := range []string{oldRoot, newRoot, outside} {
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "payload"), []byte("escape"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(oldRoot, "linked")); err != nil {
		t.Fatal(err)
	}
	escaped := filepath.Join(oldRoot, "linked", "payload")
	manifest, err := Plan(PlanInput{NodeID: "main", LayoutFingerprint: "layout", Roots: []RootMapping{{Name: "lane", OldRoot: oldRoot, NewRoot: newRoot}}, Details: []storagecatalog.EntryDetail{{Entry: storagecatalog.Entry{StorageEntryID: "entry"}, PhysicalRefs: []storagecatalog.PhysicalRef{{StoragePhysicalRefID: "ref", StorageEntryID: "entry", RefKind: storagecatalog.PhysicalRefKindLaneFile, URI: escaped, Status: storagecatalog.PhysicalRefStatusAvailable}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Actions) != 0 || !manifestHasConflict(manifest, "source_path_escape") || !manifestHasConflict(manifest, "unexpected_symlink") || !manifestHasConflict(manifest, "catalog_path_unusable") {
		t.Fatalf("symlink escape plan = %#v", manifest)
	}
}

func TestPlanInventoriesMissingDuplicateSymlinkAndSpecialCustody(t *testing.T) {
	top := t.TempDir()
	oldRoot := filepath.Join(top, "old")
	newRoot := filepath.Join(top, "new")
	if err := os.MkdirAll(oldRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	trackedPath := filepath.Join(oldRoot, "tracked")
	if err := os.WriteFile(trackedPath, []byte("cataloged custody"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("tracked", filepath.Join(oldRoot, "payload-link")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(oldRoot, "unexpected-fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	missingPath := filepath.Join(oldRoot, "missing")
	details := []storagecatalog.EntryDetail{
		{Entry: storagecatalog.Entry{StorageEntryID: "entry-tracked"}, PhysicalRefs: []storagecatalog.PhysicalRef{
			{StoragePhysicalRefID: "ref-tracked-a", StorageEntryID: "entry-tracked", RefKind: storagecatalog.PhysicalRefKindLaneFile, URI: trackedPath, Status: storagecatalog.PhysicalRefStatusAvailable},
			{StoragePhysicalRefID: "ref-tracked-b", StorageEntryID: "entry-tracked", RefKind: storagecatalog.PhysicalRefKindLaneFile, URI: trackedPath, Status: storagecatalog.PhysicalRefStatusAvailable},
		}},
		{Entry: storagecatalog.Entry{StorageEntryID: "entry-missing"}, PhysicalRefs: []storagecatalog.PhysicalRef{
			{StoragePhysicalRefID: "ref-missing", StorageEntryID: "entry-missing", RefKind: storagecatalog.PhysicalRefKindLaneFile, URI: missingPath, Status: storagecatalog.PhysicalRefStatusAvailable},
		}},
	}
	manifest, err := Plan(PlanInput{NodeID: "main", LayoutFingerprint: "layout", Roots: []RootMapping{{Name: "lane", OldRoot: oldRoot, NewRoot: newRoot}}, Details: details})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.RootInventory) != 1 {
		t.Fatalf("root inventory = %#v", manifest.RootInventory)
	}
	evidence := manifest.RootInventory[0]
	if evidence.RegularFileCount != 1 || evidence.CatalogRefCount != 3 || evidence.CatalogPathCount != 2 ||
		evidence.MatchedFileCount != 1 || evidence.MissingCatalogCount != 1 || evidence.DuplicateCatalogCount != 1 ||
		evidence.SymlinkCount != 1 || evidence.SpecialFileCount != 1 || evidence.UntrackedFileCount != 0 || evidence.InventoryErrorCount != 0 {
		t.Fatalf("root inventory = %#v", evidence)
	}
	for _, code := range []string{"duplicate_catalog_path", "catalog_path_missing", "unexpected_symlink", "unexpected_special_file"} {
		if !manifestHasConflict(manifest, code) {
			t.Fatalf("missing conflict %q in %#v", code, manifest.Conflicts)
		}
	}
}

func TestPlanReportsDirtyEntryPathInsteadOfCreatingUnusableAction(t *testing.T) {
	top := t.TempDir()
	oldRoot := filepath.Join(top, "old")
	newRoot := filepath.Join(top, "new")
	if err := os.MkdirAll(oldRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldRoot, "payload"), []byte("dirty alias"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirtyPath := oldRoot + string(filepath.Separator) + "unused" + string(filepath.Separator) + ".." + string(filepath.Separator) + "payload"
	manifest, err := Plan(PlanInput{NodeID: "main", LayoutFingerprint: "layout", Roots: []RootMapping{{Name: "lane", OldRoot: oldRoot, NewRoot: newRoot}}, Details: []storagecatalog.EntryDetail{{Entry: storagecatalog.Entry{StorageEntryID: "entry", OriginalSourcePath: dirtyPath}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Actions) != 0 || !manifestHasConflict(manifest, "entry_path_escape") || !manifestHasConflict(manifest, "untracked_regular_file") {
		t.Fatalf("dirty-path plan = %#v", manifest)
	}
}

func TestPlanRejectsEquivalentCopyAsCompletedRename(t *testing.T) {
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
	if err := os.WriteFile(oldPath, []byte("same bytes"), 0o640); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("same bytes"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newPath, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	manifest, err := Plan(PlanInput{NodeID: "main", LayoutFingerprint: "layout", Roots: []RootMapping{{Name: "lane", OldRoot: oldRoot, NewRoot: newRoot}}, Details: []storagecatalog.EntryDetail{{Entry: storagecatalog.Entry{StorageEntryID: "entry"}, PhysicalRefs: []storagecatalog.PhysicalRef{{StoragePhysicalRefID: "ref", StorageEntryID: "entry", RefKind: storagecatalog.PhysicalRefKindLaneFile, URI: oldPath, Status: storagecatalog.PhysicalRefStatusAvailable}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Actions) != 0 || len(manifest.Conflicts) != 1 || manifest.Conflicts[0].Code != "destination_collision" {
		t.Fatalf("equivalent copy plan = %#v", manifest)
	}
}

func TestApplyRejectsHardLinkWhenSourceMoveIsIncomplete(t *testing.T) {
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
	if err := os.WriteFile(oldPath, []byte("one inode, two names"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(oldPath, newPath); err != nil {
		t.Fatal(err)
	}
	manifest, err := Plan(PlanInput{NodeID: "main", LayoutFingerprint: "layout", Roots: []RootMapping{{Name: "lane", OldRoot: oldRoot, NewRoot: newRoot}}, Details: []storagecatalog.EntryDetail{{Entry: storagecatalog.Entry{StorageEntryID: "entry"}, PhysicalRefs: []storagecatalog.PhysicalRef{{StoragePhysicalRefID: "ref", StorageEntryID: "entry", RefKind: storagecatalog.PhysicalRefKindLaneFile, URI: oldPath, Status: storagecatalog.PhysicalRefStatusAvailable}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Actions) != 1 || len(manifest.Conflicts) != 0 {
		t.Fatalf("hard-link manifest = %#v", manifest)
	}
	now := time.Now().UTC()
	manifest.Review = Review{ReviewedBy: "integrator", ReviewedAt: &now}
	if _, err := Apply(context.Background(), &memoryRebinder{refURI: oldPath}, ApplyInput{Manifest: manifest, NodeID: "main", LayoutFingerprint: "layout", LoadedFromFile: true}); err == nil || !strings.Contains(err.Error(), "source root") {
		t.Fatalf("incomplete move error = %v", err)
	}
}

func TestVerifyRejectsCanonicalPathThroughIntermediateSymlink(t *testing.T) {
	top := t.TempDir()
	canonical := filepath.Join(top, "canonical")
	outside := filepath.Join(top, "outside")
	if err := os.MkdirAll(canonical, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "payload"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(canonical, "linked")); err != nil {
		t.Fatal(err)
	}
	pathValue := filepath.Join(canonical, "linked", "payload")
	result := Verify(VerifyInput{Details: []storagecatalog.EntryDetail{{Entry: storagecatalog.Entry{StorageEntryID: "entry"}, PhysicalRefs: []storagecatalog.PhysicalRef{{StoragePhysicalRefID: "ref", RefKind: storagecatalog.PhysicalRefKindLaneFile, URI: pathValue, Status: storagecatalog.PhysicalRefStatusAvailable}}}}, CanonicalRoots: []string{canonical}, ClassRoots: map[string][]string{storagecatalog.PhysicalRefClassCanonicalCustody: {canonical}}})
	if result.OK || !verifyHasFinding(result, "root_escape") {
		t.Fatalf("symlink verification = %#v", result)
	}
}

func TestVerifyTreatsArchiveRefsAsCanonicalCustodyForDuplicates(t *testing.T) {
	archiveRoot := t.TempDir()
	pathValue := filepath.Join(archiveRoot, "payload")
	if err := os.WriteFile(pathValue, []byte("archived"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := Verify(VerifyInput{
		Details: []storagecatalog.EntryDetail{
			{Entry: storagecatalog.Entry{StorageEntryID: "entry-a"}, PhysicalRefs: []storagecatalog.PhysicalRef{{StoragePhysicalRefID: "ref-a", RefKind: storagecatalog.PhysicalRefKindArchiveFile, URI: pathValue, Status: storagecatalog.PhysicalRefStatusAvailable}}},
			{Entry: storagecatalog.Entry{StorageEntryID: "entry-b"}, PhysicalRefs: []storagecatalog.PhysicalRef{{StoragePhysicalRefID: "ref-b", RefKind: storagecatalog.PhysicalRefKindArchiveFile, URI: pathValue, Status: storagecatalog.PhysicalRefStatusAvailable}}},
		},
		CanonicalRoots: []string{archiveRoot},
		ClassRoots: map[string][]string{
			storagecatalog.PhysicalRefClassArchiveCopy: {archiveRoot},
		},
	})
	if result.OK || !verifyHasFinding(result, "duplicate_canonical_custody_path") {
		t.Fatalf("duplicate archive custody verification = %#v", result)
	}
}

func verifyHasFinding(result VerifyResult, code string) bool {
	for _, finding := range result.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func TestEntryOnlyPathMoveRequiresDestinationEvidence(t *testing.T) {
	top := t.TempDir()
	oldRoot := filepath.Join(top, "old")
	newRoot := filepath.Join(top, "new")
	if err := os.MkdirAll(oldRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(oldRoot, "entry-only")
	newPath := filepath.Join(newRoot, "entry-only")
	if err := os.Mkdir(oldPath, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest, err := Plan(PlanInput{NodeID: "main", LayoutFingerprint: "layout", Roots: []RootMapping{{Name: "documents", OldRoot: oldRoot, NewRoot: newRoot}}, Details: []storagecatalog.EntryDetail{{Entry: storagecatalog.Entry{StorageEntryID: "entry", OriginalSourcePath: oldPath}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Actions) != 1 || len(manifest.Actions[0].EntryEvidence) != 1 || manifest.Actions[0].PhysicalRefID != "" {
		t.Fatalf("entry-only manifest = %#v", manifest)
	}
	now := time.Now().UTC()
	manifest.Review = Review{ReviewedBy: "integrator", ReviewedAt: &now}
	if _, err := PreflightApplyActions(context.Background(), ApplyInput{Manifest: manifest, NodeID: "main", LayoutFingerprint: "layout", LoadedFromFile: true}); err == nil || !strings.Contains(err.Error(), "destination") {
		t.Fatalf("missing entry destination error = %v", err)
	}
	moveReviewedRoot(t, oldRoot, newRoot)
	catalog := &memoryRebinder{original: oldPath}
	result, err := Apply(context.Background(), catalog, ApplyInput{Manifest: manifest, NodeID: "main", LayoutFingerprint: "layout", LoadedFromFile: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.VerifiedDestinations != 1 || catalog.original != newPath {
		t.Fatalf("entry-only apply result=%#v catalog=%#v", result, catalog)
	}
}

func TestApplyRejectsDuplicateAndCrossRootActionsBeforeEvidence(t *testing.T) {
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
	if err := os.WriteFile(oldPath, []byte("semantic fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := Plan(PlanInput{NodeID: "main", LayoutFingerprint: "layout", Roots: []RootMapping{{Name: "lane", OldRoot: oldRoot, NewRoot: newRoot}}, Details: []storagecatalog.EntryDetail{{Entry: storagecatalog.Entry{StorageEntryID: "entry"}, PhysicalRefs: []storagecatalog.PhysicalRef{{StoragePhysicalRefID: "ref", StorageEntryID: "entry", RefKind: storagecatalog.PhysicalRefKindLaneFile, URI: oldPath, Status: storagecatalog.PhysicalRefStatusAvailable}}}}})
	if err != nil {
		t.Fatal(err)
	}
	manifest.Actions = append(manifest.Actions, manifest.Actions[0])
	if err := RefreshManifestHash(&manifest); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifest.Review = Review{ReviewedBy: "integrator", ReviewedAt: &now}
	if _, err := Apply(context.Background(), &memoryRebinder{}, ApplyInput{Manifest: manifest, NodeID: "main", LayoutFingerprint: "layout", LoadedFromFile: true}); err == nil || !strings.Contains(err.Error(), "duplicate action") {
		t.Fatalf("duplicate action error = %v", err)
	}

	manifest.Actions = manifest.Actions[:1]
	manifest.Actions[0].NewURI = filepath.Join(top, "outside", "payload")
	manifest.Actions[0].Rollback.ExpectedURI = manifest.Actions[0].NewURI
	manifest.Actions[0].ActionID = actionID(manifest.Actions[0])
	if err := RefreshManifestHash(&manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(context.Background(), &memoryRebinder{}, ApplyInput{Manifest: manifest, NodeID: "main", LayoutFingerprint: "layout", LoadedFromFile: true}); err == nil || !strings.Contains(err.Error(), "escapes root") {
		t.Fatalf("cross-root action error = %v", err)
	}
}

func TestApplyRejectsBlockingRootInventoryWithoutConflictRecords(t *testing.T) {
	root := t.TempDir()
	oldRoot := filepath.Join(root, "old")
	newRoot := filepath.Join(root, "new")
	if err := os.MkdirAll(oldRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldRoot, "untracked.bin"), []byte("unregistered custody"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := Plan(PlanInput{NodeID: "main", LayoutFingerprint: "layout", Roots: []RootMapping{{Name: "lane", OldRoot: oldRoot, NewRoot: newRoot}}})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.RootInventory[0].UntrackedFileCount != 1 || !manifestHasConflict(manifest, "untracked_regular_file") {
		t.Fatalf("untracked inventory = %#v", manifest)
	}
	manifest.Conflicts = nil
	now := time.Now().UTC()
	manifest.Review = Review{ReviewedBy: "integrator", ReviewedAt: &now}
	if err := RefreshManifestHash(&manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(context.Background(), &memoryRebinder{}, ApplyInput{Manifest: manifest, NodeID: "main", LayoutFingerprint: "layout", LoadedFromFile: true}); err == nil || !strings.Contains(err.Error(), "blocking physical/catalog") {
		t.Fatalf("tampered conflict-free inventory error = %v", err)
	}
}

func TestApplyRejectsPostPlanUntrackedDestinationBeforeCatalogMutation(t *testing.T) {
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
	if err := os.WriteFile(oldPath, []byte("reviewed custody"), 0o600); err != nil {
		t.Fatal(err)
	}
	detail := storagecatalog.EntryDetail{Entry: storagecatalog.Entry{StorageEntryID: "entry"}, PhysicalRefs: []storagecatalog.PhysicalRef{{StoragePhysicalRefID: "ref", StorageEntryID: "entry", RefKind: storagecatalog.PhysicalRefKindLaneFile, URI: oldPath, Status: storagecatalog.PhysicalRefStatusAvailable}}}
	manifest, err := Plan(PlanInput{NodeID: "main", LayoutFingerprint: "layout", Roots: []RootMapping{{Name: "lane", OldRoot: oldRoot, NewRoot: newRoot}}, Details: []storagecatalog.EntryDetail{detail}})
	if err != nil {
		t.Fatal(err)
	}
	moveReviewedRoot(t, oldRoot, newRoot)
	if err := os.WriteFile(filepath.Join(newRoot, "introduced-after-review"), []byte("untracked"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifest.Review = Review{ReviewedBy: "integrator", ReviewedAt: &now}
	catalog := &memoryRebinder{refURI: oldPath}
	if _, err := Apply(context.Background(), catalog, ApplyInput{Manifest: manifest, NodeID: "main", LayoutFingerprint: "layout", LoadedFromFile: true}); err == nil || !strings.Contains(err.Error(), "physical inventory changed") {
		t.Fatalf("post-plan inventory drift error = %v", err)
	}
	if catalog.refURI != oldPath {
		t.Fatalf("catalog mutated despite root inventory drift: %#v", catalog)
	}
}

func TestApplyNoActionRootRequiresCompleteMoveAndRepeatedSourceAbsence(t *testing.T) {
	top := t.TempDir()
	oldRoot := filepath.Join(top, "old")
	newRoot := filepath.Join(top, "new")
	if err := os.MkdirAll(oldRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest, err := Plan(PlanInput{NodeID: "main", LayoutFingerprint: "layout", Roots: []RootMapping{{Name: "empty", OldRoot: oldRoot, NewRoot: newRoot}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Actions) != 0 || manifest.RootInventory[0].RegularFileCount != 0 {
		t.Fatalf("empty-root manifest = %#v", manifest)
	}
	now := time.Now().UTC()
	manifest.Review = Review{ReviewedBy: "integrator", ReviewedAt: &now}
	if _, err := Apply(context.Background(), nil, ApplyInput{Manifest: manifest, NodeID: "main", LayoutFingerprint: "layout", LoadedFromFile: true}); err == nil || !strings.Contains(err.Error(), "source root") {
		t.Fatalf("partial empty-root move error = %v", err)
	}
	if err := os.Remove(oldRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(context.Background(), nil, ApplyInput{Manifest: manifest, NodeID: "main", LayoutFingerprint: "layout", LoadedFromFile: true}); err != nil {
		t.Fatalf("complete empty-root move: %v", err)
	}
	if _, err := PreflightApply(context.Background(), ApplyInput{Manifest: manifest, NodeID: "main", LayoutFingerprint: "layout", LoadedFromFile: true}); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(oldRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := RecheckApplyEvidence(context.Background(), ApplyInput{Manifest: manifest, NodeID: "main", LayoutFingerprint: "layout", LoadedFromFile: true}); err == nil || !strings.Contains(err.Error(), "source root") {
		t.Fatalf("recreated source root error = %v", err)
	}
}

func TestApplyRootInventoryDetectsEmptyDirectoryLoss(t *testing.T) {
	top := t.TempDir()
	oldRoot := filepath.Join(top, "old")
	newRoot := filepath.Join(top, "new")
	if err := os.MkdirAll(filepath.Join(oldRoot, "kept", "empty"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest, err := Plan(PlanInput{NodeID: "main", LayoutFingerprint: "layout", Roots: []RootMapping{{Name: "empty-tree", OldRoot: oldRoot, NewRoot: newRoot}}})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.RootInventory[0].DirectoryCount != 2 || manifest.RootInventory[0].StructureDigest == "" {
		t.Fatalf("reviewed directory inventory = %#v", manifest.RootInventory[0])
	}
	moveReviewedRoot(t, oldRoot, newRoot)
	if err := os.Remove(filepath.Join(newRoot, "kept", "empty")); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifest.Review = Review{ReviewedBy: "integrator", ReviewedAt: &now}
	if _, err := Apply(context.Background(), nil, ApplyInput{Manifest: manifest, NodeID: "main", LayoutFingerprint: "layout", LoadedFromFile: true}); err == nil || !strings.Contains(err.Error(), "physical inventory changed") {
		t.Fatalf("empty-directory loss error = %v", err)
	}
}

func TestApplyRootInventoryAuthenticatesAllowedInternalMarker(t *testing.T) {
	top := t.TempDir()
	oldRoot := filepath.Join(top, "old")
	newRoot := filepath.Join(top, "new")
	markerPath := filepath.Join(oldRoot, "node-a", "2026-08-28", "batch-a", ".loom-lane-custody.json")
	if err := os.MkdirAll(filepath.Dir(markerPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"marker":"reviewed"}`)
	if err := os.WriteFile(markerPath, original, 0o640); err != nil {
		t.Fatal(err)
	}
	markerInfo, err := os.Lstat(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := Plan(PlanInput{NodeID: "main", LayoutFingerprint: "layout", Roots: []RootMapping{{Name: "lane_accepted", OldRoot: oldRoot, NewRoot: newRoot}}})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.RootInventory[0].InternalMarkerCount != 1 || manifest.RootInventory[0].RegularFileCount != 0 || len(manifest.Conflicts) != 0 {
		t.Fatalf("reviewed marker inventory = %#v", manifest)
	}
	moveReviewedRoot(t, oldRoot, newRoot)
	destinationMarker := filepath.Join(newRoot, "node-a", "2026-08-28", "batch-a", ".loom-lane-custody.json")
	replacement := []byte(`{"marker":"replaced"}`)
	if len(replacement) != len(original) {
		t.Fatalf("test replacement size %d != %d", len(replacement), len(original))
	}
	if err := os.WriteFile(destinationMarker, replacement, markerInfo.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(destinationMarker, markerInfo.ModTime(), markerInfo.ModTime()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifest.Review = Review{ReviewedBy: "integrator", ReviewedAt: &now}
	if _, err := Apply(context.Background(), nil, ApplyInput{Manifest: manifest, NodeID: "main", LayoutFingerprint: "layout", LoadedFromFile: true}); err == nil || !strings.Contains(err.Error(), "physical inventory changed") {
		t.Fatalf("same-count marker substitution error = %v", err)
	}
}

func TestRollbackRejectsPhysicalRootDriftBeforeCatalogMutation(t *testing.T) {
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
	if err := os.WriteFile(oldPath, []byte("rollback custody"), 0o600); err != nil {
		t.Fatal(err)
	}
	detail := storagecatalog.EntryDetail{Entry: storagecatalog.Entry{StorageEntryID: "entry"}, PhysicalRefs: []storagecatalog.PhysicalRef{{StoragePhysicalRefID: "ref", StorageEntryID: "entry", RefKind: storagecatalog.PhysicalRefKindLaneFile, URI: oldPath, Status: storagecatalog.PhysicalRefStatusAvailable}}}
	manifest, err := Plan(PlanInput{NodeID: "main", LayoutFingerprint: "layout", Roots: []RootMapping{{Name: "lane", OldRoot: oldRoot, NewRoot: newRoot}}, Details: []storagecatalog.EntryDetail{detail}})
	if err != nil {
		t.Fatal(err)
	}
	moveReviewedRoot(t, oldRoot, newRoot)
	now := time.Now().UTC()
	manifest.Review = Review{ReviewedBy: "integrator", ReviewedAt: &now}
	catalog := &memoryRebinder{refURI: oldPath}
	if _, err := Apply(context.Background(), catalog, ApplyInput{Manifest: manifest, NodeID: "main", LayoutFingerprint: "layout", LoadedFromFile: true}); err != nil {
		t.Fatal(err)
	}
	rollback, err := RollbackManifest(manifest, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	moveReviewedRoot(t, newRoot, oldRoot)
	if err := os.WriteFile(filepath.Join(oldRoot, "introduced-before-rollback"), []byte("drift"), 0o600); err != nil {
		t.Fatal(err)
	}
	rollback.Review = Review{ReviewedBy: "integrator", ReviewedAt: &now}
	if _, err := Apply(context.Background(), catalog, ApplyInput{Manifest: rollback, NodeID: "main", LayoutFingerprint: "layout", LoadedFromFile: true}); err == nil || !strings.Contains(err.Error(), "physical inventory changed") {
		t.Fatalf("rollback inventory drift error = %v", err)
	}
	if catalog.refURI != newPath {
		t.Fatalf("rollback mutated catalog despite root drift: %#v", catalog)
	}
}

func TestMultiplePhysicalRefsAndOneEntryPathRebindTogether(t *testing.T) {
	top := t.TempDir()
	oldRoot := filepath.Join(top, "old")
	newRoot := filepath.Join(top, "new")
	if err := os.MkdirAll(oldRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(oldRoot, "payload-a")
	newPath := filepath.Join(newRoot, "payload-a")
	secondOldPath := filepath.Join(oldRoot, "payload-b")
	secondNewPath := filepath.Join(newRoot, "payload-b")
	if err := os.WriteFile(oldPath, []byte("multiple refs a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondOldPath, []byte("multiple refs b"), 0o600); err != nil {
		t.Fatal(err)
	}
	detail := storagecatalog.EntryDetail{Entry: storagecatalog.Entry{StorageEntryID: "entry", OriginalSourcePath: oldPath}, PhysicalRefs: []storagecatalog.PhysicalRef{
		{StoragePhysicalRefID: "ref-a", StorageEntryID: "entry", RefKind: storagecatalog.PhysicalRefKindLaneFile, URI: oldPath, Status: storagecatalog.PhysicalRefStatusAvailable},
		{StoragePhysicalRefID: "ref-b", StorageEntryID: "entry", RefKind: storagecatalog.PhysicalRefKindLaneFile, URI: secondOldPath, Status: storagecatalog.PhysicalRefStatusAvailable},
	}}
	manifest, err := Plan(PlanInput{NodeID: "main", LayoutFingerprint: "layout", Roots: []RootMapping{{Name: "lane", OldRoot: oldRoot, NewRoot: newRoot}}, Details: []storagecatalog.EntryDetail{detail}})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Actions) != 3 {
		t.Fatalf("actions = %#v", manifest.Actions)
	}
	if err := ValidateManifestSemantics(manifest); err != nil {
		t.Fatalf("planned manifest semantics: %v", err)
	}
	moveReviewedRoot(t, oldRoot, newRoot)
	now := time.Now().UTC()
	manifest.Review = Review{ReviewedBy: "integrator", ReviewedAt: &now}
	catalog := &multiMemoryRebinder{refs: map[string]string{"ref-a": oldPath, "ref-b": secondOldPath}, original: oldPath}
	result, err := Apply(context.Background(), catalog, ApplyInput{Manifest: manifest, NodeID: "main", LayoutFingerprint: "layout", LoadedFromFile: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Catalog.PhysicalRefsUpdated != 2 || result.Catalog.EntriesUpdated != 1 || catalog.refs["ref-a"] != newPath || catalog.refs["ref-b"] != secondNewPath || catalog.original != newPath {
		t.Fatalf("result=%#v catalog=%#v", result, catalog)
	}
}

func manifestHasConflict(manifest Manifest, code string) bool {
	for _, conflict := range manifest.Conflicts {
		if conflict.Code == code {
			return true
		}
	}
	return false
}

func moveReviewedRoot(t *testing.T, sourceRoot, destinationRoot string) {
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

type memoryRebinder struct{ refURI, original, view string }

func (m *memoryRebinder) RebindPaths(_ context.Context, input storagecatalog.RebindPathsInput) (storagecatalog.RebindPathsResult, error) {
	result := storagecatalog.RebindPathsResult{}
	for _, ref := range input.PhysicalRefs {
		if m.refURI == ref.NewURI {
			result.AlreadyApplied++
			continue
		}
		if m.refURI != ref.ExpectedURI {
			return result, os.ErrInvalid
		}
		m.refURI = ref.NewURI
		result.PhysicalRefsUpdated++
	}
	for _, entry := range input.Entries {
		if m.original == entry.NewOriginalSourcePath && m.view == entry.NewCurrentViewPath {
			result.AlreadyApplied++
			continue
		}
		if m.original != entry.ExpectedOriginalSourcePath || m.view != entry.ExpectedCurrentViewPath {
			return result, os.ErrInvalid
		}
		m.original, m.view = entry.NewOriginalSourcePath, entry.NewCurrentViewPath
		result.EntriesUpdated++
	}
	return result, nil
}

type multiMemoryRebinder struct {
	refs           map[string]string
	original, view string
}

func (m *multiMemoryRebinder) RebindPaths(_ context.Context, input storagecatalog.RebindPathsInput) (storagecatalog.RebindPathsResult, error) {
	result := storagecatalog.RebindPathsResult{}
	for _, ref := range input.PhysicalRefs {
		current := m.refs[ref.StoragePhysicalRefID]
		if current == ref.NewURI {
			result.AlreadyApplied++
			continue
		}
		if current != ref.ExpectedURI {
			return result, os.ErrInvalid
		}
		m.refs[ref.StoragePhysicalRefID] = ref.NewURI
		result.PhysicalRefsUpdated++
	}
	for _, entry := range input.Entries {
		if m.original == entry.NewOriginalSourcePath && m.view == entry.NewCurrentViewPath {
			result.AlreadyApplied++
			continue
		}
		if m.original != entry.ExpectedOriginalSourcePath || m.view != entry.ExpectedCurrentViewPath {
			return result, os.ErrInvalid
		}
		m.original, m.view = entry.NewOriginalSourcePath, entry.NewCurrentViewPath
		result.EntriesUpdated++
	}
	return result, nil
}
