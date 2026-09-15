package supportbundle

import (
	"context"

	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectstate"
	"loom.local/loom/internal/response"
)

type ProjectsSummary struct {
	Total            int                         `json:"total"`
	ByStatus         map[string]int              `json:"by_status"`
	Projects         []ProjectOperationalSummary `json:"projects,omitempty"`
	SelectedProjects []SelectedProjectSummary    `json:"selected_projects,omitempty"`
}

type ProjectOperationalSummary struct {
	ProjectID   string `json:"project_id,omitempty"`
	Slug        string `json:"slug,omitempty"`
	Name        string `json:"name,omitempty"`
	Status      string `json:"status,omitempty"`
	Priority    string `json:"priority,omitempty"`
	ProjectType string `json:"project_type,omitempty"`
}

type SelectedProjectSummary struct {
	Project         ProjectOperationalSummary      `json:"project"`
	Facets          []ProjectFacetSummary          `json:"facets,omitempty"`
	RepositoryState *ProjectRepositoryStateSummary `json:"repository_state,omitempty"`
}

type ProjectFacetSummary struct {
	FacetKey    string `json:"facet_key,omitempty"`
	Folder      string `json:"folder,omitempty"`
	Enabled     bool   `json:"enabled"`
	Present     bool   `json:"present"`
	Placeholder bool   `json:"placeholder"`
	Status      string `json:"status,omitempty"`
}

type ProjectRepositoryStateSummary struct {
	Lifecycle                    string                                       `json:"lifecycle,omitempty"`
	ProjectContractSchemaVersion string                                       `json:"project_contract_schema_version,omitempty"`
	ReposContractSchemaVersion   string                                       `json:"repos_contract_schema_version,omitempty"`
	SemanticDigest               string                                       `json:"semantic_digest,omitempty"`
	LocationDigest               string                                       `json:"location_digest,omitempty"`
	SourceRevision               int64                                        `json:"source_revision,omitempty"`
	ObservationPosture           projects.ProjectRepositoryObservationPosture `json:"observation_posture"`
	MemberCount                  int                                          `json:"member_count"`
	Members                      []ProjectRepositoryMemberSummary             `json:"members,omitempty"`
	MembersTruncated             bool                                         `json:"members_truncated"`
}

type ProjectRepositoryMemberSummary struct {
	RepositoryID                  string                                       `json:"repository_id"`
	Role                          projects.ProjectRepositoryRole               `json:"role"`
	RelativeSource                string                                       `json:"relative_source"`
	ObservationPosture            projects.ProjectRepositoryObservationPosture `json:"observation_posture"`
	ReasonCode                    string                                       `json:"reason_code,omitempty"`
	DevelopmentStatePosture       projectstate.DevelopmentStatePosture         `json:"development_state_posture"`
	DevelopmentStateReasonCode    string                                       `json:"development_state_reason_code,omitempty"`
	Head                          string                                       `json:"head,omitempty"`
	CurrentBranch                 string                                       `json:"current_branch,omitempty"`
	DefaultBranch                 string                                       `json:"default_branch,omitempty"`
	Ahead                         int                                          `json:"ahead"`
	Behind                        int                                          `json:"behind"`
	Dirty                         bool                                         `json:"dirty"`
	TrackedChanges                bool                                         `json:"tracked_changes"`
	UntrackedChanges              bool                                         `json:"untracked_changes"`
	Conflicts                     bool                                         `json:"conflicts"`
	SubmoduleChanges              bool                                         `json:"submodule_changes"`
	Worktree                      bool                                         `json:"worktree"`
	WorktreeCanonicalProjectState bool                                         `json:"worktree_canonical_project_state"`
	ProblemCodes                  []string                                     `json:"problem_codes,omitempty"`
}

// This narrow optional interface lets the existing projects collector consume
// Slice 3 state once a later authorized client surface implements it. Keeping
// it local avoids changing collector registration or prematurely expanding the
// shared client contract in this slice.
type projectRepositoryStateClient interface {
	GetProjectRepositoryState(context.Context, string, string) (response.Envelope[projectstate.ProjectProjection], error)
}

