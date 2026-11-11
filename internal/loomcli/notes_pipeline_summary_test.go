package loomcli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/response"
)

func TestNotesPipelineSummaryAttentionFreshnessAndFollowups(t *testing.T) {
	admitted, readable, excluded := true, true, false
	now := time.Date(2026, 10, 12, 20, 0, 0, 0, time.UTC)
	old := now.Add(-7 * 24 * time.Hour)
	run := knowledge.PipelineRunProgress{PipelineRunID: "knowledge_pipeline_run_ABC123", ObjectID: "knowledge_object_DEF456",
		Status: knowledge.FilePipelineStatusWaitingHeavy, CurrentStage: "embedding", LastChangedAt: old,
		Source: &knowledge.PipelineSourceStatus{ClaimSourceEligible: &excluded, Readable: &excluded, Reason: "excluded_by_current_declaration"},
		Stages: []knowledge.PipelineStageProgress{{StageKey: "embedding", Attempts: 0}}}
	status := knowledge.PipelineOverallStatus{Progress: &knowledge.PipelineProgress{ObservedAt: now, CurrentKnown: true,
		Runs: []knowledge.PipelineRunProgress{run}, Workers: []knowledge.PipelineWorkerProgress{{WorkerKey: "main.knowledge_heavy", Status: "succeeded", FinishedAt: &old, RecentIntervalsSeconds: []float64{900, 900}}}},
		NativePublication: json.RawMessage(`{"selection_scope":"selected","inventory":[{"counters_known":false}]}`)}
	summary := summarizePipelineStatus(status)
	if summary.Runs[0].Activity != "not_admitted" || summary.Runs[0].Attention != "none" || summary.Runs[0].SourceCommand != "" || summary.Counts["not_admitted"] != 1 {
		t.Fatalf("excluded history treated as blocked work: %+v", summary)
	}
	if summary.Workers[0].AgeSeconds == nil || *summary.Workers[0].AgeSeconds != 604800 || summary.Native.PendingText != nil || summary.Native.CountersKnown {
		t.Fatalf("freshness/unknown counters misrepresented: %+v", summary)
	}
	if summary.Runs[0].InspectCommand != "loom notes pipelines inspect knowledge_pipeline_run_ABC123 --summary --json" {
		t.Fatalf("wrong typed pipeline follow-up: %+v", summary.Runs[0])
	}
	run.Source = &knowledge.PipelineSourceStatus{ClaimSourceEligible: &admitted, Readable: &readable, Reason: "eligible"}
	if got := summarizePipelineRun(run); got.SourceCommand != "loom notes objects show knowledge_object_DEF456 --source-lifecycle all --json" || got.Activity != "waiting" || got.Attention != "unknown" {
		t.Fatalf("wrong typed source follow-up: %+v", got)
	}
	run.Source = nil
	if got := summarizePipelineRun(run); got.Reason != "current_source_eligibility_unknown" || got.Attention != "unknown" {
		t.Fatalf("missing evidence treated as healthy: %+v", got)
	}
	run.Status, run.Reason = knowledge.FilePipelineStatusFailed, "embedding_transport_failed"
	if got := summarizePipelineRun(run); got.Activity != "blocked" || got.Attention != "required" || got.Reason != run.Reason {
		t.Fatalf("source uncertainty hid the execution failure: %+v", got)
	}
	run.Source = &knowledge.PipelineSourceStatus{ClaimSourceEligible: &excluded, Readable: &excluded, Reason: "custody_or_project_paused"}
	if got := summarizePipelineRun(run); got.Activity != "not_admitted" || got.Attention != "unknown" || got.Reason != run.Source.Reason || got.RecordedFailure != run.Reason {
		t.Fatalf("inactive retained failure presented as currently actionable: %+v", got)
	}
	run.Source.Reason = "source_withdrawn"
	if got := summarizePipelineRun(run); got.Attention != "none" || got.RecordedFailure != run.Reason {
		t.Fatalf("withdrawn failure erased or presented as active: %+v", got)
	}
	run.Status = knowledge.FilePipelineStatusProcessing
	run.Source = summary.Runs[0].Source
	if got := summarizePipelineRun(run); got.Activity != "running" {
		t.Fatal("source change hid an already-running execution")
	}
	run.PipelineRunID, run.ObjectID = "knowledge_pipeline_run_X;rm", "knowledge_object_X --other"
	if got := summarizePipelineRun(run); got.InspectCommand != "" || got.SourceCommand != "" {
		t.Fatal("unsafe identity interpolated in follow-up")
	}
}

func TestNotesPipelineSummaryCompletedIsNotHistorical(t *testing.T) {
	for _, status := range []string{knowledge.FilePipelineStatusComplete, knowledge.FilePipelineStatusCompleteWithWarning} {
		got := summarizePipelineRun(knowledge.PipelineRunProgress{Status: status})
		if got.Activity != "completed" {
			t.Fatalf("status=%s activity=%s", status, got.Activity)
		}
	}
}

