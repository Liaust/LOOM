package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/projectstate"
	"loom.local/loom/internal/repostate"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/response"
)

const (
	projectRepositorySurfaceDefaultLimit = 50
	projectRepositorySurfaceMaxLimit     = 200
	projectRepositorySurfaceRefMaxBytes  = 128
)

var ErrProjectRepositoryReadForbidden = errors.New("project repository read forbidden")

type ProjectRepositoryStateObserver interface {
	ObserveProject(context.Context, string) (projectstate.ProjectProjection, error)
}

type ProjectRepositoryRequestResolver func(context.Context, string) (requestctx.Context, error)

type ProjectRepositoryReadAuthorizer interface {
	AuthorizeProjectRepositoryRead(context.Context, requestctx.Context, string) (string, error)
}

type ProjectRepositoryServices struct {
	State           ProjectRepositoryStateObserver
	Provenance      projectstate.ProvenanceProjectObserver
	RepositoryState repostate.ProvenanceProjector
	Authorizer      ProjectRepositoryReadAuthorizer
	RequestResolver ProjectRepositoryRequestResolver
}

type SQLProjectRepositoryReadAuthorizer struct {
	DB           *sql.DB
	LocalNodeRef string
}

func (a SQLProjectRepositoryReadAuthorizer) AuthorizeProjectRepositoryRead(ctx context.Context, req requestctx.Context, projectRef string) (string, error) {
	projectRef = strings.TrimSpace(projectRef)
	localNodeRef := strings.TrimSpace(a.LocalNodeRef)
	if a.DB == nil || projectRef == "" || localNodeRef == "" || strings.TrimSpace(req.ActorID) == "" || strings.TrimSpace(req.OriginNodeID) == "" {
		return "", ErrProjectRepositoryReadForbidden
	}
	var selectedProjectID string
	err := a.DB.QueryRowContext(ctx, `
		WITH selected_project AS (
			SELECT p.project_id
			FROM projects.projects p
			JOIN scopes.scopes scope ON scope.scope_id = p.project_scope_id
			WHERE (p.project_id = $1 OR p.slug = $1 OR scope.scope_key = $1 OR scope.slug = $1)
			ORDER BY
				CASE
					WHEN p.project_id = $1 THEN 0
					WHEN p.slug = $1 THEN 1
					WHEN scope.scope_key = $1 THEN 2
					ELSE 3
				END
			LIMIT 1
		)
		SELECT selected.project_id
		FROM selected_project selected
		JOIN projects.project_memberships membership
		  ON membership.project_id = selected.project_id
		 AND membership.actor_id = $2
		 AND membership.status = 'active'
		 AND (membership.expires_at IS NULL OR membership.expires_at > now())
		JOIN identity.actors actor
		  ON actor.actor_id = membership.actor_id
		 AND actor.status = 'active'
		JOIN nodes.nodes origin
		  ON origin.node_id = $3
		 AND origin.status = 'active'
		JOIN nodes.nodes target
		  ON (target.node_id = $4 OR target.node_key = $4)
		 AND target.status = 'active'
		JOIN identity.actor_node_authorizations node_auth
		  ON node_auth.actor_id = actor.actor_id
		 AND node_auth.node_id = target.node_id
		 AND node_auth.status = 'active'
		 AND (node_auth.expires_at IS NULL OR node_auth.expires_at > now())
		WHERE origin.node_id = target.node_id
		LIMIT 1
	`, projectRef, req.ActorID, req.OriginNodeID, localNodeRef).Scan(&selectedProjectID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrProjectRepositoryReadForbidden
	}
	if err != nil {
		return "", fmt.Errorf("authorize project repository read: %w", err)
	}
	selectedProjectID = strings.TrimSpace(selectedProjectID)
	if selectedProjectID == "" {
		return "", ErrProjectRepositoryReadForbidden
	}
	return selectedProjectID, nil
}

type projectRepositorySurfaceProject struct {
	ProjectID string `json:"project_id"`
	Slug      string `json:"slug"`
	Lifecycle string `json:"lifecycle"`
}

