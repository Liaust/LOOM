package cloudstorage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"io"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/restoreauthority"
)

func fetchFailureFixture() (CloudRestoreDrillInput, SnapshotFetchResult) {
	return CloudRestoreDrillInput{Config: Config{Snapshots: SnapshotsConfig{Backend: SnapshotBackendBorg}}, Ref: "history-fixture", TargetDatabase: "loom_restore_drill_fetch", ProvenanceTargetDatabase: "loom_provenance_restore_drill_fetch"}, SnapshotFetchResult{Status: SnapshotStatusFailed, Backend: SnapshotBackendBorg, Ref: "history-fixture", Archive: "__loom-direct-user-data-main-history-fixture", DirectArchiveManifestSHA256: strings.Repeat("a", 64)}
}

func TestFetchFailureEvidenceClosesEveryStageAndRejectsArbitraryText(t *testing.T) {
	input, identity := fetchFailureFixture()
	secret := "postgresql://owner:canary-URL@host/secret-db /arbitrary/private/path token: canary-BARE\n-----BEGIN PRIVATE KEY-----\ncanary-KEY\n-----END PRIVATE KEY-----\n{\"password\":\"canary-JSON\"}\nPermission denied\x1b[31m\n"
	for _, stage := range []string{"fetch_resolution", "fetch_destination", "fetch_authentication", "fetch_archive_verification", "fetch_extraction", "fetch_extracted_verification"} {
		t.Run(stage, func(t *testing.T) {
			cause := &snapshotFetchFailure{Stage: stage, Identity: identity, Cause: &BorgExecutionError{Command: "extract ::/host-path --password canary-ARGS", Operation: "extract", Diagnostic: secret + strings.Repeat("x", 8000), Err: fmt.Errorf("canary-CAUSE")}}
			r, e := restoreFetchFailureEvidence(SnapshotFetchResult{}, input, time.Now(), cause)
			if err := validateRestoreFailureReceipt(r); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(r)
			for _, denied := range []string{"canary", "postgresql://", "/arbitrary", "/host-path", "PRIVATE KEY"} {
				if strings.Contains(string(raw)+e.Error(), denied) {
					t.Fatalf("leaked %s", denied)
				}
			}
			if r.FailureStage != stage || e.Code != "cloud."+stage+"_failed" || r.FetchFailure.BorgOperation != "extract" || !r.FetchFailure.DiagnosticTruncated || !r.FetchFailure.DiagnosticTextOmitted || r.FetchFailure.Diagnostic[0] != "permission_denied" {
				t.Fatalf("lost bounded truth: %+v", r)
			}
			if r.CleanupAttempted || r.CleanupSucceeded || r.FetchFailure.DatabaseOperations != "not_started" {
				t.Fatal("invented cleanup or DB operation")
			}
			if len(raw) > 4096 {
				t.Fatal("receipt unexpectedly large")
			}
			directory := exactRestoreFailureTempDir(t)
			if _, err := publishRestoreFailureReceipt(directory, r); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFetchFailureReceiptRejectsForgedTruth(t *testing.T) {
	input, identity := fetchFailureFixture()
	base, _ := restoreFetchFailureEvidence(identity, input, time.Now(), errors.New("unknown"))
	for name, mutate := range map[string]func(*RestoreFailureReceipt){
		"raw_diag":    func(r *RestoreFailureReceipt) { r.FetchFailure.Diagnostic = []string{"password=secret"} },
		"raw_command": func(r *RestoreFailureReceipt) { r.FetchFailure.BorgOperation = "extract ::/private" },
		"zero_exit":   func(r *RestoreFailureReceipt) { code := 0; r.FetchFailure.ExitCode = &code },
		"wrong_code":  func(r *RestoreFailureReceipt) { r.FailureCode = "restore_succeeded" },
		"wrong_stage": func(r *RestoreFailureReceipt) {
			r.FailureStage = "arbitrary"
			r.FailureCode = "fetch_failed"
			r.LOOMErrorCode = "cloud.fetch_failed"
		},
		"wrong_schema": func(r *RestoreFailureReceipt) { r.Schema = RestoreFailureReceiptSchema },
		"cleanup":      func(r *RestoreFailureReceipt) { r.CleanupSucceeded = true },
		"created":      func(r *RestoreFailureReceipt) { r.FetchFailure.DatabaseOperations = "created" },
		"provenance":   func(r *RestoreFailureReceipt) { r.FetchFailure.ProvenanceTarget = "postgres" },
		"manifest":     func(r *RestoreFailureReceipt) { r.FetchFailure.ManifestIdentity = "unavailable" },
		"archive":      func(r *RestoreFailureReceipt) { r.Archive = "/arbitrary" },
		"policy":       func(r *RestoreFailureReceipt) { r.FetchFailure.DiagnosticTextOmitted = false },
	} {
		t.Run(name, func(t *testing.T) {
			r := base
			d := *r.FetchFailure
			r.FetchFailure = &d
			mutate(&r)
			if validateRestoreFailureReceipt(r) == nil {
				t.Fatal("forged receipt accepted")
			}
		})
	}
}

func TestFetchFailureUnknownIdentityCancellationAndExitTruth(t *testing.T) {
	input, _ := fetchFailureFixture()
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, errors.New("/private/unknown password secret")} {
		r, _ := restoreFetchFailureEvidence(SnapshotFetchResult{}, input, time.Now(), cause)
		if err := validateRestoreFailureReceipt(r); err != nil {
			t.Fatal(err)
		}
		if r.FetchFailure.ArchiveIdentity != "unresolved" || r.FetchFailure.ManifestIdentity != "unavailable" || r.FetchFailure.ExitCode != nil {
			t.Fatal("invented identity or exit")
		}
	}
	err := exec.Command("sh", "-c", "exit 2").Run()
	r, _ := restoreFetchFailureEvidence(SnapshotFetchResult{}, input, time.Now(), &BorgExecutionError{Operation: "extract", Err: err})
	if r.FetchFailure.ExitCode == nil || *r.FetchFailure.ExitCode != 2 {
		t.Fatal("lost actual process exit")
	}
}

func TestFetchFailureAttemptNoFollowUniqueAndBoundReceipt(t *testing.T) {
	root := exactRestoreFailureTempDir(t)
	at := time.Now()
	first, identity, err := prepareRestoreFetchAttempt(filepath.Join(root, "state"), "history-fixture", at)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := prepareRestoreFetchAttempt(filepath.Join(root, "state"), "history-fixture", at)
	if err != nil || second == first {
		t.Fatal("concurrent attempt identity reused")
	}
	input, fetch := fetchFailureFixture()
	r, _ := restoreFetchFailureEvidence(fetch, input, at, errors.New("fixture"))
	if err := os.Rename(first, first+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(first, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := publishRestoreFailureReceiptBound(first, r, &identity); err == nil {
		t.Fatal("replacement accepted")
	}
	for _, parent := range []string{"state", "state/restore-drills"} {
		t.Run(strings.ReplaceAll(parent, "/", "_"), func(t *testing.T) {
			home := exactRestoreFailureTempDir(t)
			outside := exactRestoreFailureTempDir(t)
			path := filepath.Join(home, parent)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, path); err != nil {
				t.Fatal(err)
			}
			if _, _, err := prepareRestoreFetchAttempt(filepath.Join(home, "state"), "history-fixture", at); err == nil {
				t.Fatal("symlink ancestor accepted")
			}
			entries, _ := os.ReadDir(outside)
			if len(entries) != 0 {
				t.Fatal("wrote through symlink")
			}
		})
	}
}

type forbiddenFetchAuthority struct{ calls int }

func (a *forbiddenFetchAuthority) Restore(context.Context, restoreauthority.RestoreRequest) (restoreauthority.Result, error) {
	a.calls++
	return restoreauthority.Result{}, errors.New("unexpected restore")
}
func (a *forbiddenFetchAuthority) Drop(context.Context, restoreauthority.DropRequest) (restoreauthority.Result, error) {
	a.calls++
	return restoreauthority.Result{}, errors.New("unexpected drop")
}

func TestFetchFailureRestoreDrillPersistsBeforeAnyDatabaseWork(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	fixture.cfg.Snapshots.Borg.Binary = filepath.Join(exactRestoreFailureTempDir(t), "borg-fail")
	// A real process error before manifest resolution. The cause must not escape
	// through HTTP-safe typed summaries, including injected environment lines.
	if err := os.WriteFile(fixture.cfg.Snapshots.Borg.Binary, []byte("#!/bin/sh\nprintf 'Permission denied /secret/path password=canary\\n' >&2\nexit 2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	input, _ := fetchFailureFixture()
	input.Config = fixture.cfg
	input.StateDir = filepath.Join(exactRestoreFailureTempDir(t), "state")
	authority := &forbiddenFetchAuthority{}
	input.RestoreAuthority = authority
	calls := 0
	input.Runner = func(context.Context, string, []string, io.Reader) ([]byte, error) {
		calls++
		return nil, errors.New("unexpected pg_restore")
	}
	result, err := RestoreDrill(context.Background(), input)
	var typed *loomerrors.Error
	if !errors.As(err, &typed) || result.Status != SnapshotStatusFailed || result.FailureReceipt == "" || authority.calls != 0 || calls != 0 {
		t.Fatalf("result=%+v error=%v calls=%d/%d", result, err, authority.calls, calls)
	}
	raw, err := os.ReadFile(result.FailureReceipt)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw)+typed.Error(), "canary") || strings.Contains(string(raw), "/secret") {
		t.Fatal("leak")
	}
	var r RestoreFailureReceipt
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	if err := validateRestoreFailureReceipt(r); err != nil {
		t.Fatal(err)
	}
	var st unix.Stat_t
	if err := unix.Lstat(result.FailureReceipt, &st); err != nil || st.Mode&0777 != 0600 {
		t.Fatal("unsafe receipt")
	}
	input.TargetDatabase = "postgres"
	_, err = RestoreDrill(context.Background(), input)
	if err == nil {
		t.Fatal("invalid target accepted")
	}

}

func TestFetchFailureExtractionOutputBound(t *testing.T) {
	var b boundedBorgExtractOutput
	n, err := b.Write([]byte(strings.Repeat("x", 2<<20)))
	if err != nil || n != 2<<20 {
		t.Fatal("writer contract")
	}
	if !b.truncated || len(b.Bytes()) > borgFailureInspectLimit+len(borgFailureOmissionMarker) {
		t.Fatal("unbounded output")
	}
	_, truncated := boundedBorgFailureDiagnostic(b.Bytes())
	if !truncated {
		t.Fatal("lost truncation truth")
	}
}

func TestFetchFailureStreamingBorgPreservesClosedCommandAndCancellation(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	runner := BorgCommandRunner{Config: fixture.cfg, DisableRemoteLock: true, StreamExec: func(context.Context, string, BorgCommand, []string, io.Writer) error {
		return fmt.Errorf("Permission denied /private/canary: %w", context.Canceled)
	}}
	err := runner.RunStream(context.Background(), BorgCommand{Args: []string{"list", "--json-lines", "::fixture"}}, io.Discard)
	var borg *BorgExecutionError
	if !errors.As(err, &borg) || borg.Operation != "list" || !errors.Is(err, context.Canceled) {
		t.Fatal("stream identity/cancellation lost")
	}
	input, identity := fetchFailureFixture()
	r, e := restoreFetchFailureEvidence(identity, input, time.Now(), &snapshotFetchFailure{Stage: "fetch_archive_verification", Identity: identity, Cause: err})
	raw, _ := json.Marshal(r)
	if strings.Contains(string(raw)+e.Error(), "canary") || r.FetchFailure.BorgOperation != "list" || len(r.FetchFailure.Diagnostic) != 2 {
		t.Fatal("stream diagnostics unsafe or missing")
	}
}

func TestFetchFailureReceiptV2ReplaysAndRejectsAncestorReplacement(t *testing.T) {
	root := exactRestoreFailureTempDir(t)
	parent := filepath.Join(root, "parent")
	directory := filepath.Join(parent, "attempt")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	fd, err := openRestoreFailureDirectory(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	input, identity := fetchFailureFixture()
	receipt, _ := restoreFetchFailureEvidence(identity, input, time.Now(), errors.New("fixture"))
	path, err := publishRestoreFailureReceipt(directory, receipt)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if _, err := publishRestoreFailureReceipt(directory, receipt); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("replay mutated receipt")
	}
	if err := os.Rename(parent, parent+"-held"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := revalidateRestoreFailureDirectory(directory, fd); err == nil {
		t.Fatal("ancestor replacement accepted")
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		t.Fatal(err)
	}
	if _, err := publishRestoreFailureReceiptBound(directory, receipt, &st); err == nil {
		t.Fatal("receipt published into replacement")
	}
	if _, err := os.Stat(filepath.Join(directory, RestoreFailureReceiptFile)); !os.IsNotExist(err) {
		t.Fatal("replacement mutated")
	}
}
