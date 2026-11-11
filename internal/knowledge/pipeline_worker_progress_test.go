package knowledge

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPipelineWorkerSummaryDecoder(t *testing.T) {
	for _, tc := range []struct {
		name    string
		summary string
		timeout *bool
		warning *AdmissionPhaseWarning
	}{
		{name: "no run"},
		{name: "historical fields absent", summary: `{}`},
		{name: "explicit unknowns", summary: `{"admission_timed_out":null,"admission_warning":null}`},
		{name: "fully successful", summary: `{"admission_timed_out":false}`, timeout: boolPointer(false)},
		{name: "partial without phase", summary: `{"admission_timed_out":true}`, timeout: boolPointer(true)},
		{name: "partial with phase", summary: `{"admission_timed_out":true,"admission_warning":{"phase":"candidate_reconciliation","reason":"deadline_exceeded","elapsed_ms":805,"budget_ms":590,"retryable":true,"error":"private-sentinel"},"error":"private-sentinel","warnings":["private-sentinel"]}`,
			timeout: boolPointer(true), warning: &AdmissionPhaseWarning{Phase: "candidate_reconciliation", Reason: "deadline_exceeded", ElapsedMS: 805, BudgetMS: 590, Retryable: true}},
		{name: "closed strings and nonnegative timing", summary: `{"admission_warning":{"phase":"private-sentinel","reason":"private-sentinel","elapsed_ms":-12,"budget_ms":-5}}`,
			warning: &AdmissionPhaseWarning{Phase: "unknown", Reason: "unknown"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
			item := PipelineWorkerProgress{WorkerKey: "main.knowledge_indexer", RunID: "worker_run_saved", CurrentRunID: "worker_run_current",
				Status: "succeeded", StartedAt: &now, FinishedAt: &now, Counters: map[string]int64{"completed": 3}}
			if err := decodePipelineWorkerSummary(&item, []byte(tc.summary)); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(item.AdmissionTimedOut, tc.timeout) || !reflect.DeepEqual(item.AdmissionWarning, tc.warning) {
				t.Fatalf("wrong qualification: %+v", item)
			}
			if item.RunID != "worker_run_saved" || item.CurrentRunID != "worker_run_current" || item.Status != "succeeded" || item.StartedAt != &now || item.FinishedAt != &now || item.Counters["completed"] != 3 {
				t.Fatalf("summary changed receipt identity/time/processing: %+v", item)
			}
			raw, err := json.Marshal(item)
			if err != nil || strings.Contains(string(raw), "private-sentinel") || strings.Contains(string(raw), "warnings") {
				t.Fatalf("raw diagnostic leaked: %s %v", raw, err)
			}
		})
	}
}

func TestPipelineWorkerSummaryNewerRunClearsQualification(t *testing.T) {
	var item PipelineWorkerProgress
	if err := decodePipelineWorkerSummary(&item, []byte(`{"admission_timed_out":true,"admission_warning":{"phase":"catalog_hydration","reason":"deadline_exceeded"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := decodePipelineWorkerSummary(&item, []byte(`{"admission_timed_out":false,"more_work":false,"stages":[{"completed":4}]}`)); err != nil {
		t.Fatal(err)
	}
	if item.AdmissionTimedOut == nil || *item.AdmissionTimedOut || item.AdmissionWarning != nil || item.MoreWork == nil || *item.MoreWork || len(item.Turns) != 1 || item.Turns[0].Completed != 4 {
		t.Fatalf("new result retained old qualification or lost existing fields: %+v", item)
	}
	if err := decodePipelineWorkerSummary(&item, []byte(`{}`)); err != nil || item.AdmissionTimedOut != nil || item.AdmissionWarning != nil || item.MoreWork != nil || item.Turns != nil {
		t.Fatalf("missing fields inherited a previous observation: %+v %v", item, err)
	}
}

func TestPipelineWorkerSummaryInvalidFieldsDoNotLeak(t *testing.T) {
	for _, raw := range []string{`{"admission_timed_out":"private-sentinel"}`, `{"admission_warning":"private-sentinel"}`, `{"admission_warning":{"elapsed_ms":"private-sentinel"}}`, `{"private-sentinel"`} {
		var item PipelineWorkerProgress
		if err := decodePipelineWorkerSummary(&item, []byte(raw)); err == nil || err.Error() != "invalid worker summary" {
			t.Fatalf("missing or raw decoder error: %v", err)
		}
	}
}
