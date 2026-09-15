package cloudstorage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/backupcoverage"
	"loom.local/loom/internal/backupstrategy"
	"loom.local/loom/internal/maintenance"
)

const (
	SnapshotStatusPlanned   = "planned"
	SnapshotStatusSucceeded = "succeeded"
	SnapshotStatusFailed    = "failed"

	SnapshotListStatusOK            = "ok"
	SnapshotListStatusEmpty         = "empty"
	SnapshotListStatusUninitialized = "uninitialized"

	SnapshotBackendUninitializedCode = "cloud.snapshot_backend_uninitialized"
)

type SnapshotPushInput struct {
	Config          Config
	Driver          Driver
	BackupRef       string
	BackupRoot      string
	DataDir         string
	CoverageOptions backupcoverage.Options
	NodeID          string
	DryRun          bool
	Now             func() time.Time
}

type SnapshotPushResult struct {
	Status            string                         `json:"status"`
	DryRun            bool                           `json:"dry_run"`
	Backend           string                         `json:"backend,omitempty"`
	BackupRef         string                         `json:"backup_ref"`
	BackupDir         string                         `json:"backup_dir"`
	SnapshotRef       string                         `json:"snapshot_ref"`
	UploadID          string                         `json:"upload_id"`
	Repository        string                         `json:"repository,omitempty"`
	Archive           string                         `json:"archive,omitempty"`
	RemotePrefix      string                         `json:"remote_prefix"`
	RemoteURI         string                         `json:"remote_uri"`
	TempRemotePrefix  string                         `json:"temp_remote_prefix,omitempty"`
	TempRemoteURI     string                         `json:"temp_remote_uri,omitempty"`
	LocalManifestPath string                         `json:"local_manifest_path,omitempty"`
	Verification      maintenance.BackupVerification `json:"verification"`
	Coverage          backupcoverage.Report          `json:"coverage"`
	Manifest          *SnapshotUploadManifest        `json:"manifest,omitempty"`
	Checks            map[string]string              `json:"checks"`
	FileCount         int64                          `json:"file_count"`
	TotalBytes        int64                          `json:"total_bytes"`
	PackedBytes       int64                          `json:"packed_bytes,omitempty"`
	DeduplicatedBytes int64                          `json:"deduplicated_bytes,omitempty"`
	Compression       string                         `json:"compression,omitempty"`
	Error             string                         `json:"error,omitempty"`
	Metrics           SnapshotMetrics                `json:"metrics,omitempty"`
}

type SnapshotMetrics struct {
	LocalBackupVerifyDurationMS int64  `json:"local_backup_verify_duration_ms,omitempty"`
	CoverageCheckDurationMS     int64  `json:"coverage_check_duration_ms,omitempty"`
	InventoryDurationMS         int64  `json:"inventory_duration_ms,omitempty"`
	RemoteListDurationMS        int64  `json:"remote_list_duration_ms,omitempty"`
	RemoteCopyDurationMS        int64  `json:"remote_copy_duration_ms,omitempty"`
	RemoteCheckDurationMS       int64  `json:"remote_check_duration_ms,omitempty"`
	RemotePromoteDurationMS     int64  `json:"remote_promote_duration_ms,omitempty"`
	ManifestUploadDurationMS    int64  `json:"manifest_upload_duration_ms,omitempty"`
	BorgCreateDurationMS        int64  `json:"borg_create_duration_ms,omitempty"`
	BorgInfoDurationMS          int64  `json:"borg_info_duration_ms,omitempty"`
	BorgCheckDurationMS         int64  `json:"borg_check_duration_ms,omitempty"`
	BorgCheckSkippedReason      string `json:"borg_check_skipped_reason,omitempty"`
	ManifestWriteDurationMS     int64  `json:"manifest_write_duration_ms,omitempty"`
	TotalDurationMS             int64  `json:"total_duration_ms,omitempty"`
}

type SnapshotListInput struct {
	Config Config
	Driver Driver
	NodeID string
}

type SnapshotListResult struct {
	Status          string         `json:"status"`
	NodeID          string         `json:"node_id"`
	Backend         string         `json:"backend,omitempty"`
	Repository      string         `json:"repository,omitempty"`
	RemoteRoot      string         `json:"remote_root"`
	Snapshots       []SnapshotItem `json:"snapshots"`
	Latest          *SnapshotItem  `json:"latest,omitempty"`
	Code            string         `json:"code,omitempty"`
	Error           string         `json:"error,omitempty"`
	RepairHint      string         `json:"repair_hint,omitempty"`
	Cached          bool           `json:"cached,omitempty"`
	CachePath       string         `json:"cache_path,omitempty"`
	CacheAgeSeconds int64          `json:"cache_age_seconds,omitempty"`
}

