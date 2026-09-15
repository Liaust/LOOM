package projectactivation

import (
	"context"
	"encoding/json"
	"testing"

	"loom.local/loom/internal/capabilities"
	sr "loom.local/loom/internal/serviceregistry"
)

type archiveApplicationFacts struct {
	facts sr.ApplicationPrerequisiteSnapshot
}

func (f archiveApplicationFacts) QueryPrerequisites(context.Context, sr.ApplicationPrerequisiteQuery) (sr.ApplicationPrerequisiteSnapshot, error) {
	return f.facts, nil
}

func TestManagedApplicationArchiveWithoutStaticAllowlist(t *testing.T) {
	owner := sr.ApplicationOwner{ProjectID: "project_01ARZ3NDEKTSV4RRFFQ69G5FAV", NodeID: "node_01ARZ3NDEKTSV4RRFFQ69G5FAV", Resource: "server"}
	target, err := sr.ApplicationServiceTarget(owner, "main", "provider-fixture")
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := json.Marshal(sr.ApplicationServiceProfile(owner))
	metadata, _ := json.Marshal(sr.ApplicationRegistrationMetadata{Source: sr.ApplicationRegistrationSource, ProjectID: owner.ProjectID, Owner: owner})
	p := capabilities.Provider{ProviderID: target.ProviderID, ProviderKey: target.ProviderKey, CompactAddress: target.ProviderAddress, NodeID: owner.NodeID, ScopeID: "scope-project", ProviderType: capabilities.ProviderTypeService, Status: capabilities.ProviderStatusActive, RuntimeProfileJSON: profile, Metadata: metadata}
	item := capabilities.ProviderListItem{Provider: p}
	registry := &fakeCapabilityService{providerItems: []capabilities.ProviderListItem{item}}
	yes := true
	facts := sr.ApplicationPrerequisiteSnapshot{SchemaVersion: sr.ApplicationPrerequisiteSchema, Owner: owner, Publication: sr.ApplicationPrerequisitePublication{Present: true, GrantMatches: &yes, ArchiveTarget: &target}}
	service := NewService(Deps{Capabilities: registry, Applications: archiveApplicationFacts{facts}})
	_, raw, err := service.projectArchiveServiceActionMetadata(t.Context(), item)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeProjectArchiveServiceActionMetadata(raw)
	if err != nil || got.ProviderID != target.ProviderID || got.AllowlistKey != owner.AllowlistKey() || got.RuntimeProfileDigest != target.RuntimeProfileDigest {
		t.Fatal(got, err)
	}
	service.Applications = nil
	if _, _, err := service.projectArchiveServiceActionMetadata(t.Context(), item); err == nil {
		t.Fatal("missing publisher evidence allowed")
	}
}
