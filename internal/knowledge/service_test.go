package knowledge

import (
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/storagecatalog"
)

func TestServicePrepareSourceRootDefaults(t *testing.T) {
	fixed := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	service := NewService(nil, WithClock(func() time.Time { return fixed }))

	root, err := service.PrepareSourceRoot(SourceRoot{
		RootKind:       RootKindBoxNotes,
		NodeKey:        "main",
		BackendRootKey: "box:main:notes",
	})
	if err != nil {
		t.Fatalf("PrepareSourceRoot returned error: %v", err)
	}

	if err := ids.Validate(ids.NotesSourceRootPrefix, root.NotesSourceRootID); err != nil {
		t.Fatalf("source root id was not generated: %v", err)
	}
	if root.Status != SourceRootStatusActive {
		t.Fatalf("status = %q, want %q", root.Status, SourceRootStatusActive)
	}
	if !root.CreatedAt.Equal(fixed) || !root.UpdatedAt.Equal(fixed) {
		t.Fatalf("timestamps = %s/%s, want %s", root.CreatedAt, root.UpdatedAt, fixed)
	}
	if string(root.Metadata) != "{}" || string(root.AuthorizationMetadata) != "{}" {
		t.Fatalf("metadata defaults = %s/%s, want empty JSON objects", root.Metadata, root.AuthorizationMetadata)
	}
}

func TestServicePrepareKnowledgeObjectDefaults(t *testing.T) {
	fixed := time.Date(2026, 7, 3, 12, 30, 0, 0, time.UTC)
	service := NewService(nil, WithClock(func() time.Time { return fixed }))

	object, err := service.PrepareKnowledgeObject(KnowledgeObject{
		NotesSourceRootID: ids.NewNotesSourceRootID(),
		RelativePath:      "daily.md",
	})
	if err != nil {
		t.Fatalf("PrepareKnowledgeObject returned error: %v", err)
	}

	if err := ids.Validate(ids.KnowledgeObjectPrefix, object.KnowledgeObjectID); err != nil {
		t.Fatalf("knowledge object id was not generated: %v", err)
	}
	if object.FileClass != storagecatalog.FileClassUnknown {
		t.Fatalf("file class = %q, want %q", object.FileClass, storagecatalog.FileClassUnknown)
	}
	if object.ProcessingState != ProcessingStateMetadataOnly {
		t.Fatalf("processing state = %q, want %q", object.ProcessingState, ProcessingStateMetadataOnly)
	}
	if !object.LastSeenAt.Equal(fixed) || !object.CreatedAt.Equal(fixed) || !object.UpdatedAt.Equal(fixed) {
		t.Fatalf("timestamps were not defaulted to fixed clock")
	}
}

func TestServicePrepareKnowledgeChunkRequiresHash(t *testing.T) {
	service := NewService(nil)

	_, err := service.PrepareKnowledgeChunk(KnowledgeChunk{
		KnowledgeObjectID: ids.NewKnowledgeObjectID(),
		ChunkIndex:        1,
		ChunkText:         "missing hash",
		ChunkerVersion:    "markdown-v1",
	})
	if err == nil {
		t.Fatal("PrepareKnowledgeChunk returned nil error, want validation failure")
	}
}

func TestServicePreparePipelineStatusAndLink(t *testing.T) {
	service := NewService(nil)
	objectID := ids.NewKnowledgeObjectID()

	status, err := service.PreparePipelineStatus(PipelineStatus{
		KnowledgeObjectID: objectID,
		PipelineKey:       "notes-markdown",
		PipelineVersion:   "v1",
		Stage:             PipelineStageBM25,
	})
	if err != nil {
		t.Fatalf("PreparePipelineStatus returned error: %v", err)
	}
	if status.Status != PipelineStatusNotStarted {
		t.Fatalf("status = %q, want %q", status.Status, PipelineStatusNotStarted)
	}

	link, err := service.PrepareObjectLink(ObjectLink{
		SourceKnowledgeObjectID: objectID,
		RawTarget:               "[[daily.md]]",
	})
	if err != nil {
		t.Fatalf("PrepareObjectLink returned error: %v", err)
	}
	if link.LinkKind != LinkKindUnknown || link.Status != LinkStatusUnresolved {
		t.Fatalf("link defaults = %q/%q, want %q/%q", link.LinkKind, link.Status, LinkKindUnknown, LinkStatusUnresolved)
	}
}

func TestServicePrepareKnowledgeObjectVersion(t *testing.T) {
	service := NewService(nil)
	size := int64(128)

	version, err := service.PrepareKnowledgeObjectVersion(KnowledgeObjectVersion{
		KnowledgeObjectID: ids.NewKnowledgeObjectID(),
		VersionNumber:     1,
		SourceHash:        "sha256:" + strings.Repeat("c", 64),
		SizeBytes:         &size,
	})
	if err != nil {
		t.Fatalf("PrepareKnowledgeObjectVersion returned error: %v", err)
	}
	if err := ids.Validate(ids.KnowledgeObjectVersionPrefix, version.KnowledgeObjectVersionID); err != nil {
		t.Fatalf("object version id was not generated: %v", err)
	}
	if version.FileClass != storagecatalog.FileClassUnknown {
		t.Fatalf("file class = %q, want %q", version.FileClass, storagecatalog.FileClassUnknown)
	}
	if version.RecencyBasis != AbsoluteTimeBasisObservedAtFallback || version.RecencyAt.IsZero() {
		t.Fatalf("absolute time fallback = %s/%s, want observed fallback", version.RecencyAt, version.RecencyBasis)
	}
}

func TestServicePrepareKnowledgeObjectResolvesStorageSourceTime(t *testing.T) {
	fixed := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	service := NewService(nil, WithClock(func() time.Time { return fixed }))
	object, err := service.PrepareKnowledgeObject(KnowledgeObject{
		NotesSourceRootID: ids.NewNotesSourceRootID(),
		RelativePath:      "history/old.md",
		Metadata: mustJSON(t, map[string]any{
			"storage_metadata": map[string]any{
				"source_modified_at":    "2018-01-02T03:04:05Z",
				"source_modified_basis": AbsoluteTimeBasisSourceFilesystemMtime,
			},
		}),
	})
	if err != nil {
		t.Fatalf("PrepareKnowledgeObject returned error: %v", err)
	}
	if object.SourceModifiedAt == nil || object.SourceModifiedAt.Format(time.RFC3339) != "2018-01-02T03:04:05Z" {
		t.Fatalf("source_modified_at = %v", object.SourceModifiedAt)
	}
	if object.RecencyBasis != AbsoluteTimeBasisSourceFilesystemMtime || !object.RecencyAt.Equal(*object.SourceModifiedAt) {
		t.Fatalf("recency = %s/%s", object.RecencyAt, object.RecencyBasis)
	}
	if !strings.Contains(string(object.AbsoluteTimeMetadata), "2018-01-02T03:04:05Z") {
		t.Fatalf("absolute_time_metadata = %s, want candidate evidence", object.AbsoluteTimeMetadata)
	}
}

func TestServiceStore(t *testing.T) {
	service := NewService(nil)
	if service.Store().DB() != nil {
		t.Fatal("Store().DB() = non-nil, want nil")
	}
}
