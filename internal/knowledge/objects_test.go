package knowledge

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/storagecatalog"
)

func TestSyncedMetadataTimeRefreshPreservesProcessedContent(t *testing.T) {
	s := NewService(nil)
	node := ids.NewNodeID()
	root := SourceRoot{NotesSourceRootID: ids.NewNotesSourceRootID(), RootKind: RootKindBoxNotes, NodeID: &node,
		NodeKey: "owner", BackendRootKey: "notes", SourcePath: "/fixture", Status: SourceRootStatusActive}
	old := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	entry := SyncedObjectEntry{ObjectID: ids.NewObjectID(), ObjectVersionID: ids.NewObjectVersionID(), SourceNodeID: node,
		SourceNodeKey: "owner", SourcePath: "watched-root://notes/a.md", FileClass: storagecatalog.FileClassMarkdown,
		SourceHash: "sha256:" + strings.Repeat("a", 64), LastSeenAt: old,
		VersionMetadata: mustJSON(t, map[string]any{"source_mtime": old, "source_mtime_basis": AbsoluteTimeBasisSourceFilesystemMtime})}
	existing, err := s.knowledgeObjectFromSyncedObject(root, entry, "a.md")
	if err != nil {
		t.Fatal(err)
	}
	existing.ProcessingState = ProcessingStateIndexed
	existing.PipelineKey = KnowledgeObjectPipelineMarkdownText
	for i, timestamp := range []time.Time{old.Add(time.Hour), old.Add(-time.Hour), old} {
		entry.FileMetadata = mustJSON(t, map[string]any{"source_mtime": timestamp, "source_mtime_basis": AbsoluteTimeBasisSourceFilesystemMtime,
			"source_metadata_sequence": i + 1, "source_metadata_owner": node})
		next, err := s.knowledgeObjectFromSyncedObject(root, entry, "a.md")
		if err != nil {
			t.Fatal(err)
		}
		next = preserveKnowledgeObjectProcessing(existing, next)
		if next.SourceModifiedAt == nil || !next.SourceModifiedAt.Equal(timestamp) || !next.RecencyAt.Equal(timestamp) ||
			next.SourceRevision != existing.SourceRevision || next.ProcessingState != ProcessingStateIndexed {
			t.Fatalf("metadata refresh %+v", next)
		}
		existing = next
	}
	entry.FileMetadata = mustJSON(t, map[string]any{"source_mtime": old.Add(2 * time.Hour), "source_mtime_basis": AbsoluteTimeBasisSourceFilesystemMtime,
		"source_metadata_sequence": 1, "source_metadata_owner": node})
	next, err := s.knowledgeObjectFromSyncedObject(root, entry, "a.md")
	if err != nil {
		t.Fatal(err)
	}
	if got := preserveKnowledgeObjectProcessing(existing, next); !got.SourceModifiedAt.Equal(*existing.SourceModifiedAt) {
		t.Fatal("stale observation changed time")
	}
	entry.FileMetadata = mustJSON(t, map[string]any{"source_mtime": old.Add(2 * time.Hour), "source_mtime_basis": AbsoluteTimeBasisSourceFilesystemMtime,
		"source_metadata_sequence": 5, "source_metadata_owner": ids.NewNodeID()})
	next, err = s.knowledgeObjectFromSyncedObject(root, entry, "a.md")
	if err != nil {
		t.Fatal(err)
	}
	if got := preserveKnowledgeObjectProcessing(existing, next); !got.SourceModifiedAt.Equal(*existing.SourceModifiedAt) {
		t.Fatal("wrong owner changed time")
	}
}

func TestExpandedKnowledgeObjectMembershipAndPrivacy(t *testing.T) {
	service := NewService(nil)
	nodeID, projectID := ids.NewNodeID(), ids.NewProjectID()
	for _, kind := range []string{RootKindBoxTopics, RootKindBoxLibrary, RootKindProjectMaterial} {
		t.Run(kind, func(t *testing.T) {
			root := SourceRoot{NotesSourceRootID: ids.NewNotesSourceRootID(), RootKind: kind, NodeID: &nodeID, NodeKey: "main", BackendRootKey: "declared", SourcePath: "/fixture", Status: SourceRootStatusActive}
			entry := storagecatalog.Entry{StorageEntryID: ids.NewStorageEntryID(), SourceArea: storagecatalog.SourceAreaExternalWatchedRoot, OriginNodeID: &nodeID, OriginNodeKey: "main", WatchedRootKey: "declared", LogicalPath: "source.md", OriginalSourcePath: "source.md", FileClass: storagecatalog.FileClassMarkdown, AvailabilityState: storagecatalog.AvailabilityStateAvailable}
			if kind == RootKindProjectMaterial {
				root.ProjectID, entry.ProjectID, entry.SourceArea = &projectID, &projectID, storagecatalog.SourceAreaProjects
			}
			candidates, _ := service.BuildKnowledgeObjectCandidates([]SourceRoot{root}, []storagecatalog.Entry{entry})
			if len(candidates) != 1 {
				t.Fatalf("declared source not admitted: %#v", candidates)
			}
			for name, mutate := range map[string]func(*storagecatalog.Entry){
				"missing-node":  func(e *storagecatalog.Entry) { e.OriginNodeID = nil },
				"wrong-node":    func(e *storagecatalog.Entry) { other := ids.NewNodeID(); e.OriginNodeID = &other },
				"wrong-key":     func(e *storagecatalog.Entry) { e.OriginNodeKey = "other" },
				"wrong-root":    func(e *storagecatalog.Entry) { e.WatchedRootKey = "other" },
				"wrong-project": func(e *storagecatalog.Entry) { other := ids.NewProjectID(); e.ProjectID = &other },
				"private":       func(e *storagecatalog.Entry) { e.Metadata = mustJSON(t, map[string]any{"private_no_index": true}) },
			} {
				t.Run(name, func(t *testing.T) {
					changed := entry
					mutate(&changed)
					got, _ := service.BuildKnowledgeObjectCandidates([]SourceRoot{root}, []storagecatalog.Entry{changed})
					if len(got) != 0 {
						t.Fatal("admitted mismatched/private entry")
					}
				})
			}
			for _, path := range []string{"private_no_index/secret.md", "credentials/access.md", ".hermes/memory.md", ".repo/state.md", "node_modules/source.md", ".git/config", ".env.local"} {
				changed := entry
				changed.LogicalPath, changed.OriginalSourcePath = path, path
				got, _ := service.BuildKnowledgeObjectCandidates([]SourceRoot{root}, []storagecatalog.Entry{changed})
				if len(got) != 0 {
					t.Fatalf("indexed excluded path %s", path)
				}
			}
		})
	}
}

