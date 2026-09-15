package knowledge

import (
	"testing"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/storagecatalog"
)

func TestScanKnowledgeObjectAbsoluteTimeWithoutCreation(t *testing.T) {
	modified := time.Date(2017, 4, 5, 6, 7, 8, 0, time.UTC)
	observed := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	object, err := scanKnowledgeObject(fakePipelineStatusScanner{values: []any{
		ids.NewKnowledgeObjectID(), ids.NewNotesSourceRootID(), nil,
		nil, "main", nil, "/notes/old.md",
		"old.md", "old.md", storagecatalog.FileClassMarkdown, "text/markdown", nil,
		"", "revision-1", ProcessingStateMetadataOnly, KnowledgeObjectPipelineMetadata,
		KnowledgeObjectPipelineVersionV08, nil, modified, modified,
		AbsoluteTimeBasisSourceFilesystemMtime, `{"candidates":[]}`, observed,
		nil, "", "", `{}`, observed, observed, nil,
	}})
	if err != nil {
		t.Fatalf("scanKnowledgeObject: %v", err)
	}
	if object.SourceCreatedAt != nil {
		t.Fatalf("source_created_at = %s, want nil", object.SourceCreatedAt)
	}
	if object.SourceModifiedAt == nil || !object.SourceModifiedAt.Equal(modified) {
		t.Fatalf("source_modified_at = %v, want %s", object.SourceModifiedAt, modified)
	}
	if !object.RecencyAt.Equal(modified) || object.RecencyBasis != AbsoluteTimeBasisSourceFilesystemMtime {
		t.Fatalf("recency = %s/%s", object.RecencyAt, object.RecencyBasis)
	}
}

func TestScanKnowledgeObjectVersionAbsoluteTime(t *testing.T) {
	created := time.Date(2015, 1, 2, 3, 4, 5, 0, time.UTC)
	modified := time.Date(2016, 2, 3, 4, 5, 6, 0, time.UTC)
	observed := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	version, err := scanKnowledgeObjectVersion(fakePipelineStatusScanner{values: []any{
		ids.NewKnowledgeObjectVersionID(), ids.NewKnowledgeObjectID(), 1,
		nil, "", "revision-1", nil, "text/markdown",
		storagecatalog.FileClassMarkdown, "/notes/old.md", created, modified,
		modified, AbsoluteTimeBasisSourceFilesystemMtime, `{"candidates":[]}`,
		`{}`, observed, observed,
	}})
	if err != nil {
		t.Fatalf("scanKnowledgeObjectVersion: %v", err)
	}
	if version.SourceCreatedAt == nil || !version.SourceCreatedAt.Equal(created) ||
		version.SourceModifiedAt == nil || !version.SourceModifiedAt.Equal(modified) {
		t.Fatalf("source timestamps = %v/%v", version.SourceCreatedAt, version.SourceModifiedAt)
	}
	if !version.RecencyAt.Equal(modified) || version.RecencyBasis != AbsoluteTimeBasisSourceFilesystemMtime {
		t.Fatalf("recency = %s/%s", version.RecencyAt, version.RecencyBasis)
	}
}

func TestChangedRevisionBuildsVersionWithItsOwnSourceTime(t *testing.T) {
	service := NewService(nil)
	objectID := ids.NewKnowledgeObjectID()
	firstTime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	secondTime := time.Date(2021, 2, 3, 4, 5, 6, 0, time.UTC)
	first, err := service.prepareKnowledgeObjectVersionForObject(KnowledgeObject{
		KnowledgeObjectID:    objectID,
		SourceRevision:       "revision-1",
		FileClass:            storagecatalog.FileClassMarkdown,
		SourceModifiedAt:     &firstTime,
		RecencyAt:            firstTime,
		RecencyBasis:         AbsoluteTimeBasisSourceFilesystemMtime,
		AbsoluteTimeMetadata: mustJSON(t, map[string]any{"revision": 1}),
	}, 1, mustJSON(t, map[string]any{}))
	if err != nil {
		t.Fatalf("prepare first version: %v", err)
	}
	second, err := service.prepareKnowledgeObjectVersionForObject(KnowledgeObject{
		KnowledgeObjectID:    objectID,
		SourceRevision:       "revision-2",
		FileClass:            storagecatalog.FileClassMarkdown,
		SourceModifiedAt:     &secondTime,
		RecencyAt:            secondTime,
		RecencyBasis:         AbsoluteTimeBasisSourceFilesystemMtime,
		AbsoluteTimeMetadata: mustJSON(t, map[string]any{"revision": 2}),
	}, 2, mustJSON(t, map[string]any{}))
	if err != nil {
		t.Fatalf("prepare second version: %v", err)
	}
	if first.SourceRevision == second.SourceRevision || first.SourceModifiedAt == nil || second.SourceModifiedAt == nil ||
		!first.SourceModifiedAt.Equal(firstTime) || !second.SourceModifiedAt.Equal(secondTime) {
		t.Fatalf("version chronology = %#v / %#v", first, second)
	}
}
