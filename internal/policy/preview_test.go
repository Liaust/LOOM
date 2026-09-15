package policy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPolicyPreviewOptionalFacts(t *testing.T) {
	raw, err := json.Marshal(PolicyPreview{Decision: DecisionDeny, ReasonCode: "actor_inactive"})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"actor_id", "origin_node_id", "target_node_id", "scope_id", "endpoint_id", "required_level", "actor_level", "grant_ref", "grant_expires_at", "policy_decision_id", "approval_id"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("fabricated optional fact: %s", raw)
		}
	}
	var input PreviewInput
	d := json.NewDecoder(strings.NewReader(`{"operation":"capability:target@system.read","actor_ref":"privileged"}`))
	d.DisallowUnknownFields()
	if d.Decode(&input) == nil {
		t.Fatal("preview accepted a caller identity claim")
	}
}
