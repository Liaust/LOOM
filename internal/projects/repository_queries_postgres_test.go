package projects_test

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"loom.local/loom/internal/projects"
)

func TestReadProjectRepositoryStateReturnsBoundedConsistentSnapshotPostgres(t *testing.T) {
	sqlDB, req := projectRepositoryTransactionDatabase(t)
	ctx := context.Background()
	ids := []string{newRepositoryID(), newRepositoryID()}
	sort.Strings(ids)
	fixture := newRepositoryRegistrationFixture("read-model", projects.ProjectRepositoryProjectSchemaV04, projects.ProjectRepositoryReposSchemaV04, []repositoryMemberFixture{
		{ID: ids[1], Key: "alpha", Path: "alpha", Role: string(projects.ProjectRepositoryRolePrimary), StateRoot: ".repo"},
		{ID: ids[0], Key: "zeta", Path: "zeta", Role: string(projects.ProjectRepositoryRoleComponent)},
	})
	service := projects.NewService(sqlDB)
	if _, err := service.RegisterProjectContract(ctx, req, repositoryRegistrationInput(t, fixture)); err != nil {
		t.Fatalf("register query fixture: %v", err)
	}

	state, err := service.ReadProjectRepositoryState(ctx, fixture.Slug)
	if err != nil {
		t.Fatalf("read project repository state: %v", err)
	}
	if state.Project.ProjectID != fixture.ProjectID || state.Source == nil || state.Source.OwnerNode != "main" || state.Source.SourceRevision != 1 {
		t.Fatalf("unexpected project/source read model: %#v", state)
	}
	if len(state.Members) != 2 || state.Members[0].RepositoryID != ids[0] || state.Members[1].RepositoryID != ids[1] {
		t.Fatalf("members are not in canonical repository-id order: %#v", state.Members)
	}
	for _, member := range state.Members {
		if member.StoredObservationPosture != projects.ProjectRepositoryObservationNotObserved || member.StoredObservedAt != nil || member.SourceBindingDigest == "" || member.ObservationRevision != 1 {
			t.Fatalf("unexpected stored observation summary: %#v", member)
		}
	}
	payload, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal read model: %v", err)
	}
	text := string(payload)
	if strings.Contains(text, fixture.ProjectRoot) || strings.Contains(text, "registration_plan") || strings.Contains(text, "source_snapshot") {
		t.Fatalf("read model leaked raw source or absolute project root: %s", text)
	}

	if _, err := service.UpdateProjectArchiveState(ctx, req, fixture.ProjectID, json.RawMessage(`{"reason":"query-test"}`)); err != nil {
		t.Fatalf("archive query fixture: %v", err)
	}
	archived, err := service.ReadProjectRepositoryState(ctx, fixture.ProjectID)
	if err != nil {
		t.Fatalf("read archived project repository state: %v", err)
	}
	if archived.Project.Status != "archived" {
		t.Fatalf("archived project lifecycle = %q", archived.Project.Status)
	}
	for _, member := range archived.Members {
		if member.MembershipLifecycle != projects.RepositoryLifecycleArchived || member.RepositoryLifecycle != projects.RepositoryLifecycleArchived {
			t.Fatalf("archived member remains actionable: %#v", member)
		}
	}
}
