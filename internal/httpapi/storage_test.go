package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"loom.local/loom/internal/config"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/lane"
	"loom.local/loom/internal/mainstorage"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/storagedoctor"
	"loom.local/loom/internal/storageretention"
	"loom.local/loom/internal/storageview"
)

func TestLaneAcceptInputUsesTrustedRuntimeImportsRoot(t *testing.T) {
	input := lane.AcceptInput{ImportsRoot: "/caller/selected/custody"}
	got := trustedLaneAcceptInput(input, "/srv/custom/storage/imports", "/var/custom/loom")
	if got.ImportsRoot != "/srv/custom/storage/imports" {
		t.Fatalf("imports root = %q", got.ImportsRoot)
	}
	if got.RemoteRoot != "/var/custom/loom/lane" || !got.TrustedRemoteRoot {
		t.Fatalf("runtime staging root = %q trusted=%t", got.RemoteRoot, got.TrustedRemoteRoot)
	}
}

func TestLaneAcceptErrorsExposeResumableFailurePhase(t *testing.T) {
	for phase, want := range map[string]string{
		"promotion":                   "storage.lane_promotion_failed",
		"catalog":                     "storage.lane_catalog_failed",
		"source_cleanup":              "storage.lane_source_cleanup_failed",
		"obsolete_accepted_migration": "storage.lane_obsolete_accepted_migration_failed",
	} {
		got := laneAcceptErrorCode(&lane.AcceptPhaseError{Phase: phase, Err: errors.New("failed")})
		if got != want {
			t.Fatalf("phase %s code = %q, want %q", phase, got, want)
		}
	}
}

func TestStorageTreeRouteIsRetired(t *testing.T) {
	server := NewServer(Services{}, slog.Default()).Handler()
	req := httptest.NewRequest(http.MethodGet, "/v1/storage/tree?source_area=documents&node=macbook&limit=2", nil)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)

	if res.Code != http.StatusGone {
		t.Fatalf("status = %d body=%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "storage.tree_retired") {
		t.Fatalf("retirement response missing typed error: %s", res.Body.String())
	}
}

func TestStorageFilesystemStatusFailsClosedWithoutCatalog(t *testing.T) {
	server := NewServer(Services{RuntimeConfig: config.Config{}}, slog.Default()).Handler()
	req := httptest.NewRequest(http.MethodGet, "/v1/storage/filesystem/status", nil)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", res.Code, res.Body.String())
	}
	var envelope response.Envelope[storagedoctor.FilesystemStatus]
	if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !envelope.OK || envelope.Data.Status != storagedoctor.StatusError || envelope.Data.Catalog.Failed == 0 {
		t.Fatalf("missing catalog did not fail closed: %#v", envelope.Data)
	}
}

func TestStorageEntryRouteInspectsEntry(t *testing.T) {
	db, store := newHTTPStorageFakeDB(t)
	defer db.Close()
	entry := httpStorageEntry(time.Date(2026, 6, 5, 12, 30, 0, 0, time.UTC))
	ref := httpStoragePhysicalRef(entry.StorageEntryID, entry.CreatedAt)

	store.expect("FROM storage.storage_entries WHERE storage_entry_id = $1", func(args []driver.NamedValue) {
		requireHTTPStorageArgs(t, args, entry.StorageEntryID)
	}, httpStorageEntryRow(entry))
	store.expect("FROM storage.storage_physical_refs WHERE storage_entry_id = $1", func(args []driver.NamedValue) {
		requireHTTPStorageArgs(t, args, entry.StorageEntryID)
	}, httpStoragePhysicalRefRow(ref))

	server := NewServer(Services{StorageCatalog: storagecatalog.NewService(db)}, slog.Default()).Handler()
	req := httptest.NewRequest(http.MethodGet, "/v1/storage/entries/"+entry.StorageEntryID, nil)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", res.Code, res.Body.String())
	}
	var envelope response.Envelope[storagecatalog.EntryDetail]
	if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Data.Entry.StorageEntryID != entry.StorageEntryID || len(envelope.Data.PhysicalRefs) != 1 {
		t.Fatalf("unexpected detail: %#v", envelope.Data)
	}
	store.requireDone()
}

func TestStorageResolveRouteResolvesViewPath(t *testing.T) {
	db, store := newHTTPStorageFakeDB(t)
	defer db.Close()
	entry := httpStorageEntry(time.Date(2026, 6, 5, 13, 0, 0, 0, time.UTC))

	store.expect("current_view_path = $1", func(args []driver.NamedValue) {
		requireHTTPStorageArgs(t, args, "macbook/Backups/Documents/current/report.md")
	}, httpStorageEntryRow(entry))
	store.expect("FROM storage.storage_physical_refs WHERE storage_entry_id = $1", func(args []driver.NamedValue) {
		requireHTTPStorageArgs(t, args, entry.StorageEntryID)
	})

	server := NewServer(Services{StorageCatalog: storagecatalog.NewService(db)}, slog.Default()).Handler()
	req := httptest.NewRequest(http.MethodGet, "/v1/storage/resolve?path=macbook%2FBackups%2FDocuments%2Fcurrent%2Freport.md", nil)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", res.Code, res.Body.String())
	}
	var envelope response.Envelope[storageview.ResolveResult]
	if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Data.ViewEntry.StorageEntryID != entry.StorageEntryID || envelope.Data.EntryDetail == nil {
		t.Fatalf("unexpected resolve result: %#v", envelope.Data)
	}
	store.requireDone()
}

