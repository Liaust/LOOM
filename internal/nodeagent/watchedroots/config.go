package watchedroots

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/filesystemconnector"
)

const (
	ConfigSchemaVersion = "watched_root.config.v0.2"

	WatchBackendNone = "none"
	WatchBackendAuto = "auto"

	StartupScanStaleOnly = "stale_only"
	StartupScanAlways    = "always"
	StartupScanNever     = "never"

	HiddenPolicyExcludeByDefault = "exclude_by_default"
	HiddenPolicyInclude          = "include"
	HiddenPolicyPolicyControlled = "policy_controlled"

	BackupModeNone           = "none"
	BackupModeMetadataOnly   = "metadata_only"
	BackupModePrivateRaw     = "private_raw"
	BackupModeRawSnapshot    = "raw_snapshot"
	BackupModeIncrementalRaw = "incremental_raw"

	SyncModeNone          = "none"
	SyncModeSelectedFiles = "selected_files"

	SyncLogicalNameRelativePath = "relative_path"
	SyncLogicalNameBasename     = "basename"

	IndexModeNone         = "none"
	IndexModeMetadataOnly = "metadata_only"
	IndexModeMarkdownText = "markdown_text"

	DeleteModeLocalStateOnly = "local_state_only"
	DeleteModeTombstone      = "tombstone"
	DeleteModeIgnore         = "ignore"

	PackageDirectoryModeObserveBoundary = "observe_boundary"
	PackageDirectoryModeDescend         = "descend"

	PathCollisionModeWarn   = "warn"
	PathCollisionModeIgnore = "ignore"

	SpecialFileModeObserveSkip = "observe_skip"
	SpecialFileModeIgnore      = "ignore"

	ExecutableBitModeObserve = "observe"
	ExecutableBitModeIgnore  = "ignore"

	defaultDebounceWindow        = "45s"
	defaultFullRescanInterval    = "6h"
	defaultMaxRuntime            = "5m"
	defaultStabilityWindow       = "2s"
	defaultMaxFilesPerRun        = 10000
	defaultMaxDirsPerRun         = 2000
	defaultMassDeleteCount       = 100
	defaultMassDeletePercent     = 20
	defaultWorkerIntervalSecs    = 900
	defaultRootRelativePath      = "."
	defaultMaxHashBytesPerRun    = int64(1024 * 1024 * 1024)
	defaultMaxHashFileBytes      = int64(50 * 1024 * 1024)
	defaultMaxBackupFileBytes    = int64(1024 * 1024)
	defaultMaxBackupBatchBytes   = int64(1024 * 1024)
	defaultMaxBackupPendingItems = 10000
	defaultMaxBackupPendingBytes = int64(1024 * 1024 * 1024)
	defaultMaxSyncFileBytes      = int64(1024 * 1024)
	defaultMaxIndexTextBytes     = int64(512 * 1024)

	BackupOnLimitDegradeAndRequireManualAction = "degrade_and_require_manual_action"
)

var defaultExcludePatterns = append(filepolicy.DefaultExcludePatterns(),
	".trash",
	".trash/**",
	"**/.trash",
	"**/.trash/**",
)

type RootConfig struct {
	SchemaVersion    string         `json:"schema_version"`
	RootKey          string         `json:"root_key"`
	DisplayName      string         `json:"display_name"`
	SafeRootKey      string         `json:"safe_root_key"`
	RootRelativePath string         `json:"root_relative_path"`
	Include          []string       `json:"include"`
	Exclude          []string       `json:"exclude"`
	IgnorePolicy     IgnorePolicy   `json:"ignore_policy,omitempty"`
	Watch            WatchConfig    `json:"watch"`
	Scan             ScanConfig     `json:"scan"`
	BackupPolicy     BackupPolicy   `json:"backup_policy"`
	SyncPolicy       SyncPolicy     `json:"sync_policy"`
	IndexPolicy      IndexPolicy    `json:"index_policy"`
	DeletePolicy     DeletePolicy   `json:"delete_policy"`
	FidelityPolicy   FidelityPolicy `json:"fidelity_policy"`
}

type IgnorePolicy struct {
	Profile                string `json:"profile,omitempty"`
	DiscoverUserRules      bool   `json:"discover_user_rules"`
	PolicyRootRelativePath string `json:"policy_root_relative_path,omitempty"`
}