func TestNotesPipelineInspectSummaryShowsBoundedActualStageOutcomes(t *testing.T) {
	detail := knowledge.PipelineInspect{
		Run: knowledge.PipelineRun{Status: knowledge.FilePipelineStatusComplete, PlanSnapshot: json.RawMessage(`{"enrichment":{"requested":{"embeddings":true},"retained":{"ocr":true}}}`)},
		Stages: []knowledge.PipelineStageRun{
			{StageKey: "pdf_ocr", Status: "complete", OutputArtifactCount: 2, Metadata: json.RawMessage(`{"private_body":"do-not-print"}`)},
			{StageKey: "embedding", Status: "complete", AttemptCount: 1},
			{StageKey: "image_description", Status: "skipped_by_policy", Metadata: json.RawMessage(`{"skip_reason":"disabled_by_policy"}`)},
		},
	}
	summary := summarizePipelineInspect(detail)
	if len(summary.Stages) != 3 || summary.Stages[0].Key != "pdf_ocr" || summary.Stages[0].Status != "complete" || summary.Stages[0].Artifacts != 2 || summary.Stages[1].Attempts != 1 || summary.Stages[2].SkipReason != "disabled_by_policy" || summary.Refresh == nil {
		t.Fatalf("actual stage outcomes missing: %+v", summary)
	}
	if summary.SavedPlanInputs == nil || !summary.SavedPlanInputs.Retained.OCR || summary.SavedPlanInputs.Retained.Embeddings || !summary.SavedPlanInputs.Requested.Embeddings {
		t.Fatal("saved-plan inputs were confused with completed output inventory")
	}
	raw, err := json.Marshal(summary)
	if err != nil || strings.Contains(string(raw), "do-not-print") || strings.Contains(string(raw), "plan_snapshot") || strings.Contains(string(raw), "reused") {
		t.Fatalf("raw stage/plan leaked or reuse inferred: %s %v", raw, err)
	}
	for i := 0; i < 30; i++ {
		detail.Stages = append(detail.Stages, detail.Stages[0])
	}
	summary = summarizePipelineInspect(detail)
	if len(summary.Stages) != pipelineSummaryStageLimit || summary.OmittedStages != len(detail.Stages)-pipelineSummaryStageLimit {
		t.Fatalf("stage bound/omission lost: %+v", summary)
	}
	if len(summarizePipelineRun(knowledge.PipelineRunProgress{}).Stages) != 0 {
		t.Fatal("global queue summaries gained detailed stage inventories")
	}
}

func TestNotesPipelineSummaryBoundsAndUnknownText(t *testing.T) {
	p := &knowledge.PipelineProgress{CurrentKnown: true}
	for i := 0; i < 100; i++ {
		p.Runs = append(p.Runs, knowledge.PipelineRunProgress{Status: knowledge.FilePipelineStatusWaitingHeavy})
	}
	summary := summarizePipelineStatus(knowledge.PipelineOverallStatus{Progress: p})
	if len(summary.Runs) != pipelineSummaryRunLimit || summary.OmittedRuns != 90 || summary.Counts["waiting"] != 100 {
		t.Fatalf("wrong bounded-window count: %+v", summary)
	}
	var out bytes.Buffer
	cmd := newNotesPipelineStatusCommand(&options{})
	cmd.SetOut(&out)
	renderPipelineStatusSummary(cmd, summarizePipelineStatus(knowledge.PipelineOverallStatus{}))
	if !strings.Contains(out.String(), "Selected queue: unknown") || strings.Contains(out.String(), "waiting=0") {
		t.Fatalf("unknown presented as zero: %s", out.String())
	}
}

