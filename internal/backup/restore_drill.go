package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sys/unix"

	"loom.local/loom/internal/backupstrategy"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/provenance"
	"loom.local/loom/internal/restoreauthority"
)

const RestoreDrillDatabasePrefix = "loom_restore_drill_"

const ProvenanceRestoreDrillDatabasePrefix = "loom_provenance_restore_drill_"

const directArchiveRestoreCleanupTimeout = 30 * time.Second

var restoreDrillDatabasePattern = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

type CommandRunner func(ctx context.Context, name string, args []string, stdin io.Reader) ([]byte, error)

type RestoreDrillInput struct {
	BackupDir      string
	TargetDatabase string
	ActiveDatabase string
	Owner          string
	KeepDatabase   bool
	Now            func() time.Time
	Runner         CommandRunner
}

type RestoreDrillResult struct {
	Status          string                         `json:"status"`
	BackupDir       string                         `json:"backup_dir"`
	TargetDatabase  string                         `json:"target_database"`
	ActiveDatabase  string                         `json:"active_database"`
	Owner           string                         `json:"owner"`
	KeptDatabase    bool                           `json:"kept_database"`
	StartedAt       time.Time                      `json:"started_at"`
	FinishedAt      time.Time                      `json:"finished_at"`
	Verification    maintenance.BackupVerification `json:"verification"`
	DatabaseSummary map[string]any                 `json:"database_summary,omitempty"`
}

type RestoreDrillPlan struct {
	Status         string                         `json:"status"`
	BackupDir      string                         `json:"backup_dir"`
	TargetDatabase string                         `json:"target_database"`
	ActiveDatabase string                         `json:"active_database"`
	Owner          string                         `json:"owner"`
	KeepDatabase   bool                           `json:"keep_database"`
	Verification   maintenance.BackupVerification `json:"verification"`
}

type OperationalRestoreDrillInput struct {
	PackageDir             string
	ExpectedManifestSHA256 string
	ExpectedPackageID      string
	TargetDatabase         string
	ActiveDatabase         string
	Owner                  string
	KeepDatabase           bool
	Now                    func() time.Time
	Runner                 CommandRunner
	Authority              restoreauthority.RestoreDatabaseAuthority
}

type OperationalRestoreDrillPlan struct {
	Status         string                                        `json:"status"`
	PackageDir     string                                        `json:"package_dir"`
	PackageID      string                                        `json:"package_id"`
	ManifestSHA256 string                                        `json:"manifest_sha256"`
	TargetDatabase string                                        `json:"target_database"`
	ActiveDatabase string                                        `json:"active_database"`
	Owner          string                                        `json:"owner"`
	KeepDatabase   bool                                          `json:"keep_database"`
	Verification   backupstrategy.OperationalPackageVerification `json:"verification"`
}

type OperationalRestoreDrillResult struct {
	Status          string                                        `json:"status"`
	PackageDir      string                                        `json:"package_dir"`
	PackageID       string                                        `json:"package_id"`
	ManifestSHA256  string                                        `json:"manifest_sha256"`
	TargetDatabase  string                                        `json:"target_database"`
	ActiveDatabase  string                                        `json:"active_database"`
	Owner           string                                        `json:"owner"`
	KeptDatabase    bool                                          `json:"kept_database"`
	StartedAt       time.Time                                     `json:"started_at"`
	FinishedAt      time.Time                                     `json:"finished_at"`
	Verification    backupstrategy.OperationalPackageVerification `json:"verification"`
	DatabaseSummary map[string]any                                `json:"database_summary,omitempty"`
}

// RestoreDatabaseFailure preserves the final mandatory cleanup truth around a
// failed operational or Provenance restore while leaving typed authority
// errors discoverable through ordinary wrapping and errors.Join.
type RestoreDatabaseFailure struct {
	Kind             restoreauthority.Kind
	Database         string
	CleanupAttempted bool
	CleanupSucceeded bool
	Cause            error
}

func (failure *RestoreDatabaseFailure) Error() string {
	if failure == nil {
		return ""
	}
	return fmt.Sprintf("disposable %s database restore failed: %v", failure.Kind, failure.Cause)
}

func (failure *RestoreDatabaseFailure) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.Cause
}

type DirectArchiveRecoveryStepInput struct {
	PackageDir     string `json:"package_dir"`
	PackageID      string `json:"package_id"`
	ManifestSHA256 string `json:"manifest_sha256"`
	SchemaHead     int64  `json:"schema_head"`
}

type DirectArchiveRecoveryStepResult struct {
	Status         string         `json:"status"`
	PackageID      string         `json:"package_id"`
	ManifestSHA256 string         `json:"manifest_sha256"`
	SchemaHead     int64          `json:"schema_head"`
	Summary        map[string]any `json:"summary,omitempty"`
}

type DirectArchiveRecoveryStep func(context.Context, DirectArchiveRecoveryStepInput) (DirectArchiveRecoveryStepResult, error)

type DirectArchiveRestoreDrillInput struct {
	ArchiveRoot             string
	ExpectedManifestSHA256  string
	ExpectedRepository      string
	ExpectedArchive         string
	V2UserSymlinkTargets    map[string]string `json:"-"`
	OperationalTarget       string
	ActiveDatabase          string
	Owner                   string
	KeepOperationalDatabase bool
	Runner                  CommandRunner
	Authority               restoreauthority.RestoreDatabaseAuthority
	OperationalRestore      DirectArchiveRecoveryStep
	ProvenanceRestore       DirectArchiveRecoveryStep
	Now                     func() time.Time
}

type DirectArchiveRestoreDrillPlan struct {
	Status                  string                                             `json:"status"`
	ArchiveRoot             string                                             `json:"archive_root"`
	Repository              string                                             `json:"repository"`
	Archive                 string                                             `json:"archive"`
	ArchiveRef              string                                             `json:"archive_ref"`
	ArchiveClass            string                                             `json:"archive_class"`
	ManifestSHA256          string                                             `json:"manifest_sha256"`
	Extraction              backupstrategy.DirectArchiveExtractionVerification `json:"extraction"`
	OperationalPackageDir   string                                             `json:"operational_package_dir"`
	OperationalVerification backupstrategy.OperationalPackageVerification      `json:"operational_verification"`
	ProvenancePackageDir    string                                             `json:"provenance_package_dir"`
	ProvenancePackageID     string                                             `json:"provenance_package_id"`
	ProvenanceVerification  maintenance.ProvenanceBackupVerification           `json:"provenance_verification"`
	OperationalRestorePlan  OperationalRestoreDrillPlan                        `json:"operational_restore_plan"`
}

type DirectArchiveRestoreDrillResult struct {
	Status              string                          `json:"status"`
	StartedAt           time.Time                       `json:"started_at"`
	FinishedAt          time.Time                       `json:"finished_at"`
	Plan                DirectArchiveRestoreDrillPlan   `json:"plan"`
	OperationalRecovery DirectArchiveRecoveryStepResult `json:"operational_recovery"`
	ProvenanceRecovery  DirectArchiveRecoveryStepResult `json:"provenance_recovery"`
}

func PlanOperationalRestoreDrill(ctx context.Context, input OperationalRestoreDrillInput) (OperationalRestoreDrillPlan, error) {
	input.PackageDir = filepath.Clean(strings.TrimSpace(input.PackageDir))
	if input.PackageDir == "." || input.PackageDir == "" {
		return OperationalRestoreDrillPlan{}, loomerrors.New("backup.operational_directory_required", "backup", "package_dir", "Operational package directory is required.")
	}
	verification, err := backupstrategy.VerifyOperationalPackage(ctx, backupstrategy.OperationalPackageVerificationInput{
		PackageDir: input.PackageDir, ExpectedManifestSHA256: strings.TrimSpace(input.ExpectedManifestSHA256),
		ExpectedPackageID: strings.TrimSpace(input.ExpectedPackageID),
	})
	if err != nil {
		return OperationalRestoreDrillPlan{}, err
	}
	if verification.Status != "succeeded" || verification.Manifest == nil {
		return OperationalRestoreDrillPlan{}, loomerrors.New("backup.operational_verification_failed", "backup", input.PackageDir, "Operational recovery package verification failed.")
	}
	input.ActiveDatabase = strings.TrimSpace(input.ActiveDatabase)
	if input.ActiveDatabase == "" {
		input.ActiveDatabase = "loom_main"
	}
	input.TargetDatabase = strings.TrimSpace(input.TargetDatabase)
	if input.TargetDatabase == "" {
		input.TargetDatabase = DefaultRestoreDrillDatabase(currentTime(input.Now))
	}
	if err := ValidateRestoreDrillDatabase(input.TargetDatabase, input.ActiveDatabase); err != nil {
		return OperationalRestoreDrillPlan{}, err
	}
	input.Owner = strings.TrimSpace(input.Owner)
	if input.Owner == "" {
		input.Owner = "loom"
	}
	return OperationalRestoreDrillPlan{
		Status: "planned", PackageDir: verification.PackageDir, PackageID: verification.PackageID,
		ManifestSHA256: verification.ManifestSHA256, TargetDatabase: input.TargetDatabase,
		ActiveDatabase: input.ActiveDatabase, Owner: input.Owner, KeepDatabase: input.KeepDatabase,
		Verification: verification,
	}, nil
}

