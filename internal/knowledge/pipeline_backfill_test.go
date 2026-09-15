package knowledge

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"loom.local/loom/internal/storagecatalog"
)

func TestPipelineBackfillPlanIncludesEveryFileClass(t *testing.T) {
	cases := []struct {
		fileClass string
		extension string
		family    string
	}{
		{storagecatalog.FileClassMarkdown, ".md", "markdown"},
		{storagecatalog.FileClassText, ".txt", "text"},
		{storagecatalog.FileClassCode, ".go", "code"},
		{storagecatalog.FileClassCode, ".json", "structured"},
		{storagecatalog.FileClassOfficeDocument, ".docx", "docx"},
		{storagecatalog.FileClassPDF, ".pdf", "pdf"},
		{storagecatalog.FileClassImage, ".png", "image"},
		{storagecatalog.FileClassUnknown, ".bin", "metadata_only"},
	}
	for _, tc := range cases {
		t.Run(tc.fileClass+tc.extension, func(t *testing.T) {
			plan, err := CompilePipelinePlan(KnowledgeObject{FileClass: tc.fileClass, RelativePath: "fixture" + tc.extension}, DefaultPipelinePolicy())
			if err != nil {
				t.Fatal(err)
			}
			if plan.FileFamily != tc.family || len(plan.Stages) == 0 {
				t.Fatalf("plan=%#v, want family %q", plan, tc.family)
			}
		})
	}
}

func TestSummarizePipelineBackfillCandidatesIsStableAndIdempotent(t *testing.T) {
	candidates := []PipelineBackfillCandidate{
		{KnowledgeObjectID: "object_existing", FileClass: "pdf", Stages: []string{FilePipelineStageMetadata, FilePipelineStagePDFOCR}, ExistingPipeline: true, ReusableChunks: 2, ReusableLexicalDocuments: 2, ReusableVectors: 2},
		{KnowledgeObjectID: "object_new", FileClass: "image", Stages: []string{FilePipelineStageMetadata, FilePipelineStageImageDescription}, RequiresSourceReprocessing: true},
		{KnowledgeObjectID: "object_blocked", FileClass: "unknown", Stages: []string{FilePipelineStageMetadata}, BlockedReason: "invalid_knowledge_object"},
	}
	first := summarizePipelineBackfillCandidates(candidates)
	second := summarizePipelineBackfillCandidates(candidates)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated planning changed result:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if first.ObjectsToCreate != 1 || first.ExistingPipelines != 1 || first.RequiresSourceReprocessing != 1 || first.BlockedReasons["invalid_knowledge_object"] != 1 {
		t.Fatalf("unexpected summary: %#v", first)
	}
	if first.ReusableChunks != 2 || first.ReusableLexicalDocuments != 2 || first.ReusableVectors != 2 {
		t.Fatalf("reuse counts=%#v", first)
	}
}

func TestPipelineBackfillApplyRequiresConfirmationAndNoLegacyClaims(t *testing.T) {
	if err := validatePipelineBackfillApply(false, 0); err == nil || !strings.Contains(err.Error(), "confirmation") {
		t.Fatalf("confirmation error=%v", err)
	}
	if err := validatePipelineBackfillApply(true, 2); err == nil || !strings.Contains(err.Error(), "active legacy embedding claim") {
		t.Fatalf("claim refusal error=%v", err)
	}
	if err := validatePipelineBackfillApply(true, 0); err != nil {
		t.Fatalf("valid apply rejected: %v", err)
	}
}

func TestPipelineBackfillPlanJSONOmitsSourceContentAndPaths(t *testing.T) {
	candidate := PipelineBackfillCandidate{
		KnowledgeObjectID: "knowledge_object_safe",
		FileClass:         storagecatalog.FileClassPDF,
		Stages:            []string{FilePipelineStageMetadata, FilePipelineStagePDFOCR},
		object:            KnowledgeObject{SourcePath: "/secret/source.pdf", Metadata: json.RawMessage(`{"source_text":"do not leak"}`)},
	}
	payload, err := json.Marshal(summarizePipelineBackfillCandidates([]PipelineBackfillCandidate{candidate}))
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	if strings.Contains(text, "/secret/source.pdf") || strings.Contains(text, "do not leak") {
		t.Fatalf("backfill plan leaked source-bearing fields: %s", text)
	}
}

func TestPipelineAPIRedactsFailureMessagesButKeepsCodes(t *testing.T) {
	run := redactPipelineRun(PipelineRun{LastErrorCode: "ocr_failed", LastErrorMessage: "/secret/path contained source text"})
	if run.LastErrorCode != "ocr_failed" || run.LastErrorMessage != "" {
		t.Fatalf("redacted run=%#v", run)
	}
	redacted := string(redactPipelineStageError(json.RawMessage(`{"code":"ocr_failed","message":"secret OCR text","retryable":true}`)))
	if strings.Contains(redacted, "secret") || !strings.Contains(redacted, "ocr_failed") || !strings.Contains(redacted, "retryable") {
		t.Fatalf("redacted stage error=%s", redacted)
	}
}
