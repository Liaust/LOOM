package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/artifacts"
	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/objects"
	"loom.local/loom/internal/objectstore"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/realtime"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/scripts"
	"loom.local/loom/internal/workflows"
)

const (
	DefaultRunnerKey  = "main-local-runner"
	DefaultRunnerType = "local"
)

var ErrNoQueuedJob = errors.New("no queued job is ready to claim")

var (
	errJobInputProject    = errors.New("job_project_unavailable")
	errSourceInputProject = errors.New("source_project_unavailable")
)

type Job struct {
	JobID                            string          `json:"job_id"`
	JobType                          string          `json:"job_type"`
	Status                           string          `json:"status"`
	OriginActorID                    string          `json:"origin_actor_id"`
	OriginNodeID                     string          `json:"origin_node_id"`
	ExecutionNodeID                  string          `json:"execution_node_id"`
	ScopeID                          *string         `json:"scope_id,omitempty"`
	TargetKind                       *string         `json:"target_kind,omitempty"`
	TargetID                         *string         `json:"target_id,omitempty"`
	ScriptID                         *string         `json:"script_id,omitempty"`
	ScriptVersionID                  *string         `json:"script_version_id,omitempty"`
	WorkflowID                       *string         `json:"workflow_id,omitempty"`
	WorkflowVersionID                *string         `json:"workflow_version_id,omitempty"`
	SourceObjectID                   *string         `json:"source_object_id,omitempty"`
	SourceObjectVersionID            *string         `json:"source_object_version_id,omitempty"`
	InputJSON                        json.RawMessage `json:"input_json"`
	OutputJSON                       json.RawMessage `json:"output_json"`
	ProgressJSON                     json.RawMessage `json:"progress_json"`
	ArtifactRefsJSON                 json.RawMessage `json:"artifact_refs_json"`
	WorkdirPath                      *string         `json:"workdir_path,omitempty"`
	AttemptCount                     int             `json:"attempt_count"`
	MaxAttempts                      int             `json:"max_attempts"`
	TimeoutSeconds                   *int            `json:"timeout_seconds,omitempty"`
	LeaseOwner                       *string         `json:"lease_owner,omitempty"`
	LeaseExpiresAt                   *time.Time      `json:"lease_expires_at,omitempty"`
	Priority                         int             `json:"priority"`
	NextAttemptAt                    *time.Time      `json:"next_attempt_at,omitempty"`
	ManualAction                     bool            `json:"manual_action_required"`
	LastWorkerRunID                  *string         `json:"last_worker_run_id,omitempty"`
	LastHeartbeatAt                  *time.Time      `json:"last_heartbeat_at,omitempty"`
	CancelRequestedAt                *time.Time      `json:"cancel_requested_at,omitempty"`
	CancelRequestedByID              *string         `json:"cancel_requested_by_actor_id,omitempty"`
	ExitCode                         *int            `json:"exit_code,omitempty"`
	FailureCode                      *string         `json:"failure_code,omitempty"`
	FailureMessage                   *string         `json:"failure_message,omitempty"`
	FailureAttentionStatus           string          `json:"failure_attention_status,omitempty"`
	FailureAttentionUpdatedAt        *time.Time      `json:"failure_attention_updated_at,omitempty"`
	FailureAttentionUpdatedByActorID *string         `json:"failure_attention_updated_by_actor_id,omitempty"`
	FailureAttentionNote             string          `json:"failure_attention_note,omitempty"`
	CreatedAt                        time.Time       `json:"created_at"`
	QueuedAt                         *time.Time      `json:"queued_at,omitempty"`
	StartedAt                        *time.Time      `json:"started_at,omitempty"`
	CompletedAt                      *time.Time      `json:"completed_at,omitempty"`
	FailedAt                         *time.Time      `json:"failed_at,omitempty"`
	CancelledAt                      *time.Time      `json:"cancelled_at,omitempty"`
	UpdatedAt                        time.Time       `json:"updated_at"`
	Metadata                         json.RawMessage `json:"metadata"`
}

type Attempt struct {
	JobAttemptID  string          `json:"job_attempt_id"`
	JobID         string          `json:"job_id"`
	AttemptNumber int             `json:"attempt_number"`
	RunnerID      *string         `json:"runner_id,omitempty"`
	Status        string          `json:"status"`
	WorkdirPath   string          `json:"workdir_path"`
	InputDir      string          `json:"input_dir"`
	OutputDir     string          `json:"output_dir"`
	ArtifactDir   string          `json:"artifact_dir"`
	TempDir       string          `json:"temp_dir"`
	StdoutLogPath *string         `json:"stdout_log_path,omitempty"`
	StderrLogPath *string         `json:"stderr_log_path,omitempty"`
	RunnerLogPath *string         `json:"runner_log_path,omitempty"`
	ResultPath    *string         `json:"result_path,omitempty"`
	ExitCode      *int            `json:"exit_code,omitempty"`
	StartedAt     *time.Time      `json:"started_at,omitempty"`
	CompletedAt   *time.Time      `json:"completed_at,omitempty"`
	FailedAt      *time.Time      `json:"failed_at,omitempty"`
	Metadata      json.RawMessage `json:"metadata"`
}

type Runner struct {
	RunnerID          string          `json:"runner_id"`
	RunnerKey         string          `json:"runner_key"`
	NodeID            string          `json:"node_id"`
	RunnerType        string          `json:"runner_type"`
	SupportedJobTypes json.RawMessage `json:"supported_job_types"`
	SupportedRuntimes json.RawMessage `json:"supported_runtimes"`
	Status            string          `json:"status"`
	LastHeartbeatAt   *time.Time      `json:"last_heartbeat_at,omitempty"`
	CurrentJobID      *string         `json:"current_job_id,omitempty"`
	Version           string          `json:"version"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	Metadata          json.RawMessage `json:"metadata"`
}

type JobLog struct {
	JobLogID     string          `json:"job_log_id"`
	JobID        string          `json:"job_id"`
	JobAttemptID *string         `json:"job_attempt_id,omitempty"`
	Stream       string          `json:"stream"`
	StorageKind  string          `json:"storage_kind"`
	Path         string          `json:"path"`
	ByteCount    int64           `json:"byte_count"`
	TailText     string          `json:"tail_text"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	Metadata     json.RawMessage `json:"metadata"`
}

type JobOutput struct {
	JobOutputID  string          `json:"job_output_id"`
	JobID        string          `json:"job_id"`
	JobAttemptID *string         `json:"job_attempt_id,omitempty"`
	OutputKey    string          `json:"output_key"`
	OutputType   string          `json:"output_type"`
	ValueJSON    json.RawMessage `json:"value_json"`
	ArtifactID   *string         `json:"artifact_id,omitempty"`
	Status       string          `json:"status"`
	CreatedAt    time.Time       `json:"created_at"`
	Metadata     json.RawMessage `json:"metadata"`
}

type JobDetail struct {
	Job       Job                  `json:"job"`
	Attempts  []Attempt            `json:"attempts,omitempty"`
	Logs      []JobLog             `json:"logs,omitempty"`
	Outputs   []JobOutput          `json:"outputs,omitempty"`
	Artifacts []artifacts.Artifact `json:"artifacts,omitempty"`
}

type CreateScriptRunInput struct {
	ScriptRef          string              `json:"script_ref"`
	ObjectRef          string              `json:"object_ref,omitempty"`
	ProjectRef         string              `json:"project_ref,omitempty"`
	ScopeRef           string              `json:"scope_ref,omitempty"`
	Input              json.RawMessage     `json:"input,omitempty"`
	CredentialBindings []CredentialBinding `json:"credential_bindings,omitempty"`
	ExecutionMode      string              `json:"execution_mode,omitempty"`
	WaitTimeoutSecs    int                 `json:"wait_timeout_seconds,omitempty"`
	RouteID            string              `json:"-"`
	CapabilityCallID   string              `json:"-"`
}

type CreateWorkflowRunInput struct {
	WorkflowRef        string              `json:"workflow_ref"`
	ObjectRef          string              `json:"object_ref,omitempty"`
	ProjectRef         string              `json:"project_ref,omitempty"`
	ScopeRef           string              `json:"scope_ref,omitempty"`
	Input              json.RawMessage     `json:"input,omitempty"`
	CredentialBindings []CredentialBinding `json:"credential_bindings,omitempty"`
	ExecutionMode      string              `json:"execution_mode,omitempty"`
	WaitTimeoutSecs    int                 `json:"wait_timeout_seconds,omitempty"`
	RouteID            string              `json:"-"`
	CapabilityCallID   string              `json:"-"`
}

type CredentialBinding struct {
	Ref      string                  `json:"ref"`
	Kind     string                  `json:"kind"`
	ExposeAs string                  `json:"expose_as"`
	Source   CredentialBindingSource `json:"source"`
}

type CredentialBindingSource struct {
	Kind string `json:"kind"`
	Env  string `json:"env,omitempty"`
	Path string `json:"path,omitempty"`
}

type ListFilter struct {
	Limit               int
	Status              string
	JobType             string
	ScriptRef           string
	WorkflowRef         string
	ProjectRef          string
	ScopeRef            string
	ManualOnly          bool
	AttentionStatus     string
	IncludeAcknowledged bool
	IncludeArchived     bool
}

type RunnerFilter struct {
	Limit  int
	Status string
	NodeID string
}

type QueueSummary struct {
	QueuedCount            int        `json:"queued_count"`
	RunningCount           int        `json:"running_count"`
	FailedCount            int        `json:"failed_count"`
	TimedOutCount          int        `json:"timed_out_count"`
	CancelledCount         int        `json:"cancelled_count"`
	ManualActionCount      int        `json:"manual_action_count"`
	RunnerCount            int        `json:"runner_count"`
	IdleRunnerCount        int        `json:"idle_runner_count"`
	RunningRunnerCount     int        `json:"running_runner_count"`
	DrainingRunnerCount    int        `json:"draining_runner_count"`
	OfflineRunnerCount     int        `json:"offline_runner_count"`
	FailedRunnerCount      int        `json:"failed_runner_count"`
	CurrentJobCount        int        `json:"current_job_count"`
	OldestQueuedAt         *time.Time `json:"oldest_queued_at,omitempty"`
	OldestQueuedAgeSeconds *int64     `json:"oldest_queued_age_seconds,omitempty"`
	GeneratedAt            time.Time  `json:"generated_at"`
}

type WaitOptions struct {
	Timeout      time.Duration
	PollInterval time.Duration
}

type ClaimOptions struct {
	LeaseDuration time.Duration
	WorkerRunID   string
}

type RunOptions struct {
	LeaseDuration        time.Duration
	LeaseRenewalInterval time.Duration
	WorkerRunID          string
}

type CancelJobInput struct {
	JobRef string `json:"job_ref"`
}

type RetryJobInput struct {
	JobRef string `json:"job_ref"`
	Force  bool   `json:"force,omitempty"`
}

type JobAttentionInput struct {
	JobRef string `json:"job_ref"`
	Note   string `json:"note,omitempty"`
}

type SweepInput struct {
	RunnerOfflineAfter      time.Duration
	StaleRunningLeaseAfter  time.Duration
	QueuedLeaseReleaseAfter time.Duration
	BatchSize               int
	Now                     time.Time
}

type SweepResult struct {
	SchemaVersion        string    `json:"schema_version"`
	ScannedAt            time.Time `json:"scanned_at"`
	RunnersMarkedOffline int64     `json:"runners_marked_offline"`
	QueuedLeasesReleased int64     `json:"queued_leases_released"`
	JobsTimedOut         int64     `json:"jobs_timed_out"`
	ManualAction         int64     `json:"manual_action"`
	Ambiguous            int64     `json:"ambiguous"`
	TimedOutJobIDs       []string  `json:"timed_out_job_ids,omitempty"`
	ManualActionJobIDs   []string  `json:"manual_action_job_ids,omitempty"`
}

type JobClaim struct {
	Runner           Runner                    `json:"runner"`
	Job              Job                       `json:"job"`
	Attempt          Attempt                   `json:"attempt"`
	Package          ExecutablePackage         `json:"package"`
	Script           scripts.ScriptDetail      `json:"script,omitempty"`
	Version          scripts.ScriptVersion     `json:"version,omitempty"`
	Manifest         scripts.Manifest          `json:"manifest,omitempty"`
	Workflow         workflows.WorkflowDetail  `json:"workflow,omitempty"`
	WorkflowVersion  workflows.WorkflowVersion `json:"workflow_version,omitempty"`
	WorkflowManifest workflows.Manifest        `json:"workflow_manifest,omitempty"`
}

