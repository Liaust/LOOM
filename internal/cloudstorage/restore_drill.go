package cloudstorage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"loom.local/loom/internal/backup"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/restoreauthority"
)

const (
	RestoreFailureReceiptSchema   = "loom.cloud_restore_failure.v1"
	RestoreFailureReceiptFile     = "restore-failure.json"
	maxRestoreFailureReceiptBytes = 16 << 10
)

var restoreFailureTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)

type CloudRestoreDrillInput struct {
	Config                   Config
	Driver                   Driver
	NodeID                   string
	Ref                      string
	StateDir                 string
	TargetDatabase           string
	ActiveDatabase           string
	Owner                    string
	ProvenanceDatabaseURL    string `json:"-"`
	ProvenanceTargetDatabase string
	ProvenanceActiveDatabase string
	ProvenanceOwner          string
	KeepDatabase             bool
	DryRun                   bool
	Runner                   backup.CommandRunner                      `json:"-"`
	RestoreAuthority         restoreauthority.RestoreDatabaseAuthority `json:"-"`
	// ProvenanceRestore must perform an independent disposable provenance
	// restore. Its absence prevents a direct archive from being reported as a
	// successful strict restore.
	ProvenanceRestore backup.DirectArchiveRecoveryStep `json:"-"`
	Now               func() time.Time                 `json:"-"`
}

type CloudRestoreDrillResult struct {
	Status         string                                  `json:"status"`
	DryRun         bool                                    `json:"dry_run"`
	Ref            string                                  `json:"ref"`
	Backend        string                                  `json:"backend,omitempty"`
	Repository     string                                  `json:"repository,omitempty"`
	Archive        string                                  `json:"archive,omitempty"`
	RemoteURI      string                                  `json:"remote_uri,omitempty"`
	Fetch          SnapshotFetchResult                     `json:"fetch"`
	Plan           *backup.RestoreDrillPlan                `json:"plan,omitempty"`
	Result         *backup.RestoreDrillResult              `json:"result,omitempty"`
	DirectPlan     *backup.DirectArchiveRestoreDrillPlan   `json:"direct_plan,omitempty"`
	DirectResult   *backup.DirectArchiveRestoreDrillResult `json:"direct_result,omitempty"`
	StagingDir     string                                  `json:"staging_dir"`
	FailureReceipt string                                  `json:"failure_receipt,omitempty"`
}

// RestoreFailureReceipt is bounded failure evidence. It deliberately contains
// no command output, connection string, credential, environment value, dump
// byte, staging path, or arbitrary error text.
type RestoreFailureReceipt struct {
	Schema                      string                      `json:"schema"`
	Status                      string                      `json:"status"`
	Ref                         string                      `json:"ref"`
	Backend                     string                      `json:"backend"`
	Archive                     string                      `json:"archive"`
	DirectArchiveManifestSHA256 string                      `json:"direct_archive_manifest_sha256"`
	OperationKind               string                      `json:"operation_kind"`
	TargetDatabaseClass         restoreauthority.Kind       `json:"target_database_class"`
	TargetDatabase              string                      `json:"target_database"`
	FailureStage                string                      `json:"failure_stage"`
	FailureCode                 string                      `json:"failure_code"`
	LOOMErrorCode               string                      `json:"loom_error_code"`
	CleanupStatus               string                      `json:"cleanup_status"`
	CleanupAttempted            bool                        `json:"cleanup_attempted"`
	CleanupSucceeded            bool                        `json:"cleanup_succeeded"`
	OccurredAt                  time.Time                   `json:"occurred_at"`
	FetchFailure                *RestoreFetchFailureDetails `json:"fetch_failure,omitempty"`
}

