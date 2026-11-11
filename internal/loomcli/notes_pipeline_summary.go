package loomcli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/notesworkspacesync"
)

const pipelineSummaryRunLimit = 10
const pipelineSummaryStageLimit = 16

type pipelineStageSummary struct {
	Key        string `json:"stage_key"`
	Status     string `json:"status"`
	Attempts   int    `json:"attempts"`
	Artifacts  int    `json:"output_artifacts"`
	SkipReason string `json:"skip_reason,omitempty"`
}

type pipelineRunSummary struct {
	PipelineID      string                                 `json:"pipeline_id"`
	ObjectID        string                                 `json:"knowledge_object_id"`
	Stage           string                                 `json:"stage"`
	Status          string                                 `json:"recorded_status"`
	Activity        string                                 `json:"activity"`
	Attention       string                                 `json:"attention"`
	Reason          string                                 `json:"reason"`
	RecordedFailure string                                 `json:"recorded_failure,omitempty"`
	LastChangedAt   time.Time                              `json:"last_changed_at"`
	LastProgressAt  *time.Time                             `json:"last_progress_at,omitempty"`
	NextEligibleAt  *time.Time                             `json:"next_eligible_at,omitempty"`
	Attempts        int                                    `json:"stage_attempts"`
	Source          *knowledge.PipelineSourceStatus        `json:"source,omitempty"`
	Publication     *knowledge.PipelineProgressPublication `json:"publication,omitempty"`
	InspectCommand  string                                 `json:"inspect_command,omitempty"`
	SourceCommand   string                                 `json:"source_command,omitempty"`
	Stages          []pipelineStageSummary                 `json:"stages,omitempty"`
	OmittedStages   int                                    `json:"omitted_stages,omitempty"`
	Refresh         *knowledge.PipelineRefreshStatus       `json:"refresh,omitempty"`
	SavedPlanInputs *enrichmentOutputsView                 `json:"saved_plan_inputs,omitempty"`
}

type pipelineWorkerSummary struct {
	Key                    string                           `json:"worker_key"`
	ActiveRun              string                           `json:"active_run_id,omitempty"`
	LastRun                string                           `json:"last_run_id,omitempty"`
	LastStatus             string                           `json:"last_run_status,omitempty"`
	StartedAt              *time.Time                       `json:"last_run_started_at,omitempty"`
	FinishedAt             *time.Time                       `json:"last_run_finished_at,omitempty"`
	ObservedAt             *time.Time                       `json:"last_run_observed_at,omitempty"`
	AgeSeconds             *float64                         `json:"observation_age_seconds"`
	AdmissionTimedOut      *bool                            `json:"admission_timed_out"`
	AdmissionWarning       *knowledge.AdmissionPhaseWarning `json:"admission_warning,omitempty"`
	RecentIntervalsSeconds []float64                        `json:"recent_intervals_seconds,omitempty"`
}

type pipelineNativeSummary struct {
	Scope           string `json:"scope"`
	Collections     int    `json:"observed_collections"`
	CountersKnown   bool   `json:"counters_known"`
	PendingText     *int64 `json:"pending_text"`
	PendingAssets   *int64 `json:"pending_assets"`
	PublishedText   *int64 `json:"published_text"`
	PublishedAssets *int64 `json:"published_assets"`
	Excluded        *int64 `json:"excluded"`
}

type pipelineStatusSummary struct {
	SchemaVersion string                  `json:"schema_version"`
	ObservedAt    time.Time               `json:"observed_at"`
	Scope         string                  `json:"scope"`
	Known         bool                    `json:"queue_window_known"`
	WindowFull    bool                    `json:"queue_window_full"`
	Counts        map[string]int          `json:"observed_counts"`
	Runs          []pipelineRunSummary    `json:"runs"`
	OmittedRuns   int                     `json:"omitted_runs"`
	Workers       []pipelineWorkerSummary `json:"workers"`
	Native        *pipelineNativeSummary  `json:"native_publication,omitempty"`
	Unknown       map[string]string       `json:"unknown"`
	PartialErrors []string                `json:"partial_errors,omitempty"`
}

