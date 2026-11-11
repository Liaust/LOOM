package agentpack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoomAppGuidanceKeepsOriginalFilesAndDistinctAvailability(t *testing.T) {
	for _, name := range []string{
		"host-instructions/codex/AGENTS.md",
		"templates/mina/AGENTS.md",
		"templates/project/AGENTS.md",
		"skills/manage-loom-projects/references/notes-pipeline.md",
	} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", "ai-loom-pack", name))
			if err != nil {
				t.Fatal(err)
			}
			text := strings.Join(strings.Fields(string(data)), " ")
			for _, expected := range []string{"Loom", "canonical", "Box-relative", "Obsidian", "offline", "iPhone/iPad", "paus"} {
				if !strings.Contains(text, expected) {
					t.Errorf("missing app/source/availability guidance: %q", expected)
				}
			}
		})
	}
	for _, name := range []string{"WORKFLOW.md", "protocols/LOOM-ROUTING.md"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "ai-loom-pack/templates/mina", name))
		if err != nil {
			t.Fatal(err)
		}
		for _, expected := range []string{"Loom desktop", "Box-relative", "Obsidian", "protection"} {
			if !strings.Contains(string(data), expected) {
				t.Errorf("%s missing delivery reference %q", name, expected)
			}
		}
	}
}
