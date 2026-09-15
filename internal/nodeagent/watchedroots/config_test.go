package watchedroots

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/filesystemconnector"
)

func TestValidateRootConfigAcceptsSafeSubdirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	notes := filepath.Join(dir, "Notes")
	if err := os.MkdirAll(notes, 0o700); err != nil {
		t.Fatalf("mkdir notes: %v", err)
	}
	validated, err := ValidateRootConfig(RootConfig{
		RootKey:          "Notes",
		DisplayName:      "My Notes",
		SafeRootKey:      "slice09",
		RootRelativePath: "Notes",
		Include:          []string{"**/*.md"},
	}, filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{
		filesystemconnector.DefaultSafeRoot("slice09", dir),
	}})
	if err != nil {
		t.Fatalf("ValidateRootConfig failed: %v", err)
	}
	if validated.Config.RootKey != "notes" || validated.Config.SafeRootKey != "slice09" {
		t.Fatalf("unexpected normalized config %#v", validated.Config)
	}
	wantRootPath, err := filepath.EvalSymlinks(notes)
	if err != nil {
		t.Fatalf("EvalSymlinks failed: %v", err)
	}
	if validated.RootPath != wantRootPath {
		t.Fatalf("expected root path %q, got %q", wantRootPath, validated.RootPath)
	}
	if validated.ConfigHash == "" || !strings.HasPrefix(validated.ConfigHash, "sha256:") {
		t.Fatalf("expected config hash, got %q", validated.ConfigHash)
	}
}

func TestValidateRootConfigManagedPolicyUsesAncestorBoundary(t *testing.T) {
	dir := t.TempDir()
	documents := filepath.Join(dir, "Documents")
	if err := os.MkdirAll(documents, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".loomignore"), []byte("private/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	validated, err := ValidateRootConfig(RootConfig{
		RootKey:          "documents",
		SafeRootKey:      "box",
		RootRelativePath: "Documents",
		IgnorePolicy: IgnorePolicy{
			Profile:                "managed",
			DiscoverUserRules:      true,
			PolicyRootRelativePath: ".",
		},
	}, filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{filesystemconnector.DefaultSafeRoot("box", dir)}})
	if err != nil {
		t.Fatal(err)
	}
	if validated.PolicyResolver == nil || validated.Config.Scan.HiddenPolicy != HiddenPolicyPolicyControlled || len(validated.Config.Exclude) != 0 {
		t.Fatalf("unexpected managed config: %#v", validated.Config)
	}
	resolution, err := validated.PolicyResolver.Resolve("private/file.txt", false)
	if err != nil || resolution.Decision.Included {
		t.Fatalf("ancestor policy not applied: %#v err=%v", resolution, err)
	}
}

func TestValidateRootConfigRejectsUnsafeRoot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	outside := t.TempDir()
	safeRoot := filesystemconnector.DefaultSafeRoot("slice09", dir)
	if _, err := NormalizeRootPathInput(outside, safeRoot); err == nil {
		t.Fatal("expected absolute outside path to be rejected")
	}
	_, err := ValidateRootConfig(RootConfig{
		RootKey:          "notes",
		SafeRootKey:      "slice09",
		RootRelativePath: "../outside",
	}, filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{safeRoot}})
	if err == nil {
		t.Fatal("expected traversal path to be rejected")
	}
}

func TestValidateRootConfigFidelityPolicyDefaultsAndValidation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	validated, err := ValidateRootConfig(RootConfig{
		RootKey:     "documents",
		SafeRootKey: "slice09",
	}, filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{
		filesystemconnector.DefaultSafeRoot("slice09", dir),
	}})
	if err != nil {
		t.Fatalf("ValidateRootConfig failed: %v", err)
	}
	policy := validated.Config.FidelityPolicy
	if !policy.ObserveEmptyDirectories ||
		!policy.ObserveSymlinks ||
		policy.FollowSymlinks ||
		!policy.ObserveXattrs ||
		!policy.ObserveACLPresence ||
		policy.PreserveGeneratedAppleMetadata ||
		policy.PackageDirectoryMode != PackageDirectoryModeObserveBoundary ||
		policy.PathCollisionMode != PathCollisionModeWarn ||
		policy.SpecialFileMode != SpecialFileModeObserveSkip ||
		policy.ExecutableBitMode != ExecutableBitModeObserve {
		t.Fatalf("unexpected fidelity defaults %#v", policy)
	}

	_, err = ValidateRootConfig(RootConfig{
		RootKey:     "documents",
		SafeRootKey: "slice09",
		FidelityPolicy: FidelityPolicy{
			PackageDirectoryMode: "unsupported",
		},
	}, filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{
		filesystemconnector.DefaultSafeRoot("slice09", dir),
	}})
	if err == nil {
		t.Fatal("expected unsupported fidelity policy mode rejection")
	}
}

