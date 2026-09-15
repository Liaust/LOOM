package cloudstorage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	ConfigSchemaVersionV063 = "loom.cloud.config.v0.6.3"
	ConfigSchemaVersionV064 = "loom.cloud.config.v0.6.4"
	ConfigSchemaVersion     = "loom.cloud.config.v0.6.5"

	DefaultConfigPath                       = "/etc/loom/cloud/config.json"
	DefaultRcloneConfigPath                 = "/etc/loom/cloud/rclone.conf"
	DefaultStateDir                         = "/var/lib/loom/cloud"
	DefaultProvider                         = "hetzner_storage_box"
	DefaultDriver                           = "rclone"
	DefaultSnapshotBackend                  = SnapshotBackendLegacyTree
	DefaultBorgBinary                       = "borg"
	DefaultBorgEncryption                   = "repokey-blake2"
	DefaultBorgCompression                  = "zstd,6"
	DefaultBorgCheckMode                    = "repository"
	DefaultBorgCheckIntervalHours           = 24
	DefaultBorgLockWaitSeconds              = 5
	DefaultSnapshotInventoryCacheTTLSeconds = 300
	DefaultRcloneBinary                     = "rclone"
	DefaultRemoteLockWaitSeconds            = 0
	DefaultRemoteLockWorkerWaitSeconds      = 30
	DefaultRemoteLockEffectfulWaitSeconds   = 300
	DefaultRemoteName                       = "loom-cloud"
	DefaultRemoteRoot                       = "loom"
	DefaultMainSnapshots                    = "main-snapshots"
	DefaultFullOffload                      = "full-offload"
	DefaultCloudFolder                      = "cloud-folder"
)

type Config struct {
	SchemaVersion                  string          `json:"schema_version"`
	Enabled                        bool            `json:"enabled"`
	Provider                       string          `json:"provider"`
	Driver                         string          `json:"driver"`
	RemoteName                     string          `json:"remote_name"`
	RemoteRoot                     string          `json:"remote_root"`
	RcloneConfigPath               string          `json:"rclone_config_path"`
	RcloneBinary                   string          `json:"rclone_binary"`
	StateDir                       string          `json:"state_dir"`
	RemoteLockPath                 string          `json:"remote_lock_path"`
	RemoteLockWaitSeconds          int             `json:"remote_lock_wait_seconds"`
	RemoteLockWorkerWaitSeconds    int             `json:"remote_lock_worker_wait_seconds"`
	RemoteLockEffectfulWaitSeconds int             `json:"remote_lock_effectful_wait_seconds"`
	Roots                          Roots           `json:"roots"`
	Snapshots                      SnapshotsConfig `json:"snapshots"`
}

type Roots struct {
	MainSnapshots string `json:"main_snapshots"`
	FullOffload   string `json:"full_offload"`
	CloudFolder   string `json:"cloud_folder"`
}

type SnapshotsConfig struct {
	Backend string     `json:"backend"`
	Borg    BorgConfig `json:"borg,omitempty"`
}

type BorgConfig struct {
	Binary                   string              `json:"binary,omitempty"`
	Repository               string              `json:"repository,omitempty"`
	PassphraseFile           string              `json:"passphrase_file,omitempty"`
	RSH                      string              `json:"rsh,omitempty"`
	CacheDir                 string              `json:"cache_dir,omitempty"`
	SecurityDir              string              `json:"security_dir,omitempty"`
	Encryption               string              `json:"encryption,omitempty"`
	Compression              string              `json:"compression,omitempty"`
	CheckMode                string              `json:"check_mode,omitempty"`
	CheckIntervalHours       int                 `json:"check_interval_hours,omitempty"`
	LockWaitSeconds          int                 `json:"lock_wait_seconds,omitempty"`
	InventoryCacheTTLSeconds int                 `json:"inventory_cache_ttl_seconds,omitempty"`
	Retention                BorgRetentionConfig `json:"retention,omitempty"`
}

// BorgRetentionConfig is deliberately separate from the routine writer
// configuration. A retention runner is constructed only from this block; the
// normal Borg runner never inherits prune, delete, or compact authority merely
// because a repository is writable.
type BorgRetentionConfig struct {
	Enabled         bool   `json:"enabled,omitempty"`
	AuthorityID     string `json:"authority_id,omitempty"`
	Binary          string `json:"binary,omitempty"`
	Repository      string `json:"repository,omitempty"`
	PassphraseFile  string `json:"passphrase_file,omitempty"`
	RSH             string `json:"rsh,omitempty"`
	CacheDir        string `json:"cache_dir,omitempty"`
	SecurityDir     string `json:"security_dir,omitempty"`
	CheckMode       string `json:"check_mode,omitempty"`
	LockWaitSeconds int    `json:"lock_wait_seconds,omitempty"`
}

