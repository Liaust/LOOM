package supportbundle

import (
	"context"

	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/search"
)

type JobsSummary struct {
	Queue         jobs.QueueSummary     `json:"queue"`
	FailedJobs    []JobFailureSummary   `json:"failed_jobs,omitempty"`
	IndexFailures []IndexFailureSummary `json:"index_failures,omitempty"`
}

type JobFailureSummary struct {
	JobID          string `json:"job_id,omitempty"`
	JobType        string `json:"job_type,omitempty"`
	Status         string `json:"status,omitempty"`
	FailureCode    string `json:"failure_code,omitempty"`
	FailureMessage string `json:"failure_message,omitempty"`
	AttemptCount   int    `json:"attempt_count,omitempty"`
	MaxAttempts    int    `json:"max_attempts,omitempty"`
	ManualAction   bool   `json:"manual_action_required,omitempty"`
}

type IndexFailureSummary struct {
	IndexStatusID    string `json:"index_status_id,omitempty"`
	SourceKind       string `json:"source_kind,omitempty"`
	SourceID         string `json:"source_id,omitempty"`
	ObjectID         string `json:"object_id,omitempty"`
	IndexType        string `json:"index_type,omitempty"`
	Status           string `json:"status,omitempty"`
	LastErrorCode    string `json:"last_error_code,omitempty"`
	LastErrorMessage string `json:"last_error_message,omitempty"`
	AttemptCount     int    `json:"attempt_count,omitempty"`
	ManualAction     bool   `json:"manual_action_required,omitempty"`
}

func collectJobs(ctx context.Context, collection CollectionContext) (CollectorOutput, error) {
	client, err := jobsClient(collection)
	if err != nil {
		return CollectorOutput{}, err
	}
	limit := itemLimit(collection.Options)
	queue, err := client.JobStatus(ctx, correlationID(collection))
	if err != nil {
		return CollectorOutput{}, err
	}
	failedJobs, err := client.ListJobs(ctx, correlationID(collection), jobs.ListFilter{Limit: limit, Status: jobs.StatusFailed})
	if err != nil {
		return CollectorOutput{}, err
	}
	indexFailures, err := client.ListIndexFailures(ctx, correlationID(collection), search.StatusFilter{Limit: limit, FailedOnly: true})
	if err != nil {
		return CollectorOutput{}, err
	}
	summary := JobsSummary{Queue: queue.Data}
	for _, job := range limitItems(failedJobs.Data, limit) {
		summary.FailedJobs = append(summary.FailedJobs, jobFailureSummary(job))
	}
	for _, item := range limitItems(indexFailures.Data, limit) {
		summary.IndexFailures = append(summary.IndexFailures, indexFailureSummary(item))
	}
	return jsonSummary("summaries/jobs.json", summary, PrivacyDiagnosticSummary)
}

func jobFailureSummary(job jobs.Job) JobFailureSummary {
	return JobFailureSummary{
		JobID:          job.JobID,
		JobType:        job.JobType,
		Status:         job.Status,
		FailureCode:    stringPtrValue(job.FailureCode),
		FailureMessage: stringPtrValue(job.FailureMessage),
		AttemptCount:   job.AttemptCount,
		MaxAttempts:    job.MaxAttempts,
		ManualAction:   job.ManualAction,
	}
}

func indexFailureSummary(item search.IndexStatus) IndexFailureSummary {
	return IndexFailureSummary{
		IndexStatusID:    item.IndexStatusID,
		SourceKind:       item.SourceKind,
		SourceID:         item.SourceID,
		ObjectID:         item.ObjectID,
		IndexType:        item.IndexType,
		Status:           item.Status,
		LastErrorCode:    item.LastErrorCode,
		LastErrorMessage: item.LastErrorMessage,
		AttemptCount:     item.AttemptCount,
		ManualAction:     item.ManualAction,
	}
}

func stringPtrValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
