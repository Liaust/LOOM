package loomdapp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"

	"loom.local/loom/internal/config"
	hs "loom.local/loom/internal/hermesschedules"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/routing"
)

type hermesEventAdapter struct {
	DB     *sql.DB
	Config config.Config
}

func (a hermesEventAdapter) Execute(ctx context.Context, _ routing.ExecutionContext, raw json.RawMessage) (routing.ExecutionResult, error) {
	if os.Getenv("LOOM_HERMES_PROJECT_SCHEDULES") != "1" {
		return routing.ExecutionResult{}, fmt.Errorf("native Hermes event owner is disabled")
	}
	var input hs.EventInput
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return routing.ExecutionResult{}, err
	}
	if input.Event.Type != "job.failed" && input.Event.Type != "job.timed_out" {
		return routing.ExecutionResult{}, fmt.Errorf("unsupported agent event")
	}
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return routing.ExecutionResult{}, err
	}
	defer tx.Rollback()
	var actual hs.Event
	var scope string
	// This is the existing archive writer's project-row fence, held until the
	// native enqueue reply. A guard before script creation would leave a race.
	err = tx.QueryRowContext(ctx, `SELECT e.event_id, e.event_type, e.job_id, p.project_id, e.created_at, p.project_scope_id
	 FROM events.events e JOIN jobs.jobs j ON j.job_id=e.job_id
	 JOIN projects.projects p ON p.project_scope_id=j.scope_id
	 WHERE e.event_id=$1 FOR SHARE OF p`, input.Event.ID).Scan(&actual.ID, &actual.Type, &actual.JobID, &actual.ProjectID, &actual.CreatedAt, &scope)
	if err != nil {
		return routing.ExecutionResult{}, err
	}
	if actual.Type != input.Event.Type || actual.JobID != input.Event.JobID || actual.ProjectID != input.Event.ProjectID || !actual.CreatedAt.Equal(input.Event.CreatedAt) {
		return routing.ExecutionResult{}, fmt.Errorf("event source/project binding differs")
	}
	if err = projects.EnsureRuntimeActive(ctx, tx, projects.RuntimeRef{ProjectID: actual.ProjectID, ScopeID: scope, ResourceKind: "hermes_event", ResourceRef: actual.ID}); err != nil {
		return routing.ExecutionResult{}, err
	}
	source, err := a.Config.HermesScheduleSource()
	if err != nil {
		return routing.ExecutionResult{}, err
	}
	receipt, err := (hs.ProjectClient{Source: source}).AdmitEvent(ctx, input)
	if err != nil {
		return routing.ExecutionResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return routing.ExecutionResult{}, err
	}
	result, err := json.Marshal(receipt)
	return routing.ExecutionResult{Status: routing.CapabilityCallStatusCompleted, Result: result, ResultRefs: result}, err
}
