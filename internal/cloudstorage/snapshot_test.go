package cloudstorage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"loom.local/loom/internal/backupcoverage"
	"loom.local/loom/internal/maintenance"
)

func TestPushSnapshotDryRunVerifiesBackupWithoutRemoteMutation(t *testing.T) {
	t.Parallel()

	dataDir, backupDir := validCloudBackupFixture(t)
	cfg := enabledTestCloudConfig(t, dataDir)
	driver := &recordingSnapshotDriver{}
	result, err := PushSnapshot(context.Background(), SnapshotPushInput{
		Config:          cfg,
		Driver:          driver,
		BackupRef:       backupDir,
		BackupRoot:      filepath.Join(dataDir, "backups", "main"),
		DataDir:         dataDir,
		CoverageOptions: cloudBackupCoverageOptions(dataDir),
		NodeID:          "loom-main",
		DryRun:          true,
		Now:             fixedCloudNow,
	})
	if err != nil {
		t.Fatalf("PushSnapshot returned error: %v", err)
	}
	if result.Status != SnapshotStatusPlanned {
		t.Fatalf("status = %q", result.Status)
	}
	if len(driver.calls) != 0 {
		t.Fatalf("dry-run called remote driver: %#v", driver.calls)
	}
	if result.Verification.Status != maintenance.VerificationSucceeded || result.Coverage.Status != backupcoverage.OverallWarning || result.Coverage.Summary.Unknown != 1 {
		t.Fatalf("verification/coverage unexpected: %#v %#v", result.Verification, result.Coverage)
	}
}

func TestPushSnapshotUploadsTempPromotesAndWritesManifest(t *testing.T) {
	t.Parallel()

	dataDir, backupDir := validCloudBackupFixture(t)
	cfg := enabledTestCloudConfig(t, dataDir)
	driver := &recordingSnapshotDriver{}
	result, err := PushSnapshot(context.Background(), SnapshotPushInput{
		Config:          cfg,
		Driver:          driver,
		BackupRef:       backupDir,
		BackupRoot:      filepath.Join(dataDir, "backups", "main"),
		DataDir:         dataDir,
		CoverageOptions: cloudBackupCoverageOptions(dataDir),
		NodeID:          "loom-main",
		Now:             fixedCloudNow,
	})
	if err != nil {
		t.Fatalf("PushSnapshot returned error: %v", err)
	}
	if result.Status != SnapshotStatusSucceeded {
		t.Fatalf("status = %q error=%q checks=%#v", result.Status, result.Error, result.Checks)
	}
	if result.Manifest == nil || result.Manifest.SchemaVersion != SnapshotUploadManifestSchema {
		t.Fatalf("manifest = %#v", result.Manifest)
	}
	if result.Manifest.SourceBackupPaths.StorageRetention != "storage-retention" {
		t.Fatalf("manifest retention path = %q, want storage-retention", result.Manifest.SourceBackupPaths.StorageRetention)
	}
	if result.Manifest.SourceBackupPaths.UserBackups != "user-backups" || result.Manifest.SourceBackupPaths.PrivateBackups != "" {
		t.Fatalf("cloud manifest did not preserve canonical user backup custody: %#v", result.Manifest.SourceBackupPaths)
	}
	joinedCalls := strings.Join(driver.calls, "\n")
	for _, want := range []string{"copy_to:" + backupDir + "->_system/tmp/snapshot-uploads/", "check:" + backupDir + "->_system/tmp/snapshot-uploads/", "move:_system/tmp/snapshot-uploads/", "copy_to:" + result.LocalManifestPath + "->main-snapshots/loom-main/"} {
		if !strings.Contains(joinedCalls, want) {
			t.Fatalf("calls do not contain %q:\n%s", want, joinedCalls)
		}
	}
	if _, err := ReadSnapshotUploadManifest(result.LocalManifestPath); err != nil {
		t.Fatalf("local cloud upload manifest invalid: %v", err)
	}
	if !driver.copyToPreserveLinks || !driver.checkPreserveLinks || !driver.copyToPreserveMetadata || !driver.checkPreserveMetadata {
		t.Fatalf("legacy-tree upload did not preserve links and metadata: driver=%#v", driver)
	}
}

