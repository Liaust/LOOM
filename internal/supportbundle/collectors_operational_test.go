package supportbundle

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/cloudstorage"
	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/loomcli/portal"
	"loom.local/loom/internal/mainstorage"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectstate"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/search"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/storagedoctor"
	"loom.local/loom/internal/workers"
)

func TestOperationalCollectorsUseBoundedMetadata(t *testing.T) {
	root := filepath.Clean("/Users/tester/loom-box")
	output := filepath.Join(t.TempDir(), "support.tar.gz")
	failureMessage := "extractor failed password=secret"
	client := fakeOperationalClient{
		fakeCoreClient: fakeCoreClient{},
		workers: []workers.WorkerListItem{
			{WorkerInstanceID: "worker_ok", WorkerKey: "main.ok", LifecycleStatus: "running", HealthStatus: workers.HealthHealthy},
			{WorkerInstanceID: "worker_bad", WorkerKey: "main.bad", WorkerKind: "indexer", DisplayName: "Indexer", LifecycleStatus: "running", HealthStatus: workers.HealthFailed, Severity: "critical", AttentionRequired: true},
		},
		queue: jobs.QueueSummary{FailedCount: 2, RunningCount: 1},
		failedJobs: []jobs.Job{
			{JobID: "job_failed_1", JobType: jobs.TypeScriptRun, Status: jobs.StatusFailed, FailureCode: strPtr("failed"), FailureMessage: &failureMessage, AttemptCount: 1, MaxAttempts: 3},
			{JobID: "job_failed_2", JobType: jobs.TypeWorkflowRun, Status: jobs.StatusFailed},
		},
		indexFailures: []search.IndexStatus{
			{IndexStatusID: "index_failed_1", SourceKind: "object", SourceID: "object_1", IndexType: "text", Status: "failed", LastErrorMessage: "token=abc123"},
			{IndexStatusID: "index_failed_2", SourceKind: "object", SourceID: "object_2", IndexType: "text", Status: "failed"},
		},
		notesOverview: knowledge.NotesOverview{
			Totals: knowledge.NotesOverviewTotals{RootCount: 1, FileCount: 3, SearchDocumentCount: 2},
			Nodes: []knowledge.NotesNodeOverview{{
				NodeKey: "main",
				Roots: []knowledge.NotesRootOverview{{
					RootKind:   "box",
					NodeKey:    "main",
					SourcePath: filepath.Join(root, "Notes"),
					Status:     "active",
				}},
			}},
		},
		pipelineStatus: knowledge.PipelineOverallStatus{
			Counts: map[string]int{knowledge.FilePipelineStatusFailed: 1},
			Current: []knowledge.PipelineRun{{
				KnowledgePipelineRunID: "pipeline_current_1",
				Status:                 knowledge.FilePipelineStatusWaitingHeavy,
				CurrentStageKey:        knowledge.FilePipelineStagePDFOCR,
				CurrentExecutionClass:  knowledge.PipelineExecutionHeavy,
				Generation:             2,
				LastErrorMessage:       "ocr text password=secret-image-description",
			}},
			Policy: knowledge.PipelinePolicyStatus{Policy: knowledge.PipelinePolicy{
				PDFOCREnabled:            true,
				ImageDescriptionsEnabled: true,
				EmbeddingsEnabled:        true,
			}},
			Operations: knowledge.PipelineOperationsStatus{WaitingHeavy: 1, HeavyCapacity: 1},
		},
		indexStatus: []search.IndexStatus{{IndexStatusID: "index_status_1", SourceKind: "note", SourceID: "note_1", IndexType: "text", Status: "complete"}},
		mainDocuments: mainstorage.Status{
			BackingRoot:     filepath.Join(root, "main-documents"),
			RetentionRoot:   filepath.Join(root, "retention"),
			Exists:          true,
			FilesDiscovered: 5,
			FilesAccepted:   4,
		},
		filesystemStatus: storagedoctor.FilesystemStatus{
			SchemaVersion: "v0.7", Status: storagedoctor.StatusOK,
			Roots:   []storagedoctor.PhysicalRootStatus{{Key: "box", Path: root, Status: storagedoctor.StatusOK, Exists: true, Directory: true}},
			Catalog: storagedoctor.CatalogStatus{QueryLimit: 500, Returned: 2, Available: 2},
		},
		storageRetention: storagecatalog.RetentionStatus{Entries: 2, Retained: 2},
		backup:           maintenance.BackupStatus{Status: "ok", OpenFindings: maintenance.FindingSummary{Open: 1}},
		projectList: []projects.Project{
			{ProjectID: "project_alpha", Slug: "alpha", Name: "Alpha", Status: "active", ProjectType: "tooling"},
			{ProjectID: "project_beta", Slug: "beta", Name: "Beta", Status: "archived", ProjectType: "tooling"},
		},
		projectDetails: map[string]projects.ProjectRegistrationDetail{
			"alpha": {
				Project: projects.ProjectDetail{Project: projects.Project{ProjectID: "project_alpha", Slug: "alpha", Name: "Alpha", Status: "active"}},
				Facets: []projects.ProjectContractFacet{
					{FacetKey: "scripts", Folder: "scripts", Enabled: true, Present: true, FacetStatus: projects.ProjectFacetStatusActivated},
					{FacetKey: "notes", Folder: "notes", Enabled: true, Present: true, FacetStatus: projects.ProjectFacetStatusActivated},
				},
			},
		},
		projectStates: map[string]projectstate.ProjectProjection{
			"alpha": {
				Project:     projectstate.ProjectIdentityProjection{ProjectID: "project_alpha", Lifecycle: "active"},
				Source:      projectstate.SourceProjection{ProjectContractSchemaVersion: projects.ProjectRepositoryProjectSchemaV04, ReposContractSchemaVersion: projects.ProjectRepositoryReposSchemaV04, SemanticDigest: "sha256:semantic", LocationDigest: "sha256:location", SourceRevision: 2},
				Observation: projectstate.ObservationSummary{Posture: projects.ProjectRepositoryObservationNotObserved, MemberCount: 2, Observed: 1, NotObserved: 1},
				Members: []projectstate.RepositoryProjection{
					{RepositoryID: "repo_alpha", Role: projects.ProjectRepositoryRolePrimary, RelativeSource: "repos/alpha", ObservationPosture: projects.ProjectRepositoryObservationObserved, DevelopmentState: projectstate.DevelopmentStateProjection{Posture: projectstate.DevelopmentStateNotEnabled}, Git: &projectstate.GitProjection{Head: "0123456789abcdef0123456789abcdef01234567", CurrentBranch: "main", Dirty: projectstate.GitDirtyProjection{Dirty: true, UntrackedChanges: true}, Worktree: true, WorktreeCanonicalProjectState: true}},
					{RepositoryID: "repo_secret", Role: projects.ProjectRepositoryRoleComponent, RelativeSource: "repos/secret", ObservationPosture: projects.ProjectRepositoryObservationNotObserved, ReasonCode: "member_path_missing", DevelopmentState: projectstate.DevelopmentStateProjection{Posture: projectstate.DevelopmentStateNotObserved}},
				},
			},
		},
	}
	cloudCalled := false
	result, err := Create(context.Background(), Options{
		OutputPath: output,
		Profile:    ProfileDefault,
		CoreClient: client,
		DoctorDataProvider: func(context.Context, Options) (portal.DoctorData, error) {
			return portal.BuildDoctorData(portal.Snapshot{CapturedAt: fixedTestTime()}), nil
		},
		CloudStatusProvider: func(_ context.Context, opts Options) (cloudstorage.StatusReport, error) {
			cloudCalled = true
			if opts.IncludeLive {
				t.Fatal("cloud collector should not request live probes by default")
			}
			return cloudstorage.StatusReport{
				SchemaVersion: "test",
				Status:        "ok",
				Mode:          string(cloudstorage.StatusModeCached),
				CheckedAt:     fixedTestTime(),
				Config:        cloudstorage.ConfigSummary{Path: filepath.Join(root, "cloud", "config.json"), Enabled: true},
				Roots: []cloudstorage.RootStatus{
					{Name: "main", Prefix: "main/", Exists: true, Status: "ok"},
					{Name: "extra", Prefix: "extra/", Exists: true, Status: "ok"},
				},
			}, nil
		},
		Projects:    []string{"alpha"},
		MaxItems:    1,
		Now:         fixedTestTime(),
		PathAliases: []PathAlias{{Label: "$LOOM_BOX", Root: root}},
	}, nil)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	for _, key := range []string{"workers", "jobs", "notes", "storage", "backup_coverage", "cloud", "projects", "portal"} {
		if !sectionStatusExists(result.Sections, key, SectionStatusIncluded) {
			t.Fatalf("section %s not included: %#v", key, result.Sections)
		}
	}
	if !cloudCalled {
		t.Fatal("cloud cached provider was not called")
	}
	jobsText := readArchiveText(t, output, "loom-support/summaries/jobs.json")
	if !strings.Contains(jobsText, "job_failed_1") || strings.Contains(jobsText, "job_failed_2") {
		t.Fatalf("job failure max item bound not enforced:\n%s", jobsText)
	}
	if strings.Contains(jobsText, "secret") || strings.Contains(jobsText, "abc123") {
		t.Fatalf("job/index failure details were not redacted:\n%s", jobsText)
	}
	notesText := readArchiveText(t, output, "loom-support/summaries/notes.json")
	if strings.Contains(notesText, root) || !strings.Contains(notesText, "$LOOM_BOX/Notes") {
		t.Fatalf("notes root path was not aliased:\n%s", notesText)
	}
	if strings.Contains(notesText, "secret-image-description") || strings.Contains(notesText, "ocr text") {
		t.Fatalf("pipeline support summary leaked content-bearing fields:\n%s", notesText)
	}
	if !strings.Contains(notesText, "pipeline_current_1") || !strings.Contains(notesText, `"stage": "pdf_ocr"`) {
		t.Fatalf("pipeline support summary omitted bounded operational fields:\n%s", notesText)
	}
	projectsText := readArchiveText(t, output, "loom-support/summaries/projects.json")
	if !strings.Contains(projectsText, `"facet_key": "scripts"`) || strings.Contains(projectsText, `"facet_key": "notes"`) {
		t.Fatalf("selected project facets were not bounded:\n%s", projectsText)
	}
	if !strings.Contains(projectsText, `"repository_id": "repo_alpha"`) || strings.Contains(projectsText, `"repository_id": "repo_secret"`) || !strings.Contains(projectsText, `"members_truncated": true`) {
		t.Fatalf("selected project repository state was not bounded:\n%s", projectsText)
	}
	if strings.Contains(projectsText, "/registered/") || strings.Contains(projectsText, `"worktree_canonical_project_state": true`) {
		t.Fatalf("project repository support summary leaked paths or canonicalized a worktree:\n%s", projectsText)
	}
	cloudText := readArchiveText(t, output, "loom-support/summaries/cloud.json")
	if !strings.Contains(cloudText, `"mode": "cached"`) || strings.Contains(cloudText, root) || !strings.Contains(cloudText, "$LOOM_BOX/cloud/config.json") {
		t.Fatalf("cloud summary should be cached and path-aliased:\n%s", cloudText)
	}
}

