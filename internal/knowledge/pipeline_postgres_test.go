package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/storagecatalog"
)

func openPipelinePostgres(t *testing.T) *sql.DB {
	t.Helper()
	dbURL := os.Getenv("LOOM_TEST_DB_URL")
	if dbURL == "" {
		t.Skip("LOOM_TEST_DB_URL is not set")
	}
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func pipelineFixture(t *testing.T, db *sql.DB, fileClass, mimeType, body string, now time.Time) (*Service, KnowledgeObject) {
	t.Helper()
	service := NewService(db, WithClock(func() time.Time { return now }))
	root, err := service.PrepareSourceRoot(SourceRoot{
		RootKind: RootKindBoxNotes, NodeKey: "pipeline-test-" + ids.NewKnowledgeObjectID(),
		BackendRootKey: "pipeline-test-" + ids.NewKnowledgeObjectID(), SourcePath: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	root, err = service.store.UpsertSourceRoot(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM knowledge.notes_source_roots WHERE notes_source_root_id=$1`, root.NotesSourceRootID)
	})
	path := filepath.Join(root.SourcePath, "fixture"+extensionForTestClass(fileClass))
	if err = os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	size := int64(len(body))
	object, err := service.PrepareKnowledgeObject(KnowledgeObject{
		NotesSourceRootID: root.NotesSourceRootID, SourceNodeKey: root.NodeKey,
		SourcePath: path, RelativePath: filepath.Base(path), Title: "fixture", FileClass: fileClass,
		MimeType: mimeType, SizeBytes: &size, SourceHash: hashArtifactValue(body), SourceRevision: "revision-1",
		LastSeenAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	// This is a local catalog source, not a native synced Box object.
	entry, err := storagecatalog.NewService(db).RegisterEntry(t.Context(), storagecatalog.RegisterEntryInput{
		StorageClass: storagecatalog.StorageClassObjectBlob, SourceArea: storagecatalog.SourceAreaNotes,
		OriginNodeKey: root.NodeKey, WatchedRootKey: root.BackendRootKey,
		LogicalPath: object.RelativePath, OriginalSourcePath: path, FileClass: fileClass, MimeType: mimeType,
		SizeBytes: &size, ChecksumAlgorithm: "sha256", ChecksumHex: strings.TrimPrefix(object.SourceHash, "sha256:"),
		AvailabilityState: storagecatalog.AvailabilityStateAvailable,
	})
	if err != nil {
		t.Fatal(err)
	}
	object.StorageEntryID = &entry.StorageEntryID
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM storage.storage_entries WHERE storage_entry_id=$1`, entry.StorageEntryID)
	})
	object, err = service.store.UpsertKnowledgeObject(context.Background(), object)
	if err != nil {
		t.Fatal(err)
	}
	return service, object
}

func extensionForTestClass(fileClass string) string {
	switch fileClass {
	case storagecatalog.FileClassPDF:
		return ".pdf"
	case storagecatalog.FileClassOfficeDocument:
		return ".docx"
	default:
		return ".md"
	}
}

func claimSinglePipeline(t *testing.T, service *Service, class, worker string, now time.Time) PipelineWorkItem {
	t.Helper()
	items, err := service.ClaimPipelineRuns(context.Background(), class, worker, PipelineClaimOptions{Limit: 1, LeaseDuration: time.Minute, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("claimed %d pipeline items, want 1", len(items))
	}
	return items[0]
}

func completeRunForTest(t *testing.T, db *sql.DB, runID string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE knowledge.pipeline_stage_runs SET status=CASE WHEN status='skipped_by_policy' THEN status ELSE 'complete' END,claimed_by_worker_run_id='',claim_generation=0,completed_at=now() WHERE knowledge_pipeline_run_id=$1`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE knowledge.pipeline_runs SET status='complete',current_stage_key='',current_execution_class='',claimed_by_worker_run_id='',claim_expires_at=NULL,completed_at=now() WHERE knowledge_pipeline_run_id=$1`, runID); err != nil {
		t.Fatal(err)
	}
}

func setPipelinePolicyForTest(t *testing.T, db *sql.DB, policy PipelinePolicy) {
	t.Helper()
	metadata, err := json.Marshal(map[string]any{
		"schema_version": "knowledge.pipeline_policy.v1", "pdf_ocr_enabled": policy.PDFOCREnabled,
		"image_descriptions_enabled": policy.ImageDescriptionsEnabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO knowledge.embedding_settings (
		embedding_settings_id,enabled,runtime_key,model_key,dimensions,distance_metric,ollama_url,
		quiet_window_seconds,global_concurrency,history_per_lineage,metadata,created_at,updated_at)
		VALUES ($1,$2,'ollama','mxbai-embed-large',1024,'cosine','http://127.0.0.1:11434',600,1,2,$3,now(),now())
		ON CONFLICT (embedding_settings_id) DO UPDATE SET enabled=EXCLUDED.enabled,metadata=EXCLUDED.metadata,updated_at=now()`, EmbeddingSettingsID, policy.EmbeddingsEnabled, metadata); err != nil {
		t.Fatal(err)
	}
}

func positionPipelineAtStage(t *testing.T, db *sql.DB, runID, stageKey string, now time.Time) PipelineStageRun {
	t.Helper()
	var ordinal int
	if err := db.QueryRow(`SELECT ordinal FROM knowledge.pipeline_stage_runs WHERE knowledge_pipeline_run_id=$1 AND stage_key=$2`, runID, stageKey).Scan(&ordinal); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE knowledge.pipeline_stage_runs
		SET status=CASE WHEN ordinal<$2 AND status<>'skipped_by_policy' THEN 'complete' WHEN ordinal=$2 THEN 'ready' ELSE status END,
		    claimed_by_worker_run_id='',claim_generation=0,
		    completed_at=CASE WHEN ordinal<$2 AND status<>'skipped_by_policy' THEN $3 ELSE completed_at END,
		    updated_at=$3 WHERE knowledge_pipeline_run_id=$1`, runID, ordinal, now); err != nil {
		t.Fatal(err)
	}
	stage, err := scanPipelineStageRun(db.QueryRow(`SELECT `+pipelineStageRunColumns()+` FROM knowledge.pipeline_stage_runs WHERE knowledge_pipeline_run_id=$1 AND stage_key=$2`, runID, stageKey))
	if err != nil {
		t.Fatal(err)
	}
	status := waitingPipelineStatus(stage.ExecutionClass, false)
	if _, err = db.Exec(`UPDATE knowledge.pipeline_runs SET status=$2,current_stage_key=$3,current_execution_class=$4,
		claimed_by_worker_run_id='',claim_expires_at=NULL,quiet_window_eligible_at=$5,updated_at=$5
		WHERE knowledge_pipeline_run_id=$1`, runID, status, stageKey, stage.ExecutionClass, now); err != nil {
		t.Fatal(err)
	}
	return stage
}

func TestEnsurePipelineRunReconcileIdempotencePolicyAndForcePostgres(t *testing.T) {
	db := openPipelinePostgres(t)
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	service, object := pipelineFixture(t, db, storagecatalog.FileClassMarkdown, "text/markdown", "hello", now)
	policy := PipelinePolicy{PDFOCREnabled: false, ImageDescriptionsEnabled: false, EmbeddingsEnabled: false}
	first, err := service.EnsurePipelineRun(context.Background(), object, policy, false, 100)
	if err != nil {
		t.Fatal(err)
	}
	completeRunForTest(t, db, first.KnowledgePipelineRunID)
	repeated, err := service.EnsurePipelineRun(context.Background(), object, policy, false, 100)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.KnowledgePipelineRunID != first.KnowledgePipelineRunID || repeated.Generation != first.Generation {
		t.Fatalf("unchanged reconcile created %#v after %#v", repeated, first)
	}
	var disabled int
	if err = db.QueryRow(`SELECT count(*) FROM knowledge.pipeline_stage_runs WHERE knowledge_pipeline_run_id=$1 AND stage_key IN ('pdf_ocr','image_description','embedding') AND status='skipped_by_policy'`, first.KnowledgePipelineRunID).Scan(&disabled); err != nil {
		t.Fatal(err)
	}
	if disabled != 1 {
		t.Fatalf("disabled applicable policy stages = %d, want 1", disabled)
	}

	object.SourceRevision = "revision-2"
	object.SourceHash = hashArtifactValue("changed")
	object.UpdatedAt = now.Add(time.Minute)
	object, err = service.store.UpsertKnowledgeObject(context.Background(), object)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := service.EnsurePipelineRun(context.Background(), object, policy, false, 100)
	if err != nil {
		t.Fatal(err)
	}
	if changed.KnowledgePipelineRunID == first.KnowledgePipelineRunID || changed.Generation <= first.Generation {
		t.Fatalf("changed revision did not create a new run: %#v", changed)
	}
	forced, err := service.EnsurePipelineRun(context.Background(), object, policy, true, 100)
	if err != nil {
		t.Fatal(err)
	}
	if forced.KnowledgePipelineRunID == changed.KnowledgePipelineRunID || forced.Generation <= changed.Generation {
		t.Fatalf("forced reconcile did not create a new generation: %#v", forced)
	}
}

func TestUnifiedNativeExtractionPersistsChronologyAndReplacesLinksPostgres(t *testing.T) {
	db := openPipelinePostgres(t)
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name, class, mime, basis, kind string
	}{
		{"frontmatter", storagecatalog.FileClassMarkdown, "text/markdown", AbsoluteTimeBasisFrontmatterCreatedAt, AbsoluteTimeKindCreated},
		{"pdf", storagecatalog.FileClassPDF, "application/pdf", AbsoluteTimeBasisEmbeddedCreatedAt, AbsoluteTimeKindCreated},
		{"office", storagecatalog.FileClassOfficeDocument, "application/vnd.openxmlformats-officedocument.wordprocessingml.document", AbsoluteTimeBasisEmbeddedModifiedAt, AbsoluteTimeKindModified},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, object := pipelineFixture(t, db, test.class, test.mime, "fixture", now.Add(time.Duration(index)*time.Second))
			run, err := service.EnsurePipelineRun(context.Background(), object, PipelinePolicy{}, false, 100)
			if err != nil {
				t.Fatal(err)
			}
			metadata := claimSinglePipeline(t, service, PipelineExecutionCoordinator, "worker-metadata-"+test.name, now)
			if metadata.Run.KnowledgePipelineRunID != run.KnowledgePipelineRunID {
				t.Fatal("claimed unexpected run")
			}
			if _, err = service.CompletePipelineStage(context.Background(), metadata, nil); err != nil {
				t.Fatal(err)
			}
			native := claimSinglePipeline(t, service, PipelineExecutionCoordinator, "worker-native-"+test.name, now)
			refined := time.Date(2019+index, time.March, 4, 5, 6, 7, 0, time.UTC)
			linkText := "[target](target.md) and [[Wiki Target]]"
			extraction := ExtractionResult{
				Status: ExtractionStatusExtracted, ExtractorKey: "test." + test.name, ExtractorVersion: "v1",
				TextSections: []ExtractedTextSection{{Text: linkText, TextSource: TextSourceStructuredText, StructuralPath: "document"}},
				Document:     TextDocument{Text: linkText}, Links: ExtractMarkdownLinks(linkText),
				AbsoluteTimeCandidates: []AbsoluteTimeCandidate{{Kind: test.kind, Basis: test.basis, RawValue: refined.Format(time.RFC3339), Timestamp: &refined}},
			}
			if test.class == storagecatalog.FileClassMarkdown {
				extraction.Document.Frontmatter = map[string]any{"tags": []any{"hardware-acceptance"}}
			}
			version, artifacts, err := service.applyUnifiedNativeExtraction(context.Background(), native, extraction)
			if err != nil {
				t.Fatal(err)
			}
			if !version.RecencyAt.Equal(refined) || version.RecencyBasis != test.basis || len(artifacts) != 1 || !artifacts[0].RecencyAt.Equal(refined) {
				t.Fatalf("refined version/artifacts = %#v / %#v", version, artifacts)
			}
			stored, err := service.store.GetKnowledgeObject(context.Background(), object.KnowledgeObjectID)
			if err != nil {
				t.Fatal(err)
			}
			if !stored.RecencyAt.Equal(refined) || stored.RecencyBasis != test.basis {
				t.Fatalf("stored chronology = %s/%s", stored.RecencyAt, stored.RecencyBasis)
			}
			if test.class == storagecatalog.FileClassMarkdown {
				extraction.Links = extraction.Links[:1]
				if _, _, err = service.applyUnifiedNativeExtraction(context.Background(), native, extraction); err != nil {
					t.Fatal(err)
				}
				var links int
				if err = db.QueryRow(`SELECT count(*) FROM knowledge.object_links WHERE source_knowledge_object_id=$1`, object.KnowledgeObjectID).Scan(&links); err != nil {
					t.Fatal(err)
				}
				if links != 1 {
					t.Fatalf("replaced markdown links = %d, want 1", links)
				}
				if _, err = service.EnsurePipelineRun(context.Background(), object, PipelinePolicy{}, true, 100); err != nil {
					t.Fatal(err)
				}
				preserved, getErr := service.store.getKnowledgeObjectVersion(context.Background(), version.KnowledgeObjectVersionID)
				if getErr != nil {
					t.Fatal(getErr)
				}
				if tags := tagsFromVersionMetadata(preserved.Metadata); len(tags) != 1 || tags[0] != "hardware-acceptance" {
					t.Fatalf("frontmatter tags after forced reprocess = %#v, want preserved tag", tags)
				}
			}
		})
	}
}

