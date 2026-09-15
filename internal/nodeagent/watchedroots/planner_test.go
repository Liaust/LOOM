package watchedroots

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"loom.local/loom/internal/filesystemconnector"
)

func TestSourceMetadataPlannerPreservesContentAndObservationOrder(t *testing.T) {
	root := ValidatedRoot{Config: RootConfig{RootKey: "notes", SyncPolicy: SyncPolicy{Mode: SyncModeSelectedFiles}}}
	a := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	b := a.Add(-time.Hour)
	s := PathState{Status: PathStatusIncluded, Kind: PathKindFile, RelativePath: "a.md", ContentHashURI: "hash",
		LastQueuedHashURI: "hash", LastSyncedHashURI: "hash", SyncStatus: "accepted", ModifiedAt: &a,
		LocalObjectID: "object", LocalVersionID: "version", MainObjectID: "main", MainVersionID: "main-version"}
	check := func(kind, status string) {
		t.Helper()
		p, ok := planSyncOutputAction(root, s)
		if !ok || p.ActionKind != kind || p.Status != status {
			t.Fatalf("unexpected action: %+v", p)
		}
		if kind == OutputActionSyncMetadata && (p.LocalVersionID != s.LocalVersionID || p.MainVersionID != s.MainVersionID) {
			t.Fatal("metadata action changed content identity")
		}
	}
	check(OutputActionSyncMetadata, "") // Legacy state gets one safe observation.
	s.LastSyncedModifiedAt = &a
	check(OutputActionSyncObject, OutputStatusAlreadyCurrent)
	s.ModifiedAt = &b
	check(OutputActionSyncMetadata, "")
	s.LastQueuedMetadataMtime = &b
	s.ModifiedAt = &a
	check(OutputActionSyncMetadata, "") // B may still arrive; A must supersede it.
	s.LastQueuedMetadataMtime = &a
	check(OutputActionSyncObject, OutputStatusAlreadyCurrent)
	s.SyncStatus = OutputStatusQueued
	s.LastQueuedHashURI = "other"
	check(OutputActionSyncObject, "") // Content A-B-A must not disappear either.
	s.LastQueuedHashURI = "hash"
	s.ModifiedAt = &b
	check(OutputActionSyncObject, OutputStatusAlreadyCurrent) // Await blob binding.
	s.SyncStatus = "accepted"
	s.HashStatus = HashStatusDeferred
	check(OutputActionSkipped, OutputStatusSkipped)
	s.HashStatus = HashStatusUnchanged
	s.Status = PathStatusExcluded
	if _, ok := planSyncOutputAction(root, s); ok {
		t.Fatal("excluded source queued metadata")
	}
	next := PathState{}
	preserveOutputState(s, &next)
	if next.LastSyncedModifiedAt != s.LastSyncedModifiedAt || next.LastQueuedMetadataMtime != s.LastQueuedMetadataMtime {
		t.Fatal("scan lost observation acknowledgements")
	}
}