type WatchConfig struct {
	Enabled        bool   `json:"enabled"`
	Backend        string `json:"backend"`
	DebounceWindow string `json:"debounce_window"`
}

type ScanConfig struct {
	StartupScan        string `json:"startup_scan"`
	FullRescanInterval string `json:"full_rescan_interval"`
	MaxFilesPerRun     int    `json:"max_files_per_run"`
	MaxDirsPerRun      int    `json:"max_dirs_per_run"`
	MaxRuntime         string `json:"max_runtime"`
	StabilityWindow    string `json:"stability_window"`
	FollowSymlinks     bool   `json:"follow_symlinks"`
	HiddenPolicy       string `json:"hidden_policy"`
	MaxHashFileBytes   int64  `json:"max_hash_file_bytes"`
	MaxHashBytesPerRun int64  `json:"max_hash_bytes_per_run"`
}

type BackupPolicy struct {
	Mode                   string `json:"mode"`
	MaxFileBytes           int64  `json:"max_file_bytes,omitempty"`
	MaxBatchBytes          int64  `json:"max_batch_bytes,omitempty"`
	MaxPendingItems        int    `json:"max_pending_items,omitempty"`
	MaxPendingBytes        int64  `json:"max_pending_bytes,omitempty"`
	IncludeDeletionMarkers *bool  `json:"include_deletion_markers,omitempty"`
	OnLimit                string `json:"on_limit,omitempty"`
}

type SyncPolicy struct {
	Mode                string `json:"mode"`
	ProjectRef          string `json:"project_ref,omitempty"`
	ScopeRef            string `json:"scope_ref,omitempty"`
	MaxFileBytes        int64  `json:"max_file_bytes,omitempty"`
	LogicalNameStrategy string `json:"logical_name_strategy,omitempty"`
}

type IndexPolicy struct {
	Mode         string `json:"mode"`
	MaxTextBytes int64  `json:"max_text_bytes,omitempty"`
}

type DeletePolicy struct {
	Mode                       string `json:"mode"`
	MassDeleteThresholdCount   int    `json:"mass_delete_threshold_count"`
	MassDeleteThresholdPercent int    `json:"mass_delete_threshold_percent"`
}

type FidelityPolicy struct {
	ObserveEmptyDirectories        bool   `json:"observe_empty_directories"`
	ObserveSymlinks                bool   `json:"observe_symlinks"`
	FollowSymlinks                 bool   `json:"follow_symlinks"`
	ObserveXattrs                  bool   `json:"observe_xattrs"`
	ObserveACLPresence             bool   `json:"observe_acl_presence"`
	PreserveGeneratedAppleMetadata bool   `json:"preserve_generated_apple_metadata"`
	PackageDirectoryMode           string `json:"package_directory_mode"`
	PathCollisionMode              string `json:"path_collision_mode"`
	SpecialFileMode                string `json:"special_file_mode"`
	ExecutableBitMode              string `json:"executable_bit_mode"`
}

type ValidatedRoot struct {
	Config           RootConfig                   `json:"config"`
	SafeRoot         filesystemconnector.SafeRoot `json:"safe_root"`
	SafeRootPath     string                       `json:"safe_root_path"`
	RootPath         string                       `json:"root_path"`
	RootRelativePath string                       `json:"root_relative_path"`
	RootReachable    bool                         `json:"root_reachable"`
	ConfigHash       string                       `json:"config_hash"`
	PolicyResolver   *filepolicy.Resolver         `json:"-"`
}

