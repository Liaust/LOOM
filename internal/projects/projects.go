package projects

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/requestctx"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$`)

type Project struct {
	ProjectID              string          `json:"project_id"`
	ProjectScopeID         string          `json:"project_scope_id"`
	ProjectScopeKey        string          `json:"project_scope_key"`
	Slug                   string          `json:"slug"`
	Name                   string          `json:"name"`
	Description            string          `json:"description"`
	OwnerActorID           string          `json:"owner_actor_id"`
	CreatedByActorID       string          `json:"created_by_actor_id"`
	HomeNodeID             *string         `json:"home_node_id,omitempty"`
	DefaultWorkspaceViewID *string         `json:"default_workspace_view_id,omitempty"`
	DefaultPolicyRef       *string         `json:"default_policy_ref,omitempty"`
	Status                 string          `json:"status"`
	Priority               string          `json:"priority"`
	ProjectType            string          `json:"project_type"`
	IndexingPolicy         json.RawMessage `json:"indexing_policy"`
	BackupPolicy           json.RawMessage `json:"backup_policy"`
	SharingPolicy          json.RawMessage `json:"sharing_policy"`
	FreshnessPolicy        json.RawMessage `json:"freshness_policy"`
	ArchiveState           json.RawMessage `json:"archive_state"`
	Metadata               json.RawMessage `json:"metadata"`
	CreatedAt              time.Time       `json:"created_at"`
	UpdatedAt              time.Time       `json:"updated_at"`
}

type ProjectMembership struct {
	ProjectMembershipID string          `json:"project_membership_id"`
	ProjectID           string          `json:"project_id"`
	ScopeID             string          `json:"scope_id"`
	ActorID             string          `json:"actor_id"`
	Role                string          `json:"role"`
	Status              string          `json:"status"`
	AuthorizationHint   *int            `json:"authorization_hint,omitempty"`
	AssignedByActorID   *string         `json:"assigned_by_actor_id,omitempty"`
	AssignedAt          time.Time       `json:"assigned_at"`
	ExpiresAt           *time.Time      `json:"expires_at,omitempty"`
	Metadata            json.RawMessage `json:"metadata"`
}

type ProjectPolicyProfile struct {
	ProjectPolicyProfileID string          `json:"project_policy_profile_id"`
	ProjectID              string          `json:"project_id"`
	Visibility             string          `json:"visibility"`
	CreatedByActorID       string          `json:"created_by_actor_id"`
	CreatedAt              time.Time       `json:"created_at"`
	UpdatedAt              time.Time       `json:"updated_at"`
	Metadata               json.RawMessage `json:"metadata"`
}

type WorkspaceView struct {
	WorkspaceViewID  string          `json:"workspace_view_id"`
	ProjectID        string          `json:"project_id"`
	ScopeID          string          `json:"scope_id"`
	ViewKind         string          `json:"view_kind"`
	NodeID           *string         `json:"node_id,omitempty"`
	RootPath         *string         `json:"root_path,omitempty"`
	VirtualPath      *string         `json:"virtual_path,omitempty"`
	Status           string          `json:"status"`
	Materialized     bool            `json:"materialized"`
	CreatedByActorID string          `json:"created_by_actor_id"`
	CreatedAt        time.Time       `json:"created_at"`
	Metadata         json.RawMessage `json:"metadata"`
}

type ProjectDetail struct {
	Project         Project               `json:"project"`
	OwnerMembership *ProjectMembership    `json:"owner_membership,omitempty"`
	PolicyProfile   *ProjectPolicyProfile `json:"policy_profile,omitempty"`
	WorkspaceView   *WorkspaceView        `json:"workspace_view,omitempty"`
}

