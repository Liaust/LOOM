package storagedoctor

import (
	"context"
	"fmt"
	"testing"
	"time"

	"loom.local/loom/internal/mainstorage"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/storagecatalog"
)

func TestRunReportsHealthyStorage(t *testing.T) {
	entry := doctorEntry("storage_entry_ok")
	ref := doctorPhysicalRef(entry.StorageEntryID, storagecatalog.PhysicalRefStatusAvailable)
	client := newFakeDoctorClient(t, []storagecatalog.Entry{entry}, map[string]storagecatalog.EntryDetail{
		entry.StorageEntryID: {Entry: entry, PhysicalRefs: []storagecatalog.PhysicalRef{ref}},
	})

	report, err := Run(context.Background(), client, "corr_test", Options{MaxEntries: 20, MaxInspections: 20})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if report.Status != StatusOK {
		t.Fatalf("report status = %q, want ok; findings=%#v", report.Status, report.Findings)
	}
	if report.Summary.Errors != 0 || report.Summary.Warnings != 0 {
		t.Fatalf("unexpected summary: %#v findings=%#v", report.Summary, report.Findings)
	}
}

func TestRunReportsMissingPhysicalRefs(t *testing.T) {
	entry := doctorEntry("storage_entry_missing")
	client := newFakeDoctorClient(t, []storagecatalog.Entry{entry}, map[string]storagecatalog.EntryDetail{
		entry.StorageEntryID: {Entry: entry},
	})

	report, err := Run(context.Background(), client, "corr_test", Options{MaxEntries: 20, MaxInspections: 20})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if report.Status != StatusError {
		t.Fatalf("report status = %q, want error; findings=%#v", report.Status, report.Findings)
	}
	if !reportHasFinding(report, "missing_available_physical_ref") {
		t.Fatalf("expected missing physical ref finding, got %#v", report.Findings)
	}
}

func TestRunDoesNotRequirePhysicalRefsForDirectories(t *testing.T) {
	entry := doctorEntry("storage_entry_directory")
	entry.FileClass = storagecatalog.FileClassDirectory
	entry.SizeBytes = nil
	entry.LogicalPath = "folder"
	client := newFakeDoctorClient(t, []storagecatalog.Entry{entry}, map[string]storagecatalog.EntryDetail{
		entry.StorageEntryID: {Entry: entry},
	})

	report, err := Run(context.Background(), client, "corr_test", Options{MaxEntries: 20, MaxInspections: 20})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if report.Status != StatusOK {
		t.Fatalf("report status = %q, want ok; findings=%#v", report.Status, report.Findings)
	}
	if reportHasFinding(report, "missing_available_physical_ref") {
		t.Fatalf("directory metadata should not require a physical ref: %#v", report.Findings)
	}
}

func TestRunDoesNotRequirePhysicalRefsForZeroByteEntries(t *testing.T) {
	entry := doctorEntry("storage_entry_zero")
	zero := int64(0)
	entry.SizeBytes = &zero
	client := newFakeDoctorClient(t, []storagecatalog.Entry{entry}, map[string]storagecatalog.EntryDetail{
		entry.StorageEntryID: {Entry: entry},
	})

	report, err := Run(context.Background(), client, "corr_test", Options{MaxEntries: 20, MaxInspections: 20})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if report.Status != StatusOK {
		t.Fatalf("report status = %q, want ok; findings=%#v", report.Status, report.Findings)
	}
	if reportHasFinding(report, "missing_available_physical_ref") {
		t.Fatalf("zero-byte entries should not require a physical ref: %#v", report.Findings)
	}
}

func TestRunTreatsUnsafeTombstoneCountsAsInformational(t *testing.T) {
	entry := doctorEntry("storage_entry_ok")
	ref := doctorPhysicalRef(entry.StorageEntryID, storagecatalog.PhysicalRefStatusAvailable)
	client := newFakeDoctorClient(t, []storagecatalog.Entry{entry}, map[string]storagecatalog.EntryDetail{
		entry.StorageEntryID: {Entry: entry, PhysicalRefs: []storagecatalog.PhysicalRef{ref}},
	})
	client.retentionStatus = storagecatalog.RetentionStatus{
		Entries:          25,
		Retained:         1,
		Tombstoned:       24,
		UnsafeCandidates: 24,
		GeneratedAt:      time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC),
	}

	report, err := Run(context.Background(), client, "corr_test", Options{MaxEntries: 20, MaxInspections: 20})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if report.Status != StatusOK {
		t.Fatalf("report status = %q, want ok; findings=%#v", report.Status, report.Findings)
	}
	if !reportHasFinding(report, "unsafe_delete_candidates") || !reportHasFinding(report, "tombstone_count_mismatch") {
		t.Fatalf("expected informational retention findings, got %#v", report.Findings)
	}
	for _, finding := range report.Findings {
		if finding.Kind == "unsafe_delete_candidates" || finding.Kind == "tombstone_count_mismatch" {
			if finding.Severity != SeverityInfo {
				t.Fatalf("finding %s severity = %q, want info", finding.Kind, finding.Severity)
			}
		}
	}
}