func TestStorageExportStatusRoute(t *testing.T) {
	root := filepath.Join(t.TempDir(), "export")
	server := NewServer(Services{
		RuntimeConfig: config.Config{StorageExport: root},
	}, slog.Default()).Handler()
	req := httptest.NewRequest(http.MethodGet, "/v1/storage/export/status", nil)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", res.Code, res.Body.String())
	}
	var envelope response.Envelope[storagedoctor.ExportCompatibilityInfo]
	if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !envelope.OK || envelope.Data.LegacyPath != root || envelope.Data.Active || !envelope.Data.Deprecated {
		t.Fatalf("unexpected export status: %#v", envelope.Data)
	}
}

func TestMainDocumentsStatusRoute(t *testing.T) {
	root := t.TempDir()
	server := NewServer(Services{
		MainStorage: mainstorage.NewService(nil, mainstorage.Config{BackingRoot: root}),
	}, slog.Default()).Handler()
	req := httptest.NewRequest(http.MethodGet, "/v1/storage/main-documents/status", nil)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", res.Code, res.Body.String())
	}
	var envelope response.Envelope[mainstorage.Status]
	if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !envelope.OK || envelope.Data.BackingRoot != root || !envelope.Data.Exists {
		t.Fatalf("unexpected main Documents status: %#v", envelope.Data)
	}
}

func TestMainDocumentsReconcileRoute(t *testing.T) {
	root := t.TempDir()
	catalog := &httpMainDocumentsCatalog{
		active: []storagecatalog.Entry{
			httpMainDocumentEntry("Missing.md"),
		},
	}
	server := NewServer(Services{
		MainStorage: mainstorage.NewService(catalog, mainstorage.Config{BackingRoot: root}),
	}, slog.Default()).Handler()
	req := httptest.NewRequest(http.MethodPost, "/v1/storage/main-documents/reconcile", strings.NewReader(`{"dry_run":true}`))
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", res.Code, res.Body.String())
	}
	var envelope response.Envelope[mainstorage.ReconcileResult]
	if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !envelope.OK || !envelope.Data.DryRun || envelope.Data.MissingCataloged != 1 || len(catalog.tombstones) != 0 {
		t.Fatalf("unexpected reconcile response: %#v tombstones=%#v", envelope.Data, catalog.tombstones)
	}
}

func TestMainDocumentsReconcileRouteDoesNotRequestExportRefresh(t *testing.T) {
	root := t.TempDir()
	catalog := &httpMainDocumentsCatalog{
		active: []storagecatalog.Entry{
			httpMainDocumentEntry("Missing.md"),
		},
	}
	server := NewServer(Services{
		MainStorage: mainstorage.NewService(catalog, mainstorage.Config{BackingRoot: root}),
	}, slog.Default()).Handler()
	req := httptest.NewRequest(http.MethodPost, "/v1/storage/main-documents/reconcile", strings.NewReader(`{"yes":true,"reason":"unit test","created_by":"test"}`))
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", res.Code, res.Body.String())
	}
	var envelope response.Envelope[mainstorage.ReconcileResult]
	if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !envelope.OK ||
		envelope.Data.Tombstoned != 1 ||
		len(catalog.tombstones) != 1 {
		t.Fatalf("unexpected reconcile response: %#v tombstones=%#v", envelope.Data, catalog.tombstones)
	}
	for _, staleField := range []string{"needs_export_rebuild", "export_rebuilt", "export_refresh_requested", "export_refresh_state"} {
		if strings.Contains(res.Body.String(), staleField) {
			t.Fatalf("reconcile response still advertises removed export behavior %q: %s", staleField, res.Body.String())
		}
	}
}

