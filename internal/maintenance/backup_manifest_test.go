package maintenance

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/backupstrategy"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/watchedroots"
)

func TestVerifyOperationalBackupPackageAdaptsExactVerifiedPackage(t *testing.T) {
	packageResult, secret := validOperationalPackageForMaintenance(t, 61)
	verification, err := VerifyOperationalBackupPackage(context.Background(), packageResult.PackageDir, packageResult.ManifestSHA256, 61, []string{secret})
	if err != nil {
		t.Fatal(err)
	}
	if verification.Status != VerificationSucceeded || verification.ManifestSchema != OperationalBackupManifestSchema {
		t.Fatalf("verification = %#v, want operational success", verification)
	}
	if verification.CurrentMigration == nil || *verification.CurrentMigration != 61 || verification.LatestMigration == nil || *verification.LatestMigration != 61 {
		t.Fatalf("migration binding = current=%v latest=%v", verification.CurrentMigration, verification.LatestMigration)
	}
	if verification.Checks["operational_manifest_identity"] != VerificationSucceeded || verification.VerifiedArtifactNum != 7 {
		t.Fatalf("maintenance adapter checks = %#v", verification)
	}

	wrong, err := VerifyOperationalBackupPackage(context.Background(), packageResult.PackageDir, strings.Repeat("0", 64), 61, []string{secret})
	if err != nil {
		t.Fatal(err)
	}
	if wrong.Status != VerificationFailed || wrong.Checks["operational:"+backupstrategy.OperationalFindingManifestIdentityMismatch] != VerificationFailed {
		t.Fatalf("wrong identity verification = %#v", wrong)
	}

	missingIdentity, err := VerifyOperationalBackupPackage(context.Background(), packageResult.PackageDir, "", 61, nil)
	if err != nil {
		t.Fatal(err)
	}
	if missingIdentity.Status != VerificationFailed || missingIdentity.Checks["operational_manifest_identity"] != VerificationFailed {
		t.Fatalf("missing identity verification = %#v", missingIdentity)
	}
}

func TestVerifyBackupDirectoryAcceptsV01Manifest(t *testing.T) {
	dir := validBackupDir(t, BackupManifestSchemaV01)

	verification, err := VerifyBackupDirectory(context.Background(), dir)
	if err != nil {
		t.Fatalf("VerifyBackupDirectory returned error: %v", err)
	}
	if verification.Status != VerificationSucceeded {
		t.Fatalf("verification status = %q, want %q; errors=%v", verification.Status, VerificationSucceeded, verification.Errors)
	}
	if verification.ManifestSchema != BackupManifestSchemaV01 {
		t.Fatalf("manifest schema = %q, want %q", verification.ManifestSchema, BackupManifestSchemaV01)
	}
	for _, check := range []string{"manifest", "source", "migrations", "health_report", "postgres_dump", "object_store_snapshot", "private_backups_snapshot"} {
		if verification.Checks[check] != VerificationSucceeded {
			t.Fatalf("check %q = %q, want succeeded", check, verification.Checks[check])
		}
	}
}

