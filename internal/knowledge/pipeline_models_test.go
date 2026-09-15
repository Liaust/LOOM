package knowledge

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/ids"
)

func TestValidatePipelineModels(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	run := PipelineRun{
		KnowledgePipelineRunID: ids.NewKnowledgePipelineRunID(), KnowledgeObjectID: ids.NewKnowledgeObjectID(),
		KnowledgeObjectVersionID: ids.NewKnowledgeObjectVersionID(), PipelineDefinitionKey: "notes_markdown",
		PipelineDefinitionVersion: "v1", Generation: 1, Status: FilePipelineStatusQueued,
		CurrentStageKey: FilePipelineStageMetadata, CurrentExecutionClass: PipelineExecutionCoordinator,
		Priority: 100, SourceHash: "sha256:" + strings.Repeat("a", 64),
		PlanSnapshot: json.RawMessage(`{"stages":[]}`), ResourceTotals: json.RawMessage(`{}`), Metadata: json.RawMessage(`{}`),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := ValidatePipelineRun(run); err != nil {
		t.Fatalf("ValidatePipelineRun returned error: %v", err)
	}

	stage := PipelineStageRun{
		KnowledgePipelineStageRunID: ids.NewKnowledgePipelineStageRunID(), KnowledgePipelineRunID: run.KnowledgePipelineRunID,
		StageKey: FilePipelineStageMetadata, StageContractVersion: "v1", Ordinal: 1,
		DependencySnapshot: json.RawMessage(`[]`), ExecutionClass: PipelineExecutionCoordinator,
		Status: PipelineStageStatusReady, ResourceRequest: json.RawMessage(`{}`), ResourceUsage: json.RawMessage(`{}`),
		Warnings: json.RawMessage(`[]`), Error: json.RawMessage(`{}`), Metadata: json.RawMessage(`{}`), CreatedAt: now, UpdatedAt: now,
	}
	if err := ValidatePipelineStageRun(stage); err != nil {
		t.Fatalf("ValidatePipelineStageRun returned error: %v", err)
	}

	page := 1
	unit := PipelineStageUnit{
		KnowledgePipelineStageUnitID: ids.NewKnowledgePipelineStageUnitID(), KnowledgePipelineStageRunID: stage.KnowledgePipelineStageRunID,
		UnitKey: "page:1", PageNumber: &page, UnitInputHash: "sha256:" + strings.Repeat("b", 64), Status: PipelineStageStatusReady,
		ResourceUsage: json.RawMessage(`{}`), Warnings: json.RawMessage(`[]`), Error: json.RawMessage(`{}`), Metadata: json.RawMessage(`{}`),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := ValidatePipelineStageUnit(unit); err != nil {
		t.Fatalf("ValidatePipelineStageUnit returned error: %v", err)
	}

	text := "artifact text"
	artifact := DerivedArtifact{
		KnowledgeDerivedArtifactID: ids.NewKnowledgeDerivedArtifactID(), KnowledgeObjectID: run.KnowledgeObjectID,
		KnowledgeObjectVersionID: run.KnowledgeObjectVersionID, KnowledgePipelineRunID: run.KnowledgePipelineRunID,
		KnowledgePipelineStageRunID: stage.KnowledgePipelineStageRunID, Generation: 1, ArtifactKind: ArtifactKindEmbeddedText,
		SourceLocator: "document", TextContent: &text, ContentHash: "sha256:" + strings.Repeat("c", 64),
		InputHash: "sha256:" + strings.Repeat("d", 64), GeneratorKey: "text_extractor", GeneratorVersion: "v1",
		State: ArtifactStateActive, Active: true, RecencyAt: now, RecencyBasis: AbsoluteTimeBasisObservedAtFallback,
		Metadata: json.RawMessage(`{}`), CreatedAt: now,
	}
	if err := ValidateDerivedArtifact(artifact); err != nil {
		t.Fatalf("ValidateDerivedArtifact returned error: %v", err)
	}
}

func TestValidatePipelineModelsRejectInvalidState(t *testing.T) {
	run := PipelineRun{Status: "invented"}
	if err := ValidatePipelineRun(run); err == nil {
		t.Fatal("ValidatePipelineRun accepted malformed IDs and status")
	}

	text := "x"
	artifact := DerivedArtifact{TextContent: &text, PayloadRef: "payload:also-set"}
	if err := ValidateDerivedArtifact(artifact); err == nil {
		t.Fatal("ValidateDerivedArtifact accepted duplicate content channels")
	}
}
