package serviceregistry

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/requestctx"
)

type EndpointRegistry interface {
	EnsureCapabilityClass(context.Context, requestctx.Context, capabilities.RegisterCapabilityClassInput) (capabilities.CapabilityClass, bool, error)
	EnsureCapabilityEndpoint(context.Context, requestctx.Context, capabilities.RegisterCapabilityEndpointInput) (capabilities.CapabilityEndpoint, bool, error)
	EnsureEndpointVersion(context.Context, requestctx.Context, capabilities.RegisterEndpointVersionInput) (capabilities.EndpointVersion, bool, error)
	RegisterRuntimeBinding(context.Context, requestctx.Context, capabilities.RegisterRuntimeBindingInput) (capabilities.RuntimeBindingInspection, error)
	EnsureUsageDocument(context.Context, requestctx.Context, capabilities.RegisterUsageDocumentInput) (capabilities.UsageDocument, bool, error)
	InspectProvider(context.Context, string) (capabilities.ProviderInspection, error)
	InspectCapability(context.Context, string) (capabilities.CapabilityInspection, error)
	DisableCapabilityEndpoint(context.Context, requestctx.Context, string, json.RawMessage) (capabilities.CapabilityEndpoint, error)
	DisableRuntimeBinding(context.Context, requestctx.Context, string, json.RawMessage) (capabilities.EndpointRuntimeBinding, error)
}

type EndpointCompileResult struct {
	EffectiveOperations []Operation                       `json:"effective_operations"`
	Endpoints           []capabilities.CapabilityEndpoint `json:"endpoints"`
	Disabled            []string                          `json:"disabled,omitempty"`
}

func CompileServiceEndpoints(ctx context.Context, req requestctx.Context, registry EndpointRegistry, provider capabilities.Provider, profile RuntimeProfile, record AllowlistRecord) (EndpointCompileResult, error) {
	if provider.Status != capabilities.ProviderStatusActive {
		return EndpointCompileResult{}, fmt.Errorf("service provider must be active before endpoint compilation")
	}
	if provider.NodeID != "" && record.NodeKey == "" {
		return EndpointCompileResult{}, fmt.Errorf("allowlist node identity is required")
	}
	effective, err := IntersectOperations(profile, record)
	if err != nil {
		return EndpointCompileResult{}, err
	}
	result := EndpointCompileResult{EffectiveOperations: effective, Endpoints: []capabilities.CapabilityEndpoint{}}
	activeAddresses := map[string]bool{}
	for _, operation := range effective {
		policy, _ := StandardOperationPolicy(operation)
		form := capabilities.CapabilityFormQuery
		if policy.Form == CapabilityFormCommand {
			form = capabilities.CapabilityFormCommand
		}
		class, _, err := registry.EnsureCapabilityClass(ctx, req, capabilities.RegisterCapabilityClassInput{
			Namespace: "service", Name: string(operation), Version: "0.1.0", DisplayName: "Service " + string(operation),
			Description: "Allowlisted operation on an already-provisioned service.", Form: form,
			InputSchemaJSON: serviceOperationInputSchema(operation, record.LogLimits), OutputSchemaJSON: serviceOperationOutputSchema(),
			DefaultRiskLevel: string(policy.RiskLevel), DefaultPolicyRequirementsJSON: json.RawMessage(`{}`), Status: capabilities.CapabilityClassStatusActive,
			Metadata: json.RawMessage(`{"source":"service_registry"}`),
		})
		if err != nil {
			return result, err
		}
		endpointName := "service." + string(operation)
		address := provider.CompactAddress + "." + endpointName
		endpoint, _, err := registry.EnsureCapabilityEndpoint(ctx, req, capabilities.RegisterCapabilityEndpointInput{
			ProviderRef: provider.ProviderID, CapabilityClassRef: class.CapabilityClassID, EndpointName: endpointName, CompactAddress: address, Form: form,
			InputSchemaJSON: serviceOperationInputSchema(operation, record.LogLimits), OutputSchemaJSON: serviceOperationOutputSchema(), RiskLevel: string(policy.RiskLevel), ExecutionAuthorizationLevel: policy.ExecutionAuthorizationLevel,
			SideEffectsJSON: mustJSON(map[string]any{"mutates_state": policy.MutatesState}), PolicyRequirementsJSON: json.RawMessage(`{}`), CredentialRequirementsJSON: json.RawMessage(`{}`),
			ApprovalRequirementsJSON: mustJSON(map[string]any{"required": policy.RequiresConfirmation}), JobBehaviorJSON: json.RawMessage(`{}`), SessionBehaviorJSON: json.RawMessage(`{}`), StreamBehaviorJSON: json.RawMessage(`{}`), LeaseBehaviorJSON: json.RawMessage(`{}`),
			Status: capabilities.EndpointStatusActive, Metadata: mustJSON(map[string]any{"source": "service_registry", "allowlist_key": record.Key, "operation": operation}),
		})
		if err != nil {
			return result, err
		}
		now := time.Now().UTC()
		version, _, err := registry.EnsureEndpointVersion(ctx, req, capabilities.RegisterEndpointVersionInput{CapabilityEndpointRef: endpoint.CapabilityEndpointID, VersionLabel: "0.1.0", ManifestJSON: mustJSON(map[string]any{"runtime_profile_schema": profile.SchemaVersion, "allowlist_key": record.Key, "operation": operation}), InputSchemaJSON: endpoint.InputSchemaJSON, OutputSchemaJSON: endpoint.OutputSchemaJSON, RiskLevel: endpoint.RiskLevel, ExecutionAuthorizationLevel: endpoint.ExecutionAuthorizationLevel, PolicyRequirementsJSON: json.RawMessage(`{}`), CredentialRequirementsJSON: json.RawMessage(`{}`), ApprovalRequirementsJSON: endpoint.ApprovalRequirementsJSON, Status: capabilities.EndpointVersionStatusActive, ApprovedByActorID: req.ActorID, ApprovedAt: &now, Metadata: endpoint.Metadata})
		if err != nil {
			return result, err
		}
		if _, err := registry.RegisterRuntimeBinding(ctx, req, capabilities.RegisterRuntimeBindingInput{EndpointVersionRef: version.CapabilityEndpointVersionID, RuntimeKind: capabilities.RuntimeKindServiceManager, RuntimeConfigJSON: mustJSON(map[string]any{"allowlist_key": record.Key, "operation": operation}), InputMappingJSON: json.RawMessage(`{}`), OutputMappingJSON: json.RawMessage(`{}`), Status: capabilities.RuntimeBindingStatusActive, ApprovedByActorID: req.ActorID, ApprovedAt: &now, Metadata: endpoint.Metadata}); err != nil {
			return result, err
		}
		body := "# " + address + "\n\nThis endpoint operates only the reviewed allowlist entry `" + record.Key + "`; it cannot accept commands, unit paths, or service-manager arguments.\n"
		bodyHash := sha256.Sum256([]byte(body))
		if _, _, err := registry.EnsureUsageDocument(ctx, req, capabilities.RegisterUsageDocumentInput{TargetKind: capabilities.UsageTargetKindCapabilityEndpoint, TargetID: endpoint.CapabilityEndpointID, TargetAddress: address, Title: "Use " + address, VersionLabel: "0.1.0", BodyFormat: capabilities.UsageDocumentFormatMarkdown, Body: body, SectionMapJSON: json.RawMessage(`{"sections":[]}`), VisibilityPolicyJSON: json.RawMessage(`{}`), ReviewStatus: capabilities.UsageReviewStatusApproved, ContentHash: fmt.Sprintf("sha256:%x", bodyHash), SourceKind: capabilities.UsageSourceKindGenerated, SourceRef: "service_registry", ApprovedByActorID: req.ActorID, ApprovedAt: &now, Metadata: endpoint.Metadata}); err != nil {
			return result, err
		}
		activeAddresses[address] = true
		result.Endpoints = append(result.Endpoints, endpoint)
	}
	inspection, err := registry.InspectProvider(ctx, provider.ProviderID)
	if err != nil {
		return result, err
	}
	for _, endpoint := range inspection.Endpoints {
		if activeAddresses[endpoint.CompactAddress] || endpoint.Status != capabilities.EndpointStatusActive {
			continue
		}
		var metadata map[string]any
		if json.Unmarshal(endpoint.Metadata, &metadata) != nil || metadata["source"] != "service_registry" {
			continue
		}
		disableMetadata := mustJSON(map[string]any{"source": "service_registry", "reason": "operation_not_in_allowlist_intersection"})
		capability, err := registry.InspectCapability(ctx, endpoint.CapabilityEndpointID)
		if err != nil {
			return result, err
		}
		if capability.RuntimeBinding != nil && capability.RuntimeBinding.Status == capabilities.RuntimeBindingStatusActive {
			if _, err := registry.DisableRuntimeBinding(ctx, req, capability.RuntimeBinding.RuntimeBindingID, disableMetadata); err != nil {
				return result, err
			}
		}
		if _, err := registry.DisableCapabilityEndpoint(ctx, req, endpoint.CapabilityEndpointID, disableMetadata); err != nil {
			return result, err
		}
		result.Disabled = append(result.Disabled, endpoint.CompactAddress)
	}
	return result, nil
}