func TestReadBackupManifestRequiresBoundedNoFollowStableFile(t *testing.T) {
	dir := validBackupDir(t, BackupManifestSchemaV01)
	manifestPath := filepath.Join(dir, "manifest.json")
	manifest, digest, size, err := ReadBackupManifestWithSHA256(manifestPath)
	if err != nil || manifest.Schema != BackupManifestSchemaV01 || len(digest) != 64 || size <= 0 {
		t.Fatalf("bounded manifest read = %#v digest=%q size=%d err=%v", manifest, digest, size, err)
	}
	external := filepath.Join(t.TempDir(), "manifest.json")
	payload, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(external, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(manifestPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, manifestPath); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := ReadBackupManifestWithSHA256(manifestPath); err == nil || !strings.Contains(err.Error(), "no-follow regular file") {
		t.Fatalf("symlinked manifest error = %v", err)
	}
	verification, err := VerifyBackupDirectory(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if verification.Status != VerificationFailed || verification.Checks["manifest"] != VerificationFailed {
		t.Fatalf("symlinked manifest verification = %#v", verification)
	}
}

func TestVerifyBackupDirectoryAcceptsV02ManifestWithArtifacts(t *testing.T) {
	dir := validBackupDir(t, BackupManifestSchemaV02)
	dumpPath := filepath.Join(dir, "loom_main.dump")
	hash, size, err := HashFile(dumpPath)
	if err != nil {
		t.Fatalf("HashFile failed: %v", err)
	}
	manifest := readManifestForTest(t, filepath.Join(dir, "manifest.json"))
	manifest.Artifacts = []BackupManifestArtifact{
		{
			Kind:      ArtifactKindPostgresDump,
			Path:      "loom_main.dump",
			SizeBytes: &size,
			SHA256:    hash,
		},
	}
	writeJSON(t, filepath.Join(dir, "manifest.json"), manifest)

	verification, err := VerifyBackupDirectory(context.Background(), dir)
	if err != nil {
		t.Fatalf("VerifyBackupDirectory returned error: %v", err)
	}
	if verification.Status != VerificationSucceeded {
		t.Fatalf("verification status = %q, want %q; errors=%v", verification.Status, VerificationSucceeded, verification.Errors)
	}
	if verification.VerifiedArtifactNum < 3 {
		t.Fatalf("verified artifact count = %d, want at least 3", verification.VerifiedArtifactNum)
	}
}

func TestVerifyBackupDirectoryAcceptsV051ManifestWithRedactedSnapshots(t *testing.T) {
	dir := validBackupDir(t, BackupManifestSchemaV051)
	mustWrite(t, filepath.Join(dir, "install.yaml"), []byte("profile: main_full\n"))
	mustWrite(t, filepath.Join(dir, "loom.env.redacted"), []byte("LOOM_DB_URL=[REDACTED]\nLOOM_ENV=production\n"))
	manifest := readManifestForTest(t, filepath.Join(dir, "manifest.json"))
	manifest.Paths.InstallManifest = "install.yaml"
	manifest.Paths.ServiceEnvRedacted = "loom.env.redacted"
	manifest.Loom.SourceCommit = "commit_test"
	manifest.System.NixGeneration = "/nix/store/system-test"
	manifest.Retention = BackupManifestRetention{Mode: "disabled_for_slice_03", KeepDaily: 14}
	writeJSON(t, filepath.Join(dir, "manifest.json"), manifest)

	verification, err := VerifyBackupDirectory(context.Background(), dir)
	if err != nil {
		t.Fatalf("VerifyBackupDirectory returned error: %v", err)
	}
	if verification.Status != VerificationSucceeded {
		t.Fatalf("verification status = %q, want %q; errors=%v", verification.Status, VerificationSucceeded, verification.Errors)
	}
	for _, check := range []string{"install_manifest_snapshot", "service_env_redacted_snapshot"} {
		if verification.Checks[check] != VerificationSucceeded {
			t.Fatalf("check %q = %q, want succeeded", check, verification.Checks[check])
		}
	}
}

func TestVerifyBackupDirectoryAcceptsV063Manifest(t *testing.T) {
	dir := validBackupDir(t, BackupManifestSchemaV063)

	verification, err := VerifyBackupDirectory(context.Background(), dir)
	if err != nil {
		t.Fatalf("VerifyBackupDirectory returned error: %v", err)
	}
	if verification.Status != VerificationSucceeded {
		t.Fatalf("verification status = %q, want %q; errors=%v", verification.Status, VerificationSucceeded, verification.Errors)
	}
	if verification.ManifestSchema != BackupManifestSchemaV063 {
		t.Fatalf("manifest schema = %q, want %q", verification.ManifestSchema, BackupManifestSchemaV063)
	}
}

func TestVerifyBackupDirectoryAcceptsV065ManifestWithStorageRetention(t *testing.T) {
	dir := validBackupDir(t, BackupManifestSchemaV065)
	if err := os.Mkdir(filepath.Join(dir, "storage-retention"), 0o755); err != nil {
		t.Fatalf("mkdir storage-retention: %v", err)
	}
	manifest := readManifestForTest(t, filepath.Join(dir, "manifest.json"))
	manifest.Paths.StorageRetention = "storage-retention"
	writeJSON(t, filepath.Join(dir, "manifest.json"), manifest)

	verification, err := VerifyBackupDirectory(context.Background(), dir)
	if err != nil {
		t.Fatalf("VerifyBackupDirectory returned error: %v", err)
	}
	if verification.Status != VerificationSucceeded {
		t.Fatalf("verification status = %q, want %q; errors=%v", verification.Status, VerificationSucceeded, verification.Errors)
	}
	if verification.Checks["storage_retention_snapshot"] != VerificationSucceeded {
		t.Fatalf("storage retention check = %q, want succeeded", verification.Checks["storage_retention_snapshot"])
	}
}

func TestVerifyBackupDirectoryAcceptsV08ManifestWithNotesCoverage(t *testing.T) {
	dir := validBackupDir(t, BackupManifestSchemaV08)
	if err := os.Mkdir(filepath.Join(dir, "loom-notes"), 0o755); err != nil {
		t.Fatalf("mkdir loom-notes: %v", err)
	}
	manifest := readManifestForTest(t, filepath.Join(dir, "manifest.json"))
	manifest.Paths.NotesProjection = "loom-notes"
	manifest.Policies.NotesSourceRoots = "canonical_notes_roots_are_protected_by_private_backups_and_watched_root_snapshots"
	manifest.Policies.NotesProjection = "copied_as_loom-notes_projection_snapshot_and_rebuildable_from_canonical_notes_roots"
	manifest.Policies.KnowledgeIndex = "captured_by_postgres_dump_as_knowledge_schema_tables"
	writeJSON(t, filepath.Join(dir, "manifest.json"), manifest)

	verification, err := VerifyBackupDirectory(context.Background(), dir)
	if err != nil {
		t.Fatalf("VerifyBackupDirectory returned error: %v", err)
	}
	if verification.Status != VerificationSucceeded {
		t.Fatalf("verification status = %q, want %q; errors=%v", verification.Status, VerificationSucceeded, verification.Errors)
	}
	for _, check := range []string{"notes_projection_snapshot", "notes_source_roots_policy", "notes_projection_policy", "knowledge_index_policy"} {
		if verification.Checks[check] != VerificationSucceeded {
			t.Fatalf("check %q = %q, want succeeded", check, verification.Checks[check])
		}
	}
}

func TestVerifyBackupDirectoryAcceptsMainDocumentsAndStorageArchiveSnapshots(t *testing.T) {
	dir := validBackupDir(t, BackupManifestSchemaV051)
	if err := os.Mkdir(filepath.Join(dir, "user-backups"), 0o755); err != nil {
		t.Fatalf("mkdir user-backups: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "main-documents"), 0o755); err != nil {
		t.Fatalf("mkdir main-documents: %v", err)
	}
	writeValidArchiveCustody(t, filepath.Join(dir, "storage-archive"), "taxes-2026", []byte("receipt"))
	manifest := readManifestForTest(t, filepath.Join(dir, "manifest.json"))
	manifest.Paths.UserBackups = "user-backups"
	manifest.Paths.MainDocuments = "main-documents"
	manifest.Paths.StorageArchive = "storage-archive"
	manifest.Policies.MainBox = "not_copied_requires_explicit_watched_roots_or_private_backups"
	writeJSON(t, filepath.Join(dir, "manifest.json"), manifest)

	verification, err := VerifyBackupDirectory(context.Background(), dir)
	if err != nil {
		t.Fatalf("VerifyBackupDirectory returned error: %v", err)
	}
	if verification.Status != VerificationSucceeded {
		t.Fatalf("verification status = %q, want %q; errors=%v", verification.Status, VerificationSucceeded, verification.Errors)
	}
	for _, check := range []string{"user_backups_snapshot", "main_documents_snapshot", "storage_archive_snapshot"} {
		if verification.Checks[check] != VerificationSucceeded {
			t.Fatalf("check %q = %q, want succeeded", check, verification.Checks[check])
		}
	}
}

func TestVerifyBackupDirectoryRejectsStorageArchiveWithoutRequiredLayout(t *testing.T) {
	dir := validBackupDir(t, BackupManifestSchemaV051)
	if err := os.MkdirAll(filepath.Join(dir, "storage-archive", "incomplete-key", "objects"), 0o755); err != nil {
		t.Fatalf("mkdir incomplete archive key: %v", err)
	}
	manifest := readManifestForTest(t, filepath.Join(dir, "manifest.json"))
	manifest.Paths.StorageArchive = "storage-archive"
	writeJSON(t, filepath.Join(dir, "manifest.json"), manifest)

	verification, err := VerifyBackupDirectory(context.Background(), dir)
	if err != nil {
		t.Fatalf("VerifyBackupDirectory returned error: %v", err)
	}
	if verification.Status != VerificationFailed {
		t.Fatalf("verification status = %q, want failed", verification.Status)
	}
	if verification.Checks["storage_archive_snapshot"] != VerificationFailed {
		t.Fatalf("storage archive check = %q, want failed", verification.Checks["storage_archive_snapshot"])
	}
}

func TestVerifyBackupDirectoryValidatesCanonicalKeysBesideLegacyArchiveLayout(t *testing.T) {
	dir := validBackupDir(t, BackupManifestSchemaV051)
	for _, child := range []string{"objects", "manifests", filepath.Join("incomplete-key", "objects")} {
		if err := os.MkdirAll(filepath.Join(dir, "storage-archive", child), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	manifest := readManifestForTest(t, filepath.Join(dir, "manifest.json"))
	manifest.Paths.StorageArchive = "storage-archive"
	writeJSON(t, filepath.Join(dir, "manifest.json"), manifest)

	verification, err := VerifyBackupDirectory(context.Background(), dir)
	if err != nil {
		t.Fatalf("VerifyBackupDirectory returned error: %v", err)
	}
	if verification.Checks["storage_archive_snapshot"] != VerificationFailed {
		t.Fatalf("mixed archive layout check = %q, want failed", verification.Checks["storage_archive_snapshot"])
	}
}

func TestVerifyBackupDirectoryRequiresManifestForCanonicalUserBackupBatch(t *testing.T) {
	dir := validBackupDir(t, BackupManifestSchemaV08)
	userBackups := filepath.Join(dir, "user-backups")
	if err := os.MkdirAll(filepath.Join(userBackups, "workspace", "notes", "batch"), 0o755); err != nil {
		t.Fatalf("mkdir user backup batch: %v", err)
	}
	manifest := readManifestForTest(t, filepath.Join(dir, "manifest.json"))
	manifest.Paths.UserBackups = "user-backups"
	manifest.Policies.NotesSourceRoots = "watched_roots"
	manifest.Policies.NotesProjection = "excluded"
	manifest.Policies.KnowledgeIndex = "postgres"
	writeJSON(t, filepath.Join(dir, "manifest.json"), manifest)

	verification, err := VerifyBackupDirectory(context.Background(), dir)
	if err != nil {
		t.Fatalf("VerifyBackupDirectory returned error: %v", err)
	}
	if verification.Checks["user_backups_snapshot"] != VerificationFailed {
		t.Fatalf("missing batch manifest was accepted: %#v", verification)
	}
	writeValidWatchedRootBackupCustody(t, userBackups, "workspace", "notes", "batch", []byte("note"))
	verification, err = VerifyBackupDirectory(context.Background(), dir)
	if err != nil || verification.Checks["user_backups_snapshot"] != VerificationSucceeded {
		t.Fatalf("valid user backup custody rejected: verification=%#v err=%v", verification, err)
	}
}

func TestValidateStorageArchiveCustodyRejectsCorruptV07Evidence(t *testing.T) {
	t.Run("valid canonical", func(t *testing.T) {
		root := t.TempDir()
		writeValidArchiveCustody(t, root, "valid-key", []byte("valid payload"))
		if err := ValidateStorageArchiveCustody(root); err != nil {
			t.Fatalf("valid canonical archive rejected: %v", err)
		}
	})
	t.Run("garbage json", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "bad-key", "objects"), 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(root, "bad-key", "manifest.json"), []byte(`{"not":"json"`))
		if err := ValidateStorageArchiveCustody(root); err == nil {
			t.Fatal("garbage archive manifest was accepted")
		}
	})
	t.Run("path escape", func(t *testing.T) {
		root := t.TempDir()
		manifest := writeValidArchiveCustody(t, root, "escape-key", []byte("valid payload"))
		manifest.Entries[0].ArchiveObjectPath = "../external"
		writeJSON(t, filepath.Join(root, "escape-key", "manifest.json"), manifest)
		if err := ValidateStorageArchiveCustody(root); err == nil {
			t.Fatal("archive object path escape was accepted")
		}
	})
	t.Run("logical path escape", func(t *testing.T) {
		root := t.TempDir()
		manifest := writeValidArchiveCustody(t, root, "logical-key", []byte("archive payload"))
		manifest.Entries[0].ArchiveLogicalPath = "../outside"
		manifest.Entries[0].ArchiveViewPath = "main/Archive/../outside"
		writeJSON(t, filepath.Join(root, "logical-key", "manifest.json"), manifest)
		if err := ValidateStorageArchiveCustody(root); err == nil {
			t.Fatal("archive logical path escape was accepted")
		}
	})
	t.Run("content identity mismatch", func(t *testing.T) {
		root := t.TempDir()
		manifest := writeValidArchiveCustody(t, root, "identity-key", []byte("archive payload"))
		manifest.Entries[0].ContentKey = "sha256:" + strings.Repeat("0", 64)
		writeJSON(t, filepath.Join(root, "identity-key", "manifest.json"), manifest)
		if err := ValidateStorageArchiveCustody(root); err == nil {
			t.Fatal("archive content identity mismatch was accepted")
		}
	})
	t.Run("catalog recovery evidence", func(t *testing.T) {
		root := t.TempDir()
		manifest := writeValidArchiveCustody(t, root, "recovery-key", []byte("archive payload"))
		manifest.Entries[0].SourceRefURI = ""
		writeJSON(t, filepath.Join(root, "recovery-key", "manifest.json"), manifest)
		if err := ValidateStorageArchiveCustody(root); err == nil {
			t.Fatal("incomplete archive catalog recovery evidence was accepted")
		}
	})
	t.Run("missing object", func(t *testing.T) {
		root := t.TempDir()
		manifest := writeValidArchiveCustody(t, root, "missing-key", []byte("valid payload"))
		if err := os.Remove(filepath.Join(root, "missing-key", filepath.FromSlash(manifest.Entries[0].ArchiveObjectPath))); err != nil {
			t.Fatal(err)
		}
		if err := ValidateStorageArchiveCustody(root); err == nil {
			t.Fatal("missing archive object was accepted")
		}
	})
	t.Run("checksum mismatch", func(t *testing.T) {
		root := t.TempDir()
		manifest := writeValidArchiveCustody(t, root, "checksum-key", []byte("valid payload"))
		mustWrite(t, filepath.Join(root, "checksum-key", filepath.FromSlash(manifest.Entries[0].ArchiveObjectPath)), []byte("wrong payload"))
		if err := ValidateStorageArchiveCustody(root); err == nil {
			t.Fatal("archive checksum mismatch was accepted")
		}
	})
}

func TestValidateStorageArchiveCustodyPreservesExplicitLegacyCompatibility(t *testing.T) {
	root := t.TempDir()
	for _, child := range []string{"objects", "manifests"} {
		if err := os.MkdirAll(filepath.Join(root, child), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := ValidateStorageArchiveCustody(root); err != nil {
		t.Fatalf("explicit legacy objects/manifests pair rejected: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "bad-v07", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "bad-v07", "manifest.json"), []byte(`{"schema_version":"storage.archive_manifest.v0.7"}`))
	if err := ValidateStorageArchiveCustody(root); err == nil {
		t.Fatal("malformed v0.7 archive fell through legacy compatibility")
	}
}

func TestValidateStorageArchiveCustodyChecksEveryVisibleAcceptedKey(t *testing.T) {
	root := t.TempDir()
	acceptedKeys := []string{"committed-key", "_underscored", "punctuation-2026_08"}
	for _, key := range acceptedKeys {
		writeValidArchiveCustody(t, root, key, []byte("payload for "+key))
	}
	if err := os.Mkdir(filepath.Join(root, ".locks"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".committed-key.preparing"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ValidateStorageArchiveCustody(root); err != nil {
		t.Fatalf("validate every accepted archive key: %v", err)
	}
	if err := os.Remove(filepath.Join(root, acceptedKeys[len(acceptedKeys)-1], "manifest.json")); err != nil {
		t.Fatal(err)
	}
	if err := ValidateStorageArchiveCustody(root); err == nil || !strings.Contains(err.Error(), acceptedKeys[len(acceptedKeys)-1]) {
		t.Fatalf("visible accepted key was silently omitted: %v", err)
	}
}

func TestValidateUserBackupsCustodyRejectsCorruptV07Evidence(t *testing.T) {
	t.Run("valid watched root", func(t *testing.T) {
		root := t.TempDir()
		writeValidWatchedRootBackupCustody(t, root, "workspace", "notes", "batch", []byte("note"))
		if err := ValidateUserBackupsCustody(root); err != nil {
			t.Fatalf("valid watched-root custody rejected: %v", err)
		}
	})
	t.Run("garbage json", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "backups")
		if err := os.MkdirAll(filepath.Join(root, "workspace", "notes", "batch"), 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(root, "workspace", "notes", "batch", "manifest.json"), []byte(`{`))
		if err := ValidateUserBackupsCustody(root); err == nil {
			t.Fatal("garbage backup manifest was accepted")
		}
	})
	t.Run("path escape", func(t *testing.T) {
		root := t.TempDir()
		manifest := writeValidWatchedRootBackupCustody(t, root, "workspace", "notes", "batch", []byte("note"))
		manifest.Items[0].RelativePath = "../external"
		writeJSON(t, filepath.Join(root, "workspace", "notes", "batch", "manifest.json"), manifest)
		if err := ValidateUserBackupsCustody(root); err == nil {
			t.Fatal("backup payload path escape was accepted")
		}
	})
	t.Run("missing payload", func(t *testing.T) {
		root := t.TempDir()
		writeValidWatchedRootBackupCustody(t, root, "workspace", "notes", "batch", []byte("note"))
		if err := os.Remove(filepath.Join(root, "workspace", "notes", "batch", "payload", "note.md")); err != nil {
			t.Fatal(err)
		}
		if err := ValidateUserBackupsCustody(root); err == nil {
			t.Fatal("missing backup payload was accepted")
		}
	})
	t.Run("checksum mismatch", func(t *testing.T) {
		root := t.TempDir()
		writeValidWatchedRootBackupCustody(t, root, "workspace", "notes", "batch", []byte("note"))
		mustWrite(t, filepath.Join(root, "workspace", "notes", "batch", "payload", "note.md"), []byte("nope"))
		if err := ValidateUserBackupsCustody(root); err == nil {
			t.Fatal("backup payload checksum mismatch was accepted")
		}
	})
	t.Run("valid private backup", func(t *testing.T) {
		root := t.TempDir()
		writeValidPrivateBackupCustody(t, root, "workspace", "private", "private_backup_test", []byte("tar payload"))
		if err := ValidateUserBackupsCustody(root); err != nil {
			t.Fatalf("valid private backup custody rejected: %v", err)
		}
	})
	t.Run("valid watched root private artifact", func(t *testing.T) {
		root := t.TempDir()
		writeValidWatchedRootPrivateBackupCustody(t, root, []byte("note"), []byte("note"))
		if err := ValidateUserBackupsCustody(root); err != nil {
			t.Fatalf("valid watched-root private artifact rejected: %v", err)
		}
	})
	t.Run("watched root private artifact inner mismatch", func(t *testing.T) {
		root := t.TempDir()
		writeValidWatchedRootPrivateBackupCustody(t, root, []byte("note"), []byte("evil"))
		if err := ValidateUserBackupsCustody(root); err == nil {
			t.Fatal("private artifact with substituted inner content was accepted")
		}
	})
}

func TestValidateUserBackupsCustodyHistoricalOneItemBatchKindCompatibility(t *testing.T) {
	t.Run("exact metadata-only directory", func(t *testing.T) {
		root := t.TempDir()
		writeWatchedRootDirectoryCustody(t, root, watchedroots.BackupItemKindDirectory)
		if err := ValidateUserBackupsCustody(root); err != nil {
			t.Fatalf("historical directory custody rejected: %v", err)
		}
	})
	t.Run("canonical metadata-only directory", func(t *testing.T) {
		root := t.TempDir()
		writeWatchedRootDirectoryCustody(t, root, watchedroots.BackupBatchKindWatchedRoot)
		if err := ValidateUserBackupsCustody(root); err != nil {
			t.Fatalf("canonical directory custody rejected: %v", err)
		}
	})
	t.Run("legacy mismatch", func(t *testing.T) {
		root := t.TempDir()
		manifest := writeWatchedRootDirectoryCustody(t, root, watchedroots.BackupItemKindFile)
		writeJSON(t, filepath.Join(root, "workspace", "documents", "batch", "manifest.json"), manifest)
		if err := ValidateUserBackupsCustody(root); err == nil {
			t.Fatal("mismatched historical batch/item kind was accepted")
		}
	})
	t.Run("legacy multiple items", func(t *testing.T) {
		root := t.TempDir()
		manifest := writeWatchedRootDirectoryCustody(t, root, watchedroots.BackupItemKindDirectory)
		second := manifest.Items[0]
		second.WatchedRootBackupItemID = "watched_root_backup_item_second"
		second.LocalItemRef = "local_backup_item_second"
		manifest.Items = append(manifest.Items, second)
		manifest.ItemCount = len(manifest.Items)
		writeJSON(t, filepath.Join(root, "workspace", "documents", "batch", "manifest.json"), manifest)
		if err := ValidateUserBackupsCustody(root); err == nil {
			t.Fatal("multi-item historical batch kind was accepted")
		}
	})
	t.Run("directory path escape", func(t *testing.T) {
		root := t.TempDir()
		manifest := writeWatchedRootDirectoryCustody(t, root, watchedroots.BackupItemKindDirectory)
		manifest.Items[0].RelativePath = "../external"
		writeJSON(t, filepath.Join(root, "workspace", "documents", "batch", "manifest.json"), manifest)
		if err := ValidateUserBackupsCustody(root); err == nil {
			t.Fatal("metadata-only path escape was accepted")
		}
	})
	t.Run("directory transport evidence", func(t *testing.T) {
		root := t.TempDir()
		manifest := writeWatchedRootDirectoryCustody(t, root, watchedroots.BackupItemKindDirectory)
		manifest.Items[0].ArtifactKind = watchedroots.BackupArtifactKindFileTransfer
		manifest.Items[0].ArtifactRef = "file_transfer_unexpected"
		writeJSON(t, filepath.Join(root, "workspace", "documents", "batch", "manifest.json"), manifest)
		if err := ValidateUserBackupsCustody(root); err == nil {
			t.Fatal("metadata-only payload artifact was accepted")
		}
	})
	t.Run("directory observation mismatch", func(t *testing.T) {
		root := t.TempDir()
		manifest := writeWatchedRootDirectoryCustody(t, root, watchedroots.BackupItemKindDirectory)
		manifest.Items[0].Metadata = json.RawMessage(`{"source":"loom-node-agent","local_batch_id":"batch","local_item_id":"local_backup_item_test","filesystem_observation":{"kind":"regular_file","logical_size_bytes":160}}`)
		writeJSON(t, filepath.Join(root, "workspace", "documents", "batch", "manifest.json"), manifest)
		if err := ValidateUserBackupsCustody(root); err == nil {
			t.Fatal("directory with regular-file observation was accepted")
		}
	})
	t.Run("batch status mismatch", func(t *testing.T) {
		root := t.TempDir()
		manifest := writeWatchedRootDirectoryCustody(t, root, watchedroots.BackupItemKindDirectory)
		manifest.Items[0].Status = watchedroots.BackupItemStatusFailed
		writeJSON(t, filepath.Join(root, "workspace", "documents", "batch", "manifest.json"), manifest)
		if err := ValidateUserBackupsCustody(root); err == nil {
			t.Fatal("accepted batch with failed item evidence was accepted")
		}
	})
}

func TestValidateLegacyPrivateBackupsCustody(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		root := t.TempDir()
		operation := filepath.Join(root, "node_main", "private_backup_accepted")
		if err := os.MkdirAll(operation, 0o700); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(operation, "payload.tar"), []byte("tar payload"))
		if err := ValidateLegacyPrivateBackupsCustody(root); err != nil {
			t.Fatalf("valid legacy private backup rejected: %v", err)
		}
	})
	t.Run("empty payload", func(t *testing.T) {
		root := t.TempDir()
		operation := filepath.Join(root, "node_main", "private_backup_empty")
		if err := os.MkdirAll(operation, 0o700); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(operation, "payload.tar"), nil)
		if err := ValidateLegacyPrivateBackupsCustody(root); err == nil {
			t.Fatal("empty legacy payload was accepted")
		}
	})
	t.Run("unexpected shape", func(t *testing.T) {
		root := t.TempDir()
		operation := filepath.Join(root, "node_main", "private_backup_bad")
		if err := os.MkdirAll(operation, 0o700); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(operation, "payload.bin"), []byte("payload"))
		if err := ValidateLegacyPrivateBackupsCustody(root); err == nil {
			t.Fatal("unexpected legacy payload name was accepted")
		}
	})
}

func TestPrivateBackupsEvidenceIsDeterministicBoundedAndNoFollow(t *testing.T) {
	root := t.TempDir()
	for relative, payload := range map[string]string{
		filepath.Join("node_z", "private_backup_z", "payload.tar"): "z payload",
		filepath.Join("node_a", "private_backup_a", "payload.tar"): "a payload",
	} {
		pathValue := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(pathValue), 0o700); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, pathValue, []byte(payload))
	}
	evidencePath := filepath.Join(t.TempDir(), PrivateBackupsEvidencePath)
	evidence, err := WritePrivateBackupsEvidence(context.Background(), root, "private-backups", evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence.Entries) != 2 || evidence.Entries[0].RelativePath != "node_a/private_backup_a/payload.tar" || evidence.Entries[1].RelativePath != "node_z/private_backup_z/payload.tar" {
		t.Fatalf("evidence is not deterministic and sorted: %#v", evidence.Entries)
	}
	if err := ValidatePrivateBackupsEvidence(context.Background(), root, "private-backups", evidencePath); err != nil {
		t.Fatalf("valid evidence rejected: %v", err)
	}
	secondEvidencePath := filepath.Join(t.TempDir(), PrivateBackupsEvidencePath)
	if _, err := WritePrivateBackupsEvidence(context.Background(), root, "private-backups", secondEvidencePath); err != nil {
		t.Fatal(err)
	}
	firstRaw, err := os.ReadFile(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	secondRaw, err := os.ReadFile(secondEvidencePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstRaw, secondRaw) {
		t.Fatal("private backups evidence is not byte-for-byte deterministic")
	}

	boundedPath := filepath.Join(t.TempDir(), "bounded.json")
	if err := writePrivateBackupsEvidenceAtomic(boundedPath, evidence, 128); err == nil {
		t.Fatal("oversized private backups evidence was published")
	}
	if _, err := os.Lstat(boundedPath); !os.IsNotExist(err) {
		t.Fatalf("bounded writer published destination: %v", err)
	}

	external := filepath.Join(t.TempDir(), "evidence.json")
	mustWrite(t, external, firstRaw)
	if err := os.Remove(evidencePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, evidencePath); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePrivateBackupsEvidence(context.Background(), root, "private-backups", evidencePath); err == nil {
		t.Fatal("symlinked private backups evidence was accepted")
	}

	evidence.Entries[0].RelativePath = "../escape/payload.tar"
	tamperedPath := filepath.Join(t.TempDir(), "tampered.json")
	if err := writePrivateBackupsEvidenceAtomic(tamperedPath, evidence, maxPrivateBackupsEvidenceBytes); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePrivateBackupsEvidence(context.Background(), root, "private-backups", tamperedPath); err == nil {
		t.Fatal("path-escaping private backups evidence was accepted")
	}
}

