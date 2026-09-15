package knowledge

import (
	"testing"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/notesprojection"
	"loom.local/loom/internal/storagecatalog"
)

func TestNormalizeNotesFileClassBucket(t *testing.T) {
	tests := map[string]string{
		storagecatalog.FileClassMarkdown:          NotesFileClassBucketMarkdown,
		storagecatalog.FileClassPDF:               NotesFileClassBucketPDF,
		storagecatalog.FileClassImage:             NotesFileClassBucketImage,
		storagecatalog.FileClassText:              NotesFileClassBucketText,
		storagecatalog.FileClassCode:              NotesFileClassBucketText,
		storagecatalog.FileClassOfficeDocument:    NotesFileClassBucketOffice,
		storagecatalog.FileClassDirectory:         NotesFileClassBucketDirectory,
		storagecatalog.FileClassVideo:             NotesFileClassBucketOther,
		storagecatalog.FileClassArchive:           NotesFileClassBucketOther,
		storagecatalog.FileClassBinary:            NotesFileClassBucketUnsupported,
		storagecatalog.FileClassGeneratedMetadata: NotesFileClassBucketUnsupported,
		storagecatalog.FileClassUnknown:           NotesFileClassBucketUnsupported,
		"":                                        NotesFileClassBucketUnsupported,
	}
	for input, want := range tests {
		if got := normalizeNotesFileClassBucket(input); got != want {
			t.Fatalf("normalizeNotesFileClassBucket(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestBuildNotesOverviewGroupsByNodeAndRoot(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	seenAt := now.Add(-30 * time.Minute)
	processedAt := now.Add(-20 * time.Minute)
	indexedAt := now.Add(-10 * time.Minute)
	pipelineUpdatedAt := now.Add(-5 * time.Minute)
	pipelineFailedAt := now.Add(-4 * time.Minute)
	boxRootID := ids.NewNotesSourceRootID()
	projectRootID := ids.NewNotesSourceRootID()
	nodeID := ids.NewNodeID()
	projectID := ids.NewProjectID()
	projectionStatus := notesprojection.Status{
		ProjectionRoot: "/var/lib/loom/loom-notes",
		Exists:         true,
		ManifestPath:   "/var/lib/loom/loom-notes/.loom/manifest.json",
		LastRebuildAt:  &indexedAt,
		Counts: notesprojection.Counts{
			Entries:      3,
			Materialized: 3,
		},
		ReadOnly:           true,
		RawWritesSupported: false,
		GeneratedAt:        now,
	}

	overview := buildNotesOverview(now, []notesOverviewObjectRow{
		{
			NotesSourceRootID:   boxRootID,
			RootKind:            RootKindBoxNotes,
			NodeID:              nodeID,
			NodeKey:             "main",
			BackendRootKey:      "loom_box__notes",
			DisplayName:         "Box Notes",
			SourcePath:          "/srv/loom-box/Notes",
			RootRelativePath:    "Notes",
			Status:              SourceRootStatusActive,
			FileClass:           storagecatalog.FileClassMarkdown,
			ProcessingState:     ProcessingStateChunked,
			ExtractionStatus:    ExtractionStatusExtracted,
			ObjectCount:         2,
			FileCount:           2,
			SizeBytes:           2048,
			SearchDocumentCount: 4,
			LastSeenAt:          &seenAt,
			LastProcessedAt:     &processedAt,
			LastIndexedAt:       &indexedAt,
		},
		{
			NotesSourceRootID: boxRootID,
			RootKind:          RootKindBoxNotes,
			NodeID:            nodeID,
			NodeKey:           "main",
			BackendRootKey:    "loom_box__notes",
			DisplayName:       "Box Notes",
			SourcePath:        "/srv/loom-box/Notes",
			RootRelativePath:  "Notes",
			Status:            SourceRootStatusActive,
			FileClass:         storagecatalog.FileClassDirectory,
			ProcessingState:   ProcessingStateMetadataOnly,
			ExtractionStatus:  ExtractionStatusMetadataOnly,
			ObjectCount:       1,
			DirectoryCount:    1,
		},
		{
			NotesSourceRootID:   projectRootID,
			RootKind:            RootKindProjectNotes,
			NodeID:              nodeID,
			NodeKey:             "main",
			ProjectID:           projectID,
			ProjectSlug:         "osint-tools",
			ProjectName:         "OSINT Tools",
			BackendRootKey:      "osint_tools__notes",
			DisplayName:         "Project Notes",
			SourcePath:          "/srv/loom-box/Projects/osint-tools/notes",
			RootRelativePath:    "notes",
			Status:              SourceRootStatusActive,
			FileClass:           storagecatalog.FileClassImage,
			ProcessingState:     ProcessingStateMetadataOnly,
			ExtractionStatus:    ExtractionStatusTooLarge,
			ObjectCount:         1,
			FileCount:           1,
			SizeBytes:           1024,
			SearchDocumentCount: 0,
		},
	}, []notesOverviewPipelineRow{
		{
			NotesSourceRootID: boxRootID,
			Status:            PipelineStatusComplete,
			Count:             2,
			LastUpdatedAt:     &pipelineUpdatedAt,
		},
		{
			NotesSourceRootID: boxRootID,
			Status:            PipelineStatusFailed,
			Count:             1,
			LastUpdatedAt:     &pipelineUpdatedAt,
			LastFailedAt:      &pipelineFailedAt,
		},
	}, &projectionStatus)

	if overview.GeneratedAt != now {
		t.Fatalf("generated_at = %s, want %s", overview.GeneratedAt, now)
	}
	if overview.Totals.RootCount != 2 || overview.Totals.ActiveRootCount != 2 {
		t.Fatalf("root totals = %#v, want 2 active roots", overview.Totals)
	}
	if overview.Totals.FileCount != 3 || overview.Totals.DirectoryCount != 1 || overview.Totals.ObjectCount != 4 {
		t.Fatalf("object totals = %#v, want files=3 dirs=1 objects=4", overview.Totals)
	}
	if overview.Totals.SearchDocumentCount != 4 || overview.IndexHealth.SearchDocumentCount != 4 {
		t.Fatalf("search totals = %#v/%#v, want 4", overview.Totals, overview.IndexHealth)
	}
	if overview.IndexHealth.Complete != 2 || overview.IndexHealth.Failed != 1 {
		t.Fatalf("index health = %#v, want complete=2 failed=1", overview.IndexHealth)
	}
	if overview.Projection.ProjectionRoot != projectionStatus.ProjectionRoot || !overview.Projection.ReadOnly || overview.Projection.Materialized != 3 {
		t.Fatalf("projection = %#v, want projection status copied", overview.Projection)
	}
	if len(overview.Nodes) != 1 {
		t.Fatalf("nodes len = %d, want 1: %#v", len(overview.Nodes), overview.Nodes)
	}
	node := overview.Nodes[0]
	if node.NodeKey != "main" || len(node.Roots) != 2 {
		t.Fatalf("node = %#v, want main with 2 roots", node)
	}
	box := node.Roots[0]
	if box.RootKind != RootKindBoxNotes || box.Totals.FileCount != 2 || box.Totals.DirectoryCount != 1 {
		t.Fatalf("box root = %#v, want box notes file/dir counts", box)
	}
	if got := fileClassCount(box.FileClasses, NotesFileClassBucketMarkdown); got != 2 {
		t.Fatalf("box markdown count = %d, want 2", got)
	}
	if got := fileClassCount(box.FileClasses, NotesFileClassBucketDirectory); got != 1 {
		t.Fatalf("box directory count = %d, want 1", got)
	}
	project := node.Roots[1]
	if project.ProjectSlug != "osint-tools" || project.Totals.FileCount != 1 {
		t.Fatalf("project root = %#v, want project notes summary", project)
	}
	if got := fileClassCount(overview.FileClasses, NotesFileClassBucketImage); got != 1 {
		t.Fatalf("overview image count = %d, want 1", got)
	}
	if got := extractionStateCount(overview.ExtractionStates, ExtractionStatusExtracted); got != 2 {
		t.Fatalf("overview extracted count = %d, want 2", got)
	}
	if got := extractionStateCount(overview.ExtractionStates, ExtractionStatusMetadataOnly); got != 1 {
		t.Fatalf("overview metadata-only count = %d, want 1", got)
	}
	if got := extractionStateCount(overview.ExtractionStates, ExtractionStatusTooLarge); got != 1 {
		t.Fatalf("overview too-large count = %d, want 1", got)
	}
	if got := extractionStateCount(box.ExtractionStates, ExtractionStatusExtracted); got != 2 {
		t.Fatalf("box extracted count = %d, want 2", got)
	}
	if got := extractionStateCount(project.ExtractionStates, ExtractionStatusTooLarge); got != 1 {
		t.Fatalf("project too-large count = %d, want 1", got)
	}
}

func TestBuildNotesOverviewIncludesZeroFileRoots(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 30, 0, 0, time.UTC)
	rootID := ids.NewNotesSourceRootID()

	overview := buildNotesOverview(now, []notesOverviewObjectRow{{
		NotesSourceRootID: rootID,
		RootKind:          RootKindBoxNotes,
		NodeKey:           "macbook",
		BackendRootKey:    "loom_box__notes",
		DisplayName:       "Box Notes",
		Status:            SourceRootStatusActive,
	}}, nil, nil)

	if overview.Totals.RootCount != 1 || overview.Totals.FileCount != 0 {
		t.Fatalf("overview totals = %#v, want zero-file active root", overview.Totals)
	}
	if len(overview.Nodes) != 1 || len(overview.Nodes[0].Roots) != 1 {
		t.Fatalf("nodes = %#v, want one node/root", overview.Nodes)
	}
	if overview.Nodes[0].Roots[0].Totals.ObjectCount != 0 {
		t.Fatalf("root totals = %#v, want zero objects", overview.Nodes[0].Roots[0].Totals)
	}
}

func fileClassCount(counts []NotesFileClassCount, fileClass string) int {
	for _, count := range counts {
		if count.FileClass == fileClass {
			return count.Count
		}
	}
	return 0
}

func extractionStateCount(counts []NotesExtractionStateCount, status string) int {
	for _, count := range counts {
		if count.ExtractionStatus == status {
			return count.Count
		}
	}
	return 0
}
