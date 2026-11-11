package projectapply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/config"
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

func TestProjectsOwnerRefreshesCatalogServiceBeforeObserveAndApply(t *testing.T) {
	id := "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	call := ActionCall{Action: pc.DeclarationAction{Kind: pc.DeclarationRegisterProject, Owner: pc.DeclarationOwnerProjects, ID: "register_project:project", TargetRef: id}}
	call.Payload, _ = json.Marshal(projects.DeclarationRegistrationPayload{Project: projects.ProjectContractProjectInput{ID: id}, ProjectRoot: "/fixture/project", ContractPath: "/fixture/project/.loom/project.yaml", ContractHash: "sha256:" + strings.Repeat("a", 64), Repositories: []projects.ProjectRepositoryValidatedMember{}})
	calls := 0
	want := errors.New("configured catalog changed")
	owner := ProjectsOwner{CurrentProjects: func() (projects.Service, error) { calls++; return projects.Service{}, want }}
	for _, run := range []func(context.Context, ActionCall) (Observation, error){owner.Observe, owner.Apply} {
		if _, err := run(t.Context(), call); !errors.Is(err, want) {
			t.Fatalf("service failure was bypassed before replay/write: %v", err)
		}
	}
	if calls != 2 {
		t.Fatalf("catalog service not resolved for both phases: %d", calls)
	}
}

func TestProjectsOwnerRejectsForeignBoxBeforeReceiptOrApply(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{NodeID: "main", BoxPath: filepath.Join(base, "personal"), BoxesFile: filepath.Join(base, "boxes.json"), DataDir: filepath.Join(base, "data"), StorageRoot: filepath.Join(base, "storage"), BoxStateRoot: filepath.Join(base, "state")}
	doc := boxregistry.Document{SchemaVersion: boxregistry.SchemaVersion, DefaultBox: "personal"}
	for _, key := range []string{"personal", "company"} {
		root := filepath.Join(base, key)
		for _, path := range []string{filepath.Join(root, ".loom"), filepath.Join(root, "Projects", "same")} {
			if err := os.MkdirAll(path, 0700); err != nil {
				t.Fatal(err)
			}
		}
		raw, _ := json.Marshal(map[string]string{"schema_version": "loom.box.v0.5", "box_id": "box_" + key, "root_path": root, "owner_node": "main"})
		if err := os.WriteFile(filepath.Join(root, ".loom", "box.yaml"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		doc.Boxes = append(doc.Boxes, boxregistry.Entry{Key: key, Label: key, Node: "main", RootPath: root})
	}
	raw, _ := json.Marshal(doc)
	if err := os.WriteFile(cfg.BoxesFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	id := "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	root := filepath.Join(cfg.BoxPath, "Projects", "same")
	call := ActionCall{Action: pc.DeclarationAction{Kind: pc.DeclarationRegisterProject, Owner: pc.DeclarationOwnerProjects, ID: "register_project:project", TargetRef: id}, Target: pc.DeclarationTarget{ProjectID: id, ProjectRoot: root, OwnerNodeID: "node_main"}}
	call.Payload, _ = json.Marshal(projects.DeclarationRegistrationPayload{Project: projects.ProjectContractProjectInput{ID: id}, ProjectRoot: root, ContractPath: filepath.Join(root, ".loom", "project.yaml"), ContractHash: "sha256:" + strings.Repeat("a", 64), Repositories: []projects.ProjectRepositoryValidatedMember{}})
	owner := ProjectsOwner{Resolver: NewLocalResolver(nil, func() (config.Config, error) { return cfg, nil })}
	ctx := boxregistry.WithSelection(t.Context(), boxregistry.ResolvedSelection{BoxIDs: []string{"box_company"}})
	for _, run := range []func(context.Context, ActionCall) (Observation, error){owner.Observe, owner.Apply} {
		_, err := run(ctx, call)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Cause != "project_outside_selected_box" {
			t.Fatalf("scope guard did not precede receipt/database use: %v", err)
		}
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
