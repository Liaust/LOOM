package supportbundle

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigCollectorIncludesCanonicalAndMigrationFieldsSafely(t *testing.T) {
	serviceRoot := filepath.Clean("/srv/custom-loom")
	dataRoot := filepath.Clean("/var/lib/custom-loom")
	legacyExport := filepath.Clean("/legacy/generated-export")
	legacyDocuments := filepath.Clean("/legacy/main-documents")
	output := filepath.Join(t.TempDir(), "support.tar.gz")
	fields := map[string]string{
		"service_root":                          serviceRoot,
		"box_root":                              filepath.Join(serviceRoot, "box"),
		"agents_root":                           filepath.Join(serviceRoot, "agents"),
		"storage_root":                          filepath.Join(serviceRoot, "storage"),
		"imports_root":                          filepath.Join(serviceRoot, "storage", "imports"),
		"user_backups_root":                     filepath.Join(serviceRoot, "storage", "backups"),
		"archive_root":                          filepath.Join(serviceRoot, "storage", "archive"),
		"box_state_root":                        filepath.Join(dataRoot, "box-state"),
		"generated_root":                        filepath.Join(dataRoot, "generated"),
		"notes_projection_root":                 filepath.Join(dataRoot, "generated", "notes"),
		"storage_retention_root":                filepath.Join(dataRoot, "storage-retention"),
		"operational_backups_root":              filepath.Join(dataRoot, "backups", "main"),
		"deprecated_storage_export_root":        legacyExport,
		"deprecated_main_documents_root":        legacyDocuments,
		"storage_export_migration_input_only":   "true",
		"main_documents_migration_input_only":   "true",
		"legacy_box_state_compatibility_active": "false",
		"db_configured":                         "true",
		"main_url":                              "https://operator:super-secret@example.test/private-path-secret?request=unsafe",
		"embedding_ollama_url":                  "http://model-user:model-secret@example.test/model-path-secret",
	}
	_, err := Create(context.Background(), Options{
		OutputPath:       output,
		Profile:          ProfileMinimal,
		Now:              fixedTestTime(),
		SafeConfigFields: fields,
		PathAliases: []PathAlias{
			{Label: "$LOOM_SERVICE_ROOT", Root: serviceRoot},
			{Label: "$LOOM_DATA", Root: dataRoot},
			{Label: "$LOOM_DEPRECATED_STORAGE_EXPORT_ROOT", Root: legacyExport},
			{Label: "$LOOM_DEPRECATED_MAIN_DOCUMENTS_ROOT", Root: legacyDocuments},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	summary := readArchiveText(t, output, "loom-support/summaries/config.json")
	for _, want := range []string{
		`"service_root": "$LOOM_SERVICE_ROOT"`,
		`"box_root": "$LOOM_SERVICE_ROOT/box"`,
		`"storage_root": "$LOOM_SERVICE_ROOT/storage"`,
		`"box_state_root": "$LOOM_DATA/box-state"`,
		`"generated_root": "$LOOM_DATA/generated"`,
		`"deprecated_storage_export_root": "$LOOM_DEPRECATED_STORAGE_EXPORT_ROOT"`,
		`"deprecated_main_documents_root": "$LOOM_DEPRECATED_MAIN_DOCUMENTS_ROOT"`,
		`"storage_export_migration_input_only": "true"`,
		`"main_documents_migration_input_only": "true"`,
		`"legacy_box_state_compatibility_active": "false"`,
		`"db_configured": "true"`,
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("config summary missing %s:\n%s", want, summary)
		}
	}
	for _, forbidden := range []string{serviceRoot, dataRoot, legacyExport, legacyDocuments, "postgres://", "password=", "super-secret", "model-secret", "private-path-secret", "model-path-secret", "request=unsafe"} {
		if strings.Contains(summary, forbidden) {
			t.Fatalf("config summary leaked %q:\n%s", forbidden, summary)
		}
	}
}