func RestoreDrill(ctx context.Context, input CloudRestoreDrillInput) (CloudRestoreDrillResult, error) {
	if !input.DryRun && input.KeepDatabase && input.RestoreAuthority != nil {
		return CloudRestoreDrillResult{}, fmt.Errorf("restore authority requires mandatory disposable database cleanup; --keep is refused")
	}
	cfg, err := NormalizeConfig(input.Config)
	if err != nil {
		return CloudRestoreDrillResult{}, err
	}
	if !cfg.Enabled {
		return CloudRestoreDrillResult{}, fmt.Errorf("cloud storage is disabled")
	}
	input.TargetDatabase = strings.TrimSpace(input.TargetDatabase)
	input.ProvenanceTargetDatabase = strings.TrimSpace(input.ProvenanceTargetDatabase)
	now := normalizeNow(input.Now)
	attemptedAt := now()
	{
		if strings.TrimSpace(input.TargetDatabase) == "" {
			input.TargetDatabase = backup.DefaultRestoreDrillDatabase(attemptedAt)
		}
		if strings.TrimSpace(input.ProvenanceTargetDatabase) == "" {
			input.ProvenanceTargetDatabase = backup.DefaultProvenanceRestoreDrillDatabase(attemptedAt)
		}
	}
	ref := strings.TrimSpace(input.Ref)
	if ref == "" {
		ref = "latest"
	}
	if len(ref) > 180 || !restoreFailureTokenPattern.MatchString(ref) {
		return CloudRestoreDrillResult{}, loomerrors.New("cloud.restore_ref_invalid", "cloud", "ref", "Restore ref must be a bounded archive selector.")
	}
	stateDir := firstNonEmpty(input.StateDir, cfg.StateDir)
	for kind, target := range map[restoreauthority.Kind]string{restoreauthority.KindOperational: input.TargetDatabase, restoreauthority.KindProvenance: input.ProvenanceTargetDatabase} {
		if err := restoreauthority.ValidateDisposableDatabase(kind, target, input.ActiveDatabase, input.ProvenanceActiveDatabase); err != nil {
			return CloudRestoreDrillResult{}, loomerrors.New("cloud.restore_target_invalid", "cloud", "target_database", "Restore targets must be disposable databases outside both active databases.")
		}
	}
	attempt, attemptIdentity, err := prepareRestoreFetchAttempt(stateDir, ref, attemptedAt)
	if err != nil {
		return CloudRestoreDrillResult{}, loomerrors.New("cloud.restore_attempt_failed", "cloud", "restore-drill", "Could not safely prepare a fresh restore attempt.")
	}
	lifecycle, err := startRestoreAttemptLifecycle(attempt, attemptIdentity)
	if err != nil {
		return CloudRestoreDrillResult{}, loomerrors.New("cloud.restore_attempt_failed", "cloud", "restore-drill", "Could not safely lock the fresh restore attempt lifecycle.")
	}
	defer lifecycle.close()
	staging := filepath.Join(attempt, "backup")
	input.Config = cfg
	fetch, err := FetchSnapshot(ctx, SnapshotFetchInput{
		restoreLifecycle: lifecycle,
		Config:           cfg,
		Driver:           input.Driver,
		NodeID:           input.NodeID,
		Ref:              ref,
		To:               staging,
		Now:              input.Now,
	})
	if err != nil || fetch.Status == SnapshotStatusFailed {
		receipt, typedErr := restoreFetchFailureEvidence(fetch, input, now(), err)
		result := CloudRestoreDrillResult{Status: SnapshotStatusFailed, DryRun: input.DryRun, Ref: receipt.Ref, Backend: receipt.Backend, Archive: receipt.Archive, StagingDir: staging}
		path, publishErr := publishRestoreFailureReceiptBound(attempt, receipt, &attemptIdentity)
		if publishErr != nil {
			typedErr.Summary += " Failure evidence could not be published safely."
		} else {
			result.FailureReceipt = path
			typedErr.Summary += " Failure evidence saved in " + filepath.Base(attempt) + "/" + RestoreFailureReceiptFile + "."
		}
		return result, typedErr
	}
	result := CloudRestoreDrillResult{
		Status:     fetch.Status,
		DryRun:     input.DryRun,
		Ref:        fetch.Ref,
		Backend:    fetch.Backend,
		Repository: fetch.Repository,
		Archive:    fetch.Archive,
		RemoteURI:  fetch.RemoteURI,
		Fetch:      fetch,
		StagingDir: staging,
	}
	result.Fetch.V2UserSymlinkTargets = cloneV2UserSymlinkTargets(fetch.V2UserSymlinkTargets)
	if fetch.Status != SnapshotStatusSucceeded {
		return result, nil
	}
	if fetch.DirectArchiveManifestSHA256 != "" && fetch.ExtractionVerification != nil {
		directInput := directArchiveRestoreDrillInput(fetch, input, staging)
		if input.DryRun {
			plan, planErr := backup.PlanDirectArchiveRestoreDrill(ctx, directInput)
			if planErr != nil {
				return CloudRestoreDrillResult{}, planErr
			}
			result.DirectPlan = &plan
			result.Status = plan.Status
			return result, nil
		}
		if directInput.ProvenanceRestore == nil {
			directInput.ProvenanceRestore = backup.NewProvenanceDirectArchiveRecoveryStep(backup.ProvenanceDirectArchiveRecoveryConfig{
				SourceDatabase: input.ProvenanceActiveDatabase,
				TargetDatabase: input.ProvenanceTargetDatabase,
				DatabaseURL:    input.ProvenanceDatabaseURL,
				Owner:          input.ProvenanceOwner,
				KeepDatabase:   input.KeepDatabase,
				Runner:         input.Runner,
				Authority:      input.RestoreAuthority,
				Now:            input.Now,
			})
		}
		restored, restoreErr := backup.RunDirectArchiveRestoreDrill(ctx, directInput)
		if restoreErr != nil {
			result.Status = SnapshotStatusFailed
			result.DirectResult = &restored
			occurredAt := now()
			receipt, typedErr := restoreFailureEvidence(fetch, restored, input, occurredAt, restoreErr)
			receiptPath, receiptErr := publishRestoreFailureReceiptBound(filepath.Dir(staging), receipt, &attemptIdentity)
			if receiptErr == nil {
				result.FailureReceipt = receiptPath
			} else {
				typedErr.Cause = errors.Join(typedErr.Cause, loomerrors.New(
					"cloud.restore_failure_receipt_failed", "cloud", RestoreFailureReceiptFile,
					"Strict restore failure evidence could not be published safely.",
				))
			}
			return result, typedErr
		}
		result.DirectResult = &restored
		result.Status = restored.Status
		return result, nil
	}
	restoreInput := backup.RestoreDrillInput{
		BackupDir:      fetch.TargetDir,
		TargetDatabase: input.TargetDatabase,
		ActiveDatabase: input.ActiveDatabase,
		Owner:          input.Owner,
		KeepDatabase:   input.KeepDatabase,
		Runner:         input.Runner,
	}
	if input.DryRun {
		plan, err := backup.PlanRestoreDrill(ctx, restoreInput)
		if err != nil {
			return CloudRestoreDrillResult{}, err
		}
		result.Plan = &plan
		result.Status = plan.Status
		return result, nil
	}
	restoreResult, err := backup.RunRestoreDrill(ctx, restoreInput)
	if err != nil {
		return CloudRestoreDrillResult{}, err
	}
	result.Result = &restoreResult
	result.Status = restoreResult.Status
	return result, nil
}