type LoadResult struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Config Config `json:"config"`
}

func DefaultConfig() Config {
	return Config{
		SchemaVersion:                  ConfigSchemaVersion,
		Enabled:                        false,
		Provider:                       DefaultProvider,
		Driver:                         DefaultDriver,
		RemoteName:                     DefaultRemoteName,
		RemoteRoot:                     DefaultRemoteRoot,
		RcloneConfigPath:               DefaultRcloneConfigPath,
		RcloneBinary:                   DefaultRcloneBinary,
		StateDir:                       DefaultStateDir,
		RemoteLockPath:                 filepath.Join(DefaultStateDir, "locks", "storagebox.lock"),
		RemoteLockWaitSeconds:          DefaultRemoteLockWaitSeconds,
		RemoteLockWorkerWaitSeconds:    DefaultRemoteLockWorkerWaitSeconds,
		RemoteLockEffectfulWaitSeconds: DefaultRemoteLockEffectfulWaitSeconds,
		Roots: Roots{
			MainSnapshots: DefaultMainSnapshots,
			FullOffload:   DefaultFullOffload,
			CloudFolder:   DefaultCloudFolder,
		},
		Snapshots: SnapshotsConfig{
			Backend: DefaultSnapshotBackend,
			Borg: BorgConfig{
				Binary:                   DefaultBorgBinary,
				CacheDir:                 filepath.Join(DefaultStateDir, "borg", "cache"),
				SecurityDir:              filepath.Join(DefaultStateDir, "borg", "security"),
				Encryption:               DefaultBorgEncryption,
				Compression:              DefaultBorgCompression,
				CheckMode:                DefaultBorgCheckMode,
				CheckIntervalHours:       DefaultBorgCheckIntervalHours,
				LockWaitSeconds:          DefaultBorgLockWaitSeconds,
				InventoryCacheTTLSeconds: DefaultSnapshotInventoryCacheTTLSeconds,
			},
		},
	}
}

func LoadConfig(configPath string) (LoadResult, error) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		configPath = DefaultConfigPath
	}
	var cfg Config
	payload, err := os.ReadFile(configPath)
	if err != nil {
		if errorsIsNotExist(err) {
			return LoadResult{Path: configPath, Exists: false, Config: DefaultConfig()}, nil
		}
		return LoadResult{}, fmt.Errorf("read cloud config: %w", err)
	}
	if err := json.Unmarshal(payload, &cfg); err != nil {
		return LoadResult{}, fmt.Errorf("parse cloud config: %w", err)
	}
	normalized, err := NormalizeConfig(cfg)
	if err != nil {
		return LoadResult{}, err
	}
	return LoadResult{Path: configPath, Exists: true, Config: normalized}, nil
}