func RunOperationalRestoreDrill(ctx context.Context, input OperationalRestoreDrillInput) (OperationalRestoreDrillResult, error) {
	startedAt := currentTime(input.Now)
	plan, err := PlanOperationalRestoreDrill(ctx, input)
	if err != nil {
		return OperationalRestoreDrillResult{}, err
	}
	var dumpArtifact backupstrategy.OperationalPackageArtifact
	for _, artifact := range plan.Verification.Manifest.Artifacts {
		if artifact.Kind == backupstrategy.OperationalArtifactPostgresDump {
			dumpArtifact = artifact
			break
		}
	}
	if dumpArtifact.Path == "" {
		return OperationalRestoreDrillResult{}, loomerrors.New("backup.dump_missing", "backup", backupstrategy.OperationalArtifactPostgresDump, "Operational package does not name its PostgreSQL dump.")
	}
	dump, err := openVerifiedOperationalDump(plan.PackageDir, dumpArtifact)
	if err != nil {
		return OperationalRestoreDrillResult{}, err
	}
	defer dump.Close()
	if input.Authority == nil {
		return OperationalRestoreDrillResult{}, loomerrors.New("backup.restore_authority_required", "backup", "restore_authority", "A typed disposable database restore authority is required.")
	}
	if plan.KeepDatabase {
		return OperationalRestoreDrillResult{}, loomerrors.New("backup.restore_authority_keep_refused", "backup", "keep_database", "The restore authority requires mandatory disposable database cleanup.")
	}
	runner := input.Runner
	if runner == nil {
		runner = defaultCommandRunner
	}
	cleanup := func() error {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), directArchiveRestoreCleanupTimeout)
		defer cancelCleanup()
		result, cleanupErr := input.Authority.Drop(cleanupCtx, restoreauthority.DropRequest{Kind: restoreauthority.KindOperational, Database: plan.TargetDatabase})
		if cleanupErr != nil {
			return cleanupErr
		}
		return validateAuthorityResult(result, restoreauthority.KindOperational, plan.TargetDatabase, true)
	}
	returnFailure := func(primary error) (OperationalRestoreDrillResult, error) {
		cleanupErr := cleanup()
		if cleanupErr != nil {
			primary = errors.Join(primary, fmt.Errorf("cleanup operational restore database: %w", cleanupErr))
		}
		return OperationalRestoreDrillResult{}, &RestoreDatabaseFailure{
			Kind: restoreauthority.KindOperational, Database: plan.TargetDatabase,
			CleanupAttempted: true, CleanupSucceeded: cleanupErr == nil, Cause: primary,
		}
	}
	if err := dump.VerifySource(); err != nil {
		return returnFailure(fmt.Errorf("operational PostgreSQL dump changed before pg_restore: %w", err))
	}
	authorityResult, restoreErr := input.Authority.Restore(ctx, restoreauthority.RestoreRequest{
		Kind: restoreauthority.KindOperational, Database: plan.TargetDatabase,
		DumpSize: dumpArtifact.SizeBytes, DumpSHA256: strings.ToLower(strings.TrimSpace(dumpArtifact.SHA256)), Dump: dump.Reader(),
	})
	driftErr := dump.VerifySource()
	if restoreErr == nil {
		restoreErr = validateAuthorityResult(authorityResult, restoreauthority.KindOperational, plan.TargetDatabase, false)
	}
	if restoreErr != nil || driftErr != nil {
		var restoreErrors []error
		if restoreErr != nil {
			restoreErrors = append(restoreErrors, fmt.Errorf("operational restore pg_restore: %w", restoreErr))
		}
		if driftErr != nil {
			restoreErrors = append(restoreErrors, fmt.Errorf("operational PostgreSQL dump changed during pg_restore: %w", driftErr))
		}
		return returnFailure(errors.Join(restoreErrors...))
	}
	output, err := runner(ctx, "psql", []string{"--no-psqlrc", "--dbname", plan.TargetDatabase, "--set", "ON_ERROR_STOP=1", "--quiet", "--tuples-only", "--no-align"}, strings.NewReader(restoreDrillSQL))
	if err != nil {
		return returnFailure(fmt.Errorf("operational restore verification: %w", err))
	}
	if err := cleanup(); err != nil {
		return OperationalRestoreDrillResult{}, &RestoreDatabaseFailure{
			Kind: restoreauthority.KindOperational, Database: plan.TargetDatabase,
			CleanupAttempted: true, CleanupSucceeded: false,
			Cause: fmt.Errorf("cleanup operational restore database: %w", err),
		}
	}
	return OperationalRestoreDrillResult{
		Status: "succeeded", PackageDir: plan.PackageDir, PackageID: plan.PackageID,
		ManifestSHA256: plan.ManifestSHA256, TargetDatabase: plan.TargetDatabase,
		ActiveDatabase: plan.ActiveDatabase, Owner: plan.Owner, KeptDatabase: false,
		StartedAt: startedAt, FinishedAt: currentTime(input.Now), Verification: plan.Verification,
		DatabaseSummary: parseRestoreDrillSummary(output),
	}, nil
}

type verifiedOperationalDump struct {
	directory *os.File
	source    *os.File
	spool     *os.File
	name      string
	artifact  backupstrategy.OperationalPackageArtifact
	device    uint64
	inode     uint64
}

func openVerifiedOperationalDump(packageDir string, artifact backupstrategy.OperationalPackageArtifact) (*verifiedOperationalDump, error) {
	if filepath.FromSlash(artifact.Path) != filepath.Base(filepath.FromSlash(artifact.Path)) || artifact.Path == "." {
		return nil, fmt.Errorf("operational PostgreSQL dump path is not an exact package artifact")
	}
	directoryFD, err := unix.Open(packageDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open operational package directory without following links: %w", err)
	}
	directory := os.NewFile(uintptr(directoryFD), packageDir)
	if directory == nil {
		_ = unix.Close(directoryFD)
		return nil, fmt.Errorf("bind operational package directory descriptor")
	}
	name := filepath.FromSlash(artifact.Path)
	sourceFD, err := unix.Openat(directoryFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		_ = directory.Close()
		return nil, fmt.Errorf("open operational PostgreSQL dump without following links: %w", err)
	}
	source := os.NewFile(uintptr(sourceFD), filepath.Join(packageDir, name))
	if source == nil {
		_ = unix.Close(sourceFD)
		_ = directory.Close()
		return nil, fmt.Errorf("bind operational PostgreSQL dump descriptor")
	}
	dump := &verifiedOperationalDump{directory: directory, source: source, name: name, artifact: artifact}
	if err := dump.bindIdentity(); err != nil {
		_ = dump.Close()
		return nil, fmt.Errorf("bind verified operational PostgreSQL dump: %w", err)
	}
	spool, err := os.CreateTemp("", ".loom-operational-restore-dump-*")
	if err != nil {
		_ = dump.Close()
		return nil, fmt.Errorf("create private operational restore input: %w", err)
	}
	spoolPath := spool.Name()
	if err := spool.Chmod(0o600); err != nil {
		_ = spool.Close()
		_ = os.Remove(spoolPath)
		_ = dump.Close()
		return nil, fmt.Errorf("protect private operational restore input: %w", err)
	}
	if err := os.Remove(spoolPath); err != nil {
		_ = spool.Close()
		_ = dump.Close()
		return nil, fmt.Errorf("unlink private operational restore input: %w", err)
	}
	dump.spool = spool
	if err := dump.copyAuthenticatedSource(); err != nil {
		_ = dump.Close()
		return nil, fmt.Errorf("authenticate operational PostgreSQL dump descriptor: %w", err)
	}
	return dump, nil
}

