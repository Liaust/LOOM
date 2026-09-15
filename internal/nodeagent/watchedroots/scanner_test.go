package watchedroots

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/filesystemconnector"
	"loom.local/loom/internal/filesystemmeta"
)

func TestReconcileFullTracksCreateModifyDelete(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Project.md"), []byte("# Project\n"), 0o600); err != nil {
		t.Fatalf("write project: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skip.tmp"), []byte("tmp"), 0o600); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	root := testScanRoot(t, dir, RootConfig{
		RootKey:     "notes",
		SafeRootKey: "slice09",
		Include:     []string{"**/*.md"},
		Exclude:     []string{"**/*.tmp"},
		Scan: ScanConfig{
			StabilityWindow: "0s",
		},
	})
	store := NewStore(t.TempDir())
	result, err := Reconcile(context.Background(), store, ScanRequest{
		Mode: ScanModeFull,
		Root: root,
	})
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if result.Status != RunStatusHealthy || result.Counts.Included != 1 || result.Counts.Excluded == 0 {
		t.Fatalf("unexpected first scan result %#v", result)
	}
	project, err := store.LoadPathState("notes", "Project.md")
	if err != nil {
		t.Fatalf("LoadPathState Project.md failed: %v", err)
	}
	if project.Status != PathStatusIncluded || project.ContentHashURI == "" {
		t.Fatalf("unexpected project state %#v", project)
	}
	skip, err := store.LoadPathState("notes", "skip.tmp")
	if err != nil {
		t.Fatalf("LoadPathState skip.tmp failed: %v", err)
	}
	if skip.Status != PathStatusExcluded {
		t.Fatalf("expected excluded tmp, got %#v", skip)
	}

	if err := os.WriteFile(filepath.Join(dir, "Project.md"), []byte("# Project changed\n"), 0o600); err != nil {
		t.Fatalf("modify project: %v", err)
	}
	result, err = Reconcile(context.Background(), store, ScanRequest{
		Mode: ScanModeFull,
		Root: root,
	})
	if err != nil {
		t.Fatalf("Reconcile modify failed: %v", err)
	}
	if result.Counts.Changed == 0 {
		t.Fatalf("expected changed count after modify, got %#v", result.Counts)
	}
	changed, err := store.LoadPathState("notes", "Project.md")
	if err != nil {
		t.Fatalf("LoadPathState changed Project.md failed: %v", err)
	}
	if changed.ContentHashURI == project.ContentHashURI {
		t.Fatalf("expected content hash change, got %s", changed.ContentHashURI)
	}

	if err := os.Remove(filepath.Join(dir, "Project.md")); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	result, err = Reconcile(context.Background(), store, ScanRequest{
		Mode: ScanModeFull,
		Root: root,
	})
	if err != nil {
		t.Fatalf("Reconcile delete failed: %v", err)
	}
	if result.Counts.Deleted != 1 {
		t.Fatalf("expected one deleted path, got %#v", result.Counts)
	}
	deleted, err := store.LoadPathState("notes", "Project.md")
	if err != nil {
		t.Fatalf("LoadPathState deleted Project.md failed: %v", err)
	}
	if deleted.Status != PathStatusDeleted || deleted.DeletedAt == nil {
		t.Fatalf("expected deleted state, got %#v", deleted)
	}
}

