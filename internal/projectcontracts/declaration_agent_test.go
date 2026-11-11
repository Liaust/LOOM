package projectcontracts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentDeclarationSourceAndScheduleBinding(t *testing.T) {
	raw := strings.Replace(string(fixtureRead(t, "minimal.yaml")), "resources: {}", `resources:
  researcher:
    kind: agent
    agent:
      instructions_ref: agent.md
      model: gemma3:4b
      provider: ollama
  review:
    kind: hermes_schedule
    hermes_schedule:
      profile: researcher
      every_minutes: 60
      prompt: Read the project notes and report locally.
`, 1)
	root := declarationTestRoot(t, []byte(raw))
	if a := Analyze(root); a.Report.OK {
		t.Fatal("missing instructions admitted")
	}
	if err := os.WriteFile(filepath.Join(root, "agent.md"), []byte("You are the project's researcher.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a := Analyze(root)
	if !a.Report.OK {
		t.Fatalf("%+v", a.Report)
	}
	d := a.Loaded.Declaration
	input, err := AgentInput(*d.Resources["researcher"].Agent, root, a.Plan.Declaration.Sources)
	if err != nil || input.Instructions != "You are the project's researcher.\n" || input.Workdir != root {
		t.Fatalf("%+v %v", input, err)
	}
	for _, replacement := range []string{"profile: missing", "profile: ../private"} {
		if _, err := ParseProjectDeclaration([]byte(strings.Replace(raw, "profile: researcher", replacement, 1))); err == nil {
			t.Fatal("unbound profile admitted")
		}
	}
	if AgentProfileID(d.Project.ID, "researcher") != AgentProfileID(d.Project.ID, "researcher") || AgentProfileID(d.Project.ID, "other") == AgentProfileID(d.Project.ID, "researcher") {
		t.Fatal("unstable identity")
	}
}