func TestExpandedProjectSyncedIdentityPolicy(t *testing.T) {
	nodeID, projectID := ids.NewNodeID(), ids.NewProjectID()
	root := SourceRoot{NotesSourceRootID: ids.NewNotesSourceRootID(), RootKind: RootKindProjectMaterial, NodeID: &nodeID, NodeKey: "main", ProjectID: &projectID, BackendRootKey: "declared", SourcePath: "/fixture", Status: SourceRootStatusActive}
	entry := SyncedObjectEntry{NotesSourceRootID: root.NotesSourceRootID, BackendRootKey: root.BackendRootKey, SourceNodeID: nodeID, SourceNodeKey: "main", ProjectID: projectID, SourcePath: "watched-root://declared/source.md", LogicalName: "source.md", FileClass: storagecatalog.FileClassMarkdown}
	service := NewService(nil)
	got, _ := service.BuildKnowledgeObjectCandidates([]SourceRoot{root}, nil, []SyncedObjectEntry{entry})
	if len(got) != 1 {
		t.Fatal("valid synced material missing")
	}
	for name, mutate := range map[string]func(*SyncedObjectEntry){
		"owner":         func(e *SyncedObjectEntry) { e.SourceNodeID = ids.NewNodeID() },
		"project":       func(e *SyncedObjectEntry) { e.ProjectID = ids.NewProjectID() },
		"backend":       func(e *SyncedObjectEntry) { e.BackendRootKey = "wrong" },
		"file-policy":   func(e *SyncedObjectEntry) { e.IndexPolicy = "private_no_index" },
		"file-metadata": func(e *SyncedObjectEntry) { e.FileMetadata = json.RawMessage(`{"private_no_index":true}`) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := entry
			mutate(&changed)
			got, _ := service.BuildKnowledgeObjectCandidates([]SourceRoot{root}, nil, []SyncedObjectEntry{changed})
			if len(got) != 0 {
				t.Fatal("exact root ID bypassed membership/policy")
			}
		})
	}
	root.Metadata = json.RawMessage(`{"registration_metadata":{"knowledge_source":{"include":["**/*.md"],"exclude":["private/**"]}}}`)
	if expandedSourceExcluded(root, "source.md", nil) || !expandedSourceExcluded(root, "source.txt", nil) || !expandedSourceExcluded(root, "private/secret.md", nil) {
		t.Fatal("current declaration patterns not enforced")
	}
	root.Metadata = json.RawMessage(`{"registration_metadata":{"knowledge_source":{"include":[false]}}}`)
	if !expandedSourceExcluded(root, "source.md", nil) {
		t.Fatal("malformed patterns accepted")
	}
}