func TestOperationalCollectorFailureIsIsolatedAndRedacted(t *testing.T) {
	root := filepath.Clean("/Users/tester/loom-box")
	output := filepath.Join(t.TempDir(), "support.tar.gz")
	client := fakeOperationalClient{
		fakeCoreClient: fakeCoreClient{},
		workers:        []workers.WorkerListItem{{WorkerInstanceID: "worker_ok", WorkerKey: "main.ok", HealthStatus: workers.HealthHealthy}},
		notesErr:       errors.New("notes failed token=abc123 path=" + filepath.Join(root, "Notes")),
	}
	result, err := Create(context.Background(), Options{
		OutputPath: output,
		Profile:    ProfileDefault,
		CoreClient: client,
		DoctorDataProvider: func(context.Context, Options) (portal.DoctorData, error) {
			return portal.BuildDoctorData(portal.Snapshot{CapturedAt: fixedTestTime()}), nil
		},
		CloudStatusProvider: func(context.Context, Options) (cloudstorage.StatusReport, error) {
			return cloudstorage.StatusReport{Status: "disabled", Mode: string(cloudstorage.StatusModeCached)}, nil
		},
		Now:         fixedTestTime(),
		PathAliases: []PathAlias{{Label: "$LOOM_BOX", Root: root}},
	}, nil)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if !sectionStatusExists(result.Sections, "notes", SectionStatusFailed) {
		t.Fatalf("notes failure not recorded: %#v", result.Sections)
	}
	if !sectionStatusExists(result.Sections, "workers", SectionStatusIncluded) {
		t.Fatalf("workers should remain included after notes failure: %#v", result.Sections)
	}
	notes := findSectionResult(result.Sections, "notes")
	if notes == nil {
		t.Fatalf("missing notes section: %#v", result.Sections)
	}
	if strings.Contains(notes.Error, "abc123") || strings.Contains(notes.Error, root) {
		t.Fatalf("notes failure leaked sensitive data: %q", notes.Error)
	}
	if !strings.Contains(notes.Error, "[REDACTED]") || !strings.Contains(notes.Error, "$LOOM_BOX/Notes") {
		t.Fatalf("notes failure missing redaction/alias markers: %q", notes.Error)
	}
}

