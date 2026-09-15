package runtimes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"loom.local/loom/internal/provenance"
	"loom.local/loom/internal/workers"
)

const (
	provenanceArchivistConfigSchema  = "provenance_archivist.config.v1"
	provenanceArchivistRequestSchema = "provenance_archivist.request.v1"
	provenanceArchivistResultSchema  = provenance.ArchivistResultSchemaVersion
	provenanceArchivistCheckpointKey = "cursor"
)

type ProvenanceArchivistRuntime struct {
	Archivist  *provenance.Archivist
	LeaseGuard func(context.Context, workers.RunContext) error
}

type provenanceArchivistConfig struct {
	SchemaVersion  string `json:"schema_version"`
	PolicyVersion  string `json:"policy_version"`
	CandidateLimit int    `json:"candidate_limit"`
	EvidenceLimit  int    `json:"evidence_limit"`
}

type provenanceArchivistRequest struct {
	SchemaVersion  string                         `json:"schema_version,omitempty"`
	CandidateLimit int                            `json:"candidate_limit,omitempty"`
	EvidenceLimit  int                            `json:"evidence_limit,omitempty"`
	Domain         string                         `json:"domain,omitempty"`
	Visibility     string                         `json:"visibility,omitempty"`
	CandidateIDs   []provenance.SemanticID        `json:"candidate_ids,omitempty"`
	Decisions      []provenance.ArchivistDecision `json:"decisions,omitempty"`
}

type provenanceArchivistRunMetadata struct {
	SchemaVersion   string                     `json:"schema_version"`
	RunOnceReason   string                     `json:"run_once_reason,omitempty"`
	RequestMetadata provenanceArchivistRequest `json:"request_metadata"`
}

type provenanceArchivistCheckpoint struct {
	SchemaVersion string                      `json:"schema_version"`
	PolicyVersion string                      `json:"policy_version"`
	Cursor        *provenance.ArchivistCursor `json:"cursor,omitempty"`
	LastRunID     string                      `json:"last_run_id"`
}

func NewProvenanceArchivistRuntime(transport provenance.ArchivistTransport) ProvenanceArchivistRuntime {
	archivist, _ := provenance.NewArchivist(transport)
	return ProvenanceArchivistRuntime{Archivist: archivist}
}

func (runtime ProvenanceArchivistRuntime) Kind() string { return workers.KindProvenanceArchivist }

