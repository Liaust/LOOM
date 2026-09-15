package runtimes

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/mainstorage"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/workers"
)

func TestMainDocumentsImportRuntimeDefaultInstance(t *testing.T) {
	runtime := NewMainDocumentsImportRuntime(mainstorage.Service{}, "/srv/loom/box/Documents")
	if runtime.Kind() != workers.KindMainDocumentsImport {
		t.Fatalf("Kind() = %q", runtime.Kind())
	}
	instances := runtime.DefaultInstances()
	if len(instances) != 1 || instances[0].WorkerKey != "main.main_documents_import" {
		t.Fatalf("unexpected default instances: %#v", instances)
	}
	config, err := runtime.parseConfig(instances[0].ConfigJSON)
	if err != nil {
		t.Fatalf("parse default config: %v", err)
	}
	if config.BackingRoot != "/srv/loom/box/Documents" {
		t.Fatalf("unexpected config: %#v", config)
	}
	if config.SchemaVersion != mainDocumentsImportConfigV07 || !strings.Contains(string(instances[0].ConfigJSON), mainDocumentsImportConfigV07) {
		t.Fatalf("default config schema is not v0.7: parsed=%#v raw=%s", config, instances[0].ConfigJSON)
	}
	if strings.Contains(string(instances[0].ConfigJSON), "rebuild_export_on_accept") {
		t.Fatalf("default config still advertises export rebuild: %s", instances[0].ConfigJSON)
	}
}

func TestMainDocumentsImportRuntimeAcceptsLegacyV06Config(t *testing.T) {
	runtime := NewMainDocumentsImportRuntime(mainstorage.Service{}, "/srv/loom/box/Documents")
	parsed, err := runtime.parseConfig(json.RawMessage(`{"schema_version":"main_documents_import.config.v0.6","backing_root":"/srv/loom/box/Documents"}`))
	if err != nil {
		t.Fatalf("parse legacy v0.6 config: %v", err)
	}
	if parsed.SchemaVersion != mainDocumentsImportConfigV07 {
		t.Fatalf("legacy config was not normalized to current schema: %#v", parsed)
	}
}

func TestMainDocumentsImportRuntimeRejectsUnknownConfigSchema(t *testing.T) {
	runtime := NewMainDocumentsImportRuntime(mainstorage.Service{}, "/srv/loom/box/Documents")
	if _, err := runtime.parseConfig(json.RawMessage(`{"schema_version":"main_documents_import.config.v99","backing_root":"/srv/loom/box/Documents"}`)); !errors.Is(err, workers.ErrInvalid) {
		t.Fatalf("unknown schema error = %v, want workers.ErrInvalid", err)
	}
}

