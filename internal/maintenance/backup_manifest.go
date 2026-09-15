package maintenance

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"loom.local/loom/internal/backupstrategy"
	"loom.local/loom/internal/filesystemmeta"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/provenance"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/watchedroots"
)

const (
	OperationalBackupManifestSchema = backupstrategy.OperationalPackageSchema

	BackupManifestSchemaV01  = "loom.backup.manifest.v0.1"
	BackupManifestSchemaV02  = "loom.backup.manifest.v0.2"
	BackupManifestSchemaV051 = "loom.backup.manifest.v0.5.1"
	BackupManifestSchemaV063 = "loom.backup.manifest.v0.6.3"
	BackupManifestSchemaV065 = "loom.backup.manifest.v0.6.5"
	BackupManifestSchemaV08  = "loom.backup.manifest.v0.8"
	BackupManifestSchemaV09  = "loom.backup.manifest.v0.9"
	BackupManifestSchemaV010 = provenance.RecoveryManifestSchema
	maxBackupManifestBytes   = 16 << 20

	ArtifactKindPrivateBackupsEvidence = "private_backups_evidence"
	ArtifactKindProvenanceDump         = "provenance_postgres_dump"
	PrivateBackupsEvidencePath         = "private-backups-evidence.json"
	privateBackupsEvidenceSchema       = "loom.backup.private_backups_evidence.v0.9"
	maxPrivateBackupsEvidenceBytes     = 64 << 20
	maxPrivateBackupsEvidenceEntries   = 1_000_000
	maxPrivateBackupsEvidencePath      = 4096
)

// VerifyOperationalBackupPackage adapts the bounded operational package
// verifier to the existing maintenance verification result. The expected
// manifest identity is mandatory here because update compatibility must bind
// one exact pre-update rollback package rather than accept any structurally
// valid directory at the supplied path.
func VerifyOperationalBackupPackage(ctx context.Context, packageDir, expectedManifestSHA256 string, expectedSchemaHead int64, forbiddenValues []string) (BackupVerification, error) {
	verification := BackupVerification{
		Status:         VerificationFailed,
		BackupDir:      filepath.Clean(strings.TrimSpace(packageDir)),
		ManifestPath:   filepath.Join(filepath.Clean(strings.TrimSpace(packageDir)), backupstrategy.OperationalManifestFile),
		CheckedAt:      time.Now().UTC(),
		Checks:         map[string]string{},
		Errors:         []string{},
		ManifestSchema: OperationalBackupManifestSchema,
	}
	if strings.TrimSpace(expectedManifestSHA256) == "" {
		verification.Checks["operational_manifest_identity"] = VerificationFailed
		verification.Errors = append(verification.Errors, "operational package verifier requires an expected manifest identity")
		return verification, nil
	}
	packageVerification, err := backupstrategy.VerifyOperationalPackage(ctx, backupstrategy.OperationalPackageVerificationInput{
		PackageDir:             packageDir,
		ExpectedManifestSHA256: expectedManifestSHA256,
		ExpectedSchemaHead:     &expectedSchemaHead,
		ForbiddenValues:        forbiddenValues,
	})
	if err != nil {
		return BackupVerification{}, err
	}
	verification.BackupDir = packageVerification.PackageDir
	verification.ManifestPath = packageVerification.ManifestPath
	verification.TotalArtifactBytes = packageVerification.TotalBytes
	verification.VerifiedArtifactNum = packageVerification.ArtifactCount
	current := packageVerification.SchemaHead
	latest := packageVerification.LatestMigration
	verification.CurrentMigration = &current
	verification.LatestMigration = &latest
	if packageVerification.Status == "succeeded" {
		verification.Status = VerificationSucceeded
		verification.Checks["operational_package"] = VerificationSucceeded
		verification.Checks["operational_manifest_identity"] = VerificationSucceeded
		verification.Checks["operational_schema_head"] = VerificationSucceeded
		return verification, nil
	}
	verification.Checks["operational_package"] = VerificationFailed
	for _, finding := range packageVerification.Findings {
		check := "operational:" + finding.Code
		verification.Checks[check] = VerificationFailed
		message := finding.Code
		if finding.Artifact != "" {
			message += ": " + finding.Artifact
		}
		if finding.Detail != "" {
			message += ": " + finding.Detail
		}
		verification.Errors = append(verification.Errors, message)
	}
	return verification, nil
}

type BackupManifest struct {
	Schema            string                    `json:"schema"`
	BackupKind        string                    `json:"backup_kind"`
	BackupOperationID string                    `json:"backup_operation_id,omitempty"`
	WorkerRunID       string                    `json:"worker_run_id,omitempty"`
	CreatedAt         string                    `json:"created_at"`
	Source            BackupManifestSource      `json:"source"`
	Loom              BackupManifestLoom        `json:"loom"`
	System            BackupManifestSystem      `json:"system,omitempty"`
	Database          BackupManifestDatabase    `json:"database"`
	Paths             BackupManifestPaths       `json:"paths"`
	Artifacts         []BackupManifestArtifact  `json:"artifacts,omitempty"`
	Verification      map[string]any            `json:"verification,omitempty"`
	Exclusions        []string                  `json:"exclusions,omitempty"`
	Retention         BackupManifestRetention   `json:"retention,omitempty"`
	Policies          BackupManifestPolicies    `json:"policies,omitempty"`
	Provenance        *BackupManifestProvenance `json:"provenance,omitempty"`
}

type BackupManifestSource struct {
	SSHHost     string `json:"ssh_host,omitempty"`
	Hostname    string `json:"hostname,omitempty"`
	NodeID      string `json:"node_id,omitempty"`
	NodeRole    string `json:"node_role,omitempty"`
	Environment string `json:"environment,omitempty"`
	IsVPS       bool   `json:"is_vps"`
}

type BackupManifestLoom struct {
	Version          string `json:"version,omitempty"`
	SourceCommit     string `json:"source_commit,omitempty"`
	CurrentMigration *int64 `json:"current_migration"`
	LatestMigration  *int64 `json:"latest_migration"`
}

type BackupManifestDatabase struct {
	Name     string `json:"name,omitempty"`
	DumpFile string `json:"dump_file"`
	Format   string `json:"format,omitempty"`
}

type BackupManifestPaths struct {
	ObjectStore            string `json:"object_store,omitempty"`
	Imports                string `json:"imports,omitempty"`
	ImportsEvidence        string `json:"imports_evidence,omitempty"`
	UserBackups            string `json:"user_backups,omitempty"`
	PrivateBackups         string `json:"private_backups,omitempty"`
	PrivateBackupsEvidence string `json:"private_backups_evidence,omitempty"`
	MainDocuments          string `json:"main_documents,omitempty"`
	StorageRetention       string `json:"storage_retention,omitempty"`
	StorageArchive         string `json:"storage_archive,omitempty"`
	NotesProjection        string `json:"notes_projection,omitempty"`
	BoxState               string `json:"box_state,omitempty"`
	InstallManifest        string `json:"install_manifest,omitempty"`
	ServiceEnvRedacted     string `json:"service_env_redacted,omitempty"`
	RemoteObjectStore      string `json:"remote_object_store,omitempty"`
	RemotePrivateBackups   string `json:"remote_private_backups,omitempty"`
}

type BackupManifestSystem struct {
	NixGeneration string `json:"nix_generation,omitempty"`
}

type BackupManifestRetention struct {
	Mode        string `json:"mode,omitempty"`
	KeepDaily   int    `json:"keep_daily,omitempty"`
	KeepWeekly  int    `json:"keep_weekly,omitempty"`
	KeepMonthly int    `json:"keep_monthly,omitempty"`
}

type BackupManifestPolicies struct {
	MainBox          string `json:"main_box,omitempty"`
	NotesSourceRoots string `json:"notes_source_roots,omitempty"`
	NotesProjection  string `json:"notes_projection,omitempty"`
	KnowledgeIndex   string `json:"knowledge_index,omitempty"`
	Imports          string `json:"imports,omitempty"`
	ImportsSnapshot  string `json:"imports_snapshot,omitempty"`
}

