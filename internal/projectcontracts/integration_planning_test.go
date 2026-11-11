package projectcontracts

import (
	"strings"
	"testing"
)

func TestScaffoldIncludesIntegrationBeforeInfrastructure(t *testing.T) {
	result, err := ScaffoldProject(ScaffoldOptions{
		Name: "Integration First", OwnerNode: "main", Preset: PresetAutomation, Directory: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Join(strings.Fields(readFile(t, result.ProjectRoot, "AGENTS.md")), " ")
	for _, want := range []string{"Before creating a repository/project", "plan its fit with deployed LOOM first", "owning node/paths", "actual build support", "existing plan, not another approval document", "small edits need no new ceremony", "ordinary application code portable", "Report substantial LOOM friction or suspected unsupported features with evidence in the project and Basecamp", "Continue independent authorized work"} {
		if !strings.Contains(body, want) {
			t.Errorf("scaffold lost integration planning rule %q", want)
		}
	}
}