func TestMainDocumentsImportRuntimeRunsScannerAndWritesCheckpoint(t *testing.T) {
	root := t.TempDir()
	pathValue := filepath.Join(root, "runtime.md")
	if err := os.WriteFile(pathValue, []byte("runtime"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	now := time.Date(2026, 6, 5, 20, 0, 0, 0, time.UTC)
	if err := os.Chtimes(pathValue, now.Add(-time.Minute), now.Add(-time.Minute)); err != nil {
		t.Fatalf("set mtime: %v", err)
	}
	catalog := &mainDocumentsRuntimeCatalogFake{}
	mainStorage := mainstorage.NewService(catalog, mainstorage.Config{
		BackingRoot:  root,
		StableWindow: time.Second,
		Now:          func() time.Time { return now },
	})
	runtime := NewMainDocumentsImportRuntime(mainStorage, root)
	runtime.Now = func() time.Time { return now }
	config := runtime.DefaultConfig()

	result, err := runtime.RunOnce(context.Background(), workers.RunContext{
		Instance: workers.WorkerInstance{ConfigJSON: config},
		Run:      workers.WorkerRun{WorkerRunID: "worker_run_test"},
	})
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if result.Status != workers.RunStatusSucceeded || result.Counters["files_accepted"] != 1 || len(result.CheckpointUpdates) != 1 {
		t.Fatalf("unexpected run result: %#v", result)
	}
	if len(catalog.inputs) != 1 || catalog.inputs[0].RelativePath != "runtime.md" {
		t.Fatalf("unexpected catalog inputs: %#v", catalog.inputs)
	}
}

func TestMainDocumentsImportRuntimeAcceptsWithoutStorageExport(t *testing.T) {
	root := t.TempDir()
	pathValue := filepath.Join(root, "runtime.md")
	if err := os.WriteFile(pathValue, []byte("runtime"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	now := time.Date(2026, 6, 16, 20, 0, 0, 0, time.UTC)
	if err := os.Chtimes(pathValue, now.Add(-time.Minute), now.Add(-time.Minute)); err != nil {
		t.Fatalf("set mtime: %v", err)
	}
	catalog := &mainDocumentsRuntimeCatalogFake{}
	mainStorage := mainstorage.NewService(catalog, mainstorage.Config{
		BackingRoot:  root,
		StableWindow: time.Second,
		Now:          func() time.Time { return now },
	})
	runtime := NewMainDocumentsImportRuntime(mainStorage, root)
	runtime.Now = func() time.Time { return now }

	result, err := runtime.RunOnce(context.Background(), workers.RunContext{
		Instance: workers.WorkerInstance{ConfigJSON: runtime.DefaultConfig()},
		Run:      workers.WorkerRun{WorkerRunID: "worker_run_refresh"},
	})
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	var summary mainDocumentsImportSummary
	if err := json.Unmarshal(result.ResultSummary, &summary); err != nil {
		t.Fatalf("unmarshal summary: %v", err)
	}
	if summary.Accepted != 1 || summary.Status != "ok" {
		t.Fatalf("unexpected import summary: %#v", summary)
	}
	if strings.Contains(string(result.ResultSummary), "export_") {
		t.Fatalf("result summary still advertises export work: %s", result.ResultSummary)
	}
	for key := range result.Counters {
		if strings.Contains(key, "export") {
			t.Fatalf("result counter still advertises export work: %q", key)
		}
	}
}

func TestMainDocumentsImportRuntimeDoesNotUseActiveTickForAlreadyCatalogedFiles(t *testing.T) {
	root := t.TempDir()
	pathValue := filepath.Join(root, "runtime.md")
	if err := os.WriteFile(pathValue, []byte("runtime"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	now := time.Date(2026, 6, 14, 11, 0, 0, 0, time.UTC)
	old := now.Add(-time.Minute)
	if err := os.Chtimes(pathValue, old, old); err != nil {
		t.Fatalf("set mtime: %v", err)
	}
	catalog := &mainDocumentsRuntimeCatalogFake{
		active: []storagecatalog.Entry{mainDocumentsRuntimeEntryFixtureForFile(t, root, "runtime.md")},
	}
	mainStorage := mainstorage.NewService(catalog, mainstorage.Config{
		BackingRoot:  root,
		StableWindow: time.Second,
		Now:          func() time.Time { return now },
	})
	runtime := NewMainDocumentsImportRuntime(mainStorage, root)
	runtime.Now = func() time.Time { return now }
	config := mustMainDocumentsJSON(map[string]any{
		"schema_version":               "main_documents_import.config.v0.6",
		"backing_root":                 root,
		"stable_window_seconds":        1,
		"max_files_per_run":            1,
		"idle_tick_interval_seconds":   600,
		"active_tick_interval_seconds": 5,
	})

	result, err := runtime.RunOnce(context.Background(), workers.RunContext{
		Instance: workers.WorkerInstance{ConfigJSON: config},
		Run:      workers.WorkerRun{WorkerRunID: "worker_run_cataloged"},
	})
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	var summary mainDocumentsImportSummary
	if err := json.Unmarshal(result.ResultSummary, &summary); err != nil {
		t.Fatalf("unmarshal summary: %v", err)
	}
	if summary.MoreWork || summary.AlreadyCataloged != 1 || summary.Remaining != 0 || result.Counters["files_remaining"] != 0 {
		t.Fatalf("unexpected summary/counters: summary=%#v counters=%#v", summary, result.Counters)
	}
	if result.NextRunAfter == nil || time.Until(*result.NextRunAfter) < time.Minute {
		t.Fatalf("NextRunAfter used active interval, got %v", result.NextRunAfter)
	}
	if len(catalog.inputs) != 0 {
		t.Fatalf("unchanged file should not be registered again: %#v", catalog.inputs)
	}
}

func TestMainDocumentsImportRuntimeDefersMissingCatalogRows(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 6, 13, 19, 0, 0, 0, time.UTC)
	missing := storagecatalog.Entry{
		StorageEntryID:    ids.NewStorageEntryID(),
		StorageClass:      storagecatalog.StorageClassMainDocument,
		SourceArea:        storagecatalog.SourceAreaMainDocuments,
		OriginNodeKey:     "main",
		LogicalPath:       "Removed/report.md",
		CurrentViewPath:   "main/Documents/Removed/report.md",
		FileClass:         storagecatalog.FileClassMarkdown,
		ProcessingState:   storagecatalog.ProcessingStateMetadataOnly,
		AvailabilityState: storagecatalog.AvailabilityStateAvailable,
		RetentionState:    storagecatalog.RetentionStateSnapshot,
	}
	catalog := &mainDocumentsRuntimeCatalogFake{active: []storagecatalog.Entry{missing}}
	mainStorage := mainstorage.NewService(catalog, mainstorage.Config{
		BackingRoot:  root,
		StableWindow: time.Second,
		Now:          func() time.Time { return now },
	})
	runtime := NewMainDocumentsImportRuntime(mainStorage, root)
	runtime.Now = func() time.Time { return now }

	result, err := runtime.RunOnce(context.Background(), workers.RunContext{
		Instance: workers.WorkerInstance{ConfigJSON: runtime.DefaultConfig()},
		Run:      workers.WorkerRun{WorkerRunID: "worker_run_reconcile"},
	})
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if result.Status != workers.RunStatusSucceeded || result.Counters["files_tombstoned"] != 0 || result.Counters["missing_cataloged"] != 1 {
		t.Fatalf("unexpected run result: %#v", result)
	}
	if len(catalog.tombstones) != 0 {
		t.Fatalf("unexpected tombstones: %#v", catalog.tombstones)
	}
}

type mainDocumentsRuntimeCatalogFake struct {
	inputs     []storagecatalog.MainDocumentInput
	active     []storagecatalog.Entry
	tombstones []storagecatalog.MainDocumentTombstoneInput
}

func (f *mainDocumentsRuntimeCatalogFake) RegisterMainDocument(_ context.Context, input storagecatalog.MainDocumentInput) (storagecatalog.EntryDetail, error) {
	f.inputs = append(f.inputs, input)
	size := input.FileSizeBytes
	entry := storagecatalog.Entry{
		StorageEntryID:    ids.NewStorageEntryID(),
		StorageClass:      storagecatalog.StorageClassMainDocument,
		SourceArea:        storagecatalog.SourceAreaMainDocuments,
		OriginNodeKey:     input.NodeKey,
		LogicalPath:       input.RelativePath,
		CurrentViewPath:   "main/Documents/" + input.RelativePath,
		SizeBytes:         &size,
		ChecksumAlgorithm: input.ChecksumAlgorithm,
		ChecksumHex:       input.ChecksumValue,
		FileClass:         storagecatalog.ClassifyPath(input.RelativePath, ""),
		ProcessingState:   storagecatalog.ProcessingStateMetadataOnly,
		AvailabilityState: storagecatalog.AvailabilityStateAvailable,
		RetentionState:    storagecatalog.RetentionStatePending,
	}
	if strings.TrimSpace(entry.ChecksumHex) == "" {
		entry.ChecksumHex = strings.Repeat("d", 64)
	}
	return storagecatalog.EntryDetail{Entry: entry}, nil
}

func (f *mainDocumentsRuntimeCatalogFake) ListActiveMainDocuments(_ context.Context, _ int) ([]storagecatalog.Entry, error) {
	return append([]storagecatalog.Entry(nil), f.active...), nil
}

func (f *mainDocumentsRuntimeCatalogFake) ListEntries(_ context.Context, _ storagecatalog.ListFilter) ([]storagecatalog.Entry, error) {
	return nil, nil
}

func (f *mainDocumentsRuntimeCatalogFake) InspectEntry(_ context.Context, ref string) (storagecatalog.EntryDetail, error) {
	for _, entry := range f.active {
		if ref == entry.StorageEntryID || ref == entry.LogicalPath || ref == entry.CurrentViewPath {
			return storagecatalog.EntryDetail{Entry: entry}, nil
		}
	}
	return storagecatalog.EntryDetail{}, os.ErrNotExist
}

func (f *mainDocumentsRuntimeCatalogFake) TombstoneMainDocument(_ context.Context, input storagecatalog.MainDocumentTombstoneInput) (storagecatalog.Tombstone, error) {
	f.tombstones = append(f.tombstones, input)
	return storagecatalog.Tombstone{
		StorageTombstoneID: ids.NewStorageTombstoneID(),
		StorageEntryID:     input.StorageEntryID,
		TombstoneKind:      input.TombstoneKind,
		Reason:             input.Reason,
		CreatedBy:          input.CreatedBy,
		CreatedAt:          input.TombstonedAt,
	}, nil
}

func mainDocumentsRuntimeEntryFixtureForFile(t *testing.T, root, relativePath string) storagecatalog.Entry {
	t.Helper()
	pathValue := filepath.Join(root, filepath.FromSlash(relativePath))
	info, err := os.Stat(pathValue)
	if err != nil {
		t.Fatalf("stat %s: %v", relativePath, err)
	}
	size := info.Size()
	metadata, err := json.Marshal(map[string]any{
		"schema_version": "storage.main_document.metadata.v0.6",
		"source":         "mainstorage.importer",
		"relative_path":  relativePath,
		"modified_at":    info.ModTime().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	return storagecatalog.Entry{
		StorageEntryID:    ids.NewStorageEntryID(),
		StorageClass:      storagecatalog.StorageClassMainDocument,
		SourceArea:        storagecatalog.SourceAreaMainDocuments,
		OriginNodeKey:     "main",
		LogicalPath:       relativePath,
		CurrentViewPath:   "main/Documents/" + relativePath,
		SizeBytes:         &size,
		ChecksumAlgorithm: "sha256",
		ChecksumHex:       strings.Repeat("d", 64),
		FileClass:         storagecatalog.ClassifyPath(relativePath, ""),
		ProcessingState:   storagecatalog.ProcessingStateMetadataOnly,
		AvailabilityState: storagecatalog.AvailabilityStateAvailable,
		RetentionState:    storagecatalog.RetentionStateSnapshot,
		Metadata:          metadata,
	}
}
