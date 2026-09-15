package migrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLatestVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "00042_test.sql")
	if err := os.WriteFile(path, []byte("-- +goose Up\nselect 1;\n-- +goose Down\nselect 1;\n"), 0o600); err != nil {
		t.Fatalf("write migration: %v", err)
	}

	version, err := LatestVersion(dir)
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version != 42 {
		t.Fatalf("version = %d, want 42", version)
	}
}

func TestLatestVersionNoFiles(t *testing.T) {
	version, err := LatestVersion(t.TempDir())
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version != 0 {
		t.Fatalf("version = %d, want 0", version)
	}
}

func TestRepoMigrationsIncludeSlice7Registry(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 6 {
		t.Fatalf("version = %d, want at least 6", version)
	}
}

func TestRepoMigrationsIncludeSlice8Policy(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 7 {
		t.Fatalf("version = %d, want at least 7", version)
	}
}

func TestRepoMigrationsIncludeSlice9Routing(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 8 {
		t.Fatalf("version = %d, want at least 8", version)
	}
}

func TestRepoMigrationsIncludeSlice10NodeAgentCommunication(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 9 {
		t.Fatalf("version = %d, want at least 9", version)
	}
}

func TestRepoMigrationsIncludeV05NodeProfileSeeds(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 36 {
		t.Fatalf("version = %d, want at least 36", version)
	}
}

func TestRepoMigrationsIncludeSlice11ProviderAdvertisement(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 10 {
		t.Fatalf("version = %d, want at least 10", version)
	}
}

func TestRepoMigrationsIncludeSlice12WorkspaceSync(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 11 {
		t.Fatalf("version = %d, want at least 11", version)
	}
}

func TestRepoMigrationsIncludeSlice15RealtimePrimitives(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 12 {
		t.Fatalf("version = %d, want at least 12", version)
	}
}

func TestRepoMigrationsIncludeSlice16AgentToolVisibility(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 13 {
		t.Fatalf("version = %d, want at least 13", version)
	}
}

func TestRepoMigrationsIncludeSlice17MinimalModuleSubstrate(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 14 {
		t.Fatalf("version = %d, want at least 14", version)
	}
}

func TestRepoMigrationsIncludeSlice17ModuleLifecycle(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 15 {
		t.Fatalf("version = %d, want at least 15", version)
	}
}

func TestRepoMigrationsIncludeSlice17ModuleExposureAndBackup(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 16 {
		t.Fatalf("version = %d, want at least 16", version)
	}
}

func TestRepoMigrationsIncludeV02Slice01WorkerCore(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 17 {
		t.Fatalf("version = %d, want at least 17", version)
	}
}

func TestRepoMigrationsIncludeV02Slice02MainMaintenance(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 18 {
		t.Fatalf("version = %d, want at least 18", version)
	}
}

func TestRepoMigrationsIncludeV02Slice04IndexingWorkerQueue(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 19 {
		t.Fatalf("version = %d, want at least 19", version)
	}
}

func TestRepoMigrationsIncludeV02Slice05JobRunnerQueue(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 20 {
		t.Fatalf("version = %d, want at least 20", version)
	}
}

func TestRepoMigrationsIncludeV02Slice06AutomationScheduler(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 21 {
		t.Fatalf("version = %d, want at least 21", version)
	}
}

func TestRepoMigrationsIncludeV02Slice07DirectEvents(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 23 {
		t.Fatalf("version = %d, want at least 23", version)
	}
}

func TestRepoMigrationsIncludeV02Slice10WatchedRootReports(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 24 {
		t.Fatalf("version = %d, want at least 24", version)
	}
}

func TestRepoMigrationsIncludeV02Slice11WatchedRootBackups(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 25 {
		t.Fatalf("version = %d, want at least 25", version)
	}
}

func TestRepoMigrationsIncludeV03Slice02CapabilityRuntimeBindings(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 26 {
		t.Fatalf("version = %d, want at least 26", version)
	}
}

func TestRepoMigrationsIncludeV03Slice04ProjectContractRegistrations(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 27 {
		t.Fatalf("version = %d, want at least 27", version)
	}
}

func TestRepoMigrationsIncludeProjectRepositoryPersistenceSchema(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 62 {
		t.Fatalf("version = %d, want at least 62", version)
	}
}

func TestRepoMigrationsIncludeV03Slice05ProjectScriptExposures(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 28 {
		t.Fatalf("version = %d, want at least 28", version)
	}
}

func TestRepoMigrationsIncludeV03Slice06ProjectScheduleRegistrations(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 29 {
		t.Fatalf("version = %d, want at least 29", version)
	}
}

func TestRepoMigrationsIncludeV03Slice07ProjectDirectEventRegistrations(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 30 {
		t.Fatalf("version = %d, want at least 30", version)
	}
}

func TestRepoMigrationsIncludeV03Slice08ProjectWatchedRootRegistrations(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 31 {
		t.Fatalf("version = %d, want at least 31", version)
	}
}

func TestRepoMigrationsIncludeV03Slice09ProjectConnectorRegistrations(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 32 {
		t.Fatalf("version = %d, want at least 32", version)
	}
}

func TestRepoMigrationsIncludeV031WorkflowExecutablePackages(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 35 {
		t.Fatalf("version = %d, want at least 35", version)
	}
}

func TestV041Slice04BoxWatchRootRegistrationsMigrationContract(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "00035_box_watch_root_registrations.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)

	for _, want := range []string{
		"CREATE SCHEMA IF NOT EXISTS box",
		"CREATE TABLE IF NOT EXISTS box.watch_root_registrations",
		"box_watch_root_registration_id text PRIMARY KEY CHECK (box_watch_root_registration_id LIKE 'box_watch_root_registration_%')",
		"box_id text NOT NULL CHECK (box_id LIKE 'box_%')",
		"area_key text NOT NULL CHECK (area_key IN ('notes', 'launchpad'))",
		"watched_root_id text NULL REFERENCES watched_roots.roots",
		"UNIQUE (node_id, box_id, backend_root_key)",
		"UNIQUE (node_id, box_root_path, area_key)",
		"CREATE INDEX IF NOT EXISTS box_watch_root_registrations_box_status_idx",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("migration missing %q", want)
		}
	}
}

func TestV053Slice06BoxAreaScopeTypeMigrationContract(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 37 {
		t.Fatalf("version = %d, want at least 37", version)
	}

	path := filepath.Join("..", "..", "migrations", "00037_box_area_scope_type.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	for _, want := range []string{
		"'box_area'",
		"Scope for a LOOM Box area such as Notes or Launchpad.",
		"allows_object_links",
		"ON CONFLICT (scope_type) DO UPDATE",
		"WHERE scope_type = 'box_area'",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("migration missing %q", want)
		}
	}
}

func TestV06Slice01StorageCatalogMigrationContract(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 38 {
		t.Fatalf("version = %d, want at least 38", version)
	}

	path := filepath.Join("..", "..", "migrations", "00038_storage_catalog.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)

	for _, want := range []string{
		"CREATE SCHEMA IF NOT EXISTS storage",
		"CREATE TABLE IF NOT EXISTS storage.storage_entries",
		"storage_entry_id text PRIMARY KEY CHECK (storage_entry_id LIKE 'storage_entry_%')",
		"storage_class text NOT NULL CHECK (storage_class IN",
		"'dropzone_custody'",
		"'main_document'",
		"'archive_entry'",
		"availability_state text NOT NULL DEFAULT 'available'",
		"processing_state text NOT NULL DEFAULT 'metadata_only'",
		"retention_state text NOT NULL DEFAULT 'none'",
		"metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')",
		"CREATE TABLE IF NOT EXISTS storage.storage_entry_versions",
		"CREATE TABLE IF NOT EXISTS storage.storage_physical_refs",
		"CREATE TABLE IF NOT EXISTS storage.storage_view_entries",
		"CREATE TABLE IF NOT EXISTS storage.archive_manifests",
		"CREATE TABLE IF NOT EXISTS storage.retention_entries",
		"CREATE TABLE IF NOT EXISTS storage.tombstones",
		"CREATE TABLE IF NOT EXISTS storage.project_runtime_archives",
		"CREATE TABLE IF NOT EXISTS storage.export_roots",
		"CREATE INDEX IF NOT EXISTS storage_entries_logical_path_idx",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("storage catalog migration missing %q", want)
		}
	}
}

func TestV06Slice05BoxDocumentsAreaMigrationContract(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 39 {
		t.Fatalf("version = %d, want at least 39", version)
	}

	path := filepath.Join("..", "..", "migrations", "00039_box_documents_area.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	for _, want := range []string{
		"DROP CONSTRAINT IF EXISTS watch_root_registrations_area_key_check",
		"CHECK (area_key IN ('notes', 'documents', 'launchpad'))",
		"DELETE FROM box.watch_root_registrations",
		"WHERE area_key = 'documents'",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("migration missing %q", want)
		}
	}
}

func TestV063StorageLaneAndCloudConstraintMigrationContract(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 42 {
		t.Fatalf("version = %d, want at least 42", version)
	}

	path := filepath.Join("..", "..", "migrations", "00042_storage_lane_and_cloud_constraints.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	for _, want := range []string{
		"DROP CONSTRAINT IF EXISTS storage_entries_storage_class_check",
		"'lane_custody'",
		"DROP CONSTRAINT IF EXISTS storage_entries_source_area_check",
		"'lane'",
		"DROP CONSTRAINT IF EXISTS storage_physical_refs_ref_kind_check",
		"'lane_file'",
		"'cloud_object'",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("lane/cloud constraint migration missing %q", want)
		}
	}
}

func TestV065StorageRetentionPayloadConstraintMigrationContract(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 43 {
		t.Fatalf("version = %d, want at least 43", version)
	}

	path := filepath.Join("..", "..", "migrations", "00043_storage_retention_payload_ref_kind.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	for _, want := range []string{
		"DROP CONSTRAINT IF EXISTS storage_physical_refs_ref_kind_check",
		"'retention_payload'",
		"'lane_file'",
		"'cloud_object'",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("retention payload constraint migration missing %q", want)
		}
	}
}

func TestV068LaneCustodyProcessingStateMigrationContract(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 46 {
		t.Fatalf("version = %d, want at least 46", version)
	}

	path := filepath.Join("..", "..", "migrations", "00046_lane_custody_processing_state.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	for _, want := range []string{
		"UPDATE storage.storage_entries",
		"processing_state = 'backup_only'",
		"storage_class = 'lane_custody'",
		"source_area = 'lane'",
		"processing_state = 'metadata_only'",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("lane custody processing migration missing %q", want)
		}
	}
}

