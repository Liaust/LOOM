package cloudstorage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/restoreauthority"
)

const RestoreFetchFailureReceiptSchema = "loom.cloud_restore_failure.v2"

// A fetch error carries only the identity learned before failure. Its Cause is
// for in-process inspection; never serialize it or infer success from payload.
type snapshotFetchFailure struct {
	Stage    string
	Identity SnapshotFetchResult
	Cause    error
}

func (e *snapshotFetchFailure) Error() string { return e.Cause.Error() }
func (e *snapshotFetchFailure) Unwrap() error { return e.Cause }

type privateFetchCause struct{ cause error }

func (e privateFetchCause) Error() string {
	return "fetch cause omitted; inspect the bounded failure receipt"
}
func (e privateFetchCause) Unwrap() error { return e.cause }

// RestoreFetchFailureDetails is a closed projection, not scrubbed arbitrary
// stderr. Unrecognized text is deliberately unavailable in durable evidence.
type RestoreFetchFailureDetails struct {
	RequestedRef          string   `json:"requested_ref"`
	ArchiveIdentity       string   `json:"archive_identity"`
	ManifestIdentity      string   `json:"manifest_identity"`
	OperationalTarget     string   `json:"operational_target"`
	ProvenanceTarget      string   `json:"provenance_target"`
	DatabaseOperations    string   `json:"database_operations"`
	AuthorityOperations   string   `json:"authority_operations"`
	PayloadDisposition    string   `json:"payload_disposition"`
	BorgOperation         string   `json:"borg_operation"`
	ExitCode              *int     `json:"exit_code,omitempty"`
	Diagnostic            []string `json:"diagnostic"`
	DiagnosticPolicy      string   `json:"diagnostic_policy"`
	DiagnosticTextOmitted bool     `json:"diagnostic_text_omitted"`
	DiagnosticTruncated   bool     `json:"diagnostic_truncated"`
}

// Only constant categories are emitted. In particular, paths, usernames,
// arbitrary JSON/tracebacks, credential fragments and URI components cannot
// survive this projection, even if an upstream sanitizer misses them.
var fetchDiagnosticCategories = []struct{ marker, category string }{
	{"permission denied", "permission_denied"},
	{"operation not permitted", "operation_not_permitted"},
	{"no space left on device", "no_space_left"},
	{"disk quota exceeded", "disk_quota_exceeded"},
	{"read-only file system", "read_only_filesystem"},
	{"input/output error", "io_error"},
	{"is a directory", "is_a_directory"},
	{"directory not empty", "directory_not_empty"},
	{"no such file or directory", "missing_file"},
	{"connection closed", "connection_closed"},
	{"connection reset", "connection_reset"},
	{"broken pipe", "broken_pipe"},
	{"timed out", "timed_out"},
	{"integrityerror", "integrity_error"},
	{"decompressionerror", "decompression_error"},
	{"repository does not exist", "repository_missing"},
	{"passphrase", "passphrase_error"},
	{"lock", "lock_diagnostic"},
}

func fetchFailureCode(stage string) string {
	switch stage {
	case "fetch_resolution", "fetch_destination", "fetch_authentication", "fetch_archive_verification", "fetch_extraction", "fetch_extracted_verification":
		return stage + "_failed"
	default:
		return "fetch_failed"
	}
}

