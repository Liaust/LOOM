package workflows

import (
	"encoding/json"
	"time"
)

const (
	WorkflowStatusRegistered = "registered"
	WorkflowStatusActive     = "active"

	VersionStatusActive        = "active"
	VersionStatusPendingReview = "pending_review"
)

type Workflow struct {
	WorkflowID       string          `json:"workflow_id"`
	Slug             string          `json:"slug"`
	Name             string          `json:"name"`
	Description      string          `json:"description"`
	OwnerScopeID     *string         `json:"owner_scope_id,omitempty"`
	CreatedByActorID string          `json:"created_by_actor_id"`
	ActiveVersionID  *string         `json:"active_version_id,omitempty"`
	Status           string          `json:"status"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
	Metadata         json.RawMessage `json:"metadata"`
}

type WorkflowVersion struct {
	WorkflowVersionID  string          `json:"workflow_version_id"`
	WorkflowID         string          `json:"workflow_id"`
	VersionLabel       string          `json:"version_label"`
	ManifestJSON       json.RawMessage `json:"manifest_json"`
	ManifestHash       string          `json:"manifest_hash"`
	ContentHash        string          `json:"content_hash"`
	PackageRoot        string          `json:"package_root"`
	EntrypointJSON     json.RawMessage `json:"entrypoint_json"`
	RuntimeJSON        json.RawMessage `json:"runtime_json"`
	InputSchemaJSON    json.RawMessage `json:"input_schema_json"`
	OutputSchemaJSON   json.RawMessage `json:"output_schema_json"`
	ExecutionJSON      json.RawMessage `json:"execution_json"`
	ArtifactPolicyJSON json.RawMessage `json:"artifact_policy_json"`
	UsageDocumentsJSON json.RawMessage `json:"usage_documents_json"`
	Status             string          `json:"status"`
	CreatedByActorID   string          `json:"created_by_actor_id"`
	CreatedAt          time.Time       `json:"created_at"`
	ActivatedAt        *time.Time      `json:"activated_at,omitempty"`
	Metadata           json.RawMessage `json:"metadata"`
}

type WorkflowDetail struct {
	Workflow      Workflow          `json:"workflow"`
	ActiveVersion *WorkflowVersion  `json:"active_version,omitempty"`
	Versions      []WorkflowVersion `json:"versions,omitempty"`
}

type RegisterInput struct {
	ManifestPath string          `json:"manifest_path"`
	ProjectRef   string          `json:"project_ref,omitempty"`
	ScopeRef     string          `json:"scope_ref,omitempty"`
	SlugOverride string          `json:"slug_override,omitempty"`
	Activate     bool            `json:"activate,omitempty"`
	Metadata     json.RawMessage `json:"metadata,omitempty"`
}

type RegisterResult struct {
	Workflow        Workflow        `json:"workflow"`
	Version         WorkflowVersion `json:"version"`
	WorkflowCreated bool            `json:"workflow_created"`
	VersionCreated  bool            `json:"version_created"`
	Activated       bool            `json:"activated"`
	EventIDs        []string        `json:"event_ids,omitempty"`
}

type ListFilter struct {
	Limit      int
	Status     string
	ProjectRef string
	ScopeRef   string
}