func restoreFailureEvidence(fetch SnapshotFetchResult, restored backup.DirectArchiveRestoreDrillResult, input CloudRestoreDrillInput, occurredAt time.Time, cause error) (RestoreFailureReceipt, *loomerrors.Error) {
	stage := "restore_orchestration"
	failureCode := "restore_failed"
	loomCode := "backup.strict_restore_failed"
	domain := "backup"
	targetClass := restoreauthority.KindOperational
	targetDatabase := strings.TrimSpace(input.TargetDatabase)
	if restored.Plan.OperationalRestorePlan.TargetDatabase != "" {
		targetDatabase = restored.Plan.OperationalRestorePlan.TargetDatabase
	}
	if targetDatabase == "" {
		targetDatabase = backup.DefaultRestoreDrillDatabase(occurredAt)
	}
	summary := "Strict direct-archive restore failed."

	var authorityErr *restoreauthority.AuthorityError
	if errors.As(cause, &authorityErr) {
		stage = string(authorityErr.Stage)
		failureCode = authorityErr.Code
		loomCode = "backup.restore_authority." + authorityErr.Code
		targetClass = authorityErr.Result.Kind
		targetDatabase = authorityErr.Result.Database
		summary = fmt.Sprintf("Strict restore authority failed at the %s stage.", authorityErr.Stage)
	} else {
		if restored.OperationalRecovery.Status == "succeeded" {
			targetClass = restoreauthority.KindProvenance
			targetDatabase = strings.TrimSpace(input.ProvenanceTargetDatabase)
			if targetDatabase == "" {
				targetDatabase = backup.DefaultProvenanceRestoreDrillDatabase(occurredAt)
			}
		}
		var coded *loomerrors.Error
		if errors.As(cause, &coded) {
			loomCode, domain, summary = coded.Code, coded.Domain, coded.Summary
		}
	}

	receipt := RestoreFailureReceipt{
		Schema: RestoreFailureReceiptSchema, Status: SnapshotStatusFailed,
		Ref: fetch.Ref, Backend: fetch.Backend, Archive: fetch.Archive,
		DirectArchiveManifestSHA256: fetch.DirectArchiveManifestSHA256,
		OperationKind:               "direct_archive_strict_restore",
		TargetDatabaseClass:         targetClass, TargetDatabase: targetDatabase,
		FailureStage: stage, FailureCode: failureCode, LOOMErrorCode: loomCode,
		CleanupStatus: "unknown", OccurredAt: occurredAt.UTC(),
	}
	if authorityErr != nil {
		receipt.CleanupAttempted = authorityErr.Result.CleanupAttempted
		receipt.CleanupSucceeded = authorityErr.Result.CleanupSucceeded
		switch {
		case receipt.CleanupSucceeded:
			receipt.CleanupStatus = "succeeded"
		case receipt.CleanupAttempted:
			receipt.CleanupStatus = "failed"
		case authorityFailurePrecedesDatabaseMutation(authorityErr.Code):
			receipt.CleanupStatus = "not_attempted"
		}
	}
	var databaseFailure *backup.RestoreDatabaseFailure
	if errors.As(cause, &databaseFailure) {
		receipt.TargetDatabaseClass = databaseFailure.Kind
		receipt.TargetDatabase = databaseFailure.Database
		receipt.CleanupAttempted = databaseFailure.CleanupAttempted
		receipt.CleanupSucceeded = databaseFailure.CleanupSucceeded
		if databaseFailure.CleanupSucceeded {
			receipt.CleanupStatus = "succeeded"
		} else if databaseFailure.CleanupAttempted {
			receipt.CleanupStatus = "failed"
		}
	}
	return receipt, loomerrors.Wrap(loomCode, domain, receipt.TargetDatabase, summary, cause)
}