func TestBoxSyncedIdentityAndPrivacy(t *testing.T) {
	for _, kind := range []string{RootKindBoxNotes, RootKindBoxTopics, RootKindBoxLibrary} {
		t.Run(kind, func(t *testing.T) {
			nodeID := ids.NewNodeID()
			root := SourceRoot{NotesSourceRootID: ids.NewNotesSourceRootID(), RootKind: kind, NodeID: &nodeID, NodeKey: "owner", BackendRootKey: "loom_box__area", SourcePath: "/fixture", Status: SourceRootStatusActive, Metadata: json.RawMessage(`{"box_id":"fixture"}`)}
			entry := SyncedObjectEntry{NotesSourceRootID: root.NotesSourceRootID, BackendRootKey: root.BackendRootKey, SourceNodeID: nodeID, SourceNodeKey: root.NodeKey, ScopeKey: "loom_box:fixture:" + strings.TrimPrefix(kind, "box_"), SourcePath: "watched-root://loom_box__area/source.md", FileClass: storagecatalog.FileClassMarkdown}
			service := NewService(nil)
			if got, skipped := service.BuildKnowledgeObjectCandidates([]SourceRoot{root}, nil, []SyncedObjectEntry{entry}); len(got) != 1 {
				t.Fatalf("valid Box sync rejected: %+v", skipped)
			}
			for name, mutate := range map[string]func(*SyncedObjectEntry){
				"node-id":         func(e *SyncedObjectEntry) { e.SourceNodeID = ids.NewNodeID() },
				"node-key":        func(e *SyncedObjectEntry) { e.SourceNodeKey = "other" },
				"project":         func(e *SyncedObjectEntry) { e.ProjectID = ids.NewProjectID() },
				"root-id":         func(e *SyncedObjectEntry) { e.NotesSourceRootID = ids.NewNotesSourceRootID() },
				"backend":         func(e *SyncedObjectEntry) { e.BackendRootKey = "other" },
				"scope":           func(e *SyncedObjectEntry) { e.ScopeKey = "loom_box:other:notes" },
				"path":            func(e *SyncedObjectEntry) { e.SourcePath = "watched-root://loomXboxXXarea/source.md" },
				"private-policy":  func(e *SyncedObjectEntry) { e.IndexPolicy = "private_no_index" },
				"private-object":  func(e *SyncedObjectEntry) { e.ObjectMetadata = json.RawMessage(`{"private_no_index":true}`) },
				"private-version": func(e *SyncedObjectEntry) { e.VersionMetadata = json.RawMessage(`{"index_policy":"private_no_index"}`) },
				"private-file":    func(e *SyncedObjectEntry) { e.FileMetadata = json.RawMessage(`{"private_no_index":true}`) },
				"excluded-path":   func(e *SyncedObjectEntry) { e.SourcePath = "watched-root://loom_box__area/.hermes/memory.md" },
			} {
				t.Run(name, func(t *testing.T) {
					changed := entry
					mutate(&changed)
					if got, _ := service.BuildKnowledgeObjectCandidates([]SourceRoot{root}, nil, []SyncedObjectEntry{changed}); len(got) != 0 {
						t.Fatal("Box root ID bypassed source identity/privacy")
					}
				})
			}
		})
	}
}