func NormalizeConfig(input Config) (Config, error) {
	cfg := input
	if strings.TrimSpace(cfg.SchemaVersion) == "" {
		cfg.SchemaVersion = ConfigSchemaVersion
	}
	switch cfg.SchemaVersion {
	case ConfigSchemaVersion, ConfigSchemaVersionV064, ConfigSchemaVersionV063:
	default:
		return Config{}, fmt.Errorf("unsupported cloud config schema_version %q", cfg.SchemaVersion)
	}
	cfg.SchemaVersion = ConfigSchemaVersion
	if strings.TrimSpace(cfg.Provider) == "" {
		cfg.Provider = DefaultProvider
	}
	if strings.TrimSpace(cfg.Driver) == "" {
		cfg.Driver = DefaultDriver
	}
	if strings.TrimSpace(cfg.RemoteName) == "" {
		cfg.RemoteName = DefaultRemoteName
	}
	if strings.TrimSpace(cfg.RemoteRoot) == "" {
		cfg.RemoteRoot = DefaultRemoteRoot
	}
	if strings.TrimSpace(cfg.RcloneConfigPath) == "" {
		cfg.RcloneConfigPath = DefaultRcloneConfigPath
	}
	if strings.TrimSpace(cfg.RcloneBinary) == "" {
		cfg.RcloneBinary = DefaultRcloneBinary
	}
	if strings.TrimSpace(cfg.StateDir) == "" {
		cfg.StateDir = DefaultStateDir
	}
	if strings.TrimSpace(cfg.RemoteLockPath) == "" {
		cfg.RemoteLockPath = filepath.Join(cfg.StateDir, "locks", "storagebox.lock")
	}
	if cfg.RemoteLockWaitSeconds < 0 {
		return Config{}, fmt.Errorf("remote_lock_wait_seconds must be non-negative")
	}
	if cfg.RemoteLockWorkerWaitSeconds < 0 {
		return Config{}, fmt.Errorf("remote_lock_worker_wait_seconds must be non-negative")
	}
	if cfg.RemoteLockWorkerWaitSeconds == 0 {
		cfg.RemoteLockWorkerWaitSeconds = DefaultRemoteLockWorkerWaitSeconds
	}
	if cfg.RemoteLockEffectfulWaitSeconds < 0 {
		return Config{}, fmt.Errorf("remote_lock_effectful_wait_seconds must be non-negative")
	}
	if cfg.RemoteLockEffectfulWaitSeconds == 0 {
		cfg.RemoteLockEffectfulWaitSeconds = DefaultRemoteLockEffectfulWaitSeconds
	}
	if strings.TrimSpace(cfg.Roots.MainSnapshots) == "" {
		cfg.Roots.MainSnapshots = DefaultMainSnapshots
	}
	if strings.TrimSpace(cfg.Roots.FullOffload) == "" {
		cfg.Roots.FullOffload = DefaultFullOffload
	}
	if strings.TrimSpace(cfg.Roots.CloudFolder) == "" {
		cfg.Roots.CloudFolder = DefaultCloudFolder
	}
	if strings.TrimSpace(cfg.Snapshots.Backend) == "" {
		cfg.Snapshots.Backend = DefaultSnapshotBackend
	}
	if strings.TrimSpace(cfg.Snapshots.Borg.Binary) == "" {
		cfg.Snapshots.Borg.Binary = DefaultBorgBinary
	}
	if strings.TrimSpace(cfg.Snapshots.Borg.CacheDir) == "" {
		cfg.Snapshots.Borg.CacheDir = filepath.Join(cfg.StateDir, "borg", "cache")
	}
	if strings.TrimSpace(cfg.Snapshots.Borg.SecurityDir) == "" {
		cfg.Snapshots.Borg.SecurityDir = filepath.Join(cfg.StateDir, "borg", "security")
	}
	if strings.TrimSpace(cfg.Snapshots.Borg.Encryption) == "" {
		cfg.Snapshots.Borg.Encryption = DefaultBorgEncryption
	}
	if strings.TrimSpace(cfg.Snapshots.Borg.Compression) == "" {
		cfg.Snapshots.Borg.Compression = DefaultBorgCompression
	}
	if strings.TrimSpace(cfg.Snapshots.Borg.CheckMode) == "" {
		cfg.Snapshots.Borg.CheckMode = DefaultBorgCheckMode
	}
	if cfg.Snapshots.Borg.CheckIntervalHours < 0 {
		return Config{}, fmt.Errorf("snapshots.borg.check_interval_hours must be non-negative")
	}
	if cfg.Snapshots.Borg.CheckIntervalHours == 0 {
		cfg.Snapshots.Borg.CheckIntervalHours = DefaultBorgCheckIntervalHours
	}
	if cfg.Snapshots.Borg.LockWaitSeconds < 0 {
		return Config{}, fmt.Errorf("snapshots.borg.lock_wait_seconds must be non-negative")
	}
	if cfg.Snapshots.Borg.LockWaitSeconds == 0 {
		cfg.Snapshots.Borg.LockWaitSeconds = DefaultBorgLockWaitSeconds
	}
	if cfg.Snapshots.Borg.InventoryCacheTTLSeconds < 0 {
		return Config{}, fmt.Errorf("snapshots.borg.inventory_cache_ttl_seconds must be non-negative")
	}
	if cfg.Snapshots.Borg.InventoryCacheTTLSeconds == 0 {
		cfg.Snapshots.Borg.InventoryCacheTTLSeconds = DefaultSnapshotInventoryCacheTTLSeconds
	}
	cfg.Provider = normalizeToken(cfg.Provider)
	cfg.Driver = normalizeToken(cfg.Driver)
	cfg.Snapshots.Backend = normalizeToken(cfg.Snapshots.Backend)
	cfg.RemoteName = strings.TrimSpace(cfg.RemoteName)
	if err := ValidateRemotePrefix("remote_root", cfg.RemoteRoot); err != nil {
		return Config{}, err
	}
	cfg.RemoteRoot = cleanRemotePrefix(cfg.RemoteRoot)
	cfg.RcloneConfigPath = filepath.Clean(strings.TrimSpace(cfg.RcloneConfigPath))
	cfg.RcloneBinary = strings.TrimSpace(cfg.RcloneBinary)
	cfg.StateDir = filepath.Clean(strings.TrimSpace(cfg.StateDir))
	cfg.RemoteLockPath = filepath.Clean(strings.TrimSpace(cfg.RemoteLockPath))
	cfg.Snapshots.Borg.Binary = strings.TrimSpace(cfg.Snapshots.Borg.Binary)
	cfg.Snapshots.Borg.Repository = strings.TrimSpace(cfg.Snapshots.Borg.Repository)
	cfg.Snapshots.Borg.PassphraseFile = cleanOptionalLocalPath(cfg.Snapshots.Borg.PassphraseFile)
	cfg.Snapshots.Borg.RSH = strings.TrimSpace(cfg.Snapshots.Borg.RSH)
	cfg.Snapshots.Borg.CacheDir = filepath.Clean(strings.TrimSpace(cfg.Snapshots.Borg.CacheDir))
	cfg.Snapshots.Borg.SecurityDir = filepath.Clean(strings.TrimSpace(cfg.Snapshots.Borg.SecurityDir))
	cfg.Snapshots.Borg.Encryption = strings.TrimSpace(cfg.Snapshots.Borg.Encryption)
	cfg.Snapshots.Borg.Compression = strings.TrimSpace(cfg.Snapshots.Borg.Compression)
	cfg.Snapshots.Borg.CheckMode = normalizeToken(cfg.Snapshots.Borg.CheckMode)
	if err := normalizeBorgRetentionConfig(&cfg); err != nil {
		return Config{}, err
	}
	if err := ValidateRemotePrefix("main_snapshots", cfg.Roots.MainSnapshots); err != nil {
		return Config{}, err
	}
	cfg.Roots.MainSnapshots = cleanRemotePrefix(cfg.Roots.MainSnapshots)
	if err := ValidateRemotePrefix("full_offload", cfg.Roots.FullOffload); err != nil {
		return Config{}, err
	}
	cfg.Roots.FullOffload = cleanRemotePrefix(cfg.Roots.FullOffload)
	if err := ValidateRemotePrefix("cloud_folder", cfg.Roots.CloudFolder); err != nil {
		return Config{}, err
	}
	cfg.Roots.CloudFolder = cleanRemotePrefix(cfg.Roots.CloudFolder)
	if cfg.Driver != DefaultDriver {
		return Config{}, fmt.Errorf("unsupported cloud driver %q", cfg.Driver)
	}
	switch cfg.Snapshots.Backend {
	case SnapshotBackendLegacyTree, SnapshotBackendBorg:
	default:
		return Config{}, fmt.Errorf("unsupported cloud snapshot backend %q", cfg.Snapshots.Backend)
	}
	return cfg, nil
}

