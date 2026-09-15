package loomdocs

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestShippedProjectExportAndLoomignoreSemantics(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	staleClaims := []*regexp.Regexp{
		regexp.MustCompile(`project export.{0,160}(?:deferred|unavailable|not (?:implemented|available|supported))`),
		regexp.MustCompile(`(?:deferred|unavailable|not (?:implemented|available|supported)).{0,160}project export`),
		regexp.MustCompile(`\.loomignore.{0,160}(?:deferred|unavailable|not (?:implemented|available|supported))`),
		regexp.MustCompile(`(?:deferred|unavailable|not (?:implemented|available|supported)).{0,160}\.loomignore`),
		regexp.MustCompile(`export (?:is|are) not (?:a|an) portal action`),
	}
	for _, relativeRoot := range []string{"docs", "ai-loom-pack"} {
		root := filepath.Join(repositoryRoot, relativeRoot)
		err := filepath.WalkDir(root, func(pathValue string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(pathValue) != ".md" {
				return nil
			}
			payload, err := os.ReadFile(pathValue)
			if err != nil {
				return err
			}
			paragraphs := regexp.MustCompile(`\n\s*\n`).Split(strings.ToLower(string(payload)), -1)
			for _, paragraph := range paragraphs {
				normalized := strings.Join(strings.Fields(paragraph), " ")
				for _, pattern := range staleClaims {
					if claim := pattern.FindString(normalized); claim != "" {
						relative, _ := filepath.Rel(repositoryRoot, pathValue)
						t.Errorf("%s contains stale shipped-behavior claim %q", relative, claim)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	requireShippedSemantics(t, repositoryRoot, "docs/portal/projects.md", []string{
		"Export Project...",
		"Export Human (Local)",
		"Export Portable (From Main)",
		"Export Archival (From Main)",
		"caller-local",
		"main is offline",
		".loomignore",
		".loom/state/",
		"credential values",
	})
	requireShippedSemantics(t, repositoryRoot, "ai-loom-pack/skills/manage-loom-projects/SKILL.md", []string{
		"Export Project...",
		"Local",
		"From Main",
		"caller-local",
		"main is offline",
		".loomignore",
		".loom/state/",
		"credential values",
	})
}

func requireShippedSemantics(t *testing.T, repositoryRoot, relativePath string, required []string) {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join(repositoryRoot, filepath.FromSlash(relativePath)))
	if err != nil {
		t.Fatal(err)
	}
	content := string(payload)
	for _, value := range required {
		if !strings.Contains(content, value) {
			t.Errorf("%s is missing shipped-behavior semantic %q", relativePath, value)
		}
	}
}
