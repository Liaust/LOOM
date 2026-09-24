package knowledge

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDeclarationSourcePolicyEnrollmentPostgres(t *testing.T) {
	f := declarationEnrollmentDB(t, "refresh: {quiet_for: 1m, max_wait: 3m}, processing: {ocr: off, embeddings: false, image_descriptions: false}")
	f.report(f.analysis.Report.WatchedRoots[0])
	object := f.publish(t)
	policy, err := knowledgeSourcePolicy(object)
	if err != nil || policy == nil || policy.Refresh == nil || policy.Refresh.QuietForSeconds != 60 || policy.Processing.Embeddings == nil || *policy.Processing.Embeddings {
		t.Fatalf("declared policy did not reach admitted object: %+v %v", policy, err)
	}
	run, err := f.service.EnsurePipelineRun(t.Context(), object, DefaultPipelinePolicy(), false, 100)
	if err != nil || run.Status != FilePipelineStatusWaitingQuietWindow || run.CurrentStageKey != FilePipelineStageMetadata || run.QuietWindowEligibleAt == nil {
		t.Fatalf("initial debounce: %+v %v", run, err)
	}
	deadline := *run.QuietWindowEligibleAt
	clock := objectRefreshClock(object)
	if clock.PendingSince == nil || !deadline.Equal(clock.LastContentChangeAt.Add(time.Minute)) {
		t.Fatalf("admission clock: %+v deadline=%s", clock, deadline)
	}
	// Repeated discovery does not move the deadline; a new service reads it
	// from PostgreSQL rather than starting a fresh in-memory quiet interval.
	for i := 0; i < 3; i++ {
		object, err = f.service.store.UpsertKnowledgeObject(t.Context(), object)
		if err != nil {
			t.Fatal(err)
		}
	}
	restarted := NewService(f.db, WithClock(func() time.Time { return deadline }))
	replayed, err := restarted.EnsurePipelineRun(t.Context(), object, DefaultPipelinePolicy(), false, 100)
	if err != nil || replayed.KnowledgePipelineRunID != run.KnowledgePipelineRunID || !replayed.QuietWindowEligibleAt.Equal(deadline) {
		t.Fatalf("poll/restart changed pending run: %+v %v", replayed, err)
	}
	before, err := restarted.ClaimPipelineRuns(t.Context(), PipelineExecutionCoordinator, "source-policy", PipelineClaimOptions{Limit: 1, Now: deadline.Add(-time.Second)})
	if err != nil || len(before) != 0 {
		t.Fatalf("claimed before quiet deadline: %+v %v", before, err)
	}
	item := claimSinglePipeline(t, restarted, PipelineExecutionCoordinator, "source-policy", deadline)
	if item.Stage.StageKey != FilePipelineStageMetadata || objectRefreshClock(item.Object).PendingSince != nil {
		t.Fatal("selection did not consume the pending clock")
	}
	var plan CompiledPipelinePlan
	if err := json.Unmarshal(item.Run.PlanSnapshot, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.SourcePolicy == nil || plan.EffectivePolicy == nil || plan.EffectivePolicy.EmbeddingsEnabled {
		t.Fatalf("policy not persisted in inspectable plan: %+v", plan)
	}
	for _, stage := range plan.Stages {
		if stage.StageKey != FilePipelineStageMetadata && stage.QuietWindow {
			t.Fatalf("quiet window reapplied at %s", stage.StageKey)
		}
		if stage.StageKey == FilePipelineStageEmbedding && (stage.Selected || stage.SkipReason != "disabled_by_source") {
			t.Fatalf("embedding opt-out lost: %+v", stage)
		}
	}
	if _, err := restarted.CompletePipelineStage(t.Context(), item, nil); err != nil {
		t.Fatal(err)
	}
	// The next stage is immediately claimable, including after another restart.
	next := claimSinglePipeline(t, NewService(f.db), PipelineExecutionCoordinator, "source-policy-next", deadline)
	if next.Stage.StageKey == FilePipelineStageMetadata {
		t.Fatal("metadata was replayed")
	}
}