func restoreFetchFailureEvidence(fetch SnapshotFetchResult, input CloudRestoreDrillInput, at time.Time, cause error) (RestoreFailureReceipt, *loomerrors.Error) {
	stage := "fetch"
	var failure *snapshotFetchFailure
	if errors.As(cause, &failure) {
		stage, fetch = failure.Stage, failure.Identity
	} else if fetch.Status == SnapshotStatusFailed {
		stage = "fetch_extracted_verification"
	}
	details := &RestoreFetchFailureDetails{
		RequestedRef:    safeRemoteSegment(firstNonEmpty(input.Ref, "latest")),
		ArchiveIdentity: "unresolved", ManifestIdentity: "unavailable",
		OperationalTarget: input.TargetDatabase, ProvenanceTarget: input.ProvenanceTargetDatabase,
		DatabaseOperations: "not_started", AuthorityOperations: "not_started",
		PayloadDisposition: "retained", BorgOperation: "unavailable",
		Diagnostic: []string{}, DiagnosticPolicy: "closed_categories_v1",
		DiagnosticTextOmitted: true,
	}
	if fetch.Archive != "" {
		details.ArchiveIdentity = "selected"
	}
	if fetch.DirectArchiveManifestSHA256 != "" {
		details.ManifestIdentity = "authenticated"
	}
	var borg *BorgExecutionError
	if errors.As(cause, &borg) {
		switch borg.Operation {
		case "list", "info", "check", "extract":
			details.BorgOperation = borg.Operation
		}
		var exit *exec.ExitError
		if errors.As(borg.Err, &exit) {
			code := exit.ExitCode()
			details.ExitCode = &code
		}
		// Bound inspection again: injected runners and wrapped errors need not
		// have passed the process runner's normal output bound.
		diagnostic := borg.Diagnostic
		details.DiagnosticTruncated = borg.DiagnosticTruncated || len(diagnostic) > borgFailureDiagnosticLimit
		if len(diagnostic) > borgFailureDiagnosticLimit {
			diagnostic = diagnostic[:borgFailureDiagnosticLimit]
		}
		diagnostic = strings.ToLower(normalizeBorgFailureControls([]byte(diagnostic)))
		for _, known := range fetchDiagnosticCategories {
			if strings.Contains(diagnostic, known.marker) {
				details.Diagnostic = append(details.Diagnostic, known.category)
			}
		}
	}
	if errors.Is(cause, context.Canceled) {
		details.Diagnostic = append(details.Diagnostic, "request_cancelled")
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		details.Diagnostic = append(details.Diagnostic, "deadline_exceeded")
	}
	code := fetchFailureCode(stage)
	receipt := RestoreFailureReceipt{
		Schema: RestoreFetchFailureReceiptSchema, Status: SnapshotStatusFailed,
		Ref: firstNonEmpty(fetch.Ref, details.RequestedRef), Backend: input.Config.Snapshots.Backend,
		Archive: fetch.Archive, DirectArchiveManifestSHA256: fetch.DirectArchiveManifestSHA256,
		OperationKind: "cloud_restore_fetch", TargetDatabaseClass: restoreauthority.KindOperational,
		TargetDatabase: input.TargetDatabase, FailureStage: stage, FailureCode: code,
		LOOMErrorCode: "cloud." + code, CleanupStatus: "not_attempted", OccurredAt: at.UTC(), FetchFailure: details,
	}
	return receipt, loomerrors.Wrap(receipt.LOOMErrorCode, "cloud", "restore-drill",
		fmt.Sprintf("Cloud restore failed at %s before database operations.", stage), privateFetchCause{cause: cause})
}