func summarizePipelineStatus(status knowledge.PipelineOverallStatus) pipelineStatusSummary {
	out := pipelineStatusSummary{SchemaVersion: "knowledge.pipeline_summary.v1", Scope: "bounded_selected_queue_not_corpus_totals",
		Counts: map[string]int{}, Runs: []pipelineRunSummary{}, Workers: []pipelineWorkerSummary{},
		Unknown: map[string]string{"client_delivery": "no per-device receipt", "protected_generation": "no exact selected-generation protection receipt"}}
	if status.Progress == nil {
		out.PartialErrors = []string{"progress_unavailable"}
		return out
	}
	p := status.Progress
	out.ObservedAt, out.Known, out.WindowFull = p.ObservedAt, p.CurrentKnown, p.WindowFull
	out.PartialErrors = p.PartialErrors
	for _, run := range p.Runs {
		r := summarizePipelineRun(run)
		out.Counts[r.Activity]++
		out.Runs = append(out.Runs, r)
	}
	sort.SliceStable(out.Runs, func(i, j int) bool {
		rank := func(r pipelineRunSummary) int {
			if r.Attention == "required" {
				return 0
			}
			if r.Activity != "not_admitted" {
				return 1
			}
			return 2
		}
		return rank(out.Runs[i]) < rank(out.Runs[j])
	})
	if len(out.Runs) > pipelineSummaryRunLimit {
		out.OmittedRuns = len(out.Runs) - pipelineSummaryRunLimit
		out.Runs = out.Runs[:pipelineSummaryRunLimit]
	}
	for _, worker := range p.Workers {
		out.Workers = append(out.Workers, summarizePipelineWorker(worker, out.ObservedAt))
	}
	var native notesworkspacesync.WorkspaceProgress
	if len(status.NativePublication) > 0 && json.Unmarshal(status.NativePublication, &native) == nil {
		n := &pipelineNativeSummary{Scope: native.SelectionScope, Collections: len(native.Inventory), CountersKnown: len(native.Inventory) > 0}
		var text, assets, publishedText, publishedAssets, excluded int64
		textKnown, assetsKnown := len(native.Inventory) > 0, len(native.Inventory) > 0
		for _, inventory := range native.Inventory {
			n.CountersKnown = n.CountersKnown && inventory.CountersKnown
			text += inventory.Counts.TextPending
			assets += inventory.Counts.AssetPending
			excluded += inventory.Counts.Excluded
			if inventory.TextPublished == nil {
				textKnown = false
			} else {
				publishedText += *inventory.TextPublished
			}
			if inventory.AssetPublished == nil {
				assetsKnown = false
			} else {
				publishedAssets += *inventory.AssetPublished
			}
		}
		if n.CountersKnown {
			n.PendingText, n.PendingAssets = &text, &assets
			n.Excluded = &excluded
		}
		if n.CountersKnown && textKnown {
			n.PublishedText = &publishedText
		}
		if n.CountersKnown && assetsKnown {
			n.PublishedAssets = &publishedAssets
		}
		out.Native = n
		out.PartialErrors = append(append([]string{}, out.PartialErrors...), native.PartialErrors...)
	}
	return out
}

func summarizePipelineWorker(worker knowledge.PipelineWorkerProgress, observedAt time.Time) pipelineWorkerSummary {
	w := pipelineWorkerSummary{Key: worker.WorkerKey, ActiveRun: worker.CurrentRunID, LastRun: worker.RunID, LastStatus: worker.Status,
		StartedAt: worker.StartedAt, FinishedAt: worker.FinishedAt, ObservedAt: worker.FinishedAt,
		AdmissionTimedOut: worker.AdmissionTimedOut, AdmissionWarning: knowledge.NormalizeAdmissionPhaseWarning(worker.AdmissionWarning),
		RecentIntervalsSeconds: worker.RecentIntervalsSeconds}
	if w.ObservedAt == nil {
		w.ObservedAt = worker.StartedAt
	}
	if w.ObservedAt != nil && !observedAt.IsZero() {
		age := max(0, observedAt.Sub(*w.ObservedAt).Seconds())
		w.AgeSeconds = &age
	}
	return w
}

