package agentpack

import (
	"strings"
	"testing"
)

func TestIntegrationPlanningPrecedesInfrastructureForAllAgentEntries(t *testing.T) {
	for _, path := range []string{
		"host-instructions/codex/AGENTS.md",
		"templates/mina/AGENTS.md",
		"templates/project/AGENTS.md",
	} {
		t.Run(path, func(t *testing.T) {
			body := strings.Join(strings.Fields(projectProtocolRead(t, path)), " ")
			for _, want := range []string{"Before creating a repository/project", "plan its fit with deployed LOOM first", "existing plan", "small edits need no new ceremony", "Report substantial LOOM friction or suspected unsupported features with evidence", "Basecamp", "Continue independent authorized work"} {
				if !strings.Contains(body, want) {
					t.Errorf("entry instructions lost integration-first rule %q", want)
				}
			}
		})
	}
	skill := projectProtocolRead(t, "skills/manage-loom-projects/SKILL.md")
	if !strings.Contains(skill, "references/integration-planning.md") {
		t.Fatal("project skill does not route to integration planning")
	}
	reference := strings.Join(strings.Fields(projectProtocolRead(t, "skills/manage-loom-projects/references/integration-planning.md")), " ")
	for _, want := range []string{"before implementing that infrastructure", "Basecamp owns task state", "Provenance owns qualified durable meaning", "Planned is not deployed", "portable application code", "real runtime owner", "actual-use check", "Handle A Real Gap At Its Owner", "A separate integrator is not automatically required", "force a language change", "creating another document"} {
		if !strings.Contains(reference, want) {
			t.Errorf("integration reference lost %q", want)
		}
	}
	for _, want := range []string{"Report Substantial Friction", "owning project's existing issue/handoff area", "add it to Basecamp", "even when the agent fixes the problem", "Do not call a feature impossible merely because a command failed", "Expected versus observed", "remaining unknowns", "smallest proposed platform/protocol fix", "Search the relevant approved Basecamp destination first", "self-contained summary", "blocked, mitigated or fixed", "outstanding Basecamp capture"} {
		if !strings.Contains(reference, want) {
			t.Errorf("integration reference lost friction-reporting requirement %q", want)
		}
	}
}
