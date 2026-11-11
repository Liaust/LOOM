package projects_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"loom.local/loom/internal/projects"
)

func TestProjectRegistrationRetirementTransactionPostgres(t *testing.T) {
	db, req := projectRepositoryTransactionDatabase(t)
	s := projects.NewService(db)
	ctx := context.Background()
	f := newRepositoryRegistrationFixture("retirement", projects.ProjectRepositoryProjectSchemaV04, projects.ProjectRepositoryReposSchemaV04, []repositoryMemberFixture{{ID: newRepositoryID(), Key: "primary", Path: "primary", Role: string(projects.ProjectRepositoryRolePrimary)}})
	r, err := s.RegisterProjectContract(ctx, req, repositoryRegistrationInput(t, f))
	if err != nil {
		t.Fatal(err)
	}
	repo, err := s.ReadProjectRepositoryState(ctx, f.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	req.ScopeID = r.Detail.Project.Project.ProjectScopeID
	req.ScopeKey = r.Detail.Project.Project.ProjectScopeKey
	state, _ := json.Marshal(map[string]any{"schema_version": "project.registration_retirement.v1", "project_id": f.ProjectID, "plan_digest": "sha256:" + strings.Repeat("a", 64), "evidence_kind": "retained_registration_disposition"})
	in := projects.ProjectRegistrationRetirementInput{ProjectID: f.ProjectID, ScopeID: req.ScopeID, RegistrationID: r.Detail.Registration.ProjectContractRegistrationID, RegistrationRevision: r.Detail.Registration.RegistrationRevision, ContractHash: r.Detail.Registration.ContractHash, RepositoryRevision: repo.Source.SourceRevision, ExpectedArchiveState: r.Detail.Project.Project.ArchiveState, State: state}
	for _, m := range repo.Members {
		in.Members = append(in.Members, projects.ProjectArchiveRepositoryMemberBinding{RepositoryID: m.RepositoryID, RepositoryOwnerProjectID: m.RepositoryOwnerProjectID, MemberKey: m.Key, Role: m.Role})
	}
	in.ActivationStatus = r.Detail.Registration.ActivationStatus
	bad := in
	bad.RegistrationRevision++
	if _, err = s.RetireProjectRegistration(ctx, req, bad); err == nil {
		t.Fatal("changed revision retired")
	}
	current, err := s.GetProjectRegistrationStatus(ctx, f.ProjectID)
	if err != nil || current.Project.Project.Status != "active" {
		t.Fatal("refusal changed project", err)
	}
	history := rowCount(t, db, `SELECT count(*) FROM projects.project_repository_source_history WHERE project_id=$1`, f.ProjectID)
	p, err := s.RetireProjectRegistration(ctx, req, in)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != "archived" {
		t.Fatal("not retired")
	}
	current, err = s.GetProjectRegistrationStatus(ctx, f.ProjectID)
	if err != nil || current.Registration.RegistrationStatus != "archived" || current.Registration.ActivationStatus != "inactive" {
		t.Fatal("registration not retired", err)
	}
	assertRowCount(t, db, `SELECT count(*) FROM scopes.scopes WHERE scope_id=$1 AND status='archived'`, 1, req.ScopeID)
	assertRowCount(t, db, `SELECT count(*) FROM projects.project_repository_memberships WHERE project_id=$1 AND lifecycle_status='archived'`, len(in.Members), f.ProjectID)
	assertRowCount(t, db, `SELECT count(*) FROM projects.project_repository_source_history WHERE project_id=$1`, history, f.ProjectID)
}