func NormalizeRootConfig(config RootConfig) RootConfig {
	hasIgnoreProfile := strings.TrimSpace(config.IgnorePolicy.Profile) != ""
	config.SchemaVersion = strings.TrimSpace(config.SchemaVersion)
	if config.SchemaVersion == "" {
		config.SchemaVersion = ConfigSchemaVersion
	}
	config.RootKey = filesystemconnector.NormalizeRootKey(config.RootKey)
	config.SafeRootKey = filesystemconnector.NormalizeRootKey(config.SafeRootKey)
	config.DisplayName = strings.TrimSpace(config.DisplayName)
	if config.DisplayName == "" && config.RootKey != "" {
		config.DisplayName = strings.ReplaceAll(config.RootKey, "_", " ")
	}
	config.RootRelativePath = strings.TrimSpace(config.RootRelativePath)
	if config.RootRelativePath == "" {
		config.RootRelativePath = defaultRootRelativePath
	}
	if normalized, _, err := filesystemconnector.NormalizeRelativePath(config.RootRelativePath); err == nil {
		config.RootRelativePath = normalized
	}
	config.Include = normalizePatterns(config.Include)
	if len(config.Include) == 0 {
		config.Include = []string{"**/*"}
	}
	config.Exclude = normalizePatterns(config.Exclude)
	if len(config.Exclude) == 0 && !hasIgnoreProfile {
		config.Exclude = append([]string{}, defaultExcludePatterns...)
	}
	config.IgnorePolicy.Profile = strings.ToLower(strings.TrimSpace(config.IgnorePolicy.Profile))
	config.IgnorePolicy.PolicyRootRelativePath = strings.TrimSpace(config.IgnorePolicy.PolicyRootRelativePath)
	if hasIgnoreProfile && config.IgnorePolicy.PolicyRootRelativePath == "" {
		config.IgnorePolicy.PolicyRootRelativePath = config.RootRelativePath
	}
	if config.IgnorePolicy.PolicyRootRelativePath != "" {
		if normalized, _, err := filesystemconnector.NormalizeRelativePath(config.IgnorePolicy.PolicyRootRelativePath); err == nil {
			config.IgnorePolicy.PolicyRootRelativePath = normalized
		}
	}
	config.Watch.Backend = strings.ToLower(strings.TrimSpace(config.Watch.Backend))
	if config.Watch.Backend == "" {
		config.Watch.Backend = WatchBackendNone
	}
	config.Watch.DebounceWindow = strings.TrimSpace(config.Watch.DebounceWindow)
	if config.Watch.DebounceWindow == "" {
		config.Watch.DebounceWindow = defaultDebounceWindow
	}
	config.Scan.StartupScan = strings.ToLower(strings.TrimSpace(config.Scan.StartupScan))
	if config.Scan.StartupScan == "" {
		config.Scan.StartupScan = StartupScanStaleOnly
	}
	config.Scan.FullRescanInterval = strings.TrimSpace(config.Scan.FullRescanInterval)
	if config.Scan.FullRescanInterval == "" {
		config.Scan.FullRescanInterval = defaultFullRescanInterval
	}
	if config.Scan.MaxFilesPerRun <= 0 {
		config.Scan.MaxFilesPerRun = defaultMaxFilesPerRun
	}
	if config.Scan.MaxDirsPerRun <= 0 {
		config.Scan.MaxDirsPerRun = defaultMaxDirsPerRun
	}
	config.Scan.MaxRuntime = strings.TrimSpace(config.Scan.MaxRuntime)
	if config.Scan.MaxRuntime == "" {
		config.Scan.MaxRuntime = defaultMaxRuntime
	}
	config.Scan.StabilityWindow = strings.TrimSpace(config.Scan.StabilityWindow)
	if config.Scan.StabilityWindow == "" {
		config.Scan.StabilityWindow = defaultStabilityWindow
	}
	config.Scan.HiddenPolicy = strings.ToLower(strings.TrimSpace(config.Scan.HiddenPolicy))
	if config.Scan.HiddenPolicy == "" {
		if hasIgnoreProfile {
			config.Scan.HiddenPolicy = HiddenPolicyPolicyControlled
		} else {
			config.Scan.HiddenPolicy = HiddenPolicyExcludeByDefault
		}
	}
	if config.Scan.MaxHashFileBytes <= 0 {
		config.Scan.MaxHashFileBytes = defaultMaxHashFileBytes
	}
	if config.Scan.MaxHashBytesPerRun <= 0 {
		config.Scan.MaxHashBytesPerRun = defaultMaxHashBytesPerRun
	}
	config.FidelityPolicy = normalizeFidelityPolicy(config.FidelityPolicy)
	if config.Scan.FollowSymlinks {
		config.FidelityPolicy.FollowSymlinks = true
	} else if config.FidelityPolicy.FollowSymlinks {
		config.Scan.FollowSymlinks = true
	}
	config.BackupPolicy.Mode = normalizeMode(config.BackupPolicy.Mode, BackupModeNone)
	if config.BackupPolicy.MaxFileBytes <= 0 {
		config.BackupPolicy.MaxFileBytes = defaultMaxBackupFileBytes
	}
	if config.BackupPolicy.MaxBatchBytes <= 0 {
		config.BackupPolicy.MaxBatchBytes = defaultMaxBackupBatchBytes
	}
	if config.BackupPolicy.MaxPendingItems <= 0 {
		config.BackupPolicy.MaxPendingItems = defaultMaxBackupPendingItems
	}
	if config.BackupPolicy.MaxPendingBytes <= 0 {
		config.BackupPolicy.MaxPendingBytes = defaultMaxBackupPendingBytes
	}
	if config.BackupPolicy.IncludeDeletionMarkers == nil {
		enabled := true
		config.BackupPolicy.IncludeDeletionMarkers = &enabled
	}
	config.BackupPolicy.OnLimit = normalizeMode(config.BackupPolicy.OnLimit, BackupOnLimitDegradeAndRequireManualAction)
	config.SyncPolicy.Mode = normalizeMode(config.SyncPolicy.Mode, SyncModeNone)
	config.SyncPolicy.ProjectRef = strings.TrimSpace(config.SyncPolicy.ProjectRef)
	config.SyncPolicy.ScopeRef = strings.TrimSpace(config.SyncPolicy.ScopeRef)
	if config.SyncPolicy.MaxFileBytes <= 0 {
		config.SyncPolicy.MaxFileBytes = defaultMaxSyncFileBytes
	}
	config.SyncPolicy.LogicalNameStrategy = normalizeMode(config.SyncPolicy.LogicalNameStrategy, SyncLogicalNameRelativePath)
	config.IndexPolicy.Mode = normalizeMode(config.IndexPolicy.Mode, IndexModeNone)
	if config.IndexPolicy.MaxTextBytes <= 0 {
		config.IndexPolicy.MaxTextBytes = defaultMaxIndexTextBytes
	}
	config.DeletePolicy.Mode = normalizeMode(config.DeletePolicy.Mode, DeleteModeLocalStateOnly)
	if config.DeletePolicy.MassDeleteThresholdCount <= 0 {
		config.DeletePolicy.MassDeleteThresholdCount = defaultMassDeleteCount
	}
	if config.DeletePolicy.MassDeleteThresholdPercent <= 0 {
		config.DeletePolicy.MassDeleteThresholdPercent = defaultMassDeletePercent
	}
	return config
}