func TestVerifyV09BackupValidatesLegacyPrivateBackups(t *testing.T) {
	dir := validBackupDir(t, BackupManifestSchemaV09)
	privateBackups := filepath.Join(dir, "private-backups")
	operation := filepath.Join(privateBackups, "node_main", "private_backup_accepted")
	if err := os.MkdirAll(operation, 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(operation, "payload.tar"), []byte("tar payload"))
	manifest := readManifestForTest(t, filepath.Join(dir, "manifest.json"))
	manifest.Policies.NotesSourceRoots = "watched_roots"
	manifest.Policies.NotesProjection = "excluded"
	manifest.Policies.KnowledgeIndex = "postgres"
	writeJSON(t, filepath.Join(dir, "manifest.json"), manifest)

	verification, err := VerifyBackupDirectory(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if verification.Checks["private_backups_snapshot"] != VerificationSucceeded {
		t.Fatalf("valid v0.9 legacy custody was rejected: %#v", verification)
	}
	// The overall fixture remains failed because v0.9 also requires Imports;
	// that independent policy must not mask the legacy-custody check.
	if err := ValidateLegacyPrivateBackupsCustody(privateBackups); err != nil {
		t.Fatalf("legacy custody validation failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(operation, "extra"), []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	verification, err = VerifyBackupDirectory(context.Background(), dir)
	if err != nil || verification.Checks["private_backups_snapshot"] != VerificationFailed {
		t.Fatalf("malformed v0.9 legacy custody was accepted: %#v err=%v", verification, err)
	}
}

func TestVerifyV09BackupRequiresBothMixedCustodyPathsAndArtifacts(t *testing.T) {
	dir := validBackupDir(t, BackupManifestSchemaV09)
	userBackups := filepath.Join(dir, "user-backups")
	if err := os.Mkdir(userBackups, 0o755); err != nil {
		t.Fatal(err)
	}
	privateBackups := filepath.Join(dir, "private-backups")
	operation := filepath.Join(privateBackups, "node_main", "private_backup_accepted")
	if err := os.MkdirAll(operation, 0o700); err != nil {
		t.Fatal(err)
	}
	payload := []byte("retained legacy payload")
	mustWrite(t, filepath.Join(operation, "payload.tar"), payload)
	userSize := int64(0)
	privateSize := int64(len(payload))
	userFiles := int64(0)
	privateFiles := int64(1)
	manifest := readManifestForTest(t, filepath.Join(dir, "manifest.json"))
	manifest.Paths.UserBackups = "user-backups"
	manifest.Paths.PrivateBackups = "private-backups"
	manifest.Paths.PrivateBackupsEvidence = PrivateBackupsEvidencePath
	manifest.Policies.NotesSourceRoots = "watched_roots"
	manifest.Policies.NotesProjection = "excluded"
	manifest.Policies.KnowledgeIndex = "postgres"
	if _, err := WritePrivateBackupsEvidence(context.Background(), privateBackups, manifest.Paths.PrivateBackups, filepath.Join(dir, PrivateBackupsEvidencePath)); err != nil {
		t.Fatal(err)
	}
	evidenceHash, evidenceSize, err := HashFile(filepath.Join(dir, PrivateBackupsEvidencePath))
	if err != nil {
		t.Fatal(err)
	}
	manifest.Artifacts = []BackupManifestArtifact{
		{Kind: ArtifactKindPrivateBackupsSnapshot, Path: "user-backups", FileCount: &userFiles, SizeBytes: &userSize},
		{Kind: ArtifactKindPrivateBackupsSnapshot, Path: "private-backups", FileCount: &privateFiles, SizeBytes: &privateSize},
		{Kind: ArtifactKindPrivateBackupsEvidence, Path: PrivateBackupsEvidencePath, SizeBytes: &evidenceSize, SHA256: evidenceHash},
	}
	writeJSON(t, filepath.Join(dir, "manifest.json"), manifest)

	verification, err := VerifyBackupDirectory(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []string{"user_backups_snapshot", "private_backups_snapshot", "private_backups_evidence", "user_backups_dual_path", "artifact:private_backups_snapshot:user-backups", "artifact:private_backups_snapshot:private-backups", "artifact:private_backups_evidence:" + PrivateBackupsEvidencePath} {
		if verification.Checks[check] != VerificationSucceeded {
			t.Fatalf("mixed custody check %q = %q errors=%v", check, verification.Checks[check], verification.Errors)
		}
	}
	if err := os.WriteFile(filepath.Join(operation, "payload.tar"), []byte("retained legacy payloaD"), 0o600); err != nil {
		t.Fatal(err)
	}
	verification, err = VerifyBackupDirectory(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if verification.Checks["private_backups_evidence"] != VerificationFailed {
		t.Fatalf("same-size legacy payload tamper passed: %#v", verification)
	}
	if err := os.WriteFile(filepath.Join(operation, "payload.tar"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	withoutEvidence := manifest
	withoutEvidence.Paths.PrivateBackupsEvidence = ""
	withoutEvidence.Artifacts = append([]BackupManifestArtifact(nil), manifest.Artifacts[:2]...)
	writeJSON(t, filepath.Join(dir, "manifest.json"), withoutEvidence)
	verification, err = VerifyBackupDirectory(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if verification.Checks["user_backups_dual_path"] != VerificationFailed {
		t.Fatalf("mixed custody without byte evidence passed: %#v", verification)
	}
	writeJSON(t, filepath.Join(dir, "manifest.json"), manifest)
	wrongPrivateFiles := int64(2)
	manifest.Artifacts[1].FileCount = &wrongPrivateFiles
	writeJSON(t, filepath.Join(dir, "manifest.json"), manifest)
	verification, err = VerifyBackupDirectory(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if verification.Checks["artifact:private_backups_snapshot:private-backups"] != VerificationFailed {
		t.Fatalf("wrong legacy file summary passed: %#v", verification)
	}
	manifest.Artifacts[1].FileCount = &privateFiles

	manifest.Artifacts = manifest.Artifacts[:1]
	writeJSON(t, filepath.Join(dir, "manifest.json"), manifest)
	verification, err = VerifyBackupDirectory(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if verification.Checks["user_backups_dual_path"] != VerificationFailed {
		t.Fatalf("mixed manifest missing legacy artifact passed: %#v", verification)
	}

	manifest.Artifacts = append(manifest.Artifacts, BackupManifestArtifact{Kind: ArtifactKindPrivateBackupsSnapshot, Path: "private-backups", SizeBytes: &privateSize})
	writeJSON(t, filepath.Join(dir, "manifest.json"), manifest)
	if err := os.RemoveAll(privateBackups); err != nil {
		t.Fatal(err)
	}
	verification, err = VerifyBackupDirectory(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if verification.Status != VerificationFailed || verification.Checks["private_backups_snapshot"] != VerificationFailed {
		t.Fatalf("backup missing retained legacy custody passed: %#v", verification)
	}
}

func TestVerifyV09BackupRejectsOverlappingMixedCustodyPaths(t *testing.T) {
	dir := validBackupDir(t, BackupManifestSchemaV09)
	if err := os.MkdirAll(filepath.Join(dir, "user-backups", "private-backups"), 0o755); err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	manifest := readManifestForTest(t, filepath.Join(dir, "manifest.json"))
	manifest.Paths.UserBackups = "user-backups"
	manifest.Paths.PrivateBackups = "user-backups/private-backups"
	manifest.Policies.NotesSourceRoots = "watched_roots"
	manifest.Policies.NotesProjection = "excluded"
	manifest.Policies.KnowledgeIndex = "postgres"
	manifest.Artifacts = []BackupManifestArtifact{
		{Kind: ArtifactKindPrivateBackupsSnapshot, Path: "user-backups", SizeBytes: &zero},
		{Kind: ArtifactKindPrivateBackupsSnapshot, Path: "user-backups/private-backups", SizeBytes: &zero},
	}
	writeJSON(t, filepath.Join(dir, "manifest.json"), manifest)
	verification, err := VerifyBackupDirectory(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if verification.Checks["user_backups_dual_path"] != VerificationFailed {
		t.Fatalf("overlapping mixed custody paths passed: %#v", verification)
	}
}

func TestVerifyBackupDirectoryRejectsUnredactedServiceEnvSnapshot(t *testing.T) {
	dir := validBackupDir(t, BackupManifestSchemaV051)
	mustWrite(t, filepath.Join(dir, "loom.env.redacted"), []byte("LOOM_DB_URL=postgres://loom:secret@example/loom\n"))
	manifest := readManifestForTest(t, filepath.Join(dir, "manifest.json"))
	manifest.Paths.ServiceEnvRedacted = "loom.env.redacted"
	writeJSON(t, filepath.Join(dir, "manifest.json"), manifest)

	verification, err := VerifyBackupDirectory(context.Background(), dir)
	if err != nil {
		t.Fatalf("VerifyBackupDirectory returned error: %v", err)
	}
	if verification.Status != VerificationFailed {
		t.Fatalf("verification status = %q, want failed", verification.Status)
	}
	if verification.Checks["service_env_redacted_snapshot"] != VerificationFailed {
		t.Fatalf("service env check = %q, want failed", verification.Checks["service_env_redacted_snapshot"])
	}
}

func TestVerifyBackupDirectoryReportsMissingManifest(t *testing.T) {
	dir := t.TempDir()

	verification, err := VerifyBackupDirectory(context.Background(), dir)
	if err != nil {
		t.Fatalf("VerifyBackupDirectory returned error: %v", err)
	}
	if verification.Status != VerificationFailed {
		t.Fatalf("verification status = %q, want %q", verification.Status, VerificationFailed)
	}
	if verification.Checks["manifest"] != VerificationFailed {
		t.Fatalf("manifest check = %q, want failed", verification.Checks["manifest"])
	}
}

func TestVerifyBackupDirectoryReportsMissingDump(t *testing.T) {
	dir := validBackupDir(t, BackupManifestSchemaV01)
	if err := os.Remove(filepath.Join(dir, "loom_main.dump")); err != nil {
		t.Fatalf("remove dump: %v", err)
	}

	verification, err := VerifyBackupDirectory(context.Background(), dir)
	if err != nil {
		t.Fatalf("VerifyBackupDirectory returned error: %v", err)
	}
	if verification.Status != VerificationFailed {
		t.Fatalf("verification status = %q, want failed", verification.Status)
	}
	if verification.Checks["postgres_dump"] != VerificationFailed {
		t.Fatalf("postgres dump check = %q, want failed", verification.Checks["postgres_dump"])
	}
}

func TestVerifyBackupDirectoryReportsArtifactHashMismatch(t *testing.T) {
	dir := validBackupDir(t, BackupManifestSchemaV02)
	size := int64(len("database dump\n"))
	manifest := readManifestForTest(t, filepath.Join(dir, "manifest.json"))
	manifest.Artifacts = []BackupManifestArtifact{
		{
			Kind:      ArtifactKindPostgresDump,
			Path:      "loom_main.dump",
			SizeBytes: &size,
			SHA256:    "0000000000000000000000000000000000000000000000000000000000000000",
		},
	}
	writeJSON(t, filepath.Join(dir, "manifest.json"), manifest)

	verification, err := VerifyBackupDirectory(context.Background(), dir)
	if err != nil {
		t.Fatalf("VerifyBackupDirectory returned error: %v", err)
	}
	if verification.Status != VerificationFailed {
		t.Fatalf("verification status = %q, want failed", verification.Status)
	}
}

func TestSafeBackupPathRejectsTraversal(t *testing.T) {
	_, err := safeBackupPath(t.TempDir(), "../escape")
	if err == nil {
		t.Fatal("safeBackupPath accepted traversal path")
	}
}

func validBackupDir(t *testing.T, schema string) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "health.json"), []byte(`{"ok":true}`))
	mustWrite(t, filepath.Join(dir, "loom_main.dump"), []byte("database dump\n"))
	if err := os.Mkdir(filepath.Join(dir, "object-store"), 0o755); err != nil {
		t.Fatalf("mkdir object-store: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "private-backups"), 0o755); err != nil {
		t.Fatalf("mkdir private-backups: %v", err)
	}
	current := int64(18)
	latest := int64(18)
	writeJSON(t, filepath.Join(dir, "manifest.json"), BackupManifest{
		Schema:     schema,
		BackupKind: "loom_main_state",
		CreatedAt:  "2026-05-24T10:00:00Z",
		Source: BackupManifestSource{
			Hostname: "loom-dev",
			NodeID:   "dev-main",
			IsVPS:    false,
		},
		Loom: BackupManifestLoom{
			Version:          "0.0.0-dev",
			CurrentMigration: &current,
			LatestMigration:  &latest,
		},
		Database: BackupManifestDatabase{
			Name:     "loom_main",
			DumpFile: "loom_main.dump",
			Format:   "pg_dump_custom",
		},
		Paths: BackupManifestPaths{
			ObjectStore:    "object-store",
			PrivateBackups: "private-backups",
		},
	})
	return dir
}

func readManifestForTest(t *testing.T, path string) BackupManifest {
	t.Helper()
	manifest, err := ReadBackupManifest(path)
	if err != nil {
		t.Fatalf("ReadBackupManifest failed: %v", err)
	}
	return manifest
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	mustWrite(t, path, raw)
}

func mustWrite(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func validOperationalPackageForMaintenance(t *testing.T, schemaHead int64) (backupstrategy.OperationalPackageCreateResult, string) {
	t.Helper()
	root := t.TempDir()
	packages := filepath.Join(root, "packages")
	sources := filepath.Join(root, "sources")
	if err := os.Mkdir(packages, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sources, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := "maintenance-adapter-secret"
	service := filepath.Join(sources, "service.env")
	install := filepath.Join(sources, "install.yaml")
	release := filepath.Join(sources, "release.yaml")
	migration := filepath.Join(sources, "migration.json")
	updateState := filepath.Join(sources, "update.json")
	health := filepath.Join(sources, "health.json")
	mustWrite(t, service, []byte("LOOM_DB_URL="+secret+"\n"))
	mustWrite(t, install, []byte("credential: "+secret+"\n"))
	mustWrite(t, release, []byte("token: "+secret+"\n"))
	writeJSON(t, migration, backupstrategy.OperationalMigrationState{
		Schema: backupstrategy.OperationalMigrationStateSchema, CurrentVersion: schemaHead, LatestVersion: schemaHead, Status: "ok",
	})
	mustWrite(t, updateState, []byte(`{"schema":"loom.update.state.v1","status":"idle"}`))
	mustWrite(t, health, []byte(`{"status":"ok"}`))
	result, err := backupstrategy.CreateOperationalPackage(context.Background(), backupstrategy.OperationalPackageCreateInput{
		PackagesRoot: packages, PackageID: "operational-maintenance", CreatedAt: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC), SchemaHead: schemaHead,
		PostgresDump: func(_ context.Context, destination string) error {
			return os.WriteFile(destination, []byte("PGDMP\x01maintenance"), 0o600)
		},
		ServiceConfig: service, InstallConfig: install, ReleaseConfig: release,
		MigrationState: migration, UpdateState: updateState, Health: health,
		RedactConfig: func(_ string, source []byte) ([]byte, error) {
			return bytes.ReplaceAll(source, []byte(secret), []byte("[REDACTED]")), nil
		},
		ForbiddenValues: []string{secret},
	})
	if err != nil {
		t.Fatal(err)
	}
	return result, secret
}

func writeValidArchiveCustody(t *testing.T, root, archiveKey string, payload []byte) storagearchive.ManifestDocument {
	t.Helper()
	sum := sha256.Sum256(payload)
	size := int64(len(payload))
	checksum := hex.EncodeToString(sum[:])
	objectRelative := filepath.ToSlash(filepath.Join("objects", "sha256", checksum[:2], checksum))
	objectPath := filepath.Join(root, archiveKey, filepath.FromSlash(objectRelative))
	if err := os.MkdirAll(filepath.Dir(objectPath), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, objectPath, payload)
	manifest := storagearchive.ManifestDocument{
		SchemaVersion: storagearchive.ManifestSchemaVersion, CatalogRecovery: storagearchive.CatalogRecoverySchemaVersion,
		ArchiveManifestID: ids.NewStorageArchiveManifestID(),
		ArchiveKey:        archiveKey, ArchiveKind: "document_archive", SourceRef: "source/file.txt",
		TargetPath: "main/Archive/Documents/file.txt", OwnerNodeKey: "main",
		ExpectedEntryCount: 1, ArchivedEntryCount: 1, Complete: true, CreatedAt: time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC),
		Entries: []storagearchive.ManifestItem{{
			SourceStorageEntryID: ids.NewStorageEntryID(), ArchiveStorageEntryID: ids.NewStorageEntryID(),
			ArchivePhysicalRefID: ids.NewStoragePhysicalRefID(), SourceViewPath: "source/file.txt",
			SourceLogicalPath: "file.txt", SourceOriginalPath: "source/file.txt", SourceMimeType: "text/plain",
			SourceRefKind: "local_path", SourceRefURI: "/source/file.txt", SourceAvailabilityState: "available",
			ArchiveViewPath: "main/Archive/Documents/file.txt", ArchiveLogicalPath: "Documents/file.txt",
			ChecksumAlgorithm: "sha256", ChecksumHex: hex.EncodeToString(sum[:]), SizeBytes: &size,
			FileClass: "text", ContentKey: "sha256:" + hex.EncodeToString(sum[:]),
			ArchiveObjectPath: objectRelative,
		}},
	}
	writeJSON(t, filepath.Join(root, archiveKey, "manifest.json"), manifest)
	return manifest
}

func writeValidWatchedRootBackupCustody(t *testing.T, root, node, protectedRoot, batch string, payload []byte) watchedRootBackupCustodyManifest {
	t.Helper()
	sum := sha256.Sum256(payload)
	relativePath := "note.md"
	payloadPath := filepath.Join(root, node, protectedRoot, batch, "payload", relativePath)
	if err := os.MkdirAll(filepath.Dir(payloadPath), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, payloadPath, payload)
	mainBatchID := "watched_root_backup_batch_test"
	manifest := watchedRootBackupCustodyManifest{
		SchemaVersion: "storage.watched_root_backup_manifest.v0.7", SourceNodeID: "node_test",
		SourceNodeKey: node, RootKey: protectedRoot, LocalBatchID: batch, MainBatchID: mainBatchID,
		BatchKind: "watched_root_backup", BackupMode: "incremental_raw", Status: watchedroots.BackupBatchStatusAccepted,
		ItemCount: 1, Items: []watchedroots.BackupItem{{
			WatchedRootBackupItemID: "watched_root_backup_item_test", WatchedRootBackupBatchID: mainBatchID,
			NodeID: "node_test", RootKey: protectedRoot, LocalItemRef: "local_backup_item_test", ItemKind: watchedroots.BackupItemKindFile,
			Status: watchedroots.BackupItemStatusAccepted, BackupMode: "incremental_raw", RelativePath: relativePath,
			ContentHashURI: "sha256:" + hex.EncodeToString(sum[:]), SizeBytes: int64(len(payload)),
			ArtifactKind: watchedroots.BackupArtifactKindFileTransfer, ArtifactRef: "file_transfer_test",
		}},
	}
	writeJSON(t, filepath.Join(root, node, protectedRoot, batch, "manifest.json"), manifest)
	return manifest
}

func writeWatchedRootDirectoryCustody(t *testing.T, root, batchKind string) watchedRootBackupCustodyManifest {
	t.Helper()
	const (
		node          = "workspace"
		protectedRoot = "documents"
		batch         = "batch"
		mainBatchID   = "watched_root_backup_batch_test"
		localItemID   = "local_backup_item_test"
	)
	modifiedAt := time.Date(2026, 8, 28, 12, 33, 0, 598034000, time.UTC)
	manifest := watchedRootBackupCustodyManifest{
		SchemaVersion: "storage.watched_root_backup_manifest.v0.7",
		SourceNodeID:  "node_test",
		SourceNodeKey: node,
		RootKey:       protectedRoot,
		LocalBatchID:  batch,
		MainBatchID:   mainBatchID,
		BatchKind:     batchKind,
		BackupMode:    "incremental_raw",
		Status:        watchedroots.BackupBatchStatusAccepted,
		ItemCount:     1,
		Items: []watchedroots.BackupItem{{
			WatchedRootBackupItemID:  "watched_root_backup_item_test",
			WatchedRootBackupBatchID: mainBatchID,
			NodeID:                   "node_test",
			RootKey:                  protectedRoot,
			LocalItemRef:             localItemID,
			ItemKind:                 watchedroots.BackupItemKindDirectory,
			Status:                   watchedroots.BackupItemStatusAccepted,
			BackupMode:               "incremental_raw",
			RelativePath:             ".loom-acceptance",
			ContentHashURI:           "sha256:d98bbba89552ada7e53b891b230fc26bea2e2b272e009b580c40fe649336c855",
			SizeBytes:                160,
			ModifiedAt:               &modifiedAt,
			Metadata:                 json.RawMessage(`{"source":"loom-node-agent","local_batch_id":"batch","local_item_id":"local_backup_item_test","local_artifact_id":"","file_transfer_id":"","file_transfer_storage_entry_id":"","file_transfer_accepted_path":"","filesystem_observation":{"kind":"directory","source_mode":493,"logical_size_bytes":160}}`),
		}},
	}
	if err := os.MkdirAll(filepath.Join(root, node, protectedRoot, batch), 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(root, node, protectedRoot, batch, "manifest.json"), manifest)
	return manifest
}

func writeValidPrivateBackupCustody(t *testing.T, root, node, protectedRoot, batch string, payload []byte) privateBackupCustodyManifest {
	t.Helper()
	sum := sha256.Sum256(payload)
	directory := filepath.Join(root, node, protectedRoot, batch)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(directory, "payload.tar"), payload)
	manifest := privateBackupCustodyManifest{
		SchemaVersion: "storage.private_backup_manifest.v0.7", PrivateBackupOperationID: batch,
		SourceNodeID: "node_test", SourceNodeKey: node, RootKey: protectedRoot, BatchKey: batch,
		PayloadPath: "payload.tar", PayloadSHA256: hex.EncodeToString(sum[:]), CoarseSizeBytes: int64(len(payload)),
		Metadata: map[string]any{"backup_kind": "private_raw_folder"},
	}
	writeJSON(t, filepath.Join(directory, "manifest.json"), manifest)
	return manifest
}

func writeValidWatchedRootPrivateBackupCustody(t *testing.T, root string, claimedContent, storedContent []byte) watchedRootBackupCustodyManifest {
	t.Helper()
	const (
		node          = "workspace"
		protectedRoot = "notes"
		batch         = "batch"
		operationID   = "private_backup_test"
		localItemID   = "local_backup_item_test"
	)
	claimedHash := sha256.Sum256(claimedContent)
	artifactManifest, err := json.Marshal(map[string]any{
		"schema_version": "watched_root.backup_artifact.v0.2", "root_key": protectedRoot,
		"relative_path": "note.md", "content_hash_uri": "sha256:" + hex.EncodeToString(claimedHash[:]),
		"size_bytes": len(claimedContent), "backup_mode": "incremental_raw", "local_batch_id": batch,
		"local_item_id": localItemID,
	})
	if err != nil {
		t.Fatal(err)
	}
	var artifact bytes.Buffer
	writer := tar.NewWriter(&artifact)
	for _, member := range []struct {
		name    string
		payload []byte
	}{{"manifest.json", artifactManifest}, {"content", storedContent}} {
		if err := writer.WriteHeader(&tar.Header{Name: member.name, Mode: 0o600, Size: int64(len(member.payload)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(member.payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	operationRelative := filepath.Join(node, protectedRoot, batch, "artifacts", operationID)
	if err := os.MkdirAll(filepath.Join(root, operationRelative), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, operationRelative, "payload.tar"), artifact.Bytes())
	artifactHash := sha256.Sum256(artifact.Bytes())
	writeJSON(t, filepath.Join(root, operationRelative, "manifest.json"), privateBackupCustodyManifest{
		SchemaVersion: "storage.private_backup_manifest.v0.7", PrivateBackupOperationID: operationID,
		SourceNodeID: "node_test", SourceNodeKey: node, RootKey: protectedRoot, BatchKey: batch,
		PayloadPath: "payload.tar", PayloadSHA256: hex.EncodeToString(artifactHash[:]), CoarseSizeBytes: int64(artifact.Len()),
		Metadata: map[string]any{"backup_kind": "watched_root_file"},
	})
	operationCopy := operationID
	manifest := watchedRootBackupCustodyManifest{
		SchemaVersion: "storage.watched_root_backup_manifest.v0.7", SourceNodeID: "node_test",
		SourceNodeKey: node, RootKey: protectedRoot, LocalBatchID: batch, MainBatchID: "watched_root_backup_batch_test",
		BatchKind: "watched_root_backup", BackupMode: "incremental_raw", Status: watchedroots.BackupBatchStatusAccepted,
		ItemCount: 1, Items: []watchedroots.BackupItem{{
			WatchedRootBackupItemID: "watched_root_backup_item_test", WatchedRootBackupBatchID: "watched_root_backup_batch_test",
			NodeID: "node_test", RootKey: protectedRoot, LocalItemRef: localItemID, ItemKind: watchedroots.BackupItemKindFile,
			Status: watchedroots.BackupItemStatusAccepted, BackupMode: "incremental_raw", RelativePath: "note.md",
			ContentHashURI: "sha256:" + hex.EncodeToString(claimedHash[:]), SizeBytes: int64(len(claimedContent)),
			ArtifactKind: watchedroots.BackupArtifactKindPrivateBackupOperation, ArtifactRef: operationID,
			PrivateBackupOperationID: &operationCopy,
		}},
	}
	writeJSON(t, filepath.Join(root, node, protectedRoot, batch, "manifest.json"), manifest)
	return manifest
}