func TestBuildKnowledgeObjectCandidatesMapsNotesCatalogueEntries(t *testing.T) {
	fixed := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	service := NewService(nil, WithClock(func() time.Time { return fixed }))

	nodeID := ids.NewNodeID()
	projectID := ids.NewProjectID()
	boxRoot := SourceRoot{
		NotesSourceRootID: ids.NewNotesSourceRootID(),
		RootKind:          RootKindBoxNotes,
		NodeID:            &nodeID,
		NodeKey:           "main",
		BackendRootKey:    "loom_box__notes",
		SourcePath:        "/srv/loom-box/notes",
		Status:            SourceRootStatusActive,
	}
	projectRoot := SourceRoot{
		NotesSourceRootID: ids.NewNotesSourceRootID(),
		RootKind:          RootKindProjectNotes,
		NodeID:            &nodeID,
		NodeKey:           "main",
		ProjectID:         &projectID,
		BackendRootKey:    "project_osint_tools__notes",
		SourcePath:        "/srv/loom-box/projects/osint-tools/notes",
		Status:            SourceRootStatusActive,
	}
	size := int64(4096)
	deletedAt := fixed.Add(-time.Minute)
	originMtime := time.Date(2018, 3, 4, 5, 6, 7, 0, time.UTC)
	entries := []storagecatalog.Entry{
		{
			StorageEntryID:     ids.NewStorageEntryID(),
			StorageClass:       storagecatalog.StorageClassPrivateBackup,
			SourceArea:         storagecatalog.SourceAreaNotes,
			OriginNodeID:       &nodeID,
			OriginNodeKey:      "main",
			WatchedRootKey:     "loom_box__notes",
			LogicalPath:        "daily.md",
			OriginalSourcePath: "daily.md",
			ChecksumAlgorithm:  "sha256",
			ChecksumHex:        strings.Repeat("a", 64),
			SizeBytes:          &size,
			MimeType:           "text/markdown",
			FileClass:          storagecatalog.FileClassMarkdown,
			ProcessingState:    storagecatalog.ProcessingStateBackupOnly,
			AvailabilityState:  storagecatalog.AvailabilityStateAvailable,
			RetentionState:     storagecatalog.RetentionStateNone,
			Metadata: mustJSON(t, map[string]any{
				"source":                "test",
				"source_modified_at":    originMtime.Format(time.RFC3339),
				"source_modified_basis": AbsoluteTimeBasisSourceFilesystemMtime,
			}),
			UpdatedAt: fixed,
		},
		{
			StorageEntryID:     ids.NewStorageEntryID(),
			StorageClass:       storagecatalog.StorageClassPrivateBackup,
			SourceArea:         storagecatalog.SourceAreaProjects,
			OriginNodeID:       &nodeID,
			OriginNodeKey:      "main",
			ProjectID:          &projectID,
			WatchedRootKey:     "project_osint_tools__notes",
			LogicalPath:        "captures/map.png",
			OriginalSourcePath: "captures/map.png",
			SizeBytes:          &size,
			MimeType:           "image/png",
			FileClass:          storagecatalog.FileClassImage,
			ProcessingState:    storagecatalog.ProcessingStateBackupOnly,
			AvailabilityState:  storagecatalog.AvailabilityStateAvailable,
			RetentionState:     storagecatalog.RetentionStateNone,
			Metadata:           mustJSON(t, map[string]any{}),
			UpdatedAt:          fixed,
		},
		{
			StorageEntryID:     ids.NewStorageEntryID(),
			StorageClass:       storagecatalog.StorageClassPrivateBackup,
			SourceArea:         storagecatalog.SourceAreaNotes,
			OriginNodeID:       &nodeID,
			OriginNodeKey:      "main",
			WatchedRootKey:     "loom_box__notes",
			LogicalPath:        "old.md",
			OriginalSourcePath: "old.md",
			FileClass:          storagecatalog.FileClassMarkdown,
			ProcessingState:    storagecatalog.ProcessingStateBackupOnly,
			AvailabilityState:  storagecatalog.AvailabilityStateDeleted,
			RetentionState:     storagecatalog.RetentionStateNone,
			Metadata:           mustJSON(t, map[string]any{}),
			UpdatedAt:          deletedAt,
		},
		{
			StorageEntryID:     ids.NewStorageEntryID(),
			SourceArea:         storagecatalog.SourceAreaDocuments,
			WatchedRootKey:     "loom_box__documents",
			LogicalPath:        "docs/ignored.md",
			OriginalSourcePath: "docs/ignored.md",
		},
		{
			StorageEntryID:     ids.NewStorageEntryID(),
			SourceArea:         storagecatalog.SourceAreaNotes,
			WatchedRootKey:     "other_notes",
			LogicalPath:        "unmatched.md",
			OriginalSourcePath: "unmatched.md",
		},
	}

	candidates, skipped := service.BuildKnowledgeObjectCandidates([]SourceRoot{boxRoot, projectRoot}, entries)

	if len(candidates) != 3 {
		t.Fatalf("candidates len = %d, want 3: %#v", len(candidates), candidates)
	}
	if len(skipped) != 2 {
		t.Fatalf("skipped len = %d, want 2: %#v", len(skipped), skipped)
	}
	markdown := candidates[0].Object
	if markdown.NotesSourceRootID != boxRoot.NotesSourceRootID || markdown.RelativePath != "daily.md" {
		t.Fatalf("markdown object = %#v, want box root daily.md", markdown)
	}
	if markdown.SourceHash != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("source hash = %q, want sha256 content address", markdown.SourceHash)
	}
	if markdown.PipelineKey != KnowledgeObjectPipelineTextReady || markdown.ProcessingState != ProcessingStateMetadataOnly {
		t.Fatalf("markdown pipeline/state = %q/%q, want text-ready metadata-only", markdown.PipelineKey, markdown.ProcessingState)
	}
	if markdown.SourceModifiedAt == nil || !markdown.SourceModifiedAt.Equal(originMtime) ||
		!markdown.RecencyAt.Equal(originMtime) || markdown.RecencyBasis != AbsoluteTimeBasisSourceFilesystemMtime {
		t.Fatalf("markdown source chronology = %v/%s/%s", markdown.SourceModifiedAt, markdown.RecencyAt, markdown.RecencyBasis)
	}
	if !metadataBool(t, markdown.Metadata, "text_pipeline_ready") {
		t.Fatalf("markdown metadata = %s, want text_pipeline_ready true", markdown.Metadata)
	}
	image := candidates[1].Object
	if image.NotesSourceRootID != projectRoot.NotesSourceRootID || image.FileClass != storagecatalog.FileClassImage {
		t.Fatalf("image object = %#v, want project image object", image)
	}
	if image.PipelineKey != KnowledgeObjectPipelineMetadata || metadataBool(t, image.Metadata, "text_pipeline_ready") {
		t.Fatalf("image pipeline metadata = %q/%s, want metadata-only not text-ready", image.PipelineKey, image.Metadata)
	}
	deleted := candidates[2].Object
	if deleted.ProcessingState != ProcessingStateDeleted || deleted.DeletedAt == nil || !deleted.DeletedAt.Equal(deletedAt) {
		t.Fatalf("deleted object = %#v, want deleted state at %s", deleted, deletedAt)
	}
}