func authorityFailurePrecedesDatabaseMutation(code string) bool {
	switch code {
	case restoreauthority.ErrorRequestRefused,
		restoreauthority.ErrorSocketIdentityInvalid,
		restoreauthority.ErrorPeerIdentityInvalid,
		restoreauthority.ErrorProtocolInvalid,
		restoreauthority.ErrorRequestInvalid,
		restoreauthority.ErrorDatabaseRefused,
		restoreauthority.ErrorPayloadInvalid,
		restoreauthority.ErrorRequestCancelled,
		restoreauthority.ErrorClientSocketInvalid,
		restoreauthority.ErrorClientConnectFailed,
		restoreauthority.ErrorClientPeerInvalid,
		restoreauthority.ErrorClientSendFailed,
		restoreauthority.ErrorClientPayloadFailed:
		return true
	default:
		return false
	}
}

func publishRestoreFailureReceipt(drillDirectory string, receipt RestoreFailureReceipt) (string, error) {
	return publishRestoreFailureReceiptBound(drillDirectory, receipt, nil)
}

func publishRestoreFailureReceiptBound(drillDirectory string, receipt RestoreFailureReceipt, expected *unix.Stat_t) (string, error) {
	if err := validateRestoreFailureReceipt(receipt); err != nil {
		return "", err
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return "", err
	}
	raw = append(raw, '\n')
	if len(raw) > maxRestoreFailureReceiptBytes {
		return "", fmt.Errorf("restore failure receipt exceeds its byte bound")
	}

	directoryFD, err := openRestoreFailureDirectory(drillDirectory)
	if err != nil {
		return "", fmt.Errorf("open exact restore drill directory: %w", err)
	}
	defer unix.Close(directoryFD)
	var directoryStat unix.Stat_t
	if err := unix.Fstat(directoryFD, &directoryStat); err != nil || directoryStat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return "", fmt.Errorf("restore drill directory identity is invalid")
	}
	if expected != nil && (directoryStat.Dev != expected.Dev || directoryStat.Ino != expected.Ino) {
		return "", fmt.Errorf("restore attempt directory was replaced")
	}
	if err := unix.Flock(directoryFD, unix.LOCK_EX); err != nil {
		return "", fmt.Errorf("lock restore drill directory for failure evidence: %w", err)
	}
	defer unix.Flock(directoryFD, unix.LOCK_UN)

	if existing, exists, err := readRestoreFailureReceiptAt(directoryFD); err != nil {
		return "", err
	} else if exists {
		if !bytes.Equal(existing, raw) {
			return "", fmt.Errorf("restore failure receipt conflicts with existing bytes")
		}
		if err := revalidateRestoreFailureDirectory(drillDirectory, directoryFD); err != nil {
			return "", err
		}
		return filepath.Join(drillDirectory, RestoreFailureReceiptFile), nil
	}

	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("create restore failure receipt nonce: %w", err)
	}
	temporaryName := ".restore-failure-" + hex.EncodeToString(random) + ".tmp"
	temporaryFD, err := unix.Openat(directoryFD, temporaryName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return "", fmt.Errorf("create private restore failure receipt: %w", err)
	}
	temporaryPresent := true
	defer func() {
		if temporaryFD >= 0 {
			_ = unix.Close(temporaryFD)
		}
		if temporaryPresent {
			_ = unix.Unlinkat(directoryFD, temporaryName, 0)
		}
	}()
	if err := unix.Fchmod(temporaryFD, 0o600); err != nil {
		return "", fmt.Errorf("protect restore failure receipt: %w", err)
	}
	file := os.NewFile(uintptr(temporaryFD), temporaryName)
	if file == nil {
		return "", fmt.Errorf("bind restore failure receipt descriptor")
	}
	temporaryFD = -1
	fileOpen := true
	defer func() {
		if fileOpen {
			_ = file.Close()
		}
	}()
	if written, err := file.Write(raw); err != nil || written != len(raw) {
		if err == nil {
			err = io.ErrShortWrite
		}
		return "", fmt.Errorf("write restore failure receipt: %w", err)
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("sync restore failure receipt: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close restore failure receipt: %w", err)
	}
	fileOpen = false
	if err := revalidateRestoreFailureDirectory(drillDirectory, directoryFD); err != nil {
		return "", err
	}
	if err := unix.Linkat(directoryFD, temporaryName, directoryFD, RestoreFailureReceiptFile, 0); err != nil {
		if errors.Is(err, unix.EEXIST) {
			existing, exists, readErr := readRestoreFailureReceiptAt(directoryFD)
			if readErr != nil {
				return "", readErr
			}
			if !exists || !bytes.Equal(existing, raw) {
				return "", fmt.Errorf("restore failure receipt conflicts with concurrent bytes")
			}
			if err := revalidateRestoreFailureDirectory(drillDirectory, directoryFD); err != nil {
				return "", err
			}
			return filepath.Join(drillDirectory, RestoreFailureReceiptFile), nil
		}
		return "", fmt.Errorf("publish restore failure receipt without overwrite: %w", err)
	}
	if err := unix.Unlinkat(directoryFD, temporaryName, 0); err != nil {
		return "", fmt.Errorf("finalize restore failure receipt link count: %w", err)
	}
	temporaryPresent = false
	if err := unix.Fsync(directoryFD); err != nil {
		return "", fmt.Errorf("sync restore failure receipt directory: %w", err)
	}
	existing, exists, err := readRestoreFailureReceiptAt(directoryFD)
	if err != nil {
		return "", err
	}
	if !exists || !bytes.Equal(existing, raw) {
		return "", fmt.Errorf("published restore failure receipt identity changed")
	}
	if err := revalidateRestoreFailureDirectory(drillDirectory, directoryFD); err != nil {
		return "", err
	}
	return filepath.Join(drillDirectory, RestoreFailureReceiptFile), nil
}

