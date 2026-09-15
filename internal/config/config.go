package config

import (
	"bufio"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"loom.local/loom/internal/filesystemlayout"

	"loom.local/loom/internal/hermesprofile"
)

var restoreAuthorityIdentifierPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)
var restoreAuthorityDatabasePattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
var workspaceArchiveManifestKeyIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{2,127}$`)

const DefaultWorkspaceArchiveManifestKeyID = "workspace-archive-manifest-key-v1"

const (
	DefaultEnv                         = "dev"
	DefaultNodeID                      = "dev-main"
	DefaultNodeKind                    = "main"
	DefaultNodeRole                    = "main"
	DefaultRuntimeClass                = "main_full"
	DefaultDataDir                     = "/var/lib/loom"
	DefaultObjectStore                 = "/var/lib/loom/object-store"
	DefaultServiceRoot                 = filesystemlayout.DefaultServiceRoot
	DefaultStorageRoot                 = filesystemlayout.DefaultStorageRoot
	DefaultImportsRoot                 = filesystemlayout.DefaultImportsRoot
	DefaultUserBackupsRoot             = filesystemlayout.DefaultUserBackupsRoot
	DefaultArchiveRoot                 = filesystemlayout.DefaultArchiveRoot
	DefaultGeneratedRoot               = filesystemlayout.DefaultGeneratedRoot
	DefaultStorageExport               = "/var/lib/loom/storage-views/main-export"
	DefaultStorageRetention            = "/var/lib/loom/storage-retention"
	DefaultMainDocuments               = "/var/lib/loom/main-documents"
	DefaultNotesProjection             = "notes"
	DefaultSocketPath                  = "/run/loom/loomd.sock"
	DefaultRestoreAuthoritySocketPath  = "/run/loom-restore-authority/restore-authority.sock"
	DefaultRestoreAuthoritySocketOwner = "postgres"
	DefaultRestoreAuthoritySocketGroup = "loom"
	DefaultRestoreActivatorUser        = "root"
	DefaultRestoreExecutorUser         = "postgres"
	DefaultRestoreOperationalDatabase  = "loom_main"
	DefaultRestoreOperationalOwner     = "loom"
	DefaultRestoreProvenanceDatabase   = "loom_provenance"
	DefaultRestoreProvenanceOwner      = "loom_provenance"
	DefaultLogLevel                    = "info"
	DefaultMigrationsDir               = "migrations"
	DefaultBootstrapMode               = "none"

	DefaultEmbeddingsEnabled            = false
	DefaultEmbeddingRuntime             = "ollama"
	DefaultEmbeddingModel               = "mxbai-embed-large"
	DefaultEmbeddingOllamaURL           = "http://127.0.0.1:11434"
	DefaultEmbeddingDimensions          = 1024
	DefaultEmbeddingQuietWindowSeconds  = 600
	DefaultEmbeddingConcurrency         = 1
	DefaultVisionEnabled                = false
	DefaultVisionRuntime                = "ollama"
	DefaultVisionOllamaURL              = "http://127.0.0.1:11434"
	DefaultVisionMaxBytes               = 20 * 1024 * 1024
	DefaultVisionMaxPixels              = 40000000
	DefaultImportsBackupPolicy          = "committed_lane_custody_only"
	ImportsBackupPolicyCompletePhysical = "legacy_complete_physical_custody"
)

type Config struct {
	ApplicationDataBackupRoot string

	MinaSelected          bool
	MinaEnabled           bool
	MinaRecoveryEnabled   bool
	MinaRecoveryPublicKey string
	// JSON at the configuration boundary; no delimiter splitting or discovery.
	MinaRecoveryRetainedMorathustraIDs string

	MorathustraEnabled           bool
	MorathustraRecoveryEnabled   bool
	MorathustraRecoveryPublicKey string
	Env                          string
	NodeID                       string
	NodeKind                     string
	NodeRole                     string
	RuntimeClass                 string
	DataDir                      string
	ObjectStore                  string
	ServiceRoot                  string
	StorageRoot                  string
	CanonicalUserBackupsRoot     string
	ImportsBackupPolicy          string
	ImportsRoot                  string
	UserBackupsRoot              string
	ArchiveRoot                  string
	GeneratedRoot                string
	BoxStateRoot                 string
	StorageExport                string
	StorageRetention             string
	MainDocuments                string
	NotesProjection              string
	BoxPath                      string
	BoxProfile                   string
	MainURL                      string
	DBURL                        string
	ProvenanceDBURL              string
	RestoreAuthoritySocketPath   string
	RestoreAuthoritySocketOwner  string
	RestoreAuthoritySocketGroup  string
	RestoreActivatorUser         string
	RestoreExecutorUser          string
	RestoreOperationalDatabase   string
	RestoreOperationalOwner      string
	RestoreProvenanceDatabase    string
	RestoreProvenanceOwner       string
	SocketPath                   string
	HTTPListenAddr               string
	ServiceAllowlist             string
	LogLevel                     string
	ConfigFile                   string
	MigrationsDir                string
	AutoMigrate                  bool
	BootstrapDev                 bool
	BootstrapMode                string
	LegacySplitRoots             bool

	WorkspaceArchiveEnabled       bool
	WorkspaceArchiveManifestKeyID string

	EmbeddingsEnabled           bool
	EmbeddingRuntime            string
	EmbeddingModel              string
	EmbeddingOllamaURL          string
	EmbeddingDimensions         int
	EmbeddingQuietWindowSeconds int
	EmbeddingConcurrency        int
	VisionEnabled               bool
	VisionRuntime               string
	VisionModel                 string
	VisionOllamaURL             string
	VisionMaxBytes              int
	VisionMaxPixels             int
}

type Overrides struct {
	ApplicationDataBackupRoot   string
	Env                         string
	NodeID                      string
	NodeKind                    string
	NodeRole                    string
	RuntimeClass                string
	DataDir                     string
	ObjectStore                 string
	ServiceRoot                 string
	StorageRoot                 string
	CanonicalUserBackupsRoot    string
	ImportsBackupPolicy         string
	ImportsRoot                 string
	UserBackupsRoot             string
	ArchiveRoot                 string
	GeneratedRoot               string
	BoxStateRoot                string
	StorageExport               string
	StorageRetention            string
	MainDocuments               string
	NotesProjection             string
	BoxPath                     string
	BoxProfile                  string
	MainURL                     string
	DBURL                       string
	ProvenanceDBURL             string
	RestoreAuthoritySocketPath  string
	RestoreAuthoritySocketOwner string
	RestoreAuthoritySocketGroup string
	RestoreActivatorUser        string
	RestoreExecutorUser         string
	RestoreOperationalDatabase  string
	RestoreOperationalOwner     string
	RestoreProvenanceDatabase   string
	RestoreProvenanceOwner      string
	SocketPath                  string
	HTTPListenAddr              string
	ServiceAllowlist            string
	LogLevel                    string
	ConfigFile                  string
	MigrationsDir               string
	AutoMigrate                 string
	BootstrapDev                string
	BootstrapMode               string
	LegacySplitRoots            string

	WorkspaceArchiveEnabled       string
	WorkspaceArchiveManifestKeyID string

	EmbeddingsEnabled           string
	EmbeddingRuntime            string
	EmbeddingModel              string
	EmbeddingOllamaURL          string
	EmbeddingDimensions         string
	EmbeddingQuietWindowSeconds string
	EmbeddingConcurrency        string
	VisionEnabled               string
	VisionRuntime               string
	VisionModel                 string
	VisionOllamaURL             string
	VisionMaxBytes              string
	VisionMaxPixels             string
}

func Load(overrides Overrides) (Config, error) {
	cfg := Config{
		Env:                         DefaultEnv,
		NodeID:                      DefaultNodeID,
		NodeKind:                    DefaultNodeKind,
		NodeRole:                    DefaultNodeRole,
		RuntimeClass:                DefaultRuntimeClass,
		DataDir:                     DefaultDataDir,
		ObjectStore:                 DefaultObjectStore,
		ServiceRoot:                 DefaultServiceRoot,
		StorageExport:               DefaultStorageExport,
		StorageRetention:            DefaultStorageRetention,
		MainDocuments:               DefaultMainDocuments,
		SocketPath:                  DefaultSocketPath,
		RestoreAuthoritySocketPath:  DefaultRestoreAuthoritySocketPath,
		RestoreAuthoritySocketOwner: DefaultRestoreAuthoritySocketOwner,
		RestoreAuthoritySocketGroup: DefaultRestoreAuthoritySocketGroup,
		RestoreActivatorUser:        DefaultRestoreActivatorUser,
		RestoreExecutorUser:         DefaultRestoreExecutorUser,
		RestoreOperationalDatabase:  DefaultRestoreOperationalDatabase,
		RestoreOperationalOwner:     DefaultRestoreOperationalOwner,
		RestoreProvenanceDatabase:   DefaultRestoreProvenanceDatabase,
		RestoreProvenanceOwner:      DefaultRestoreProvenanceOwner,
		LogLevel:                    DefaultLogLevel,
		MigrationsDir:               DefaultMigrationsDir,
		BootstrapMode:               DefaultBootstrapMode,

		WorkspaceArchiveManifestKeyID: DefaultWorkspaceArchiveManifestKeyID,

		ImportsBackupPolicy: DefaultImportsBackupPolicy,

		EmbeddingsEnabled:           DefaultEmbeddingsEnabled,
		EmbeddingRuntime:            DefaultEmbeddingRuntime,
		EmbeddingModel:              DefaultEmbeddingModel,
		EmbeddingOllamaURL:          DefaultEmbeddingOllamaURL,
		EmbeddingDimensions:         DefaultEmbeddingDimensions,
		EmbeddingQuietWindowSeconds: DefaultEmbeddingQuietWindowSeconds,
		EmbeddingConcurrency:        DefaultEmbeddingConcurrency,
		VisionEnabled:               DefaultVisionEnabled,
		VisionRuntime:               DefaultVisionRuntime,
		VisionOllamaURL:             DefaultVisionOllamaURL,
		VisionMaxBytes:              DefaultVisionMaxBytes,
		VisionMaxPixels:             DefaultVisionMaxPixels,
	}

	configFile := firstNonEmpty(overrides.ConfigFile, os.Getenv("LOOM_CONFIG_FILE"))
	if configFile != "" {
		values, err := readEnvFile(configFile)
		if err != nil {
			return Config{}, err
		}
		if err := applyMap(&cfg, values); err != nil {
			return Config{}, err
		}
		cfg.ConfigFile = configFile
	}

	if err := applyEnv(&cfg); err != nil {
		return Config{}, err
	}
	if err := applyOverrides(&cfg, overrides); err != nil {
		return Config{}, err
	}
	applyLayoutDefaults(&cfg)

	if cfg.ConfigFile != "" {
		cfg.ConfigFile = filepath.Clean(cfg.ConfigFile)
	}

	return cfg, cfg.Validate()
}

func (c Config) Validate() error {
	if _, err := c.HermesRecoveryPolicy(); err != nil {
		return err
	}
	var missing []string
	if strings.TrimSpace(c.Env) == "" {
		missing = append(missing, "LOOM_ENV")
	}
	if strings.TrimSpace(c.NodeID) == "" {
		missing = append(missing, "LOOM_NODE_ID")
	}
	if strings.TrimSpace(c.NodeKind) == "" {
		missing = append(missing, "LOOM_NODE_KIND")
	}
	if strings.TrimSpace(c.NodeRole) == "" {
		missing = append(missing, "LOOM_NODE_ROLE")
	}
	if strings.TrimSpace(c.RuntimeClass) == "" {
		missing = append(missing, "LOOM_RUNTIME_CLASS")
	}
	if strings.TrimSpace(c.DataDir) == "" {
		missing = append(missing, "LOOM_DATA_DIR")
	}
	if strings.TrimSpace(c.ObjectStore) == "" {
		missing = append(missing, "LOOM_OBJECT_STORE")
	}
	if strings.TrimSpace(c.ServiceRoot) == "" {
		missing = append(missing, "LOOM_SERVICE_ROOT")
	}
	if strings.TrimSpace(c.StorageRoot) == "" {
		missing = append(missing, "LOOM_STORAGE_ROOT")
	}
	if strings.TrimSpace(c.ImportsRoot) == "" {
		missing = append(missing, "LOOM_IMPORTS_ROOT")
	}
	if policy := c.EffectiveImportsBackupPolicy(); policy != DefaultImportsBackupPolicy && policy != ImportsBackupPolicyCompletePhysical {
		return fmt.Errorf("invalid LOOM_IMPORTS_BACKUP_POLICY %q", c.ImportsBackupPolicy)
	}
	if strings.TrimSpace(c.UserBackupsRoot) == "" {
		missing = append(missing, "LOOM_USER_BACKUPS_ROOT")
	}
	if strings.TrimSpace(c.ArchiveRoot) == "" {
		missing = append(missing, "LOOM_ARCHIVE_ROOT")
	}
	if strings.TrimSpace(c.GeneratedRoot) == "" {
		missing = append(missing, "LOOM_GENERATED_ROOT")
	}
	if strings.TrimSpace(c.StorageExport) == "" {
		missing = append(missing, "LOOM_STORAGE_EXPORT_ROOT")
	}
	if strings.TrimSpace(c.StorageRetention) == "" {
		missing = append(missing, "LOOM_STORAGE_RETENTION_ROOT")
	}
	if strings.TrimSpace(c.MainDocuments) == "" {
		missing = append(missing, "LOOM_MAIN_DOCUMENTS_ROOT")
	}
	if strings.TrimSpace(c.SocketPath) == "" {
		missing = append(missing, "LOOM_SOCKET_PATH")
	}
	if strings.TrimSpace(c.MigrationsDir) == "" {
		missing = append(missing, "LOOM_MIGRATIONS_DIR")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required config: %s", strings.Join(missing, ", "))
	}
	if _, err := c.FilesystemLayout(); err != nil {
		return fmt.Errorf("invalid canonical filesystem layout: %w", err)
	}
	if err := c.validateCustodyTransition(); err != nil {
		return err
	}
	if err := validateHTTPListenAddr(c.HTTPListenAddr); err != nil {
		return err
	}
	if err := validateBootstrapMode(c.BootstrapMode); err != nil {
		return err
	}
	if err := validateRestoreAuthorityConfig(c); err != nil {
		return err
	}
	if err := validateEmbeddingConfig(c); err != nil {
		return err
	}
	if err := validateVisionConfig(c); err != nil {
		return err
	}
	if err := validateWorkspaceArchiveConfig(c); err != nil {
		return err
	}
	return nil
}

func validateWorkspaceArchiveConfig(c Config) error {
	keyID := strings.TrimSpace(c.WorkspaceArchiveManifestKeyID)
	if keyID == "" && !c.WorkspaceArchiveEnabled {
		return nil
	}
	if !workspaceArchiveManifestKeyIDPattern.MatchString(keyID) {
		return fmt.Errorf("invalid LOOM_WORKSPACE_ARCHIVE_MANIFEST_KEY_ID")
	}
	if c.WorkspaceArchiveEnabled && strings.TrimSpace(c.BoxPath) == "" {
		return fmt.Errorf("LOOM_WORKSPACE_ARCHIVE_ENABLED requires LOOM_BOX_PATH")
	}
	return nil
}

func validateRestoreAuthorityConfig(c Config) error {
	socketPath := firstNonEmpty(c.RestoreAuthoritySocketPath, DefaultRestoreAuthoritySocketPath)
	socketOwner := firstNonEmpty(c.RestoreAuthoritySocketOwner, DefaultRestoreAuthoritySocketOwner)
	socketGroup := firstNonEmpty(c.RestoreAuthoritySocketGroup, DefaultRestoreAuthoritySocketGroup)
	socketActivatorUser := firstNonEmpty(c.RestoreActivatorUser, DefaultRestoreActivatorUser)
	executorUser := firstNonEmpty(c.RestoreExecutorUser, DefaultRestoreExecutorUser)
	operationalDatabase := firstNonEmpty(c.RestoreOperationalDatabase, DefaultRestoreOperationalDatabase)
	operationalOwner := firstNonEmpty(c.RestoreOperationalOwner, DefaultRestoreOperationalOwner)
	provenanceDatabase := firstNonEmpty(c.RestoreProvenanceDatabase, DefaultRestoreProvenanceDatabase)
	provenanceOwner := firstNonEmpty(c.RestoreProvenanceOwner, DefaultRestoreProvenanceOwner)
	if socketPath != DefaultRestoreAuthoritySocketPath {
		return fmt.Errorf("LOOM_RESTORE_AUTHORITY_SOCKET_PATH must use fixed reviewed path %q", DefaultRestoreAuthoritySocketPath)
	}
	for key, value := range map[string]string{
		"LOOM_RESTORE_AUTHORITY_SOCKET_OWNER":          socketOwner,
		"LOOM_RESTORE_AUTHORITY_SOCKET_GROUP":          socketGroup,
		"LOOM_RESTORE_AUTHORITY_SOCKET_ACTIVATOR_USER": socketActivatorUser,
		"LOOM_RESTORE_AUTHORITY_EXECUTOR_USER":         executorUser,
	} {
		if len(value) == 0 || len(value) > 63 || !restoreAuthorityIdentifierPattern.MatchString(value) {
			return fmt.Errorf("invalid %s %q", key, value)
		}
	}
	if socketOwner != "postgres" || socketActivatorUser != "root" || executorUser != "postgres" {
		return fmt.Errorf("restore authority socket owner, systemd activator, and executor identities must remain postgres, root, and postgres")
	}
	for key, value := range map[string]string{
		"LOOM_RESTORE_OPERATIONAL_DATABASE": operationalDatabase,
		"LOOM_RESTORE_OPERATIONAL_OWNER":    operationalOwner,
		"LOOM_RESTORE_PROVENANCE_DATABASE":  provenanceDatabase,
		"LOOM_RESTORE_PROVENANCE_OWNER":     provenanceOwner,
	} {
		if len(value) == 0 || len(value) > 63 || !restoreAuthorityDatabasePattern.MatchString(value) {
			return fmt.Errorf("invalid %s %q", key, value)
		}
	}
	for _, database := range []string{operationalDatabase, provenanceDatabase} {
		if database == "postgres" || database == "template0" || database == "template1" {
			return fmt.Errorf("restore authority active database cannot be a PostgreSQL system database")
		}
		if strings.HasPrefix(database, "loom_restore_drill_") || strings.HasPrefix(database, "loom_provenance_restore_drill_") {
			return fmt.Errorf("restore authority active database cannot use a disposable namespace")
		}
	}
	if operationalDatabase == provenanceDatabase || operationalOwner == provenanceOwner {
		return fmt.Errorf("restore authority operational and Provenance identities must remain distinct")
	}
	if operationalOwner == "postgres" || provenanceOwner == "postgres" {
		return fmt.Errorf("restore authority disposable databases cannot be owned by postgres")
	}
	return nil
}

func (c Config) SafeFields() map[string]string {
	fields := map[string]string{
		"env":                         c.Env,
		"node_id":                     c.NodeID,
		"node_kind":                   c.NodeKind,
		"node_role":                   c.NodeRole,
		"runtime_class":               c.RuntimeClass,
		"data_dir":                    c.DataDir,
		"object_store":                c.ObjectStore,
		"service_root":                c.ServiceRoot,
		"storage_root":                c.StorageRoot,
		"canonical_user_backups_root": c.EffectiveCanonicalUserBackupsRoot(),
		"imports_backup_policy":       c.EffectiveImportsBackupPolicy(),
		"imports_root":                c.ImportsRoot,
		"user_backups_root":           c.UserBackupsRoot,
		"archive_root":                c.ArchiveRoot,
		"generated_root":              c.GeneratedRoot,
		"box_state_root":              c.BoxStateRoot,
		"storage_export":              c.StorageExport,
		"storage_retention":           c.StorageRetention,
		"main_documents":              c.MainDocuments,
		"active_main_documents":       c.MainDocumentsRoot(),
		"box_notes":                   c.BoxNotesRoot(),
		"notes_projection":            c.NotesProjectionRoot(),
		"box_path":                    c.BoxPath,
		"box_profile":                 c.BoxProfile,
		"main_url":                    safeDiagnosticURL(c.MainURL),
		"socket_path":                 c.SocketPath,
		"http_listen_addr":            c.HTTPListenAddr,
		"service_allowlist":           c.ServiceAllowlist,
		"http_enabled":                fmt.Sprintf("%t", strings.TrimSpace(c.HTTPListenAddr) != ""),
		"log_level":                   c.LogLevel,
		"config_file":                 c.ConfigFile,
		"migrations_dir":              c.MigrationsDir,
		"auto_migrate":                fmt.Sprintf("%t", c.AutoMigrate),
		"bootstrap_dev":               fmt.Sprintf("%t", c.BootstrapDev),
		"bootstrap_mode":              c.BootstrapMode,
		"legacy_split_roots":          fmt.Sprintf("%t", c.LegacySplitRoots),

		"workspace_archive_enabled":         fmt.Sprintf("%t", c.WorkspaceArchiveEnabled),
		"workspace_archive_manifest_key_id": c.WorkspaceArchiveManifestKeyID,

		"db_configured":                           fmt.Sprintf("%t", strings.TrimSpace(c.DBURL) != ""),
		"provenance_db_configured":                fmt.Sprintf("%t", strings.TrimSpace(c.ProvenanceDBURL) != ""),
		"restore_authority_socket_path":           c.RestoreAuthoritySocketPath,
		"restore_authority_socket_owner":          c.RestoreAuthoritySocketOwner,
		"restore_authority_socket_group":          c.RestoreAuthoritySocketGroup,
		"restore_authority_socket_activator_user": c.RestoreActivatorUser,
		"restore_authority_executor_user":         c.RestoreExecutorUser,
		"restore_operational_database":            c.RestoreOperationalDatabase,
		"restore_operational_owner":               c.RestoreOperationalOwner,
		"restore_provenance_database":             c.RestoreProvenanceDatabase,
		"restore_provenance_owner":                c.RestoreProvenanceOwner,

		"embeddings_enabled":             fmt.Sprintf("%t", c.EmbeddingsEnabled),
		"embedding_runtime":              c.EmbeddingRuntime,
		"embedding_model":                c.EmbeddingModel,
		"embedding_ollama_url":           safeDiagnosticURL(c.EmbeddingOllamaURL),
		"embedding_dimensions":           strconv.Itoa(c.EmbeddingDimensions),
		"embedding_quiet_window_seconds": strconv.Itoa(c.EmbeddingQuietWindowSeconds),
		"embedding_concurrency":          strconv.Itoa(c.EmbeddingConcurrency),
		"vision_enabled":                 fmt.Sprintf("%t", c.VisionEnabled),
		"vision_runtime":                 c.VisionRuntime,
		"vision_model":                   c.VisionModel,
		"vision_ollama_url":              safeDiagnosticURL(c.VisionOllamaURL),
		"vision_max_bytes":               strconv.Itoa(c.VisionMaxBytes),
		"vision_max_pixels":              strconv.Itoa(c.VisionMaxPixels),
	}
	if layout, err := c.FilesystemLayout(); err == nil {
		for key, value := range layout.SafeFields() {
			fields[key] = value
		}
	}
	return fields
}

func safeDiagnosticURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "configured_invalid_url"
	}
	parsed.User = nil
	parsed.Path = ""
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String()
}

func (c Config) FilesystemLayout() (filesystemlayout.Layout, error) {
	return filesystemlayout.New(filesystemlayout.Options{
		ServiceRoot:                 c.ServiceRoot,
		BoxRoot:                     c.BoxPath,
		StorageRoot:                 c.StorageRoot,
		ImportsRoot:                 c.ImportsRoot,
		UserBackupsRoot:             c.UserBackupsRoot,
		ArchiveRoot:                 c.ArchiveRoot,
		DataRoot:                    c.DataDir,
		BoxStateRoot:                c.BoxStateRoot,
		GeneratedRoot:               c.GeneratedRoot,
		StorageRetentionRoot:        c.StorageRetention,
		NotesProjectionRoot:         c.NotesProjectionRoot(),
		LegacySplitRoots:            c.LegacySplitRoots,
		ExternalNotesProjection:     strings.TrimSpace(c.NotesProjection) != "",
		DeprecatedStorageExportRoot: c.StorageExport,
		DeprecatedMainDocumentsRoot: c.MainDocuments,
	})
}

// validateCustodyTransition recognizes only the complete production transition
// layout. StorageRoot and CanonicalUserBackupsRoot remain canonical identities;
// only the explicitly named active custody roots may remain legacy.
func (c Config) validateCustodyTransition() error {
	dataRoot := filepath.Clean(strings.TrimSpace(c.DataDir))
	storageRoot := filepath.Clean(strings.TrimSpace(c.StorageRoot))
	canonicalUserBackupsRoot := filepath.Clean(strings.TrimSpace(c.EffectiveCanonicalUserBackupsRoot()))
	if !c.LegacySplitRoots {
		if canonicalUserBackupsRoot != filepath.Clean(strings.TrimSpace(c.UserBackupsRoot)) {
			return fmt.Errorf("LOOM_CANONICAL_USER_BACKUPS_ROOT must equal LOOM_USER_BACKUPS_ROOT outside the explicit legacy split-root transition")
		}
		return nil
	}
	if dataRoot == "." || dataRoot == "" || storageRoot == dataRoot || !pathStrictlyWithin(storageRoot, canonicalUserBackupsRoot) ||
		filepath.Clean(strings.TrimSpace(c.BoxStateRoot)) != filepath.Join(filepath.Clean(strings.TrimSpace(c.BoxPath)), ".loom", "state") ||
		filepath.Clean(strings.TrimSpace(c.ImportsRoot)) != filepath.Join(dataRoot, "lane", "accepted") ||
		filepath.Clean(strings.TrimSpace(c.UserBackupsRoot)) != filepath.Join(dataRoot, "private-backups") ||
		filepath.Clean(strings.TrimSpace(c.ArchiveRoot)) != filepath.Join(dataRoot, "storage-archive") ||
		filepath.Clean(strings.TrimSpace(c.NotesProjectionRoot())) != filepath.Join(dataRoot, "loom-notes") {
		return fmt.Errorf("LOOM_LEGACY_SPLIT_ROOTS requires the complete legacy custody root set and a distinct canonical LOOM_STORAGE_ROOT")
	}
	return nil
}

func pathStrictlyWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != "." && rel != "" && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// EffectiveCanonicalUserBackupsRoot preserves direct Config fixture and
// embedding compatibility. Loaded production config always carries the field;
// canonical callers that omit it retain the established UserBackupsRoot.
func (c Config) EffectiveCanonicalUserBackupsRoot() string {
	if root := strings.TrimSpace(c.CanonicalUserBackupsRoot); root != "" {
		return filepath.Clean(root)
	}
	return filepath.Clean(strings.TrimSpace(c.UserBackupsRoot))
}

// EffectiveImportsBackupPolicy keeps direct Config construction compatible
// while ensuring filesystem cutover never implicitly changes custody
// provenance. Existing production explicitly selects complete-physical mode;
// new installations default to strict committed Lane custody.
func (c Config) EffectiveImportsBackupPolicy() string {
	if policy := strings.TrimSpace(c.ImportsBackupPolicy); policy != "" {
		return policy
	}
	return DefaultImportsBackupPolicy
}

func (c Config) NotesProjectionRoot() string {
	if strings.TrimSpace(c.NotesProjection) != "" {
		return filepath.Clean(c.NotesProjection)
	}
	generatedRoot := strings.TrimSpace(c.GeneratedRoot)
	if generatedRoot == "" {
		dataDir := strings.TrimSpace(c.DataDir)
		if dataDir == "" {
			dataDir = DefaultDataDir
		}
		generatedRoot = filepath.Join(dataDir, "generated")
	}
	return filepath.Join(generatedRoot, DefaultNotesProjection)
}

// MainDocumentsRoot returns the active canonical Documents source. The
// MainDocuments field remains the deprecated legacy migration input.
func (c Config) MainDocumentsRoot() string {
	if layout, err := c.FilesystemLayout(); err == nil {
		if root, err := layout.BoxRoot.Join("Documents"); err == nil {
			return root
		}
	}
	return filepath.Join(canonicalBoxRootFallback(c), "Documents")
}

// BoxNotesRoot returns the canonical Box Notes source root.
func (c Config) BoxNotesRoot() string {
	if layout, err := c.FilesystemLayout(); err == nil {
		if root, err := layout.BoxRoot.Join("Notes"); err == nil {
			return root
		}
	}
	return filepath.Join(canonicalBoxRootFallback(c), "Notes")
}

func canonicalBoxRootFallback(c Config) string {
	if root := strings.TrimSpace(c.BoxPath); root != "" {
		return filepath.Clean(root)
	}
	serviceRoot := strings.TrimSpace(c.ServiceRoot)
	if serviceRoot == "" {
		serviceRoot = DefaultServiceRoot
	}
	return filepath.Join(serviceRoot, "box")
}

func applyEnv(cfg *Config) error {
	return applyMap(cfg, map[string]string{
		"LOOM_ENV":                         os.Getenv("LOOM_ENV"),
		"LOOM_NODE_ID":                     os.Getenv("LOOM_NODE_ID"),
		"LOOM_NODE_KIND":                   os.Getenv("LOOM_NODE_KIND"),
		"LOOM_NODE_ROLE":                   os.Getenv("LOOM_NODE_ROLE"),
		"LOOM_RUNTIME_CLASS":               os.Getenv("LOOM_RUNTIME_CLASS"),
		"LOOM_DATA_DIR":                    os.Getenv("LOOM_DATA_DIR"),
		"LOOM_OBJECT_STORE":                os.Getenv("LOOM_OBJECT_STORE"),
		"LOOM_SERVICE_ROOT":                os.Getenv("LOOM_SERVICE_ROOT"),
		"LOOM_STORAGE_ROOT":                os.Getenv("LOOM_STORAGE_ROOT"),
		"LOOM_CANONICAL_USER_BACKUPS_ROOT": os.Getenv("LOOM_CANONICAL_USER_BACKUPS_ROOT"),
		"LOOM_IMPORTS_BACKUP_POLICY":       os.Getenv("LOOM_IMPORTS_BACKUP_POLICY"),
		"LOOM_IMPORTS_ROOT":                os.Getenv("LOOM_IMPORTS_ROOT"),
		"LOOM_USER_BACKUPS_ROOT":           os.Getenv("LOOM_USER_BACKUPS_ROOT"),
		"LOOM_ARCHIVE_ROOT":                os.Getenv("LOOM_ARCHIVE_ROOT"),
		"LOOM_GENERATED_ROOT":              os.Getenv("LOOM_GENERATED_ROOT"),
		"LOOM_BOX_STATE_ROOT":              os.Getenv("LOOM_BOX_STATE_ROOT"),
		"LOOM_STORAGE_EXPORT_ROOT":         os.Getenv("LOOM_STORAGE_EXPORT_ROOT"),
		"LOOM_STORAGE_RETENTION_ROOT":      os.Getenv("LOOM_STORAGE_RETENTION_ROOT"),
		"LOOM_MAIN_DOCUMENTS_ROOT":         os.Getenv("LOOM_MAIN_DOCUMENTS_ROOT"),
		"LOOM_NOTES_PROJECTION_ROOT":       os.Getenv("LOOM_NOTES_PROJECTION_ROOT"),
		"LOOM_BOX_PATH":                    os.Getenv("LOOM_BOX_PATH"),
		"LOOM_BOX_PROFILE":                 os.Getenv("LOOM_BOX_PROFILE"),
		"LOOM_MAIN_URL":                    os.Getenv("LOOM_MAIN_URL"),
		"LOOM_DB_URL":                      os.Getenv("LOOM_DB_URL"),
		"LOOM_PROVENANCE_DB_URL":           os.Getenv("LOOM_PROVENANCE_DB_URL"),

		"LOOM_APPLICATION_DATA_BACKUP_ROOT": os.Getenv("LOOM_APPLICATION_DATA_BACKUP_ROOT"),

		"LOOM_RESTORE_AUTHORITY_SOCKET_PATH":           os.Getenv("LOOM_RESTORE_AUTHORITY_SOCKET_PATH"),
		"LOOM_RESTORE_AUTHORITY_SOCKET_OWNER":          os.Getenv("LOOM_RESTORE_AUTHORITY_SOCKET_OWNER"),
		"LOOM_RESTORE_AUTHORITY_SOCKET_GROUP":          os.Getenv("LOOM_RESTORE_AUTHORITY_SOCKET_GROUP"),
		"LOOM_RESTORE_AUTHORITY_SOCKET_ACTIVATOR_USER": os.Getenv("LOOM_RESTORE_AUTHORITY_SOCKET_ACTIVATOR_USER"),
		"LOOM_RESTORE_AUTHORITY_EXECUTOR_USER":         os.Getenv("LOOM_RESTORE_AUTHORITY_EXECUTOR_USER"),
		"LOOM_RESTORE_OPERATIONAL_DATABASE":            os.Getenv("LOOM_RESTORE_OPERATIONAL_DATABASE"),
		"LOOM_RESTORE_OPERATIONAL_OWNER":               os.Getenv("LOOM_RESTORE_OPERATIONAL_OWNER"),
		"LOOM_RESTORE_PROVENANCE_DATABASE":             os.Getenv("LOOM_RESTORE_PROVENANCE_DATABASE"),
		"LOOM_RESTORE_PROVENANCE_OWNER":                os.Getenv("LOOM_RESTORE_PROVENANCE_OWNER"),

		"LOOM_SOCKET_PATH":            os.Getenv("LOOM_SOCKET_PATH"),
		"LOOM_HTTP_LISTEN_ADDR":       os.Getenv("LOOM_HTTP_LISTEN_ADDR"),
		"LOOM_SERVICE_ALLOWLIST_PATH": os.Getenv("LOOM_SERVICE_ALLOWLIST_PATH"),
		"LOOM_LOG_LEVEL":              os.Getenv("LOOM_LOG_LEVEL"),
		"LOOM_CONFIG_FILE":            os.Getenv("LOOM_CONFIG_FILE"),
		"LOOM_MIGRATIONS_DIR":         os.Getenv("LOOM_MIGRATIONS_DIR"),
		"LOOM_AUTO_MIGRATE":           os.Getenv("LOOM_AUTO_MIGRATE"),
		"LOOM_BOOTSTRAP_DEV":          os.Getenv("LOOM_BOOTSTRAP_DEV"),
		"LOOM_BOOTSTRAP_MODE":         os.Getenv("LOOM_BOOTSTRAP_MODE"),
		"LOOM_LEGACY_SPLIT_ROOTS":     os.Getenv("LOOM_LEGACY_SPLIT_ROOTS"),

		"LOOM_MINA_SELECTED":                          os.Getenv("LOOM_MINA_SELECTED"),
		"LOOM_MINA_ENABLED":                           os.Getenv("LOOM_MINA_ENABLED"),
		"LOOM_MINA_RECOVERY_ENABLED":                  os.Getenv("LOOM_MINA_RECOVERY_ENABLED"),
		"LOOM_MINA_RECOVERY_PUBLIC_KEY":               os.Getenv("LOOM_MINA_RECOVERY_PUBLIC_KEY"),
		"LOOM_MINA_RECOVERY_RETAINED_MORATHUSTRA_IDS": os.Getenv("LOOM_MINA_RECOVERY_RETAINED_MORATHUSTRA_IDS"),

		"LOOM_MORATHUSTRA_ENABLED":               os.Getenv("LOOM_MORATHUSTRA_ENABLED"),
		"LOOM_MORATHUSTRA_RECOVERY_ENABLED":      os.Getenv("LOOM_MORATHUSTRA_RECOVERY_ENABLED"),
		"LOOM_MORATHUSTRA_RECOVERY_PUBLIC_KEY":   os.Getenv("LOOM_MORATHUSTRA_RECOVERY_PUBLIC_KEY"),
		"LOOM_WORKSPACE_ARCHIVE_ENABLED":         os.Getenv("LOOM_WORKSPACE_ARCHIVE_ENABLED"),
		"LOOM_WORKSPACE_ARCHIVE_MANIFEST_KEY_ID": os.Getenv("LOOM_WORKSPACE_ARCHIVE_MANIFEST_KEY_ID"),

		"LOOM_EMBEDDINGS_ENABLED":             os.Getenv("LOOM_EMBEDDINGS_ENABLED"),
		"LOOM_EMBEDDING_RUNTIME":              os.Getenv("LOOM_EMBEDDING_RUNTIME"),
		"LOOM_EMBEDDING_MODEL":                os.Getenv("LOOM_EMBEDDING_MODEL"),
		"LOOM_EMBEDDING_OLLAMA_URL":           os.Getenv("LOOM_EMBEDDING_OLLAMA_URL"),
		"LOOM_EMBEDDING_DIMENSIONS":           os.Getenv("LOOM_EMBEDDING_DIMENSIONS"),
		"LOOM_EMBEDDING_QUIET_WINDOW_SECONDS": os.Getenv("LOOM_EMBEDDING_QUIET_WINDOW_SECONDS"),
		"LOOM_EMBEDDING_CONCURRENCY":          os.Getenv("LOOM_EMBEDDING_CONCURRENCY"),
		"LOOM_VISION_ENABLED":                 os.Getenv("LOOM_VISION_ENABLED"),
		"LOOM_VISION_RUNTIME":                 os.Getenv("LOOM_VISION_RUNTIME"),
		"LOOM_VISION_MODEL":                   os.Getenv("LOOM_VISION_MODEL"),
		"LOOM_VISION_OLLAMA_URL":              os.Getenv("LOOM_VISION_OLLAMA_URL"),
		"LOOM_VISION_MAX_BYTES":               os.Getenv("LOOM_VISION_MAX_BYTES"),
		"LOOM_VISION_MAX_PIXELS":              os.Getenv("LOOM_VISION_MAX_PIXELS"),
	})
}

func applyOverrides(cfg *Config, overrides Overrides) error {
	return applyMap(cfg, map[string]string{
		"LOOM_ENV":                         overrides.Env,
		"LOOM_NODE_ID":                     overrides.NodeID,
		"LOOM_NODE_KIND":                   overrides.NodeKind,
		"LOOM_NODE_ROLE":                   overrides.NodeRole,
		"LOOM_RUNTIME_CLASS":               overrides.RuntimeClass,
		"LOOM_DATA_DIR":                    overrides.DataDir,
		"LOOM_OBJECT_STORE":                overrides.ObjectStore,
		"LOOM_SERVICE_ROOT":                overrides.ServiceRoot,
		"LOOM_STORAGE_ROOT":                overrides.StorageRoot,
		"LOOM_CANONICAL_USER_BACKUPS_ROOT": overrides.CanonicalUserBackupsRoot,
		"LOOM_IMPORTS_BACKUP_POLICY":       overrides.ImportsBackupPolicy,
		"LOOM_IMPORTS_ROOT":                overrides.ImportsRoot,
		"LOOM_USER_BACKUPS_ROOT":           overrides.UserBackupsRoot,
		"LOOM_ARCHIVE_ROOT":                overrides.ArchiveRoot,
		"LOOM_GENERATED_ROOT":              overrides.GeneratedRoot,
		"LOOM_BOX_STATE_ROOT":              overrides.BoxStateRoot,
		"LOOM_STORAGE_EXPORT_ROOT":         overrides.StorageExport,
		"LOOM_STORAGE_RETENTION_ROOT":      overrides.StorageRetention,
		"LOOM_MAIN_DOCUMENTS_ROOT":         overrides.MainDocuments,
		"LOOM_NOTES_PROJECTION_ROOT":       overrides.NotesProjection,
		"LOOM_BOX_PATH":                    overrides.BoxPath,
		"LOOM_BOX_PROFILE":                 overrides.BoxProfile,
		"LOOM_MAIN_URL":                    overrides.MainURL,
		"LOOM_DB_URL":                      overrides.DBURL,
		"LOOM_PROVENANCE_DB_URL":           overrides.ProvenanceDBURL,

		"LOOM_APPLICATION_DATA_BACKUP_ROOT": overrides.ApplicationDataBackupRoot,

		"LOOM_RESTORE_AUTHORITY_SOCKET_PATH":           overrides.RestoreAuthoritySocketPath,
		"LOOM_RESTORE_AUTHORITY_SOCKET_OWNER":          overrides.RestoreAuthoritySocketOwner,
		"LOOM_RESTORE_AUTHORITY_SOCKET_GROUP":          overrides.RestoreAuthoritySocketGroup,
		"LOOM_RESTORE_AUTHORITY_SOCKET_ACTIVATOR_USER": overrides.RestoreActivatorUser,
		"LOOM_RESTORE_AUTHORITY_EXECUTOR_USER":         overrides.RestoreExecutorUser,
		"LOOM_RESTORE_OPERATIONAL_DATABASE":            overrides.RestoreOperationalDatabase,
		"LOOM_RESTORE_OPERATIONAL_OWNER":               overrides.RestoreOperationalOwner,
		"LOOM_RESTORE_PROVENANCE_DATABASE":             overrides.RestoreProvenanceDatabase,
		"LOOM_RESTORE_PROVENANCE_OWNER":                overrides.RestoreProvenanceOwner,

		"LOOM_SOCKET_PATH":            overrides.SocketPath,
		"LOOM_HTTP_LISTEN_ADDR":       overrides.HTTPListenAddr,
		"LOOM_SERVICE_ALLOWLIST_PATH": overrides.ServiceAllowlist,
		"LOOM_LOG_LEVEL":              overrides.LogLevel,
		"LOOM_CONFIG_FILE":            overrides.ConfigFile,
		"LOOM_MIGRATIONS_DIR":         overrides.MigrationsDir,
		"LOOM_AUTO_MIGRATE":           overrides.AutoMigrate,
		"LOOM_BOOTSTRAP_DEV":          overrides.BootstrapDev,
		"LOOM_BOOTSTRAP_MODE":         overrides.BootstrapMode,
		"LOOM_LEGACY_SPLIT_ROOTS":     overrides.LegacySplitRoots,

		"LOOM_WORKSPACE_ARCHIVE_ENABLED":         overrides.WorkspaceArchiveEnabled,
		"LOOM_WORKSPACE_ARCHIVE_MANIFEST_KEY_ID": overrides.WorkspaceArchiveManifestKeyID,

		"LOOM_EMBEDDINGS_ENABLED":             overrides.EmbeddingsEnabled,
		"LOOM_EMBEDDING_RUNTIME":              overrides.EmbeddingRuntime,
		"LOOM_EMBEDDING_MODEL":                overrides.EmbeddingModel,
		"LOOM_EMBEDDING_OLLAMA_URL":           overrides.EmbeddingOllamaURL,
		"LOOM_EMBEDDING_DIMENSIONS":           overrides.EmbeddingDimensions,
		"LOOM_EMBEDDING_QUIET_WINDOW_SECONDS": overrides.EmbeddingQuietWindowSeconds,
		"LOOM_EMBEDDING_CONCURRENCY":          overrides.EmbeddingConcurrency,
		"LOOM_VISION_ENABLED":                 overrides.VisionEnabled,
		"LOOM_VISION_RUNTIME":                 overrides.VisionRuntime,
		"LOOM_VISION_MODEL":                   overrides.VisionModel,
		"LOOM_VISION_OLLAMA_URL":              overrides.VisionOllamaURL,
		"LOOM_VISION_MAX_BYTES":               overrides.VisionMaxBytes,
		"LOOM_VISION_MAX_PIXELS":              overrides.VisionMaxPixels,
	})
}

func applyMap(cfg *Config, values map[string]string) error {
	for key, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		switch key {
		case "LOOM_ENV":
			cfg.Env = value
		case "LOOM_NODE_ID":
			cfg.NodeID = value
		case "LOOM_NODE_KIND":
			cfg.NodeKind = strings.ToLower(value)
		case "LOOM_NODE_ROLE":
			cfg.NodeRole = value
		case "LOOM_RUNTIME_CLASS":
			cfg.RuntimeClass = strings.ToLower(value)
		case "LOOM_DATA_DIR":
			cfg.DataDir = value
		case "LOOM_OBJECT_STORE":
			cfg.ObjectStore = value
		case "LOOM_APPLICATION_DATA_BACKUP_ROOT":
			if !filepath.IsAbs(value) || filepath.Clean(value) != value || value == "/" {
				return fmt.Errorf("LOOM_APPLICATION_DATA_BACKUP_ROOT must be an exact non-root absolute directory")
			}
			cfg.ApplicationDataBackupRoot = value
		case "LOOM_SERVICE_ROOT":
			cfg.ServiceRoot = value
		case "LOOM_STORAGE_ROOT":
			cfg.StorageRoot = value
		case "LOOM_CANONICAL_USER_BACKUPS_ROOT":
			cfg.CanonicalUserBackupsRoot = value
		case "LOOM_IMPORTS_BACKUP_POLICY":
			cfg.ImportsBackupPolicy = value
		case "LOOM_IMPORTS_ROOT":
			cfg.ImportsRoot = value
		case "LOOM_USER_BACKUPS_ROOT":
			cfg.UserBackupsRoot = value
		case "LOOM_ARCHIVE_ROOT":
			cfg.ArchiveRoot = value
		case "LOOM_GENERATED_ROOT":
			cfg.GeneratedRoot = value
		case "LOOM_BOX_STATE_ROOT":
			cfg.BoxStateRoot = value
		case "LOOM_STORAGE_EXPORT_ROOT":
			cfg.StorageExport = value
		case "LOOM_STORAGE_RETENTION_ROOT":
			cfg.StorageRetention = value
		case "LOOM_MAIN_DOCUMENTS_ROOT":
			cfg.MainDocuments = value
		case "LOOM_NOTES_PROJECTION_ROOT":
			cfg.NotesProjection = value
		case "LOOM_BOX_PATH":
			cfg.BoxPath = value
		case "LOOM_BOX_PROFILE":
			cfg.BoxProfile = strings.ToLower(value)
		case "LOOM_MAIN_URL":
			cfg.MainURL = strings.TrimRight(value, "/")
		case "LOOM_DB_URL":
			cfg.DBURL = value
		case "LOOM_PROVENANCE_DB_URL":
			cfg.ProvenanceDBURL = value
		case "LOOM_RESTORE_AUTHORITY_SOCKET_PATH":
			cfg.RestoreAuthoritySocketPath = value
		case "LOOM_RESTORE_AUTHORITY_SOCKET_OWNER":
			cfg.RestoreAuthoritySocketOwner = value
		case "LOOM_RESTORE_AUTHORITY_SOCKET_GROUP":
			cfg.RestoreAuthoritySocketGroup = value
		case "LOOM_RESTORE_AUTHORITY_SOCKET_ACTIVATOR_USER":
			cfg.RestoreActivatorUser = value
		case "LOOM_RESTORE_AUTHORITY_EXECUTOR_USER":
			cfg.RestoreExecutorUser = value
		case "LOOM_RESTORE_OPERATIONAL_DATABASE":
			cfg.RestoreOperationalDatabase = value
		case "LOOM_RESTORE_OPERATIONAL_OWNER":
			cfg.RestoreOperationalOwner = value
		case "LOOM_RESTORE_PROVENANCE_DATABASE":
			cfg.RestoreProvenanceDatabase = value
		case "LOOM_RESTORE_PROVENANCE_OWNER":
			cfg.RestoreProvenanceOwner = value
		case "LOOM_SOCKET_PATH":
			cfg.SocketPath = value
		case "LOOM_HTTP_LISTEN_ADDR":
			cfg.HTTPListenAddr = value
		case "LOOM_SERVICE_ALLOWLIST_PATH":
			cfg.ServiceAllowlist = filepath.Clean(value)
		case "LOOM_LOG_LEVEL":
			cfg.LogLevel = value
		case "LOOM_CONFIG_FILE":
			cfg.ConfigFile = value
		case "LOOM_MIGRATIONS_DIR":
			cfg.MigrationsDir = value
		case "LOOM_AUTO_MIGRATE":
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("invalid LOOM_AUTO_MIGRATE value %q: %w", value, err)
			}
			cfg.AutoMigrate = parsed
		case "LOOM_BOOTSTRAP_DEV":
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("invalid LOOM_BOOTSTRAP_DEV value %q: %w", value, err)
			}
			cfg.BootstrapDev = parsed
		case "LOOM_BOOTSTRAP_MODE":
			cfg.BootstrapMode = strings.ToLower(value)
		case "LOOM_LEGACY_SPLIT_ROOTS":
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("invalid LOOM_LEGACY_SPLIT_ROOTS value %q: %w", value, err)
			}
			cfg.LegacySplitRoots = parsed
		case "LOOM_MINA_SELECTED", "LOOM_MINA_ENABLED", "LOOM_MINA_RECOVERY_ENABLED":
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("invalid MINA activation flag")
			}
			switch key {
			case "LOOM_MINA_SELECTED":
				cfg.MinaSelected = parsed
			case "LOOM_MINA_ENABLED":
				cfg.MinaEnabled = parsed
			case "LOOM_MINA_RECOVERY_ENABLED":
				cfg.MinaRecoveryEnabled = parsed
			}
		case "LOOM_MINA_RECOVERY_PUBLIC_KEY":
			cfg.MinaRecoveryPublicKey = value
		case "LOOM_MINA_RECOVERY_RETAINED_MORATHUSTRA_IDS":
			cfg.MinaRecoveryRetainedMorathustraIDs = value
		case "LOOM_MORATHUSTRA_ENABLED":
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("invalid Morathustra enable flag")
			}
			cfg.MorathustraEnabled = parsed
		case "LOOM_MORATHUSTRA_RECOVERY_ENABLED":
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("invalid Morathustra recovery enable flag")
			}
			cfg.MorathustraRecoveryEnabled = parsed
		case "LOOM_MORATHUSTRA_RECOVERY_PUBLIC_KEY":
			cfg.MorathustraRecoveryPublicKey = value
		case "LOOM_WORKSPACE_ARCHIVE_ENABLED":
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("invalid LOOM_WORKSPACE_ARCHIVE_ENABLED value %q: %w", value, err)
			}
			cfg.WorkspaceArchiveEnabled = parsed
		case "LOOM_WORKSPACE_ARCHIVE_MANIFEST_KEY_ID":
			cfg.WorkspaceArchiveManifestKeyID = value
		case "LOOM_EMBEDDINGS_ENABLED":
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("invalid LOOM_EMBEDDINGS_ENABLED value %q: %w", value, err)
			}
			cfg.EmbeddingsEnabled = parsed
		case "LOOM_EMBEDDING_RUNTIME":
			cfg.EmbeddingRuntime = strings.ToLower(value)
		case "LOOM_EMBEDDING_MODEL":
			cfg.EmbeddingModel = value
		case "LOOM_EMBEDDING_OLLAMA_URL":
			cfg.EmbeddingOllamaURL = strings.TrimRight(value, "/")
		case "LOOM_EMBEDDING_DIMENSIONS":
			parsed, err := parsePositiveInt("LOOM_EMBEDDING_DIMENSIONS", value)
			if err != nil {
				return err
			}
			cfg.EmbeddingDimensions = parsed
		case "LOOM_EMBEDDING_QUIET_WINDOW_SECONDS":
			parsed, err := parsePositiveInt("LOOM_EMBEDDING_QUIET_WINDOW_SECONDS", value)
			if err != nil {
				return err
			}
			cfg.EmbeddingQuietWindowSeconds = parsed
		case "LOOM_EMBEDDING_CONCURRENCY":
			parsed, err := parsePositiveInt("LOOM_EMBEDDING_CONCURRENCY", value)
			if err != nil {
				return err
			}
			cfg.EmbeddingConcurrency = parsed
		case "LOOM_VISION_ENABLED":
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("invalid LOOM_VISION_ENABLED value %q: %w", value, err)
			}
			cfg.VisionEnabled = parsed
		case "LOOM_VISION_RUNTIME":
			cfg.VisionRuntime = strings.ToLower(value)
		case "LOOM_VISION_MODEL":
			cfg.VisionModel = value
		case "LOOM_VISION_OLLAMA_URL":
			cfg.VisionOllamaURL = strings.TrimRight(value, "/")
		case "LOOM_VISION_MAX_BYTES":
			parsed, err := parsePositiveInt("LOOM_VISION_MAX_BYTES", value)
			if err != nil {
				return err
			}
			cfg.VisionMaxBytes = parsed
		case "LOOM_VISION_MAX_PIXELS":
			parsed, err := parsePositiveInt("LOOM_VISION_MAX_PIXELS", value)
			if err != nil {
				return err
			}
			cfg.VisionMaxPixels = parsed
		}
	}
	return nil
}

func applyLayoutDefaults(cfg *Config) {
	if cfg == nil {
		return
	}
	if strings.TrimSpace(cfg.ServiceRoot) == "" {
		cfg.ServiceRoot = DefaultServiceRoot
	}
	if strings.TrimSpace(cfg.StorageRoot) == "" {
		cfg.StorageRoot = filepath.Join(cfg.ServiceRoot, "storage")
	}
	if strings.TrimSpace(cfg.ImportsBackupPolicy) == "" {
		cfg.ImportsBackupPolicy = DefaultImportsBackupPolicy
	}
	if strings.TrimSpace(cfg.ImportsRoot) == "" {
		cfg.ImportsRoot = filepath.Join(cfg.StorageRoot, "imports")
	}
	if strings.TrimSpace(cfg.UserBackupsRoot) == "" {
		cfg.UserBackupsRoot = filepath.Join(cfg.StorageRoot, "backups")
	}
	if strings.TrimSpace(cfg.CanonicalUserBackupsRoot) == "" {
		cfg.CanonicalUserBackupsRoot = cfg.UserBackupsRoot
	}
	if strings.TrimSpace(cfg.ArchiveRoot) == "" {
		cfg.ArchiveRoot = filepath.Join(cfg.StorageRoot, "archive")
	}
	if strings.TrimSpace(cfg.GeneratedRoot) == "" {
		cfg.GeneratedRoot = filepath.Join(cfg.DataDir, "generated")
	}
	if strings.TrimSpace(cfg.BoxStateRoot) == "" {
		cfg.BoxStateRoot = filepath.Join(cfg.DataDir, "box-state")
	}
}

func parsePositiveInt(key string, value string) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("invalid %s value %q: expected positive integer", key, value)
	}
	return parsed, nil
}

func validateBootstrapMode(mode string) error {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", DefaultBootstrapMode, "dev", "production":
		return nil
	default:
		return fmt.Errorf("invalid LOOM_BOOTSTRAP_MODE %q: expected none, dev, or production", mode)
	}
}

func validateHTTPListenAddr(addr string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid LOOM_HTTP_LISTEN_ADDR %q: expected host:port: %w", addr, err)
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return fmt.Errorf("invalid LOOM_HTTP_LISTEN_ADDR %q: wildcard binds are not allowed", addr)
	}
	if strings.EqualFold(host, "localhost") {
		return validatePort(addr, port)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("invalid LOOM_HTTP_LISTEN_ADDR %q: host must be an IP address or localhost", addr)
	}
	if ip.IsUnspecified() {
		return fmt.Errorf("invalid LOOM_HTTP_LISTEN_ADDR %q: wildcard binds are not allowed", addr)
	}
	if !ip.IsLoopback() && !ip.IsPrivate() {
		return fmt.Errorf("invalid LOOM_HTTP_LISTEN_ADDR %q: public interface binds are not allowed", addr)
	}
	return validatePort(addr, port)
}

func validateEmbeddingConfig(c Config) error {
	runtime := strings.ToLower(strings.TrimSpace(c.EmbeddingRuntime))
	if runtime == "" {
		runtime = DefaultEmbeddingRuntime
	}
	switch runtime {
	case DefaultEmbeddingRuntime:
	default:
		return fmt.Errorf("invalid LOOM_EMBEDDING_RUNTIME %q: expected ollama", c.EmbeddingRuntime)
	}
	model := strings.TrimSpace(c.EmbeddingModel)
	if model == "" {
		model = DefaultEmbeddingModel
	}
	if model == "" {
		return fmt.Errorf("missing required config: LOOM_EMBEDDING_MODEL")
	}
	dimensions := c.EmbeddingDimensions
	if dimensions == 0 {
		dimensions = DefaultEmbeddingDimensions
	}
	if dimensions < 0 {
		return fmt.Errorf("invalid LOOM_EMBEDDING_DIMENSIONS %d: expected positive integer", c.EmbeddingDimensions)
	}
	quietWindowSeconds := c.EmbeddingQuietWindowSeconds
	if quietWindowSeconds == 0 {
		quietWindowSeconds = DefaultEmbeddingQuietWindowSeconds
	}
	if quietWindowSeconds < 0 {
		return fmt.Errorf("invalid LOOM_EMBEDDING_QUIET_WINDOW_SECONDS %d: expected positive integer", c.EmbeddingQuietWindowSeconds)
	}
	concurrency := c.EmbeddingConcurrency
	if concurrency == 0 {
		concurrency = DefaultEmbeddingConcurrency
	}
	if concurrency < 0 {
		return fmt.Errorf("invalid LOOM_EMBEDDING_CONCURRENCY %d: expected positive integer", c.EmbeddingConcurrency)
	}
	ollamaURL := strings.TrimSpace(c.EmbeddingOllamaURL)
	if ollamaURL == "" {
		ollamaURL = DefaultEmbeddingOllamaURL
	}
	parsedURL, err := url.Parse(ollamaURL)
	if err != nil {
		return fmt.Errorf("invalid LOOM_EMBEDDING_OLLAMA_URL %q: %w", c.EmbeddingOllamaURL, err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return fmt.Errorf("invalid LOOM_EMBEDDING_OLLAMA_URL %q: expected http or https URL", ollamaURL)
	}
	if strings.TrimSpace(parsedURL.Host) == "" {
		return fmt.Errorf("invalid LOOM_EMBEDDING_OLLAMA_URL %q: host is required", ollamaURL)
	}
	return nil
}

func validateVisionConfig(c Config) error {
	if c.VisionRuntime != "" && strings.ToLower(strings.TrimSpace(c.VisionRuntime)) != "ollama" {
		return fmt.Errorf("invalid LOOM_VISION_RUNTIME %q: expected ollama", c.VisionRuntime)
	}
	if c.VisionEnabled && strings.TrimSpace(c.VisionModel) == "" {
		return fmt.Errorf("missing required config: LOOM_VISION_MODEL")
	}
	if c.VisionMaxBytes < 0 || c.VisionMaxPixels < 0 {
		return fmt.Errorf("invalid vision limits: expected positive integers")
	}
	endpoint := strings.TrimSpace(c.VisionOllamaURL)
	if endpoint == "" {
		endpoint = DefaultVisionOllamaURL
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || ((parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "") {
		return fmt.Errorf("invalid LOOM_VISION_OLLAMA_URL %q", endpoint)
	}
	return nil
}

func validatePort(addr string, port string) error {
	parsed, err := strconv.Atoi(port)
	if err != nil || parsed < 1 || parsed > 65535 {
		return fmt.Errorf("invalid LOOM_HTTP_LISTEN_ADDR %q: port must be 1-65535", addr)
	}
	return nil
}

func readEnvFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("config file does not exist: %s", path)
		}
		return nil, fmt.Errorf("read config file %s: %w", path, err)
	}
	defer file.Close()

	values := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("invalid config line in %s: %q", path, line)
		}
		values[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan config file %s: %w", path, err)
	}
	return values, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// HermesRecoveryPolicy binds coverage and worker checks to the same daemon
// configuration. Only a public verification key is stored here.
func (c Config) HermesRecoveryPolicy() (hermesprofile.Policy, error) {
	selected := c.MinaSelected || c.MinaEnabled || c.MinaRecoveryEnabled
	if !selected {
		if c.MinaRecoveryPublicKey != "" || c.MinaRecoveryRetainedMorathustraIDs != "" {
			return hermesprofile.Policy{}, fmt.Errorf("MINA recovery bindings require explicit MINA selection")
		}
		return hermesprofile.ParsePolicy(strconv.FormatBool(c.MorathustraRecoveryEnabled), c.MorathustraRecoveryPublicKey)
	}
	if c.MorathustraEnabled || c.MorathustraRecoveryEnabled || c.MorathustraRecoveryPublicKey != "" {
		return hermesprofile.Policy{}, fmt.Errorf("conflicting Morathustra and MINA runtime/recovery selections")
	}
	p := hermesprofile.Policy{
		Identity:  hermesprofile.MinaIdentity,
		Workspace: hermesprofile.MinaWorkspaceRoot,
		Enabled:   c.MinaRecoveryEnabled,
	}
	if c.MinaRecoveryPublicKey != "" {
		key, err := hex.DecodeString(c.MinaRecoveryPublicKey)
		if err != nil || len(key) != ed25519.PublicKeySize {
			return p, fmt.Errorf("invalid MINA recovery public key")
		}
		p.PublicKey = key
	}
	if p.Enabled && len(p.PublicKey) != ed25519.PublicKeySize {
		return p, fmt.Errorf("enabled MINA recovery requires a public key")
	}
	if raw := c.MinaRecoveryRetainedMorathustraIDs; raw != "" {
		// Reject null, scalar/object input, non-string entries and trailing data.
		// Resolve below validates exact ID syntax and duplicate bindings.
		if err := json.Unmarshal([]byte(raw), &p.RetainedMorathustra); err != nil || p.RetainedMorathustra == nil {
			return p, fmt.Errorf("MINA retained recovery IDs must be a JSON string array")
		}
	}
	resolved, _, err := p.Resolve()
	return resolved, err
}
func LoadHermesRecoveryPolicy() (hermesprofile.Policy, error) {
	c, err := Load(Overrides{})
	if err != nil {
		return hermesprofile.Policy{}, err
	}
	return c.HermesRecoveryPolicy()
}
