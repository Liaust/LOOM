package bootstrap

import (
	"strings"
	"testing"

	"loom.local/loom/internal/nodeprofiles"
)

func TestDefaultProductionInputValidates(t *testing.T) {
	input := DefaultProductionInput()
	if err := ValidateProductionInput(input); err != nil {
		t.Fatalf("ValidateProductionInput returned error: %v", err)
	}
	if input.NodeKind != "main" || input.RuntimeClass != nodeprofiles.RuntimeMainFull {
		t.Fatalf("default node profile = %s/%s", input.NodeKind, input.RuntimeClass)
	}
	if input.AuthorityProfileKey != nodeprofiles.AuthorityMainNodeDefault || input.RuntimeProfileKey != nodeprofiles.RuntimeMainFull {
		t.Fatalf("default profiles = %s/%s", input.AuthorityProfileKey, input.RuntimeProfileKey)
	}
}

func TestValidateProductionInputRejectsMissingNodeKey(t *testing.T) {
	input := DefaultProductionInput()
	input.NodeKey = ""

	err := ValidateProductionInput(input)
	if err == nil {
		t.Fatal("ValidateProductionInput accepted missing node key")
	}
	if !strings.Contains(err.Error(), "node_key") {
		t.Fatalf("error = %q, want node_key", err)
	}
}

func TestValidateProductionInputRejectsNonMainNodeKind(t *testing.T) {
	input := DefaultProductionInput()
	input.NodeKind = "workspace"

	err := ValidateProductionInput(input)
	if err == nil {
		t.Fatal("ValidateProductionInput accepted workspace node kind")
	}
	if !strings.Contains(err.Error(), "node_kind main") {
		t.Fatalf("error = %q", err)
	}
}

func TestValidateProductionInputRejectsIncompatibleRuntime(t *testing.T) {
	input := DefaultProductionInput()
	input.RuntimeClass = nodeprofiles.RuntimeWorkspaceFull

	err := ValidateProductionInput(input)
	if err == nil {
		t.Fatal("ValidateProductionInput accepted workspace runtime")
	}
	if !strings.Contains(err.Error(), "runtime_class main_full") {
		t.Fatalf("error = %q", err)
	}
}

func TestNormalizeProductionInputDerivesCustomScopeKeys(t *testing.T) {
	input := NormalizeProductionInput(ProductionInput{
		NodeKey:       "main hardware",
		OwnerActorKey: "person:leo",
	})

	if input.NodeScopeKey != "node-main-hardware" {
		t.Fatalf("NodeScopeKey = %q", input.NodeScopeKey)
	}
	if input.NodeScopeSlug != "node-main-hardware" {
		t.Fatalf("NodeScopeSlug = %q", input.NodeScopeSlug)
	}
	if input.OwnerScopeKey != "actor-person-leo" {
		t.Fatalf("OwnerScopeKey = %q", input.OwnerScopeKey)
	}
	if input.OwnerScopeSlug != "actor-person-leo" {
		t.Fatalf("OwnerScopeSlug = %q", input.OwnerScopeSlug)
	}
}

func TestProductionStatusInputFromProductionInputPreservesRefs(t *testing.T) {
	input := NormalizeProductionInput(ProductionInput{
		NodeKey:             "main-prod",
		OwnerActorKey:       "person:leo",
		AuthorityProfileKey: nodeprofiles.AuthorityMainNodeDefault,
		RuntimeProfileKey:   nodeprofiles.RuntimeMainFull,
	})

	statusInput := ProductionStatusInputFromProductionInput(input)
	if statusInput.NodeKey != "main-prod" {
		t.Fatalf("NodeKey = %q", statusInput.NodeKey)
	}
	if statusInput.NodeScopeKey != "node-main-prod" {
		t.Fatalf("NodeScopeKey = %q", statusInput.NodeScopeKey)
	}
	if statusInput.OwnerScopeKey != "actor-person-leo" {
		t.Fatalf("OwnerScopeKey = %q", statusInput.OwnerScopeKey)
	}
}

func TestProductionBootstrapPayloadRecordsProductionMode(t *testing.T) {
	input := NormalizeProductionInput(ProductionInput{
		NodeKey:   "main-prod",
		InstallID: "install_test",
		PlanHash:  "sha256:test",
		Metadata:  map[string]any{"source": "test"},
	})

	payload := productionBootstrapPayload(input)
	for key, want := range map[string]any{
		"mode":              "production",
		"node_key":          "main-prod",
		"node_kind":         "main",
		"node_role":         "main",
		"runtime_class":     nodeprofiles.RuntimeMainFull,
		"setup_install_id":  "install_test",
		"setup_plan_hash":   "sha256:test",
		"owner_actor_key":   "owner",
		"service_actor_key": "service:loomd",
		"system_scope_key":  "system",
	} {
		if got := payload[key]; got != want {
			t.Fatalf("payload[%q] = %#v, want %#v", key, got, want)
		}
	}
}
