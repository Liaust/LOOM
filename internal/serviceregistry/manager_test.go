package serviceregistry

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeManagerRunner struct {
	result ManagerResult
	err    error
	calls  int
}

func (runner *fakeManagerRunner) Run(context.Context, AllowlistRecord, ManagerRequest) (ManagerResult, error) {
	runner.calls++
	return runner.result, runner.err
}

func testAllowlistRecord() AllowlistRecord {
	record, err := NormalizeAndValidateAllowlistRecord(AllowlistRecord{SchemaVersion: AllowlistSchemaV1, Key: "example", NodeKey: "macbook", Manager: ManagerLaunchd, Unit: "com.example.service", Operations: []Operation{OperationStatus, OperationRestart, OperationLogs}, LogLimits: LogLimits{MaxLines: 10, MaxBytes: 2048, MaxLineBytes: 512, MaxAgeSeconds: 600}})
	if err != nil {
		panic(err)
	}
	return record
}

func TestParseAllowlistIsStrictAndDeterministic(t *testing.T) {
	raw := []byte("records:\n  - schema_version: loom.service_allowlist.v1\n    key: example\n    node_key: macbook\n    manager: launchd\n    unit: com.example.service\n    operations: [status, logs]\n    lifecycle_policy: service_operations\n    health: {kind: manager}\n    log_limits: {max_lines: 10, max_bytes: 2048, max_line_bytes: 512, max_age_seconds: 600}\n")
	list, err := ParseAllowlist(raw)
	if err != nil || len(list.Records) != 1 {
		t.Fatalf("parse: %#v %v", list, err)
	}
	for _, bad := range [][]byte{append(raw, []byte("unknown: true\n")...), []byte("records:\n - schema_version: loom.service_allowlist.v1\n   key: ../../bad\n")} {
		if _, err := ParseAllowlist(bad); err == nil {
			t.Fatalf("accepted invalid allowlist: %s", bad)
		}
	}
}

func TestManagerServiceDenialsAndRedaction(t *testing.T) {
	record := testAllowlistRecord()
	runner := &fakeManagerRunner{result: ManagerResult{Success: true, ProcessState: ProcessStateRunning, Message: "token=secret path:/srv/private\u0085", LogLines: []string{"https://secret.example/path"}}}
	service := ManagerService{Allowlist: Allowlist{Records: []AllowlistRecord{record}}, Launchd: runner}
	tests := []struct {
		name    string
		request ManagerRequest
	}{
		{"unknown unit", ManagerRequest{AllowlistKey: "missing", Operation: OperationStatus}},
		{"disallowed operation", ManagerRequest{AllowlistKey: "example", Operation: OperationStart}},
		{"shell operation", ManagerRequest{AllowlistKey: "example", Operation: "status;rm"}},
		{"oversized logs", ManagerRequest{AllowlistKey: "example", Operation: OperationLogs, LogLines: 11}},
		{"negative log lines", ManagerRequest{AllowlistKey: "example", Operation: OperationLogs, LogLines: -1}},
		{"negative log bytes", ManagerRequest{AllowlistKey: "example", Operation: OperationLogs, LogMaxBytes: -1}},
		{"negative log age", ManagerRequest{AllowlistKey: "example", Operation: OperationLogs, LogMaxAgeSec: -1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := service.Execute(context.Background(), test.request); err == nil {
				t.Fatal("request accepted")
			}
		})
	}
	result, err := service.Execute(context.Background(), ManagerRequest{AllowlistKey: "example", Operation: OperationStatus})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Message, "/srv") || strings.Contains(result.Message, "secret") || strings.Contains(result.Message, "\u0085") {
		t.Fatalf("message not redacted: %q", result.Message)
	}
}

func TestManagerServiceSanitizesRunnerFailure(t *testing.T) {
	runner := &fakeManagerRunner{err: errors.New("failed at /etc/private token=abc123")}
	service := ManagerService{Allowlist: Allowlist{Records: []AllowlistRecord{testAllowlistRecord()}}, Launchd: runner}
	_, err := service.Execute(context.Background(), ManagerRequest{AllowlistKey: "example", Operation: OperationStatus})
	if err == nil || strings.Contains(err.Error(), "/etc/private") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestInspectOnlyCoreCannotMutate(t *testing.T) {
	record := testAllowlistRecord()
	record.LifecyclePolicy = LifecyclePolicyInspectOnlyCore
	if _, err := NormalizeAndValidateAllowlistRecord(record); err == nil {
		t.Fatal("core lifecycle mutation accepted")
	}
}