func (dump *verifiedOperationalDump) bindIdentity() error {
	var opened unix.Stat_t
	if err := unix.Fstat(int(dump.source.Fd()), &opened); err != nil {
		return err
	}
	var named unix.Stat_t
	if err := unix.Fstatat(int(dump.directory.Fd()), dump.name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if !matchingOperationalDumpStat(opened, named) {
		return fmt.Errorf("dump path and opened descriptor do not identify the same regular file")
	}
	if opened.Size != dump.artifact.SizeBytes || uint32(opened.Mode)&0o7777 != dump.artifact.Mode || opened.Size <= 0 {
		return fmt.Errorf("dump type, mode, or size differs from authenticated artifact evidence")
	}
	dump.device = uint64(opened.Dev)
	dump.inode = uint64(opened.Ino)
	return nil
}

func matchingOperationalDumpStat(left, right unix.Stat_t) bool {
	return uint32(left.Mode)&unix.S_IFMT == unix.S_IFREG &&
		uint32(right.Mode)&unix.S_IFMT == unix.S_IFREG &&
		left.Dev == right.Dev && left.Ino == right.Ino &&
		left.Mode == right.Mode && left.Size == right.Size
}

func (dump *verifiedOperationalDump) copyAuthenticatedSource() error {
	if _, err := dump.source.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := dump.spool.Truncate(0); err != nil {
		return err
	}
	if _, err := dump.spool.Seek(0, io.SeekStart); err != nil {
		return err
	}
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(dump.spool, hash), io.LimitReader(dump.source, dump.artifact.SizeBytes+1))
	if err != nil {
		return err
	}
	if size != dump.artifact.SizeBytes || fmt.Sprintf("%x", hash.Sum(nil)) != strings.ToLower(strings.TrimSpace(dump.artifact.SHA256)) {
		return fmt.Errorf("dump size or SHA-256 differs from authenticated artifact evidence")
	}
	if err := dump.verifyBoundIdentity(); err != nil {
		return err
	}
	_, err = dump.spool.Seek(0, io.SeekStart)
	return err
}

func (dump *verifiedOperationalDump) verifyBoundIdentity() error {
	var opened unix.Stat_t
	if err := unix.Fstat(int(dump.source.Fd()), &opened); err != nil {
		return err
	}
	var named unix.Stat_t
	if err := unix.Fstatat(int(dump.directory.Fd()), dump.name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if !matchingOperationalDumpStat(opened, named) || uint64(opened.Dev) != dump.device || uint64(opened.Ino) != dump.inode || opened.Size != dump.artifact.SizeBytes || uint32(opened.Mode)&0o7777 != dump.artifact.Mode {
		return fmt.Errorf("dump descriptor identity, type, mode, size, or package path changed")
	}
	return nil
}

func (dump *verifiedOperationalDump) VerifySource() error {
	if err := dump.verifyBoundIdentity(); err != nil {
		return err
	}
	if _, err := dump.source.Seek(0, io.SeekStart); err != nil {
		return err
	}
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(dump.source, dump.artifact.SizeBytes+1))
	if err != nil {
		return err
	}
	if size != dump.artifact.SizeBytes || fmt.Sprintf("%x", hash.Sum(nil)) != strings.ToLower(strings.TrimSpace(dump.artifact.SHA256)) {
		return fmt.Errorf("dump content changed from authenticated artifact evidence")
	}
	return dump.verifyBoundIdentity()
}

func (dump *verifiedOperationalDump) Reader() io.Reader {
	return io.NewSectionReader(dump.spool, 0, dump.artifact.SizeBytes)
}