func serviceOperationInputSchema(operation Operation, limits LogLimits) json.RawMessage {
	if operation != OperationLogs {
		return json.RawMessage(`{"type":"object","additionalProperties":false}`)
	}
	normalized, _ := limits.Normalize()
	return mustJSON(map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"lines": map[string]any{"type": "integer", "minimum": 1, "maximum": normalized.MaxLines}, "max_bytes": map[string]any{"type": "integer", "minimum": 1, "maximum": normalized.MaxBytes}, "max_age_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": normalized.MaxAgeSeconds}}})
}
func serviceOperationOutputSchema() json.RawMessage {
	return mustJSON(map[string]any{
		"type":                 "object",
		"required":             []string{"operation", "success", "process_state"},
		"additionalProperties": false,
		"properties": map[string]any{
			"operation": map[string]any{"type": "string", "enum": StandardOperations()},
			"success":   map[string]any{"type": "boolean"},
			"process_state": map[string]any{
				"type": "string",
				"enum": []ObservedProcessState{ProcessStateUnknown, ProcessStateRunning, ProcessStateStopped, ProcessStateFailed, ProcessStateUnavailable},
			},
			"message":   map[string]any{"type": "string", "maxLength": maximumMessageBytes},
			"log_lines": map[string]any{"type": "array", "maxItems": MaximumLogLines, "items": map[string]any{"type": "string", "maxLength": MaximumLogLineBytes}},
			"exit_code": map[string]any{"type": "integer", "minimum": -2147483648, "maximum": 2147483647},
			"truncated": map[string]any{"type": "boolean"},
		},
	})
}
func mustJSON(value any) json.RawMessage { payload, _ := json.Marshal(value); return payload }