func ValidateRootConfig(config RootConfig, fsConfig filesystemconnector.Config) (ValidatedRoot, error) {
	return validateRootConfig(config, fsConfig, true)
}

func ValidateRootConfigForRun(config RootConfig, fsConfig filesystemconnector.Config) (ValidatedRoot, error) {
	return validateRootConfig(config, fsConfig, false)
}

func ValidatePortableRootConfig(config RootConfig) (RootConfig, error) {
	rawConfig := config
	config = NormalizeRootConfig(config)
	if config.SchemaVersion != ConfigSchemaVersion {
		return RootConfig{}, fmt.Errorf("unsupported watched-root config schema %q", config.SchemaVersion)
	}
	if config.RootKey == "" {
		return RootConfig{}, errors.New("watched root root_key is required")
	}
	if config.SafeRootKey == "" {
		return RootConfig{}, errors.New("watched root safe_root_key is required")
	}
	relativePath, _, err := filesystemconnector.NormalizeRelativePath(config.RootRelativePath)
	if err != nil {
		return RootConfig{}, fmt.Errorf("invalid watched-root path: %w", err)
	}
	config.RootRelativePath = relativePath
	if err := validatePatterns(config.Include); err != nil {
		return RootConfig{}, fmt.Errorf("invalid include pattern: %w", err)
	}
	if err := validatePatterns(config.Exclude); err != nil {
		return RootConfig{}, fmt.Errorf("invalid exclude pattern: %w", err)
	}
	if err := validateIgnorePolicy(config.IgnorePolicy, config.RootRelativePath); err != nil {
		return RootConfig{}, err
	}
	if err := validateWatch(config.Watch); err != nil {
		return RootConfig{}, err
	}
	if err := validateScan(config.Scan); err != nil {
		return RootConfig{}, err
	}
	if err := validateFidelityPolicy(config.FidelityPolicy); err != nil {
		return RootConfig{}, err
	}
	if err := validatePolicies(config, filesystemconnector.SafeRoot{}); err != nil {
		return RootConfig{}, err
	}
	if err := validateExplicitBackupLimits(rawConfig, config); err != nil {
		return RootConfig{}, err
	}
	return config, nil
}