func TestRunReportsMainDocumentsMissingCatalogRows(t *testing.T) {
	entry := doctorEntry("storage_entry_ok")
	ref := doctorPhysicalRef(entry.StorageEntryID, storagecatalog.PhysicalRefStatusAvailable)
	client := newFakeDoctorClient(t, []storagecatalog.Entry{entry}, map[string]storagecatalog.EntryDetail{
		entry.StorageEntryID: {Entry: entry, PhysicalRefs: []storagecatalog.PhysicalRef{ref}},
	})
	client.mainStatus.FilesMissingCataloged = 2
	client.mainStatus.FilesTombstoned = 1

	report, err := Run(context.Background(), client, "corr_test", Options{MaxEntries: 20, MaxInspections: 20})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if report.Status != StatusWarning {
		t.Fatalf("report status = %q, want warning; findings=%#v", report.Status, report.Findings)
	}
	if !reportHasFinding(report, "main_documents_missing_cataloged") {
		t.Fatalf("expected missing main Documents finding, got %#v", report.Findings)
	}
	if !reportHasFinding(report, "main_documents_tombstoned") {
		t.Fatalf("expected tombstoned main Documents finding, got %#v", report.Findings)
	}
}

func TestRunReportsArchivedProjectWithoutRuntimeManifest(t *testing.T) {
	entry := doctorEntry("storage_entry_ok")
	client := newFakeDoctorClient(t, []storagecatalog.Entry{entry}, map[string]storagecatalog.EntryDetail{
		entry.StorageEntryID: {Entry: entry, PhysicalRefs: []storagecatalog.PhysicalRef{doctorPhysicalRef(entry.StorageEntryID, storagecatalog.PhysicalRefStatusAvailable)}},
	})
	client.projects = []projects.Project{{
		Slug:         "gmail-automation",
		Status:       "archived",
		ArchiveState: []byte(`{"status":"archived"}`),
	}}

	report, err := Run(context.Background(), client, "corr_test", Options{MaxEntries: 20, MaxInspections: 20})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if report.Status != StatusError {
		t.Fatalf("report status = %q, want error; findings=%#v", report.Status, report.Findings)
	}
	if !reportHasFinding(report, "project_runtime_manifest_missing") {
		t.Fatalf("expected runtime manifest finding, got %#v", report.Findings)
	}
}

func reportHasFinding(report Report, kind string) bool {
	for _, finding := range report.Findings {
		if finding.Kind == kind {
			return true
		}
	}
	return false
}

func newFakeDoctorClient(t *testing.T, entries []storagecatalog.Entry, details map[string]storagecatalog.EntryDetail) *fakeDoctorClient {
	t.Helper()
	return &fakeDoctorClient{
		entries: entries,
		details: details,
		mainStatus: mainstorage.Status{
			BackingRoot:         "/var/lib/loom/main-documents",
			Exists:              true,
			StableWindowSeconds: 15,
			GeneratedAt:         time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC),
		},
		retentionStatus: storagecatalog.RetentionStatus{Entries: len(entries), Retained: len(entries), GeneratedAt: time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)},
	}
}

type fakeDoctorClient struct {
	entries           []storagecatalog.Entry
	details           map[string]storagecatalog.EntryDetail
	mainStatus        mainstorage.Status
	filesystemStatus  FilesystemStatus
	retentionStatus   storagecatalog.RetentionStatus
	projects          []projects.Project
	projectInspection storagearchive.ProjectArchiveInspectResult
}

func (f *fakeDoctorClient) GetStorageFilesystemStatus(context.Context, string) (response.Envelope[FilesystemStatus], error) {
	status := f.filesystemStatus
	if status.SchemaVersion == "" {
		status = FilesystemStatus{SchemaVersion: "v0.7", Status: StatusOK, Catalog: CatalogStatus{QueryLimit: 5000, Returned: len(f.entries)}}
	}
	return response.Success("corr_test", status), nil
}

