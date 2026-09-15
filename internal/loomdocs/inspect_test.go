package loomdocs

import (
	"errors"
	"strings"
	"testing"
)

func TestInspectHeadingAndCharacterBound(t *testing.T) {
	corpus := loadFixtureCorpus(t)
	inspection, err := Inspect(corpus, "projects.md", "Project Lifecycle", 90)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(inspection.Content, "## Project Lifecycle") || strings.Contains(inspection.Content, "## Archival") {
		t.Fatalf("unexpected heading content: %q", inspection.Content)
	}
	if !inspection.Truncated || len([]rune(inspection.Content)) != 90 {
		t.Fatalf("unexpected bound: truncated=%v runes=%d", inspection.Truncated, len([]rune(inspection.Content)))
	}
}

func TestInspectNotFoundAndRelated(t *testing.T) {
	corpus := loadFixtureCorpus(t)
	if _, err := Inspect(corpus, "missing", "", 100); err == nil {
		t.Fatal("expected missing target error")
	} else {
		var notFound *TargetNotFoundError
		if !errors.As(err, &notFound) {
			t.Fatalf("error = %T %v", err, err)
		}
	}
	related, err := Related(corpus, "Projects And Scopes")
	if err != nil {
		t.Fatal(err)
	}
	if len(related) != 2 || related[0].RelativePath != "README.md" || related[1].RelativePath != "storage.md" {
		t.Fatalf("related = %#v", related)
	}
}