func (dump *verifiedOperationalDump) Close() error {
	var errs []error
	for _, file := range []*os.File{dump.spool, dump.source, dump.directory} {
		if file != nil {
			if err := file.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func PlanDirectArchiveRestoreDrill(ctx context.Context, input DirectArchiveRestoreDrillInput) (DirectArchiveRestoreDrillPlan, error) {
	prepared, envelope, extraction, archiveRoot, err := prepareDirectArchiveRestore(ctx, input)
	if err != nil {
		return DirectArchiveRestoreDrillPlan{}, err
	}
	operationalDir, err := resolveExtractedArchiveDirectory(archiveRoot, envelope.OperationalArchivePath)
	if err != nil {
		return DirectArchiveRestoreDrillPlan{}, err
	}
	provenanceDir, err := resolveExtractedArchiveDirectory(archiveRoot, envelope.ProvenanceArchivePath)
	if err != nil {
		return DirectArchiveRestoreDrillPlan{}, err
	}
	operationalPlan, err := PlanOperationalRestoreDrill(ctx, OperationalRestoreDrillInput{
		PackageDir: operationalDir, ExpectedManifestSHA256: envelope.OperationalManifestSHA256,
		ExpectedPackageID: envelope.OperationalPackageID,
		TargetDatabase:    input.OperationalTarget, ActiveDatabase: input.ActiveDatabase,
		Owner: input.Owner, KeepDatabase: input.KeepOperationalDatabase, Now: input.Now, Runner: input.Runner,
	})
	if err != nil {
		return DirectArchiveRestoreDrillPlan{}, err
	}
	provenanceVerification, err := maintenance.VerifyProvenanceBackupPackage(ctx, provenanceDir, envelope.ProvenanceManifestSHA256)
	if err != nil {
		return DirectArchiveRestoreDrillPlan{}, err
	}
	if provenanceVerification.Status != maintenance.VerificationSucceeded || provenanceVerification.SchemaHead != envelope.ProvenanceSchemaHead || provenanceVerification.GraphDigest != envelope.ProvenanceGraphDigest || provenanceVerification.DumpSizeBytes != envelope.ProvenanceDumpSizeBytes || !provenanceVerification.CompletedAt.Equal(envelope.ProvenanceCompletedAt) {
		return DirectArchiveRestoreDrillPlan{}, loomerrors.New("backup.provenance_recovery_identity_mismatch", "backup", provenanceDir, "Fetched provenance recovery package does not match direct-archive evidence.")
	}
	return DirectArchiveRestoreDrillPlan{
		Status: "planned", ArchiveRoot: archiveRoot, Repository: envelope.Repository,
		Archive: envelope.ArchiveName, ArchiveRef: envelope.ArchiveRef,
		ArchiveClass: envelope.ArchiveClass, ManifestSHA256: prepared.ManifestSHA256,
		Extraction: extraction, OperationalPackageDir: operationalDir,
		OperationalVerification: operationalPlan.Verification,
		ProvenancePackageDir:    provenanceDir, ProvenancePackageID: envelope.ProvenancePackageID,
		ProvenanceVerification: provenanceVerification,
		OperationalRestorePlan: operationalPlan,
	}, nil
}

func prepareDirectArchiveRestore(ctx context.Context, input DirectArchiveRestoreDrillInput) (backupstrategy.PreparedDirectArchiveManifest, directArchiveRestoreManifestEnvelope, backupstrategy.DirectArchiveExtractionVerification, string, error) {
	archiveRoot := filepath.Clean(strings.TrimSpace(input.ArchiveRoot))
	if archiveRoot == "." || archiveRoot == "" || !filepath.IsAbs(archiveRoot) {
		return backupstrategy.PreparedDirectArchiveManifest{}, directArchiveRestoreManifestEnvelope{}, backupstrategy.DirectArchiveExtractionVerification{}, "", loomerrors.New("backup.direct_archive_root_required", "backup", "archive_root", "An absolute isolated direct-archive extraction root is required.")
	}
	manifestRaw, err := readStableRestoreFile(filepath.Join(archiveRoot, backupstrategy.DirectArchiveEvidenceDir, backupstrategy.DirectArchiveManifestFile), 16<<20)
	if err != nil {
		return backupstrategy.PreparedDirectArchiveManifest{}, directArchiveRestoreManifestEnvelope{}, backupstrategy.DirectArchiveExtractionVerification{}, "", err
	}
	hashRaw, err := readStableRestoreFile(filepath.Join(archiveRoot, backupstrategy.DirectArchiveEvidenceDir, backupstrategy.DirectArchiveManifestHashFile), 1024)
	if err != nil {
		return backupstrategy.PreparedDirectArchiveManifest{}, directArchiveRestoreManifestEnvelope{}, backupstrategy.DirectArchiveExtractionVerification{}, "", err
	}
	prepared, err := backupstrategy.ParseAuthenticatedDirectArchiveManifest(manifestRaw, hashRaw)
	if err != nil {
		return backupstrategy.PreparedDirectArchiveManifest{}, directArchiveRestoreManifestEnvelope{}, backupstrategy.DirectArchiveExtractionVerification{}, "", err
	}
	if prepared.ManifestV2 != nil {
		if input.V2UserSymlinkTargets == nil {
			return backupstrategy.PreparedDirectArchiveManifest{}, directArchiveRestoreManifestEnvelope{}, backupstrategy.DirectArchiveExtractionVerification{}, "", loomerrors.New("backup.direct_archive_v2_symlink_evidence_required", "backup", "v2_user_symlink_targets", "Strict v2 restore requires authenticated user-symlink target evidence from fetch.")
		}
		prepared.V2UserSymlinkTargets = cloneDirectArchiveV2UserSymlinkTargets(input.V2UserSymlinkTargets)
	}
	envelope, err := directArchiveRestoreEnvelope(prepared)
	if err != nil {
		return backupstrategy.PreparedDirectArchiveManifest{}, directArchiveRestoreManifestEnvelope{}, backupstrategy.DirectArchiveExtractionVerification{}, "", err
	}
	if strings.TrimSpace(input.ExpectedManifestSHA256) == "" || prepared.ManifestSHA256 != strings.TrimSpace(input.ExpectedManifestSHA256) {
		return backupstrategy.PreparedDirectArchiveManifest{}, directArchiveRestoreManifestEnvelope{}, backupstrategy.DirectArchiveExtractionVerification{}, "", loomerrors.New("backup.direct_archive_identity_mismatch", "backup", "manifest_sha256", "Fetched direct archive does not match the expected manifest identity.")
	}
	if expected := strings.TrimSpace(input.ExpectedRepository); expected != "" && envelope.Repository != expected {
		return backupstrategy.PreparedDirectArchiveManifest{}, directArchiveRestoreManifestEnvelope{}, backupstrategy.DirectArchiveExtractionVerification{}, "", loomerrors.New("backup.direct_archive_repository_mismatch", "backup", "repository", "Fetched direct archive belongs to a different repository.")
	}
	if expected := strings.TrimSpace(input.ExpectedArchive); expected != "" && envelope.ArchiveName != expected {
		return backupstrategy.PreparedDirectArchiveManifest{}, directArchiveRestoreManifestEnvelope{}, backupstrategy.DirectArchiveExtractionVerification{}, "", loomerrors.New("backup.direct_archive_name_mismatch", "backup", "archive", "Fetched direct archive name does not match the requested archive.")
	}
	extraction, err := backupstrategy.VerifyExtractedDirectArchive(ctx, archiveRoot, prepared)
	if err != nil {
		return backupstrategy.PreparedDirectArchiveManifest{}, directArchiveRestoreManifestEnvelope{}, backupstrategy.DirectArchiveExtractionVerification{}, "", err
	}
	if extraction.Status != "succeeded" {
		return backupstrategy.PreparedDirectArchiveManifest{}, directArchiveRestoreManifestEnvelope{}, backupstrategy.DirectArchiveExtractionVerification{}, "", loomerrors.New("backup.direct_archive_extraction_invalid", "backup", archiveRoot, "Fetched direct archive payload does not match its authenticated manifest.")
	}
	return prepared, envelope, extraction, archiveRoot, nil
}

func cloneDirectArchiveV2UserSymlinkTargets(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for archivePath, target := range source {
		cloned[archivePath] = target
	}
	return cloned
}

type directArchiveRestoreManifestEnvelope struct {
	Repository                string
	ArchiveName               string
	ArchiveRef                string
	ArchiveClass              string
	OperationalArchivePath    string
	OperationalPackageID      string
	OperationalManifestSHA256 string
	ProvenanceArchivePath     string
	ProvenancePackageID       string
	ProvenanceManifestSHA256  string
	ProvenanceSchemaHead      int
	ProvenanceGraphDigest     string
	ProvenanceCompletedAt     time.Time
	ProvenanceDumpSizeBytes   int64
}

func directArchiveRestoreEnvelope(prepared backupstrategy.PreparedDirectArchiveManifest) (directArchiveRestoreManifestEnvelope, error) {
	if prepared.ManifestV2 != nil {
		manifest := prepared.ManifestV2
		return directArchiveRestoreManifestEnvelope{
			Repository: manifest.Repository, ArchiveName: manifest.ArchiveName,
			ArchiveRef: manifest.ArchiveRef, ArchiveClass: manifest.ArchiveClass,
			OperationalArchivePath:    manifest.OperationalPackage.ArchivePath,
			OperationalPackageID:      manifest.OperationalPackage.PackageID,
			OperationalManifestSHA256: manifest.OperationalPackage.ManifestSHA256,
			ProvenanceArchivePath:     manifest.ProvenancePackage.ArchivePath,
			ProvenancePackageID:       manifest.ProvenancePackage.PackageID,
			ProvenanceManifestSHA256:  manifest.ProvenancePackage.ManifestSHA256,
			ProvenanceSchemaHead:      manifest.ProvenancePackage.SchemaHead,
			ProvenanceGraphDigest:     manifest.ProvenancePackage.GraphDigest,
			ProvenanceCompletedAt:     manifest.ProvenancePackage.CompletedAt,
			ProvenanceDumpSizeBytes:   manifest.ProvenancePackage.DumpSizeBytes,
		}, nil
	}
	manifest := prepared.Manifest
	if manifest.Schema != backupstrategy.DirectArchiveManifestSchema {
		return directArchiveRestoreManifestEnvelope{}, fmt.Errorf("unsupported direct archive restore manifest schema %q", manifest.Schema)
	}
	return directArchiveRestoreManifestEnvelope{
		Repository: manifest.Repository, ArchiveName: manifest.ArchiveName,
		ArchiveRef: manifest.ArchiveRef, ArchiveClass: manifest.ArchiveClass,
		OperationalArchivePath:    manifest.OperationalPackage.ArchivePath,
		OperationalPackageID:      manifest.OperationalPackage.PackageID,
		OperationalManifestSHA256: manifest.OperationalPackage.ManifestSHA256,
		ProvenanceArchivePath:     manifest.ProvenancePackage.ArchivePath,
		ProvenancePackageID:       manifest.ProvenancePackage.PackageID,
		ProvenanceManifestSHA256:  manifest.ProvenancePackage.ManifestSHA256,
		ProvenanceSchemaHead:      manifest.ProvenancePackage.SchemaHead,
		ProvenanceGraphDigest:     manifest.ProvenancePackage.GraphDigest,
		ProvenanceCompletedAt:     manifest.ProvenancePackage.CompletedAt,
		ProvenanceDumpSizeBytes:   manifest.ProvenancePackage.DumpSizeBytes,
	}, nil
}

func RunDirectArchiveRestoreDrill(ctx context.Context, input DirectArchiveRestoreDrillInput) (DirectArchiveRestoreDrillResult, error) {
	startedAt := currentTime(input.Now)
	input.V2UserSymlinkTargets = cloneDirectArchiveV2UserSymlinkTargets(input.V2UserSymlinkTargets)
	plan, err := PlanDirectArchiveRestoreDrill(ctx, input)
	if err != nil {
		return DirectArchiveRestoreDrillResult{}, err
	}
	result := DirectArchiveRestoreDrillResult{Status: "partial", StartedAt: startedAt, Plan: plan}
	operationalStep := input.OperationalRestore
	if operationalStep == nil {
		operationalStep = func(ctx context.Context, step DirectArchiveRecoveryStepInput) (DirectArchiveRecoveryStepResult, error) {
			restored, err := RunOperationalRestoreDrill(ctx, OperationalRestoreDrillInput{
				PackageDir: step.PackageDir, ExpectedManifestSHA256: step.ManifestSHA256, ExpectedPackageID: step.PackageID,
				TargetDatabase: plan.OperationalRestorePlan.TargetDatabase, ActiveDatabase: plan.OperationalRestorePlan.ActiveDatabase,
				Owner: plan.OperationalRestorePlan.Owner, KeepDatabase: plan.OperationalRestorePlan.KeepDatabase,
				Runner: input.Runner, Authority: input.Authority, Now: input.Now,
			})
			if err != nil {
				return DirectArchiveRecoveryStepResult{}, err
			}
			return DirectArchiveRecoveryStepResult{Status: restored.Status, PackageID: restored.PackageID, ManifestSHA256: restored.ManifestSHA256, SchemaHead: step.SchemaHead, Summary: restored.DatabaseSummary}, nil
		}
	}
	if input.ProvenanceRestore == nil {
		return result, loomerrors.New("backup.provenance_restore_required", "backup", "provenance_restore", "Strict direct-archive restore requires an independent disposable provenance restore step.")
	}
	opManifest := plan.OperationalVerification.Manifest
	opInput := DirectArchiveRecoveryStepInput{PackageDir: plan.OperationalPackageDir, PackageID: opManifest.PackageID, ManifestSHA256: plan.OperationalVerification.ManifestSHA256, SchemaHead: opManifest.SchemaHead}
	result.OperationalRecovery, err = operationalStep(ctx, opInput)
	if err != nil {
		return result, fmt.Errorf("operational package strict restore: %w", err)
	}
	if err := validateDirectArchiveRecoveryStep(result.OperationalRecovery, opInput); err != nil {
		return result, err
	}
	provManifest := plan.ProvenanceVerification
	provInput := DirectArchiveRecoveryStepInput{PackageDir: plan.ProvenancePackageDir, PackageID: plan.ProvenancePackageID, ManifestSHA256: provManifest.ManifestSHA256, SchemaHead: int64(provManifest.SchemaHead)}
	result.ProvenanceRecovery, err = input.ProvenanceRestore(ctx, provInput)
	if err != nil {
		return result, fmt.Errorf("provenance package strict restore: %w", err)
	}
	if err := validateDirectArchiveRecoveryStep(result.ProvenanceRecovery, provInput); err != nil {
		return result, err
	}
	if input.V2UserSymlinkTargets != nil {
		_, _, finalExtraction, _, err := prepareDirectArchiveRestore(ctx, input)
		if err != nil {
			return result, fmt.Errorf("final direct-archive extraction verification: %w", err)
		}
		result.Plan.Extraction = finalExtraction
	}
	result.Status = "succeeded"
	result.FinishedAt = currentTime(input.Now)
	return result, nil
}

func MigrationEvidenceFromDirectArchiveRestore(repositoryID string, result DirectArchiveRestoreDrillResult, acceptedAt time.Time) (backupstrategy.StrictRestoreEvidence, error) {
	if result.Status != "succeeded" || result.Plan.ManifestSHA256 == "" || result.Plan.OperationalVerification.Manifest == nil || result.OperationalRecovery.Status != "succeeded" || result.ProvenanceRecovery.Status != "succeeded" {
		return backupstrategy.StrictRestoreEvidence{}, loomerrors.New("backup.strict_restore_incomplete", "backup", "restore", "Only a complete strict direct-archive restore can become migration evidence.")
	}
	acceptedAt = acceptedAt.UTC()
	if acceptedAt.IsZero() || result.FinishedAt.IsZero() || acceptedAt.Before(result.FinishedAt) {
		return backupstrategy.StrictRestoreEvidence{}, loomerrors.New("backup.strict_restore_acceptance_invalid", "backup", "accepted_at", "Migration evidence acceptance must be at or after the completed strict restore.")
	}
	if err := validateDirectArchiveRecoveryStep(result.OperationalRecovery, DirectArchiveRecoveryStepInput{
		PackageID: result.Plan.OperationalVerification.PackageID, ManifestSHA256: result.Plan.OperationalVerification.ManifestSHA256,
		SchemaHead: result.Plan.OperationalVerification.Manifest.SchemaHead,
	}); err != nil {
		return backupstrategy.StrictRestoreEvidence{}, err
	}
	if err := validateDirectArchiveRecoveryStep(result.ProvenanceRecovery, DirectArchiveRecoveryStepInput{
		PackageID: result.Plan.ProvenancePackageID, ManifestSHA256: result.Plan.ProvenanceVerification.ManifestSHA256,
		SchemaHead: int64(result.Plan.ProvenanceVerification.SchemaHead),
	}); err != nil {
		return backupstrategy.StrictRestoreEvidence{}, err
	}
	return backupstrategy.BindStrictRestoreEvidence(backupstrategy.StrictRestoreEvidence{
		Schema: backupstrategy.StrictRestoreEvidenceSchema, Status: backupstrategy.StrictRestoreSucceeded,
		RepositoryID: strings.TrimSpace(repositoryID), Archive: result.Plan.Archive,
		DirectArchiveManifestSHA:  result.Plan.ManifestSHA256,
		OperationalManifestSHA256: result.OperationalRecovery.ManifestSHA256,
		ProvenanceManifestSHA256:  result.ProvenanceRecovery.ManifestSHA256,
		AcceptedAt:                acceptedAt,
	})
}

func validateDirectArchiveRecoveryStep(result DirectArchiveRecoveryStepResult, expected DirectArchiveRecoveryStepInput) error {
	if result.Status != "succeeded" || result.PackageID != expected.PackageID || result.ManifestSHA256 != expected.ManifestSHA256 || result.SchemaHead != expected.SchemaHead {
		return loomerrors.New("backup.strict_restore_identity_mismatch", "backup", expected.PackageID, "Strict recovery step did not return the exact package and manifest identity.")
	}
	return nil
}

func resolveExtractedArchiveDirectory(root, portable string) (string, error) {
	portable = filepath.ToSlash(strings.TrimSpace(portable))
	if portable == "" || portable == "." || pathEscapesRestoreRoot(portable) {
		return "", fmt.Errorf("invalid extracted archive directory %q", portable)
	}
	current := root
	for _, segment := range strings.Split(portable, "/") {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("extracted archive directory %q is not a real directory", portable)
		}
	}
	return current, nil
}

func pathEscapesRestoreRoot(value string) bool {
	return filepath.IsAbs(value) || value != pathCleanPortable(value) || value == ".." || strings.HasPrefix(value, "../")
}

func pathCleanPortable(value string) string {
	return filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
}

func readStableRestoreFile(path string, limit int64) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() <= 0 || before.Size() > limit {
		return nil, fmt.Errorf("restore evidence %s is not a bounded regular file", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, fmt.Errorf("restore evidence changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, fmt.Errorf("restore evidence exceeds its byte bound")
	}
	after, err := file.Stat()
	pathAfter, pathErr := os.Lstat(path)
	if err != nil || pathErr != nil || int64(len(raw)) != before.Size() || !os.SameFile(before, after) || !os.SameFile(after, pathAfter) {
		return nil, fmt.Errorf("restore evidence changed while reading")
	}
	return raw, nil
}

// ProvenanceRestoreDrillInput receives an already restored disposable target.
// The declared package snapshot is the recovery oracle. SourceDatabase remains
// only an active-name safety guard; SourcePool is retained for source
// compatibility but is never read because mutable live state cannot establish
// whether a historical package is correct.
type ProvenanceRestoreDrillInput struct {
	BackupDir              string
	ExpectedManifestSHA256 string
	SourceDatabase         string
	TargetDatabase         string
	SourcePool             *pgxpool.Pool
	RestoredPool           *pgxpool.Pool
	Now                    func() time.Time
}

type ProvenanceRestoreDrillResult struct {
	Status             string           `json:"status"`
	SourceDatabase     string           `json:"source_database"`
	TargetDatabase     string           `json:"target_database"`
	StartedAt          time.Time        `json:"started_at"`
	FinishedAt         time.Time        `json:"finished_at"`
	SchemaHead         int              `json:"schema_head"`
	RestoredSchemaHead int              `json:"restored_schema_head"`
	FinalSchemaHead    int              `json:"final_schema_head"`
	LogicalCounts      map[string]int64 `json:"logical_counts"`
	GraphDigest        string           `json:"graph_digest"`
	BackupCompleted    time.Time        `json:"backup_completed_at"`
	ManifestSHA256     string           `json:"manifest_sha256"`
}

type ProvenanceDirectArchiveRecoveryConfig struct {
	SourceDatabase string
	TargetDatabase string
	DatabaseURL    string
	Owner          string
	KeepDatabase   bool
	Runner         CommandRunner
	Authority      restoreauthority.RestoreDatabaseAuthority
	SourcePool     *pgxpool.Pool
	RestoredPool   *pgxpool.Pool
	Now            func() time.Time
}

// NewProvenanceDirectArchiveRecoveryStep adapts the independent logical-ledger
// restore drill to the strict direct-archive orchestration contract. The
// typed local authority owns creation, pg_restore, and exact cleanup of the
// disposable target, so no production credentials or connection strings enter
// archive evidence or the authority protocol.
func NewProvenanceDirectArchiveRecoveryStep(config ProvenanceDirectArchiveRecoveryConfig) DirectArchiveRecoveryStep {
	return func(ctx context.Context, input DirectArchiveRecoveryStepInput) (DirectArchiveRecoveryStepResult, error) {
		var result ProvenanceRestoreDrillResult
		var err error
		if config.RestoredPool != nil {
			result, err = RunProvenanceRestoreDrill(ctx, ProvenanceRestoreDrillInput{
				BackupDir: input.PackageDir, ExpectedManifestSHA256: input.ManifestSHA256,
				SourceDatabase: config.SourceDatabase, TargetDatabase: config.TargetDatabase,
				SourcePool: config.SourcePool, RestoredPool: config.RestoredPool, Now: config.Now,
			})
		} else {
			result, err = runManagedProvenanceRestoreDrill(ctx, input, config)
		}
		if err != nil {
			return DirectArchiveRecoveryStepResult{}, err
		}
		return DirectArchiveRecoveryStepResult{
			Status: result.Status, PackageID: input.PackageID, ManifestSHA256: result.ManifestSHA256,
			SchemaHead: int64(result.SchemaHead), Summary: map[string]any{
				"source_database": result.SourceDatabase, "target_database": result.TargetDatabase,
				"restored_schema_head": result.RestoredSchemaHead, "final_schema_head": result.FinalSchemaHead,
				"logical_counts": result.LogicalCounts, "graph_digest": result.GraphDigest,
			},
		}, nil
	}
}

func runManagedProvenanceRestoreDrill(ctx context.Context, input DirectArchiveRecoveryStepInput, config ProvenanceDirectArchiveRecoveryConfig) (ProvenanceRestoreDrillResult, error) {
	activeDatabase := strings.TrimSpace(config.SourceDatabase)
	if activeDatabase == "" {
		activeDatabase = provenance.DatabaseName
	}
	targetDatabase := strings.TrimSpace(config.TargetDatabase)
	if targetDatabase == "" {
		targetDatabase = DefaultProvenanceRestoreDrillDatabase(currentTime(config.Now))
	}
	owner := strings.TrimSpace(config.Owner)
	if owner == "" {
		owner = provenance.DatabaseRole
	}
	if err := ValidateProvenanceRestoreDrillDatabase(targetDatabase, activeDatabase); err != nil {
		return ProvenanceRestoreDrillResult{}, err
	}
	if config.Authority == nil {
		return ProvenanceRestoreDrillResult{}, loomerrors.New("backup.restore_authority_required", "backup", "restore_authority", "A typed disposable database restore authority is required.")
	}
	if config.KeepDatabase {
		return ProvenanceRestoreDrillResult{}, loomerrors.New("backup.restore_authority_keep_refused", "backup", "keep_database", "The restore authority requires mandatory disposable database cleanup.")
	}
	verification, err := maintenance.VerifyProvenanceBackupPackage(ctx, input.PackageDir, input.ManifestSHA256)
	if err != nil {
		return ProvenanceRestoreDrillResult{}, fmt.Errorf("verify provenance recovery package before database creation: %w", err)
	}
	if verification.SchemaHead != int(input.SchemaHead) {
		return ProvenanceRestoreDrillResult{}, loomerrors.New("backup.provenance_recovery_identity_mismatch", "backup", input.PackageID, "Provenance recovery package schema head does not match direct-archive evidence.")
	}
	poolConfig, err := provenanceRestoreTargetPoolConfig(config.DatabaseURL, activeDatabase, targetDatabase, owner)
	if err != nil {
		return ProvenanceRestoreDrillResult{}, err
	}
	dump, err := openVerifiedOperationalDump(input.PackageDir, backupstrategy.OperationalPackageArtifact{
		Path: verification.DumpFile, SizeBytes: verification.DumpSizeBytes,
		SHA256: verification.DumpSHA256, Mode: 0o600,
	})
	if err != nil {
		return ProvenanceRestoreDrillResult{}, fmt.Errorf("open authenticated provenance PostgreSQL dump: %w", err)
	}
	defer dump.Close()
	cleanup := func() error {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), directArchiveRestoreCleanupTimeout)
		defer cancelCleanup()
		result, cleanupErr := config.Authority.Drop(cleanupCtx, restoreauthority.DropRequest{Kind: restoreauthority.KindProvenance, Database: targetDatabase})
		if cleanupErr != nil {
			return cleanupErr
		}
		return validateAuthorityResult(result, restoreauthority.KindProvenance, targetDatabase, true)
	}
	returnFailure := func(primary error) (ProvenanceRestoreDrillResult, error) {
		cleanupErr := cleanup()
		if cleanupErr != nil {
			primary = errors.Join(primary, fmt.Errorf("cleanup provenance restore database: %w", cleanupErr))
		}
		return ProvenanceRestoreDrillResult{}, &RestoreDatabaseFailure{
			Kind: restoreauthority.KindProvenance, Database: targetDatabase,
			CleanupAttempted: true, CleanupSucceeded: cleanupErr == nil, Cause: primary,
		}
	}
	if err := dump.VerifySource(); err != nil {
		return returnFailure(fmt.Errorf("provenance PostgreSQL dump changed before pg_restore: %w", err))
	}
	authorityResult, restoreErr := config.Authority.Restore(ctx, restoreauthority.RestoreRequest{
		Kind: restoreauthority.KindProvenance, Database: targetDatabase,
		DumpSize: verification.DumpSizeBytes, DumpSHA256: strings.ToLower(strings.TrimSpace(verification.DumpSHA256)), Dump: dump.Reader(),
	})
	driftErr := dump.VerifySource()
	if restoreErr == nil {
		restoreErr = validateAuthorityResult(authorityResult, restoreauthority.KindProvenance, targetDatabase, false)
	}
	if restoreErr != nil || driftErr != nil {
		var restoreErrors []error
		if restoreErr != nil {
			restoreErrors = append(restoreErrors, fmt.Errorf("provenance restore pg_restore: %w", restoreErr))
		}
		if driftErr != nil {
			restoreErrors = append(restoreErrors, fmt.Errorf("provenance PostgreSQL dump changed during pg_restore: %w", driftErr))
		}
		return returnFailure(errors.Join(restoreErrors...))
	}
	restoredPool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return returnFailure(loomerrors.New("backup.provenance_restore_database_unavailable", "backup", targetDatabase, "Disposable provenance restore database is unavailable."))
	}
	result, drillErr := RunProvenanceRestoreDrill(ctx, ProvenanceRestoreDrillInput{
		BackupDir: input.PackageDir, ExpectedManifestSHA256: input.ManifestSHA256,
		SourceDatabase: activeDatabase, TargetDatabase: targetDatabase,
		RestoredPool: restoredPool, Now: config.Now,
	})
	restoredPool.Close()
	if drillErr != nil {
		return returnFailure(drillErr)
	}
	if err := cleanup(); err != nil {
		return ProvenanceRestoreDrillResult{}, &RestoreDatabaseFailure{
			Kind: restoreauthority.KindProvenance, Database: targetDatabase,
			CleanupAttempted: true, CleanupSucceeded: false,
			Cause: fmt.Errorf("cleanup provenance restore database: %w", err),
		}
	}
	return result, nil
}