func normalizeBorgRetentionConfig(cfg *Config) error {
	retention := &cfg.Snapshots.Borg.Retention
	if !retention.Enabled {
		if strings.TrimSpace(retention.AuthorityID) != "" || strings.TrimSpace(retention.Repository) != "" || strings.TrimSpace(retention.PassphraseFile) != "" || strings.TrimSpace(retention.RSH) != "" {
			return fmt.Errorf("snapshots.borg.retention must be enabled before retention authority fields are configured")
		}
		return nil
	}
	retention.AuthorityID = strings.TrimSpace(retention.AuthorityID)
	if retention.AuthorityID == "" || normalizeToken(retention.AuthorityID) == "writer" {
		return fmt.Errorf("snapshots.borg.retention.authority_id must name a non-writer authority")
	}
	retention.Binary = strings.TrimSpace(retention.Binary)
	if retention.Binary == "" {
		retention.Binary = cfg.Snapshots.Borg.Binary
	}
	retention.Repository = strings.TrimSpace(retention.Repository)
	if retention.Repository == "" {
		return fmt.Errorf("snapshots.borg.retention.repository is required")
	}
	if retention.Repository != cfg.Snapshots.Borg.Repository {
		return fmt.Errorf("snapshots.borg.retention.repository must exactly match the writer repository")
	}
	retention.PassphraseFile = cleanOptionalLocalPath(retention.PassphraseFile)
	if retention.PassphraseFile == "" {
		return fmt.Errorf("snapshots.borg.retention.passphrase_file is required")
	}
	if err := validateDistinctAuthorityFiles(
		"snapshots.borg.passphrase_file", cfg.Snapshots.Borg.PassphraseFile,
		"snapshots.borg.retention.passphrase_file", retention.PassphraseFile,
	); err != nil {
		return err
	}
	retention.RSH = strings.TrimSpace(retention.RSH)
	if borgRepositoryUsesSSH(retention.Repository) {
		writerIdentity, err := explicitBorgSSHIdentity("snapshots.borg.rsh", cfg.Snapshots.Borg.RSH)
		if err != nil {
			return err
		}
		retentionIdentity, err := explicitBorgSSHIdentity("snapshots.borg.retention.rsh", retention.RSH)
		if err != nil {
			return err
		}
		if err := validateDistinctAuthorityFiles(
			"snapshots.borg.rsh identity", writerIdentity,
			"snapshots.borg.retention.rsh identity", retentionIdentity,
		); err != nil {
			return err
		}
	}
	retention.CacheDir = strings.TrimSpace(retention.CacheDir)
	if retention.CacheDir == "" {
		retention.CacheDir = filepath.Join(cfg.StateDir, "borg", "retention-cache")
	}
	retention.CacheDir = filepath.Clean(retention.CacheDir)
	retention.SecurityDir = strings.TrimSpace(retention.SecurityDir)
	if retention.SecurityDir == "" {
		retention.SecurityDir = filepath.Join(cfg.StateDir, "borg", "retention-security")
	}
	retention.SecurityDir = filepath.Clean(retention.SecurityDir)
	if err := validateDistinctAuthorityStateRoots(map[string]string{
		"snapshots.borg.cache_dir":              cfg.Snapshots.Borg.CacheDir,
		"snapshots.borg.security_dir":           cfg.Snapshots.Borg.SecurityDir,
		"snapshots.borg.retention.cache_dir":    retention.CacheDir,
		"snapshots.borg.retention.security_dir": retention.SecurityDir,
	}); err != nil {
		return err
	}
	retention.CheckMode = normalizeToken(retention.CheckMode)
	if retention.CheckMode == "" {
		retention.CheckMode = DefaultBorgCheckMode
	}
	if retention.CheckMode != "repository" && retention.CheckMode != "repo" && retention.CheckMode != "repository_only" && retention.CheckMode != "full" {
		return fmt.Errorf("snapshots.borg.retention.check_mode must require a repository-wide check")
	}
	if retention.LockWaitSeconds < 0 {
		return fmt.Errorf("snapshots.borg.retention.lock_wait_seconds must be non-negative")
	}
	if retention.LockWaitSeconds == 0 {
		retention.LockWaitSeconds = DefaultBorgLockWaitSeconds
	}
	return nil
}

