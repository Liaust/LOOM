package projectapply

import (
	"encoding/json"
	"loom.local/loom/internal/ids"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"os"
	"path/filepath"
	"testing"
)

func TestDeclarationRealAuthorityPostgres(t *testing.T) {
	service, _, p, root, database := realOwnerFixture(t, 0)
	request := pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: root}
	plan, err := service.Plan(t.Context(), p, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(`UPDATE identity.actor_node_authorizations SET status='revoked' WHERE actor_id=$1`, p.ActorID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Apply(t.Context(), p, realApplyRequest(plan, root)); err == nil {
		t.Fatal("revoked authority accepted")
	}
	if _, err = database.Exec(`UPDATE identity.actor_node_authorizations SET status='active',expires_at=now()-interval '1 second' WHERE actor_id=$1`, p.ActorID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Plan(t.Context(), p, request); err == nil {
		t.Fatal("expired authority accepted")
	}
	if _, err = database.Exec(`UPDATE identity.actor_node_authorizations SET expires_at=NULL WHERE actor_id=$1`, p.ActorID); err != nil {
		t.Fatal(err)
	}
	plan, err = service.Plan(t.Context(), p, request)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(t.Context(), p, realApplyRequest(plan, root))
	if err != nil {
		t.Fatal(err)
	}
	project := result.Target.ProjectID
	if _, err = database.Exec(`UPDATE projects.project_policy_profiles SET backup_policy='{"retain":"all"}',indexing_policy='{"mode":"notes"}' WHERE project_id=$1`, project); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Plan(t.Context(), p, request); err != nil {
		t.Fatalf("unrelated policies treated as authority restrictions: %v", err)
	}
	if _, err = database.Exec(`UPDATE projects.project_policy_profiles SET approval_requirements='{"declaration":true}' WHERE project_id=$1`, project); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Plan(t.Context(), p, request); err == nil {
		t.Fatal("unknown relevant policy accepted")
	}
	if _, err = database.Exec(`UPDATE projects.project_policy_profiles SET approval_requirements='{}' WHERE project_id=$1`, project); err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(`UPDATE projects.project_memberships SET status='revoked' WHERE project_id=$1 AND actor_id=$2`, project, p.ActorID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Operation(t.Context(), p, result.OperationID); err == nil {
		t.Fatal("historical result leaked after membership revocation")
	}
}

func TestDeclarationRealForeignRepositoryAuthorityPostgres(t *testing.T) {
	service, resolver, p, root, db := realOwnerFixture(t, 1)
	plan, err := service.Plan(t.Context(), p, pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: root})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Apply(t.Context(), p, realApplyRequest(plan, root))
	if err != nil {
		t.Fatal(err)
	}
	state, err := projects.NewService(db).GetDeclarationProjectState(t.Context(), first.Target.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	secondRoot := filepath.Join(filepath.Dir(root), "reference-project")
	if err = os.MkdirAll(filepath.Join(secondRoot, ".loom"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(secondRoot, "shared"), 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"kind": "loom.project", "schema_version": pc.ProjectSchemaV05, "project": map[string]string{"id": ids.NewProjectID(), "slug": "reference-project", "name": "Reference", "owner_node": "main"}, "resources": map[string]any{"shared": map[string]any{"kind": "repository", "repository": map[string]string{"id": state.Repositories["repo0"], "role": "reference", "path": "shared"}}}})
	if err = os.WriteFile(filepath.Join(secondRoot, pc.CanonicalRootContractPath), raw, 0600); err != nil {
		t.Fatal(err)
	}
	plan, err = service.Plan(t.Context(), p, pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: secondRoot})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE projects.project_memberships SET status='revoked' WHERE project_id=$1 AND actor_id=$2`, first.Target.ProjectID, p.ActorID); err != nil {
		t.Fatal(err)
	}
	authority, err := projects.NewService(db).GetDeclarationAuthorityForRepositories(t.Context(), declarationRequest(p), plan.Basis.Target.ProjectID, plan.Basis.Target.OwnerNodeID, []string{state.Repositories["repo0"]})
	if err != nil {
		t.Fatal(err)
	}
	if authority.CanWrite {
		t.Fatal("foreign reference retained revoked read authority")
	}
	if _, err = service.Apply(t.Context(), p, realApplyRequest(plan, secondRoot)); err == nil {
		t.Fatal("foreign repository authorization was omitted")
	}
	if _, err = db.Exec(`UPDATE projects.project_memberships SET status='active' WHERE project_id=$1 AND actor_id=$2`, first.Target.ProjectID, p.ActorID); err != nil {
		t.Fatal(err)
	}
	plan, err = service.Plan(t.Context(), p, pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: secondRoot})
	if err != nil {
		t.Fatal(err)
	}
	request := realApplyRequest(plan, secondRoot)
	request.IdempotencyKey = "foreign-reference"
	done, err := service.Apply(t.Context(), p, request)
	if err != nil || done.State != pc.DeclarationOperationSucceeded {
		t.Fatalf("authorized reference: %+v %v", done, err)
	}
	loaded, err := resolver.load(t.Context(), p, secondRoot, "")
	if err != nil || loaded.State.Repositories["shared"] != state.Repositories["repo0"] {
		t.Fatalf("reference identity=%+v %v", loaded.State, err)
	}
}