type ExecutablePackage struct {
	Kind            string                 `json:"kind"`
	ID              string                 `json:"id"`
	VersionID       string                 `json:"version_id"`
	Slug            string                 `json:"slug,omitempty"`
	PackageRoot     string                 `json:"package_root"`
	Entrypoint      scripts.Entrypoint     `json:"entrypoint"`
	Execution       scripts.Execution      `json:"execution"`
	ManifestJSON    json.RawMessage        `json:"manifest_json"`
	Artifacts       []scripts.ArtifactSpec `json:"artifacts,omitempty"`
	PackageMetadata map[string]any         `json:"package_metadata,omitempty"`
}

type RunResult struct {
	Job           JobDetail                  `json:"job"`
	Artifacts     []artifacts.ArtifactDetail `json:"artifacts,omitempty"`
	ExecutionMode string                     `json:"execution_mode,omitempty"`
	WaitResult    string                     `json:"wait_result,omitempty"`
	TimedOut      bool                       `json:"timed_out,omitempty"`
}

type Service struct {
	DB        *sql.DB
	DataDir   string
	Scripts   scripts.Service
	Workflows workflows.Service
	Objects   objects.Service
	Artifacts artifacts.Service
	Progress  realtime.Service
}

func NewService(db *sql.DB, dataDir string, scriptService scripts.Service, objectService objects.Service, artifactService artifacts.Service) Service {
	return Service{
		DB:        db,
		DataDir:   dataDir,
		Scripts:   scriptService,
		Workflows: workflows.NewService(db),
		Objects:   objectService,
		Artifacts: artifactService,
		Progress:  realtime.NewService(db),
	}
}

func (s Service) CreateScriptRun(ctx context.Context, req requestctx.Context, input CreateScriptRunInput) (Job, error) {
	scriptRef := strings.TrimSpace(input.ScriptRef)
	if scriptRef == "" {
		return Job{}, fmt.Errorf("script_ref is required")
	}
	scriptDetail, err := s.Scripts.GetScript(ctx, scriptRef)
	if err != nil {
		return Job{}, fmt.Errorf("resolve script: %w", err)
	}
	version, err := s.Scripts.ResolveActiveVersion(ctx, scriptRef)
	if err != nil {
		return Job{}, fmt.Errorf("resolve active script version: %w", err)
	}
	manifest, err := version.Manifest()
	if err != nil {
		return Job{}, err
	}

	scopeID, sourceObjectID, sourceVersionID, resolvedInput, err := s.resolveScriptRunContext(ctx, req, input, manifest)
	if err != nil {
		return Job{}, err
	}
	if err := projects.EnsureRuntimeActive(ctx, s.DB, projects.RuntimeRef{
		ScopeID:      scopeID,
		ResourceKind: "script_run",
		ResourceRef:  scriptRef,
	}); err != nil {
		return Job{}, err
	}

	timeoutSeconds := manifest.Execution.TimeoutSeconds
	jobID := ids.NewJobID()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()

	job, err := insertJobTx(ctx, tx, jobID, req, TypeScriptRun, StatusCreated, scopeID, "script", scriptDetail.Script.ScriptID, scriptDetail.Script.ScriptID, version.ScriptVersionID, "", "", sourceObjectID, sourceVersionID, timeoutSeconds, resolvedInput, input.RouteID, input.CapabilityCallID, input.CredentialBindings)
	if err != nil {
		return Job{}, err
	}
	for _, eventInput := range []events.AppendInput{
		jobEventInput(req, job, events.TypeJobCreated, "created", "ok", map[string]any{
			"job_id":            job.JobID,
			"job_type":          job.JobType,
			"script_id":         scriptDetail.Script.ScriptID,
			"script_version_id": version.ScriptVersionID,
			"source_object_id":  sourceObjectID,
			"execution_node_id": req.OriginNodeID,
		}),
		scriptRunEventInput(req, job, events.TypeScriptRunRequested, "queued", "ok", map[string]any{
			"job_id":            job.JobID,
			"script_id":         scriptDetail.Script.ScriptID,
			"script_version_id": version.ScriptVersionID,
			"script_ref":        scriptRef,
		}),
	} {
		if _, err := events.AppendTx(ctx, tx, eventInput); err != nil {
			return Job{}, err
		}
	}
	job, err = updateJobStatusTx(ctx, tx, job.JobID, StatusQueued, nil, "", "")
	if err != nil {
		return Job{}, err
	}
	if _, err := events.AppendTx(ctx, tx, jobEventInput(req, job, events.TypeJobQueued, "queued", "ok", map[string]any{
		"job_id": job.JobID,
	})); err != nil {
		return Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	s.recordJobProgress(ctx, req, job, realtime.ProgressStatusPending, "queued", "Script run queued.", realtime.ProgressSeverityNormal, map[string]any{
		"job_id":            job.JobID,
		"job_type":          job.JobType,
		"script_id":         scriptDetail.Script.ScriptID,
		"script_version_id": version.ScriptVersionID,
	})
	return job, nil
}

func (s Service) CreateWorkflowRun(ctx context.Context, req requestctx.Context, input CreateWorkflowRunInput) (Job, error) {
	workflowRef := strings.TrimSpace(input.WorkflowRef)
	if workflowRef == "" {
		return Job{}, fmt.Errorf("workflow_ref is required")
	}
	workflowDetail, err := s.Workflows.GetWorkflow(ctx, workflowRef)
	if err != nil {
		return Job{}, fmt.Errorf("resolve workflow: %w", err)
	}
	version, err := s.Workflows.ResolveActiveVersion(ctx, workflowRef)
	if err != nil {
		return Job{}, fmt.Errorf("resolve active workflow version: %w", err)
	}
	manifest, err := version.Manifest()
	if err != nil {
		return Job{}, err
	}

	scopeID, sourceObjectID, sourceVersionID, resolvedInput, err := s.resolveWorkflowRunContext(ctx, req, input, manifest)
	if err != nil {
		return Job{}, err
	}
	if err := projects.EnsureRuntimeActive(ctx, s.DB, projects.RuntimeRef{
		ScopeID:      scopeID,
		ResourceKind: "workflow_run",
		ResourceRef:  workflowRef,
	}); err != nil {
		return Job{}, err
	}

	timeoutSeconds := manifest.Execution.TimeoutSeconds
	jobID := ids.NewJobID()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()

	job, err := insertJobTx(ctx, tx, jobID, req, TypeWorkflowRun, StatusCreated, scopeID, "workflow", workflowDetail.Workflow.WorkflowID, "", "", workflowDetail.Workflow.WorkflowID, version.WorkflowVersionID, sourceObjectID, sourceVersionID, timeoutSeconds, resolvedInput, input.RouteID, input.CapabilityCallID, input.CredentialBindings)
	if err != nil {
		return Job{}, err
	}
	for _, eventInput := range []events.AppendInput{
		jobEventInput(req, job, events.TypeJobCreated, "created", "ok", map[string]any{
			"job_id":              job.JobID,
			"job_type":            job.JobType,
			"workflow_id":         workflowDetail.Workflow.WorkflowID,
			"workflow_version_id": version.WorkflowVersionID,
			"source_object_id":    sourceObjectID,
			"execution_node_id":   req.OriginNodeID,
		}),
		workflowRunEventInput(req, job, events.TypeWorkflowRunRequested, "queued", "ok", map[string]any{
			"job_id":              job.JobID,
			"workflow_id":         workflowDetail.Workflow.WorkflowID,
			"workflow_version_id": version.WorkflowVersionID,
			"workflow_ref":        workflowRef,
		}),
	} {
		if _, err := events.AppendTx(ctx, tx, eventInput); err != nil {
			return Job{}, err
		}
	}
	job, err = updateJobStatusTx(ctx, tx, job.JobID, StatusQueued, nil, "", "")
	if err != nil {
		return Job{}, err
	}
	if _, err := events.AppendTx(ctx, tx, jobEventInput(req, job, events.TypeJobQueued, "queued", "ok", map[string]any{
		"job_id": job.JobID,
	})); err != nil {
		return Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	s.recordJobProgress(ctx, req, job, realtime.ProgressStatusPending, "queued", "Workflow run queued.", realtime.ProgressSeverityNormal, map[string]any{
		"job_id":              job.JobID,
		"job_type":            job.JobType,
		"workflow_id":         workflowDetail.Workflow.WorkflowID,
		"workflow_version_id": version.WorkflowVersionID,
	})
	return job, nil
}

func (s Service) ListJobs(ctx context.Context, filter ListFilter) ([]Job, error) {
	return s.listJobsWithFilter(ctx, filter, false)
}

func (s Service) GetJob(ctx context.Context, ref string) (JobDetail, error) {
	job, err := s.resolveJob(ctx, ref)
	if err != nil {
		return JobDetail{}, err
	}
	attempts, err := s.listAttempts(ctx, job.JobID)
	if err != nil {
		return JobDetail{}, err
	}
	logs, err := s.ListJobLogs(ctx, job.JobID)
	if err != nil {
		return JobDetail{}, err
	}
	outputs, err := s.listOutputs(ctx, job.JobID)
	if err != nil {
		return JobDetail{}, err
	}
	artifactRows, err := s.Artifacts.ListArtifacts(ctx, artifacts.ListFilter{JobRef: job.JobID, Limit: 200})
	if err != nil {
		return JobDetail{}, err
	}
	return JobDetail{Job: job, Attempts: attempts, Logs: logs, Outputs: outputs, Artifacts: artifactRows}, nil
}

func (s Service) EnsureLocalRunner(ctx context.Context, req requestctx.Context, version string) (Runner, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Runner{}, err
	}
	defer tx.Rollback()

	row := tx.QueryRowContext(ctx, runnerSelectSQL()+`
		WHERE r.runner_key = $1
	`, DefaultRunnerKey)
	runner, err := scanRunner(row)
	if err == nil {
		runner, err = updateRunnerHeartbeatTx(ctx, tx, runner.RunnerID, "idle", version, "")
		if err != nil {
			return Runner{}, err
		}
		runner, err = updateRunnerSupportedJobTypesTx(ctx, tx, runner.RunnerID, []string{TypeScriptRun, TypeWorkflowRun})
		if err != nil {
			return Runner{}, err
		}
		if err := tx.Commit(); err != nil {
			return Runner{}, err
		}
		return runner, nil
	}
	if err != sql.ErrNoRows {
		return Runner{}, err
	}

	runnerID := ids.NewRunnerID()
	row = tx.QueryRowContext(ctx, `
		INSERT INTO jobs.runners (
			runner_id, runner_key, node_id, runner_type, supported_job_types,
			supported_runtimes, status, last_heartbeat_at, version, metadata
		)
		VALUES ($1, $2, $3, $4, '["script_run","workflow_run"]'::jsonb, '["local"]'::jsonb,
		        'idle', now(), $5, '{"version":"v0.3.1","part":"2"}'::jsonb)
		RETURNING `+runnerReturningColumns()+`
	`, runnerID, DefaultRunnerKey, req.OriginNodeID, DefaultRunnerType, version)
	runner, err = scanRunner(row)
	if err != nil {
		return Runner{}, err
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  events.TypeRunnerRegistered,
		EventLevel: "node_activity",
		Request:    req,
		ScopeID:    req.ScopeID,
		TargetKind: "runner",
		TargetID:   runner.RunnerID,
		Status:     runner.Status,
		Result:     "ok",
		Payload: map[string]any{
			"runner_id":  runner.RunnerID,
			"runner_key": runner.RunnerKey,
			"node_id":    runner.NodeID,
		},
		VisibilityClass: "internal",
	}); err != nil {
		return Runner{}, err
	}
	if err := tx.Commit(); err != nil {
		return Runner{}, err
	}
	return runner, nil
}

func (s Service) Heartbeat(ctx context.Context, runnerID string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := updateRunnerHeartbeatTx(ctx, tx, runnerID, "idle", "", ""); err != nil {
		return err
	}
	return tx.Commit()
}

func (s Service) ClaimNext(ctx context.Context, req requestctx.Context, runnerID string) (JobClaim, error) {
	return s.ClaimNextWithOptions(ctx, req, runnerID, ClaimOptions{})
}

