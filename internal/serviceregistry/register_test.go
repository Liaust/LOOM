package serviceregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"testing"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/requestctx"
)

type fakeRegistry struct {
	fakeEndpointRegistry
	providers map[string]capabilities.Provider
	healths   int
	health    *capabilities.ProviderHealth
}

func (f *fakeRegistry) EnsureProvider(_ context.Context, _ requestctx.Context, input capabilities.RegisterProviderInput) (capabilities.Provider, bool, error) {
	if f.providers == nil {
		f.providers = map[string]capabilities.Provider{}
	}
	provider, found := f.providers[input.ProviderKey]
	if !found {
		provider.ProviderID = ids.NewProviderID()
	}
	provider.ProviderKey, provider.CompactAddress, provider.DisplayName, provider.Description = input.ProviderKey, input.CompactAddress, input.DisplayName, input.Description
	provider.ProviderType, provider.NodeID, provider.ScopeID, provider.Version, provider.Status = input.ProviderType, input.NodeRef, input.ScopeRef, input.Version, input.Status
	provider.RuntimeProfileJSON, provider.DocumentationRefsJSON, provider.Metadata = input.RuntimeProfileJSON, input.DocumentationRefsJSON, input.Metadata
	f.providers[input.ProviderKey] = provider
	return provider, !found, nil
}
func (f *fakeRegistry) UpsertProviderHealth(_ context.Context, _ requestctx.Context, providerID string, input capabilities.ProviderHealthInput) (capabilities.ProviderHealth, error) {
	f.healths++
	health := capabilities.ProviderHealth{ProviderID: providerID, HealthStatus: input.HealthStatus, AvailabilityStatus: input.AvailabilityStatus, DetailsJSON: input.DetailsJSON}
	f.health = &health
	return health, nil
}
func (f *fakeRegistry) InspectProvider(_ context.Context, ref string) (capabilities.ProviderInspection, error) {
	inspection := capabilities.ProviderInspection{Health: f.health, Endpoints: f.endpoints}
	for _, provider := range f.providers {
		if provider.ProviderID == ref || provider.ProviderKey == ref {
			inspection.Provider = provider
			break
		}
	}
	return inspection, nil
}
func (f *fakeRegistry) ListProviders(_ context.Context, filter capabilities.ProviderFilter) ([]capabilities.ProviderListItem, error) {
	items := []capabilities.ProviderListItem{}
	for _, provider := range f.providers {
		if filter.ProviderType == "" || provider.ProviderType == filter.ProviderType {
			items = append(items, capabilities.ProviderListItem{Provider: provider})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CompactAddress < items[j].CompactAddress })
	if filter.Offset >= len(items) {
		return []capabilities.ProviderListItem{}, nil
	}
	items = items[filter.Offset:]
	if filter.Limit > 0 && len(items) > filter.Limit {
		items = items[:filter.Limit]
	}
	return items, nil
}

func projectPlanInput(projectID string) ProjectRegistrationPlanInput {
	return ProjectRegistrationPlanInput{
		ProviderKey: "example-project-api", ProjectSlug: "example-project", ScopeRef: "scope_test",
		Registration: ProjectRegistrationInput{
			ProjectID: projectID, ScopeKey: "projects/example-project", TargetNode: "macbook", SourcePath: ".loom/contracts/services/api.yaml",
			Service: ServiceIdentity{Key: "api", DisplayName: "Project API", Class: ServiceClassProject},
			Runtime: RuntimeProfileInput{Manager: ManagerLaunchd, Unit: "com.example.api", ServiceClass: ServiceClassProject, Operations: []Operation{}},
		},
	}
}

func TestReconcileProjectPagesAllExistingProviders(t *testing.T) {
	registry := &fakeRegistry{providers: map[string]capabilities.Provider{}}
	for index := 0; index < 205; index++ {
		key := fmt.Sprintf("service-%03d", index)
		registry.providers[key] = capabilities.Provider{
			ProviderID: ids.NewProviderID(), ProviderKey: key, CompactAddress: "macbook@" + key,
			ProviderType: capabilities.ProviderTypeService, Status: capabilities.ProviderStatusActive,
			Metadata: json.RawMessage(`{"source":"project.service_registration"}`),
		}
	}
	result, err := ReconcileProject(context.Background(), requestctx.Context{}, registry, projectAllowlist(), "example-project", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Disabled) != 205 {
		t.Fatalf("disabled %d providers, want 205", len(result.Disabled))
	}
}

func projectAllowlist(operations ...Operation) StaticAllowlistResolver {
	return StaticAllowlistResolver{Allowlist: Allowlist{Records: []AllowlistRecord{{
		SchemaVersion: AllowlistSchemaV1, Key: "example-api", NodeKey: "macbook", Manager: ManagerLaunchd,
		Unit: "com.example.api", Operations: operations, LifecyclePolicy: "service_operations",
		Health: AllowlistHealth{Kind: HealthKindManager}, LogLimits: LogLimits{},
	}}}}
}

func TestRegisterProjectIsIdempotentAndNeverProvisions(t *testing.T) {
	registry := &fakeRegistry{}
	input := projectPlanInput(ids.NewProjectID())
	first, err := RegisterProject(context.Background(), requestctx.Context{}, registry, projectAllowlist(), input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RegisterProject(context.Background(), requestctx.Context{}, registry, projectAllowlist(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || second.Created || first.Provider.ProviderID != second.Provider.ProviderID {
		t.Fatalf("identity was not stable: %#v %#v", first, second)
	}
	if first.Plan.Provisioning != "external" || len(first.Plan.RuntimeProfile.Operations) != 0 {
		t.Fatalf("unsafe registration plan: %#v", first.Plan)
	}
	var metadata map[string]any
	if err := json.Unmarshal(first.Provider.Metadata, &metadata); err != nil || metadata["provisioning"] != "external" {
		t.Fatalf("metadata: %s", first.Provider.Metadata)
	}
	if registry.healths != 1 {
		t.Fatalf("health projections = %d", registry.healths)
	}
}

func TestReconcileProjectDisablesRemovedDeclarations(t *testing.T) {
	registry := &fakeRegistry{}
	input := projectPlanInput(ids.NewProjectID())
	if _, err := RegisterProject(context.Background(), requestctx.Context{}, registry, projectAllowlist(), input); err != nil {
		t.Fatal(err)
	}
	result, err := ReconcileProject(context.Background(), requestctx.Context{}, registry, projectAllowlist(), "example-project", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Disabled) != 1 || registry.providers[input.ProviderKey].Status != capabilities.ProviderStatusDisabled {
		t.Fatalf("removed provider not disabled: %#v %#v", result, registry.providers)
	}
}

func TestRegisterProjectPersistsOnlyAllowlistIntersection(t *testing.T) {
	registry := &fakeRegistry{}
	input := projectPlanInput(ids.NewProjectID())
	input.Registration.Runtime.Operations = []Operation{OperationStatus, OperationStart}
	result, err := RegisterProject(context.Background(), requestctx.Context{ActorID: "actor_test"}, registry, projectAllowlist(OperationStatus), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Endpoints.EffectiveOperations) != 1 || result.Endpoints.EffectiveOperations[0] != OperationStatus {
		t.Fatalf("effective endpoints = %#v", result.Endpoints)
	}
	if len(registry.bindings) != 1 || string(registry.bindings[0].RuntimeConfigJSON) != `{"allowlist_key":"example-api","operation":"status"}` {
		t.Fatalf("persisted runtime bindings = %#v", registry.bindings)
	}
}
