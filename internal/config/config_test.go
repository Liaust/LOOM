package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadUsesDefaults(t *testing.T) {
	t.Setenv("LOOM_ENV", "")
	t.Setenv("LOOM_NODE_ID", "")
	t.Setenv("LOOM_NODE_KIND", "")
	t.Setenv("LOOM_NODE_ROLE", "")
	t.Setenv("LOOM_RUNTIME_CLASS", "")
	t.Setenv("LOOM_DATA_DIR", "")
	t.Setenv("LOOM_OBJECT_STORE", "")
	t.Setenv("LOOM_SERVICE_ROOT", "")
	t.Setenv("LOOM_STORAGE_ROOT", "")
	t.Setenv("LOOM_CANONICAL_USER_BACKUPS_ROOT", "")
	t.Setenv("LOOM_IMPORTS_ROOT", "")
	t.Setenv("LOOM_USER_BACKUPS_ROOT", "")
	t.Setenv("LOOM_ARCHIVE_ROOT", "")
	t.Setenv("LOOM_GENERATED_ROOT", "")
	t.Setenv("LOOM_BOX_STATE_ROOT", "")
	t.Setenv("LOOM_STORAGE_EXPORT_ROOT", "")
	t.Setenv("LOOM_STORAGE_RETENTION_ROOT", "")
	t.Setenv("LOOM_MAIN_DOCUMENTS_ROOT", "")
	t.Setenv("LOOM_NOTES_PROJECTION_ROOT", "")
	t.Setenv("LOOM_BOX_PATH", "")
	t.Setenv("LOOM_BOX_PROFILE", "")
	t.Setenv("LOOM_DB_URL", "")
	t.Setenv("LOOM_PROVENANCE_DB_URL", "")
	t.Setenv("LOOM_RESTORE_AUTHORITY_SOCKET_PATH", "")
	t.Setenv("LOOM_RESTORE_AUTHORITY_SOCKET_OWNER", "")
	t.Setenv("LOOM_RESTORE_AUTHORITY_SOCKET_GROUP", "")
	t.Setenv("LOOM_RESTORE_AUTHORITY_SOCKET_ACTIVATOR_USER", "")
	t.Setenv("LOOM_RESTORE_AUTHORITY_EXECUTOR_USER", "")
	t.Setenv("LOOM_RESTORE_OPERATIONAL_DATABASE", "")
	t.Setenv("LOOM_RESTORE_OPERATIONAL_OWNER", "")
	t.Setenv("LOOM_RESTORE_PROVENANCE_DATABASE", "")
	t.Setenv("LOOM_RESTORE_PROVENANCE_OWNER", "")
	t.Setenv("LOOM_SOCKET_PATH", "")
	t.Setenv("LOOM_HTTP_LISTEN_ADDR", "")
	t.Setenv("LOOM_LOG_LEVEL", "")
	t.Setenv("LOOM_CONFIG_FILE", "")
	t.Setenv("LOOM_MIGRATIONS_DIR", "")
	t.Setenv("LOOM_AUTO_MIGRATE", "")
	t.Setenv("LOOM_BOOTSTRAP_DEV", "")
	t.Setenv("LOOM_BOOTSTRAP_MODE", "")
	t.Setenv("LOOM_LEGACY_SPLIT_ROOTS", "")
	t.Setenv("LOOM_WORKSPACE_ARCHIVE_ENABLED", "")
	t.Setenv("LOOM_WORKSPACE_ARCHIVE_MANIFEST_KEY_ID", "")
	t.Setenv("LOOM_EMBEDDINGS_ENABLED", "")
	t.Setenv("LOOM_EMBEDDING_RUNTIME", "")
	t.Setenv("LOOM_EMBEDDING_MODEL", "")
	t.Setenv("LOOM_EMBEDDING_OLLAMA_URL", "")
	t.Setenv("LOOM_EMBEDDING_DIMENSIONS", "")
	t.Setenv("LOOM_EMBEDDING_QUIET_WINDOW_SECONDS", "")
	t.Setenv("LOOM_EMBEDDING_CONCURRENCY", "")

	cfg, err := Load(Overrides{})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Env != DefaultEnv {
		t.Fatalf("Env = %q, want %q", cfg.Env, DefaultEnv)
	}
	if cfg.NodeKind != DefaultNodeKind {
		t.Fatalf("NodeKind = %q, want %q", cfg.NodeKind, DefaultNodeKind)
	}
	if cfg.RuntimeClass != DefaultRuntimeClass {
		t.Fatalf("RuntimeClass = %q, want %q", cfg.RuntimeClass, DefaultRuntimeClass)
	}
	if cfg.BootstrapMode != DefaultBootstrapMode {
		t.Fatalf("BootstrapMode = %q, want %q", cfg.BootstrapMode, DefaultBootstrapMode)
	}
	if cfg.WorkspaceArchiveEnabled || cfg.WorkspaceArchiveManifestKeyID != DefaultWorkspaceArchiveManifestKeyID {
		t.Fatalf("workspace archive defaults = enabled:%t key:%q", cfg.WorkspaceArchiveEnabled, cfg.WorkspaceArchiveManifestKeyID)
	}
	if cfg.SocketPath != DefaultSocketPath {
		t.Fatalf("SocketPath = %q, want %q", cfg.SocketPath, DefaultSocketPath)
	}
	if cfg.RestoreAuthoritySocketPath != DefaultRestoreAuthoritySocketPath ||
		cfg.RestoreAuthoritySocketOwner != DefaultRestoreAuthoritySocketOwner ||
		cfg.RestoreAuthoritySocketGroup != DefaultRestoreAuthoritySocketGroup ||
		cfg.RestoreActivatorUser != DefaultRestoreActivatorUser ||
		cfg.RestoreExecutorUser != DefaultRestoreExecutorUser ||
		cfg.RestoreOperationalDatabase != DefaultRestoreOperationalDatabase ||
		cfg.RestoreOperationalOwner != DefaultRestoreOperationalOwner ||
		cfg.RestoreProvenanceDatabase != DefaultRestoreProvenanceDatabase ||
		cfg.RestoreProvenanceOwner != DefaultRestoreProvenanceOwner {
		t.Fatalf("restore authority defaults = %#v", cfg.SafeFields())
	}
	restoreFields := cfg.SafeFields()
	if restoreFields["restore_authority_socket_activator_user"] != "root" || restoreFields["restore_authority_executor_user"] != "postgres" {
		t.Fatalf("restore authority diagnostics do not separate activator and executor: %#v", restoreFields)
	}
	if _, exists := restoreFields["restore_authority_server_user"]; exists {
		t.Fatalf("restore authority diagnostics retain ambiguous server identity: %#v", restoreFields)
	}
	if cfg.StorageExport != DefaultStorageExport {
		t.Fatalf("StorageExport = %q, want %q", cfg.StorageExport, DefaultStorageExport)
	}
	if cfg.ServiceRoot != DefaultServiceRoot {
		t.Fatalf("ServiceRoot = %q, want %q", cfg.ServiceRoot, DefaultServiceRoot)
	}
	if cfg.StorageRoot != DefaultStorageRoot {
		t.Fatalf("StorageRoot = %q, want %q", cfg.StorageRoot, DefaultStorageRoot)
	}
	if cfg.CanonicalUserBackupsRoot != DefaultUserBackupsRoot {
		t.Fatalf("CanonicalUserBackupsRoot = %q, want %q", cfg.CanonicalUserBackupsRoot, DefaultUserBackupsRoot)
	}
	if cfg.ImportsRoot != DefaultImportsRoot {
		t.Fatalf("ImportsRoot = %q, want %q", cfg.ImportsRoot, DefaultImportsRoot)
	}
	if cfg.UserBackupsRoot != DefaultUserBackupsRoot {
		t.Fatalf("UserBackupsRoot = %q, want %q", cfg.UserBackupsRoot, DefaultUserBackupsRoot)
	}
	if cfg.ArchiveRoot != DefaultArchiveRoot {
		t.Fatalf("ArchiveRoot = %q, want %q", cfg.ArchiveRoot, DefaultArchiveRoot)
	}
	if cfg.GeneratedRoot != DefaultGeneratedRoot {
		t.Fatalf("GeneratedRoot = %q, want %q", cfg.GeneratedRoot, DefaultGeneratedRoot)
	}
	if cfg.BoxStateRoot != filepath.Join(DefaultDataDir, "box-state") {
		t.Fatalf("BoxStateRoot = %q, want %q", cfg.BoxStateRoot, filepath.Join(DefaultDataDir, "box-state"))
	}
	if cfg.StorageRetention != DefaultStorageRetention {
		t.Fatalf("StorageRetention = %q, want %q", cfg.StorageRetention, DefaultStorageRetention)
	}
	if cfg.MainDocuments != DefaultMainDocuments {
		t.Fatalf("MainDocuments = %q, want %q", cfg.MainDocuments, DefaultMainDocuments)
	}
	if cfg.HTTPListenAddr != "" {
		t.Fatalf("HTTPListenAddr = %q, want empty", cfg.HTTPListenAddr)
	}
	if cfg.MigrationsDir != DefaultMigrationsDir {
		t.Fatalf("MigrationsDir = %q, want %q", cfg.MigrationsDir, DefaultMigrationsDir)
	}
	if cfg.BoxPath != "" {
		t.Fatalf("BoxPath = %q, want empty", cfg.BoxPath)
	}
	if cfg.BoxProfile != "" {
		t.Fatalf("BoxProfile = %q, want empty", cfg.BoxProfile)
	}
	if cfg.MainDocumentsRoot() != filepath.Join(DefaultServiceRoot, "box", "Documents") {
		t.Fatalf("MainDocumentsRoot = %q, want canonical Box Documents root", cfg.MainDocumentsRoot())
	}
	if cfg.BoxNotesRoot() != filepath.Join(DefaultServiceRoot, "box", "Notes") {
		t.Fatalf("BoxNotesRoot = %q, want canonical Box Notes root", cfg.BoxNotesRoot())
	}
	if cfg.NotesProjectionRoot() != filepath.Join(DefaultGeneratedRoot, DefaultNotesProjection) {
		t.Fatalf("NotesProjectionRoot = %q, want generated-root projection", cfg.NotesProjectionRoot())
	}
	if cfg.EmbeddingsEnabled != DefaultEmbeddingsEnabled {
		t.Fatalf("EmbeddingsEnabled = %t, want %t", cfg.EmbeddingsEnabled, DefaultEmbeddingsEnabled)
	}
	if cfg.EmbeddingRuntime != DefaultEmbeddingRuntime {
		t.Fatalf("EmbeddingRuntime = %q, want %q", cfg.EmbeddingRuntime, DefaultEmbeddingRuntime)
	}
	if cfg.EmbeddingModel != DefaultEmbeddingModel {
		t.Fatalf("EmbeddingModel = %q, want %q", cfg.EmbeddingModel, DefaultEmbeddingModel)
	}
	if cfg.EmbeddingOllamaURL != DefaultEmbeddingOllamaURL {
		t.Fatalf("EmbeddingOllamaURL = %q, want %q", cfg.EmbeddingOllamaURL, DefaultEmbeddingOllamaURL)
	}
	if cfg.EmbeddingDimensions != DefaultEmbeddingDimensions {
		t.Fatalf("EmbeddingDimensions = %d, want %d", cfg.EmbeddingDimensions, DefaultEmbeddingDimensions)
	}
	if cfg.EmbeddingQuietWindowSeconds != DefaultEmbeddingQuietWindowSeconds {
		t.Fatalf("EmbeddingQuietWindowSeconds = %d, want %d", cfg.EmbeddingQuietWindowSeconds, DefaultEmbeddingQuietWindowSeconds)
	}
	if cfg.EmbeddingConcurrency != DefaultEmbeddingConcurrency {
		t.Fatalf("EmbeddingConcurrency = %d, want %d", cfg.EmbeddingConcurrency, DefaultEmbeddingConcurrency)
	}
}