func TestReconcileManagedPolicyAndBackupPlanUseSameFiles(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		".loomignore":               "custom/**\n!node_modules/kept.js\n",
		".git/config":               "git",
		".env":                      "env",
		".secrets/token":            "secret",
		".data/app.db":              "data",
		".hidden/state":             "hidden",
		".loom/project.yaml":        "project",
		".loom/state/runtime.db":    "runtime",
		"node_modules/pkg/index.js": "dependency",
		"node_modules/kept.js":      "kept",
		".venv/bin/python":          "python",
		"custom/drop.bin":           "custom",
		"ordinary.txt":              "ordinary",
	}
	for relativePath, content := range files {
		pathValue := filepath.Join(dir, filepath.FromSlash(relativePath))
		if err := os.MkdirAll(filepath.Dir(pathValue), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pathValue, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root := testScanRoot(t, dir, RootConfig{
		RootKey:      "documents",
		SafeRootKey:  "slice09",
		Include:      []string{"**/*"},
		IgnorePolicy: IgnorePolicy{Profile: "managed", DiscoverUserRules: true},
		Scan:         ScanConfig{StabilityWindow: "0s", HiddenPolicy: HiddenPolicyPolicyControlled},
		BackupPolicy: BackupPolicy{Mode: BackupModeIncrementalRaw, MaxFileBytes: 1024 * 1024, MaxBatchBytes: 1024 * 1024},
	})
	store := NewStore(t.TempDir())
	result, err := Reconcile(context.Background(), store, ScanRequest{Mode: ScanModeFull, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	for _, relativePath := range []string{".git/config", ".env", ".secrets/token", ".data/app.db", ".hidden/state", ".loom/project.yaml", ".loomignore", "node_modules/kept.js", "ordinary.txt"} {
		state, err := store.LoadPathState("documents", relativePath)
		if err != nil || state.Status != PathStatusIncluded {
			t.Fatalf("expected %s included: state=%#v err=%v", relativePath, state, err)
		}
	}
	for _, relativePath := range []string{".loom/state/runtime.db", "node_modules/pkg/index.js", ".venv/bin/python", "custom/drop.bin"} {
		state, err := store.LoadPathState("documents", relativePath)
		if err == nil && state.Status != PathStatusExcluded {
			t.Fatalf("expected %s excluded: %#v", relativePath, state)
		}
	}
	plan, err := PlanOutputs(store, root, "node-agent.watched_root.documents")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Counts.BackupFiles != 9 {
		t.Fatalf("backup files=%d, want 9; plan=%#v scan=%#v", plan.Counts.BackupFiles, plan, result.Counts)
	}
	inspection, err := filepolicy.Inspect(dir, filepolicy.ProfileManaged, filepolicy.ResolverOptions{DiscoverUserRules: true}, filepolicy.ScanOptions{})
	if err != nil || inspection.Included.Count != plan.Counts.BackupFiles {
		t.Fatalf("inspection/backup mismatch: inspection=%#v plan=%#v err=%v", inspection, plan.Counts, err)
	}
	if result.Counts.IgnoredBySource["reconstructible"] == 0 || result.Counts.IgnoredBySource["mandatory_safety"] == 0 || result.Counts.IgnoredBySource["user"] == 0 || result.PolicyFingerprint == "" {
		t.Fatalf("missing policy evidence: %#v", result)
	}
}

func TestIgnoredDirtyHintDoesNotTriggerAutomaticScan(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "node_modules"), 0o700); err != nil {
		t.Fatal(err)
	}
	root := testScanRoot(t, dir, RootConfig{RootKey: "documents", SafeRootKey: "slice09", IgnorePolicy: IgnorePolicy{Profile: "managed", DiscoverUserRules: true}})
	store := NewStore(t.TempDir())
	previous := time.Now().UTC()
	if err := store.SaveCheckpoint("documents", RootCheckpoint{SchemaVersion: CheckpointSchemaVersion, RootKey: "documents", LastFullRescanAt: &previous}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddDirtyHint("documents", DirtyHint{RelativePath: "node_modules/cache.js", HintKind: DirtyHintModified}); err != nil {
		t.Fatal(err)
	}
	result, err := Reconcile(context.Background(), store, ScanRequest{Mode: ScanModeAuto, Root: root, StartedAt: previous.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != RunStatusSkipped || result.Counts.IgnoredDirtyHints != 1 {
		t.Fatalf("ignored churn triggered scan: %#v", result)
	}
}

func TestReconcileFullMarksDescendantsExcludedWhenDirectoryBecomesExcluded(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dependencyRel := "app/node_modules/pkg/index.js"
	dependencyPath := filepath.Join(dir, filepath.FromSlash(dependencyRel))
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
	})
	store := NewStore(t.TempDir())
	if _, err := Reconcile(context.Background(), store, ScanRequest{Mode: ScanModeFull, Root: root}); err != nil {
		t.Fatalf("initial Reconcile failed: %v", err)
	}
	initial, err := store.LoadPathState("documents", dependencyRel)
	if err != nil {
		t.Fatalf("LoadPathState initial dependency failed: %v", err)
	}
	if initial.Status != PathStatusIncluded {
		t.Fatalf("expected initial dependency included, got %#v", initial)
	}

	root.Config.Exclude = []string{"node_modules", "node_modules/**", "**/node_modules", "**/node_modules/**"}
	result, err := Reconcile(context.Background(), store, ScanRequest{Mode: ScanModeFull, Root: root})
	if err != nil {
		t.Fatalf("exclude Reconcile failed: %v", err)
	}
	if result.Counts.Deleted != 0 {
		t.Fatalf("policy exclusion should not record deletes, got %#v", result.Counts)
	}
	excluded, err := store.LoadPathState("documents", dependencyRel)
	if err != nil {
		t.Fatalf("LoadPathState excluded dependency failed: %v", err)
	}
	if excluded.Status != PathStatusExcluded || excluded.Classification.ReasonCode != ReasonExcludedByPattern {
		t.Fatalf("expected dependency to become excluded, got %#v", excluded)
	}
}

func TestReconcileRootUnavailableDoesNotDeleteKnownState(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	rootDir := filepath.Join(parent, "root")
	if err := os.MkdirAll(rootDir, 0o700); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, "Project.md"), []byte("# Project\n"), 0o600); err != nil {
		t.Fatalf("write project: %v", err)
	}
	config := RootConfig{
		RootKey:          "notes",
		SafeRootKey:      "slice09",
		RootRelativePath: "root",
		Include:          []string{"**/*.md"},
		Scan: ScanConfig{
			StabilityWindow: "0s",
		},
	}
	root, err := ValidateRootConfig(config, filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{
		filesystemconnector.DefaultSafeRoot("slice09", parent),
	}})
	if err != nil {
		t.Fatalf("ValidateRootConfig failed: %v", err)
	}
	store := NewStore(t.TempDir())
	if _, err := Reconcile(context.Background(), store, ScanRequest{Mode: ScanModeFull, Root: root}); err != nil {
		t.Fatalf("initial Reconcile failed: %v", err)
	}
	if err := os.RemoveAll(rootDir); err != nil {
		t.Fatalf("remove root: %v", err)
	}
	root, err = ValidateRootConfigForRun(config, filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{
		filesystemconnector.DefaultSafeRoot("slice09", parent),
	}})
	if err != nil {
		t.Fatalf("ValidateRootConfigForRun failed: %v", err)
	}
	result, err := Reconcile(context.Background(), store, ScanRequest{Mode: ScanModeFull, Root: root})
	if err != nil {
		t.Fatalf("unavailable Reconcile failed: %v", err)
	}
	if result.Status != RunStatusBlocked || len(result.Findings) == 0 {
		t.Fatalf("expected blocked result with finding, got %#v", result)
	}
	state, err := store.LoadPathState("notes", "Project.md")
	if err != nil {
		t.Fatalf("LoadPathState after unavailable failed: %v", err)
	}
	if state.Status == PathStatusDeleted {
		t.Fatalf("root unavailable should not mark known state deleted: %#v", state)
	}
	if err := os.MkdirAll(rootDir, 0o700); err != nil {
		t.Fatalf("restore root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, "Project.md"), []byte("# Project restored\n"), 0o600); err != nil {
		t.Fatalf("restore project: %v", err)
	}
	root, err = ValidateRootConfigForRun(config, filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{
		filesystemconnector.DefaultSafeRoot("slice09", parent),
	}})
	if err != nil {
		t.Fatalf("ValidateRootConfigForRun restored failed: %v", err)
	}
	result, err = Reconcile(context.Background(), store, ScanRequest{Mode: ScanModeFull, Root: root})
	if err != nil {
		t.Fatalf("restored Reconcile failed: %v", err)
	}
	if result.Status != RunStatusHealthy || result.Counts.FindingsResolved != 1 || result.Counts.Findings != 0 {
		t.Fatalf("expected restored root to resolve stale finding, got %#v", result)
	}
	findings, err := store.ListFindings("notes", 0)
	if err != nil {
		t.Fatalf("ListFindings after restore failed: %v", err)
	}
	if len(findings) != 1 || findings[0].Kind != FindingRootUnavailable || findings[0].Status != FindingStatusResolved {
		t.Fatalf("expected resolved root-unavailable audit finding, got %#v", findings)
	}
}

