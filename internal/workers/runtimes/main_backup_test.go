package runtimes

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"loom.local/loom/internal/hermesprofile"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"loom.local/loom/internal/config"
	"loom.local/loom/internal/health"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/provenance"
	"loom.local/loom/internal/version"
	"loom.local/loom/internal/workers"
)

func TestMainBackupRuntimeDescriptorAndInstance(t *testing.T) {
	runtime := NewMainBackupRuntime(nil, maintenance.Service{}, testHealthService(t.TempDir()), "", "/var/lib/loom", "/var/lib/loom/object-store", "test")
	if runtime.Kind() != workers.KindMainBackup {
		t.Fatalf("kind = %q, want %q", runtime.Kind(), workers.KindMainBackup)
	}
	descriptor := runtime.Describe()
	if descriptor.WorkerKind != workers.KindMainBackup {
		t.Fatalf("descriptor kind = %q, want %q", descriptor.WorkerKind, workers.KindMainBackup)
	}
	assertManualTransitionTickPolicy(t, descriptor.DefaultTickPolicyJSON)
	instances := runtime.DefaultInstances()
	if len(instances) != 1 {
		t.Fatalf("default instance count = %d, want 1", len(instances))
	}
	if instances[0].WorkerKey != "main.main_backup" {
		t.Fatalf("worker key = %q, want main.main_backup", instances[0].WorkerKey)
	}
	assertManualTransitionTickPolicy(t, instances[0].TickPolicyJSON)

	registry := workers.NewRegistry()
	if err := registry.Register(runtime); err != nil {
		t.Fatalf("register seed-facing runtime: %v", err)
	}
	registeredDescriptors := registry.Descriptors()
	registeredInstances := registry.DefaultInstances()
	if len(registeredDescriptors) != 1 || len(registeredInstances) != 1 {
		t.Fatalf("seed-facing registry = descriptors:%#v instances:%#v", registeredDescriptors, registeredInstances)
	}
	assertManualTransitionTickPolicy(t, registeredDescriptors[0].DefaultTickPolicyJSON)
	assertManualTransitionTickPolicy(t, registeredInstances[0].TickPolicyJSON)
}

func TestDetachedMainBackupCompletionContextSurvivesRequestCancellation(t *testing.T) {
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()

	completionCtx, cancelCompletion := detachedMainBackupCompletionContext(requestCtx)
	defer cancelCompletion()
	if err := completionCtx.Err(); err != nil {
		t.Fatalf("completion context inherited canceled request: %v", err)
	}
	deadline, ok := completionCtx.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > mainBackupCompletionTimeout {
		t.Fatalf("completion deadline = %v, want bounded future deadline", deadline)
	}
}

func TestRecoverableTerminalWorkerRunStatusFailsClosed(t *testing.T) {
	for _, status := range []string{workers.RunStatusSucceeded, workers.RunStatusFailed, workers.RunStatusCancelled, workers.RunStatusTimedOut} {
		if !recoverableTerminalWorkerRunStatus(status) {
			t.Fatalf("terminal status %q was not recoverable", status)
		}
	}
	for _, status := range []string{"", workers.RunStatusStarting, workers.RunStatusRunning, "unknown"} {
		if recoverableTerminalWorkerRunStatus(status) {
			t.Fatalf("non-terminal status %q was treated as recoverable", status)
		}
	}
}

func assertManualTransitionTickPolicy(t *testing.T, raw json.RawMessage) {
	t.Helper()
	policy, err := workers.ParseTickPolicy(raw)
	if err != nil {
		t.Fatalf("ParseTickPolicy(%s): %v", raw, err)
	}
	if policy.Mode != workers.TickModeManual || policy.RunOnStartup || policy.NextAfter(time.Now()) != nil {
		t.Fatalf("transition tick policy = %#v, want unscheduled manual execution", policy)
	}
}

func TestMainBackupRuntimeValidateConfig(t *testing.T) {
	dir := t.TempDir()
	runtime := NewMainBackupRuntime(nil, maintenance.Service{}, testHealthService(dir), "", dir, filepath.Join(dir, "object-store"), "test")
	if err := runtime.ValidateConfig(context.Background(), runtime.DefaultConfig()); err != nil {
		t.Fatalf("ValidateConfig returned error: %v", err)
	}
	var defaults mainBackupConfig
	if err := json.Unmarshal(runtime.DefaultConfig(), &defaults); err != nil {
		t.Fatalf("default config is invalid JSON: %v", err)
	}
	if !defaults.IncludeMainDocuments || defaults.MainDocumentsRoot != filepath.Join(dir, "box", "Documents") {
		t.Fatalf("default main documents config = include:%t root:%q", defaults.IncludeMainDocuments, defaults.MainDocumentsRoot)
	}
	if !defaults.IncludeBoxNotes || defaults.BoxNotesRoot != filepath.Join(dir, "box", "Notes") {
		t.Fatalf("default Box Notes config = include:%t root:%q", defaults.IncludeBoxNotes, defaults.BoxNotesRoot)
	}
	if !defaults.IncludeStorageRetention || defaults.StorageRetentionRoot != filepath.Join(dir, "storage-retention") {
		t.Fatalf("default storage retention config = include:%t root:%q", defaults.IncludeStorageRetention, defaults.StorageRetentionRoot)
	}
	if !defaults.IncludePrivateBackups || defaults.UserBackupsRoot != filepath.Join(dir, "user-backups") {
		t.Fatalf("default user backups config = include:%t root:%q", defaults.IncludePrivateBackups, defaults.UserBackupsRoot)
	}
	if !defaults.IncludeStorageArchive || defaults.StorageArchiveRoot != filepath.Join(dir, "archive") {
		t.Fatalf("default storage archive config = include:%t root:%q", defaults.IncludeStorageArchive, defaults.StorageArchiveRoot)
	}
	if defaults.IncludeNotesProjection || defaults.NotesProjectionRoot != filepath.Join(dir, "generated", "notes") {
		t.Fatalf("default notes projection config = include:%t root:%q", defaults.IncludeNotesProjection, defaults.NotesProjectionRoot)
	}
	if defaults.MainBoxPolicy != "selected_canonical_roots_copied_once" {
		t.Fatalf("main box policy = %q", defaults.MainBoxPolicy)
	}
	err := runtime.ValidateConfig(context.Background(), json.RawMessage(`{"driver":"operator_script"}`))
	if !errors.Is(err, workers.ErrInvalid) {
		t.Fatalf("ValidateConfig error = %v, want workers.ErrInvalid", err)
	}
}

func TestMainBackupImportsPolicyDoesNotFollowFilesystemCutoverFlag(t *testing.T) {
	for _, legacySplit := range []bool{false, true} {
		service := testHealthService(t.TempDir())
		service.Config.LegacySplitRoots = legacySplit
		service.Config.ImportsBackupPolicy = maintenance.ImportsBackupPolicyLegacy
		runtime := NewMainBackupRuntime(nil, maintenance.Service{}, service, "", service.Config.DataDir, service.Config.ObjectStore, "test")
		if got := runtime.defaultImportsPolicy(); got != maintenance.ImportsBackupPolicyLegacy {
			t.Fatalf("legacy_split_roots=%t changed explicit Imports policy to %q", legacySplit, got)
		}
	}
}

func TestMainBackupRuntimeHistoricalBackupRootCannotSelectNewProducerOutput(t *testing.T) {
	dir := t.TempDir()
	objectStore := filepath.Join(dir, "object-store")
	runtime := NewMainBackupRuntime(nil, maintenance.Service{}, testHealthService(dir), "", dir, objectStore, "test")
	cfg, err := runtime.parseConfig(json.RawMessage(`{"schema_version":"main_backup.config.v0.2","backup_root":"` + filepath.ToSlash(filepath.Join(objectStore, "backups")) + `"}`))
	if err != nil {
		t.Fatalf("historical config did not decode: %v", err)
	}
	if cfg.SchemaVersion != mainBackupConfigSchema || cfg.ProducerMode != mainBackupProducerMode || cfg.OperationalPackagesRoot != filepath.Join(dir, "backups", "operational") || cfg.ProvenancePackagesRoot != filepath.Join(dir, "backups", "provenance") {
		t.Fatalf("normalized historical config = %#v", cfg)
	}
}

func TestMainBackupRuntimeHistoricalCoverageFlagsDecodeWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	runtime := NewMainBackupRuntime(nil, maintenance.Service{}, testHealthService(dir), "", dir, filepath.Join(dir, "object-store"), "test")
	if err := runtime.ValidateConfig(context.Background(), json.RawMessage(`{"schema_version":"main_backup.config.v0.2","include_imports":false}`)); err != nil {
		t.Fatalf("historical coverage config did not decode: %v", err)
	}
	if entries, readErr := os.ReadDir(filepath.Join(dir, "backups")); readErr == nil && len(entries) != 0 {
		t.Fatalf("invalid config mutated backup root: %v", entries)
	} else if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatalf("inspect backup root: %v", readErr)
	}
}

func TestMainBackupRuntimeIgnoresHistoricalBackupRootInsideNotesProjection(t *testing.T) {
	dir := t.TempDir()
	runtime := NewMainBackupRuntime(nil, maintenance.Service{}, testHealthService(dir), "", dir, filepath.Join(dir, "object-store"), "test")
	err := runtime.ValidateConfig(context.Background(), json.RawMessage(`{"backup_root":"`+filepath.ToSlash(filepath.Join(dir, "generated", "notes", "backups"))+`"}`))
	if err != nil {
		t.Fatalf("historical config did not decode: %v", err)
	}
}

