package backupcoverage

import (
	"context"
	"encoding/json"
	"fmt"
	"loom.local/loom/internal/hermesprofile"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/backupstrategy"
	"loom.local/loom/internal/box"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/provenance"
)

func TestMigrationEligibilityCoverageIsCriticalAndNeverGrantsCleanup(t *testing.T) {
	eligible := migrationEligibilityEntry(backupstrategy.MigrationEligibilityReport{
		Schema: backupstrategy.MigrationEligibilitySchema, Status: backupstrategy.MigrationEligibilityEligible,
		Digest: strings.Repeat("a", 64), Candidates: []backupstrategy.MigrationEligibilityComponent{{RelativePath: "legacy"}},
		ExpectedReclaimableBytes: 4096, CleanupApplyAllowed: false,
	})
	if eligible.Status != StatusCovered || !eligible.Critical || !strings.Contains(eligible.Message, "cleanup still requires Slice 6 review") {
		t.Fatalf("eligible coverage entry = %#v", eligible)
	}
	blocked := migrationEligibilityEntry(backupstrategy.MigrationEligibilityReport{
		Schema: backupstrategy.MigrationEligibilitySchema, Status: backupstrategy.MigrationEligibilityBlocked,
		Blockers: []string{"strict_restore_evidence_invalid"}, CleanupApplyAllowed: false,
	})
	if blocked.Status != StatusRequiresDecision || !blocked.Critical {
		t.Fatalf("blocked coverage entry = %#v", blocked)
	}
}

func TestOptionsFromConfigUsesCanonicalBackupAndArchiveRoots(t *testing.T) {
	root := t.TempDir()
	userBackups := filepath.Join(root, "custom", "protected-copies")
	archive := filepath.Join(root, "custom", "archive-custody")
	opts := OptionsFromConfig(config.Config{
		DataDir:         filepath.Join(root, "runtime"),
		UserBackupsRoot: userBackups,
		ArchiveRoot:     archive,
		ServiceRoot:     filepath.Join(root, "service"),
		GeneratedRoot:   filepath.Join(root, "generated"),
	})
	if opts.UserBackupsRoot != userBackups || opts.StorageArchiveRoot != archive {
		t.Fatalf("canonical coverage roots = backups:%q archive:%q", opts.UserBackupsRoot, opts.StorageArchiveRoot)
	}
}