func validateDistinctAuthorityFiles(leftLabel, leftPath, rightLabel, rightPath string) error {
	leftInfo, leftResolved, err := authorityRegularFile(leftLabel, leftPath)
	if err != nil {
		return err
	}
	rightInfo, rightResolved, err := authorityRegularFile(rightLabel, rightPath)
	if err != nil {
		return err
	}
	if leftResolved == rightResolved || os.SameFile(leftInfo, rightInfo) {
		return fmt.Errorf("%s and %s must be separate underlying files", leftLabel, rightLabel)
	}
	return nil
}

func authorityRegularFile(label, filePath string) (os.FileInfo, string, error) {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return nil, "", fmt.Errorf("%s is required", label)
	}
	if !filepath.IsAbs(filePath) {
		return nil, "", fmt.Errorf("%s must be an absolute path", label)
	}
	linkInfo, err := os.Lstat(filePath)
	if err != nil {
		return nil, "", fmt.Errorf("inspect %s: %w", label, err)
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 {
		return nil, "", fmt.Errorf("%s must not be a symlink", label)
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return nil, "", fmt.Errorf("inspect %s: %w", label, err)
	}
	if !info.Mode().IsRegular() {
		return nil, "", fmt.Errorf("%s must identify a regular file", label)
	}
	resolved, err := filepath.EvalSymlinks(filePath)
	if err != nil {
		return nil, "", fmt.Errorf("resolve %s: %w", label, err)
	}
	return info, filepath.Clean(resolved), nil
}

