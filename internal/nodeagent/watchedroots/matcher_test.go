package watchedroots

import "testing"

func TestMayMatchDescendant(t *testing.T) {
	for _, tc := range []struct {
		pattern, directory string
		want               bool
	}{
		{"paper/current.pdf", "paper", true},
		{"paper/current.pdf", "toolchain", false},
		{"paper/current.pdf", "paper-old", false},
		{"paper/current.pdf", "paper/current.pdf", false},
		{"runs/selected/COVERAGE.md", "runs", true},
		{"runs/selected/COVERAGE.md", "runs/selected/.lake", false},
		{"reports/20??/*.md", "reports/2026", true},
		{"reports/20??/*.md", "reports/old", false},
		{"**/*.md", "any/depth", true},
		{"paper/**/current.pdf", "paper/any/depth", true},
		{"paper/**/current.pdf", "other", false},
		{"notes", "notes", false},
		{"notes/**", "notes", true},
	} {
		t.Run(tc.pattern+":"+tc.directory, func(t *testing.T) {
			if got := mayMatchDescendant([]string{tc.pattern}, tc.directory); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
	if !mayMatchDescendant([]string{"other/file.md", "paper/current.pdf"}, "paper") {
		t.Fatal("later matching include was pruned")
	}
}

func TestMatchPatternSupportsDoubleStar(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"Project.md", "Projects/LOOM.md", "a/b/c.md"} {
		matched, err := MatchPattern("**/*.md", path)
		if err != nil {
			t.Fatalf("MatchPattern failed: %v", err)
		}
		if !matched {
			t.Fatalf("expected **/*.md to match %s", path)
		}
	}
}

func TestMatchPatternSupportsDirectoryPrefixes(t *testing.T) {
	t.Parallel()
	for _, path := range []string{".git", ".git/config", ".git/objects/pack"} {
		matched, err := MatchPattern(".git/**", path)
		if err != nil {
			t.Fatalf("MatchPattern failed: %v", err)
		}
		if !matched {
			t.Fatalf("expected .git/** to match %s", path)
		}
	}
}

func TestValidatePatternRejectsUnsafePatterns(t *testing.T) {
	t.Parallel()
	for _, pattern := range []string{"../secret", "/absolute", "["} {
		if err := validatePattern(pattern); err == nil {
			t.Fatalf("expected pattern %q to be rejected", pattern)
		}
	}
}
