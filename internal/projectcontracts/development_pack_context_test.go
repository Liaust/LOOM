package projectcontracts

import (
	"os"
	"strings"
	"testing"
)

// Exercise the embedded pack that scaffolding ships, not installed workspaces.
func TestDevelopmentPackTaskCases(t *testing.T) {
	for _, harness := range []string{"CODEX", "ORCA"} {
		t.Run(harness, func(t *testing.T) {
			raw := developmentContextRead(t, harness+"_WORKFLOW.md")
			_, tail, ok := strings.Cut(raw, "## Ordinary edits\n")
			if !ok {
				t.Fatal("missing ordinary edit route")
			}
			body, _, _ := strings.Cut(tail, "\n## ")
			body = strings.Join(strings.Fields(body), " ")
			for _, term := range []string{"actual repository instructions", "appropriate tests", "concise handoff", "No new feature slices or formal file inventories", "optional `.repo/`"} {
				if !strings.Contains(body, term) {
					t.Errorf("small edit route lacks %q", term)
				}
			}
			for _, term := range []string{"exact accepted base", "status: review", "--apply", "Load the installed"} {
				if strings.Contains(body, term) {
					t.Errorf("mandatory ceremony in small edit route: %q", term)
				}
			}
			if !strings.Contains(raw, "## Planned features") {
				t.Error("lost planned feature route")
			}
			if strings.Index(raw, "## Ordinary edits") > strings.Index(raw, "## Planned features") {
				t.Error("formal feature route precedes ordinary edits")
			}
			for _, term := range []string{"accepted plan", "checkpoint", "integrator", "native"} {
				if !strings.Contains(raw, term) {
					t.Errorf("planned workflow lacks %q", term)
				}
			}
		})
	}
}

func TestDevelopmentPackOwnershipAndOptIn(t *testing.T) {
	for _, c := range []struct {
		file  string
		terms []string
	}{
		{"WORKTREE_OWNERSHIP.md", []string{"One harness owns each worktree", "running harness", "Codex model inside ORCA remains ORCA-owned", "Neither harness adopts", "Git branch", "never canonical repository state", "deliberately discarded by an authorized operator"}},
		{"REPOSITORY_STATE_PROTOCOL.md", []string{"optional", "Ordinary edits do not require a feature lifecycle", "Reading source does not register resources, refresh projections or accept semantic claims", "When a repository uses the feature lifecycle", "status: ready", "integrated", "private memory", "--plan-digest", "--apply", "--yes", "Unrelated dirty Git state blocks", "never stages or commits"}},
		{"CODEX_WORKFLOW.md", []string{"Codex model inside ORCA", "ORCA_WORKFLOW.md", "Codex-native", "`openai-docs`", "does not duplicate product commands"}},
		{"ORCA_WORKFLOW.md", []string{"Codex model inside ORCA remains ORCA-owned", "`orca-cli`", "`orchestration`", "version-matched guidance", "non-Git", "does not\nduplicate subcommands or flags"}},
	} {
		t.Run(c.file, func(t *testing.T) {
			raw := developmentContextRead(t, c.file)
			for _, term := range c.terms {
				if !strings.Contains(raw, term) && !strings.Contains(strings.Join(strings.Fields(raw), " "), term) {
					t.Errorf("missing boundary %q", term)
				}
			}
			for _, term := range []string{"/Users/", "/home/", "session_id:", "terminal_id:", "repo add --kind folder"} {
				if strings.Contains(raw, term) {
					t.Errorf("nonportable or unproven recipe %q", term)
				}
			}
		})
	}
}

func TestDevelopmentPackContextBudget(t *testing.T) {
	files := []string{"CODEX_WORKFLOW.md", "ORCA_WORKFLOW.md", "WORKTREE_OWNERSHIP.md", "REPOSITORY_STATE_PROTOCOL.md"}
	var words, bytes int
	for _, file := range files {
		raw := developmentContextRead(t, file)
		w := len(strings.Fields(raw))
		t.Logf("%s words=%d UTF-8 bytes=%d", file, w, len(raw))
		words += w
		bytes += len(raw)
	}
	if words > 1050 || bytes > 8200 {
		t.Errorf("pack protocols words=%d bytes=%d exceed 1050/8200", words, bytes)
	}
	// Full entry files, no section clipping: delegation + project skill + one harness + ownership.
	var entry string
	for _, file := range []string{"templates/mina/protocols/PROJECT-DELEGATION.md", "skills/manage-loom-projects/SKILL.md"} {
		raw, err := os.ReadFile("../../ai-loom-pack/" + file)
		if err != nil {
			t.Fatal(err)
		}
		entry += string(raw) + "\n"
	}
	for _, harness := range files[:2] {
		raw := entry + developmentContextRead(t, harness) + "\n" + developmentContextRead(t, "WORKTREE_OWNERSHIP.md")
		w := len(strings.Fields(raw))
		t.Logf("%s full entry words=%d UTF-8 bytes=%d budget=1250/9800", harness, w, len(raw))
		if w > 1250 || len(raw) > 9800 {
			t.Errorf("%s entry budget exceeded", harness)
		}
	}
}

func developmentContextRead(t *testing.T, file string) string {
	t.Helper()
	raw, err := repositoryDevelopmentPackTemplates.ReadFile("repo_development_pack/templates/.repo/protocols/" + file)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