type projectRepositorySurfaceSource struct {
	Posture                      projectstate.SourcePosture `json:"posture"`
	ProjectContractSchemaVersion string                     `json:"project_contract_schema_version,omitempty"`
	ReposContractSchemaVersion   string                     `json:"repos_contract_schema_version,omitempty"`
	SourceRevision               int64                      `json:"source_revision,omitempty"`
	RegisteredAt                 *time.Time                 `json:"registered_at,omitempty"`
}

type projectRepositorySurfacePage struct {
	Limit     int    `json:"limit"`
	After     string `json:"after,omitempty"`
	NextAfter string `json:"next_after,omitempty"`
	HasMore   bool   `json:"has_more"`
}

type projectRepositorySurfaceListItem struct {
	RepositoryID             string                                   `json:"repository_id"`
	RepositoryOwnerProjectID string                                   `json:"repository_owner_project_id"`
	Key                      string                                   `json:"key"`
	RelativePath             string                                   `json:"relative_path"`
	Role                     string                                   `json:"role"`
	StateRoot                string                                   `json:"state_root,omitempty"`
	MembershipLifecycle      string                                   `json:"membership_lifecycle"`
	RepositoryLifecycle      string                                   `json:"repository_lifecycle"`
	ObservationPosture       string                                   `json:"observation_posture"`
	ReasonCode               string                                   `json:"reason_code,omitempty"`
	ObservedAt               *time.Time                               `json:"observed_at,omitempty"`
	DevelopmentState         projectRepositorySurfaceDevelopmentState `json:"development_state"`
	Git                      *projectRepositorySurfaceGit             `json:"git,omitempty"`
}

type projectRepositorySurfaceDevelopmentState struct {
	Posture      projectstate.DevelopmentStatePosture `json:"posture"`
	RelativePath string                               `json:"relative_path,omitempty"`
	ReasonCode   string                               `json:"reason_code,omitempty"`
}

type projectRepositorySurfaceGit struct {
	Worktree                      bool                                 `json:"worktree"`
	WorktreeCanonicalProjectState bool                                 `json:"worktree_canonical_project_state"`
	Bare                          bool                                 `json:"bare"`
	RootMatchesMember             bool                                 `json:"root_matches_member"`
	CurrentBranch                 string                               `json:"current_branch,omitempty"`
	Detached                      bool                                 `json:"detached"`
	DefaultBranch                 string                               `json:"default_branch,omitempty"`
	DefaultBranchPosture          projectstate.GitDefaultBranchPosture `json:"default_branch_posture"`
	Head                          string                               `json:"head,omitempty"`
	HeadPosture                   projectstate.GitHeadPosture          `json:"head_posture"`
	Upstream                      string                               `json:"upstream,omitempty"`
	Ahead                         int                                  `json:"ahead"`
	Behind                        int                                  `json:"behind"`
	AheadBehindObserved           bool                                 `json:"ahead_behind_observed"`
	Dirty                         projectRepositorySurfaceGitDirty     `json:"dirty"`
}

type projectRepositorySurfaceGitDirty struct {
	Dirty            bool `json:"dirty"`
	TrackedChanges   bool `json:"tracked_changes"`
	UntrackedChanges bool `json:"untracked_changes"`
	Conflicts        bool `json:"conflicts"`
	SubmoduleChanges bool `json:"submodule_changes"`
}

type projectRepositoryListResult struct {
	SchemaVersion string                             `json:"schema_version"`
	Project       projectRepositorySurfaceProject    `json:"project"`
	Source        projectRepositorySurfaceSource     `json:"source"`
	Repositories  []projectRepositorySurfaceListItem `json:"repositories"`
	Page          projectRepositorySurfacePage       `json:"page"`
	ObservedAt    time.Time                          `json:"observed_at"`
}

type projectRepositoryInspectResult struct {
	SchemaVersion string                           `json:"schema_version"`
	Project       projectRepositorySurfaceProject  `json:"project"`
	Source        projectRepositorySurfaceSource   `json:"source"`
	Repository    projectRepositorySurfaceListItem `json:"repository"`
	ObservedAt    time.Time                        `json:"observed_at"`
}

