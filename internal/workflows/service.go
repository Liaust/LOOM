package workflows

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
)

type Service struct {
	DB *sql.DB
}

func NewService(db *sql.DB) Service {
	return Service{DB: db}
}

func (s Service) RegisterWorkflow(ctx context.Context, req requestctx.Context, input RegisterInput) (RegisterResult, error) {
	manifestPath := strings.TrimSpace(input.ManifestPath)
	if manifestPath == "" {
		return RegisterResult{}, fmt.Errorf("manifest_path is required")
	}
	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		return RegisterResult{}, err
	}
	slug := strings.TrimSpace(input.SlugOverride)
	if slug == "" {
		slug = manifest.Workflow.ID
	}
	if !workflowIDPattern.MatchString(slug) {
		return RegisterResult{}, fmt.Errorf("workflow slug override must match %s", workflowIDPattern.String())
	}
	packageRoot, err := PackageRoot(manifestPath)
	if err != nil {
		return RegisterResult{}, err
	}
	manifestJSON, err := NormalizeManifestJSON(manifest)
	if err != nil {
		return RegisterResult{}, err
	}
	manifestHash, err := HashManifest(manifest)
	if err != nil {
		return RegisterResult{}, err
	}
	contentHash, err := HashPackage(packageRoot)
	if err != nil {
		return RegisterResult{}, err
	}
	metadata, err := normalizeJSONObject(input.Metadata)
	if err != nil {
		return RegisterResult{}, fmt.Errorf("workflow metadata must be a JSON object: %w", err)
	}
	ownerScopeID, err := s.resolveOptionalScope(ctx, input.ProjectRef, input.ScopeRef)
	if err != nil {
		return RegisterResult{}, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return RegisterResult{}, err
	}
	defer tx.Rollback()

	workflow, workflowCreated, err := s.ensureWorkflowTx(ctx, tx, req, manifest, slug, ownerScopeID, metadata)
	if err != nil {
		return RegisterResult{}, err
	}
	version, versionCreated, err := s.ensureWorkflowVersionTx(ctx, tx, req, workflow, manifest, manifestJSON, manifestHash, contentHash, packageRoot)
	if err != nil {
		return RegisterResult{}, err
	}

	activated := false
	if version.Status == VersionStatusActive || workflow.ActiveVersionID == nil || input.Activate {
		version, err = activateWorkflowVersionTx(ctx, tx, workflow.WorkflowID, version.WorkflowVersionID)
		if err != nil {
			return RegisterResult{}, err
		}
		workflow, err = getWorkflowTx(ctx, tx, workflow.WorkflowID)
		if err != nil {
			return RegisterResult{}, err
		}
		activated = true
	}
	if err := tx.Commit(); err != nil {
		return RegisterResult{}, err
	}

	return RegisterResult{
		Workflow:        workflow,
		Version:         version,
		WorkflowCreated: workflowCreated,
		VersionCreated:  versionCreated,
		Activated:       activated,
	}, nil
}

