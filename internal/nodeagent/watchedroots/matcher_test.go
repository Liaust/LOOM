package watchedroots

import "testing"

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
