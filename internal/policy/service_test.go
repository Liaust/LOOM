package policy

import (
	"encoding/json"
	"testing"

	"loom.local/loom/internal/requestctx"
)

func TestParsePolicyOperationSupportsCapabilityOperations(t *testing.T) {
	operation := parsePolicyOperation(" capability:main@system.status.read ")

	if !operation.Supported {
		t.Fatal("operation should be supported")
	}
	if operation.ResourceKind != resourceKindCapability {
		t.Fatalf("resource kind = %q, want %q", operation.ResourceKind, resourceKindCapability)
	}
	if operation.ResourceRef != "main@system.status.read" {
		t.Fatalf("resource ref = %q, want main@system.status.read", operation.ResourceRef)
	}
	if operation.Raw != "capability:main@system.status.read" {
		t.Fatalf("raw = %q, want normalized operation", operation.Raw)
	}
}

func TestParsePolicyOperationRejectsUnsupportedOperations(t *testing.T) {
	for _, raw := range []string{"", "main@system.status.read", "capability:"} {
		t.Run(raw, func(t *testing.T) {
			operation := parsePolicyOperation(raw)
			if operation.Supported {
				t.Fatalf("operation %q should not be supported", raw)
			}
			if operation.ResourceKind != resourceKindOperation {
				t.Fatalf("resource kind = %q, want %q", operation.ResourceKind, resourceKindOperation)
			}
		})
	}
}

func TestConstraintAllowsEmptyObjectsAndMatchingStrings(t *testing.T) {
	if !constraintAllows(json.RawMessage(`{}`), "node_1", "node_id") {
		t.Fatal("empty constraints should allow")
	}
	if !constraintAllows(json.RawMessage(`{"node_id":"node_1"}`), "node_1", "node_id") {
		t.Fatal("matching string constraint should allow")
	}
	if !constraintAllows(json.RawMessage(`{"node_ids":["node_1","node_2"]}`), "node_2", "node_ids") {
		t.Fatal("matching list constraint should allow")
	}
	if constraintAllows(json.RawMessage(`{"node_id":"node_1"}`), "node_2", "node_id") {
		t.Fatal("different string constraint should deny")
	}
	if constraintAllows(json.RawMessage(`{"unknown":"node_1"}`), "node_1", "node_id") {
		t.Fatal("unknown non-empty constraints should deny")
	}
}

// Explain still accepts its existing explicit inspection references. Preview
// does not grow this authority surface when sharing the evaluator.
func TestExplainContextSummaryPreservesExplicitReferences(t *testing.T) {
	input := DecisionInput{Operation: "unsupported", ActorRef: "inspected-actor", OriginNodeRef: "inspected-node", ScopeRef: "inspected-scope"}
	raw, hash, e := buildContextSummary(requestctx.Context{ActorKey: "caller", OriginNodeKey: "origin"}, input, parsePolicyOperation(input.Operation), nil, nil, nil, nil, nil, nil)
	if e != nil || hash == "" {
		t.Fatal(e)
	}
	var summary map[string]any
	if e = json.Unmarshal(raw, &summary); e != nil {
		t.Fatal(e)
	}
	if summary["actor_ref"] != "inspected-actor" || summary["origin_node_ref"] != "inspected-node" || summary["scope_ref"] != "inspected-scope" {
		t.Fatalf("Explain reference contract changed: %s", raw)
	}
}