func TestNotesPipelineSummaryNativePublicationCounters(t *testing.T) {
	status := knowledge.PipelineOverallStatus{Progress: &knowledge.PipelineProgress{}, NativePublication: json.RawMessage(`{
 "selection_scope":"last_committed_worker_checkpoint", "inventory":[
 {"counters_known":true,"counts":{"text_pending":2,"asset_pending":1,"excluded":3},"text_published":10,"asset_published":4},
 {"counters_known":true,"counts":{"text_pending":1,"asset_pending":0,"excluded":2},"text_published":5,"asset_published":0}
 ]}`)}
	native := summarizePipelineStatus(status).Native
	if native == nil || !native.CountersKnown || native.Collections != 2 ||
		native.PendingText == nil || *native.PendingText != 3 || native.PendingAssets == nil || *native.PendingAssets != 1 ||
		native.PublishedText == nil || *native.PublishedText != 15 || native.PublishedAssets == nil || *native.PublishedAssets != 4 ||
		native.Excluded == nil || *native.Excluded != 5 {
		t.Fatalf("wrong observed publication counters: %+v", native)
	}
	status.NativePublication = json.RawMessage(`{"inventory":[
 {"counters_known":true,"counts":{"excluded":2},"text_published":10,"asset_published":4},
 {"counters_known":true,"counts":{"excluded":1},"text_published":null,"asset_published":null}
 ]}`)
	summary := summarizePipelineStatus(status)
	if summary.Native.PublishedText != nil || summary.Native.PublishedAssets != nil || summary.Native.Excluded == nil || *summary.Native.Excluded != 3 {
		t.Fatalf("legacy publication uncertainty lost: %+v", summary.Native)
	}
	var out bytes.Buffer
	cmd := newNotesPipelineStatusCommand(&options{})
	cmd.SetOut(&out)
	renderPipelineStatusSummary(cmd, summary)
	if !strings.Contains(out.String(), "text=unknown assets=unknown excluded=3") {
		t.Fatalf("unknown publication shown as zero: %s", out.String())
	}
	for _, payload := range []string{`{"inventory":[]}`, `{"inventory":[{"counters_known":false,"text_published":4,"asset_published":2}]}`} {
		status.NativePublication = json.RawMessage(payload)
		native = summarizePipelineStatus(status).Native
		if native.CountersKnown || native.PendingText != nil || native.PendingAssets != nil || native.PublishedText != nil || native.PublishedAssets != nil || native.Excluded != nil {
			t.Fatalf("missing counter evidence shown as known: %+v", native)
		}
	}
}

func TestNotesPipelineSummaryCLIIsSingleCompactJSONAndHumanDefault(t *testing.T) {
	worker := notesPipelineWorkerReceiptFixture()
	status := knowledge.PipelineOverallStatus{Current: []knowledge.PipelineRun{{PlanSnapshot: json.RawMessage(`{"private_large_plan":"sentinel"}`)}},
		Progress: &knowledge.PipelineProgress{CurrentKnown: true, ObservedAt: worker.FinishedAt.Add(time.Minute), Runs: []knowledge.PipelineRunProgress{},
			Workers: []knowledge.PipelineWorkerProgress{worker, {WorkerKey: "main.knowledge_heavy", RunID: "worker_run_OLD", Status: "succeeded"}}},
		Counts: map[string]int{}, Operations: knowledge.PipelineOperationsStatus{}}
	socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.RequestURI() != "/v1/knowledge/notes/pipelines/status" {
			t.Errorf("unexpected request: %s", req.URL.RequestURI())
		}
		response.WriteJSON(w, http.StatusOK, response.Success("summary", status))
	})
	defer stop()
	out, _, err := executeRootCommand("--json", "--socket", socket, "notes", "pipelines", "status", "--summary")
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(strings.NewReader(out))
	var summary pipelineStatusSummary
	if err = decoder.Decode(&summary); err != nil || summary.SchemaVersion != "knowledge.pipeline_summary.v1" || strings.Contains(out, "sentinel") {
		t.Fatalf("not compact: %s %v", out, err)
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		t.Fatal("multiple JSON results")
	}
	if len(summary.Workers) != 2 {
		t.Fatalf("workers dropped from compact JSON: %s", out)
	}
	w := summary.Workers[0]
	if w.LastRun != worker.RunID || w.ActiveRun != worker.CurrentRunID || w.LastStatus != worker.Status ||
		w.StartedAt == nil || !w.StartedAt.Equal(*worker.StartedAt) || w.FinishedAt == nil || !w.FinishedAt.Equal(*worker.FinishedAt) ||
		!reflect.DeepEqual(w.AdmissionTimedOut, worker.AdmissionTimedOut) || !reflect.DeepEqual(w.AdmissionWarning, worker.AdmissionWarning) ||
		summary.Workers[1].AdmissionTimedOut != nil || !strings.Contains(out, `"admission_timed_out":null`) {
		t.Fatalf("compact JSON receipt differs from API: %s", out)
	}
	for _, mode := range []string{"", "--summary", "--details"} {
		args := []string{"--socket", socket, "notes", "pipelines", "status"}
		if mode != "" {
			args = append(args, mode)
		}
		out, _, err = executeRootCommand(args...)
		if err != nil || strings.Contains(out, "sentinel") {
			t.Fatalf("human %s failed or leaked: %s %v", mode, out, err)
		}
		for _, want := range []string{"Worker main.knowledge_indexer: current_run=worker_run_CURRENT; last run: succeeded; admission incomplete",
			"run=worker_run_SAVED", "started=2026-10-13T12:00:00Z", "finished=2026-10-13T12:00:10Z",
			"phase=candidate_reconciliation reason=deadline_exceeded elapsed=805ms remaining_budget_at_phase_entry=590ms retryable=true",
			"admission unknown (unreported)"} {
			if !strings.Contains(out, want) {
				t.Fatalf("human %s missing %q: %s", mode, want, out)
			}
		}
	}
}