type projectRepositoryStatusResult struct {
	SchemaVersion string                             `json:"schema_version"`
	Project       projectRepositorySurfaceProject    `json:"project"`
	Source        projectRepositorySurfaceSource     `json:"source"`
	Observation   projectstate.ObservationSummary    `json:"observation"`
	Repositories  []projectRepositorySurfaceListItem `json:"repositories"`
	Page          projectRepositorySurfacePage       `json:"page"`
	ObservedAt    time.Time                          `json:"observed_at"`
}

func projectRepositoryRoute(r *http.Request) (projectRef, operation, repositoryRef string, ok bool) {
	path := strings.TrimRight(r.URL.EscapedPath(), "/")
	const prefix = "/v1/projects/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] != "repos" {
		return "", "", "", false
	}
	decodedProject, err := url.PathUnescape(parts[0])
	if err != nil || strings.TrimSpace(decodedProject) == "" {
		return "", "", "", false
	}
	switch {
	case len(parts) == 2:
		return decodedProject, "list", "", true
	case len(parts) == 3 && parts[2] == "status":
		return decodedProject, "status", "", true
	case len(parts) == 4 && parts[2] == "inspect" && parts[3] != "":
		decodedRepository, err := url.PathUnescape(parts[3])
		if err != nil || strings.TrimSpace(decodedRepository) == "" {
			return "", "", "", false
		}
		return decodedProject, "inspect", decodedRepository, true
	default:
		return "", "", "", false
	}
}

func (s Server) handleProjectRepositories(w http.ResponseWriter, r *http.Request, projectRef, operation, repositoryRef string) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "projects", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	if len(projectRef) > projectRepositorySurfaceRefMaxBytes || len(repositoryRef) > projectRepositorySurfaceRefMaxBytes {
		s.writeError(w, correlationID, http.StatusBadRequest, "project.repositories.ref_invalid", "projects", projectRef, "Project or repository reference is invalid.", nil)
		return
	}
	limit, after, err := parseProjectRepositoryPage(r)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "project.repositories.page_invalid", "projects", projectRef, "Repository page is invalid.", err)
		return
	}
	if operation == "inspect" && (limit != projectRepositorySurfaceDefaultLimit || after != "") {
		s.writeError(w, correlationID, http.StatusBadRequest, "project.repositories.page_invalid", "projects", projectRef, "Repository inspection does not accept pagination.", nil)
		return
	}
	selectedProjectID, authorized := s.authorizeProjectRepositoryRead(w, ctx, correlationID, projectRef)
	if !authorized {
		return
	}
	if s.services.ProjectRepos.State == nil {
		s.writeError(w, correlationID, http.StatusServiceUnavailable, "project.repositories.unavailable", "projects", projectRef, "Project repository state is unavailable.", nil)
		return
	}
	projection, err := s.services.ProjectRepos.State.ObserveProject(ctx, selectedProjectID)
	if err != nil {
		status := http.StatusInternalServerError
		code := "project.repositories.read_failed"
		summary := "Could not read project repository state."
		if errors.Is(err, projectstate.ErrProjectStateUnavailable) {
			status = http.StatusNotFound
			code = "project.repositories.not_found"
			summary = "Project repository state was not found."
		}
		s.writeError(w, correlationID, status, code, "projects", projectRef, summary, err)
		return
	}
	switch operation {
	case "list":
		items, page := projectRepositorySurfacePageItems(projection.Members, limit, after)
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, projectRepositoryListResult{
			SchemaVersion: projection.SchemaVersion,
			Project:       projectRepositorySurfaceProjectFromProjection(projection),
			Source:        projectRepositorySurfaceSourceFromProjection(projection),
			Repositories:  items,
			Page:          page,
			ObservedAt:    projection.ObservedAt,
		}))
	case "status":
		items, page := projectRepositorySurfacePageItems(projection.Members, limit, after)
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, projectRepositoryStatusResult{
			SchemaVersion: projection.SchemaVersion,
			Project:       projectRepositorySurfaceProjectFromProjection(projection),
			Source:        projectRepositorySurfaceSourceFromProjection(projection),
			Observation:   projection.Observation,
			Repositories:  items,
			Page:          page,
			ObservedAt:    projection.ObservedAt,
		}))
	case "inspect":
		item, found := projectRepositorySurfaceInspectItem(projection.Members, repositoryRef)
		if !found {
			s.writeError(w, correlationID, http.StatusNotFound, "project.repositories.member_not_found", "projects", repositoryRef, "Project repository member was not found.", nil)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, projectRepositoryInspectResult{
			SchemaVersion: projection.SchemaVersion,
			Project:       projectRepositorySurfaceProjectFromProjection(projection),
			Source:        projectRepositorySurfaceSourceFromProjection(projection),
			Repository:    item,
			ObservedAt:    projection.ObservedAt,
		}))
	default:
		s.writeError(w, correlationID, http.StatusNotFound, "project.repositories.operation_not_found", "projects", operation, "Project repository operation was not found.", nil)
	}
}