func revalidateRestoreFailureDirectory(path string, held int) error {
	named, err := openRestoreFailureDirectory(path)
	if err != nil {
		return fmt.Errorf("restore failure directory binding changed")
	}
	defer unix.Close(named)
	var a, b unix.Stat_t
	if unix.Fstat(held, &a) != nil || unix.Fstat(named, &b) != nil || a.Dev != b.Dev || a.Ino != b.Ino {
		return fmt.Errorf("restore failure directory binding changed")
	}
	return nil
}

func openRestoreFailureDirectory(path string) (int, error) {
	clean := filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(clean) || clean == string(filepath.Separator) {
		return -1, fmt.Errorf("restore drill directory must be an exact absolute non-root path")
	}
	current, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	for _, component := range strings.Split(strings.TrimPrefix(clean, string(filepath.Separator)), string(filepath.Separator)) {
		next, openErr := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(current)
		if openErr != nil {
			return -1, openErr
		}
		current = next
	}
	return current, nil
}

func readRestoreFailureReceiptAt(directoryFD int) ([]byte, bool, error) {
	fd, err := unix.Openat(directoryFD, RestoreFailureReceiptFile, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("open existing restore failure receipt: %w", err)
	}
	file := os.NewFile(uintptr(fd), RestoreFailureReceiptFile)
	if file == nil {
		_ = unix.Close(fd)
		return nil, false, fmt.Errorf("bind existing restore failure receipt descriptor")
	}
	defer file.Close()
	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return nil, false, fmt.Errorf("inspect existing restore failure receipt: %w", err)
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Mode&0o7777 != 0o600 || before.Nlink != 1 || before.Size <= 0 || before.Size > maxRestoreFailureReceiptBytes {
		return nil, false, fmt.Errorf("existing restore failure receipt type, mode, link count, or size is invalid")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxRestoreFailureReceiptBytes+1))
	if err != nil || int64(len(raw)) != before.Size {
		return nil, false, fmt.Errorf("read exact existing restore failure receipt")
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil || before.Dev != after.Dev || before.Ino != after.Ino || before.Mode != after.Mode || before.Nlink != after.Nlink || before.Size != after.Size {
		return nil, false, fmt.Errorf("existing restore failure receipt changed while reading")
	}
	var named unix.Stat_t
	if err := unix.Fstatat(directoryFD, RestoreFailureReceiptFile, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil || before.Dev != named.Dev || before.Ino != named.Ino || before.Mode != named.Mode || before.Nlink != named.Nlink || before.Size != named.Size {
		return nil, false, fmt.Errorf("existing restore failure receipt name changed while reading")
	}
	var receipt RestoreFailureReceipt
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, false, fmt.Errorf("existing restore failure receipt schema is invalid")
	}
	if err := validateRestoreFailureReceipt(receipt); err != nil {
		return nil, false, err
	}
	return raw, true, nil
}