type fakeOperationalClient struct {
	fakeCoreClient
	workers          []workers.WorkerListItem
	queue            jobs.QueueSummary
	failedJobs       []jobs.Job
	indexFailures    []search.IndexStatus
	notesOverview    knowledge.NotesOverview
	pipelineStatus   knowledge.PipelineOverallStatus
	indexStatus      []search.IndexStatus
	mainDocuments    mainstorage.Status
	filesystemStatus storagedoctor.FilesystemStatus
	storageRetention storagecatalog.RetentionStatus
	backup           maintenance.BackupStatus
	projectList      []projects.Project
	projectDetails   map[string]projects.ProjectRegistrationDetail
	projectStates    map[string]projectstate.ProjectProjection
	notesErr         error
}

func (f fakeOperationalClient) ListWorkers(_ context.Context, correlationID string, _ workers.WorkerFilter) (response.Envelope[[]workers.WorkerListItem], error) {
	return response.Success(correlationID, f.workers), nil
}

func (f fakeOperationalClient) JobStatus(_ context.Context, correlationID string) (response.Envelope[jobs.QueueSummary], error) {
	return response.Success(correlationID, f.queue), nil
}

func (f fakeOperationalClient) ListJobs(_ context.Context, correlationID string, _ jobs.ListFilter) (response.Envelope[[]jobs.Job], error) {
	return response.Success(correlationID, f.failedJobs), nil
}

