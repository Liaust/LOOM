package nodeagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/routing"
)

func TestBuildCapabilityResultInputExecutesRuntimeSnapshot(t *testing.T) {
	dispatch := routing.RemoteDispatchPayload{
		RouteID:              "route_test",
		CapabilityCallID:     "call_test",
		TargetNodeID:         "node_test",
		ProviderID:           "provider_test",
		ProviderAddress:      "workspace@example_connector",
		CapabilityEndpointID: "endp_test",
		CapabilityAddress:    "workspace@example_connector.ping",
		Operation:            "workspace@example_connector.ping",
		ActorID:              "actor_test",
		OriginNodeID:         "main_node",
		Input:                json.RawMessage(`{}`),
		RequestedAt:          time.Now().UTC(),
		RuntimeBinding: &routing.RuntimeBindingSnapshot{
			RuntimeBindingID:            "runtime_binding_test",
			CapabilityEndpointVersionID: "endpv_test",
			RuntimeKind:                 capabilities.RuntimeKindCommand,
			RuntimeConfigJSON:           json.RawMessage(`{"argv":["printf","{\"ok\":true}"]}`),
			InputMappingJSON:            json.RawMessage(`{}`),
			OutputMappingJSON:           json.RawMessage(`{}`),
			Status:                      capabilities.RuntimeBindingStatusActive,
			SnapshotHash:                "sha256:test",
			CapturedAt:                  time.Now().UTC(),
		},
	}
	payload, err := json.Marshal(dispatch)
	if err != nil {
		t.Fatalf("marshal dispatch: %v", err)
	}
	input, err := buildCapabilityResultInput(context.Background(), Config{NodeKey: "workspace", RuntimeClass: "dev"}, State{NodeID: "node_test", CredentialToken: "cred_test"}, Store{}, communication.Message{
		CommunicationMessageID: "message_test",
		Kind:                   communication.KindCapabilityDispatch,
		PayloadJSON:            payload,
	})
	if err != nil {
		t.Fatalf("buildCapabilityResultInput returned error: %v", err)
	}
	if input.Payload.ExecutionStatus != routing.CapabilityCallStatusCompleted {
		t.Fatalf("execution status = %s error=%s", input.Payload.ExecutionStatus, input.Payload.ErrorMessage)
	}
	if string(input.Payload.ResultJSON) != `{"ok":true}` {
		t.Fatalf("result = %s", input.Payload.ResultJSON)
	}
	var metadata map[string]any
	if err := json.Unmarshal(input.Payload.RuntimeMetadataJSON, &metadata); err != nil {
		t.Fatalf("runtime metadata is not JSON: %v", err)
	}
	if metadata["runtime_kind"] != capabilities.RuntimeKindCommand || metadata["runtime_snapshot_hash"] != "sha256:test" {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func TestBuildCapabilityResultInputExecutesHTTPRuntimeSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"runtime":"http"}`))
	}))
	defer server.Close()

	dispatch := routing.RemoteDispatchPayload{
		RouteID:              "route_test",
		CapabilityCallID:     "call_test",
		TargetNodeID:         "node_test",
		ProviderID:           "provider_test",
		ProviderAddress:      "workspace@example_connector",
		CapabilityEndpointID: "endp_test",
		CapabilityAddress:    "workspace@example_connector.ping",
		Operation:            "workspace@example_connector.ping",
		ActorID:              "actor_test",
		OriginNodeID:         "main_node",
		Input:                json.RawMessage(`{}`),
		RequestedAt:          time.Now().UTC(),
		RuntimeBinding: &routing.RuntimeBindingSnapshot{
			RuntimeBindingID:            "runtime_binding_test",
			CapabilityEndpointVersionID: "endpv_test",
			RuntimeKind:                 capabilities.RuntimeKindHTTP,
			RuntimeConfigJSON:           json.RawMessage(`{"url":"` + server.URL + `"}`),
			InputMappingJSON:            json.RawMessage(`{}`),
			OutputMappingJSON:           json.RawMessage(`{}`),
			Status:                      capabilities.RuntimeBindingStatusActive,
			SnapshotHash:                "sha256:http-test",
			CapturedAt:                  time.Now().UTC(),
		},
	}
	payload, err := json.Marshal(dispatch)
	if err != nil {
		t.Fatalf("marshal dispatch: %v", err)
	}
	input, err := buildCapabilityResultInput(context.Background(), Config{NodeKey: "workspace", RuntimeClass: "dev"}, State{NodeID: "node_test", CredentialToken: "cred_test"}, Store{}, communication.Message{
		CommunicationMessageID: "message_test",
		Kind:                   communication.KindCapabilityDispatch,
		PayloadJSON:            payload,
	})
	if err != nil {
		t.Fatalf("buildCapabilityResultInput returned error: %v", err)
	}
	if input.Payload.ExecutionStatus != routing.CapabilityCallStatusCompleted {
		t.Fatalf("execution status = %s error=%s", input.Payload.ExecutionStatus, input.Payload.ErrorMessage)
	}
	var result map[string]any
	if err := json.Unmarshal(input.Payload.ResultJSON, &result); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	if result["runtime"] != "http" || result["ok"] != true {
		t.Fatalf("result = %#v", result)
	}
}

func TestBuildCapabilityResultInputRejectsUnsupportedRuntimeSnapshot(t *testing.T) {
	dispatch := routing.RemoteDispatchPayload{
		RouteID:              "route_test",
		CapabilityCallID:     "call_test",
		TargetNodeID:         "node_test",
		ProviderID:           "provider_test",
		ProviderAddress:      "workspace@example_connector",
		CapabilityEndpointID: "endp_test",
		CapabilityAddress:    "workspace@example_connector.ping",
		Operation:            "workspace@example_connector.ping",
		ActorID:              "actor_test",
		OriginNodeID:         "main_node",
		Input:                json.RawMessage(`{}`),
		RequestedAt:          time.Now().UTC(),
		RuntimeBinding: &routing.RuntimeBindingSnapshot{
			RuntimeBindingID:            "runtime_binding_test",
			CapabilityEndpointVersionID: "endpv_test",
			RuntimeKind:                 capabilities.RuntimeKindScript,
			RuntimeConfigJSON:           json.RawMessage(`{}`),
			InputMappingJSON:            json.RawMessage(`{}`),
			OutputMappingJSON:           json.RawMessage(`{}`),
			Status:                      capabilities.RuntimeBindingStatusActive,
			SnapshotHash:                "sha256:script-test",
			CapturedAt:                  time.Now().UTC(),
		},
	}
	payload, err := json.Marshal(dispatch)
	if err != nil {
		t.Fatalf("marshal dispatch: %v", err)
	}
	input, err := buildCapabilityResultInput(context.Background(), Config{NodeKey: "workspace", RuntimeClass: "dev"}, State{NodeID: "node_test", CredentialToken: "cred_test"}, Store{}, communication.Message{
		CommunicationMessageID: "message_test",
		Kind:                   communication.KindCapabilityDispatch,
		PayloadJSON:            payload,
	})
	if err != nil {
		t.Fatalf("buildCapabilityResultInput returned error: %v", err)
	}
	if input.Payload.ExecutionStatus != routing.CapabilityCallStatusFailed {
		t.Fatalf("execution status = %s", input.Payload.ExecutionStatus)
	}
	if input.Payload.ErrorCode != "node_agent.unsupported_runtime_kind" {
		t.Fatalf("error code = %s", input.Payload.ErrorCode)
	}
}