func validateRootConfig(config RootConfig, fsConfig filesystemconnector.Config, requireReachable bool) (ValidatedRoot, error) {
	rawConfig := config
	config = NormalizeRootConfig(config)
	if config.SchemaVersion != ConfigSchemaVersion {
		return ValidatedRoot{}, fmt.Errorf("unsupported watched-root config schema %q", config.SchemaVersion)
	}
	if config.RootKey == "" {
		return ValidatedRoot{}, errors.New("watched root root_key is required")
	}
	if config.SafeRootKey == "" {
		return ValidatedRoot{}, errors.New("watched root safe_root_key is required")
	}
	safeRoot, ok := filesystemconnector.FindSafeRoot(fsConfig, config.SafeRootKey)
	if !ok {
		return ValidatedRoot{}, fmt.Errorf("filesystem safe root %q is not configured", config.SafeRootKey)
	}
	if err := filesystemconnector.ValidateSafeRoot(safeRoot); err != nil {
		return ValidatedRoot{}, err
	}
	relativePath, _, err := filesystemconnector.NormalizeRelativePath(config.RootRelativePath)
	if err != nil {
		return ValidatedRoot{}, fmt.Errorf("invalid watched-root path: %w", err)
	}
	config.RootRelativePath = relativePath

	safeRootPath, err := filesystemconnector.CanonicalSafeRootPath(safeRoot)
	if err != nil {
		return ValidatedRoot{}, err
	}
	rootPath := filepath.Join(safeRootPath, filepath.FromSlash(relativePath))
	canonicalRootPath, err := filepath.EvalSymlinks(rootPath)
	rootReachable := true
	if err != nil {
		if os.IsNotExist(err) {
			if requireReachable {
				return ValidatedRoot{}, fmt.Errorf("watched root path is not reachable: %s", relativePath)
			}
			canonicalRootPath = filepath.Clean(rootPath)
			rootReachable = false
		} else {
			return ValidatedRoot{}, fmt.Errorf("watched root path could not be safely resolved: %w", err)
		}
	}
	canonicalRootPath = filepath.Clean(canonicalRootPath)
	if !filesystemconnector.IsWithin(safeRootPath, canonicalRootPath) {
		return ValidatedRoot{}, fmt.Errorf("watched root path escapes filesystem safe root")
	}
	info, err := os.Stat(canonicalRootPath)
	if err != nil {
		if os.IsNotExist(err) && !requireReachable {
			rootReachable = false
		} else {
			return ValidatedRoot{}, fmt.Errorf("inspect watched root path: %w", err)
		}
	}
	if rootReachable && !info.IsDir() {
		return ValidatedRoot{}, fmt.Errorf("watched root path must be a directory: %s", relativePath)
	}

	if err := validatePatterns(config.Include); err != nil {
		return ValidatedRoot{}, fmt.Errorf("invalid include pattern: %w", err)
	}
	if err := validatePatterns(config.Exclude); err != nil {
		return ValidatedRoot{}, fmt.Errorf("invalid exclude pattern: %w", err)
	}
	if err := validateIgnorePolicy(config.IgnorePolicy, config.RootRelativePath); err != nil {
		return ValidatedRoot{}, err
	}
	if err := validateWatch(config.Watch); err != nil {
		return ValidatedRoot{}, err
	}
	if err := validateScan(config.Scan); err != nil {
		return ValidatedRoot{}, err
	}
	if err := validateFidelityPolicy(config.FidelityPolicy); err != nil {
		return ValidatedRoot{}, err
	}
	if config.Scan.MaxHashFileBytes > safeRoot.MaxFileBytes {
		config.Scan.MaxHashFileBytes = safeRoot.MaxFileBytes
	}
	if err := validatePolicies(config, safeRoot); err != nil {
		return ValidatedRoot{}, err
	}
	if err := validateExplicitBackupLimits(rawConfig, config); err != nil {
		return ValidatedRoot{}, err
	}

	var policyResolver *filepolicy.Resolver
	if rootReachable && config.IgnorePolicy.Profile != "" {
		profile, err := filepolicy.ParseProfile(config.IgnorePolicy.Profile)
		if err != nil {
			return ValidatedRoot{}, err
		}
		policyRootPath := filepath.Join(safeRootPath, filepath.FromSlash(config.IgnorePolicy.PolicyRootRelativePath))
		policyResolver, err = filepolicy.NewResolver(canonicalRootPath, profile, filepolicy.ResolverOptions{
			DiscoverUserRules: config.IgnorePolicy.DiscoverUserRules,
			PolicyRoot:        policyRootPath,
			ContractExcludes:  config.Exclude,
		})
		if err != nil {
			return ValidatedRoot{}, fmt.Errorf("resolve watched-root ignore policy: %w", err)
		}
	}

	return ValidatedRoot{
		Config:           config,
		SafeRoot:         safeRoot,
		SafeRootPath:     safeRootPath,
		RootPath:         canonicalRootPath,
		RootRelativePath: relativePath,
		RootReachable:    rootReachable,
		ConfigHash:       ConfigHash(config),
		PolicyResolver:   policyResolver,
	}, nil
}