func TestLoadPrecedenceOverridesEnvAndFile(t *testing.T) {
	dir := t.TempDir()
	configFile := filepath.Join(dir, "loom.env")
	if err := os.WriteFile(configFile, []byte("LOOM_NODE_ID=file-node\nLOOM_NODE_KIND=workspace\nLOOM_RUNTIME_CLASS=workspace_full\nLOOM_SOCKET_PATH=/tmp/file.sock\nLOOM_HTTP_LISTEN_ADDR=127.0.0.1:8080\nLOOM_AUTO_MIGRATE=true\nLOOM_BOOTSTRAP_DEV=true\nLOOM_BOOTSTRAP_MODE=dev\nLOOM_BOX_PATH=/tmp/file-box\nLOOM_BOX_PROFILE=workspace\nLOOM_STORAGE_EXPORT_ROOT=/tmp/file-export\nLOOM_STORAGE_RETENTION_ROOT=/tmp/file-retention\nLOOM_MAIN_DOCUMENTS_ROOT=/tmp/file-documents\nLOOM_NOTES_PROJECTION_ROOT=/tmp/file-projection\nLOOM_EMBEDDINGS_ENABLED=true\nLOOM_EMBEDDING_RUNTIME=ollama\nLOOM_EMBEDDING_MODEL=file-model\nLOOM_EMBEDDING_OLLAMA_URL=http://127.0.0.1:11435\nLOOM_EMBEDDING_DIMENSIONS=384\nLOOM_EMBEDDING_QUIET_WINDOW_SECONDS=30\nLOOM_EMBEDDING_CONCURRENCY=2\n"), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	t.Setenv("LOOM_NODE_ID", "env-node")
	t.Setenv("LOOM_NODE_KIND", "hardware")
	t.Setenv("LOOM_RUNTIME_CLASS", "hardware_agent")
	t.Setenv("LOOM_SOCKET_PATH", "")
	t.Setenv("LOOM_BOX_PATH", "/tmp/env-box")
	t.Setenv("LOOM_STORAGE_EXPORT_ROOT", "/tmp/env-export")
	t.Setenv("LOOM_STORAGE_RETENTION_ROOT", "/tmp/env-retention")
	t.Setenv("LOOM_MAIN_DOCUMENTS_ROOT", "/tmp/env-documents")
	t.Setenv("LOOM_NOTES_PROJECTION_ROOT", "/tmp/env-projection")
	t.Setenv("LOOM_BOOTSTRAP_MODE", "production")
	t.Setenv("LOOM_EMBEDDING_MODEL", "env-model")
	t.Setenv("LOOM_EMBEDDING_OLLAMA_URL", "http://127.0.0.1:11436")
	t.Setenv("LOOM_EMBEDDING_DIMENSIONS", "768")
	t.Setenv("LOOM_CONFIG_FILE", configFile)

	cfg, err := Load(Overrides{
		NodeID:                      "override-node",
		NodeKind:                    "integration",
		RuntimeClass:                "hardware_agent",
		BoxPath:                     "/tmp/override-box",
		StorageExport:               "/tmp/override-export",
		StorageRetention:            "/tmp/override-retention",
		MainDocuments:               "/tmp/override-documents",
		NotesProjection:             "/tmp/override-projection",
		BootstrapMode:               "none",
		EmbeddingsEnabled:           "false",
		EmbeddingModel:              "override-model",
		EmbeddingDimensions:         "1024",
		EmbeddingQuietWindowSeconds: "600",
		EmbeddingConcurrency:        "1",
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.NodeID != "override-node" {
		t.Fatalf("NodeID = %q, want override-node", cfg.NodeID)
	}
	if cfg.NodeKind != "integration" {
		t.Fatalf("NodeKind = %q, want integration", cfg.NodeKind)
	}
	if cfg.RuntimeClass != "hardware_agent" {
		t.Fatalf("RuntimeClass = %q, want hardware_agent", cfg.RuntimeClass)
	}
	if cfg.SocketPath != "/tmp/file.sock" {
		t.Fatalf("SocketPath = %q, want /tmp/file.sock", cfg.SocketPath)
	}
	if cfg.HTTPListenAddr != "127.0.0.1:8080" {
		t.Fatalf("HTTPListenAddr = %q, want 127.0.0.1:8080", cfg.HTTPListenAddr)
	}
	if !cfg.AutoMigrate {
		t.Fatal("AutoMigrate = false, want true")
	}
	if !cfg.BootstrapDev {
		t.Fatal("BootstrapDev = false, want true")
	}
	if cfg.BoxPath != "/tmp/override-box" {
		t.Fatalf("BoxPath = %q, want /tmp/override-box", cfg.BoxPath)
	}
	if cfg.StorageExport != "/tmp/override-export" {
		t.Fatalf("StorageExport = %q, want /tmp/override-export", cfg.StorageExport)
	}
	if cfg.StorageRetention != "/tmp/override-retention" {
		t.Fatalf("StorageRetention = %q, want /tmp/override-retention", cfg.StorageRetention)
	}
	if cfg.MainDocuments != "/tmp/override-documents" {
		t.Fatalf("MainDocuments = %q, want /tmp/override-documents", cfg.MainDocuments)
	}
	if cfg.MainDocumentsRoot() != "/tmp/override-box/Documents" {
		t.Fatalf("MainDocumentsRoot = %q, want active Box Documents independent of deprecated override", cfg.MainDocumentsRoot())
	}
	if cfg.BoxNotesRoot() != "/tmp/override-box/Notes" {
		t.Fatalf("BoxNotesRoot = %q, want active Box Notes", cfg.BoxNotesRoot())
	}
	if cfg.BoxProfile != "workspace" {
		t.Fatalf("BoxProfile = %q, want workspace", cfg.BoxProfile)
	}
	if cfg.NotesProjectionRoot() != "/tmp/override-projection" {
		t.Fatalf("NotesProjectionRoot = %q, want /tmp/override-projection", cfg.NotesProjectionRoot())
	}
	if cfg.BootstrapMode != "none" {
		t.Fatalf("BootstrapMode = %q, want none", cfg.BootstrapMode)
	}
	if cfg.EmbeddingsEnabled {
		t.Fatal("EmbeddingsEnabled = true, want override false")
	}
	if cfg.EmbeddingRuntime != DefaultEmbeddingRuntime {
		t.Fatalf("EmbeddingRuntime = %q, want %q", cfg.EmbeddingRuntime, DefaultEmbeddingRuntime)
	}
	if cfg.EmbeddingModel != "override-model" {
		t.Fatalf("EmbeddingModel = %q, want override-model", cfg.EmbeddingModel)
	}
	if cfg.EmbeddingOllamaURL != "http://127.0.0.1:11436" {
		t.Fatalf("EmbeddingOllamaURL = %q, want env value", cfg.EmbeddingOllamaURL)
	}
	if cfg.EmbeddingDimensions != 1024 {
		t.Fatalf("EmbeddingDimensions = %d, want override 1024", cfg.EmbeddingDimensions)
	}
	if cfg.EmbeddingQuietWindowSeconds != 600 {
		t.Fatalf("EmbeddingQuietWindowSeconds = %d, want override 600", cfg.EmbeddingQuietWindowSeconds)
	}
	if cfg.EmbeddingConcurrency != 1 {
		t.Fatalf("EmbeddingConcurrency = %d, want override 1", cfg.EmbeddingConcurrency)
	}
}

func TestLoadKeepsProvenanceDatabaseConfigurationSeparateAndSafe(t *testing.T) {
	dir := t.TempDir()
	configFile := filepath.Join(dir, "loom.env")
	if err := os.WriteFile(configFile, []byte("LOOM_DB_URL=postgresql://main:file-secret@localhost/loom_main\nLOOM_PROVENANCE_DB_URL=postgresql://provenance:file-secret@localhost/loom_provenance\n"), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}
	t.Setenv("LOOM_CONFIG_FILE", configFile)
	t.Setenv("LOOM_PROVENANCE_DB_URL", "postgresql://provenance:env-secret@localhost/loom_provenance")

	cfg, err := Load(Overrides{
		ProvenanceDBURL: "postgresql://provenance:override-secret@localhost/loom_provenance",
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.DBURL != "postgresql://main:file-secret@localhost/loom_main" {
		t.Fatalf("DBURL = %q, want independent main database URL", cfg.DBURL)
	}
	if cfg.ProvenanceDBURL != "postgresql://provenance:override-secret@localhost/loom_provenance" {
		t.Fatalf("ProvenanceDBURL = %q, want override value", cfg.ProvenanceDBURL)
	}
	fields := cfg.SafeFields()
	if fields["db_configured"] != "true" || fields["provenance_db_configured"] != "true" {
		t.Fatalf("database SafeFields = %#v", fields)
	}
	for key, value := range fields {
		if key == "db_url" || key == "provenance_db_url" || value == cfg.DBURL || value == cfg.ProvenanceDBURL {
			t.Fatalf("SafeFields exposed database configuration through %q", key)
		}
	}
}

func TestLoadDerivesCanonicalChildrenFromOverriddenRoots(t *testing.T) {
	root := t.TempDir()
	cfg, err := Load(Overrides{
		DataDir:     filepath.Join(root, "runtime"),
		ServiceRoot: filepath.Join(root, "service"),
		ObjectStore: filepath.Join(root, "runtime", "object-store"),
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	for name, pair := range map[string][2]string{
		"storage":      {cfg.StorageRoot, filepath.Join(root, "service", "storage")},
		"imports":      {cfg.ImportsRoot, filepath.Join(root, "service", "storage", "imports")},
		"user backups": {cfg.UserBackupsRoot, filepath.Join(root, "service", "storage", "backups")},
		"archive":      {cfg.ArchiveRoot, filepath.Join(root, "service", "storage", "archive")},
		"generated":    {cfg.GeneratedRoot, filepath.Join(root, "runtime", "generated")},
		"box state":    {cfg.BoxStateRoot, filepath.Join(root, "runtime", "box-state")},
	} {
		if pair[0] != pair[1] {
			t.Fatalf("%s root = %q, want %q", name, pair[0], pair[1])
		}
	}
}

func TestLoadRejectsInvalidCanonicalLayout(t *testing.T) {
	if _, err := Load(Overrides{ImportsRoot: "/tmp/loom-imports-outside-storage"}); err == nil {
		t.Fatal("Load accepted imports root outside storage root")
	}
	if _, err := Load(Overrides{ServiceRoot: "relative/service"}); err == nil {
		t.Fatal("Load accepted relative service root")
	}
}

func TestLoadRejectsLegacyBoxStateWithoutCompleteTransition(t *testing.T) {
	boxRoot := filepath.Join(t.TempDir(), "loom-box")
	cfg, err := Load(Overrides{
		NodeID:       "main",
		NodeKind:     "main",
		NodeRole:     "main",
		RuntimeClass: "main_full",
		BoxPath:      boxRoot,
		BoxProfile:   "main",
		BoxStateRoot: filepath.Join(boxRoot, ".loom", "state"),
	})
	if err == nil {
		t.Fatalf("partial legacy Box-state config was accepted: %#v", cfg.SafeFields())
	}
}

func TestConfigAcceptsOnlyCompleteLegacyProductionRootSet(t *testing.T) {
	dataRoot := filepath.Join(t.TempDir(), "runtime")
	boxRoot := filepath.Join(t.TempDir(), "loom-box")
	legacy := Config{
		DataDir:                  dataRoot,
		ServiceRoot:              "/srv/loom",
		StorageRoot:              "/srv/loom/storage",
		CanonicalUserBackupsRoot: "/srv/loom/storage/backups",
		ImportsRoot:              filepath.Join(dataRoot, "lane", "accepted"),
		UserBackupsRoot:          filepath.Join(dataRoot, "private-backups"),
		ArchiveRoot:              filepath.Join(dataRoot, "storage-archive"),
		GeneratedRoot:            filepath.Join(dataRoot, "generated"),
		NotesProjection:          filepath.Join(dataRoot, "loom-notes"),
		StorageExport:            filepath.Join(dataRoot, "storage-views", "main-export"),
		StorageRetention:         filepath.Join(dataRoot, "storage-retention"),
		BoxPath:                  boxRoot,
		BoxStateRoot:             filepath.Join(boxRoot, ".loom", "state"),
		LegacySplitRoots:         true,
	}
	layout, err := legacy.FilesystemLayout()
	if err != nil {
		t.Fatalf("complete legacy root set: %v", err)
	}
	if got := layout.SafeFields()["legacy_split_roots"]; got != "true" {
		t.Fatalf("legacy split diagnostic = %q", got)
	}
	if err := legacy.validateCustodyTransition(); err != nil {
		t.Fatalf("complete legacy transition contract: %v", err)
	}
	partial := legacy
	partial.ArchiveRoot = filepath.Join(dataRoot, "canonical-archive")
	if err := partial.validateCustodyTransition(); err == nil {
		t.Fatal("partial legacy/canonical root mix was accepted")
	}
	partial = legacy
	partial.BoxStateRoot = filepath.Join(dataRoot, "box-state")
	if err := partial.validateCustodyTransition(); err == nil {
		t.Fatal("partial canonical Box-state/legacy custody mix was accepted")
	}
}

func TestImportsBackupPolicyIsIndependentOfFilesystemTransition(t *testing.T) {
	for _, legacySplit := range []bool{false, true} {
		cfg := Config{ImportsBackupPolicy: ImportsBackupPolicyCompletePhysical, LegacySplitRoots: legacySplit}
		if got := cfg.EffectiveImportsBackupPolicy(); got != ImportsBackupPolicyCompletePhysical {
			t.Fatalf("legacy_split_roots=%t changed Imports backup policy to %q", legacySplit, got)
		}
	}
	if got := (Config{}).EffectiveImportsBackupPolicy(); got != DefaultImportsBackupPolicy {
		t.Fatalf("new-install default Imports policy = %q", got)
	}
}

func TestLoadAcceptsProductionPreCutoverRootsAsOneLegacySet(t *testing.T) {
	root := t.TempDir()
	dataRoot := filepath.Join(root, "var-lib-loom")
	boxRoot := filepath.Join(root, "loom-box")
	cfg, err := Load(Overrides{
		DataDir:                  dataRoot,
		ServiceRoot:              filepath.Join(root, "srv-loom"),
		StorageRoot:              filepath.Join(root, "srv-loom", "storage"),
		CanonicalUserBackupsRoot: filepath.Join(root, "srv-loom", "storage", "backups"),
		ImportsRoot:              filepath.Join(dataRoot, "lane", "accepted"),
		UserBackupsRoot:          filepath.Join(dataRoot, "private-backups"),
		ArchiveRoot:              filepath.Join(dataRoot, "storage-archive"),
		GeneratedRoot:            filepath.Join(dataRoot, "generated"),
		NotesProjection:          filepath.Join(dataRoot, "loom-notes"),
		StorageExport:            filepath.Join(dataRoot, "storage-views", "main-export"),
		StorageRetention:         filepath.Join(dataRoot, "storage-retention"),
		BoxPath:                  boxRoot,
		BoxStateRoot:             filepath.Join(boxRoot, ".loom", "state"),
		LegacySplitRoots:         "true",
	})
	if err != nil {
		t.Fatalf("Load production pre-cutover roots: %v", err)
	}
	if cfg.StorageRoot == dataRoot || cfg.StorageRoot == cfg.StorageExport || cfg.CanonicalUserBackupsRoot == cfg.UserBackupsRoot {
		t.Fatalf("canonical Storage/backups and active legacy/SMB roots were conflated: storage=%q canonical_backups=%q active_backups=%q export=%q", cfg.StorageRoot, cfg.CanonicalUserBackupsRoot, cfg.UserBackupsRoot, cfg.StorageExport)
	}
	if got := cfg.SafeFields()["legacy_split_roots"]; got != "true" {
		t.Fatalf("legacy split diagnostic = %q", got)
	}
}

func TestLoadRejectsInvalidAutoMigrate(t *testing.T) {
	t.Setenv("LOOM_AUTO_MIGRATE", "sometimes")

	if _, err := Load(Overrides{}); err == nil {
		t.Fatal("Load accepted invalid LOOM_AUTO_MIGRATE")
	}
}

func TestLoadRejectsInvalidBootstrapDev(t *testing.T) {
	t.Setenv("LOOM_BOOTSTRAP_DEV", "sometimes")

	if _, err := Load(Overrides{}); err == nil {
		t.Fatal("Load accepted invalid LOOM_BOOTSTRAP_DEV")
	}
}

func TestLoadRejectsInvalidBootstrapMode(t *testing.T) {
	t.Setenv("LOOM_BOOTSTRAP_MODE", "sometimes")

	if _, err := Load(Overrides{}); err == nil {
		t.Fatal("Load accepted invalid LOOM_BOOTSTRAP_MODE")
	}
}

func TestLoadRejectsInvalidEmbeddingsEnabled(t *testing.T) {
	t.Setenv("LOOM_EMBEDDINGS_ENABLED", "sometimes")

	if _, err := Load(Overrides{}); err == nil {
		t.Fatal("Load accepted invalid LOOM_EMBEDDINGS_ENABLED")
	}
}

func TestLoadRejectsInvalidEmbeddingRuntime(t *testing.T) {
	t.Setenv("LOOM_EMBEDDING_RUNTIME", "remote-api")

	if _, err := Load(Overrides{}); err == nil {
		t.Fatal("Load accepted invalid LOOM_EMBEDDING_RUNTIME")
	}
}

func TestLoadRejectsInvalidEmbeddingDimensions(t *testing.T) {
	t.Setenv("LOOM_EMBEDDING_DIMENSIONS", "0")

	if _, err := Load(Overrides{}); err == nil {
		t.Fatal("Load accepted invalid LOOM_EMBEDDING_DIMENSIONS")
	}
}

func TestLoadRejectsInvalidEmbeddingOllamaURL(t *testing.T) {
	t.Setenv("LOOM_EMBEDDING_OLLAMA_URL", "127.0.0.1:11434")

	if _, err := Load(Overrides{}); err == nil {
		t.Fatal("Load accepted invalid LOOM_EMBEDDING_OLLAMA_URL")
	}
}

func TestSafeFieldsIncludesNodeAndBootstrapProfile(t *testing.T) {
	cfg := Config{
		Env:                      DefaultEnv,
		NodeID:                   "main",
		NodeKind:                 "main",
		NodeRole:                 "main",
		RuntimeClass:             "main_full",
		DataDir:                  DefaultDataDir,
		ObjectStore:              DefaultObjectStore,
		ServiceRoot:              DefaultServiceRoot,
		StorageRoot:              DefaultStorageRoot,
		CanonicalUserBackupsRoot: DefaultUserBackupsRoot,
		ImportsRoot:              DefaultImportsRoot,
		UserBackupsRoot:          DefaultUserBackupsRoot,
		ArchiveRoot:              DefaultArchiveRoot,
		GeneratedRoot:            DefaultGeneratedRoot,
		StorageExport:            DefaultStorageExport,
		StorageRetention:         DefaultStorageRetention,
		MainDocuments:            DefaultMainDocuments,
		SocketPath:               DefaultSocketPath,
		MigrationsDir:            DefaultMigrationsDir,
		BootstrapMode:            "production",
		BoxPath:                  "/home/loomadmin/LOOM Box",
		BoxProfile:               "main",
		HTTPListenAddr:           "10.44.0.2:8080",
		AutoMigrate:              true,

		EmbeddingsEnabled:           DefaultEmbeddingsEnabled,
		EmbeddingRuntime:            DefaultEmbeddingRuntime,
		EmbeddingModel:              DefaultEmbeddingModel,
		EmbeddingOllamaURL:          DefaultEmbeddingOllamaURL,
		EmbeddingDimensions:         DefaultEmbeddingDimensions,
		EmbeddingQuietWindowSeconds: DefaultEmbeddingQuietWindowSeconds,
		EmbeddingConcurrency:        DefaultEmbeddingConcurrency,
	}

	fields := cfg.SafeFields()
	for key, want := range map[string]string{
		"node_kind":                             "main",
		"runtime_class":                         "main_full",
		"bootstrap_mode":                        "production",
		"box_path":                              "/home/loomadmin/LOOM Box",
		"box_root":                              "/home/loomadmin/LOOM Box",
		"box_profile":                           "main",
		"storage_export":                        DefaultStorageExport,
		"service_root":                          DefaultServiceRoot,
		"storage_root":                          DefaultStorageRoot,
		"canonical_user_backups_root":           DefaultUserBackupsRoot,
		"legacy_split_roots":                    "false",
		"imports_root":                          DefaultImportsRoot,
		"user_backups_root":                     DefaultUserBackupsRoot,
		"archive_root":                          DefaultArchiveRoot,
		"generated_root":                        DefaultGeneratedRoot,
		"notes_projection_root":                 "/var/lib/loom/generated/notes",
		"deprecated_storage_export_root":        DefaultStorageExport,
		"deprecated_main_documents_root":        DefaultMainDocuments,
		"storage_export_migration_input_only":   "true",
		"main_documents_migration_input_only":   "true",
		"legacy_box_state_compatibility_active": "false",
		"storage_retention":                     DefaultStorageRetention,
		"main_documents":                        DefaultMainDocuments,
		"active_main_documents":                 "/home/loomadmin/LOOM Box/Documents",
		"box_notes":                             "/home/loomadmin/LOOM Box/Notes",
		"notes_projection":                      "/var/lib/loom/generated/notes",
		"embeddings_enabled":                    "false",
		"embedding_runtime":                     DefaultEmbeddingRuntime,
		"embedding_model":                       DefaultEmbeddingModel,
		"embedding_ollama_url":                  DefaultEmbeddingOllamaURL,
		"embedding_dimensions":                  "1024",
		"embedding_quiet_window_seconds":        "600",
		"embedding_concurrency":                 "1",
	} {
		if got := fields[key]; got != want {
			t.Fatalf("SafeFields()[%q] = %q, want %q", key, got, want)
		}
	}
}

func TestSafeFieldsStripsURLCredentialsAndRequestPayload(t *testing.T) {
	cfg := Config{
		MainURL:            "https://operator:main-secret@example.test/path-secret?token=query-secret#fragment-secret",
		EmbeddingOllamaURL: "http://model:model-secret@example.test/model-path-secret?api_key=secret",
		VisionOllamaURL:    "http://vision:vision-secret@example.test/vision-path-secret#private",
	}
	fields := cfg.SafeFields()
	wants := map[string]string{
		"main_url":             "https://example.test",
		"embedding_ollama_url": "http://example.test",
		"vision_ollama_url":    "http://example.test",
	}
	for key, want := range wants {
		if got := fields[key]; got != want {
			t.Fatalf("SafeFields()[%q] = %q, want %q", key, got, want)
		}
	}
}

func TestWorkspaceArchiveConfigurationIsDefaultOffAndNonSecret(t *testing.T) {
	t.Setenv("LOOM_WORKSPACE_ARCHIVE_ENABLED", "true")
	t.Setenv("LOOM_WORKSPACE_ARCHIVE_MANIFEST_KEY_ID", "workspace-archive-rotation-2026")
	cfg, err := Load(Overrides{BoxPath: filepath.Join(t.TempDir(), "box")})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.WorkspaceArchiveEnabled || cfg.WorkspaceArchiveManifestKeyID != "workspace-archive-rotation-2026" {
		t.Fatalf("workspace archive config = %#v", cfg.SafeFields())
	}
	fields := cfg.SafeFields()
	if fields["workspace_archive_enabled"] != "true" || fields["workspace_archive_manifest_key_id"] != "workspace-archive-rotation-2026" {
		t.Fatalf("workspace archive safe fields = %#v", fields)
	}
	for key, value := range fields {
		if strings.Contains(strings.ToLower(key), "credential") || strings.Contains(value, "0011223344556677") {
			t.Fatalf("safe fields exposed credential material: %q=%q", key, value)
		}
	}
}

func TestWorkspaceArchiveConfigurationRejectsInvalidValues(t *testing.T) {
	if _, err := Load(Overrides{WorkspaceArchiveEnabled: "not-a-bool"}); err == nil {
		t.Fatal("invalid workspace archive enable flag was accepted")
	}
	if _, err := Load(Overrides{WorkspaceArchiveManifestKeyID: "../../secret"}); err == nil {
		t.Fatal("path-like workspace archive key ID was accepted")
	}
}

func TestLoadAcceptsPrivateHTTPListenAddr(t *testing.T) {
	t.Setenv("LOOM_HTTP_LISTEN_ADDR", "10.44.0.2:8080")

	cfg, err := Load(Overrides{})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.HTTPListenAddr != "10.44.0.2:8080" {
		t.Fatalf("HTTPListenAddr = %q, want 10.44.0.2:8080", cfg.HTTPListenAddr)
	}
}

func TestLoadRejectsPublicHTTPListenAddr(t *testing.T) {
	t.Setenv("LOOM_HTTP_LISTEN_ADDR", "8.8.8.8:8080")

	if _, err := Load(Overrides{}); err == nil {
		t.Fatal("Load accepted public LOOM_HTTP_LISTEN_ADDR")
	}
}

func TestLoadRejectsWildcardHTTPListenAddr(t *testing.T) {
	t.Setenv("LOOM_HTTP_LISTEN_ADDR", "0.0.0.0:8080")

	if _, err := Load(Overrides{}); err == nil {
		t.Fatal("Load accepted wildcard LOOM_HTTP_LISTEN_ADDR")
	}
}

func TestLoadRestoreAuthorityConfigurationUsesReviewedPrecedence(t *testing.T) {
	dir := t.TempDir()
	configFile := filepath.Join(dir, "loom.env")
	if err := os.WriteFile(configFile, []byte(strings.Join([]string{
		"LOOM_RESTORE_AUTHORITY_SOCKET_PATH=" + DefaultRestoreAuthoritySocketPath,
		"LOOM_RESTORE_AUTHORITY_SOCKET_OWNER=postgres",
		"LOOM_RESTORE_AUTHORITY_SOCKET_GROUP=loom-file",
		"LOOM_RESTORE_AUTHORITY_SOCKET_ACTIVATOR_USER=root",
		"LOOM_RESTORE_AUTHORITY_EXECUTOR_USER=postgres",
		"LOOM_RESTORE_OPERATIONAL_DATABASE=loom_file",
		"LOOM_RESTORE_OPERATIONAL_OWNER=loom_file_owner",
		"LOOM_RESTORE_PROVENANCE_DATABASE=loom_provenance_file",
		"LOOM_RESTORE_PROVENANCE_OWNER=loom_provenance_file_owner",
	}, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOOM_CONFIG_FILE", configFile)
	t.Setenv("LOOM_RESTORE_AUTHORITY_SOCKET_GROUP", "loom-env")
	t.Setenv("LOOM_RESTORE_OPERATIONAL_DATABASE", "loom_env")
	cfg, err := Load(Overrides{
		RestoreAuthoritySocketPath: DefaultRestoreAuthoritySocketPath,
		RestoreActivatorUser:       "root",
		RestoreOperationalDatabase: "loom_override",
		RestoreProvenanceOwner:     "loom_provenance_override_owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RestoreAuthoritySocketPath != DefaultRestoreAuthoritySocketPath ||
		cfg.RestoreAuthoritySocketGroup != "loom-env" ||
		cfg.RestoreActivatorUser != "root" ||
		cfg.RestoreExecutorUser != "postgres" ||
		cfg.RestoreOperationalDatabase != "loom_override" ||
		cfg.RestoreOperationalOwner != "loom_file_owner" ||
		cfg.RestoreProvenanceDatabase != "loom_provenance_file" ||
		cfg.RestoreProvenanceOwner != "loom_provenance_override_owner" {
		t.Fatalf("restore authority precedence = %#v", cfg.SafeFields())
	}
}

func TestRestoreAuthorityConfigurationRejectsIdentityAndNamespaceConfusion(t *testing.T) {
	base, err := Load(Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*Config)
	}{
		{"relative_socket", func(cfg *Config) { cfg.RestoreAuthoritySocketPath = "run/authority.sock" }},
		{"old_shared_socket", func(cfg *Config) { cfg.RestoreAuthoritySocketPath = "/run/loom/restore-authority.sock" }},
		{"other_absolute_socket", func(cfg *Config) { cfg.RestoreAuthoritySocketPath = "/run/other/restore-authority.sock" }},
		{"socket_owner", func(cfg *Config) { cfg.RestoreAuthoritySocketOwner = "loom" }},
		{"socket_activator_user", func(cfg *Config) { cfg.RestoreActivatorUser = "postgres" }},
		{"executor_user", func(cfg *Config) { cfg.RestoreExecutorUser = "root" }},
		{"punctuated_group", func(cfg *Config) { cfg.RestoreAuthoritySocketGroup = "loom;root" }},
		{"system_operational", func(cfg *Config) { cfg.RestoreOperationalDatabase = "postgres" }},
		{"disposable_active", func(cfg *Config) { cfg.RestoreOperationalDatabase = "loom_restore_drill_active" }},
		{"cross_database", func(cfg *Config) { cfg.RestoreProvenanceDatabase = cfg.RestoreOperationalDatabase }},
		{"cross_owner", func(cfg *Config) { cfg.RestoreProvenanceOwner = cfg.RestoreOperationalOwner }},
		{"postgres_owner", func(cfg *Config) { cfg.RestoreOperationalOwner = "postgres" }},
		{"case_drift", func(cfg *Config) { cfg.RestoreProvenanceDatabase = "Loom_provenance" }},
		{"overlong", func(cfg *Config) { cfg.RestoreOperationalOwner = "a" + strings.Repeat("b", 63) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := base
			test.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatalf("unsafe restore authority config accepted: %#v", cfg.SafeFields())
			}
		})
	}
}

func TestRestoreAuthorityConfigurationRejectsSocketPathDriftFromLoadSources(t *testing.T) {
	const oldSharedSocketPath = "/run/loom/restore-authority.sock"
	for _, test := range []struct {
		name string
		load func(*testing.T) (Config, error)
	}{
		{
			name: "override",
			load: func(t *testing.T) (Config, error) {
				return Load(Overrides{RestoreAuthoritySocketPath: oldSharedSocketPath})
			},
		},
		{
			name: "environment",
			load: func(t *testing.T) (Config, error) {
				t.Setenv("LOOM_RESTORE_AUTHORITY_SOCKET_PATH", oldSharedSocketPath)
				return Load(Overrides{})
			},
		},
		{
			name: "config_file",
			load: func(t *testing.T) (Config, error) {
				path := filepath.Join(t.TempDir(), "loom.env")
				if err := os.WriteFile(path, []byte("LOOM_RESTORE_AUTHORITY_SOCKET_PATH="+oldSharedSocketPath+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return Load(Overrides{ConfigFile: path})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.load(t); err == nil || !strings.Contains(err.Error(), DefaultRestoreAuthoritySocketPath) {
				t.Fatalf("old shared restore-authority socket path was not rejected with the fixed path: %v", err)
			}
		})
	}
}

func TestRestoreAuthorityConfigurationRejectsActivatorAndExecutorDriftFromLoadSources(t *testing.T) {
	for _, test := range []struct {
		name string
		load func(*testing.T) (Config, error)
	}{
		{
			name: "activator_override",
			load: func(t *testing.T) (Config, error) {
				return Load(Overrides{RestoreActivatorUser: "postgres"})
			},
		},
		{
			name: "activator_environment",
			load: func(t *testing.T) (Config, error) {
				t.Setenv("LOOM_RESTORE_AUTHORITY_SOCKET_ACTIVATOR_USER", "postgres")
				return Load(Overrides{})
			},
		},
		{
			name: "activator_config_file",
			load: func(t *testing.T) (Config, error) {
				path := filepath.Join(t.TempDir(), "loom.env")
				if err := os.WriteFile(path, []byte("LOOM_RESTORE_AUTHORITY_SOCKET_ACTIVATOR_USER=postgres\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return Load(Overrides{ConfigFile: path})
			},
		},
		{
			name: "executor_override",
			load: func(t *testing.T) (Config, error) {
				return Load(Overrides{RestoreExecutorUser: "root"})
			},
		},
		{
			name: "executor_environment",
			load: func(t *testing.T) (Config, error) {
				t.Setenv("LOOM_RESTORE_AUTHORITY_EXECUTOR_USER", "root")
				return Load(Overrides{})
			},
		},
		{
			name: "executor_config_file",
			load: func(t *testing.T) (Config, error) {
				path := filepath.Join(t.TempDir(), "loom.env")
				if err := os.WriteFile(path, []byte("LOOM_RESTORE_AUTHORITY_EXECUTOR_USER=root\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return Load(Overrides{ConfigFile: path})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.load(t); err == nil || !strings.Contains(err.Error(), "systemd activator") {
				t.Fatalf("restore authority identity drift was not rejected: %v", err)
			}
		})
	}
}

// Exercise the public file/environment loader, including omission of the new
// flag by existing runtime-only configurations. Invalid supplied keys still fail
// validation even when recovery is off; an absent key is required only when on.
func TestHermesRecoveryActivationMatrix(t *testing.T) {
	for _, source := range []string{"environment", "file"} {
		for _, runtime := range []string{"", "false", "true"} {
			for _, recovery := range []string{"", "false", "true"} {
				for _, key := range []struct{ name, value string }{
					{"absent", ""}, {"valid", strings.Repeat("aB", 32)},
					{"short", strings.Repeat("a", 62)}, {"long", strings.Repeat("a", 66)},
					{"nonhex", strings.Repeat("z", 64)}, {"odd", strings.Repeat("a", 63)},
				} {
					t.Run(source+"/runtime="+runtime+"/recovery="+recovery+"/"+key.name, func(t *testing.T) {
						values := map[string]string{
							"LOOM_MORATHUSTRA_ENABLED":             runtime,
							"LOOM_MORATHUSTRA_RECOVERY_ENABLED":    recovery,
							"LOOM_MORATHUSTRA_RECOVERY_PUBLIC_KEY": key.value,
						}
						t.Setenv("LOOM_CONFIG_FILE", "")
						overrides := Overrides{}
						var lines []string
						for name, value := range values {
							t.Setenv(name, "")
							if source == "environment" {
								t.Setenv(name, value)
							} else if value != "" {
								lines = append(lines, name+"="+value)
							}
						}
						if source == "file" {
							overrides.ConfigFile = filepath.Join(t.TempDir(), "fixture.conf")
							if err := os.WriteFile(overrides.ConfigFile, []byte(strings.Join(lines, "\n")), 0600); err != nil {
								t.Fatal(err)
							}
						}
						cfg, err := Load(overrides)
						wantError := (key.name != "valid" && key.name != "absent") || (recovery == "true" && key.name == "absent")
						if wantError {
							if err == nil || !strings.Contains(err.Error(), "recovery public key") {
								t.Fatalf("invalid trust accepted: %v", err)
							}
							return
						}
						if err != nil {
							t.Fatal(err)
						}
						if cfg.MorathustraEnabled != (runtime == "true") || cfg.MorathustraRecoveryEnabled != (recovery == "true") {
							t.Fatal("activation flags coupled or dropped")
						}
						policy, err := cfg.HermesRecoveryPolicy()
						if err != nil || policy.Enabled != (recovery == "true") || policy.Workspace != "/srv/loom/agents/morathustra" {
							t.Fatalf("invalid policy: %#v %v", policy, err)
						}
						if key.name == "valid" && len(policy.PublicKey) != 32 {
							t.Fatal("public trust not bound")
						}
					})
				}
			}
		}
	}
}

func TestHermesRecoveryInvalidActivationFlags(t *testing.T) {
	for _, name := range []string{"LOOM_MORATHUSTRA_ENABLED", "LOOM_MORATHUSTRA_RECOVERY_ENABLED"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("LOOM_CONFIG_FILE", "")
			t.Setenv("LOOM_MORATHUSTRA_ENABLED", "false")
			t.Setenv("LOOM_MORATHUSTRA_RECOVERY_ENABLED", "false")
			t.Setenv("LOOM_MORATHUSTRA_RECOVERY_PUBLIC_KEY", "")
			t.Setenv(name, "invalid-flag-do-not-echo")
			_, err := Load(Overrides{})
			if err == nil || !strings.Contains(err.Error(), "enable flag") || strings.Contains(err.Error(), "do-not-echo") {
				t.Fatalf("invalid flag not safely rejected: %v", err)
			}
		})
	}
}

func TestHermesRecoveryEnvironmentOverridesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.conf")
	if err := os.WriteFile(path, []byte("LOOM_MORATHUSTRA_ENABLED=true\nLOOM_MORATHUSTRA_RECOVERY_ENABLED=true\nLOOM_MORATHUSTRA_RECOVERY_PUBLIC_KEY="+strings.Repeat("a", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOOM_MORATHUSTRA_ENABLED", "true")
	t.Setenv("LOOM_MORATHUSTRA_RECOVERY_ENABLED", "false")
	t.Setenv("LOOM_MORATHUSTRA_RECOVERY_PUBLIC_KEY", "")
	cfg, err := Load(Overrides{ConfigFile: path})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := cfg.HermesRecoveryPolicy()
	if err != nil || !cfg.MorathustraEnabled || policy.Enabled {
		t.Fatalf("explicit recovery disable ignored: %#v %v", policy, err)
	}
}

func TestMinaHermesRecoveryPolicySelection(t *testing.T) {
	keys := []string{"LOOM_CONFIG_FILE", "LOOM_MORATHUSTRA_ENABLED", "LOOM_MORATHUSTRA_RECOVERY_ENABLED", "LOOM_MORATHUSTRA_RECOVERY_PUBLIC_KEY", "LOOM_MINA_SELECTED", "LOOM_MINA_ENABLED", "LOOM_MINA_RECOVERY_ENABLED", "LOOM_MINA_RECOVERY_PUBLIC_KEY", "LOOM_MINA_RECOVERY_RETAINED_MORATHUSTRA_IDS"}
	for _, k := range keys {
		t.Setenv(k, "")
	}
	for _, source := range []string{"file", "environment"} {
		for _, runtime := range []bool{false, true} {
			for _, recovery := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%v/%v", source, runtime, recovery), func(t *testing.T) {
					vals := map[string]string{"LOOM_MINA_SELECTED": "true", "LOOM_MINA_ENABLED": fmt.Sprint(runtime), "LOOM_MINA_RECOVERY_ENABLED": fmt.Sprint(recovery), "LOOM_MINA_RECOVERY_PUBLIC_KEY": strings.Repeat("ab", 32), "LOOM_MINA_RECOVERY_RETAINED_MORATHUSTRA_IDS": `["retained-v1","retained-v2"]`}
					o := Overrides{}
					var lines []string
					for k, v := range vals {
						if source == "environment" {
							t.Setenv(k, v)
						} else {
							lines = append(lines, k+"="+v)
						}
					}
					if source == "file" {
						o.ConfigFile = filepath.Join(t.TempDir(), "fixture.conf")
						if err := os.WriteFile(o.ConfigFile, []byte(strings.Join(lines, "\n")), 0600); err != nil {
							t.Fatal(err)
						}
					}
					c, err := Load(o)
					if err != nil {
						t.Fatal(err)
					}
					p, err := c.HermesRecoveryPolicy()
					if err != nil || string(p.Identity) != "mina" || p.Workspace != "/srv/loom/agents/mina" || p.Enabled != recovery || c.MinaEnabled != runtime || len(p.PublicKey) != 32 || len(p.RetainedMorathustra) != 2 {
						t.Fatalf("wrong selection: %+v %v", p, err)
					}
				})
			}
		}
	}
	for _, vals := range []map[string]string{
		{"LOOM_MINA_SELECTED": "unknown"},
		{"LOOM_MINA_ENABLED": "unknown"},
		{"LOOM_MINA_RECOVERY_ENABLED": "unknown"},
		{"LOOM_MINA_RECOVERY_PUBLIC_KEY": strings.Repeat("a", 64)},
		{"LOOM_MINA_RECOVERY_RETAINED_MORATHUSTRA_IDS": `[]`},
		{"LOOM_MINA_ENABLED": "true", "LOOM_MORATHUSTRA_ENABLED": "true"},
		{"LOOM_MINA_SELECTED": "true", "LOOM_MORATHUSTRA_RECOVERY_ENABLED": "true"},
		{"LOOM_MINA_SELECTED": "true", "LOOM_MORATHUSTRA_RECOVERY_PUBLIC_KEY": strings.Repeat("a", 64)},
		{"LOOM_MINA_RECOVERY_ENABLED": "true"},
		{"LOOM_MINA_SELECTED": "true", "LOOM_MINA_RECOVERY_PUBLIC_KEY": "bad"},
	} {
		t.Run("refusal", func(t *testing.T) {
			for k, v := range vals {
				t.Setenv(k, v)
			}
			if _, err := Load(Overrides{}); err == nil {
				t.Fatal("conflicting or invalid binding admitted")
			}
		})
	}
	for _, raw := range []string{`null`, `{}`, `"old"`, `[1]`, `[null]`, `["one","one"]`, `["*"]`, `["../old"]`, `["UPPER"]`, `[""]`, `["one"] []`, `one,two`} {
		t.Run("retained/"+raw, func(t *testing.T) {
			t.Setenv("LOOM_MINA_SELECTED", "true")
			t.Setenv("LOOM_MINA_RECOVERY_RETAINED_MORATHUSTRA_IDS", raw)
			if _, err := Load(Overrides{}); err == nil {
				t.Fatal("non-exact retained IDs admitted")
			}
		})
	}
}
