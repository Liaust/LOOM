package projectapply

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
)

func TestDeclarationRegistrationReceiptStableAcrossResume(t *testing.T) {
	x := testResolution(testPrincipal(), 1)
	key := pc.ResourceKey("code")
	x.Plan.Basis.Bindings[key] = pc.DeclarationBinding{Kind: pc.DeclarationRepository}
	desired := projects.DeclarationRegistrationPayload{Project: projects.ProjectContractProjectInput{ID: x.Plan.Basis.Target.ProjectID}, Repositories: []projects.ProjectRepositoryValidatedMember{{Key: string(key), Path: "code", Role: projects.ProjectRepositoryRoleComponent}}}
	raw, err := canonicalValue(desired)
	if err != nil {
		t.Fatal(err)
	}
	x.Payloads[x.Plan.Basis.Actions[0].ID] = raw
	call := ActionCall{Action: x.Plan.Basis.Actions[0], Payload: raw, Expected: prerequisites(x.Plan.Basis)}
	result := &projects.DeclarationOwnerResult{EffectRef: "registration", Before: projects.DeclarationProjectState{Repositories: map[string]string{}}, After: projects.DeclarationProjectState{Repositories: map[string]string{"code": "repo_01ARZ3NDEKTSV4RRFFQ69G5FAV"}}}
	first, err := projectReceipt(call, result)
	if err != nil {
		t.Fatal(err)
	}
	call.Expected.Bindings[key] = first.Bindings[key].After
	second, err := projectReceipt(call, result)
	if err != nil || !same(first, second) {
		t.Fatalf("owner evidence changed on resume: %v", err)
	}
}
func TestDeclarationRegistrationOwnerStrictPayload(t *testing.T) {
	owner := ProjectsOwner{}
	action := pc.DeclarationAction{Kind: pc.DeclarationRegisterProject, Owner: pc.DeclarationOwnerProjects, ID: "register_project:project", TargetRef: "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"}
	for _, raw := range []string{`{"project":{},"repositories":[],"allow":true}`, `{"project":{},"repositories":[],"repositories":[]}`, `{"project":{},"repositories":null}`} {
		if owner.Validate(context.Background(), action, []byte(raw)) == nil {
			t.Fatalf("invalid payload accepted: %s", raw)
		}
	}
}

func TestDeclarationCompatibilityProjectsOwnerStateRoot(t *testing.T) {
	action := pc.DeclarationAction{Kind: pc.DeclarationRegisterProject, Owner: pc.DeclarationOwnerProjects, ID: "register_project:project", TargetRef: "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"}
	for _, group := range []struct {
		valid bool
		roots []string
	}{
		{true, []string{"", ".repo", "development/state", "nested/.repo"}},
		{false, []string{".", "..", "../state", "a/../state", "./state", "state/", "a//state", " state", "state ", "/tmp/state", "C:/state", "a\\b", "a\n", "a\x00b", "a\u0085b", ".git", ".loom", ".project", "a/.GIT", "a/.LoOm/state", "a/.PROJECT"}},
	} {
		for _, state := range group.roots {
			t.Run(fmt.Sprintf("valid_%t_%q", group.valid, state), func(t *testing.T) {
				input := projects.DeclarationRegistrationPayload{
					ContractPath: "/fixture/.loom/project.yaml", ContractHash: "sha256:" + strings.Repeat("a", 64),
					Project:      projects.ProjectContractProjectInput{ID: action.TargetRef},
					Repositories: []projects.ProjectRepositoryValidatedMember{{Key: "api", Path: "workspace", Role: projects.ProjectRepositoryRolePrimary, StateRoot: state}},
				}
				raw, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				err = (ProjectsOwner{}).Validate(t.Context(), action, raw)
				if (err == nil) != group.valid {
					t.Fatalf("state_root=%q valid=%t: %v", state, group.valid, err)
				}
			})
		}
	}
}