func notesPipelineWorkerReceiptFixture() knowledge.PipelineWorkerProgress {
	started := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	finished := started.Add(10 * time.Second)
	timedOut := true
	return knowledge.PipelineWorkerProgress{WorkerKey: "main.knowledge_indexer", RunID: "worker_run_SAVED", CurrentRunID: "worker_run_CURRENT",
		Status: "succeeded", StartedAt: &started, FinishedAt: &finished, AdmissionTimedOut: &timedOut, CounterScope: "last_finished_turn",
		Counters:         map[string]int64{"completed": 3},
		AdmissionWarning: &knowledge.AdmissionPhaseWarning{Phase: "candidate_reconciliation", Reason: "deadline_exceeded", ElapsedMS: 805, BudgetMS: 590, Retryable: true}}
}

func TestNotesPipelineSummaryWorkerQualifications(t *testing.T) {
	worker := notesPipelineWorkerReceiptFixture()
	complete := false
	for _, tc := range []struct {
		name    string
		timeout *bool
		warning *knowledge.AdmissionPhaseWarning
		want    []string
	}{
		{name: "missing phase", timeout: worker.AdmissionTimedOut, want: []string{"last run: succeeded; admission incomplete", "phase detail unavailable"}},
		{name: "historical unknown", want: []string{"admission unknown (unreported)"}},
		{name: "complete", timeout: &complete, want: []string{"admission timeout: no"}},
		{name: "unsafe warning", timeout: worker.AdmissionTimedOut,
			warning: &knowledge.AdmissionPhaseWarning{Phase: "private-sentinel", Reason: "private-sentinel", ElapsedMS: -3, BudgetMS: -5},
			want:    []string{"admission incomplete", "phase=unknown reason=unknown elapsed=0ms remaining_budget_at_phase_entry=0ms"}},
		{name: "warning with unknown timeout", warning: worker.AdmissionWarning, want: []string{"admission incomplete", "phase=candidate_reconciliation"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			worker.AdmissionTimedOut, worker.AdmissionWarning = tc.timeout, tc.warning
			status := knowledge.PipelineOverallStatus{Progress: &knowledge.PipelineProgress{Workers: []knowledge.PipelineWorkerProgress{worker}}}
			summary := summarizePipelineStatus(status)
			raw, err := json.Marshal(summary)
			if err != nil || strings.Contains(string(raw), "private-sentinel") {
				t.Fatalf("unsafe summary: %s %v", raw, err)
			}
			for _, detailed := range []bool{false, true} {
				cmd, out, _ := bufferedCommand()
				if detailed {
					renderPipelineProgress(cmd, status)
				} else {
					renderPipelineStatusSummary(cmd, summary)
				}
				for _, want := range tc.want {
					if !strings.Contains(out.String(), want) {
						t.Fatalf("missing %q: %s", want, out.String())
					}
				}
				if strings.Contains(out.String(), "private-sentinel") || tc.warning == nil && tc.timeout != nil && !*tc.timeout && strings.Contains(out.String(), "admission incomplete") {
					t.Fatalf("unsafe or stale qualification: %s", out.String())
				}
			}
		})
	}
}

func TestNotesPipelineSummaryNewerSuccessClearsLatestWarning(t *testing.T) {
	worker := notesPipelineWorkerReceiptFixture()
	status := knowledge.PipelineOverallStatus{Progress: &knowledge.PipelineProgress{Workers: []knowledge.PipelineWorkerProgress{worker}}}
	older := summarizePipelineStatus(status)
	complete := false
	worker.RunID, worker.CurrentRunID = "worker_run_NEW", ""
	worker.AdmissionTimedOut, worker.AdmissionWarning = &complete, nil
	started, finished := worker.StartedAt.Add(time.Minute), worker.FinishedAt.Add(time.Minute)
	worker.StartedAt, worker.FinishedAt = &started, &finished
	status.Progress.Workers[0] = worker
	latest := summarizePipelineStatus(status).Workers[0]
	if latest.LastRun != "worker_run_NEW" || latest.ActiveRun != "" || latest.AdmissionTimedOut == nil || *latest.AdmissionTimedOut || latest.AdmissionWarning != nil ||
		!latest.StartedAt.Equal(started) || !latest.FinishedAt.Equal(finished) || older.Workers[0].LastRun != "worker_run_SAVED" || older.Workers[0].AdmissionWarning == nil {
		t.Fatalf("new/old run evidence mixed: latest=%+v older=%+v", latest, older.Workers[0])
	}
}
