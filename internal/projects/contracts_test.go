package projects

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeRegisterProjectContractInputRejectsInvalidHash(t *testing.T) {
	input := validRegisterProjectContractInput()
	input.ContractHash = "sha256:nope"
	if _, err := normalizeRegisterProjectContractInput(input); err == nil {
		t.Fatal("expected invalid hash to fail")
	}
}

func TestNormalizeRegisterProjectContractInputRejectsNonObjectSnapshots(t *testing.T) {
	input := validRegisterProjectContractInput()
	input.Contract = json.RawMessage(`[]`)
	if _, err := normalizeRegisterProjectContractInput(input); err == nil || !strings.Contains(err.Error(), "contract") {
		t.Fatalf("expected contract object error, got %v", err)
	}

	input = validRegisterProjectContractInput()
	input.DerivedProviders = json.RawMessage(`{}`)
	if _, err := normalizeRegisterProjectContractInput(input); err == nil || !strings.Contains(err.Error(), "derived providers") {
		t.Fatalf("expected derived providers array error, got %v", err)
	}
}

func TestProjectFacetStatusMapping(t *testing.T) {
	tests := []struct {
		name  string
		input ProjectContractFacetInput
		want  string
	}{
		{name: "disabled", input: ProjectContractFacetInput{Enabled: false, Present: true}, want: ProjectFacetStatusDisabled},
		{name: "missing", input: ProjectContractFacetInput{Enabled: true, Present: false}, want: ProjectFacetStatusMissing},
		{name: "placeholder", input: ProjectContractFacetInput{Enabled: true, Present: true, Placeholder: true}, want: ProjectFacetStatusPlaceholder},
		{name: "pending", input: ProjectContractFacetInput{Enabled: true, Present: true}, want: ProjectFacetStatusPendingLaterSlice},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := facetStatus(tt.input); got != tt.want {
				t.Fatalf("facetStatus = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProjectFacetDeclarationSignatureIgnoresLifecycleStatus(t *testing.T) {
	input := []ProjectContractFacetInput{{
		Key:     "scripts",
		Folder:  "scripts",
		Enabled: true,
		Present: true,
	}}
	existing := []ProjectContractFacet{{
		FacetKey:    "scripts",
		Folder:      "scripts",
		Enabled:     true,
		Present:     true,
		FacetStatus: ProjectFacetStatusActivated,
	}}
	if facetDeclarationInputSignature(input) != facetDeclarationRecordSignature(existing) {
		t.Fatal("expected unchanged facet declaration to ignore activation lifecycle status")
	}
}

func TestReplacementFacetStatePreservesUnchangedLifecycleStatus(t *testing.T) {
	existing := ProjectContractFacet{
		ProjectContractFacetID: "project_contract_facet_existing",
		FacetKey:               "scripts",
		Folder:                 "scripts",
		Enabled:                true,
		Present:                true,
		FacetStatus:            ProjectFacetStatusActivated,
		Metadata:               json.RawMessage(`{"activated_by":"test"}`),
	}
	id, status, metadata := replacementFacetState(ProjectContractFacetInput{
		Key:     "scripts",
		Folder:  "scripts",
		Enabled: true,
		Present: true,
	}, map[string]ProjectContractFacet{"scripts": existing})
	if id != existing.ProjectContractFacetID {
		t.Fatalf("facet id = %q, want %q", id, existing.ProjectContractFacetID)
	}
	if status != ProjectFacetStatusActivated {
		t.Fatalf("status = %q, want activated", status)
	}
	if string(metadata) != string(existing.Metadata) {
		t.Fatalf("metadata = %s, want %s", metadata, existing.Metadata)
	}
}

func TestReplacementFacetStateRecomputesChangedDeclaration(t *testing.T) {
	existing := ProjectContractFacet{
		ProjectContractFacetID: "project_contract_facet_existing",
		FacetKey:               "scripts",
		Folder:                 "scripts",
		Enabled:                true,
		Present:                true,
		FacetStatus:            ProjectFacetStatusActivated,
		Metadata:               json.RawMessage(`{"activated_by":"test"}`),
	}
	id, status, metadata := replacementFacetState(ProjectContractFacetInput{
		Key:     "scripts",
		Folder:  "scripts",
		Enabled: true,
		Present: false,
	}, map[string]ProjectContractFacet{"scripts": existing})
	if id == existing.ProjectContractFacetID {
		t.Fatalf("changed declaration reused facet id %q", id)
	}
	if status != ProjectFacetStatusMissing {
		t.Fatalf("status = %q, want missing", status)
	}
	if string(metadata) != `{}` {
		t.Fatalf("metadata = %s, want {}", metadata)
	}
}

func TestProjectWatchedRootRegistrationStatuses(t *testing.T) {
	valid := []string{
		ProjectWatchedRootRegistrationStatusRegistered,
		ProjectWatchedRootRegistrationStatusPendingAgentApply,
		ProjectWatchedRootRegistrationStatusApplied,
		ProjectWatchedRootRegistrationStatusReported,
		ProjectWatchedRootRegistrationStatusBlocked,
		ProjectWatchedRootRegistrationStatusDisabled,
		ProjectWatchedRootRegistrationStatusStale,
	}
	for _, status := range valid {
		if !validProjectWatchedRootRegistrationStatus(status) {
			t.Fatalf("status %q should be valid", status)
		}
	}
	if validProjectWatchedRootRegistrationStatus("unknown") {
		t.Fatal("unknown watched-root status should be invalid")
	}
}

func TestProjectConnectorRegistrationStatuses(t *testing.T) {
	valid := []string{
		ProjectConnectorRegistrationStatusRegistered,
		ProjectConnectorRegistrationStatusActive,
		ProjectConnectorRegistrationStatusDisabled,
		ProjectConnectorRegistrationStatusBlocked,
		ProjectConnectorRegistrationStatusStale,
	}
	for _, status := range valid {
		if !validProjectConnectorRegistrationStatus(status) {
			t.Fatalf("status %q should be valid", status)
		}
	}
	if validProjectConnectorRegistrationStatus("unknown") {
		t.Fatal("unknown connector status should be invalid")
	}
}

func TestProjectRegistrationMetadataPreservesContractState(t *testing.T) {
	input := validRegisterProjectContractInput()
	input.Project.Status = "draft"
	existing := json.RawMessage(`{"keep":"value"}`)

	raw, err := projectRegistrationMetadata(existing, input)
	if err != nil {
		t.Fatalf("project registration metadata: %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if metadata["keep"] != "value" {
		t.Fatalf("expected unrelated metadata to be preserved: %#v", metadata)
	}
	registration, ok := metadata["v0_3_project_contract"].(map[string]any)
	if !ok {
		t.Fatalf("missing v0_3_project_contract metadata: %#v", metadata)
	}
	if registration["contract_status"] != "draft" {
		t.Fatalf("contract_status = %#v, want draft", registration["contract_status"])
	}
	if registration["last_contract_hash"] != input.ContractHash {
		t.Fatalf("last_contract_hash = %#v, want %s", registration["last_contract_hash"], input.ContractHash)
	}
	if registration["contract_hash"] != input.ContractHash {
		t.Fatalf("contract_hash = %#v, want %s", registration["contract_hash"], input.ContractHash)
	}
}

func TestJSONRawEqualUsesSemanticComparison(t *testing.T) {
	if !jsonRawEqual(json.RawMessage(`{"b":2,"a":1}`), json.RawMessage(`{"a":1,"b":2}`)) {
		t.Fatal("expected object key order to compare equal")
	}
	if jsonRawEqual(json.RawMessage(`{"a":1}`), json.RawMessage(`{"a":2}`)) {
		t.Fatal("expected changed JSON value to compare unequal")
	}
}

func TestJSONRawEqualIgnoringKeysSkipsVolatilePlanFields(t *testing.T) {
	left := json.RawMessage(`{"generated_at":"2026-01-01T00:00:00Z","actions":[{"status":"pending_later_slice"}]}`)
	right := json.RawMessage(`{"generated_at":"2026-01-02T00:00:00Z","actions":[{"status":"pending_later_slice"}]}`)
	if !jsonRawEqualIgnoringKeys(left, right, "generated_at") {
		t.Fatal("expected generated_at-only plan change to compare equal")
	}
	changed := json.RawMessage(`{"generated_at":"2026-01-02T00:00:00Z","actions":[{"status":"activated"}]}`)
	if jsonRawEqualIgnoringKeys(left, changed, "generated_at") {
		t.Fatal("expected non-volatile plan change to compare unequal")
	}
}

func validRegisterProjectContractInput() RegisterProjectContractInput {
	return RegisterProjectContractInput{
		ProjectRoot:           "/tmp/gmail-automation",
		ContractPath:          "/tmp/gmail-automation/loom.project.yaml",
		ContractHash:          "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ContractSchemaVersion: "project.contract.v0.3",
		Contract:              json.RawMessage(`{"kind":"loom.project"}`),
		ValidationReport:      json.RawMessage(`{"ok":true}`),
		RegistrationPlan:      json.RawMessage(`{"registerable":true}`),
		Project: ProjectContractProjectInput{
			Slug:      "gmail-automation",
			Name:      "Gmail Automation",
			OwnerNode: "main",
			Status:    "draft",
		},
		DerivedProviders: json.RawMessage(`[]`),
		Facets:           []ProjectContractFacetInput{{Key: "notes", Enabled: true, Present: true}},
		PolicyRefs:       json.RawMessage(`[]`),
		Metadata:         json.RawMessage(`{}`),
	}
}