func TestMainDocumentsReconcileRouteForcesConfiguredLegacyGuard(t *testing.T) {
	root := t.TempDir()
	legacyRoot := filepath.Join(root, "legacy")
	canonicalRoot := filepath.Join(root, "box", "Documents")
	if err := os.MkdirAll(legacyRoot, 0o755); err != nil {
		t.Fatalf("mkdir legacy root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyRoot, "legacy.md"), []byte("legacy"), 0o644); err != nil {
		t.Fatalf("write legacy fixture: %v", err)
	}
	catalog := &httpMainDocumentsCatalog{
		active: []storagecatalog.Entry{
			httpMainDocumentEntry("Missing.md"),
		},
	}
	server := NewServer(Services{
		MainStorage: mainstorage.NewService(catalog, mainstorage.Config{BackingRoot: canonicalRoot, LegacyRoot: legacyRoot}),
	}, slog.Default()).Handler()
	req := httptest.NewRequest(http.MethodPost, "/v1/storage/main-documents/reconcile", strings.NewReader(`{"yes":true}`))
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", res.Code, res.Body.String())
	}
	var envelope response.Envelope[mainstorage.ReconcileResult]
	if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !envelope.OK ||
		envelope.Data.Migration.Status != mainstorage.DocumentsMigrationReady ||
		!envelope.Data.CatalogMutationBlocked ||
		envelope.Data.Tombstoned != 0 ||
		len(catalog.tombstones) != 0 {
		t.Fatalf("unexpected reconcile response: %#v tombstones=%#v", envelope.Data, catalog.tombstones)
	}
}

func TestMainDocumentsRetentionBackfillRoute(t *testing.T) {
	root := t.TempDir()
	pathValue := filepath.Join(root, "Report.md")
	if err := os.WriteFile(pathValue, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	size := int64(len("payload"))
	entry := httpMainDocumentEntry("Report.md")
	entry.SizeBytes = &size
	entry.ChecksumAlgorithm = "sha256"
	entry.ChecksumHex = strings.Repeat("a", 64)
	catalog := &httpMainDocumentsCatalog{
		active: []storagecatalog.Entry{entry},
		refs: map[string][]storagecatalog.PhysicalRef{
			entry.StorageEntryID: {{
				StoragePhysicalRefID: ids.NewStoragePhysicalRefID(),
				StorageEntryID:       entry.StorageEntryID,
				RefKind:              storagecatalog.PhysicalRefKindLocalPath,
				URI:                  pathValue,
				NodeKey:              "main",
				Status:               storagecatalog.PhysicalRefStatusAvailable,
			}},
		},
	}
	server := NewServer(Services{
		MainStorage: mainstorage.NewService(catalog, mainstorage.Config{BackingRoot: root}),
	}, slog.Default()).Handler()
	req := httptest.NewRequest(http.MethodPost, "/v1/storage/main-documents/retention/backfill", strings.NewReader(`{"dry_run":true}`))
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", res.Code, res.Body.String())
	}
	var envelope response.Envelope[mainstorage.RetentionBackfillResult]
	if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !envelope.OK || !envelope.Data.DryRun || envelope.Data.WouldCreate != 1 || envelope.Data.Scanned != 1 {
		t.Fatalf("unexpected retention backfill response: %#v", envelope.Data)
	}
}

func TestStorageExportRebuildRoute(t *testing.T) {
	root := filepath.Join(t.TempDir(), "export")
	server := NewServer(Services{RuntimeConfig: config.Config{StorageExport: root}}, slog.Default()).Handler()
	req := httptest.NewRequest(http.MethodPost, "/v1/storage/export/rebuild", strings.NewReader(`{"dry_run":true}`))
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)

	if res.Code != http.StatusGone {
		t.Fatalf("status = %d body=%s", res.Code, res.Body.String())
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("retired rebuild must not create export root, stat err=%v", err)
	}
}

