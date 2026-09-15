package knowledge

import (
	"errors"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/storagecatalog"
)

func TestKnowledgeIDPrefixes(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		id     string
	}{
		{"source root", ids.NotesSourceRootPrefix, ids.NewNotesSourceRootID()},
		{"object", ids.KnowledgeObjectPrefix, ids.NewKnowledgeObjectID()},
		{"version", ids.KnowledgeObjectVersionPrefix, ids.NewKnowledgeObjectVersionID()},
		{"chunk", ids.KnowledgeChunkPrefix, ids.NewKnowledgeChunkID()},
		{"pipeline status", ids.KnowledgePipelineStatusPrefix, ids.NewKnowledgePipelineStatusID()},
		{"object link", ids.KnowledgeObjectLinkPrefix, ids.NewKnowledgeObjectLinkID()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ids.Validate(tt.prefix, tt.id); err != nil {
				t.Fatalf("Validate(%q, %q) returned error: %v", tt.prefix, tt.id, err)
			}
		})
	}
}

func TestKnowledgeEnumPredicates(t *testing.T) {
	if !IsRootKind(RootKindBoxNotes) || !IsRootKind(RootKindProjectNotes) {
		t.Fatal("expected notes source root kinds to be valid")
	}
	if IsRootKind("documents") {
		t.Fatal("documents must not be a knowledge root kind in v0.8 slice 1")
	}
	if !IsProcessingState(ProcessingStateMetadataOnly) || !IsProcessingState(ProcessingStateIndexed) {
		t.Fatal("expected core processing states to be valid")
	}
	if !IsPipelineStage(PipelineStageBM25) {
		t.Fatal("expected BM25 pipeline stage to be valid")
	}
	if !IsPipelineStage(PipelineStageEmbedding) {
		t.Fatal("expected embedding pipeline stage to be valid")
	}
	if !IsPipelineStatus(PipelineStatusSkippedUnsupported) {
		t.Fatal("expected skipped unsupported pipeline status to be valid")
	}
	if !IsLinkKind(LinkKindWikilink) {
		t.Fatal("expected wikilink link kind to be valid")
	}
	if !IsEmbeddingRuntime(EmbeddingRuntimeOllama) {
		t.Fatal("expected Ollama embedding runtime to be valid")
	}
	if !IsEmbeddingDistance(EmbeddingDistanceCosine) {
		t.Fatal("expected cosine embedding distance to be valid")
	}
	if !IsEmbeddingObjectStatus(EmbeddingObjectStatusQueued) {
		t.Fatal("expected queued embedding object status to be valid")
	}
	if !IsChunkEmbeddingStatus(ChunkEmbeddingStatusReusable) {
		t.Fatal("expected reusable chunk embedding status to be valid")
	}
	if !IsEmbeddingWorkStatus(EmbeddingWorkStatusProcessing) {
		t.Fatal("expected processing embedding work status to be valid")
	}
	if !IsNotesSearchMode(NotesSearchModeHybrid) {
		t.Fatal("expected hybrid notes search mode to be valid")
	}
	if IsNotesSearchMode("tags") {
		t.Fatal("ai-generated tag search mode is out of scope for v0.8.6")
	}
}

func TestEmbeddingDefaults(t *testing.T) {
	if EmbeddingSettingsID != "notes_embeddings" {
		t.Fatalf("EmbeddingSettingsID = %q, want notes_embeddings", EmbeddingSettingsID)
	}
	if EmbeddingModelMXBAIEmbedLarge != "mxbai-embed-large" {
		t.Fatalf("EmbeddingModelMXBAIEmbedLarge = %q, want mxbai-embed-large", EmbeddingModelMXBAIEmbedLarge)
	}
	if DefaultEmbeddingDimensions != 1024 {
		t.Fatalf("DefaultEmbeddingDimensions = %d, want 1024", DefaultEmbeddingDimensions)
	}
	if DefaultEmbeddingQuietWindowSec != 600 {
		t.Fatalf("DefaultEmbeddingQuietWindowSec = %d, want 600", DefaultEmbeddingQuietWindowSec)
	}
	if DefaultEmbeddingConcurrency != 1 {
		t.Fatalf("DefaultEmbeddingConcurrency = %d, want 1", DefaultEmbeddingConcurrency)
	}
	if DefaultEmbeddingHistoryPerLineage != 5 {
		t.Fatalf("DefaultEmbeddingHistoryPerLineage = %d, want 5", DefaultEmbeddingHistoryPerLineage)
	}
}