func TestPipelineChunkStageRecordsChunkCountPostgres(t *testing.T) {
	db := openPipelinePostgres(t)
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	service, object := pipelineFixture(t, db, storagecatalog.FileClassMarkdown, "text/markdown", "chunk body", now)
	run, err := service.EnsurePipelineRun(context.Background(), object, PipelinePolicy{}, false, 100)
	if err != nil {
		t.Fatal(err)
	}
	positionPipelineAtStage(t, db, run.KnowledgePipelineRunID, FilePipelineStageChunk, now)
	item := claimSinglePipeline(t, service, PipelineExecutionCoordinator, "worker-chunk", now)
	version, err := service.store.getKnowledgeObjectVersion(context.Background(), item.Run.KnowledgeObjectVersionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.createAndActivatePipelineArtifact(context.Background(), item, version, DerivedArtifactInput{
		ArtifactKind:     ArtifactKindConsolidatedText,
		SourceLocator:    "document",
		Text:             "chunk body",
		GeneratorKey:     "test.consolidation",
		GeneratorVersion: "v1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.executeCoordinatorStage(context.Background(), item, PipelineCoordinatorRunInput{}); err != nil {
		t.Fatal(err)
	}
	stage, err := service.store.getCurrentPipelineStage(context.Background(), run.KnowledgePipelineRunID, FilePipelineStageChunk)
	if err != nil {
		t.Fatal(err)
	}
	if stage.ProgressCompleted != 1 || stage.ProgressTotal != 1 {
		t.Fatalf("chunk progress = %d/%d, want 1/1", stage.ProgressCompleted, stage.ProgressTotal)
	}
	var metadata map[string]any
	if err = json.Unmarshal(stage.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if count, ok := metadata["chunk_count"].(float64); !ok || count != 1 {
		t.Fatalf("chunk_count = %#v, want 1", metadata["chunk_count"])
	}
}

func TestPipelineClaimLeaseReclamationFencesStaleWorkerPostgres(t *testing.T) {
	db := openPipelinePostgres(t)
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	service, object := pipelineFixture(t, db, storagecatalog.FileClassMarkdown, "text/markdown", "lease", now)
	if _, err := service.EnsurePipelineRun(context.Background(), object, PipelinePolicy{}, false, 100); err != nil {
		t.Fatal(err)
	}
	stale := claimSinglePipeline(t, service, PipelineExecutionCoordinator, "worker-stale", now)
	if _, err := service.ReleaseExpiredPipelineClaims(context.Background(), now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	current := claimSinglePipeline(t, service, PipelineExecutionCoordinator, "worker-current", now.Add(2*time.Minute))
	if _, err := service.CompletePipelineStage(context.Background(), stale, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale completion error = %v, want conflict", err)
	}
	stage, err := service.store.getCurrentPipelineStage(context.Background(), current.Run.KnowledgePipelineRunID, current.Stage.StageKey)
	if err != nil {
		t.Fatal(err)
	}
	if stage.Status != PipelineStageStatusProcessing || stage.ClaimedByWorkerRunID != "worker-current" || stage.ClaimGeneration != current.Run.ClaimGeneration {
		t.Fatalf("stale worker mutated reclaimed stage: %#v", stage)
	}
}

func TestPipelineSupersessionClearsClaimsAndFencesOldWorkersPostgres(t *testing.T) {
	db := openPipelinePostgres(t)
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)

	t.Run("force coordinator claim", func(t *testing.T) {
		setPipelinePolicyForTest(t, db, PipelinePolicy{})
		service, object := pipelineFixture(t, db, storagecatalog.FileClassMarkdown, "text/markdown", "force", now)
		run, err := service.EnsurePipelineRun(context.Background(), object, PipelinePolicy{}, false, 100)
		if err != nil {
			t.Fatal(err)
		}
		old := claimSinglePipeline(t, service, PipelineExecutionCoordinator, "force-old", now)
		replacement, err := service.EnsurePipelineRun(context.Background(), object, PipelinePolicy{}, true, 100)
		if err != nil {
			t.Fatal(err)
		}
		if replacement.Generation <= run.Generation {
			t.Fatalf("replacement generation = %d", replacement.Generation)
		}
		assertSupersededClaim(t, db, old)
		assertOldWorkerMutationsConflict(t, service, old)
		claimed := claimSinglePipeline(t, service, PipelineExecutionCoordinator, "force-new", now)
		if claimed.Run.KnowledgePipelineRunID != replacement.KnowledgePipelineRunID {
			t.Fatalf("claimed replacement %s, want %s", claimed.Run.KnowledgePipelineRunID, replacement.KnowledgePipelineRunID)
		}
	})

	t.Run("changed revision coordinator claim", func(t *testing.T) {
		setPipelinePolicyForTest(t, db, PipelinePolicy{})
		service, object := pipelineFixture(t, db, storagecatalog.FileClassMarkdown, "text/markdown", "revision", now.Add(time.Second))
		if _, err := service.EnsurePipelineRun(context.Background(), object, PipelinePolicy{}, false, 100); err != nil {
			t.Fatal(err)
		}
		old := claimSinglePipeline(t, service, PipelineExecutionCoordinator, "revision-old", now)
		object.SourceRevision = "revision-2"
		object.SourceHash = hashArtifactValue("revision changed")
		object.UpdatedAt = now.Add(time.Minute)
		object, err := service.store.UpsertKnowledgeObject(context.Background(), object)
		if err != nil {
			t.Fatal(err)
		}
		replacement, err := service.EnsurePipelineRun(context.Background(), object, PipelinePolicy{}, false, 100)
		if err != nil {
			t.Fatal(err)
		}
		assertSupersededClaim(t, db, old)
		if _, err = service.CompletePipelineStage(context.Background(), old, nil); !errors.Is(err, ErrConflict) {
			t.Fatalf("old revision completion error = %v", err)
		}
		claimed := claimSinglePipeline(t, service, PipelineExecutionCoordinator, "revision-new", now)
		if claimed.Run.KnowledgePipelineRunID != replacement.KnowledgePipelineRunID {
			t.Fatal("changed revision replacement was not claimable")
		}
	})

	t.Run("policy replacement heavy claim and units", func(t *testing.T) {
		setPipelinePolicyForTest(t, db, PipelinePolicy{PDFOCREnabled: true})
		service, object := pipelineFixture(t, db, storagecatalog.FileClassPDF, "application/pdf", "policy", now.Add(2*time.Second))
		run, err := service.EnsurePipelineRun(context.Background(), object, PipelinePolicy{PDFOCREnabled: true}, false, 100)
		if err != nil {
			t.Fatal(err)
		}
		ocr := positionPipelineAtStage(t, db, run.KnowledgePipelineRunID, FilePipelineStagePDFOCR, now)
		for page := 1; page <= 2; page++ {
			if err = service.store.upsertPDFOCRUnit(context.Background(), ocr, PDFPage{Number: page}, object.SourceHash); err != nil {
				t.Fatal(err)
			}
		}
		old := claimSinglePipeline(t, service, PipelineExecutionHeavy, "policy-old", now)
		units, err := service.store.listReadyPDFOCRUnits(context.Background(), old, 2)
		if err != nil || len(units) != 2 {
			t.Fatalf("claimed OCR units = %d, %v", len(units), err)
		}
		disabled := false
		if _, err = service.UpdatePipelinePolicy(context.Background(), PipelinePolicyUpdate{PDFOCREnabled: &disabled}); err != nil {
			t.Fatal(err)
		}
		assertSupersededClaim(t, db, old)
		assertPDFUnitCounts(t, db, old.Stage.KnowledgePipelineStageRunID, map[string]int{PipelineStageStatusStale: 2, PipelineStageStatusProcessing: 0})
		if err = service.store.cleanupPDFOCRBatch(context.Background(), old, units[0].KnowledgePipelineStageUnitID, PipelineStageStatusFailedRetryable, []byte(`{"code":"late"}`)); !errors.Is(err, ErrConflict) {
			t.Fatalf("old unit mutation error = %v", err)
		}
		assertOldWorkerMutationsConflict(t, service, old)
		claimed := claimSinglePipeline(t, service, PipelineExecutionCoordinator, "policy-new", now)
		if claimed.Run.Generation <= run.Generation || claimed.Stage.StageKey != FilePipelineStageMetadata {
			t.Fatalf("policy replacement was not immediately claimable: %#v", claimed)
		}
	})
}

func assertSupersededClaim(t *testing.T, db *sql.DB, item PipelineWorkItem) {
	t.Helper()
	var runStatus, runWorker, stageStatus, stageWorker string
	var stageGeneration int64
	if err := db.QueryRow(`SELECT r.status,r.claimed_by_worker_run_id,s.status,s.claimed_by_worker_run_id,s.claim_generation
		FROM knowledge.pipeline_runs r JOIN knowledge.pipeline_stage_runs s ON s.knowledge_pipeline_run_id=r.knowledge_pipeline_run_id
		WHERE r.knowledge_pipeline_run_id=$1 AND s.knowledge_pipeline_stage_run_id=$2`, item.Run.KnowledgePipelineRunID, item.Stage.KnowledgePipelineStageRunID).Scan(&runStatus, &runWorker, &stageStatus, &stageWorker, &stageGeneration); err != nil {
		t.Fatal(err)
	}
	if runStatus != FilePipelineStatusStale || runWorker != "" || stageStatus != PipelineStageStatusStale || stageWorker != "" || stageGeneration != 0 {
		t.Fatalf("superseded claim retained ownership: %s/%q %s/%q/%d", runStatus, runWorker, stageStatus, stageWorker, stageGeneration)
	}
}

func assertOldWorkerMutationsConflict(t *testing.T, service *Service, item PipelineWorkItem) {
	t.Helper()
	ctx := context.Background()
	if _, err := service.CompletePipelineStage(ctx, item, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale completion error = %v", err)
	}
	if err := service.FailPipelineStage(ctx, item, errors.New("late failure"), true); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale failure error = %v", err)
	}
	if err := service.ReleasePipelineClaim(ctx, item); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale release error = %v", err)
	}
	if err := service.storePipelineStageObservation(ctx, item, HeavyStageObservation{Observed: json.RawMessage(`{"late":true}`)}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale observation error = %v", err)
	}
	version, err := service.store.getKnowledgeObjectVersion(ctx, item.Run.KnowledgeObjectVersionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.createAndActivatePipelineArtifact(ctx, item, version, DerivedArtifactInput{ArtifactKind: ArtifactKindMetadataText, SourceLocator: "late", Text: "late", GeneratorKey: "test", GeneratorVersion: "v1"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale artifact error = %v", err)
	}
}

func TestQuietWindowGatesOnlyCurrentHeavyStagePostgres(t *testing.T) {
	db := openPipelinePostgres(t)
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	service, object := pipelineFixture(t, db, storagecatalog.FileClassPDF, "application/pdf", "pdf", now)
	run, err := service.EnsurePipelineRun(context.Background(), object, PipelinePolicy{PDFOCREnabled: true}, false, 100)
	if err != nil {
		t.Fatal(err)
	}
	if run.QuietWindowEligibleAt == nil || !run.QuietWindowEligibleAt.After(now) {
		t.Fatalf("quiet eligibility = %v", run.QuietWindowEligibleAt)
	}
	first := claimSinglePipeline(t, service, PipelineExecutionCoordinator, "quiet-coordinator", now)
	if first.Stage.StageKey != FilePipelineStageMetadata {
		t.Fatalf("first stage = %s", first.Stage.StageKey)
	}
	item := first
	for item.Run.CurrentExecutionClass == PipelineExecutionCoordinator {
		if _, err = service.CompletePipelineStage(context.Background(), item, nil); err != nil {
			t.Fatal(err)
		}
		run, err = service.store.GetPipelineRun(context.Background(), run.KnowledgePipelineRunID)
		if err != nil {
			t.Fatal(err)
		}
		if run.CurrentExecutionClass != PipelineExecutionCoordinator {
			break
		}
		item = claimSinglePipeline(t, service, PipelineExecutionCoordinator, "quiet-coordinator", now)
	}
	before, err := service.ClaimPipelineRuns(context.Background(), PipelineExecutionHeavy, "quiet-heavy-before", PipelineClaimOptions{Limit: 1, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 0 {
		t.Fatalf("heavy stage claimed before quiet window: %#v", before)
	}
	after := claimSinglePipeline(t, service, PipelineExecutionHeavy, "quiet-heavy-after", *run.QuietWindowEligibleAt)
	if after.Run.KnowledgePipelineRunID != run.KnowledgePipelineRunID {
		t.Fatal("claimed unexpected heavy run")
	}
}

func TestRetryPipelineFromStageReusesUpstreamOnlyPostgres(t *testing.T) {
	db := openPipelinePostgres(t)
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	service, object := pipelineFixture(t, db, storagecatalog.FileClassMarkdown, "text/markdown", "retry", now)
	run, err := service.EnsurePipelineRun(context.Background(), object, PipelinePolicy{}, false, 100)
	if err != nil {
		t.Fatal(err)
	}
	completeRunForTest(t, db, run.KnowledgePipelineRunID)
	stages, err := service.store.ListPipelineStageRuns(context.Background(), run.KnowledgePipelineRunID)
	if err != nil {
		t.Fatal(err)
	}
	version, err := service.store.getKnowledgeObjectVersion(context.Background(), run.KnowledgeObjectVersionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		stageKey, kind, text string
	}{{FilePipelineStageMetadata, ArtifactKindMetadataText, "upstream metadata"}, {FilePipelineStageNativeText, ArtifactKindStructuredText, "upstream native"}} {
		var artifactStage PipelineStageRun
		for _, stage := range stages {
			if stage.StageKey == fixture.stageKey {
				artifactStage = stage
				break
			}
		}
		artifact, prepareErr := service.PrepareDerivedArtifact(run, artifactStage, version, DerivedArtifactInput{ArtifactKind: fixture.kind, SourceLocator: "document", Text: fixture.text, GeneratorKey: "test", GeneratorVersion: "v1"})
		if prepareErr != nil {
			t.Fatal(prepareErr)
		}
		artifact, prepareErr = service.store.CreateDerivedArtifact(context.Background(), artifact)
		if prepareErr != nil {
			t.Fatal(prepareErr)
		}
		if _, prepareErr = service.store.ActivateDerivedArtifact(context.Background(), artifact.KnowledgeDerivedArtifactID, run.Generation); prepareErr != nil {
			t.Fatal(prepareErr)
		}
	}
	retried, err := service.RetryPipeline(context.Background(), run.KnowledgePipelineRunID, PipelineRetryInput{StageKey: FilePipelineStageConsolidateText})
	if err != nil {
		t.Fatal(err)
	}
	if retried.Generation <= run.Generation || retried.CurrentStageKey != FilePipelineStageConsolidateText {
		t.Fatalf("retried run = %#v", retried)
	}
	newStages, err := service.store.ListPipelineStageRuns(context.Background(), retried.KnowledgePipelineRunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range newStages {
		switch {
		case stage.Ordinal < stageOrdinal(newStages, FilePipelineStageConsolidateText) && !isReusableUpstreamStageStatus(stage.Status):
			t.Fatalf("upstream stage reran: %#v", stage)
		case stage.StageKey == FilePipelineStageConsolidateText && stage.Status != PipelineStageStatusReady:
			t.Fatalf("target stage = %#v", stage)
		case stage.Ordinal > stageOrdinal(newStages, FilePipelineStageConsolidateText) && stage.Status != PipelineStageStatusWaitingDependency && stage.Status != PipelineStageStatusSkippedByPolicy:
			t.Fatalf("downstream stage was not invalidated: %#v", stage)
		}
	}
	active, err := service.store.ListDerivedArtifacts(context.Background(), retried.KnowledgePipelineRunID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 {
		t.Fatalf("upstream artifact was not retained: %#v", active)
	}
	var staleSourceActive int
	if err = db.QueryRow(`SELECT count(*) FROM knowledge.derived_artifacts WHERE knowledge_pipeline_run_id=$1 AND active=true`, run.KnowledgePipelineRunID).Scan(&staleSourceActive); err != nil {
		t.Fatal(err)
	}
	if staleSourceActive != 0 {
		t.Fatalf("retry source retained %d active artifacts after supersession", staleSourceActive)
	}
	status, err := service.GetPipelineOverallStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Operations.ActivationMismatches != 0 {
		t.Fatalf("retry left %d activation mismatches", status.Operations.ActivationMismatches)
	}
	if _, err = service.RetryPipeline(context.Background(), retried.KnowledgePipelineRunID, PipelineRetryInput{StageKey: "not-a-stage"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid retry stage error = %v", err)
	}
}

func TestRetryPipelineRejectsIncompatibleUpstreamStatePostgres(t *testing.T) {
	db := openPipelinePostgres(t)
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)

	t.Run("policy changed skipped upstream", func(t *testing.T) {
		setPipelinePolicyForTest(t, db, PipelinePolicy{})
		service, object := pipelineFixture(t, db, storagecatalog.FileClassPDF, "application/pdf", "retry policy", now)
		run, err := service.EnsurePipelineRun(context.Background(), object, PipelinePolicy{}, false, 100)
		if err != nil {
			t.Fatal(err)
		}
		completeRunForTest(t, db, run.KnowledgePipelineRunID)
		setPipelinePolicyForTest(t, db, PipelinePolicy{PDFOCREnabled: true})
		if _, err = service.RetryPipeline(context.Background(), run.KnowledgePipelineRunID, PipelineRetryInput{StageKey: FilePipelineStageConsolidateText}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("policy-incompatible retry error = %v", err)
		}
	})

	t.Run("source revision changed", func(t *testing.T) {
		setPipelinePolicyForTest(t, db, PipelinePolicy{})
		service, object := pipelineFixture(t, db, storagecatalog.FileClassMarkdown, "text/markdown", "retry revision", now.Add(time.Second))
		run, err := service.EnsurePipelineRun(context.Background(), object, PipelinePolicy{}, false, 100)
		if err != nil {
			t.Fatal(err)
		}
		completeRunForTest(t, db, run.KnowledgePipelineRunID)
		object.SourceRevision = "revision-2"
		object.SourceHash = hashArtifactValue("retry revision changed")
		object.UpdatedAt = now.Add(time.Minute)
		if _, err = service.store.UpsertKnowledgeObject(context.Background(), object); err != nil {
			t.Fatal(err)
		}
		if _, err = service.RetryPipeline(context.Background(), run.KnowledgePipelineRunID, PipelineRetryInput{StageKey: FilePipelineStageConsolidateText}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("old-revision retry error = %v", err)
		}
	})

	t.Run("required upstream artifact inactive", func(t *testing.T) {
		setPipelinePolicyForTest(t, db, PipelinePolicy{})
		service, object := pipelineFixture(t, db, storagecatalog.FileClassMarkdown, "text/markdown", "retry artifact", now.Add(2*time.Second))
		run, err := service.EnsurePipelineRun(context.Background(), object, PipelinePolicy{}, false, 100)
		if err != nil {
			t.Fatal(err)
		}
		completeRunForTest(t, db, run.KnowledgePipelineRunID)
		stages, err := service.store.ListPipelineStageRuns(context.Background(), run.KnowledgePipelineRunID)
		if err != nil {
			t.Fatal(err)
		}
		version, err := service.store.getKnowledgeObjectVersion(context.Background(), run.KnowledgeObjectVersionID)
		if err != nil {
			t.Fatal(err)
		}
		for _, fixture := range []struct{ stage, kind string }{{FilePipelineStageMetadata, ArtifactKindMetadataText}, {FilePipelineStageNativeText, ArtifactKindStructuredText}} {
			var sourceStage PipelineStageRun
			for _, stage := range stages {
				if stage.StageKey == fixture.stage {
					sourceStage = stage
				}
			}
			artifact, prepareErr := service.PrepareDerivedArtifact(run, sourceStage, version, DerivedArtifactInput{ArtifactKind: fixture.kind, SourceLocator: "document", Text: fixture.stage, GeneratorKey: "test", GeneratorVersion: "v1"})
			if prepareErr != nil {
				t.Fatal(prepareErr)
			}
			artifact, prepareErr = service.store.CreateDerivedArtifact(context.Background(), artifact)
			if prepareErr != nil {
				t.Fatal(prepareErr)
			}
			if _, prepareErr = service.store.ActivateDerivedArtifact(context.Background(), artifact.KnowledgeDerivedArtifactID, run.Generation); prepareErr != nil {
				t.Fatal(prepareErr)
			}
			if fixture.stage == FilePipelineStageNativeText {
				if _, prepareErr = db.Exec(`UPDATE knowledge.derived_artifacts SET active=false,state='historical',deactivated_at=now() WHERE knowledge_derived_artifact_id=$1`, artifact.KnowledgeDerivedArtifactID); prepareErr != nil {
					t.Fatal(prepareErr)
				}
			}
		}
		if _, err = service.RetryPipeline(context.Background(), run.KnowledgePipelineRunID, PipelineRetryInput{StageKey: FilePipelineStageConsolidateText}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("missing-output retry error = %v", err)
		}
	})
}

func stageOrdinal(stages []PipelineStageRun, key string) int {
	for _, stage := range stages {
		if stage.StageKey == key {
			return stage.Ordinal
		}
	}
	return -1
}

func TestPipelineOverallStatusCountsBeyondPresentationLimitPostgres(t *testing.T) {
	db := openPipelinePostgres(t)
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	service, first := pipelineFixture(t, db, storagecatalog.FileClassMarkdown, "text/markdown", "status-0", now)
	objects := []KnowledgeObject{first}
	for index := 1; index < 105; index++ {
		body := fmt.Sprintf("status-%d", index)
		object, err := service.PrepareKnowledgeObject(KnowledgeObject{
			NotesSourceRootID: first.NotesSourceRootID, SourceNodeKey: first.SourceNodeKey,
			SourcePath:   filepath.Join(filepath.Dir(first.SourcePath), fmt.Sprintf("status-%d.md", index)),
			RelativePath: fmt.Sprintf("status-%d.md", index), FileClass: storagecatalog.FileClassMarkdown,
			MimeType: "text/markdown", SourceHash: hashArtifactValue(body), SourceRevision: "revision-1", LastSeenAt: now, UpdatedAt: now,
		})
		if err != nil {
			t.Fatal(err)
		}
		object, err = service.store.UpsertKnowledgeObject(context.Background(), object)
		if err != nil {
			t.Fatal(err)
		}
		objects = append(objects, object)
	}
	for _, object := range objects {
		if _, err := service.EnsurePipelineRun(context.Background(), object, PipelinePolicy{}, false, 100); err != nil {
			t.Fatal(err)
		}
	}
	status, err := service.GetPipelineOverallStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Counts[FilePipelineStatusWaitingCoordinator] < 105 {
		t.Fatalf("waiting coordinator count = %d, want at least 105", status.Counts[FilePipelineStatusWaitingCoordinator])
	}
	if len(status.Current) != 100 {
		t.Fatalf("presentation current length = %d, want 100", len(status.Current))
	}
}

func TestPDFOCRBatchOwnershipFailureCancellationAndResumePostgres(t *testing.T) {
	db := openPipelinePostgres(t)
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	service, object := pipelineFixture(t, db, storagecatalog.FileClassPDF, "application/pdf", "pdf", now)
	run, err := service.EnsurePipelineRun(context.Background(), object, PipelinePolicy{PDFOCREnabled: true}, false, 100)
	if err != nil {
		t.Fatal(err)
	}
	stages, err := service.store.ListPipelineStageRuns(context.Background(), run.KnowledgePipelineRunID)
	if err != nil {
		t.Fatal(err)
	}
	ocrOrdinal := stageOrdinal(stages, FilePipelineStagePDFOCR)
	var ocr PipelineStageRun
	for _, stage := range stages {
		if stage.StageKey == FilePipelineStagePDFOCR {
			ocr = stage
		}
	}
	if ocr.KnowledgePipelineStageRunID == "" {
		t.Fatal("PDF OCR stage missing")
	}
	if _, err = db.Exec(`UPDATE knowledge.pipeline_stage_runs SET status=CASE WHEN ordinal<$2 THEN 'complete' WHEN ordinal=$2 THEN 'ready' ELSE status END,completed_at=CASE WHEN ordinal<$2 THEN now() ELSE completed_at END WHERE knowledge_pipeline_run_id=$1`, run.KnowledgePipelineRunID, ocrOrdinal); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE knowledge.pipeline_runs SET status='waiting_heavy',current_stage_key=$2,current_execution_class='heavy',quiet_window_eligible_at=$3 WHERE knowledge_pipeline_run_id=$1`, run.KnowledgePipelineRunID, FilePipelineStagePDFOCR, now); err != nil {
		t.Fatal(err)
	}
	for page := 1; page <= 4; page++ {
		if err = service.store.upsertPDFOCRUnit(context.Background(), ocr, PDFPage{Number: page}, object.SourceHash); err != nil {
			t.Fatal(err)
		}
	}

	claim := claimSinglePipeline(t, service, PipelineExecutionHeavy, "ocr-first", now)
	units, err := service.store.listReadyPDFOCRUnits(context.Background(), claim, 4)
	if err != nil || len(units) != 4 {
		t.Fatalf("first batch = %d, %v", len(units), err)
	}
	if err = service.store.cleanupPDFOCRBatch(context.Background(), claim, units[0].KnowledgePipelineStageUnitID, PipelineStageStatusFailedRetryable, []byte(`{"code":"first"}`)); err != nil {
		t.Fatal(err)
	}
	assertPDFUnitCounts(t, db, ocr.KnowledgePipelineStageRunID, map[string]int{PipelineStageStatusFailedRetryable: 1, PipelineStageStatusReady: 3, PipelineStageStatusProcessing: 0})
	if err = service.FailPipelineStage(context.Background(), claim, errors.New("first page failed"), true); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE knowledge.pipeline_stage_runs SET next_attempt_at=$2 WHERE knowledge_pipeline_stage_run_id=$1`, ocr.KnowledgePipelineStageRunID, now); err != nil {
		t.Fatal(err)
	}

	claim = claimSinglePipeline(t, service, PipelineExecutionHeavy, "ocr-middle", now)
	units, err = service.store.listReadyPDFOCRUnits(context.Background(), claim, 4)
	if err != nil || len(units) != 4 {
		t.Fatalf("middle batch = %d, %v", len(units), err)
	}
	for _, unit := range units[:2] {
		result, updateErr := db.Exec(`UPDATE knowledge.pipeline_stage_units SET status='complete',claimed_by_worker_run_id='',claim_generation=0,completed_at=now() WHERE knowledge_pipeline_stage_unit_id=$1 AND claimed_by_worker_run_id=$2 AND claim_generation=$3`, unit.KnowledgePipelineStageUnitID, claim.Run.ClaimedByWorkerRunID, claim.Run.ClaimGeneration)
		if updateErr != nil || requireOneRow(result, "test PDF OCR completion") != nil {
			t.Fatalf("complete unit: %v", updateErr)
		}
	}
	if err = service.store.cleanupPDFOCRBatch(context.Background(), claim, units[2].KnowledgePipelineStageUnitID, PipelineStageStatusFailedRetryable, []byte(`{"code":"cancelled"}`)); err != nil {
		t.Fatal(err)
	}
	assertPDFUnitCounts(t, db, ocr.KnowledgePipelineStageRunID, map[string]int{PipelineStageStatusComplete: 2, PipelineStageStatusFailedRetryable: 1, PipelineStageStatusReady: 1, PipelineStageStatusProcessing: 0})
	if err = service.FailPipelineStage(context.Background(), claim, context.Canceled, true); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE knowledge.pipeline_stage_runs SET next_attempt_at=$2 WHERE knowledge_pipeline_stage_run_id=$1`, ocr.KnowledgePipelineStageRunID, now); err != nil {
		t.Fatal(err)
	}

	claim = claimSinglePipeline(t, service, PipelineExecutionHeavy, "ocr-resume", now)
	units, err = service.store.listReadyPDFOCRUnits(context.Background(), claim, 4)
	if err != nil || len(units) != 2 {
		t.Fatalf("resume batch = %d, %v", len(units), err)
	}
	for _, unit := range units {
		result, updateErr := db.Exec(`UPDATE knowledge.pipeline_stage_units SET status='complete',claimed_by_worker_run_id='',claim_generation=0,completed_at=now() WHERE knowledge_pipeline_stage_unit_id=$1 AND claimed_by_worker_run_id=$2 AND claim_generation=$3`, unit.KnowledgePipelineStageUnitID, claim.Run.ClaimedByWorkerRunID, claim.Run.ClaimGeneration)
		if updateErr != nil || requireOneRow(result, "test PDF OCR retry completion") != nil {
			t.Fatalf("complete retry unit: %v", updateErr)
		}
	}
	assertPDFUnitCounts(t, db, ocr.KnowledgePipelineStageRunID, map[string]int{PipelineStageStatusComplete: 4, PipelineStageStatusProcessing: 0, PipelineStageStatusReady: 0, PipelineStageStatusFailedRetryable: 0})
}

func assertPDFUnitCounts(t *testing.T, db *sql.DB, stageID string, expected map[string]int) {
	t.Helper()
	for status, want := range expected {
		var got int
		if err := db.QueryRow(`SELECT count(*) FROM knowledge.pipeline_stage_units WHERE knowledge_pipeline_stage_run_id=$1 AND status=$2`, stageID, status).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("PDF OCR units in %s = %d, want %d", status, got, want)
		}
	}
}

func TestForcedSameVersionPreservesEmbeddingsAndQueueCountsAreTruthfulPostgres(t *testing.T) {
	db := openPipelinePostgres(t)
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	service, object := pipelineFixture(t, db, storagecatalog.FileClassMarkdown, "text/markdown", "embedding", now)
	if _, err := service.SetEmbeddingsEnabled(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = service.SetEmbeddingsEnabled(context.Background(), false) })
	run, err := service.EnsurePipelineRun(context.Background(), object, PipelinePolicy{}, false, 100)
	if err != nil {
		t.Fatal(err)
	}
	version, err := service.store.getKnowledgeObjectVersion(context.Background(), run.KnowledgeObjectVersionID)
	if err != nil {
		t.Fatal(err)
	}
	inputs := ChunkTextDocument(TextDocument{Text: "embedding"}, ChunkerOptions{})
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := service.replaceKnowledgeChunksTx(context.Background(), tx, object, version, inputs)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	chunk := chunks[0]
	if _, err = db.Exec(`INSERT INTO knowledge.chunk_embeddings (
		knowledge_chunk_embedding_id,knowledge_chunk_id,knowledge_object_id,knowledge_object_version_id,
		embedding_runtime_model_id,runtime_key,model_key,dimensions,distance_metric,chunk_hash,chunker_version,
		input_hash,source_generation,embedding,status,active,metadata,created_at,activated_at)
		VALUES ($1,$2,$3,$4,'embedding_runtime_model_ollama_mxbai_embed_large_1024','ollama','mxbai-embed-large',1024,'cosine',$5,$6,$7,$8,array_fill(0::real,ARRAY[1024])::vector,'active',true,'{}',now(),now())`,
		newKnowledgeChunkEmbeddingID(), chunk.KnowledgeChunkID, object.KnowledgeObjectID, version.KnowledgeObjectVersionID,
		chunk.ChunkHash, chunk.ChunkerVersion, hashArtifactValue(chunk.ChunkText), run.Generation); err != nil {
		t.Fatal(err)
	}
	if _, err = service.EnsurePipelineRun(context.Background(), object, PipelinePolicy{}, true, 100); err != nil {
		t.Fatal(err)
	}
	var active int
	if err = db.QueryRow(`SELECT count(*) FROM knowledge.chunk_embeddings WHERE knowledge_object_id=$1 AND active=true`, object.KnowledgeObjectID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("forced same-version run deactivated %d valid embeddings", 1-active)
	}

	enabled, err := service.SetEmbeddingsEnabled(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if enabled.Queued != 1 {
		t.Fatalf("embedding enable queued = %d, want 1", enabled.Queued)
	}
	repeated, err := service.QueueAllCurrentEmbeddingWork(context.Background(), EmbeddingQueueAllInput{})
	if err != nil {
		t.Fatal(err)
	}
	if repeated.Queued != 0 || repeated.Skipped < 1 {
		t.Fatalf("repeated embedding queue result = %#v", repeated)
	}
}

func TestEmbeddingEnableQueuesEveryEligibleChunkClassPostgres(t *testing.T) {
	db := openPipelinePostgres(t)
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	setPipelinePolicyForTest(t, db, PipelinePolicy{})
	tests := []struct {
		name, class, mime string
	}{
		{"markdown", storagecatalog.FileClassMarkdown, "text/markdown"},
		{"text", storagecatalog.FileClassText, "text/plain"},
		{"pdf", storagecatalog.FileClassPDF, "application/pdf"},
		{"docx", storagecatalog.FileClassOfficeDocument, "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		{"code", storagecatalog.FileClassCode, "text/x-go"},
		{"structured", storagecatalog.FileClassCode, "application/json"},
		{"image-derived", storagecatalog.FileClassImage, "image/png"},
	}
	var service *Service
	for index, fixture := range tests {
		var object KnowledgeObject
		service, object = pipelineFixture(t, db, fixture.class, fixture.mime, fixture.name, now.Add(time.Duration(index)*time.Second))
		version, err := service.getOrCreateKnowledgeObjectVersionTxForTest(context.Background(), object)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = service.replaceKnowledgeChunksTx(context.Background(), tx, object, version, ChunkTextDocument(TextDocument{Text: fixture.name + " extracted text"}, ChunkerOptions{})); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if _, err = tx.Exec(`UPDATE knowledge.knowledge_objects SET processing_state='indexed' WHERE knowledge_object_id=$1`, object.KnowledgeObjectID); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	_, withoutChunks := pipelineFixture(t, db, storagecatalog.FileClassMarkdown, "text/markdown", "no chunks", now.Add(time.Minute))
	enabled, err := service.SetEmbeddingsEnabled(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if enabled.Queued != len(tests) {
		t.Fatalf("embedding enable queued = %d, want %d", enabled.Queued, len(tests))
	}
	var withoutRun int
	if err = db.QueryRow(`SELECT count(*) FROM knowledge.pipeline_runs WHERE knowledge_object_id=$1`, withoutChunks.KnowledgeObjectID).Scan(&withoutRun); err != nil {
		t.Fatal(err)
	}
	if withoutRun != 0 {
		t.Fatalf("object without chunks received %d runs", withoutRun)
	}
	repeated, err := service.QueueAllCurrentEmbeddingWork(context.Background(), EmbeddingQueueAllInput{})
	if err != nil {
		t.Fatal(err)
	}
	if repeated.Queued != 0 || repeated.Skipped != len(tests) {
		t.Fatalf("repeated embedding queue = %#v", repeated)
	}
	reenabled, err := service.SetEmbeddingsEnabled(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if reenabled.Queued != 0 {
		t.Fatalf("repeated embedding enable queued = %d", reenabled.Queued)
	}
}

func TestPipelinePolicyTransitionsReplaceOptionalStagePlansPostgres(t *testing.T) {
	db := openPipelinePostgres(t)
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name, class, mime, stage string
		policy                   PipelinePolicy
		disable                  PipelinePolicyUpdate
	}{
		{"pdf OCR", storagecatalog.FileClassPDF, "application/pdf", FilePipelineStagePDFOCR, PipelinePolicy{PDFOCREnabled: true}, PipelinePolicyUpdate{PDFOCREnabled: boolPointer(false)}},
		{"image description", storagecatalog.FileClassImage, "image/png", FilePipelineStageImageDescription, PipelinePolicy{ImageDescriptionsEnabled: true}, PipelinePolicyUpdate{ImageDescriptionsEnabled: boolPointer(false)}},
		{"embedding", storagecatalog.FileClassMarkdown, "text/markdown", FilePipelineStageEmbedding, PipelinePolicy{EmbeddingsEnabled: true}, PipelinePolicyUpdate{EmbeddingsEnabled: boolPointer(false)}},
	}
	for index, fixture := range tests {
		t.Run(fixture.name, func(t *testing.T) {
			setPipelinePolicyForTest(t, db, fixture.policy)
			service, object := pipelineFixture(t, db, fixture.class, fixture.mime, fixture.name, now.Add(time.Duration(index)*time.Second))
			run, err := service.EnsurePipelineRun(context.Background(), object, fixture.policy, false, 100)
			if err != nil {
				t.Fatal(err)
			}
			positionPipelineAtStage(t, db, run.KnowledgePipelineRunID, fixture.stage, now)
			old := claimSinglePipeline(t, service, PipelineExecutionHeavy, "policy-disable-"+fixture.stage, now)
			if _, err = service.UpdatePipelinePolicy(context.Background(), fixture.disable); err != nil {
				t.Fatal(err)
			}
			assertSupersededClaim(t, db, old)
			assertOldWorkerMutationsConflict(t, service, old)
			var claimable int
			if err = db.QueryRow(`SELECT count(*) FROM knowledge.pipeline_stage_runs s
				JOIN knowledge.pipeline_runs r ON r.knowledge_pipeline_run_id=s.knowledge_pipeline_run_id
				WHERE r.knowledge_object_id=$1 AND r.status NOT IN ('stale','cancelled') AND s.stage_key=$2
				  AND s.status IN ('ready','waiting_dependency','waiting_quiet_window','failed_retryable','processing')`, object.KnowledgeObjectID, fixture.stage).Scan(&claimable); err != nil {
				t.Fatal(err)
			}
			if claimable != 0 {
				t.Fatalf("disabled stage %s remained claimable in %d trajectories", fixture.stage, claimable)
			}
			var skipped int
			if err = db.QueryRow(`SELECT count(*) FROM knowledge.pipeline_stage_runs s
				JOIN knowledge.pipeline_runs r ON r.knowledge_pipeline_run_id=s.knowledge_pipeline_run_id
				WHERE r.knowledge_object_id=$1 AND r.status NOT IN ('stale','cancelled') AND s.stage_key=$2 AND s.status='skipped_by_policy'`, object.KnowledgeObjectID, fixture.stage).Scan(&skipped); err != nil {
				t.Fatal(err)
			}
			if skipped != 1 {
				t.Fatalf("disabled stage %s skipped rows = %d", fixture.stage, skipped)
			}
		})
	}
	for index, fixture := range []struct {
		name, class, mime, stage string
		enable                   PipelinePolicyUpdate
	}{
		{"enable PDF OCR", storagecatalog.FileClassPDF, "application/pdf", FilePipelineStagePDFOCR, PipelinePolicyUpdate{PDFOCREnabled: boolPointer(true)}},
		{"enable image description", storagecatalog.FileClassImage, "image/png", FilePipelineStageImageDescription, PipelinePolicyUpdate{ImageDescriptionsEnabled: boolPointer(true)}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			setPipelinePolicyForTest(t, db, PipelinePolicy{})
			service, object := pipelineFixture(t, db, fixture.class, fixture.mime, fixture.name, now.Add(time.Duration(10+index)*time.Second))
			old, err := service.EnsurePipelineRun(context.Background(), object, PipelinePolicy{}, false, 100)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = service.UpdatePipelinePolicy(context.Background(), fixture.enable); err != nil {
				t.Fatal(err)
			}
			var generation int64
			var status string
			if err = db.QueryRow(`SELECT r.generation,s.status FROM knowledge.pipeline_runs r
				JOIN knowledge.pipeline_stage_runs s ON s.knowledge_pipeline_run_id=r.knowledge_pipeline_run_id
				WHERE r.knowledge_object_id=$1 AND r.status NOT IN ('stale','cancelled') AND s.stage_key=$2
				ORDER BY r.generation DESC LIMIT 1`, object.KnowledgeObjectID, fixture.stage).Scan(&generation, &status); err != nil {
				t.Fatal(err)
			}
			if generation <= old.Generation || status == PipelineStageStatusSkippedByPolicy {
				t.Fatalf("enabled plan did not replace disabled trajectory: generation=%d status=%s", generation, status)
			}
		})
	}

	t.Run("enable replaces active disabled plan and compatibility matches canonical", func(t *testing.T) {
		setPipelinePolicyForTest(t, db, PipelinePolicy{})
		service, object := pipelineFixture(t, db, storagecatalog.FileClassMarkdown, "text/markdown", "enable", now.Add(time.Minute))
		run, err := service.EnsurePipelineRun(context.Background(), object, PipelinePolicy{}, false, 100)
		if err != nil {
			t.Fatal(err)
		}
		version, err := service.store.getKnowledgeObjectVersion(context.Background(), run.KnowledgeObjectVersionID)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = service.replaceKnowledgeChunksTx(context.Background(), tx, object, version, ChunkTextDocument(TextDocument{Text: "enable"}, ChunkerOptions{})); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		old := claimSinglePipeline(t, service, PipelineExecutionCoordinator, "enable-old", now)
		enabled, err := service.SetEmbeddingsEnabled(context.Background(), true)
		if err != nil {
			t.Fatal(err)
		}
		if enabled.Queued != 1 || !enabled.Settings.Enabled {
			t.Fatalf("compatibility enable result = %#v", enabled)
		}
		assertSupersededClaim(t, db, old)
		if _, err = service.CompletePipelineStage(context.Background(), old, nil); !errors.Is(err, ErrConflict) {
			t.Fatalf("stale active-plan worker completion = %v", err)
		}
		var selected int
		if err = db.QueryRow(`SELECT count(*) FROM knowledge.pipeline_stage_runs s
			JOIN knowledge.pipeline_runs r ON r.knowledge_pipeline_run_id=s.knowledge_pipeline_run_id
			WHERE r.knowledge_object_id=$1 AND r.status NOT IN ('stale','cancelled') AND s.stage_key='embedding' AND s.status<>'skipped_by_policy'`, object.KnowledgeObjectID).Scan(&selected); err != nil {
			t.Fatal(err)
		}
		if selected != 1 {
			t.Fatalf("enabled embedding stages = %d", selected)
		}
		disabled := false
		status, err := service.UpdatePipelinePolicy(context.Background(), PipelinePolicyUpdate{EmbeddingsEnabled: &disabled})
		if err != nil {
			t.Fatal(err)
		}
		if status.Policy.EmbeddingsEnabled || status.EmbeddingSettings.Enabled {
			t.Fatalf("canonical disable diverged from compatibility settings: %#v", status)
		}
	})
}

func TestPolicyDisableStalesSupersededSameVersionArtifactsPostgres(t *testing.T) {
	db := openPipelinePostgres(t)
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	policy := PipelinePolicy{ImageDescriptionsEnabled: true}
	setPipelinePolicyForTest(t, db, policy)
	service, object := pipelineFixture(t, db, storagecatalog.FileClassImage, "image/png", "vision artifact", now)
	run, err := service.EnsurePipelineRun(context.Background(), object, policy, false, 100)
	if err != nil {
		t.Fatal(err)
	}
	completeRunForTest(t, db, run.KnowledgePipelineRunID)
	stages, err := service.store.ListPipelineStageRuns(context.Background(), run.KnowledgePipelineRunID)
	if err != nil {
		t.Fatal(err)
	}
	var vision PipelineStageRun
	for _, stage := range stages {
		if stage.StageKey == FilePipelineStageImageDescription {
			vision = stage
			break
		}
	}
	version, err := service.store.getKnowledgeObjectVersion(context.Background(), run.KnowledgeObjectVersionID)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := service.PrepareDerivedArtifact(run, vision, version, DerivedArtifactInput{
		ArtifactKind: ArtifactKindVisionDescription, SourceLocator: "document", Text: "described image",
		GeneratorKey: "test.vision", GeneratorVersion: "v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err = service.store.CreateDerivedArtifact(context.Background(), artifact)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.store.ActivateDerivedArtifact(context.Background(), artifact.KnowledgeDerivedArtifactID, run.Generation); err != nil {
		t.Fatal(err)
	}
	disabled := false
	if _, err = service.UpdatePipelinePolicy(context.Background(), PipelinePolicyUpdate{ImageDescriptionsEnabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	var activeOnStale, staleArtifact int
	if err = db.QueryRow(`SELECT count(*) FROM knowledge.derived_artifacts a
		JOIN knowledge.pipeline_runs r ON r.knowledge_pipeline_run_id=a.knowledge_pipeline_run_id
		WHERE a.knowledge_object_id=$1 AND a.active=true AND r.status IN ('stale','cancelled')`, object.KnowledgeObjectID).Scan(&activeOnStale); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM knowledge.derived_artifacts WHERE knowledge_derived_artifact_id=$1 AND active=false AND state='stale'`, artifact.KnowledgeDerivedArtifactID).Scan(&staleArtifact); err != nil {
		t.Fatal(err)
	}
	if activeOnStale != 0 || staleArtifact != 1 {
		t.Fatalf("superseded artifact state: active_on_stale=%d stale_artifact=%d", activeOnStale, staleArtifact)
	}
	status, err := service.GetPipelineOverallStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Operations.ActivationMismatches != 0 {
		t.Fatalf("derived artifact activation mismatches = %d", status.Operations.ActivationMismatches)
	}
}

func TestEmbeddingEnableSkipsCompletedNoChunkTrajectoryPostgres(t *testing.T) {
	db := openPipelinePostgres(t)
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	setPipelinePolicyForTest(t, db, PipelinePolicy{})
	service, object := pipelineFixture(t, db, storagecatalog.FileClassMarkdown, "text/markdown", "no embedding chunks", now)
	run, err := service.EnsurePipelineRun(context.Background(), object, PipelinePolicy{}, false, 100)
	if err != nil {
		t.Fatal(err)
	}
	completeRunForTest(t, db, run.KnowledgePipelineRunID)
	enabled, err := service.SetEmbeddingsEnabled(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if enabled.Queued != 0 {
		t.Fatalf("no-chunk embedding enable queued = %d", enabled.Queued)
	}
	var runCount int
	var maxGeneration int64
	var originalStatus string
	if err = db.QueryRow(`SELECT count(*),max(generation),max(status) FILTER (WHERE knowledge_pipeline_run_id=$2)
		FROM knowledge.pipeline_runs WHERE knowledge_object_id=$1`, object.KnowledgeObjectID, run.KnowledgePipelineRunID).Scan(&runCount, &maxGeneration, &originalStatus); err != nil {
		t.Fatal(err)
	}
	if runCount != 1 || maxGeneration != run.Generation || originalStatus != FilePipelineStatusComplete {
		t.Fatalf("no-chunk trajectory changed: count=%d generation=%d status=%s", runCount, maxGeneration, originalStatus)
	}
}

func TestPolicyTransitionAndWorkerPublicationShareLockOrderPostgres(t *testing.T) {
	db := openPipelinePostgres(t)
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	policy := PipelinePolicy{ImageDescriptionsEnabled: true}
	setPipelinePolicyForTest(t, db, policy)
	service, object := pipelineFixture(t, db, storagecatalog.FileClassImage, "image/png", "concurrent policy", now)
	run, err := service.EnsurePipelineRun(context.Background(), object, policy, false, 100)
	if err != nil {
		t.Fatal(err)
	}
	claim := claimSinglePipeline(t, service, PipelineExecutionCoordinator, "concurrent-worker", now)
	version, err := service.store.getKnowledgeObjectVersion(context.Background(), run.KnowledgeObjectVersionID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := service.PrepareDerivedArtifact(run, claim.Stage, version, DerivedArtifactInput{
		ArtifactKind: ArtifactKindMetadataText, SourceLocator: "document", Text: "worker publication",
		GeneratorKey: "test.concurrent", GeneratorVersion: "v1",
	})
	if err != nil {
		t.Fatal(err)
	}

	workerLocked := make(chan struct{})
	releaseWorker := make(chan struct{})
	workerResult := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		tx, txErr := db.BeginTx(ctx, nil)
		if txErr != nil {
			workerResult <- txErr
			return
		}
		defer tx.Rollback()
		if txErr = lockPipelineClaimTx(ctx, tx, claim); txErr != nil {
			workerResult <- txErr
			return
		}
		close(workerLocked)
		<-releaseWorker
		created, txErr := createDerivedArtifactTx(ctx, tx, prepared)
		if txErr == nil {
			_, txErr = activateDerivedArtifactTx(ctx, tx, created.KnowledgeDerivedArtifactID, run.Generation)
		}
		if txErr == nil {
			var result sql.Result
			result, txErr = tx.ExecContext(ctx, `UPDATE knowledge.knowledge_objects SET processing_state='text_extracted',updated_at=now() WHERE knowledge_object_id=$1`, object.KnowledgeObjectID)
			if txErr == nil {
				txErr = requireOneRow(result, "concurrent worker object publication")
			}
		}
		if txErr == nil {
			txErr = tx.Commit()
		}
		workerResult <- txErr
	}()
	select {
	case <-workerLocked:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not acquire the pipeline claim locks")
	}

	policyResult := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		disabled := false
		_, policyErr := service.UpdatePipelinePolicy(ctx, PipelinePolicyUpdate{ImageDescriptionsEnabled: &disabled})
		policyResult <- policyErr
	}()
	if err = waitForBlockedPolicyRunLock(db, 3*time.Second); err != nil {
		close(releaseWorker)
		t.Fatal(err)
	}
	close(releaseWorker)
	select {
	case err = <-workerResult:
		if err != nil {
			t.Fatalf("worker publication failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker publication timed out")
	}
	select {
	case err = <-policyResult:
		if err != nil {
			t.Fatalf("policy transition failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("policy transition timed out")
	}
	if _, err = service.CompletePipelineStage(context.Background(), claim, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("superseded worker completion error = %v", err)
	}
	var activeOnStale int
	if err = db.QueryRow(`SELECT count(*) FROM knowledge.derived_artifacts a
		JOIN knowledge.pipeline_runs r ON r.knowledge_pipeline_run_id=a.knowledge_pipeline_run_id
		WHERE a.knowledge_object_id=$1 AND a.active=true AND r.status IN ('stale','cancelled')`, object.KnowledgeObjectID).Scan(&activeOnStale); err != nil {
		t.Fatal(err)
	}
	if activeOnStale != 0 {
		t.Fatalf("concurrent publication left %d active stale artifacts", activeOnStale)
	}
}

func TestEnsurePipelineRunUsesLockedCurrentObjectPostgres(t *testing.T) {
	db := openPipelinePostgres(t)
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	service, stale := pipelineFixture(t, db, storagecatalog.FileClassMarkdown, "text/markdown", "stale caller", now)
	current := stale
	current.SourceRevision = "revision-2"
	current.SourceHash = hashArtifactValue("current pdf")
	current.FileClass = storagecatalog.FileClassPDF
	current.MimeType = "application/pdf"
	current.UpdatedAt = now.Add(time.Minute)
	var err error
	current, err = service.store.UpsertKnowledgeObject(context.Background(), current)
	if err != nil {
		t.Fatal(err)
	}
	policy := PipelinePolicy{PDFOCREnabled: true}
	run, err := service.EnsurePipelineRun(context.Background(), stale, policy, false, 100)
	if err != nil {
		t.Fatal(err)
	}
	wantPlan, err := CompilePipelinePlan(current, policy)
	if err != nil {
		t.Fatal(err)
	}
	if run.SourceRevision != current.SourceRevision || run.SourceHash != current.SourceHash ||
		run.PipelineDefinitionKey != wantPlan.DefinitionKey || run.PipelineDefinitionVersion != wantPlan.DefinitionVersion {
		t.Fatalf("run used stale caller identity: run=%#v current=%#v plan=%#v", run, current, wantPlan)
	}
	version, err := service.store.getKnowledgeObjectVersion(context.Background(), run.KnowledgeObjectVersionID)
	if err != nil {
		t.Fatal(err)
	}
	if version.SourceRevision != current.SourceRevision || version.SourceHash != current.SourceHash || version.FileClass != current.FileClass {
		t.Fatalf("version used stale caller identity: %#v", version)
	}
}

func TestPolicyReconcileRescansObjectAfterDiscoveryPostgres(t *testing.T) {
	db := openPipelinePostgres(t)
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	setPipelinePolicyForTest(t, db, PipelinePolicy{})
	service, object := pipelineFixture(t, db, storagecatalog.FileClassMarkdown, "text/markdown", "policy discovery", now)
	oldRun, err := service.EnsurePipelineRun(context.Background(), object, PipelinePolicy{}, false, 100)
	if err != nil {
		t.Fatal(err)
	}

	blocker, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	var ignored string
	if err = blocker.QueryRow(`SELECT knowledge_pipeline_run_id FROM knowledge.pipeline_runs WHERE knowledge_pipeline_run_id=$1 FOR UPDATE`, oldRun.KnowledgePipelineRunID).Scan(&ignored); err != nil {
		t.Fatal(err)
	}
	policyResult := make(chan struct {
		queued int
		err    error
	}, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		enabled := true
		_, queued, policyErr := service.updatePipelinePolicyAndReconcile(ctx, PipelinePolicyUpdate{ImageDescriptionsEnabled: &enabled})
		policyResult <- struct {
			queued int
			err    error
		}{queued: queued, err: policyErr}
	}()
	if err = waitForBlockedQueryContaining(db, "FROM knowledge.pipeline_runs r", 3*time.Second); err != nil {
		t.Fatal(err)
	}

	current := object
	current.SourceRevision = "revision-2"
	current.SourceHash = hashArtifactValue("current image")
	current.FileClass = storagecatalog.FileClassImage
	current.MimeType = "image/png"
	current.UpdatedAt = now.Add(time.Minute)
	current, err = service.store.UpsertKnowledgeObject(context.Background(), current)
	if err != nil {
		t.Fatal(err)
	}
	if err = blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-policyResult:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.queued != 1 {
			t.Fatalf("policy reconciliation queued %d trajectories, want 1", result.queued)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("policy reconciliation timed out")
	}
	var runID string
	if err = db.QueryRow(`SELECT knowledge_pipeline_run_id FROM knowledge.pipeline_runs
		WHERE knowledge_object_id=$1 AND status NOT IN ('stale','cancelled') ORDER BY generation DESC LIMIT 1`, current.KnowledgeObjectID).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	run, err := service.store.GetPipelineRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	wantPlan, err := CompilePipelinePlan(current, PipelinePolicy{ImageDescriptionsEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if run.SourceRevision != current.SourceRevision || run.SourceHash != current.SourceHash || run.PipelineDefinitionKey != wantPlan.DefinitionKey {
		t.Fatalf("policy trajectory did not use locked current object: run=%#v current=%#v", run, current)
	}
}

func TestSourceIdentityFencesAllWorkerPublicationPostgres(t *testing.T) {
	db := openPipelinePostgres(t)
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	policy := PipelinePolicy{EmbeddingsEnabled: true}
	setPipelinePolicyForTest(t, db, policy)
	service, object := pipelineFixture(t, db, storagecatalog.FileClassMarkdown, "text/markdown", "source fence", now)
	run, err := service.EnsurePipelineRun(context.Background(), object, policy, false, 100)
	if err != nil {
		t.Fatal(err)
	}
	version, chunk := createPipelineChunkForTest(t, db, service, object, run, "source fence chunk")
	positionPipelineAtStage(t, db, run.KnowledgePipelineRunID, FilePipelineStageEmbedding, now)
	claim := claimSinglePipeline(t, service, PipelineExecutionHeavy, "source-fence-worker", now)
	settings, err := service.store.GetEmbeddingSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	current := object
	current.SourceRevision = "revision-2"
	current.SourceHash = hashArtifactValue("advanced source")
	current.UpdatedAt = now.Add(time.Minute)
	if _, err = service.store.UpsertKnowledgeObject(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	text := "stale consolidated text"
	consolidated := DerivedArtifact{TextContent: &text, Metadata: json.RawMessage(`{}`)}
	vector := make([]float32, settings.Dimensions)
	checks := []struct {
		name string
		call func() error
	}{
		{"derived artifact", func() error {
			_, callErr := service.createAndActivatePipelineArtifact(context.Background(), claim, version, DerivedArtifactInput{ArtifactKind: ArtifactKindMetadataText, SourceLocator: "stale", Text: "stale", GeneratorKey: "test", GeneratorVersion: "v1"})
			return callErr
		}},
		{"lexical chunks and search", func() error {
			return service.publishConsolidatedLexical(context.Background(), claim, version, consolidated)
		}},
		{"generated embedding", func() error {
			return service.activateUnifiedChunkEmbedding(context.Background(), claim, chunk, settings, vector, "stale-input")
		}},
		{"reused embedding", func() error {
			_, callErr := service.reuseUnifiedChunkEmbedding(context.Background(), claim, chunk, settings)
			return callErr
		}},
		{"object state", func() error { return service.publishUnifiedEmbeddingObjectState(context.Background(), claim) }},
		{"observation", func() error {
			return service.storePipelineStageObservation(context.Background(), claim, HeavyStageObservation{Observed: json.RawMessage(`{"stale":true}`)})
		}},
		{"completion", func() error {
			_, callErr := service.CompletePipelineStage(context.Background(), claim, nil)
			return callErr
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if callErr := check.call(); !errors.Is(callErr, ErrConflict) {
				t.Fatalf("stale publication error = %v, want ErrConflict", callErr)
			}
		})
	}
	var artifactCount, chunkCount, vectorCount int
	if err = db.QueryRow(`SELECT count(*) FROM knowledge.derived_artifacts WHERE knowledge_pipeline_run_id=$1`, run.KnowledgePipelineRunID).Scan(&artifactCount); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM knowledge.knowledge_chunks WHERE knowledge_object_version_id=$1`, version.KnowledgeObjectVersionID).Scan(&chunkCount); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM knowledge.chunk_embeddings WHERE knowledge_object_id=$1`, object.KnowledgeObjectID).Scan(&vectorCount); err != nil {
		t.Fatal(err)
	}
	if artifactCount != 0 || chunkCount != 1 || vectorCount != 0 {
		t.Fatalf("stale publication mutated state: artifacts=%d chunks=%d vectors=%d", artifactCount, chunkCount, vectorCount)
	}
}

func TestEmbeddingPublicationSerializesWithSupersessionPostgres(t *testing.T) {
	db := openPipelinePostgres(t)
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)

	t.Run("generated vector commits before policy supersession", func(t *testing.T) {
		policy := PipelinePolicy{EmbeddingsEnabled: true}
		setPipelinePolicyForTest(t, db, policy)
		service, object := pipelineFixture(t, db, storagecatalog.FileClassMarkdown, "text/markdown", "generated race", now)
		run, err := service.EnsurePipelineRun(context.Background(), object, policy, false, 100)
		if err != nil {
			t.Fatal(err)
		}
		_, chunk := createPipelineChunkForTest(t, db, service, object, run, "generated race chunk")
		positionPipelineAtStage(t, db, run.KnowledgePipelineRunID, FilePipelineStageEmbedding, now)
		claim := claimSinglePipeline(t, service, PipelineExecutionHeavy, "generated-race-worker", now)
		settings, err := service.store.GetEmbeddingSettings(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		seedID := seedActiveChunkEmbeddingForTest(t, db, chunk, settings, run.Generation, "seed")

		blocker, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback()
		var ignored string
		if err = blocker.QueryRow(`SELECT knowledge_chunk_embedding_id FROM knowledge.chunk_embeddings WHERE knowledge_chunk_embedding_id=$1 FOR UPDATE`, seedID).Scan(&ignored); err != nil {
			t.Fatal(err)
		}
		publication := make(chan error, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			publication <- service.activateUnifiedChunkEmbedding(ctx, claim, chunk, settings, make([]float32, settings.Dimensions), hashArtifactValue("generated"))
		}()
		if err = waitForBlockedQueryContaining(db, "UPDATE knowledge.chunk_embeddings", 3*time.Second); err != nil {
			t.Fatal(err)
		}
		policyResult := make(chan error, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			disabled := false
			_, policyErr := service.UpdatePipelinePolicy(ctx, PipelinePolicyUpdate{EmbeddingsEnabled: &disabled})
			policyResult <- policyErr
		}()
		if err = waitForBlockedQueryContaining(db, "FROM knowledge.pipeline_runs r", 3*time.Second); err != nil {
			t.Fatal(err)
		}
		if err = blocker.Commit(); err != nil {
			t.Fatal(err)
		}
		select {
		case err = <-publication:
			if err != nil {
				t.Fatalf("generated publication failed: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("generated publication timed out")
		}
		select {
		case err = <-policyResult:
			if err != nil {
				t.Fatalf("policy supersession failed: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("policy supersession timed out")
		}
		var activeGenerated, staleRuns int
		if err = db.QueryRow(`SELECT count(*) FROM knowledge.chunk_embeddings WHERE knowledge_chunk_id=$1 AND active=true AND input_hash=$2`, chunk.KnowledgeChunkID, hashArtifactValue("generated")).Scan(&activeGenerated); err != nil {
			t.Fatal(err)
		}
		if err = db.QueryRow(`SELECT count(*) FROM knowledge.pipeline_runs WHERE knowledge_pipeline_run_id=$1 AND status='stale'`, run.KnowledgePipelineRunID).Scan(&staleRuns); err != nil {
			t.Fatal(err)
		}
		if activeGenerated != 1 || staleRuns != 1 {
			t.Fatalf("serialized generated publication state: active=%d stale_runs=%d", activeGenerated, staleRuns)
		}
	})

	t.Run("reused vector loses source replacement fence", func(t *testing.T) {
		policy := PipelinePolicy{EmbeddingsEnabled: true}
		setPipelinePolicyForTest(t, db, policy)
		service, object := pipelineFixture(t, db, storagecatalog.FileClassMarkdown, "text/markdown", "reuse race", now.Add(time.Second))
		run, err := service.EnsurePipelineRun(context.Background(), object, policy, false, 100)
		if err != nil {
			t.Fatal(err)
		}
		_, chunk := createPipelineChunkForTest(t, db, service, object, run, "reuse race chunk")
		positionPipelineAtStage(t, db, run.KnowledgePipelineRunID, FilePipelineStageEmbedding, now)
		claim := claimSinglePipeline(t, service, PipelineExecutionHeavy, "reuse-race-worker", now)
		settings, err := service.store.GetEmbeddingSettings(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		seedActiveChunkEmbeddingForTest(t, db, chunk, settings, run.Generation, "reusable-source")
		var before int
		if err = db.QueryRow(`SELECT count(*) FROM knowledge.chunk_embeddings WHERE knowledge_chunk_id=$1`, chunk.KnowledgeChunkID).Scan(&before); err != nil {
			t.Fatal(err)
		}

		current := object
		current.SourceRevision = "revision-2"
		current.SourceHash = hashArtifactValue("reuse race current")
		current.UpdatedAt = now.Add(time.Minute)
		current, err = service.store.UpsertKnowledgeObject(context.Background(), current)
		if err != nil {
			t.Fatal(err)
		}
		blocker, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback()
		var ignored string
		if err = blocker.QueryRow(`SELECT knowledge_object_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1 FOR UPDATE`, object.KnowledgeObjectID).Scan(&ignored); err != nil {
			t.Fatal(err)
		}
		reuseResult := make(chan error, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, reuseErr := service.reuseUnifiedChunkEmbedding(ctx, claim, chunk, settings)
			reuseResult <- reuseErr
		}()
		if err = waitForBlockedQueryContaining(db, "SELECT source_hash,source_revision", 3*time.Second); err != nil {
			t.Fatal(err)
		}
		ensureResult := make(chan struct {
			run PipelineRun
			err error
		}, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			replacement, ensureErr := service.EnsurePipelineRun(ctx, current, policy, false, 100)
			ensureResult <- struct {
				run PipelineRun
				err error
			}{run: replacement, err: ensureErr}
		}()
		if err = waitForBlockedQueryContaining(db, "FROM knowledge.pipeline_runs r", 3*time.Second); err != nil {
			t.Fatal(err)
		}
		if err = blocker.Commit(); err != nil {
			t.Fatal(err)
		}
		select {
		case err = <-reuseResult:
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("reused publication error = %v, want ErrConflict", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("reused publication timed out")
		}
		select {
		case result := <-ensureResult:
			if result.err != nil {
				t.Fatal(result.err)
			}
			if result.run.SourceRevision != current.SourceRevision || result.run.SourceHash != current.SourceHash || result.run.Generation <= run.Generation {
				t.Fatalf("replacement run = %#v", result.run)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("source replacement timed out")
		}
		var after int
		if err = db.QueryRow(`SELECT count(*) FROM knowledge.chunk_embeddings WHERE knowledge_chunk_id=$1`, chunk.KnowledgeChunkID).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if after != before {
			t.Fatalf("stale reusable publication inserted a vector: before=%d after=%d", before, after)
		}
	})
}

func createPipelineChunkForTest(t *testing.T, db *sql.DB, service *Service, object KnowledgeObject, run PipelineRun, text string) (KnowledgeObjectVersion, KnowledgeChunk) {
	t.Helper()
	version, err := service.store.getKnowledgeObjectVersion(context.Background(), run.KnowledgeObjectVersionID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := service.replaceKnowledgeChunksTx(context.Background(), tx, object, version, ChunkTextDocument(TextDocument{Text: text}, ChunkerOptions{}))
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 {
		t.Fatalf("created %d chunks, want 1", len(chunks))
	}
	return version, chunks[0]
}

func seedActiveChunkEmbeddingForTest(t *testing.T, db *sql.DB, chunk KnowledgeChunk, settings EmbeddingSettings, generation int64, inputHash string) string {
	t.Helper()
	id := newKnowledgeChunkEmbeddingID()
	inputHash = hashArtifactValue(inputHash)
	if _, err := db.Exec(`INSERT INTO knowledge.chunk_embeddings (
		knowledge_chunk_embedding_id,knowledge_chunk_id,knowledge_object_id,knowledge_object_version_id,
		embedding_runtime_model_id,runtime_key,model_key,dimensions,distance_metric,chunk_hash,chunker_version,
		input_hash,source_generation,embedding,status,active,metadata,created_at,activated_at)
		VALUES ($1,$2,$3,$4,'embedding_runtime_model_ollama_mxbai_embed_large_1024',$5,$6,$7,$8,$9,$10,$11,$12,
		array_fill(0::real,ARRAY[$7::integer])::vector,'active',true,'{}',now(),now())`,
		id, chunk.KnowledgeChunkID, chunk.KnowledgeObjectID, chunk.KnowledgeObjectVersionID,
		settings.RuntimeKey, settings.ModelKey, settings.Dimensions, settings.DistanceMetric,
		chunk.ChunkHash, chunk.ChunkerVersion, inputHash, generation); err != nil {
		t.Fatal(err)
	}
	return id
}

func waitForBlockedQueryContaining(db *sql.DB, fragment string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var waiting bool
		err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity
			WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock'
			  AND position($1 in query)>0)`, fragment).Scan(&waiting)
		if err != nil {
			return err
		}
		if waiting {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("query containing %q did not block within %s", fragment, timeout)
}

func waitForBlockedPolicyRunLock(db *sql.DB, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var waiting bool
		err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity
			WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock'
			  AND query LIKE '%knowledge.pipeline_runs%')`).Scan(&waiting)
		if err != nil {
			return err
		}
		if waiting {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("policy transition did not block on the worker-owned run within %s", timeout)
}

func boolPointer(value bool) *bool { return &value }

func (s *Service) getOrCreateKnowledgeObjectVersionTxForTest(ctx context.Context, object KnowledgeObject) (KnowledgeObjectVersion, error) {
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return KnowledgeObjectVersion{}, err
	}
	defer tx.Rollback()
	version, err := s.getOrCreateKnowledgeObjectVersionTx(ctx, tx, object, json.RawMessage(`{"schema_version":"test"}`))
	if err != nil {
		return KnowledgeObjectVersion{}, err
	}
	if err = tx.Commit(); err != nil {
		return KnowledgeObjectVersion{}, err
	}
	return version, nil
}