func TestStorageRetentionRoutes(t *testing.T) {
	entry := httpStorageEntry(time.Date(2026, 6, 5, 15, 0, 0, 0, time.UTC))
	ref := httpStoragePhysicalRef(entry.StorageEntryID, entry.CreatedAt)
	ref.ContentAddress = entry.ChecksumAlgorithm + ":" + entry.ChecksumHex
	source := filepath.Join(t.TempDir(), "retained.md")
	if err := os.WriteFile(source, []byte("retained"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	ref.URI = source
	catalog := &httpStorageRetentionCatalog{
		details: map[string]storagecatalog.EntryDetail{
			entry.StorageEntryID: {Entry: entry, PhysicalRefs: []storagecatalog.PhysicalRef{ref}},
		},
	}
	server := NewServer(Services{
		StorageRetention: storageretention.NewService(catalog),
	}, slog.Default()).Handler()

	req := httptest.NewRequest(http.MethodGet, "/v1/storage/retention/status", nil)
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status route code = %d body=%s", res.Code, res.Body.String())
	}
	var statusEnvelope response.Envelope[storagecatalog.RetentionStatus]
	if err := json.NewDecoder(res.Body).Decode(&statusEnvelope); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if statusEnvelope.Data.Entries != 1 {
		t.Fatalf("unexpected status: %#v", statusEnvelope.Data)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/storage/safe-to-delete", strings.NewReader(`{"ref":"`+entry.StorageEntryID+`"}`))
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("safe route code = %d body=%s", res.Code, res.Body.String())
	}
	var safeEnvelope response.Envelope[storageretention.SafeToDeleteResult]
	if err := json.NewDecoder(res.Body).Decode(&safeEnvelope); err != nil {
		t.Fatalf("decode safe result: %v", err)
	}
	if safeEnvelope.Data.Decision != storageretention.DecisionSafe {
		t.Fatalf("unexpected safe result: %#v", safeEnvelope.Data)
	}
}

func TestStorageArchiveRoute(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.txt")
	content := []byte("archive route")
	if err := os.WriteFile(source, content, 0o640); err != nil {
		t.Fatalf("write source: %v", err)
	}
	entry := httpStorageEntry(time.Date(2026, 6, 5, 15, 30, 0, 0, time.UTC))
	entry.LogicalPath = "Route/source.txt"
	entry.CurrentViewPath = "macbook/Backups/Documents/current/Route/source.txt"
	entry.ChecksumAlgorithm = "sha256"
	checksum := sha256.Sum256(content)
	entry.ChecksumHex = fmt.Sprintf("%x", checksum)
	size := int64(len(content))
	entry.SizeBytes = &size
	ref := httpStoragePhysicalRef(entry.StorageEntryID, entry.CreatedAt)
	ref.URI = source
	ref.ContentAddress = entry.ChecksumAlgorithm + ":" + entry.ChecksumHex
	catalog := &httpStorageArchiveCatalog{
		entries: map[string]storagecatalog.Entry{entry.StorageEntryID: entry},
		details: map[string]storagecatalog.EntryDetail{
			entry.StorageEntryID: {Entry: entry, PhysicalRefs: []storagecatalog.PhysicalRef{ref}},
		},
		manifests: map[string]storagecatalog.ArchiveManifest{},
	}
	server := NewServer(Services{
		StorageArchive: storagearchive.NewService(catalog, filepath.Join(root, "archive")),
	}, slog.Default()).Handler()
	req := httptest.NewRequest(http.MethodPost, "/v1/storage/archive", strings.NewReader(`{"source_ref":"macbook/Backups/Documents/current/Route","target_path":"main/Archive/Documents/Route"}`))
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", res.Code, res.Body.String())
	}
	var envelope response.Envelope[storagearchive.ArchiveResult]
	if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !envelope.OK || len(envelope.Data.Entries) != 1 {
		t.Fatalf("unexpected archive response: %#v", envelope.Data)
	}
	if envelope.Data.Entries[0].ArchiveViewPath != "main/Archive/Documents/Route/source.txt" {
		t.Fatalf("archive path = %q", envelope.Data.Entries[0].ArchiveViewPath)
	}
	if len(catalog.manifests) != 1 || len(catalog.archiveItems) != 1 {
		t.Fatalf("archive catalog writes missing: manifests=%d items=%d", len(catalog.manifests), len(catalog.archiveItems))
	}
}

type httpStorageRetentionCatalog struct {
	details map[string]storagecatalog.EntryDetail
}

func (c *httpStorageRetentionCatalog) InspectEntry(_ context.Context, ref string) (storagecatalog.EntryDetail, error) {
	detail, ok := c.details[ref]
	if !ok {
		return storagecatalog.EntryDetail{}, fmt.Errorf("not found")
	}
	return detail, nil
}

func (c *httpStorageRetentionCatalog) InspectMainDocumentByPath(_ context.Context, relativePath string, _ storagecatalog.InspectOptions) (storagecatalog.EntryDetail, error) {
	viewPath := storagecatalog.MainDocumentViewPath(strings.Trim(strings.TrimSpace(relativePath), "/"))
	for _, detail := range c.details {
		entry := detail.Entry
		if entry.CurrentViewPath == viewPath || entry.LogicalPath == relativePath {
			return detail, nil
		}
	}
	return storagecatalog.EntryDetail{}, fmt.Errorf("not found")
}

func (c *httpStorageRetentionCatalog) ListEntries(context.Context, storagecatalog.ListFilter) ([]storagecatalog.Entry, error) {
	entries := make([]storagecatalog.Entry, 0, len(c.details))
	for _, detail := range c.details {
		entries = append(entries, detail.Entry)
	}
	return entries, nil
}

func (c *httpStorageRetentionCatalog) RetentionStatus(ctx context.Context) (storagecatalog.RetentionStatus, error) {
	entries, err := c.ListEntries(ctx, storagecatalog.ListFilter{})
	if err != nil {
		return storagecatalog.RetentionStatus{}, err
	}
	return storagecatalog.RetentionStatus{Entries: len(entries), GeneratedAt: time.Now().UTC()}, nil
}

func (c *httpStorageRetentionCatalog) CreateTombstone(context.Context, storagecatalog.TombstoneInput) (storagecatalog.Tombstone, error) {
	return storagecatalog.Tombstone{}, fmt.Errorf("not implemented")
}