func (f fakeOperationalClient) ListIndexFailures(_ context.Context, correlationID string, _ search.StatusFilter) (response.Envelope[[]search.IndexStatus], error) {
	return response.Success(correlationID, f.indexFailures), nil
}

func (f fakeOperationalClient) GetKnowledgeNotesOverview(_ context.Context, correlationID string, _ knowledge.NotesOverviewInput) (response.Envelope[knowledge.NotesOverview], error) {
	if f.notesErr != nil {
		return response.Envelope[knowledge.NotesOverview]{}, f.notesErr
	}
	return response.Success(correlationID, f.notesOverview), nil
}

func (f fakeOperationalClient) GetKnowledgeNotesPipelineStatus(_ context.Context, correlationID string) (response.Envelope[knowledge.PipelineOverallStatus], error) {
	return response.Success(correlationID, f.pipelineStatus), nil
}

func (f fakeOperationalClient) ListIndexStatus(_ context.Context, correlationID string, _ search.StatusFilter) (response.Envelope[[]search.IndexStatus], error) {
	return response.Success(correlationID, f.indexStatus), nil
}

func (f fakeOperationalClient) GetMainDocumentsStatus(_ context.Context, correlationID string) (response.Envelope[mainstorage.Status], error) {
	return response.Success(correlationID, f.mainDocuments), nil
}

func (f fakeOperationalClient) GetStorageFilesystemStatus(_ context.Context, correlationID string) (response.Envelope[storagedoctor.FilesystemStatus], error) {
	return response.Success(correlationID, f.filesystemStatus), nil
}

func (f fakeOperationalClient) GetStorageRetentionStatus(_ context.Context, correlationID string) (response.Envelope[storagecatalog.RetentionStatus], error) {
	return response.Success(correlationID, f.storageRetention), nil
}

func (f fakeOperationalClient) MaintenanceBackupStatus(_ context.Context, correlationID string) (response.Envelope[maintenance.BackupStatus], error) {
	return response.Success(correlationID, f.backup), nil
}

func (f fakeOperationalClient) ListProjects(_ context.Context, correlationID string, _ int) (response.Envelope[[]projects.Project], error) {
	return response.Success(correlationID, f.projectList), nil
}

func (f fakeOperationalClient) GetProjectRegistrationStatus(_ context.Context, correlationID, ref string) (response.Envelope[projects.ProjectRegistrationDetail], error) {
	return response.Success(correlationID, f.projectDetails[ref]), nil
}

func (f fakeOperationalClient) GetProjectRepositoryState(_ context.Context, correlationID, ref string) (response.Envelope[projectstate.ProjectProjection], error) {
	return response.Success(correlationID, f.projectStates[ref]), nil
}

func strPtr(value string) *string {
	return &value
}