func validateRestoreFetchFailureReceipt(r RestoreFailureReceipt) error {
	d := r.FetchFailure
	if d == nil || r.Schema != RestoreFetchFailureReceiptSchema || r.Status != SnapshotStatusFailed || r.OperationKind != "cloud_restore_fetch" ||
		r.FailureCode != fetchFailureCode(r.FailureStage) || r.LOOMErrorCode != "cloud."+r.FailureCode ||
		(r.FailureStage != "fetch" && fetchFailureCode(r.FailureStage) == "fetch_failed") {
		return fmt.Errorf("fetch failure receipt contract is invalid")
	}
	for _, value := range []string{r.Ref, d.RequestedRef} {
		if len(value) == 0 || len(value) > 255 || !restoreFailureTokenPattern.MatchString(value) {
			return fmt.Errorf("fetch failure ref is invalid")
		}
	}
	if r.Backend != SnapshotBackendBorg && r.Backend != SnapshotBackendLegacyTree {
		return fmt.Errorf("fetch backend is invalid")
	}
	if d.ArchiveIdentity == "selected" {
		if len(r.Archive) == 0 || len(r.Archive) > 255 || !restoreFailureTokenPattern.MatchString(r.Archive) {
			return fmt.Errorf("fetch archive is invalid")
		}
	} else if d.ArchiveIdentity != "unresolved" || r.Archive != "" {
		return fmt.Errorf("fetch archive identity truth is invalid")
	}
	if d.ManifestIdentity == "authenticated" {
		if d.ArchiveIdentity != "selected" || !restoreFailureDigestPattern.MatchString(r.DirectArchiveManifestSHA256) {
			return fmt.Errorf("fetch manifest identity is invalid")
		}
	} else if d.ManifestIdentity != "unavailable" || r.DirectArchiveManifestSHA256 != "" {
		return fmt.Errorf("fetch manifest identity truth is invalid")
	}
	if r.TargetDatabaseClass != restoreauthority.KindOperational || r.TargetDatabase != d.OperationalTarget {
		return fmt.Errorf("fetch target identity is invalid")
	}
	for kind, target := range map[restoreauthority.Kind]string{restoreauthority.KindOperational: d.OperationalTarget, restoreauthority.KindProvenance: d.ProvenanceTarget} {
		if err := restoreauthority.ValidateDisposableDatabase(kind, target); err != nil {
			return fmt.Errorf("fetch target is invalid")
		}
	}
	if r.CleanupStatus != "not_attempted" || r.CleanupAttempted || r.CleanupSucceeded || d.DatabaseOperations != "not_started" || d.AuthorityOperations != "not_started" || d.PayloadDisposition != "retained" {
		return fmt.Errorf("fetch non-mutation truth is invalid")
	}
	if r.OccurredAt.IsZero() || r.OccurredAt.Location() != time.UTC {
		return fmt.Errorf("fetch occurrence is invalid")
	}
	switch d.BorgOperation {
	case "unavailable", "list", "info", "check", "extract":
	default:
		return fmt.Errorf("fetch Borg operation is invalid")
	}
	if d.ExitCode != nil && (d.BorgOperation == "unavailable" || *d.ExitCode == 0 || *d.ExitCode < -1 || *d.ExitCode > 255) {
		return fmt.Errorf("fetch exit code is invalid")
	}
	if d.DiagnosticPolicy != "closed_categories_v1" || !d.DiagnosticTextOmitted || len(d.Diagnostic) > len(fetchDiagnosticCategories)+2 {
		return fmt.Errorf("fetch diagnostic policy is invalid")
	}
	allowed := map[string]bool{"request_cancelled": true, "deadline_exceeded": true}
	for _, known := range fetchDiagnosticCategories {
		allowed[known.category] = true
	}
	seen := map[string]bool{}
	for _, category := range d.Diagnostic {
		if !allowed[category] || seen[category] {
			return fmt.Errorf("fetch diagnostic category is invalid")
		}
		seen[category] = true
	}
	return nil
}

// Make a fresh attempt beneath the configured root using no-follow directory
// handles. A random suffix prevents concurrent attempts sharing a receipt/tree.
func prepareRestoreFetchAttempt(stateDir, ref string, at time.Time) (string, unix.Stat_t, error) {
	var identity unix.Stat_t
	root := filepath.Clean(stateDir)
	if !filepath.IsAbs(root) || root == "/" {
		return "", identity, fmt.Errorf("cloud state must be an absolute non-root directory")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", identity, err
	}
	defer func() { _ = unix.Close(fd) }()
	for _, component := range strings.Split(strings.TrimPrefix(filepath.Join(root, "restore-drills"), "/"), "/") {
		err = unix.Mkdirat(fd, component, 0o700)
		if err != nil && !errors.Is(err, unix.EEXIST) {
			return "", identity, err
		}
		next, err := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return "", identity, err
		}
		_ = unix.Close(fd)
		fd = next
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return "", identity, err
	}
	name := at.UTC().Format("20060102T150405Z") + "-" + safeRemoteSegment(ref) + "-" + hex.EncodeToString(nonce)
	if len(name) > 255 {
		return "", identity, fmt.Errorf("restore ref exceeds attempt name bound")
	}
	if err := unix.Mkdirat(fd, name, 0o700); err != nil {
		return "", identity, err
	}
	if err := unix.Fstatat(fd, name, &identity, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return "", identity, err
	}
	return filepath.Join(root, "restore-drills", name), identity, unix.Fsync(fd)
}