type httpStorageArchiveCatalog struct {
	entries        map[string]storagecatalog.Entry
	details        map[string]storagecatalog.EntryDetail
	manifests      map[string]storagecatalog.ArchiveManifest
	archiveItems   []storagecatalog.ArchiveItemInput
	markedArchived []string
}

func (c *httpStorageArchiveCatalog) ListEntries(context.Context, storagecatalog.ListFilter) ([]storagecatalog.Entry, error) {
	entries := make([]storagecatalog.Entry, 0, len(c.entries))
	for _, entry := range c.entries {
		entries = append(entries, entry)
	}
	return entries, nil
}

func (c *httpStorageArchiveCatalog) InspectEntry(_ context.Context, ref string) (storagecatalog.EntryDetail, error) {
	if detail, ok := c.details[ref]; ok {
		return detail, nil
	}
	for _, detail := range c.details {
		if detail.Entry.CurrentViewPath == ref || detail.Entry.LogicalPath == ref {
			return detail, nil
		}
	}
	return storagecatalog.EntryDetail{}, fmt.Errorf("not found")
}

func (c *httpStorageArchiveCatalog) CommitArchive(ctx context.Context, input storagecatalog.ArchiveCommitInput) (storagecatalog.ArchiveCommitResult, error) {
	staged := &httpStorageArchiveCatalog{
		entries:        make(map[string]storagecatalog.Entry, len(c.entries)+len(input.Entries)),
		details:        make(map[string]storagecatalog.EntryDetail, len(c.details)+len(input.Entries)),
		manifests:      make(map[string]storagecatalog.ArchiveManifest, len(c.manifests)+1),
		archiveItems:   append([]storagecatalog.ArchiveItemInput(nil), c.archiveItems...),
		markedArchived: append([]string(nil), c.markedArchived...),
	}
	for key, entry := range c.entries {
		staged.entries[key] = entry
	}
	for key, detail := range c.details {
		detail.PhysicalRefs = append([]storagecatalog.PhysicalRef(nil), detail.PhysicalRefs...)
		staged.details[key] = detail
	}
	for key, manifest := range c.manifests {
		staged.manifests[key] = manifest
	}
	manifest, err := staged.CreateArchiveManifest(ctx, input.Manifest)
	if err != nil {
		return storagecatalog.ArchiveCommitResult{}, err
	}
	result := storagecatalog.ArchiveCommitResult{Manifest: manifest}
	for _, tuple := range input.Entries {
		entry, err := staged.RegisterEntry(ctx, tuple.Entry)
		if err != nil {
			return storagecatalog.ArchiveCommitResult{}, err
		}
		ref, err := staged.RegisterPhysicalRef(ctx, tuple.Ref)
		if err != nil {
			return storagecatalog.ArchiveCommitResult{}, err
		}
		if err := staged.AddArchiveItem(ctx, tuple.Item); err != nil {
			return storagecatalog.ArchiveCommitResult{}, err
		}
		result.Entries = append(result.Entries, entry)
		result.Refs = append(result.Refs, ref)
	}
	for _, disposition := range input.SourceDispositions {
		entry := staged.entries[disposition.StorageEntryID]
		entry.AvailabilityState = disposition.AvailabilityState
		staged.entries[entry.StorageEntryID] = entry
		detail := staged.details[entry.StorageEntryID]
		detail.Entry = entry
		staged.details[entry.StorageEntryID] = detail
		if disposition.AvailabilityState == storagecatalog.AvailabilityStateArchived {
			staged.markedArchived = append(staged.markedArchived, disposition.StorageEntryID)
		}
	}
	c.entries, c.details, c.manifests = staged.entries, staged.details, staged.manifests
	c.archiveItems, c.markedArchived = staged.archiveItems, staged.markedArchived
	return result, nil
}