func (runtime ProvenanceArchivistRuntime) Describe() workers.KindDescriptor {
	return workers.KindDescriptor{
		WorkerKind: workers.KindProvenanceArchivist, DisplayName: "Provenance Archivist",
		Description:  "Runs a bounded manual-first reconciliation cycle over provenance candidates.",
		RuntimeOwner: workers.RuntimeOwnerLoomd, RuntimePackage: "loom.core.provenance",
		Status: workers.KindStatusActive, SupportedLocalities: []string{workers.LocalityMainOwned},
		DefaultTickPolicyJSON:        json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"manual"}`),
		DefaultConcurrencyPolicyJSON: json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
		DefaultRetryPolicyJSON:       json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
		DefaultTimeoutPolicyJSON:     json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":60}`),
		DefaultResourceLimitsJSON:    json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"light"}`),
		ConfigSchemaJSON:             json.RawMessage(`{"schema_version":"provenance_archivist.config_schema.v1","type":"object","additionalProperties":false}`),
		CheckpointSchemaJSON:         json.RawMessage(`{"schema_version":"provenance_archivist.checkpoint_schema.v1","type":"object","additionalProperties":false}`),
		ResultSchemaJSON:             json.RawMessage(`{"schema_version":"provenance_archivist.result_schema.v1","type":"object","additionalProperties":false}`),
		Metadata:                     json.RawMessage(`{"schema_version":"worker_kind.metadata.v0.2","builtin":true,"manual_only":true,"scheduling_enabled":false}`),
	}
}

func (runtime ProvenanceArchivistRuntime) DefaultConfig() json.RawMessage {
	return mustWorkerJSON(provenanceArchivistConfig{
		SchemaVersion: provenanceArchivistConfigSchema, PolicyVersion: provenance.ArchivistPolicyVersion,
		CandidateLimit: provenance.DefaultArchivistCandidateLimit, EvidenceLimit: provenance.DefaultArchivistEvidenceLimit,
	})
}

func (runtime ProvenanceArchivistRuntime) DefaultInstances() []workers.InstanceDescriptor {
	return []workers.InstanceDescriptor{{
		WorkerKey: "main.provenance_archivist", WorkerKind: workers.KindProvenanceArchivist,
		DisplayName: "Provenance Archivist", Description: "Runs an explicitly invoked bounded provenance reconciliation cycle.",
		Locality: workers.LocalityMainOwned, ConfigJSON: runtime.DefaultConfig(),
		TickPolicyJSON:     json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"manual"}`),
		ConcurrencyJSON:    json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
		RetryPolicyJSON:    json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
		TimeoutPolicyJSON:  json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":60}`),
		ResourceLimitsJSON: json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"light"}`),
		VisibilityJSON:     json.RawMessage(`{}`),
		Metadata:           json.RawMessage(`{"schema_version":"worker_instance.metadata.v0.2","seeded_by":"workers.SeedBuiltins","manual_only":true,"scheduling_enabled":false}`),
	}}
}

func (runtime ProvenanceArchivistRuntime) ValidateConfig(_ context.Context, raw json.RawMessage) error {
	_, err := parseProvenanceArchivistConfig(raw)
	return err
}

func (runtime ProvenanceArchivistRuntime) RunOnce(ctx context.Context, run workers.RunContext) (workers.RunResult, error) {
	if runtime.Archivist == nil {
		return workers.RunResult{}, errors.New("provenance Archivist service is not configured")
	}
	if run.Run.TriggerKind != workers.TriggerManual && run.Run.TriggerKind != workers.TriggerRetry {
		return workers.RunResult{}, fmt.Errorf("provenance Archivist only accepts manual or explicit retry runs, got %s", run.Run.TriggerKind)
	}
	policy, err := workers.ParseTickPolicy(run.Instance.TickPolicyJSON)
	if err != nil {
		return workers.RunResult{}, err
	}
	if policy.Mode != workers.TickModeManual || policy.RunOnStartup || policy.NextAfter(time.Now().UTC()) != nil {
		return workers.RunResult{}, errors.New("provenance Archivist scheduling is disabled")
	}
	config, err := parseProvenanceArchivistConfig(run.Instance.ConfigJSON)
	if err != nil {
		return workers.RunResult{}, err
	}
	requestMetadata, err := parseProvenanceArchivistRequest(run.Run.Metadata)
	if err != nil {
		return workers.RunResult{}, err
	}
	if requestMetadata.CandidateLimit == 0 {
		requestMetadata.CandidateLimit = config.CandidateLimit
	}
	if requestMetadata.EvidenceLimit == 0 {
		requestMetadata.EvidenceLimit = config.EvidenceLimit
	}
	if requestMetadata.CandidateLimit > config.CandidateLimit || requestMetadata.EvidenceLimit > config.EvidenceLimit {
		return workers.RunResult{}, errors.New("provenance Archivist request cannot expand configured bounds")
	}
	explicitSelection := len(requestMetadata.CandidateIDs) > 0 || len(requestMetadata.Decisions) > 0
	var cursor *provenance.ArchivistCursor
	if !explicitSelection {
		cursor, err = provenanceArchivistCursor(run.Checkpoints[provenanceArchivistCheckpointKey])
		if err != nil {
			return workers.RunResult{}, err
		}
	}
	guard := func(guardCtx context.Context) error {
		if runtime.LeaseGuard != nil {
			return runtime.LeaseGuard(guardCtx, run)
		}
		return provenanceArchivistLeaseFence(guardCtx, run)
	}
	result, err := runtime.Archivist.Run(ctx, provenance.ArchivistRunRequest{
		CandidateLimit: requestMetadata.CandidateLimit, EvidenceLimit: requestMetadata.EvidenceLimit,
		Domain: requestMetadata.Domain, Visibility: requestMetadata.Visibility, Cursor: cursor,
		CandidateIDs: requestMetadata.CandidateIDs, Decisions: requestMetadata.Decisions,
		IdempotencyKey: run.IdempotencyKey,
		Producer: provenance.ProducerIdentity{
			ProducerID: "loom.provenance_archivist", ProducerKind: "loom_worker", TaskID: run.Instance.WorkerKey,
			Metadata: map[string]any{"policy_version": provenance.ArchivistPolicyVersion},
		},
		LeaseGuard: guard,
	})
	if err != nil {
		return workers.RunResult{}, err
	}
	summary, err := json.Marshal(result)
	if err != nil {
		return workers.RunResult{}, err
	}
	workerResult := workers.RunResult{
		Status: workers.RunStatusSucceeded, ResultSummary: summary,
		Counters: map[string]int64{
			"selected": int64(result.Selected), "examined": int64(result.Examined),
			"accepted": int64(result.Accepted), "consolidated": int64(result.Consolidated),
			"related": int64(result.Related), "rejected": int64(result.Rejected),
			"deferred": int64(result.Deferred), "stale": int64(result.Stale),
			"replayed": int64(result.Replayed), "exact_reads": int64(result.ExactReads),
		},
		ResourceUsage: mustWorkerJSON(map[string]any{
			"schema_version":  "provenance_archivist.resource_usage.v1",
			"candidate_limit": requestMetadata.CandidateLimit, "evidence_limit": requestMetadata.EvidenceLimit,
		}),
	}
	if !explicitSelection {
		checkpoint := provenanceArchivistCheckpoint{
			SchemaVersion: provenance.ArchivistCheckpointSchema, PolicyVersion: provenance.ArchivistPolicyVersion,
			Cursor: result.NextCursor, LastRunID: run.Run.WorkerRunID,
		}
		workerResult.CheckpointUpdates = []workers.CheckpointUpdate{{
			Key: provenanceArchivistCheckpointKey, SchemaVersion: provenance.ArchivistCheckpointSchema,
			Value: mustWorkerJSON(checkpoint), Metadata: json.RawMessage(`{"manual_only":true,"scheduling_enabled":false}`),
		}}
	}
	return workerResult, nil
}

func parseProvenanceArchivistConfig(raw json.RawMessage) (provenanceArchivistConfig, error) {
	var config provenanceArchivistConfig
	if err := decodeProvenanceArchivistJSON(raw, &config); err != nil {
		return provenanceArchivistConfig{}, fmt.Errorf("decode provenance Archivist config: %w", err)
	}
	if config.SchemaVersion != provenanceArchivistConfigSchema || config.PolicyVersion != provenance.ArchivistPolicyVersion {
		return provenanceArchivistConfig{}, errors.New("provenance Archivist config requires the frozen v1 schema and policy")
	}
	if config.CandidateLimit < 1 || config.CandidateLimit > provenance.MaximumArchivistCandidateLimit {
		return provenanceArchivistConfig{}, fmt.Errorf("provenance Archivist candidate_limit must be between 1 and %d", provenance.MaximumArchivistCandidateLimit)
	}
	if config.EvidenceLimit < 1 || config.EvidenceLimit > provenance.MaximumArchivistEvidenceLimit {
		return provenanceArchivistConfig{}, fmt.Errorf("provenance Archivist evidence_limit must be between 1 and %d", provenance.MaximumArchivistEvidenceLimit)
	}
	return config, nil
}

func parseProvenanceArchivistRequest(raw json.RawMessage) (provenanceArchivistRequest, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return provenanceArchivistRequest{SchemaVersion: provenanceArchivistRequestSchema}, nil
	}
	var metadata provenanceArchivistRunMetadata
	if err := decodeProvenanceArchivistJSON(raw, &metadata); err != nil {
		return provenanceArchivistRequest{}, fmt.Errorf("decode provenance Archivist worker metadata: %w", err)
	}
	if metadata.SchemaVersion != "worker_run.metadata.v0.2" {
		return provenanceArchivistRequest{}, errors.New("provenance Archivist requires worker_run.metadata.v0.2")
	}
	request := metadata.RequestMetadata
	if request.SchemaVersion == "" {
		request.SchemaVersion = provenanceArchivistRequestSchema
	}
	if request.SchemaVersion != provenanceArchivistRequestSchema {
		return provenanceArchivistRequest{}, errors.New("provenance Archivist request schema is unsupported")
	}
	return request, nil
}

func provenanceArchivistCursor(checkpoint workers.WorkerCheckpoint) (*provenance.ArchivistCursor, error) {
	if checkpoint.WorkerCheckpointID == "" {
		return nil, nil
	}
	if checkpoint.SchemaVersion != provenance.ArchivistCheckpointSchema {
		return nil, errors.New("provenance Archivist checkpoint schema is unsupported")
	}
	var value provenanceArchivistCheckpoint
	if err := decodeProvenanceArchivistJSON(checkpoint.CheckpointJSON, &value); err != nil {
		return nil, err
	}
	if value.SchemaVersion != provenance.ArchivistCheckpointSchema || value.PolicyVersion != provenance.ArchivistPolicyVersion {
		return nil, errors.New("provenance Archivist checkpoint policy is stale")
	}
	return value.Cursor, nil
}

func provenanceArchivistLeaseFence(ctx context.Context, run workers.RunContext) error {
	if run.Service.DB == nil {
		return errors.New("worker database is not configured")
	}
	var active bool
	err := run.Service.DB.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM workers.worker_leases
			WHERE worker_lease_id = $1
			  AND worker_instance_id = $2
			  AND generation = $3
			  AND run_id = $4
			  AND lease_status = 'active'
			  AND expires_at > now()
		)
	`, run.Lease.WorkerLeaseID, run.Instance.WorkerInstanceID, run.Lease.Generation, run.Run.WorkerRunID).Scan(&active)
	if err != nil {
		return err
	}
	if !active {
		return fmt.Errorf("worker lease %s generation %d is no longer active", run.Lease.WorkerLeaseID, run.Lease.Generation)
	}
	return nil
}

func decodeProvenanceArchivistJSON(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("request must contain one JSON object")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

var _ workers.Runtime = ProvenanceArchivistRuntime{}
var _ workers.DefaultInstanceProvider = ProvenanceArchivistRuntime{}
