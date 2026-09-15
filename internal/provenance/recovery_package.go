package provenance

import (
	"bytes"
	"context"
	"crypto/sha256"
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

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sys/unix"
)

const (
	RecoveryManifestSchema = "loom.backup.manifest.v0.10"
	RecoveryManifestFile   = "manifest.json"
	RecoveryDumpFile       = "loom_provenance.dump"
	RecoveryBackupKind     = "loom_provenance_recovery"
	RecoveryArtifactKind   = "provenance_postgres_dump"
	RecoveryDumpMaxBytes   = int64(4 << 30)
)

var recoveryPackageIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type RecoveryDump func(ctx context.Context, destination, database, exportedSnapshot string) error

// RecoveryPackageVerifier is required so a producer cannot report success from
// its own manifest bytes alone. The worker supplies the maintenance verifier,
// which binds the independently retained manifest identity.
type RecoveryPackageVerifier func(ctx context.Context, packageDir, expectedManifestSHA256 string) error

type RecoveryPackageInput struct {
	Pool              *pgxpool.Pool
	ExpectedDatabase  string
	PackagesRoot      string
	PackageID         string
	BackupOperationID string
	WorkerRunID       string
	NodeID            string
	CreatedAt         time.Time
	Dump              RecoveryDump
	Verify            RecoveryPackageVerifier
}

type RecoveryPackageResult struct {
	PackageDir        string           `json:"package_dir"`
	PackageID         string           `json:"package_id"`
	ManifestPath      string           `json:"manifest_path"`
	ManifestSHA256    string           `json:"manifest_sha256"`
	DumpSizeBytes     int64            `json:"dump_size_bytes"`
	Snapshot          RecoverySnapshot `json:"snapshot"`
	CreatedAt         time.Time        `json:"created_at"`
	BackupOperationID string           `json:"backup_operation_id,omitempty"`
	WorkerRunID       string           `json:"worker_run_id,omitempty"`
}

type recoveryManifest struct {
	Schema            string                     `json:"schema"`
	BackupKind        string                     `json:"backup_kind"`
	BackupOperationID string                     `json:"backup_operation_id,omitempty"`
	WorkerRunID       string                     `json:"worker_run_id,omitempty"`
	CreatedAt         string                     `json:"created_at"`
	Source            recoveryManifestSource     `json:"source"`
	Artifacts         []recoveryManifestArtifact `json:"artifacts"`
	Provenance        recoveryManifestContract   `json:"provenance"`
}

type recoveryManifestSource struct {
	NodeID   string `json:"node_id,omitempty"`
	NodeRole string `json:"node_role,omitempty"`
	IsVPS    bool   `json:"is_vps"`
}

type recoveryManifestDatabase struct {
	Name     string `json:"name"`
	DumpFile string `json:"dump_file"`
	Format   string `json:"format"`
}

