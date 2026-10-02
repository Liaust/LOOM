package knowledge

import (
	"encoding/json"
	"errors"
	"fmt"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/storagecatalog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func enrichmentTestObject(t *testing.T, class string) KnowledgeObject {
	t.Helper()
	return KnowledgeObject{KnowledgeObjectID: "object", NotesSourceRootID: "root", SourcePath: "/admitted/file", RelativePath: "file.pdf", FileClass: class, SourceRevision: "r1", SourceHash: "sha256:one", Metadata: json.RawMessage(`{"source_root":{"knowledge_source":{"policy":{"processing":{"ocr":"off","image_descriptions":false,"embeddings":false}}}}}`)}
}
func enrichmentTestIntent(t *testing.T, o KnowledgeObject, host PipelinePolicy, requested EnrichmentStages) PipelineEnrichment {
	t.Helper()
	b, e := enrichmentBinding(o)
	if e != nil {
		t.Fatal(e)
	}
	p, e := knowledgeSourcePolicy(o)
	if e != nil {
		t.Fatal(e)
	}
	return PipelineEnrichment{Binding: b, Requested: requested, HostPolicy: host, AutomaticPolicy: effectiveSourcePipelinePolicy(host, p), ActorID: "actor"}
}
func TestEnrichmentIndependentStagesAndDefaults(t *testing.T) {
	host := PipelinePolicy{PDFOCREnabled: true, ImageOCREnabled: true, ImageDescriptionsEnabled: true, EmbeddingsEnabled: true}
	for _, tc := range []struct {
		name, class string
		stages      EnrichmentStages
		selected    string
	}{
		{"OCR", storagecatalog.FileClassPDF, EnrichmentStages{OCR: true}, FilePipelineStagePDFOCR},
		{"vision", storagecatalog.FileClassImage, EnrichmentStages{Vision: true}, FilePipelineStageImageDescription},
		{"embedding", storagecatalog.FileClassPDF, EnrichmentStages{Embeddings: true}, FilePipelineStageEmbedding},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := enrichmentTestObject(t, tc.class)
			intent := enrichmentTestIntent(t, o, host, tc.stages)
			plan, e := compileEnrichmentPlan(o, host, intent)
			if e != nil {
				t.Fatal(e)
			}
			for _, stage := range plan.Stages {
				switch stage.StageKey {
				case FilePipelineStagePDFOCR, FilePipelineStageImageOCR, FilePipelineStageImageDescription, FilePipelineStageEmbedding:
					if stage.Selected != (stage.StageKey == tc.selected) {
						t.Fatalf("unexpected selected stage: %#v", stage)
					}
				}
			}
			auto, e := CompilePipelinePlan(o, host)
			if e != nil {
				t.Fatal(e)
			}
			if compiledPlanContainsSelectedStage(auto, tc.selected) {
				t.Fatal("automatic source defaults changed")
			}
			if plan.Enrichment.ActorID != "actor" || plan.Enrichment.AutomaticPolicy.EmbeddingsEnabled {
				t.Fatal("intent/default evidence missing")
			}
		})
	}
}
func TestEnrichmentRejectsChangedIdentityPolicyAndUnsupportedStages(t *testing.T) {
	host := PipelinePolicy{PDFOCREnabled: true, EmbeddingsEnabled: true}
	o := enrichmentTestObject(t, storagecatalog.FileClassPDF)
	intent := enrichmentTestIntent(t, o, host, EnrichmentStages{OCR: true})
	for _, change := range []func(*KnowledgeObject){func(o *KnowledgeObject) { o.SourceRevision = "r2" }, func(o *KnowledgeObject) { o.SourceHash = "sha256:two" }, func(o *KnowledgeObject) { o.NotesSourceRootID = "other" }, func(o *KnowledgeObject) { o.Metadata = json.RawMessage(`{}`) }} {
		changed := o
		change(&changed)
		if _, e := compileEnrichmentPlan(changed, host, intent); e == nil {
			t.Fatal("changed source accepted")
		}
	}
	disabled := host
	disabled.PDFOCREnabled = false
	if _, e := compileEnrichmentPlan(o, disabled, intent); e == nil {
		t.Fatal("host disable bypassed")
	}
	intent = enrichmentTestIntent(t, o, host, EnrichmentStages{Vision: true})
	if _, e := compileEnrichmentPlan(o, host, intent); e == nil {
		t.Fatal("PDF vision accepted")
	}
}