func TestReconcileBudgetCreatesFinding(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"a.md", "b.md", "c.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("# "+name+"\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	root := testScanRoot(t, dir, RootConfig{
		RootKey:     "notes",
		SafeRootKey: "slice09",
		Include:     []string{"**/*.md"},
		Scan: ScanConfig{
			StabilityWindow: "0s",
			MaxFilesPerRun:  1,
		},
	})
	result, err := Reconcile(context.Background(), NewStore(t.TempDir()), ScanRequest{
		Mode: ScanModeFull,
		Root: root,
	})
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if result.Status != RunStatusDegraded || result.Counts.BudgetExhausted == 0 || len(result.Findings) == 0 {
		t.Fatalf("expected degraded budget result, got %#v", result)
	}
}

func TestReconcileBudgetExhaustionDoesNotMarkUnvisitedPreviousStateDeleted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"a.md", "z.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("# "+name+"\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	store := NewStore(t.TempDir())
	root := testScanRoot(t, dir, RootConfig{
		RootKey:     "notes",
		SafeRootKey: "slice09",
		Include:     []string{"**/*.md"},
		Scan: ScanConfig{
			StabilityWindow: "0s",
			MaxFilesPerRun:  10,
		},
	})
	initial, err := Reconcile(context.Background(), store, ScanRequest{
		Mode: ScanModeFull,
		Root: root,
	})
	if err != nil {
		t.Fatalf("initial Reconcile failed: %v", err)
	}
	if initial.Status != RunStatusHealthy || initial.Counts.Included != 2 {
		t.Fatalf("unexpected initial result %#v", initial)
	}

	budgetedRoot := testScanRoot(t, dir, RootConfig{
		RootKey:     "notes",
		SafeRootKey: "slice09",
		Include:     []string{"**/*.md"},
		Scan: ScanConfig{
			StabilityWindow: "0s",
			MaxFilesPerRun:  1,
		},
	})
	result, err := Reconcile(context.Background(), store, ScanRequest{
		Mode: ScanModeFull,
		Root: budgetedRoot,
	})
	if err != nil {
		t.Fatalf("budgeted Reconcile failed: %v", err)
	}
	if result.Status != RunStatusDegraded || result.Counts.BudgetExhausted == 0 {
		t.Fatalf("expected degraded budget result, got %#v", result)
	}
	state, err := store.LoadPathState("notes", "z.md")
	if err != nil {
		t.Fatalf("LoadPathState z.md failed: %v", err)
	}
	if state.Status == PathStatusDeleted || state.Status == PathStatusMissingDeferred {
		t.Fatalf("budget exhaustion should not mark unvisited state deleted: %#v", state)
	}
	findings, err := store.ListFindings("notes", 0)
	if err != nil {
		t.Fatalf("ListFindings after budgeted scan failed: %v", err)
	}
	if len(findings) != 1 || findings[0].Kind != FindingScanBudgetExceeded || findings[0].Status != FindingStatusOpen {
		t.Fatalf("expected open budget finding, got %#v", findings)
	}

	result, err = Reconcile(context.Background(), store, ScanRequest{
		Mode: ScanModeFull,
		Root: root,
	})
	if err != nil {
		t.Fatalf("full Reconcile after budget exhaustion failed: %v", err)
	}
	if result.Status != RunStatusHealthy || result.Counts.FindingsResolved != 1 || result.Counts.Findings != 0 {
		t.Fatalf("expected complete scan to resolve budget finding, got %#v", result)
	}
	findings, err = store.ListFindings("notes", 0)
	if err != nil {
		t.Fatalf("ListFindings after complete scan failed: %v", err)
	}
	if len(findings) != 1 || findings[0].Status != FindingStatusResolved {
		t.Fatalf("expected resolved budget finding, got %#v", findings)
	}
}

