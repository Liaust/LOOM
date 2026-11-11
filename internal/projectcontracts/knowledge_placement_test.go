package projectcontracts

import (
	"encoding/json"
	"testing"
)

func TestKnowledgeWorkspacePlacementPolicyRoundTrip(t *testing.T) {
	policy, err := NormalizeKnowledgeSourcePolicy(KnowledgeDeclaration{Workspace: &KnowledgeWorkspacePolicy{Path: "Projects/Superdeterminism/Research", Include: []string{"paper/manuscript.pdf"}}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(policy)
	again, err := DecodeKnowledgeSourcePolicy(raw)
	if err != nil || again.Workspace.Path != policy.Workspace.Path || len(again.Workspace.Include) != 1 {
		t.Fatalf("placement policy lost: %s %v", raw, err)
	}
	for _, bad := range []string{"Projects/P", "Notes/Other/Research", "Projects/_Passive/P/Research", "Projects/P/../Other", "Projects/P/.private", "Projects/P/x:y", "Projects/P/end ", "Projects/P/credentials/x", "Projects/P/line\nname", "Projects/P/cafe\u0301"} {
		if _, err := NormalizeKnowledgeSourcePolicy(KnowledgeDeclaration{Workspace: &KnowledgeWorkspacePolicy{Path: bad}}); err == nil {
			t.Fatalf("unsafe placement accepted: %q", bad)
		}
	}
}