func TestV08Slice01KnowledgeNotesIndexMigrationContract(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 47 {
		t.Fatalf("version = %d, want at least 47", version)
	}

	path := filepath.Join("..", "..", "migrations", "00047_knowledge_notes_index.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	for _, want := range []string{
		"CREATE SCHEMA IF NOT EXISTS knowledge",
		"CREATE TABLE IF NOT EXISTS knowledge.notes_source_roots",
		"notes_source_root_id text PRIMARY KEY CHECK (notes_source_root_id LIKE 'notes_source_root_%')",
		"root_kind text NOT NULL CHECK (root_kind IN ('box_notes', 'project_notes'))",
		"CREATE TABLE IF NOT EXISTS knowledge.knowledge_objects",
		"knowledge_object_id text PRIMARY KEY CHECK (knowledge_object_id LIKE 'knowledge_object_%')",
		"CREATE TABLE IF NOT EXISTS knowledge.knowledge_object_versions",
		"knowledge_object_version_id text PRIMARY KEY CHECK (knowledge_object_version_id LIKE 'knowledge_object_version_%')",
		"CREATE TABLE IF NOT EXISTS knowledge.knowledge_chunks",
		"knowledge_chunk_id text PRIMARY KEY CHECK (knowledge_chunk_id LIKE 'knowledge_chunk_%')",
		"CREATE TABLE IF NOT EXISTS knowledge.pipeline_statuses",
		"knowledge_pipeline_status_id text PRIMARY KEY CHECK (knowledge_pipeline_status_id LIKE 'knowledge_pipeline_status_%')",
		"CREATE TABLE IF NOT EXISTS knowledge.object_links",
		"knowledge_object_link_id text PRIMARY KEY CHECK (knowledge_object_link_id LIKE 'knowledge_object_link_%')",
		"stage IN ('metadata', 'text_extraction', 'chunking', 'bm25', 'projection')",
		"CREATE UNIQUE INDEX IF NOT EXISTS knowledge_objects_active_source_path_idx",
		"CREATE UNIQUE INDEX IF NOT EXISTS pipeline_statuses_identity_idx",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("knowledge notes index migration missing %q", want)
		}
	}
}

func TestV08Slice05KnowledgeSearchDocumentsMigrationContract(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 48 {
		t.Fatalf("version = %d, want at least 48", version)
	}

	path := filepath.Join("..", "..", "migrations", "00048_knowledge_search_documents.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	for _, want := range []string{
		"source_kind IN (",
		"'knowledge_chunk'",
		"CREATE INDEX IF NOT EXISTS search_search_documents_metadata_idx",
		"CREATE INDEX IF NOT EXISTS search_search_documents_knowledge_source_idx",
		"DELETE FROM search.search_documents",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("knowledge search documents migration missing %q", want)
		}
	}
}

func TestV084NotesHybridSearchMigrationContract(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 50 {
		t.Fatalf("version = %d, want at least 50", version)
	}

	path := filepath.Join("..", "..", "migrations", "00050_notes_hybrid_search.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	for _, want := range []string{
		"'knowledge_object_metadata'",
		"CREATE TABLE IF NOT EXISTS search.lexical_documents",
		"CREATE TABLE IF NOT EXISTS search.lexical_terms",
		"REFERENCES search.search_documents(search_document_id) ON DELETE CASCADE",
		"field_lengths jsonb NOT NULL DEFAULT '{}'::jsonb",
		"document_length integer NOT NULL DEFAULT 0",
		"PRIMARY KEY (search_document_id, field_key, term)",
		"CREATE INDEX IF NOT EXISTS search_lexical_terms_term_idx",
		"DELETE FROM search.search_documents",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("notes hybrid search migration missing %q", want)
		}
	}
}

func TestV085NotesFileExtractionFoundationMigrationContract(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 51 {
		t.Fatalf("version = %d, want at least 51", version)
	}

	path := filepath.Join("..", "..", "migrations", "00051_notes_file_extraction_foundation.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	for _, want := range []string{
		"DROP CONSTRAINT IF EXISTS storage_entries_file_class_check",
		"ADD CONSTRAINT storage_entries_file_class_check CHECK",
		"DROP CONSTRAINT IF EXISTS knowledge_objects_file_class_check",
		"ADD CONSTRAINT knowledge_objects_file_class_check CHECK",
		"DROP CONSTRAINT IF EXISTS knowledge_object_versions_file_class_check",
		"ADD CONSTRAINT knowledge_object_versions_file_class_check CHECK",
		"'office_document'",
		"'package'",
		"'generated_metadata'",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("notes file extraction foundation migration missing %q", want)
		}
	}
}

func TestV086NotesEmbeddingsMigrationContract(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 52 {
		t.Fatalf("version = %d, want at least 52", version)
	}

	path := filepath.Join("..", "..", "migrations", "00052_notes_embeddings.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	for _, want := range []string{
		"CREATE EXTENSION IF NOT EXISTS vector",
		"stage IN ('metadata', 'text_extraction', 'chunking', 'bm25', 'projection', 'embedding')",
		"CREATE TABLE IF NOT EXISTS knowledge.embedding_runtime_models",
		"embedding_runtime_model_ollama_mxbai_embed_large_1024",
		"CREATE TABLE IF NOT EXISTS knowledge.embedding_settings",
		"enabled boolean NOT NULL DEFAULT false",
		"ollama_url text NOT NULL DEFAULT 'http://127.0.0.1:11434'",
		"quiet_window_seconds integer NOT NULL DEFAULT 600",
		"global_concurrency integer NOT NULL DEFAULT 1",
		"history_per_lineage integer NOT NULL DEFAULT 5",
		"CREATE TABLE IF NOT EXISTS knowledge.embedding_object_states",
		"generation bigint NOT NULL DEFAULT 0",
		"eligible_at timestamptz NULL",
		"CREATE TABLE IF NOT EXISTS knowledge.chunk_embeddings",
		"embedding vector(1024) NOT NULL",
		"CREATE INDEX IF NOT EXISTS chunk_embeddings_embedding_hnsw_idx",
		"CREATE TABLE IF NOT EXISTS knowledge.embedding_work_items",
		"claimed_by_worker_run_id text NOT NULL DEFAULT ''",
		"CREATE UNIQUE INDEX IF NOT EXISTS embedding_work_items_active_chunk_generation_idx",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("notes embeddings migration missing %q", want)
		}
	}
	if strings.Contains(sql, "ai_tag") {
		t.Fatal("v0.8.6 embeddings migration must not add ai-generated tag schema")
	}
}

func TestV092DropzoneCustodyMigrationContract(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 53 {
		t.Fatalf("version = %d, want at least 53", version)
	}

	path := filepath.Join("..", "..", "migrations", "00053_dropzone_custody_processing_state.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	for _, want := range []string{
		"UPDATE storage.storage_entries",
		"processing_state = 'backup_only'",
		"storage_class = 'dropzone_custody'",
		"source_area = 'dropzone'",
		"availability_state = 'available'",
		"processing_state = 'metadata_only'",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("dropzone custody migration missing %q", want)
		}
	}
}

func TestV092JobAttentionLifecycleMigrationContract(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 54 {
		t.Fatalf("version = %d, want at least 54", version)
	}

	path := filepath.Join("..", "..", "migrations", "00054_job_attention_lifecycle.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	for _, want := range []string{
		"ALTER TABLE jobs.jobs",
		"failure_attention_status text NOT NULL DEFAULT 'active'",
		"failure_attention_updated_at timestamptz NULL",
		"failure_attention_updated_by_actor_id text NULL",
		"failure_attention_note text NOT NULL DEFAULT ''",
		"jobs_jobs_failure_attention_status_check",
		"failure_attention_status IN ('active', 'acknowledged', 'archived')",
		"jobs_jobs_failure_attention_updated_by_actor_id_fkey",
		"REFERENCES identity.actors(actor_id)",
		"CREATE INDEX IF NOT EXISTS jobs_jobs_failure_attention_idx",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("job attention lifecycle migration missing %q", want)
		}
	}
}

func TestWorkerTickIntervalTuningMigrationContract(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 55 {
		t.Fatalf("version = %d, want at least 55", version)
	}

	path := filepath.Join("..", "..", "migrations", "00055_worker_tick_interval_tuning.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	for _, want := range []string{
		"workers.worker_kinds",
		"workers.worker_instances",
		"default_tick_policy_json",
		"tick_policy_json",
		"idle_tick_interval_seconds",
		"main.realtime_expiry', 60",
		"main.automation_dispatcher', 60",
		"main.indexer_text', 60",
		"main.knowledge_indexer', 60",
		"main.main_documents_import', 300",
		"main.policy_expiry', 300",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("worker tick interval tuning migration missing %q", want)
		}
	}
}

func TestV062Slice05FileTransferMigrationContract(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 40 {
		t.Fatalf("version = %d, want at least 40", version)
	}

	path := filepath.Join("..", "..", "migrations", "00040_file_transfers.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS storage.file_transfers",
		"file_transfer_id text PRIMARY KEY CHECK (file_transfer_id LIKE 'file_transfer_%')",
		"CREATE TABLE IF NOT EXISTS storage.file_transfer_chunks",
		"file_transfer_chunk_id text PRIMARY KEY CHECK (file_transfer_chunk_id LIKE 'file_transfer_chunk_%')",
		"'watched_root_backup'",
		"'dropzone_custody'",
		"'main_documents_import'",
		"'manual_upload'",
		"'backup_copy'",
		"'custody_transfer'",
		"'main_owned'",
		"CREATE UNIQUE INDEX IF NOT EXISTS file_transfers_idempotency_key_idx",
		"UNIQUE (file_transfer_id, chunk_index)",
		"storage_entry_id text NULL CHECK",
		"dropzone_transfer_id text NOT NULL DEFAULT ''",
		"metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("file transfer migration missing %q", want)
		}
	}
}

func TestV031WorkflowExecutablePackagesMigrationContract(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "00034_workflow_executable_packages.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)

	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS packages.workflows",
		"CREATE TABLE IF NOT EXISTS packages.workflow_versions",
		"workflow_id text PRIMARY KEY CHECK (workflow_id LIKE 'workflow_%')",
		"workflow_version_id text PRIMARY KEY CHECK (workflow_version_id LIKE 'workflow_version_%')",
		"active_version_id text NULL",
		"package_root text NOT NULL",
		"entrypoint_json jsonb NOT NULL",
		"runtime_json jsonb NOT NULL DEFAULT '{}'::jsonb",
		"artifact_policy_json jsonb NOT NULL DEFAULT '{}'::jsonb",
		"usage_documents_json jsonb NOT NULL DEFAULT '[]'::jsonb",
		"DROP CONSTRAINT jobs_job_type_check",
		"DROP CONSTRAINT jobs_jobs_job_type_check",
		"job_type IN ('script_run', 'workflow_run')",
		"ADD COLUMN IF NOT EXISTS workflow_id text NULL REFERENCES packages.workflows(workflow_id)",
		"ADD COLUMN IF NOT EXISTS workflow_version_id text NULL REFERENCES packages.workflow_versions(workflow_version_id)",
		"CREATE INDEX IF NOT EXISTS jobs_jobs_workflow_idx",
		"CREATE TABLE IF NOT EXISTS projects.project_workflow_registrations",
		"project_workflow_registration_id text PRIMARY KEY CHECK (project_workflow_registration_id LIKE 'project_workflow_registration_%')",
		"project_contract_registration_id text NOT NULL REFERENCES projects.project_contract_registrations",
		"workflow_key text NOT NULL",
		"implementation_kind text NOT NULL DEFAULT ''",
		"runtime_kind text NOT NULL DEFAULT ''",
		"workflow_id text NULL REFERENCES packages.workflows(workflow_id)",
		"workflow_version_id text NULL REFERENCES packages.workflow_versions(workflow_version_id)",
		"capability_endpoint_id text NULL REFERENCES capabilities.capability_endpoints(capability_endpoint_id)",
		"capability_endpoint_version_id text NULL REFERENCES capabilities.endpoint_versions(capability_endpoint_version_id)",
		"runtime_binding_id text NULL REFERENCES capabilities.endpoint_runtime_bindings(runtime_binding_id)",
		"activation_status text NOT NULL CHECK",
		"'active'",
		"'stale'",
		"UNIQUE (project_id, workflow_key)",
		"project_workflow_registrations_project_status_idx",
		"project_workflow_registrations_capability_idx",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("workflow executable migration missing %q", want)
		}
	}
}

