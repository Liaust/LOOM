package projectstate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/projects"
)

type Service struct {
	Reader             Reader
	Git                GitObserver
	Paths              PathResolver
	DevelopmentState   DevelopmentStateInspector
	ProjectDevelopment ProjectDevelopmentInspector
	Clock              Clock
	LocalNode          string
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

func (s Service) ObserveProject(ctx context.Context, ref string) (ProjectProjection, error) {
	projection, _, err := s.observeProject(ctx, ref)
	return projection, err
}

// observeProject retains the exact bounded registry snapshot used for observation.
// The provenance bridge consumes it directly instead of mixing a later reread.
func (s Service) observeProject(ctx context.Context, ref string) (ProjectProjection, projects.ProjectRepositoryReadModel, error) {
	return s.observeProjectMode(ctx, ref, true)
}

func (s Service) observeProjectMode(ctx context.Context, ref string, inspectRepositories bool) (ProjectProjection, projects.ProjectRepositoryReadModel, error) {
	if s.Reader == nil {
		return ProjectProjection{}, projects.ProjectRepositoryReadModel{}, fmt.Errorf("%w: reader is required", ErrInvalidObservationConfiguration)
	}
	localNode := strings.TrimSpace(s.LocalNode)
	if localNode == "" {
		return ProjectProjection{}, projects.ProjectRepositoryReadModel{}, fmt.Errorf("%w: local node is required", ErrInvalidObservationConfiguration)
	}
	clock := s.Clock
	if clock == nil {
		clock = systemClock{}
	}
	now := clock.Now().UTC()
	model, err := s.Reader.ReadProjectRepositoryState(ctx, ref)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ProjectProjection{}, projects.ProjectRepositoryReadModel{}, fmt.Errorf("%w: project not found", ErrProjectStateUnavailable)
		}
		return ProjectProjection{}, projects.ProjectRepositoryReadModel{}, fmt.Errorf("%w: %v", ErrProjectStateUnavailable, err)
	}
	projection := ProjectProjection{
		SchemaVersion: SchemaVersion,
		Project: ProjectIdentityProjection{
			ProjectID:       model.Project.ProjectID,
			ProjectScopeID:  model.Project.ProjectScopeID,
			ProjectScopeKey: model.Project.ProjectScopeKey,
			Slug:            model.Project.Slug,
			Name:            model.Project.Name,
			Lifecycle:       model.Project.Status,
			UpdatedAt:       model.Project.UpdatedAt.UTC(),
		},
		Facets:     make([]FacetProjection, 0, len(model.Facets)),
		Source:     SourceProjection{Posture: SourcePostureNotRegistered},
		Members:    []RepositoryProjection{},
		ObservedAt: now,
	}
	projection.Development = emptyProjectDevelopment(ProjectDevelopmentInput{ProjectID: model.Project.ProjectID})
	projection.Development.Posture = ProjectDevelopmentNotRegistered
	projection.Development.ReasonCode = "project_source_not_registered"
	for _, facet := range model.Facets {
		projection.Facets = append(projection.Facets, FacetProjection{
			Key:         facet.FacetKey,
			Folder:      facet.Folder,
			Enabled:     facet.Enabled,
			Present:     facet.Present,
			Placeholder: facet.Placeholder,
			Status:      facet.Status,
		})
	}
	sort.Slice(projection.Facets, func(i, j int) bool { return projection.Facets[i].Key < projection.Facets[j].Key })
	if model.Source == nil {
		projection.Observation = summarizeObservations(projection.Members)
		return projection, model, nil
	}
	if _, err := repositoryMembershipSource(model.Source); err != nil {
		return ProjectProjection{}, projects.ProjectRepositoryReadModel{}, err
	}
	registeredAt := model.Source.RegisteredAt.UTC()
	projection.Source = SourceProjection{
		Posture:                      SourcePostureRegistered,
		ProjectContractSchemaVersion: model.Source.ProjectContractSchemaVersion,
		ReposContractSchemaVersion:   model.Source.ReposContractSchemaVersion,
		OwnerNode:                    model.Source.OwnerNode,
		SemanticDigest:               model.Source.SemanticDigest,
		LocationDigest:               model.Source.LocationDigest,
		SourceRevision:               model.Source.SourceRevision,
		RegisteredAt:                 &registeredAt,
	}
	projectDevelopment := s.ProjectDevelopment
	if projectDevelopment == nil {
		projectDevelopment = FileProjectDevelopmentInspector{}
	}
	projection.Development = projectDevelopment.Inspect(ctx, ProjectDevelopmentInput{
		ProjectRoot:    model.Source.ProjectRoot,
		ProjectID:      model.Project.ProjectID,
		OwnerNode:      model.Source.OwnerNode,
		LocalNode:      localNode,
		Lifecycle:      model.Project.Status,
		SourceRevision: model.Source.SourceRevision,
		ObserveGit:     inspectRepositories,
	})

	paths := s.Paths
	if paths == nil {
		paths = OSPathResolver{}
	}
	git := s.Git
	if git == nil {
		git = GitAdapter{}
	}
	developmentState := s.DevelopmentState
	if developmentState == nil {
		developmentState = FileDevelopmentStateInspector{Paths: paths}
	}
	members := append([]projects.ProjectRepositoryReadMember(nil), model.Members...)
	sort.Slice(members, func(i, j int) bool { return members[i].RepositoryID < members[j].RepositoryID })
	for _, member := range members {
		item := RepositoryProjection{
			RepositoryID:             member.RepositoryID,
			RepositoryOwnerProjectID: member.RepositoryOwnerProjectID,
			Key:                      member.Key,
			Role:                     member.Role,
			RelativeSource:           member.Path,
			StateRoot:                member.StateRoot,
			MembershipLifecycle:      member.MembershipLifecycle,
			RepositoryLifecycle:      member.RepositoryLifecycle,
			SourceBindingDigest:      member.SourceBindingDigest,
			ObservationPosture:       projects.ProjectRepositoryObservationNotObserved,
			DevelopmentState:         DevelopmentStateProjection{Posture: DevelopmentStateNotObserved},
			Problems:                 []Problem{},
		}
		if strings.TrimSpace(model.Source.OwnerNode) != localNode {
			item.ObservationPosture = projects.ProjectRepositoryObservationRemoteUnavailable
			item.ReasonCode = "owner_node_not_local"
			observedAt := now
			item.ObservedAt = &observedAt
			item.Problems = append(item.Problems, Problem{Code: "remote_unavailable", Severity: ProblemSeverityWarning, Summary: "Repository owner node is not observable through the current local backend."})
			projection.Members = append(projection.Members, item)
			continue
		}
		if !inspectRepositories {
			item.ReasonCode = "repository_not_sampled"
			projection.Members = append(projection.Members, item)
			continue
		}

		physicalMemberPath, pathErr := repositorySourceLocation(model.Source, member.Path)
		var resolved ResolvedPath
		if pathErr == nil {
			resolved, pathErr = paths.ResolveWithin(model.Source.ProjectRoot, physicalMemberPath)
		}
		if pathErr != nil {
			item.ReasonCode = pathObservationReason(pathErr)
			item.Problems = append(item.Problems, Problem{Code: item.ReasonCode, Severity: ProblemSeverityError, Summary: "Registered repository source path is unsafe or unavailable."})
			projection.Members = append(projection.Members, item)
			continue
		}
		if !resolved.Exists {
			item.ReasonCode = "member_path_missing"
			item.Problems = append(item.Problems, Problem{Code: item.ReasonCode, Severity: ProblemSeverityWarning, Summary: "Registered repository source path is absent."})
			projection.Members = append(projection.Members, item)
			continue
		}
		if !resolved.Directory {
			item.ReasonCode = "member_path_not_directory"
			item.Problems = append(item.Problems, Problem{Code: item.ReasonCode, Severity: ProblemSeverityError, Summary: "Registered repository source path is not a directory."})
			projection.Members = append(projection.Members, item)
			continue
		}

		item.DevelopmentState = developmentState.Inspect(ctx, DevelopmentStateInput{
			MemberRoot:   resolved.Path,
			StateRoot:    member.StateRoot,
			ProjectID:    model.Project.ProjectID,
			RepositoryID: member.RepositoryID,
		})
		if item.DevelopmentState.Posture == DevelopmentStateInvalid || item.DevelopmentState.Posture == DevelopmentStateMismatch {
			item.Problems = append(item.Problems, Problem{Code: item.DevelopmentState.ReasonCode, Severity: ProblemSeverityError, Summary: "Repository development-state backlink is invalid or mismatched."})
		}

		gitProjection, gitErr := git.Observe(ctx, resolved.Path)
		if gitErr != nil {
			item.ReasonCode = gitObservationReason(gitErr)
			severity := ProblemSeverityWarning
			if item.ReasonCode == "git_root_mismatch" {
				severity = ProblemSeverityError
			}
			item.Problems = append(item.Problems, Problem{Code: item.ReasonCode, Severity: severity, Summary: "Registered member could not be observed as its own Git repository root."})
			projection.Members = append(projection.Members, item)
			continue
		}
		gitProjection.WorktreeCanonicalProjectState = false
		item.Git = &gitProjection
		item.ObservationPosture = projects.ProjectRepositoryObservationObserved
		observedAt := now
		item.ObservedAt = &observedAt
		projection.Members = append(projection.Members, item)
	}
	projection.Observation = summarizeObservations(projection.Members)
	return projection, model, nil
}

func summarizeObservations(members []RepositoryProjection) ObservationSummary {
	summary := ObservationSummary{MemberCount: len(members), Posture: projects.ProjectRepositoryObservationNotObserved}
	for _, member := range members {
		switch member.ObservationPosture {
		case projects.ProjectRepositoryObservationObserved:
			summary.Observed++
		case projects.ProjectRepositoryObservationRemoteUnavailable:
			summary.RemoteUnavailable++
		default:
			summary.NotObserved++
		}
	}
	switch {
	case len(members) > 0 && summary.Observed == len(members):
		summary.Posture = projects.ProjectRepositoryObservationObserved
	case summary.NotObserved == 0 && summary.RemoteUnavailable > 0:
		summary.Posture = projects.ProjectRepositoryObservationRemoteUnavailable
	default:
		summary.Posture = projects.ProjectRepositoryObservationNotObserved
	}
	return summary
}

func pathObservationReason(err error) string {
	switch {
	case errors.Is(err, ErrPathEscape):
		return "path_escape"
	case errors.Is(err, ErrPathInvalid):
		return "invalid_member_path"
	default:
		return "member_path_unavailable"
	}
}