func summarizePipelineInspect(detail knowledge.PipelineInspect) pipelineRunSummary {
	p := knowledge.PipelineRunProgress{PipelineRunID: detail.Run.KnowledgePipelineRunID, ObjectID: detail.Run.KnowledgeObjectID,
		Status: detail.Run.Status, CurrentStage: detail.Run.CurrentStageKey, LastChangedAt: detail.Run.UpdatedAt,
		Source: detail.Source, Reason: detail.Run.LastErrorCode}
	if detail.Run.Status == knowledge.FilePipelineStatusWaitingQuietWindow {
		p.NextEligibleAt = detail.Run.QuietWindowEligibleAt
	}
	for _, stage := range detail.Stages {
		p.Stages = append(p.Stages, knowledge.PipelineStageProgress{StageKey: stage.StageKey, Status: stage.Status, Attempts: stage.AttemptCount})
		if stage.CompletedAt != nil && (p.LastProgressAt == nil || stage.CompletedAt.After(*p.LastProgressAt)) {
			p.LastProgressAt = stage.CompletedAt
		}
		if stage.StageKey == p.CurrentStage && stage.NextAttemptAt != nil {
			p.NextEligibleAt = stage.NextAttemptAt
		}
	}
	out := summarizePipelineRun(p)
	out.Refresh = &detail.Refresh
	for _, stage := range detail.Stages {
		if len(out.Stages) >= pipelineSummaryStageLimit || len(stage.StageKey) > 64 || len(stage.Status) > 64 || !declarationDisplayText(stage.StageKey, stage.Status) {
			out.OmittedStages++
			continue
		}
		item := pipelineStageSummary{Key: stage.StageKey, Status: stage.Status, Attempts: stage.AttemptCount, Artifacts: stage.OutputArtifactCount}
		var metadata struct {
			SkipReason string `json:"skip_reason"`
		}
		if json.Unmarshal(stage.Metadata, &metadata) == nil && len(metadata.SkipReason) <= 240 && declarationDisplayText(metadata.SkipReason) {
			item.SkipReason = metadata.SkipReason
		}
		out.Stages = append(out.Stages, item)
	}
	var plan knowledge.CompiledPipelinePlan
	if json.Unmarshal(detail.Run.PlanSnapshot, &plan) == nil && plan.Enrichment != nil {
		e := plan.Enrichment
		out.SavedPlanInputs = &enrichmentOutputsView{Requested: e.Requested, Retained: e.Retained, Preserved: e.Preserved}
	}
	return out
}

func summarizePipelineRun(run knowledge.PipelineRunProgress) pipelineRunSummary {
	out := pipelineRunSummary{PipelineID: run.PipelineRunID, ObjectID: run.ObjectID, Stage: run.CurrentStage, Status: run.Status,
		Activity: "waiting", Attention: "unknown", Reason: run.Reason, Source: run.Source, Publication: run.Publication,
		LastChangedAt: run.LastChangedAt, LastProgressAt: run.LastProgressAt, NextEligibleAt: run.NextEligibleAt}
	for _, stage := range run.Stages {
		if stage.StageKey == run.CurrentStage {
			out.Attempts = stage.Attempts
		}
	}
	switch run.Status {
	case knowledge.FilePipelineStatusProcessing:
		out.Activity, out.Attention = "running", "none"
	case knowledge.FilePipelineStatusFailed, knowledge.FilePipelineStatusBlockedManual:
		out.Activity, out.Attention = "blocked", "required"
	case knowledge.FilePipelineStatusComplete, knowledge.FilePipelineStatusCompleteWithWarning:
		out.Activity, out.Attention = "completed", "none"
	case knowledge.FilePipelineStatusCancelled, knowledge.FilePipelineStatusStale:
		out.Activity, out.Attention = "historical", "none"
	}
	if (run.Source == nil || run.Source.ClaimSourceEligible == nil) && out.Activity == "waiting" {
		out.Reason = "current_source_eligibility_unknown"
	} else if run.Source != nil && run.Source.ClaimSourceEligible != nil && !*run.Source.ClaimSourceEligible && (out.Activity == "waiting" || out.Activity == "blocked") {
		if out.Activity == "blocked" {
			out.RecordedFailure = run.Reason
		}
		out.Activity, out.Attention, out.Reason = "not_admitted", "unknown", run.Source.Reason
		switch run.Source.Reason {
		case "excluded_by_current_declaration", "source_withdrawn", "body_owned_by_another_source", "source_root_inactive":
			out.Attention = "none"
		}
	}
	if pipelineSafeReference(run.PipelineRunID, "knowledge_pipeline_run_") {
		out.InspectCommand = "loom notes pipelines inspect " + run.PipelineRunID + " --summary --json"
	}
	if run.Source != nil && run.Source.Readable != nil && *run.Source.Readable && pipelineSafeReference(run.ObjectID, "knowledge_object_") {
		out.SourceCommand = "loom notes objects show " + run.ObjectID + " --source-lifecycle all --json"
	}
	return out
}

func pipelineSafeReference(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) || len(value) > 96 {
		return false
	}
	for _, r := range strings.TrimPrefix(value, prefix) {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return len(value) > len(prefix)
}