func (s Service) ListWorkflows(ctx context.Context, filter ListFilter) ([]Workflow, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	query := workflowSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	if strings.TrimSpace(filter.Status) != "" {
		add("w.status =", strings.TrimSpace(filter.Status))
	}
	if strings.TrimSpace(filter.ProjectRef) != "" || strings.TrimSpace(filter.ScopeRef) != "" {
		scopeID, err := s.resolveOptionalScope(ctx, filter.ProjectRef, filter.ScopeRef)
		if err != nil {
			return nil, err
		}
		add("w.owner_scope_id =", scopeID)
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY w.updated_at DESC, w.slug LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	workflows := []Workflow{}
	for rows.Next() {
		workflow, err := scanWorkflow(rows)
		if err != nil {
			return nil, err
		}
		workflows = append(workflows, workflow)
	}
	return workflows, rows.Err()
}

func (s Service) GetWorkflow(ctx context.Context, ref string) (WorkflowDetail, error) {
	workflow, err := s.resolveWorkflow(ctx, ref)
	if err != nil {
		return WorkflowDetail{}, err
	}
	detail := WorkflowDetail{Workflow: workflow}
	if workflow.ActiveVersionID != nil {
		version, err := s.GetWorkflowVersion(ctx, *workflow.ActiveVersionID)
		if err != nil {
			return WorkflowDetail{}, err
		}
		detail.ActiveVersion = &version
	}
	versions, err := s.listVersions(ctx, workflow.WorkflowID)
	if err != nil {
		return WorkflowDetail{}, err
	}
	detail.Versions = versions
	return detail, nil
}

func (s Service) ResolveActiveVersion(ctx context.Context, ref string) (WorkflowVersion, error) {
	workflow, err := s.resolveWorkflow(ctx, ref)
	if err != nil {
		return WorkflowVersion{}, err
	}
	if workflow.ActiveVersionID == nil || strings.TrimSpace(*workflow.ActiveVersionID) == "" {
		return WorkflowVersion{}, fmt.Errorf("workflow has no active version: %s", ref)
	}
	return s.GetWorkflowVersion(ctx, *workflow.ActiveVersionID)
}

func (s Service) GetWorkflowVersion(ctx context.Context, ref string) (WorkflowVersion, error) {
	row := s.DB.QueryRowContext(ctx, workflowVersionSelectSQL()+`
		WHERE wv.workflow_version_id = $1
		LIMIT 1
	`, strings.TrimSpace(ref))
	return scanWorkflowVersion(row)
}

func (s Service) resolveWorkflow(ctx context.Context, ref string) (Workflow, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Workflow{}, fmt.Errorf("workflow ref is required")
	}
	row := s.DB.QueryRowContext(ctx, workflowSelectSQL()+`
		WHERE w.workflow_id = $1 OR w.slug = $1
		LIMIT 1
	`, ref)
	return scanWorkflow(row)
}

func (s Service) listVersions(ctx context.Context, workflowID string) ([]WorkflowVersion, error) {
	rows, err := s.DB.QueryContext(ctx, workflowVersionSelectSQL()+`
		WHERE wv.workflow_id = $1
		ORDER BY wv.created_at DESC
	`, workflowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	versions := []WorkflowVersion{}
	for rows.Next() {
		version, err := scanWorkflowVersion(rows)
		if err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	return versions, rows.Err()
}

func (s Service) ensureWorkflowTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, manifest Manifest, slug, ownerScopeID string, metadata []byte) (Workflow, bool, error) {
	workflow, err := getWorkflowTx(ctx, tx, slug)
	if err == nil {
		return workflow, false, nil
	}
	if err != sql.ErrNoRows {
		return Workflow{}, false, err
	}
	row := tx.QueryRowContext(ctx, `
		INSERT INTO packages.workflows (
			workflow_id, slug, name, description, owner_scope_id, created_by_actor_id,
			status, metadata
		)
		VALUES ($1, $2, $3, $4, nullif($5, ''), $6, $7, $8)
		RETURNING `+workflowReturningColumns()+`
	`, ids.NewWorkflowID(), slug, manifest.Workflow.Name, strings.TrimSpace(manifest.Workflow.Description), ownerScopeID, req.ActorID, WorkflowStatusRegistered, metadata)
	workflow, err = scanWorkflow(row)
	return workflow, true, err
}

func (s Service) ensureWorkflowVersionTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, workflow Workflow, manifest Manifest, manifestJSON []byte, manifestHash, contentHash, packageRoot string) (WorkflowVersion, bool, error) {
	row := tx.QueryRowContext(ctx, workflowVersionSelectSQL()+`
		WHERE wv.workflow_id = $1 AND wv.version_label = $2 AND wv.content_hash = $3
		LIMIT 1
	`, workflow.WorkflowID, manifest.Workflow.Version, contentHash)
	version, err := scanWorkflowVersion(row)
	if err == nil {
		return version, false, nil
	}
	if err != sql.ErrNoRows {
		return WorkflowVersion{}, false, err
	}

	versionStatus := VersionStatusPendingReview
	if workflow.ActiveVersionID == nil {
		versionStatus = VersionStatusActive
	}
	entrypointJSON, err := marshalJSON(manifest.Entrypoint)
	if err != nil {
		return WorkflowVersion{}, false, err
	}
	runtimeJSON, err := marshalObjectJSON(manifest.Runtime)
	if err != nil {
		return WorkflowVersion{}, false, err
	}
	inputsJSON, err := marshalObjectJSON(manifest.Inputs)
	if err != nil {
		return WorkflowVersion{}, false, err
	}
	outputsJSON, err := marshalObjectJSON(manifest.Outputs)
	if err != nil {
		return WorkflowVersion{}, false, err
	}
	executionJSON, err := marshalJSON(manifest.Execution)
	if err != nil {
		return WorkflowVersion{}, false, err
	}
	artifactPolicyJSON, err := marshalJSON(manifest.Artifacts)
	if err != nil {
		return WorkflowVersion{}, false, err
	}
	usageDocumentsJSON, err := marshalJSON(manifest.UsageDocuments)
	if err != nil {
		return WorkflowVersion{}, false, err
	}

	row = tx.QueryRowContext(ctx, `
		INSERT INTO packages.workflow_versions (
			workflow_version_id, workflow_id, version_label, manifest_json, manifest_hash,
			content_hash, package_root, entrypoint_json, runtime_json, input_schema_json,
			output_schema_json, execution_json, artifact_policy_json, usage_documents_json,
			status, created_by_actor_id, activated_at, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14,
		        $15, $16, $17, '{"version":"v0.3.1","part":"2"}'::jsonb)
		RETURNING `+workflowVersionReturningColumns()+`
	`, ids.NewWorkflowVersionID(), workflow.WorkflowID, manifest.Workflow.Version, manifestJSON, manifestHash, contentHash,
		packageRoot, entrypointJSON, runtimeJSON, inputsJSON, outputsJSON, executionJSON,
		artifactPolicyJSON, usageDocumentsJSON, versionStatus, req.ActorID, activatedAt(versionStatus))
	version, err = scanWorkflowVersion(row)
	return version, true, err
}

