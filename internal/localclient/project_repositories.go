package localclient

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"loom.local/loom/internal/projectstate"
	"loom.local/loom/internal/response"
)

type ProjectRepositoryPageOptions struct {
	Limit int
	After string
}

type ProjectRepositoryProject struct {
	ProjectID string `json:"project_id"`
	Slug      string `json:"slug"`
	Lifecycle string `json:"lifecycle"`
}

type ProjectRepositorySource struct {
	Posture                      projectstate.SourcePosture `json:"posture"`
	ProjectContractSchemaVersion string                     `json:"project_contract_schema_version,omitempty"`
	ReposContractSchemaVersion   string                     `json:"repos_contract_schema_version,omitempty"`
	SourceRevision               int64                      `json:"source_revision,omitempty"`
	RegisteredAt                 *time.Time                 `json:"registered_at,omitempty"`
}

type ProjectRepositoryPage struct {
	Limit     int    `json:"limit"`
	After     string `json:"after,omitempty"`
	NextAfter string `json:"next_after,omitempty"`
	HasMore   bool   `json:"has_more"`
}

type ProjectRepositoryItem struct {
	RepositoryID             string                            `json:"repository_id"`
	RepositoryOwnerProjectID string                            `json:"repository_owner_project_id"`
	Key                      string                            `json:"key"`
	RelativePath             string                            `json:"relative_path"`
	Role                     string                            `json:"role"`
	StateRoot                string                            `json:"state_root,omitempty"`
	MembershipLifecycle      string                            `json:"membership_lifecycle"`
	RepositoryLifecycle      string                            `json:"repository_lifecycle"`
	ObservationPosture       string                            `json:"observation_posture"`
	ReasonCode               string                            `json:"reason_code,omitempty"`
	ObservedAt               *time.Time                        `json:"observed_at,omitempty"`
	DevelopmentState         ProjectRepositoryDevelopmentState `json:"development_state"`
	Git                      *ProjectRepositoryGit             `json:"git,omitempty"`
}

type ProjectRepositoryDevelopmentState struct {
	Posture      projectstate.DevelopmentStatePosture `json:"posture"`
	RelativePath string                               `json:"relative_path,omitempty"`
	ReasonCode   string                               `json:"reason_code,omitempty"`
}

type ProjectRepositoryGit struct {
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
	Dirty                         ProjectRepositoryGitDirty            `json:"dirty"`
}

type ProjectRepositoryGitDirty struct {
	Dirty            bool `json:"dirty"`
	TrackedChanges   bool `json:"tracked_changes"`
	UntrackedChanges bool `json:"untracked_changes"`
	Conflicts        bool `json:"conflicts"`
	SubmoduleChanges bool `json:"submodule_changes"`
}

type ProjectRepositoryListResult struct {
	SchemaVersion string                   `json:"schema_version"`
	Project       ProjectRepositoryProject `json:"project"`
	Source        ProjectRepositorySource  `json:"source"`
	Repositories  []ProjectRepositoryItem  `json:"repositories"`
	Page          ProjectRepositoryPage    `json:"page"`
	ObservedAt    time.Time                `json:"observed_at"`
}

type ProjectRepositoryInspectResult struct {
	SchemaVersion string                   `json:"schema_version"`
	Project       ProjectRepositoryProject `json:"project"`
	Source        ProjectRepositorySource  `json:"source"`
	Repository    ProjectRepositoryItem    `json:"repository"`
	ObservedAt    time.Time                `json:"observed_at"`
}

type ProjectRepositoryStatusResult struct {
	SchemaVersion string                          `json:"schema_version"`
	Project       ProjectRepositoryProject        `json:"project"`
	Source        ProjectRepositorySource         `json:"source"`
	Observation   projectstate.ObservationSummary `json:"observation"`
	Repositories  []ProjectRepositoryItem         `json:"repositories"`
	Page          ProjectRepositoryPage           `json:"page"`
	ObservedAt    time.Time                       `json:"observed_at"`
}

func (c Client) ListProjectRepositories(ctx context.Context, correlationID, projectRef string, options ProjectRepositoryPageOptions) (response.Envelope[ProjectRepositoryListResult], error) {
	path := "/v1/projects/" + url.PathEscape(projectRef) + "/repos" + projectRepositoryPageQuery(options)
	return doJSON[ProjectRepositoryListResult](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) InspectProjectRepository(ctx context.Context, correlationID, projectRef, repositoryRef string) (response.Envelope[ProjectRepositoryInspectResult], error) {
	path := "/v1/projects/" + url.PathEscape(projectRef) + "/repos/inspect/" + url.PathEscape(repositoryRef)
	return doJSON[ProjectRepositoryInspectResult](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetProjectRepositoryStatus(ctx context.Context, correlationID, projectRef string, options ProjectRepositoryPageOptions) (response.Envelope[ProjectRepositoryStatusResult], error) {
	path := "/v1/projects/" + url.PathEscape(projectRef) + "/repos/status" + projectRepositoryPageQuery(options)
	return doJSON[ProjectRepositoryStatusResult](c, ctx, http.MethodGet, path, correlationID, nil)
}

func projectRepositoryPageQuery(options ProjectRepositoryPageOptions) string {
	values := url.Values{}
	if options.Limit > 0 {
		values.Set("limit", strconv.Itoa(options.Limit))
	}
	if options.After != "" {
		values.Set("after", options.After)
	}
	if encoded := values.Encode(); encoded != "" {
		return "?" + encoded
	}
	return ""
}
