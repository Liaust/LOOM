package knowledge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/storagecatalog"
)

func TestDeclarationSourcePolicyBoundToEvidence(t *testing.T) {
	a := declarationKnowledgeAnalysis(t)
	path := filepath.Join(a.Report.ProjectRoot, ".loom/project.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "category: notes}", "category: notes, refresh: {quiet_for: 10m}, processing: {embeddings: false}}", 1))
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	a = projectcontracts.Analyze(a.Report.ProjectRoot)
	if !a.Report.OK {
		t.Fatalf("analysis: %+v", a.Report.Diagnostics)
	}
	reg := declarationKnowledgeRegistration(t, a)
	if _, ok := declarationKnowledgeMetadata(reg.Metadata); !ok {
		t.Fatal("valid policy refused")
	}
	for _, mutation := range []string{"remove", "substitute", "extra"} {
		m := jsonObject(reg.Metadata)
		source := m["knowledge_source"].(map[string]any)
		switch mutation {
		case "remove":
			delete(source, "policy")
		case "substitute":
			source["policy"].(map[string]any)["refresh"].(map[string]any)["max_wait_seconds"] = 1
		case "extra":
			source["policy"].(map[string]any)["other"] = true
		}
		if _, ok := declarationKnowledgeMetadata(mustJSON(t, m)); ok {
			t.Fatalf("accepted %s", mutation)
		}
	}
}

func TestSourcePipelinePolicyOverrides(t *testing.T) {
	for _, tc := range []struct {
		name, policy, stage, reason string
		host, selected              bool
	}{
		{"source off", `{"processing":{"embeddings":false}}`, FilePipelineStageEmbedding, "disabled_by_source", true, false},
		{"cannot enable host", `{"processing":{"embeddings":true}}`, FilePipelineStageEmbedding, "disabled_by_host", false, false},
		{"inherit", `{"processing":{}}`, FilePipelineStageEmbedding, "", true, true},
		{"ocr off", `{"processing":{"ocr":"off"}}`, FilePipelineStagePDFOCR, "disabled_by_source", true, false},
		{"ocr auto", `{"processing":{"ocr":"auto"}}`, FilePipelineStagePDFOCR, "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			object := KnowledgeObject{FileClass: storagecatalog.FileClassPDF, RelativePath: "paper.pdf", Metadata: json.RawMessage(`{"source_root":{"knowledge_source":{"policy":` + tc.policy + `}}}`)}
			plan, err := CompilePipelinePlan(object, PipelinePolicy{EmbeddingsEnabled: tc.host, PDFOCREnabled: tc.host})
			if err != nil {
				t.Fatal(err)
			}
			if plan.SourcePolicy == nil || plan.EffectivePolicy == nil {
				t.Fatal("policy not inspectable")
			}
			for _, stage := range plan.Stages {
				if stage.StageKey == tc.stage && (stage.Selected != tc.selected || stage.SkipReason != tc.reason) {
					t.Fatalf("stage: %+v", stage)
				}
			}
		})
	}
	legacy, err := CompilePipelinePlan(KnowledgeObject{FileClass: storagecatalog.FileClassPDF}, DefaultPipelinePolicy())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := legacy.Snapshot()
	if strings.Contains(string(raw), "source_policy") || strings.Contains(string(raw), "effective_policy") {
		t.Fatal("legacy snapshot changed")
	}
}

func TestCollectionAttachmentProcessingPolicy(t *testing.T) {
	metadata := json.RawMessage(`{"source_root":{"knowledge_source":{"policy":{"processing":{"paths":["TreeOfLife/**"],"ocr":"off","image_descriptions":false,"embeddings":true,"embedding_file_types":["markdown","text"]}}}}}`)
	for _, tc := range []struct {
		path, class        string
		embed, ocr, vision bool
	}{
		{"TreeOfLife/note.md", storagecatalog.FileClassMarkdown, true, false, false},
		{"TreeOfLife/paper.pdf", storagecatalog.FileClassPDF, false, false, false},
		{"TreeOfLife/image.png", storagecatalog.FileClassImage, false, false, false},
		{"TreeOfLife/diagram.canvas", storagecatalog.FileClassText, false, false, false},
		{"TreeOfLife/recording.wav", storagecatalog.FileClassAudio, false, false, false},
		{"Other/paper.pdf", storagecatalog.FileClassPDF, true, true, true},
	} {
		t.Run(tc.path, func(t *testing.T) {
			object := KnowledgeObject{RelativePath: tc.path, FileClass: tc.class, Metadata: metadata}
			source, err := knowledgeSourcePolicy(object)
			if err != nil {
				t.Fatal(err)
			}
			effective := effectiveSourcePipelinePolicy(PipelinePolicy{EmbeddingsEnabled: true, PDFOCREnabled: true, ImageOCREnabled: true, ImageDescriptionsEnabled: true}, source)
			if effective.EmbeddingsEnabled != tc.embed || effective.PDFOCREnabled != tc.ocr || effective.ImageOCREnabled != tc.ocr || effective.ImageDescriptionsEnabled != tc.vision {
				t.Fatalf("wrong effective policy: %+v", effective)
			}
			plan, err := CompilePipelinePlan(object, PipelinePolicy{EmbeddingsEnabled: true, PDFOCREnabled: true, ImageOCREnabled: true, ImageDescriptionsEnabled: true})
			if err != nil {
				t.Fatal(err)
			}
			for _, stage := range plan.Stages {
				if stage.StageKey == FilePipelineStageEmbedding && stage.Selected != tc.embed {
					t.Fatalf("unexpected embedding stage: %+v", stage)
				}
			}
		})
	}
}