type SnapshotItem struct {
	Ref          string    `json:"ref"`
	Backend      string    `json:"backend,omitempty"`
	Repository   string    `json:"repository,omitempty"`
	Archive      string    `json:"archive,omitempty"`
	ArchiveClass string    `json:"archive_class,omitempty"`
	RemotePrefix string    `json:"remote_prefix"`
	RemoteURI    string    `json:"remote_uri"`
	Status       string    `json:"status"`
	DiscoveredAt time.Time `json:"discovered_at"`
}

type SnapshotVerifyProfile string
type SnapshotVerifyCoverage string

const (
	SnapshotVerifyProfileMetadata          SnapshotVerifyProfile = "metadata"
	SnapshotVerifyProfileRollingRepository SnapshotVerifyProfile = "rolling_repository"
	SnapshotVerifyProfileArchiveData       SnapshotVerifyProfile = "archive_data"

	SnapshotVerifyCoverageArchiveMetadata       SnapshotVerifyCoverage = "archive_metadata_complete"
	SnapshotVerifyCoverageRepositoryTimeBounded SnapshotVerifyCoverage = "repository_check_time_bounded"
	SnapshotVerifyCoverageArchiveData           SnapshotVerifyCoverage = "archive_data_complete"
)

type SnapshotVerifyOptions struct {
	Profile            SnapshotVerifyProfile `json:"profile,omitempty"`
	MaxDurationSeconds int64                 `json:"max_duration_seconds,omitempty"`
}

// SnapshotVerifyLiveInput is the typed request used by the existing live
// cloud snapshot verify endpoint. It is intentionally separate from worker
// policy: selecting a deep-assurance profile never establishes a cadence.
type SnapshotVerifyLiveInput struct {
	CloudSnapshotVerifyLiveInput
	SnapshotVerifyOptions
}

type SnapshotVerifyInput struct {
	Config   Config
	Driver   Driver
	NodeID   string
	Ref      string
	StateDir string
	Now      func() time.Time
	SnapshotVerifyOptions
}

type SnapshotVerifyResult struct {
	Status             string                 `json:"status"`
	Profile            SnapshotVerifyProfile  `json:"profile"`
	Coverage           SnapshotVerifyCoverage `json:"coverage,omitempty"`
	MaxDurationSeconds int64                  `json:"max_duration_seconds,omitempty"`
	Ref                string                 `json:"ref"`
	Backend            string                 `json:"backend,omitempty"`
	Repository         string                 `json:"repository,omitempty"`
	Archive            string                 `json:"archive,omitempty"`
	RemotePrefix       string                 `json:"remote_prefix"`
	RemoteURI          string                 `json:"remote_uri"`
	Manifest           SnapshotUploadManifest `json:"manifest,omitempty"`
	ManifestPath       string                 `json:"manifest_path,omitempty"`
	CheckedAt          time.Time              `json:"checked_at"`
	Checks             map[string]string      `json:"checks"`
	Errors             []string               `json:"errors,omitempty"`
}

type SnapshotFetchInput struct {
	restoreLifecycle *restoreAttemptLifecycle
	Config           Config
	Driver           Driver
	NodeID           string
	Ref              string
	To               string
	Now              func() time.Time
}

type SnapshotFetchResult struct {
	Status                      string                                              `json:"status"`
	Ref                         string                                              `json:"ref"`
	Backend                     string                                              `json:"backend,omitempty"`
	Repository                  string                                              `json:"repository,omitempty"`
	Archive                     string                                              `json:"archive,omitempty"`
	RemotePrefix                string                                              `json:"remote_prefix"`
	RemoteURI                   string                                              `json:"remote_uri"`
	TargetDir                   string                                              `json:"target_dir"`
	Copy                        CopyResult                                          `json:"copy"`
	Verification                maintenance.BackupVerification                      `json:"verification"`
	DirectArchiveManifest       *backupstrategy.DirectArchiveManifest               `json:"direct_archive_manifest,omitempty"`
	DirectArchiveManifestSHA256 string                                              `json:"direct_archive_manifest_sha256,omitempty"`
	ExtractionVerification      *backupstrategy.DirectArchiveExtractionVerification `json:"extraction_verification,omitempty"`
	V2UserSymlinkTargets        map[string]string                                   `json:"-"`
	Checks                      map[string]string                                   `json:"checks,omitempty"`
}