type CreateInput struct {
	Name        string          `json:"name"`
	Slug        string          `json:"slug"`
	Description string          `json:"description"`
	HomeNodeRef string          `json:"home_node_ref"`
	IfNotExists bool            `json:"if_not_exists"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
}

type createProjectTxOptions struct {
	ProjectID   string
	EventSource string
}

type CreateResult struct {
	Project  ProjectDetail `json:"project"`
	Created  bool          `json:"created"`
	EventIDs []string      `json:"event_ids,omitempty"`
}

type Service struct {
	DB *sql.DB
}

func NewService(db *sql.DB) Service {
	return Service{DB: db}
}

func (s Service) ListProjects(ctx context.Context, limit int) ([]Project, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	rows, err := s.DB.QueryContext(ctx, projectSelectSQL()+` ORDER BY p.updated_at DESC, p.slug LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		project, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	return projects, rows.Err()
}

func (s Service) GetProject(ctx context.Context, ref string) (ProjectDetail, error) {
	project, err := s.ResolveProjectRef(ctx, ref)
	if err != nil {
		return ProjectDetail{}, err
	}

	detail := ProjectDetail{Project: project}
	if membership, err := s.getOwnerMembership(ctx, project.ProjectID); err == nil {
		detail.OwnerMembership = &membership
	} else if err != sql.ErrNoRows {
		return ProjectDetail{}, err
	}
	if policy, err := s.getPolicyProfile(ctx, project.ProjectID); err == nil {
		detail.PolicyProfile = &policy
	} else if err != sql.ErrNoRows {
		return ProjectDetail{}, err
	}
	if view, err := s.getDefaultWorkspaceView(ctx, project); err == nil {
		detail.WorkspaceView = &view
	} else if err != sql.ErrNoRows {
		return ProjectDetail{}, err
	}
	return detail, nil
}

func (s Service) ResolveProjectRef(ctx context.Context, ref string) (Project, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Project{}, fmt.Errorf("project ref is required")
	}

	row := s.DB.QueryRowContext(ctx, projectSelectSQL()+`
		WHERE p.project_id = $1
		   OR p.slug = $1
		   OR scope.scope_key = $1
		   OR scope.slug = $1
		ORDER BY
			CASE
				WHEN p.project_id = $1 THEN 0
				WHEN p.slug = $1 THEN 1
				WHEN scope.scope_key = $1 THEN 2
				ELSE 3
			END
		LIMIT 1
	`, ref)
	return scanProject(row)
}

func (s Service) UpdateProjectArchiveState(ctx context.Context, req requestctx.Context, projectID string, archiveState json.RawMessage) (Project, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return Project{}, fmt.Errorf("project_id is required")
	}
	state, err := normalizeJSON(archiveState)
	if err != nil {
		return Project{}, fmt.Errorf("project archive_state must be valid JSON object: %w", err)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback()

	row := tx.QueryRowContext(ctx, projectSelectSQL()+`
		WHERE p.project_id = $1
		FOR UPDATE OF p
	`, projectID)
	project, err := scanProject(row)
	if err != nil {
		return Project{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE projects.projects
		SET archive_state = $2,
		    status = 'archived',
		    updated_at = now()
		WHERE project_id = $1
	`, projectID, state); err != nil {
		return Project{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE projects.project_repository_memberships
		SET lifecycle_status = 'archived',
		    updated_at = now()
		WHERE project_id = $1
	`, projectID); err != nil {
		return Project{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE projects.repositories
		SET lifecycle_status = 'archived',
		    updated_at = now()
		WHERE owning_project_id = $1
	`, projectID); err != nil {
		return Project{}, err
	}
	row = tx.QueryRowContext(ctx, projectSelectSQL()+` WHERE p.project_id = $1`, projectID)
	updated, err := scanProject(row)
	if err != nil {
		return Project{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE scopes.scopes
		SET status = 'archived',
		    updated_at = now()
		WHERE scope_id = $1
	`, project.ProjectScopeID); err != nil {
		return Project{}, err
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  events.TypeProjectArchived,
		EventLevel: "audit",
		Request:    req,
		ScopeID:    updated.ProjectScopeID,
		TargetKind: "project",
		TargetID:   updated.ProjectID,
		Status:     "archived",
		Result:     "ok",
		Payload: map[string]any{
			"project_id":    updated.ProjectID,
			"project_slug":  updated.Slug,
			"archive_state": json.RawMessage(state),
		},
	}); err != nil {
		return Project{}, err
	}
	if err := tx.Commit(); err != nil {
		return Project{}, err
	}
	return updated, nil
}

func (s Service) CreateProject(ctx context.Context, req requestctx.Context, input CreateInput) (CreateResult, error) {
	input, err := normalizeCreateInput(input)
	if err != nil {
		return CreateResult{}, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return CreateResult{}, err
	}
	defer tx.Rollback()

	created, err := createProjectTx(ctx, tx, req, input, createProjectTxOptions{EventSource: "project.create"})
	if err != nil {
		return CreateResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CreateResult{}, err
	}

	detail, err := s.GetProject(ctx, created.Project.Project.ProjectID)
	if err != nil {
		return CreateResult{}, err
	}
	created.Project = detail
	return created, nil
}

func normalizeCreateInput(input CreateInput) (CreateInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return input, fmt.Errorf("project name is required")
	}
	input.Slug = strings.TrimSpace(input.Slug)
	if input.Slug == "" {
		input.Slug = slugify(input.Name)
	}
	if !slugPattern.MatchString(input.Slug) {
		return input, fmt.Errorf("project slug must be lowercase URL-safe and 3-64 characters")
	}
	input.Description = strings.TrimSpace(input.Description)
	input.HomeNodeRef = strings.TrimSpace(input.HomeNodeRef)
	metadata, err := normalizeJSON(input.Metadata)
	if err != nil {
		return input, fmt.Errorf("project metadata must be valid JSON object: %w", err)
	}
	input.Metadata = metadata
	return input, nil
}

func createProjectTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, input CreateInput, options createProjectTxOptions) (CreateResult, error) {
	projectID := strings.TrimSpace(options.ProjectID)
	if projectID != "" {
		if err := ids.Validate(ids.ProjectPrefix, projectID); err != nil {
			return CreateResult{}, fmt.Errorf("project id is invalid: %w", err)
		}
	}
	existing, err := getProjectByIDOrSlugTx(ctx, tx, projectID, input.Slug, true)
	if err == nil {
		if projectID != "" && existing.ProjectID != projectID {
			return CreateResult{}, fmt.Errorf("project slug %q belongs to project %s, not %s", input.Slug, existing.ProjectID, projectID)
		}
		if existing.Slug != input.Slug {
			return CreateResult{}, fmt.Errorf("project id %s belongs to slug %q, not %q", existing.ProjectID, existing.Slug, input.Slug)
		}
		if input.IfNotExists {
			return CreateResult{Project: ProjectDetail{Project: existing}}, nil
		}
		return CreateResult{}, fmt.Errorf("project already exists: %s", input.Slug)
	}
	if err != sql.ErrNoRows {
		return CreateResult{}, err
	}

	homeNodeID := req.OriginNodeID
	if input.HomeNodeRef != "" {
		homeNodeID, err = resolveNodeRefTx(ctx, tx, input.HomeNodeRef)
		if err != nil {
			return CreateResult{}, fmt.Errorf("resolve home node: %w", err)
		}
	}
	scope, scopeCreated, err := ensureProjectScopeTx(ctx, tx, req, input.Slug, input.Name, homeNodeID)
	if err != nil {
		return CreateResult{}, err
	}
	if projectID == "" {
		projectID = ids.NewProjectID()
	}
	policyID := ids.NewProjectPolicyID()
	workspaceViewID := ids.NewWorkspaceViewID()
	membershipID := ids.NewProjectMembershipID()
	project, err := insertProjectTx(ctx, tx, projectID, scope.ScopeID, input.Slug, input.Name, input.Description, req.ActorID, req.ActorID, homeNodeID, input.Metadata)
	if err != nil {
		return CreateResult{}, err
	}
	if err := insertProjectPolicyTx(ctx, tx, policyID, projectID, req.ActorID); err != nil {
		return CreateResult{}, err
	}
	if err := insertProjectMembershipTx(ctx, tx, membershipID, projectID, scope.ScopeID, req.ActorID, req.ActorID); err != nil {
		return CreateResult{}, err
	}
	if err := insertWorkspaceViewTx(ctx, tx, workspaceViewID, projectID, scope.ScopeID, homeNodeID, "project:"+input.Slug, req.ActorID); err != nil {
		return CreateResult{}, err
	}
	if err := updateProjectDefaultsTx(ctx, tx, projectID, policyID, workspaceViewID); err != nil {
		return CreateResult{}, err
	}
	project.DefaultPolicyRef = &policyID
	project.DefaultWorkspaceViewID = &workspaceViewID

	eventSource := strings.TrimSpace(options.EventSource)
	if eventSource == "" {
		eventSource = "project.create"
	}
	eventIDs := []string{}
	if scopeCreated {
		event, err := events.AppendTx(ctx, tx, events.AppendInput{
			EventType: events.TypeScopeCreated, EventLevel: "audit", Request: req,
			ScopeID: scope.ScopeID, TargetKind: "scope", TargetID: scope.ScopeID,
			Status: "created", Result: "ok", VisibilityClass: "internal",
			Payload: map[string]any{"scope_type": scope.ScopeType, "scope_key": scope.ScopeKey, "slug": scope.Slug, "source": eventSource},
		})
		if err != nil {
			return CreateResult{}, err
		}
		eventIDs = append(eventIDs, event.EventID)
	}
	for _, eventInput := range []events.AppendInput{
		{EventType: events.TypeProjectCreated, EventLevel: "audit", Request: req, ScopeID: scope.ScopeID, TargetKind: "project", TargetID: projectID, Status: "created", Result: "ok", Payload: map[string]any{"project_id": projectID, "slug": input.Slug, "scope_id": scope.ScopeID, "scope_key": scope.ScopeKey}, VisibilityClass: "internal"},
		{EventType: events.TypeProjectPolicyCreated, EventLevel: "audit", Request: req, ScopeID: scope.ScopeID, TargetKind: "project_policy_profile", TargetID: policyID, Status: "created", Result: "ok", Payload: map[string]any{"project_id": projectID, "project_policy_profile_id": policyID, "visibility": "private"}, VisibilityClass: "internal"},
		{EventType: events.TypeProjectMemberAdded, EventLevel: "audit", Request: req, ScopeID: scope.ScopeID, TargetKind: "project_membership", TargetID: membershipID, Status: "created", Result: "ok", Payload: map[string]any{"project_id": projectID, "project_membership_id": membershipID, "actor_id": req.ActorID, "role": "owner"}, VisibilityClass: "internal"},
		{EventType: events.TypeProjectWorkspaceViewCreated, EventLevel: "audit", Request: req, ScopeID: scope.ScopeID, TargetKind: "workspace_view", TargetID: workspaceViewID, Status: "created", Result: "ok", Payload: map[string]any{"project_id": projectID, "workspace_view_id": workspaceViewID, "view_kind": "metadata_only"}, VisibilityClass: "internal"},
	} {
		event, err := events.AppendTx(ctx, tx, eventInput)
		if err != nil {
			return CreateResult{}, err
		}
		eventIDs = append(eventIDs, event.EventID)
	}
	return CreateResult{Project: ProjectDetail{Project: project}, Created: true, EventIDs: eventIDs}, nil
}