func validateRestoreFailureReceipt(receipt RestoreFailureReceipt) error {
	if receipt.Schema == RestoreFetchFailureReceiptSchema {
		return validateRestoreFetchFailureReceipt(receipt)
	}
	if receipt.FetchFailure != nil {
		return fmt.Errorf("v1 receipt cannot contain fetch evidence")
	}
	if receipt.Schema != RestoreFailureReceiptSchema || receipt.Status != SnapshotStatusFailed || receipt.OperationKind != "direct_archive_strict_restore" {
		return fmt.Errorf("restore failure receipt contract is invalid")
	}
	for label, value := range map[string]string{
		"ref": receipt.Ref, "backend": receipt.Backend, "archive": receipt.Archive,
		"failure_stage": receipt.FailureStage, "failure_code": receipt.FailureCode,
		"loom_error_code": receipt.LOOMErrorCode, "cleanup_status": receipt.CleanupStatus,
	} {
		if len(value) == 0 || len(value) > 255 || !restoreFailureTokenPattern.MatchString(value) {
			return fmt.Errorf("restore failure receipt %s is invalid", label)
		}
	}
	if !restoreFailureDigestPattern.MatchString(receipt.DirectArchiveManifestSHA256) {
		return fmt.Errorf("restore failure receipt manifest identity is invalid")
	}
	if err := restoreauthority.ValidateDisposableDatabase(receipt.TargetDatabaseClass, receipt.TargetDatabase); err != nil {
		return fmt.Errorf("restore failure receipt database identity is invalid")
	}
	if receipt.FailureStage == "restore_orchestration" {
		if receipt.FailureCode != "restore_failed" {
			return fmt.Errorf("restore failure receipt orchestration code is invalid")
		}
	} else if err := restoreauthority.ValidateFailurePair(restoreauthority.FailureStage(receipt.FailureStage), receipt.FailureCode); err != nil {
		return err
	}
	switch receipt.CleanupStatus {
	case "succeeded":
		if !receipt.CleanupAttempted || !receipt.CleanupSucceeded {
			return fmt.Errorf("restore failure receipt cleanup evidence is invalid")
		}
	case "failed":
		if !receipt.CleanupAttempted || receipt.CleanupSucceeded {
			return fmt.Errorf("restore failure receipt cleanup evidence is invalid")
		}
	case "not_attempted", "unknown":
		if receipt.CleanupAttempted || receipt.CleanupSucceeded {
			return fmt.Errorf("restore failure receipt cleanup evidence is invalid")
		}
	default:
		return fmt.Errorf("restore failure receipt cleanup status is invalid")
	}
	if receipt.CleanupSucceeded && !receipt.CleanupAttempted {
		return fmt.Errorf("restore failure receipt cleanup evidence is invalid")
	}
	if receipt.OccurredAt.IsZero() || receipt.OccurredAt.Location() != time.UTC {
		return fmt.Errorf("restore failure receipt occurrence time is invalid")
	}
	return nil
}

var restoreFailureDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func directArchiveRestoreDrillInput(fetch SnapshotFetchResult, input CloudRestoreDrillInput, staging string) backup.DirectArchiveRestoreDrillInput {
	return backup.DirectArchiveRestoreDrillInput{
		ArchiveRoot: staging, ExpectedManifestSHA256: fetch.DirectArchiveManifestSHA256,
		ExpectedRepository: fetch.Repository, ExpectedArchive: fetch.Archive,
		V2UserSymlinkTargets: cloneV2UserSymlinkTargets(fetch.V2UserSymlinkTargets),
		OperationalTarget:    input.TargetDatabase, ActiveDatabase: input.ActiveDatabase,
		Owner: input.Owner, KeepOperationalDatabase: input.KeepDatabase,
		Runner: input.Runner, Authority: input.RestoreAuthority,
		ProvenanceRestore: input.ProvenanceRestore, Now: input.Now,
	}
}