func (s Service) ClaimNextWithOptions(ctx context.Context, req requestctx.Context, runnerID string, opts ClaimOptions) (JobClaim, error) {
	runnerID = strings.TrimSpace(runnerID)
	if runnerID == "" {
		return JobClaim{}, fmt.Errorf("runner_id is required")
	}
	opts = normalizeClaimOptions(opts)

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return JobClaim{}, err
	}
	defer tx.Rollback()

	runner, err := getRunnerTx(ctx, tx, runnerID)
	if err != nil {
		return JobClaim{}, err
	}

	row := tx.QueryRowContext(ctx, jobSelectSQL()+`
		WHERE j.status = 'queued'
		  AND j.job_type IN ('script_run', 'workflow_run')
		  AND j.execution_node_id = $1
		  AND (j.lease_expires_at IS NULL OR j.lease_expires_at < now())
		  AND (j.next_attempt_at IS NULL OR j.next_attempt_at <= now())
		  AND j.manual_action_required = false
		  AND j.cancel_requested_at IS NULL
		  AND j.cancelled_at IS NULL
		  AND NOT EXISTS (
		      SELECT 1
		      FROM projects.projects p
		      JOIN scopes.scopes scope ON scope.scope_id = p.project_scope_id
		      WHERE p.project_scope_id = j.scope_id
		        AND (p.status = 'archived' OR scope.status = 'archived')
		  )
		ORDER BY j.priority ASC, COALESCE(j.next_attempt_at, j.queued_at, j.created_at) ASC,
		         j.queued_at ASC NULLS LAST, j.created_at ASC
		FOR UPDATE SKIP LOCKED
		LIMIT 1
	`, runner.NodeID)
	job, err := scanJob(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return JobClaim{}, ErrNoQueuedJob
		}
		return JobClaim{}, err
	}
	if err := projects.EnsureRuntimeActive(ctx, tx, projects.RuntimeRef{
		ScopeID:      stringValue(job.ScopeID),
		ResourceKind: "job_claim",
		ResourceRef:  job.JobID,
	}); err != nil {
		return JobClaim{}, err
	}

	attemptNumber := job.AttemptCount + 1
	workdir := s.attemptWorkdir(job.JobID, attemptNumber)
	attemptID := ids.NewJobAttemptID()
	job, err = claimJobWithOptionsTx(ctx, tx, job.JobID, runner.RunnerID, workdir, attemptNumber, opts.LeaseDuration, opts.WorkerRunID)
	if err != nil {
		return JobClaim{}, err
	}
	attempt, err := insertAttemptTx(ctx, tx, attemptID, job.JobID, attemptNumber, runner.RunnerID, workdir)
	if err != nil {
		return JobClaim{}, err
	}
	runner, err = updateRunnerHeartbeatTx(ctx, tx, runner.RunnerID, "running", runner.Version, job.JobID)
	if err != nil {
		return JobClaim{}, err
	}
	for _, eventInput := range startedEventInputs(req, runner, job, attempt) {
		if _, err := events.AppendTx(ctx, tx, eventInput); err != nil {
			return JobClaim{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return JobClaim{}, err
	}
	s.recordJobProgress(ctx, req, job, realtime.ProgressStatusRunning, "claimed", runLabel(job.JobType)+" claimed by local runner.", realtime.ProgressSeverityNormal, map[string]any{
		"job_id":         job.JobID,
		"runner_id":      runner.RunnerID,
		"attempt_id":     attempt.JobAttemptID,
		"attempt_number": attempt.AttemptNumber,
	})

	return s.buildJobClaim(ctx, runner, job, attempt)
}

func (s Service) RunQueuedOnce(ctx context.Context, req requestctx.Context, runnerID string) (RunResult, error) {
	claim, err := s.ClaimNext(ctx, req, runnerID)
	if err != nil {
		return RunResult{}, err
	}
	return s.RunClaim(ctx, req, claim)
}

func (s Service) RunJob(ctx context.Context, req requestctx.Context, runnerID string, jobID string) (RunResult, error) {
	claim, err := s.ClaimJob(ctx, req, runnerID, jobID)
	if err != nil {
		return RunResult{}, err
	}
	return s.RunClaim(ctx, req, claim)
}

func (s Service) ClaimJob(ctx context.Context, req requestctx.Context, runnerID string, jobID string) (JobClaim, error) {
	runnerID = strings.TrimSpace(runnerID)
	jobID = strings.TrimSpace(jobID)
	if runnerID == "" {
		return JobClaim{}, fmt.Errorf("runner_id is required")
	}
	if jobID == "" {
		return JobClaim{}, fmt.Errorf("job_id is required")
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return JobClaim{}, err
	}
	defer tx.Rollback()

	runner, err := getRunnerTx(ctx, tx, runnerID)
	if err != nil {
		return JobClaim{}, err
	}

	row := tx.QueryRowContext(ctx, jobSelectSQL()+`
		WHERE j.job_id = $1
		  AND j.status = 'queued'
		  AND j.job_type IN ('script_run', 'workflow_run')
		  AND j.execution_node_id = $2
		  AND (j.lease_expires_at IS NULL OR j.lease_expires_at < now())
		  AND (j.next_attempt_at IS NULL OR j.next_attempt_at <= now())
		  AND j.manual_action_required = false
		  AND j.cancel_requested_at IS NULL
		  AND j.cancelled_at IS NULL
		FOR UPDATE SKIP LOCKED
		LIMIT 1
	`, jobID, runner.NodeID)
	job, err := scanJob(row)
	if err != nil {
		return JobClaim{}, err
	}
	if err := projects.EnsureRuntimeActive(ctx, tx, projects.RuntimeRef{
		ScopeID:      stringValue(job.ScopeID),
		ResourceKind: "job_claim",
		ResourceRef:  job.JobID,
	}); err != nil {
		return JobClaim{}, err
	}

	attemptNumber := job.AttemptCount + 1
	workdir := s.attemptWorkdir(job.JobID, attemptNumber)
	attemptID := ids.NewJobAttemptID()
	job, err = claimJobTx(ctx, tx, job.JobID, runner.RunnerID, workdir, attemptNumber)
	if err != nil {
		return JobClaim{}, err
	}
	attempt, err := insertAttemptTx(ctx, tx, attemptID, job.JobID, attemptNumber, runner.RunnerID, workdir)
	if err != nil {
		return JobClaim{}, err
	}
	runner, err = updateRunnerHeartbeatTx(ctx, tx, runner.RunnerID, "running", runner.Version, job.JobID)
	if err != nil {
		return JobClaim{}, err
	}
	for _, eventInput := range startedEventInputs(req, runner, job, attempt) {
		if _, err := events.AppendTx(ctx, tx, eventInput); err != nil {
			return JobClaim{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return JobClaim{}, err
	}
	s.recordJobProgress(ctx, req, job, realtime.ProgressStatusRunning, "claimed", runLabel(job.JobType)+" claimed by local runner.", realtime.ProgressSeverityNormal, map[string]any{
		"job_id":         job.JobID,
		"runner_id":      runner.RunnerID,
		"attempt_id":     attempt.JobAttemptID,
		"attempt_number": attempt.AttemptNumber,
	})

	return s.buildJobClaim(ctx, runner, job, attempt)
}

func (s Service) buildJobClaim(ctx context.Context, runner Runner, job Job, attempt Attempt) (JobClaim, error) {
	switch job.JobType {
	case TypeScriptRun:
		scriptRef := stringValue(job.ScriptID)
		if scriptRef == "" {
			return JobClaim{}, fmt.Errorf("script_run job %s has no script_id", job.JobID)
		}
		versionRef := stringValue(job.ScriptVersionID)
		if versionRef == "" {
			return JobClaim{}, fmt.Errorf("script_run job %s has no script_version_id", job.JobID)
		}
		scriptDetail, err := s.Scripts.GetScript(ctx, scriptRef)
		if err != nil {
			return JobClaim{}, err
		}
		version, err := s.Scripts.GetScriptVersion(ctx, versionRef)
		if err != nil {
			return JobClaim{}, err
		}
		manifest, err := version.Manifest()
		if err != nil {
			return JobClaim{}, err
		}
		return JobClaim{
			Runner:   runner,
			Job:      job,
			Attempt:  attempt,
			Script:   scriptDetail,
			Version:  version,
			Manifest: manifest,
			Package: ExecutablePackage{
				Kind:            "script",
				ID:              scriptDetail.Script.ScriptID,
				VersionID:       version.ScriptVersionID,
				Slug:            scriptDetail.Script.Slug,
				PackageRoot:     version.PackageRoot,
				Entrypoint:      manifest.Entrypoint,
				Execution:       manifest.Execution,
				ManifestJSON:    version.ManifestJSON,
				Artifacts:       manifest.Artifacts,
				PackageMetadata: manifest.Metadata,
			},
		}, nil
	case TypeWorkflowRun:
		workflowRef := stringValue(job.WorkflowID)
		if workflowRef == "" {
			return JobClaim{}, fmt.Errorf("workflow_run job %s has no workflow_id", job.JobID)
		}
		versionRef := stringValue(job.WorkflowVersionID)
		if versionRef == "" {
			return JobClaim{}, fmt.Errorf("workflow_run job %s has no workflow_version_id", job.JobID)
		}
		workflowDetail, err := s.Workflows.GetWorkflow(ctx, workflowRef)
		if err != nil {
			return JobClaim{}, err
		}
		version, err := s.Workflows.GetWorkflowVersion(ctx, versionRef)
		if err != nil {
			return JobClaim{}, err
		}
		manifest, err := version.Manifest()
		if err != nil {
			return JobClaim{}, err
		}
		return JobClaim{
			Runner:           runner,
			Job:              job,
			Attempt:          attempt,
			Workflow:         workflowDetail,
			WorkflowVersion:  version,
			WorkflowManifest: manifest,
			Package: ExecutablePackage{
				Kind:            "workflow",
				ID:              workflowDetail.Workflow.WorkflowID,
				VersionID:       version.WorkflowVersionID,
				Slug:            workflowDetail.Workflow.Slug,
				PackageRoot:     version.PackageRoot,
				Entrypoint:      manifest.Entrypoint,
				Execution:       manifest.Execution,
				ManifestJSON:    version.ManifestJSON,
				Artifacts:       manifest.Artifacts,
				PackageMetadata: manifest.Metadata,
			},
		}, nil
	default:
		return JobClaim{}, fmt.Errorf("unsupported claimed job type: %s", job.JobType)
	}
}

func (s Service) RunClaim(ctx context.Context, req requestctx.Context, claim JobClaim) (RunResult, error) {
	return s.RunClaimWithOptions(ctx, req, claim, RunOptions{})
}

func (s Service) RunClaimWithOptions(ctx context.Context, req requestctx.Context, claim JobClaim, opts RunOptions) (RunResult, error) {
	opts = normalizeRunOptions(opts)
	if opts.LeaseRenewalInterval <= 0 {
		return s.runClaim(ctx, req, claim)
	}

	renewCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(opts.LeaseRenewalInterval)
		defer ticker.Stop()
		for {
			select {
			case <-renewCtx.Done():
				return
			case <-ticker.C:
				_ = s.RenewLease(renewCtx, claim.Runner.RunnerID, claim.Job.JobID, opts.LeaseDuration, opts.WorkerRunID)
			}
		}
	}()
	result, err := s.runClaim(ctx, req, claim)
	cancel()
	<-done
	return result, err
}

func (s Service) RenewLease(ctx context.Context, runnerID, jobID string, extend time.Duration, workerRunID string) error {
	runnerID = strings.TrimSpace(runnerID)
	jobID = strings.TrimSpace(jobID)
	if runnerID == "" {
		return fmt.Errorf("runner_id is required")
	}
	if jobID == "" {
		return fmt.Errorf("job_id is required")
	}
	if extend <= 0 {
		extend = 10 * time.Minute
	}
	leaseExpiresAt := time.Now().UTC().Add(extend)
	result, err := s.DB.ExecContext(ctx, `
		UPDATE jobs.jobs
		SET lease_expires_at = $3,
		    last_heartbeat_at = now(),
		    last_worker_run_id = COALESCE(nullif($4, ''), last_worker_run_id),
		    updated_at = now()
		WHERE job_id = $1
		  AND lease_owner = $2
		  AND status = 'running'
	`, jobID, runnerID, leaseExpiresAt, strings.TrimSpace(workerRunID))
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("job lease was not renewed for job %s", jobID)
	}
	return nil
}

func (s Service) runClaim(ctx context.Context, req requestctx.Context, claim JobClaim) (RunResult, error) {
	s.recordJobProgress(ctx, req, claim.Job, realtime.ProgressStatusRunning, "preparing", "Preparing "+runLabel(claim.Job.JobType)+" work directory.", realtime.ProgressSeverityNormal, map[string]any{
		"job_id":         claim.Job.JobID,
		"attempt_id":     claim.Attempt.JobAttemptID,
		"attempt_number": claim.Attempt.AttemptNumber,
	})
	paths := attemptPaths(claim.Attempt)
	if err := paths.create(); err != nil {
		return s.failClaim(ctx, req, claim, "workdir_setup_failed", err, nil)
	}
	runnerLog, _ := os.OpenFile(paths.RunnerLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if runnerLog != nil {
		defer runnerLog.Close()
		fmt.Fprintf(runnerLog, "starting job %s attempt %d\n", claim.Job.JobID, claim.Attempt.AttemptNumber)
	}

	inputObjectPath, err := s.materializeInputObject(ctx, claim.Job, paths.InputObject)
	if err != nil {
		// Cancellation must still leave a truthful failed attempt. This bounded
		// persistence context grants no permission to launch the command.
		failureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return s.failClaim(failureCtx, req, claim, "input_materialize_failed", safeMaterializeError(err), runnerLog)
	}

	if err := os.WriteFile(paths.InputJSON, claim.Job.InputJSON, 0o600); err != nil {
		return s.failClaim(ctx, req, claim, "input_write_failed", err, runnerLog)
	}
	if err := os.WriteFile(paths.ManifestSnapshot, claim.Package.ManifestJSON, 0o600); err != nil {
		return s.failClaim(ctx, req, claim, "manifest_snapshot_failed", err, runnerLog)
	}

	timeout := time.Duration(claim.Package.Execution.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stdoutFile, err := os.OpenFile(paths.StdoutLog, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return s.failClaim(ctx, req, claim, "stdout_log_open_failed", err, runnerLog)
	}
	defer stdoutFile.Close()
	stderrFile, err := os.OpenFile(paths.StderrLog, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return s.failClaim(ctx, req, claim, "stderr_log_open_failed", err, runnerLog)
	}
	defer stderrFile.Close()

	command := claim.Package.Entrypoint.Command
	executable, err := resolveExecutable(command[0])
	if err != nil {
		return s.failClaimWithStatus(ctx, req, claim, StatusFailed, claim.Package.Kind+"_command_not_found", err, nil, runnerLog)
	}
	env, err := executableEnvironment(claim, paths, inputObjectPath)
	if err != nil {
		return s.failClaimWithStatus(ctx, req, claim, StatusFailed, claim.Package.Kind+"_credential_unavailable", err, nil, runnerLog)
	}
	cmd := exec.CommandContext(runCtx, executable, command[1:]...)
	cmd.Dir = claim.Package.PackageRoot
	cmd.Env = env
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile

	s.recordJobProgress(ctx, req, claim.Job, realtime.ProgressStatusRunning, "executing", "Executing "+runLabel(claim.Job.JobType)+" process.", realtime.ProgressSeverityNormal, map[string]any{
		"job_id":     claim.Job.JobID,
		"attempt_id": claim.Attempt.JobAttemptID,
	})
	err = cmd.Run()
	_ = stdoutFile.Close()
	_ = stderrFile.Close()
	exitCode := commandExitCode(err)
	timedOut := runCtx.Err() == context.DeadlineExceeded

	if runnerLog != nil {
		fmt.Fprintf(runnerLog, "process finished exit_code=%d timed_out=%t error=%v\n", exitCode, timedOut, err)
	}
	if logErr := s.recordAttemptLogs(ctx, claim, paths); logErr != nil && err == nil {
		err = logErr
		exitCode = -1
	}

	if timedOut {
		return s.failClaimWithStatus(ctx, req, claim, StatusTimedOut, claim.Package.Kind+"_timed_out", fmt.Errorf("%s timed out after %s", runLabel(claim.Job.JobType), timeout), &exitCode, runnerLog)
	}
	if err != nil {
		return s.failClaimWithStatus(ctx, req, claim, StatusFailed, claim.Package.Kind+"_failed", err, &exitCode, runnerLog)
	}

	resultFile, err := parseResultFile(paths.ResultFile)
	if err != nil {
		return s.failClaimWithStatus(ctx, req, claim, StatusFailed, "result_parse_failed", err, &exitCode, runnerLog)
	}
	if strings.EqualFold(resultFile.Status, "error") || strings.EqualFold(resultFile.Status, "failed") {
		return s.failClaimWithStatus(ctx, req, claim, StatusFailed, "result_failed", fmt.Errorf("%s result status is %q", runLabel(claim.Job.JobType), resultFile.Status), &exitCode, runnerLog)
	}

	createdArtifacts := []artifacts.ArtifactDetail{}
	artifactRefs := []map[string]any{}
	s.recordJobProgress(ctx, req, claim.Job, realtime.ProgressStatusRunning, "collecting_outputs", "Collecting "+runLabel(claim.Job.JobType)+" outputs and artifacts.", realtime.ProgressSeverityNormal, map[string]any{
		"job_id":     claim.Job.JobID,
		"attempt_id": claim.Attempt.JobAttemptID,
	})
	for key, value := range resultFile.Outputs {
		if _, err := s.RecordOutput(ctx, RecordOutputInput{
			JobID:        claim.Job.JobID,
			JobAttemptID: claim.Attempt.JobAttemptID,
			OutputKey:    key,
			OutputType:   inferOutputType(value),
			Value:        value,
			Status:       "created",
		}); err != nil {
			return s.failClaimWithStatus(ctx, req, claim, StatusFailed, "output_record_failed", err, &exitCode, runnerLog)
		}
	}
	for _, candidate := range resultFile.Artifacts {
		artifact, err := s.Artifacts.CreateArtifactFromJobFile(ctx, req, artifacts.CreateFromJobFileInput{
			JobID:           claim.Job.JobID,
			JobAttemptID:    claim.Attempt.JobAttemptID,
			ScriptID:        stringValue(claim.Job.ScriptID),
			ScriptVersionID: stringValue(claim.Job.ScriptVersionID),
			ScopeID:         stringValue(claim.Job.ScopeID),
			SourceObjectID:  stringValue(claim.Job.SourceObjectID),
			ArtifactDir:     paths.ArtifactDir,
			Path:            candidate.Path,
			ArtifactType:    candidate.Type,
			Title:           candidate.Title,
		})
		if err != nil {
			return s.failClaimWithStatus(ctx, req, claim, StatusFailed, "artifact_create_failed", err, &exitCode, runnerLog)
		}
		createdArtifacts = append(createdArtifacts, artifact)
		artifactRefs = append(artifactRefs, map[string]any{
			"artifact_id":   artifact.Artifact.ArtifactID,
			"artifact_key":  candidate.Key,
			"artifact_type": artifact.Artifact.ArtifactType,
			"object_id":     artifact.Artifact.ObjectID,
		})
		value, _ := json.Marshal(map[string]any{
			"artifact_id": artifact.Artifact.ArtifactID,
			"object_id":   artifact.Artifact.ObjectID,
			"path":        candidate.Path,
		})
		if _, err := s.RecordOutput(ctx, RecordOutputInput{
			JobID:        claim.Job.JobID,
			JobAttemptID: claim.Attempt.JobAttemptID,
			OutputKey:    candidate.Key,
			OutputType:   "artifact",
			Value:        value,
			ArtifactID:   artifact.Artifact.ArtifactID,
			Status:       "created",
		}); err != nil {
			return s.failClaimWithStatus(ctx, req, claim, StatusFailed, "artifact_output_record_failed", err, &exitCode, runnerLog)
		}
	}

	job, err := s.completeClaim(ctx, req, claim, resultFile.Raw, artifactRefs, exitCode)
	if err != nil {
		return RunResult{}, err
	}
	detail, err := s.GetJob(ctx, job.JobID)
	if err != nil {
		return RunResult{}, err
	}
	return RunResult{Job: detail, Artifacts: createdArtifacts}, nil
}

type RecordOutputInput struct {
	JobID        string
	JobAttemptID string
	OutputKey    string
	OutputType   string
	Value        json.RawMessage
	ArtifactID   string
	Status       string
}

func (s Service) RecordOutput(ctx context.Context, input RecordOutputInput) (JobOutput, error) {
	value := input.Value
	if len(value) == 0 {
		value = json.RawMessage(`{}`)
	}
	status := strings.TrimSpace(input.Status)
	if status == "" {
		status = "created"
	}
	row := s.DB.QueryRowContext(ctx, `
		INSERT INTO jobs.job_outputs (
			job_output_id, job_id, job_attempt_id, output_key, output_type,
			value_json, artifact_id, status, metadata
		)
		VALUES ($1, $2, nullif($3, ''), $4, $5, $6, nullif($7, ''), $8,
		        '{"slice":"5","part":"2"}'::jsonb)
		RETURNING `+jobOutputReturningColumns()+`
	`, ids.NewJobOutputID(), input.JobID, input.JobAttemptID, input.OutputKey, input.OutputType, value, input.ArtifactID, status)
	return scanJobOutput(row)
}

func (s Service) ListJobLogs(ctx context.Context, jobRef string) ([]JobLog, error) {
	job, err := s.resolveJob(ctx, jobRef)
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, jobLogSelectSQL()+`
		WHERE jl.job_id = $1
		ORDER BY jl.created_at ASC, jl.stream
	`, job.JobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	logs := []JobLog{}
	for rows.Next() {
		log, err := scanJobLog(rows)
		if err != nil {
			return nil, err
		}
		logs = append(logs, log)
	}
	return logs, rows.Err()
}

func (s Service) resolveScriptRunContext(ctx context.Context, req requestctx.Context, input CreateScriptRunInput, manifest scripts.Manifest) (scopeID, sourceObjectID, sourceVersionID string, inputJSON []byte, err error) {
	scopeID, err = s.resolveOptionalScope(ctx, input.ProjectRef, input.ScopeRef)
	if err != nil {
		return "", "", "", nil, err
	}

	payload := map[string]any{
		"script": map[string]any{
			"id":      manifest.ID,
			"version": manifest.Version,
		},
	}
	routeID := strings.TrimSpace(input.RouteID)
	callID := strings.TrimSpace(input.CapabilityCallID)
	if routeID != "" || callID != "" {
		payload["routing"] = map[string]any{
			"route_id":           routeID,
			"capability_call_id": callID,
		}
	}
	if len(input.Input) > 0 {
		var custom any
		if err := json.Unmarshal(input.Input, &custom); err != nil {
			return "", "", "", nil, fmt.Errorf("input must be valid JSON: %w", err)
		}
		payload["input"] = custom
	}
	if strings.TrimSpace(input.ObjectRef) != "" {
		detail, err := s.Objects.GetObject(ctx, input.ObjectRef)
		if err != nil {
			return "", "", "", nil, fmt.Errorf("resolve source object: %w", err)
		}
		if detail.LatestVersion == nil {
			return "", "", "", nil, fmt.Errorf("source object has no version")
		}
		sourceObjectID = detail.Object.ObjectID
		sourceVersionID = detail.LatestVersion.ObjectVersionID
		if scopeID == "" && detail.Object.HomeScopeID != nil {
			scopeID = *detail.Object.HomeScopeID
		}
		payload["object"] = map[string]any{
			"object_id":         sourceObjectID,
			"object_version_id": sourceVersionID,
			"name":              detail.Object.Name,
		}
	}
	if scopeID == "" {
		scopeID = req.ScopeID
	}
	inputJSON, err = json.Marshal(payload)
	return scopeID, sourceObjectID, sourceVersionID, inputJSON, err
}

func (s Service) resolveWorkflowRunContext(ctx context.Context, req requestctx.Context, input CreateWorkflowRunInput, manifest workflows.Manifest) (scopeID, sourceObjectID, sourceVersionID string, inputJSON []byte, err error) {
	scopeID, err = s.resolveOptionalScope(ctx, input.ProjectRef, input.ScopeRef)
	if err != nil {
		return "", "", "", nil, err
	}

	payload := map[string]any{
		"workflow": map[string]any{
			"id":      manifest.Workflow.ID,
			"version": manifest.Workflow.Version,
		},
	}
	routeID := strings.TrimSpace(input.RouteID)
	callID := strings.TrimSpace(input.CapabilityCallID)
	if routeID != "" || callID != "" {
		payload["routing"] = map[string]any{
			"route_id":           routeID,
			"capability_call_id": callID,
		}
	}
	if len(input.Input) > 0 {
		var custom any
		if err := json.Unmarshal(input.Input, &custom); err != nil {
			return "", "", "", nil, fmt.Errorf("input must be valid JSON: %w", err)
		}
		payload["input"] = custom
	}
	if strings.TrimSpace(input.ObjectRef) != "" {
		detail, err := s.Objects.GetObject(ctx, input.ObjectRef)
		if err != nil {
			return "", "", "", nil, fmt.Errorf("resolve source object: %w", err)
		}
		if detail.LatestVersion == nil {
			return "", "", "", nil, fmt.Errorf("source object has no version")
		}
		sourceObjectID = detail.Object.ObjectID
		sourceVersionID = detail.LatestVersion.ObjectVersionID
		if scopeID == "" && detail.Object.HomeScopeID != nil {
			scopeID = *detail.Object.HomeScopeID
		}
		payload["object"] = map[string]any{
			"object_id":         sourceObjectID,
			"object_version_id": sourceVersionID,
			"name":              detail.Object.Name,
		}
	}
	if scopeID == "" {
		scopeID = req.ScopeID
	}
	inputJSON, err = json.Marshal(payload)
	return scopeID, sourceObjectID, sourceVersionID, inputJSON, err
}

func (s Service) resolveOptionalScope(ctx context.Context, projectRef, scopeRef string) (string, error) {
	projectRef = strings.TrimSpace(projectRef)
	scopeRef = strings.TrimSpace(scopeRef)
	if projectRef != "" && scopeRef != "" {
		return "", fmt.Errorf("provide either project_ref or scope_ref, not both")
	}
	if projectRef != "" {
		project, err := projects.NewService(s.DB).ResolveProjectRef(ctx, projectRef)
		if err != nil {
			return "", fmt.Errorf("resolve project: %w", err)
		}
		return project.ProjectScopeID, nil
	}
	if scopeRef == "" {
		return "", nil
	}
	var scopeID string
	err := s.DB.QueryRowContext(ctx, `
		SELECT scope_id
		FROM scopes.scopes
		WHERE scope_id = $1 OR scope_key = $1 OR slug = $1
		LIMIT 1
	`, scopeRef).Scan(&scopeID)
	if err != nil {
		return "", fmt.Errorf("resolve scope: %w", err)
	}
	return scopeID, nil
}

func (s Service) attemptWorkdir(jobID string, attemptNumber int) string {
	root := strings.TrimSpace(s.DataDir)
	if root == "" {
		root = "/var/lib/loom"
	}
	return filepath.Join(root, "jobs", jobID, "attempts", fmt.Sprintf("%d", attemptNumber))
}

func insertJobTx(ctx context.Context, tx *sql.Tx, jobID string, req requestctx.Context, jobType, status, scopeID, targetKind, targetID, scriptID, scriptVersionID, workflowID, workflowVersionID, sourceObjectID, sourceVersionID string, timeoutSeconds int, inputJSON []byte, routeID, capabilityCallID string, credentialBindings []CredentialBinding) (Job, error) {
	metadata := jobMetadata(routeID, capabilityCallID, req.CorrelationID, req.Source, credentialBindings)
	row := tx.QueryRowContext(ctx, `
		INSERT INTO jobs.jobs (
			job_id, job_type, status, origin_actor_id, origin_node_id, execution_node_id,
			scope_id, target_kind, target_id, script_id, script_version_id,
			workflow_id, workflow_version_id, source_object_id, source_object_version_id,
			input_json, max_attempts, timeout_seconds, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, nullif($7, ''), nullif($8, ''),
		        nullif($9, ''), nullif($10, ''), nullif($11, ''),
		        nullif($12, ''), nullif($13, ''), nullif($14, ''), nullif($15, ''),
		        $16, 1, $17, $18)
		RETURNING `+jobReturningColumns()+`
	`, jobID, jobType, status, req.ActorID, req.OriginNodeID, req.OriginNodeID, scopeID,
		targetKind, targetID, scriptID, scriptVersionID, workflowID, workflowVersionID,
		sourceObjectID, sourceVersionID, inputJSON, timeoutSeconds, metadata)
	return scanJob(row)
}

func updateJobStatusTx(ctx context.Context, tx *sql.Tx, jobID, status string, exitCode *int, failureCode, failureMessage string) (Job, error) {
	if !ValidStatus(status) {
		return Job{}, fmt.Errorf("invalid job status: %s", status)
	}
	attentionStatus := FailureAttentionStatusForTransition(status)
	row := tx.QueryRowContext(ctx, `
		UPDATE jobs.jobs
		SET status = $2,
		    queued_at = CASE WHEN $2 = 'queued' THEN COALESCE(queued_at, now()) ELSE queued_at END,
		    started_at = CASE WHEN $2 = 'running' THEN COALESCE(started_at, now()) ELSE started_at END,
		    completed_at = CASE WHEN $2 = 'completed' THEN COALESCE(completed_at, now()) ELSE completed_at END,
		    failed_at = CASE WHEN $2 IN ('failed', 'timed_out') THEN COALESCE(failed_at, now()) ELSE failed_at END,
		    cancelled_at = CASE WHEN $2 = 'cancelled' THEN COALESCE(cancelled_at, now()) ELSE cancelled_at END,
		    exit_code = COALESCE($3, exit_code),
		    failure_code = nullif($4, ''),
		    failure_message = nullif($5, ''),
		    failure_attention_status = CASE WHEN nullif($6, '') IS NULL THEN failure_attention_status ELSE $6 END,
		    failure_attention_updated_at = CASE WHEN nullif($6, '') IS NULL THEN failure_attention_updated_at ELSE now() END,
		    failure_attention_note = CASE
		        WHEN $6 = 'active' THEN ''
		        WHEN $6 = 'archived' AND failure_attention_status = 'active' AND attempt_count > 1 THEN 'Resolved by successful retry.'
		        ELSE failure_attention_note
		    END,
		    updated_at = now()
		WHERE job_id = $1
		RETURNING `+jobReturningColumns()+`
	`, jobID, status, exitCode, failureCode, failureMessage, attentionStatus)
	return scanJob(row)
}

func claimJobTx(ctx context.Context, tx *sql.Tx, jobID, runnerID, workdir string, attemptNumber int) (Job, error) {
	return claimJobWithOptionsTx(ctx, tx, jobID, runnerID, workdir, attemptNumber, 10*time.Minute, "")
}

func claimJobWithOptionsTx(ctx context.Context, tx *sql.Tx, jobID, runnerID, workdir string, attemptNumber int, leaseDuration time.Duration, workerRunID string) (Job, error) {
	if leaseDuration <= 0 {
		leaseDuration = 10 * time.Minute
	}
	leaseExpiresAt := time.Now().UTC().Add(leaseDuration)
	row := tx.QueryRowContext(ctx, `
		UPDATE jobs.jobs
		SET status = 'running',
		    workdir_path = $3,
		    attempt_count = $4,
		    lease_owner = $2,
		    lease_expires_at = $5,
		    last_worker_run_id = COALESCE(nullif($6, ''), last_worker_run_id),
		    last_heartbeat_at = now(),
		    started_at = COALESCE(started_at, now()),
		    updated_at = now()
		WHERE job_id = $1
		RETURNING `+jobReturningColumns()+`
	`, jobID, runnerID, workdir, attemptNumber, leaseExpiresAt, strings.TrimSpace(workerRunID))
	return scanJob(row)
}

func normalizeClaimOptions(opts ClaimOptions) ClaimOptions {
	if opts.LeaseDuration <= 0 {
		opts.LeaseDuration = 10 * time.Minute
	}
	opts.WorkerRunID = strings.TrimSpace(opts.WorkerRunID)
	return opts
}

func normalizeRunOptions(opts RunOptions) RunOptions {
	if opts.LeaseDuration <= 0 {
		opts.LeaseDuration = 10 * time.Minute
	}
	if opts.LeaseRenewalInterval < 0 {
		opts.LeaseRenewalInterval = 0
	}
	if opts.LeaseRenewalInterval == 0 && opts.LeaseDuration > 0 {
		opts.LeaseRenewalInterval = opts.LeaseDuration / 2
	}
	if opts.LeaseRenewalInterval < time.Second {
		opts.LeaseRenewalInterval = time.Second
	}
	opts.WorkerRunID = strings.TrimSpace(opts.WorkerRunID)
	return opts
}

func insertAttemptTx(ctx context.Context, tx *sql.Tx, attemptID, jobID string, attemptNumber int, runnerID, workdir string) (Attempt, error) {
	row := tx.QueryRowContext(ctx, `
		INSERT INTO jobs.job_attempts (
			job_attempt_id, job_id, attempt_number, runner_id, status, workdir_path,
			input_dir, output_dir, artifact_dir, temp_dir, stdout_log_path,
			stderr_log_path, runner_log_path, result_path, started_at, metadata
		)
		VALUES ($1, $2, $3, $4, 'running', $5, $6, $7, $8, $9, $10, $11, $12, $13,
		        now(), '{"slice":"5","part":"2"}'::jsonb)
		RETURNING `+attemptReturningColumns()+`
	`, attemptID, jobID, attemptNumber, runnerID, workdir, filepath.Join(workdir, "input"),
		filepath.Join(workdir, "output"), filepath.Join(workdir, "artifacts"),
		filepath.Join(workdir, "temp"), filepath.Join(workdir, "logs", "stdout.log"),
		filepath.Join(workdir, "logs", "stderr.log"), filepath.Join(workdir, "logs", "runner.log"),
		filepath.Join(workdir, "result.json"))
	return scanAttempt(row)
}

func updateAttemptFinished(ctx context.Context, tx *sql.Tx, attemptID, status string, exitCode *int) (Attempt, error) {
	row := tx.QueryRowContext(ctx, `
		UPDATE jobs.job_attempts
		SET status = $2,
		    exit_code = COALESCE($3, exit_code),
		    completed_at = CASE WHEN $2 = 'completed' THEN COALESCE(completed_at, now()) ELSE completed_at END,
		    failed_at = CASE WHEN $2 IN ('failed', 'timed_out', 'cancelled') THEN COALESCE(failed_at, now()) ELSE failed_at END
		WHERE job_attempt_id = $1
		RETURNING `+attemptReturningColumns()+`
	`, attemptID, status, exitCode)
	return scanAttempt(row)
}

func updateRunnerHeartbeatTx(ctx context.Context, tx *sql.Tx, runnerID, status, version, currentJobID string) (Runner, error) {
	if status == "" {
		status = "idle"
	}
	row := tx.QueryRowContext(ctx, `
		UPDATE jobs.runners
		SET status = $2,
		    version = CASE WHEN nullif($3, '') IS NULL THEN version ELSE $3 END,
		    current_job_id = nullif($4, ''),
		    last_heartbeat_at = now(),
		    updated_at = now()
		WHERE runner_id = $1
		RETURNING `+runnerReturningColumns()+`
	`, runnerID, status, version, currentJobID)
	return scanRunner(row)
}

func updateRunnerSupportedJobTypesTx(ctx context.Context, tx *sql.Tx, runnerID string, jobTypes []string) (Runner, error) {
	payload, err := json.Marshal(jobTypes)
	if err != nil {
		return Runner{}, err
	}
	row := tx.QueryRowContext(ctx, `
		UPDATE jobs.runners
		SET supported_job_types = $2,
		    updated_at = now()
		WHERE runner_id = $1
		RETURNING `+runnerReturningColumns()+`
	`, runnerID, payload)
	return scanRunner(row)
}

func getRunnerTx(ctx context.Context, tx *sql.Tx, runnerID string) (Runner, error) {
	row := tx.QueryRowContext(ctx, runnerSelectSQL()+`
		WHERE r.runner_id = $1 OR r.runner_key = $1
		LIMIT 1
	`, runnerID)
	return scanRunner(row)
}

func (s Service) completeClaim(ctx context.Context, req requestctx.Context, claim JobClaim, outputJSON []byte, artifactRefs []map[string]any, exitCode int) (Job, error) {
	artifactRefsJSON, _ := json.Marshal(artifactRefs)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		UPDATE jobs.jobs
		SET output_json = $2,
		    artifact_refs_json = $3
		WHERE job_id = $1
	`, claim.Job.JobID, outputJSONOrEmpty(outputJSON), artifactRefsJSON); err != nil {
		return Job{}, err
	}
	exit := exitCode
	job, err := updateJobStatusTx(ctx, tx, claim.Job.JobID, StatusCompleted, &exit, "", "")
	if err != nil {
		return Job{}, err
	}
	if _, err := updateAttemptFinished(ctx, tx, claim.Attempt.JobAttemptID, StatusCompleted, &exit); err != nil {
		return Job{}, err
	}
	if _, err := updateRunnerHeartbeatTx(ctx, tx, claim.Runner.RunnerID, "idle", claim.Runner.Version, ""); err != nil {
		return Job{}, err
	}
	for _, eventInput := range completedEventInputs(req, job, claim.Attempt, exitCode, len(artifactRefs)) {
		if _, err := events.AppendTx(ctx, tx, eventInput); err != nil {
			return Job{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	s.recordJobProgress(ctx, req, job, realtime.ProgressStatusSucceeded, "completed", runLabel(job.JobType)+" completed.", realtime.ProgressSeverityNormal, map[string]any{
		"job_id":         job.JobID,
		"attempt_id":     claim.Attempt.JobAttemptID,
		"exit_code":      exitCode,
		"artifact_count": len(artifactRefs),
	})
	return job, nil
}

func (s Service) failClaim(ctx context.Context, req requestctx.Context, claim JobClaim, code string, cause error, runnerLog io.Writer) (RunResult, error) {
	return s.failClaimWithStatus(ctx, req, claim, StatusFailed, code, cause, nil, runnerLog)
}

func (s Service) failClaimWithStatus(ctx context.Context, req requestctx.Context, claim JobClaim, status, code string, cause error, exitCode *int, runnerLog io.Writer) (RunResult, error) {
	if runnerLog != nil {
		fmt.Fprintf(runnerLog, "failure code=%s error=%v\n", code, cause)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return RunResult{}, err
	}
	defer tx.Rollback()
	job, err := updateJobStatusTx(ctx, tx, claim.Job.JobID, status, exitCode, code, cause.Error())
	if err != nil {
		return RunResult{}, err
	}
	attemptStatus := status
	if attemptStatus == StatusTimedOut {
		attemptStatus = StatusTimedOut
	}
	if _, err := updateAttemptFinished(ctx, tx, claim.Attempt.JobAttemptID, attemptStatus, exitCode); err != nil {
		return RunResult{}, err
	}
	if _, err := updateRunnerHeartbeatTx(ctx, tx, claim.Runner.RunnerID, "idle", claim.Runner.Version, ""); err != nil {
		return RunResult{}, err
	}
	jobEventType := events.TypeJobFailed
	runEventType := failedRunEventType(job.JobType)
	if status == StatusTimedOut {
		jobEventType = events.TypeJobTimedOut
	}
	for _, eventInput := range failedEventInputs(req, job, claim.Attempt, jobEventType, runEventType, status, code, cause.Error()) {
		if _, err := events.AppendTx(ctx, tx, eventInput); err != nil {
			return RunResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return RunResult{}, err
	}
	progressStatus := realtime.ProgressStatusFailed
	severity := realtime.ProgressSeverityError
	if status == StatusTimedOut {
		progressStatus = realtime.ProgressStatusFailed
	}
	if status == StatusCancelled {
		progressStatus = realtime.ProgressStatusCancelled
		severity = realtime.ProgressSeverityWarning
	}
	s.recordJobProgress(ctx, req, job, progressStatus, "failed", cause.Error(), severity, map[string]any{
		"job_id":        job.JobID,
		"attempt_id":    claim.Attempt.JobAttemptID,
		"failure_code":  code,
		"failure_error": cause.Error(),
	})
	detail, detailErr := s.GetJob(ctx, job.JobID)
	if detailErr != nil {
		return RunResult{}, detailErr
	}
	return RunResult{Job: detail}, cause
}

func (s Service) recordJobProgress(ctx context.Context, req requestctx.Context, job Job, status, stage, message, severity string, payload map[string]any) {
	if s.Progress.DB == nil {
		return
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["job_status"] = job.Status
	if job.ScriptID != nil {
		payload["script_id"] = *job.ScriptID
	}
	if job.ScriptVersionID != nil {
		payload["script_version_id"] = *job.ScriptVersionID
	}
	if job.WorkflowID != nil {
		payload["workflow_id"] = *job.WorkflowID
	}
	if job.WorkflowVersionID != nil {
		payload["workflow_version_id"] = *job.WorkflowVersionID
	}
	_, _ = s.Progress.UpdateProgress(ctx, req, realtime.UpdateProgressInput{
		SourceKind: progressSourceKind(job.JobType),
		SourceRef:  job.JobID,
		ScopeRef:   stringValue(job.ScopeID),
		Status:     status,
		Stage:      stage,
		Message:    message,
		Payload:    mustJSON(payload),
		Severity:   severity,
		Metadata:   json.RawMessage(`{}`),
		TotalValue: floatPtr(1),
	})
}

func (s Service) recordAttemptLogs(ctx context.Context, claim JobClaim, paths runPaths) error {
	for _, item := range []struct {
		stream string
		path   string
	}{
		{"stdout", paths.StdoutLog},
		{"stderr", paths.StderrLog},
		{"runner", paths.RunnerLog},
	} {
		if _, err := s.AppendJobLogRef(ctx, claim.Job.JobID, claim.Attempt.JobAttemptID, item.stream, item.path); err != nil {
			return err
		}
	}
	return nil
}

func (s Service) AppendJobLogRef(ctx context.Context, jobID, attemptID, stream, path string) (JobLog, error) {
	info, err := os.Stat(path)
	if err != nil {
		return JobLog{}, err
	}
	tail, err := readTail(path, 4096)
	if err != nil {
		return JobLog{}, err
	}
	row := s.DB.QueryRowContext(ctx, `
		INSERT INTO jobs.job_logs (
			job_log_id, job_id, job_attempt_id, stream, storage_kind, path,
			byte_count, tail_text, metadata
		)
		VALUES ($1, $2, nullif($3, ''), $4, 'file', $5, $6, $7,
		        '{"slice":"5","part":"2"}'::jsonb)
		RETURNING `+jobLogReturningColumns()+`
	`, ids.NewJobLogID(), jobID, attemptID, stream, path, info.Size(), tail)
	return scanJobLog(row)
}

func (s Service) materializeInputObject(ctx context.Context, job Job, destPath string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := projects.EnsureRuntimeActive(ctx, s.DB, projects.RuntimeRef{ScopeID: stringValue(job.ScopeID), ResourceKind: "job_input", ResourceRef: job.JobID}); err != nil {
		return "", errJobInputProject
	}
	if job.SourceObjectID == nil && job.SourceObjectVersionID == nil {
		return "", nil
	}
	if job.SourceObjectID == nil || job.SourceObjectVersionID == nil {
		return "", objects.ErrObjectVersionPin
	}
	source, err := s.Objects.ReadObjectVersionSource(ctx, *job.SourceObjectID, *job.SourceObjectVersionID)
	if err != nil {
		return "", err
	}
	if err := projects.EnsureRuntimeActive(ctx, s.DB, projects.RuntimeRef{ScopeID: stringValue(source.HomeScopeID), ResourceKind: "job_input", ResourceRef: job.JobID}); err != nil {
		return "", errSourceInputProject
	}
	if err := s.Objects.Store.MaterializeVerified(ctx, source.Blob, destPath); err != nil {
		return "", err
	}
	return destPath, nil
}

// Only fixed categories cross into failClaim's durable events, logs and receipts.
func safeMaterializeError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return errors.New("input_materialization_cancelled")
	}
	for _, known := range []error{errJobInputProject, errSourceInputProject, objects.ErrObjectVersionPin, objects.ErrObjectVersionUnavailable, objects.ErrObjectVersionMetadata, objectstore.ErrMaterializeMetadata, objectstore.ErrMaterializeSource, objectstore.ErrMaterializeContent, objectstore.ErrMaterializeDestination, objectstore.ErrMaterializeIO} {
		if errors.Is(err, known) {
			return known
		}
	}
	return errors.New("input_materialization_failed")
}

type resultFile struct {
	Status    string                     `json:"status"`
	Outputs   map[string]json.RawMessage `json:"outputs"`
	Artifacts []resultArtifactCandidate  `json:"artifacts"`
	Raw       json.RawMessage            `json:"-"`
}

type resultArtifactCandidate struct {
	Key   string `json:"key"`
	Path  string `json:"path"`
	Type  string `json:"type"`
	Title string `json:"title"`
}

func parseResultFile(path string) (resultFile, error) {
	payload, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return resultFile{Status: "ok", Outputs: map[string]json.RawMessage{}, Raw: json.RawMessage(`{}`)}, nil
	}
	if err != nil {
		return resultFile{}, err
	}
	var result resultFile
	if err := json.Unmarshal(payload, &result); err != nil {
		return resultFile{}, err
	}
	if result.Status == "" {
		result.Status = "ok"
	}
	if result.Outputs == nil {
		result.Outputs = map[string]json.RawMessage{}
	}
	result.Raw = json.RawMessage(payload)
	for _, artifact := range result.Artifacts {
		if strings.TrimSpace(artifact.Key) == "" {
			return resultFile{}, fmt.Errorf("artifact key is required")
		}
		if strings.TrimSpace(artifact.Path) == "" {
			return resultFile{}, fmt.Errorf("artifact path is required")
		}
	}
	return result, nil
}

type runPaths struct {
	Workdir          string
	InputDir         string
	OutputDir        string
	ArtifactDir      string
	TempDir          string
	LogDir           string
	InputJSON        string
	InputObject      string
	StdoutLog        string
	StderrLog        string
	RunnerLog        string
	ResultFile       string
	ManifestSnapshot string
}

func attemptPaths(attempt Attempt) runPaths {
	return runPaths{
		Workdir:          attempt.WorkdirPath,
		InputDir:         attempt.InputDir,
		OutputDir:        attempt.OutputDir,
		ArtifactDir:      attempt.ArtifactDir,
		TempDir:          attempt.TempDir,
		LogDir:           filepath.Join(attempt.WorkdirPath, "logs"),
		InputJSON:        filepath.Join(attempt.InputDir, "input.json"),
		InputObject:      filepath.Join(attempt.InputDir, "object"),
		StdoutLog:        filepath.Join(attempt.WorkdirPath, "logs", "stdout.log"),
		StderrLog:        filepath.Join(attempt.WorkdirPath, "logs", "stderr.log"),
		RunnerLog:        filepath.Join(attempt.WorkdirPath, "logs", "runner.log"),
		ResultFile:       filepath.Join(attempt.WorkdirPath, "result.json"),
		ManifestSnapshot: filepath.Join(attempt.WorkdirPath, "manifest.snapshot.json"),
	}
}

func (p runPaths) create() error {
	for _, dir := range []string{p.Workdir, p.InputDir, p.OutputDir, p.ArtifactDir, p.TempDir, p.LogDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func executableEnvironment(claim JobClaim, paths runPaths, inputObjectPath string) ([]string, error) {
	env := []string{
		"PATH=" + runnerPath(),
		"HOME=" + paths.TempDir,
		"TMPDIR=" + paths.TempDir,
		"LOOM_JOB_ID=" + claim.Job.JobID,
		"LOOM_JOB_TYPE=" + claim.Job.JobType,
		fmt.Sprintf("LOOM_ATTEMPT=%d", claim.Attempt.AttemptNumber),
		"LOOM_INPUT_DIR=" + paths.InputDir,
		"LOOM_OUTPUT_DIR=" + paths.OutputDir,
		"LOOM_ARTIFACT_DIR=" + paths.ArtifactDir,
		"LOOM_TEMP_DIR=" + paths.TempDir,
		"LOOM_RESULT_FILE=" + paths.ResultFile,
		"LOOM_INPUT_JSON=" + string(claim.Job.InputJSON),
	}
	if claim.Job.ScriptID != nil {
		env = append(env, "LOOM_SCRIPT_ID="+*claim.Job.ScriptID)
	}
	if claim.Job.ScriptVersionID != nil {
		env = append(env, "LOOM_SCRIPT_VERSION_ID="+*claim.Job.ScriptVersionID)
	}
	if claim.Job.WorkflowID != nil {
		env = append(env, "LOOM_WORKFLOW_ID="+*claim.Job.WorkflowID)
	}
	if claim.Job.WorkflowVersionID != nil {
		env = append(env, "LOOM_WORKFLOW_VERSION_ID="+*claim.Job.WorkflowVersionID)
	}
	if claim.Package.Slug != "" && claim.Job.JobType == TypeWorkflowRun {
		env = append(env, "LOOM_WORKFLOW_SLUG="+claim.Package.Slug)
	}
	if inputObjectPath != "" {
		env = append(env, "LOOM_INPUT_OBJECT_PATH="+inputObjectPath)
	}
	credentialEnv, err := credentialEnvironment(claim.Job.Metadata)
	if err != nil {
		return nil, err
	}
	env = append(env, credentialEnv...)
	return env, nil
}

func runLabel(jobType string) string {
	switch jobType {
	case TypeWorkflowRun:
		return "Workflow run"
	default:
		return "Script run"
	}
}

func progressSourceKind(jobType string) string {
	switch jobType {
	case TypeWorkflowRun:
		return realtime.ProgressSourceKindWorkflowRun
	case TypeScriptRun:
		return realtime.ProgressSourceKindScriptRun
	default:
		return realtime.ProgressSourceKindJob
	}
}

func failedRunEventType(jobType string) string {
	switch jobType {
	case TypeWorkflowRun:
		return events.TypeWorkflowRunFailed
	default:
		return events.TypeScriptRunFailed
	}
}

func runnerPath() string {
	return "/run/current-system/sw/bin:/usr/bin:/bin:/usr/local/bin"
}

func resolveExecutable(command string) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", fmt.Errorf("command is required")
	}
	if strings.ContainsRune(command, os.PathSeparator) {
		return command, nil
	}
	for _, dir := range filepath.SplitList(runnerPath()) {
		candidate := filepath.Join(dir, command)
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("executable file not found in runner PATH: %s", command)
}

func credentialEnvironment(metadataJSON json.RawMessage) ([]string, error) {
	var metadata struct {
		CredentialBindings []CredentialBinding `json:"credential_bindings"`
	}
	if err := json.Unmarshal(jsonOrEmpty(metadataJSON), &metadata); err != nil {
		return nil, fmt.Errorf("decode job credential bindings: %w", err)
	}
	env := []string{}
	for _, binding := range metadata.CredentialBindings {
		kind := strings.ToLower(strings.TrimSpace(binding.Kind))
		if kind == "" {
			kind = "env"
		}
		if kind != "env" {
			return nil, fmt.Errorf("credential %s uses unsupported exposure kind %s", strings.TrimSpace(binding.Ref), kind)
		}
		exposeAs := strings.TrimSpace(binding.ExposeAs)
		if !validCredentialEnvName(exposeAs) {
			return nil, fmt.Errorf("credential %s has invalid env exposure name %s", strings.TrimSpace(binding.Ref), exposeAs)
		}
		value, err := resolveCredentialSource(binding)
		if err != nil {
			return nil, err
		}
		env = append(env, exposeAs+"="+value)
	}
	return env, nil
}

func resolveCredentialSource(binding CredentialBinding) (string, error) {
	ref := strings.TrimSpace(binding.Ref)
	sourceKind := strings.ToLower(strings.TrimSpace(binding.Source.Kind))
	switch sourceKind {
	case "env":
		sourceEnv := strings.TrimSpace(binding.Source.Env)
		if !validCredentialEnvName(sourceEnv) {
			return "", fmt.Errorf("credential %s has invalid source env %s", ref, sourceEnv)
		}
		value, ok := os.LookupEnv(sourceEnv)
		if !ok || value == "" {
			return "", fmt.Errorf("credential %s source env %s is unavailable", ref, sourceEnv)
		}
		return value, nil
	case "file":
		sourcePath := strings.TrimSpace(binding.Source.Path)
		if sourcePath == "" || !filepath.IsAbs(sourcePath) {
			return "", fmt.Errorf("credential %s source file path is invalid", ref)
		}
		payload, err := os.ReadFile(sourcePath)
		if err != nil {
			return "", fmt.Errorf("credential %s source file is unavailable: %w", ref, err)
		}
		value := strings.TrimRight(string(payload), "\r\n")
		if value == "" {
			return "", fmt.Errorf("credential %s source file is empty", ref)
		}
		return value, nil
	default:
		return "", fmt.Errorf("credential %s has unsupported source kind %s", ref, sourceKind)
	}
}

func validCredentialEnvName(value string) bool {
	if value == "" {
		return false
	}
	for idx, r := range value {
		if r == '_' || (r >= 'A' && r <= 'Z') || (idx > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func jobMetadata(routeID, capabilityCallID, correlationID, source string, credentialBindings []CredentialBinding) json.RawMessage {
	routeID = strings.TrimSpace(routeID)
	capabilityCallID = strings.TrimSpace(capabilityCallID)
	correlationID = strings.TrimSpace(correlationID)
	source = strings.TrimSpace(source)
	payload := map[string]any{
		"slice": "5",
		"part":  "2",
	}
	if correlationID != "" || source != "" {
		payload["request"] = map[string]any{
			"correlation_id": correlationID,
			"source":         source,
		}
	}
	if routeID != "" || capabilityCallID != "" {
		payload["slice"] = "9"
		payload["part"] = "3"
		payload["routing"] = map[string]any{
			"route_id":           routeID,
			"capability_call_id": capabilityCallID,
		}
	}
	if len(credentialBindings) > 0 {
		payload["credential_bindings"] = credentialBindings
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return json.RawMessage(`{"slice":"5","part":"2"}`)
	}
	return raw
}

func jobRoutingRefs(job Job) (string, string) {
	var metadata struct {
		Routing struct {
			RouteID          string `json:"route_id"`
			CapabilityCallID string `json:"capability_call_id"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(job.Metadata, &metadata); err != nil {
		return "", ""
	}
	return strings.TrimSpace(metadata.Routing.RouteID), strings.TrimSpace(metadata.Routing.CapabilityCallID)
}

func jobRequestRefs(job Job) (string, string) {
	var metadata struct {
		Request struct {
			CorrelationID string `json:"correlation_id"`
			Source        string `json:"source"`
		} `json:"request"`
	}
	if err := json.Unmarshal(job.Metadata, &metadata); err != nil {
		return "", ""
	}
	return strings.TrimSpace(metadata.Request.CorrelationID), strings.TrimSpace(metadata.Request.Source)
}

func jobEventRequest(req requestctx.Context, job Job) requestctx.Context {
	correlationID, source := jobRequestRefs(job)
	if correlationID != "" {
		req.CorrelationID = correlationID
	}
	if source != "" {
		req.Source = source
	}
	return req
}

func addJobRoutingPayload(job Job, payload map[string]any) map[string]any {
	if payload == nil {
		payload = map[string]any{}
	}
	routeID, capabilityCallID := jobRoutingRefs(job)
	if routeID != "" {
		payload["route_id"] = routeID
	}
	if capabilityCallID != "" {
		payload["capability_call_id"] = capabilityCallID
	}
	return payload
}

func jobEventInput(req requestctx.Context, job Job, eventType, status, result string, payload map[string]any) events.AppendInput {
	routeID, _ := jobRoutingRefs(job)
	return events.AppendInput{
		EventType:       eventType,
		EventLevel:      "audit",
		Request:         jobEventRequest(req, job),
		ScopeID:         stringValue(job.ScopeID),
		TargetKind:      "job",
		TargetID:        job.JobID,
		RouteID:         routeID,
		JobID:           job.JobID,
		Status:          status,
		Result:          result,
		Payload:         addJobRoutingPayload(job, payload),
		VisibilityClass: "internal",
	}
}

func scriptRunEventInput(req requestctx.Context, job Job, eventType, status, result string, payload map[string]any) events.AppendInput {
	routeID, _ := jobRoutingRefs(job)
	return events.AppendInput{
		EventType:       eventType,
		EventLevel:      "audit",
		Request:         jobEventRequest(req, job),
		ScopeID:         stringValue(job.ScopeID),
		TargetKind:      "script",
		TargetID:        stringValue(job.ScriptID),
		RouteID:         routeID,
		JobID:           job.JobID,
		Status:          status,
		Result:          result,
		Payload:         addJobRoutingPayload(job, payload),
		VisibilityClass: "internal",
	}
}

func workflowRunEventInput(req requestctx.Context, job Job, eventType, status, result string, payload map[string]any) events.AppendInput {
	routeID, _ := jobRoutingRefs(job)
	return events.AppendInput{
		EventType:       eventType,
		EventLevel:      "audit",
		Request:         jobEventRequest(req, job),
		ScopeID:         stringValue(job.ScopeID),
		TargetKind:      "workflow",
		TargetID:        stringValue(job.WorkflowID),
		RouteID:         routeID,
		JobID:           job.JobID,
		Status:          status,
		Result:          result,
		Payload:         addJobRoutingPayload(job, payload),
		VisibilityClass: "internal",
	}
}

func startedEventInputs(req requestctx.Context, runner Runner, job Job, attempt Attempt) []events.AppendInput {
	jobPayload := map[string]any{
		"job_id":         job.JobID,
		"runner_id":      runner.RunnerID,
		"attempt_id":     attempt.JobAttemptID,
		"attempt_number": attempt.AttemptNumber,
	}
	runPayload := map[string]any{
		"job_id":         job.JobID,
		"runner_id":      runner.RunnerID,
		"attempt_id":     attempt.JobAttemptID,
		"attempt_number": attempt.AttemptNumber,
	}
	switch job.JobType {
	case TypeWorkflowRun:
		runPayload["workflow_id"] = stringValue(job.WorkflowID)
		runPayload["workflow_version_id"] = stringValue(job.WorkflowVersionID)
		return []events.AppendInput{
			jobEventInput(req, job, events.TypeJobStarted, "running", "ok", jobPayload),
			workflowRunEventInput(req, job, events.TypeWorkflowRunStarted, "running", "ok", runPayload),
		}
	default:
		runPayload["script_id"] = stringValue(job.ScriptID)
		runPayload["script_version_id"] = stringValue(job.ScriptVersionID)
		return []events.AppendInput{
			jobEventInput(req, job, events.TypeJobStarted, "running", "ok", jobPayload),
			scriptRunEventInput(req, job, events.TypeScriptRunStarted, "running", "ok", runPayload),
		}
	}
}

func completedEventInputs(req requestctx.Context, job Job, attempt Attempt, exitCode, artifactCount int) []events.AppendInput {
	jobPayload := map[string]any{
		"job_id":         job.JobID,
		"attempt_id":     attempt.JobAttemptID,
		"exit_code":      exitCode,
		"artifact_count": artifactCount,
	}
	runPayload := map[string]any{
		"job_id":         job.JobID,
		"artifact_count": artifactCount,
	}
	switch job.JobType {
	case TypeWorkflowRun:
		runPayload["workflow_id"] = stringValue(job.WorkflowID)
		runPayload["workflow_version_id"] = stringValue(job.WorkflowVersionID)
		return []events.AppendInput{
			jobEventInput(req, job, events.TypeJobCompleted, "completed", "ok", jobPayload),
			workflowRunEventInput(req, job, events.TypeWorkflowRunCompleted, "completed", "ok", runPayload),
		}
	default:
		runPayload["script_id"] = stringValue(job.ScriptID)
		runPayload["script_version_id"] = stringValue(job.ScriptVersionID)
		return []events.AppendInput{
			jobEventInput(req, job, events.TypeJobCompleted, "completed", "ok", jobPayload),
			scriptRunEventInput(req, job, events.TypeScriptRunCompleted, "completed", "ok", runPayload),
		}
	}
}

func failedEventInputs(req requestctx.Context, job Job, attempt Attempt, jobEventType, runEventType, status, code, message string) []events.AppendInput {
	jobPayload := map[string]any{
		"job_id":        job.JobID,
		"attempt_id":    attempt.JobAttemptID,
		"failure_code":  code,
		"failure_error": message,
	}
	runPayload := map[string]any{
		"job_id":        job.JobID,
		"failure_code":  code,
		"failure_error": message,
	}
	switch job.JobType {
	case TypeWorkflowRun:
		runPayload["workflow_id"] = stringValue(job.WorkflowID)
		runPayload["workflow_version_id"] = stringValue(job.WorkflowVersionID)
		return []events.AppendInput{
			jobEventInput(req, job, jobEventType, status, "error", jobPayload),
			workflowRunEventInput(req, job, runEventType, status, "error", runPayload),
		}
	default:
		runPayload["script_id"] = stringValue(job.ScriptID)
		runPayload["script_version_id"] = stringValue(job.ScriptVersionID)
		return []events.AppendInput{
			jobEventInput(req, job, jobEventType, status, "error", jobPayload),
			scriptRunEventInput(req, job, runEventType, status, "error", runPayload),
		}
	}
}

func readTail(path string, maxBytes int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	start := int64(0)
	if info.Size() > maxBytes {
		start = info.Size() - maxBytes
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return "", err
	}
	payload, err := io.ReadAll(file)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func commandExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func outputJSONOrEmpty(value []byte) []byte {
	if len(value) == 0 {
		return []byte(`{}`)
	}
	return value
}

func inferOutputType(value json.RawMessage) string {
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		return "json"
	}
	switch decoded.(type) {
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "json"
	}
}

func jobSelectSQL() string {
	return `SELECT ` + jobColumns() + ` FROM jobs.jobs j`
}

func jobColumns() string {
	return `
		j.job_id, j.job_type, j.status, j.origin_actor_id, j.origin_node_id,
		j.execution_node_id, j.scope_id, j.target_kind, j.target_id, j.script_id,
		j.script_version_id, j.workflow_id, j.workflow_version_id,
		j.source_object_id, j.source_object_version_id, j.input_json,
		j.output_json, j.progress_json, j.artifact_refs_json,
		j.workdir_path, j.attempt_count, j.max_attempts, j.timeout_seconds,
		j.lease_owner, j.lease_expires_at, j.priority, j.next_attempt_at,
		j.manual_action_required, j.last_worker_run_id, j.last_heartbeat_at,
		j.cancel_requested_at, j.cancel_requested_by_actor_id, j.exit_code, j.failure_code,
		j.failure_message, j.failure_attention_status, j.failure_attention_updated_at,
		j.failure_attention_updated_by_actor_id, j.failure_attention_note,
		j.created_at, j.queued_at, j.started_at, j.completed_at, j.failed_at,
		j.cancelled_at, j.updated_at, j.metadata`
}

func jobReturningColumns() string {
	return `
		job_id, job_type, status, origin_actor_id, origin_node_id,
		execution_node_id, scope_id, target_kind, target_id, script_id,
		script_version_id, workflow_id, workflow_version_id, source_object_id,
		source_object_version_id, input_json, output_json, progress_json,
		artifact_refs_json, workdir_path, attempt_count, max_attempts,
		timeout_seconds, lease_owner, lease_expires_at, priority,
		next_attempt_at, manual_action_required, last_worker_run_id,
		last_heartbeat_at, cancel_requested_at, cancel_requested_by_actor_id,
		exit_code, failure_code, failure_message, failure_attention_status,
		failure_attention_updated_at, failure_attention_updated_by_actor_id,
		failure_attention_note, created_at, queued_at, started_at, completed_at,
		failed_at, cancelled_at, updated_at, metadata`
}

func runnerSelectSQL() string {
	return `SELECT ` + runnerColumns() + ` FROM jobs.runners r`
}

func runnerColumns() string {
	return `
		r.runner_id, r.runner_key, r.node_id, r.runner_type, r.supported_job_types,
		r.supported_runtimes, r.status, r.last_heartbeat_at, r.current_job_id,
		r.version, r.created_at, r.updated_at, r.metadata`
}

func runnerReturningColumns() string {
	return `
		runner_id, runner_key, node_id, runner_type, supported_job_types,
		supported_runtimes, status, last_heartbeat_at, current_job_id, version,
		created_at, updated_at, metadata`
}

func attemptSelectSQL() string {
	return `SELECT ` + attemptColumns() + ` FROM jobs.job_attempts ja`
}

func attemptColumns() string {
	return `
		ja.job_attempt_id, ja.job_id, ja.attempt_number, ja.runner_id, ja.status,
		ja.workdir_path, ja.input_dir, ja.output_dir, ja.artifact_dir, ja.temp_dir,
		ja.stdout_log_path, ja.stderr_log_path, ja.runner_log_path, ja.result_path,
		ja.exit_code, ja.started_at, ja.completed_at, ja.failed_at, ja.metadata`
}

func attemptReturningColumns() string {
	return `
		job_attempt_id, job_id, attempt_number, runner_id, status, workdir_path,
		input_dir, output_dir, artifact_dir, temp_dir, stdout_log_path,
		stderr_log_path, runner_log_path, result_path, exit_code, started_at,
		completed_at, failed_at, metadata`
}

func jobLogSelectSQL() string {
	return `SELECT ` + jobLogColumns() + ` FROM jobs.job_logs jl`
}

func jobLogColumns() string {
	return `
		jl.job_log_id, jl.job_id, jl.job_attempt_id, jl.stream, jl.storage_kind,
		jl.path, jl.byte_count, jl.tail_text, jl.created_at, jl.updated_at,
		jl.metadata`
}

func jobLogReturningColumns() string {
	return `
		job_log_id, job_id, job_attempt_id, stream, storage_kind, path,
		byte_count, tail_text, created_at, updated_at, metadata`
}

func jobOutputSelectSQL() string {
	return `SELECT ` + jobOutputColumns() + ` FROM jobs.job_outputs jo`
}

func jobOutputColumns() string {
	return `
		jo.job_output_id, jo.job_id, jo.job_attempt_id, jo.output_key,
		jo.output_type, jo.value_json, jo.artifact_id, jo.status, jo.created_at,
		jo.metadata`
}

func jobOutputReturningColumns() string {
	return `
		job_output_id, job_id, job_attempt_id, output_key, output_type,
		value_json, artifact_id, status, created_at, metadata`
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanJob(scanner rowScanner) (Job, error) {
	var job Job
	var scopeID, targetKind, targetID, scriptID, scriptVersionID, workflowID, workflowVersionID, sourceObjectID, sourceVersionID sql.NullString
	var workdirPath, leaseOwner, lastWorkerRunID, cancelRequestedByID, failureCode, failureMessage sql.NullString
	var failureAttentionStatus, failureAttentionByActorID, failureAttentionNote sql.NullString
	var leaseExpiresAt, nextAttemptAt, lastHeartbeatAt, cancelRequestedAt, failureAttentionUpdatedAt, queuedAt, startedAt, completedAt, failedAt, cancelledAt sql.NullTime
	var timeoutSeconds, exitCode sql.NullInt64
	var inputJSON, outputJSON, progressJSON, artifactRefsJSON, metadata []byte
	if err := scanner.Scan(
		&job.JobID, &job.JobType, &job.Status, &job.OriginActorID, &job.OriginNodeID,
		&job.ExecutionNodeID, &scopeID, &targetKind, &targetID, &scriptID,
		&scriptVersionID, &workflowID, &workflowVersionID, &sourceObjectID,
		&sourceVersionID, &inputJSON, &outputJSON, &progressJSON,
		&artifactRefsJSON, &workdirPath, &job.AttemptCount, &job.MaxAttempts,
		&timeoutSeconds, &leaseOwner, &leaseExpiresAt, &job.Priority,
		&nextAttemptAt, &job.ManualAction, &lastWorkerRunID, &lastHeartbeatAt,
		&cancelRequestedAt, &cancelRequestedByID, &exitCode, &failureCode,
		&failureMessage, &failureAttentionStatus, &failureAttentionUpdatedAt,
		&failureAttentionByActorID, &failureAttentionNote, &job.CreatedAt,
		&queuedAt, &startedAt, &completedAt, &failedAt, &cancelledAt,
		&job.UpdatedAt, &metadata,
	); err != nil {
		return Job{}, err
	}
	job.ScopeID = stringPtr(scopeID)
	job.TargetKind = stringPtr(targetKind)
	job.TargetID = stringPtr(targetID)
	job.ScriptID = stringPtr(scriptID)
	job.ScriptVersionID = stringPtr(scriptVersionID)
	job.WorkflowID = stringPtr(workflowID)
	job.WorkflowVersionID = stringPtr(workflowVersionID)
	job.SourceObjectID = stringPtr(sourceObjectID)
	job.SourceObjectVersionID = stringPtr(sourceVersionID)
	job.WorkdirPath = stringPtr(workdirPath)
	job.TimeoutSeconds = intPtr(timeoutSeconds)
	job.LeaseOwner = stringPtr(leaseOwner)
	job.LeaseExpiresAt = timePtr(leaseExpiresAt)
	job.NextAttemptAt = timePtr(nextAttemptAt)
	job.LastWorkerRunID = stringPtr(lastWorkerRunID)
	job.LastHeartbeatAt = timePtr(lastHeartbeatAt)
	job.CancelRequestedAt = timePtr(cancelRequestedAt)
	job.CancelRequestedByID = stringPtr(cancelRequestedByID)
	job.ExitCode = intPtr(exitCode)
	job.FailureCode = stringPtr(failureCode)
	job.FailureMessage = stringPtr(failureMessage)
	job.FailureAttentionStatus = EffectiveFailureAttentionStatus(failureAttentionStatus.String)
	job.FailureAttentionUpdatedAt = timePtr(failureAttentionUpdatedAt)
	job.FailureAttentionUpdatedByActorID = stringPtr(failureAttentionByActorID)
	job.FailureAttentionNote = failureAttentionNote.String
	job.QueuedAt = timePtr(queuedAt)
	job.StartedAt = timePtr(startedAt)
	job.CompletedAt = timePtr(completedAt)
	job.FailedAt = timePtr(failedAt)
	job.CancelledAt = timePtr(cancelledAt)
	job.InputJSON = jsonOrEmpty(inputJSON)
	job.OutputJSON = jsonOrEmpty(outputJSON)
	job.ProgressJSON = jsonOrEmpty(progressJSON)
	job.ArtifactRefsJSON = jsonOrEmptyArray(artifactRefsJSON)
	job.Metadata = jsonOrEmpty(metadata)
	return job, nil
}

func scanAttempt(scanner rowScanner) (Attempt, error) {
	var attempt Attempt
	var runnerID, stdoutLogPath, stderrLogPath, runnerLogPath, resultPath sql.NullString
	var exitCode sql.NullInt64
	var startedAt, completedAt, failedAt sql.NullTime
	var metadata []byte
	if err := scanner.Scan(
		&attempt.JobAttemptID, &attempt.JobID, &attempt.AttemptNumber,
		&runnerID, &attempt.Status, &attempt.WorkdirPath, &attempt.InputDir,
		&attempt.OutputDir, &attempt.ArtifactDir, &attempt.TempDir,
		&stdoutLogPath, &stderrLogPath, &runnerLogPath, &resultPath, &exitCode,
		&startedAt, &completedAt, &failedAt, &metadata,
	); err != nil {
		return Attempt{}, err
	}
	attempt.RunnerID = stringPtr(runnerID)
	attempt.StdoutLogPath = stringPtr(stdoutLogPath)
	attempt.StderrLogPath = stringPtr(stderrLogPath)
	attempt.RunnerLogPath = stringPtr(runnerLogPath)
	attempt.ResultPath = stringPtr(resultPath)
	attempt.ExitCode = intPtr(exitCode)
	attempt.StartedAt = timePtr(startedAt)
	attempt.CompletedAt = timePtr(completedAt)
	attempt.FailedAt = timePtr(failedAt)
	attempt.Metadata = jsonOrEmpty(metadata)
	return attempt, nil
}

func scanRunner(scanner rowScanner) (Runner, error) {
	var runner Runner
	var supportedJobTypes, supportedRuntimes, metadata []byte
	var lastHeartbeatAt sql.NullTime
	var currentJobID sql.NullString
	if err := scanner.Scan(
		&runner.RunnerID, &runner.RunnerKey, &runner.NodeID, &runner.RunnerType,
		&supportedJobTypes, &supportedRuntimes, &runner.Status, &lastHeartbeatAt,
		&currentJobID, &runner.Version, &runner.CreatedAt, &runner.UpdatedAt,
		&metadata,
	); err != nil {
		return Runner{}, err
	}
	runner.SupportedJobTypes = jsonOrEmptyArray(supportedJobTypes)
	runner.SupportedRuntimes = jsonOrEmptyArray(supportedRuntimes)
	runner.LastHeartbeatAt = timePtr(lastHeartbeatAt)
	runner.CurrentJobID = stringPtr(currentJobID)
	runner.Metadata = jsonOrEmpty(metadata)
	return runner, nil
}

func scanJobLog(scanner rowScanner) (JobLog, error) {
	var log JobLog
	var attemptID sql.NullString
	var metadata []byte
	if err := scanner.Scan(
		&log.JobLogID, &log.JobID, &attemptID, &log.Stream, &log.StorageKind,
		&log.Path, &log.ByteCount, &log.TailText, &log.CreatedAt, &log.UpdatedAt,
		&metadata,
	); err != nil {
		return JobLog{}, err
	}
	log.JobAttemptID = stringPtr(attemptID)
	log.Metadata = jsonOrEmpty(metadata)
	return log, nil
}

func scanJobOutput(scanner rowScanner) (JobOutput, error) {
	var output JobOutput
	var attemptID, artifactID sql.NullString
	var valueJSON, metadata []byte
	if err := scanner.Scan(
		&output.JobOutputID, &output.JobID, &attemptID, &output.OutputKey,
		&output.OutputType, &valueJSON, &artifactID, &output.Status,
		&output.CreatedAt, &metadata,
	); err != nil {
		return JobOutput{}, err
	}
	output.JobAttemptID = stringPtr(attemptID)
	output.ArtifactID = stringPtr(artifactID)
	output.ValueJSON = jsonOrEmpty(valueJSON)
	output.Metadata = jsonOrEmpty(metadata)
	return output, nil
}

func (s Service) resolveJob(ctx context.Context, ref string) (Job, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Job{}, fmt.Errorf("job ref is required")
	}
	row := s.DB.QueryRowContext(ctx, jobSelectSQL()+`
		WHERE j.job_id = $1
		LIMIT 1
	`, ref)
	return scanJob(row)
}

func (s Service) listAttempts(ctx context.Context, jobID string) ([]Attempt, error) {
	rows, err := s.DB.QueryContext(ctx, attemptSelectSQL()+`
		WHERE ja.job_id = $1
		ORDER BY ja.attempt_number ASC
	`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	attempts := []Attempt{}
	for rows.Next() {
		attempt, err := scanAttempt(rows)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, attempt)
	}
	return attempts, rows.Err()
}

func (s Service) listOutputs(ctx context.Context, jobID string) ([]JobOutput, error) {
	rows, err := s.DB.QueryContext(ctx, jobOutputSelectSQL()+`
		WHERE jo.job_id = $1
		ORDER BY jo.created_at ASC, jo.output_key
	`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	outputs := []JobOutput{}
	for rows.Next() {
		output, err := scanJobOutput(rows)
		if err != nil {
			return nil, err
		}
		outputs = append(outputs, output)
	}
	return outputs, rows.Err()
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func stringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func timePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}

func intPtr(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	converted := int(value.Int64)
	return &converted
}

func floatPtr(value float64) *float64 {
	return &value
}

func mustJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func jsonOrEmpty(value []byte) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(value)
}

func jsonOrEmptyArray(value []byte) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`[]`)
	}
	return json.RawMessage(value)
}