func PushSnapshot(ctx context.Context, input SnapshotPushInput) (SnapshotPushResult, error) {
	cfg, err := NormalizeConfig(input.Config)
	if err != nil {
		return SnapshotPushResult{}, err
	}
	if !cfg.Enabled {
		return SnapshotPushResult{}, fmt.Errorf("cloud storage is disabled")
	}
	backend, err := resolveSnapshotBackend(cfg, input.Driver)
	if err != nil {
		return SnapshotPushResult{}, err
	}
	input.Config = cfg
	return backend.Push(ctx, input)
}

func snapshotCoverageOptions(input SnapshotPushInput, backupRoot, manifestPath string) backupcoverage.Options {
	opts := input.CoverageOptions
	if strings.TrimSpace(opts.DataDir) == "" {
		opts.DataDir = input.DataDir
	}
	opts.Mode = backupcoverage.ModeLocal
	opts.BackupRoot = backupRoot
	opts.ManifestPath = manifestPath
	if input.Now != nil {
		opts.Now = input.Now
	}
	return opts
}

func ListSnapshots(ctx context.Context, input SnapshotListInput) (SnapshotListResult, error) {
	cfg, err := NormalizeConfig(input.Config)
	if err != nil {
		return SnapshotListResult{}, err
	}
	backend, err := resolveSnapshotBackend(cfg, input.Driver)
	if err != nil {
		return SnapshotListResult{}, err
	}
	input.Config = cfg
	return backend.List(ctx, input)
}

func VerifySnapshot(ctx context.Context, input SnapshotVerifyInput) (SnapshotVerifyResult, error) {
	cfg, err := NormalizeConfig(input.Config)
	if err != nil {
		return SnapshotVerifyResult{}, err
	}
	backend, err := resolveSnapshotBackend(cfg, input.Driver)
	if err != nil {
		return SnapshotVerifyResult{}, err
	}
	options, err := NormalizeSnapshotVerifyOptions(input.Ref, input.SnapshotVerifyOptions)
	if err != nil {
		return SnapshotVerifyResult{}, err
	}
	if err := ValidateSnapshotVerifyBackend(backend.BackendKind(), options); err != nil {
		return SnapshotVerifyResult{}, err
	}
	input.Config = cfg
	input.SnapshotVerifyOptions = options
	result, err := backend.Verify(ctx, input)
	if result.Profile == "" {
		result.Profile = options.Profile
	}
	return result, err
}

func ValidateSnapshotVerifyBackend(backend string, options SnapshotVerifyOptions) error {
	if backend != SnapshotBackendBorg && options.Profile != SnapshotVerifyProfileMetadata {
		return fmt.Errorf("cloud snapshot verify profile %q requires the Borg backend", options.Profile)
	}
	return nil
}

func NormalizeSnapshotVerifyOptions(ref string, options SnapshotVerifyOptions) (SnapshotVerifyOptions, error) {
	if options.Profile == "" {
		options.Profile = SnapshotVerifyProfileMetadata
	}
	switch options.Profile {
	case SnapshotVerifyProfileMetadata:
		if options.MaxDurationSeconds != 0 {
			return SnapshotVerifyOptions{}, fmt.Errorf("max_duration_seconds is only valid for profile %q", SnapshotVerifyProfileRollingRepository)
		}
	case SnapshotVerifyProfileRollingRepository:
		if strings.TrimSpace(ref) != "" {
			return SnapshotVerifyOptions{}, fmt.Errorf("profile %q verifies the repository and does not accept an archive ref", options.Profile)
		}
		if options.MaxDurationSeconds <= 0 {
			return SnapshotVerifyOptions{}, fmt.Errorf("profile %q requires a positive max_duration_seconds", options.Profile)
		}
	case SnapshotVerifyProfileArchiveData:
		ref = strings.TrimSpace(ref)
		if ref == "" || ref == "latest" || safeRemoteSegment(ref) != ref {
			return SnapshotVerifyOptions{}, fmt.Errorf("profile %q requires an exact canonical archive ref", options.Profile)
		}
		if options.MaxDurationSeconds != 0 {
			return SnapshotVerifyOptions{}, fmt.Errorf("max_duration_seconds is only valid for profile %q", SnapshotVerifyProfileRollingRepository)
		}
	default:
		return SnapshotVerifyOptions{}, fmt.Errorf("unsupported cloud snapshot verify profile %q", options.Profile)
	}
	return options, nil
}

