package cloudstorage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/backupcoverage"
	"loom.local/loom/internal/maintenance"
)

func TestDigitalEstateProjectPilotBackupFetchRestore(t *testing.T) {
	pilotRoot := strings.TrimSpace(os.Getenv("LOOM_DIGITAL_ESTATE_PILOT_ROOT"))
	if pilotRoot == "" {
		t.Skip("disposable project pilot is driven by the smoke test")
	}
	pilotRoot, err := filepath.Abs(pilotRoot)
	if err != nil {
		t.Fatal(err)
	}
	acceptedRoot := filepath.Join(pilotRoot, "runtime", "box", "Projects")
	before, err := projectPilotTreeDigest(acceptedRoot)
	if err != nil {
		t.Fatal(err)
	}

	dataDir, backupDir := validCloudBackupFixture(t)
	backupProjects := filepath.Join(backupDir, "main-documents")
	if err := os.RemoveAll(backupProjects); err != nil {
		t.Fatal(err)
	}
	if err := copyDirForTest(acceptedRoot, backupProjects); err != nil {
		t.Fatal(err)
	}
	remoteRoot := filepath.Join(pilotRoot, "disposable-cloud")
	driver := &projectPilotSnapshotDriver{root: remoteRoot}
	cfg := enabledTestCloudConfig(t, dataDir)
	coverageOptions := cloudBackupCoverageOptions(dataDir)
	coverageOptions.MainDocumentsRoot = acceptedRoot
	backend := LegacyTreeSnapshotBackend{Driver: driver}
	push, err := backend.Push(context.Background(), SnapshotPushInput{
		Config: cfg, Driver: driver, BackupRef: backupDir,
		BackupRoot: filepath.Join(dataDir, "backups", "main"), DataDir: dataDir,
		CoverageOptions: coverageOptions, NodeID: "digital-estate-pilot", Now: func() time.Time {
			return time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	projectCoverage := projectPilotCoverageStatus(push.Coverage, "main-documents")
	if push.Status != SnapshotStatusSucceeded || push.Coverage.Status == backupcoverage.OverallCritical || push.Coverage.Summary.CriticalMissing != 0 || projectCoverage != backupcoverage.StatusCovered {
		t.Fatalf("snapshot push or coverage failed: %#v", push)
	}
	if got, err := projectPilotTreeDigest(acceptedRoot); err != nil || got != before {
		t.Fatalf("accepted project root changed during backup: before=%s after=%s err=%v", before, got, err)
	}

	restoreRoot := filepath.Join(pilotRoot, "isolated-restore")
	fetch, err := backend.Fetch(context.Background(), SnapshotFetchInput{
		Config: cfg, Driver: driver, NodeID: "digital-estate-pilot", Ref: push.SnapshotRef, To: restoreRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fetch.Status != SnapshotStatusSucceeded || fetch.Verification.Status != maintenance.VerificationSucceeded {
		t.Fatalf("snapshot fetch failed: %#v", fetch)
	}
	restored, err := projectPilotTreeDigest(filepath.Join(restoreRoot, "main-documents"))
	if err != nil {
		t.Fatal(err)
	}
	if restored != before {
		t.Fatalf("restored project digest = %s, want %s", restored, before)
	}
	if got, err := projectPilotTreeDigest(acceptedRoot); err != nil || got != before {
		t.Fatalf("accepted project root changed during isolated restore: before=%s after=%s err=%v", before, got, err)
	}

	evidence := struct {
		SchemaVersion         string `json:"schema_version"`
		CoverageStatus        string `json:"coverage_status"`
		ProjectCoverageStatus string `json:"project_coverage_status"`
		CriticalMissing       int    `json:"critical_missing"`
		SnapshotStatus        string `json:"snapshot_status"`
		FetchStatus           string `json:"fetch_status"`
		ProjectDigest         string `json:"project_digest"`
		SnapshotRef           string `json:"snapshot_ref"`
	}{
		SchemaVersion: "loom.digital_estate_project_backup_pilot.v1", CoverageStatus: push.Coverage.Status,
		ProjectCoverageStatus: projectCoverage, CriticalMissing: push.Coverage.Summary.CriticalMissing,
		SnapshotStatus: push.Status, FetchStatus: fetch.Status, ProjectDigest: before, SnapshotRef: push.SnapshotRef,
	}
	payload, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pilotRoot, "backup-evidence.json"), append(payload, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

type projectPilotRetainedTreeEvidence struct {
	SchemaVersion string            `json:"schema_version"`
	Digests       map[string]string `json:"digests"`
}

// TestDigitalEstateProjectPilotRetainedTrees records or verifies the bounded
// tree identities carried between the resumable smoke phases. The signed shell
// receipt authenticates this evidence file; this test independently recomputes
// every declared tree without mutating the estate.
func TestDigitalEstateProjectPilotRetainedTrees(t *testing.T) {
	pilotRoot := strings.TrimSpace(os.Getenv("LOOM_DIGITAL_ESTATE_PILOT_ROOT"))
	if pilotRoot == "" {
		t.Skip("disposable project pilot is driven by the smoke test")
	}
	pilotRoot, err := filepath.Abs(pilotRoot)
	if err != nil {
		t.Fatal(err)
	}
	trees := map[string]string{
		"source_single":   filepath.Join(pilotRoot, "sources", "single-project"),
		"source_multi":    filepath.Join(pilotRoot, "sources", "multi-project"),
		"accepted_single": filepath.Join(pilotRoot, "runtime", "box", "Projects", "single-project"),
		"accepted_multi":  filepath.Join(pilotRoot, "runtime", "box", "Projects", "multi-project"),
		"restored_single": filepath.Join(pilotRoot, "isolated-restore", "main-documents", "single-project"),
		"restored_multi":  filepath.Join(pilotRoot, "isolated-restore", "main-documents", "multi-project"),
	}
	actual := projectPilotRetainedTreeEvidence{
		SchemaVersion: "loom.digital_estate_project_retained_trees.v1",
		Digests:       make(map[string]string, len(trees)),
	}
	for key, root := range trees {
		digest, err := projectPilotTreeDigest(root)
		if err != nil {
			t.Fatalf("digest %s: %v", key, err)
		}
		actual.Digests[key] = digest
	}

	evidencePath := filepath.Join(pilotRoot, "tree-evidence.json")
	if strings.TrimSpace(os.Getenv("LOOM_DIGITAL_ESTATE_RECORD_TREE_EVIDENCE")) == "1" {
		payload, err := json.MarshalIndent(actual, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(evidencePath, append(payload, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}

	payload, err := os.ReadFile(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	var expected projectPilotRetainedTreeEvidence
	if err := json.Unmarshal(payload, &expected); err != nil {
		t.Fatal(err)
	}
	if expected.SchemaVersion != actual.SchemaVersion || !reflect.DeepEqual(expected.Digests, actual.Digests) {
		t.Fatalf("retained tree evidence changed: expected=%#v actual=%#v", expected, actual)
	}
}

func projectPilotCoverageStatus(report backupcoverage.Report, key string) string {
	for _, entry := range report.Entries {
		if entry.Key == key {
			return entry.Status
		}
	}
	return "missing"
}

type projectPilotSnapshotDriver struct {
	root string
}

func (driver *projectPilotSnapshotDriver) Status(context.Context) (RemoteStatus, error) {
	return RemoteStatus{Reachable: true, RemoteURI: "file://disposable-project-pilot", CheckedAt: time.Now().UTC()}, nil
}

func (driver *projectPilotSnapshotDriver) List(_ context.Context, prefix string) ([]RemoteEntry, error) {
	root, err := driver.remotePath(prefix)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return []RemoteEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]RemoteEntry, 0, len(entries))
	for _, entry := range entries {
		result = append(result, RemoteEntry{Path: path.Join(prefix, entry.Name()), IsDir: entry.IsDir()})
	}
	return result, nil
}

func (driver *projectPilotSnapshotDriver) CopyToRemote(_ context.Context, localPath, remotePath string, options CopyOptions) (CopyResult, error) {
	target, err := driver.remotePath(remotePath)
	if err == nil {
		if options.SingleFile {
			err = projectPilotCopyFile(localPath, target)
		} else {
			err = copyDirForTest(localPath, target)
		}
	}
	return projectPilotCopyResult(localPath, target), err
}

func (driver *projectPilotSnapshotDriver) CopyFromRemote(_ context.Context, remotePath, localPath string, options CopyOptions) (CopyResult, error) {
	source, err := driver.remotePath(remotePath)
	if err == nil {
		if options.SingleFile {
			err = projectPilotCopyFile(source, localPath)
		} else {
			err = copyDirForTest(source, localPath)
		}
	}
	return projectPilotCopyResult(source, localPath), err
}

func (driver *projectPilotSnapshotDriver) Check(_ context.Context, localPath, remotePath string) (CheckResult, error) {
	return driver.CheckWithOptions(context.Background(), localPath, remotePath, CheckOptions{})
}

func (driver *projectPilotSnapshotDriver) CheckWithOptions(_ context.Context, localPath, remotePath string, _ CheckOptions) (CheckResult, error) {
	target, err := driver.remotePath(remotePath)
	if err != nil {
		return CheckResult{}, err
	}
	localDigest, err := projectPilotTreeDigest(localPath)
	if err != nil {
		return CheckResult{}, err
	}
	remoteDigest, err := projectPilotTreeDigest(target)
	if err != nil {
		return CheckResult{}, err
	}
	return CheckResult{Matched: localDigest == remoteDigest, LocalPath: localPath, RemotePath: remotePath, CheckedAt: time.Now().UTC()}, nil
}

func (driver *projectPilotSnapshotDriver) MoveRemote(_ context.Context, fromRemotePath, toRemotePath string, _ CopyOptions) (CopyResult, error) {
	from, err := driver.remotePath(fromRemotePath)
	if err != nil {
		return CopyResult{}, err
	}
	to, err := driver.remotePath(toRemotePath)
	if err != nil {
		return CopyResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return CopyResult{}, err
	}
	err = os.Rename(from, to)
	return projectPilotCopyResult(from, to), err
}

func (driver *projectPilotSnapshotDriver) remotePath(relative string) (string, error) {
	clean := path.Clean(strings.TrimSpace(relative))
	if clean == "." || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("unsafe disposable remote path %q", relative)
	}
	return filepath.Join(driver.root, filepath.FromSlash(clean)), nil
}

func projectPilotCopyFile(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("project pilot single-file copy requires a regular file")
	}
	payload, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	return os.WriteFile(target, payload, info.Mode().Perm())
}

func projectPilotCopyResult(source, target string) CopyResult {
	return CopyResult{Command: "disposable-local-copy", Source: source, Dest: target, FinishedAt: time.Now().UTC()}
}

func projectPilotTreeDigest(root string) (string, error) {
	var records []string
	err := filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		record := filepath.ToSlash(relative) + "\x00" + info.Mode().String()
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(current)
			if err != nil {
				return err
			}
			record += "\x00" + target
		case info.Mode().IsRegular():
			payload, err := os.ReadFile(current)
			if err != nil {
				return err
			}
			digest := sha256.Sum256(payload)
			record += "\x00" + hex.EncodeToString(digest[:])
		}
		records = append(records, record)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(records)
	digest := sha256.Sum256([]byte(strings.Join(records, "\n")))
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