func explicitBorgSSHIdentity(label, value string) (string, error) {
	args, err := splitBorgRSH(value)
	if err != nil {
		return "", fmt.Errorf("%s is invalid: %w", label, err)
	}
	if len(args) == 0 {
		return "", fmt.Errorf("%s must explicitly select a separate SSH identity file", label)
	}
	identities := []string{}
	for index := 1; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "-i":
			if index+1 >= len(args) {
				return "", fmt.Errorf("%s has -i without an identity file", label)
			}
			index++
			identities = append(identities, args[index])
		case strings.HasPrefix(arg, "-i") && len(arg) > 2:
			identities = append(identities, arg[2:])
		case arg == "-o":
			if index+1 >= len(args) {
				return "", fmt.Errorf("%s has -o without a value", label)
			}
			index++
			option := args[index]
			if value, ok := borgSSHOptionValue(option, "IdentitiesOnly"); ok && !strings.EqualFold(value, "yes") {
				return "", fmt.Errorf("%s must not disable IdentitiesOnly", label)
			}
			if value, ok := borgSSHOptionValue(option, "IdentityFile"); ok {
				identities = append(identities, value)
			}
		case strings.HasPrefix(strings.ToLower(arg), "-o"):
			option := arg[2:]
			if value, ok := borgSSHOptionValue(option, "IdentitiesOnly"); ok && !strings.EqualFold(value, "yes") {
				return "", fmt.Errorf("%s must not disable IdentitiesOnly", label)
			}
			if value, ok := borgSSHOptionValue(option, "IdentityFile"); ok {
				identities = append(identities, value)
			}
		}
	}
	if len(identities) != 1 || strings.TrimSpace(identities[0]) == "" {
		return "", fmt.Errorf("%s must explicitly select exactly one SSH identity file", label)
	}
	if !filepath.IsAbs(identities[0]) {
		return "", fmt.Errorf("%s SSH identity file must be an absolute path", label)
	}
	return filepath.Clean(identities[0]), nil
}

func borgSSHOptionValue(option, name string) (string, bool) {
	option = strings.TrimSpace(option)
	if equal := strings.IndexByte(option, '='); equal >= 0 {
		if strings.EqualFold(strings.TrimSpace(option[:equal]), name) {
			return strings.TrimSpace(option[equal+1:]), true
		}
		return "", false
	}
	fields := strings.Fields(option)
	if len(fields) == 2 && strings.EqualFold(fields[0], name) {
		return fields[1], true
	}
	return "", false
}

func splitBorgRSH(value string) ([]string, error) {
	var args []string
	var token strings.Builder
	quote := rune(0)
	escaped := false
	flush := func() {
		if token.Len() > 0 {
			args = append(args, token.String())
			token.Reset()
		}
	}
	for _, char := range strings.TrimSpace(value) {
		if escaped {
			token.WriteRune(char)
			escaped = false
			continue
		}
		if char == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if char == quote {
				quote = 0
			} else {
				token.WriteRune(char)
			}
			continue
		}
		if char == '\'' || char == '"' {
			quote = char
			continue
		}
		if char == ' ' || char == '\t' || char == '\n' || char == '\r' {
			flush()
			continue
		}
		token.WriteRune(char)
	}
	if escaped || quote != 0 {
		return nil, fmt.Errorf("unterminated quote or escape")
	}
	flush()
	return args, nil
}

