package cloudstorage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"loom.local/loom/internal/backup"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/restoreauthority"
)

func testRestoreFailureReceipt() RestoreFailureReceipt {
	return RestoreFailureReceipt{
		Schema: RestoreFailureReceiptSchema, Status: SnapshotStatusFailed,
		Ref: "history-fixture", Backend: SnapshotBackendBorg,
		Archive:                     "__loom-direct-user-data-main-history-fixture",
		DirectArchiveManifestSHA256: strings.Repeat("a", 64),
		OperationKind:               "direct_archive_strict_restore",
		TargetDatabaseClass:         restoreauthority.KindOperational,
		TargetDatabase:              "loom_restore_drill_failure_receipt",
		FailureStage:                string(restoreauthority.FailureStageDatabaseCreate),
		FailureCode:                 restoreauthority.ErrorDatabaseCreateFailed,
		LOOMErrorCode:               "backup.restore_authority.database_create_failed",
		CleanupStatus:               "succeeded",
		CleanupAttempted:            true, CleanupSucceeded: true,
		OccurredAt: time.Date(2026, 9, 2, 18, 30, 45, 123456789, time.UTC),
	}
}

func exactRestoreFailureTempDir(t *testing.T) string {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestRestoreFailureReceiptPublishesAtomicallyAndReplaysExactBytes(t *testing.T) {
	directory := exactRestoreFailureTempDir(t)
	receipt := testRestoreFailureReceipt()
	path, err := publishRestoreFailureReceipt(directory, receipt)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || stat.Nlink != 1 {
		t.Fatalf("receipt metadata = mode %v links %d", info.Mode(), stat.Nlink)
	}
	replayedPath, err := publishRestoreFailureReceipt(directory, receipt)
	if err != nil || replayedPath != path {
		t.Fatalf("exact replay path=%q err=%v", replayedPath, err)
	}
	replayed, err := os.ReadFile(path)
	if err != nil || string(replayed) != string(raw) {
		t.Fatalf("replay changed bytes: err=%v before=%q after=%q", err, raw, replayed)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != RestoreFailureReceiptFile {
		t.Fatalf("receipt publication left temporary state: entries=%v err=%v", entries, err)
	}
}

func TestRestoreFailureReceiptConcurrentReplayIsByteStable(t *testing.T) {
	directory := exactRestoreFailureTempDir(t)
	receipt := testRestoreFailureReceipt()
	const attempts = 20
	errorsSeen := make(chan error, attempts)
	var wait sync.WaitGroup
	for range attempts {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := publishRestoreFailureReceipt(directory, receipt)
			errorsSeen <- err
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent receipt replay failed: %v", err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(directory, RestoreFailureReceiptFile))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(receipt)
	want = append(want, '\n')
	if string(raw) != string(want) {
		t.Fatalf("concurrent replay bytes=%q want=%q", raw, want)
	}
}

func TestRestoreFailureReceiptRejectsDriftAndUnsafeEntries(t *testing.T) {
	receipt := testRestoreFailureReceipt()
	valid, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	valid = append(valid, '\n')
	unknown := append([]byte(strings.TrimSuffix(string(valid), "}\n")), []byte(",\"unknown\":true}\n")...)
	malformed := []byte(`{"schema":`)
	oversized := make([]byte, maxRestoreFailureReceiptBytes+1)
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{"symlink", func(t *testing.T, directory string) {
			target := filepath.Join(directory, "outside")
			if err := os.WriteFile(target, valid, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(directory, RestoreFailureReceiptFile)); err != nil {
				t.Fatal(err)
			}
		}},
		{"hardlink", func(t *testing.T, directory string) {
			target := filepath.Join(directory, "outside")
			if err := os.WriteFile(target, valid, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(target, filepath.Join(directory, RestoreFailureReceiptFile)); err != nil {
				t.Fatal(err)
			}
		}},
		{"permissive_mode", func(t *testing.T, directory string) {
			if err := os.WriteFile(filepath.Join(directory, RestoreFailureReceiptFile), valid, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"unknown_field", func(t *testing.T, directory string) {
			if err := os.WriteFile(filepath.Join(directory, RestoreFailureReceiptFile), unknown, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"malformed", func(t *testing.T, directory string) {
			if err := os.WriteFile(filepath.Join(directory, RestoreFailureReceiptFile), malformed, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"oversized", func(t *testing.T, directory string) {
			if err := os.WriteFile(filepath.Join(directory, RestoreFailureReceiptFile), oversized, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := exactRestoreFailureTempDir(t)
			test.setup(t, directory)
			if _, err := publishRestoreFailureReceipt(directory, receipt); err == nil {
				t.Fatal("unsafe existing receipt was accepted")
			}
		})
	}

	directory := exactRestoreFailureTempDir(t)
	if _, err := publishRestoreFailureReceipt(directory, receipt); err != nil {
		t.Fatal(err)
	}
	conflict := receipt
	conflict.FailureStage = string(restoreauthority.FailureStageDatabaseRestore)
	conflict.FailureCode = restoreauthority.ErrorDatabaseRestoreFailed
	conflict.LOOMErrorCode = "backup.restore_authority.database_restore_failed"
	if _, err := publishRestoreFailureReceipt(directory, conflict); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("identity-conflicting receipt error=%v", err)
	}

	realDirectory := exactRestoreFailureTempDir(t)
	symlinkedDirectory := filepath.Join(exactRestoreFailureTempDir(t), "drill")
	if err := os.Symlink(realDirectory, symlinkedDirectory); err != nil {
		t.Fatal(err)
	}
	if _, err := publishRestoreFailureReceipt(symlinkedDirectory, receipt); err == nil {
		t.Fatal("symlinked drill directory was accepted")
	}
	realParent := exactRestoreFailureTempDir(t)
	if err := os.Mkdir(filepath.Join(realParent, "drill"), 0o700); err != nil {
		t.Fatal(err)
	}
	visibleParent := exactRestoreFailureTempDir(t)
	if err := os.Symlink(realParent, filepath.Join(visibleParent, "linked-parent")); err != nil {
		t.Fatal(err)
	}
	if _, err := publishRestoreFailureReceipt(filepath.Join(visibleParent, "linked-parent", "drill"), receipt); err == nil {
		t.Fatal("restore drill directory with a symlinked ancestor was accepted")
	}
}

func TestRestoreFailureEvidencePreservesAuthorityTruthAndRedactsCause(t *testing.T) {
	secret := "postgresql://owner:credential-MUST-NOT-LEAK@localhost/database /private/staging/dump"
	authorityResult := restoreauthority.Result{
		Status: "failed", Kind: restoreauthority.KindProvenance,
		Database:         "loom_provenance_restore_drill_typed_failure",
		FailureStage:     restoreauthority.FailureStageDatabaseRestore,
		ErrorCode:        restoreauthority.ErrorDatabaseRestoreFailed,
		CleanupAttempted: true, CleanupSucceeded: true,
	}
	authorityErr := &restoreauthority.AuthorityError{
		Stage: authorityResult.FailureStage, Code: authorityResult.ErrorCode,
		Message: "Disposable database restore failed.", Result: authorityResult,
		Cause: errors.New(secret),
	}
	wrapped := fmt.Errorf("provenance package strict restore: %w", &backup.RestoreDatabaseFailure{
		Kind: restoreauthority.KindProvenance, Database: authorityResult.Database,
		CleanupAttempted: true, CleanupSucceeded: false, Cause: authorityErr,
	})
	fetch := SnapshotFetchResult{
		Ref: "history-fixture", Backend: SnapshotBackendBorg,
		Archive:                     "__loom-direct-user-data-main-history-fixture",
		DirectArchiveManifestSHA256: strings.Repeat("b", 64),
	}
	receipt, typedErr := restoreFailureEvidence(fetch, backup.DirectArchiveRestoreDrillResult{}, CloudRestoreDrillInput{}, time.Date(2026, 9, 2, 19, 0, 0, 0, time.UTC), wrapped)
	if receipt.FailureStage != string(authorityResult.FailureStage) || receipt.FailureCode != authorityResult.ErrorCode || receipt.LOOMErrorCode != "backup.restore_authority.database_restore_failed" || receipt.TargetDatabaseClass != authorityResult.Kind || receipt.TargetDatabase != authorityResult.Database || receipt.CleanupStatus != "failed" || !receipt.CleanupAttempted || receipt.CleanupSucceeded {
		t.Fatalf("receipt lost typed authority truth: %#v", receipt)
	}
	if typedErr.Code != receipt.LOOMErrorCode || typedErr.Domain != "backup" || typedErr.Target != authorityResult.Database {
		t.Fatalf("CLI error metadata=%#v", typedErr)
	}
	var preserved *restoreauthority.AuthorityError
	if !errors.As(typedErr, &preserved) || preserved != authorityErr {
		t.Fatalf("typed authority error was lost: %v", typedErr)
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "credential-MUST-NOT-LEAK") || strings.Contains(string(raw), "postgresql://") || strings.Contains(string(raw), "/private/") || strings.Contains(typedErr.Error(), secret) {
		t.Fatalf("failure evidence leaked secret runtime detail: receipt=%s error=%v", raw, typedErr)
	}
	var loomErr *loomerrors.Error
	if !errors.As(typedErr, &loomErr) || loomErr.Code != receipt.LOOMErrorCode {
		t.Fatalf("typed LOOM error was not exposed: %v", typedErr)
	}
}