func TestV03Slice09ProjectConnectorRegistrationsMigrationContract(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "00032_project_connector_registrations.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)

	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS projects.project_connector_registrations",
		"project_connector_registration_id text PRIMARY KEY CHECK (project_connector_registration_id LIKE 'project_connector_registration_%')",
		"project_contract_registration_id text NOT NULL REFERENCES projects.project_contract_registrations",
		"project_id text NOT NULL REFERENCES projects.projects",
		"connector_key text NOT NULL",
		"provider_key text NOT NULL",
		"provider_address text NOT NULL DEFAULT ''",
		"provider_id text NULL REFERENCES capabilities.providers",
		"runtime_kind text NOT NULL DEFAULT ''",
		"capability_count integer NOT NULL DEFAULT 0",
		"active_capability_count integer NOT NULL DEFAULT 0",
		"usage_document_count integer NOT NULL DEFAULT 0",
		"activation_status text NOT NULL CHECK",
		"'active'",
		"'stale'",
		"UNIQUE (project_id, connector_key)",
		"project_connector_registrations_project_status_idx",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("v0.3 slice 09 migration missing %q", want)
		}
	}
}

func TestV03Slice08ProjectWatchedRootRegistrationsMigrationContract(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "00031_project_watched_root_registrations.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)

	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS projects.project_watched_root_registrations",
		"project_watched_root_registration_id text PRIMARY KEY CHECK (project_watched_root_registration_id LIKE 'project_watched_root_registration_%')",
		"project_contract_registration_id text NOT NULL REFERENCES projects.project_contract_registrations",
		"project_id text NOT NULL REFERENCES projects.projects",
		"node_id text NOT NULL REFERENCES nodes.nodes",
		"backend_root_key text NOT NULL",
		"worker_key text NOT NULL DEFAULT ''",
		"config_hash text NOT NULL DEFAULT ''",
		"config_json jsonb NOT NULL DEFAULT '{}'::jsonb",
		"command_json jsonb NOT NULL DEFAULT '[]'::jsonb",
		"watched_root_id text NULL REFERENCES watched_roots.roots",
		"activation_status text NOT NULL CHECK",
		"'pending_agent_apply'",
		"'reported'",
		"'stale'",
		"UNIQUE (project_id, backend_root_key)",
		"UNIQUE (project_contract_registration_id, local_root_key)",
		"projects_watched_root_registrations_project_status_idx",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("v0.3 slice 08 migration missing %q", want)
		}
	}
}

func TestV03Slice07ProjectDirectEventRegistrationsMigrationContract(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "00030_project_direct_event_registrations.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)

	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS projects.project_direct_event_registrations",
		"project_direct_event_registration_id text PRIMARY KEY CHECK (project_direct_event_registration_id LIKE 'project_direct_event_registration_%')",
		"project_contract_registration_id text NOT NULL REFERENCES projects.project_contract_registrations",
		"project_id text NOT NULL REFERENCES projects.projects",
		"event_key text NOT NULL",
		"backend_integration_key text NOT NULL",
		"backend_endpoint_slug text NOT NULL",
		"target_capability text NOT NULL DEFAULT ''",
		"integration_id text NULL REFERENCES automation.integrations",
		"auth_profile_id text NULL REFERENCES automation.integration_auth_profiles",
		"endpoint_id text NULL REFERENCES automation.direct_event_endpoints",
		"automation_id text NULL REFERENCES automation.automations",
		"activation_status text NOT NULL CHECK",
		"'paused'",
		"'disabled'",
		"'stale'",
		"UNIQUE (project_id, event_key)",
		"projects_direct_event_registrations_project_status_idx",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("v0.3 slice 07 migration missing %q", want)
		}
	}
}

func TestV03Slice06ProjectScheduleRegistrationsMigrationContract(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "00029_project_schedule_registrations.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)

	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS projects.project_schedule_registrations",
		"project_schedule_registration_id text PRIMARY KEY CHECK (project_schedule_registration_id LIKE 'project_schedule_registration_%')",
		"project_contract_registration_id text NOT NULL REFERENCES projects.project_contract_registrations",
		"project_id text NOT NULL REFERENCES projects.projects",
		"schedule_key text NOT NULL",
		"backend_schedule_key text NOT NULL",
		"target_capability text NOT NULL DEFAULT ''",
		"automation_id text NULL REFERENCES automation.automations",
		"schedule_id text NULL REFERENCES automation.schedules",
		"activation_status text NOT NULL CHECK",
		"'paused'",
		"'disabled'",
		"'stale'",
		"UNIQUE (project_id, schedule_key)",
		"projects_schedule_registrations_project_status_idx",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("v0.3 slice 06 migration missing %q", want)
		}
	}
}

func TestV03Slice05ProjectScriptExposuresMigrationContract(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "00028_project_script_exposures.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)

	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS projects.project_script_exposures",
		"project_script_exposure_id text PRIMARY KEY CHECK (project_script_exposure_id LIKE 'project_script_exposure_%')",
		"project_contract_registration_id text NOT NULL REFERENCES projects.project_contract_registrations",
		"project_id text NOT NULL REFERENCES projects.projects",
		"script_key text NOT NULL",
		"exposure_enabled boolean NOT NULL DEFAULT false",
		"provider_id text NULL REFERENCES capabilities.providers",
		"script_id text NULL REFERENCES packages.scripts",
		"capability_endpoint_id text NULL REFERENCES capabilities.capability_endpoints",
		"runtime_binding_id text NULL REFERENCES capabilities.endpoint_runtime_bindings",
		"activation_status text NOT NULL CHECK",
		"'active'",
		"'disabled'",
		"'stale'",
		"UNIQUE (project_id, script_key)",
		"projects_script_exposures_capability_idx",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("v0.3 slice 05 migration missing %q", want)
		}
	}
}

func TestV03Slice04ProjectContractRegistrationMigrationContract(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "00027_project_contract_registrations.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)

	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS projects.project_contract_registrations",
		"project_contract_registration_id text PRIMARY KEY CHECK (project_contract_registration_id LIKE 'project_contract_registration_%')",
		"project_id text UNIQUE NOT NULL REFERENCES projects.projects(project_id) ON DELETE CASCADE",
		"contract_hash text NOT NULL CHECK (contract_hash ~ '^sha256:[0-9a-f]{64}$')",
		"contract_json jsonb NOT NULL CHECK (jsonb_typeof(contract_json) = 'object')",
		"validation_report_json jsonb NOT NULL CHECK (jsonb_typeof(validation_report_json) = 'object')",
		"registration_plan_json jsonb NOT NULL CHECK (jsonb_typeof(registration_plan_json) = 'object')",
		"derived_providers_json jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(derived_providers_json) = 'array')",
		"registration_status text NOT NULL CHECK (registration_status IN ('registered', 'blocked', 'stale', 'archived'))",
		"activation_status text NOT NULL CHECK (activation_status IN ('inactive', 'base_active', 'facet_activation_pending', 'blocked'))",
		"CREATE TABLE IF NOT EXISTS projects.project_contract_facets",
		"project_contract_facet_id text PRIMARY KEY CHECK (project_contract_facet_id LIKE 'project_contract_facet_%')",
		"facet_status text NOT NULL CHECK",
		"'pending_later_slice'",
		"project_contract_registrations_project_idx",
		"project_contract_registrations_hash_idx",
		"project_contract_facets_project_idx",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("v0.3 slice 04 migration missing %q", want)
		}
	}

	for _, forbidden := range []string{
		"CREATE TABLE IF NOT EXISTS automation.",
		"CREATE TABLE IF NOT EXISTS schedules.",
		"CREATE TABLE IF NOT EXISTS direct_events.",
		"CREATE TABLE IF NOT EXISTS capabilities.",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("v0.3 slice 04 migration should not include %q", forbidden)
		}
	}
}

