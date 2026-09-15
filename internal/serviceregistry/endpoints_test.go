package serviceregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/requestctx"
)

type fakeEndpointRegistry struct {
	endpoints        []capabilities.CapabilityEndpoint
	bindings         []capabilities.RegisterRuntimeBindingInput
	disabled         []string
	disabledBindings []string
}

func (f *fakeEndpointRegistry) EnsureCapabilityClass(_ context.Context, _ requestctx.Context, input capabilities.RegisterCapabilityClassInput) (capabilities.CapabilityClass, bool, error) {
	return capabilities.CapabilityClass{CapabilityClassID: "cls_" + input.Name, Namespace: input.Namespace, Name: input.Name}, true, nil
}
func (f *fakeEndpointRegistry) EnsureCapabilityEndpoint(_ context.Context, _ requestctx.Context, input capabilities.RegisterCapabilityEndpointInput) (capabilities.CapabilityEndpoint, bool, error) {
	endpoint := capabilities.CapabilityEndpoint{CapabilityEndpointID: "endp_" + input.EndpointName, ProviderID: input.ProviderRef, EndpointName: input.EndpointName, CompactAddress: input.CompactAddress, InputSchemaJSON: input.InputSchemaJSON, OutputSchemaJSON: input.OutputSchemaJSON, RiskLevel: input.RiskLevel, ExecutionAuthorizationLevel: input.ExecutionAuthorizationLevel, ApprovalRequirementsJSON: input.ApprovalRequirementsJSON, Status: input.Status, Metadata: input.Metadata}
	f.endpoints = append(f.endpoints, endpoint)
	return endpoint, true, nil
}
func (f *fakeEndpointRegistry) EnsureEndpointVersion(_ context.Context, _ requestctx.Context, input capabilities.RegisterEndpointVersionInput) (capabilities.EndpointVersion, bool, error) {
	return capabilities.EndpointVersion{CapabilityEndpointVersionID: "version_" + input.CapabilityEndpointRef}, true, nil
}
func (f *fakeEndpointRegistry) RegisterRuntimeBinding(_ context.Context, _ requestctx.Context, input capabilities.RegisterRuntimeBindingInput) (capabilities.RuntimeBindingInspection, error) {
	f.bindings = append(f.bindings, input)
	return capabilities.RuntimeBindingInspection{}, nil
}
func (f *fakeEndpointRegistry) EnsureUsageDocument(context.Context, requestctx.Context, capabilities.RegisterUsageDocumentInput) (capabilities.UsageDocument, bool, error) {
	return capabilities.UsageDocument{}, true, nil
}
func (f *fakeEndpointRegistry) InspectProvider(context.Context, string) (capabilities.ProviderInspection, error) {
	return capabilities.ProviderInspection{Endpoints: f.endpoints}, nil
}
func (f *fakeEndpointRegistry) InspectCapability(_ context.Context, ref string) (capabilities.CapabilityInspection, error) {
	for _, endpoint := range f.endpoints {
		if endpoint.CapabilityEndpointID == ref {
			return capabilities.CapabilityInspection{Endpoint: endpoint, RuntimeBinding: &capabilities.EndpointRuntimeBinding{RuntimeBindingID: "binding_" + ref, Status: capabilities.RuntimeBindingStatusActive}}, nil
		}
	}
	return capabilities.CapabilityInspection{}, fmt.Errorf("endpoint not found: %s", ref)
}
func (f *fakeEndpointRegistry) DisableCapabilityEndpoint(_ context.Context, _ requestctx.Context, ref string, _ json.RawMessage) (capabilities.CapabilityEndpoint, error) {
	f.disabled = append(f.disabled, ref)
	return capabilities.CapabilityEndpoint{}, nil
}
func (f *fakeEndpointRegistry) DisableRuntimeBinding(_ context.Context, _ requestctx.Context, ref string, _ json.RawMessage) (capabilities.EndpointRuntimeBinding, error) {
	f.disabledBindings = append(f.disabledBindings, ref)
	return capabilities.EndpointRuntimeBinding{RuntimeBindingID: ref, Status: capabilities.RuntimeBindingStatusDisabled}, nil
}