func TestBuildKnowledgeObjectCandidatesPrefersCurrentStorageEntryForSamePath(t *testing.T) {
	fixed := time.Date(2026, 7, 4, 10, 30, 0, 0, time.UTC)
	service := NewService(nil, WithClock(func() time.Time { return fixed }))
	root := SourceRoot{
		NotesSourceRootID: ids.NewNotesSourceRootID(),
		RootKind:          RootKindBoxNotes,
		NodeKey:           "macbook",
		BackendRootKey:    "loom_box__notes",
		SourcePath:        "/Users/test/loom-box/Notes",
		Status:            SourceRootStatusActive,
	}
	size := int64(96)
	supersededID := ids.NewStorageEntryID()
	availableID := ids.NewStorageEntryID()
	entries := []storagecatalog.Entry{
		{
			StorageEntryID:     supersededID,
			StorageClass:       storagecatalog.StorageClassPrivateBackup,
			SourceArea:         storagecatalog.SourceAreaNotes,
			OriginNodeKey:      "macbook",
			WatchedRootKey:     "loom_box__notes",
			LogicalPath:        ".loom-acceptance",
			OriginalSourcePath: ".loom-acceptance",
			CurrentViewPath:    "macbook/Backups/Notes/history/OLD/.loom-acceptance",
			FileClass:          storagecatalog.FileClassDirectory,
			SizeBytes:          &size,
			AvailabilityState:  storagecatalog.AvailabilityStateSuperseded,
			UpdatedAt:          fixed,
		},
		{
			StorageEntryID:     availableID,
			StorageClass:       storagecatalog.StorageClassPrivateBackup,
			SourceArea:         storagecatalog.SourceAreaNotes,
			OriginNodeKey:      "macbook",
			WatchedRootKey:     "loom_box__notes",
			LogicalPath:        ".loom-acceptance",
			OriginalSourcePath: ".loom-acceptance",
			CurrentViewPath:    "macbook/Backups/Notes/current/.loom-acceptance",
			FileClass:          storagecatalog.FileClassDirectory,
			SizeBytes:          &size,
			AvailabilityState:  storagecatalog.AvailabilityStateAvailable,
			UpdatedAt:          fixed,
		},
	}

	candidates, skipped := service.BuildKnowledgeObjectCandidates([]SourceRoot{root}, entries)

	if len(skipped) != 0 {
		t.Fatalf("skipped len = %d, want 0: %#v", len(skipped), skipped)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates len = %d, want 1: %#v", len(candidates), candidates)
	}
	candidate := candidates[0]
	if candidate.Entry.StorageEntryID != availableID {
		t.Fatalf("candidate storage entry = %s, want current available %s", candidate.Entry.StorageEntryID, availableID)
	}
	if candidate.Object.StorageEntryID == nil || *candidate.Object.StorageEntryID != availableID {
		t.Fatalf("object storage entry = %v, want %s", candidate.Object.StorageEntryID, availableID)
	}
	if candidate.Object.ProcessingState != ProcessingStateMetadataOnly {
		t.Fatalf("processing state = %q, want metadata_only", candidate.Object.ProcessingState)
	}
	if metadataString(t, candidate.Object.Metadata, "origin") != KnowledgeObjectOriginStorageCatalog {
		t.Fatalf("metadata = %s, want storage catalog origin", candidate.Object.Metadata)
	}
}

func TestBuildKnowledgeObjectCandidatesMapsProjectSyncedObjects(t *testing.T) {
	fixed := time.Date(2026, 7, 4, 10, 45, 0, 0, time.UTC)
	service := NewService(nil, WithClock(func() time.Time { return fixed }))
	nodeID := ids.NewNodeID()
	projectID := ids.NewProjectID()
	root := SourceRoot{
		NotesSourceRootID: ids.NewNotesSourceRootID(),
		RootKind:          RootKindProjectNotes,
		NodeID:            &nodeID,
		NodeKey:           "main",
		ProjectID:         &projectID,
		BackendRootKey:    "osint_tools__notes",
		SourcePath:        "/srv/loom-box/projects/osint-tools/notes",
		Status:            SourceRootStatusActive,
	}
	size := int64(300)
	synced := SyncedObjectEntry{
		NotesSourceRootID: root.NotesSourceRootID,
		BackendRootKey:    root.BackendRootKey,
		ReplicaID:         "replica_test",
		StorageRef:        "sha256:" + strings.Repeat("d", 64),
		ObjectID:          "object_test",
		ObjectVersionID:   "version_test",
		SourceNodeID:      nodeID,
		SourceNodeKey:     "main",
		ProjectID:         projectID,
		SourcePath:        "watched-root://osint_tools__notes/.loom-acceptance/project-note.md",
		LogicalName:       ".loom-acceptance/project-note.md",
		FileClass:         storagecatalog.FileClassMarkdown,
		MimeType:          "text/markdown",
		SizeBytes:         &size,
		VersionMetadata: mustJSON(t, map[string]any{
			"source_mtime":       "2019-04-05T06:07:08Z",
			"source_mtime_basis": AbsoluteTimeBasisSourceFilesystemMtime,
		}),
		LastSeenAt: fixed,
	}

	candidates, skipped := service.BuildKnowledgeObjectCandidates([]SourceRoot{root}, nil, []SyncedObjectEntry{synced})

	if len(skipped) != 0 {
		t.Fatalf("skipped len = %d, want 0: %#v", len(skipped), skipped)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates len = %d, want 1: %#v", len(candidates), candidates)
	}
	candidate := candidates[0]
	if candidate.Origin != KnowledgeObjectOriginSyncedObject || candidate.Synced == nil {
		t.Fatalf("candidate origin/synced = %q/%#v, want synced object origin", candidate.Origin, candidate.Synced)
	}
	object := candidate.Object
	if object.StorageEntryID != nil {
		t.Fatalf("storage entry id = %q, want nil for synced object", *object.StorageEntryID)
	}
	if object.NotesSourceRootID != root.NotesSourceRootID || object.RelativePath != ".loom-acceptance/project-note.md" {
		t.Fatalf("object root/path = %s/%s, want project root relative path", object.NotesSourceRootID, object.RelativePath)
	}
	if object.SourcePath != "/srv/loom-box/projects/osint-tools/notes/.loom-acceptance/project-note.md" {
		t.Fatalf("source path = %q, want real project notes source path", object.SourcePath)
	}
	if object.SourceHash != synced.StorageRef {
		t.Fatalf("source hash = %q, want %q", object.SourceHash, synced.StorageRef)
	}
	if object.SourceModifiedAt == nil || object.SourceModifiedAt.Format(time.RFC3339) != "2019-04-05T06:07:08Z" ||
		object.RecencyBasis != AbsoluteTimeBasisSourceFilesystemMtime {
		t.Fatalf("synced object chronology = %v/%s", object.SourceModifiedAt, object.RecencyBasis)
	}
	if object.PipelineKey != KnowledgeObjectPipelineTextReady || !metadataBool(t, object.Metadata, "text_pipeline_ready") {
		t.Fatalf("object pipeline metadata = %q/%s, want text-ready", object.PipelineKey, object.Metadata)
	}
	if metadataString(t, object.Metadata, "origin") != KnowledgeObjectOriginSyncedObject {
		t.Fatalf("metadata origin = %s, want %s", object.Metadata, KnowledgeObjectOriginSyncedObject)
	}
}

