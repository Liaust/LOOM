package serviceregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/requestctx"
)

const RegistrationSourceProject = "project.service_registration"

func ProjectRegistrationScopeKey(projectSlug string) string {
	return "projects/" + strings.TrimSpace(projectSlug)
}

type ProviderRegistry interface {
	EnsureProvider(context.Context, requestctx.Context, capabilities.RegisterProviderInput) (capabilities.Provider, bool, error)
	UpsertProviderHealth(context.Context, requestctx.Context, string, capabilities.ProviderHealthInput) (capabilities.ProviderHealth, error)
	ListProviders(context.Context, capabilities.ProviderFilter) ([]capabilities.ProviderListItem, error)
}

type RegistrationRegistry interface {
	ProviderRegistry
	EndpointRegistry
}

func ProjectRegistrationFromContract(projectID, scopeKey, sourcePath string, contract projectcontracts.ServiceRegistrationContract) ProjectRegistrationInput {
	operations := make([]Operation, 0, len(contract.RequestedOperations()))
	for _, operation := range contract.RequestedOperations() {
		operations = append(operations, Operation(operation))
	}
	return ProjectRegistrationInput{ProjectID: projectID, ScopeKey: scopeKey, TargetNode: contract.Service.TargetNode, SourcePath: sourcePath, Service: ServiceIdentity{Key: contract.Service.Key, DisplayName: contract.Service.Name, Description: contract.Service.Description, Class: ServiceClass(contract.Service.Class)}, Runtime: RuntimeProfileInput{Manager: Manager(contract.Runtime.Manager), Unit: contract.Runtime.Unit, ServiceClass: ServiceClass(contract.Service.Class), Operations: operations, Health: RuntimeHealth{Kind: HealthKind(contract.Health.Kind)}, References: RuntimeReferences{Protection: contract.References.Protection, Exposure: contract.References.Exposure, Credentials: contract.References.Credentials}}}
}

type endpointDeactivator interface {
	InspectProvider(context.Context, string) (capabilities.ProviderInspection, error)
	InspectCapability(context.Context, string) (capabilities.CapabilityInspection, error)
	DisableCapabilityEndpoint(context.Context, requestctx.Context, string, json.RawMessage) (capabilities.CapabilityEndpoint, error)
	DisableRuntimeBinding(context.Context, requestctx.Context, string, json.RawMessage) (capabilities.EndpointRuntimeBinding, error)
}

type ProjectRegistrationPlanInput struct {
	Registration ProjectRegistrationInput `json:"registration"`
	ProviderKey  string                   `json:"provider_key"`
	ProjectSlug  string                   `json:"project_slug"`
	ScopeRef     string                   `json:"scope_ref"`
}

type RegistrationPlan struct {
	ProviderKey     string          `json:"provider_key"`
	ProviderAddress string          `json:"provider_address"`
	TargetNode      string          `json:"target_node"`
	ScopeKey        string          `json:"scope_key"`
	Service         ServiceIdentity `json:"service"`
	RuntimeProfile  RuntimeProfile  `json:"runtime_profile"`
	Provisioning    string          `json:"provisioning"`
	SourcePath      string          `json:"source_path,omitempty"`
	ProjectID       string          `json:"project_id,omitempty"`
	ProjectSlug     string          `json:"project_slug,omitempty"`
}

type RegistrationResult struct {
	Plan      RegistrationPlan      `json:"plan"`
	Provider  capabilities.Provider `json:"provider"`
	Endpoints EndpointCompileResult `json:"endpoints"`
	Created   bool                  `json:"created"`
}

type ProjectReconcileResult struct {
	Registrations []RegistrationResult `json:"registrations"`
	Disabled      []string             `json:"disabled,omitempty"`
}

func PlanProjectRegistration(input ProjectRegistrationPlanInput) (RegistrationPlan, error) {
	if err := input.Registration.Validate(); err != nil {
		return RegistrationPlan{}, err
	}
	if !canonicalIdentifier(input.ProviderKey, registryKeyPattern) {
		return RegistrationPlan{}, fmt.Errorf("provider_key must be a canonical lowercase bounded registry key")
	}
	if strings.TrimSpace(input.ScopeRef) == "" {
		return RegistrationPlan{}, fmt.Errorf("scope_ref is required")
	}
	profile, err := BuildRuntimeProfile(input.Registration.Runtime)
	if err != nil {
		return RegistrationPlan{}, err
	}
	return RegistrationPlan{
		ProviderKey: input.ProviderKey, ProviderAddress: input.Registration.TargetNode + "@" + input.ProviderKey,
		TargetNode: input.Registration.TargetNode, ScopeKey: input.Registration.ScopeKey,
		Service: input.Registration.Service, RuntimeProfile: profile, Provisioning: "external",
		SourcePath: input.Registration.SourcePath, ProjectID: input.Registration.ProjectID,
		ProjectSlug: strings.TrimSpace(input.ProjectSlug),
	}, nil
}