func TestProjectRepositoryPersistenceMigrationContract(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "00062_project_repository_state.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)

	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS projects.repositories",
		"repository_id text PRIMARY KEY CHECK (repository_id ~ '^repo_[0-7][0-9A-HJKMNP-TV-Z]{25}$')",
		"owning_project_id text NOT NULL REFERENCES projects.projects(project_id)",
		"lifecycle_status text NOT NULL CHECK (lifecycle_status IN ('active', 'archived'))",
		"CREATE TABLE IF NOT EXISTS projects.project_repository_memberships",
		"role text NOT NULL CHECK (role IN ('primary', 'component', 'reference'))",
		"FOREIGN KEY (repository_id, repository_owner_project_id)",
		"role IN ('primary', 'component') AND project_id = repository_owner_project_id",
		"role = 'reference' AND project_id <> repository_owner_project_id",
		"UNIQUE (project_id, member_key)",
		"UNIQUE (project_id, member_path)",
		"CONSTRAINT project_contract_registrations_registration_project_key",
		"UNIQUE (project_contract_registration_id, project_id)",
		"CREATE TABLE IF NOT EXISTS projects.project_repository_sources",
		"project_id text PRIMARY KEY REFERENCES projects.projects(project_id)",
		"CONSTRAINT project_repository_sources_registration_project_fkey",
		"FOREIGN KEY (project_contract_registration_id, project_id)",
		"REFERENCES projects.project_contract_registrations(project_contract_registration_id, project_id)",
		"project_contract_schema_version text NOT NULL CHECK",
		"repos_contract_schema_version text NOT NULL CHECK",
		"'project.contract.v0.3'",
		"'project.contract.v0.4'",
		"'repos.contract.v0.3'",
		"'repos.contract.v0.4'",
		"semantic_digest text NOT NULL CHECK (semantic_digest ~ '^sha256:[0-9a-f]{64}$')",
		"location_digest text NOT NULL CHECK (location_digest ~ '^sha256:[0-9a-f]{64}$')",
		"source_snapshot_json jsonb NOT NULL CHECK (jsonb_typeof(source_snapshot_json) = 'object')",
		"CREATE TABLE IF NOT EXISTS projects.project_repository_source_history",
		"source_change_kind text NOT NULL CHECK",
		"'first_registration'",
		"'semantic_change'",
		"'source_relocation'",
		"'semantic_change_and_relocation'",
		"change_summary_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(change_summary_json) = 'object')",
		"PRIMARY KEY (project_id, source_revision)",
		"CREATE TABLE IF NOT EXISTS projects.project_repository_observations",
		"source_binding_digest text NOT NULL CHECK (source_binding_digest ~ '^sha256:[0-9a-f]{64}$')",
		"observation_posture text NOT NULL CHECK",
		"'not_observed'",
		"'observed'",
		"'remote_unavailable'",
		"observation_posture = 'not_observed' AND observed_at IS NULL",
		"FOREIGN KEY (project_id, repository_id)",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("project repository persistence migration missing %q", want)
		}
	}

	sourceStart := strings.Index(sql, "CREATE TABLE IF NOT EXISTS projects.project_repository_sources")
	historyStart := strings.Index(sql, "CREATE TABLE IF NOT EXISTS projects.project_repository_source_history")
	historyEnd := strings.Index(sql, "CREATE TABLE IF NOT EXISTS projects.project_repository_observations")
	if sourceStart < 0 || historyStart <= sourceStart || historyEnd <= historyStart {
		t.Fatal("could not isolate current/history registration-source tables")
	}
	for name, tableSQL := range map[string]string{
		"current source": sql[sourceStart:historyStart],
		"source history": sql[historyStart:historyEnd],
	} {
		for _, versionColumn := range []string{"project_contract_schema_version", "repos_contract_schema_version"} {
			if strings.Count(tableSQL, versionColumn) != 2 {
				t.Fatalf("%s must store and constrain %s separately", name, versionColumn)
			}
		}
	}
	historySQL := sql[historyStart:historyEnd]
	for _, forbidden := range []string{
		"archive_state",
		"lifecycle_status",
		"project_status",
		"registration_status",
		"observation_posture",
		"'archived'",
		"UNIQUE (project_id, semantic_digest, location_digest)",
	} {
		if strings.Contains(historySQL, forbidden) {
			t.Fatalf("registration-source history must contain source snapshots only; found %q", forbidden)
		}
	}

	for _, forbidden := range []string{
		"INSERT INTO projects.",
		"UPDATE projects.",
		"DELETE FROM projects.",
		"portable_project_id",
		"runtime_project_id",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("schema-only migration must not perform registration mutation; found %q", forbidden)
		}
	}
}

func TestV03Slice02CapabilityRuntimeBindingsMigrationContract(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "00026_capability_runtime_bindings.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)

	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS capabilities.endpoint_runtime_bindings",
		"runtime_binding_id text PRIMARY KEY CHECK (runtime_binding_id LIKE 'runtime_binding_%')",
		"capability_endpoint_version_id text NOT NULL REFERENCES capabilities.endpoint_versions",
		"UNIQUE (capability_endpoint_version_id)",
		"jsonb_typeof(runtime_config_json) = 'object'",
		"jsonb_typeof(input_mapping_json) = 'object'",
		"jsonb_typeof(output_mapping_json) = 'object'",
		"jsonb_typeof(metadata) = 'object'",
		"'script'",
		"'command'",
		"'http'",
		"'node_agent'",
		"'native'",
		"'module'",
		"'workflow'",
		"'external_process'",
		"'registered'",
		"'active'",
		"'disabled'",
		"'deprecated'",
		"'revoked'",
		"capabilities_endpoint_runtime_bindings_version_idx",
		"capabilities_endpoint_runtime_bindings_kind_status_idx",
		"capabilities_endpoint_runtime_bindings_created_at_desc_idx",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("v0.3 slice 02 migration missing %q", want)
		}
	}

	for _, forbidden := range []string{
		"CREATE TABLE IF NOT EXISTS projects.",
		"CREATE TABLE IF NOT EXISTS automation.",
		"CREATE TABLE IF NOT EXISTS schedules.",
		"CREATE TABLE IF NOT EXISTS direct_events.",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("v0.3 slice 02 migration should not include %q", forbidden)
		}
	}
}

func TestV02Slice10WatchedRootReportsMigrationContract(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "00024_watched_root_reports.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)

	for _, want := range []string{
		"CREATE SCHEMA IF NOT EXISTS watched_roots",
		"CREATE TABLE IF NOT EXISTS watched_roots.roots",
		"CREATE TABLE IF NOT EXISTS watched_roots.findings",
		"watched_root_id text PRIMARY KEY CHECK (watched_root_id LIKE 'watched_root_%')",
		"watched_root_finding_id text PRIMARY KEY CHECK (watched_root_finding_id LIKE 'watched_root_finding_%')",
		"UNIQUE (node_id, root_key)",
		"UNIQUE (watched_root_id, finding_key)",
		"CREATE INDEX IF NOT EXISTS watched_roots_roots_node_status_idx",
		"CREATE INDEX IF NOT EXISTS watched_roots_findings_node_status_idx",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("migration missing %q", want)
		}
	}
}

func TestV02Slice11WatchedRootBackupsMigrationContract(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "00025_watched_root_backups.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)

	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS watched_roots.backup_batches",
		"CREATE TABLE IF NOT EXISTS watched_roots.backup_items",
		"watched_root_backup_batch_id text PRIMARY KEY CHECK (watched_root_backup_batch_id LIKE 'watched_root_backup_batch_%')",
		"watched_root_backup_item_id text PRIMARY KEY CHECK (watched_root_backup_item_id LIKE 'watched_root_backup_item_%')",
		"UNIQUE (node_id, idempotency_key)",
		"private_backup_operation_id text NULL REFERENCES sync.private_backup_operations(private_backup_operation_id)",
		"CREATE INDEX IF NOT EXISTS watched_roots_backup_batches_root_received_idx",
		"CREATE INDEX IF NOT EXISTS watched_roots_backup_batches_node_status_idx",
		"CREATE INDEX IF NOT EXISTS watched_roots_backup_items_root_path_idx",
		"CREATE INDEX IF NOT EXISTS watched_roots_backup_items_artifact_idx",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("migration missing %q", want)
		}
	}
}

func TestV02Slice07DirectEventsMigrationContract(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "00022_direct_events.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)

	for _, want := range []string{
		"ADD COLUMN IF NOT EXISTS integration_profile_json",
		"ADD COLUMN IF NOT EXISTS mapping_profile_json",
		"ADD COLUMN IF NOT EXISTS storage_profile_json",
		"CHECK (source_kind IN ('schedule', 'direct_event'))",
		"CREATE TABLE IF NOT EXISTS automation.integrations",
		"CREATE TABLE IF NOT EXISTS automation.integration_auth_profiles",
		"CREATE TABLE IF NOT EXISTS automation.direct_event_endpoints",
		"CREATE TABLE IF NOT EXISTS automation.direct_events",
		"integration_id text PRIMARY KEY CHECK (integration_id LIKE 'integration_%')",
		"auth_profile_id text PRIMARY KEY CHECK (auth_profile_id LIKE 'integration_auth_profile_%')",
		"endpoint_id text PRIMARY KEY CHECK (endpoint_id LIKE 'direct_event_endpoint_%')",
		"direct_event_id text PRIMARY KEY CHECK (direct_event_id LIKE 'direct_event_%')",
		"response_mode text NOT NULL CHECK (response_mode IN ('accepted', 'sync_wait'))",
		"CREATE UNIQUE INDEX IF NOT EXISTS automation_direct_events_endpoint_idempotency_idx",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("migration missing %q", want)
		}
	}
}

func TestV02Slice07DirectEventIngestMigrationContract(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "00023_direct_event_ingest.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)

	for _, want := range []string{
		"ADD COLUMN IF NOT EXISTS request_method",
		"ADD COLUMN IF NOT EXISTS payload_hash",
		"ADD COLUMN IF NOT EXISTS attempt_count",
		"ADD COLUMN IF NOT EXISTS ingest_worker_run_id",
		"ADD COLUMN IF NOT EXISTS route_id",
		"ADD COLUMN IF NOT EXISTS capability_call_id",
		"ADD COLUMN IF NOT EXISTS job_id",
		"ADD COLUMN IF NOT EXISTS result_json",
		"status IN (",
		"'accepted'",
		"CREATE INDEX IF NOT EXISTS automation_direct_events_claim_idx",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("migration missing %q", want)
		}
	}
}

func TestV02Slice01WorkerCoreMigrationContract(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "00017_worker_core.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)

	for _, want := range []string{
		"CREATE SCHEMA IF NOT EXISTS workers",
		"CREATE TABLE IF NOT EXISTS workers.worker_kinds",
		"CREATE TABLE IF NOT EXISTS workers.worker_instances",
		"CREATE TABLE IF NOT EXISTS workers.worker_runs",
		"CREATE TABLE IF NOT EXISTS workers.worker_leases",
		"CREATE TABLE IF NOT EXISTS workers.worker_checkpoints",
		"CREATE TABLE IF NOT EXISTS workers.worker_controls",
		"CREATE TABLE IF NOT EXISTS workers.worker_heartbeats",
		"CREATE TABLE IF NOT EXISTS workers.worker_health",
		"worker_instance_id text PRIMARY KEY CHECK (worker_instance_id LIKE 'worker_instance_%')",
		"worker_run_id text PRIMARY KEY CHECK (worker_run_id LIKE 'worker_run_%')",
		"worker_lease_id text PRIMARY KEY CHECK (worker_lease_id LIKE 'worker_lease_%')",
		"worker_checkpoint_id text PRIMARY KEY CHECK (worker_checkpoint_id LIKE 'worker_checkpoint_%')",
		"worker_control_id text PRIMARY KEY CHECK (worker_control_id LIKE 'worker_control_%')",
		"worker_heartbeat_id text PRIMARY KEY CHECK (worker_heartbeat_id LIKE 'worker_heartbeat_%')",
		"worker_health_id text PRIMARY KEY CHECK (worker_health_id LIKE 'worker_health_%')",
		"worker_kind text PRIMARY KEY",
		"'main_owned'",
		"'node_agent_owned'",
		"'external_reported'",
		"workers_worker_instances_current_run_fk",
		"workers_worker_instances_last_run_fk",
		"workers_worker_leases_active_key_unique_idx",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("v0.2 slice 01 migration missing %q", want)
		}
	}

	for _, forbidden := range []string{
		"CREATE TABLE IF NOT EXISTS workers.worker_events",
		"CREATE TABLE IF NOT EXISTS automation.",
		"CREATE TABLE IF NOT EXISTS schedules.",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("v0.2 slice 01 migration should not include %q", forbidden)
		}
	}
}