func TestMainBackupRuntimeIgnoresHistoricalBackupRootInsideCanonicalCustody(t *testing.T) {
	dir := t.TempDir()
	runtime := NewMainBackupRuntime(nil, maintenance.Service{}, testHealthService(dir), "", dir, filepath.Join(dir, "object-store"), "test")
	for _, backupRoot := range []string{
		filepath.Join(dir, "user-backups", "recursive-main-backup"),
		filepath.Join(dir, "archive", "recursive-main-backup"),
	} {
		raw, err := json.Marshal(map[string]any{"backup_root": backupRoot})
		if err != nil {
			t.Fatal(err)
		}
		if err := runtime.ValidateConfig(context.Background(), raw); err != nil {
			t.Fatalf("historical backup root %q did not decode: %v", backupRoot, err)
		}
	}
}

func TestMainBackupRuntimeIgnoresHistoricalUserBackupsRootOverride(t *testing.T) {
	dir := t.TempDir()
	runtime := NewMainBackupRuntime(nil, maintenance.Service{}, testHealthService(dir), "", dir, filepath.Join(dir, "object-store"), "test")
	raw, err := json.Marshal(map[string]any{"user_backups_root": filepath.Join(dir, "other-backups")})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.ValidateConfig(context.Background(), raw); err != nil {
		t.Fatalf("historical user root did not decode: %v", err)
	}
}