func NormalizeRootPathInput(rawPath string, safeRoot filesystemconnector.SafeRoot) (string, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return "", errors.New("watched root path is required")
	}
	if !filepath.IsAbs(rawPath) {
		relativePath, _, err := filesystemconnector.NormalizeRelativePath(rawPath)
		if err != nil {
			return "", err
		}
		return relativePath, nil
	}
	safeRootPath, err := filesystemconnector.CanonicalSafeRootPath(safeRoot)
	if err != nil {
		return "", err
	}
	absolutePath, err := filepath.Abs(rawPath)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(absolutePath); err == nil {
		absolutePath = resolved
	}
	absolutePath = filepath.Clean(absolutePath)
	if !filesystemconnector.IsWithin(safeRootPath, absolutePath) {
		return "", fmt.Errorf("watched root path escapes filesystem safe root")
	}
	relativePath, err := filepath.Rel(safeRootPath, absolutePath)
	if err != nil {
		return "", err
	}
	if relativePath == "" {
		relativePath = "."
	}
	normalized, _, err := filesystemconnector.NormalizeRelativePath(filepath.ToSlash(relativePath))
	return normalized, err
}

func ConfigHash(config RootConfig) string {
	config = NormalizeRootConfig(config)
	raw, _ := json.Marshal(config)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func DefaultWorkerIntervalSeconds() int {
	return defaultWorkerIntervalSecs
}

func validateWatch(config WatchConfig) error {
	switch config.Backend {
	case WatchBackendNone, WatchBackendAuto:
	default:
		return fmt.Errorf("unsupported watch backend %q", config.Backend)
	}
	if _, err := time.ParseDuration(config.DebounceWindow); err != nil {
		return fmt.Errorf("invalid watch debounce_window: %w", err)
	}
	return nil
}

func validateScan(config ScanConfig) error {
	switch config.StartupScan {
	case StartupScanStaleOnly, StartupScanAlways, StartupScanNever:
	default:
		return fmt.Errorf("unsupported startup_scan %q", config.StartupScan)
	}
	for label, value := range map[string]string{
		"full_rescan_interval": config.FullRescanInterval,
		"max_runtime":          config.MaxRuntime,
		"stability_window":     config.StabilityWindow,
	} {
		if _, err := time.ParseDuration(value); err != nil {
			return fmt.Errorf("invalid scan %s: %w", label, err)
		}
	}
	if config.MaxFilesPerRun <= 0 {
		return errors.New("scan max_files_per_run must be positive")
	}
	if config.MaxDirsPerRun <= 0 {
		return errors.New("scan max_dirs_per_run must be positive")
	}
	if config.MaxHashFileBytes <= 0 {
		return errors.New("scan max_hash_file_bytes must be positive")
	}
	if config.MaxHashBytesPerRun <= 0 {
		return errors.New("scan max_hash_bytes_per_run must be positive")
	}
	switch config.HiddenPolicy {
	case HiddenPolicyExcludeByDefault, HiddenPolicyInclude, HiddenPolicyPolicyControlled:
	default:
		return fmt.Errorf("unsupported hidden_policy %q", config.HiddenPolicy)
	}
	return nil
}

func validateIgnorePolicy(policy IgnorePolicy, rootRelativePath string) error {
	if policy.Profile == "" {
		return nil
	}
	if _, err := filepolicy.ParseProfile(policy.Profile); err != nil {
		return err
	}
	policyRoot, _, err := filesystemconnector.NormalizeRelativePath(policy.PolicyRootRelativePath)
	if err != nil {
		return fmt.Errorf("invalid ignore policy root: %w", err)
	}
	root, _, err := filesystemconnector.NormalizeRelativePath(rootRelativePath)
	if err != nil {
		return fmt.Errorf("invalid watched-root path: %w", err)
	}
	policyRootPath := filepath.Clean(filepath.FromSlash(policyRoot))
	rootPath := filepath.Clean(filepath.FromSlash(root))
	relative, err := filepath.Rel(policyRootPath, rootPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("ignore policy root %q must contain watched-root path %q", policy.PolicyRootRelativePath, rootRelativePath)
	}
	return nil
}

func validateFidelityPolicy(config FidelityPolicy) error {
	if !allowedMode(config.PackageDirectoryMode, PackageDirectoryModeObserveBoundary, PackageDirectoryModeDescend) {
		return fmt.Errorf("unsupported package_directory_mode %q", config.PackageDirectoryMode)
	}
	if !allowedMode(config.PathCollisionMode, PathCollisionModeWarn, PathCollisionModeIgnore) {
		return fmt.Errorf("unsupported path_collision_mode %q", config.PathCollisionMode)
	}
	if !allowedMode(config.SpecialFileMode, SpecialFileModeObserveSkip, SpecialFileModeIgnore) {
		return fmt.Errorf("unsupported special_file_mode %q", config.SpecialFileMode)
	}
	if !allowedMode(config.ExecutableBitMode, ExecutableBitModeObserve, ExecutableBitModeIgnore) {
		return fmt.Errorf("unsupported executable_bit_mode %q", config.ExecutableBitMode)
	}
	return nil
}

func validatePolicies(config RootConfig, safeRoot filesystemconnector.SafeRoot) error {
	if !allowedMode(config.BackupPolicy.Mode, BackupModeNone, BackupModeMetadataOnly, BackupModePrivateRaw, BackupModeRawSnapshot, BackupModeIncrementalRaw) {
		return fmt.Errorf("unsupported backup policy mode %q", config.BackupPolicy.Mode)
	}
	if config.BackupPolicy.MaxFileBytes <= 0 {
		return errors.New("backup policy max_file_bytes must be positive")
	}
	if config.BackupPolicy.MaxBatchBytes <= 0 {
		return errors.New("backup policy max_batch_bytes must be positive")
	}
	if config.BackupPolicy.MaxPendingItems <= 0 {
		return errors.New("backup policy max_pending_items must be positive")
	}
	if config.BackupPolicy.MaxPendingBytes <= 0 {
		return errors.New("backup policy max_pending_bytes must be positive")
	}
	if !allowedMode(config.BackupPolicy.OnLimit, BackupOnLimitDegradeAndRequireManualAction) {
		return fmt.Errorf("unsupported backup policy on_limit %q", config.BackupPolicy.OnLimit)
	}
	if safeRoot.MaxFileBytes > 0 && config.BackupPolicy.MaxFileBytes > safeRoot.MaxFileBytes {
		return fmt.Errorf("backup policy max_file_bytes exceeds safe root max_file_bytes")
	}
	if !allowedMode(config.SyncPolicy.Mode, SyncModeNone, SyncModeSelectedFiles) {
		return fmt.Errorf("unsupported sync policy mode %q", config.SyncPolicy.Mode)
	}
	if !allowedMode(config.SyncPolicy.LogicalNameStrategy, SyncLogicalNameRelativePath, SyncLogicalNameBasename) {
		return fmt.Errorf("unsupported sync logical_name_strategy %q", config.SyncPolicy.LogicalNameStrategy)
	}
	if !allowedMode(config.IndexPolicy.Mode, IndexModeNone, IndexModeMetadataOnly, IndexModeMarkdownText) {
		return fmt.Errorf("unsupported index policy mode %q", config.IndexPolicy.Mode)
	}
	if !allowedMode(config.DeletePolicy.Mode, DeleteModeLocalStateOnly, DeleteModeTombstone, DeleteModeIgnore) {
		return fmt.Errorf("unsupported delete policy mode %q", config.DeletePolicy.Mode)
	}
	if config.DeletePolicy.MassDeleteThresholdCount <= 0 {
		return errors.New("delete policy mass_delete_threshold_count must be positive")
	}
	if config.DeletePolicy.MassDeleteThresholdPercent <= 0 || config.DeletePolicy.MassDeleteThresholdPercent > 100 {
		return errors.New("delete policy mass_delete_threshold_percent must be between 1 and 100")
	}
	if safeRoot.PrivateBackupOnly {
		if config.SyncPolicy.Mode != SyncModeNone {
			return errors.New("private-backup-only safe roots cannot enable watched-root sync policy")
		}
		if config.IndexPolicy.Mode != IndexModeNone {
			return errors.New("private-backup-only safe roots cannot enable watched-root index policy")
		}
	}
	if config.SyncPolicy.Mode == SyncModeSelectedFiles {
		if (config.SyncPolicy.ProjectRef == "") == (config.SyncPolicy.ScopeRef == "") {
			return errors.New("selected watched-root sync policy requires exactly one project_ref or scope_ref")
		}
		if config.SyncPolicy.MaxFileBytes <= 0 {
			return errors.New("sync policy max_file_bytes must be positive")
		}
		if safeRoot.MaxFileBytes > 0 && config.SyncPolicy.MaxFileBytes > safeRoot.MaxFileBytes {
			return fmt.Errorf("sync policy max_file_bytes exceeds safe root max_file_bytes")
		}
	} else {
		if config.SyncPolicy.ProjectRef != "" || config.SyncPolicy.ScopeRef != "" {
			return errors.New("sync project_ref or scope_ref requires selected_files mode")
		}
	}
	if config.IndexPolicy.Mode == IndexModeMarkdownText && config.SyncPolicy.Mode != SyncModeSelectedFiles {
		return errors.New("markdown text indexing requires selected watched-root sync policy")
	}
	if config.IndexPolicy.MaxTextBytes <= 0 {
		return errors.New("index policy max_text_bytes must be positive")
	}
	return nil
}

func validateExplicitBackupLimits(rawConfig RootConfig, normalized RootConfig) error {
	if normalized.BackupPolicy.Mode == BackupModeNone {
		return nil
	}
	if rawConfig.BackupPolicy.MaxFileBytes <= 0 {
		return errors.New("backup policy max_file_bytes must be explicit when backup mode is enabled; choose a size that matches the root's real file policy")
	}
	if rawConfig.BackupPolicy.MaxBatchBytes <= 0 {
		return errors.New("backup policy max_batch_bytes must be explicit when backup mode is enabled; choose a size that can carry the largest intended file")
	}
	return nil
}

func normalizePatterns(patterns []string) []string {
	out := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(filepath.ToSlash(pattern))
		if pattern == "" {
			continue
		}
		out = append(out, pattern)
	}
	return out
}