func TestBuildKnowledgeObjectCandidatesSkipsNotesControlContracts(t *testing.T) {
	service := NewService(nil)
	nodeID := ids.NewNodeID()
	projectID := ids.NewProjectID()
	root := SourceRoot{
		NotesSourceRootID: ids.NewNotesSourceRootID(),
		RootKind:          RootKindProjectNotes,
		NodeID:            &nodeID,
		NodeKey:           "main",
		ProjectID:         &projectID,
		BackendRootKey:    "osint_tools__notes",
		SourcePath:        "/srv/loom-box/projects/osint-tools/notes",
		Status:            SourceRootStatusActive,
	}
	entries := []storagecatalog.Entry{
		{
			StorageEntryID:     ids.NewStorageEntryID(),
			SourceArea:         storagecatalog.SourceAreaProjects,
			OriginNodeID:       &nodeID,
			OriginNodeKey:      "main",
			ProjectID:          &projectID,
			WatchedRootKey:     root.BackendRootKey,
			LogicalPath:        "loom.notes.yaml",
			OriginalSourcePath: "loom.notes.yaml",
			FileClass:          storagecatalog.FileClassText,
			AvailabilityState:  storagecatalog.AvailabilityStateAvailable,
		},
	}
	synced := []SyncedObjectEntry{
		{
			NotesSourceRootID: root.NotesSourceRootID,
			BackendRootKey:    root.BackendRootKey,
			ReplicaID:         "replica_contract",
			ObjectID:          "object_contract",
			ObjectVersionID:   "version_contract",
			SourceNodeID:      nodeID,
			SourceNodeKey:     "main",
			ProjectID:         projectID,
			SourcePath:        "watched-root://osint_tools__notes/nested/loom.notes.yml",
			LogicalName:       "nested/loom.notes.yml",
			FileClass:         storagecatalog.FileClassText,
		},
	}

	candidates, skipped := service.BuildKnowledgeObjectCandidates([]SourceRoot{root}, entries, synced)

	if len(candidates) != 0 {
		t.Fatalf("candidates len = %d, want 0: %#v", len(candidates), candidates)
	}
	if len(skipped) != 2 {
		t.Fatalf("skipped len = %d, want 2: %#v", len(skipped), skipped)
	}
	for _, skip := range skipped {
		if skip.Reason != ignoredNotesKnowledgeContractReason {
			t.Fatalf("skip reason = %q, want %q: %#v", skip.Reason, ignoredNotesKnowledgeContractReason, skipped)
		}
	}
}

func TestPreserveKnowledgeObjectProcessingKeepsIndexedStateForSameRevision(t *testing.T) {
	processedAt := time.Date(2026, 7, 4, 11, 0, 0, 0, time.UTC)
	existing := KnowledgeObject{
		SourceRevision:  "sha256:" + strings.Repeat("e", 64),
		ProcessingState: ProcessingStateChunked,
		PipelineKey:     KnowledgeObjectPipelineMarkdownText,
		PipelineVersion: KnowledgeMarkdownTextPipelineVersion,
		LastProcessedAt: &processedAt,
		Metadata:        mustJSON(t, map[string]any{"origin": "old", "text_pipeline": map[string]any{"chunk_count": 2}}),
	}
	next := KnowledgeObject{
		SourceRevision:  existing.SourceRevision,
		ProcessingState: ProcessingStateMetadataOnly,
		PipelineKey:     KnowledgeObjectPipelineTextReady,
		PipelineVersion: KnowledgeObjectPipelineVersionV08,
		Metadata:        mustJSON(t, map[string]any{"origin": KnowledgeObjectOriginSyncedObject}),
	}

	preserved := preserveKnowledgeObjectProcessing(existing, next)

	if preserved.ProcessingState != ProcessingStateChunked || preserved.PipelineKey != KnowledgeObjectPipelineMarkdownText {
		t.Fatalf("processing = %s/%s, want preserved chunked markdown pipeline", preserved.ProcessingState, preserved.PipelineKey)
	}
	if preserved.LastProcessedAt == nil || !preserved.LastProcessedAt.Equal(processedAt) {
		t.Fatalf("last_processed_at = %v, want %s", preserved.LastProcessedAt, processedAt)
	}
	if metadataString(t, preserved.Metadata, "origin") != KnowledgeObjectOriginSyncedObject {
		t.Fatalf("metadata origin = %s, want new origin", preserved.Metadata)
	}
	var decoded map[string]any
	if err := json.Unmarshal(preserved.Metadata, &decoded); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if _, ok := decoded["text_pipeline"]; !ok {
		t.Fatalf("metadata = %s, want preserved text_pipeline", preserved.Metadata)
	}
}