type BackupManifestArtifact struct {
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	FileCount *int64 `json:"file_count,omitempty"`
	SizeBytes *int64 `json:"size_bytes,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
}

// BackupManifestProvenance binds a complete isolated semantic-ledger dump to
// the exact logical state that a disposable restore must reproduce. It carries
// only recovery metadata; semantic bodies never enter the manifest.
type BackupManifestProvenance struct {
	State             string                 `json:"state"`
	StartedAt         string                 `json:"started_at"`
	CompletedAt       string                 `json:"completed_at"`
	Database          BackupManifestDatabase `json:"database"`
	SchemaHead        int                    `json:"schema_head"`
	RequiredRelations []string               `json:"required_relations"`
	LogicalCounts     map[string]int64       `json:"logical_counts"`
	GraphDigest       string                 `json:"graph_digest"`
	DumpSizeBytes     int64                  `json:"dump_size_bytes"`
	DumpSHA256        string                 `json:"dump_sha256"`
}

type ProvenanceBackupVerification struct {
	Status         string           `json:"status"`
	CheckedAt      time.Time        `json:"checked_at"`
	ManifestSHA256 string           `json:"manifest_sha256"`
	SchemaHead     int              `json:"schema_head"`
	LogicalCounts  map[string]int64 `json:"logical_counts"`
	GraphDigest    string           `json:"graph_digest"`
	CompletedAt    time.Time        `json:"completed_at"`
	DumpFile       string           `json:"dump_file"`
	DumpSizeBytes  int64            `json:"dump_size_bytes"`
	DumpSHA256     string           `json:"dump_sha256"`
}

// PrivateBackupsEvidence is portable, deterministic evidence for the exact
// retained legacy payload bytes represented by BackupManifestPaths.PrivateBackups.
// Historical shape-only manifests remain readable, but every newly generated
// mixed v0.9 backup binds one of these files into its normal artifact set.
type PrivateBackupsEvidence struct {
	Schema      string                        `json:"schema"`
	CustodyPath string                        `json:"custody_path"`
	FileCount   int64                         `json:"file_count"`
	TotalBytes  int64                         `json:"total_bytes"`
	Entries     []PrivateBackupsEvidenceEntry `json:"entries"`
}

type PrivateBackupsEvidenceEntry struct {
	RelativePath string `json:"relative_path"`
	SizeBytes    int64  `json:"size_bytes"`
	SHA256       string `json:"sha256"`
}

func ReadBackupManifest(path string) (BackupManifest, error) {
	manifest, _, _, err := ReadBackupManifestWithSHA256(path)
	return manifest, err
}

// ReadBackupManifestWithSHA256 decodes and authenticates the exact bytes read
// from one bounded no-follow regular file. Cloud upload callers use the same
// opened payload for schema validation and the independent source digest.
func ReadBackupManifestWithSHA256(path string) (BackupManifest, string, int64, error) {
	return readBackupManifestWithSHA256(path, nil)
}

func readBackupManifestWithSHA256(path string, requiredMode *os.FileMode) (BackupManifest, string, int64, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return BackupManifest{}, "", 0, fmt.Errorf("%w: manifest path is required", ErrInvalid)
	}
	clean := filepath.Clean(path)
	info, err := os.Lstat(clean)
	if err != nil {
		return BackupManifest{}, "", 0, fmt.Errorf("read backup manifest: %w", err)
	}
	if !info.Mode().IsRegular() {
		return BackupManifest{}, "", 0, fmt.Errorf("%w: backup manifest must be a no-follow regular file", ErrInvalid)
	}
	if requiredMode != nil && !exactPrivateFileMode(info, *requiredMode) {
		return BackupManifest{}, "", 0, fmt.Errorf("%w: backup manifest must be a no-follow regular file at exact mode %04o", ErrInvalid, *requiredMode)
	}
	if info.Size() > maxBackupManifestBytes {
		return BackupManifest{}, "", 0, fmt.Errorf("%w: backup manifest exceeds %d bytes", ErrInvalid, maxBackupManifestBytes)
	}
	file, err := openNoFollowRegularFile(clean)
	if err != nil {
		return BackupManifest{}, "", 0, fmt.Errorf("read backup manifest: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return BackupManifest{}, "", 0, fmt.Errorf("read backup manifest: %w", err)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) || (requiredMode != nil && !exactPrivateFileMode(openedInfo, *requiredMode)) {
		return BackupManifest{}, "", 0, fmt.Errorf("%w: backup manifest changed while opening", ErrInvalid)
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxBackupManifestBytes+1))
	if err != nil {
		return BackupManifest{}, "", 0, fmt.Errorf("read backup manifest: %w", err)
	}
	if int64(len(raw)) > maxBackupManifestBytes || int64(len(raw)) != info.Size() {
		return BackupManifest{}, "", 0, fmt.Errorf("%w: backup manifest changed size or exceeds %d bytes", ErrInvalid, maxBackupManifestBytes)
	}
	openedAfter, err := file.Stat()
	if err != nil || !openedAfter.Mode().IsRegular() || !os.SameFile(openedInfo, openedAfter) || openedAfter.Size() != openedInfo.Size() || !openedAfter.ModTime().Equal(openedInfo.ModTime()) || (requiredMode != nil && !exactPrivateFileMode(openedAfter, *requiredMode)) {
		return BackupManifest{}, "", 0, fmt.Errorf("%w: backup manifest changed while reading", ErrInvalid)
	}
	namedAfter, err := os.Lstat(clean)
	if err != nil || !namedAfter.Mode().IsRegular() || !os.SameFile(openedAfter, namedAfter) || namedAfter.Size() != openedInfo.Size() || !namedAfter.ModTime().Equal(openedInfo.ModTime()) || (requiredMode != nil && !exactPrivateFileMode(namedAfter, *requiredMode)) {
		return BackupManifest{}, "", 0, fmt.Errorf("%w: backup manifest path changed while reading", ErrInvalid)
	}

	var manifest BackupManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return BackupManifest{}, "", 0, fmt.Errorf("%w: backup manifest is invalid JSON: %w", ErrInvalid, err)
	}
	manifest.Schema = strings.TrimSpace(manifest.Schema)
	switch manifest.Schema {
	case BackupManifestSchemaV01, BackupManifestSchemaV02, BackupManifestSchemaV051, BackupManifestSchemaV063, BackupManifestSchemaV065, BackupManifestSchemaV08, BackupManifestSchemaV09, BackupManifestSchemaV010:
	default:
		return BackupManifest{}, "", 0, fmt.Errorf("%w: unsupported backup manifest schema %q", ErrInvalid, manifest.Schema)
	}
	if strings.TrimSpace(manifest.BackupKind) == "" {
		return BackupManifest{}, "", 0, fmt.Errorf("%w: backup manifest missing backup_kind", ErrInvalid)
	}
	digest := sha256.Sum256(raw)
	return manifest, hex.EncodeToString(digest[:]), int64(len(raw)), nil
}

// VerifyProvenanceBackupPackage requires an independently retained manifest
// identity. Structural validity alone is insufficient because an attacker who
// replaces both a dump and its manifest must not create recovery success.
func VerifyProvenanceBackupPackage(ctx context.Context, backupDir, expectedManifestSHA256 string) (ProvenanceBackupVerification, error) {
	checkedAt := time.Now().UTC()
	backupDir = filepath.Clean(strings.TrimSpace(backupDir))
	if backupDir == "." || backupDir == "" {
		return ProvenanceBackupVerification{}, fmt.Errorf("%w: backup directory is required", ErrInvalid)
	}
	if err := requireProvenancePackageRoot(backupDir); err != nil {
		return ProvenanceBackupVerification{}, err
	}
	expected := normalizeManifestSHA256(expectedManifestSHA256)
	if !validManifestSHA256(expected) {
		return ProvenanceBackupVerification{}, fmt.Errorf("%w: exact provenance manifest sha256 is required", ErrInvalid)
	}
	privateFileMode := os.FileMode(0o600)
	manifest, actual, _, err := readBackupManifestWithSHA256(filepath.Join(backupDir, "manifest.json"), &privateFileMode)
	if err != nil {
		return ProvenanceBackupVerification{}, err
	}
	if subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) != 1 {
		return ProvenanceBackupVerification{}, fmt.Errorf("%w: provenance backup manifest identity mismatch", ErrInvalid)
	}
	completedAt, err := ValidateProvenanceBackupManifest(manifest)
	if err != nil {
		return ProvenanceBackupVerification{}, err
	}
	size, err := verifyProvenanceDump(ctx, backupDir, *manifest.Provenance)
	if err != nil {
		return ProvenanceBackupVerification{}, err
	}
	return ProvenanceBackupVerification{
		Status:         VerificationSucceeded,
		CheckedAt:      checkedAt,
		ManifestSHA256: actual,
		SchemaHead:     manifest.Provenance.SchemaHead,
		LogicalCounts:  cloneCountMap(manifest.Provenance.LogicalCounts),
		GraphDigest:    manifest.Provenance.GraphDigest,
		CompletedAt:    completedAt,
		DumpFile:       manifest.Provenance.Database.DumpFile,
		DumpSizeBytes:  size,
		DumpSHA256:     normalizeManifestSHA256(manifest.Provenance.DumpSHA256),
	}, nil
}

// ValidateProvenanceBackupManifest verifies complete logical coverage without
// reading the dump. Restore paths must additionally call
// VerifyProvenanceBackupPackage with an independently retained manifest hash.
func ValidateProvenanceBackupManifest(manifest BackupManifest) (time.Time, error) {
	if manifest.Schema != BackupManifestSchemaV010 {
		return time.Time{}, fmt.Errorf("%w: provenance recovery requires manifest schema %s", ErrInvalid, BackupManifestSchemaV010)
	}
	if manifest.Provenance == nil {
		return time.Time{}, fmt.Errorf("%w: provenance recovery metadata is missing", ErrInvalid)
	}
	contract := manifest.Provenance
	if strings.TrimSpace(contract.State) != provenance.BackupStateComplete {
		return time.Time{}, fmt.Errorf("%w: provenance backup is not complete", ErrInvalid)
	}
	startedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(contract.StartedAt))
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: provenance backup started_at is invalid", ErrInvalid)
	}
	completedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(contract.CompletedAt))
	if err != nil || completedAt.Before(startedAt) {
		return time.Time{}, fmt.Errorf("%w: provenance backup completed_at is invalid", ErrInvalid)
	}
	if contract.Database.Name != provenance.DatabaseName || contract.Database.Format != "pg_dump_custom" || strings.TrimSpace(contract.Database.DumpFile) == "" {
		return time.Time{}, fmt.Errorf("%w: provenance backup database contract is incompatible", ErrInvalid)
	}
	required, err := provenance.RecoveryRelationsForSchemaHead(contract.SchemaHead)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: provenance backup schema head %d is incompatible with packaged recovery contracts: %v", ErrInvalid, contract.SchemaHead, err)
	}
	declared := append([]string(nil), contract.RequiredRelations...)
	sort.Strings(required)
	sort.Strings(declared)
	if strings.Join(required, "\x00") != strings.Join(declared, "\x00") {
		return time.Time{}, fmt.Errorf("%w: provenance backup required relation coverage is incomplete", ErrInvalid)
	}
	if len(contract.LogicalCounts) != len(required) {
		return time.Time{}, fmt.Errorf("%w: provenance backup logical count coverage is incomplete", ErrInvalid)
	}
	for _, relation := range required {
		count, ok := contract.LogicalCounts[relation]
		if !ok || count < 0 {
			return time.Time{}, fmt.Errorf("%w: provenance backup logical count is missing for %s", ErrInvalid, relation)
		}
	}
	if !validGraphDigest(contract.GraphDigest) {
		return time.Time{}, fmt.Errorf("%w: provenance backup graph digest is invalid", ErrInvalid)
	}
	if contract.DumpSizeBytes <= 0 || !validManifestSHA256(normalizeManifestSHA256(contract.DumpSHA256)) {
		return time.Time{}, fmt.Errorf("%w: provenance backup dump size or sha256 is invalid", ErrInvalid)
	}
	if err := validateProvenanceArtifactBinding(manifest); err != nil {
		return time.Time{}, err
	}
	return completedAt.UTC(), nil
}

func validateProvenanceArtifactBinding(manifest BackupManifest) error {
	contract := manifest.Provenance
	matches := 0
	for _, artifact := range manifest.Artifacts {
		if strings.TrimSpace(artifact.Kind) != ArtifactKindProvenanceDump {
			continue
		}
		matches++
		if strings.TrimSpace(artifact.Path) != strings.TrimSpace(contract.Database.DumpFile) ||
			artifact.FileCount == nil || *artifact.FileCount != 1 ||
			artifact.SizeBytes == nil || *artifact.SizeBytes != contract.DumpSizeBytes ||
			normalizeManifestSHA256(artifact.SHA256) != normalizeManifestSHA256(contract.DumpSHA256) {
			return fmt.Errorf("%w: provenance dump artifact does not match its recovery contract", ErrInvalid)
		}
	}
	if matches != 1 {
		return fmt.Errorf("%w: provenance recovery requires exactly one authenticated dump artifact", ErrInvalid)
	}
	return nil
}

func verifyProvenanceDump(ctx context.Context, backupDir string, contract BackupManifestProvenance) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	dumpPath, err := safeBackupPath(backupDir, contract.Database.DumpFile)
	if err != nil {
		return 0, err
	}
	privateFileMode := os.FileMode(0o600)
	digest, size, err := hashStableRegularFileWithMode(ctx, dumpPath, &privateFileMode)
	if err != nil {
		return 0, fmt.Errorf("verify provenance postgres dump: %w", err)
	}
	if size != contract.DumpSizeBytes || subtle.ConstantTimeCompare([]byte(digest), []byte(normalizeManifestSHA256(contract.DumpSHA256))) != 1 {
		return 0, fmt.Errorf("%w: provenance postgres dump size or sha256 mismatch", ErrInvalid)
	}
	return size, nil
}

func hashStableRegularFile(ctx context.Context, filePath string) (string, int64, error) {
	return hashStableRegularFileWithMode(ctx, filePath, nil)
}

func hashStableRegularFileWithMode(ctx context.Context, filePath string, requiredMode *os.FileMode) (string, int64, error) {
	before, err := os.Lstat(filePath)
	if err != nil {
		return "", 0, err
	}
	if !before.Mode().IsRegular() {
		return "", 0, fmt.Errorf("file must be a no-follow regular file")
	}
	if requiredMode != nil && !exactPrivateFileMode(before, *requiredMode) {
		return "", 0, fmt.Errorf("file must be a no-follow regular file at exact mode %04o", *requiredMode)
	}
	file, err := openNoFollowRegularFile(filePath)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) || (requiredMode != nil && !exactPrivateFileMode(opened, *requiredMode)) {
		return "", 0, fmt.Errorf("file changed while opening")
	}
	hasher := sha256.New()
	size, err := io.Copy(hasher, &contextReader{ctx: ctx, reader: file})
	if err != nil {
		return "", 0, err
	}
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(opened, after) || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) || size != opened.Size() || (requiredMode != nil && !exactPrivateFileMode(after, *requiredMode)) {
		return "", 0, fmt.Errorf("file changed while hashing")
	}
	namedAfter, err := os.Lstat(filePath)
	if err != nil || !namedAfter.Mode().IsRegular() || !os.SameFile(after, namedAfter) || namedAfter.Size() != opened.Size() || !namedAfter.ModTime().Equal(opened.ModTime()) || (requiredMode != nil && !exactPrivateFileMode(namedAfter, *requiredMode)) {
		return "", 0, fmt.Errorf("file path changed while hashing")
	}
	return hex.EncodeToString(hasher.Sum(nil)), size, nil
}

func requireProvenancePackageRoot(backupDir string) error {
	info, err := os.Lstat(backupDir)
	if err != nil {
		return fmt.Errorf("inspect provenance backup package root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !exactPrivatePermissionMode(info.Mode(), 0o700) {
		return fmt.Errorf("%w: provenance backup package root must be a real directory at exact mode 0700", ErrInvalid)
	}
	return nil
}

func openNoFollowRegularFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open no-follow regular file returned no file")
	}
	return file, nil
}

func exactPrivateFileMode(info os.FileInfo, want os.FileMode) bool {
	return info != nil && info.Mode().IsRegular() && exactPrivatePermissionMode(info.Mode(), want)
}

func exactPrivatePermissionMode(mode, want os.FileMode) bool {
	return mode.Perm() == want && mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *contextReader) Read(target []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(target)
}

func validManifestSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validGraphDigest(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "sha256:") && validManifestSHA256(strings.TrimPrefix(value, "sha256:"))
}

func cloneCountMap(input map[string]int64) map[string]int64 {
	result := make(map[string]int64, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func VerifyBackupDirectory(ctx context.Context, backupDir string) (BackupVerification, error) {
	backupDir = filepath.Clean(strings.TrimSpace(backupDir))
	if backupDir == "." || backupDir == "" {
		return BackupVerification{}, fmt.Errorf("%w: backup directory is required", ErrInvalid)
	}

	verification := BackupVerification{
		Status:    VerificationSucceeded,
		BackupDir: backupDir,
		CheckedAt: time.Now().UTC(),
		Checks:    map[string]string{},
		Errors:    []string{},
	}
	fail := func(check, message string) {
		verification.Status = VerificationFailed
		verification.Checks[check] = VerificationFailed
		verification.Errors = append(verification.Errors, message)
	}
	pass := func(check string) {
		if verification.Checks[check] == "" {
			verification.Checks[check] = VerificationSucceeded
		}
	}

	if err := ctx.Err(); err != nil {
		return BackupVerification{}, err
	}
	info, err := os.Stat(backupDir)
	if err != nil {
		fail("backup_dir", fmt.Sprintf("backup directory is not readable: %v", err))
		return verification, nil
	}
	if !info.IsDir() {
		fail("backup_dir", "backup path is not a directory")
		return verification, nil
	}
	pass("backup_dir")

	manifestPath := filepath.Join(backupDir, "manifest.json")
	verification.ManifestPath = manifestPath
	manifest, err := ReadBackupManifest(manifestPath)
	if err != nil {
		fail("manifest", err.Error())
		return verification, nil
	}
	verification.ManifestSchema = manifest.Schema
	verification.CurrentMigration = manifest.Loom.CurrentMigration
	verification.LatestMigration = manifest.Loom.LatestMigration
	pass("manifest")
	if manifest.Schema == BackupManifestSchemaV010 {
		if _, err := ValidateProvenanceBackupManifest(manifest); err != nil {
			fail("provenance_manifest", err.Error())
		} else if size, err := verifyProvenanceDump(ctx, backupDir, *manifest.Provenance); err != nil {
			fail("provenance_dump", err.Error())
		} else {
			verification.TotalArtifactBytes += size
			verification.VerifiedArtifactNum++
			pass("provenance_manifest")
			pass("provenance_dump")
		}
	}

	if manifest.Source.IsVPS || strings.Contains(strings.ToLower(manifest.Source.SSHHost), "vps") {
		fail("source", "backup source must not be the VPS")
	} else {
		pass("source")
	}
	if manifest.Loom.CurrentMigration == nil || manifest.Loom.LatestMigration == nil {
		fail("migrations", "manifest is missing migration metadata")
	} else {
		pass("migrations")
	}

	healthPath := filepath.Join(backupDir, "health.json")
	if size, err := requireNonEmptyFile(healthPath); err != nil {
		fail("health_report", err.Error())
	} else {
		verification.TotalArtifactBytes += size
		verification.VerifiedArtifactNum++
		pass("health_report")
	}

	dumpPath, err := safeBackupPath(backupDir, manifest.Database.DumpFile)
	if err != nil {
		fail("postgres_dump", err.Error())
	} else if size, err := requireNonEmptyFile(dumpPath); err != nil {
		fail("postgres_dump", err.Error())
	} else {
		verification.TotalArtifactBytes += size
		verification.VerifiedArtifactNum++
		pass("postgres_dump")
	}

	if strings.TrimSpace(manifest.Paths.ObjectStore) != "" {
		if err := requireDirectoryUnder(backupDir, manifest.Paths.ObjectStore); err != nil {
			fail("object_store_snapshot", err.Error())
		} else {
			pass("object_store_snapshot")
		}
	}
	if strings.TrimSpace(manifest.Paths.Imports) != "" {
		importsRoot, importsErr := safeBackupPath(backupDir, manifest.Paths.Imports)
		evidencePath, evidenceErr := safeBackupPath(backupDir, manifest.Paths.ImportsEvidence)
		if importsErr != nil {
			fail("imports_snapshot", importsErr.Error())
		} else if evidenceErr != nil {
			fail("imports_snapshot", evidenceErr.Error())
		} else if err := ValidateImportsBackup(ctx, importsRoot, evidencePath, manifest.Policies.Imports, manifest.Policies.ImportsSnapshot); err != nil {
			fail("imports_snapshot", err.Error())
		} else {
			pass("imports_snapshot")
		}
	}
	if strings.TrimSpace(manifest.Paths.PrivateBackups) != "" {
		if err := requireDirectoryUnder(backupDir, manifest.Paths.PrivateBackups); err != nil {
			fail("private_backups_snapshot", err.Error())
		} else if manifest.Schema == BackupManifestSchemaV09 {
			root, resolveErr := safeBackupPath(backupDir, manifest.Paths.PrivateBackups)
			if resolveErr != nil {
				fail("private_backups_snapshot", resolveErr.Error())
			} else if err := ValidateLegacyPrivateBackupsCustody(root); err != nil {
				fail("private_backups_snapshot", err.Error())
			} else {
				pass("private_backups_snapshot")
			}
		} else {
			pass("private_backups_snapshot")
		}
	}
	if manifest.Schema == BackupManifestSchemaV09 && strings.TrimSpace(manifest.Paths.PrivateBackupsEvidence) != "" {
		root, rootErr := safeBackupPath(backupDir, manifest.Paths.PrivateBackups)
		evidencePath, evidenceErr := safeBackupPath(backupDir, manifest.Paths.PrivateBackupsEvidence)
		if rootErr != nil {
			fail("private_backups_evidence", rootErr.Error())
		} else if evidenceErr != nil {
			fail("private_backups_evidence", evidenceErr.Error())
		} else if err := ValidatePrivateBackupsEvidence(ctx, root, manifest.Paths.PrivateBackups, evidencePath); err != nil {
			fail("private_backups_evidence", err.Error())
		} else if err := validatePrivateBackupsEvidenceArtifactBinding(backupDir, manifest); err != nil {
			fail("private_backups_evidence", err.Error())
		} else {
			pass("private_backups_evidence")
		}
	}
	if strings.TrimSpace(manifest.Paths.UserBackups) != "" {
		if err := requireReadableDirectoryUnder(backupDir, manifest.Paths.UserBackups); err != nil {
			fail("user_backups_snapshot", err.Error())
		} else if err := requireUserBackupsLayout(backupDir, manifest.Paths.UserBackups); err != nil {
			fail("user_backups_snapshot", err.Error())
		} else {
			pass("user_backups_snapshot")
		}
	}
	if manifest.Schema == BackupManifestSchemaV09 && strings.TrimSpace(manifest.Paths.UserBackups) != "" && strings.TrimSpace(manifest.Paths.PrivateBackups) != "" {
		if err := validateDualUserBackupsManifest(backupDir, manifest); err != nil {
			fail("user_backups_dual_path", err.Error())
		} else {
			pass("user_backups_dual_path")
		}
	}
	if strings.TrimSpace(manifest.Paths.MainDocuments) != "" {
		if err := requireReadableDirectoryUnder(backupDir, manifest.Paths.MainDocuments); err != nil {
			fail("main_documents_snapshot", err.Error())
		} else {
			pass("main_documents_snapshot")
		}
	}
	if strings.TrimSpace(manifest.Paths.StorageRetention) != "" {
		if err := requireReadableDirectoryUnder(backupDir, manifest.Paths.StorageRetention); err != nil {
			fail("storage_retention_snapshot", err.Error())
		} else {
			pass("storage_retention_snapshot")
		}
	}
	if strings.TrimSpace(manifest.Paths.StorageArchive) != "" {
		if err := requireReadableDirectoryUnder(backupDir, manifest.Paths.StorageArchive); err != nil {
			fail("storage_archive_snapshot", err.Error())
		} else if err := requireStorageArchiveLayout(backupDir, manifest.Paths.StorageArchive); err != nil {
			fail("storage_archive_snapshot", err.Error())
		} else {
			pass("storage_archive_snapshot")
		}
	}
	if strings.TrimSpace(manifest.Paths.NotesProjection) != "" {
		if err := requireReadableDirectoryUnder(backupDir, manifest.Paths.NotesProjection); err != nil {
			fail("notes_projection_snapshot", err.Error())
		} else {
			pass("notes_projection_snapshot")
		}
	}
	if manifest.Schema == BackupManifestSchemaV08 || manifest.Schema == BackupManifestSchemaV09 {
		if strings.TrimSpace(manifest.Policies.NotesSourceRoots) == "" {
			fail("notes_source_roots_policy", "manifest is missing notes source roots backup policy")
		} else {
			pass("notes_source_roots_policy")
		}
		if strings.TrimSpace(manifest.Policies.NotesProjection) == "" {
			fail("notes_projection_policy", "manifest is missing notes projection backup policy")
		} else {
			pass("notes_projection_policy")
		}
		if strings.TrimSpace(manifest.Policies.KnowledgeIndex) == "" {
			fail("knowledge_index_policy", "manifest is missing knowledge index backup policy")
		} else {
			pass("knowledge_index_policy")
		}
		if manifest.Schema == BackupManifestSchemaV09 {
			if strings.TrimSpace(manifest.Paths.Imports) == "" || strings.TrimSpace(manifest.Paths.ImportsEvidence) == "" || strings.TrimSpace(manifest.Policies.Imports) == "" || strings.TrimSpace(manifest.Policies.ImportsSnapshot) == "" {
				fail("imports_policy", "v0.9 manifest is missing Imports snapshot path, evidence path, custody policy, or snapshot method")
			} else {
				pass("imports_policy")
			}
		}
	}
	if strings.TrimSpace(manifest.Paths.BoxState) != "" {
		if err := requireDirectoryUnder(backupDir, manifest.Paths.BoxState); err != nil {
			fail("box_state_snapshot", err.Error())
		} else {
			pass("box_state_snapshot")
		}
	}
	if strings.TrimSpace(manifest.Paths.InstallManifest) != "" {
		if path, err := ResolveBackupPath(backupDir, manifest.Paths.InstallManifest); err != nil {
			fail("install_manifest_snapshot", err.Error())
		} else if size, err := requireNonEmptyFile(path); err != nil {
			fail("install_manifest_snapshot", err.Error())
		} else {
			verification.TotalArtifactBytes += size
			verification.VerifiedArtifactNum++
			pass("install_manifest_snapshot")
		}
	}
	if strings.TrimSpace(manifest.Paths.ServiceEnvRedacted) != "" {
		if path, err := ResolveBackupPath(backupDir, manifest.Paths.ServiceEnvRedacted); err != nil {
			fail("service_env_redacted_snapshot", err.Error())
		} else if size, err := requireNonEmptyFile(path); err != nil {
			fail("service_env_redacted_snapshot", err.Error())
		} else if err := requireRedactedServiceEnv(path); err != nil {
			fail("service_env_redacted_snapshot", err.Error())
		} else {
			verification.TotalArtifactBytes += size
			verification.VerifiedArtifactNum++
			pass("service_env_redacted_snapshot")
		}
	}

	for _, artifact := range manifest.Artifacts {
		if err := ctx.Err(); err != nil {
			return BackupVerification{}, err
		}
		size, err := verifyManifestArtifact(backupDir, artifact)
		check := "artifact:" + strings.TrimSpace(artifact.Kind)
		if strings.TrimSpace(artifact.Path) != "" {
			check += ":" + strings.TrimSpace(artifact.Path)
		}
		if err != nil {
			fail(check, err.Error())
			continue
		}
		verification.TotalArtifactBytes += size
		verification.VerifiedArtifactNum++
		pass(check)
	}

	return verification, nil
}

func requireUserBackupsLayout(backupDir, relativePath string) error {
	root, err := safeBackupPath(backupDir, relativePath)
	if err != nil {
		return err
	}
	return ValidateUserBackupsCustody(root)
}

func validateDualUserBackupsManifest(backupDir string, manifest BackupManifest) error {
	userPath, err := safeBackupPath(backupDir, manifest.Paths.UserBackups)
	if err != nil {
		return err
	}
	privatePath, err := safeBackupPath(backupDir, manifest.Paths.PrivateBackups)
	if err != nil {
		return err
	}
	if pathWithin(userPath, privatePath) || pathWithin(privatePath, userPath) {
		return fmt.Errorf("canonical and legacy user backup snapshot paths must be distinct and non-overlapping")
	}
	userArtifacts := 0
	privateArtifacts := 0
	for _, artifact := range manifest.Artifacts {
		if strings.TrimSpace(artifact.Kind) != ArtifactKindPrivateBackupsSnapshot {
			continue
		}
		artifactPath, err := safeBackupPath(backupDir, artifact.Path)
		if err != nil {
			return err
		}
		switch artifactPath {
		case userPath:
			userArtifacts++
		case privatePath:
			privateArtifacts++
		default:
			return fmt.Errorf("user backup snapshot artifact %q does not identify either declared custody path", artifact.Path)
		}
	}
	if userArtifacts != 1 || privateArtifacts != 1 {
		return fmt.Errorf("dual-path user backup manifest requires exactly one snapshot artifact per custody path (user=%d private=%d)", userArtifacts, privateArtifacts)
	}
	if strings.TrimSpace(manifest.Paths.PrivateBackupsEvidence) == "" {
		return fmt.Errorf("dual-path user backup manifest requires private backups byte evidence")
	}
	return validatePrivateBackupsEvidenceArtifactBinding(backupDir, manifest)
}

func validatePrivateBackupsEvidenceArtifactBinding(backupDir string, manifest BackupManifest) error {
	evidencePath, err := safeBackupPath(backupDir, manifest.Paths.PrivateBackupsEvidence)
	if err != nil {
		return err
	}
	evidenceArtifacts := 0
	for _, artifact := range manifest.Artifacts {
		if strings.TrimSpace(artifact.Kind) != ArtifactKindPrivateBackupsEvidence {
			continue
		}
		artifactPath, err := safeBackupPath(backupDir, artifact.Path)
		if err != nil {
			return err
		}
		if artifactPath != evidencePath {
			return fmt.Errorf("private backups evidence artifact %q does not identify the declared evidence path", artifact.Path)
		}
		if artifact.SizeBytes == nil || *artifact.SizeBytes <= 0 || normalizeManifestSHA256(artifact.SHA256) == "" {
			return fmt.Errorf("private backups evidence artifact is missing size or sha256")
		}
		evidenceArtifacts++
	}
	if evidenceArtifacts != 1 {
		return fmt.Errorf("backup manifest requires exactly one private backups evidence artifact (got %d)", evidenceArtifacts)
	}
	return nil
}

func ResolveBackupPath(backupDir, relativePath string) (string, error) {
	return safeBackupPath(backupDir, relativePath)
}

func HashFile(path string) (string, int64, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", 0, err
	}
	defer file.Close()

	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func DirectoryInventory(root string) (int64, int64, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	var fileCount int64
	var totalBytes int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		fileCount++
		totalBytes += info.Size()
		return nil
	})
	return fileCount, totalBytes, err
}

func verifyManifestArtifact(backupDir string, artifact BackupManifestArtifact) (int64, error) {
	artifact.Path = strings.TrimSpace(artifact.Path)
	if artifact.Path == "" {
		return 0, fmt.Errorf("backup artifact %q is missing path", artifact.Kind)
	}
	path, err := safeBackupPath(backupDir, artifact.Path)
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("backup artifact %q is missing: %w", artifact.Path, err)
	}
	if info.IsDir() {
		fileCount, totalBytes, err := DirectoryInventory(path)
		if err != nil {
			return 0, fmt.Errorf("inventory backup artifact directory %q: %w", artifact.Path, err)
		}
		if artifact.FileCount != nil && *artifact.FileCount != fileCount {
			return 0, fmt.Errorf("backup artifact %q file count mismatch: got %d want %d", artifact.Path, fileCount, *artifact.FileCount)
		}
		if artifact.SizeBytes != nil && *artifact.SizeBytes != totalBytes {
			return 0, fmt.Errorf("backup artifact %q size mismatch: got %d want %d", artifact.Path, totalBytes, *artifact.SizeBytes)
		}
		if strings.TrimSpace(artifact.SHA256) != "" {
			return 0, fmt.Errorf("backup artifact %q is a directory and cannot have a sha256 file hash", artifact.Path)
		}
		return totalBytes, nil
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("backup artifact %q is not a regular file", artifact.Path)
	}
	if artifact.FileCount != nil && *artifact.FileCount != 1 {
		return 0, fmt.Errorf("backup artifact %q file count mismatch: got 1 want %d", artifact.Path, *artifact.FileCount)
	}
	hash, size, err := HashFile(path)
	if err != nil {
		return 0, fmt.Errorf("hash backup artifact %q: %w", artifact.Path, err)
	}
	if artifact.SizeBytes != nil && *artifact.SizeBytes != size {
		return 0, fmt.Errorf("backup artifact %q size mismatch: got %d want %d", artifact.Path, size, *artifact.SizeBytes)
	}
	if want := normalizeManifestSHA256(artifact.SHA256); want != "" && want != hash {
		return 0, fmt.Errorf("backup artifact %q sha256 mismatch", artifact.Path)
	}
	return size, nil
}

func safeBackupPath(backupDir, relativePath string) (string, error) {
	backupDir = filepath.Clean(backupDir)
	relativePath = strings.TrimSpace(relativePath)
	if relativePath == "" {
		return "", fmt.Errorf("%w: backup-relative path is required", ErrInvalid)
	}
	var path string
	if filepath.IsAbs(relativePath) {
		path = filepath.Clean(relativePath)
	} else {
		path = filepath.Join(backupDir, relativePath)
	}
	if !pathWithin(backupDir, path) {
		return "", fmt.Errorf("%w: backup path %q escapes backup directory", ErrInvalid, relativePath)
	}
	return path, nil
}

func requireDirectoryUnder(backupDir, relativePath string) error {
	path, err := safeBackupPath(backupDir, relativePath)
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

func requireReadableDirectoryUnder(backupDir, relativePath string) error {
	if err := requireDirectoryUnder(backupDir, relativePath); err != nil {
		return err
	}
	path, err := safeBackupPath(backupDir, relativePath)
	if err != nil {
		return err
	}
	if _, err := os.ReadDir(path); err != nil {
		return fmt.Errorf("backup directory %q is not readable: %w", relativePath, err)
	}
	return nil
}

func requireStorageArchiveLayout(backupDir, relativePath string) error {
	root, err := safeBackupPath(backupDir, relativePath)
	if err != nil {
		return err
	}
	return ValidateStorageArchiveCustody(root)
}

const maxCustodyManifestBytes = 64 << 20

type privateBackupCustodyManifest struct {
	SchemaVersion            string         `json:"schema_version"`
	PrivateBackupOperationID string         `json:"private_backup_operation_id"`
	SourceNodeID             string         `json:"source_node_id"`
	SourceNodeKey            string         `json:"source_node_key"`
	RootKey                  string         `json:"root_key"`
	BatchKey                 string         `json:"batch_key"`
	PayloadPath              string         `json:"payload_path"`
	PayloadSHA256            string         `json:"payload_sha256"`
	CoarseSizeBytes          int64          `json:"coarse_size_bytes"`
	Metadata                 map[string]any `json:"metadata"`
}

type watchedRootBackupCustodyManifest struct {
	SchemaVersion string                    `json:"schema_version"`
	SourceNodeID  string                    `json:"source_node_id"`
	SourceNodeKey string                    `json:"source_node_key"`
	RootKey       string                    `json:"root_key"`
	LocalBatchID  string                    `json:"local_batch_id"`
	MainBatchID   string                    `json:"watched_root_backup_batch_id"`
	BatchKind     string                    `json:"batch_kind"`
	BackupMode    string                    `json:"backup_mode"`
	Status        string                    `json:"status"`
	ItemCount     int                       `json:"item_count"`
	Items         []watchedroots.BackupItem `json:"items"`
}

// ValidateUserBackupsCustody verifies canonical v0.7 backup manifests and
// their snapshot-local payload evidence. Legacy raw backup coverage remains
// explicit through BackupManifestPaths.PrivateBackups.
func ValidateUserBackupsCustody(root string) error {
	root = filepath.Clean(strings.TrimSpace(root))
	if err := requireRealCustodyDirectory(root); err != nil {
		return err
	}
	nodes, err := custodyDirectories(root, ".")
	if err != nil {
		return err
	}
	for _, node := range nodes {
		protectedRoots, err := custodyDirectories(root, node)
		if err != nil {
			return err
		}
		for _, protectedRoot := range protectedRoots {
			rootRelative := filepath.Join(node, protectedRoot)
			batches, err := custodyDirectories(root, rootRelative)
			if err != nil {
				return err
			}
			for _, batch := range batches {
				batchRelative := filepath.Join(rootRelative, batch)
				manifestRelative := filepath.Join(batchRelative, "manifest.json")
				payload, err := readCustodyFile(root, manifestRelative, maxCustodyManifestBytes)
				if err != nil {
					return fmt.Errorf("user backup batch %q manifest: %w", batch, err)
				}
				var header struct {
					SchemaVersion string `json:"schema_version"`
				}
				if err := json.Unmarshal(payload, &header); err != nil {
					return fmt.Errorf("user backup batch %q manifest is invalid JSON: %w", batch, err)
				}
				switch header.SchemaVersion {
				case "storage.watched_root_backup_manifest.v0.7":
					var manifest watchedRootBackupCustodyManifest
					if err := json.Unmarshal(payload, &manifest); err != nil {
						return fmt.Errorf("decode watched-root backup manifest: %w", err)
					}
					if err := validateWatchedRootBackupManifest(root, node, protectedRoot, batch, batchRelative, manifest); err != nil {
						return err
					}
				case "storage.private_backup_manifest.v0.7":
					var manifest privateBackupCustodyManifest
					if err := json.Unmarshal(payload, &manifest); err != nil {
						return fmt.Errorf("decode private backup manifest: %w", err)
					}
					if err := validatePrivateBackupManifest(root, node, protectedRoot, batch, batchRelative, manifest, ""); err != nil {
						return err
					}
				default:
					return fmt.Errorf("user backup batch %q has unsupported or missing schema_version %q", batch, header.SchemaVersion)
				}
			}
		}
	}
	return nil
}

// ValidateLegacyPrivateBackupsCustody verifies the exact pre-v0.7 production
// custody shape retained during the canonical-filesystem transition:
// node_*/private_backup_*/payload.tar. New backups declare this layout through
// BackupManifestPaths.PrivateBackups instead of presenting it as v0.7 custody.
func ValidateLegacyPrivateBackupsCustody(root string) error {
	root = filepath.Clean(strings.TrimSpace(root))
	if err := requireRealCustodyDirectory(root); err != nil {
		return err
	}
	nodes, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, node := range nodes {
		if !strings.HasPrefix(node.Name(), "node_") || strings.TrimPrefix(node.Name(), "node_") == "" {
			return fmt.Errorf("legacy private backup node %q has an invalid custody name", node.Name())
		}
		if err := requireRealDirectoryEntry(root, node); err != nil {
			return err
		}
		nodeRoot := filepath.Join(root, node.Name())
		operations, err := os.ReadDir(nodeRoot)
		if err != nil {
			return err
		}
		if len(operations) == 0 {
			return fmt.Errorf("legacy private backup node %q has no operations", node.Name())
		}
		for _, operation := range operations {
			if !strings.HasPrefix(operation.Name(), "private_backup_") || strings.TrimPrefix(operation.Name(), "private_backup_") == "" {
				return fmt.Errorf("legacy private backup operation %q has an invalid custody name", filepath.Join(node.Name(), operation.Name()))
			}
			if err := requireRealDirectoryEntry(nodeRoot, operation); err != nil {
				return err
			}
			operationRoot := filepath.Join(nodeRoot, operation.Name())
			entries, err := os.ReadDir(operationRoot)
			if err != nil {
				return err
			}
			if len(entries) != 1 || entries[0].Name() != "payload.tar" {
				return fmt.Errorf("legacy private backup operation %q must contain only payload.tar", filepath.Join(node.Name(), operation.Name()))
			}
			payloadInfo, err := os.Lstat(filepath.Join(operationRoot, "payload.tar"))
			if err != nil {
				return err
			}
			if !payloadInfo.Mode().IsRegular() || payloadInfo.Size() <= 0 {
				return fmt.Errorf("legacy private backup payload %q must be a non-empty regular file", filepath.Join(node.Name(), operation.Name(), "payload.tar"))
			}
		}
	}
	return nil
}

// WritePrivateBackupsEvidence inventories the already-copied retained legacy
// custody tree and atomically publishes bounded portable byte evidence. The
// custody path is backup-relative and is included in the evidence so the file
// cannot be rebound to a different manifest path.
func WritePrivateBackupsEvidence(ctx context.Context, custodyRoot, custodyPath, evidencePath string) (PrivateBackupsEvidence, error) {
	evidence, err := buildPrivateBackupsEvidence(ctx, custodyRoot, custodyPath)
	if err != nil {
		return PrivateBackupsEvidence{}, err
	}
	if err := writePrivateBackupsEvidenceAtomic(evidencePath, evidence, maxPrivateBackupsEvidenceBytes); err != nil {
		return PrivateBackupsEvidence{}, err
	}
	return evidence, nil
}

// ValidatePrivateBackupsEvidence proves that every exact legacy payload still
// matches the bounded evidence file and that no payload is absent or omitted.
func ValidatePrivateBackupsEvidence(ctx context.Context, custodyRoot, custodyPath, evidencePath string) error {
	want, _, _, err := readPrivateBackupsEvidenceWithSHA256(evidencePath)
	if err != nil {
		return err
	}
	custodyPath, err = normalizePrivateBackupsCustodyPath(custodyPath)
	if err != nil {
		return err
	}
	if want.CustodyPath != custodyPath {
		return fmt.Errorf("private backups evidence custody path mismatch: evidence=%q manifest=%q", want.CustodyPath, custodyPath)
	}
	got, err := buildPrivateBackupsEvidence(ctx, custodyRoot, custodyPath)
	if err != nil {
		return err
	}
	if got.FileCount != want.FileCount || got.TotalBytes != want.TotalBytes || len(got.Entries) != len(want.Entries) {
		return fmt.Errorf("private backups evidence summary does not match custody")
	}
	for index := range want.Entries {
		if got.Entries[index] != want.Entries[index] {
			return fmt.Errorf("private backups payload %q does not match durable evidence", want.Entries[index].RelativePath)
		}
	}
	return nil
}

func buildPrivateBackupsEvidence(ctx context.Context, custodyRoot, custodyPath string) (PrivateBackupsEvidence, error) {
	if err := ctx.Err(); err != nil {
		return PrivateBackupsEvidence{}, err
	}
	custodyRoot = filepath.Clean(strings.TrimSpace(custodyRoot))
	if custodyRoot == "." || custodyRoot == "" || !filepath.IsAbs(custodyRoot) {
		return PrivateBackupsEvidence{}, fmt.Errorf("private backups custody root must be absolute")
	}
	var err error
	custodyPath, err = normalizePrivateBackupsCustodyPath(custodyPath)
	if err != nil {
		return PrivateBackupsEvidence{}, err
	}
	if err := ValidateLegacyPrivateBackupsCustody(custodyRoot); err != nil {
		return PrivateBackupsEvidence{}, err
	}
	evidence := PrivateBackupsEvidence{
		Schema:      privateBackupsEvidenceSchema,
		CustodyPath: custodyPath,
		Entries:     []PrivateBackupsEvidenceEntry{},
	}
	nodes, err := os.ReadDir(custodyRoot)
	if err != nil {
		return PrivateBackupsEvidence{}, err
	}
	for _, node := range nodes {
		operations, err := os.ReadDir(filepath.Join(custodyRoot, node.Name()))
		if err != nil {
			return PrivateBackupsEvidence{}, err
		}
		for _, operation := range operations {
			if err := ctx.Err(); err != nil {
				return PrivateBackupsEvidence{}, err
			}
			relative := filepath.ToSlash(filepath.Join(node.Name(), operation.Name(), "payload.tar"))
			relative, err = normalizePrivateBackupsEvidencePath(relative)
			if err != nil {
				return PrivateBackupsEvidence{}, err
			}
			hash, size, err := hashPrivateBackupsPayload(ctx, filepath.Join(custodyRoot, filepath.FromSlash(relative)))
			if err != nil {
				return PrivateBackupsEvidence{}, err
			}
			if evidence.TotalBytes > int64(^uint64(0)>>1)-size {
				return PrivateBackupsEvidence{}, fmt.Errorf("private backups evidence total bytes overflow")
			}
			evidence.Entries = append(evidence.Entries, PrivateBackupsEvidenceEntry{
				RelativePath: relative,
				SizeBytes:    size,
				SHA256:       hash,
			})
			evidence.TotalBytes += size
			if len(evidence.Entries) > maxPrivateBackupsEvidenceEntries {
				return PrivateBackupsEvidence{}, fmt.Errorf("private backups evidence exceeds %d entries", maxPrivateBackupsEvidenceEntries)
			}
		}
	}
	sort.Slice(evidence.Entries, func(i, j int) bool {
		return evidence.Entries[i].RelativePath < evidence.Entries[j].RelativePath
	})
	evidence.FileCount = int64(len(evidence.Entries))
	return evidence, nil
}

func readPrivateBackupsEvidenceWithSHA256(evidencePath string) (PrivateBackupsEvidence, string, int64, error) {
	evidencePath = filepath.Clean(strings.TrimSpace(evidencePath))
	if evidencePath == "." || evidencePath == "" || !filepath.IsAbs(evidencePath) {
		return PrivateBackupsEvidence{}, "", 0, fmt.Errorf("private backups evidence path must be absolute")
	}
	before, err := os.Lstat(evidencePath)
	if err != nil {
		return PrivateBackupsEvidence{}, "", 0, err
	}
	if !before.Mode().IsRegular() || before.Size() > maxPrivateBackupsEvidenceBytes {
		return PrivateBackupsEvidence{}, "", 0, fmt.Errorf("private backups evidence must be a bounded no-follow regular file")
	}
	fd, err := unix.Open(evidencePath, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return PrivateBackupsEvidence{}, "", 0, err
	}
	file := os.NewFile(uintptr(fd), evidencePath)
	if file == nil {
		_ = unix.Close(fd)
		return PrivateBackupsEvidence{}, "", 0, fmt.Errorf("open private backups evidence")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return PrivateBackupsEvidence{}, "", 0, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) || opened.Size() > maxPrivateBackupsEvidenceBytes {
		return PrivateBackupsEvidence{}, "", 0, fmt.Errorf("private backups evidence changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxPrivateBackupsEvidenceBytes+1))
	if err != nil {
		return PrivateBackupsEvidence{}, "", 0, err
	}
	after, err := file.Stat()
	if err != nil {
		return PrivateBackupsEvidence{}, "", 0, err
	}
	pathAfter, err := os.Lstat(evidencePath)
	if err != nil {
		return PrivateBackupsEvidence{}, "", 0, err
	}
	if int64(len(raw)) > maxPrivateBackupsEvidenceBytes || int64(len(raw)) != opened.Size() || !os.SameFile(opened, after) || !os.SameFile(after, pathAfter) ||
		after.Size() != opened.Size() || after.Mode() != opened.Mode() || !after.ModTime().Equal(opened.ModTime()) ||
		pathAfter.Mode() != after.Mode() || !pathAfter.ModTime().Equal(after.ModTime()) {
		return PrivateBackupsEvidence{}, "", 0, fmt.Errorf("private backups evidence changed size or exceeds %d bytes", maxPrivateBackupsEvidenceBytes)
	}
	var evidence PrivateBackupsEvidence
	if err := json.Unmarshal(raw, &evidence); err != nil {
		return PrivateBackupsEvidence{}, "", 0, fmt.Errorf("decode private backups evidence: %w", err)
	}
	if err := validatePrivateBackupsEvidenceDocument(evidence); err != nil {
		return PrivateBackupsEvidence{}, "", 0, err
	}
	digest := sha256.Sum256(raw)
	return evidence, hex.EncodeToString(digest[:]), int64(len(raw)), nil
}

func validatePrivateBackupsEvidenceDocument(evidence PrivateBackupsEvidence) error {
	if evidence.Schema != privateBackupsEvidenceSchema {
		return fmt.Errorf("unsupported private backups evidence schema %q", evidence.Schema)
	}
	custodyPath, err := normalizePrivateBackupsCustodyPath(evidence.CustodyPath)
	if err != nil || custodyPath != evidence.CustodyPath {
		return fmt.Errorf("private backups evidence has invalid custody path")
	}
	if len(evidence.Entries) > maxPrivateBackupsEvidenceEntries || evidence.FileCount != int64(len(evidence.Entries)) || evidence.TotalBytes < 0 {
		return fmt.Errorf("private backups evidence summary is invalid")
	}
	var total int64
	previous := ""
	for _, entry := range evidence.Entries {
		relative, err := normalizePrivateBackupsEvidencePath(entry.RelativePath)
		if err != nil || relative != entry.RelativePath {
			return fmt.Errorf("private backups evidence path %q is invalid", entry.RelativePath)
		}
		if previous != "" && relative <= previous {
			return fmt.Errorf("private backups evidence entries must be unique and sorted")
		}
		if entry.SizeBytes <= 0 || len(entry.SHA256) != sha256.Size*2 || strings.ToLower(entry.SHA256) != entry.SHA256 {
			return fmt.Errorf("private backups evidence entry %q is incomplete", relative)
		}
		if _, err := hex.DecodeString(entry.SHA256); err != nil {
			return fmt.Errorf("private backups evidence entry %q has invalid sha256", relative)
		}
		if total > int64(^uint64(0)>>1)-entry.SizeBytes {
			return fmt.Errorf("private backups evidence total bytes overflow")
		}
		total += entry.SizeBytes
		previous = relative
	}
	if total != evidence.TotalBytes {
		return fmt.Errorf("private backups evidence total bytes do not match entries")
	}
	return nil
}

func normalizePrivateBackupsCustodyPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || filepath.IsAbs(value) || strings.ContainsRune(value, '\x00') {
		return "", fmt.Errorf("private backups custody path must be backup-relative")
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != value {
		return "", fmt.Errorf("private backups custody path %q is invalid", value)
	}
	return clean, nil
}

func normalizePrivateBackupsEvidencePath(value string) (string, error) {
	if value == "" || len(value) > maxPrivateBackupsEvidencePath || filepath.IsAbs(value) || strings.ContainsRune(value, '\x00') || strings.Contains(value, "\\") {
		return "", fmt.Errorf("private backups evidence path %q is invalid", value)
	}
	clean := path.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != value {
		return "", fmt.Errorf("private backups evidence path %q is invalid", value)
	}
	parts := strings.Split(clean, "/")
	if len(parts) != 3 || !strings.HasPrefix(parts[0], "node_") || strings.TrimPrefix(parts[0], "node_") == "" || !strings.HasPrefix(parts[1], "private_backup_") || strings.TrimPrefix(parts[1], "private_backup_") == "" || parts[2] != "payload.tar" {
		return "", fmt.Errorf("private backups evidence path %q is outside the legacy custody shape", value)
	}
	return clean, nil
}

func hashPrivateBackupsPayload(ctx context.Context, pathValue string) (string, int64, error) {
	before, err := os.Lstat(pathValue)
	if err != nil {
		return "", 0, err
	}
	if !before.Mode().IsRegular() {
		return "", 0, fmt.Errorf("private backups payload %q must be a no-follow regular file", pathValue)
	}
	fd, err := unix.Open(pathValue, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", 0, err
	}
	file := os.NewFile(uintptr(fd), pathValue)
	if file == nil {
		_ = unix.Close(fd)
		return "", 0, fmt.Errorf("open private backups payload %q", pathValue)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return "", 0, fmt.Errorf("private backups payload %q changed while opening", pathValue)
	}
	hasher := sha256.New()
	buffer := make([]byte, 1024*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			if _, err := hasher.Write(buffer[:count]); err != nil {
				return "", 0, err
			}
			total += int64(count)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", 0, readErr
		}
	}
	after, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	pathAfter, err := os.Lstat(pathValue)
	if err != nil {
		return "", 0, err
	}
	if !pathAfter.Mode().IsRegular() || !os.SameFile(opened, after) || !os.SameFile(after, pathAfter) || total != opened.Size() || after.Size() != opened.Size() || after.Mode() != opened.Mode() || !after.ModTime().Equal(opened.ModTime()) {
		return "", 0, fmt.Errorf("private backups payload %q changed while hashing", pathValue)
	}
	return hex.EncodeToString(hasher.Sum(nil)), total, nil
}

func writePrivateBackupsEvidenceAtomic(pathValue string, evidence PrivateBackupsEvidence, maxBytes int64) error {
	pathValue = filepath.Clean(strings.TrimSpace(pathValue))
	if pathValue == "." || pathValue == "" || !filepath.IsAbs(pathValue) {
		return fmt.Errorf("private backups evidence path must be absolute")
	}
	if maxBytes <= 0 {
		return fmt.Errorf("private backups evidence byte limit must be positive")
	}
	if _, err := os.Lstat(pathValue); err == nil {
		return fmt.Errorf("private backups evidence destination already exists: %s", pathValue)
	} else if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(pathValue)
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	if !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("private backups evidence parent must be a real directory")
	}
	file, err := os.CreateTemp(parent, ".private-backups-evidence-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(0o640); err != nil {
		_ = file.Close()
		return err
	}
	bounded := &privateBackupsEvidenceLimitWriter{destination: file, remaining: maxBytes, limit: maxBytes}
	encoder := json.NewEncoder(bounded)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(evidence); err != nil {
		_ = file.Close()
		return fmt.Errorf("write private backups evidence: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, pathValue); err != nil {
		return err
	}
	directory, err := os.Open(parent)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

type privateBackupsEvidenceLimitWriter struct {
	destination io.Writer
	remaining   int64
	limit       int64
}

func (w *privateBackupsEvidenceLimitWriter) Write(payload []byte) (int, error) {
	if int64(len(payload)) > w.remaining {
		return 0, fmt.Errorf("private backups evidence exceeds %d-byte symmetric limit", w.limit)
	}
	written, err := w.destination.Write(payload)
	w.remaining -= int64(written)
	return written, err
}

func requireRealDirectoryEntry(parent string, entry os.DirEntry) error {
	path := filepath.Join(parent, entry.Name())
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("legacy private backup custody component %q must be a real directory", path)
	}
	return nil
}

func validateWatchedRootBackupManifest(root, node, protectedRoot, batch, batchRelative string, manifest watchedRootBackupCustodyManifest) error {
	if manifest.SourceNodeID == "" || manifest.SourceNodeKey != node || manifest.RootKey != protectedRoot || manifest.LocalBatchID != batch || manifest.MainBatchID == "" {
		return fmt.Errorf("watched-root backup manifest identity does not match custody path %s", batchRelative)
	}
	if manifest.BackupMode == "" || manifest.ItemCount <= 0 || manifest.ItemCount != len(manifest.Items) {
		return fmt.Errorf("watched-root backup manifest for %s has inconsistent batch evidence", batchRelative)
	}
	if err := validateWatchedRootBackupBatchKind(manifest); err != nil {
		return fmt.Errorf("watched-root backup manifest for %s: %w", batchRelative, err)
	}
	switch manifest.Status {
	case watchedroots.BackupBatchStatusAccepted, watchedroots.BackupBatchStatusPartial, watchedroots.BackupBatchStatusFailed:
	default:
		return fmt.Errorf("watched-root backup manifest for %s has invalid status %q", batchRelative, manifest.Status)
	}
	seenItems := map[string]struct{}{}
	failedItems := 0
	skippedItems := 0
	for index, item := range manifest.Items {
		if item.WatchedRootBackupItemID == "" || item.WatchedRootBackupBatchID != manifest.MainBatchID || item.NodeID != manifest.SourceNodeID || item.RootKey != manifest.RootKey {
			return fmt.Errorf("watched-root backup item %d identity does not match its batch", index)
		}
		if _, duplicate := seenItems[item.WatchedRootBackupItemID]; duplicate {
			return fmt.Errorf("watched-root backup item %s is duplicated", item.WatchedRootBackupItemID)
		}
		seenItems[item.WatchedRootBackupItemID] = struct{}{}
		if strings.TrimSpace(item.LocalItemRef) == "" || item.BackupMode != manifest.BackupMode {
			return fmt.Errorf("watched-root backup item %s has inconsistent local or backup-mode evidence", item.WatchedRootBackupItemID)
		}
		relativePath, err := confinedCustodyRelativePath(item.RelativePath)
		if err != nil {
			return fmt.Errorf("watched-root backup item %s: %w", item.WatchedRootBackupItemID, err)
		}
		switch item.Status {
		case watchedroots.BackupItemStatusAccepted, watchedroots.BackupItemStatusDuplicate:
		case watchedroots.BackupItemStatusSkipped:
			skippedItems++
			continue
		case watchedroots.BackupItemStatusFailed:
			failedItems++
			continue
		default:
			return fmt.Errorf("watched-root backup item %s has invalid status %q", item.WatchedRootBackupItemID, item.Status)
		}
		switch item.ItemKind {
		case watchedroots.BackupItemKindDirectory, watchedroots.BackupItemKindMetadata, watchedroots.BackupItemKindDeletionMarker:
			if err := validateWatchedRootMetadataOnlyItem(manifest, item); err != nil {
				return fmt.Errorf("watched-root backup item %s: %w", item.WatchedRootBackupItemID, err)
			}
			continue
		case watchedroots.BackupItemKindFile:
		default:
			return fmt.Errorf("watched-root backup item %s has invalid kind %q", item.WatchedRootBackupItemID, item.ItemKind)
		}
		switch item.ArtifactKind {
		case watchedroots.BackupArtifactKindFileTransfer:
			if item.ArtifactRef == "" {
				return fmt.Errorf("watched-root backup item %s has no file transfer identity", item.WatchedRootBackupItemID)
			}
			if err := verifyCustodyPayload(root, filepath.Join(batchRelative, "payload", relativePath), item.SizeBytes, item.ContentHashURI); err != nil {
				return fmt.Errorf("watched-root backup item %s: %w", item.WatchedRootBackupItemID, err)
			}
		case watchedroots.BackupArtifactKindPrivateBackupOperation:
			if item.PrivateBackupOperationID == nil || strings.TrimSpace(*item.PrivateBackupOperationID) == "" {
				return fmt.Errorf("watched-root backup item %s has no private backup operation", item.WatchedRootBackupItemID)
			}
			operationID := strings.TrimSpace(*item.PrivateBackupOperationID)
			if item.ArtifactRef != operationID || strings.TrimSpace(item.LocalItemRef) == "" {
				return fmt.Errorf("watched-root backup item %s has inconsistent private artifact identity", item.WatchedRootBackupItemID)
			}
			operationRelative := filepath.Join(batchRelative, "artifacts", operationID)
			payload, err := readCustodyFile(root, filepath.Join(operationRelative, "manifest.json"), maxCustodyManifestBytes)
			if err != nil {
				return err
			}
			var privateManifest privateBackupCustodyManifest
			if err := json.Unmarshal(payload, &privateManifest); err != nil {
				return fmt.Errorf("decode private backup artifact manifest: %w", err)
			}
			if err := validatePrivateBackupManifest(root, node, protectedRoot, batch, operationRelative, privateManifest, operationID); err != nil {
				return err
			}
			if err := verifyWatchedRootPrivateArtifact(root, filepath.Join(operationRelative, privateManifest.PayloadPath), manifest, item); err != nil {
				return fmt.Errorf("watched-root backup item %s: %w", item.WatchedRootBackupItemID, err)
			}
		default:
			return fmt.Errorf("watched-root backup item %s has unsupported artifact kind %q", item.WatchedRootBackupItemID, item.ArtifactKind)
		}
	}
	expectedStatus := watchedroots.BackupBatchStatusAccepted
	if failedItems == len(manifest.Items) {
		expectedStatus = watchedroots.BackupBatchStatusFailed
	} else if failedItems > 0 || skippedItems > 0 {
		expectedStatus = watchedroots.BackupBatchStatusPartial
	}
	if manifest.Status != expectedStatus {
		return fmt.Errorf("watched-root backup manifest for %s status %q does not match item evidence %q", batchRelative, manifest.Status, expectedStatus)
	}
	return nil
}

// validateWatchedRootBackupBatchKind keeps immutable custody written by the
// pre-fix node agent readable without treating arbitrary batch kinds as valid.
// That producer emitted exactly one item per batch and copied the item kind
// into batch_kind. New admission accepts only BackupBatchKindWatchedRoot.
func validateWatchedRootBackupBatchKind(manifest watchedRootBackupCustodyManifest) error {
	if manifest.BatchKind == watchedroots.BackupBatchKindWatchedRoot {
		return nil
	}
	if len(manifest.Items) != 1 || manifest.BatchKind != manifest.Items[0].ItemKind {
		return fmt.Errorf("batch_kind %q is neither canonical nor coherent historical one-item evidence", manifest.BatchKind)
	}
	switch manifest.BatchKind {
	case watchedroots.BackupItemKindFile,
		watchedroots.BackupItemKindDirectory,
		watchedroots.BackupItemKindMetadata,
		watchedroots.BackupItemKindDeletionMarker:
		return nil
	default:
		return fmt.Errorf("batch_kind %q is unsupported", manifest.BatchKind)
	}
}

func validateWatchedRootMetadataOnlyItem(manifest watchedRootBackupCustodyManifest, item watchedroots.BackupItem) error {
	if item.ArtifactKind != "" || item.ArtifactRef != "" || item.PrivateBackupOperationID != nil {
		return fmt.Errorf("metadata-only item has unexpected payload artifact evidence")
	}
	if item.SizeBytes < 0 || !validSHA256URI(item.ContentHashURI) {
		return fmt.Errorf("metadata-only item has invalid size or content-hash evidence")
	}
	if item.PreviousHashURI != "" && !validSHA256URI(item.PreviousHashURI) {
		return fmt.Errorf("metadata-only item has invalid previous-hash evidence")
	}
	var metadata struct {
		Source                     string                      `json:"source"`
		LocalBatchID               string                      `json:"local_batch_id"`
		LocalItemID                string                      `json:"local_item_id"`
		LocalArtifactID            string                      `json:"local_artifact_id"`
		FileTransferID             string                      `json:"file_transfer_id"`
		FileTransferStorageEntryID string                      `json:"file_transfer_storage_entry_id"`
		FileTransferAcceptedPath   string                      `json:"file_transfer_accepted_path"`
		FilesystemObservation      *filesystemmeta.Observation `json:"filesystem_observation"`
	}
	if err := json.Unmarshal(item.Metadata, &metadata); err != nil {
		return fmt.Errorf("decode metadata-only evidence: %w", err)
	}
	if metadata.Source != "loom-node-agent" || metadata.LocalBatchID != manifest.LocalBatchID || metadata.LocalItemID != item.LocalItemRef {
		return fmt.Errorf("metadata-only item identity does not match its local batch")
	}
	if metadata.LocalArtifactID != "" || metadata.FileTransferID != "" || metadata.FileTransferStorageEntryID != "" || metadata.FileTransferAcceptedPath != "" {
		return fmt.Errorf("metadata-only item has unexpected transport evidence")
	}
	observation := metadata.FilesystemObservation
	if observation == nil || !filesystemmeta.ValidObjectKind(observation.Kind) || observation.LogicalSizeBytes != item.SizeBytes {
		return fmt.Errorf("metadata-only item has inconsistent filesystem observation")
	}
	switch item.ItemKind {
	case watchedroots.BackupItemKindDirectory:
		if item.ModifiedAt == nil || item.DeletedAt != nil || observation.Kind != filesystemmeta.ObjectKindDirectory {
			return fmt.Errorf("directory item has inconsistent lifecycle evidence")
		}
	case watchedroots.BackupItemKindMetadata:
		if item.ModifiedAt == nil || item.DeletedAt != nil || observation.Kind != filesystemmeta.ObjectKindRegularFile {
			return fmt.Errorf("metadata item has inconsistent lifecycle evidence")
		}
	case watchedroots.BackupItemKindDeletionMarker:
		if item.DeletedAt == nil || item.PreviousHashURI == "" {
			return fmt.Errorf("deletion marker has inconsistent lifecycle evidence")
		}
	}
	return nil
}

func validSHA256URI(value string) bool {
	algorithm, checksum, found := strings.Cut(strings.TrimSpace(value), ":")
	if !found || algorithm != "sha256" || len(checksum) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(checksum)
	return err == nil
}

func verifyWatchedRootPrivateArtifact(root, payloadRelative string, batch watchedRootBackupCustodyManifest, item watchedroots.BackupItem) error {
	payloadRelative, err := confinedCustodyRelativePath(payloadRelative)
	if err != nil {
		return err
	}
	confined, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer confined.Close()
	if err := requireRealCustodyPath(confined, payloadRelative, true); err != nil {
		return err
	}
	file, err := confined.Open(payloadRelative)
	if err != nil {
		return err
	}
	defer file.Close()

	var artifactManifest struct {
		SchemaVersion  string `json:"schema_version"`
		RootKey        string `json:"root_key"`
		RelativePath   string `json:"relative_path"`
		ContentHashURI string `json:"content_hash_uri"`
		SizeBytes      int64  `json:"size_bytes"`
		BackupMode     string `json:"backup_mode"`
		LocalBatchID   string `json:"local_batch_id"`
		LocalItemID    string `json:"local_item_id"`
	}
	foundManifest, foundContent := false, false
	reader := tar.NewReader(file)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read private backup artifact: %w", err)
		}
		if header == nil || header.Typeflag != tar.TypeReg || (header.Name != "manifest.json" && header.Name != "content") {
			return fmt.Errorf("private backup artifact contains an unexpected member")
		}
		switch header.Name {
		case "manifest.json":
			if foundManifest || header.Size < 0 || header.Size > 1<<20 {
				return fmt.Errorf("private backup artifact manifest is duplicated or oversized")
			}
			payload, err := io.ReadAll(io.LimitReader(reader, header.Size+1))
			if err != nil || int64(len(payload)) != header.Size || json.Unmarshal(payload, &artifactManifest) != nil {
				return fmt.Errorf("private backup artifact manifest is invalid")
			}
			foundManifest = true
		case "content":
			if foundContent || header.Size != item.SizeBytes {
				return fmt.Errorf("private backup artifact content size does not match backup item")
			}
			hash := sha256.New()
			written, err := io.Copy(hash, reader)
			if err != nil || written != header.Size {
				return fmt.Errorf("private backup artifact content is truncated")
			}
			algorithm, checksum, found := strings.Cut(strings.TrimSpace(item.ContentHashURI), ":")
			if !found || algorithm != "sha256" || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), checksum) {
				return fmt.Errorf("private backup artifact content checksum does not match backup item")
			}
			foundContent = true
		}
	}
	if !foundManifest || !foundContent || artifactManifest.SchemaVersion != "watched_root.backup_artifact.v0.2" ||
		artifactManifest.RootKey != batch.RootKey || artifactManifest.RelativePath != item.RelativePath ||
		artifactManifest.ContentHashURI != item.ContentHashURI || artifactManifest.SizeBytes != item.SizeBytes ||
		artifactManifest.BackupMode != item.BackupMode || artifactManifest.LocalBatchID != batch.LocalBatchID || artifactManifest.LocalItemID != item.LocalItemRef {
		return fmt.Errorf("private backup artifact manifest does not match backup item")
	}
	return nil
}

func validatePrivateBackupManifest(root, node, protectedRoot, batch, operationRelative string, manifest privateBackupCustodyManifest, expectedOperationID string) error {
	if manifest.SchemaVersion != "storage.private_backup_manifest.v0.7" || manifest.PrivateBackupOperationID == "" || manifest.SourceNodeID == "" ||
		manifest.SourceNodeKey != node || manifest.RootKey != protectedRoot || manifest.BatchKey != batch || manifest.PayloadPath != "payload.tar" {
		return fmt.Errorf("private backup manifest identity does not match custody path %s", operationRelative)
	}
	if expectedOperationID != "" && manifest.PrivateBackupOperationID != expectedOperationID {
		return fmt.Errorf("private backup operation identity does not match watched-root item")
	}
	if manifest.CoarseSizeBytes < 0 || len(manifest.PayloadSHA256) != sha256.Size*2 {
		return fmt.Errorf("private backup manifest %s has invalid payload evidence", manifest.PrivateBackupOperationID)
	}
	return verifyCustodyPayload(root, filepath.Join(operationRelative, manifest.PayloadPath), manifest.CoarseSizeBytes, "sha256:"+manifest.PayloadSHA256)
}

// ValidateStorageArchiveCustody validates portable v0.7 archive-key evidence.
// A legacy archive is accepted only as the explicit global objects/manifests
// pair; malformed v0.7 keys never fall through to that compatibility path.
func ValidateStorageArchiveCustody(root string) error {
	root = filepath.Clean(strings.TrimSpace(root))
	if err := requireRealCustodyDirectory(root); err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	legacyObjects, legacyManifests := false, false
	for _, entry := range entries {
		if entry.Name() == "objects" && entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			legacyObjects = true
		}
		if entry.Name() == "manifests" && entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			legacyManifests = true
		}
	}
	if legacyObjects != legacyManifests {
		return fmt.Errorf("legacy storage archive snapshot must contain both objects and manifests")
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if legacyObjects && (entry.Name() == "objects" || entry.Name() == "manifests") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			return fmt.Errorf("storage archive entry %q is not a real archive-key directory", entry.Name())
		}
		manifestPayload, err := readCustodyFile(root, filepath.Join(entry.Name(), "manifest.json"), maxCustodyManifestBytes)
		if err != nil {
			return fmt.Errorf("storage archive key %q manifest: %w", entry.Name(), err)
		}
		var manifest storagearchive.ManifestDocument
		if err := json.Unmarshal(manifestPayload, &manifest); err != nil {
			return fmt.Errorf("storage archive key %q manifest is invalid JSON: %w", entry.Name(), err)
		}
		if manifest.SchemaVersion != storagearchive.ManifestSchemaVersion || manifest.ArchiveKey != entry.Name() || !manifest.Complete ||
			manifest.ExpectedEntryCount <= 0 || manifest.ArchivedEntryCount != manifest.ExpectedEntryCount || len(manifest.Entries) != manifest.ExpectedEntryCount ||
			manifest.SourceRef == "" || manifest.OwnerNodeKey == "" || manifest.CreatedAt.IsZero() {
			return fmt.Errorf("storage archive key %q has invalid v0.7 manifest evidence", entry.Name())
		}
		if manifest.CatalogRecovery != "" && manifest.CatalogRecovery != storagearchive.CatalogRecoverySchemaVersion {
			return fmt.Errorf("storage archive key %q has an unsupported catalog recovery schema", entry.Name())
		}
		targetLogical := strings.TrimPrefix(strings.TrimSpace(manifest.TargetPath), "main/Archive/")
		normalizedTarget, err := storagecatalog.NormalizeLogicalPath(targetLogical)
		if err != nil || targetLogical != normalizedTarget || !strings.HasPrefix(manifest.TargetPath, "main/Archive/") {
			return fmt.Errorf("storage archive key %q has an invalid target path", entry.Name())
		}
		if err := ids.Validate(ids.StorageArchiveManifestPrefix, manifest.ArchiveManifestID); err != nil {
			return fmt.Errorf("storage archive key %q has invalid manifest identity: %w", entry.Name(), err)
		}
		seenEntries := map[string]struct{}{}
		seenRefs := map[string]struct{}{}
		seenSources := map[string]struct{}{}
		for index, item := range manifest.Entries {
			if err := ids.Validate(ids.StorageEntryPrefix, item.SourceStorageEntryID); err != nil {
				return fmt.Errorf("storage archive item %d source identity: %w", index, err)
			}
			if err := ids.Validate(ids.StorageEntryPrefix, item.ArchiveStorageEntryID); err != nil {
				return fmt.Errorf("storage archive item %d archive identity: %w", index, err)
			}
			if err := ids.Validate(ids.StoragePhysicalRefPrefix, item.ArchivePhysicalRefID); err != nil {
				return fmt.Errorf("storage archive item %d ref identity: %w", index, err)
			}
			if _, duplicate := seenEntries[item.ArchiveStorageEntryID]; duplicate {
				return fmt.Errorf("storage archive manifest repeats entry %s", item.ArchiveStorageEntryID)
			}
			if _, duplicate := seenRefs[item.ArchivePhysicalRefID]; duplicate {
				return fmt.Errorf("storage archive manifest repeats ref %s", item.ArchivePhysicalRefID)
			}
			seenEntries[item.ArchiveStorageEntryID] = struct{}{}
			seenRefs[item.ArchivePhysicalRefID] = struct{}{}
			if _, duplicate := seenSources[item.SourceStorageEntryID]; duplicate {
				return fmt.Errorf("storage archive manifest repeats source entry %s", item.SourceStorageEntryID)
			}
			seenSources[item.SourceStorageEntryID] = struct{}{}
			if manifest.CatalogRecovery == storagearchive.CatalogRecoverySchemaVersion &&
				(strings.TrimSpace(item.SourceViewPath) == "" || strings.TrimSpace(item.SourceLogicalPath) == "" ||
					strings.TrimSpace(item.SourceOriginalPath) == "" || !storagecatalog.ValidPhysicalRefKind(item.SourceRefKind) ||
					strings.TrimSpace(item.SourceRefURI) == "" || !storagecatalog.ValidAvailabilityState(item.SourceAvailabilityState)) {
				return fmt.Errorf("storage archive item %d has incomplete catalog recovery evidence", index)
			}
			normalizedLogical, err := storagecatalog.NormalizeLogicalPath(item.ArchiveLogicalPath)
			if err != nil || normalizedLogical != item.ArchiveLogicalPath ||
				(normalizedLogical != normalizedTarget && !strings.HasPrefix(normalizedLogical, normalizedTarget+"/")) ||
				item.ArchiveViewPath != path.Join("main", "Archive", normalizedLogical) {
				return fmt.Errorf("storage archive item %d has inconsistent logical custody paths", index)
			}
			objectPath, err := confinedArchiveCustodyPath(item.ArchiveObjectPath)
			if err != nil {
				return fmt.Errorf("storage archive item %d: %w", index, err)
			}
			if item.SizeBytes == nil || *item.SizeBytes < 0 || item.ChecksumAlgorithm != "sha256" || len(item.ChecksumHex) != sha256.Size*2 {
				return fmt.Errorf("storage archive item %d has incomplete payload evidence", index)
			}
			expectedObjectPath := filepath.ToSlash(filepath.Join("objects", "sha256", item.ChecksumHex[:2], item.ChecksumHex))
			if item.ContentKey != "sha256:"+item.ChecksumHex || filepath.ToSlash(objectPath) != expectedObjectPath {
				return fmt.Errorf("storage archive item %d object identity does not match its checksum", index)
			}
			if err := verifyCustodyPayload(root, filepath.Join(entry.Name(), objectPath), *item.SizeBytes, "sha256:"+item.ChecksumHex); err != nil {
				return fmt.Errorf("storage archive item %d: %w", index, err)
			}
		}
	}
	return nil
}

func requireRealCustodyDirectory(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("custody root %q must be a real directory", root)
	}
	return nil
}

func custodyDirectories(root, relative string) ([]string, error) {
	directory := root
	if relative != "." {
		directory = filepath.Join(root, relative)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			return nil, fmt.Errorf("custody component %q is not a real directory", filepath.Join(relative, entry.Name()))
		}
		result = append(result, entry.Name())
	}
	return result, nil
}

func readCustodyFile(root, relative string, maxBytes int64) ([]byte, error) {
	relative, err := confinedCustodyRelativePath(relative)
	if err != nil {
		return nil, err
	}
	confined, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer confined.Close()
	if err := requireRealCustodyPath(confined, relative, true); err != nil {
		return nil, err
	}
	file, err := confined.Open(relative)
	if err != nil {
		return nil, err
	}
	payload, readErr := io.ReadAll(io.LimitReader(file, maxBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if int64(len(payload)) > maxBytes {
		return nil, fmt.Errorf("custody file %q exceeds %d bytes", relative, maxBytes)
	}
	return payload, nil
}

func verifyCustodyPayload(root, relative string, expectedSize int64, contentHashURI string) error {
	relative, err := confinedCustodyRelativePath(relative)
	if err != nil {
		return err
	}
	algorithm, checksum, found := strings.Cut(strings.TrimSpace(contentHashURI), ":")
	if !found || algorithm != "sha256" || len(checksum) != sha256.Size*2 {
		return fmt.Errorf("payload %q has invalid sha256 evidence", relative)
	}
	confined, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer confined.Close()
	if err := requireRealCustodyPath(confined, relative, true); err != nil {
		return err
	}
	info, err := confined.Lstat(relative)
	if err != nil {
		return err
	}
	if info.Size() != expectedSize {
		return fmt.Errorf("payload %q size = %d, want %d", relative, info.Size(), expectedSize)
	}
	file, err := confined.Open(relative)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, checksum) {
		return fmt.Errorf("payload %q checksum = sha256:%s, want sha256:%s", relative, actual, checksum)
	}
	return nil
}

func requireRealCustodyPath(root *os.Root, relative string, finalFile bool) error {
	components := strings.Split(filepath.ToSlash(relative), "/")
	current := ""
	for index, component := range components {
		current = filepath.Join(current, component)
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("custody path %q contains a symlink", current)
		}
		if index == len(components)-1 && finalFile {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("custody path %q is not a regular file", current)
			}
		} else if !info.IsDir() {
			return fmt.Errorf("custody path component %q is not a directory", current)
		}
	}
	return nil
}

func confinedCustodyRelativePath(value string) (string, error) {
	value = filepath.Clean(filepath.FromSlash(strings.TrimSpace(value)))
	if value == "." || value == "" || filepath.IsAbs(value) || value == ".." || strings.HasPrefix(value, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("custody path must be a confined relative path")
	}
	return value, nil
}

func confinedArchiveCustodyPath(value string) (string, error) {
	value, err := confinedCustodyRelativePath(value)
	if err != nil {
		return "", err
	}
	if value != "objects" && !strings.HasPrefix(value, "objects"+string(filepath.Separator)) {
		return "", fmt.Errorf("archive object path must be below objects")
	}
	return value, nil
}

func requireNonEmptyFile(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("backup file %q is missing: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("backup file %q is not a regular file", path)
	}
	if info.Size() <= 0 {
		return 0, fmt.Errorf("backup file %q is empty", path)
	}
	return info.Size(), nil
}

func requireRedactedServiceEnv(path string) error {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("read redacted service env snapshot: %w", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, value, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		if isSensitiveBackupMetadataKey(key) && strings.TrimSpace(value) != "[REDACTED]" {
			return fmt.Errorf("redacted service env snapshot contains unredacted sensitive key %q", strings.TrimSpace(key))
		}
	}
	return nil
}

func isSensitiveBackupMetadataKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, token := range []string{"db_url", "password", "token", "secret", "api_key", "apikey", "credential"} {
		if strings.Contains(key, token) {
			return true
		}
	}
	return false
}

func pathWithin(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}

func normalizeManifestSHA256(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "sha256:")
	return value
}