func validateAuthorityResult(result restoreauthority.Result, kind restoreauthority.Kind, database string, cleanup bool) error {
	if result.Status != "succeeded" || result.Kind != kind || result.Database != database || result.FailureStage != "" || result.ErrorCode != "" || result.CleanupAttempted != cleanup || result.CleanupSucceeded != cleanup {
		return loomerrors.New("backup.restore_authority_identity_mismatch", "backup", database, "Restore authority returned an inexact disposable database identity.")
	}
	return nil
}

func provenanceRestoreTargetPoolConfig(databaseURL, activeDatabase, targetDatabase, owner string) (*pgxpool.Config, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, loomerrors.New("backup.provenance_restore_database_unconfigured", "backup", activeDatabase, "A dedicated Provenance database connection is required for a non-dry restore drill.")
	}
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, loomerrors.New("backup.provenance_restore_database_invalid", "backup", activeDatabase, "The dedicated Provenance database connection is invalid.")
	}
	if strings.TrimSpace(poolConfig.ConnConfig.Database) != activeDatabase || activeDatabase == "loom_main" || strings.TrimSpace(poolConfig.ConnConfig.User) != owner {
		return nil, loomerrors.New("backup.provenance_restore_database_unsafe", "backup", activeDatabase, "The dedicated Provenance database connection does not match the expected active database and owner.")
	}
	poolConfig = poolConfig.Copy()
	poolConfig.ConnConfig.Database = targetDatabase
	poolConfig.MaxConns = 2
	poolConfig.MinConns = 0
	return poolConfig, nil
}

