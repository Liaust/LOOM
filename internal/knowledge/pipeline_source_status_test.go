package knowledge

import (
	"context"
	"encoding/json"
	"testing"
)

func TestPipelineSourceStatusReasonsAndReadability(t *testing.T) {
	base := pipelineSourceEvidence{Exists: true, ClaimEligible: true, RootActive: true, ExclusionAllowed: true,
		RevisionCurrent: true, Visible: true, Present: true, BodyOwner: true, Writable: true, Readable: true,
		RootKind: RootKindBoxNotes, RelativePath: "historical/source.md"}
	for _, tc := range []struct {
		name, reason string
		change       func(*pipelineSourceEvidence)
		readable     bool
	}{
		{"eligible", "eligible", func(*pipelineSourceEvidence) {}, true},
		{"missing", "source_object_unavailable", func(e *pipelineSourceEvidence) { e.Exists = false }, false},
		{"withdrawn", "source_withdrawn", func(e *pipelineSourceEvidence) { e.Deleted = true }, false},
		{"paused", "custody_or_project_paused", func(e *pipelineSourceEvidence) { e.Writable = false }, true},
		{"inactive", "source_root_inactive", func(e *pipelineSourceEvidence) { e.RootActive = false }, true},
		{"excluded", "excluded_by_current_declaration", func(e *pipelineSourceEvidence) {
			e.RootMetadata = json.RawMessage(`{"registration_metadata":{"knowledge_source":{"include":["**/*.md"],"exclude":["historical/**"]}}}`)
		}, false},
		{"malformed is not positive exclusion", "source_admission_evidence_not_current", func(e *pipelineSourceEvidence) {
			e.Visible = false
			e.RootMetadata = json.RawMessage(`{"registration_metadata":{"knowledge_source":{"include":true}}}`)
		}, false},
		{"unverified exclusion", "source_admission_evidence_not_current", func(e *pipelineSourceEvidence) {
			e.Visible, e.ExclusionAllowed = false, false
			e.RootMetadata = json.RawMessage(`{"registration_metadata":{"knowledge_source":{"include":["other/**"]}}}`)
		}, false},
		{"presence", "source_presence_unavailable", func(e *pipelineSourceEvidence) { e.Present = false }, true},
		{"revision", "source_revision_changed", func(e *pipelineSourceEvidence) { e.RevisionCurrent = false }, true},
		{"admission", "source_admission_evidence_not_current", func(e *pipelineSourceEvidence) { e.Visible = false }, true},
		{"canonical owner", "body_owned_by_another_source", func(e *pipelineSourceEvidence) { e.BodyOwner = false }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := base
			tc.change(&e)
			if tc.name != "eligible" {
				e.ClaimEligible = false
			}
			out := pipelineSourceStatus(e)
			if out.Reason != tc.reason || out.ClaimSourceEligible == nil || *out.ClaimSourceEligible != e.ClaimEligible || out.Readable == nil || *out.Readable != tc.readable {
				t.Fatalf("unexpected source status: %+v", out)
			}
			if !tc.readable && out.RelativePath != "" {
				t.Fatal("hidden path disclosed")
			}
		})
	}
	// Eligibility comes from the claim owner, not explanatory legacy fields.
	legacy := base
	legacy.RootActive = false
	if out := pipelineSourceStatus(legacy); !*out.ClaimSourceEligible || out.Reason != "eligible" {
		t.Fatalf("reader invented an eligibility policy: %+v", out)
	}
	for _, path := range []string{"credentials/password.md", ".codex/session.md", "private_no_index/secret.md", "loom.notes.yaml"} {
		e := base
		e.RelativePath = path
		if out := pipelineSourceStatus(e); *out.Readable || out.RelativePath != "" {
			t.Fatalf("private/control path disclosed: %s", path)
		}
	}
}

func TestPipelineSourceStatusesBoundsBeforeDatabaseAccess(t *testing.T) {
	s := &Service{}
	if got, err := s.pipelineSourceStatuses(context.Background(), nil); err != nil || len(got) != 0 {
		t.Fatalf("empty window: %v %v", got, err)
	}
	if _, err := s.pipelineSourceStatuses(context.Background(), make([]string, pipelineProgressLimit+1)); err == nil {
		t.Fatal("unbounded source query accepted")
	}
}
