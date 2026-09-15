package knowledge

import (
	"testing"
	"time"

	"loom.local/loom/internal/storagecatalog"
)

func TestConsolidatedArtifactTextOrdersLocatorsAndOmitsMetadata(t *testing.T) {
	second, first, metadata := "second", "first", "title"
	got := consolidatedArtifactText([]DerivedArtifact{
		{ArtifactKind: ArtifactKindEmbeddedText, SourceLocator: "page:2", TextContent: &second},
		{ArtifactKind: ArtifactKindMetadataText, SourceLocator: "document", TextContent: &metadata},
		{ArtifactKind: ArtifactKindOCRText, SourceLocator: "page:1", TextContent: &first},
	})
	if got != "first\n\nsecond" {
		t.Fatalf("consolidated text = %q", got)
	}
}

func TestWaitingPipelineStatusSeparatesCoordinatorAndHeavy(t *testing.T) {
	if got := waitingPipelineStatus(PipelineExecutionCoordinator, false); got != FilePipelineStatusWaitingCoordinator {
		t.Fatalf("coordinator status = %q", got)
	}
	if got := waitingPipelineStatus(PipelineExecutionHeavy, false); got != FilePipelineStatusWaitingHeavy {
		t.Fatalf("heavy status = %q", got)
	}
	if got := waitingPipelineStatus(PipelineExecutionHeavy, true); got != FilePipelineStatusWaitingQuietWindow {
		t.Fatalf("quiet status = %q", got)
	}
}

func TestPipelineQuietWindowUsesSourceChangeAndSelectedHeavyStages(t *testing.T) {
	changedAt := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	plan, err := CompilePipelinePlan(KnowledgeObject{FileClass: storagecatalog.FileClassPDF, RelativePath: "scan.pdf"}, DefaultPipelinePolicy())
	if err != nil {
		t.Fatal(err)
	}
	eligibleAt, ok := pipelineQuietWindowEligibleAt(KnowledgeObject{UpdatedAt: changedAt}, plan, changedAt)
	if !ok || !eligibleAt.Equal(changedAt.Add(10*time.Minute)) {
		t.Fatalf("eligibleAt=%s ok=%t", eligibleAt, ok)
	}
	metadataPlan, err := CompilePipelinePlan(KnowledgeObject{FileClass: storagecatalog.FileClassUnknown, RelativePath: "item.bin"}, DefaultPipelinePolicy())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pipelineQuietWindowEligibleAt(KnowledgeObject{UpdatedAt: changedAt}, metadataPlan, changedAt); ok {
		t.Fatal("metadata-only plan should not receive a heavy quiet window")
	}
}