func TestPreserveKnowledgeObjectProcessingKeepsAbsoluteTimeForSameRevision(t *testing.T) {
	revision := "sha256:" + strings.Repeat("d", 64)
	sourceCreated := time.Date(2019, 2, 3, 4, 5, 6, 0, time.UTC)
	sourceModified := time.Date(2020, 3, 4, 5, 6, 7, 0, time.UTC)
	existing := KnowledgeObject{
		SourceRevision:       revision,
		SourceCreatedAt:      &sourceCreated,
		SourceModifiedAt:     &sourceModified,
		RecencyAt:            sourceModified,
		RecencyBasis:         AbsoluteTimeBasisSourceFilesystemMtime,
		AbsoluteTimeMetadata: mustJSON(t, map[string]any{"selected": "existing"}),
		Metadata:             mustJSON(t, map[string]any{}),
	}
	nextObserved := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	next := KnowledgeObject{
		SourceRevision:       revision,
		RecencyAt:            nextObserved,
		RecencyBasis:         AbsoluteTimeBasisObservedAtFallback,
		AbsoluteTimeMetadata: mustJSON(t, map[string]any{"selected": "new"}),
		Metadata:             mustJSON(t, map[string]any{}),
	}

	preserved := preserveKnowledgeObjectProcessing(existing, next)

	if preserved.SourceCreatedAt == nil || !preserved.SourceCreatedAt.Equal(sourceCreated) ||
		preserved.SourceModifiedAt == nil || !preserved.SourceModifiedAt.Equal(sourceModified) ||
		!preserved.RecencyAt.Equal(sourceModified) || preserved.RecencyBasis != AbsoluteTimeBasisSourceFilesystemMtime {
		t.Fatalf("absolute time = %#v, want existing revision chronology", preserved)
	}
	if string(preserved.AbsoluteTimeMetadata) != string(existing.AbsoluteTimeMetadata) {
		t.Fatalf("absolute_time_metadata = %s, want %s", preserved.AbsoluteTimeMetadata, existing.AbsoluteTimeMetadata)
	}
}

func TestPreserveKnowledgeObjectProcessingDoesNotKeepStaleMetadataDirectory(t *testing.T) {
	revision := "sha256:" + strings.Repeat("e", 64)
	existing := KnowledgeObject{
		SourceRevision:  revision,
		FileClass:       storagecatalog.FileClassDirectory,
		ProcessingState: ProcessingStateStale,
		PipelineKey:     KnowledgeObjectPipelineMetadata,
		PipelineVersion: KnowledgeObjectPipelineVersionV08,
		Metadata:        mustJSON(t, map[string]any{"origin": "old", "storage_entry": map[string]any{"availability_state": storagecatalog.AvailabilityStateSuperseded}}),
	}
	next := KnowledgeObject{
		SourceRevision:  revision,
		FileClass:       storagecatalog.FileClassDirectory,
		ProcessingState: ProcessingStateMetadataOnly,
		PipelineKey:     KnowledgeObjectPipelineMetadata,
		PipelineVersion: KnowledgeObjectPipelineVersionV08,
		Metadata:        mustJSON(t, map[string]any{"origin": KnowledgeObjectOriginStorageCatalog, "storage_entry": map[string]any{"availability_state": storagecatalog.AvailabilityStateAvailable}}),
	}

	preserved := preserveKnowledgeObjectProcessing(existing, next)

	if preserved.ProcessingState != ProcessingStateMetadataOnly || preserved.PipelineKey != KnowledgeObjectPipelineMetadata {
		t.Fatalf("processing = %s/%s, want current metadata-only directory", preserved.ProcessingState, preserved.PipelineKey)
	}
	if metadataString(t, preserved.Metadata, "origin") != KnowledgeObjectOriginStorageCatalog {
		t.Fatalf("metadata = %s, want current metadata", preserved.Metadata)
	}
}

func TestPreserveKnowledgeObjectProcessingKeepsProcessedNonDirectoryMetadataCandidate(t *testing.T) {
	revision := "sha256:" + strings.Repeat("a", 64)
	processedAt := time.Date(2026, 7, 4, 11, 30, 0, 0, time.UTC)
	existing := KnowledgeObject{
		SourceRevision:  revision,
		FileClass:       storagecatalog.FileClassPDF,
		ProcessingState: ProcessingStateChunked,
		PipelineKey:     KnowledgeObjectPipelineNotesFileExtraction,
		PipelineVersion: KnowledgeFileExtractionPipelineVersion,
		LastProcessedAt: &processedAt,
		Metadata:        mustJSON(t, map[string]any{"text_pipeline": map[string]any{"extractor_key": "pdf_text"}}),
	}
	next := KnowledgeObject{
		SourceRevision:  revision,
		FileClass:       storagecatalog.FileClassPDF,
		ProcessingState: ProcessingStateMetadataOnly,
		PipelineKey:     KnowledgeObjectPipelineMetadata,
		PipelineVersion: KnowledgeObjectPipelineVersionV08,
		Metadata:        mustJSON(t, map[string]any{"origin": KnowledgeObjectOriginStorageCatalog}),
	}

	preserved := preserveKnowledgeObjectProcessing(existing, next)

	if preserved.ProcessingState != ProcessingStateChunked || preserved.PipelineKey != KnowledgeObjectPipelineNotesFileExtraction {
		t.Fatalf("processing = %s/%s, want processed PDF state preserved", preserved.ProcessingState, preserved.PipelineKey)
	}
	if preserved.LastProcessedAt == nil || !preserved.LastProcessedAt.Equal(processedAt) {
		t.Fatalf("last_processed_at = %v, want %s", preserved.LastProcessedAt, processedAt)
	}
}