type recoveryManifestArtifact struct {
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	FileCount *int64 `json:"file_count"`
	SizeBytes *int64 `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

type recoveryManifestContract struct {
	State             string                   `json:"state"`
	StartedAt         string                   `json:"started_at"`
	CompletedAt       string                   `json:"completed_at"`
	Database          recoveryManifestDatabase `json:"database"`
	SchemaHead        int                      `json:"schema_head"`
	RequiredRelations []string                 `json:"required_relations"`
	LogicalCounts     map[string]int64         `json:"logical_counts"`
	GraphDigest       string                   `json:"graph_digest"`
	DumpSizeBytes     int64                    `json:"dump_size_bytes"`
	DumpSHA256        string                   `json:"dump_sha256"`
}

// CreateRecoveryPackage binds package creation to this runtime's already
// verified isolated pool and database identity.
func (r *Runtime) CreateRecoveryPackage(ctx context.Context, input RecoveryPackageInput) (RecoveryPackageResult, error) {
	if r == nil || r.pool == nil {
		return RecoveryPackageResult{}, errors.New("provenance recovery runtime is not configured")
	}
	readiness := r.Readiness()
	if readiness.State != ReadinessReady || readiness.Database != DatabaseName {
		return RecoveryPackageResult{}, errors.New("provenance recovery runtime is not ready for the isolated database")
	}
	input.Pool = r.pool
	input.ExpectedDatabase = DatabaseName
	return CreateRecoveryPackage(ctx, input)
}

// CreateRecoveryPackage publishes one verified, bounded recovery package for
// the isolated provenance database. pg_dump must import exportedSnapshot so
// the custom-format dump and SnapshotLedger evidence describe the same
// repeatable-read transaction.
func CreateRecoveryPackage(ctx context.Context, input RecoveryPackageInput) (RecoveryPackageResult, error) {
	if input.Pool == nil {
		return RecoveryPackageResult{}, errors.New("provenance recovery package pool is required")
	}
	if input.Dump == nil {
		return RecoveryPackageResult{}, errors.New("provenance recovery package dump producer is required")
	}
	if input.Verify == nil {
		return RecoveryPackageResult{}, errors.New("provenance recovery package independent verifier is required")
	}
	root, err := exactRecoveryDirectory(input.PackagesRoot)
	if err != nil {
		return RecoveryPackageResult{}, err
	}
	packageID := strings.TrimSpace(input.PackageID)
	if !recoveryPackageIDRE.MatchString(packageID) {
		return RecoveryPackageResult{}, errors.New("provenance recovery package id is invalid")
	}
	finalDir := filepath.Join(root, packageID)
	if _, err := os.Lstat(finalDir); err == nil {
		return RecoveryPackageResult{}, fmt.Errorf("provenance recovery package %s already exists", packageID)
	} else if !os.IsNotExist(err) {
		return RecoveryPackageResult{}, err
	}

	stage, err := os.MkdirTemp(root, ".provenance-recovery-staging-")
	if err != nil {
		return RecoveryPackageResult{}, err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(stage)
		}
	}()
	if err := os.Chmod(stage, 0o700); err != nil {
		return RecoveryPackageResult{}, err
	}

	startedAt := input.CreatedAt.UTC()
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	tx, _, err := beginRecoverySnapshot(ctx, input.Pool, input.ExpectedDatabase)
	if err != nil {
		return RecoveryPackageResult{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var exportedSnapshot string
	if err := tx.QueryRow(ctx, `SELECT pg_export_snapshot()`).Scan(&exportedSnapshot); err != nil {
		return RecoveryPackageResult{}, fmt.Errorf("export provenance recovery snapshot: %w", err)
	}
	if strings.TrimSpace(exportedSnapshot) == "" {
		return RecoveryPackageResult{}, errors.New("export provenance recovery snapshot returned an empty identity")
	}
	dumpPath := filepath.Join(stage, RecoveryDumpFile)
	if err := input.Dump(ctx, dumpPath, strings.TrimSpace(input.ExpectedDatabase), exportedSnapshot); err != nil {
		return RecoveryPackageResult{}, fmt.Errorf("capture provenance custom-format dump: %w", err)
	}
	if err := sealRecoveryDump(dumpPath); err != nil {
		return RecoveryPackageResult{}, fmt.Errorf("seal provenance custom-format dump: %w", err)
	}
	dumpSHA, dumpSize, prefix, err := hashRecoveryFile(ctx, dumpPath, RecoveryDumpMaxBytes)
	if err != nil {
		return RecoveryPackageResult{}, fmt.Errorf("inspect provenance custom-format dump: %w", err)
	}
	if !bytes.HasPrefix(prefix, []byte("PGDMP")) {
		return RecoveryPackageResult{}, errors.New("provenance recovery dump is not PostgreSQL custom format")
	}
	snapshot, err := snapshotLedgerTx(ctx, tx)
	if err != nil {
		return RecoveryPackageResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RecoveryPackageResult{}, fmt.Errorf("commit provenance recovery snapshot: %w", err)
	}

	completedAt := time.Now().UTC()
	if completedAt.Before(startedAt) {
		completedAt = startedAt
	}
	one := int64(1)
	manifest := recoveryManifest{
		Schema: RecoveryManifestSchema, BackupKind: RecoveryBackupKind,
		BackupOperationID: strings.TrimSpace(input.BackupOperationID), WorkerRunID: strings.TrimSpace(input.WorkerRunID),
		CreatedAt: completedAt.Format(time.RFC3339Nano),
		Source:    recoveryManifestSource{NodeID: strings.TrimSpace(input.NodeID), NodeRole: "main", IsVPS: false},
		Artifacts: []recoveryManifestArtifact{{Kind: RecoveryArtifactKind, Path: RecoveryDumpFile, FileCount: &one, SizeBytes: &dumpSize, SHA256: dumpSHA}},
		Provenance: recoveryManifestContract{
			State: BackupStateComplete, StartedAt: startedAt.Format(time.RFC3339Nano), CompletedAt: completedAt.Format(time.RFC3339Nano),
			Database:   recoveryManifestDatabase{Name: DatabaseName, DumpFile: RecoveryDumpFile, Format: "pg_dump_custom"},
			SchemaHead: snapshot.SchemaHead, RequiredRelations: RecoveryRelations(), LogicalCounts: snapshot.RelationCounts,
			GraphDigest: snapshot.GraphDigest, DumpSizeBytes: dumpSize, DumpSHA256: dumpSHA,
		},
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return RecoveryPackageResult{}, err
	}
	raw = append(raw, '\n')
	manifestPath := filepath.Join(stage, RecoveryManifestFile)
	if err := writeRecoveryFile(manifestPath, raw); err != nil {
		return RecoveryPackageResult{}, err
	}
	manifestDigest := sha256.Sum256(raw)
	manifestSHA := hex.EncodeToString(manifestDigest[:])
	if err := input.Verify(ctx, stage, manifestSHA); err != nil {
		return RecoveryPackageResult{}, fmt.Errorf("verify staged provenance recovery package: %w", err)
	}
	if _, err := os.Lstat(finalDir); err == nil {
		return RecoveryPackageResult{}, fmt.Errorf("provenance recovery package %s appeared before publish", packageID)
	} else if !os.IsNotExist(err) {
		return RecoveryPackageResult{}, err
	}
	if err := os.Rename(stage, finalDir); err != nil {
		return RecoveryPackageResult{}, fmt.Errorf("publish provenance recovery package: %w", err)
	}
	published = true
	if err := input.Verify(ctx, finalDir, manifestSHA); err != nil {
		return RecoveryPackageResult{}, fmt.Errorf("verify published provenance recovery package: %w", err)
	}
	return RecoveryPackageResult{
		PackageDir: finalDir, PackageID: packageID, ManifestPath: filepath.Join(finalDir, RecoveryManifestFile), ManifestSHA256: manifestSHA,
		DumpSizeBytes: dumpSize, Snapshot: snapshot, CreatedAt: completedAt,
		BackupOperationID: strings.TrimSpace(input.BackupOperationID), WorkerRunID: strings.TrimSpace(input.WorkerRunID),
	}, nil
}

func exactRecoveryDirectory(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return "", errors.New("provenance recovery packages root must be an exact absolute path")
	}
	info, err := os.Lstat(value)
	if err != nil {
		return "", fmt.Errorf("inspect provenance recovery packages root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("provenance recovery packages root must be a real directory")
	}
	return value, nil
}

func hashRecoveryFile(ctx context.Context, value string, maxBytes int64) (string, int64, []byte, error) {
	return hashRecoveryFileWithHook(ctx, value, maxBytes, nil)
}

func sealRecoveryDump(value string) error {
	return sealRecoveryDumpWithHook(value, nil)
}

// sealRecoveryDumpWithHook holds the exact producer-created inode while
// normalizing its private mode. The hook exists only so focused tests can
// deterministically replace the named path after the no-follow open.
func sealRecoveryDumpWithHook(value string, afterOpen func() error) error {
	before, err := os.Lstat(value)
	if err != nil {
		return err
	}
	if !before.Mode().IsRegular() {
		return errors.New("recovery dump must be a no-follow regular file")
	}
	file, err := openRecoveryFileNoFollow(value, unix.O_RDWR)
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return errors.New("recovery dump changed while opening")
	}
	if afterOpen != nil {
		if err := afterOpen(); err != nil {
			return err
		}
	}
	if err := unix.Fchmod(int(file.Fd()), 0o600); err != nil {
		return fmt.Errorf("set private recovery dump mode: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync private recovery dump mode: %w", err)
	}
	sealed, err := file.Stat()
	if err != nil || !exactRecoveryRegularMode(sealed, 0o600) || !os.SameFile(opened, sealed) {
		return errors.New("recovery dump descriptor changed while sealing")
	}
	named, err := os.Lstat(value)
	if err != nil || !exactRecoveryRegularMode(named, 0o600) || !os.SameFile(sealed, named) {
		return errors.New("recovery dump path changed while sealing")
	}
	return nil

}

func hashRecoveryFileWithHook(ctx context.Context, value string, maxBytes int64, afterOpen func() error) (string, int64, []byte, error) {
	before, err := os.Lstat(value)
	if err != nil {
		return "", 0, nil, err
	}
	if !exactRecoveryRegularMode(before, 0o600) || before.Size() <= 0 || before.Size() > maxBytes {
		return "", 0, nil, errors.New("recovery artifact must be a bounded no-follow regular file at exact mode 0600")
	}
	file, err := openRecoveryFileNoFollow(value, unix.O_RDONLY)
	if err != nil {
		return "", 0, nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !exactRecoveryRegularMode(opened, 0o600) || !os.SameFile(before, opened) {
		return "", 0, nil, errors.New("recovery artifact changed while opening")
	}
	if afterOpen != nil {
		if err := afterOpen(); err != nil {
			return "", 0, nil, err
		}
	}
	hasher := sha256.New()
	prefix := make([]byte, 5)
	n, err := io.ReadFull(file, prefix)
	if err != nil {
		return "", 0, nil, err
	}
	prefix = prefix[:n]
	if _, err := hasher.Write(prefix); err != nil {
		return "", 0, nil, err
	}
	written, err := io.Copy(hasher, io.LimitReader(file, maxBytes+1-int64(n)))
	if err != nil {
		return "", 0, nil, err
	}
	size := written + int64(n)
	openedAfter, err := file.Stat()
	if err != nil || !exactRecoveryRegularMode(openedAfter, 0o600) || !os.SameFile(opened, openedAfter) || openedAfter.Size() != opened.Size() || !openedAfter.ModTime().Equal(opened.ModTime()) || size != opened.Size() {
		return "", 0, nil, errors.New("recovery artifact changed while hashing")
	}
	namedAfter, err := os.Lstat(value)
	if err != nil || !exactRecoveryRegularMode(namedAfter, 0o600) || !os.SameFile(openedAfter, namedAfter) || namedAfter.Size() != opened.Size() || !namedAfter.ModTime().Equal(opened.ModTime()) {
		return "", 0, nil, errors.New("recovery artifact changed while hashing")
	}
	return hex.EncodeToString(hasher.Sum(nil)), size, prefix, nil
}

func openRecoveryFileNoFollow(value string, access int) (*os.File, error) {
	fd, err := unix.Open(value, access|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), value)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open recovery artifact returned no file")
	}
	return file, nil
}

func exactRecoveryRegularMode(info os.FileInfo, want os.FileMode) bool {
	if info == nil || !info.Mode().IsRegular() || info.Mode().Perm() != want {
		return false
	}
	return info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0
}

func writeRecoveryFile(path string, raw []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