func ValidateProvenanceRestoreDrillDatabase(targetDatabase, sourceDatabase string) error {
	targetDatabase = strings.TrimSpace(targetDatabase)
	sourceDatabase = strings.TrimSpace(sourceDatabase)
	if sourceDatabase == "" || sourceDatabase == "loom_main" || !restoreDrillDatabasePattern.MatchString(sourceDatabase) {
		return loomerrors.New("backup.provenance_source_unsafe", "backup", "source_database", "Provenance restore drill source database is invalid.")
	}
	if targetDatabase == "" || !strings.HasPrefix(targetDatabase, ProvenanceRestoreDrillDatabasePrefix) || !restoreDrillDatabasePattern.MatchString(targetDatabase) {
		return loomerrors.New("backup.provenance_restore_target_unsafe", "backup", "target_database", "Provenance restore drill target must use the disposable loom_provenance_restore_drill_ prefix.")
	}
	if targetDatabase == sourceDatabase || targetDatabase == "loom_main" || targetDatabase == provenance.DatabaseName {
		return loomerrors.New("backup.provenance_restore_refuses_active_database", "backup", targetDatabase, "Provenance restore drill refuses an active database.")
	}
	return nil
}

func RunProvenanceRestoreDrill(ctx context.Context, input ProvenanceRestoreDrillInput) (ProvenanceRestoreDrillResult, error) {
	startedAt := currentTime(input.Now)
	input.BackupDir = filepath.Clean(strings.TrimSpace(input.BackupDir))
	input.SourceDatabase = strings.TrimSpace(input.SourceDatabase)
	input.TargetDatabase = strings.TrimSpace(input.TargetDatabase)
	if input.BackupDir == "." || input.BackupDir == "" {
		return ProvenanceRestoreDrillResult{}, loomerrors.New("backup.directory_required", "backup", "backup_dir", "Backup directory is required.")
	}
	if input.RestoredPool == nil {
		return ProvenanceRestoreDrillResult{}, loomerrors.New("backup.provenance_restore_pool_required", "backup", "database", "A restored provenance database pool is required.")
	}
	if err := ValidateProvenanceRestoreDrillDatabase(input.TargetDatabase, input.SourceDatabase); err != nil {
		return ProvenanceRestoreDrillResult{}, err
	}
	verification, err := maintenance.VerifyProvenanceBackupPackage(ctx, input.BackupDir, input.ExpectedManifestSHA256)
	if err != nil {
		return ProvenanceRestoreDrillResult{}, fmt.Errorf("verify provenance recovery package: %w", err)
	}
	declared := provenance.RecoverySnapshot{
		SchemaHead:     verification.SchemaHead,
		RelationCounts: verification.LogicalCounts,
		GraphDigest:    verification.GraphDigest,
	}
	restoredAtHead, err := provenance.SnapshotLedgerAtHead(ctx, input.RestoredPool, input.TargetDatabase, verification.SchemaHead)
	if err != nil {
		return ProvenanceRestoreDrillResult{}, fmt.Errorf("snapshot restored provenance ledger at declared head %d: %w", verification.SchemaHead, err)
	}
	if err := provenance.CompareRecoverySnapshots(declared, restoredAtHead); err != nil {
		return ProvenanceRestoreDrillResult{}, fmt.Errorf("provenance restore drill failed: %w", err)
	}
	migrationStatus, err := provenance.ApplyMigrations(ctx, input.RestoredPool, input.TargetDatabase)
	if err != nil {
		return ProvenanceRestoreDrillResult{}, fmt.Errorf("migrate disposable provenance restore target: %w", err)
	}
	if !migrationStatus.Ready || migrationStatus.AppliedHead != provenance.SchemaHead {
		return ProvenanceRestoreDrillResult{}, fmt.Errorf("migrate disposable provenance restore target: current readiness is incomplete")
	}
	current, err := provenance.SnapshotLedger(ctx, input.RestoredPool, input.TargetDatabase)
	if err != nil {
		return ProvenanceRestoreDrillResult{}, fmt.Errorf("verify migrated provenance restore target readiness and integrity: %w", err)
	}
	return ProvenanceRestoreDrillResult{
		Status:             "succeeded",
		SourceDatabase:     input.SourceDatabase,
		TargetDatabase:     input.TargetDatabase,
		StartedAt:          startedAt,
		FinishedAt:         currentTime(input.Now),
		SchemaHead:         restoredAtHead.SchemaHead,
		RestoredSchemaHead: restoredAtHead.SchemaHead,
		FinalSchemaHead:    current.SchemaHead,
		LogicalCounts:      cloneLogicalCounts(restoredAtHead.RelationCounts),
		GraphDigest:        restoredAtHead.GraphDigest,
		BackupCompleted:    verification.CompletedAt,
		ManifestSHA256:     verification.ManifestSHA256,
	}, nil
}

