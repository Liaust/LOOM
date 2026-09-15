package projects_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/projects"
)

func TestProjectPhysicalArchiveLifecycleTransactionPostgres(t *testing.T) {
	sqlDB, req := projectRepositoryTransactionDatabase(t)
	service := projects.NewService(sqlDB)
	ctx := context.Background()
	fixture := newRepositoryRegistrationFixture("physical-archive", projects.ProjectRepositoryProjectSchemaV04, projects.ProjectRepositoryReposSchemaV04, []repositoryMemberFixture{
		{ID: newRepositoryID(), Key: "primary", Path: "primary", Role: string(projects.ProjectRepositoryRolePrimary)},
		{ID: newRepositoryID(), Key: "component", Path: "component", Role: string(projects.ProjectRepositoryRoleComponent)},
	})
	registered, err := service.RegisterProjectContract(ctx, req, repositoryRegistrationInput(t, fixture))
	if err != nil {
		t.Fatal(err)
	}
	repository, err := service.ReadProjectRepositoryState(ctx, fixture.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if registered.Detail.Registration == nil || repository.Source == nil {
		t.Fatal("physical archive fixture is missing registration or repository source")
	}

	historyBefore := rowCount(t, sqlDB, `SELECT count(*) FROM projects.project_repository_source_history WHERE project_id = $1`, fixture.ProjectID)
	started := time.Date(2026, 9, 4, 13, 0, 0, 0, time.UTC)
	state := projects.ProjectPhysicalArchiveState{
		SchemaVersion:       projects.ProjectPhysicalArchiveStateSchemaVersion,
		Status:              projects.ProjectPhysicalArchiveStatusInProgress,
		Phase:               projects.ProjectArchivePhaseDeactivationPending,
		MutationBlocked:     true,
		ProjectID:           fixture.ProjectID,
		ProjectSlug:         fixture.Slug,
		OperationID:         "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		PlanDigest:          "sha256:" + strings.Repeat("a", 64),
		WorkspacePlanDigest: "sha256:" + strings.Repeat("b", 64),
		ActivePath:          fixture.ProjectRoot,
		ArchivePath:         fixture.ProjectRoot + "-archive",
		ActorID:             req.ActorID,
		Reason:              "archive disposable project fixture",
		StartedAt:           started,
	}
	members := make([]projects.ProjectArchiveRepositoryMemberBinding, 0, len(repository.Members))
	for _, member := range repository.Members {
		members = append(members, projects.ProjectArchiveRepositoryMemberBinding{
			RepositoryID:             member.RepositoryID,
			RepositoryOwnerProjectID: member.RepositoryOwnerProjectID,
			MemberKey:                member.Key,
			Role:                     member.Role,
		})
	}
	transition := func(state projects.ProjectPhysicalArchiveState) projects.ProjectPhysicalArchiveTransitionResult {
		t.Helper()
		result, err := service.TransitionProjectPhysicalArchive(ctx, req, projects.ProjectPhysicalArchiveTransitionInput{
			State:                            state,
			ExpectedScopeID:                  registered.Detail.Project.Project.ProjectScopeID,
			ExpectedRegistrationID:           registered.Detail.Registration.ProjectContractRegistrationID,
			ExpectedRegistrationRevision:     registered.Detail.Registration.RegistrationRevision,
			ExpectedRepositorySourceRevision: repository.Source.SourceRevision,
			ExpectedMembers:                  members,
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	transition(state)
	runtimeAt := started.Add(time.Second)
	state.Phase = projects.ProjectArchivePhaseRuntimeDeactivated
	state.RuntimeDeactivatedAt = &runtimeAt
	state.RuntimeQuiescenceDigest = "sha256:" + strings.Repeat("d", 64)
	transition(state)
	workspaceAt := runtimeAt.Add(time.Second)
	state.Phase = projects.ProjectArchivePhaseProjectStatePending
	state.WorkspaceCompletedAt = &workspaceAt
	state.ArchiveManifestDigest = "sha256:" + strings.Repeat("c", 64)
	transition(state)
	archivedAt := workspaceAt.Add(time.Second)
	state.Phase = projects.ProjectArchivePhaseComplete
	state.Status = projects.ProjectPhysicalArchiveStatusArchived
	state.ArchivedAt = &archivedAt
	completed := transition(state)
	if completed.Replay || completed.EventID == "" || completed.Project.Status != "archived" {
		t.Fatalf("completed transition = %#v", completed)
	}

	assertRowCount(t, sqlDB, `SELECT count(*) FROM projects.project_repository_source_history WHERE project_id = $1`, historyBefore, fixture.ProjectID)
	assertRowCount(t, sqlDB, `SELECT count(*) FROM projects.project_repository_sources WHERE project_id = $1 AND source_revision = $2`, 1, fixture.ProjectID, repository.Source.SourceRevision)
	assertRowCount(t, sqlDB, `SELECT count(*) FROM projects.project_repository_memberships WHERE project_id = $1 AND lifecycle_status = 'archived'`, len(members), fixture.ProjectID)
	assertRowCount(t, sqlDB, `SELECT count(*) FROM projects.repositories WHERE owning_project_id = $1 AND lifecycle_status = 'archived'`, len(members), fixture.ProjectID)
	assertRowCount(t, sqlDB, `SELECT count(*) FROM events.events WHERE event_type = 'project.archived' AND target_id = $1`, 1, fixture.ProjectID)

	replay := transition(state)
	if !replay.Replay || replay.EventID != "" {
		t.Fatalf("completed replay = %#v", replay)
	}
	assertRowCount(t, sqlDB, `SELECT count(*) FROM events.events WHERE event_type = 'project.archived' AND target_id = $1`, 1, fixture.ProjectID)
}