func TestLegacyTreePushRejectsSymlinkedSourceManifestBeforeRemoteUpload(t *testing.T) {
	dataDir, backupDir := validCloudBackupFixture(t)
	manifestPath := filepath.Join(backupDir, "manifest.json")
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
	cfg := enabledTestCloudConfig(t, dataDir)
	driver := &recordingSnapshotDriver{}
	result, err := PushSnapshot(context.Background(), SnapshotPushInput{
		Config:          cfg,
		Driver:          driver,
		BackupRef:       backupDir,
		BackupRoot:      filepath.Join(dataDir, "backups", "main"),
		DataDir:         dataDir,
		CoverageOptions: cloudBackupCoverageOptions(dataDir),
		NodeID:          "loom-main",
		Now:             fixedCloudNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != SnapshotStatusFailed || result.Verification.Status != maintenance.VerificationFailed || len(driver.calls) != 0 {
		t.Fatalf("symlinked source manifest push = %#v calls=%#v", result, driver.calls)
	}
}

func TestLegacyTreePushRejectsSourceManifestChangeAfterFinalCheck(t *testing.T) {
	dataDir, backupDir := validCloudBackupFixture(t)
	cfg := enabledTestCloudConfig(t, dataDir)
	driver := &recordingSnapshotDriver{}
	backend := LegacyTreeSnapshotBackend{
		Driver: driver,
		afterFinalRemoteCheck: func(manifestPath string) error {
			manifest, err := maintenance.ReadBackupManifest(manifestPath)
			if err != nil {
				return err
			}
			manifest.Source.Hostname = "changed-after-final-remote-check"
			payload, err := json.MarshalIndent(manifest, "", "  ")
			if err != nil {
				return err
			}
			return os.WriteFile(manifestPath, append(payload, '\n'), 0o640)
		},
	}
	result, err := backend.Push(context.Background(), SnapshotPushInput{
		Config:          cfg,
		BackupRef:       backupDir,
		BackupRoot:      filepath.Join(dataDir, "backups", "main"),
		DataDir:         dataDir,
		CoverageOptions: cloudBackupCoverageOptions(dataDir),
		NodeID:          "loom-main",
		Now:             fixedCloudNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != SnapshotStatusFailed || result.Checks["source_backup_manifest_stable"] != SnapshotStatusFailed || !strings.Contains(result.Error, "changed during cloud upload") {
		t.Fatalf("manifest race result = %#v", result)
	}
	for _, call := range driver.calls {
		if strings.Contains(call, "cloud-upload.json") {
			t.Fatalf("manifest race published cloud success evidence: %#v", driver.calls)
		}
	}
}

func TestLatestVerifiedRetentionCoverageUsesLocalSnapshotManifest(t *testing.T) {
	t.Parallel()

	dataDir, backupDir := validCloudBackupFixture(t)
	cfg := enabledTestCloudConfig(t, dataDir)
	driver := &recordingSnapshotDriver{}
	result, err := PushSnapshot(context.Background(), SnapshotPushInput{
		Config:          cfg,
		Driver:          driver,
		BackupRef:       backupDir,
		BackupRoot:      filepath.Join(dataDir, "backups", "main"),
		DataDir:         dataDir,
		CoverageOptions: cloudBackupCoverageOptions(dataDir),
		NodeID:          "loom-main",
		Now:             fixedCloudNow,
	})
	if err != nil {
		t.Fatalf("PushSnapshot returned error: %v", err)
	}
	if result.Status != SnapshotStatusSucceeded {
		t.Fatalf("push status = %q", result.Status)
	}
	coverage, err := LatestVerifiedRetentionCoverage(context.Background(), cfg)
	if err != nil {
		t.Fatalf("LatestVerifiedRetentionCoverage returned error: %v", err)
	}
	if !coverage.Confirmed || !coverage.CoversStorageRetention || coverage.Ref == "" {
		t.Fatalf("coverage = %#v", coverage)
	}
}

func TestFetchSnapshotCopiesAndVerifiesDownloadedBackup(t *testing.T) {
	t.Parallel()

	cfg := enabledTestCloudConfig(t, t.TempDir())
	backupDir := validLegacyTreeRemoteFixture(t, cfg, "loom-main", "20260614T100000Z-smoke")
	driver := &recordingSnapshotDriver{copyFromDir: backupDir, listEntries: []RemoteEntry{{Path: "20260614T100000Z-smoke", IsDir: true}}, afterCopyFrom: truncateLegacyTreeImportsMetadata}
	target := filepath.Join(t.TempDir(), "fetch")
	result, err := FetchSnapshot(context.Background(), SnapshotFetchInput{
		Config: cfg,
		Driver: driver,
		NodeID: "loom-main",
		Ref:    "latest",
		To:     target,
	})
	if err != nil {
		t.Fatalf("FetchSnapshot returned error: %v", err)
	}
	if result.Status != SnapshotStatusSucceeded || result.Verification.Status != maintenance.VerificationSucceeded {
		t.Fatalf("result = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(target, "manifest.json")); err != nil {
		t.Fatalf("downloaded manifest missing: %v", err)
	}
	if !driver.copyFromPreserveLinks || !driver.copyFromPreserveMetadata {
		t.Fatal("legacy-tree fetch did not request link and metadata preservation")
	}
	if targetValue, err := os.Readlink(filepath.Join(target, "imports", "node-a", "2026-06-14", "batch-a", "payload-link")); err != nil || targetValue != "payload.bin" {
		t.Fatalf("restored Imports symlink = %q err=%v", targetValue, err)
	}
	if targetValue, err := os.Readlink(filepath.Join(target, "imports", "node-a", "2026-06-14", "batch-a", "absolute-link")); err != nil || targetValue != "/Users/archive/Absolute Target " {
		t.Fatalf("restored absolute Imports symlink = %q err=%v", targetValue, err)
	}
	restored, err := os.Lstat(filepath.Join(target, "imports", "node-a", "2026-06-14", "batch-a", "payload.bin"))
	if err != nil || restored.Mode().Perm() != 0o640 || !restored.ModTime().UTC().Equal(time.Date(2026, 6, 14, 9, 55, 0, 123_000_000, time.UTC)) {
		t.Fatalf("restored Imports metadata = %#v err=%v", restored, err)
	}
}

func TestFetchSnapshotRejectsTamperedImportsBeforeMetadataRehydration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(string) error
	}{
		{name: "evidence", mutate: func(target string) error {
			pathValue := filepath.Join(target, "imports-evidence.json")
			file, err := os.OpenFile(pathValue, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				return err
			}
			_, writeErr := file.WriteString(" \n")
			closeErr := file.Close()
			if writeErr != nil {
				return writeErr
			}
			return closeErr
		}},
		{name: "payload", mutate: func(target string) error {
			return os.WriteFile(filepath.Join(target, "imports", "node-a", "2026-06-14", "batch-a", "payload.bin"), []byte("tampered cloud payload"), 0o644)
		}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := enabledTestCloudConfig(t, t.TempDir())
			backupDir := validLegacyTreeRemoteFixture(t, cfg, "loom-main", "20260614T100000Z-smoke")
			driver := &recordingSnapshotDriver{
				copyFromDir: backupDir,
				listEntries: []RemoteEntry{{Path: "20260614T100000Z-smoke", IsDir: true}},
				afterCopyFrom: func(target string) error {
					if err := truncateLegacyTreeImportsMetadata(target); err != nil {
						return err
					}
					return testCase.mutate(target)
				},
			}
			if _, err := FetchSnapshot(context.Background(), SnapshotFetchInput{Config: cfg, Driver: driver, NodeID: "loom-main", Ref: "latest", To: filepath.Join(t.TempDir(), "fetch")}); err == nil {
				t.Fatal("tampered legacy-tree Imports restore returned success")
			}
		})
	}
}

func TestLegacyTreeFetchAuthenticatesUploadAndBackupManifestBeforeMetadata(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{name: "tampered backup manifest", mutate: func(t *testing.T, remote string) {
			file, err := os.OpenFile(filepath.Join(remote, "manifest.json"), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.WriteString(" \n"); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "wrong backend", mutate: func(t *testing.T, remote string) {
			rewriteLegacyTreeUploadFixture(t, remote, func(manifest *SnapshotUploadManifest) { manifest.Backend = SnapshotBackendBorg })
		}},
		{name: "wrong snapshot ref", mutate: func(t *testing.T, remote string) {
			rewriteLegacyTreeUploadFixture(t, remote, func(manifest *SnapshotUploadManifest) { manifest.SnapshotRef = "different" })
		}},
		{name: "wrong remote prefix", mutate: func(t *testing.T, remote string) {
			rewriteLegacyTreeUploadFixture(t, remote, func(manifest *SnapshotUploadManifest) { manifest.RemotePrefix = "main-snapshots/loom-main/different" })
		}},
		{name: "wrong source manifest hash", mutate: func(t *testing.T, remote string) {
			rewriteLegacyTreeUploadFixture(t, remote, func(manifest *SnapshotUploadManifest) { manifest.SourceBackupManifestSHA256 = strings.Repeat("0", 64) })
		}},
		{name: "missing current source manifest hash", mutate: func(t *testing.T, remote string) {
			rewriteLegacyTreeUploadFixture(t, remote, func(manifest *SnapshotUploadManifest) { manifest.SourceBackupManifestSHA256 = "" })
		}},
		{name: "symlinked upload manifest", mutate: func(t *testing.T, remote string) {
			pathValue := filepath.Join(remote, "cloud-upload.json")
			external := filepath.Join(t.TempDir(), "cloud-upload.json")
			payload, err := os.ReadFile(pathValue)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(external, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(pathValue); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(external, pathValue); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlinked backup manifest", mutate: func(t *testing.T, remote string) {
			pathValue := filepath.Join(remote, "manifest.json")
			external := filepath.Join(t.TempDir(), "manifest.json")
			payload, err := os.ReadFile(pathValue)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(external, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(pathValue); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(external, pathValue); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := enabledTestCloudConfig(t, t.TempDir())
			remote := validLegacyTreeRemoteFixture(t, cfg, "loom-main", "20260614T100000Z-smoke")
			testCase.mutate(t, remote)
			driver := &recordingSnapshotDriver{copyFromDir: remote, listEntries: []RemoteEntry{{Path: "20260614T100000Z-smoke", IsDir: true}}, afterCopyFrom: truncateLegacyTreeImportsMetadata}
			metadataCalls := 0
			backend := LegacyTreeSnapshotBackend{Driver: driver, beforeMetadataRehydration: func(string) error {
				metadataCalls++
				return nil
			}}
			target := filepath.Join(t.TempDir(), "fetch")
			if _, err := backend.Fetch(context.Background(), SnapshotFetchInput{Config: cfg, Driver: driver, NodeID: "loom-main", Ref: "latest", To: target}); err == nil {
				t.Fatal("unauthenticated legacy-tree snapshot returned success")
			}
			if metadataCalls != 0 {
				t.Fatalf("metadata rehydration hook ran %d times before authentication", metadataCalls)
			}
			if info, err := os.Lstat(filepath.Join(target, "imports", "node-a", "2026-06-14", "batch-a", "payload.bin")); err == nil && info.Mode().Perm() == 0o640 && info.ModTime().Nanosecond() != 0 {
				t.Fatalf("payload metadata was rehydrated before upload authentication: %#v", info)
			}
		})
	}
}

func TestLegacyTreeVerifyAuthenticatesRemoteBackupManifest(t *testing.T) {
	cfg := enabledTestCloudConfig(t, t.TempDir())
	remote := validLegacyTreeRemoteFixture(t, cfg, "loom-main", "20260614T100000Z-smoke")
	driver := &recordingSnapshotDriver{copyFromDir: remote, listEntries: []RemoteEntry{{Path: "20260614T100000Z-smoke", IsDir: true}}}
	backend := LegacyTreeSnapshotBackend{Driver: driver}
	result, err := backend.Verify(context.Background(), SnapshotVerifyInput{Config: cfg, Driver: driver, NodeID: "loom-main", Ref: "latest", StateDir: t.TempDir(), Now: fixedCloudNow})
	if err != nil || result.Status != SnapshotStatusSucceeded {
		t.Fatalf("verified legacy-tree snapshot = %#v err=%v", result, err)
	}
	if err := os.WriteFile(filepath.Join(remote, "manifest.json"), []byte(`{"tampered":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err = backend.Verify(context.Background(), SnapshotVerifyInput{Config: cfg, Driver: driver, NodeID: "loom-main", Ref: "latest", StateDir: t.TempDir(), Now: fixedCloudNow})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != SnapshotStatusFailed || result.Checks["source_backup_manifest"] != SnapshotStatusFailed {
		t.Fatalf("tampered remote manifest verification = %#v", result)
	}
}

func TestPushSnapshotBorgBackendRequiresRepositoryConfig(t *testing.T) {
	t.Parallel()

	cfg, err := NormalizeConfig(Config{
		SchemaVersion: ConfigSchemaVersion,
		Enabled:       true,
		Snapshots:     SnapshotsConfig{Backend: SnapshotBackendBorg},
	})
	if err != nil {
		t.Fatalf("NormalizeConfig returned error: %v", err)
	}
	_, err = PushSnapshot(context.Background(), SnapshotPushInput{Config: cfg})
	if err == nil || !strings.Contains(err.Error(), "borg repository is required") {
		t.Fatalf("PushSnapshot error = %v, want missing borg repository", err)
	}
}

func TestBorgBackendPushCreatesArchiveAndWritesManifest(t *testing.T) {
	t.Parallel()

	dataDir, backupDir := validCloudBackupFixture(t)
	cfg := enabledTestCloudConfig(t, dataDir)
	passphrasePath := filepath.Join(t.TempDir(), "borg.passphrase")
	if err := os.WriteFile(passphrasePath, []byte("do-not-leak-this-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Snapshots = SnapshotsConfig{
		Backend: SnapshotBackendBorg,
		Borg: BorgConfig{
			Binary:         "borg",
			Repository:     filepath.Join(t.TempDir(), "repo"),
			PassphraseFile: passphrasePath,
			CacheDir:       filepath.Join(t.TempDir(), "cache"),
			SecurityDir:    filepath.Join(t.TempDir(), "security"),
			Encryption:     DefaultBorgEncryption,
			Compression:    "zstd,3",
			CheckMode:      "repository",
		},
	}
	normalized, err := NormalizeConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	calls := []string{}
	execFunc := func(ctx context.Context, binary string, command BorgCommand, env []string) ([]byte, error) {
		joinedArgs := strings.Join(command.Args, " ")
		joinedEnv := strings.Join(env, "\n")
		if strings.Contains(joinedArgs, "do-not-leak-this-secret") || strings.Contains(joinedEnv, "do-not-leak-this-secret") {
			t.Fatalf("borg command leaked passphrase: args=%q env=%q", joinedArgs, joinedEnv)
		}
		if !strings.Contains(joinedEnv, "BORG_PASSCOMMAND=") || !strings.Contains(joinedEnv, passphrasePath) {
			t.Fatalf("borg passcommand missing passphrase file path: %q", joinedEnv)
		}
		name := command.Name()
		calls = append(calls, name+":"+joinedArgs+":dir="+command.Dir)
		switch name {
		case "create":
			if command.Dir != backupDir {
				t.Fatalf("create dir = %q, want backup dir %q", command.Dir, backupDir)
			}
			return []byte(`{"archive":{"stats":{"compressed_size":1234,"deduplicated_size":567}}}`), nil
		case "info", "check":
			return []byte(`{}`), nil
		default:
			t.Fatalf("unexpected borg command: %#v", command.Args)
			return nil, nil
		}
	}
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: normalized, Exec: execFunc}}
	result, err := backend.Push(context.Background(), SnapshotPushInput{
		Config:          normalized,
		BackupRef:       backupDir,
		BackupRoot:      filepath.Join(dataDir, "backups", "main"),
		DataDir:         dataDir,
		CoverageOptions: cloudBackupCoverageOptions(dataDir),
		NodeID:          "loom-main",
		Now:             fixedCloudNow,
	})
	if err != nil {
		t.Fatalf("Push returned error: %v", err)
	}
	if result.Status != SnapshotStatusSucceeded || result.Backend != SnapshotBackendBorg {
		t.Fatalf("result = %#v", result)
	}
	if result.Archive != "loom-main-20260614T100000Z-smoke" {
		t.Fatalf("archive = %q", result.Archive)
	}
	if result.Manifest == nil || result.Manifest.Backend != SnapshotBackendBorg || result.Manifest.Archive != result.Archive {
		t.Fatalf("manifest = %#v", result.Manifest)
	}
	if result.Manifest.SourceBackupManifestSHA256 == "" {
		t.Fatal("source backup manifest checksum missing")
	}
	joinedCalls := strings.Join(calls, "\n")
	for _, want := range []string{"create:--lock-wait 5 create --json --stats --compression zstd,3 ::loom-main-20260614T100000Z-smoke .", "info:--lock-wait 5 info --json ::loom-main-20260614T100000Z-smoke", "check:--lock-wait 5 check --repository-only"} {
		if !strings.Contains(joinedCalls, want) {
			t.Fatalf("calls missing %q:\n%s", want, joinedCalls)
		}
	}
	if _, err := ReadSnapshotUploadManifest(result.LocalManifestPath); err != nil {
		t.Fatalf("local borg cloud upload manifest invalid: %v", err)
	}
}

func TestBorgBackendFetchPreservesStrictImportsEvidence(t *testing.T) {
	_, backupDir := validCloudBackupFixture(t)
	cfg := testBorgStatusConfig(t)
	archive := "loom-main-20260614T100000Z-smoke"
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: cfg, Exec: func(ctx context.Context, binary string, command BorgCommand, env []string) ([]byte, error) {
		switch command.Name() {
		case "list":
			return []byte(`{"archives":[{"name":"` + archive + `","time":"2026-06-14T10:00:00Z"}]}`), nil
		case "extract":
			if command.Dir == "" {
				t.Fatal("Borg extract omitted target directory")
			}
			if err := copyDirForTest(backupDir, command.Dir); err != nil {
				return nil, err
			}
			return []byte("extracted"), nil
		default:
			t.Fatalf("unexpected Borg fetch command: %#v", command.Args)
			return nil, nil
		}
	}}}
	target := filepath.Join(t.TempDir(), "borg-fetch")
	result, err := backend.Fetch(context.Background(), SnapshotFetchInput{Config: cfg, NodeID: "loom-main", Ref: "latest", To: target})
	if err != nil {
		t.Fatalf("Borg Fetch returned error: %v", err)
	}
	if result.Status != SnapshotStatusSucceeded || result.Verification.Status != maintenance.VerificationSucceeded {
		t.Fatalf("Borg strict Imports restore = %#v", result)
	}
	payloadInfo, err := os.Lstat(filepath.Join(target, "imports", "node-a", "2026-06-14", "batch-a", "payload.bin"))
	if err != nil || payloadInfo.Mode().Perm() != 0o640 || !payloadInfo.ModTime().UTC().Equal(time.Date(2026, 6, 14, 9, 55, 0, 123_000_000, time.UTC)) {
		t.Fatalf("Borg restored Imports metadata = %#v err=%v", payloadInfo, err)
	}
	if linkTarget, err := os.Readlink(filepath.Join(target, "imports", "node-a", "2026-06-14", "batch-a", "payload-link")); err != nil || linkTarget != "payload.bin" {
		t.Fatalf("Borg restored Imports symlink = %q err=%v", linkTarget, err)
	}
	if linkTarget, err := os.Readlink(filepath.Join(target, "imports", "node-a", "2026-06-14", "batch-a", "absolute-link")); err != nil || linkTarget != "/Users/archive/Absolute Target " {
		t.Fatalf("Borg restored absolute Imports symlink = %q err=%v", linkTarget, err)
	}
}

func TestBorgBackendListReportsUninitializedRepository(t *testing.T) {
	t.Parallel()

	cfg := testBorgStatusConfig(t)
	backend := BorgSnapshotBackend{Runner: BorgCommandRunner{Config: cfg, Exec: func(ctx context.Context, binary string, command BorgCommand, env []string) ([]byte, error) {
		if command.Name() != "list" {
			t.Fatalf("command = %#v, want list", command.Args)
		}
		return nil, errors.New("Repository /borg/loom-main does not exist")
	}}}

	result, err := backend.List(context.Background(), SnapshotListInput{Config: cfg, NodeID: "main"})
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if result.Status != SnapshotListStatusUninitialized || result.Code != SnapshotBackendUninitializedCode {
		t.Fatalf("result = %#v", result)
	}
	if result.Backend != SnapshotBackendBorg || result.Repository != cfg.Snapshots.Borg.Repository || result.RemoteRoot == "" {
		t.Fatalf("missing Borg metadata in result: %#v", result)
	}
	if !strings.Contains(result.RepairHint, "backend init --confirm") {
		t.Fatalf("repair hint = %q", result.RepairHint)
	}
}

func TestInitializeSnapshotBackendRunsBorgInitAfterUninitializedStatus(t *testing.T) {
	t.Parallel()

	cfg := testBorgStatusConfig(t)
	calls := []string{}
	result, err := InitializeSnapshotBackend(context.Background(), SnapshotBackendInitInput{
		Config:  cfg,
		Confirm: true,
		Now:     fixedCloudNow,
		Runner: BorgCommandRunner{Config: cfg, Exec: func(ctx context.Context, binary string, command BorgCommand, env []string) ([]byte, error) {
			calls = append(calls, command.Name()+":"+strings.Join(command.Args, " "))
			switch command.Name() {
			case "list":
				return nil, errors.New("Repository /borg/loom-main does not exist")
			case "init":
				return []byte(`{}`), nil
			default:
				t.Fatalf("unexpected Borg command: %#v", command.Args)
				return nil, nil
			}
		}},
	})
	if err != nil {
		t.Fatalf("InitializeSnapshotBackend returned error: %v", err)
	}
	if result.Status != SnapshotStatusSucceeded || !result.Initialized {
		t.Fatalf("result = %#v", result)
	}
	joined := strings.Join(calls, "\n")
	for _, want := range []string{"list:", "init:"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("calls missing %q:\n%s", want, joined)
		}
	}
	if cached, ok := readSnapshotBackendStatusCache(cfg, fixedCloudNow().Add(time.Minute)); !ok || !cached.Initialized || cached.Status != "ok" {
		t.Fatalf("backend status cache = %#v ok=%t", cached, ok)
	}
}

type recordingSnapshotDriver struct {
	calls                    []string
	copyFromDir              string
	listEntries              []RemoteEntry
	copyToPreserveLinks      bool
	copyFromPreserveLinks    bool
	checkPreserveLinks       bool
	copyToPreserveMetadata   bool
	copyFromPreserveMetadata bool
	checkPreserveMetadata    bool
	afterCopyFrom            func(string) error
	afterCopyTo              func(string, string) error
}

func (d *recordingSnapshotDriver) Status(ctx context.Context) (RemoteStatus, error) {
	return RemoteStatus{Reachable: true, RemoteURI: "loom-cloud:loom", CheckedAt: fixedCloudNow(), Entries: len(d.listEntries)}, nil
}

func (d *recordingSnapshotDriver) List(ctx context.Context, prefix string) ([]RemoteEntry, error) {
	d.calls = append(d.calls, "list:"+prefix)
	if len(d.listEntries) > 0 {
		return d.listEntries, nil
	}
	return []RemoteEntry{
		{Path: "main-snapshots", IsDir: true},
		{Path: "full-offload", IsDir: true},
		{Path: "cloud-folder", IsDir: true},
	}, nil
}

func (d *recordingSnapshotDriver) CopyToRemote(ctx context.Context, localPath, remotePath string, opts CopyOptions) (CopyResult, error) {
	d.copyToPreserveLinks = d.copyToPreserveLinks || opts.PreserveLinks
	d.copyToPreserveMetadata = d.copyToPreserveMetadata || opts.PreserveMetadata
	d.calls = append(d.calls, "copy_to:"+localPath+"->"+remotePath)
	if d.afterCopyTo != nil {
		if err := d.afterCopyTo(localPath, remotePath); err != nil {
			return CopyResult{}, err
		}
	}
	return CopyResult{Command: "copy", Source: localPath, Dest: "loom-cloud:loom/" + remotePath, FinishedAt: fixedCloudNow()}, nil
}

func (d *recordingSnapshotDriver) CopyFromRemote(ctx context.Context, remotePath, localPath string, opts CopyOptions) (CopyResult, error) {
	d.copyFromPreserveLinks = d.copyFromPreserveLinks || opts.PreserveLinks
	d.copyFromPreserveMetadata = d.copyFromPreserveMetadata || opts.PreserveMetadata
	d.calls = append(d.calls, "copy_from:"+remotePath+"->"+localPath)
	if d.copyFromDir != "" {
		if opts.SingleFile {
			payload, err := os.ReadFile(filepath.Join(d.copyFromDir, filepath.Base(remotePath)))
			if err != nil {
				return CopyResult{}, err
			}
			if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
				return CopyResult{}, err
			}
			if err := os.WriteFile(localPath, payload, 0o600); err != nil {
				return CopyResult{}, err
			}
		} else if err := copyDirForTest(d.copyFromDir, localPath); err != nil {
			return CopyResult{}, err
		}
	}
	if d.afterCopyFrom != nil && !opts.SingleFile {
		if err := d.afterCopyFrom(localPath); err != nil {
			return CopyResult{}, err
		}
	}
	return CopyResult{Command: "copy", Source: "loom-cloud:loom/" + remotePath, Dest: localPath, FinishedAt: fixedCloudNow()}, nil
}

func (d *recordingSnapshotDriver) Check(ctx context.Context, localPath, remotePath string) (CheckResult, error) {
	d.calls = append(d.calls, "check:"+localPath+"->"+remotePath)
	return CheckResult{Matched: true, LocalPath: localPath, RemotePath: "loom-cloud:loom/" + remotePath, CheckedAt: fixedCloudNow()}, nil
}

func (d *recordingSnapshotDriver) CheckWithOptions(ctx context.Context, localPath, remotePath string, opts CheckOptions) (CheckResult, error) {
	d.checkPreserveLinks = d.checkPreserveLinks || opts.PreserveLinks
	d.checkPreserveMetadata = d.checkPreserveMetadata || opts.PreserveMetadata
	return d.Check(ctx, localPath, remotePath)
}

func (d *recordingSnapshotDriver) MoveRemote(ctx context.Context, fromRemotePath, toRemotePath string, opts CopyOptions) (CopyResult, error) {
	d.calls = append(d.calls, "move:"+fromRemotePath+"->"+toRemotePath)
	return CopyResult{Command: "moveto", Source: "loom-cloud:loom/" + fromRemotePath, Dest: "loom-cloud:loom/" + toRemotePath, FinishedAt: fixedCloudNow()}, nil
}

func validCloudBackupFixture(t *testing.T) (string, string) {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "data")
	boxRoot := filepath.Join(filepath.Dir(dataDir), "custom-box")
	backupDir := filepath.Join(dataDir, "backups", "main", "20260614T100000Z-smoke")
	for _, dir := range []string{
		filepath.Join(dataDir, "object-store"),
		filepath.Join(dataDir, "imports"),
		filepath.Join(dataDir, "user-backups"),
		filepath.Join(boxRoot, "Documents"),
		filepath.Join(boxRoot, "Notes"),
		filepath.Join(dataDir, "storage-retention", "main-documents", "by-sha256"),
		filepath.Join(dataDir, "storage-archive"),
		filepath.Join(dataDir, "generated", "notes"),
		filepath.Join(backupDir, "object-store"),
		filepath.Join(backupDir, "user-backups"),
		filepath.Join(backupDir, "main-documents"),
		filepath.Join(backupDir, "box-notes"),
		filepath.Join(backupDir, "storage-retention", "main-documents", "by-sha256"),
		filepath.Join(backupDir, "storage-archive"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	writeCloudTestFile(t, filepath.Join(boxRoot, "Documents", ".loom-acceptance.md"), []byte("hello\n"))
	writeCloudTestFile(t, filepath.Join(boxRoot, "Notes", "source.md"), []byte("note\n"))
	writeCloudTestFile(t, filepath.Join(dataDir, "storage-retention", "main-documents", "by-sha256", "payload"), []byte("hello\n"))
	writeCloudTestFile(t, filepath.Join(backupDir, "storage-retention", "main-documents", "by-sha256", "payload"), []byte("hello\n"))
	writeCloudTestFile(t, filepath.Join(backupDir, "health.json"), []byte(`{"status":"ok"}`+"\n"))
	writeCloudTestFile(t, filepath.Join(backupDir, "loom_main.dump"), []byte("dump\n"))
	writeCloudImportsFixture(t, backupDir)
	importsEvidenceHash, importsEvidenceSize, err := maintenance.HashFile(filepath.Join(backupDir, "imports-evidence.json"))
	if err != nil {
		t.Fatal(err)
	}
	current := int64(1)
	manifest := maintenance.BackupManifest{
		Schema:     maintenance.BackupManifestSchemaV09,
		BackupKind: "loom_main_state",
		CreatedAt:  "2026-06-14T10:00:00Z",
		Source: maintenance.BackupManifestSource{
			Hostname:    "loom-main",
			NodeID:      "main",
			NodeRole:    "main",
			Environment: "production",
		},
		Loom: maintenance.BackupManifestLoom{CurrentMigration: &current, LatestMigration: &current},
		Database: maintenance.BackupManifestDatabase{
			Name:     "loom_main",
			DumpFile: "loom_main.dump",
			Format:   "pg_dump_custom",
		},
		Paths: maintenance.BackupManifestPaths{
			ObjectStore:      "object-store",
			Imports:          "imports",
			ImportsEvidence:  "imports-evidence.json",
			UserBackups:      "user-backups",
			MainDocuments:    "main-documents",
			StorageRetention: "storage-retention",
			StorageArchive:   "storage-archive",
		},
		Artifacts: []maintenance.BackupManifestArtifact{
			{Kind: "box_notes_snapshot", Path: "box-notes"},
			{Kind: maintenance.ArtifactKindImportsSnapshot, Path: "imports"},
			{Kind: maintenance.ArtifactKindImportsEvidence, Path: "imports-evidence.json", SizeBytes: &importsEvidenceSize, SHA256: importsEvidenceHash},
		},
		Exclusions: []string{"generated_notes_projection"},
		Policies: maintenance.BackupManifestPolicies{
			MainBox:          "selected_canonical_roots_copied_once",
			Imports:          maintenance.ImportsBackupPolicyCanonical,
			ImportsSnapshot:  maintenance.ImportsSnapshotSharedStore,
			NotesSourceRoots: "box_notes_snapshot_and_project_notes_private_backups_or_watched_roots",
			NotesProjection:  "excluded_rebuildable_generated_output",
			KnowledgeIndex:   "captured_by_postgres_dump_as_knowledge_schema_tables",
		},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeCloudTestFile(t, filepath.Join(backupDir, "manifest.json"), raw)
	return dataDir, backupDir
}

func validLegacyTreeRemoteFixture(t *testing.T, cfg Config, nodeID, ref string) string {
	t.Helper()
	_, backupDir := validCloudBackupFixture(t)
	manifestHash, err := fileSHA256(filepath.Join(backupDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	prefix := path.Join(cfg.Roots.MainSnapshots, safeRemoteSegment(nodeID), ref)
	upload := SnapshotUploadManifest{
		SchemaVersion:              SnapshotUploadManifestSchema,
		UploadID:                   "upload-fixture",
		Backend:                    SnapshotBackendLegacyTree,
		SourceBackupManifestSHA256: manifestHash,
		RemoteURI:                  cfg.RemoteURI(prefix),
		RemotePrefix:               prefix,
		SnapshotRef:                ref,
		CreatedAt:                  fixedCloudNow(),
		CompletedAt:                fixedCloudNow(),
		Driver:                     "rclone_sftp",
		VerifyBeforeUpload:         SnapshotStatusSucceeded,
		VerifyAfterUpload:          SnapshotStatusSucceeded,
		Checks:                     map[string]string{"remote_check_final": SnapshotStatusSucceeded},
	}
	if err := WriteSnapshotUploadManifest(filepath.Join(backupDir, "cloud-upload.json"), upload); err != nil {
		t.Fatal(err)
	}
	return backupDir
}

func rewriteLegacyTreeUploadFixture(t *testing.T, remote string, mutate func(*SnapshotUploadManifest)) {
	t.Helper()
	pathValue := filepath.Join(remote, "cloud-upload.json")
	manifest, err := ReadSnapshotUploadManifest(pathValue)
	if err != nil {
		t.Fatal(err)
	}
	mutate(&manifest)
	if err := WriteSnapshotUploadManifest(pathValue, manifest); err != nil {
		t.Fatal(err)
	}
}

func truncateLegacyTreeImportsMetadata(target string) error {
	root := filepath.Join(target, "imports")
	var paths []string
	err := filepath.WalkDir(root, func(pathValue string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if pathValue == root {
			return nil
		}
		paths = append(paths, pathValue)
		return nil
	})
	if err != nil {
		return err
	}
	for index := len(paths) - 1; index >= 0; index-- {
		pathValue := paths[index]
		info, err := os.Lstat(pathValue)
		if err != nil {
			return err
		}
		truncated := info.ModTime().UTC().Truncate(time.Second)
		if info.Mode()&os.ModeSymlink != 0 {
			times := []unix.Timespec{unix.NsecToTimespec(truncated.UnixNano()), unix.NsecToTimespec(truncated.UnixNano())}
			if err := unix.UtimesNanoAt(unix.AT_FDCWD, pathValue, times, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return err
			}
			continue
		}
		mode := os.FileMode(0o755)
		if info.Mode().IsRegular() {
			mode = 0o644
		}
		if err := os.Chmod(pathValue, mode); err != nil {
			return err
		}
		if err := os.Chtimes(pathValue, truncated, truncated); err != nil {
			return err
		}
	}
	return nil
}

type cloudLaneManifestEntry struct {
	RelativePath string    `json:"relative_path"`
	Kind         string    `json:"kind"`
	SizeBytes    int64     `json:"size_bytes,omitempty"`
	Mode         uint32    `json:"mode"`
	ModifiedAt   time.Time `json:"modified_at"`
	SHA256       string    `json:"sha256,omitempty"`
	LinkTarget   string    `json:"link_target,omitempty"`
}

func writeCloudImportsFixture(t *testing.T, backupDir string) {
	t.Helper()
	batchRoot := filepath.Join(backupDir, "imports", "node-a", "2026-06-14", "batch-a")
	if err := os.MkdirAll(batchRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	payload := []byte("cloud Imports payload")
	payloadPath := filepath.Join(batchRoot, "payload.bin")
	modified := time.Date(2026, 6, 14, 9, 55, 0, 123_000_000, time.UTC)
	if err := os.WriteFile(payloadPath, payload, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(payloadPath, modified, modified); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(batchRoot, "payload-link")
	if err := os.Symlink("payload.bin", linkPath); err != nil {
		t.Fatal(err)
	}
	linkInfo, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	absoluteLinkPath := filepath.Join(batchRoot, "absolute-link")
	absoluteTarget := "/Users/archive/Absolute Target "
	if err := os.Symlink(absoluteTarget, absoluteLinkPath); err != nil {
		t.Fatal(err)
	}
	absoluteLinkInfo, err := os.Lstat(absoluteLinkPath)
	if err != nil {
		t.Fatal(err)
	}
	payloadDigest := sha256.Sum256(payload)
	entries := []cloudLaneManifestEntry{
		{RelativePath: "absolute-link", Kind: "symlink", Mode: uint32(absoluteLinkInfo.Mode().Perm()), ModifiedAt: absoluteLinkInfo.ModTime().UTC(), LinkTarget: absoluteTarget},
		{RelativePath: "payload-link", Kind: "symlink", Mode: uint32(linkInfo.Mode().Perm()), ModifiedAt: linkInfo.ModTime().UTC(), LinkTarget: "payload.bin"},
		{RelativePath: "payload.bin", Kind: "regular_file", SizeBytes: int64(len(payload)), Mode: 0o640, ModifiedAt: modified, SHA256: hex.EncodeToString(payloadDigest[:])},
	}
	inventoryDigest := sha256.New()
	if err := json.NewEncoder(inventoryDigest).Encode(entries); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"schema_version":  "loom.lane.custody_manifest.v1",
		"source_node_key": "node-a",
		"batch_id":        "batch-a",
		"accepted_date":   "2026-06-14",
		"inventory_hash":  "sha256:" + hex.EncodeToString(inventoryDigest.Sum(nil)),
		"file_count":      1,
		"directory_count": 0,
		"symlink_count":   2,
		"total_bytes":     len(payload),
		"entries":         entries,
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(batchRoot, ".loom-lane-custody.json"), raw, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := maintenance.WriteImportsBackupEvidence(context.Background(), filepath.Join(backupDir, "imports"), filepath.Join(backupDir, "imports-evidence.json"), maintenance.ImportsBackupPolicyCanonical, maintenance.ImportsSnapshotSharedStore, 2); err != nil {
		t.Fatalf("write cloud Imports evidence: %v", err)
	}
}

func cloudBackupCoverageOptions(dataDir string) backupcoverage.Options {
	boxRoot := filepath.Join(filepath.Dir(dataDir), "custom-box")
	return backupcoverage.Options{
		DataDir:              dataDir,
		ObjectStoreRoot:      filepath.Join(dataDir, "object-store"),
		ImportsRoot:          filepath.Join(dataDir, "imports"),
		UserBackupsRoot:      filepath.Join(dataDir, "user-backups"),
		MainDocumentsRoot:    filepath.Join(boxRoot, "Documents"),
		StorageRetentionRoot: filepath.Join(dataDir, "storage-retention"),
		StorageArchiveRoot:   filepath.Join(dataDir, "storage-archive"),
		MainBoxPath:          boxRoot,
		BoxNotesRoot:         filepath.Join(boxRoot, "Notes"),
		NotesProjectionRoot:  filepath.Join(dataDir, "generated", "notes"),
		MainBoxPolicy:        "selected_canonical_roots_copied_once",
		BackupRoot:           filepath.Join(dataDir, "backups", "main"),
	}
}

func enabledTestCloudConfig(t *testing.T, dataDir string) Config {
	t.Helper()
	cfg, err := NormalizeConfig(Config{
		SchemaVersion: ConfigSchemaVersion,
		Enabled:       true,
		StateDir:      filepath.Join(dataDir, "cloud"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func writeCloudTestFile(t *testing.T, path string, payload []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
}

func copyDirForTest(src, dst string) error {
	type copiedMetadata struct {
		path       string
		mode       os.FileMode
		modifiedAt time.Time
		isDir      bool
		isSymlink  bool
	}
	var metadata []copiedMetadata
	err := filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
				return err
			}
			metadata = append(metadata, copiedMetadata{path: target, mode: info.Mode().Perm(), modifiedAt: info.ModTime(), isDir: true})
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.Symlink(linkTarget, target); err != nil {
				return err
			}
			metadata = append(metadata, copiedMetadata{path: target, modifiedAt: info.ModTime(), isSymlink: true})
			return nil
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, payload, info.Mode().Perm()); err != nil {
			return err
		}
		metadata = append(metadata, copiedMetadata{path: target, mode: info.Mode().Perm(), modifiedAt: info.ModTime()})
		return nil
	})
	if err != nil {
		return err
	}
	for index := len(metadata) - 1; index >= 0; index-- {
		item := metadata[index]
		if item.isSymlink {
			times := []unix.Timespec{unix.NsecToTimespec(item.modifiedAt.UnixNano()), unix.NsecToTimespec(item.modifiedAt.UnixNano())}
			if err := unix.UtimesNanoAt(unix.AT_FDCWD, item.path, times, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return err
			}
			continue
		}
		if err := os.Chmod(item.path, item.mode); err != nil {
			return err
		}
		if err := os.Chtimes(item.path, item.modifiedAt, item.modifiedAt); err != nil {
			return err
		}
	}
	return nil
}

func fixedCloudNow() time.Time {
	return time.Date(2026, 6, 14, 10, 0, 0, 0, time.UTC)
}