func getWorkflowTx(ctx context.Context, tx *sql.Tx, ref string) (Workflow, error) {
	row := tx.QueryRowContext(ctx, workflowSelectSQL()+`
		WHERE w.workflow_id = $1 OR w.slug = $1
		LIMIT 1
	`, strings.TrimSpace(ref))
	return scanWorkflow(row)
}

func activateWorkflowVersionTx(ctx context.Context, tx *sql.Tx, workflowID, versionID string) (WorkflowVersion, error) {
	if _, err := tx.ExecContext(ctx, `
		UPDATE packages.workflow_versions
		SET status = CASE WHEN workflow_version_id = $2 THEN 'active' ELSE status END,
		    activated_at = CASE WHEN workflow_version_id = $2 THEN COALESCE(activated_at, now()) ELSE activated_at END
		WHERE workflow_id = $1
	`, workflowID, versionID); err != nil {
		return WorkflowVersion{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE packages.workflows
		SET active_version_id = $2,
		    status = 'active',
		    updated_at = now()
		WHERE workflow_id = $1
	`, workflowID, versionID); err != nil {
		return WorkflowVersion{}, err
	}
	row := tx.QueryRowContext(ctx, workflowVersionSelectSQL()+`
		WHERE wv.workflow_version_id = $1
	`, versionID)
	return scanWorkflowVersion(row)
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

func workflowSelectSQL() string {
	return `SELECT ` + workflowColumns() + ` FROM packages.workflows w`
}

func workflowColumns() string {
	return `
		w.workflow_id, w.slug, w.name, w.description, w.owner_scope_id,
		w.created_by_actor_id, w.active_version_id, w.status, w.created_at,
		w.updated_at, w.metadata`
}

func workflowReturningColumns() string {
	return `
		workflow_id, slug, name, description, owner_scope_id, created_by_actor_id,
		active_version_id, status, created_at, updated_at, metadata`
}

func workflowVersionSelectSQL() string {
	return `SELECT ` + workflowVersionColumns() + ` FROM packages.workflow_versions wv`
}

func workflowVersionColumns() string {
	return `
		wv.workflow_version_id, wv.workflow_id, wv.version_label, wv.manifest_json,
		wv.manifest_hash, wv.content_hash, wv.package_root, wv.entrypoint_json,
		wv.runtime_json, wv.input_schema_json, wv.output_schema_json,
		wv.execution_json, wv.artifact_policy_json, wv.usage_documents_json,
		wv.status, wv.created_by_actor_id, wv.created_at, wv.activated_at,
		wv.metadata`
}

func workflowVersionReturningColumns() string {
	return `
		workflow_version_id, workflow_id, version_label, manifest_json, manifest_hash,
		content_hash, package_root, entrypoint_json, runtime_json,
		input_schema_json, output_schema_json, execution_json,
		artifact_policy_json, usage_documents_json, status, created_by_actor_id,
		created_at, activated_at, metadata`
}

type workflowScanner interface {
	Scan(dest ...any) error
}

func scanWorkflow(scanner workflowScanner) (Workflow, error) {
	var workflow Workflow
	var ownerScopeID sql.NullString
	var activeVersionID sql.NullString
	var metadata []byte
	if err := scanner.Scan(
		&workflow.WorkflowID,
		&workflow.Slug,
		&workflow.Name,
		&workflow.Description,
		&ownerScopeID,
		&workflow.CreatedByActorID,
		&activeVersionID,
		&workflow.Status,
		&workflow.CreatedAt,
		&workflow.UpdatedAt,
		&metadata,
	); err != nil {
		return Workflow{}, err
	}
	workflow.OwnerScopeID = stringPtr(ownerScopeID)
	workflow.ActiveVersionID = stringPtr(activeVersionID)
	workflow.Metadata = jsonOrEmpty(metadata)
	return workflow, nil
}

func scanWorkflowVersion(scanner workflowScanner) (WorkflowVersion, error) {
	var version WorkflowVersion
	var manifestJSON, entrypointJSON, runtimeJSON, inputsJSON, outputsJSON, executionJSON, artifactPolicyJSON, usageDocumentsJSON, metadata []byte
	var activatedAt sql.NullTime
	if err := scanner.Scan(
		&version.WorkflowVersionID,
		&version.WorkflowID,
		&version.VersionLabel,
		&manifestJSON,
		&version.ManifestHash,
		&version.ContentHash,
		&version.PackageRoot,
		&entrypointJSON,
		&runtimeJSON,
		&inputsJSON,
		&outputsJSON,
		&executionJSON,
		&artifactPolicyJSON,
		&usageDocumentsJSON,
		&version.Status,
		&version.CreatedByActorID,
		&version.CreatedAt,
		&activatedAt,
		&metadata,
	); err != nil {
		return WorkflowVersion{}, err
	}
	version.ManifestJSON = jsonOrEmpty(manifestJSON)
	version.EntrypointJSON = jsonOrEmpty(entrypointJSON)
	version.RuntimeJSON = jsonOrEmpty(runtimeJSON)
	version.InputSchemaJSON = jsonOrEmpty(inputsJSON)
	version.OutputSchemaJSON = jsonOrEmpty(outputsJSON)
	version.ExecutionJSON = jsonOrEmpty(executionJSON)
	version.ArtifactPolicyJSON = jsonOrEmpty(artifactPolicyJSON)
	version.UsageDocumentsJSON = jsonOrEmptyArray(usageDocumentsJSON)
	version.ActivatedAt = timePtr(activatedAt)
	version.Metadata = jsonOrEmpty(metadata)
	return version, nil
}

func (v WorkflowVersion) Manifest() (Manifest, error) {
	var manifest Manifest
	if err := json.Unmarshal(v.ManifestJSON, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse stored workflow manifest: %w", err)
	}
	manifest = NormalizeManifest(manifest)
	if err := ValidateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func normalizeJSONObject(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return []byte(`{}`), nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("expected object")
	}
	return raw, nil
}

func marshalObjectJSON(value map[string]any) ([]byte, error) {
	if value == nil {
		return []byte(`{}`), nil
	}
	return marshalJSON(value)
}

func marshalJSON(value any) ([]byte, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if string(payload) == "null" {
		return []byte(`{}`), nil
	}
	return payload, nil
}

func activatedAt(status string) any {
	if status == VersionStatusActive {
		return time.Now().UTC()
	}
	return nil
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