func TestPlanOutputsQueuesSelectedFilesAndSkipsCurrentHashes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Project.md"), []byte("# Project\n"), 0o600); err != nil {
		t.Fatalf("write project: %v", err)
	}
	root := testScanRoot(t, dir, RootConfig{
		RootKey:     "notes",
		SafeRootKey: "slice09",
		Include:     []string{"**/*.md"},
		Scan: ScanConfig{
			StabilityWindow: "0s",
		},
		SyncPolicy: SyncPolicy{
			Mode:       SyncModeSelectedFiles,
			ProjectRef: "project_test",
		},
		IndexPolicy: IndexPolicy{
			Mode: IndexModeMarkdownText,
		},
		DeletePolicy: DeletePolicy{
			Mode: DeleteModeTombstone,
		},
	})
	store := NewStore(t.TempDir())
	if _, err := Reconcile(context.Background(), store, ScanRequest{Mode: ScanModeFull, Root: root}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	plan, err := PlanOutputs(store, root, "node-agent.watched_root.notes")
	if err != nil {
		t.Fatalf("PlanOutputs failed: %v", err)
	}
	if plan.Counts.SyncObjects != 1 || len(plan.Actions) != 1 {
		t.Fatalf("expected one sync object action, got %#v", plan)
	}
	action := plan.Actions[0]
	if action.ActionKind != OutputActionSyncObject ||
		action.LocalObjectID == "" ||
		action.LocalVersionID == "" ||
		action.ProjectRef != "project_test" ||
		action.IndexPolicy != IndexModeMarkdownText ||
		action.SourcePath != "watched-root://notes/Project.md" {
		t.Fatalf("unexpected sync action %#v", action)
	}

	state, err := store.LoadPathState("notes", "Project.md")
	if err != nil {
		t.Fatalf("LoadPathState failed: %v", err)
	}
	state.LocalObjectID = action.LocalObjectID
	state.LocalVersionID = action.LocalVersionID
	state.LastQueuedHashURI = state.ContentHashURI
	state.SyncStatus = OutputStatusQueued
	if err := store.SavePathState(state); err != nil {
		t.Fatalf("SavePathState failed: %v", err)
	}
	plan, err = PlanOutputs(store, root, "node-agent.watched_root.notes")
	if err != nil {
		t.Fatalf("PlanOutputs current failed: %v", err)
	}
	if plan.Counts.SyncObjects != 0 || plan.Counts.AlreadyCurrent != 1 || len(plan.Actions) != 0 {
		t.Fatalf("expected already-current plan, got %#v", plan)
	}
}