func TestReconcileObservesEmptyDirectoriesWithMetadataBackupAction(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "Empty"), 0o700); err != nil {
		t.Fatalf("mkdir empty: %v", err)
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
	result, err := Reconcile(context.Background(), store, ScanRequest{Mode: ScanModeFull, Root: root})
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if result.Counts.Directories == 0 {
		t.Fatalf("expected directory count, got %#v", result.Counts)
	}
	state, err := store.LoadPathState("documents", "Empty")
	if err != nil {
		t.Fatalf("LoadPathState Empty failed: %v", err)
	}
	if state.Status != PathStatusExcluded || state.Kind != PathKindDirectory || state.Fidelity == nil || state.Fidelity.Kind != filesystemmeta.ObjectKindDirectory {
		t.Fatalf("unexpected empty dir state %#v", state)
	}
	plan, err := PlanOutputs(store, root, "node-agent.watched_root.documents")
	if err != nil {
		t.Fatalf("PlanOutputs failed: %v", err)
	}
	if plan.Counts.BackupMetadata != 1 || len(plan.Actions) != 1 {
		t.Fatalf("empty directory should produce one metadata backup action, got %#v", plan)
	}
	action := plan.Actions[0]
	if action.ActionKind != OutputActionBackupMetadata || action.BackupItemKind != "directory" || action.ContentHashURI == "" {
		t.Fatalf("unexpected empty directory backup action: %#v", action)
	}
}