func TestV02Slice02MainMaintenanceMigrationContract(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "00018_main_maintenance.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)

	for _, want := range []string{
		"CREATE SCHEMA IF NOT EXISTS maintenance",
		"CREATE TABLE IF NOT EXISTS maintenance.operations",
		"CREATE TABLE IF NOT EXISTS maintenance.findings",
		"CREATE TABLE IF NOT EXISTS maintenance.artifacts",
		"maintenance_operation_id text PRIMARY KEY CHECK (maintenance_operation_id LIKE 'maintenance_operation_%')",
		"maintenance_finding_id text PRIMARY KEY CHECK (maintenance_finding_id LIKE 'maintenance_finding_%')",
		"maintenance_artifact_id text PRIMARY KEY CHECK (maintenance_artifact_id LIKE 'maintenance_artifact_%')",
		"operation_key text NOT NULL UNIQUE",
		"finding_key text NOT NULL UNIQUE",
		"severity text NOT NULL CHECK (severity IN ('info', 'warning', 'error', 'critical'))",
		"status text NOT NULL CHECK (status IN ('open', 'acknowledged', 'resolved', 'ignored'))",
		"maintenance_artifacts_operation_idx",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("v0.2 slice 02 migration missing %q", want)
		}
	}

	for _, forbidden := range []string{
		"CREATE TABLE IF NOT EXISTS backups.",
		"CREATE TABLE IF NOT EXISTS automation.",
		"VACUUM FULL",
		"REINDEX",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("v0.2 slice 02 migration should not include %q", forbidden)
		}
	}
}

func TestV02Slice04IndexingWorkerQueueMigrationContract(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "00019_indexing_worker_queue.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)

	for _, want := range []string{
		"ALTER TABLE search.index_status",
		"attempt_count integer NOT NULL DEFAULT 0",
		"next_attempt_at timestamptz NULL",
		"claimed_by_worker_run_id text NULL",
		"claim_expires_at timestamptz NULL",
		"priority integer NOT NULL DEFAULT 100",
		"manual_action_required boolean NOT NULL DEFAULT false",
		"last_worker_run_id text NULL",
		"last_attempt_at timestamptz NULL",
		"search_index_status_queue_ready_idx",
		"search_index_status_claimed_run_idx",
		"search_index_status_claim_expires_idx",
		"search_index_status_manual_action_idx",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("v0.2 slice 04 migration missing %q", want)
		}
	}

	for _, forbidden := range []string{
		"CREATE TABLE IF NOT EXISTS search.index_work_items",
		"CREATE TABLE IF NOT EXISTS automation.",
		"CREATE TABLE IF NOT EXISTS schedules.",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("v0.2 slice 04 migration should not include %q", forbidden)
		}
	}
}

func TestV02Slice05JobRunnerQueueMigrationContract(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "00020_job_runner_queue.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)

	for _, want := range []string{
		"ALTER TABLE jobs.jobs",
		"priority integer NOT NULL DEFAULT 100",
		"next_attempt_at timestamptz NULL",
		"manual_action_required boolean NOT NULL DEFAULT false",
		"last_worker_run_id text NULL",
		"last_heartbeat_at timestamptz NULL",
		"cancel_requested_at timestamptz NULL",
		"cancel_requested_by_actor_id text NULL",
		"jobs_jobs_last_worker_run_id_fkey",
		"jobs_jobs_cancel_requested_by_actor_id_fkey",
		"jobs_jobs_queue_ready_idx",
		"jobs_jobs_running_lease_idx",
		"jobs_jobs_manual_action_idx",
		"jobs_jobs_last_worker_run_idx",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("v0.2 slice 05 migration missing %q", want)
		}
	}

	for _, forbidden := range []string{
		"CREATE TABLE IF NOT EXISTS jobs.job_queue",
		"CREATE TABLE IF NOT EXISTS automation.",
		"CREATE TABLE IF NOT EXISTS schedules.",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("v0.2 slice 05 migration should not include %q", forbidden)
		}
	}
}

func TestV02Slice06AutomationSchedulerMigrationContract(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "00021_automation_scheduler.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)

	for _, want := range []string{
		"CREATE SCHEMA IF NOT EXISTS automation",
		"CREATE TABLE IF NOT EXISTS automation.automations",
		"CREATE TABLE IF NOT EXISTS automation.schedules",
		"CREATE TABLE IF NOT EXISTS automation.schedule_fires",
		"CREATE TABLE IF NOT EXISTS automation.invocations",
		"automation_id text PRIMARY KEY CHECK (automation_id LIKE 'automation_%')",
		"schedule_id text PRIMARY KEY CHECK (schedule_id LIKE 'schedule_%')",
		"schedule_fire_id text PRIMARY KEY CHECK (schedule_fire_id LIKE 'schedule_fire_%')",
		"invocation_id text PRIMARY KEY CHECK (invocation_id LIKE 'invocation_%')",
		"source_kind text NOT NULL CHECK (source_kind IN ('schedule'))",
		"schedule_kind text NOT NULL CHECK (schedule_kind IN ('one_shot', 'interval'))",
		"UNIQUE (schedule_id, scheduled_for)",
		"UNIQUE (source_kind, source_occurrence_ref)",
		"automation_schedules_status_next_fire_idx",
		"automation_invocations_claim_idx",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("v0.2 slice 06 migration missing %q", want)
		}
	}

	for _, forbidden := range []string{
		"direct_event",
		"integration_auth",
		"callback_deliveries",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("v0.2 slice 06 migration should not include %q", forbidden)
		}
	}
}

func TestSlice12WorkspaceSyncMigrationContract(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "00011_workspace_sync_and_backup.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)

	for _, want := range []string{
		"CREATE SCHEMA IF NOT EXISTS sync",
		"CREATE TABLE IF NOT EXISTS sync.local_events",
		"CREATE TABLE IF NOT EXISTS sync.local_outbox",
		"CREATE TABLE IF NOT EXISTS sync.local_cursors",
		"CREATE TABLE IF NOT EXISTS sync.ingested_events",
		"CREATE TABLE IF NOT EXISTS sync.cursors",
		"CREATE TABLE IF NOT EXISTS sync.batches",
		"CREATE TABLE IF NOT EXISTS sync.batch_items",
		"CREATE TABLE IF NOT EXISTS sync.conflicts",
		"CREATE TABLE IF NOT EXISTS sync.replicas",
		"CREATE TABLE IF NOT EXISTS sync.private_backup_operations",
		"CREATE TABLE IF NOT EXISTS sync.deletion_requests",
		"local_event_id text PRIMARY KEY CHECK (local_event_id LIKE 'local_event_%')",
		"sync_batch_id text PRIMARY KEY CHECK (sync_batch_id LIKE 'sync_batch_%')",
		"sync_cursor_id text PRIMARY KEY CHECK (sync_cursor_id LIKE 'sync_cursor_%')",
		"private_backup_operation_id text PRIMARY KEY CHECK (private_backup_operation_id LIKE 'private_backup_%')",
		"UNIQUE (origin_node_id, local_event_id)",
		"UNIQUE (origin_node_id, stream_name, local_sequence)",
		"'duplicate_payload_mismatch'",
		"'raw_private_backup'",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("slice 12 migration missing %q", want)
		}
	}

	for _, forbidden := range []string{
		"CREATE TABLE IF NOT EXISTS identity.actors",
		"CREATE TABLE IF NOT EXISTS nodes.nodes",
		"CREATE TABLE IF NOT EXISTS files.blobs",
		"CREATE TABLE IF NOT EXISTS objects.objects",
		"CREATE TABLE IF NOT EXISTS search.search_documents",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("slice 12 migration should not include %q", forbidden)
		}
	}
}

func TestSlice15RealtimePrimitivesMigrationContract(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "00012_minimal_realtime_primitives.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)

	for _, want := range []string{
		"CREATE SCHEMA IF NOT EXISTS realtime",
		"CREATE TABLE IF NOT EXISTS realtime.topics",
		"CREATE TABLE IF NOT EXISTS realtime.topic_publications",
		"CREATE TABLE IF NOT EXISTS realtime.subscriptions",
		"CREATE TABLE IF NOT EXISTS realtime.presence",
		"CREATE TABLE IF NOT EXISTS realtime.notifications",
		"CREATE TABLE IF NOT EXISTS realtime.notification_deliveries",
		"CREATE TABLE IF NOT EXISTS realtime.progress_feeds",
		"CREATE TABLE IF NOT EXISTS realtime.progress_updates",
		"CREATE TABLE IF NOT EXISTS realtime.leases",
		"topic_id text PRIMARY KEY CHECK (topic_id LIKE 'topic_%')",
		"topic_publication_id text PRIMARY KEY CHECK (topic_publication_id LIKE 'topic_publication_%')",
		"subscription_id text PRIMARY KEY CHECK (subscription_id LIKE 'subscription_%')",
		"presence_id text PRIMARY KEY CHECK (presence_id LIKE 'presence_%')",
		"notification_id text PRIMARY KEY CHECK (notification_id LIKE 'notification_%')",
		"notification_delivery_id text PRIMARY KEY CHECK (notification_delivery_id LIKE 'notification_delivery_%')",
		"progress_feed_id text PRIMARY KEY CHECK (progress_feed_id LIKE 'progress_feed_%')",
		"progress_update_id text PRIMARY KEY CHECK (progress_update_id LIKE 'progress_update_%')",
		"lease_id text PRIMARY KEY CHECK (lease_id LIKE 'lease_%')",
		"UNIQUE (topic_id, sequence)",
		"UNIQUE (source_kind, source_ref)",
		"CHECK (expires_at > starts_at)",
		"'retain_bounded'",
		"'durable_event_only'",
		"'polling'",
		"'actor_inbox'",
		"'main_outbox'",
		"'approval'",
		"'private_backup'",
		"'exclusive'",
		"realtime_presence_subject_node_idx",
		"realtime_leases_resource_status_idx",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("slice 15 migration missing %q", want)
		}
	}

	for _, forbidden := range []string{
		"CREATE TABLE IF NOT EXISTS identity.actors",
		"CREATE TABLE IF NOT EXISTS nodes.nodes",
		"CREATE TABLE IF NOT EXISTS routing.routes",
		"CREATE TABLE IF NOT EXISTS routing.capability_calls",
		"CREATE TABLE IF NOT EXISTS communication.messages",
		"CREATE TABLE IF NOT EXISTS realtime.sessions",
		"CREATE TABLE IF NOT EXISTS realtime.streams",
		"CREATE TABLE IF NOT EXISTS realtime.watchers",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("slice 15 migration should not include %q", forbidden)
		}
	}
}