func TestPreserveKnowledgeObjectProcessingKeepsQueuedTextReprocessState(t *testing.T) {
	revision := "sha256:" + strings.Repeat("f", 64)
	existing := KnowledgeObject{
		SourceRevision:  revision,
		FileClass:       storagecatalog.FileClassMarkdown,
		ProcessingState: ProcessingStateStale,
		PipelineKey:     KnowledgeObjectPipelineNotesFileExtraction,
		PipelineVersion: KnowledgeFileExtractionPipelineVersion,
		Metadata:        mustJSON(t, map[string]any{"text_pipeline": map[string]any{"chunk_count": 1}}),
	}
	next := KnowledgeObject{
		SourceRevision:  revision,
		FileClass:       storagecatalog.FileClassMarkdown,
		ProcessingState: ProcessingStateMetadataOnly,
		PipelineKey:     KnowledgeObjectPipelineTextReady,
		PipelineVersion: KnowledgeObjectPipelineVersionV08,
		Metadata:        mustJSON(t, map[string]any{"origin": KnowledgeObjectOriginStorageCatalog}),
	}

	preserved := preserveKnowledgeObjectProcessing(existing, next)

	if preserved.ProcessingState != ProcessingStateStale || preserved.PipelineKey != KnowledgeObjectPipelineNotesFileExtraction {
		t.Fatalf("processing = %s/%s, want queued stale text reprocess state", preserved.ProcessingState, preserved.PipelineKey)
	}
}

func TestReconcileKnowledgeObjectsDryRun(t *testing.T) {
	service := NewService(nil)
	root := SourceRoot{
		NotesSourceRootID: ids.NewNotesSourceRootID(),
		RootKind:          RootKindBoxNotes,
		NodeKey:           "main",
		BackendRootKey:    "loom_box__notes",
		Status:            SourceRootStatusActive,
	}

	result, err := service.ReconcileKnowledgeObjects(t.Context(), KnowledgeObjectReconcileInput{
		DryRun:      true,
		SourceRoots: []SourceRoot{root},
		StorageEntries: []storagecatalog.Entry{{
			StorageEntryID:     ids.NewStorageEntryID(),
			SourceArea:         storagecatalog.SourceAreaNotes,
			OriginNodeKey:      "main",
			WatchedRootKey:     "loom_box__notes",
			LogicalPath:        "daily.txt",
			OriginalSourcePath: "daily.txt",
			FileClass:          storagecatalog.FileClassText,
			AvailabilityState:  storagecatalog.AvailabilityStateAvailable,
		}},
	})
	if err != nil {
		t.Fatalf("ReconcileKnowledgeObjects dry run returned error: %v", err)
	}
	if !result.DryRun || result.Applied != 0 {
		t.Fatalf("dry run result = %#v, want dry_run true and applied 0", result)
	}
	if len(result.Candidates) != 1 || len(result.KnowledgeObjects) != 1 {
		t.Fatalf("dry run candidates/objects = %d/%d, want 1/1", len(result.Candidates), len(result.KnowledgeObjects))
	}
}

func TestReconcileKnowledgeObjectsWithoutStoreFailsWhenApplying(t *testing.T) {
	service := NewService(nil)
	root := SourceRoot{
		NotesSourceRootID: ids.NewNotesSourceRootID(),
		RootKind:          RootKindBoxNotes,
		NodeKey:           "main",
		BackendRootKey:    "loom_box__notes",
		Status:            SourceRootStatusActive,
	}

	_, err := service.ReconcileKnowledgeObjects(t.Context(), KnowledgeObjectReconcileInput{
		SourceRoots: []SourceRoot{root},
		StorageEntries: []storagecatalog.Entry{{
			StorageEntryID:     ids.NewStorageEntryID(),
			SourceArea:         storagecatalog.SourceAreaNotes,
			OriginNodeKey:      "main",
			WatchedRootKey:     "loom_box__notes",
			LogicalPath:        "daily.txt",
			OriginalSourcePath: "daily.txt",
			FileClass:          storagecatalog.FileClassText,
			AvailabilityState:  storagecatalog.AvailabilityStateAvailable,
		}},
	})
	if err == nil {
		t.Fatal("ReconcileKnowledgeObjects apply returned nil error without configured store")
	}
}

func metadataBool(t *testing.T, raw json.RawMessage, key string) bool {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	value, _ := decoded[key].(bool)
	return value
}

func metadataString(t *testing.T, raw json.RawMessage, key string) string {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	value, _ := decoded[key].(string)
	return value
}