func TestPlanOutputsBacksUpNotesAttachmentsWithoutSyncingThem(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	files := map[string]string{
		"Project.md":        "# Project\n",
		"attachments/a.pdf": "%PDF-1.4\n",
	}
	for rel, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	root := testScanRoot(t, dir, RootConfig{
		RootKey:     "notes",
		SafeRootKey: "slice09",
		Include:     []string{"**/*"},
		Scan:        ScanConfig{StabilityWindow: "0s"},
		BackupPolicy: BackupPolicy{
			Mode:          BackupModeIncrementalRaw,
			MaxFileBytes:  1024 * 1024,
			MaxBatchBytes: 1024 * 1024,
		},
		SyncPolicy: SyncPolicy{
			Mode:       SyncModeSelectedFiles,
			ProjectRef: "project_test",
		},
		IndexPolicy: IndexPolicy{Mode: IndexModeMarkdownText},
	})
	store := NewStore(t.TempDir())
	if _, err := Reconcile(context.Background(), store, ScanRequest{Mode: ScanModeFull, Root: root}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	plan, err := PlanOutputs(store, root, "node-agent.watched_root.notes")
	if err != nil {
		t.Fatalf("PlanOutputs failed: %v", err)
	}
	if plan.Counts.SyncObjects != 1 || plan.Counts.BackupFiles != 2 || plan.Counts.BackupMetadata != 1 || len(plan.Actions) != 4 {
		t.Fatalf("expected one markdown sync, two file backups, and one directory metadata backup, got %#v", plan)
	}
	pdfBackups := 0
	for _, action := range plan.Actions {
		if action.RelativePath == "attachments/a.pdf" {
			if action.ActionKind != OutputActionBackupFile || action.BackupMode != BackupModeIncrementalRaw {
				t.Fatalf("pdf should only produce an incremental backup action, got %#v", action)
			}
			pdfBackups++
		}
	}
	if pdfBackups != 1 {
		t.Fatalf("expected one pdf backup action, got %d in %#v", pdfBackups, plan.Actions)
	}
	pdfState, err := store.LoadPathState("notes", "attachments/a.pdf")
	if err != nil {
		t.Fatalf("LoadPathState pdf failed: %v", err)
	}
	if pdfState.Classification.Policies.Sync != SyncModeNone || pdfState.Classification.Policies.Index != IndexModeMetadataOnly || pdfState.Classification.Policies.Backup != BackupModeIncrementalRaw {
		t.Fatalf("pdf state policies should retain backup and metadata-only index but disable sync: %#v", pdfState.Classification.Policies)
	}
}

func TestPlanOutputsSyncsMetadataOnlyNotesAttachmentsWithoutBackup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	files := map[string]string{
		"Project.md":        "# Project\n",
		"attachments/a.pdf": "%PDF-1.4\n",
	}
	for rel, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	root := testScanRoot(t, dir, RootConfig{
		RootKey:     "notes",
		SafeRootKey: "slice09",
		Include:     []string{"**/*"},
		Scan:        ScanConfig{StabilityWindow: "0s"},
		SyncPolicy: SyncPolicy{
			Mode:       SyncModeSelectedFiles,
			ProjectRef: "project_test",
		},
		IndexPolicy: IndexPolicy{Mode: IndexModeMarkdownText},
	})
	store := NewStore(t.TempDir())
	if _, err := Reconcile(context.Background(), store, ScanRequest{Mode: ScanModeFull, Root: root}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	plan, err := PlanOutputs(store, root, "node-agent.watched_root.notes")
	if err != nil {
		t.Fatalf("PlanOutputs failed: %v", err)
	}
	if plan.Counts.SyncObjects != 2 || len(plan.Actions) != 2 {
		t.Fatalf("expected markdown text sync and pdf metadata-only sync, got %#v", plan)
	}
	pdfSyncs := 0
	for _, action := range plan.Actions {
		if action.RelativePath == "attachments/a.pdf" {
			if action.ActionKind != OutputActionSyncObject || action.SyncPolicy != SyncModeSelectedFiles || action.IndexPolicy != IndexModeMetadataOnly {
				t.Fatalf("pdf should produce metadata-only sync action, got %#v", action)
			}
			pdfSyncs++
		}
	}
	if pdfSyncs != 1 {
		t.Fatalf("expected one pdf metadata-only sync action, got %d in %#v", pdfSyncs, plan.Actions)
	}
}

func TestPlanOutputsSkipsSyncObjectsAboveInlineLimit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	largePath := filepath.Join(dir, "large.txt")
	if err := os.WriteFile(largePath, []byte("large"), 0o600); err != nil {
		t.Fatalf("write large: %v", err)
	}
	rootConfig := RootConfig{
		RootKey:     "notes",
		SafeRootKey: "slice09",
		Include:     []string{"**/*"},
		Scan:        ScanConfig{StabilityWindow: "0s"},
		SyncPolicy: SyncPolicy{
			Mode:         SyncModeSelectedFiles,
			ProjectRef:   "project_test",
			MaxFileBytes: 2 * 1024 * 1024,
		},
		IndexPolicy: IndexPolicy{Mode: IndexModeMarkdownText},
	}
	root, err := ValidateRootConfig(rootConfig, filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{{
		RootKey:       "slice09",
		AbsolutePath:  dir,
		AllowList:     true,
		AllowMetadata: true,
		AllowIngest:   true,
		MaxFileBytes:  2 * 1024 * 1024,
	}}})
	if err != nil {
		t.Fatalf("ValidateRootConfig failed: %v", err)
	}
	store := NewStore(t.TempDir())
	if _, err := Reconcile(context.Background(), store, ScanRequest{Mode: ScanModeFull, Root: root}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	state, err := store.LoadPathState("notes", "large.txt")
	if err != nil {
		t.Fatalf("LoadPathState failed: %v", err)
	}
	state.SizeBytes = effectiveSyncMaxFileBytes(root.Config.SyncPolicy.MaxFileBytes) + 1
	if err := store.SavePathState(state); err != nil {
		t.Fatalf("SavePathState failed: %v", err)
	}

	plan, err := PlanOutputs(store, root, "node-agent.watched_root.notes")
	if err != nil {
		t.Fatalf("PlanOutputs failed: %v", err)
	}
	if plan.Counts.SyncObjects != 0 || plan.Counts.Skipped != 1 || len(plan.Actions) != 1 {
		t.Fatalf("expected oversize sync skip, got %#v", plan)
	}
	action := plan.Actions[0]
	if action.ActionKind != OutputActionSkipped ||
		action.ReasonCode != ReasonSkippedTooLarge ||
		action.Reason != "path exceeds watched-root inline sync limit" {
		t.Fatalf("unexpected skip action %#v", action)
	}
}