func TestEnrichmentReplayAndNewRevisionPostgres(t *testing.T) {
	db, url := boxSourcesDatabase(t)
	if _, e := migrations.Up(t.Context(), url, filepath.Join("..", "..", "migrations")); e != nil {
		t.Fatal(e)
	}
	host := PipelinePolicy{EmbeddingsEnabled: true}
	setPipelinePolicyForTest(t, db, host)
	tags := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"models":[{"name":"test:latest"}]}`) }))
	defer tags.Close()
	if _, e := db.Exec(`UPDATE knowledge.embedding_settings SET runtime_key='ollama',model_key='test:latest',ollama_url=$1 WHERE embedding_settings_id=$2`, tags.URL, EmbeddingSettingsID); e != nil {
		t.Fatal(e)
	}
	service, object := pipelineFixture(t, db, storagecatalog.FileClassMarkdown, "text/markdown", "hello", time.Now().UTC())
	binding, e := enrichmentBinding(object)
	if e != nil {
		t.Fatal(e)
	}
	results := make(chan PipelineRun, 2)
	failures := make(chan error, 2)
	for range 2 {
		go func() {
			run, e := service.EnrichPipelineObject(t.Context(), binding, EnrichmentStages{Embeddings: true}, "actor")
			results <- run
			failures <- e
		}()
	}
	a, b := <-results, <-results
	for range 2 {
		if e := <-failures; e != nil {
			t.Fatal(e)
		}
	}
	if a.KnowledgePipelineRunID != b.KnowledgePipelineRunID {
		t.Fatal("duplicate intent created distinct runs")
	}
	reconciled, e := service.EnsurePipelineRun(t.Context(), object, host, false, 100)
	if e != nil || reconciled.KnowledgePipelineRunID != a.KnowledgePipelineRunID {
		t.Fatalf("reconcile lost intent: %v", e)
	}
	if _, e = db.Exec(`UPDATE knowledge.knowledge_objects SET source_revision='revision-2' WHERE knowledge_object_id=$1`, object.KnowledgeObjectID); e != nil {
		t.Fatal(e)
	}
	if _, e = service.EnrichPipelineObject(t.Context(), binding, EnrichmentStages{Embeddings: true}, "actor"); !errors.Is(e, ErrConflict) {
		t.Fatalf("stale preview: %v", e)
	}
	next, e := service.EnsurePipelineRun(t.Context(), object, host, false, 100)
	if e != nil {
		t.Fatal(e)
	}
	var plan CompiledPipelinePlan
	if e = json.Unmarshal(next.PlanSnapshot, &plan); e != nil || plan.Enrichment != nil {
		t.Fatal("new revision inherited enrichment")
	}
}

func TestEnrichmentSelectionBoundsAndBindingValidation(t *testing.T) {
	for _, input := range []EnrichmentSelection{
		{Ref: "/", Kind: "folder"}, {Ref: "relative", Kind: "file"}, {Ref: "/source/../other", Kind: "folder"},
		{Ref: "object", Recursive: true}, {Ref: "/folder", Kind: "folder", Limit: 101}, {Ref: "/folder", Kind: "folder", SourceHash: "one"},
	} {
		if _, err := NormalizeEnrichmentSelection(input); err == nil {
			t.Fatalf("accepted invalid selector: %#v", input)
		}
	}
	normalized, err := NormalizeEnrichmentSelection(EnrichmentSelection{Ref: "/source", Kind: "folder"})
	if err != nil || normalized.Recursive || normalized.Limit != MaxEnrichmentSelection {
		t.Fatalf("bad default selection: %#v %v", normalized, err)
	}
	input := EnrichmentApplyInput{Confirm: true, Stages: EnrichmentStages{OCR: true}, Bindings: []EnrichmentBinding{{ObjectID: "object", SourceRevision: "r1", SourceHash: "hash", AdmissionHash: "admission"}}}
	if err = ValidateEnrichmentApply(input); err != nil {
		t.Fatal(err)
	}
	input.Bindings = append(input.Bindings, input.Bindings[0])
	if err = ValidateEnrichmentApply(input); err == nil {
		t.Fatal("duplicate object binding accepted")
	}
	input.Bindings = input.Bindings[:1]
	input.Bindings[0].SourceHash = ""
	if err = ValidateEnrichmentApply(input); err == nil {
		t.Fatal("unbound version accepted")
	}
}
func TestEnrichmentVisionUnavailableFailsInsteadOfWarning(t *testing.T) {
	snapshot, _ := json.Marshal(CompiledPipelinePlan{Enrichment: &PipelineEnrichment{Requested: EnrichmentStages{Vision: true}}})
	_, _, err := (ImageDescriptionStageHandler{}).Execute(t.Context(), PipelineWorkItem{Object: KnowledgeObject{FileClass: storagecatalog.FileClassImage}, Run: PipelineRun{PlanSnapshot: snapshot}}, DefaultHeavyResourcePolicy())
	if err == nil {
		t.Fatal("explicit unavailable vision reported success")
	}
}

func TestEnrichmentPreviewFolderIsBoundedAndApplyUsesManifestPostgres(t *testing.T) {
	db, url := boxSourcesDatabase(t)
	if _, err := migrations.Up(t.Context(), url, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	service, object := pipelineFixture(t, db, storagecatalog.FileClassMarkdown, "text/markdown", "one", time.Now().UTC())
	originalRoot := filepath.Dir(object.SourcePath)
	// Add admitted catalog records through the existing fixture/service owners.
	add := func(relative string) KnowledgeObject {
		t.Helper()
		o := object
		o.KnowledgeObjectID = ""
		o.RelativePath = relative
		o.SourcePath = filepath.Join(originalRoot, relative)
		o.SourceRevision = relative
		o.SourceHash = hashArtifactValue(relative)
		entry, err := storagecatalog.NewService(db).RegisterEntry(t.Context(), storagecatalog.RegisterEntryInput{StorageClass: storagecatalog.StorageClassObjectBlob, SourceArea: storagecatalog.SourceAreaNotes, OriginNodeKey: o.SourceNodeKey, WatchedRootKey: object.NotesSourceRootID, LogicalPath: relative, OriginalSourcePath: o.SourcePath, FileClass: o.FileClass, MimeType: o.MimeType, SizeBytes: o.SizeBytes, ChecksumAlgorithm: "sha256", ChecksumHex: strings.TrimPrefix(o.SourceHash, "sha256:"), AvailabilityState: storagecatalog.AvailabilityStateAvailable})
		if err != nil {
			t.Fatal(err)
		}
		o.StorageEntryID = &entry.StorageEntryID
		o, err = service.PrepareKnowledgeObject(o)
		if err != nil {
			t.Fatal(err)
		}
		o, err = service.store.UpsertKnowledgeObject(t.Context(), o)
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	add("nested/two.md")
	shallow, err := service.PreviewEnrichment(t.Context(), EnrichmentSelection{Ref: originalRoot, Kind: "folder", NodeKey: object.SourceNodeKey})
	if err != nil || len(shallow.Entries) != 1 {
		t.Fatalf("shallow: %#v %v", shallow, err)
	}
	deep, err := service.PreviewEnrichment(t.Context(), EnrichmentSelection{Ref: originalRoot, Kind: "folder", NodeKey: object.SourceNodeKey, Recursive: true, Limit: 1})
	if err != nil || len(deep.Entries) != 1 || !deep.Truncated || deep.NextAfter == "" {
		t.Fatalf("bounded recursive preview: %#v %v", deep, err)
	}
	next, err := service.PreviewEnrichment(t.Context(), EnrichmentSelection{Ref: originalRoot, Kind: "folder", NodeKey: object.SourceNodeKey, Recursive: true, After: deep.NextAfter, Limit: 1})
	if err != nil || len(next.Entries) != 1 || next.Entries[0].Binding == deep.Entries[0].Binding {
		t.Fatalf("continuation: %#v %v", next, err)
	}
	add("later.md")
	if _, err = db.Exec(`UPDATE knowledge.knowledge_objects SET source_revision='changed' WHERE knowledge_object_id=$1`, deep.Entries[0].Binding.ObjectID); err != nil {
		t.Fatal(err)
	}
	receipt, err := service.ApplyEnrichment(t.Context(), EnrichmentApplyInput{Confirm: true, Stages: EnrichmentStages{Embeddings: true}, Bindings: []EnrichmentBinding{deep.Entries[0].Binding}}, "actor")
	if err != nil || len(receipt.Results) != 1 || receipt.Failed != 1 || !strings.Contains(receipt.Results[0].Error, "stale") {
		t.Fatalf("changed manifest: %#v %v", receipt, err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM knowledge.pipeline_runs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("preview/stale apply queued work: %d %v", count, err)
	}
}

func TestEnrichmentPreservedEmbeddingsDoNotExecuteAgain(t *testing.T) {
	host := PipelinePolicy{PDFOCREnabled: true, EmbeddingsEnabled: true}
	object := enrichmentTestObject(t, storagecatalog.FileClassPDF)
	intent := enrichmentTestIntent(t, object, host, EnrichmentStages{OCR: true})
	intent.Preserved.Embeddings = true
	plan, err := compileEnrichmentPlan(object, host, intent)
	if err != nil {
		t.Fatal(err)
	}
	if compiledPlanContainsSelectedStage(plan, FilePipelineStageEmbedding) {
		t.Fatal("adding OCR enabled a source-disabled embedding stage")
	}
	if !compiledPlanContainsSelectedStage(plan, FilePipelineStagePDFOCR) {
		t.Fatal("requested OCR missing")
	}
}

func TestEnrichmentReusesCompletedEmptyPDFExtraction(t *testing.T) {
	for _, status := range []string{ExtractionStatusNoEmbeddedText, ExtractionStatusExtracted, "failed", ""} {
		raw, _ := json.Marshal(map[string]string{"extractor_key": ExtractorKeyPDF, "extraction_status": status})
		if completedEmptyPDFExtraction(raw) != (status == ExtractionStatusNoEmbeddedText) {
			t.Fatalf("incorrect empty PDF evidence for %q", status)
		}
	}
	if completedEmptyPDFExtraction(json.RawMessage(`{"extraction_status":"no_embedded_text"}`)) || completedEmptyPDFExtraction(nil) {
		t.Fatal("missing extractor evidence accepted")
	}
}

func TestEnrichmentReplayRequiresEmbeddingsForCurrentDerivedOutput(t *testing.T) {
	for _, tc := range []struct {
		name      string
		intent    PipelineEnrichment
		published bool
		want      bool
	}{
		{"preserved older text", PipelineEnrichment{Requested: EnrichmentStages{OCR: true}, Preserved: EnrichmentStages{Embeddings: true}}, false, false},
		{"preserved exact published output", PipelineEnrichment{Requested: EnrichmentStages{OCR: true}, Preserved: EnrichmentStages{Embeddings: true}}, true, true},
		{"new embeddings still queued or failed", PipelineEnrichment{Requested: EnrichmentStages{Embeddings: true}, Retained: EnrichmentStages{OCR: true}}, false, true},
		{"completed embeddings replay", PipelineEnrichment{Requested: EnrichmentStages{Embeddings: true}, Retained: EnrichmentStages{OCR: true}}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stages := enrichmentReplayStages(tc.intent, tc.published)
			if stages.contains(EnrichmentStages{Embeddings: true}) != tc.want {
				t.Fatalf("embedding replay eligibility: %#v", stages)
			}
			if !stages.OCR {
				t.Fatal("unrelated retained OCR permission lost")
			}
		})
	}
}

func TestEnrichmentReembedsNewOCRPublicationPostgres(t *testing.T) {
	db, url := boxSourcesDatabase(t)
	if _, err := migrations.Up(t.Context(), url, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	host := PipelinePolicy{ImageOCREnabled: true, EmbeddingsEnabled: true}
	setPipelinePolicyForTest(t, db, host)
	tags := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"models":[{"name":"mxbai-embed-large:latest"}]}`)
	}))
	defer tags.Close()
	if _, err := db.Exec(`UPDATE knowledge.embedding_settings SET ollama_url=$1 WHERE embedding_settings_id=$2`, tags.URL, EmbeddingSettingsID); err != nil {
		t.Fatal(err)
	}
	service, object := pipelineFixture(t, db, storagecatalog.FileClassImage, "image/png", "image bytes", time.Now().UTC())
	actualPolicy, err := service.GetPipelinePolicy(t.Context())
	if err != nil || actualPolicy.Policy != host {
		t.Fatalf("fixture host policy mismatch: got %#v, want %#v: %v", actualPolicy.Policy, host, err)
	}
	// The legacy catalog fixture admits this policy without introducing a new
	// declaration/engine test harness. Execution outputs below use existing helpers.
	object.Metadata = enrichmentTestObject(t, storagecatalog.FileClassImage).Metadata
	object, err = service.store.UpsertKnowledgeObject(t.Context(), object)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := enrichmentBinding(object)
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.EnrichPipelineObject(t.Context(), binding, EnrichmentStages{Embeddings: true}, "actor")
	if err != nil {
		t.Fatal(err)
	}
	completeRunForTest(t, db, first.KnowledgePipelineRunID)
	_, oldChunk := createPipelineChunkForTest(t, db, service, object, first, "earlier extracted text")
	settings, err := service.store.GetEmbeddingSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	vectorID := seedActiveChunkEmbeddingForTest(t, db, oldChunk, settings, first.Generation, "earlier extracted text")
	if _, err = db.Exec(`UPDATE knowledge.knowledge_objects SET lexical_version_id=$2,semantic_version_id=$2 WHERE knowledge_object_id=$1`, object.KnowledgeObjectID, first.KnowledgeObjectVersionID); err != nil {
		t.Fatal(err)
	}
	replay, err := service.EnrichPipelineObject(t.Context(), binding, EnrichmentStages{Embeddings: true}, "actor")
	if err != nil || replay.KnowledgePipelineRunID != first.KnowledgePipelineRunID {
		t.Fatalf("initial replay run=%s want=%s: %v", replay.KnowledgePipelineRunID, first.KnowledgePipelineRunID, err)
	}

	// Materialize a completed OCR-only publication through the real run/version
	// writer; no OCR process is needed to test the subsequent embedding request.
	intent := enrichmentTestIntent(t, object, host, EnrichmentStages{OCR: true})
	intent.Preserved.Embeddings = true
	plan, err := compileEnrichmentPlan(object, host, intent)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := plan.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	locked, err := lockCurrentPipelineObjectTx(t.Context(), tx, object.KnowledgeObjectID)
	if err != nil {
		t.Fatal(err)
	}
	ocr, _, err := service.ensureLockedPipelineRunTx(t.Context(), tx, locked, plan, snapshot, true, DefaultPipelinePriority, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if ocr.KnowledgeObjectVersionID == first.KnowledgeObjectVersionID {
		t.Fatal("OCR did not fork its output")
	}
	completeRunForTest(t, db, ocr.KnowledgePipelineRunID)
	version, newChunk := createPipelineChunkForTest(t, db, service, object, ocr, "new OCR-derived text")
	stages, err := service.store.ListPipelineStageRuns(t.Context(), ocr.KnowledgePipelineRunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range stages {
		var kind string
		switch stage.StageKey {
		case FilePipelineStageMetadata:
			kind = ArtifactKindMetadataText
		case FilePipelineStageImageOCR:
			kind = ArtifactKindOCRText
		case FilePipelineStageConsolidateText:
			kind = ArtifactKindConsolidatedText
		default:
			continue
		}
		artifact, err := service.PrepareDerivedArtifact(ocr, stage, version, DerivedArtifactInput{ArtifactKind: kind, SourceLocator: "document", Text: "new OCR-derived text", GeneratorKey: "test", GeneratorVersion: "v1"})
		if err != nil {
			t.Fatal(err)
		}
		artifact, err = service.store.CreateDerivedArtifact(t.Context(), artifact)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = service.store.ActivateDerivedArtifact(t.Context(), artifact.KnowledgeDerivedArtifactID, ocr.Generation); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`UPDATE knowledge.knowledge_objects SET lexical_version_id=$2 WHERE knowledge_object_id=$1`, object.KnowledgeObjectID, ocr.KnowledgeObjectVersionID); err != nil {
		t.Fatal(err)
	}
	next, err := service.EnrichPipelineObject(t.Context(), binding, EnrichmentStages{Embeddings: true}, "actor")
	if err != nil {
		t.Fatal(err)
	}
	if next.KnowledgePipelineRunID == ocr.KnowledgePipelineRunID || next.CurrentStageKey != FilePipelineStageEmbedding || next.KnowledgeObjectVersionID != ocr.KnowledgeObjectVersionID {
		t.Fatalf("embedding reuse run=%s stage=%s version=%s; prior OCR run=%s version=%s", next.KnowledgePipelineRunID, next.CurrentStageKey, next.KnowledgeObjectVersionID, ocr.KnowledgePipelineRunID, ocr.KnowledgeObjectVersionID)
	}
	var semantic, text string
	var active bool
	if err = db.QueryRow(`SELECT semantic_version_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1`, object.KnowledgeObjectID).Scan(&semantic); err != nil || semantic != first.KnowledgeObjectVersionID {
		t.Fatalf("prior publication withdrawn: %s %v", semantic, err)
	}
	if err = db.QueryRow(`SELECT active FROM knowledge.chunk_embeddings WHERE knowledge_chunk_embedding_id=$1`, vectorID).Scan(&active); err != nil || !active {
		t.Fatalf("prior vector withdrawn: %v %v", active, err)
	}
	if err = db.QueryRow(`SELECT chunk_text FROM knowledge.knowledge_chunks WHERE knowledge_chunk_id=$1`, newChunk.KnowledgeChunkID).Scan(&text); err != nil || text != "new OCR-derived text" {
		t.Fatalf("OCR chunks replaced: %q %v", text, err)
	}
	replay, err = service.EnrichPipelineObject(t.Context(), binding, EnrichmentStages{Embeddings: true}, "actor")
	if err != nil || replay.KnowledgePipelineRunID != next.KnowledgePipelineRunID {
		t.Fatalf("queued replay run=%s want=%s: %v", replay.KnowledgePipelineRunID, next.KnowledgePipelineRunID, err)
	}
	completeRunForTest(t, db, next.KnowledgePipelineRunID)
	if _, err = db.Exec(`UPDATE knowledge.knowledge_objects SET semantic_version_id=$2 WHERE knowledge_object_id=$1`, object.KnowledgeObjectID, next.KnowledgeObjectVersionID); err != nil {
		t.Fatal(err)
	}
	replay, err = service.EnrichPipelineObject(t.Context(), binding, EnrichmentStages{Embeddings: true}, "actor")
	if err != nil || replay.KnowledgePipelineRunID != next.KnowledgePipelineRunID {
		t.Fatalf("completed replay run=%s want=%s: %v", replay.KnowledgePipelineRunID, next.KnowledgePipelineRunID, err)
	}
}