func cloneLogicalCounts(input map[string]int64) map[string]int64 {
	result := make(map[string]int64, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func DefaultRestoreDrillDatabase(now time.Time) string {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return RestoreDrillDatabasePrefix + now.UTC().Format("20060102150405")
}

func DefaultProvenanceRestoreDrillDatabase(now time.Time) string {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return ProvenanceRestoreDrillDatabasePrefix + now.UTC().Format("20060102150405")
}

func ValidateRestoreDrillDatabase(targetDatabase, activeDatabase string) error {
	targetDatabase = strings.TrimSpace(targetDatabase)
	activeDatabase = strings.TrimSpace(activeDatabase)
	if targetDatabase == "" {
		return loomerrors.New("backup.restore_drill_target_required", "backup", "target_database", "Restore drill database is required.")
	}
	if !strings.HasPrefix(targetDatabase, RestoreDrillDatabasePrefix) {
		return loomerrors.New("backup.restore_drill_target_unsafe", "backup", targetDatabase, "Restore drill database must start with loom_restore_drill_.")
	}
	if !restoreDrillDatabasePattern.MatchString(targetDatabase) {
		return loomerrors.New("backup.restore_drill_target_unsafe", "backup", targetDatabase, "Restore drill database contains unsafe characters.")
	}
	if activeDatabase == "" {
		activeDatabase = "loom_main"
	}
	if targetDatabase == activeDatabase || targetDatabase == "loom_main" {
		return loomerrors.New("backup.restore_drill_refuses_active_database", "backup", targetDatabase, "Restore drill refuses to use the active LOOM database.")
	}
	return nil
}

func PlanRestoreDrill(ctx context.Context, input RestoreDrillInput) (RestoreDrillPlan, error) {
	normalized, verification, manifest, err := normalizeRestoreDrillInput(ctx, &input)
	if err != nil {
		return RestoreDrillPlan{}, err
	}
	return RestoreDrillPlan{
		Status:         "planned",
		BackupDir:      normalized.BackupDir,
		TargetDatabase: normalized.TargetDatabase,
		ActiveDatabase: normalized.ActiveDatabase,
		Owner:          normalized.Owner,
		KeepDatabase:   normalized.KeepDatabase,
		Verification:   verification,
	}, requireBackupArtifactsForRestoreDrill(ctx, normalized.BackupDir, manifest)
}

func RunRestoreDrill(ctx context.Context, input RestoreDrillInput) (RestoreDrillResult, error) {
	startedAt := currentTime(input.Now)
	normalized, verification, manifest, err := normalizeRestoreDrillInput(ctx, &input)
	if err != nil {
		return RestoreDrillResult{}, err
	}
	if err := requireBackupArtifactsForRestoreDrill(ctx, normalized.BackupDir, manifest); err != nil {
		return RestoreDrillResult{}, err
	}

	dumpPath, err := maintenance.ResolveBackupPath(normalized.BackupDir, manifest.Database.DumpFile)
	if err != nil {
		return RestoreDrillResult{}, err
	}
	runner := normalized.Runner
	if runner == nil {
		return RestoreDrillResult{}, loomerrors.New("backup.legacy_restore_runner_required", "backup", "runner", "Legacy local restore drills require an explicitly injected disposable command runner.")
	}

	if _, err := runner(ctx, "createdb", []string{"--owner", normalized.Owner, "--", normalized.TargetDatabase}, nil); err != nil {
		return RestoreDrillResult{}, fmt.Errorf("create restore drill database: %w", err)
	}
	cleanup := func() error {
		if normalized.KeepDatabase {
			return nil
		}
		_, cleanupErr := runner(ctx, "dropdb", []string{"--if-exists", "--", normalized.TargetDatabase}, nil)
		return cleanupErr
	}

	dumpFile, err := os.Open(dumpPath)
	if err != nil {
		_ = cleanup()
		return RestoreDrillResult{}, fmt.Errorf("open postgres dump: %w", err)
	}
	defer dumpFile.Close()
	// Recovery dumps can contain privileged extensions such as pgvector. Restore
	// as PostgreSQL's recovery operator while retaining the owners recorded by
	// the trusted production dump; validation still runs as the LOOM owner.
	if _, err := runner(ctx, "pg_restore", []string{"--no-acl", "--dbname", normalized.TargetDatabase}, dumpFile); err != nil {
		_ = cleanup()
		return RestoreDrillResult{}, fmt.Errorf("restore drill pg_restore: %w", err)
	}

	output, err := runner(ctx, "psql", []string{"--no-psqlrc", "--dbname", normalized.TargetDatabase, "--set", "ON_ERROR_STOP=1", "--quiet", "--tuples-only", "--no-align"}, strings.NewReader(restoreDrillSQL))
	if err != nil {
		_ = cleanup()
		return RestoreDrillResult{}, fmt.Errorf("restore drill verification: %w", err)
	}
	if err := cleanup(); err != nil {
		return RestoreDrillResult{}, fmt.Errorf("cleanup restore drill database: %w", err)
	}

	return RestoreDrillResult{
		Status:          "succeeded",
		BackupDir:       normalized.BackupDir,
		TargetDatabase:  normalized.TargetDatabase,
		ActiveDatabase:  normalized.ActiveDatabase,
		Owner:           normalized.Owner,
		KeptDatabase:    normalized.KeepDatabase,
		StartedAt:       startedAt,
		FinishedAt:      currentTime(input.Now),
		Verification:    verification,
		DatabaseSummary: parseRestoreDrillSummary(output),
	}, nil
}

func normalizeRestoreDrillInput(ctx context.Context, input *RestoreDrillInput) (RestoreDrillInput, maintenance.BackupVerification, maintenance.BackupManifest, error) {
	input.BackupDir = filepath.Clean(strings.TrimSpace(input.BackupDir))
	if input.BackupDir == "." || input.BackupDir == "" {
		return RestoreDrillInput{}, maintenance.BackupVerification{}, maintenance.BackupManifest{}, loomerrors.New("backup.directory_required", "backup", "backup_dir", "Backup directory is required.")
	}
	if input.Owner = strings.TrimSpace(input.Owner); input.Owner == "" {
		input.Owner = "loom"
	}
	manifest, err := maintenance.ReadBackupManifest(filepath.Join(input.BackupDir, "manifest.json"))
	if err != nil {
		return RestoreDrillInput{}, maintenance.BackupVerification{}, maintenance.BackupManifest{}, err
	}
	if input.ActiveDatabase = strings.TrimSpace(input.ActiveDatabase); input.ActiveDatabase == "" {
		input.ActiveDatabase = strings.TrimSpace(manifest.Database.Name)
		if input.ActiveDatabase == "" {
			input.ActiveDatabase = "loom_main"
		}
	}
	if input.TargetDatabase = strings.TrimSpace(input.TargetDatabase); input.TargetDatabase == "" {
		input.TargetDatabase = DefaultRestoreDrillDatabase(currentTime(input.Now))
	}
	if err := ValidateRestoreDrillDatabase(input.TargetDatabase, input.ActiveDatabase); err != nil {
		return RestoreDrillInput{}, maintenance.BackupVerification{}, maintenance.BackupManifest{}, err
	}
	verification, err := maintenance.VerifyBackupDirectory(ctx, input.BackupDir)
	if err != nil {
		return RestoreDrillInput{}, maintenance.BackupVerification{}, maintenance.BackupManifest{}, err
	}
	if verification.Status != maintenance.VerificationSucceeded {
		return RestoreDrillInput{}, verification, manifest, loomerrors.New("backup.verification_failed", "backup", input.BackupDir, "Backup verification failed; run loom backup verify for details.")
	}
	return *input, verification, manifest, nil
}

func requireBackupArtifactsForRestoreDrill(ctx context.Context, backupDir string, manifest maintenance.BackupManifest) error {
	if strings.TrimSpace(manifest.Database.DumpFile) == "" {
		return loomerrors.New("backup.dump_missing", "backup", "database.dump_file", "Backup manifest does not name a PostgreSQL dump.")
	}
	dumpPath, err := maintenance.ResolveBackupPath(backupDir, manifest.Database.DumpFile)
	if err != nil {
		return err
	}
	info, err := os.Stat(dumpPath)
	if err != nil {
		return fmt.Errorf("postgres dump is missing: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return loomerrors.New("backup.dump_invalid", "backup", manifest.Database.DumpFile, "PostgreSQL dump is missing or empty.")
	}
	if strings.TrimSpace(manifest.Paths.ObjectStore) != "" {
		if err := requireDirectory(backupDir, manifest.Paths.ObjectStore); err != nil {
			return err
		}
	}
	if strings.TrimSpace(manifest.Paths.Imports) != "" {
		importsRoot, err := maintenance.ResolveBackupPath(backupDir, manifest.Paths.Imports)
		if err != nil {
			return err
		}
		evidencePath, err := maintenance.ResolveBackupPath(backupDir, manifest.Paths.ImportsEvidence)
		if err != nil {
			return err
		}
		if err := maintenance.ValidateImportsBackup(ctx, importsRoot, evidencePath, manifest.Policies.Imports, manifest.Policies.ImportsSnapshot); err != nil {
			return fmt.Errorf("imports custody backup is invalid: %w", err)
		}
	}
	if strings.TrimSpace(manifest.Paths.PrivateBackups) != "" {
		if err := requireDirectory(backupDir, manifest.Paths.PrivateBackups); err != nil {
			return err
		}
		if manifest.Schema == maintenance.BackupManifestSchemaV09 {
			root, err := maintenance.ResolveBackupPath(backupDir, manifest.Paths.PrivateBackups)
			if err != nil {
				return err
			}
			if err := maintenance.ValidateLegacyPrivateBackupsCustody(root); err != nil {
				return fmt.Errorf("legacy private backups custody is invalid: %w", err)
			}
		}
	}
	if strings.TrimSpace(manifest.Paths.UserBackups) != "" {
		if err := requireUserBackupCustody(backupDir, manifest.Paths.UserBackups); err != nil {
			return err
		}
	}
	if strings.TrimSpace(manifest.Paths.MainDocuments) != "" {
		if err := requireDirectory(backupDir, manifest.Paths.MainDocuments); err != nil {
			return err
		}
	}
	if strings.TrimSpace(manifest.Paths.StorageRetention) != "" {
		if err := requireDirectory(backupDir, manifest.Paths.StorageRetention); err != nil {
			return err
		}
	}
	if strings.TrimSpace(manifest.Paths.StorageArchive) != "" {
		if err := requireArchiveCustody(backupDir, manifest.Paths.StorageArchive); err != nil {
			return err
		}
	}
	if strings.TrimSpace(manifest.Paths.NotesProjection) != "" {
		if err := requireDirectory(backupDir, manifest.Paths.NotesProjection); err != nil {
			return err
		}
	}
	return nil
}

func requireUserBackupCustody(backupDir, relativePath string) error {
	if err := requireDirectory(backupDir, relativePath); err != nil {
		return err
	}
	root, err := maintenance.ResolveBackupPath(backupDir, relativePath)
	if err != nil {
		return err
	}
	return maintenance.ValidateUserBackupsCustody(root)
}

func requireArchiveCustody(backupDir, relativePath string) error {
	if err := requireDirectory(backupDir, relativePath); err != nil {
		return err
	}
	root, err := maintenance.ResolveBackupPath(backupDir, relativePath)
	if err != nil {
		return err
	}
	return maintenance.ValidateStorageArchiveCustody(root)
}

func requireDirectory(backupDir, relativePath string) error {
	path, err := maintenance.ResolveBackupPath(backupDir, relativePath)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("backup directory %q is missing: %w", relativePath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("backup path %q is not a directory", relativePath)
	}
	return nil
}

func defaultCommandRunner(ctx context.Context, name string, args []string, stdin io.Reader) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s %s failed: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func parseRestoreDrillSummary(output []byte) map[string]any {
	trimmed := bytes.TrimSpace(output)
	if len(trimmed) == 0 {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(trimmed, &parsed); err == nil {
		return parsed
	}
	return map[string]any{"raw": string(trimmed)}
}

func currentTime(now func() time.Time) time.Time {
	if now == nil {
		return time.Now().UTC()
	}
	value := now()
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}

const restoreDrillSQL = `DO $$
BEGIN
  IF to_regclass('public.goose_db_version') IS NULL THEN
    RAISE EXCEPTION 'missing public.goose_db_version';
  END IF;
  IF to_regclass('nodes.nodes') IS NULL THEN
    RAISE EXCEPTION 'missing nodes.nodes';
  END IF;
  IF to_regclass('workers.worker_instances') IS NULL THEN
    RAISE EXCEPTION 'missing workers.worker_instances';
  END IF;
  IF to_regclass('workers.worker_runs') IS NULL THEN
    RAISE EXCEPTION 'missing workers.worker_runs';
  END IF;
  IF to_regclass('maintenance.operations') IS NULL THEN
    RAISE EXCEPTION 'missing maintenance.operations';
  END IF;
  IF (SELECT count(*) FROM nodes.nodes) < 1 THEN
    RAISE EXCEPTION 'restored database has no nodes';
  END IF;
  IF (SELECT count(*) FROM workers.worker_instances) < 1 THEN
    RAISE EXCEPTION 'restored database has no worker instances';
  END IF;
END $$;
SELECT json_build_object(
  'migration', (SELECT max(version_id) FROM public.goose_db_version WHERE is_applied),
  'nodes', (SELECT count(*) FROM nodes.nodes),
  'worker_instances', (SELECT count(*) FROM workers.worker_instances),
  'worker_runs', (SELECT count(*) FROM workers.worker_runs),
  'maintenance_operations', (SELECT count(*) FROM maintenance.operations)
)::text;
`
