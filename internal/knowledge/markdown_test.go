package knowledge

import (
	"strings"
	"testing"
	"time"
)

func TestExtractMarkdownDocumentParsesFrontmatterAndHeadings(t *testing.T) {
	document := ExtractMarkdownDocument("---\ntitle: Daily Note\ntags:\n  - osint\n  - loom\n---\n# Daily\n\n## Leads\n\nBody text.\n")

	if document.Text == "" || document.Text[0] == '-' {
		t.Fatalf("document text = %q, want frontmatter removed", document.Text)
	}
	if got, _ := document.Frontmatter["title"].(string); got != "Daily Note" {
		t.Fatalf("frontmatter title = %#v, want Daily Note", document.Frontmatter["title"])
	}
	tags, ok := document.Frontmatter["tags"].([]any)
	if !ok || len(tags) != 2 {
		t.Fatalf("frontmatter tags = %#v, want two tags", document.Frontmatter["tags"])
	}
	if len(document.Headings) != 2 {
		t.Fatalf("headings len = %d, want 2: %#v", len(document.Headings), document.Headings)
	}
	if document.Headings[1].StructuralPath != "Daily / Leads" {
		t.Fatalf("second heading path = %q, want hierarchy", document.Headings[1].StructuralPath)
	}
	if len(document.Warnings) != 0 {
		t.Fatalf("warnings = %#v, want none", document.Warnings)
	}
}

func TestExtractMarkdownDocumentNormalizesExactAbsoluteTimeFields(t *testing.T) {
	document := ExtractMarkdownDocument("---\ncreated_at: 2020-01-02T03:04:05Z\nupdated_at: 2021-02-03T06:05:06+02:00\ndate: 1999-01-01\n---\nBody\n")

	if len(document.AbsoluteTimeCandidates) != 2 {
		t.Fatalf("absolute time candidates = %#v, want created_at and updated_at only", document.AbsoluteTimeCandidates)
	}
	modified := document.AbsoluteTimeCandidates[0]
	if modified.Basis != AbsoluteTimeBasisFrontmatterUpdatedAt || modified.Timestamp == nil ||
		modified.Timestamp.Format(time.RFC3339) != "2021-02-03T04:05:06Z" {
		t.Fatalf("modified candidate = %#v", modified)
	}
	if strings.Contains(string(mustJSON(t, document.AbsoluteTimeCandidates)), "1999-01-01") {
		t.Fatalf("generic date field was treated as source chronology: %#v", document.AbsoluteTimeCandidates)
	}
	if !strings.Contains(document.FrontmatterRaw, "updated_at: 2021-02-03T06:05:06+02:00") {
		t.Fatalf("frontmatter_raw = %q, want original timestamp", document.FrontmatterRaw)
	}
}

func TestExtractMarkdownDocumentPreservesInvalidAbsoluteTimeWarning(t *testing.T) {
	document := ExtractMarkdownDocument("---\ncreated_at: someday\nupdated_at: 07/04/2026\n---\nBody\n")

	if len(document.AbsoluteTimeCandidates) != 2 || len(document.AbsoluteTimeWarnings) != 2 {
		t.Fatalf("absolute time evidence = %#v / %#v", document.AbsoluteTimeCandidates, document.AbsoluteTimeWarnings)
	}
	if len(document.Warnings) != 2 || !strings.Contains(strings.Join(document.Warnings, " "), "07/04/2026") {
		t.Fatalf("warnings = %#v, want inspectable invalid raw values", document.Warnings)
	}
}

func TestExtractMarkdownDocumentInvalidFrontmatterWarnsAndKeepsText(t *testing.T) {
	input := "---\ntitle: [unterminated\n---\n# Body\n"
	document := ExtractMarkdownDocument(input)

	if document.Text != input {
		t.Fatalf("document text = %q, want invalid frontmatter retained", document.Text)
	}
	if len(document.Warnings) != 1 {
		t.Fatalf("warnings = %#v, want one invalid-frontmatter warning", document.Warnings)
	}
}