func (c *httpStorageArchiveCatalog) RegisterEntry(_ context.Context, input storagecatalog.RegisterEntryInput) (storagecatalog.Entry, error) {
	if input.StorageEntryID == "" {
		input.StorageEntryID = ids.NewStorageEntryID()
	}
	now := time.Date(2026, 6, 5, 15, 31, 0, 0, time.UTC)
	entry := storagecatalog.Entry{
		StorageEntryID:     input.StorageEntryID,
		StorageClass:       input.StorageClass,
		SourceArea:         input.SourceArea,
		OriginNodeKey:      input.OriginNodeKey,
		LogicalPath:        input.LogicalPath,
		OriginalSourcePath: input.OriginalSourcePath,
		CurrentViewPath:    input.CurrentViewPath,
		ChecksumAlgorithm:  input.ChecksumAlgorithm,
		ChecksumHex:        input.ChecksumHex,
		SizeBytes:          input.SizeBytes,
		MimeType:           input.MimeType,
		FileClass:          input.FileClass,
		ProcessingState:    input.ProcessingState,
		AvailabilityState:  input.AvailabilityState,
		RetentionState:     input.RetentionState,
		Metadata:           input.Metadata,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if input.ArchiveManifestID != "" {
		entry.ArchiveManifestID = &input.ArchiveManifestID
	}
	c.entries[entry.StorageEntryID] = entry
	c.details[entry.StorageEntryID] = storagecatalog.EntryDetail{Entry: entry}
	return entry, nil
}

func (c *httpStorageArchiveCatalog) RegisterPhysicalRef(_ context.Context, input storagecatalog.RegisterPhysicalRefInput) (storagecatalog.PhysicalRef, error) {
	if input.StoragePhysicalRefID == "" {
		input.StoragePhysicalRefID = ids.NewStoragePhysicalRefID()
	}
	now := time.Date(2026, 6, 5, 15, 31, 0, 0, time.UTC)
	ref := storagecatalog.PhysicalRef{
		StoragePhysicalRefID: input.StoragePhysicalRefID,
		StorageEntryID:       input.StorageEntryID,
		RefKind:              input.RefKind,
		URI:                  input.URI,
		NodeKey:              input.NodeKey,
		ContentAddress:       input.ContentAddress,
		Status:               input.Status,
		Metadata:             input.Metadata,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	detail := c.details[input.StorageEntryID]
	detail.PhysicalRefs = append(detail.PhysicalRefs, ref)
	c.details[input.StorageEntryID] = detail
	return ref, nil
}

func (c *httpStorageArchiveCatalog) CreateArchiveManifest(_ context.Context, input storagecatalog.CreateArchiveManifestInput) (storagecatalog.ArchiveManifest, error) {
	manifest := storagecatalog.ArchiveManifest{
		ArchiveManifestID: input.ArchiveManifestID,
		ArchiveKey:        input.ArchiveKey,
		ArchiveKind:       input.ArchiveKind,
		OwnerNodeKey:      input.OwnerNodeKey,
		SourceRef:         input.SourceRef,
		Status:            input.Status,
		ManifestJSON:      input.ManifestJSON,
		CreatedAt:         time.Date(2026, 6, 5, 15, 31, 0, 0, time.UTC),
		FinalizedAt:       input.FinalizedAt,
	}
	c.manifests[manifest.ArchiveManifestID] = manifest
	return manifest, nil
}

func (c *httpStorageArchiveCatalog) AddArchiveItem(_ context.Context, input storagecatalog.ArchiveItemInput) error {
	c.archiveItems = append(c.archiveItems, input)
	return nil
}

func (c *httpStorageArchiveCatalog) MarkEntriesArchived(_ context.Context, storageEntryIDs []string, _ string) error {
	c.markedArchived = append(c.markedArchived, storageEntryIDs...)
	return nil
}

func (c *httpStorageArchiveCatalog) MarkEntriesSuperseded(context.Context, []string, string) error {
	return nil
}

type httpStorageFakeStore struct {
	t       *testing.T
	mu      sync.Mutex
	queries []httpStorageFakeQuery
}

type httpStorageFakeQuery struct {
	snippet string
	check   func([]driver.NamedValue)
	rows    [][]driver.Value
}

func newHTTPStorageFakeDB(t *testing.T) (*sql.DB, *httpStorageFakeStore) {
	t.Helper()
	store := &httpStorageFakeStore{t: t}
	return sql.OpenDB(httpStorageFakeConnector{store: store}), store
}

func (s *httpStorageFakeStore) expect(snippet string, check func([]driver.NamedValue), rows ...[]driver.Value) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries = append(s.queries, httpStorageFakeQuery{snippet: compactHTTPStorageSQL(snippet), check: check, rows: rows})
}

func (s *httpStorageFakeStore) query(query string, args []driver.NamedValue) (driver.Rows, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queries) == 0 {
		s.t.Fatalf("unexpected query: %s", query)
	}
	expected := s.queries[0]
	s.queries = s.queries[1:]
	if !strings.Contains(compactHTTPStorageSQL(query), expected.snippet) {
		s.t.Fatalf("query = %q, want to contain %q", compactHTTPStorageSQL(query), expected.snippet)
	}
	if expected.check != nil {
		expected.check(args)
	}
	return &httpStorageFakeRows{columns: httpStorageFakeColumns(expected.rows), rows: expected.rows}, nil
}

func (s *httpStorageFakeStore) requireDone() {
	s.t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queries) != 0 {
		s.t.Fatalf("unconsumed queries: %d", len(s.queries))
	}
}

type httpStorageFakeConnector struct {
	store *httpStorageFakeStore
}

func (c httpStorageFakeConnector) Connect(context.Context) (driver.Conn, error) {
	return httpStorageFakeConn{store: c.store}, nil
}

func (c httpStorageFakeConnector) Driver() driver.Driver {
	return httpStorageFakeDriver{}
}

type httpStorageFakeDriver struct{}

func (httpStorageFakeDriver) Open(string) (driver.Conn, error) {
	return nil, fmt.Errorf("use sql.OpenDB with httpStorageFakeConnector")
}

