package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/capabilityruntime"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/projectquiescence"
	"loom.local/loom/internal/routing"
	"loom.local/loom/internal/serviceregistry"
)

func executeRuntimeBindingDispatch(ctx context.Context, config Config, state State, dispatch routing.RemoteDispatchPayload, stores ...Store) (routing.ExecutionResult, error) {
	if dispatch.RuntimeBinding == nil {
		return routing.ExecutionResult{}, fmt.Errorf("runtime binding snapshot is required")
	}
	snapshot := dispatch.RuntimeBinding
	if strings.TrimSpace(snapshot.Status) != capabilities.RuntimeBindingStatusActive {
		return routing.ExecutionResult{}, routing.NewExecutionFailure("node_agent.invalid_runtime_snapshot", "runtime binding snapshot is not active", nil)
	}
	binding := capabilities.EndpointRuntimeBinding{
		RuntimeBindingID:            snapshot.RuntimeBindingID,
		CapabilityEndpointVersionID: snapshot.CapabilityEndpointVersionID,
		RuntimeKind:                 snapshot.RuntimeKind,
		RuntimeConfigJSON:           snapshot.RuntimeConfigJSON,
		InputMappingJSON:            snapshot.InputMappingJSON,
		OutputMappingJSON:           snapshot.OutputMappingJSON,
		Status:                      snapshot.Status,
	}
	execCtx := routing.ExecutionContext{
		RouteID:                 dispatch.RouteID,
		CapabilityCallID:        dispatch.CapabilityCallID,
		CorrelationID:           filesystemDispatchCorrelationID(dispatch),
		ActorID:                 dispatch.ActorID,
		OriginNodeID:            dispatch.OriginNodeID,
		ScopeID:                 dispatch.ScopeID,
		TargetNodeID:            state.NodeID,
		ProviderID:              dispatch.ProviderID,
		CapabilityEndpointID:    dispatch.CapabilityEndpointID,
		ActiveEndpointVersionID: snapshot.CapabilityEndpointVersionID,
		Operation:               dispatch.Operation,
		PolicyDecisionID:        dispatch.PolicyDecisionID,
		GrantID:                 dispatch.GrantID,
	}
	switch snapshot.RuntimeKind {
	case capabilities.RuntimeKindCommand:
		return routing.NewCommandRuntimeExecutor().Execute(ctx, execCtx, binding, dispatch.Input)
	case capabilities.RuntimeKindHTTP:
		return routing.NewHTTPRuntimeExecutor().Execute(ctx, execCtx, binding, dispatch.Input)
	case capabilities.RuntimeKindServiceManager:
		return executeServiceManagerDispatch(ctx, config, dispatch, stores...)
	default:
		return routing.ExecutionResult{}, routing.NewExecutionFailure("node_agent.unsupported_runtime_kind", "node-agent does not support runtime kind "+snapshot.RuntimeKind, nil)
	}
}

type serviceManagerDispatchInput struct {
	Lines         int `json:"lines,omitempty"`
	MaxBytes      int `json:"max_bytes,omitempty"`
	MaxAgeSeconds int `json:"max_age_seconds,omitempty"`
}

