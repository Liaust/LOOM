package supportbundle

import (
	"context"

	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/search"
)

type NotesSummary struct {
	Overview    knowledge.NotesOverview `json:"overview"`
	Pipelines   *PipelineSupportSummary `json:"pipelines,omitempty"`
	IndexStatus []IndexStatusSummary    `json:"index_status,omitempty"`
}

type PipelineSupportSummary struct {
	Counts     map[string]int                     `json:"counts"`
	Policy     knowledge.PipelinePolicy           `json:"policy"`
	Operations knowledge.PipelineOperationsStatus `json:"operations"`
	Current    []PipelineCurrentSummary           `json:"current,omitempty"`
}
type PipelineCurrentSummary struct {
	PipelineRunID  string `json:"pipeline_run_id"`
	Status         string `json:"status"`
	Stage          string `json:"stage"`
	ExecutionClass string `json:"execution_class"`
	Generation     int64  `json:"generation"`
}
type notesPipelinesClient interface {
	GetKnowledgeNotesPipelineStatus(context.Context, string) (response.Envelope[knowledge.PipelineOverallStatus], error)
}

type IndexStatusSummary struct {
	IndexStatusID    string `json:"index_status_id,omitempty"`
	SourceKind       string `json:"source_kind,omitempty"`
	SourceID         string `json:"source_id,omitempty"`
	IndexType        string `json:"index_type,omitempty"`
	Status           string `json:"status,omitempty"`
	LastErrorCode    string `json:"last_error_code,omitempty"`
	LastErrorMessage string `json:"last_error_message,omitempty"`
	AttemptCount     int    `json:"attempt_count,omitempty"`
	ManualAction     bool   `json:"manual_action_required,omitempty"`
}

func collectNotes(ctx context.Context, collection CollectionContext) (CollectorOutput, error) {
	client, err := notesClient(collection)
	if err != nil {
		return CollectorOutput{}, err
	}
	limit := itemLimit(collection.Options)
	overview, err := client.GetKnowledgeNotesOverview(ctx, correlationID(collection), knowledge.NotesOverviewInput{})
	if err != nil {
		return CollectorOutput{}, err
	}
	statuses, err := client.ListIndexStatus(ctx, correlationID(collection), search.StatusFilter{Limit: limit})
	if err != nil {
		return CollectorOutput{}, err
	}
	summary := NotesSummary{Overview: overview.Data}
	if pipelineClient, ok := client.(notesPipelinesClient); ok {
		if envelope, pipelineErr := pipelineClient.GetKnowledgeNotesPipelineStatus(ctx, correlationID(collection)); pipelineErr == nil {
			pipeline := &PipelineSupportSummary{Counts: envelope.Data.Counts, Policy: envelope.Data.Policy.Policy, Operations: envelope.Data.Operations}
			for _, run := range limitItems(envelope.Data.Current, limit) {
				pipeline.Current = append(pipeline.Current, PipelineCurrentSummary{PipelineRunID: run.KnowledgePipelineRunID, Status: run.Status, Stage: run.CurrentStageKey, ExecutionClass: run.CurrentExecutionClass, Generation: run.Generation})
			}
			summary.Pipelines = pipeline
		}
	}
	for _, item := range limitItems(statuses.Data, limit) {
		summary.IndexStatus = append(summary.IndexStatus, IndexStatusSummary{
			IndexStatusID:    item.IndexStatusID,
			SourceKind:       item.SourceKind,
			SourceID:         item.SourceID,
			IndexType:        item.IndexType,
			Status:           item.Status,
			LastErrorCode:    item.LastErrorCode,
			LastErrorMessage: item.LastErrorMessage,
			AttemptCount:     item.AttemptCount,
			ManualAction:     item.ManualAction,
		})
	}
	return jsonSummary("summaries/notes.json", summary, PrivacyDiagnosticSummary)
}