type httpStorageFakeConn struct {
	store *httpStorageFakeStore
}

func (c httpStorageFakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("prepared statements are not supported by httpStorageFakeConn")
}

func (c httpStorageFakeConn) Close() error {
	return nil
}

func (c httpStorageFakeConn) Begin() (driver.Tx, error) {
	return nil, fmt.Errorf("transactions are not supported by httpStorageFakeConn")
}

func (c httpStorageFakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.store.query(query, args)
}

type httpStorageFakeRows struct {
	columns []string
	rows    [][]driver.Value
	index   int
}

func (r *httpStorageFakeRows) Columns() []string {
	return r.columns
}

func (r *httpStorageFakeRows) Close() error {
	return nil
}

func (r *httpStorageFakeRows) Next(dest []driver.Value) error {
	if r.index >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.index])
	r.index++
	return nil
}

func requireHTTPStorageArgs(t *testing.T, args []driver.NamedValue, want ...any) {
	t.Helper()
	if len(args) != len(want) {
		t.Fatalf("arg count = %d, want %d", len(args), len(want))
	}
	for i := range want {
		if fmt.Sprint(args[i].Value) != fmt.Sprint(want[i]) {
			t.Fatalf("arg %d = %v, want %v", i+1, args[i].Value, want[i])
		}
	}
}

func compactHTTPStorageSQL(query string) string {
	return strings.Join(strings.Fields(query), " ")
}

func httpStorageFakeColumns(rows [][]driver.Value) []string {
	count := 0
	if len(rows) > 0 {
		count = len(rows[0])
	}
	columns := make([]string, count)
	for i := range columns {
		columns[i] = fmt.Sprintf("col_%d", i)
	}
	return columns
}

func httpStorageEntry(now time.Time) storagecatalog.Entry {
	size := int64(42)
	return storagecatalog.Entry{
		StorageEntryID:    ids.NewStorageEntryID(),
		StorageClass:      storagecatalog.StorageClassPrivateBackup,
		SourceArea:        storagecatalog.SourceAreaDocuments,
		OriginNodeKey:     "macbook",
		LogicalPath:       "report.md",
		SizeBytes:         &size,
		FileClass:         storagecatalog.FileClassMarkdown,
		ProcessingState:   storagecatalog.ProcessingStateBackupOnly,
		AvailabilityState: storagecatalog.AvailabilityStateAvailable,
		RetentionState:    storagecatalog.RetentionStateNone,
		Metadata:          json.RawMessage(`{"source":"http-test"}`),
		CreatedAt:         now,
		UpdatedAt:         now,
	}
}

func httpStoragePhysicalRef(storageEntryID string, now time.Time) storagecatalog.PhysicalRef {
	return storagecatalog.PhysicalRef{
		StoragePhysicalRefID: ids.NewStoragePhysicalRefID(),
		StorageEntryID:       storageEntryID,
		RefKind:              storagecatalog.PhysicalRefKindLocalPath,
		URI:                  "/var/lib/loom/storage/report.md",
		Status:               storagecatalog.PhysicalRefStatusAvailable,
		Metadata:             json.RawMessage(`{"source":"http-test"}`),
		CreatedAt:            now,
		UpdatedAt:            now,
	}
}

func httpStorageEntryRow(entry storagecatalog.Entry) []driver.Value {
	return []driver.Value{
		entry.StorageEntryID,
		entry.StorageClass,
		entry.SourceArea,
		stringPtrHTTPStorageValue(entry.OriginNodeID),
		entry.OriginNodeKey,
		stringPtrHTTPStorageValue(entry.ProjectID),
		stringPtrHTTPStorageValue(entry.WatchedRootID),
		entry.WatchedRootKey,
		entry.DropzoneTransferID,
		stringPtrHTTPStorageValue(entry.PrivateBackupOperationID),
		stringPtrHTTPStorageValue(entry.PrivateBackupItemID),
		stringPtrHTTPStorageValue(entry.ObjectID),
		stringPtrHTTPStorageValue(entry.ObjectVersionID),
		stringPtrHTTPStorageValue(entry.ArchiveManifestID),
		entry.LogicalPath,
		entry.OriginalSourcePath,
		entry.CurrentViewPath,
		entry.ChecksumAlgorithm,
		entry.ChecksumHex,
		int64PtrHTTPStorageValue(entry.SizeBytes),
		entry.MimeType,
		entry.FileClass,
		entry.ProcessingState,
		entry.AvailabilityState,
		entry.RetentionState,
		[]byte(entry.Metadata),
		entry.CreatedAt,
		entry.UpdatedAt,
		timePtrHTTPStorageValue(entry.DeletedAt),
	}
}