func TestSlice16AgentToolVisibilityMigrationContract(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "00013_agent_tool_visibility.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)

	for _, want := range []string{
		"CREATE SCHEMA IF NOT EXISTS agents",
		"CREATE TABLE IF NOT EXISTS agents.agent_access_sessions",
		"CREATE TABLE IF NOT EXISTS agents.agent_work_contexts",
		"CREATE TABLE IF NOT EXISTS agents.tool_views",
		"CREATE TABLE IF NOT EXISTS agents.tool_view_entries",
		"CREATE TABLE IF NOT EXISTS agents.tool_calls",
		"CREATE TABLE IF NOT EXISTS agents.worklog_entries",
		"agent_access_session_id text PRIMARY KEY CHECK (agent_access_session_id LIKE 'agent_access_session_%')",
		"agent_work_context_id text PRIMARY KEY CHECK (agent_work_context_id LIKE 'agent_work_context_%')",
		"tool_view_id text PRIMARY KEY CHECK (tool_view_id LIKE 'tool_view_%')",
		"tool_view_entry_id text PRIMARY KEY CHECK (tool_view_entry_id LIKE 'tool_view_entry_%')",
		"agent_tool_call_id text PRIMARY KEY CHECK (agent_tool_call_id LIKE 'agent_tool_call_%')",
		"worklog_entry_id text PRIMARY KEY CHECK (worklog_entry_id LIKE 'worklog_entry_%')",
		"'operating_tool'",
		"'capability'",
		"'visible'",
		"'requestable'",
		"agents_work_contexts_active_tool_view_fkey",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("slice 16 migration missing %q", want)
		}
	}

	for _, forbidden := range []string{
		"CREATE TABLE IF NOT EXISTS identity.actors",
		"CREATE TABLE IF NOT EXISTS agents.agent_memory",
		"CREATE TABLE IF NOT EXISTS agents.model_runs",
		"CREATE TABLE IF NOT EXISTS agents.prompts",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("slice 16 migration should not include %q", forbidden)
		}
	}
}

func TestSlice17MinimalModuleSubstrateMigrationContract(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "00014_minimal_module_substrate.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)

	for _, want := range []string{
		"CREATE SCHEMA IF NOT EXISTS modules",
		"CREATE TABLE IF NOT EXISTS modules.packages",
		"CREATE TABLE IF NOT EXISTS modules.module_versions",
		"CREATE TABLE IF NOT EXISTS modules.runtime_requirements",
		"CREATE TABLE IF NOT EXISTS modules.object_type_declarations",
		"CREATE TABLE IF NOT EXISTS modules.provider_declarations",
		"CREATE TABLE IF NOT EXISTS modules.capability_declarations",
		"CREATE TABLE IF NOT EXISTS modules.usage_document_declarations",
		"CREATE TABLE IF NOT EXISTS modules.backup_hook_declarations",
		"module_package_id text PRIMARY KEY CHECK (module_package_id LIKE 'module_package_%')",
		"module_version_id text PRIMARY KEY CHECK (module_version_id LIKE 'module_version_%')",
		"module_requirement_id text PRIMARY KEY CHECK (module_requirement_id LIKE 'module_requirement_%')",
		"module_declaration_id text PRIMARY KEY CHECK (module_declaration_id LIKE 'module_declaration_%')",
		"package_kind IN ('native_module'",
		"module_kind IN ('native', 'internal', 'experimental', 'deprecated')",
		"UNIQUE (module_id, version)",
		"UNIQUE (module_version_id, provider_key, endpoint_name)",
		"'manifest'",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("slice 17 migration missing %q", want)
		}
	}

	for _, forbidden := range []string{
		"CREATE TABLE IF NOT EXISTS capabilities.providers",
		"CREATE TABLE IF NOT EXISTS capabilities.capability_endpoints",
		"CREATE TABLE IF NOT EXISTS modules.installations",
		"CREATE TABLE IF NOT EXISTS modules.namespaces",
		"CREATE TABLE IF NOT EXISTS modules.health_snapshots",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("slice 17 migration should not include %q", forbidden)
		}
	}
}

func TestSlice17ModuleLifecycleMigrationContract(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "00015_module_installation_lifecycle.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)

	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS modules.installations",
		"CREATE TABLE IF NOT EXISTS modules.namespaces",
		"CREATE TABLE IF NOT EXISTS modules.installation_providers",
		"CREATE TABLE IF NOT EXISTS modules.installation_capabilities",
		"CREATE TABLE IF NOT EXISTS modules.health_snapshots",
		"module_installation_id text PRIMARY KEY CHECK (module_installation_id LIKE 'module_installation_%')",
		"module_namespace_id text PRIMARY KEY CHECK (module_namespace_id LIKE 'module_namespace_%')",
		"module_health_id text PRIMARY KEY CHECK (module_health_id LIKE 'module_health_%')",
		"status IN ('installing', 'installed', 'enabled', 'disabled', 'failed', 'removed')",
		"namespace_kind IN (",
		"exposure_status IN ('declared', 'installed_disabled', 'exposed', 'disabled', 'revoked')",
		"UNIQUE (module_version_id, target_node_id, install_scope_id)",
		"UNIQUE (provider_id)",
		"UNIQUE (capability_endpoint_id)",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("slice 17 lifecycle migration missing %q", want)
		}
	}

	for _, forbidden := range []string{
		"CREATE TABLE IF NOT EXISTS modules.packages",
		"CREATE TABLE IF NOT EXISTS identity.actors",
		"CREATE TABLE IF NOT EXISTS nodes.nodes",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("slice 17 lifecycle migration should not include %q", forbidden)
		}
	}
}

func TestSlice17ModuleExposureAndBackupMigrationContract(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "00016_module_exposure_and_backup.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)

	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS modules.backup_exports",
		"module_backup_export_id text PRIMARY KEY CHECK (module_backup_export_id LIKE 'module_backup_export_%')",
		"module_installation_id text NOT NULL REFERENCES modules.installations(module_installation_id)",
		"module_version_id text NOT NULL REFERENCES modules.module_versions(module_version_id)",
		"backup_hook_declaration_id text NULL REFERENCES modules.backup_hook_declarations(module_declaration_id)",
		"export_kind text NOT NULL CHECK (export_kind IN ('manifest'))",
		"export_status text NOT NULL CHECK (export_status IN ('completed', 'failed'))",
		"payload_hash text NOT NULL CHECK",
		"UNIQUE (module_installation_id, export_kind, payload_hash)",
		"modules_backup_exports_installation_created_idx",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("slice 17 exposure/backup migration missing %q", want)
		}
	}

	for _, forbidden := range []string{
		"CREATE TABLE IF NOT EXISTS modules.installations",
		"CREATE TABLE IF NOT EXISTS capabilities.providers",
		"CREATE TABLE IF NOT EXISTS identity.actors",
		"CREATE TABLE IF NOT EXISTS nodes.nodes",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("slice 17 exposure/backup migration should not include %q", forbidden)
		}
	}
}

func TestSlice8PolicyMigrationContract(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "00007_basic_policy_authorization_approval_audit.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)

	for _, want := range []string{
		"CREATE SCHEMA IF NOT EXISTS policy",
		"CREATE SCHEMA IF NOT EXISTS security",
		"CREATE TABLE IF NOT EXISTS policy.decisions",
		"CREATE TABLE IF NOT EXISTS policy.approvals",
		"CREATE TABLE IF NOT EXISTS policy.grants",
		"policy_decision_id text PRIMARY KEY CHECK (policy_decision_id LIKE 'policy_decision_%')",
		"approval_id text PRIMARY KEY CHECK (approval_id LIKE 'approval_%')",
		"grant_id text PRIMARY KEY CHECK (grant_id LIKE 'grant_%')",
		"decision IN (",
		"'allow'",
		"'deny'",
		"'approval_required'",
		"status IN (",
		"'pending'",
		"'approved'",
		"'denied'",
		"'active'",
		"'consumed'",
		"'revoked'",
		"execution_authorization_level BETWEEN 1 AND 5",
		"actor_authorization_level BETWEEN 1 AND 5",
		"max_authorization_level BETWEEN 1 AND 5",
		"risk_level IN (",
		"max_risk_level IN (",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("slice 8 migration missing %q", want)
		}
	}

	for _, forbidden := range []string{
		"CREATE TABLE IF NOT EXISTS identity.actors",
		"CREATE TABLE IF NOT EXISTS nodes.nodes",
		"CREATE TABLE IF NOT EXISTS security.node_auth_credentials",
		"CREATE TABLE IF NOT EXISTS security.node_enrollment_requests",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("slice 8 migration should not include %q", forbidden)
		}
	}
}

func TestSlice11ProviderAdvertisementMigrationContract(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "00010_remote_provider_advertisement.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)

	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS capabilities.provider_advertisements",
		"provider_advertisement_id text PRIMARY KEY CHECK (provider_advertisement_id LIKE 'provider_advertisement_%')",
		"origin_node_id text NOT NULL REFERENCES nodes.nodes(node_id)",
		"provider_id text NULL REFERENCES capabilities.providers(provider_id)",
		"communication_message_id text NULL REFERENCES communication.messages(communication_message_id)",
		"advertisement_hash text NOT NULL CHECK",
		"'pending_review'",
		"'approved'",
		"'rejected'",
		"UNIQUE (origin_node_id, advertisement_hash)",
		"provider_advertisements_origin_status_idx",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("slice 11 migration missing %q", want)
		}
	}

	for _, forbidden := range []string{
		"CREATE TABLE IF NOT EXISTS identity.actors",
		"CREATE TABLE IF NOT EXISTS nodes.nodes",
		"CREATE TABLE IF NOT EXISTS communication.messages",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("slice 11 migration should not include %q", forbidden)
		}
	}
}

func TestSlice9RoutingMigrationContract(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "00008_local_capability_calls_through_router.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)

	for _, want := range []string{
		"CREATE SCHEMA IF NOT EXISTS routing",
		"CREATE TABLE IF NOT EXISTS routing.routes",
		"CREATE TABLE IF NOT EXISTS routing.capability_calls",
		"route_id text PRIMARY KEY CHECK (route_id LIKE 'route_%')",
		"capability_call_id text PRIMARY KEY CHECK (capability_call_id LIKE 'capability_call_%')",
		"actor_id text NOT NULL REFERENCES identity.actors(actor_id)",
		"origin_node_id text NOT NULL REFERENCES nodes.nodes(node_id)",
		"target_node_id text NOT NULL REFERENCES nodes.nodes(node_id)",
		"provider_id text NOT NULL REFERENCES capabilities.providers(provider_id)",
		"capability_endpoint_id text NOT NULL REFERENCES capabilities.capability_endpoints(capability_endpoint_id)",
		"capability_class_id text NULL REFERENCES capabilities.capability_classes(capability_class_id)",
		"policy_decision_id text NULL REFERENCES policy.decisions(policy_decision_id)",
		"approval_id text NULL REFERENCES policy.approvals(approval_id)",
		"grant_id text NULL REFERENCES policy.grants(grant_id)",
		"job_id text NULL REFERENCES jobs.jobs(job_id)",
		"route_kind IN (",
		"'local'",
		"'remote'",
		"execution_mode IN (",
		"'immediate'",
		"'job'",
		"'session'",
		"'stream'",
		"status IN (",
		"'planned'",
		"'authorized'",
		"'waiting_for_approval'",
		"'approval_required'",
		"'completed'",
		"'failed'",
		"events_events_route_id_fkey",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("slice 9 migration missing %q", want)
		}
	}

	for _, forbidden := range []string{
		"CREATE TABLE IF NOT EXISTS identity.actors",
		"CREATE TABLE IF NOT EXISTS nodes.nodes",
		"CREATE TABLE IF NOT EXISTS capabilities.providers",
		"CREATE TABLE IF NOT EXISTS policy.decisions",
		"CREATE TABLE IF NOT EXISTS jobs.jobs",
		"CREATE TABLE IF NOT EXISTS events.events",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("slice 9 migration should not include %q", forbidden)
		}
	}
}