func TestReconcileIgnoreTransitionProducesMetadataOnlyDirectoryEvidence(t *testing.T) {
	dir := t.TempDir()
	acceptanceDir := filepath.Join(dir, ".loom-acceptance", "exact-run")
	if err := os.MkdirAll(acceptanceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	ignorePath := filepath.Join(dir, ".loomignore")
	if err := os.WriteFile(ignorePath, []byte(".loom-acceptance/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := RootConfig{
		RootKey:      "documents",
		SafeRootKey:  "slice09",
		Include:      []string{"**/*"},
		IgnorePolicy: IgnorePolicy{Profile: "managed", DiscoverUserRules: true},
		Scan:         ScanConfig{StabilityWindow: "0s", HiddenPolicy: HiddenPolicyPolicyControlled},
		BackupPolicy: BackupPolicy{Mode: BackupModeIncrementalRaw, MaxFileBytes: 1024, MaxBatchBytes: 1024},
	}
	store := NewStore(t.TempDir())
	ignoredRoot := testScanRoot(t, dir, config)
	if _, err := Reconcile(context.Background(), store, ScanRequest{Mode: ScanModeFull, Root: ignoredRoot}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ignorePath); err != nil {
		t.Fatal(err)
	}
	includedRoot := testScanRoot(t, dir, config)
	if _, err := Reconcile(context.Background(), store, ScanRequest{Mode: ScanModeFull, Root: includedRoot}); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanOutputs(store, includedRoot, "node-agent.watched_root.documents")
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.ToSlash(filepath.Join(".loom-acceptance", "exact-run"))
	for _, action := range plan.Actions {
		if action.RelativePath != wantPath {
			continue
		}
		if action.ActionKind != OutputActionBackupMetadata || action.BackupItemKind != "directory" || action.ContentHashURI == "" || action.Fidelity == nil || action.Fidelity.Kind != filesystemmeta.ObjectKindDirectory {
			t.Fatalf("unexpected post-ignore directory action: %#v", action)
		}
		return
	}
	t.Fatalf("post-ignore exact directory metadata action missing: %#v", plan.Actions)
}

func TestReconcileDeletedDirectoryMetadataQueuesBackupDeletionMarker(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	emptyDir := filepath.Join(dir, "Empty")
	if err := os.Mkdir(emptyDir, 0o700); err != nil {
		t.Fatalf("mkdir empty: %v", err)
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
		t.Fatalf("first reconcile failed: %v", err)
	}
	plan, err := PlanOutputs(store, root, "node-agent.watched_root.documents")
	if err != nil {
		t.Fatalf("PlanOutputs failed: %v", err)
	}
	if plan.Counts.BackupMetadata != 1 || len(plan.Actions) != 1 {
		t.Fatalf("expected directory metadata backup action, got %#v", plan)
	}
	state, err := store.LoadPathState("documents", "Empty")
	if err != nil {
		t.Fatalf("LoadPathState Empty failed: %v", err)
	}
	state.LastBackedUpHashURI = plan.Actions[0].ContentHashURI
	state.BackupStatus = OutputStatusRecorded
	if err := store.SavePathState(state); err != nil {
		t.Fatalf("SavePathState failed: %v", err)
	}

	if err := os.Remove(emptyDir); err != nil {
		t.Fatalf("remove empty dir: %v", err)
	}
	result, err := Reconcile(context.Background(), store, ScanRequest{Mode: ScanModeFull, Root: root})
	if err != nil {
		t.Fatalf("delete reconcile failed: %v", err)
	}
	if result.Counts.Deleted != 1 {
		t.Fatalf("expected one deleted directory metadata state, got %#v", result.Counts)
	}
	deleted, err := store.LoadPathState("documents", "Empty")
	if err != nil {
		t.Fatalf("LoadPathState deleted Empty failed: %v", err)
	}
	if deleted.Status != PathStatusDeleted || deleted.DeletedAt == nil || deleted.Kind != PathKindMissing {
		t.Fatalf("unexpected deleted directory metadata state: %#v", deleted)
	}
	plan, err = PlanOutputs(store, root, "node-agent.watched_root.documents")
	if err != nil {
		t.Fatalf("PlanOutputs deletion failed: %v", err)
	}
	if plan.Counts.BackupDeletionMarkers != 1 || len(plan.Actions) != 1 {
		t.Fatalf("expected one directory backup deletion marker, got %#v", plan)
	}
	if plan.Actions[0].ActionKind != OutputActionBackupDeletionMarker ||
		plan.Actions[0].PreviousHashURI == "" ||
		plan.Actions[0].DeletedAt == nil {
		t.Fatalf("unexpected directory backup deletion action %#v", plan.Actions[0])
	}
}

func TestReconcileObservesSymlinkWithoutFollowing(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "target.md"), []byte("# target\n"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink("target.md", filepath.Join(dir, "link.md")); err != nil {
		t.Fatalf("symlink target: %v", err)
	}
	if err := os.Symlink("missing.md", filepath.Join(dir, "broken.md")); err != nil {
		t.Fatalf("symlink broken: %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, ".."), filepath.Join(dir, "external")); err != nil {
		t.Fatalf("symlink external: %v", err)
	}
	root := testScanRoot(t, dir, RootConfig{
		RootKey:     "notes",
		SafeRootKey: "slice09",
		Include:     []string{"**/*"},
		Scan:        ScanConfig{StabilityWindow: "0s"},
	})
	store := NewStore(t.TempDir())
	if _, err := Reconcile(context.Background(), store, ScanRequest{Mode: ScanModeFull, Root: root}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	for _, rel := range []string{"link.md", "broken.md", "external"} {
		state, err := store.LoadPathState("notes", rel)
		if err != nil {
			t.Fatalf("LoadPathState %s failed: %v", rel, err)
		}
		if state.Status != PathStatusSkipped || state.Classification.ReasonCode != ReasonSkippedSymlink || state.Fidelity == nil || state.Fidelity.Kind != filesystemmeta.ObjectKindSymlink {
			t.Fatalf("unexpected symlink state for %s: %#v", rel, state)
		}
	}
	external, err := store.LoadPathState("notes", "external")
	if err != nil {
		t.Fatalf("LoadPathState external failed: %v", err)
	}
	if !containsString(external.Fidelity.Risks, filesystemmeta.FidelityRiskExternalReference) {
		t.Fatalf("external symlink risks = %#v, want external_reference", external.Fidelity.Risks)
	}
	plan, err := PlanOutputs(store, root, "node-agent.watched_root.notes")
	if err != nil {
		t.Fatalf("PlanOutputs failed: %v", err)
	}
	for _, action := range plan.Actions {
		if action.RelativePath == "link.md" || action.RelativePath == "broken.md" || action.RelativePath == "external" {
			t.Fatalf("symlink should not produce output action: %#v", action)
		}
	}
}

func TestReconcilePackageDirectoryStopsAtBoundary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	inside := filepath.Join(dir, "Draft.pages", "index.zip")
	if err := os.MkdirAll(filepath.Dir(inside), 0o700); err != nil {
		t.Fatalf("mkdir package: %v", err)
	}
	if err := os.WriteFile(inside, []byte("package content"), 0o600); err != nil {
		t.Fatalf("write package child: %v", err)
	}
	root := testScanRoot(t, dir, RootConfig{
		RootKey:     "documents",
		SafeRootKey: "slice09",
		Include:     []string{"**/*"},
		Scan:        ScanConfig{StabilityWindow: "0s"},
	})
	store := NewStore(t.TempDir())
	if _, err := Reconcile(context.Background(), store, ScanRequest{Mode: ScanModeFull, Root: root}); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	state, err := store.LoadPathState("documents", "Draft.pages")
	if err != nil {
		t.Fatalf("LoadPathState package failed: %v", err)
	}
	if state.Status != PathStatusExcluded || state.Classification.ReasonCode != ReasonExcludedPackageBoundary || state.Fidelity == nil || !state.Fidelity.IsPackage {
		t.Fatalf("unexpected package state %#v", state)
	}
	if _, err := store.LoadPathState("documents", "Draft.pages/index.zip"); !IsNotExist(err) {
		t.Fatalf("package child should not be scanned, err=%v", err)
	}
}

func TestReconcileSpecialFileIsObservedAndSkipped(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("special file fixture uses Unix FIFO")
	}
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	root := testScanRoot(t, dir, RootConfig{
		RootKey:     "documents",
		SafeRootKey: "slice09",
		Include:     []string{"**/*"},
		Scan:        ScanConfig{StabilityWindow: "0s"},
	})
	store := NewStore(t.TempDir())
	result, err := Reconcile(context.Background(), store, ScanRequest{Mode: ScanModeFull, Root: root})
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	state, err := store.LoadPathState("documents", "pipe")
	if err != nil {
		t.Fatalf("LoadPathState pipe failed: %v", err)
	}
	if state.Status != PathStatusSkipped || state.Classification.ReasonCode != ReasonSkippedSpecialFile || state.Fidelity == nil || state.Fidelity.Kind != filesystemmeta.ObjectKindSpecial {
		t.Fatalf("unexpected special file state %#v", state)
	}
	var sawSpecialFinding bool
	for _, finding := range result.Findings {
		if finding.Kind == FindingSpecialFileObserved && finding.RelativePath == "pipe" {
			sawSpecialFinding = true
		}
	}
	if !sawSpecialFinding {
		t.Fatalf("expected special file finding, got %#v", result.Findings)
	}
	plan, err := PlanOutputs(store, root, "node-agent.watched_root.documents")
	if err != nil {
		t.Fatalf("PlanOutputs failed: %v", err)
	}
	if len(plan.Actions) != 0 {
		t.Fatalf("special file should not produce output actions: %#v", plan.Actions)
	}
}

func TestReconcileGeneratedAppleMetadataIsObservedButExcluded(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "._note.md"), []byte("sidecar"), 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	root := testScanRoot(t, dir, RootConfig{
		RootKey:     "notes",
		SafeRootKey: "slice09",
		Include:     []string{"**/*"},
		Scan:        ScanConfig{StabilityWindow: "0s"},
	})
	store := NewStore(t.TempDir())
	result, err := Reconcile(context.Background(), store, ScanRequest{Mode: ScanModeFull, Root: root})
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if result.Status != RunStatusHealthy {
		t.Fatalf("generated metadata info finding should not degrade scan: %#v", result)
	}
	state, err := store.LoadPathState("notes", "._note.md")
	if err != nil {
		t.Fatalf("LoadPathState sidecar failed: %v", err)
	}
	if state.Status != PathStatusExcluded || state.Classification.ReasonCode != ReasonExcludedGeneratedMetadata || state.Fidelity == nil || !state.Fidelity.GeneratedMetadata {
		t.Fatalf("unexpected sidecar state %#v", state)
	}
}

func TestReconcileExecutableBitMetadataChangeCountsAsChange(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("executable mode bits differ on Windows")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	root := testScanRoot(t, dir, RootConfig{
		RootKey:     "scripts",
		SafeRootKey: "slice09",
		Include:     []string{"**/*"},
		Scan:        ScanConfig{StabilityWindow: "0s"},
	})
	store := NewStore(t.TempDir())
	if _, err := Reconcile(context.Background(), store, ScanRequest{Mode: ScanModeFull, Root: root}); err != nil {
		t.Fatalf("initial Reconcile failed: %v", err)
	}
	before, err := store.LoadPathState("scripts", "run.sh")
	if err != nil {
		t.Fatalf("LoadPathState before failed: %v", err)
	}
	if before.Fidelity == nil || before.Fidelity.Executable {
		t.Fatalf("expected non-executable initial fidelity, got %#v", before.Fidelity)
	}
	if err := os.Chmod(script, 0o700); err != nil {
		t.Fatalf("chmod executable: %v", err)
	}
	result, err := Reconcile(context.Background(), store, ScanRequest{Mode: ScanModeFull, Root: root})
	if err != nil {
		t.Fatalf("second Reconcile failed: %v", err)
	}
	if result.Counts.Changed == 0 {
		t.Fatalf("expected metadata-only executable bit change, got %#v", result.Counts)
	}
	after, err := store.LoadPathState("scripts", "run.sh")
	if err != nil {
		t.Fatalf("LoadPathState after failed: %v", err)
	}
	if after.ContentHashURI != before.ContentHashURI || after.Fidelity == nil || !after.Fidelity.Executable {
		t.Fatalf("expected same hash and executable fidelity, before=%#v after=%#v", before, after)
	}
}

func TestRecordPathCollisionFindingMarksObservationRisk(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	root := testScanRoot(t, t.TempDir(), RootConfig{
		RootKey:     "notes",
		SafeRootKey: "slice09",
	})
	result := &ScanResult{}
	seen := map[string]string{}
	first := PathObservation{
		RelativePath: "Report.md",
		Fidelity:     &filesystemmeta.Observation{CasefoldKey: "report.md"},
	}
	second := PathObservation{
		RelativePath: "report.md",
		Fidelity:     &filesystemmeta.Observation{CasefoldKey: "report.md"},
	}
	if err := recordPathCollisionFinding(store, root, seen, &first, result); err != nil {
		t.Fatalf("record first failed: %v", err)
	}
	if err := recordPathCollisionFinding(store, root, seen, &second, result); err != nil {
		t.Fatalf("record second failed: %v", err)
	}
	if len(result.Findings) != 1 || result.Findings[0].Kind != FindingPathCollisionWarning {
		t.Fatalf("expected one collision finding, got %#v", result.Findings)
	}
	if !containsString(second.Fidelity.Risks, filesystemmeta.FidelityRiskPathCollision) {
		t.Fatalf("expected path collision risk, got %#v", second.Fidelity.Risks)
	}
}

func TestReconcilePermissionDeniedCreatesFindingWithoutDeletingState(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission-denied directory traversal is not reliable for this user/platform")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.MkdirAll(filepath.Join(locked, "child"), 0o700); err != nil {
		t.Fatalf("mkdir locked child: %v", err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatalf("chmod locked: %v", err)
	}
	defer func() {
		_ = os.Chmod(locked, 0o700)
	}()
	root := testScanRoot(t, dir, RootConfig{
		RootKey:     "documents",
		SafeRootKey: "slice09",
		Include:     []string{"**/*"},
		Scan:        ScanConfig{StabilityWindow: "0s"},
	})
	store := NewStore(t.TempDir())
	result, err := Reconcile(context.Background(), store, ScanRequest{Mode: ScanModeFull, Root: root})
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if result.Status != RunStatusDegraded || len(result.Findings) == 0 {
		t.Fatalf("expected degraded permission finding, got %#v", result)
	}
	var sawPermissionFinding bool
	for _, finding := range result.Findings {
		if finding.Kind == FindingPermissionDenied && finding.RelativePath == "locked" {
			sawPermissionFinding = true
		}
	}
	if !sawPermissionFinding {
		t.Fatalf("expected permission finding for locked dir, got %#v", result.Findings)
	}
	state, err := store.LoadPathState("documents", "locked")
	if err != nil {
		t.Fatalf("LoadPathState locked failed: %v", err)
	}
	if state.Status == PathStatusDeleted || state.Status == PathStatusMissingDeferred {
		t.Fatalf("permission denied should not mark path deleted: %#v", state)
	}
}

func testScanRoot(t *testing.T, dir string, config RootConfig) ValidatedRoot {
	t.Helper()
	root, err := ValidateRootConfig(config, filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{
		filesystemconnector.DefaultSafeRoot("slice09", dir),
	}})
	if err != nil {
		t.Fatalf("ValidateRootConfig failed: %v", err)
	}
	return root
}