func renderPipelineStatusSummary(cmd *cobra.Command, summary pipelineStatusSummary) {
	fmt.Fprintf(cmd.OutOrStdout(), "Notes observed: %s\n", summary.ObservedAt.UTC().Format(time.RFC3339))
	if summary.Known {
		fmt.Fprintf(cmd.OutOrStdout(), "Selected queue: running=%d waiting=%d blocked=%d not_admitted=%d; window_full=%t (not corpus totals)\n",
			summary.Counts["running"], summary.Counts["waiting"], summary.Counts["blocked"], summary.Counts["not_admitted"], summary.WindowFull)
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "Selected queue: unknown (partial observations, not corpus totals)")
	}
	for _, run := range summary.Runs {
		renderPipelineRunSummary(cmd, run)
	}
	if summary.OmittedRuns > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Additional observed runs: %d (--details)\n", summary.OmittedRuns)
	}
	for _, w := range summary.Workers {
		renderPipelineWorkerReceipt(cmd, w)
		age := "unknown"
		if w.AgeSeconds != nil {
			age = (time.Duration(*w.AgeSeconds) * time.Second).Round(time.Second).String()
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  Last-run observation_age=%s recent_intervals_seconds=%v\n", age, w.RecentIntervalsSeconds)
	}
	if n := summary.Native; n != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Native publication: %s collections=%d pending_text=%s pending_assets=%s (not client delivery)\n", n.Scope, n.Collections, pipelineCountText(n.PendingText), pipelineCountText(n.PendingAssets))
		fmt.Fprintf(cmd.OutOrStdout(), "  Published observations: text=%s assets=%s excluded=%s\n", pipelineCountText(n.PublishedText), pipelineCountText(n.PublishedAssets), pipelineCountText(n.Excluded))
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Client delivery and exact-generation protection: unknown; publication is not either receipt.")
	for _, failure := range summary.PartialErrors {
		fmt.Fprintf(cmd.OutOrStdout(), "Partial: %s\n", failure)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Details: loom notes pipelines status --details; compact JSON: --summary --json")
}

func renderPipelineWorkerReceipt(cmd *cobra.Command, w pipelineWorkerSummary) {
	qualification := "admission unknown (unreported)"
	switch {
	case w.AdmissionWarning != nil || w.AdmissionTimedOut != nil && *w.AdmissionTimedOut:
		qualification = "admission incomplete"
	case w.AdmissionTimedOut != nil:
		qualification = "admission timeout: no"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Worker %s: current_run=%s; last run: %s; %s\n", w.Key, dashIfEmpty(w.ActiveRun), dashIfEmpty(w.LastStatus), qualification)
	fmt.Fprintf(cmd.OutOrStdout(), "  Last run receipt: run=%s started=%s finished=%s\n", dashIfEmpty(w.LastRun), timePtrOrDash(w.StartedAt), timePtrOrDash(w.FinishedAt))
	if warning := knowledge.NormalizeAdmissionPhaseWarning(w.AdmissionWarning); warning != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "  Last-run admission warning: phase=%s reason=%s elapsed=%dms remaining_budget_at_phase_entry=%dms retryable=%t\n",
			warning.Phase, warning.Reason, warning.ElapsedMS, warning.BudgetMS, warning.Retryable)
	} else if w.AdmissionTimedOut != nil && *w.AdmissionTimedOut {
		fmt.Fprintln(cmd.OutOrStdout(), "  Last-run admission warning: phase detail unavailable")
	}
}

func renderPipelineRunSummary(cmd *cobra.Command, run pipelineRunSummary) {
	path := ""
	if run.Source != nil && run.Source.RelativePath != "" {
		path = fmt.Sprintf(" source=%q", run.Source.RelativePath)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s: %s saved_status=%s stage=%s attempts=%d attention=%s reason=%s%s\n", run.PipelineID, run.Activity, run.Status, run.Stage, run.Attempts, run.Attention, run.Reason, path)
	if run.RecordedFailure != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "  Retained failure: %s (not evidence of currently actionable work)\n", run.RecordedFailure)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  Last change: %s; last completed stage: %s\n", run.LastChangedAt.UTC().Format(time.RFC3339), timePtrOrDash(run.LastProgressAt))
	if run.NextEligibleAt != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "  Recorded eligibility deadline: %s (source admission still required)\n", timePtrOrDash(run.NextEligibleAt))
	}
	if run.InspectCommand != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "  Inspect: %s\n", run.InspectCommand)
	}
	if run.SourceCommand != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "  Source: %s\n", run.SourceCommand)
	}
}