func getProjectByIDOrSlugTx(ctx context.Context, tx *sql.Tx, projectID, slug string, forUpdate bool) (Project, error) {
	lockClause := ""
	if forUpdate {
		lockClause = " FOR UPDATE OF p"
	}
	row := tx.QueryRowContext(ctx, projectSelectSQL()+`
		WHERE ($1 <> '' AND p.project_id = $1) OR p.slug = $2
		ORDER BY CASE WHEN p.project_id = $1 THEN 0 ELSE 1 END
		LIMIT 1
	`+lockClause, strings.TrimSpace(projectID), strings.TrimSpace(slug))
	return scanProject(row)
}

type projectScope struct {
	ScopeID     string
	ScopeType   string
	ScopeKey    string
	Slug        string
	DisplayName string
}

func ensureProjectScopeTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, slug, name, homeNodeID string) (projectScope, bool, error) {
	ref := "project:" + slug
	scope, err := getProjectScopeTx(ctx, tx, ref)
	if err == nil {
		return scope, false, nil
	}
	if err != sql.ErrNoRows {
		return projectScope{}, false, err
	}

	scopeID := ids.NewScopeID()
	metadata := []byte(`{"slice":"3","source":"project.create"}`)
	row := tx.QueryRowContext(ctx, `
		INSERT INTO scopes.scopes (
			scope_id, scope_type, scope_key, slug, display_name, owner_actor_id,
			home_node_id, status, created_by_actor_id, metadata
		)
		VALUES ($1, 'project', $2, $3, $4, $5, $6, 'active', $7, $8)
		RETURNING scope_id, scope_type, scope_key, slug, display_name
	`, scopeID, ref, slug, name, req.ActorID, homeNodeID, req.ActorID, metadata)
	if err := row.Scan(&scope.ScopeID, &scope.ScopeType, &scope.ScopeKey, &scope.Slug, &scope.DisplayName); err != nil {
		return projectScope{}, false, err
	}
	return scope, true, nil
}