func httpStoragePhysicalRefRow(ref storagecatalog.PhysicalRef) []driver.Value {
	return []driver.Value{
		ref.StoragePhysicalRefID,
		ref.StorageEntryID,
		stringPtrHTTPStorageValue(ref.StorageEntryVersionID),
		ref.RefKind,
		ref.URI,
		stringPtrHTTPStorageValue(ref.NodeID),
		ref.NodeKey,
		ref.ContentAddress,
		ref.Status,
		[]byte(ref.Metadata),
		ref.CreatedAt,
		ref.UpdatedAt,
	}
}

func stringPtrHTTPStorageValue(value *string) driver.Value {
	if value == nil {
		return nil
	}
	return *value
}

func int64PtrHTTPStorageValue(value *int64) driver.Value {
	if value == nil {
		return nil
	}
	return *value
}

func timePtrHTTPStorageValue(value *time.Time) driver.Value {
	if value == nil {
		return nil
	}
	return *value
}

func makeHTTPStorageWritableForCleanup(t *testing.T, root string) {
	t.Helper()
	t.Cleanup(func() {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(path, 0o755)
			}
			return nil
		})
	})
}

type httpMainDocumentsCatalog struct {
	active     []storagecatalog.Entry
	refs       map[string][]storagecatalog.PhysicalRef
	tombstones []storagecatalog.MainDocumentTombstoneInput
}

func (c *httpMainDocumentsCatalog) RegisterMainDocument(_ context.Context, input storagecatalog.MainDocumentInput) (storagecatalog.EntryDetail, error) {
	entry := httpMainDocumentEntry(input.RelativePath)
	entry.StorageEntryID = ids.NewStorageEntryID()
	return storagecatalog.EntryDetail{Entry: entry}, nil
}

func (c *httpMainDocumentsCatalog) ListActiveMainDocuments(_ context.Context, _ int) ([]storagecatalog.Entry, error) {
	active := make([]storagecatalog.Entry, 0, len(c.active))
	for _, entry := range c.active {
		if entry.AvailabilityState == storagecatalog.AvailabilityStateTombstoned {
			continue
		}
		active = append(active, entry)
	}
	return active, nil
}

func (c *httpMainDocumentsCatalog) ListEntries(_ context.Context, filter storagecatalog.ListFilter) ([]storagecatalog.Entry, error) {
	entries := make([]storagecatalog.Entry, 0, len(c.active))
	for _, entry := range c.active {
		if !filter.IncludeDeleted && entry.AvailabilityState == storagecatalog.AvailabilityStateTombstoned {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (c *httpMainDocumentsCatalog) InspectEntry(_ context.Context, ref string) (storagecatalog.EntryDetail, error) {
	for _, entry := range c.active {
		if ref == entry.StorageEntryID || ref == entry.LogicalPath || ref == entry.CurrentViewPath {
			return storagecatalog.EntryDetail{
				Entry:        entry,
				PhysicalRefs: append([]storagecatalog.PhysicalRef(nil), c.refs[entry.StorageEntryID]...),
			}, nil
		}
	}
	return storagecatalog.EntryDetail{}, sql.ErrNoRows
}

func (c *httpMainDocumentsCatalog) TombstoneMainDocument(_ context.Context, input storagecatalog.MainDocumentTombstoneInput) (storagecatalog.Tombstone, error) {
	c.tombstones = append(c.tombstones, input)
	for i := range c.active {
		if c.active[i].StorageEntryID == input.StorageEntryID {
			c.active[i].AvailabilityState = storagecatalog.AvailabilityStateTombstoned
		}
	}
	return storagecatalog.Tombstone{
		StorageTombstoneID: ids.NewStorageTombstoneID(),
		StorageEntryID:     input.StorageEntryID,
		TombstoneKind:      input.TombstoneKind,
		Reason:             input.Reason,
		CreatedBy:          input.CreatedBy,
		CreatedAt:          input.TombstonedAt,
	}, nil
}

func httpMainDocumentEntry(relativePath string) storagecatalog.Entry {
	size := int64(len(relativePath))
	now := time.Date(2026, 6, 13, 18, 45, 0, 0, time.UTC)
	return storagecatalog.Entry{
		StorageEntryID:    ids.NewStorageEntryID(),
		StorageClass:      storagecatalog.StorageClassMainDocument,
		SourceArea:        storagecatalog.SourceAreaMainDocuments,
		OriginNodeKey:     "main",
		LogicalPath:       relativePath,
		CurrentViewPath:   "main/Documents/" + relativePath,
		SizeBytes:         &size,
		FileClass:         storagecatalog.ClassifyPath(relativePath, ""),
		ProcessingState:   storagecatalog.ProcessingStateMetadataOnly,
		AvailabilityState: storagecatalog.AvailabilityStateAvailable,
		RetentionState:    storagecatalog.RetentionStateSnapshot,
		Metadata:          json.RawMessage(`{"source":"http-main-documents-test"}`),
		CreatedAt:         now,
		UpdatedAt:         now,
	}
}
