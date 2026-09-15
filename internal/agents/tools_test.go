package agents

import (
	"strings"
	"testing"

	"loom.local/loom/internal/capabilities"
)

func TestExtractUsageSectionsMatchesHeadingAndBoundsExcerpt(t *testing.T) {
	sections := extractUsageSections([]capabilities.UsageDocument{
		{
			CapabilityUsageDocumentID: "capability_usage_document_01",
			Title:                     "Status Usage",
			ReviewStatus:              capabilities.UsageReviewStatusApproved,
			Body: strings.Join([]string{
				"# Overview",
				"Use this for a general check.",
				"# Node Status",
				"Read status, health, and current runtime information for the node.",
			}, "\n"),
		},
	}, "node status", "", 2, 36)

	if len(sections) != 1 {
		t.Fatalf("expected one matched section, got %d", len(sections))
	}
	if sections[0].SectionHeading != "Node Status" {
		t.Fatalf("expected Node Status heading, got %q", sections[0].SectionHeading)
	}
	if sections[0].MatchReason != "heading" {
		t.Fatalf("expected heading match, got %q", sections[0].MatchReason)
	}
	if len(sections[0].Excerpt) > 36 {
		t.Fatalf("expected bounded excerpt, got %d chars: %q", len(sections[0].Excerpt), sections[0].Excerpt)
	}
}

func TestExtractUsageSectionsSkipsUnapprovedDocuments(t *testing.T) {
	sections := extractUsageSections([]capabilities.UsageDocument{
		{
			CapabilityUsageDocumentID: "capability_usage_document_01",
			Title:                     "Draft",
			ReviewStatus:              capabilities.UsageReviewStatusDraft,
			Body:                      "# Secret\nThis should not be returned.",
		},
	}, "secret", "", 2, 80)

	if len(sections) != 0 {
		t.Fatalf("expected no sections from unapproved document, got %d", len(sections))
	}
}