func (s Server) authorizeProjectRepositoryRead(w http.ResponseWriter, ctx context.Context, correlationID, projectRef string) (string, bool) {
	resolver := s.services.ProjectRepos.RequestResolver
	if resolver == nil {
		s.writeError(w, correlationID, http.StatusServiceUnavailable, "project.repositories.request_context_unavailable", "projects", projectRef, "Authenticated project request context is unavailable.", nil)
		return "", false
	}
	req, err := resolver(ctx, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusServiceUnavailable, "project.repositories.request_context_unavailable", "projects", projectRef, "Authenticated project request context is unavailable.", err)
		return "", false
	}
	authorizer := s.services.ProjectRepos.Authorizer
	if authorizer == nil {
		s.writeError(w, correlationID, http.StatusServiceUnavailable, "project.repositories.authorization_unavailable", "projects", projectRef, "Project repository authorization is unavailable.", nil)
		return "", false
	}
	selectedProjectID, err := authorizer.AuthorizeProjectRepositoryRead(ctx, req, projectRef)
	if err != nil {
		if errors.Is(err, ErrProjectRepositoryReadForbidden) {
			s.writeError(w, correlationID, http.StatusForbidden, "project.repositories.forbidden", "projects", projectRef, "Actor and node are not authorized to inspect this project repository state.", err)
			return "", false
		}
		s.writeError(w, correlationID, http.StatusServiceUnavailable, "project.repositories.authorization_failed", "projects", projectRef, "Project repository authorization could not be evaluated.", err)
		return "", false
	}
	selectedProjectID = strings.TrimSpace(selectedProjectID)
	if selectedProjectID == "" {
		s.writeError(w, correlationID, http.StatusServiceUnavailable, "project.repositories.authorization_failed", "projects", projectRef, "Project repository authorization did not bind an exact project.", nil)
		return "", false
	}
	return selectedProjectID, true
}

func parseProjectRepositoryPage(r *http.Request) (int, string, error) {
	limit := projectRepositorySurfaceDefaultLimit
	rawLimit := strings.TrimSpace(r.URL.Query().Get("limit"))
	if rawLimit != "" {
		parsed, err := parseLimit(r, projectRepositorySurfaceDefaultLimit)
		if err != nil {
			return 0, "", err
		}
		limit = parsed
	}
	if limit > projectRepositorySurfaceMaxLimit {
		limit = projectRepositorySurfaceMaxLimit
	}
	after := strings.TrimSpace(r.URL.Query().Get("after"))
	if len(after) > projectRepositorySurfaceRefMaxBytes {
		return 0, "", fmt.Errorf("after cursor is too long")
	}
	return limit, after, nil
}

func projectRepositorySurfaceProjectFromProjection(projection projectstate.ProjectProjection) projectRepositorySurfaceProject {
	return projectRepositorySurfaceProject{
		ProjectID: projection.Project.ProjectID,
		Slug:      projection.Project.Slug,
		Lifecycle: projection.Project.Lifecycle,
	}
}