func normalizeMode(value, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return fallback
	}
	return value
}

func normalizeFidelityPolicy(policy FidelityPolicy) FidelityPolicy {
	if policy == (FidelityPolicy{}) {
		return FidelityPolicy{
			ObserveEmptyDirectories:        true,
			ObserveSymlinks:                true,
			FollowSymlinks:                 false,
			ObserveXattrs:                  true,
			ObserveACLPresence:             true,
			PreserveGeneratedAppleMetadata: false,
			PackageDirectoryMode:           PackageDirectoryModeObserveBoundary,
			PathCollisionMode:              PathCollisionModeWarn,
			SpecialFileMode:                SpecialFileModeObserveSkip,
			ExecutableBitMode:              ExecutableBitModeObserve,
		}
	}
	policy.PackageDirectoryMode = normalizeMode(policy.PackageDirectoryMode, PackageDirectoryModeObserveBoundary)
	policy.PathCollisionMode = normalizeMode(policy.PathCollisionMode, PathCollisionModeWarn)
	policy.SpecialFileMode = normalizeMode(policy.SpecialFileMode, SpecialFileModeObserveSkip)
	policy.ExecutableBitMode = normalizeMode(policy.ExecutableBitMode, ExecutableBitModeObserve)
	return policy
}

func allowedMode(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
