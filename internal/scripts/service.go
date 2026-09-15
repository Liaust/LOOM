package scripts

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
)

const (
	ScriptStatusRegistered = "registered"
	ScriptStatusActive     = "active"

	VersionStatusActive        = "active"
	VersionStatusPendingReview = "pending_review"
)

type Script struct {
	ScriptID         string          `json:"script_id"`
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

type ScriptVersion struct {
	ScriptVersionID    string          `json:"script_version_id"`
	ScriptID           string          `json:"script_id"`
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

type ScriptDetail struct {
	Script        Script          `json:"script"`
	ActiveVersion *ScriptVersion  `json:"active_version,omitempty"`
	Versions      []ScriptVersion `json:"versions,omitempty"`
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
	Script         Script        `json:"script"`
	Version        ScriptVersion `json:"version"`
	ScriptCreated  bool          `json:"script_created"`
	VersionCreated bool          `json:"version_created"`
	Activated      bool          `json:"activated"`
	EventIDs       []string      `json:"event_ids,omitempty"`
}

type ListFilter struct {
	Limit      int
	Status     string
	ProjectRef string
	ScopeRef   string
}

type Service struct {
	DB *sql.DB
}

func NewService(db *sql.DB) Service {
	return Service{DB: db}
}

func (s Service) RegisterScript(ctx context.Context, req requestctx.Context, input RegisterInput) (RegisterResult, error) {
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
		slug = manifest.ID
	}
	if !scriptIDPattern.MatchString(slug) {
		return RegisterResult{}, fmt.Errorf("script slug override must match %s", scriptIDPattern.String())
	}
	packageRoot := filepath.Dir(manifestPath)
	if packageRoot, err = filepath.Abs(packageRoot); err != nil {
		return RegisterResult{}, fmt.Errorf("resolve script package root: %w", err)
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
		return RegisterResult{}, fmt.Errorf("script metadata must be a JSON object: %w", err)
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

	script, scriptCreated, err := s.ensureScriptTx(ctx, tx, req, manifest, slug, ownerScopeID, metadata)
	if err != nil {
		return RegisterResult{}, err
	}

	version, versionCreated, err := s.ensureScriptVersionTx(ctx, tx, req, script, manifest, manifestJSON, manifestHash, contentHash, packageRoot)
	if err != nil {
		return RegisterResult{}, err
	}

	activated := false
	if version.Status == VersionStatusActive || script.ActiveVersionID == nil || input.Activate {
		version, err = activateVersionTx(ctx, tx, script.ScriptID, version.ScriptVersionID)
		if err != nil {
			return RegisterResult{}, err
		}
		script, err = getScriptTx(ctx, tx, script.ScriptID)
		if err != nil {
			return RegisterResult{}, err
		}
		activated = true
	}

	eventIDs := []string{}
	if scriptCreated {
		event, err := events.AppendTx(ctx, tx, events.AppendInput{
			EventType:  events.TypeScriptRegistered,
			EventLevel: "audit",
			Request:    req,
			ScopeID:    stringValue(script.OwnerScopeID),
			TargetKind: "script",
			TargetID:   script.ScriptID,
			Status:     script.Status,
			Result:     "ok",
			Payload: map[string]any{
				"script_id": script.ScriptID,
				"slug":      script.Slug,
				"name":      script.Name,
			},
			VisibilityClass: "internal",
		})
		if err != nil {
			return RegisterResult{}, err
		}
		eventIDs = append(eventIDs, event.EventID)
	}
	if versionCreated {
		event, err := events.AppendTx(ctx, tx, events.AppendInput{
			EventType:  events.TypeScriptVersionCreated,
			EventLevel: "audit",
			Request:    req,
			ScopeID:    stringValue(script.OwnerScopeID),
			TargetKind: "script_version",
			TargetID:   version.ScriptVersionID,
			Status:     version.Status,
			Result:     "ok",
			Payload: map[string]any{
				"script_id":         script.ScriptID,
				"script_version_id": version.ScriptVersionID,
				"version_label":     version.VersionLabel,
				"manifest_hash":     version.ManifestHash,
				"content_hash":      version.ContentHash,
				"activated":         activated,
			},
			VisibilityClass: "internal",
		})
		if err != nil {
			return RegisterResult{}, err
		}
		eventIDs = append(eventIDs, event.EventID)
	}

	if err := tx.Commit(); err != nil {
		return RegisterResult{}, err
	}

	return RegisterResult{
		Script:         script,
		Version:        version,
		ScriptCreated:  scriptCreated,
		VersionCreated: versionCreated,
		Activated:      activated,
		EventIDs:       eventIDs,
	}, nil
}

func (s Service) ListScripts(ctx context.Context, filter ListFilter) ([]Script, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}

	query := scriptSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	if strings.TrimSpace(filter.Status) != "" {
		add("s.status =", strings.TrimSpace(filter.Status))
	}
	if strings.TrimSpace(filter.ProjectRef) != "" || strings.TrimSpace(filter.ScopeRef) != "" {
		scopeID, err := s.resolveOptionalScope(ctx, filter.ProjectRef, filter.ScopeRef)
		if err != nil {
			return nil, err
		}
		add("s.owner_scope_id =", scopeID)
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY s.updated_at DESC, s.slug LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var scripts []Script
	for rows.Next() {
		script, err := scanScript(rows)
		if err != nil {
			return nil, err
		}
		scripts = append(scripts, script)
	}
	return scripts, rows.Err()
}

func (s Service) GetScript(ctx context.Context, ref string) (ScriptDetail, error) {
	script, err := s.resolveScript(ctx, ref)
	if err != nil {
		return ScriptDetail{}, err
	}
	detail := ScriptDetail{Script: script}
	if script.ActiveVersionID != nil {
		version, err := s.GetScriptVersion(ctx, *script.ActiveVersionID)
		if err != nil {
			return ScriptDetail{}, err
		}
		detail.ActiveVersion = &version
	}
	versions, err := s.listVersions(ctx, script.ScriptID)
	if err != nil {
		return ScriptDetail{}, err
	}
	detail.Versions = versions
	return detail, nil
}

func (s Service) ResolveActiveVersion(ctx context.Context, ref string) (ScriptVersion, error) {
	script, err := s.resolveScript(ctx, ref)
	if err != nil {
		return ScriptVersion{}, err
	}
	if script.ActiveVersionID == nil || strings.TrimSpace(*script.ActiveVersionID) == "" {
		return ScriptVersion{}, fmt.Errorf("script has no active version: %s", ref)
	}
	return s.GetScriptVersion(ctx, *script.ActiveVersionID)
}

func (s Service) GetScriptVersion(ctx context.Context, ref string) (ScriptVersion, error) {
	row := s.DB.QueryRowContext(ctx, scriptVersionSelectSQL()+`
		WHERE sv.script_version_id = $1
		LIMIT 1
	`, strings.TrimSpace(ref))
	return scanScriptVersion(row)
}

func (s Service) resolveScript(ctx context.Context, ref string) (Script, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Script{}, fmt.Errorf("script ref is required")
	}
	row := s.DB.QueryRowContext(ctx, scriptSelectSQL()+`
		WHERE s.script_id = $1 OR s.slug = $1
		LIMIT 1
	`, ref)
	return scanScript(row)
}

func (s Service) listVersions(ctx context.Context, scriptID string) ([]ScriptVersion, error) {
	rows, err := s.DB.QueryContext(ctx, scriptVersionSelectSQL()+`
		WHERE sv.script_id = $1
		ORDER BY sv.created_at DESC
	`, scriptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var versions []ScriptVersion
	for rows.Next() {
		version, err := scanScriptVersion(rows)
		if err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	return versions, rows.Err()
}

func (s Service) ensureScriptTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, manifest Manifest, slug, ownerScopeID string, metadata []byte) (Script, bool, error) {
	script, err := getScriptTx(ctx, tx, slug)
	if err == nil {
		return script, false, nil
	}
	if err != sql.ErrNoRows {
		return Script{}, false, err
	}

	row := tx.QueryRowContext(ctx, `
		INSERT INTO packages.scripts (
			script_id, slug, name, description, owner_scope_id, created_by_actor_id,
			status, metadata
		)
		VALUES ($1, $2, $3, $4, nullif($5, ''), $6, $7, $8)
		RETURNING `+scriptReturningColumns()+`
	`, ids.NewScriptID(), slug, manifest.Name, strings.TrimSpace(manifest.Description), ownerScopeID, req.ActorID, ScriptStatusRegistered, metadata)
	script, err = scanScript(row)
	return script, true, err
}

func (s Service) ensureScriptVersionTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, script Script, manifest Manifest, manifestJSON []byte, manifestHash, contentHash, packageRoot string) (ScriptVersion, bool, error) {
	row := tx.QueryRowContext(ctx, scriptVersionSelectSQL()+`
		WHERE sv.script_id = $1 AND sv.version_label = $2 AND sv.content_hash = $3
		LIMIT 1
	`, script.ScriptID, manifest.Version, contentHash)
	version, err := scanScriptVersion(row)
	if err == nil {
		return version, false, nil
	}
	if err != sql.ErrNoRows {
		return ScriptVersion{}, false, err
	}

	versionStatus := VersionStatusPendingReview
	if script.ActiveVersionID == nil {
		versionStatus = VersionStatusActive
	}

	entrypointJSON, err := marshalJSON(manifest.Entrypoint)
	if err != nil {
		return ScriptVersion{}, false, err
	}
	runtimeJSON, err := marshalObjectJSON(manifest.Runtime)
	if err != nil {
		return ScriptVersion{}, false, err
	}
	inputsJSON, err := marshalObjectJSON(manifest.Inputs)
	if err != nil {
		return ScriptVersion{}, false, err
	}
	outputsJSON, err := marshalObjectJSON(manifest.Outputs)
	if err != nil {
		return ScriptVersion{}, false, err
	}
	executionJSON, err := marshalJSON(manifest.Execution)
	if err != nil {
		return ScriptVersion{}, false, err
	}
	artifactPolicyJSON, err := marshalJSON(manifest.Artifacts)
	if err != nil {
		return ScriptVersion{}, false, err
	}
	usageDocumentsJSON, err := marshalJSON(manifest.UsageDocuments)
	if err != nil {
		return ScriptVersion{}, false, err
	}

	row = tx.QueryRowContext(ctx, `
		INSERT INTO packages.script_versions (
			script_version_id, script_id, version_label, manifest_json, manifest_hash,
			content_hash, package_root, entrypoint_json, runtime_json, input_schema_json,
			output_schema_json, execution_json, artifact_policy_json, usage_documents_json,
			status, created_by_actor_id, activated_at, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14,
		        $15, $16, $17, '{"slice":"5","part":"2"}'::jsonb)
		RETURNING `+scriptVersionReturningColumns()+`
	`, ids.NewScriptVersionID(), script.ScriptID, manifest.Version, manifestJSON, manifestHash, contentHash,
		packageRoot, entrypointJSON, runtimeJSON, inputsJSON, outputsJSON, executionJSON,
		artifactPolicyJSON, usageDocumentsJSON, versionStatus, req.ActorID, activatedAt(versionStatus))
	version, err = scanScriptVersion(row)
	return version, true, err
}

func getScriptTx(ctx context.Context, tx *sql.Tx, ref string) (Script, error) {
	row := tx.QueryRowContext(ctx, scriptSelectSQL()+`
		WHERE s.script_id = $1 OR s.slug = $1
		LIMIT 1
	`, strings.TrimSpace(ref))
	return scanScript(row)
}

func activateVersionTx(ctx context.Context, tx *sql.Tx, scriptID, versionID string) (ScriptVersion, error) {
	if _, err := tx.ExecContext(ctx, `
		UPDATE packages.script_versions
		SET status = CASE WHEN script_version_id = $2 THEN 'active' ELSE status END,
		    activated_at = CASE WHEN script_version_id = $2 THEN COALESCE(activated_at, now()) ELSE activated_at END
		WHERE script_id = $1
	`, scriptID, versionID); err != nil {
		return ScriptVersion{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE packages.scripts
		SET active_version_id = $2,
		    status = 'active',
		    updated_at = now()
		WHERE script_id = $1
	`, scriptID, versionID); err != nil {
		return ScriptVersion{}, err
	}
	row := tx.QueryRowContext(ctx, scriptVersionSelectSQL()+`
		WHERE sv.script_version_id = $1
	`, versionID)
	return scanScriptVersion(row)
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

func scriptSelectSQL() string {
	return `SELECT ` + scriptColumns() + ` FROM packages.scripts s`
}

func scriptColumns() string {
	return `
		s.script_id, s.slug, s.name, s.description, s.owner_scope_id,
		s.created_by_actor_id, s.active_version_id, s.status, s.created_at,
		s.updated_at, s.metadata`
}

func scriptReturningColumns() string {
	return `
		script_id, slug, name, description, owner_scope_id, created_by_actor_id,
		active_version_id, status, created_at, updated_at, metadata`
}

func scriptVersionSelectSQL() string {
	return `SELECT ` + scriptVersionColumns() + ` FROM packages.script_versions sv`
}

func scriptVersionColumns() string {
	return `
		sv.script_version_id, sv.script_id, sv.version_label, sv.manifest_json,
		sv.manifest_hash, sv.content_hash, sv.package_root, sv.entrypoint_json,
		sv.runtime_json, sv.input_schema_json, sv.output_schema_json,
		sv.execution_json, sv.artifact_policy_json, sv.usage_documents_json,
		sv.status, sv.created_by_actor_id, sv.created_at, sv.activated_at,
		sv.metadata`
}

func scriptVersionReturningColumns() string {
	return `
		script_version_id, script_id, version_label, manifest_json, manifest_hash,
		content_hash, package_root, entrypoint_json, runtime_json,
		input_schema_json, output_schema_json, execution_json,
		artifact_policy_json, usage_documents_json, status, created_by_actor_id,
		created_at, activated_at, metadata`
}

type scriptScanner interface {
	Scan(dest ...any) error
}

func scanScript(scanner scriptScanner) (Script, error) {
	var script Script
	var ownerScopeID sql.NullString
	var activeVersionID sql.NullString
	var metadata []byte
	if err := scanner.Scan(
		&script.ScriptID,
		&script.Slug,
		&script.Name,
		&script.Description,
		&ownerScopeID,
		&script.CreatedByActorID,
		&activeVersionID,
		&script.Status,
		&script.CreatedAt,
		&script.UpdatedAt,
		&metadata,
	); err != nil {
		return Script{}, err
	}
	script.OwnerScopeID = stringPtr(ownerScopeID)
	script.ActiveVersionID = stringPtr(activeVersionID)
	script.Metadata = jsonOrEmpty(metadata)
	return script, nil
}

func scanScriptVersion(scanner scriptScanner) (ScriptVersion, error) {
	var version ScriptVersion
	var manifestJSON, entrypointJSON, runtimeJSON, inputsJSON, outputsJSON, executionJSON, artifactPolicyJSON, usageDocumentsJSON, metadata []byte
	var activatedAt sql.NullTime
	if err := scanner.Scan(
		&version.ScriptVersionID,
		&version.ScriptID,
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
		return ScriptVersion{}, err
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

func (v ScriptVersion) Manifest() (Manifest, error) {
	var manifest Manifest
	if err := json.Unmarshal(v.ManifestJSON, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse stored script manifest: %w", err)
	}
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
