package agentpack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConversationHandoffGuidanceKeepsExactPointersAndOwners(t *testing.T) {
	path := filepath.Join("..", "..", "ai-loom-pack", "templates", "mina", "protocols", "PROJECT-DELEGATION.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{".project/CONVERSATIONS.md", "returned exact", "host, actual cwd and role", "Unknown stays unknown", "scheduled verification", "not transcript storage"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("missing handoff distinction %q", want)
		}
	}
}

func TestDelegatedConversationGuidanceSeparatesNewTasksAndReports(t *testing.T) {
	for _, file := range []string{
		"host-instructions/codex/AGENTS.md",
		"templates/mina/protocols/PROJECT-DELEGATION.md",
	} {
		t.Run(file, func(t *testing.T) {
			raw := strings.Join(strings.Fields(projectProtocolRead(t, file)), " ")
			for _, want := range []string{
				"MINA", "fresh chat", "remote MINA project", "personal", "bootstrap",
				"loom-codex-message task", "report", "tool output",
			} {
				if !strings.Contains(raw, want) {
					t.Errorf("missing delegated conversation distinction %q", want)
				}
			}
		})
	}
}