func validateDistinctAuthorityStateRoots(paths map[string]string) error {
	type stateRoot struct {
		label string
		path  string
	}
	roots := make([]stateRoot, 0, len(paths))
	labels := make([]string, 0, len(paths))
	for label := range paths {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	for _, label := range labels {
		statePath := paths[label]
		resolved, err := canonicalAuthorityStateRoot(label, statePath)
		if err != nil {
			return err
		}
		roots = append(roots, stateRoot{label: label, path: resolved})
	}
	for left := 0; left < len(roots); left++ {
		for right := left + 1; right < len(roots); right++ {
			if pathsOverlap(roots[left].path, roots[right].path) {
				return fmt.Errorf("%s and %s must be distinct non-overlapping state roots", roots[left].label, roots[right].label)
			}
		}
	}
	return nil
}

func canonicalAuthorityStateRoot(label, statePath string) (string, error) {
	statePath = strings.TrimSpace(statePath)
	if statePath == "" || !filepath.IsAbs(statePath) {
		return "", fmt.Errorf("%s must be an absolute path", label)
	}
	cleaned := filepath.Clean(statePath)
	if info, err := os.Lstat(cleaned); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%s must not be a symlink", label)
		}
		resolved, err := filepath.EvalSymlinks(cleaned)
		if err != nil {
			return "", fmt.Errorf("resolve %s: %w", label, err)
		}
		return filepath.Clean(resolved), nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect %s: %w", label, err)
	}
	ancestor := cleaned
	missing := []string{}
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("inspect %s: %w", label, err)
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", fmt.Errorf("resolve %s: no existing ancestor", label)
		}
		missing = append(missing, filepath.Base(ancestor))
		ancestor = parent
	}
	resolved, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", label, err)
	}
	for index := len(missing) - 1; index >= 0; index-- {
		resolved = filepath.Join(resolved, missing[index])
	}
	return filepath.Clean(resolved), nil
}

func pathsOverlap(left, right string) bool {
	if left == right {
		return true
	}
	for _, pair := range [][2]string{{left, right}, {right, left}} {
		relative, err := filepath.Rel(pair[0], pair[1])
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func borgRepositoryUsesSSH(repository string) bool {
	repository = strings.TrimSpace(repository)
	return strings.HasPrefix(repository, "ssh://") || (!filepath.IsAbs(repository) && strings.Contains(repository, ":"))
}

func retentionAuthorityConfig(cfg Config) (Config, error) {
	normalized, err := NormalizeConfig(cfg)
	if err != nil {
		return Config{}, err
	}
	retention := normalized.Snapshots.Borg.Retention
	if !retention.Enabled {
		return Config{}, fmt.Errorf("Borg retention authority is not configured")
	}
	normalized.Snapshots.Borg.Binary = retention.Binary
	normalized.Snapshots.Borg.Repository = retention.Repository
	normalized.Snapshots.Borg.PassphraseFile = retention.PassphraseFile
	normalized.Snapshots.Borg.RSH = retention.RSH
	normalized.Snapshots.Borg.CacheDir = retention.CacheDir
	normalized.Snapshots.Borg.SecurityDir = retention.SecurityDir
	normalized.Snapshots.Borg.CheckMode = retention.CheckMode
	normalized.Snapshots.Borg.LockWaitSeconds = retention.LockWaitSeconds
	// Prevent NormalizeConfig from recursively treating the authority-specific
	// view as a second user-supplied retention block.
	normalized.Snapshots.Borg.Retention = BorgRetentionConfig{}
	return normalized, nil
}

func cleanOptionalLocalPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return filepath.Clean(value)
}

func ValidateRemotePrefix(field, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("%s must not be empty", field)
	}
	if strings.Contains(value, "\\") || strings.Contains(value, ":") {
		return fmt.Errorf("%s must be a remote prefix, not a platform path or rclone URI", field)
	}
	if strings.HasPrefix(value, "/") {
		return fmt.Errorf("%s must be relative to the configured remote", field)
	}
	for _, segment := range strings.Split(value, "/") {
		switch segment {
		case "", ".", "..":
			return fmt.Errorf("%s contains unsafe segment %q", field, segment)
		}
	}
	return nil
}

func (c Config) RemoteURI(prefix string) string {
	joined := c.RemotePath(prefix)
	return c.RemoteName + ":" + joined
}

func (c Config) RemotePath(prefix string) string {
	parts := []string{c.RemoteRoot}
	prefix = cleanRemotePrefix(prefix)
	if prefix != "" {
		parts = append(parts, prefix)
	}
	return path.Join(parts...)
}

func (r Roots) Required() []Root {
	return []Root{
		{Name: "main_snapshots", Prefix: r.MainSnapshots},
		{Name: "full_offload", Prefix: r.FullOffload},
		{Name: "cloud_folder", Prefix: r.CloudFolder},
	}
}

type Root struct {
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
}

func cleanRemotePrefix(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "/")
	if value == "" {
		return ""
	}
	return path.Clean(value)
}

func normalizeToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.NewReplacer(" ", "_", "-", "_").Replace(value)
}

func errorsIsNotExist(err error) bool {
	return err != nil && (os.IsNotExist(err) || errors.Is(err, fs.ErrNotExist))
}