func TestCompileServiceEndpointsUsesAllowlistIntersection(t *testing.T) {
	record := testAllowlistRecord()
	record.Operations = []Operation{OperationStatus, OperationRestart}
	profile, err := BuildRuntimeProfile(RuntimeProfileInput{Manager: ManagerLaunchd, Unit: record.Unit, ServiceClass: ServiceClassProject, Operations: []Operation{OperationStatus, OperationStart, OperationRestart}})
	if err != nil {
		t.Fatal(err)
	}
	registry := &fakeEndpointRegistry{endpoints: []capabilities.CapabilityEndpoint{{CapabilityEndpointID: "old", CompactAddress: "macbook@example.service.start", Status: capabilities.EndpointStatusActive, Metadata: json.RawMessage(`{"source":"service_registry"}`)}}}
	provider := capabilities.Provider{ProviderID: "prov_test", CompactAddress: "macbook@example", Status: capabilities.ProviderStatusActive, NodeID: "node_test"}
	result, err := CompileServiceEndpoints(context.Background(), requestctx.Context{ActorID: "actor_test"}, registry, provider, profile, record)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.EffectiveOperations) != 2 || len(result.Endpoints) != 2 || len(result.Disabled) != 1 {
		t.Fatalf("intersection: %#v", result)
	}
	for _, binding := range registry.bindings {
		if binding.RuntimeKind != capabilities.RuntimeKindServiceManager || string(binding.RuntimeConfigJSON) == "" {
			t.Fatalf("unsafe binding: %#v", binding)
		}
	}
	for _, endpoint := range result.Endpoints {
		if endpoint.EndpointName == "service.restart" && endpoint.ExecutionAuthorizationLevel != 4 {
			t.Fatalf("restart auth=%d", endpoint.ExecutionAuthorizationLevel)
		}
	}
}

func TestCompileServiceEndpointsRejectsDisabledProviderAndUnitMismatch(t *testing.T) {
	record := testAllowlistRecord()
	profile, _ := BuildRuntimeProfile(RuntimeProfileInput{Manager: ManagerLaunchd, Unit: record.Unit, ServiceClass: ServiceClassProject})
	provider := capabilities.Provider{Status: capabilities.ProviderStatusDisabled}
	if _, err := CompileServiceEndpoints(context.Background(), requestctx.Context{}, &fakeEndpointRegistry{}, provider, profile, record); err == nil {
		t.Fatal("disabled provider compiled")
	}
	provider.Status = capabilities.ProviderStatusActive
	record.Unit = "com.other.service"
	if _, err := CompileServiceEndpoints(context.Background(), requestctx.Context{}, &fakeEndpointRegistry{}, provider, profile, record); err == nil {
		t.Fatal("mismatched unit compiled")
	}
}

func TestServiceOperationOutputSchemaDescribesBoundedManagerResult(t *testing.T) {
	var schema struct {
		Type                 string   `json:"type"`
		Required             []string `json:"required"`
		AdditionalProperties bool     `json:"additionalProperties"`
		Properties           map[string]struct {
			Type      string         `json:"type"`
			Enum      []string       `json:"enum"`
			MaxLength int            `json:"maxLength"`
			MaxItems  int            `json:"maxItems"`
			Minimum   *int64         `json:"minimum"`
			Maximum   *int64         `json:"maximum"`
			Items     map[string]any `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(serviceOperationOutputSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Type != "object" || schema.AdditionalProperties || len(schema.Properties) != 7 {
		t.Fatalf("schema envelope = %#v", schema)
	}
	for _, field := range []string{"operation", "success", "process_state"} {
		if !containsString(schema.Required, field) {
			t.Fatalf("required fields = %#v, missing %s", schema.Required, field)
		}
	}
	if got := schema.Properties["operation"].Enum; len(got) != len(StandardOperations()) {
		t.Fatalf("operation enum = %#v", got)
	}
	if got := schema.Properties["process_state"].Enum; len(got) != 5 {
		t.Fatalf("process_state enum = %#v", got)
	}
	if schema.Properties["message"].MaxLength != maximumMessageBytes {
		t.Fatalf("message bound = %d", schema.Properties["message"].MaxLength)
	}
	logs := schema.Properties["log_lines"]
	if logs.MaxItems != MaximumLogLines || logs.Items["type"] != "string" || int(logs.Items["maxLength"].(float64)) != MaximumLogLineBytes {
		t.Fatalf("log bounds = %#v", logs)
	}
	if schema.Properties["exit_code"].Minimum == nil || schema.Properties["exit_code"].Maximum == nil {
		t.Fatal("exit_code is not bounded")
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