func (f *fakeDoctorClient) ListStorageEntries(context.Context, string, storagecatalog.ListFilter) (response.Envelope[[]storagecatalog.Entry], error) {
	return response.Success("corr_test", f.entries), nil
}

func (f *fakeDoctorClient) InspectStorageEntry(_ context.Context, _ string, ref string) (response.Envelope[storagecatalog.EntryDetail], error) {
	detail, ok := f.details[ref]
	if !ok {
		return response.Envelope[storagecatalog.EntryDetail]{}, fmt.Errorf("missing detail %s", ref)
	}
	return response.Success("corr_test", detail), nil
}

func (f *fakeDoctorClient) GetMainDocumentsStatus(context.Context, string) (response.Envelope[mainstorage.Status], error) {
	return response.Success("corr_test", f.mainStatus), nil
}

func (f *fakeDoctorClient) GetStorageRetentionStatus(context.Context, string) (response.Envelope[storagecatalog.RetentionStatus], error) {
	return response.Success("corr_test", f.retentionStatus), nil
}

func (f *fakeDoctorClient) ListProjects(context.Context, string, int) (response.Envelope[[]projects.Project], error) {
	return response.Success("corr_test", f.projects), nil
}

func (f *fakeDoctorClient) InspectProjectArchive(context.Context, string, string) (response.Envelope[storagearchive.ProjectArchiveInspectResult], error) {
	return response.Success("corr_test", f.projectInspection), nil
}

func TestProjectPhysicalArchiveDoctorSeparatesCustodyAndHistory(t *testing.T) {
	for _, test := range []struct {
		evidence, next, kind string
		severity             string
	}{
		{"verified", "review_restore_plan", "", ""},
		{"verified", "activation_not_available", "project_restored_inactive", SeverityInfo},
		{"pending", "recover_restore", "project_physical_archive_incomplete", SeverityWarning},
		{"unavailable", "recover_archive", "project_physical_archive_unverified", SeverityWarning},
		{"conflict", "inspect_evidence", "project_physical_archive_conflict", SeverityError},
	} {
		t.Run(test.evidence+test.next, func(t *testing.T) {
			client := newFakeDoctorClient(t, nil, nil)
			client.projects = []projects.Project{{ProjectID: "project_test", Slug: "test", Status: "active", ArchiveState: []byte(`{"schema_version":"project.physical_archive_state.v1"}`)}}
			client.projectInspection.Physical = &storagearchive.ProjectPhysicalArchiveInspection{ProjectID: "project_test", MutationBlocked: true, EvidenceStatus: test.evidence, NextAction: test.next}
			check := checkProjectRuntimeArchives(context.Background(), client, "corr_test")
			for _, finding := range check.Findings {
				if finding.Kind == "project_runtime_manifest_missing" {
					t.Fatal("physical operation requires legacy manifest")
				}
			}
			if test.kind == "" {
				if len(check.Findings) != 0 {
					t.Fatalf("unexpected finding: %+v", check.Findings)
				}
				return
			}
			if len(check.Findings) != 1 || check.Findings[0].Kind != test.kind || check.Findings[0].Severity != test.severity {
				t.Fatalf("finding: %+v", check.Findings)
			}
		})
	}
}

func doctorEntry(id string) storagecatalog.Entry {
	size := int64(128)
	return storagecatalog.Entry{
		StorageEntryID:    id,
		StorageClass:      storagecatalog.StorageClassPrivateBackup,
		SourceArea:        storagecatalog.SourceAreaDocuments,
		OriginNodeKey:     "macbook",
		LogicalPath:       "report.md",
		SizeBytes:         &size,
		FileClass:         storagecatalog.FileClassMarkdown,
		ProcessingState:   storagecatalog.ProcessingStateBackupOnly,
		AvailabilityState: storagecatalog.AvailabilityStateAvailable,
		RetentionState:    storagecatalog.RetentionStateRetained,
	}
}

func doctorPhysicalRef(entryID, status string) storagecatalog.PhysicalRef {
	return storagecatalog.PhysicalRef{
		StoragePhysicalRefID: "storage_physical_ref_test",
		StorageEntryID:       entryID,
		RefKind:              storagecatalog.PhysicalRefKindBackupArtifact,
		URI:                  "/var/lib/loom/private-backups/report.md",
		Status:               status,
		CreatedAt:            time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC),
		UpdatedAt:            time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC),
	}
}