func TestSlice10NodeAgentCommunicationMigrationContract(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "00009_node_agent_communication_spine.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)

	for _, want := range []string{
		"CREATE SCHEMA IF NOT EXISTS communication",
		"ALTER TABLE nodes.nodes",
		"presence_state text NOT NULL DEFAULT 'unknown'",
		"last_heartbeat_at timestamptz NULL",
		"enrollment_status text NOT NULL DEFAULT 'approved'",
		"credential_status text NOT NULL DEFAULT 'none'",
		"CREATE TABLE IF NOT EXISTS nodes.authority_profiles",
		"CREATE TABLE IF NOT EXISTS nodes.runtime_profiles",
		"CREATE TABLE IF NOT EXISTS nodes.node_profile_assignments",
		"CREATE TABLE IF NOT EXISTS nodes.heartbeats",
		"CREATE TABLE IF NOT EXISTS nodes.node_status_history",
		"CREATE TABLE IF NOT EXISTS security.node_enrollment_tokens",
		"CREATE TABLE IF NOT EXISTS security.node_enrollment_requests",
		"CREATE TABLE IF NOT EXISTS security.node_auth_credentials",
		"CREATE TABLE IF NOT EXISTS communication.messages",
		"CREATE TABLE IF NOT EXISTS communication.message_acks",
		"node_enrollment_token_id text PRIMARY KEY CHECK (node_enrollment_token_id LIKE 'node_enrollment_token_%')",
		"node_enrollment_request_id text PRIMARY KEY CHECK (node_enrollment_request_id LIKE 'node_enrollment_request_%')",
		"node_credential_id text PRIMARY KEY CHECK (node_credential_id LIKE 'node_credential_%')",
		"node_heartbeat_id text PRIMARY KEY CHECK (node_heartbeat_id LIKE 'node_heartbeat_%')",
		"communication_message_id text PRIMARY KEY CHECK (communication_message_id LIKE 'communication_message_%')",
		"communication_ack_id text PRIMARY KEY CHECK (communication_ack_id LIKE 'communication_ack_%')",
		"node_token_bootstrap",
		"main_to_node",
		"node_to_main",
		"'available'",
		"'acked'",
		"'dead_letter'",
		"CREATE UNIQUE INDEX IF NOT EXISTS communication_messages_node_direction_idem_idx",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("slice 10 migration missing %q", want)
		}
	}

	for _, forbidden := range []string{
		"CREATE TABLE IF NOT EXISTS identity.actors",
		"CREATE TABLE IF NOT EXISTS events.events",
		"CREATE TABLE IF NOT EXISTS routing.routes",
		"CREATE TABLE IF NOT EXISTS routing.capability_calls",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("slice 10 migration should not include %q", forbidden)
		}
	}
}

func TestV05NodeProfileSeedsMigrationContract(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "00036_v0_5_node_profile_seeds.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)

	for _, want := range []string{
		"INSERT INTO nodes.authority_profiles",
		"INSERT INTO nodes.runtime_profiles",
		"primary_workspace_default",
		"secondary_workspace_default",
		"hardware_capability_default",
		"compute_runner_default",
		"storage_edge_default",
		"automation_edge_default",
		"main_full",
		"workspace_full",
		"workspace_light",
		"hardware_agent",
		"compute_runner",
		"storage_edge",
		"guest_restricted",
		"ON CONFLICT (profile_key) DO NOTHING",
		"DELETE FROM nodes.node_profile_assignments",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("v0.5 node profile seeds migration missing %q", want)
		}
	}
}

func TestV099BackupContractAreaKeysMigrationContract(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 56 {
		t.Fatalf("version = %d, want at least 56", version)
	}

	path := filepath.Join("..", "..", "migrations", "00056_box_backup_contract_area_keys.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	for _, want := range []string{
		"DROP CONSTRAINT IF EXISTS watch_root_registrations_area_key_check",
		"area_key IN ('notes', 'documents', 'launchpad')",
		"area_key ~ '^backup_[a-z0-9][a-z0-9_-]{1,78}[a-z0-9]$'",
		"DELETE FROM box.watch_root_registrations",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("v0.9.9 backup contract area-key migration missing %q", want)
		}
	}
}

func TestProtectedFolderControlMigrationContract(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 57 {
		t.Fatalf("version = %d, want at least 57", version)
	}

	path := filepath.Join("..", "..", "migrations", "00057_backup_contract_control.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS backup.protected_folder_preflights",
		"communication_message_id text NULL REFERENCES communication.messages",
		"idempotency_key text NOT NULL",
		"retry_of_preflight_id text NULL REFERENCES backup.protected_folder_preflights",
		"protected_folder_preflights_idempotency_idx",
		"source_contract_key text NOT NULL",
		"source_contract_deleted_at timestamptz NULL",
		"desired_revision bigint NOT NULL",
		"applied_revision bigint NOT NULL",
		"desired_config_hash text NOT NULL",
		"reconciliation_message_id text NULL REFERENCES communication.messages",
		"WHERE desired_revision > applied_revision",
		"DROP TABLE IF EXISTS backup.protected_folder_preflights",
		"DROP COLUMN IF EXISTS source_contract_deleted_at",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("protected-folder migration missing %q", want)
		}
	}
}

func TestServiceManagerRuntimeKindMigrationContract(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 58 {
		t.Fatalf("version = %d, want at least 58", version)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", "00058_service_manager_runtime_kind.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for _, want := range []string{"endpoint_runtime_bindings_runtime_kind_check", "'service_manager'", "-- +goose Down"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("service-manager runtime migration missing %q", want)
		}
	}
}

func TestAbsoluteTimeMigrationContract(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 59 {
		t.Fatalf("version = %d, want at least 59", version)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", "00059_knowledge_absolute_time.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for _, want := range []string{
		"ALTER TABLE knowledge.knowledge_objects",
		"ALTER TABLE knowledge.knowledge_object_versions",
		"ALTER TABLE search.search_documents",
		"SET recency_at = last_seen_at",
		"SET recency_at = observed_at",
		"source_updated_at = object.source_modified_at",
		"source_updated_at = version.source_modified_at",
		"knowledge_objects_recency_idx",
		"knowledge_object_versions_recency_idx",
		"search_search_documents_recency_idx",
		"-- +goose Down",
		"SET source_updated_at = object.updated_at",
		"document.source_id = chunk.knowledge_chunk_id",
		"DROP COLUMN IF EXISTS source_created_at",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("absolute-time migration missing %q", want)
		}
	}
	if strings.Contains(sql, "updated_at,\n    recency_basis") || strings.Contains(sql, "SET recency_at = updated_at") {
		t.Fatal("absolute-time migration must not relabel knowledge row update time as source chronology")
	}
}

func TestUnifiedKnowledgePipelineMigrationsContract(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 62 {
		t.Fatalf("version = %d, want at least 62", version)
	}
	checks := map[string][]string{
		"00060_knowledge_file_pipelines.sql": {
			"CREATE TABLE IF NOT EXISTS knowledge.pipeline_runs",
			"pipeline_runs_active_revision_definition_idx",
			"CREATE TABLE IF NOT EXISTS knowledge.pipeline_stage_runs",
			"CREATE TABLE IF NOT EXISTS knowledge.pipeline_stage_units",
			"CREATE TABLE IF NOT EXISTS knowledge.derived_artifacts",
			"derived_artifacts_active_identity_idx",
			"source_created_at timestamptz NULL",
			"recency_basis text NOT NULL",
			"DROP TABLE IF EXISTS knowledge.pipeline_runs",
		},
		"00061_worker_resource_admission.sql": {
			"CREATE TABLE IF NOT EXISTS workers.resource_capacities",
			"'knowledge_heavy'",
			"CREATE TABLE IF NOT EXISTS workers.resource_leases",
			"resource_leases_active_slot_idx",
			"slot_number integer NOT NULL",
			"DROP TABLE IF EXISTS workers.resource_leases",
		},
	}
	for name, wants := range checks {
		raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		sql := string(raw)
		for _, want := range wants {
			if !strings.Contains(sql, want) {
				t.Fatalf("%s missing %q", name, want)
			}
		}
	}
}

