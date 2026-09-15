package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/capabilityruntime"
	"loom.local/loom/internal/requestctx"
)

type RuntimeBindingValidation struct {
	Binding        capabilities.RuntimeBindingInspection `json:"binding"`
	Validation     capabilityruntime.ValidationResult    `json:"validation"`
	RedactedConfig json.RawMessage                       `json:"redacted_config,omitempty"`
}

type RuntimeBindingTestInput struct {
	Input         json.RawMessage `json:"input,omitempty"`
	AllowInactive bool            `json:"allow_inactive,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
}

type RuntimeBindingTestResult struct {
	Binding        capabilities.RuntimeBindingInspection `json:"binding"`
	Status         string                                `json:"status"`
	Result         json.RawMessage                       `json:"result,omitempty"`
	ResultRefs     json.RawMessage                       `json:"result_refs,omitempty"`
	ErrorCode      string                                `json:"error_code,omitempty"`
	ErrorMessage   string                                `json:"error_message,omitempty"`
	RedactedConfig json.RawMessage                       `json:"redacted_config,omitempty"`
}

func (s Service) ValidateRuntimeBinding(ctx context.Context, ref string) (RuntimeBindingValidation, error) {
	inspection, err := capabilities.NewService(s.DB).InspectRuntimeBinding(ctx, ref)
	if err != nil {
		return RuntimeBindingValidation{}, err
	}
	validation := capabilityruntime.NormalizeAndValidate(inspection.Binding.RuntimeKind, inspection.Binding.RuntimeConfigJSON, capabilityruntime.ValidationModeExecute)
	return RuntimeBindingValidation{
		Binding:        inspection,
		Validation:     validation,
		RedactedConfig: validation.RedactedConfig,
	}, nil
}

func (s Service) TestRuntimeBinding(ctx context.Context, req requestctx.Context, ref string, input RuntimeBindingTestInput) (RuntimeBindingTestResult, error) {
	inspection, err := capabilities.NewService(s.DB).InspectRuntimeBinding(ctx, ref)
	if err != nil {
		return RuntimeBindingTestResult{}, err
	}
	result := RuntimeBindingTestResult{
		Binding:        inspection,
		Status:         CapabilityCallStatusFailed,
		RedactedConfig: capabilityruntime.Redact(inspection.Binding.RuntimeKind, inspection.Binding.RuntimeConfigJSON),
	}
	if inspection.Binding.Status != capabilities.RuntimeBindingStatusActive && !input.AllowInactive {
		result.ErrorCode = RuntimeFailureInactive
		result.ErrorMessage = fmt.Sprintf("runtime binding %s is not active: %s", inspection.Binding.RuntimeBindingID, inspection.Binding.Status)
		return result, nil
	}
	validation := capabilityruntime.NormalizeAndValidate(inspection.Binding.RuntimeKind, inspection.Binding.RuntimeConfigJSON, capabilityruntime.ValidationModeExecute)
	if !validation.Valid {
		result.ErrorCode = RuntimeFailureInvalidConfig
		result.ErrorMessage = diagnosticsMessage(validation.Errors)
		result.RedactedConfig = validation.RedactedConfig
		return result, nil
	}
	if s.Registry == nil {
		result.ErrorCode = "runtime_executor.registry_missing"
		result.ErrorMessage = "runtime executor registry is not configured"
		return result, nil
	}
	executor, ok := s.Registry.RuntimeExecutor(inspection.Binding.RuntimeKind)
	if !ok {
		failure := UnsupportedRuntimeExecutorFailure(inspection.Binding.RuntimeKind)
		result.ErrorCode = failure.Code
		result.ErrorMessage = failure.Message
		return result, nil
	}
	execResult, err := executor.Execute(ctx, ExecutionContext{
		RouteID:                 "runtime_binding_test",
		CapabilityCallID:        "runtime_binding_test",
		CorrelationID:           req.CorrelationID,
		ActorID:                 req.ActorID,
		OriginNodeID:            req.OriginNodeID,
		ScopeID:                 req.ScopeID,
		TargetNodeID:            inspection.Provider.NodeID,
		ProviderID:              inspection.Provider.ProviderID,
		CapabilityEndpointID:    inspection.Endpoint.CapabilityEndpointID,
		ActiveEndpointVersionID: inspection.EndpointVersion.CapabilityEndpointVersionID,
		Operation:               inspection.Endpoint.CompactAddress,
	}, inspection.Binding, objectOrDefault(input.Input))
	if err != nil {
		code := "provider.execution_failed"
		message := err.Error()
		if runtimeCode, runtimeMessage, ok := RuntimeFailureCode(err); ok {
			code = runtimeCode
			message = runtimeMessage
		}
		result.ErrorCode = code
		result.ErrorMessage = message
		return result, nil
	}
	status := strings.TrimSpace(execResult.Status)
	if status == "" {
		status = CapabilityCallStatusCompleted
	}
	result.Status = status
	result.Result = objectOrDefault(execResult.Result)
	result.ResultRefs = objectOrDefault(execResult.ResultRefs)
	return result, nil
}
