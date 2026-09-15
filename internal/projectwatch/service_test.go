package projectwatch

import (
	"context"
	"strings"
	"testing"

	"loom.local/loom/internal/projectcontracts"
)

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