func RegisterProject(ctx context.Context, req requestctx.Context, registry RegistrationRegistry, allowlists AllowlistResolver, input ProjectRegistrationPlanInput) (RegistrationResult, error) {
	plan, err := PlanProjectRegistration(input)
	if err != nil {
		return RegistrationResult{}, err
	}
	if allowlists == nil {
		return RegistrationResult{}, fmt.Errorf("reviewed service allowlist resolver is required")
	}
	record, err := allowlists.ResolveServiceAllowlist(ctx, plan.TargetNode, plan.RuntimeProfile)
	if err != nil {
		return RegistrationResult{}, err
	}
	runtimeJSON, _ := json.Marshal(plan.RuntimeProfile)
	metadata, _ := json.Marshal(map[string]any{
		"source": RegistrationSourceProject, "project_id": plan.ProjectID, "project_slug": plan.ProjectSlug,
		"service_key": plan.Service.Key, "source_path": plan.SourcePath, "provisioning": plan.Provisioning,
	})
	provider, created, err := registry.EnsureProvider(ctx, req, capabilities.RegisterProviderInput{
		ProviderKey: plan.ProviderKey, CompactAddress: plan.ProviderAddress,
		DisplayName: plan.Service.DisplayName, Description: plan.Service.Description,
		ProviderType: capabilities.ProviderTypeService, NodeRef: plan.TargetNode, ScopeRef: input.ScopeRef,
		Version: "0.1.0", Status: capabilities.ProviderStatusActive,
		RuntimeProfileJSON: runtimeJSON, DocumentationRefsJSON: json.RawMessage(`[]`), Metadata: metadata,
	})
	if err != nil {
		return RegistrationResult{}, err
	}
	initializeHealth := created
	if !initializeHealth {
		inspection, err := registry.InspectProvider(ctx, provider.ProviderID)
		if err != nil {
			return RegistrationResult{}, err
		}
		initializeHealth = inspection.Health == nil
	}
	if initializeHealth {
		if _, err := registry.UpsertProviderHealth(ctx, req, provider.ProviderID, capabilities.ProviderHealthInput{
			HealthStatus: capabilities.HealthStatusUnknown, AvailabilityStatus: capabilities.AvailabilityStatusUnknown,
			Message: "Registered; process state has not been observed.", DetailsJSON: json.RawMessage(`{"process_state":"unknown"}`),
		}); err != nil {
			return RegistrationResult{}, err
		}
	}
	endpoints, err := CompileServiceEndpoints(ctx, req, registry, provider, plan.RuntimeProfile, record)
	if err != nil {
		return RegistrationResult{}, err
	}
	return RegistrationResult{Plan: plan, Provider: provider, Endpoints: endpoints, Created: created}, nil
}

func ReconcileProject(ctx context.Context, req requestctx.Context, registry RegistrationRegistry, allowlists AllowlistResolver, projectRef string, inputs []ProjectRegistrationPlanInput) (ProjectReconcileResult, error) {
	result := ProjectReconcileResult{Registrations: []RegistrationResult{}}
	seen := map[string]bool{}
	for _, input := range inputs {
		registered, err := RegisterProject(ctx, req, registry, allowlists, input)
		if err != nil {
			return result, err
		}
		seen[registered.Provider.ProviderID] = true
		result.Registrations = append(result.Registrations, registered)
	}
	const pageSize = 200
	for offset := 0; ; offset += pageSize {
		existing, err := registry.ListProviders(ctx, capabilities.ProviderFilter{ProjectRef: projectRef, ProviderType: capabilities.ProviderTypeService, Limit: pageSize, Offset: offset})
		if err != nil {
			return result, err
		}
		for _, item := range existing {
			if seen[item.ProviderID] || !projectServiceProvider(item.Metadata) || item.Status == capabilities.ProviderStatusDisabled {
				continue
			}
			metadata := mergeMetadata(item.Metadata, map[string]any{"registration_state": "stale", "provisioning": "external"})
			if deactivator, ok := registry.(endpointDeactivator); ok {
				inspection, err := deactivator.InspectProvider(ctx, item.ProviderID)
				if err != nil {
					return result, err
				}
				for _, endpoint := range inspection.Endpoints {
					capability, err := deactivator.InspectCapability(ctx, endpoint.CapabilityEndpointID)
					if err != nil {
						return result, err
					}
					if capability.RuntimeBinding != nil && capability.RuntimeBinding.Status == capabilities.RuntimeBindingStatusActive {
						if _, err := deactivator.DisableRuntimeBinding(ctx, req, capability.RuntimeBinding.RuntimeBindingID, metadata); err != nil {
							return result, err
						}
					}
					if endpoint.Status == capabilities.EndpointStatusActive {
						if _, err := deactivator.DisableCapabilityEndpoint(ctx, req, endpoint.CapabilityEndpointID, metadata); err != nil {
							return result, err
						}
					}
				}
			}
			_, _, err := registry.EnsureProvider(ctx, req, capabilities.RegisterProviderInput{
				ProviderKey: item.ProviderKey, CompactAddress: item.CompactAddress, DisplayName: item.DisplayName,
				Description: item.Description, ProviderType: item.ProviderType, NodeRef: item.NodeID, ScopeRef: item.ScopeID,
				Version: item.Version, Status: capabilities.ProviderStatusDisabled, RuntimeProfileJSON: item.RuntimeProfileJSON,
				DocumentationRefsJSON: item.DocumentationRefsJSON, Metadata: metadata,
			})
			if err != nil {
				return result, err
			}
			result.Disabled = append(result.Disabled, item.CompactAddress)
		}
		if len(existing) < pageSize {
			break
		}
	}
	return result, nil
}

func projectServiceProvider(raw json.RawMessage) bool {
	var metadata map[string]any
	return json.Unmarshal(raw, &metadata) == nil && metadata["source"] == RegistrationSourceProject
}

func mergeMetadata(raw json.RawMessage, values map[string]any) json.RawMessage {
	metadata := map[string]any{}
	_ = json.Unmarshal(raw, &metadata)
	for key, value := range values {
		metadata[key] = value
	}
	payload, _ := json.Marshal(metadata)
	return payload
}