func TestValidateSourceRoot(t *testing.T) {
	projectID := ids.NewProjectID()
	root := SourceRoot{
		NotesSourceRootID:     ids.NewNotesSourceRootID(),
		RootKind:              RootKindProjectNotes,
		NodeKey:               "main",
		ProjectID:             &projectID,
		BackendRootKey:        "project:osint-tools:notes",
		Status:                SourceRootStatusActive,
		Metadata:              emptyJSONObject,
		AuthorizationMetadata: emptyJSONObject,
	}

	if err := ValidateSourceRoot(root); err != nil {
		t.Fatalf("ValidateSourceRoot returned error: %v", err)
	}

	root.ProjectID = nil
	if err := ValidateSourceRoot(root); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ValidateSourceRoot without project_id error = %v, want ErrInvalid", err)
	}
}

func TestExpandedSourceRootOwnershipValidation(t *testing.T) {
	projectID, boxID, projectRegistration := ids.NewProjectID(), ids.NewBoxWatchRootRegistrationID(), ids.NewProjectWatchedRootRegistrationID()
	for _, kind := range []string{RootKindBoxTopics, RootKindBoxLibrary, RootKindProjectMaterial} {
		root := SourceRoot{NotesSourceRootID: ids.NewNotesSourceRootID(), RootKind: kind, NodeKey: "main", BackendRootKey: "declared", Status: SourceRootStatusActive, Metadata: emptyJSONObject, AuthorizationMetadata: emptyJSONObject}
		if kind == RootKindProjectMaterial {
			root.ProjectID = &projectID
		}
		if err := ValidateSourceRoot(root); err != nil {
			t.Fatal(err)
		}
		if kind == RootKindProjectMaterial {
			root.BoxWatchRootRegistrationID = &boxID
		} else {
			root.ProjectWatchedRootRegistrationID = &projectRegistration
		}
		if err := ValidateSourceRoot(root); err == nil {
			t.Fatalf("accepted cross-owner registration: %s", kind)
		}
	}
}

func TestValidateKnowledgeObject(t *testing.T) {
	recencyAt := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	object := KnowledgeObject{
		KnowledgeObjectID:    ids.NewKnowledgeObjectID(),
		NotesSourceRootID:    ids.NewNotesSourceRootID(),
		RelativePath:         "research/index.md",
		FileClass:            storagecatalog.FileClassMarkdown,
		SourceHash:           "sha256:" + strings.Repeat("a", 64),
		ProcessingState:      ProcessingStateIndexed,
		RecencyAt:            recencyAt,
		RecencyBasis:         AbsoluteTimeBasisObservedAtFallback,
		AbsoluteTimeMetadata: emptyJSONObject,
		Metadata:             emptyJSONObject,
	}

	if err := ValidateKnowledgeObject(object); err != nil {
		t.Fatalf("ValidateKnowledgeObject returned error: %v", err)
	}

	object.SourceHash = "not-a-hash"
	if err := ValidateKnowledgeObject(object); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ValidateKnowledgeObject with invalid hash error = %v, want ErrInvalid", err)
	}
}

func TestValidateKnowledgeChunk(t *testing.T) {
	start := 10
	end := 20
	chunk := KnowledgeChunk{
		KnowledgeChunkID:  ids.NewKnowledgeChunkID(),
		KnowledgeObjectID: ids.NewKnowledgeObjectID(),
		ChunkIndex:        1,
		ChunkText:         "example note chunk",
		ChunkHash:         "sha256:" + strings.Repeat("b", 64),
		StartOffset:       &start,
		EndOffset:         &end,
		ChunkerVersion:    "markdown-v1",
		Status:            ChunkStatusCreated,
		Metadata:          emptyJSONObject,
	}

	if err := ValidateKnowledgeChunk(chunk); err != nil {
		t.Fatalf("ValidateKnowledgeChunk returned error: %v", err)
	}

	invalidEnd := 5
	chunk.EndOffset = &invalidEnd
	if err := ValidateKnowledgeChunk(chunk); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ValidateKnowledgeChunk with invalid offsets error = %v, want ErrInvalid", err)
	}
}