func TestPlanOutputsQueuesBackupFilesAndDeletionMarkers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	projectPath := filepath.Join(dir, "Project.md")
	if err := os.WriteFile(projectPath, []byte("# Project\n"), 0o600); err != nil {
		t.Fatalf("write project: %v", err)
	}
	root := testScanRoot(t, dir, RootConfig{
		RootKey:     "notes",
		SafeRootKey: "slice09",
		Include:     []string{"**/*.md"},
		Scan:        ScanConfig{StabilityWindow: "0s"},
		BackupPolicy: BackupPolicy{
			Mode:          BackupModeIncrementalRaw,
			MaxFileBytes:  1024 * 1024,
			MaxBatchBytes: 1024 * 1024,
		},
	})
	store := NewStore(t.TempDir())
	if _, err := Reconcile(context.Background(), store, ScanRequest{Mode: ScanModeFull, Root: root}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	plan, err := PlanOutputs(store, root, "node-agent.watched_root.notes")
	if err != nil {
		t.Fatalf("PlanOutputs failed: %v", err)
	}
	if plan.Counts.BackupFiles != 1 || plan.Counts.SyncObjects != 0 || len(plan.Actions) != 1 {
		t.Fatalf("expected one backup file action, got %#v", plan)
	}
	action := plan.Actions[0]
	if action.ActionKind != OutputActionBackupFile ||
		action.BackupMode != BackupModeIncrementalRaw ||
		action.BackupItemKind != "file" ||
		action.ContentHashURI == "" ||
		action.BackupMaxFileBytes <= 0 {
		t.Fatalf("unexpected backup action %#v", action)
	}

	state, err := store.LoadPathState("notes", "Project.md")
	if err != nil {
		t.Fatalf("LoadPathState failed: %v", err)
	}
	state.LastQueuedBackupHashURI = state.ContentHashURI
	state.BackupStatus = OutputStatusQueued
	if err := store.SavePathState(state); err != nil {
		t.Fatalf("SavePathState failed: %v", err)
	}
	plan, err = PlanOutputs(store, root, "node-agent.watched_root.notes")
	if err != nil {
		t.Fatalf("PlanOutputs already current failed: %v", err)
	}
	if plan.Counts.BackupFiles != 0 || plan.Counts.AlreadyCurrent != 1 || len(plan.Actions) != 0 {
		t.Fatalf("expected already-current backup plan, got %#v", plan)
	}

	if err := os.Remove(projectPath); err != nil {
		t.Fatalf("remove project: %v", err)
	}
	if _, err := Reconcile(context.Background(), store, ScanRequest{Mode: ScanModeFull, Root: root}); err != nil {
		t.Fatalf("Reconcile delete failed: %v", err)
	}
	plan, err = PlanOutputs(store, root, "node-agent.watched_root.notes")
	if err != nil {
		t.Fatalf("PlanOutputs deletion failed: %v", err)
	}
	if plan.Counts.BackupDeletionMarkers != 1 || len(plan.Actions) != 1 {
		t.Fatalf("expected one backup deletion marker, got %#v", plan)
	}
	if plan.Actions[0].ActionKind != OutputActionBackupDeletionMarker ||
		plan.Actions[0].PreviousHashURI == "" ||
		plan.Actions[0].DeletedAt == nil {
		t.Fatalf("unexpected backup deletion action %#v", plan.Actions[0])
	}
}

func TestPlanOutputsQueuesMetadataOnlyBackup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Project.md"), []byte("# Project\n"), 0o600); err != nil {
		t.Fatalf("write project: %v", err)
	}
	root := testScanRoot(t, dir, RootConfig{
		RootKey:     "notes",
		SafeRootKey: "slice09",
		Include:     []string{"**/*.md"},
		Scan:        ScanConfig{StabilityWindow: "0s"},
		BackupPolicy: BackupPolicy{
			Mode:          BackupModeMetadataOnly,
			MaxFileBytes:  1024 * 1024,
			MaxBatchBytes: 1024 * 1024,
		},
	})
	store := NewStore(t.TempDir())
	if _, err := Reconcile(context.Background(), store, ScanRequest{Mode: ScanModeFull, Root: root}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	plan, err := PlanOutputs(store, root, "node-agent.watched_root.notes")
	if err != nil {
		t.Fatalf("PlanOutputs failed: %v", err)
	}
	if plan.Counts.BackupMetadata != 1 || len(plan.Actions) != 1 {
		t.Fatalf("expected metadata backup action, got %#v", plan)
	}
	if plan.Actions[0].ActionKind != OutputActionBackupMetadata || plan.Actions[0].BackupItemKind != "metadata" {
		t.Fatalf("unexpected metadata backup action %#v", plan.Actions[0])
	}
}

