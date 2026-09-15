package idempotency

import "testing"

func TestRequestHashIsStableForMapOrder(t *testing.T) {
	first, err := RequestHash(map[string]any{
		"operation": "project.create",
		"request": map[string]any{
			"slug": "demo",
			"name": "Demo",
		},
	})
	if err != nil {
		t.Fatalf("first hash failed: %v", err)
	}
	second, err := RequestHash(map[string]any{
		"request": map[string]any{
			"name": "Demo",
			"slug": "demo",
		},
		"operation": "project.create",
	})
	if err != nil {
		t.Fatalf("second hash failed: %v", err)
	}
	if first != second {
		t.Fatalf("hash mismatch: %s != %s", first, second)
	}
}

func TestRequestHashChangesWithOperation(t *testing.T) {
	request := map[string]any{"slug": "demo"}
	first, err := RequestHash(map[string]any{"operation": "project.create", "request": request})
	if err != nil {
		t.Fatal(err)
	}
	second, err := RequestHash(map[string]any{"operation": "scope.create", "request": request})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("hash should change when operation changes")
	}
}

func TestNormalizeBeginInputScopesActorNode(t *testing.T) {
	input := normalizeBeginInput(BeginInput{
		ActorID: "actor_1",
		NodeID:  "node_1",
		Key:     " key ",
	})
	if input.ScopeKind != "actor_node" {
		t.Fatalf("scope kind = %q, want actor_node", input.ScopeKind)
	}
	if input.ScopeRef != "actor_1@node_1" {
		t.Fatalf("scope ref = %q, want actor_1@node_1", input.ScopeRef)
	}
	if input.Key != "key" {
		t.Fatalf("key = %q, want key", input.Key)
	}
	if input.TTL <= 0 {
		t.Fatal("ttl should be defaulted")
	}
}