func TestMainBackupRuntimeCreatesBoundedOperationalPackageAndReplaysExactly(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	sources := filepath.Join(root, "sources")
	if err := os.MkdirAll(sources, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSource := func(name, value string) string {
		path := filepath.Join(sources, name)
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	runtime := NewMainBackupRuntime(nil, maintenance.Service{}, testHealthService(dataDir), "postgres://unused", dataDir, filepath.Join(dataDir, "object-store"), "test")
	runtime.ExecCommand = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		destination := args[len(args)-1]
		return exec.CommandContext(ctx, "/bin/sh", "-c", `printf 'PGDMP\001bounded-operational-package' > "$1"`, "sh", destination)
	}
	cfg := runtime.defaultMainBackupConfig()
	cfg.ServiceConfigPath = writeSource("loom.env", "LOOM_ENV=production\nLOOM_DB_URL=postgres://redacted\n")
	cfg.InstallConfigPath = writeSource("install.yaml", "profile: main\n")
	cfg.ReleaseConfigPath = writeSource("loom-release.yaml", "release_id: current\n")
	cfg.BackupRoot = filepath.Join(root, "legacy-full-generations")
	packageID := operationalPackageID("worker_run_replay")
	createdAt := time.Date(2026, 8, 30, 7, 30, 0, 0, time.UTC)
	created, idempotent, err := runtime.createOperationalPackage(context.Background(), cfg, packageID, createdAt)
	if err != nil {
		t.Fatalf("create operational package: %v", err)
	}
	if idempotent || created.Verification.Status != maintenance.VerificationSucceeded {
		t.Fatalf("initial result = %#v idempotent=%t", created, idempotent)
	}
	if _, err := os.Lstat(cfg.BackupRoot); !os.IsNotExist(err) {
		t.Fatalf("new path created complete local generation root %q: %v", cfg.BackupRoot, err)
	}
	entries, err := os.ReadDir(created.PackageDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != created.Verification.ArtifactCount+2 {
		t.Fatalf("package entries = %d verification=%#v", len(entries), created.Verification)
	}
	replayed, idempotent, err := runtime.createOperationalPackage(context.Background(), cfg, packageID, createdAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("replay operational package: %v", err)
	}
	if !idempotent || replayed.ManifestSHA256 != created.ManifestSHA256 || replayed.PackageDir != created.PackageDir {
		t.Fatalf("replay = %#v idempotent=%t, initial=%#v", replayed, idempotent, created)
	}
}

func TestMainBackupRuntimeCreatesAndVerifiesIndependentProvenanceRecoveryPackage(t *testing.T) {
	root := t.TempDir()
	runtime := NewMainBackupRuntime(nil, maintenance.Service{}, testHealthService(root), "", root, filepath.Join(root, "object-store"), "test")
	verifyCalls := 0
	runtime.CreateProvenancePackage = func(ctx context.Context, input provenance.RecoveryPackageInput) (provenance.RecoveryPackageResult, error) {
		result := writeMainBackupProvenanceFixture(t, input)
		if err := input.Verify(ctx, result.PackageDir, result.ManifestSHA256); err != nil {
			return provenance.RecoveryPackageResult{}, err
		}
		verifyCalls++
		return result, nil
	}
	config := runtime.defaultMainBackupConfig()
	createdAt := time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC)
	result, err := runtime.createProvenanceRecoveryPackage(context.Background(), config, "provenance-worker-test", "maintenance-operation-test", "worker-run-test", "loom-main", createdAt)
	if err != nil {
		t.Fatalf("create provenance recovery package: %v", err)
	}
	if verifyCalls != 1 || result.ManifestSHA256 == "" || result.ManifestSHA256 == strings.Repeat("a", 64) || result.PackageDir != filepath.Join(config.ProvenancePackagesRoot, result.PackageID) {
		t.Fatalf("provenance result = %#v verify calls=%d", result, verifyCalls)
	}
	verification, err := maintenance.VerifyProvenanceBackupPackage(context.Background(), result.PackageDir, result.ManifestSHA256)
	if err != nil || verification.ManifestSHA256 != result.ManifestSHA256 || verification.GraphDigest != result.Snapshot.GraphDigest {
		t.Fatalf("independent provenance verification = %#v err=%v", verification, err)
	}
}

func writeMainBackupProvenanceFixture(t *testing.T, input provenance.RecoveryPackageInput) provenance.RecoveryPackageResult {
	t.Helper()
	packageDir := filepath.Join(input.PackagesRoot, input.PackageID)
	if err := os.Mkdir(packageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dump := []byte("PGDMP\x01worker provenance fixture\n")
	dumpPath := filepath.Join(packageDir, provenance.RecoveryDumpFile)
	if err := os.WriteFile(dumpPath, dump, 0o600); err != nil {
		t.Fatal(err)
	}
	dumpDigest := sha256.Sum256(dump)
	dumpSHA := hex.EncodeToString(dumpDigest[:])
	dumpSize := int64(len(dump))
	fileCount := int64(1)
	counts := make(map[string]int64, len(provenance.RecoveryRelations()))
	for _, relation := range provenance.RecoveryRelations() {
		counts[relation] = 0
	}
	graphDigest := "sha256:" + strings.Repeat("6", sha256.Size*2)
	completedAt := input.CreatedAt.Add(time.Minute).UTC()
	manifest := maintenance.BackupManifest{
		Schema: provenance.RecoveryManifestSchema, BackupKind: provenance.RecoveryBackupKind,
		BackupOperationID: input.BackupOperationID, WorkerRunID: input.WorkerRunID, CreatedAt: completedAt.Format(time.RFC3339Nano),
		Source:    maintenance.BackupManifestSource{NodeID: input.NodeID},
		Artifacts: []maintenance.BackupManifestArtifact{{Kind: maintenance.ArtifactKindProvenanceDump, Path: provenance.RecoveryDumpFile, FileCount: &fileCount, SizeBytes: &dumpSize, SHA256: dumpSHA}},
		Provenance: &maintenance.BackupManifestProvenance{
			State: provenance.BackupStateComplete, StartedAt: input.CreatedAt.UTC().Format(time.RFC3339Nano), CompletedAt: completedAt.Format(time.RFC3339Nano),
			Database:   maintenance.BackupManifestDatabase{Name: provenance.DatabaseName, DumpFile: provenance.RecoveryDumpFile, Format: "pg_dump_custom"},
			SchemaHead: provenance.SchemaHead, RequiredRelations: provenance.RecoveryRelations(), LogicalCounts: counts,
			GraphDigest: graphDigest, DumpSizeBytes: dumpSize, DumpSHA256: dumpSHA,
		},
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	manifestBytes = append(manifestBytes, '\n')
	manifestPath := filepath.Join(packageDir, provenance.RecoveryManifestFile)
	if err := os.WriteFile(manifestPath, manifestBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	manifestDigest := sha256.Sum256(manifestBytes)
	return provenance.RecoveryPackageResult{
		PackageDir: packageDir, PackageID: input.PackageID, ManifestPath: manifestPath, ManifestSHA256: hex.EncodeToString(manifestDigest[:]),
		DumpSizeBytes: dumpSize, Snapshot: provenance.RecoverySnapshot{SchemaHead: provenance.SchemaHead, RelationCounts: counts, GraphDigest: graphDigest},
		CreatedAt: completedAt, BackupOperationID: input.BackupOperationID, WorkerRunID: input.WorkerRunID,
	}
}

func TestMainBackupManifestNotesProjectionExclusionMatchesCopyPolicy(t *testing.T) {
	runtime := NewMainBackupRuntime(nil, maintenance.Service{}, testHealthService(t.TempDir()), "", "/var/lib/loom", "/var/lib/loom/object-store", "test")
	config := runtime.defaultMainBackupConfig()
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	manifest := runtime.backupManifest(workers.RunContext{}, maintenance.Operation{}, config, now, "main", nil, userBackupsSnapshotCanonical)
	if manifest.Paths.UserBackups != "user-backups" || manifest.Paths.PrivateBackups != "" {
		t.Fatalf("backup manifest did not use canonical user backup path: %#v", manifest.Paths)
	}
	if !containsBackupExclusion(manifest.Exclusions, "generated_notes_projection") || manifest.Paths.NotesProjection != "" {
		t.Fatalf("default projection policy is contradictory: %#v", manifest)
	}

	config.IncludeNotesProjection = true
	manifest = runtime.backupManifest(workers.RunContext{}, maintenance.Operation{}, config, now, "main", nil, userBackupsSnapshotCanonical)
	if containsBackupExclusion(manifest.Exclusions, "generated_notes_projection") || manifest.Paths.NotesProjection != "loom-notes" {
		t.Fatalf("explicit projection copy policy is contradictory: %#v", manifest)
	}
	if manifest.Policies.NotesProjection != "explicitly_copied_generated_projection_and_rebuildable_from_canonical_notes_roots" {
		t.Fatalf("explicit projection policy = %q", manifest.Policies.NotesProjection)
	}
}

func TestMainBackupManifestDeclaresLegacyPrivateBackupsDuringSplitRootTransition(t *testing.T) {
	dir := t.TempDir()
	service := testHealthService(dir)
	service.Config.LegacySplitRoots = true
	runtime := NewMainBackupRuntime(nil, maintenance.Service{}, service, "", dir, filepath.Join(dir, "object-store"), "test")
	config := runtime.defaultMainBackupConfig()
	manifest := runtime.backupManifest(workers.RunContext{}, maintenance.Operation{}, config, time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC), "main", nil, runtime.userBackupsSnapshotLayout())
	if manifest.Paths.PrivateBackups != "private-backups" || manifest.Paths.UserBackups != "" || manifest.Paths.PrivateBackupsEvidence != maintenance.PrivateBackupsEvidencePath {
		t.Fatalf("legacy backup manifest paths = %#v", manifest.Paths)
	}
}

func TestMainBackupManifestDeclaresBothMixedCustodyPaths(t *testing.T) {
	runtime := NewMainBackupRuntime(nil, maintenance.Service{}, testHealthService(t.TempDir()), "", "/var/lib/loom", "/var/lib/loom/object-store", "test")
	manifest := runtime.backupManifest(
		workers.RunContext{},
		maintenance.Operation{},
		runtime.defaultMainBackupConfig(),
		time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC),
		"main",
		nil,
		userBackupsSnapshotMixed,
	)
	if manifest.Paths.UserBackups != "user-backups" || manifest.Paths.PrivateBackups != "private-backups" || manifest.Paths.PrivateBackupsEvidence != maintenance.PrivateBackupsEvidencePath {
		t.Fatalf("mixed backup manifest paths = %#v", manifest.Paths)
	}
	canonical := copySummary{SourceExists: true, FileCount: 2, TotalBytes: 41}
	legacy := copySummary{SourceExists: true, FileCount: 1, TotalBytes: 23}
	plan := userBackupsSnapshotPlan{Layout: userBackupsSnapshotMixed}
	manifestArtifacts := userBackupsManifestArtifacts(plan, canonical, legacy)
	maintenanceArtifacts := userBackupsMaintenanceArtifacts("maintenance_operation_test", "/backup", "/source", plan, canonical, legacy)
	if len(manifestArtifacts) != 2 || len(maintenanceArtifacts) != 2 {
		t.Fatalf("mixed artifacts = manifest:%#v maintenance:%#v", manifestArtifacts, maintenanceArtifacts)
	}
	for index, wantPath := range []string{"user-backups", "private-backups"} {
		if manifestArtifacts[index].Path != wantPath || manifestArtifacts[index].Kind != maintenance.ArtifactKindPrivateBackupsSnapshot || manifestArtifacts[index].FileCount == nil || manifestArtifacts[index].SizeBytes == nil {
			t.Fatalf("manifest artifact %d = %#v", index, manifestArtifacts[index])
		}
		if maintenanceArtifacts[index].ArtifactKind != maintenance.ArtifactKindPrivateBackupsSnapshot || maintenanceArtifacts[index].URI != fileURI(filepath.Join("/backup", wantPath)) || maintenanceArtifacts[index].SizeBytes == nil {
			t.Fatalf("maintenance artifact %d = %#v", index, maintenanceArtifacts[index])
		}
		var metadata map[string]any
		if err := json.Unmarshal(maintenanceArtifacts[index].Metadata, &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata["relative_path"] != wantPath || metadata["source_root"] != "/source" {
			t.Fatalf("maintenance artifact %d metadata = %#v", index, metadata)
		}
	}
}

func TestHistoricalV09BackupReaderAcceptsMixedCustodyEvidence(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	service := testHealthService(dataDir)
	importsRoot := service.Config.ImportsRoot
	userBackupsRoot := service.Config.UserBackupsRoot
	backupRoot := filepath.Join(root, "backups")
	for _, pathValue := range []string{importsRoot, userBackupsRoot, backupRoot} {
		if err := os.MkdirAll(pathValue, 0o750); err != nil {
			t.Fatal(err)
		}
	}

	batchID := ids.NewLocalBackupBatchID()
	canonicalBatch := filepath.Join(userBackupsRoot, "macbook", "documents", batchID)
	if err := os.MkdirAll(canonicalBatch, 0o750); err != nil {
		t.Fatal(err)
	}
	writeCanonicalMetadataOnlyUserBackupBatch(t, canonicalBatch, "macbook", "documents", batchID)
	legacyPayload := filepath.Join(userBackupsRoot, "node_legacy", "private_backup_accepted", "payload.tar")
	if err := os.MkdirAll(filepath.Dir(legacyPayload), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPayload, []byte("exact retained legacy payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	runtime := NewMainBackupRuntime(nil, maintenance.Service{}, service, "postgres://unused", dataDir, service.Config.ObjectStore, "test")
	var recorded []maintenance.CreateArtifactInput
	runtime.recordArtifact = func(_ context.Context, input maintenance.CreateArtifactInput) error {
		recorded = append(recorded, input)
		return nil
	}
	runtime.ExecCommand = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		dumpPath := args[len(args)-1]
		return exec.CommandContext(ctx, "/bin/sh", "-c", `printf 'test postgres dump' > "$1"`, "sh", dumpPath)
	}
	config := runtime.defaultMainBackupConfig()
	config.BackupRoot = backupRoot
	config.ImportsRoot = importsRoot
	config.ImportsPolicy = maintenance.ImportsBackupPolicyLegacy
	config.UserBackupsRoot = userBackupsRoot
	config.IncludeObjectStore = false
	config.IncludeMainDocuments = false
	config.IncludeBoxNotes = false
	config.IncludeStorageRetention = false
	config.IncludeStorageArchive = false
	config.IncludeNotesProjection = false
	config.VerifyAfterWrite = true

	now := time.Date(2026, 8, 28, 15, 0, 0, 0, time.UTC)
	backupDir := filepath.Join(backupRoot, "mixed-backup")
	operation := maintenance.Operation{MaintenanceOperationID: "maintenance_operation_mixed", WorkerInstanceID: "worker_instance_main_backup"}
	run := workers.RunContext{
		Instance: workers.WorkerInstance{OwnerNodeID: "node_main"},
		Run:      workers.WorkerRun{WorkerRunID: "worker_run_mixed"},
	}
	resultRaw, err := runtime.createBackup(context.Background(), run, operation, config, backupDir, now)
	if err != nil {
		t.Fatalf("createBackup mixed custody: %v", err)
	}

	manifest, err := maintenance.ReadBackupManifest(filepath.Join(backupDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Schema != maintenance.BackupManifestSchemaV09 {
		t.Fatalf("historical manifest schema = %q, want %q", manifest.Schema, maintenance.BackupManifestSchemaV09)
	}
	if manifest.Paths.UserBackups != "user-backups" || manifest.Paths.PrivateBackups != "private-backups" || manifest.Paths.PrivateBackupsEvidence != maintenance.PrivateBackupsEvidencePath {
		t.Fatalf("mixed manifest paths = %#v", manifest.Paths)
	}
	verification, err := maintenance.VerifyBackupDirectory(context.Background(), backupDir)
	if err != nil || verification.Status != maintenance.VerificationSucceeded {
		t.Fatalf("VerifyBackupDirectory = %#v err=%v", verification, err)
	}
	for _, pathValue := range []string{
		filepath.Join(backupDir, "user-backups", "macbook", "documents", batchID, "manifest.json"),
		filepath.Join(backupDir, "private-backups", "node_legacy", "private_backup_accepted", "payload.tar"),
		filepath.Join(backupDir, maintenance.PrivateBackupsEvidencePath),
	} {
		if _, err := os.Lstat(pathValue); err != nil {
			t.Fatalf("mixed backup omitted %s: %v", pathValue, err)
		}
	}
	for _, unexpected := range []string{
		filepath.Join(backupDir, "user-backups", "node_legacy"),
		filepath.Join(backupDir, "private-backups", "macbook"),
	} {
		if _, err := os.Lstat(unexpected); !os.IsNotExist(err) {
			t.Fatalf("mixed backup duplicated custody into %s: %v", unexpected, err)
		}
	}

	var result map[string]any
	if err := json.Unmarshal(resultRaw, &result); err != nil {
		t.Fatal(err)
	}
	if result["user_backups_layout"] != string(userBackupsSnapshotMixed) {
		t.Fatalf("result layout = %#v", result["user_backups_layout"])
	}
	canonicalResult, canonicalOK := result["user_backups"].(map[string]any)
	legacyResult, legacyOK := result["private_backups"].(map[string]any)
	if !canonicalOK || !legacyOK || canonicalResult["source_exists"] != true || legacyResult["source_exists"] != true || canonicalResult["file_count"] != float64(1) || legacyResult["file_count"] != float64(1) || legacyResult["total_bytes"] != float64(len("exact retained legacy payload")) {
		t.Fatalf("mixed result summaries = canonical:%#v legacy:%#v", result["user_backups"], result["private_backups"])
	}
	evidenceResult, ok := result["private_backups_evidence"].(map[string]any)
	if !ok || evidenceResult["path"] != maintenance.PrivateBackupsEvidencePath || evidenceResult["sha256"] == "" {
		t.Fatalf("result evidence = %#v", result["private_backups_evidence"])
	}
	seenEvidenceArtifact := false
	for _, input := range recorded {
		if input.ArtifactKind == maintenance.ArtifactKindPrivateBackupsEvidence {
			seenEvidenceArtifact = input.SHA256 != "" && input.SizeBytes != nil && input.URI == fileURI(filepath.Join(backupDir, maintenance.PrivateBackupsEvidencePath))
		}
	}
	if !seenEvidenceArtifact {
		t.Fatalf("maintenance artifacts did not authenticate private backup evidence: %#v", recorded)
	}
}

func TestCreateBackupRejectsMissingRequiredCustodyRootBeforeMutation(t *testing.T) {
	root := t.TempDir()
	runtime := NewMainBackupRuntime(nil, maintenance.Service{}, testHealthService(filepath.Join(root, "data")), "", filepath.Join(root, "data"), filepath.Join(root, "objects"), "test")
	missingRoot := filepath.Join(root, "missing-user-backups")
	backupDir := filepath.Join(root, "backup-root", "must-not-exist")
	config := mainBackupConfig{IncludePrivateBackups: true, UserBackupsRoot: missingRoot}
	_, err := runtime.createBackup(context.Background(), workers.RunContext{}, maintenance.Operation{}, config, backupDir, time.Now())
	if err == nil || !strings.Contains(err.Error(), "required custody root") {
		t.Fatalf("missing custody root error = %v", err)
	}
	if _, statErr := os.Lstat(backupDir); !os.IsNotExist(statErr) {
		t.Fatalf("missing custody root published backup state: %v", statErr)
	}
}

func containsBackupExclusion(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func writeCanonicalMetadataOnlyUserBackupBatch(t *testing.T, batchRoot, node, protectedRoot, batch string) {
	t.Helper()
	modifiedAt := "2026-08-28T12:00:00Z"
	manifest := map[string]any{
		"schema_version":               "storage.watched_root_backup_manifest.v0.7",
		"source_node_id":               "node_test",
		"source_node_key":              node,
		"root_key":                     protectedRoot,
		"local_batch_id":               batch,
		"watched_root_backup_batch_id": "watched_root_backup_batch_test",
		"batch_kind":                   "watched_root_backup",
		"backup_mode":                  "full",
		"status":                       "accepted",
		"item_count":                   1,
		"items": []map[string]any{{
			"watched_root_backup_item_id":  "watched_root_backup_item_test",
			"watched_root_backup_batch_id": "watched_root_backup_batch_test",
			"node_id":                      "node_test",
			"root_key":                     protectedRoot,
			"local_item_ref":               "local_backup_item_test",
			"item_kind":                    "directory",
			"status":                       "accepted",
			"backup_mode":                  "full",
			"relative_path":                ".loom-acceptance",
			"content_hash_uri":             "sha256:" + strings.Repeat("0", 64),
			"size_bytes":                   160,
			"modified_at":                  modifiedAt,
			"metadata": map[string]any{
				"source":                         "loom-node-agent",
				"local_batch_id":                 batch,
				"local_item_id":                  "local_backup_item_test",
				"local_artifact_id":              "",
				"file_transfer_id":               "",
				"file_transfer_storage_entry_id": "",
				"file_transfer_accepted_path":    "",
				"filesystem_observation": map[string]any{
					"kind":               "directory",
					"source_mode":        493,
					"logical_size_bytes": 160,
				},
			},
		}},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(batchRoot, "manifest.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCopyDirIfExistsCopiesRegularFiles(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	dest := filepath.Join(t.TempDir(), "dest")
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "note.md"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	summary, err := copyDirIfExists(source, dest)
	if err != nil {
		t.Fatalf("copyDirIfExists returned error: %v", err)
	}
	if !summary.SourceExists || summary.FileCount != 1 || summary.TotalBytes != 5 {
		t.Fatalf("summary = %#v, want source_exists file_count=1 total_bytes=5", summary)
	}
	if raw, err := os.ReadFile(filepath.Join(dest, "nested", "note.md")); err != nil || string(raw) != "hello" {
		t.Fatalf("copied file = %q, err=%v", string(raw), err)
	}
}

func TestCopyImportsSharedStoreReusesObjectsWithoutMutatingSource(t *testing.T) {
	source := filepath.Join(t.TempDir(), "imports")
	nested := filepath.Join(source, "node", "batch")
	if err := os.MkdirAll(nested, 0o711); err != nil {
		t.Fatal(err)
	}
	modified := time.Date(2026, 8, 28, 13, 14, 15, 123_000_000, time.UTC)
	payload := filepath.Join(nested, "payload.bin")
	if err := os.WriteFile(payload, []byte("immutable Imports payload"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(payload, modified, modified); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "external-sentinel")
	if err := os.WriteFile(external, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(nested, "external-link")); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(payload)
	if err != nil {
		t.Fatal(err)
	}
	beforeChange := portableStatField(before, "Ctim", "Ctimespec")
	beforeLinks := portableStatField(before, "Nlink")

	backupRoot := t.TempDir()
	store := filepath.Join(backupRoot, ".imports-objects")
	writes := 0
	hooks := importsSnapshotHooks{
		availableBytes: func(string) (uint64, error) { return ^uint64(0), nil },
		beforeObjectWrite: func(string) {
			writes++
		},
	}
	destinationOne := filepath.Join(backupRoot, "one", "imports")
	first, err := copyImportsCustodyWithHooks(context.Background(), source, destinationOne, store, maintenance.ImportsBackupPolicyLegacy, maintenance.ImportsSnapshotSharedStore, hooks)
	if err != nil {
		t.Fatalf("first Imports snapshot: %v", err)
	}
	destinationTwo := filepath.Join(backupRoot, "two", "imports")
	second, err := copyImportsCustodyWithHooks(context.Background(), source, destinationTwo, store, maintenance.ImportsBackupPolicyLegacy, maintenance.ImportsSnapshotSharedStore, hooks)
	if err != nil {
		t.Fatalf("second Imports snapshot: %v", err)
	}
	if writes != 1 {
		t.Fatalf("shared object writes = %d, want one initial write and zero repeated writes", writes)
	}
	if first.SharedObjectLinkCount != 1 || second.SharedObjectLinkCount != 1 {
		t.Fatalf("shared object summaries = %#v %#v", first, second)
	}
	after, err := os.Lstat(payload)
	if err != nil {
		t.Fatal(err)
	}
	if before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) || beforeChange != portableStatField(after, "Ctim", "Ctimespec") || beforeLinks != portableStatField(after, "Nlink") {
		t.Fatalf("source metadata changed: before=%v/%v/%s/%s after=%v/%v/%s/%s", before.Mode(), before.ModTime(), beforeChange, beforeLinks, after.Mode(), after.ModTime(), portableStatField(after, "Ctim", "Ctimespec"), portableStatField(after, "Nlink"))
	}
	firstPayload := filepath.Join(destinationOne, "node", "batch", "payload.bin")
	secondPayload := filepath.Join(destinationTwo, "node", "batch", "payload.bin")
	firstInfo, err := os.Lstat(firstPayload)
	if err != nil {
		t.Fatal(err)
	}
	secondInfo, err := os.Lstat(secondPayload)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(after, firstInfo) || !os.SameFile(firstInfo, secondInfo) {
		t.Fatal("snapshots must share a backup-owned object without linking canonical source custody")
	}
	if target, err := os.Readlink(filepath.Join(destinationTwo, "node", "batch", "external-link")); err != nil || target != external {
		t.Fatalf("symlink fidelity = %q err=%v", target, err)
	}
	if got, err := os.ReadFile(external); err != nil || string(got) != "outside" {
		t.Fatalf("external symlink target changed: %q err=%v", got, err)
	}
	if err := maintenance.ValidateImportsBackup(context.Background(), destinationOne, filepath.Join(filepath.Dir(destinationOne), "imports-evidence.json"), maintenance.ImportsBackupPolicyLegacy, maintenance.ImportsSnapshotSharedStore); err != nil {
		t.Fatalf("first portable evidence failed: %v", err)
	}
	if err := os.Remove(payload); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(secondPayload); err != nil || string(got) != "immutable Imports payload" {
		t.Fatalf("snapshot did not retain source-deleted content: %q err=%v", got, err)
	}
}

func TestCopyImportsSharedStoreChecksCapacityBeforeMissingObjectWrite(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "payload.bin"), []byte("payload"), 0o640); err != nil {
		t.Fatal(err)
	}
	backupRoot := t.TempDir()
	writes := 0
	_, err := copyImportsCustodyWithHooks(context.Background(), source, filepath.Join(backupRoot, "dated", "imports"), filepath.Join(backupRoot, ".imports-objects"), maintenance.ImportsBackupPolicyLegacy, maintenance.ImportsSnapshotSharedStore, importsSnapshotHooks{
		availableBytes: func(string) (uint64, error) { return importsStoreSafetyReserveBytes, nil },
		beforeObjectWrite: func(string) {
			writes++
		},
	})
	if err == nil || !strings.Contains(err.Error(), "safety reserve") {
		t.Fatalf("copy error = %v, want bounded capacity failure", err)
	}
	if writes != 0 {
		t.Fatalf("missing object write started despite insufficient capacity: %d", writes)
	}
}

func TestCopyImportsSharedStoreRepeatRequiresSnapshotReserveBeforeMutation(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "payload.bin"), []byte("payload"), 0o640); err != nil {
		t.Fatal(err)
	}
	backupRoot := t.TempDir()
	store := filepath.Join(backupRoot, ".imports-objects")
	firstDestination := filepath.Join(backupRoot, "first", "imports")
	if _, err := copyImportsCustodyWithHooks(context.Background(), source, firstDestination, store, maintenance.ImportsBackupPolicyLegacy, maintenance.ImportsSnapshotSharedStore, importsSnapshotHooks{availableBytes: func(string) (uint64, error) { return ^uint64(0), nil }}); err != nil {
		t.Fatalf("initial shared snapshot: %v", err)
	}
	writes := 0
	secondDestination := filepath.Join(backupRoot, "second", "imports")
	_, err := copyImportsCustodyWithHooks(context.Background(), source, secondDestination, store, maintenance.ImportsBackupPolicyLegacy, maintenance.ImportsSnapshotSharedStore, importsSnapshotHooks{
		availableBytes: func(string) (uint64, error) { return importsStoreSafetyReserveBytes - 1, nil },
		beforeObjectWrite: func(string) {
			writes++
		},
	})
	if err == nil || !strings.Contains(err.Error(), "safety reserve") {
		t.Fatalf("repeat snapshot reserve error = %v", err)
	}
	if writes != 0 {
		t.Fatalf("repeat snapshot attempted shared-object writes: %d", writes)
	}
	for _, pathValue := range []string{secondDestination, filepath.Join(filepath.Dir(secondDestination), "imports-evidence.json")} {
		if _, statErr := os.Lstat(pathValue); !os.IsNotExist(statErr) {
			t.Fatalf("repeat reserve failure mutated %s: %v", pathValue, statErr)
		}
	}
}

func TestCopyImportsSharedStoreChecksAggregateCapacityBeforeAnyWrite(t *testing.T) {
	source := t.TempDir()
	for _, name := range []string{"one.bin", "two.bin"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte("12345678"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	backupRoot := t.TempDir()
	destination := filepath.Join(backupRoot, "dated", "imports")
	store := filepath.Join(backupRoot, ".imports-objects")
	writes := 0
	_, err := copyImportsCustodyWithHooks(context.Background(), source, destination, store, maintenance.ImportsBackupPolicyLegacy, maintenance.ImportsSnapshotSharedStore, importsSnapshotHooks{
		availableBytes: func(string) (uint64, error) { return importsStoreSafetyReserveBytes + 8, nil },
		beforeObjectWrite: func(string) {
			writes++
		},
	})
	if err == nil || !strings.Contains(err.Error(), "all missing objects") {
		t.Fatalf("aggregate capacity error = %v", err)
	}
	if writes != 0 {
		t.Fatalf("object publication began before aggregate capacity passed: %d", writes)
	}
	for _, pathValue := range []string{destination, store} {
		if _, statErr := os.Lstat(pathValue); !os.IsNotExist(statErr) {
			t.Fatalf("aggregate preflight failure mutated %s: %v", pathValue, statErr)
		}
	}
}

func TestCopyImportsSharedStoreRejectsSymlinkedStoreParents(t *testing.T) {
	source := t.TempDir()
	payload := filepath.Join(source, "payload.bin")
	if err := os.WriteFile(payload, []byte("immutable payload"), 0o640); err != nil {
		t.Fatal(err)
	}
	sourceBefore, err := os.Lstat(payload)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("immutable payload"))
	shard := hex.EncodeToString(digest[:])[:2]
	for _, testCase := range []struct {
		name  string
		build func(string, string) error
	}{
		{name: "store", build: func(store, external string) error { return os.Symlink(external, store) }},
		{name: "sha256", build: func(store, external string) error {
			if err := os.Mkdir(store, 0o750); err != nil {
				return err
			}
			return os.Symlink(external, filepath.Join(store, "sha256"))
		}},
		{name: "shard", build: func(store, external string) error {
			if err := os.MkdirAll(filepath.Join(store, "sha256"), 0o750); err != nil {
				return err
			}
			return os.Symlink(external, filepath.Join(store, "sha256", shard))
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			backupRoot := t.TempDir()
			store := filepath.Join(backupRoot, ".imports-objects")
			external := t.TempDir()
			sentinel := filepath.Join(external, "sentinel")
			if err := os.WriteFile(sentinel, []byte("outside"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := testCase.build(store, external); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(backupRoot, "dated", "imports")
			if _, err := copyImportsCustodyWithHooks(context.Background(), source, destination, store, maintenance.ImportsBackupPolicyLegacy, maintenance.ImportsSnapshotSharedStore, importsSnapshotHooks{availableBytes: func(string) (uint64, error) { return ^uint64(0), nil }}); err == nil {
				t.Fatal("symlinked Imports store parent was accepted")
			}
			entries, err := os.ReadDir(external)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != "sentinel" {
				t.Fatalf("external store target changed: %#v", entries)
			}
			if content, err := os.ReadFile(sentinel); err != nil || string(content) != "outside" {
				t.Fatalf("external sentinel changed: %q err=%v", content, err)
			}
			sourceAfter, err := os.Lstat(payload)
			if err != nil || sourceAfter.Mode() != sourceBefore.Mode() || !sourceAfter.ModTime().Equal(sourceBefore.ModTime()) {
				t.Fatalf("source changed after rejected store path: before=%#v after=%#v err=%v", sourceBefore, sourceAfter, err)
			}
		})
	}
}

func TestCopyImportsRejectsSourceInventoryChangesAfterPreflight(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(string) error
	}{
		{name: "removed regular file", mutate: func(root string) error { return os.Remove(filepath.Join(root, "payload.bin")) }},
		{name: "added directory", mutate: func(root string) error { return os.Mkdir(filepath.Join(root, " added directory "), 0o711) }},
		{name: "added symlink", mutate: func(root string) error { return os.Symlink("payload.bin", filepath.Join(root, " added link ")) }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			source := t.TempDir()
			if err := os.WriteFile(filepath.Join(source, "payload.bin"), []byte("payload"), 0o640); err != nil {
				t.Fatal(err)
			}
			backupRoot := t.TempDir()
			destination := filepath.Join(backupRoot, "dated", "imports")
			store := filepath.Join(backupRoot, ".imports-objects")
			_, err := copyImportsCustodyWithHooks(context.Background(), source, destination, store, maintenance.ImportsBackupPolicyLegacy, maintenance.ImportsSnapshotSharedStore, importsSnapshotHooks{
				availableBytes: func(string) (uint64, error) { return ^uint64(0), nil },
				afterSourcePreflight: func() error {
					return testCase.mutate(source)
				},
			})
			if err == nil || !strings.Contains(err.Error(), "does not match preflight custody inventory") {
				t.Fatalf("source inventory change error = %v", err)
			}
			evidencePath := filepath.Join(filepath.Dir(destination), "imports-evidence.json")
			if _, statErr := os.Lstat(evidencePath); !os.IsNotExist(statErr) {
				t.Fatalf("changed source published verified evidence: %v", statErr)
			}
		})
	}
}

func TestCopyCanonicalImportsRejectsNewBatchAfterPreflight(t *testing.T) {
	source := t.TempDir()
	writeEmptyCommittedImportsBatch(t, source, "node", "2026-08-28", "batch-a")
	backupRoot := t.TempDir()
	destination := filepath.Join(backupRoot, "dated", "imports")
	store := filepath.Join(backupRoot, ".imports-objects")
	_, err := copyImportsCustodyWithHooks(context.Background(), source, destination, store, maintenance.ImportsBackupPolicyCanonical, maintenance.ImportsSnapshotSharedStore, importsSnapshotHooks{
		availableBytes: func(string) (uint64, error) { return ^uint64(0), nil },
		afterSourcePreflight: func() error {
			writeEmptyCommittedImportsBatch(t, source, "node", "2026-08-28", "batch-b")
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "batch inventory changed") {
		t.Fatalf("new canonical batch error = %v", err)
	}
	for _, pathValue := range []string{destination, filepath.Join(filepath.Dir(destination), "imports-evidence.json")} {
		if _, statErr := os.Lstat(pathValue); !os.IsNotExist(statErr) {
			t.Fatalf("new canonical batch published destination/evidence %s: %v", pathValue, statErr)
		}
	}
}

func writeEmptyCommittedImportsBatch(t *testing.T, root, node, date, batch string) {
	t.Helper()
	batchRoot := filepath.Join(root, node, date, batch)
	if err := os.MkdirAll(batchRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	entries := []map[string]any{}
	digest := sha256.New()
	if err := json.NewEncoder(digest).Encode(entries); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"schema_version":  "loom.lane.custody_manifest.v1",
		"source_node_key": node,
		"batch_id":        batch,
		"accepted_date":   date,
		"inventory_hash":  "sha256:" + hex.EncodeToString(digest.Sum(nil)),
		"file_count":      0,
		"directory_count": 0,
		"symlink_count":   0,
		"total_bytes":     0,
		"entries":         entries,
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(batchRoot, importsLaneCommitManifest), raw, 0o640); err != nil {
		t.Fatal(err)
	}
}

func TestCopyCanonicalImportsValidatesCommitManifestBeforeAnyWrite(t *testing.T) {
	source := t.TempDir()
	batch := filepath.Join(source, "node", "2026-08-28", "batch")
	if err := os.MkdirAll(batch, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(batch, "payload.bin"), []byte("payload"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(batch, importsLaneCommitManifest), []byte(`{"schema_version":"wrong"}`), 0o640); err != nil {
		t.Fatal(err)
	}
	backupRoot := t.TempDir()
	destination := filepath.Join(backupRoot, "dated", "imports")
	store := filepath.Join(backupRoot, ".imports-objects")
	writes := 0
	_, err := copyImportsCustodyWithHooks(context.Background(), source, destination, store, maintenance.ImportsBackupPolicyCanonical, maintenance.ImportsSnapshotSharedStore, importsSnapshotHooks{
		availableBytes: func(string) (uint64, error) { return ^uint64(0), nil },
		beforeObjectWrite: func(string) {
			writes++
		},
	})
	if err == nil || !strings.Contains(err.Error(), "custody manifest") {
		t.Fatalf("corrupt custody manifest error = %v", err)
	}
	if writes != 0 {
		t.Fatalf("corrupt custody manifest allowed %d object writes", writes)
	}
	for _, pathValue := range []string{destination, store, filepath.Join(backupRoot, "dated", "imports-evidence.json")} {
		if _, statErr := os.Lstat(pathValue); !os.IsNotExist(statErr) {
			t.Fatalf("corrupt custody manifest published %s: %v", pathValue, statErr)
		}
	}
}

func TestCopyCanonicalImportsRejectsAnomaliesBeforeBackupMutation(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		build func(*testing.T, string)
	}{
		{name: "hidden component", build: func(t *testing.T, root string) {
			t.Helper()
			if err := os.Mkdir(filepath.Join(root, ".unexpected"), 0o750); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "uncommitted batch", build: func(t *testing.T, root string) {
			t.Helper()
			batch := filepath.Join(root, "node", "2026-08-28", "batch")
			if err := os.MkdirAll(batch, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(batch, "payload.bin"), []byte("payload"), 0o640); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			source := t.TempDir()
			testCase.build(t, source)
			backupRoot := t.TempDir()
			destination := filepath.Join(backupRoot, "dated", "imports")
			store := filepath.Join(backupRoot, ".imports-objects")
			if _, err := copyImportsCustody(context.Background(), source, destination, store, maintenance.ImportsBackupPolicyCanonical, maintenance.ImportsSnapshotSharedStore); err == nil {
				t.Fatal("canonical Imports anomaly was accepted")
			}
			for _, pathValue := range []string{destination, store} {
				if _, err := os.Lstat(pathValue); !os.IsNotExist(err) {
					t.Fatalf("invalid canonical Imports mutated %s: %v", pathValue, err)
				}
			}
		})
	}
}

func TestCompletePhysicalImportsPolicyCoversMarkerlessAndCommittedMixedCustody(t *testing.T) {
	source := t.TempDir()
	legacyBatch := filepath.Join(source, "macbook", "2026-07-01", "legacy-batch")
	newBatch := filepath.Join(source, "macbook", "2026-08-28", "new-batch")
	for _, batch := range []string{legacyBatch, newBatch} {
		if err := os.MkdirAll(batch, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(legacyBatch, "markerless.bin"), []byte("legacy markerless custody"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newBatch, "payload.bin"), []byte("new committed payload"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newBatch, importsLaneCommitManifest), []byte(`{"schema_version":"loom.lane.custody_manifest.v1"}`), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("markerless.bin", filepath.Join(legacyBatch, "relative-link")); err != nil {
		t.Fatal(err)
	}
	backupRoot := t.TempDir()
	destination := filepath.Join(backupRoot, "dated", "imports")
	store := filepath.Join(backupRoot, ".imports-objects")
	summary, err := copyImportsCustodyWithHooks(context.Background(), source, destination, store, maintenance.ImportsBackupPolicyLegacy, maintenance.ImportsSnapshotSharedStore, importsSnapshotHooks{availableBytes: func(string) (uint64, error) { return ^uint64(0), nil }})
	if err != nil {
		t.Fatalf("complete-physical mixed custody backup: %v", err)
	}
	if summary.FileCount != 3 {
		t.Fatalf("mixed custody file count = %d, want markerless, payload, and commit marker", summary.FileCount)
	}
	for _, relative := range []string{
		filepath.Join("macbook", "2026-07-01", "legacy-batch", "markerless.bin"),
		filepath.Join("macbook", "2026-07-01", "legacy-batch", "relative-link"),
		filepath.Join("macbook", "2026-08-28", "new-batch", "payload.bin"),
		filepath.Join("macbook", "2026-08-28", "new-batch", importsLaneCommitManifest),
	} {
		if _, err := os.Lstat(filepath.Join(destination, relative)); err != nil {
			t.Fatalf("mixed custody omitted %s: %v", relative, err)
		}
	}
	evidencePath := filepath.Join(filepath.Dir(destination), "imports-evidence.json")
	if err := maintenance.ValidateImportsBackup(context.Background(), destination, evidencePath, maintenance.ImportsBackupPolicyLegacy, maintenance.ImportsSnapshotSharedStore); err != nil {
		t.Fatalf("mixed complete-physical custody verification: %v", err)
	}
	if err := os.WriteFile(filepath.Join(destination, "unaccounted.bin"), []byte("unexpected after snapshot"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := maintenance.ValidateImportsBackup(context.Background(), destination, evidencePath, maintenance.ImportsBackupPolicyLegacy, maintenance.ImportsSnapshotSharedStore); err == nil {
		t.Fatal("unaccounted post-snapshot Imports addition passed portable evidence verification")
	}
}

func portableStatField(info os.FileInfo, names ...string) string {
	if info == nil || info.Sys() == nil {
		return ""
	}
	value := reflect.ValueOf(info.Sys())
	if value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return ""
	}
	for _, name := range names {
		field := value.FieldByName(name)
		if field.IsValid() && field.CanInterface() {
			return fmt.Sprint(field.Interface())
		}
	}
	return ""
}

func TestCopyUserBackupsIncludesOnlyCommittedBatches(t *testing.T) {
	source := t.TempDir()
	destination := filepath.Join(t.TempDir(), "user-backups")
	complete := filepath.Join(source, "workspace", "notes", "batch")
	activeBatchID := ids.NewLocalBackupBatchID()
	activeBatch := filepath.Join(source, "workspace", "notes", activeBatchID)
	validPayload := filepath.Join(complete, "payload", "artifacts", ".private_backup_valid.tmp")
	if err := os.MkdirAll(complete, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(activeBatch, "payload"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(validPayload, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCanonicalMetadataOnlyUserBackupBatch(t, complete, "workspace", "notes", "batch")
	if err := os.WriteFile(filepath.Join(activeBatch, "payload", "partial.bin"), []byte("active partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(validPayload, "file.txt"), []byte("user payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := copyCommittedUserBackupBatches(source, destination); err != nil {
		t.Fatalf("copy user backups: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "workspace", "notes", "batch", "manifest.json")); err != nil {
		t.Fatalf("complete custody missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "workspace", "notes", activeBatchID)); !os.IsNotExist(err) {
		t.Fatalf("active uncommitted batch copied: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(destination, "workspace", "notes", "batch", "payload", "artifacts", ".private_backup_valid.tmp", "file.txt")); err != nil || string(got) != "user payload" {
		t.Fatalf("valid payload namespace was skipped: %q err=%v", got, err)
	}
}

func TestClassifyCanonicalUserBackupsRejectsUnknownOrUnsafeNoManifestBatches(t *testing.T) {
	t.Run("direct private-backup writer temp", func(t *testing.T) {
		source := t.TempDir()
		operationID := ids.NewPrivateBackupID()
		batch := filepath.Join(source, "workspace", "notes", "."+operationID+".tmp")
		if err := os.MkdirAll(batch, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(batch, "payload.tar"), []byte("partial payload"), 0o600); err != nil {
			t.Fatal(err)
		}
		plan, err := classifyUserBackupsSnapshot(source, userBackupsSnapshotCanonical)
		if err != nil {
			t.Fatalf("legitimate direct private-backup temp was rejected: %v", err)
		}
		if len(plan.InProgressBatches) != 1 || plan.InProgressBatches[0] != filepath.Join("workspace", "notes", "."+operationID+".tmp") {
			t.Fatalf("direct private-backup temp classification = %#v", plan.InProgressBatches)
		}
	})

	t.Run("arbitrary stable directory", func(t *testing.T) {
		source := t.TempDir()
		pathValue := filepath.Join(source, "workspace", "notes", "active-batch", "payload")
		if err := os.MkdirAll(pathValue, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := classifyUserBackupsSnapshot(source, userBackupsSnapshotCanonical); err == nil || !strings.Contains(err.Error(), "not a valid writer-owned in-progress batch") {
			t.Fatalf("arbitrary no-manifest directory was accepted: %v", err)
		}
	})

	t.Run("nested symlink", func(t *testing.T) {
		source := t.TempDir()
		batch := filepath.Join(source, "workspace", "notes", ids.NewLocalBackupBatchID())
		if err := os.MkdirAll(filepath.Join(batch, "payload"), 0o700); err != nil {
			t.Fatal(err)
		}
		external := filepath.Join(t.TempDir(), "external")
		if err := os.WriteFile(external, []byte("sentinel"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, filepath.Join(batch, "payload", "linked")); err != nil {
			t.Fatal(err)
		}
		if _, err := classifyUserBackupsSnapshot(source, userBackupsSnapshotCanonical); err == nil {
			t.Fatal("symlinked in-progress payload was accepted")
		}
	})

	t.Run("nested special file", func(t *testing.T) {
		source := t.TempDir()
		batch := filepath.Join(source, "workspace", "notes", ids.NewLocalBackupBatchID())
		if err := os.MkdirAll(filepath.Join(batch, "payload"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := unix.Mkfifo(filepath.Join(batch, "payload", "pipe"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := classifyUserBackupsSnapshot(source, userBackupsSnapshotCanonical); err == nil {
			t.Fatal("special in-progress payload was accepted")
		}
	})

	t.Run("malformed private artifact staging", func(t *testing.T) {
		source := t.TempDir()
		batch := filepath.Join(source, "workspace", "notes", ids.NewLocalBackupBatchID())
		if err := os.MkdirAll(filepath.Join(batch, "artifacts", "not-a-private-operation"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := classifyUserBackupsSnapshot(source, userBackupsSnapshotCanonical); err == nil {
			t.Fatal("malformed private-artifact staging was accepted")
		}
	})
}

func TestClassifyUserBackupsAcceptsExistingEmptyRealRoot(t *testing.T) {
	source := t.TempDir()
	plan, err := classifyUserBackupsSnapshot(source, userBackupsSnapshotCanonical)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.SourceExists || plan.Layout != userBackupsSnapshotCanonical || len(plan.CanonicalBatches) != 0 || len(plan.LegacyPayloads) != 0 {
		t.Fatalf("empty real custody root classification = %#v", plan)
	}
}

func TestClassifyAndCopyMixedUserBackupCustodyWithoutDuplication(t *testing.T) {
	source := t.TempDir()
	canonicalBatch := filepath.Join(source, "macbook", "loom_box__documents", "local_backup_batch_test")
	if err := os.MkdirAll(canonicalBatch, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCanonicalMetadataOnlyUserBackupBatch(t, canonicalBatch, "macbook", "loom_box__documents", "local_backup_batch_test")
	legacyPayload := filepath.Join(source, "node_macbook", "private_backup_test", "payload.tar")
	if err := os.MkdirAll(filepath.Dir(legacyPayload), 0o700); err != nil {
		t.Fatal(err)
	}
	legacyBytes := []byte("retained legacy payload")
	if err := os.WriteFile(legacyPayload, legacyBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	plan, err := classifyUserBackupsSnapshot(source, userBackupsSnapshotCanonical)
	if err != nil {
		t.Fatalf("classify mixed custody: %v", err)
	}
	if plan.Layout != userBackupsSnapshotMixed || len(plan.CanonicalBatches) != 1 || len(plan.LegacyPayloads) != 1 {
		t.Fatalf("mixed custody plan = %#v", plan)
	}
	backupRoot := t.TempDir()
	canonicalSummary, legacySummary, err := copyUserBackupsSnapshot(
		source,
		filepath.Join(backupRoot, "user-backups"),
		filepath.Join(backupRoot, "private-backups"),
		plan,
	)
	if err != nil {
		t.Fatalf("copy mixed custody: %v", err)
	}
	if canonicalSummary.FileCount != 1 || legacySummary.FileCount != 1 || legacySummary.TotalBytes != int64(len(legacyBytes)) {
		t.Fatalf("mixed summaries = canonical:%#v legacy:%#v", canonicalSummary, legacySummary)
	}
	if _, err := os.Stat(filepath.Join(backupRoot, "user-backups", "macbook", "loom_box__documents", "local_backup_batch_test", "manifest.json")); err != nil {
		t.Fatalf("canonical custody missing: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(backupRoot, "private-backups", "node_macbook", "private_backup_test", "payload.tar")); err != nil || !reflect.DeepEqual(got, legacyBytes) {
		t.Fatalf("legacy custody = %q err=%v", got, err)
	}
	for _, absent := range []string{
		filepath.Join(backupRoot, "user-backups", "node_macbook"),
		filepath.Join(backupRoot, "private-backups", "macbook"),
	} {
		if _, err := os.Lstat(absent); !os.IsNotExist(err) {
			t.Fatalf("custody subset was duplicated at %s: %v", absent, err)
		}
	}
}

func TestClassifyUserBackupCustodyPreservesCanonicalAndLegacyCompatibility(t *testing.T) {
	t.Run("canonical only", func(t *testing.T) {
		source := t.TempDir()
		batch := filepath.Join(source, "main", "documents", "local_backup_batch_test")
		if err := os.MkdirAll(batch, 0o700); err != nil {
			t.Fatal(err)
		}
		writeCanonicalMetadataOnlyUserBackupBatch(t, batch, "main", "documents", "local_backup_batch_test")
		plan, err := classifyUserBackupsSnapshot(source, userBackupsSnapshotLegacy)
		if err != nil || plan.Layout != userBackupsSnapshotCanonical || len(plan.CanonicalBatches) != 1 || len(plan.LegacyPayloads) != 0 {
			t.Fatalf("canonical plan = %#v err=%v", plan, err)
		}
	})
	t.Run("legacy only", func(t *testing.T) {
		source := t.TempDir()
		payload := filepath.Join(source, "node_main", "private_backup_test", "payload.tar")
		if err := os.MkdirAll(filepath.Dir(payload), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(payload, []byte("payload"), 0o600); err != nil {
			t.Fatal(err)
		}
		plan, err := classifyUserBackupsSnapshot(source, userBackupsSnapshotCanonical)
		if err != nil || plan.Layout != userBackupsSnapshotLegacy || len(plan.CanonicalBatches) != 0 || len(plan.LegacyPayloads) != 1 {
			t.Fatalf("legacy plan = %#v err=%v", plan, err)
		}
	})
}

func TestClassifyUserBackupCustodyRejectsUnclassifiedMalformedAndLinkedComponents(t *testing.T) {
	t.Run("unknown top-level file", func(t *testing.T) {
		source := t.TempDir()
		if err := os.WriteFile(filepath.Join(source, "unknown.bin"), []byte("unknown"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := classifyUserBackupsSnapshot(source, userBackupsSnapshotCanonical); err == nil {
			t.Fatal("unknown top-level file was classified")
		}
	})
	t.Run("empty top-level directory", func(t *testing.T) {
		source := t.TempDir()
		if err := os.Mkdir(filepath.Join(source, "unknown"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := classifyUserBackupsSnapshot(source, userBackupsSnapshotCanonical); err == nil {
			t.Fatal("empty top-level directory was classified")
		}
	})
	t.Run("malformed legacy subtree", func(t *testing.T) {
		source := t.TempDir()
		operation := filepath.Join(source, "node_main", "private_backup_test")
		if err := os.MkdirAll(operation, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(operation, "payload.tar"), []byte("payload"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(operation, "extra"), []byte("unexpected"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := classifyUserBackupsSnapshot(source, userBackupsSnapshotCanonical); err == nil {
			t.Fatal("malformed legacy subtree was classified")
		}
	})
	t.Run("symlinked top-level component", func(t *testing.T) {
		source := t.TempDir()
		external := t.TempDir()
		sentinel := filepath.Join(external, "sentinel")
		if err := os.WriteFile(sentinel, []byte("unchanged"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, filepath.Join(source, "macbook")); err != nil {
			t.Fatal(err)
		}
		if _, err := classifyUserBackupsSnapshot(source, userBackupsSnapshotCanonical); err == nil {
			t.Fatal("symlinked top-level component was classified")
		}
		if got, err := os.ReadFile(sentinel); err != nil || string(got) != "unchanged" {
			t.Fatalf("external sentinel changed: %q err=%v", got, err)
		}
	})
}

func TestCopyUserBackupCustodyRejectsPostClassificationDrift(t *testing.T) {
	source := t.TempDir()
	payload := filepath.Join(source, "node_main", "private_backup_test", "payload.tar")
	if err := os.MkdirAll(filepath.Dir(payload), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := classifyUserBackupsSnapshot(source, userBackupsSnapshotLegacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte("after"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = copyUserBackupsSnapshot(source, filepath.Join(t.TempDir(), "user-backups"), filepath.Join(t.TempDir(), "private-backups"), plan)
	if err == nil || !strings.Contains(err.Error(), "changed after classification") {
		t.Fatalf("post-classification drift error = %v", err)
	}
}

func TestCopyLegacyPrivateBackupsPreservesEveryPayload(t *testing.T) {
	source := t.TempDir()
	destination := filepath.Join(t.TempDir(), "private-backups")
	payloads := map[string]string{
		filepath.Join("node_main", "private_backup_one", "payload.tar"): "first payload",
		filepath.Join("node_main", "private_backup_two", "payload.tar"): "second payload",
	}
	var wantBytes int64
	for relative, payload := range payloads {
		path := filepath.Join(source, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
			t.Fatal(err)
		}
		wantBytes += int64(len(payload))
	}
	summary, err := copyLegacyPrivateBackups(source, destination)
	if err != nil {
		t.Fatalf("copy legacy private backups: %v", err)
	}
	if !summary.SourceExists || summary.FileCount != int64(len(payloads)) || summary.TotalBytes != wantBytes {
		t.Fatalf("legacy copy summary = %#v", summary)
	}
	for relative, want := range payloads {
		got, err := os.ReadFile(filepath.Join(destination, relative))
		if err != nil || string(got) != want {
			t.Fatalf("copied %s = %q err=%v", relative, got, err)
		}
	}
}

func TestCopyLegacyPrivateBackupsRejectsMalformedOrLinkedCustody(t *testing.T) {
	t.Run("unexpected file", func(t *testing.T) {
		source := t.TempDir()
		operation := filepath.Join(source, "node_main", "private_backup_one")
		if err := os.MkdirAll(operation, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(operation, "payload.tar"), []byte("payload"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(operation, "extra"), []byte("unexpected"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := copyLegacyPrivateBackups(source, filepath.Join(t.TempDir(), "copy")); err == nil {
			t.Fatal("malformed legacy custody was accepted")
		}
	})
	t.Run("symlinked payload", func(t *testing.T) {
		source := t.TempDir()
		operation := filepath.Join(source, "node_main", "private_backup_one")
		if err := os.MkdirAll(operation, 0o700); err != nil {
			t.Fatal(err)
		}
		external := filepath.Join(t.TempDir(), "payload.tar")
		if err := os.WriteFile(external, []byte("external"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, filepath.Join(operation, "payload.tar")); err != nil {
			t.Fatal(err)
		}
		if _, err := copyLegacyPrivateBackups(source, filepath.Join(t.TempDir(), "copy")); err == nil {
			t.Fatal("symlinked legacy custody was accepted")
		}
	})
}

func TestCopyStorageArchivesIncludesOnlyCommittedKeys(t *testing.T) {
	source := t.TempDir()
	destination := filepath.Join(t.TempDir(), "storage-archive")
	active := filepath.Join(source, "active-key")
	hidden := filepath.Join(source, ".active-key.preparing")
	locks := filepath.Join(source, ".locks")
	acceptedKeys := []string{"committed-key", "_underscored", "punctuation-2026_08"}
	for _, key := range acceptedKeys {
		directory := filepath.Join(source, key, "objects")
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "payload.bin"), []byte(key), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, key, "manifest.json"), []byte(`{"schema_version":"storage.archive_manifest.v0.7"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, directory := range []string{filepath.Join(active, "objects"), filepath.Join(hidden, "objects"), locks} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(active, "objects", "partial.bin"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hidden, "objects", "partial.bin"), []byte("hidden partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	summary, err := copyCommittedStorageArchives(source, destination)
	if err != nil {
		t.Fatalf("copy storage archives: %v", err)
	}
	if !summary.SourceExists || summary.FileCount != int64(2*len(acceptedKeys)) {
		t.Fatalf("copy summary = %#v, want every committed manifest and payload", summary)
	}
	for _, key := range acceptedKeys {
		if got, err := os.ReadFile(filepath.Join(destination, key, "objects", "payload.bin")); err != nil || string(got) != key {
			t.Fatalf("committed payload for %q = %q err=%v", key, got, err)
		}
	}
	for _, skipped := range []string{"active-key", ".active-key.preparing", ".locks"} {
		if _, err := os.Stat(filepath.Join(destination, skipped)); !os.IsNotExist(err) {
			t.Fatalf("uncommitted archive %s was copied: %v", skipped, err)
		}
	}
}

func TestCopyStorageArchivesRejectsIncompleteLegacyPair(t *testing.T) {
	source := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "objects"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := copyCommittedStorageArchives(source, filepath.Join(t.TempDir(), "storage-archive")); err == nil {
		t.Fatal("incomplete legacy archive pair was silently omitted")
	}
}

func TestCopyDirIfExistsNormalizesBackupCopyPermissions(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	dest := filepath.Join(t.TempDir(), "dest")
	privateDir := filepath.Join(source, "node_private")
	if err := os.MkdirAll(privateDir, 0o700); err != nil {
		t.Fatalf("mkdir private source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(privateDir, "payload.bin"), []byte("secret"), 0o600); err != nil {
		t.Fatalf("write private source file: %v", err)
	}

	summary, err := copyDirIfExists(source, dest)
	if err != nil {
		t.Fatalf("copyDirIfExists returned error: %v", err)
	}
	if !summary.SourceExists || summary.FileCount != 1 || summary.TotalBytes != 6 {
		t.Fatalf("summary = %#v, want source_exists file_count=1 total_bytes=6", summary)
	}

	dirInfo, err := os.Stat(filepath.Join(dest, "node_private"))
	if err != nil {
		t.Fatalf("stat copied private dir: %v", err)
	}
	if got, want := dirInfo.Mode().Perm(), os.FileMode(0o750); got != want {
		t.Fatalf("copied private dir mode = %o, want %o", got, want)
	}
	fileInfo, err := os.Stat(filepath.Join(dest, "node_private", "payload.bin"))
	if err != nil {
		t.Fatalf("stat copied private file: %v", err)
	}
	if got, want := fileInfo.Mode().Perm(), os.FileMode(0o640); got != want {
		t.Fatalf("copied private file mode = %o, want %o", got, want)
	}
}

func TestCopyDirIfExistsCreatesEmptyDestinationForMissingSource(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "dest")
	summary, err := copyDirIfExists(filepath.Join(t.TempDir(), "missing"), dest)
	if err != nil {
		t.Fatalf("copyDirIfExists returned error: %v", err)
	}
	if summary.SourceExists {
		t.Fatalf("source_exists = true, want false")
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("destination missing: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("destination is not a directory")
	}
}

func TestEnsureStorageArchiveBackupLayoutCreatesPhysicalCustodyRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "storage-archive")
	if err := ensureStorageArchiveBackupLayout(root); err != nil {
		t.Fatalf("ensureStorageArchiveBackupLayout returned error: %v", err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		t.Fatalf("archive custody root missing: info=%v err=%v", info, err)
	}
	for _, obsolete := range []string{"objects", "manifests"} {
		if _, err := os.Stat(filepath.Join(root, obsolete)); !os.IsNotExist(err) {
			t.Fatalf("obsolete global archive child %s was created: %v", obsolete, err)
		}
	}
}

func testHealthService(dataDir string) health.Service {
	objectStore := filepath.Join(dataDir, "object-store")
	_ = os.MkdirAll(objectStore, 0o755)
	return health.NewService(config.Config{
		Env:              "test",
		NodeID:           "test-main",
		NodeKind:         "main",
		NodeRole:         "main",
		RuntimeClass:     "main_full",
		DataDir:          dataDir,
		ServiceRoot:      dataDir,
		StorageRoot:      filepath.Join(dataDir, "storage"),
		ImportsRoot:      filepath.Join(dataDir, "imports"),
		UserBackupsRoot:  filepath.Join(dataDir, "user-backups"),
		ArchiveRoot:      filepath.Join(dataDir, "archive"),
		GeneratedRoot:    filepath.Join(dataDir, "generated"),
		BoxPath:          filepath.Join(dataDir, "box"),
		BoxStateRoot:     filepath.Join(dataDir, "state"),
		ObjectStore:      objectStore,
		StorageRetention: filepath.Join(dataDir, "storage-retention"),
		StorageExport:    filepath.Join(dataDir, "storage-export"),
		MainDocuments:    filepath.Join(dataDir, "main-documents"),
		MigrationsDir:    "migrations",
	}, version.Info{Version: "test"})
}

func TestMainBackupHermesEvidenceGatePrecedesDatabaseEffects(t *testing.T) {
	db := &sql.DB{}
	p := hermesprofile.Policy{Enabled: true, Workspace: filepath.Join(t.TempDir(), "morathustra"), PublicKey: make([]byte, 32)}
	runtime := NewMainBackupRuntime(db, maintenance.Service{DB: db}, health.Service{}, "", t.TempDir(), t.TempDir(), "fixture")
	runtime.hermesPolicy = &p
	// An uninitialized *sql.DB would panic on use: missing recovery evidence
	// must be refused before reconciliation, operation creation or a dump.
	_, err := runtime.RunOnce(context.Background(), workers.RunContext{Instance: workers.WorkerInstance{ConfigJSON: runtime.DefaultConfig()}})
	if err == nil {
		t.Fatal("main backup accepted missing Hermes recovery")
	}
}

func TestMainBackupHermesFinalEvidenceGate(t *testing.T) {
	p := hermesprofile.Policy{}
	r := MainBackupRuntime{hermesPolicy: &p}
	if err := r.revalidateHermesRecovery(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := r.revalidateHermesRecovery(context.Background(), []hermesprofile.Evidence{{ID: "previously-enabled"}}); err == nil {
		t.Fatal("policy change during run accepted")
	}
	p.Enabled = true
	if err := r.revalidateHermesRecovery(context.Background(), nil); err == nil {
		t.Fatal("enabled missing trust accepted at completion")
	}
}

func TestMainBackupHermesActivationMatrix(t *testing.T) {
	for _, runtime := range []bool{false, true} {
		for _, recovery := range []bool{false, true} {
			t.Run(fmt.Sprintf("runtime=%t/recovery=%t", runtime, recovery), func(t *testing.T) {
				cfg := config.Config{MorathustraEnabled: runtime, MorathustraRecoveryEnabled: recovery, MorathustraRecoveryPublicKey: strings.Repeat("a", 64)}
				policy, err := cfg.HermesRecoveryPolicy()
				if err != nil {
					t.Fatal(err)
				}
				policy.Workspace = filepath.Join(t.TempDir(), "missing-profile")
				db := &sql.DB{}
				r := NewMainBackupRuntime(db, maintenance.Service{DB: db}, health.Service{}, "", t.TempDir(), t.TempDir(), "fixture")
				r.hermesPolicy = &policy
				evidence, err := r.checkHermesRecovery(context.Background(), time.Now())
				if (err != nil) != recovery || len(evidence) != 0 {
					t.Fatalf("initial gate coupled to runtime: %v %v", evidence, err)
				}
				if err := r.revalidateHermesRecovery(context.Background(), nil); (err != nil) != recovery {
					t.Fatalf("final gate coupled to runtime: %v", err)
				}
				if recovery {
					// An uninitialized DB panics if recovery refusal is bypassed.
					if _, err := r.RunOnce(context.Background(), workers.RunContext{Instance: workers.WorkerInstance{ConfigJSON: r.DefaultConfig()}}); err == nil {
						t.Fatal("missing recovery passed before database effects")
					}
				}
			})
		}
	}
}

func TestMainBackupHermesEnvironmentActivation(t *testing.T) {
	t.Setenv("LOOM_CONFIG_FILE", "")
	t.Setenv("LOOM_MORATHUSTRA_RECOVERY_PUBLIC_KEY", "")
	for _, runtime := range []string{"false", "true"} {
		for _, recovery := range []string{"", "false", "true"} {
			t.Run("runtime="+runtime+"/recovery="+recovery, func(t *testing.T) {
				t.Setenv("LOOM_MORATHUSTRA_ENABLED", runtime)
				t.Setenv("LOOM_MORATHUSTRA_RECOVERY_ENABLED", recovery)
				// Exercise the actual loader without touching a live profile:
				// enabled recovery must stop at missing trust, off must not scan.
				r := MainBackupRuntime{}
				evidence, err := r.checkHermesRecovery(context.Background(), time.Now())
				if recovery == "true" {
					if err == nil || !strings.Contains(err.Error(), "recovery public key") {
						t.Fatalf("missing trust bypassed: %v", err)
					}
				} else if err != nil || len(evidence) != 0 {
					t.Fatalf("runtime-only backup blocked: %v %v", evidence, err)
				}
				if err := r.revalidateHermesRecovery(context.Background(), nil); (err != nil) != (recovery == "true") {
					t.Fatalf("final loader gate: %v", err)
				}
			})
		}
	}
}