func TestApplicationDataEnrollmentIsNotBackupEvidence(t *testing.T) {
	root := t.TempDir()
	opts := OptionsFromConfig(config.Config{DataDir: root, ApplicationDataBackupRoot: root})
	if opts.ApplicationDataRoot != root {
		t.Fatal("configured root lost")
	}
	report, err := Check(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range report.Entries {
		if entry.Key == "application-data" {
			if entry.Status != StatusUnknown || entry.Policy != "cloud_history" {
				t.Fatal("enrollment claimed a backup", entry)
			}
			return
		}
	}
	t.Fatal("application data absent from coverage")
}

func TestNormalizeOptionsPreservesDeprecatedPrivateBackupRootOverride(t *testing.T) {
	root := t.TempDir()
	opts := normalizeOptions(Options{
		DataDir:            filepath.Join(root, "runtime"),
		UserBackupsRoot:    filepath.Join(root, "canonical"),
		PrivateBackupsRoot: filepath.Join(root, "explicit-legacy-override"),
	})
	if opts.UserBackupsRoot != filepath.Join(root, "explicit-legacy-override") {
		t.Fatalf("legacy explicit override was ignored: %#v", opts)
	}
}

func TestCheckReportsCoveredRootsAndLatestManifest(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	mustMkdir(t, filepath.Join(dataDir, "object-store"))
	importsRoot := filepath.Join(dataDir, "lane", "accepted")
	mustMkdir(t, importsRoot)
	mustMkdir(t, filepath.Join(dataDir, "user-backups"))
	boxRoot := filepath.Join(root, "box")
	mainDocumentsRoot := filepath.Join(boxRoot, "Documents")
	mustWrite(t, filepath.Join(mainDocumentsRoot, "note.md"), []byte("hello"))
	mustWrite(t, filepath.Join(dataDir, "storage-retention", "main-documents", "by-sha256", "payload"), []byte("hello"))
	mustMkdir(t, filepath.Join(dataDir, "storage-archive"))
	boxNotesRoot := filepath.Join(boxRoot, "Notes")
	mustWrite(t, filepath.Join(boxNotesRoot, "source.md"), []byte("source note"))
	mustWrite(t, filepath.Join(dataDir, "generated", "notes", "main", "source.md"), []byte("projected note"))

	backupDir := filepath.Join(dataDir, "backups", "main", "20260614T100000Z-test")
	mustMkdir(t, backupDir)
	if err := os.Chmod(backupDir, 0o700); err != nil {
		t.Fatalf("seal provenance package root: %v", err)
	}
	mustMkdir(t, filepath.Join(backupDir, "storage-retention"))
	mustMkdir(t, filepath.Join(backupDir, "user-backups"))
	mustWrite(t, filepath.Join(backupDir, "imports", "macbook", "2026-06-14", "batch", "payload.bin"), []byte("Imports payload"))
	if _, err := maintenance.WriteImportsBackupEvidence(context.Background(), filepath.Join(backupDir, "imports"), filepath.Join(backupDir, "imports-evidence.json"), maintenance.ImportsBackupPolicyLegacy, maintenance.ImportsSnapshotSharedStore, 1); err != nil {
		t.Fatalf("write Imports evidence fixture: %v", err)
	}
	provenanceDump := filepath.Join(backupDir, "loom_provenance.dump")
	mustWrite(t, provenanceDump, []byte("provenance dump"))
	provenanceHash, provenanceSize, err := maintenance.HashFile(provenanceDump)
	if err != nil {
		t.Fatal(err)
	}
	provenanceCounts := emptyProvenanceCounts()
	manifestPath := filepath.Join(backupDir, "manifest.json")
	writeManifest(t, manifestPath, maintenance.BackupManifest{
		Schema:     maintenance.BackupManifestSchemaV010,
		BackupKind: "loom_main_state",
		CreatedAt:  "2026-06-14T10:00:00Z",
		Source: maintenance.BackupManifestSource{
			Hostname: "loom-main",
			NodeID:   "main",
		},
		Loom: maintenance.BackupManifestLoom{
			CurrentMigration: int64Ptr(1),
			LatestMigration:  int64Ptr(1),
		},
		Database: maintenance.BackupManifestDatabase{
			Name:     "loom_main",
			DumpFile: "loom_main.dump",
		},
		Paths: maintenance.BackupManifestPaths{
			Imports:          "imports",
			ImportsEvidence:  "imports-evidence.json",
			UserBackups:      "user-backups",
			MainDocuments:    "main-documents",
			StorageRetention: "storage-retention",
			StorageArchive:   "storage-archive",
		},
		Artifacts: []maintenance.BackupManifestArtifact{
			{Kind: boxNotesSnapshotArtifactKind, Path: "box-notes"},
			{Kind: maintenance.ArtifactKindProvenanceDump, Path: "loom_provenance.dump", FileCount: int64Ptr(1), SizeBytes: &provenanceSize, SHA256: provenanceHash},
		},
		Policies: maintenance.BackupManifestPolicies{
			MainBox:          "selected_canonical_roots_copied_once",
			NotesSourceRoots: "box_notes_snapshot_and_project_notes_private_backups_or_watched_roots",
			NotesProjection:  "excluded_rebuildable_generated_output",
			KnowledgeIndex:   "captured_by_postgres_dump_as_knowledge_schema_tables",
			Imports:          maintenance.ImportsBackupPolicyLegacy,
			ImportsSnapshot:  maintenance.ImportsSnapshotSharedStore,
		},
		Provenance: &maintenance.BackupManifestProvenance{
			State:             provenance.BackupStateComplete,
			StartedAt:         "2026-06-14T09:59:00Z",
			CompletedAt:       "2026-06-14T10:00:00Z",
			Database:          maintenance.BackupManifestDatabase{Name: provenance.DatabaseName, DumpFile: "loom_provenance.dump", Format: "pg_dump_custom"},
			SchemaHead:        provenance.SchemaHead,
			RequiredRelations: provenance.RecoveryRelations(),
			LogicalCounts:     provenanceCounts,
			GraphDigest:       "sha256:" + strings.Repeat("a", 64),
			DumpSizeBytes:     provenanceSize,
			DumpSHA256:        provenanceHash,
		},
	})
	_, trustedManifestSHA256, _, err := maintenance.ReadBackupManifestWithSHA256(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	report, err := Check(context.Background(), Options{
		DataDir:                         dataDir,
		ImportsRoot:                     importsRoot,
		UserBackupsRoot:                 filepath.Join(dataDir, "user-backups"),
		MainBoxPath:                     boxRoot,
		MainDocumentsRoot:               mainDocumentsRoot,
		BoxNotesRoot:                    boxNotesRoot,
		StorageArchiveRoot:              filepath.Join(dataDir, "storage-archive"),
		TrustedProvenanceManifestSHA256: trustedManifestSHA256,
		Now: func() time.Time {
			return time.Date(2026, 6, 14, 10, 0, 0, 0, time.UTC)
		},
	})
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if report.Status != OverallOK {
		t.Fatalf("status = %q, want %q; critical=%v entries=%#v", report.Status, OverallOK, report.CriticalMissing, report.Entries)
	}
	if report.LatestManifestPath == "" {
		t.Fatal("latest manifest path was not recorded")
	}
}

func TestCheckFailsClosedWhenLatestManifestOmitsOrCorruptsProvenanceRecovery(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("seal provenance package root: %v", err)
	}
	manifestPath := filepath.Join(root, "manifest.json")
	writeManifest(t, manifestPath, maintenance.BackupManifest{
		Schema:     maintenance.BackupManifestSchemaV09,
		BackupKind: "loom_main_state",
	})
	report, err := Check(context.Background(), Options{DataDir: root, ManifestPath: manifestPath})
	if err != nil {
		t.Fatal(err)
	}
	entry := findCoverageEntry(report.Entries, "latest-provenance-ledger")
	if entry.Status != StatusUnknown || !entry.Critical || !strings.Contains(entry.Message, "evidence is unavailable") {
		t.Fatalf("legacy provenance recovery entry = %#v", entry)
	}

	dumpPath := filepath.Join(root, "loom_provenance.dump")
	mustWrite(t, dumpPath, []byte("expected dump"))
	dumpHash, dumpSize, err := maintenance.HashFile(dumpPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest := maintenance.BackupManifest{
		Schema:     maintenance.BackupManifestSchemaV010,
		BackupKind: "loom_main_and_provenance_state",
		Provenance: &maintenance.BackupManifestProvenance{
			State: provenance.BackupStateComplete, StartedAt: "2026-08-29T18:00:00Z", CompletedAt: "2026-08-29T18:01:00Z",
			Database:   maintenance.BackupManifestDatabase{Name: provenance.DatabaseName, DumpFile: "loom_provenance.dump", Format: "pg_dump_custom"},
			SchemaHead: provenance.SchemaHead, RequiredRelations: provenance.RecoveryRelations(), LogicalCounts: emptyProvenanceCounts(),
			GraphDigest: "sha256:" + strings.Repeat("b", 64), DumpSizeBytes: dumpSize, DumpSHA256: dumpHash,
		},
		Artifacts: []maintenance.BackupManifestArtifact{{Kind: maintenance.ArtifactKindProvenanceDump, Path: "loom_provenance.dump", FileCount: int64Ptr(1), SizeBytes: &dumpSize, SHA256: dumpHash}},
	}
	writeManifest(t, manifestPath, manifest)
	_, trustedManifestSHA256, _, err := maintenance.ReadBackupManifestWithSHA256(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	report, err = Check(context.Background(), Options{DataDir: root, ManifestPath: manifestPath})
	if err != nil {
		t.Fatal(err)
	}
	entry = findCoverageEntry(report.Entries, "latest-provenance-ledger")
	if entry.Status == StatusCovered || !entry.Critical || !strings.Contains(entry.Message, "independently retained") || report.Status == OverallOK {
		t.Fatalf("untrusted provenance recovery entry = %#v; report status=%q", entry, report.Status)
	}

	mustWrite(t, dumpPath, []byte("tampered dump"))
	report, err = Check(context.Background(), Options{
		DataDir: root, ManifestPath: manifestPath,
		TrustedProvenanceManifestSHA256: trustedManifestSHA256,
	})
	if err != nil {
		t.Fatal(err)
	}
	entry = findCoverageEntry(report.Entries, "latest-provenance-ledger")
	if entry.Status != StatusMissing || !strings.Contains(entry.Message, "sha256 mismatch") {
		t.Fatalf("tampered provenance recovery entry = %#v", entry)
	}

	replacementHash, replacementSize, err := maintenance.HashFile(dumpPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Provenance.DumpSHA256 = replacementHash
	manifest.Provenance.DumpSizeBytes = replacementSize
	manifest.Artifacts[0].SHA256 = replacementHash
	manifest.Artifacts[0].SizeBytes = &replacementSize
	writeManifest(t, manifestPath, manifest)
	report, err = Check(context.Background(), Options{
		DataDir: root, ManifestPath: manifestPath,
		TrustedProvenanceManifestSHA256: trustedManifestSHA256,
	})
	if err != nil {
		t.Fatal(err)
	}
	entry = findCoverageEntry(report.Entries, "latest-provenance-ledger")
	if entry.Status == StatusCovered || !entry.Critical || !strings.Contains(entry.Message, "manifest identity mismatch") {
		t.Fatalf("coherently replaced provenance recovery package = %#v", entry)
	}
}

func TestCheckUsesIndependentProvenanceOperationEvidenceBesideV09Manifest(t *testing.T) {
	root := t.TempDir()
	legacyManifestPath := filepath.Join(root, "legacy", "manifest.json")
	mustMkdir(t, filepath.Dir(legacyManifestPath))
	writeManifest(t, legacyManifestPath, maintenance.BackupManifest{Schema: maintenance.BackupManifestSchemaV09, BackupKind: "loom_main_state"})

	provenanceDir := filepath.Join(root, "provenance-package")
	mustMkdir(t, provenanceDir)
	if err := os.Chmod(provenanceDir, 0o700); err != nil {
		t.Fatalf("seal provenance package root: %v", err)
	}
	dumpPath := filepath.Join(provenanceDir, provenance.RecoveryDumpFile)
	mustWrite(t, dumpPath, []byte("PGDMP\x01independent provenance evidence"))
	dumpHash, dumpSize, err := maintenance.HashFile(dumpPath)
	if err != nil {
		t.Fatal(err)
	}
	fileCount := int64(1)
	completedAt := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	provenanceManifestPath := filepath.Join(provenanceDir, provenance.RecoveryManifestFile)
	writeManifest(t, provenanceManifestPath, maintenance.BackupManifest{
		Schema: provenance.RecoveryManifestSchema, BackupKind: provenance.RecoveryBackupKind,
		CreatedAt: completedAt.Format(time.RFC3339Nano), Source: maintenance.BackupManifestSource{NodeID: "loom-main"},
		Artifacts: []maintenance.BackupManifestArtifact{{Kind: maintenance.ArtifactKindProvenanceDump, Path: provenance.RecoveryDumpFile, FileCount: &fileCount, SizeBytes: &dumpSize, SHA256: dumpHash}},
		Provenance: &maintenance.BackupManifestProvenance{
			State: provenance.BackupStateComplete, StartedAt: completedAt.Add(-time.Minute).Format(time.RFC3339Nano), CompletedAt: completedAt.Format(time.RFC3339Nano),
			Database:   maintenance.BackupManifestDatabase{Name: provenance.DatabaseName, DumpFile: provenance.RecoveryDumpFile, Format: "pg_dump_custom"},
			SchemaHead: provenance.SchemaHead, RequiredRelations: provenance.RecoveryRelations(), LogicalCounts: emptyProvenanceCounts(),
			GraphDigest: "sha256:" + strings.Repeat("8", 64), DumpSizeBytes: dumpSize, DumpSHA256: dumpHash,
		},
	})
	_, manifestSHA256, _, err := maintenance.ReadBackupManifestWithSHA256(provenanceManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Check(context.Background(), Options{
		DataDir: root, ManifestPath: legacyManifestPath, ProvenanceManifestPath: provenanceManifestPath,
		TrustedProvenanceManifestSHA256: manifestSHA256,
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := findCoverageEntry(report.Entries, "latest-provenance-ledger")
	if entry.Status != StatusCovered || entry.Path != provenance.RecoveryDumpFile || !entry.Critical {
		t.Fatalf("independent provenance evidence = %#v", entry)
	}
	report, err = Check(context.Background(), Options{DataDir: root, ManifestPath: legacyManifestPath, ProvenanceManifestPath: provenanceManifestPath})
	if err != nil {
		t.Fatal(err)
	}
	entry = findCoverageEntry(report.Entries, "latest-provenance-ledger")
	if entry.Status != StatusUnknown || !strings.Contains(entry.Message, "independently retained") {
		t.Fatalf("untrusted provenance evidence = %#v", entry)
	}
}

func emptyProvenanceCounts() map[string]int64 {
	counts := make(map[string]int64, len(provenance.RecoveryRelations()))
	for _, relation := range provenance.RecoveryRelations() {
		counts[relation] = 0
	}
	return counts
}

func TestCheckAcceptsExplicitLegacyPrivateBackupsManifestPath(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "manifest.json")
	writeManifest(t, manifestPath, maintenance.BackupManifest{
		Schema:     maintenance.BackupManifestSchemaV09,
		BackupKind: "loom_main_state",
		Paths: maintenance.BackupManifestPaths{
			PrivateBackups: "private-backups",
		},
	})

	report, err := Check(context.Background(), Options{
		DataDir:      root,
		ManifestPath: manifestPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range report.Entries {
		if entry.Key != "latest-user-backups" {
			continue
		}
		if entry.Status != StatusCovered || entry.Path != "private-backups" {
			t.Fatalf("legacy private-backups manifest coverage = %#v", entry)
		}
		return
	}
	t.Fatal("latest-user-backups coverage entry was not emitted")
}

func TestCheckRequiresBothPathsFromMixedCustodyManifest(t *testing.T) {
	fixture := newMixedCoverageFixture(t)
	report, err := Check(context.Background(), Options{DataDir: fixture.root, ManifestPath: fixture.manifestPath})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"latest-user-backups":                      "user-backups",
		"latest-private-backups":                   "private-backups",
		"latest-private-backups-evidence":          maintenance.PrivateBackupsEvidencePath,
		"latest-private-backups-evidence-artifact": maintenance.PrivateBackupsEvidencePath,
	}
	for _, entry := range report.Entries {
		expectedPath, ok := want[entry.Key]
		if !ok {
			continue
		}
		if entry.Status != StatusCovered || entry.Path != expectedPath {
			t.Fatalf("mixed custody coverage %s = %#v", entry.Key, entry)
		}
		delete(want, entry.Key)
	}
	if len(want) != 0 {
		t.Fatalf("missing mixed custody coverage entries: %#v", want)
	}
	if containsString(report.CriticalMissing, "latest-private-backups-evidence-artifact") {
		t.Fatalf("valid mixed custody evidence was reported missing: %#v", report.Entries)
	}
	if err := maintenance.ValidateUserBackupsCustody(filepath.Join(fixture.root, "user-backups")); err != nil {
		t.Fatalf("canonical custody fixture is not valid: %v", err)
	}
	if err := maintenance.ValidateLegacyPrivateBackupsCustody(filepath.Join(fixture.root, "private-backups")); err != nil {
		t.Fatalf("legacy custody fixture is not valid: %v", err)
	}
}

func TestCheckRejectsInvalidMixedCustodyEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, mixedCoverageFixture)
	}{
		{
			name: "missing evidence",
			mutate: func(t *testing.T, fixture mixedCoverageFixture) {
				if err := os.Remove(fixture.evidencePath); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlinked evidence",
			mutate: func(t *testing.T, fixture mixedCoverageFixture) {
				raw, err := os.ReadFile(fixture.evidencePath)
				if err != nil {
					t.Fatal(err)
				}
				external := filepath.Join(t.TempDir(), "external-evidence.json")
				mustWrite(t, external, raw)
				if err := os.Remove(fixture.evidencePath); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(external, fixture.evidencePath); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "changed evidence bytes",
			mutate: func(t *testing.T, fixture mixedCoverageFixture) {
				raw, err := os.ReadFile(fixture.evidencePath)
				if err != nil {
					t.Fatal(err)
				}
				raw[len(raw)-1] ^= 1
				mustWrite(t, fixture.evidencePath, raw)
			},
		},
		{
			name: "same size legacy payload rewrite",
			mutate: func(t *testing.T, fixture mixedCoverageFixture) {
				raw, err := os.ReadFile(fixture.legacyPayloadPath)
				if err != nil {
					t.Fatal(err)
				}
				raw[0] ^= 1
				mustWrite(t, fixture.legacyPayloadPath, raw)
			},
		},
		{
			name: "path escape",
			mutate: func(t *testing.T, fixture mixedCoverageFixture) {
				fixture.manifest.Paths.PrivateBackupsEvidence = "../private-backups-evidence.json"
				fixture.manifest.Artifacts[2].Path = "../private-backups-evidence.json"
				writeManifest(t, fixture.manifestPath, fixture.manifest)
			},
		},
		{
			name: "missing artifact hash",
			mutate: func(t *testing.T, fixture mixedCoverageFixture) {
				fixture.manifest.Artifacts[2].SHA256 = ""
				writeManifest(t, fixture.manifestPath, fixture.manifest)
			},
		},
		{
			name: "duplicate evidence artifact",
			mutate: func(t *testing.T, fixture mixedCoverageFixture) {
				fixture.manifest.Artifacts = append(fixture.manifest.Artifacts, fixture.manifest.Artifacts[2])
				writeManifest(t, fixture.manifestPath, fixture.manifest)
			},
		},
		{
			name: "oversized evidence declaration",
			mutate: func(t *testing.T, fixture mixedCoverageFixture) {
				size := int64(maxPrivateBackupsCoverageEvidenceBytes + 1)
				fixture.manifest.Artifacts[2].SizeBytes = &size
				writeManifest(t, fixture.manifestPath, fixture.manifest)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMixedCoverageFixture(t)
			test.mutate(t, fixture)
			report, err := Check(context.Background(), Options{DataDir: fixture.root, ManifestPath: fixture.manifestPath})
			if err != nil {
				t.Fatal(err)
			}
			entry := findCoverageEntry(report.Entries, "latest-private-backups-evidence-artifact")
			if entry.Status != StatusMissing || !entry.Critical || entry.Message == "" {
				t.Fatalf("invalid mixed evidence did not fail closed: %#v", entry)
			}
			if !containsString(report.CriticalMissing, entry.Key) {
				t.Fatalf("invalid mixed evidence was not critical: %#v", report.CriticalMissing)
			}
		})
	}
}

func TestCheckReportsCriticalMissingDurableRoots(t *testing.T) {
	dataDir := t.TempDir()
	report, err := Check(context.Background(), Options{DataDir: dataDir})
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if report.Status != OverallCritical {
		t.Fatalf("status = %q, want %q", report.Status, OverallCritical)
	}
	if len(report.CriticalMissing) == 0 {
		t.Fatal("expected critical missing entries")
	}
}

func TestPlanContractsFromReportSummarizesCoverageContracts(t *testing.T) {
	report := Report{
		SchemaVersion: SchemaVersion,
		Mode:          ModeMainBacked,
		Status:        OverallWarning,
		GeneratedAt:   time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC),
		Entries: []Entry{
			{Key: "postgres", Label: "PostgreSQL", Category: "runtime_config", Status: StatusCovered, Critical: true},
			{Key: "storage-export", Label: "generated storage export", Category: "excluded_view", Status: StatusExcludedExplicitly, Policy: "rebuild_from_canonical_roots"},
			{Key: "storage-retention", Label: "storage-retention", Category: "runtime_root", Status: StatusMissing, Critical: true},
		},
	}
	plan := PlanContractsFromReport(report)
	if plan.Status != OverallCritical {
		t.Fatalf("status = %q, want %q", plan.Status, OverallCritical)
	}
	if plan.Mode != ModeMainBacked || plan.Summary.Satisfied != 2 || plan.Summary.NeedsAttention != 1 || plan.Summary.Critical != 1 {
		t.Fatalf("unexpected contract summary: %#v", plan)
	}
	export := findBackupContract(plan.Contracts, "storage-export")
	if export.ContractStatus != "satisfied" || export.Action != "keep exclusion documented" {
		t.Fatalf("storage export exclusion should be a satisfied contract: %#v", export)
	}
	retention := findBackupContract(plan.Contracts, "storage-retention")
	if retention.ContractStatus != "needs_attention" || retention.Action == "" {
		t.Fatalf("missing retention should need attention with action: %#v", retention)
	}
}

func TestCheckReportsStandaloneBackupContractStates(t *testing.T) {
	root := t.TempDir()
	boxRoot := initializedCoverageBox(t, filepath.Join(root, "box"))
	activeTarget := filepath.Join(root, "active-target")
	staleTarget := filepath.Join(root, "stale-target")
	disabledTarget := filepath.Join(root, "disabled-target")
	mustMkdir(t, activeTarget)
	createBackupContractFixture(t, boxRoot, backupcontracts.Contract{
		Key:         "active-path",
		DisplayName: "Active Path",
		OwnerNode:   "main",
		Target:      backupcontracts.TargetSpec{Scope: backupcontracts.TargetScopeOwnerNodeAbsolute, Path: activeTarget},
	})
	createBackupContractFixture(t, boxRoot, backupcontracts.Contract{
		Key:       "stale-path",
		OwnerNode: "main",
		Target:    backupcontracts.TargetSpec{Scope: backupcontracts.TargetScopeOwnerNodeAbsolute, Path: staleTarget},
	})
	createBackupContractFixture(t, boxRoot, backupcontracts.Contract{
		Key:       "disabled-path",
		OwnerNode: "main",
		Target:    backupcontracts.TargetSpec{Scope: backupcontracts.TargetScopeOwnerNodeAbsolute, Path: disabledTarget},
	})
	if _, err := backupcontracts.Disable(backupcontracts.DisableInput{
		BoxRoot:          boxRoot,
		DirectoryRelPath: backupcontracts.DefaultDirectoryRelPath,
		Key:              "disabled-path",
		Actor:            "test",
	}); err != nil {
		t.Fatalf("disable backup contract fixture: %v", err)
	}
	mustWrite(t, filepath.Join(boxRoot, ".loom", "contracts", "backup", "bad-contract.yaml"), []byte("schema_version: wrong\nkey: bad-contract\n"))

	report, err := Check(context.Background(), Options{
		DataDir:     filepath.Join(root, "data"),
		MainBoxPath: boxRoot,
	})
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	active := findCoverageEntry(report.Entries, "backup-contract-active-path")
	if active.Status != StatusCovered || active.Category != "standalone_backup_contract" || active.Path != activeTarget {
		t.Fatalf("active contract entry = %#v", active)
	}
	stale := findCoverageEntry(report.Entries, "backup-contract-stale-path")
	if stale.Status != StatusMissing || !stale.Critical || !strings.Contains(stale.Message, "stale") {
		t.Fatalf("stale contract entry = %#v", stale)
	}
	if !containsString(report.CriticalMissing, "backup-contract-stale-path") {
		t.Fatalf("stale contract should be critical missing: %#v", report.CriticalMissing)
	}
	disabled := findCoverageEntry(report.Entries, "backup-contract-disabled-path")
	if disabled.Status != StatusExcludedExplicitly || disabled.Critical {
		t.Fatalf("disabled contract entry = %#v", disabled)
	}
	invalid := findCoverageEntry(report.Entries, "backup-contract-bad-contract")
	if invalid.Status != StatusUnknown || invalid.Policy != "invalid_contract_yaml" {
		t.Fatalf("invalid contract entry = %#v", invalid)
	}

	plan := PlanContractsFromReport(report)
	if contract := findBackupContract(plan.Contracts, "backup-contract-stale-path"); contract.ContractStatus != "needs_attention" || !strings.Contains(contract.Action, "disable/delete") {
		t.Fatalf("stale contract plan = %#v", contract)
	}
	if contract := findBackupContract(plan.Contracts, "backup-contract-disabled-path"); contract.ContractStatus != "satisfied" {
		t.Fatalf("disabled contract plan = %#v", contract)
	}
}

func TestCheckReportsPermissionBlockedAsUnknownNotCriticalMissing(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-denied coverage semantics require non-root test user")
	}
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	for _, dir := range []string{
		"object-store",
		filepath.Join("lane", "accepted"),
		"user-backups",
		"main-documents",
		"box-notes",
		"storage-retention",
		"storage-archive",
		filepath.Join("generated", "notes"),
	} {
		mustMkdir(t, filepath.Join(dataDir, dir))
	}
	userBackups := filepath.Join(dataDir, "user-backups")
	if err := os.Chmod(userBackups, 0); err != nil {
		t.Fatalf("chmod private backups: %v", err)
	}
	defer func() {
		_ = os.Chmod(userBackups, 0o755)
	}()

	report, err := Check(context.Background(), Options{
		DataDir:            dataDir,
		ImportsRoot:        filepath.Join(dataDir, "lane", "accepted"),
		UserBackupsRoot:    userBackups,
		StorageArchiveRoot: filepath.Join(dataDir, "storage-archive"),
		MainBoxPath:        filepath.Join(dataDir, "box"),
		MainDocumentsRoot:  filepath.Join(dataDir, "main-documents"),
		BoxNotesRoot:       filepath.Join(dataDir, "box-notes"),
	})
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	entry := findCoverageEntry(report.Entries, "user-backups")
	if entry.Status != StatusUnknown {
		t.Fatalf("user-backups status = %q, want %q", entry.Status, StatusUnknown)
	}
	if entry.Policy != "permission_blocked" || !strings.Contains(entry.Message, "permission blocked") {
		t.Fatalf("user-backups permission detail missing: %#v", entry)
	}
	if containsString(report.CriticalMissing, "user-backups") {
		t.Fatalf("permission-blocked user backups should not be critical missing: %#v", report.CriticalMissing)
	}
	if report.Status != OverallWarning {
		t.Fatalf("status = %q, want %q", report.Status, OverallWarning)
	}
}

func initializedCoverageBox(t *testing.T, root string) string {
	t.Helper()
	if _, err := box.Init(box.InitInput{Resolved: box.Resolved{
		RootPath:  root,
		Profile:   box.ProfileMain,
		OwnerNode: "main",
		NodeRole:  "main",
	}}); err != nil {
		t.Fatalf("initialize Box: %v", err)
	}
	return root
}

func createBackupContractFixture(t *testing.T, boxRoot string, contract backupcontracts.Contract) {
	t.Helper()
	if _, err := backupcontracts.Create(backupcontracts.MutateInput{
		BoxRoot:          boxRoot,
		DirectoryRelPath: backupcontracts.DefaultDirectoryRelPath,
		Contract:         contract,
		Actor:            "test",
	}); err != nil {
		t.Fatalf("create backup contract fixture %s: %v", contract.Key, err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWrite(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeManifest(t *testing.T, path string, manifest maintenance.BackupManifest) {
	t.Helper()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	mustWrite(t, path, raw)
}

func int64Ptr(value int64) *int64 {
	return &value
}

func findBackupContract(contracts []BackupContract, key string) BackupContract {
	for _, contract := range contracts {
		if contract.Key == key {
			return contract
		}
	}
	return BackupContract{}
}

func findCoverageEntry(entries []Entry, key string) Entry {
	for _, entry := range entries {
		if entry.Key == key {
			return entry
		}
	}
	return Entry{}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type mixedCoverageFixture struct {
	root              string
	manifestPath      string
	evidencePath      string
	legacyPayloadPath string
	manifest          maintenance.BackupManifest
}

func newMixedCoverageFixture(t *testing.T) mixedCoverageFixture {
	t.Helper()
	root := t.TempDir()
	userBackups := filepath.Join(root, "user-backups")
	canonicalPayload := filepath.Join(userBackups, "macbook", "documents", "local_backup_batch_test", "payload", "note.md")
	mustWrite(t, canonicalPayload, []byte("canonical watched-root payload"))
	canonicalHash, canonicalSize, err := maintenance.HashFile(canonicalPayload)
	if err != nil {
		t.Fatal(err)
	}
	canonicalManifest := map[string]any{
		"schema_version":               "storage.watched_root_backup_manifest.v0.7",
		"source_node_id":               "node_test",
		"source_node_key":              "macbook",
		"root_key":                     "documents",
		"local_batch_id":               "local_backup_batch_test",
		"watched_root_backup_batch_id": "watched_root_backup_batch_test",
		"batch_kind":                   "watched_root_backup",
		"backup_mode":                  "incremental_raw",
		"status":                       "accepted",
		"item_count":                   1,
		"items": []map[string]any{{
			"watched_root_backup_item_id":  "watched_root_backup_item_test",
			"watched_root_backup_batch_id": "watched_root_backup_batch_test",
			"node_id":                      "node_test",
			"root_key":                     "documents",
			"local_item_ref":               "local_backup_item_test",
			"item_kind":                    "file",
			"status":                       "accepted",
			"backup_mode":                  "incremental_raw",
			"relative_path":                "note.md",
			"content_hash_uri":             "sha256:" + canonicalHash,
			"size_bytes":                   canonicalSize,
			"artifact_kind":                "file_transfer",
			"artifact_ref":                 "file_transfer_test",
		}},
	}
	raw, err := json.Marshal(canonicalManifest)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(userBackups, "macbook", "documents", "local_backup_batch_test", "manifest.json"), raw)
	if err := maintenance.ValidateUserBackupsCustody(userBackups); err != nil {
		t.Fatalf("build canonical custody fixture: %v", err)
	}

	privateBackups := filepath.Join(root, "private-backups")
	legacyPayload := filepath.Join(privateBackups, "node_macbook", "private_backup_test", "payload.tar")
	mustWrite(t, legacyPayload, []byte("retained legacy payload"))
	if err := maintenance.ValidateLegacyPrivateBackupsCustody(privateBackups); err != nil {
		t.Fatalf("build legacy custody fixture: %v", err)
	}
	evidencePath := filepath.Join(root, maintenance.PrivateBackupsEvidencePath)
	if _, err := maintenance.WritePrivateBackupsEvidence(context.Background(), privateBackups, "private-backups", evidencePath); err != nil {
		t.Fatalf("write private-backups evidence: %v", err)
	}
	evidenceHash, evidenceSize, err := maintenance.HashFile(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	userFiles, userBytes, err := maintenance.DirectoryInventory(userBackups)
	if err != nil {
		t.Fatal(err)
	}
	privateFiles, privateBytes, err := maintenance.DirectoryInventory(privateBackups)
	if err != nil {
		t.Fatal(err)
	}
	manifest := maintenance.BackupManifest{
		Schema:     maintenance.BackupManifestSchemaV09,
		BackupKind: "loom_main_state",
		Paths: maintenance.BackupManifestPaths{
			UserBackups:            "user-backups",
			PrivateBackups:         "private-backups",
			PrivateBackupsEvidence: maintenance.PrivateBackupsEvidencePath,
		},
		Artifacts: []maintenance.BackupManifestArtifact{
			{Kind: maintenance.ArtifactKindPrivateBackupsSnapshot, Path: "user-backups", FileCount: &userFiles, SizeBytes: &userBytes},
			{Kind: maintenance.ArtifactKindPrivateBackupsSnapshot, Path: "private-backups", FileCount: &privateFiles, SizeBytes: &privateBytes},
			{Kind: maintenance.ArtifactKindPrivateBackupsEvidence, Path: maintenance.PrivateBackupsEvidencePath, SizeBytes: &evidenceSize, SHA256: evidenceHash},
		},
	}
	manifestPath := filepath.Join(root, "manifest.json")
	writeManifest(t, manifestPath, manifest)
	return mixedCoverageFixture{
		root:              root,
		manifestPath:      manifestPath,
		evidencePath:      evidencePath,
		legacyPayloadPath: legacyPayload,
		manifest:          manifest,
	}
}

func TestHermesCoverageFailsClosedForEnabledProfile(t *testing.T) {
	opts := Options{Mode: ModeMainBacked, HermesRecovery: hermesprofile.Policy{Enabled: true, Workspace: filepath.Join(t.TempDir(), "morathustra"), PublicKey: make([]byte, 32)}}
	report, err := Check(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range report.Entries {
		if entry.Key == "morathustra-recovery" {
			found = true
			if !entry.Critical || entry.Status != StatusMissing {
				t.Fatalf("unproven profile covered: %#v", entry)
			}
		}
	}
	if !found || !slices.Contains(report.CriticalMissing, "morathustra-recovery") {
		t.Fatal("missing critical Hermes recovery not exposed")
	}
	opts.HermesRecovery.Enabled = false
	report, err = Check(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range report.Entries {
		if entry.Key == "morathustra-recovery" {
			t.Fatal("disabled profile changed coverage")
		}
	}
}

func TestHermesCoverageActivationMatrix(t *testing.T) {
	for _, runtime := range []bool{false, true} {
		for _, recovery := range []bool{false, true} {
			t.Run(fmt.Sprintf("runtime=%t/recovery=%t", runtime, recovery), func(t *testing.T) {
				cfg := config.Config{MorathustraEnabled: runtime, MorathustraRecoveryEnabled: recovery, MorathustraRecoveryPublicKey: strings.Repeat("a", 64), DataDir: t.TempDir(), ServiceRoot: t.TempDir(), StorageRoot: t.TempDir(), BoxPath: t.TempDir()}
				opts := OptionsFromConfig(cfg)
				if opts.HermesPolicyError != nil || opts.HermesRecovery.Enabled != recovery {
					t.Fatal("coverage ignored recovery configuration")
				}
				// No production namespace is inspected, even on an enabled case.
				opts.HermesRecovery.Workspace = filepath.Join(t.TempDir(), "missing-profile")
				opts.Mode = ModeMainBacked
				report, err := Check(context.Background(), opts)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, entry := range report.Entries {
					if entry.Key == "morathustra-recovery" {
						found = true
						if !recovery || !entry.Critical || entry.Status != StatusMissing {
							t.Fatalf("unproven recovery claim: %#v", entry)
						}
					}
				}
				if found != recovery || slices.Contains(report.CriticalMissing, "morathustra-recovery") != recovery {
					t.Fatal("coverage gate coupled to runtime")
				}
				// Both archive formats share this boundary before source enumeration.
				// Runtime-only bootstrap preserves the complete live/recovery exclusions.
				exclusions, evidence, err := backupstrategy.HermesArchiveBoundary(context.Background(), []backupstrategy.DirectArchiveRoot{{Name: "agents", Path: opts.HermesRecovery.Workspace}}, nil, opts.HermesRecovery, time.Now())
				if recovery {
					if err == nil {
						t.Fatal("cloud boundary accepted absent recovery")
					}
				} else {
					want := []backupstrategy.DirectArchiveExclusion{{Root: "agents", RelativePath: ".hermes"}, {Root: "agents", RelativePath: "recovery"}}
					if err != nil || len(evidence) != 0 || !slices.Equal(exclusions, want) {
						t.Fatalf("bootstrap cloud boundary changed: %v %v %v", exclusions, evidence, err)
					}
				}
			})
		}
	}
}

func TestHermesCoverageInvalidPublicTrust(t *testing.T) {
	for _, runtime := range []bool{false, true} {
		cfg := config.Config{MorathustraEnabled: runtime, MorathustraRecoveryEnabled: true, DataDir: t.TempDir(), ServiceRoot: t.TempDir(), StorageRoot: t.TempDir(), BoxPath: t.TempDir()}
		opts := OptionsFromConfig(cfg)
		if opts.HermesPolicyError == nil {
			t.Fatal("missing recovery trust ignored")
		}
		opts.Mode = ModeMainBacked
		report, err := Check(context.Background(), opts)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(report.CriticalMissing, "morathustra-recovery") {
			t.Fatal("invalid recovery trust not reported")
		}
	}
}

func TestMinaRecoveryCoverageUsesSelectedProducer(t *testing.T) {
	opts := Options{Mode: ModeMainBacked, HermesRecovery: hermesprofile.Policy{Enabled: true, Identity: hermesprofile.MinaIdentity, Workspace: filepath.Join(t.TempDir(), ".loom-acceptance", "missing"), PublicKey: make([]byte, 32)}}
	report, err := Check(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range report.Entries {
		if entry.Key == "morathustra-recovery" {
			t.Fatal("selected MINA presented as old producer")
		}
		if entry.Key == "mina-recovery" {
			found = true
			if entry.Label != "MINA Hermes recovery" || entry.Status != StatusMissing || !entry.Critical {
				t.Fatalf("%+v", entry)
			}
		}
	}
	if !found || !slices.Contains(report.CriticalMissing, "mina-recovery") {
		t.Fatal("MINA missing evidence hidden")
	}
}