func executeServiceManagerDispatch(ctx context.Context, config Config, dispatch routing.RemoteDispatchPayload, stores ...Store) (routing.ExecutionResult, error) {
	if dispatch.RuntimeBinding == nil {
		return routing.ExecutionResult{}, fmt.Errorf("runtime binding snapshot is required")
	}
	runtimeConfig, validation := capabilityruntime.DecodeServiceManager(dispatch.RuntimeBinding.RuntimeConfigJSON)
	if !validation.Valid {
		return routing.ExecutionResult{}, routing.NewExecutionFailure("node_agent.invalid_service_manager_binding", "service manager binding is invalid", nil)
	}
	if !serviceManagerDispatchMatchesRuntimeOperation(dispatch, runtimeConfig.Operation) {
		return routing.ExecutionResult{}, routing.NewExecutionFailure("node_agent.service_operation_mismatch", "service manager binding operation does not match dispatched capability", nil)
	}
	var input serviceManagerDispatchInput
	if len(dispatch.Input) > 0 && string(dispatch.Input) != "null" {
		decoder := json.NewDecoder(strings.NewReader(string(dispatch.Input)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			return routing.ExecutionResult{}, routing.NewExecutionFailure("node_agent.invalid_service_manager_input", "service input supports only bounded log selectors", nil)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return routing.ExecutionResult{}, routing.NewExecutionFailure("node_agent.invalid_service_manager_input", "service input must contain one bounded JSON object", nil)
		}
	}
	globalLimits := serviceregistry.LogLimits{MaxLines: serviceregistry.MaximumLogLines, MaxBytes: serviceregistry.MaximumLogBytes, MaxLineBytes: serviceregistry.MaximumLogLineBytes, MaxAgeSeconds: serviceregistry.MaximumLogAgeSeconds}
	if err := serviceregistry.ValidateLogSelectors(input.Lines, input.MaxBytes, input.MaxAgeSeconds, globalLimits); err != nil {
		return routing.ExecutionResult{}, routing.NewExecutionFailure("node_agent.invalid_service_log_selectors", "service log selectors are outside bounded limits", nil)
	}
	allowlist, err := serviceregistry.LoadAllowlist(config.ServiceManager.AllowlistPath)
	if err != nil {
		return routing.ExecutionResult{}, routing.NewExecutionFailure("node_agent.service_allowlist_unavailable", "reviewed service allowlist is unavailable", nil)
	}
	record, ok := allowlist.Lookup(runtimeConfig.AllowlistKey)
	if !ok || record.NodeKey != config.NodeKey {
		return routing.ExecutionResult{}, routing.NewExecutionFailure("node_agent.service_not_allowlisted", "service is not allowlisted for this node", nil)
	}
	var release func() error
	if len(stores) > 0 && strings.TrimSpace(stores[0].DataDir) != "" {
		runtimeStore := noderuntime.NewStore(stores[0].DataDir)
		release, err = runtimeStore.AcquireServiceExecutionLock(ctx, runtimeConfig.AllowlistKey)
		if err != nil {
			return routing.ExecutionResult{}, routing.NewExecutionFailure("node_agent.service_lock_unavailable", "service execution lock is unavailable", nil)
		}
		defer func() { _ = release() }()
		if runtimeConfig.Operation == string(serviceregistry.OperationStart) || runtimeConfig.Operation == string(serviceregistry.OperationRestart) {
			fenced, fenceErr := runtimeStore.ProjectArchiveFenceActive(projectquiescence.TargetKindService, runtimeConfig.AllowlistKey)
			if fenceErr != nil {
				return routing.ExecutionResult{}, routing.NewExecutionFailure("node_agent.service_fence_unreadable", "service project-archive fence is unreadable", nil)
			}
			if fenced {
				return routing.ExecutionResult{}, routing.NewExecutionFailure("node_agent.service_project_archive_fenced", "service is fenced for project archive", nil)
			}
		}
	}
	manager := serviceregistry.ManagerService{Allowlist: allowlist, Launchd: serviceregistry.LaunchdUserRunner{}, Systemd: serviceregistry.HelperClientRunner{Path: config.ServiceManager.HelperPath, AllowlistPath: config.ServiceManager.AllowlistPath}}
	result, err := manager.Execute(ctx, serviceregistry.ManagerRequest{AllowlistKey: runtimeConfig.AllowlistKey, Operation: serviceregistry.Operation(runtimeConfig.Operation), LogLines: input.Lines, LogMaxBytes: input.MaxBytes, LogMaxAgeSec: input.MaxAgeSeconds})
	if err != nil {
		return routing.ExecutionResult{}, routing.NewExecutionFailure("node_agent.service_manager_failed", "service manager operation failed", nil)
	}
	payload, _ := json.Marshal(result)
	return routing.ExecutionResult{Status: "succeeded", Result: payload, ResultRefs: json.RawMessage(`{}`)}, nil
}

func serviceManagerDispatchMatchesRuntimeOperation(dispatch routing.RemoteDispatchPayload, operation string) bool {
	address := strings.TrimSpace(dispatch.CapabilityAddress)
	operation = strings.TrimSpace(operation)
	if address == "" || operation == "" || !strings.HasSuffix(address, ".service."+operation) {
		return false
	}
	return dispatch.Operation == "capability:"+address
}

func runtimeDispatchMetadata(config Config, state State, messageID string, dispatch routing.RemoteDispatchPayload) json.RawMessage {
	snapshotHash := ""
	runtimeKind := ""
	if dispatch.RuntimeBinding != nil {
		snapshotHash = dispatch.RuntimeBinding.SnapshotHash
		runtimeKind = dispatch.RuntimeBinding.RuntimeKind
	}
	return objectJSON(map[string]any{
		"source":                  "loom-node-agent",
		"node_key":                config.NodeKey,
		"runtime_class":           config.RuntimeClass,
		"dispatch_message_id":     messageID,
		"capability_call_id":      dispatch.CapabilityCallID,
		"communication_transport": "poll_ack_result",
		"runtime_kind":            runtimeKind,
		"runtime_snapshot_hash":   snapshotHash,
		"node_id":                 state.NodeID,
	})
}
