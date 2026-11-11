package projectapply

import (
	"context"
	"encoding/json"
	"testing"

	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
)

func TestProjectCreateRegistrationOnlyBoundary(t *testing.T) {
	created, err := pc.ScaffoldProject(pc.ScaffoldOptions{Name: "Created", Slug: "created", OwnerNode: "main", Directory: t.TempDir(), Mode: pc.ScaffoldModeDeclaration})
	if err != nil {
		t.Fatal(err)
	}
	guard := createdProjectResolver{projectID: created.ProjectID, root: created.ProjectRoot}
	if err := guard.source(); err != nil {
		t.Fatal(err)
	}
	payload := projects.DeclarationRegistrationPayload{Project: projects.ProjectContractProjectInput{ID: created.ProjectID}, ProjectRoot: created.ProjectRoot, Repositories: []projects.ProjectRepositoryValidatedMember{}}
	// Only boundary fields are relevant here; normal plan validation separately
	// validates the complete owner payload and current authority.
	raw, _ := json.Marshal(payload)
	x := Resolution{Plan: pc.DeclarationPlan{Basis: pc.DeclarationPlanBasis{Target: pc.DeclarationTarget{ProjectID: created.ProjectID, ProjectRoot: created.ProjectRoot}, Bindings: map[pc.ResourceKey]pc.DeclarationBinding{}, Actions: []pc.DeclarationAction{{ID: "register_project:project", Kind: pc.DeclarationRegisterProject, Owner: pc.DeclarationOwnerProjects, TargetRef: created.ProjectID}}}}, Payloads: map[string]json.RawMessage{"register_project:project": raw}}
	if err := guard.resolution(x); err != nil {
		t.Fatal(err)
	}
	x.Plan.Basis.Bindings["files"] = pc.DeclarationBinding{Kind: pc.DeclarationKnowledge}
	if err := guard.resolution(x); err != nil {
		t.Fatalf("default files binding is not an activation: %v", err)
	}
	guard.Resolver = &staticResolver{x: x}
	resolved, err := guard.Resolve(context.Background(), Principal{}, pc.DeclarationPlanRequest{})
	if err != nil || len(resolved.Plan.Basis.Actions) != 1 || len(resolved.Plan.Basis.Bindings) != 1 {
		t.Fatalf("default constructor resolver: %+v %v", resolved, err)
	}
	current, err := guard.Current(context.Background(), Principal{}, resolved)
	if err != nil || current.Bindings["files"].Kind != pc.DeclarationKnowledge {
		t.Fatalf("default binding must remain in the prerequisite fence: %+v %v", current, err)
	}
	x.Plan.Basis.Bindings["files"] = pc.DeclarationBinding{Kind: pc.DeclarationKnowledge, OwnerRef: "unexpected-owner"}
	if err := guard.resolution(x); err == nil {
		t.Fatal("create accepted a bound default resource")
	}
	x.Plan.Basis.Bindings["files"] = pc.DeclarationBinding{Kind: pc.DeclarationKnowledge}
	x.Plan.Basis.Bindings["another"] = pc.DeclarationBinding{Kind: pc.DeclarationKnowledge}
	if err := guard.resolution(x); err == nil {
		t.Fatal("create accepted an extra resource binding")
	}
	delete(x.Plan.Basis.Bindings, "another")
	x.Plan.Basis.Actions = append(x.Plan.Basis.Actions, pc.DeclarationAction{ID: "activate"})
	if err := guard.resolution(x); err == nil {
		t.Fatal("create accepted resource activation")
	}
	x.Plan.Basis.Actions = x.Plan.Basis.Actions[:1]
	x.Plan.Basis.Target.ProjectID = "another-project"
	if err := guard.resolution(x); err == nil {
		t.Fatal("create accepted another identity")
	}
}