func TestPlanOutputsQueuesDirectoryMetadataBackup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "Empty"), 0o755); err != nil {
		t.Fatalf("mkdir Empty: %v", err)
	}
	root := testScanRoot(t, dir, RootConfig{
		RootKey:     "documents",
		SafeRootKey: "slice09",
		Include:     []string{"**/*"},
		Scan:        ScanConfig{StabilityWindow: "0s"},
		BackupPolicy: BackupPolicy{
			Mode:          BackupModeIncrementalRaw,
			MaxFileBytes:  1024 * 1024,
			MaxBatchBytes: 1024 * 1024,
		},
	})
	store := NewStore(t.TempDir())
	if _, err := Reconcile(context.Background(), store, ScanRequest{Mode: ScanModeFull, Root: root}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	plan, err := PlanOutputs(store, root, "node-agent.watched_root.documents")
	if err != nil {
		t.Fatalf("PlanOutputs failed: %v", err)
	}
	if plan.Counts.BackupMetadata != 1 || len(plan.Actions) != 1 {
		t.Fatalf("expected one directory metadata backup action, got %#v", plan)
	}
	action := plan.Actions[0]
	if action.ActionKind != OutputActionBackupMetadata || action.BackupItemKind != "directory" || action.ContentHashURI == "" || action.SizeBytes <= 0 {
		t.Fatalf("unexpected directory backup action %#v", action)
	}

	state, err := store.LoadPathState("documents", "Empty")
	if err != nil {
		t.Fatalf("LoadPathState Empty failed: %v", err)
	}
	state.LastBackedUpHashURI = action.ContentHashURI
	if err := store.SavePathState(state); err != nil {
		t.Fatalf("SavePathState failed: %v", err)
	}
	plan, err = PlanOutputs(store, root, "node-agent.watched_root.documents")
	if err != nil {
		t.Fatalf("PlanOutputs already current failed: %v", err)
	}
	if plan.Counts.BackupMetadata != 0 || plan.Counts.AlreadyCurrent != 1 || len(plan.Actions) != 0 {
		t.Fatalf("expected already-current directory backup plan, got %#v", plan)
	}
}

func TestPlanOutputsSkipsStoredStatesExcludedByCurrentConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dependencyPath := filepath.Join(dir, "app", "node_modules", "pkg", "index.js")
	if err := os.MkdirAll(filepath.Dir(dependencyPath), 0o700); err != nil {
		t.Fatalf("mkdir dependency: %v", err)
	}
	if err := os.WriteFile(dependencyPath, []byte("module.exports = {}\n"), 0o600); err != nil {
		t.Fatalf("write dependency: %v", err)
	}
	root := testScanRoot(t, dir, RootConfig{
		RootKey:     "documents",
		SafeRootKey: "slice09",
		Include:     []string{"**/*"},
		Exclude:     []string{"__never__"},
		Scan:        ScanConfig{StabilityWindow: "0s"},
		BackupPolicy: BackupPolicy{
			Mode:          BackupModeIncrementalRaw,
			MaxFileBytes:  1024 * 1024,
			MaxBatchBytes: 1024 * 1024,
		},
	})
	store := NewStore(t.TempDir())
	if _, err := Reconcile(context.Background(), store, ScanRequest{Mode: ScanModeFull, Root: root}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	root.Config.Exclude = []string{"node_modules", "node_modules/**", "**/node_modules", "**/node_modules/**"}
	plan, err := PlanOutputs(store, root, "node-agent.watched_root.documents")
	if err != nil {
		t.Fatalf("PlanOutputs failed: %v", err)
	}
	for _, action := range plan.Actions {
		if action.RelativePath == "app/node_modules/pkg/index.js" {
			t.Fatalf("excluded stale dependency should not produce output action: %#v", action)
		}
	}
}