func TestWorkspaceArchiveLifecycleMigrationContract(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("LatestVersion returned error: %v", err)
	}
	if version < 63 {
		t.Fatalf("version = %d, want at least 63", version)
	}

	path := filepath.Join("..", "..", "migrations", "00063_workspace_archive_lifecycle.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS storage.workspace_archive_operations",
		"schema_version = 'storage.workspace_archive_operation.v1'",
		"evidence_kind = 'physical_workspace_move'",
		"operation_kind IN ('archive', 'restore')",
		"workspace_kind IN ('topic', 'project', 'library_item')",
		"'archive_planned'",
		"'archive_intent_committed'",
		"'archive_payload_moved'",
		"'archive_projections_committed'",
		"'archive_complete'",
		"'restore_planned'",
		"'restore_intent_committed'",
		"'restore_payload_moved'",
		"'restore_projections_committed'",
		"'restore_complete'",
		"'blocked'",
		"'manual_repair_required'",
		"source_relative_path = CASE",
		"destination_relative_path = CASE",
		"'Topics/' || slug",
		"'Projects/' || slug",
		"'Library/' || slug",
		"'archive/topics/' || slug || '/content'",
		"'archive/projects/' || slug || '/project'",
		"'archive/library/' || slug || '/content'",
		"source_identity_json jsonb NOT NULL",
		"source_identity_json->>'presence' IS NOT DISTINCT FROM 'present'",
		"destination_identity_json->>'presence' IS NOT DISTINCT FROM 'absent'",
		"destination_parent_identity_json jsonb NOT NULL",
		"plan_digest text NOT NULL CHECK",
		"plan_json->>'operation_kind' IS NOT DISTINCT FROM operation_kind",
		"plan_json->>'kind' IS NOT DISTINCT FROM workspace_kind",
		"plan_json->>'object_id' IS NOT DISTINCT FROM object_id",
		"plan_json->>'slug' IS NOT DISTINCT FROM slug",
		"plan_json->>'plan_digest' IS NOT DISTINCT FROM plan_digest",
		"plan_json#>>'{source,path,root}' IS NOT DISTINCT FROM source_root",
		"plan_json#>>'{source,path,relative_path}' IS NOT DISTINCT FROM source_relative_path",
		"plan_json#>'{source,identity}' IS NOT DISTINCT FROM source_identity_json",
		"plan_json#>'{source,parent_identity}' IS NOT DISTINCT FROM source_parent_identity_json",
		"plan_json#>>'{destination,path,root}' IS NOT DISTINCT FROM destination_root",
		"plan_json#>>'{destination,path,relative_path}' IS NOT DISTINCT FROM destination_relative_path",
		"plan_json#>'{destination,identity}' IS NOT DISTINCT FROM destination_identity_json",
		"plan_json#>'{destination,parent_identity}' IS NOT DISTINCT FROM destination_parent_identity_json",
		"plan_json#>>'{inventory,digest}' IS NOT DISTINCT FROM inventory_digest",
		"plan_json->>'actor_id' IS NOT DISTINCT FROM actor_id",
		"plan_json->>'reason' IS NOT DISTINCT FROM reason",
		"(plan_json->>'planned_at')::timestamptz IS NOT DISTINCT FROM planned_at",
		"inventory_digest text NOT NULL CHECK",
		"intent_committed_at IS NULL AND payload_moved_at IS NULL",
		"intent_committed_at >= planned_at",
		"payload_moved_at >= intent_committed_at",
		"projections_committed_at >= payload_moved_at",
		"completed_at >= projections_committed_at",
		"CREATE TABLE IF NOT EXISTS storage.workspace_archive_manifests",
		"schema_version = 'storage.workspace_archive_manifest.v1'",
		"restore_plan_digest text NULL CHECK",
		"archive_source_identity_json jsonb NOT NULL CHECK",
		"manifest_json->>'archive_operation_id' IS NOT DISTINCT FROM workspace_archive_operation_id",
		"manifest_json->>'kind' IS NOT DISTINCT FROM workspace_kind",
		"manifest_json->>'lifecycle_state' IS NOT DISTINCT FROM lifecycle_state",
		"manifest_json->>'object_id' IS NOT DISTINCT FROM object_id",
		"manifest_json->>'slug' IS NOT DISTINCT FROM slug",
		"manifest_json->>'plan_digest' IS NOT DISTINCT FROM plan_digest",
		"manifest_json->>'restore_plan_digest' IS NOT DISTINCT FROM restore_plan_digest",
		"manifest_json->>'inventory_digest' IS NOT DISTINCT FROM inventory_digest",
		"manifest_json->'archive_source_identity' IS NOT DISTINCT FROM archive_source_identity_json",
		"manifest_json#>>'{authentication,key_id}' IS NOT DISTINCT FROM authentication_key_id",
		"manifest_json#>>'{authentication,tag}' IS NOT DISTINCT FROM authentication_tag",
		"(manifest_json->>'archived_at')::timestamptz IS NOT DISTINCT FROM archived_at",
		"manifest_json->>'restore_operation_id' IS NOT DISTINCT FROM restore_operation_id",
		"ELSE (manifest_json->>'restored_at')::timestamptz",
		"restored_at IS NULL OR restored_at >= archived_at",
		"archive_operation_kind text NOT NULL DEFAULT 'archive'",
		"restore_operation_kind text NULL CHECK",
		"authentication_tag text NOT NULL CHECK",
		"CREATE TABLE IF NOT EXISTS storage.workspace_archive_findings",
		"'destination_collision'",
		"'symlink_escape'",
		"'cross_device'",
		"'source_drift'",
		"'destination_substitution'",
		"'replay_conflict'",
		"'historical_copy_evidence'",
		"CREATE TABLE IF NOT EXISTS storage.workspace_lifecycle_events",
		"event_kind = 'workspace.lifecycle_changed'",
		"operation_kind text NOT NULL CHECK (operation_kind IN ('archive', 'restore'))",
		"transition IN ('active_to_archived', 'archived_to_active')",
		"source_relative_path = CASE",
		"destination_relative_path = CASE",
		"DROP TABLE IF EXISTS storage.workspace_lifecycle_events",
		"DROP TABLE IF EXISTS storage.workspace_archive_operations",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("workspace archive lifecycle migration missing %q", want)
		}
	}
	if count := strings.Count(sql, "FOREIGN KEY ("); count != 3 {
		t.Fatalf("workspace archive lifecycle migration composite relation foreign keys = %d, want 3", count)
	}

	for _, forbidden := range []string{
		"ALTER TABLE storage.archive_manifests",
		"ALTER TABLE storage.archive_items",
		"UPDATE storage.archive_manifests",
		"INSERT INTO storage.archive_manifests",
		"COPY ",
		"pg_read_file",
		"CREATE TRIGGER",
		"CREATE OR REPLACE FUNCTION",
		"/srv/loom/",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("physical lifecycle migration must not reuse copy evidence or touch live paths; found %q", forbidden)
		}
	}
}

func TestBoxKnowledgeSourcesMigrationContract(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil || version < 64 {
		t.Fatalf("migration head %d: %v", version, err)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", "00064_box_knowledge_sources.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"'box_topics'", "'box_library'", "'project_material'", "notes_source_roots_expanded_owner_check", "^backup_[a-z0-9][a-z0-9_-]{1,78}[a-z0-9]$"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("missing %s", want)
		}
	}
	for _, forbidden := range []string{"DELETE FROM", "UPDATE knowledge.", "CREATE TABLE", "DROP TABLE"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("migration rewrites source history: %s", forbidden)
		}
	}
}

func TestProjectArchivePlanEvidenceMigrationContract(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil || version != 71 {
		t.Fatalf("migration head %d: %v", version, err)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", "00065_project_archive_plan_evidence.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"CREATE TABLE projects.physical_archive_plan_evidence",
		"REFERENCES projects.projects(project_id)", "REFERENCES identity.actors(actor_id)",
		"REFERENCES nodes.nodes(node_id)", "REFERENCES scopes.scopes(scope_id)",
		"payload bytea NOT NULL", "BETWEEN 2 AND 134217728", "encode(sha256(payload), 'hex')",
		"request_json->>'actor_id' IS NOT DISTINCT FROM actor_id",
		"'{workspace,operation_id}' IS NOT DISTINCT FROM operation_id",
		"'{workspace,operation_kind}' IS NOT DISTINCT FROM operation_kind",
		"'{workspace,object_id}' IS NOT DISTINCT FROM project_id",
		"BEFORE UPDATE OR DELETE", "REVOKE ALL ON projects.physical_archive_plan_evidence FROM PUBLIC",
	} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("private plan migration missing %q", want)
		}
	}
	for _, forbidden := range []string{"ALTER TABLE", "UPDATE projects.projects", "INSERT INTO storage.", "DELETE FROM", "pg_read_file", "/srv/loom/"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("plan persistence expanded authority: %q", forbidden)
		}
	}
}

func TestWorkspaceArchiveProjectIdentityMigrationContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", "00066_workspace_archive_project_identity.sql"))
	if err != nil {
		t.Fatal(err)
	}
	up := strings.Split(string(raw), "-- +goose Down")[0]
	for _, table := range []string{"workspace_archive_operations", "workspace_archive_manifests", "workspace_lifecycle_events"} {
		if !strings.Contains(up, "ALTER TABLE storage."+table) || !strings.Contains(up, "ADD CONSTRAINT "+table+"_object_id_check") {
			t.Fatalf("missing exact object identity constraint for %s", table)
		}
	}
	if strings.Count(up, "object_id ~ '^[a-z][a-z0-9_]{2,127}$'") != 3 || strings.Count(up, "workspace_kind = 'project' AND object_id ~ '^project_[0-7][0-9A-HJKMNP-TV-Z]{25}$'") != 3 {
		t.Fatal("legacy or typed project identity alternative drifted")
	}
	for _, forbidden := range []string{"UPDATE ", "DELETE FROM", "INSERT INTO", "DROP TABLE", "CASCADE", "NOT VALID"} {
		if strings.Contains(up, forbidden) {
			t.Fatalf("identity migration expanded scope: %s", forbidden)
		}
	}
}

func TestNotesArchiveLifecycleMigrationContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", "00067_notes_archive_lifecycle.sql"))
	if err != nil {
		t.Fatal(err)
	}
	up := strings.Split(string(raw), "-- +goose Down")[0]
	for _, want := range []string{
		"CREATE TABLE knowledge.notes_custody_transitions", "CREATE TABLE knowledge.notes_current_custody",
		"CREATE VIEW knowledge.notes_object_custody", "COALESCE(e.to_state, 'active') AS source_lifecycle",
		"PRIMARY KEY (knowledge_object_id, workspace_lifecycle_event_id)",
		"FOREIGN KEY (knowledge_object_id, previous_event_id)",
		"REFERENCES storage.workspace_lifecycle_events(workspace_lifecycle_event_id) ON DELETE RESTRICT",
		"BEFORE UPDATE OR DELETE", "Notes custody history must be preserved; downgrade refused",
	} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("missing Notes custody contract %q", want)
		}
	}
	for _, forbidden := range []string{"ALTER TABLE", "UPDATE knowledge.", "INSERT INTO", "DELETE FROM", "processing_state", "SECURITY DEFINER", "CASCADE"} {
		if strings.Contains(up, forbidden) {
			t.Fatalf("custody migration changes processing/source authority: %s", forbidden)
		}
	}
}

func TestNotesCustodyReceiptMigrationContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", "00068_notes_custody_projection_receipts.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"CREATE TABLE knowledge.notes_custody_projection_receipts", "PRIMARY KEY (node_id, workspace_lifecycle_event_id)",
		"FOREIGN KEY (node_id, previous_event_id)", "REFERENCES storage.workspace_lifecycle_events(workspace_lifecycle_event_id)",
		"object_count BETWEEN 0 AND 250000", "BEFORE UPDATE OR DELETE", "downgrade refused"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("missing receipt contract %q", want)
		}
	}
	up := strings.Split(string(raw), "-- +goose Down")[0]
	for _, forbidden := range []string{"ALTER TABLE", "INSERT INTO", "UPDATE knowledge.", "DELETE FROM", "SECURITY DEFINER", "CASCADE"} {
		if strings.Contains(up, forbidden) {
			t.Fatalf("receipt migration expands authority: %s", forbidden)
		}
	}
}

func TestCalendarSchedulesMigrationContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", "00069_calendar_schedules.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"DROP CONSTRAINT schedules_schedule_kind_check", "'one_shot', 'interval', 'cron'", "RAISE EXCEPTION 'calendar schedule rollback refused", "WHERE schedule_kind = 'cron'"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("migration missing %q", want)
		}
	}
	for _, forbidden := range []string{"DELETE FROM", "DROP TABLE", "CREATE TABLE", "UPDATE automation."} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("migration changes retained data: %s", forbidden)
		}
	}
}

func TestDeclarationOperationJournalMigrationContract(t *testing.T) {
	version, err := LatestVersion(filepath.Join("..", "..", "migrations"))
	if err != nil || version != 71 {
		t.Fatalf("declaration operation migration head %d: %v", version, err)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", "00070_project_declaration_operations.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"CREATE TABLE projects.declaration_operations", "CREATE TABLE projects.declaration_action_receipts", "REFERENCES identity.actors(actor_id)", "REFERENCES nodes.nodes(node_id)", "REFERENCES projects.declaration_operations(operation_id)", "UNIQUE (actor_id, origin_node_id, project_id, operation_kind, idempotency_key)", "PRIMARY KEY (operation_id, action_id)", "BEFORE UPDATE OR DELETE", "dedup evidence must be preserved"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("missing journal invariant %s", want)
		}
	}
	for _, forbidden := range []string{"INSERT INTO jobs.jobs", "UPDATE jobs.jobs", "DELETE FROM", "ALTER TABLE projects.projects"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("unexpected owner mutation %s", forbidden)
		}
	}
}