func collectProjects(ctx context.Context, collection CollectionContext) (CollectorOutput, error) {
	client, err := projectsClient(collection)
	if err != nil {
		return CollectorOutput{}, err
	}
	limit := itemLimit(collection.Options)
	envelope, err := client.ListProjects(ctx, correlationID(collection), limit)
	if err != nil {
		return CollectorOutput{}, err
	}
	summary := ProjectsSummary{ByStatus: map[string]int{}}
	for _, project := range limitItems(envelope.Data, limit) {
		summary.Total++
		summary.ByStatus[project.Status]++
		summary.Projects = append(summary.Projects, projectOperationalSummary(project))
	}
	for _, ref := range collection.Options.Projects {
		detail, err := client.GetProjectRegistrationStatus(ctx, correlationID(collection), ref)
		if err != nil {
			return CollectorOutput{}, err
		}
		selected := SelectedProjectSummary{Project: projectOperationalSummary(detail.Data.Project.Project)}
		for _, facet := range limitItems(detail.Data.Facets, limit) {
			selected.Facets = append(selected.Facets, ProjectFacetSummary{
				FacetKey:    facet.FacetKey,
				Folder:      facet.Folder,
				Enabled:     facet.Enabled,
				Present:     facet.Present,
				Placeholder: facet.Placeholder,
				Status:      facet.FacetStatus,
			})
		}
		if stateClient, ok := client.(projectRepositoryStateClient); ok {
			state, err := stateClient.GetProjectRepositoryState(ctx, correlationID(collection), ref)
			if err != nil {
				return CollectorOutput{}, err
			}
			repositorySummary := projectRepositoryOperationalSummary(state.Data, limit)
			selected.RepositoryState = &repositorySummary
		}
		summary.SelectedProjects = append(summary.SelectedProjects, selected)
	}
	return jsonSummary("summaries/projects.json", summary, PrivacyDiagnosticSummary)
}

func projectRepositoryOperationalSummary(state projectstate.ProjectProjection, limit int) ProjectRepositoryStateSummary {
	summary := ProjectRepositoryStateSummary{
		Lifecycle:                    state.Project.Lifecycle,
		ProjectContractSchemaVersion: state.Source.ProjectContractSchemaVersion,
		ReposContractSchemaVersion:   state.Source.ReposContractSchemaVersion,
		SemanticDigest:               state.Source.SemanticDigest,
		LocationDigest:               state.Source.LocationDigest,
		SourceRevision:               state.Source.SourceRevision,
		ObservationPosture:           state.Observation.Posture,
		MemberCount:                  state.Observation.MemberCount,
		Members:                      []ProjectRepositoryMemberSummary{},
	}
	bounded := limitItems(state.Members, limit)
	summary.MembersTruncated = len(bounded) < len(state.Members)
	for _, member := range bounded {
		item := ProjectRepositoryMemberSummary{
			RepositoryID:               member.RepositoryID,
			Role:                       member.Role,
			RelativeSource:             member.RelativeSource,
			ObservationPosture:         member.ObservationPosture,
			ReasonCode:                 member.ReasonCode,
			DevelopmentStatePosture:    member.DevelopmentState.Posture,
			DevelopmentStateReasonCode: member.DevelopmentState.ReasonCode,
			ProblemCodes:               []string{},
		}
		for _, problem := range limitItems(member.Problems, limit) {
			item.ProblemCodes = append(item.ProblemCodes, problem.Code)
		}
		if member.Git != nil {
			item.Head = member.Git.Head
			item.CurrentBranch = member.Git.CurrentBranch
			item.DefaultBranch = member.Git.DefaultBranch
			item.Ahead = member.Git.Ahead
			item.Behind = member.Git.Behind
			item.Dirty = member.Git.Dirty.Dirty
			item.TrackedChanges = member.Git.Dirty.TrackedChanges
			item.UntrackedChanges = member.Git.Dirty.UntrackedChanges
			item.Conflicts = member.Git.Dirty.Conflicts
			item.SubmoduleChanges = member.Git.Dirty.SubmoduleChanges
			item.Worktree = member.Git.Worktree
			item.WorktreeCanonicalProjectState = false
		}
		summary.Members = append(summary.Members, item)
	}
	return summary
}

func projectOperationalSummary(project projects.Project) ProjectOperationalSummary {
	return ProjectOperationalSummary{
		ProjectID:   project.ProjectID,
		Slug:        project.Slug,
		Name:        project.Name,
		Status:      project.Status,
		Priority:    project.Priority,
		ProjectType: project.ProjectType,
	}
}