func getProjectScopeTx(ctx context.Context, tx *sql.Tx, ref string) (projectScope, error) {
	var scope projectScope
	err := tx.QueryRowContext(ctx, `
		SELECT scope_id, scope_type, scope_key, slug, display_name
		FROM scopes.scopes
		WHERE scope_id = $1 OR scope_key = $1 OR slug = $1
		LIMIT 1
	`, ref).Scan(&scope.ScopeID, &scope.ScopeType, &scope.ScopeKey, &scope.Slug, &scope.DisplayName)
	return scope, err
}

func insertProjectTx(ctx context.Context, tx *sql.Tx, projectID, scopeID, slug, name, description, ownerActorID, createdByActorID, homeNodeID string, metadata []byte) (Project, error) {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO projects.projects (
			project_id, project_scope_id, slug, name, description,
			owner_actor_id, created_by_actor_id, home_node_id, status, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, nullif($8, ''), 'active', $9)
	`, projectID, scopeID, slug, name, description, ownerActorID, createdByActorID, homeNodeID, metadata); err != nil {
		return Project{}, err
	}
	return getProjectTx(ctx, tx, projectID)
}

func getProjectTx(ctx context.Context, tx *sql.Tx, ref string) (Project, error) {
	row := tx.QueryRowContext(ctx, projectSelectSQL()+`
		WHERE p.project_id = $1 OR p.slug = $1
		LIMIT 1
	`, ref)
	return scanProject(row)
}

func insertProjectPolicyTx(ctx context.Context, tx *sql.Tx, policyID, projectID, actorID string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO projects.project_policy_profiles (
			project_policy_profile_id, project_id, visibility, created_by_actor_id, metadata
		)
		VALUES ($1, $2, 'private', $3, '{"slice":"3","default":true}'::jsonb)
	`, policyID, projectID, actorID)
	return err
}

func insertProjectMembershipTx(ctx context.Context, tx *sql.Tx, membershipID, projectID, scopeID, actorID, assignedByActorID string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO projects.project_memberships (
			project_membership_id, project_id, scope_id, actor_id, role, status,
			authorization_hint, assigned_by_actor_id, metadata
		)
		VALUES ($1, $2, $3, $4, 'owner', 'active', 5, $5, '{"slice":"3","default_owner":true}'::jsonb)
	`, membershipID, projectID, scopeID, actorID, assignedByActorID)
	return err
}

func insertWorkspaceViewTx(ctx context.Context, tx *sql.Tx, workspaceViewID, projectID, scopeID, nodeID, virtualPath, actorID string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO projects.workspace_views (
			workspace_view_id, project_id, scope_id, view_kind, node_id, virtual_path,
			status, materialized, created_by_actor_id, metadata
		)
		VALUES ($1, $2, $3, 'metadata_only', nullif($4, ''), $5, 'active', false, $6,
		        '{"slice":"3","default":true}'::jsonb)
	`, workspaceViewID, projectID, scopeID, nodeID, virtualPath, actorID)
	return err
}