func FetchSnapshot(ctx context.Context, input SnapshotFetchInput) (SnapshotFetchResult, error) {
	cfg, err := NormalizeConfig(input.Config)
	if err != nil {
		return SnapshotFetchResult{}, err
	}
	backend, err := resolveSnapshotBackend(cfg, input.Driver)
	if err != nil {
		return SnapshotFetchResult{}, err
	}
	input.Config = cfg
	result, err := backend.Fetch(ctx, input)
	result.V2UserSymlinkTargets = cloneV2UserSymlinkTargets(result.V2UserSymlinkTargets)
	return result, err
}

func cloneV2UserSymlinkTargets(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for archivePath, target := range source {
		cloned[archivePath] = target
	}
	return cloned
}

func ResolveSnapshotRef(ctx context.Context, cfg Config, driver Driver, nodeID, ref string) (SnapshotItem, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = "latest"
	}
	if ref == "latest" {
		list, err := ListSnapshots(ctx, SnapshotListInput{Config: cfg, Driver: driver, NodeID: nodeID})
		if err != nil {
			return SnapshotItem{}, err
		}
		if list.Latest == nil {
			return SnapshotItem{}, fmt.Errorf("no cloud snapshots found for node %s", list.NodeID)
		}
		return *list.Latest, nil
	}
	node := safeRemoteSegment(firstNonEmpty(nodeID, "main"))
	prefix := path.Join(cfg.Roots.MainSnapshots, node, safeRemoteSegment(ref))
	return SnapshotItem{Ref: safeRemoteSegment(ref), RemotePrefix: prefix, RemoteURI: cfg.RemoteURI(prefix), Status: "requested", DiscoveredAt: time.Now().UTC()}, nil
}

func ResolveBackupDir(ref, backupRoot string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = "latest"
	}
	if ref == "latest" {
		return latestBackupDir(backupRoot)
	}
	candidate := filepath.Clean(ref)
	if isDirectory(candidate) {
		return candidate, nil
	}
	if !filepath.IsAbs(candidate) {
		joined := filepath.Join(backupRoot, candidate)
		if isDirectory(joined) {
			return joined, nil
		}
	}
	matches, err := filepath.Glob(filepath.Join(backupRoot, "*"+ref+"*"))
	if err != nil {
		return "", err
	}
	sort.Strings(matches)
	for i := len(matches) - 1; i >= 0; i-- {
		if isDirectory(matches[i]) {
			return matches[i], nil
		}
	}
	return "", fmt.Errorf("backup %q could not be resolved under %s", ref, backupRoot)
}

func latestBackupDir(backupRoot string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(filepath.Clean(backupRoot), "*", "manifest.json"))
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no backup manifests found under %s", backupRoot)
	}
	sort.Strings(matches)
	return filepath.Dir(matches[len(matches)-1]), nil
}

func normalizeDriver(cfg Config, driver Driver) (Driver, error) {
	if driver != nil {
		return driver, nil
	}
	if !cfg.Enabled {
		return nil, fmt.Errorf("cloud storage is disabled")
	}
	d := NewRcloneDriver(cfg)
	return d, nil
}

func snapshotRefFromBackupDir(backupDir string) string {
	base := filepath.Base(filepath.Clean(backupDir))
	return safeRemoteSegment(base)
}

func newCloudUploadID(now time.Time, snapshotRef string) string {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return "cloud_upload_" + now.UTC().Format("20060102T150405Z") + "_" + safeRemoteSegment(snapshotRef)
	}
	return "cloud_upload_" + now.UTC().Format("20060102T150405Z") + "_" + hex.EncodeToString(random)
}

func safeRemoteSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-", " ", "-", "\t", "-", "\n", "-")
	value = replacer.Replace(value)
	value = strings.Trim(value, ".-")
	if value == "" {
		return "unknown"
	}
	return value
}

func requireEmptyOrMissingDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return os.MkdirAll(path, 0o750)
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("target path exists but is not a directory: %s", path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("target directory must be empty: %s", path)
	}
	return nil
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func normalizeNow(now func() time.Time) func() time.Time {
	if now != nil {
		return func() time.Time { return now().UTC() }
	}
	return func() time.Time { return time.Now().UTC() }
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
