package nodeagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/routing"
)

func TestServiceManagerDispatchFailsClosedOnAllowlistIntersection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allowlist.yaml")
	raw := "records:\n  - schema_version: loom.service_allowlist.v1\n    key: example\n    node_key: macbook\n    manager: launchd\n    unit: com.example.service\n    operations: [status, restart]\n    lifecycle_policy: service_operations\n    health: {kind: manager}\n    log_limits: {}\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	address := "macbook@example.service.status"
	dispatch := routing.RemoteDispatchPayload{CapabilityAddress: address, Operation: "capability:" + address, RuntimeBinding: &routing.RuntimeBindingSnapshot{RuntimeKind: capabilities.RuntimeKindServiceManager, RuntimeConfigJSON: json.RawMessage(`{"allowlist_key":"example","operation":"start"}`), Status: capabilities.RuntimeBindingStatusActive}, Input: json.RawMessage(`{}`)}
	if _, err := executeRuntimeBindingDispatch(context.Background(), Config{NodeKey: "macbook", ServiceManager: ServiceManagerConfig{AllowlistPath: path}}, State{}, dispatch); err == nil {
		t.Fatal("disallowed operation reached launchd")
	}
	dispatch.RuntimeBinding.RuntimeConfigJSON = json.RawMessage(`{"allowlist_key":"example","operation":"status","argv":["sh"]}`)
	if _, err := executeRuntimeBindingDispatch(context.Background(), Config{NodeKey: "macbook", ServiceManager: ServiceManagerConfig{AllowlistPath: path}}, State{}, dispatch); err == nil {
		t.Fatal("arbitrary runtime config accepted")
	}
	dispatch.RuntimeBinding.RuntimeConfigJSON = json.RawMessage(`{"allowlist_key":"example","operation":"status"}`)
	for _, input := range []json.RawMessage{
		json.RawMessage(`{"lines":-1}`),
		json.RawMessage(`{"max_bytes":-1}`),
		json.RawMessage(`{"max_age_seconds":-1}`),
		json.RawMessage(`{} {}`),
	} {
		dispatch.Input = input
		if _, err := executeRuntimeBindingDispatch(context.Background(), Config{NodeKey: "macbook", ServiceManager: ServiceManagerConfig{AllowlistPath: path}}, State{}, dispatch); err == nil {
			t.Fatalf("invalid node dispatch input accepted: %s", input)
		}
	}
	dispatch.Input = json.RawMessage(`{}`)
	dispatch.RuntimeBinding.RuntimeConfigJSON = json.RawMessage(`{"allowlist_key":"example","operation":"restart"}`)
	if _, err := executeRuntimeBindingDispatch(context.Background(), Config{NodeKey: "macbook", ServiceManager: ServiceManagerConfig{AllowlistPath: path}}, State{}, dispatch); err == nil {
		t.Fatal("status endpoint invoked restart from a tampered runtime snapshot")
	} else if code, _, ok := routing.RuntimeFailureCode(err); !ok || code != "node_agent.service_operation_mismatch" {
		t.Fatalf("operation mismatch error = %v code=%q", err, code)
	}
}

func TestProjectArchiveServiceFenceRefusesSupportedStartAndRestartDispatch(t *testing.T) {
	for _, operation := range []string{"start", "restart"} {
		t.Run(operation, func(t *testing.T) {
			environment := newProjectArchiveQuiescenceEnvironment(t)
			if _, err := environment.service.Quiesce(context.Background(), environment.request); err != nil {
				t.Fatal(err)
			}
			address := "workspace/node-a@service-one.service." + operation
			dispatch := routing.RemoteDispatchPayload{
				CapabilityAddress: address,
				Operation:         "capability:" + address,
				Input:             json.RawMessage(`{}`),
				RuntimeBinding: &routing.RuntimeBindingSnapshot{
					RuntimeKind:       capabilities.RuntimeKindServiceManager,
					RuntimeConfigJSON: json.RawMessage(`{"allowlist_key":"service-one","operation":"` + operation + `"}`),
					Status:            capabilities.RuntimeBindingStatusActive,
				},
			}
			_, err := executeRuntimeBindingDispatch(context.Background(), environment.service.Config, State{}, dispatch, environment.service.Store)
			if err == nil {
				t.Fatalf("fenced service %s succeeded", operation)
			}
			code, _, _ := routing.RuntimeFailureCode(err)
			if code != "node_agent.service_project_archive_fenced" {
				t.Fatalf("fenced service %s code = %q, err=%v", operation, code, err)
			}
		})
	}
}