func TestValidateRootConfigRejectsPrivateRootSyncAndInvalidGlob(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	privateRoot := filesystemconnector.DefaultSafeRoot("private", dir)
	privateRoot.PrivateBackupOnly = true
	privateRoot.MaxFileBytes = 8 * 1024 * 1024
	_, err := ValidateRootConfig(RootConfig{
		RootKey:     "private",
		SafeRootKey: "private",
		SyncPolicy:  SyncPolicy{Mode: SyncModeSelectedFiles},
	}, filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{privateRoot}})
	if err == nil {
		t.Fatal("expected private backup-only sync policy rejection")
	}
	_, err = ValidateRootConfig(RootConfig{
		RootKey:     "notes",
		SafeRootKey: "private",
		Include:     []string{"["},
	}, filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{filesystemconnector.DefaultSafeRoot("private", dir)}})
	if err == nil {
		t.Fatal("expected invalid glob rejection")
	}
}

func TestValidateRootConfigBackupPolicyDefaultsAndPrivateRoot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	privateRoot := filesystemconnector.DefaultSafeRoot("private", dir)
	privateRoot.PrivateBackupOnly = true
	privateRoot.MaxFileBytes = 8 * 1024 * 1024
	validated, err := ValidateRootConfig(RootConfig{
		RootKey:     "private",
		SafeRootKey: "private",
		BackupPolicy: BackupPolicy{
			Mode:          BackupModeIncrementalRaw,
			MaxFileBytes:  4 * 1024 * 1024,
			MaxBatchBytes: 4 * 1024 * 1024,
		},
	}, filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{privateRoot}})
	if err != nil {
		t.Fatalf("ValidateRootConfig failed: %v", err)
	}
	if validated.Config.BackupPolicy.Mode != BackupModeIncrementalRaw ||
		validated.Config.BackupPolicy.MaxFileBytes <= 0 ||
		validated.Config.BackupPolicy.MaxBatchBytes <= 0 ||
		validated.Config.BackupPolicy.MaxPendingItems <= 0 ||
		validated.Config.BackupPolicy.MaxPendingBytes <= 0 ||
		validated.Config.BackupPolicy.IncludeDeletionMarkers == nil ||
		!*validated.Config.BackupPolicy.IncludeDeletionMarkers ||
		validated.Config.BackupPolicy.OnLimit != BackupOnLimitDegradeAndRequireManualAction {
		t.Fatalf("unexpected backup policy defaults %#v", validated.Config.BackupPolicy)
	}

	_, err = ValidateRootConfig(RootConfig{
		RootKey:      "private",
		SafeRootKey:  "private",
		BackupPolicy: BackupPolicy{Mode: "unsupported"},
	}, filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{privateRoot}})
	if err == nil {
		t.Fatal("expected unsupported backup mode to be rejected")
	}

	_, err = ValidateRootConfig(RootConfig{
		RootKey:      "private",
		SafeRootKey:  "private",
		BackupPolicy: BackupPolicy{Mode: BackupModeIncrementalRaw},
	}, filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{privateRoot}})
	if err == nil || !strings.Contains(err.Error(), "max_file_bytes must be explicit") {
		t.Fatalf("expected enabled backup policy to require explicit max_file_bytes, got %v", err)
	}
}

func TestValidateRootConfigSyncIndexPolicies(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	root, err := ValidateRootConfig(RootConfig{
		RootKey:     "notes",
		SafeRootKey: "slice10",
		Include:     []string{"**/*.md"},
		SyncPolicy: SyncPolicy{
			Mode:       SyncModeSelectedFiles,
			ProjectRef: "project_test",
		},
		IndexPolicy: IndexPolicy{Mode: IndexModeMarkdownText},
		DeletePolicy: DeletePolicy{
			Mode: DeleteModeTombstone,
		},
	}, filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{
		filesystemconnector.DefaultSafeRoot("slice10", dir),
	}})
	if err != nil {
		t.Fatalf("ValidateRootConfig failed: %v", err)
	}
	if root.Config.SyncPolicy.ProjectRef != "project_test" ||
		root.Config.SyncPolicy.ScopeRef != "" ||
		root.Config.SyncPolicy.MaxFileBytes <= 0 ||
		root.Config.IndexPolicy.MaxTextBytes <= 0 ||
		root.Config.DeletePolicy.Mode != DeleteModeTombstone {
		t.Fatalf("unexpected normalized policies %#v", root.Config)
	}

	_, err = ValidateRootConfig(RootConfig{
		RootKey:     "notes",
		SafeRootKey: "slice10",
		SyncPolicy:  SyncPolicy{Mode: SyncModeSelectedFiles},
	}, filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{
		filesystemconnector.DefaultSafeRoot("slice10", dir),
	}})
	if err == nil {
		t.Fatal("expected selected file sync without project/scope to be rejected")
	}

	_, err = ValidateRootConfig(RootConfig{
		RootKey:     "notes",
		SafeRootKey: "slice10",
		SyncPolicy: SyncPolicy{
			Mode:       SyncModeSelectedFiles,
			ProjectRef: "project_test",
			ScopeRef:   "scope_test",
		},
	}, filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{
		filesystemconnector.DefaultSafeRoot("slice10", dir),
	}})
	if err == nil {
		t.Fatal("expected selected file sync with both project and scope to be rejected")
	}

	_, err = ValidateRootConfig(RootConfig{
		RootKey:     "notes",
		SafeRootKey: "slice10",
		IndexPolicy: IndexPolicy{Mode: IndexModeMarkdownText},
	}, filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{
		filesystemconnector.DefaultSafeRoot("slice10", dir),
	}})
	if err == nil {
		t.Fatal("expected markdown text indexing without selected sync to be rejected")
	}
}
