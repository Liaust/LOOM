package storagearchive

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
)

type retirementActivation struct{ changed bool }

func (s retirementActivation) Deactivate(context.Context, requestctx.Context, string, projects.DeactivateProjectInput) (projects.ProjectDeactivationResult, error) {
	panic("unexpected effectful deactivation")
}
func (s retirementActivation) DeactivateForPhysicalArchive(_ context.Context, _ requestctx.Context, _ string, in projects.DeactivateProjectInput) (projects.ProjectDeactivationResult, error) {
	return projects.ProjectDeactivationResult{DryRun: in.DryRun, Facet: in.Facet, Changed: s.changed}, nil
}

type retirementProjectOwner struct {
	*projectArchivePlanProjectService
	commits int
}

func (s *retirementProjectOwner) RetireProjectRegistration(_ context.Context, _ requestctx.Context, in projects.ProjectRegistrationRetirementInput) (projects.Project, error) {
	s.commits++
	s.detail.Project.Project.Status = "archived"
	s.detail.Project.Project.ArchiveState = in.State
	s.detail.Registration.RegistrationStatus = projects.ProjectRegistrationStatusArchived
	return s.detail.Project.Project, nil
}

func retirementEnvironment(t *testing.T) (ProjectRuntimeService, *retirementProjectOwner, requestctx.Context, string) {
	t.Helper()
	env, _, req := newProjectSurfaceEnvironment(t)
	old := filepath.Join(env.service.WorkspaceRoots.BoxRoot, "Projects", ".loom-acceptance", "canonical-many")
	retained := filepath.Join(filepath.Dir(env.service.WorkspaceRoots.BoxRoot), ".retired-fixtures", "kept-project")
	if err := os.MkdirAll(filepath.Dir(retained), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(env.canonicalRoot, retained); err != nil {
		t.Fatal(err)
	}
	r := env.projects.detail.Registration
	rel, err := filepath.Rel(r.ProjectRoot, r.ContractPath)
	if err != nil {
		t.Fatal(err)
	}
	r.ProjectRoot = old
	r.ContractPath = filepath.Join(old, rel)
	r.ActivationStatus = projects.ProjectActivationStatusInactive
	owner := &retirementProjectOwner{projectArchivePlanProjectService: env.projects}
	env.service.Projects = owner
	env.service.Activation = retirementActivation{}
	return env.service, owner, req, retained
}

func TestRegistrationRetirementPreservesRetainedPayloadAndTruthfulHistory(t *testing.T) {
	s, owner, req, root := retirementEnvironment(t)
	in := ProjectRetirementInput{RetainedRoot: root, Reason: "finish legacy fixture retirement"}
	p, err := s.ReviewProjectRegistrationRetirement(context.Background(), req, "canonical-many", in)
	if err != nil {
		t.Fatal(err)
	}
	if owner.commits != 0 || p.InventoryEntries < 1 {
		t.Fatal("review mutated lifecycle or omitted inventory")
	}
	raw, _ := json.Marshal(p)
	if err = json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	result, err := s.ApplyProjectRegistrationRetirement(context.Background(), req, p.ProjectID, ProjectRetirementApply{Plan: p, PlanDigest: p.PlanDigest, Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if json.Unmarshal(result.ArchiveState, &state) != nil || state["evidence_kind"] != "retained_registration_disposition" || state["safe_to_delete"] != false || result.Status != "archived" || owner.commits != 1 {
		t.Fatalf("untruthful retirement: %s", result.ArchiveState)
	}
	if _, err = os.Stat(root + "/README.md"); err != nil {
		t.Fatal("retained payload changed", err)
	}
}

func TestRegistrationRetirementSupportsRegistryAssignedV03IdentityOnly(t *testing.T) {
	for _, schema := range []string{"project.contract.v0.3", "project.contract.v0.5"} {
		t.Run(schema, func(t *testing.T) {
			s, owner, req, root := retirementEnvironment(t)
			r := owner.detail.Registration
			var contract map[string]any
			if err := json.Unmarshal(r.Contract, &contract); err != nil {
				t.Fatal(err)
			}
			delete(contract["project"].(map[string]any), "id")
			contract["schema_version"] = schema
			r.Contract, _ = json.Marshal(contract)
			r.ContractSchemaVersion = schema
			// The contract owner authenticates bytes; JSON is also valid YAML.
			rel, _ := filepath.Rel(r.ProjectRoot, r.ContractPath)
			if err := os.WriteFile(filepath.Join(root, rel), r.Contract, 0600); err != nil {
				t.Fatal(err)
			}
			r.ContractHash = sha256Hex(r.Contract)
			_, err := s.ReviewProjectRegistrationRetirement(context.Background(), req, "canonical-many", ProjectRetirementInput{RetainedRoot: root, Reason: "retire old registry-assigned identity"})
			if (err == nil) != (schema == "project.contract.v0.3") {
				t.Fatalf("legacy ID rule: %v", err)
			}
		})
	}
}

func TestRegistrationRetirementRefusesStaleOrUntrustedInput(t *testing.T) {
	for _, change := range []string{"file", "contract", "original_reappears", "symlink", "active_runtime", "active_registration", "caller", "digest", "confirmation", "normal_project"} {
		t.Run(change, func(t *testing.T) {
			s, owner, req, root := retirementEnvironment(t)
			p, err := s.ReviewProjectRegistrationRetirement(context.Background(), req, "canonical-many", ProjectRetirementInput{RetainedRoot: root, Reason: "retire fixture"})
			if err != nil {
				t.Fatal(err)
			}
			in := ProjectRetirementApply{Plan: p, PlanDigest: p.PlanDigest, Confirm: true}
			switch change {
			case "file":
				err = os.WriteFile(root+"/README.md", []byte("new content"), 0600)
			case "contract":
				rel, _ := filepath.Rel(owner.detail.Registration.ProjectRoot, owner.detail.Registration.ContractPath)
				err = os.WriteFile(filepath.Join(root, rel), []byte("wrong contract"), 0600)
			case "original_reappears":
				err = os.MkdirAll(owner.detail.Registration.ProjectRoot, 0700)
			case "symlink":
				err = os.Rename(root, root+"-kept")
				if err == nil {
					err = os.Symlink(root+"-kept", root)
				}
			case "active_runtime":
				s.Activation = retirementActivation{changed: true}
			case "active_registration":
				owner.detail.Registration.ActivationStatus = "unrecognised"
			case "caller":
				req.ActorID += "other"
			case "digest":
				in.PlanDigest += "bad"
			case "confirmation":
				in.Confirm = false
			case "normal_project":
				owner.detail.Registration.ProjectRoot = filepath.Join(s.WorkspaceRoots.BoxRoot, "Projects", "real-project")
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.ApplyProjectRegistrationRetirement(context.Background(), req, p.ProjectID, in); err == nil || owner.commits != 0 {
				t.Fatalf("unsafe retirement accepted: %v", err)
			}
		})
	}
}