func projectRepositorySurfaceSourceFromProjection(projection projectstate.ProjectProjection) projectRepositorySurfaceSource {
	return projectRepositorySurfaceSource{
		Posture:                      projection.Source.Posture,
		ProjectContractSchemaVersion: projection.Source.ProjectContractSchemaVersion,
		ReposContractSchemaVersion:   projection.Source.ReposContractSchemaVersion,
		SourceRevision:               projection.Source.SourceRevision,
		RegisteredAt:                 projection.Source.RegisteredAt,
	}
}

func projectRepositorySurfacePageItems(members []projectstate.RepositoryProjection, limit int, after string) ([]projectRepositorySurfaceListItem, projectRepositorySurfacePage) {
	ordered := append([]projectstate.RepositoryProjection(nil), members...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].RepositoryID < ordered[j].RepositoryID })
	start := sort.Search(len(ordered), func(index int) bool { return ordered[index].RepositoryID > after })
	end := start + limit
	if end > len(ordered) {
		end = len(ordered)
	}
	items := make([]projectRepositorySurfaceListItem, 0, end-start)
	for _, member := range ordered[start:end] {
		items = append(items, projectRepositorySurfaceItem(member))
	}
	page := projectRepositorySurfacePage{Limit: limit, After: after, HasMore: end < len(ordered)}
	if page.HasMore && len(items) > 0 {
		page.NextAfter = items[len(items)-1].RepositoryID
	}
	return items, page
}

func projectRepositorySurfaceInspectItem(members []projectstate.RepositoryProjection, ref string) (projectRepositorySurfaceListItem, bool) {
	ref = strings.TrimSpace(ref)
	for _, member := range members {
		if member.RepositoryID == ref || member.Key == ref {
			return projectRepositorySurfaceItem(member), true
		}
	}
	return projectRepositorySurfaceListItem{}, false
}

func projectRepositorySurfaceItem(member projectstate.RepositoryProjection) projectRepositorySurfaceListItem {
	var git *projectRepositorySurfaceGit
	if member.Git != nil {
		git = &projectRepositorySurfaceGit{
			Worktree:                      member.Git.Worktree,
			WorktreeCanonicalProjectState: false,
			Bare:                          member.Git.Bare,
			RootMatchesMember:             member.Git.RootMatchesMember,
			CurrentBranch:                 member.Git.CurrentBranch,
			Detached:                      member.Git.Detached,
			DefaultBranch:                 member.Git.DefaultBranch,
			DefaultBranchPosture:          member.Git.DefaultBranchPosture,
			Head:                          member.Git.Head,
			HeadPosture:                   member.Git.HeadPosture,
			Upstream:                      member.Git.Upstream,
			Ahead:                         member.Git.Ahead,
			Behind:                        member.Git.Behind,
			AheadBehindObserved:           member.Git.AheadBehindObserved,
			Dirty: projectRepositorySurfaceGitDirty{
				Dirty:            member.Git.Dirty.Dirty,
				TrackedChanges:   member.Git.Dirty.TrackedChanges,
				UntrackedChanges: member.Git.Dirty.UntrackedChanges,
				Conflicts:        member.Git.Dirty.Conflicts,
				SubmoduleChanges: member.Git.Dirty.SubmoduleChanges,
			},
		}
	}
	return projectRepositorySurfaceListItem{
		RepositoryID:             member.RepositoryID,
		RepositoryOwnerProjectID: member.RepositoryOwnerProjectID,
		Key:                      member.Key,
		RelativePath:             member.RelativeSource,
		Role:                     string(member.Role),
		StateRoot:                member.StateRoot,
		MembershipLifecycle:      string(member.MembershipLifecycle),
		RepositoryLifecycle:      string(member.RepositoryLifecycle),
		ObservationPosture:       string(member.ObservationPosture),
		ReasonCode:               member.ReasonCode,
		ObservedAt:               member.ObservedAt,
		DevelopmentState: projectRepositorySurfaceDevelopmentState{
			Posture:      member.DevelopmentState.Posture,
			RelativePath: member.DevelopmentState.RelativePath,
			ReasonCode:   member.DevelopmentState.ReasonCode,
		},
		Git: git,
	}
}