func updateProjectDefaultsTx(ctx context.Context, tx *sql.Tx, projectID, policyID, workspaceViewID string) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE projects.projects
		SET default_policy_ref = $2,
		    default_workspace_view_id = $3,
		    updated_at = now()
		WHERE project_id = $1
	`, projectID, policyID, workspaceViewID)
	return err
}

func resolveNodeRefTx(ctx context.Context, tx *sql.Tx, ref string) (string, error) {
	var nodeID string
	err := tx.QueryRowContext(ctx, `
		SELECT node_id
		FROM nodes.nodes
		WHERE node_id = $1 OR node_key = $1
	`, strings.TrimSpace(ref)).Scan(&nodeID)
	if err != nil {
		return "", err
	}
	return nodeID, nil
}

func (s Service) getOwnerMembership(ctx context.Context, projectID string) (ProjectMembership, error) {
	var membership ProjectMembership
	var authorizationHint sql.NullInt64
	var assignedByActorID sql.NullString
	var expiresAt sql.NullTime
	var metadata []byte
	err := s.DB.QueryRowContext(ctx, `
		SELECT project_membership_id, project_id, scope_id, actor_id, role, status,
		       authorization_hint, assigned_by_actor_id, assigned_at, expires_at, metadata
		FROM projects.project_memberships
		WHERE project_id = $1 AND role = 'owner'
		ORDER BY assigned_at ASC
		LIMIT 1
	`, projectID).Scan(
		&membership.ProjectMembershipID,
		&membership.ProjectID,
		&membership.ScopeID,
		&membership.ActorID,
		&membership.Role,
		&membership.Status,
		&authorizationHint,
		&assignedByActorID,
		&membership.AssignedAt,
		&expiresAt,
		&metadata,
	)
	if err != nil {
		return ProjectMembership{}, err
	}
	if authorizationHint.Valid {
		value := int(authorizationHint.Int64)
		membership.AuthorizationHint = &value
	}
	membership.AssignedByActorID = stringPtr(assignedByActorID)
	membership.ExpiresAt = timePtr(expiresAt)
	membership.Metadata = jsonOrEmpty(metadata)
	return membership, nil
}

func (s Service) getPolicyProfile(ctx context.Context, projectID string) (ProjectPolicyProfile, error) {
	var policy ProjectPolicyProfile
	var metadata []byte
	err := s.DB.QueryRowContext(ctx, `
		SELECT project_policy_profile_id, project_id, visibility, created_by_actor_id,
		       created_at, updated_at, metadata
		FROM projects.project_policy_profiles
		WHERE project_id = $1
	`, projectID).Scan(
		&policy.ProjectPolicyProfileID,
		&policy.ProjectID,
		&policy.Visibility,
		&policy.CreatedByActorID,
		&policy.CreatedAt,
		&policy.UpdatedAt,
		&metadata,
	)
	if err != nil {
		return ProjectPolicyProfile{}, err
	}
	policy.Metadata = jsonOrEmpty(metadata)
	return policy, nil
}

func (s Service) getDefaultWorkspaceView(ctx context.Context, project Project) (WorkspaceView, error) {
	if project.DefaultWorkspaceViewID == nil {
		return WorkspaceView{}, sql.ErrNoRows
	}
	var view WorkspaceView
	var nodeID sql.NullString
	var rootPath sql.NullString
	var virtualPath sql.NullString
	var metadata []byte
	err := s.DB.QueryRowContext(ctx, `
		SELECT workspace_view_id, project_id, scope_id, view_kind, node_id, root_path,
		       virtual_path, status, materialized, created_by_actor_id, created_at, metadata
		FROM projects.workspace_views
		WHERE workspace_view_id = $1
	`, *project.DefaultWorkspaceViewID).Scan(
		&view.WorkspaceViewID,
		&view.ProjectID,
		&view.ScopeID,
		&view.ViewKind,
		&nodeID,
		&rootPath,
		&virtualPath,
		&view.Status,
		&view.Materialized,
		&view.CreatedByActorID,
		&view.CreatedAt,
		&metadata,
	)
	if err != nil {
		return WorkspaceView{}, err
	}
	view.NodeID = stringPtr(nodeID)
	view.RootPath = stringPtr(rootPath)
	view.VirtualPath = stringPtr(virtualPath)
	view.Metadata = jsonOrEmpty(metadata)
	return view, nil
}

func projectSelectSQL() string {
	return `
		SELECT p.project_id, p.project_scope_id, scope.scope_key, p.slug, p.name,
		       p.description, p.owner_actor_id, p.created_by_actor_id, p.home_node_id,
		       p.default_workspace_view_id, p.default_policy_ref, p.status, p.priority,
		       p.project_type, p.indexing_policy, p.backup_policy, p.sharing_policy,
		       p.freshness_policy, p.archive_state, p.metadata, p.created_at, p.updated_at
		FROM projects.projects p
		JOIN scopes.scopes scope ON scope.scope_id = p.project_scope_id
	`
}

type projectScanner interface {
	Scan(dest ...any) error
}

func scanProject(scanner projectScanner) (Project, error) {
	var project Project
	var homeNodeID sql.NullString
	var defaultWorkspaceViewID sql.NullString
	var defaultPolicyRef sql.NullString
	var indexingPolicy []byte
	var backupPolicy []byte
	var sharingPolicy []byte
	var freshnessPolicy []byte
	var archiveState []byte
	var metadata []byte
	if err := scanner.Scan(
		&project.ProjectID,
		&project.ProjectScopeID,
		&project.ProjectScopeKey,
		&project.Slug,
		&project.Name,
		&project.Description,
		&project.OwnerActorID,
		&project.CreatedByActorID,
		&homeNodeID,
		&defaultWorkspaceViewID,
		&defaultPolicyRef,
		&project.Status,
		&project.Priority,
		&project.ProjectType,
		&indexingPolicy,
		&backupPolicy,
		&sharingPolicy,
		&freshnessPolicy,
		&archiveState,
		&metadata,
		&project.CreatedAt,
		&project.UpdatedAt,
	); err != nil {
		return Project{}, err
	}
	project.HomeNodeID = stringPtr(homeNodeID)
	project.DefaultWorkspaceViewID = stringPtr(defaultWorkspaceViewID)
	project.DefaultPolicyRef = stringPtr(defaultPolicyRef)
	project.IndexingPolicy = jsonOrEmpty(indexingPolicy)
	project.BackupPolicy = jsonOrEmpty(backupPolicy)
	project.SharingPolicy = jsonOrEmpty(sharingPolicy)
	project.FreshnessPolicy = jsonOrEmpty(freshnessPolicy)
	project.ArchiveState = jsonOrEmpty(archiveState)
	project.Metadata = jsonOrEmpty(metadata)
	return project, nil
}

func slugify(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var out strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out.WriteRune(r)
			lastDash = false
		case unicode.IsSpace(r) || r == '-' || r == '_' || r == '/':
			if !lastDash {
				out.WriteByte('-')
				lastDash = true
			}
		}
	}
	slug := strings.Trim(out.String(), "-")
	if len(slug) > 64 {
		slug = strings.Trim(slug[:64], "-")
	}
	return slug
}

func normalizeJSON(raw json.RawMessage) ([]byte, error) {
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

func jsonOrEmpty(value []byte) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(value)
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
