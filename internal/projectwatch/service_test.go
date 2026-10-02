package projectwatch

import (
	"context"
	"strings"
	"testing"

	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
)

type declaredProjectStore struct{ projectStore }
type unusedNodeStore struct{ nodeStore }

func (declaredProjectStore) GetProjectRegistrationStatus(context.Context, string) (projects.ProjectRegistrationDetail, error) {
	return projects.ProjectRegistrationDetail{Registration: &projects.ProjectContractRegistration{
		ContractSchemaVersion: projects.ProjectRepositoryProjectSchemaV05,
	}}, nil
}

func TestLegacyWatchMutationCannotOverwriteDeclaredOwners(t *testing.T) {
	s := NewService(Deps{Projects: declaredProjectStore{}, Nodes: unusedNodeStore{}})
	_, err := s.ApplyDesiredState(context.Background(), requestctx.Context{}, "project", projects.ApplyProjectWatchPolicyInput{})
	if err == nil || !strings.Contains(err.Error(), "loom project apply") {
		t.Fatalf("expected declaration-owner guidance before mutation, got %v", err)
	}
}

func TestBuildPlanLocalProjectUsesCompiledWatchedRoots(t *testing.T) {
	result, err := projectcontracts.ScaffoldProject(projectcontracts.ScaffoldOptions{
		Name:      "Research Notes",
		Slug:      "research-notes",
		OwnerNode: "workspace",
		Preset:    projectcontracts.PresetResearch,
		Directory: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("ScaffoldProject returned error: %v", err)
	}

	plan, err := NewService(Deps{}).BuildPlan(context.Background(), result.ProjectRoot, BuildPlanInput{})
	if err != nil {
		t.Fatalf("BuildPlan returned error: %v", err)
	}
	if plan.ProjectRoot != result.ProjectRoot {
		t.Fatalf("ProjectRoot = %q, want %q", plan.ProjectRoot, result.ProjectRoot)
	}
	if plan.ContractHash == "" {
		t.Fatalf("expected contract hash")
	}
	if len(plan.WatchedRoots) != 2 {
		t.Fatalf("expected notes and project watched roots, got %#v", plan.WatchedRoots)
	}
	if len(plan.Commands) != 1 {
		t.Fatalf("expected one exact node-agent apply-plan recipe, got %#v", plan.Commands)
	}
	shell := plan.Commands[0].Shell
	if !strings.Contains(shell, "loom-node-agent watched-roots apply-plan /tmp/loom-project-watch-plan.json") || !strings.Contains(shell, "--project-root "+result.ProjectRoot) {
		t.Fatalf("expected exact watched-root apply-plan command, got %#v", plan.Commands)
	}
}
