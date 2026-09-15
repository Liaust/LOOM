package serviceregistry

import (
	"reflect"
	"strings"
	"testing"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/ids"
)

const validProjectID = "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"

func TestStandardOperationPolicies(t *testing.T) {
	tests := []struct {
		operation    Operation
		capability   string
		form         CapabilityForm
		risk         RiskLevel
		auth         int
		mutation     bool
		confirmation bool
	}{
		{OperationStatus, "service.status", CapabilityFormQuery, RiskLow, 1, false, false},
		{OperationLogs, "service.logs", CapabilityFormQuery, RiskLow, 2, false, false},
		{OperationStart, "service.start", CapabilityFormCommand, RiskHigh, 4, true, true},
		{OperationStop, "service.stop", CapabilityFormCommand, RiskHigh, 4, true, true},
		{OperationRestart, "service.restart", CapabilityFormCommand, RiskHigh, 4, true, true},
	}
	for _, test := range tests {
		t.Run(string(test.operation), func(t *testing.T) {
			policy, err := StandardOperationPolicy(test.operation)
			if err != nil {
				t.Fatal(err)
			}
			if policy.CapabilityName != test.capability || policy.Form != test.form || policy.RiskLevel != test.risk || policy.ExecutionAuthorizationLevel != test.auth || policy.MutatesState != test.mutation || policy.RequiresConfirmation != test.confirmation {
				t.Fatalf("policy mismatch: %#v", policy)
			}
		})
	}
	if _, err := StandardOperationPolicy("exec"); err == nil {
		t.Fatal("arbitrary operation was accepted")
	}
}

func TestProviderAndProcessStatesRemainDistinct(t *testing.T) {
	for _, state := range []ProviderRegistryState{ProviderStateRegistered, ProviderStateActive, ProviderStateDisabled, ProviderStateDeprecated, ProviderStateRevoked} {
		if !ValidProviderRegistryState(state) {
			t.Fatalf("provider state %q rejected", state)
		}
		if ValidObservedProcessState(ObservedProcessState(state)) {
			t.Fatalf("provider state %q was accepted as process state", state)
		}
	}
	for _, state := range []ObservedProcessState{ProcessStateUnknown, ProcessStateRunning, ProcessStateStopped, ProcessStateFailed, ProcessStateUnavailable} {
		if !ValidObservedProcessState(state) {
			t.Fatalf("process state %q rejected", state)
		}
		if ValidProviderRegistryState(ProviderRegistryState(state)) {
			t.Fatalf("process state %q was accepted as provider state", state)
		}
	}
}

func TestServiceVocabularyMatchesExistingProviderAndCapabilityVocabulary(t *testing.T) {
	providerStates := map[ProviderRegistryState]string{
		ProviderStateRegistered: capabilities.ProviderStatusRegistered,
		ProviderStateActive:     capabilities.ProviderStatusActive,
		ProviderStateDisabled:   capabilities.ProviderStatusDisabled,
		ProviderStateDeprecated: capabilities.ProviderStatusDeprecated,
		ProviderStateRevoked:    capabilities.ProviderStatusRevoked,
	}
	for state, existing := range providerStates {
		if string(state) != existing {
			t.Fatalf("provider state %q does not match existing value %q", state, existing)
		}
	}
	for _, operation := range StandardOperations() {
		policy, err := StandardOperationPolicy(operation)
		if err != nil {
			t.Fatal(err)
		}
		if policy.RiskLevel == RiskLow && string(policy.RiskLevel) != capabilities.RiskLevelLow {
			t.Fatalf("low risk mismatch: %#v", policy)
		}
		if policy.RiskLevel == RiskHigh && string(policy.RiskLevel) != capabilities.RiskLevelHigh {
			t.Fatalf("high risk mismatch: %#v", policy)
		}
	}
}

func TestObservedProcessStateProjectsIntoExistingProviderHealth(t *testing.T) {
	tests := []struct {
		state        ObservedProcessState
		health       string
		availability string
	}{
		{ProcessStateUnknown, capabilities.HealthStatusUnknown, capabilities.AvailabilityStatusUnknown},
		{ProcessStateRunning, capabilities.HealthStatusOK, capabilities.AvailabilityStatusAvailable},
		{ProcessStateStopped, capabilities.HealthStatusDegraded, capabilities.AvailabilityStatusUnavailable},
		{ProcessStateFailed, capabilities.HealthStatusUnhealthy, capabilities.AvailabilityStatusUnavailable},
		{ProcessStateUnavailable, capabilities.HealthStatusOffline, capabilities.AvailabilityStatusUnavailable},
	}
	for _, test := range tests {
		projection, err := ProjectProcessState(test.state)
		if err != nil {
			t.Fatal(err)
		}
		if projection.HealthStatus != test.health || projection.AvailabilityStatus != test.availability {
			t.Fatalf("state %q projection = %#v", test.state, projection)
		}
	}
	if _, err := ProjectProcessState(ObservedProcessState(ProviderStateActive)); err == nil {
		t.Fatal("provider registry state projected as an observed process state")
	}
}

func TestNormalizeAndValidateAllowlistRecord(t *testing.T) {
	archiveIdentity := &ProjectArchiveServiceIdentity{
		ProviderKey: "project-api", ProviderAddress: "main@project-api", ProviderID: ids.NewProviderID(), RuntimeProfileDigest: "sha256:" + strings.Repeat("a", 64),
	}
	tests := []struct {
		name    string
		record  AllowlistRecord
		wantErr bool
	}{
		{
			name:   "systemd lifecycle",
			record: AllowlistRecord{SchemaVersion: AllowlistSchemaV1, Key: "project-api", NodeKey: "main", Manager: ManagerSystemd, Unit: "loom-project-api.service", Operations: []Operation{OperationLogs, OperationStatus, OperationRestart}, Health: AllowlistHealth{Kind: HealthKindManager}, ProjectArchiveIdentity: archiveIdentity},
		},
		{
			name:   "launchd inspect-only core",
			record: AllowlistRecord{SchemaVersion: AllowlistSchemaV1, Key: "mini-dashboard", NodeKey: "macbook", Manager: ManagerLaunchd, Unit: "local.loom.mini-dashboard", Operations: []Operation{OperationLogs, OperationStatus}, LifecyclePolicy: LifecyclePolicyInspectOnlyCore, Health: AllowlistHealth{Kind: HealthKindManager}},
		},
		{
			name:   "trusted http probe reference",
			record: AllowlistRecord{SchemaVersion: AllowlistSchemaV1, Key: "project-api", NodeKey: "main", Manager: ManagerSystemd, Unit: "loom-project-api.service", Operations: []Operation{OperationStatus}, Health: AllowlistHealth{Kind: HealthKindHTTP, ProbeRef: "project-api.private-health"}},
		},
		{
			name:   "unit traversal",
			record: AllowlistRecord{SchemaVersion: AllowlistSchemaV1, Key: "bad", NodeKey: "main", Manager: ManagerSystemd, Unit: "../bad.service", Operations: []Operation{OperationStatus}}, wantErr: true,
		},
		{
			name:   "arbitrary operation",
			record: AllowlistRecord{SchemaVersion: AllowlistSchemaV1, Key: "bad", NodeKey: "main", Manager: ManagerSystemd, Unit: "bad.service", Operations: []Operation{"exec"}}, wantErr: true,
		},
		{
			name:   "core mutation",
			record: AllowlistRecord{SchemaVersion: AllowlistSchemaV1, Key: "dashboard", NodeKey: "main", Manager: ManagerSystemd, Unit: "loom-dashboard.service", Operations: []Operation{OperationRestart}, LifecyclePolicy: LifecyclePolicyInspectOnlyCore}, wantErr: true,
		},
		{
			name:   "raw http target",
			record: AllowlistRecord{SchemaVersion: AllowlistSchemaV1, Key: "bad", NodeKey: "main", Manager: ManagerSystemd, Unit: "bad.service", Operations: []Operation{OperationStatus}, Health: AllowlistHealth{Kind: HealthKindHTTP, ProbeRef: "http://127.0.0.1:8080/health"}}, wantErr: true,
		},
		{
			name:   "unbounded logs",
			record: AllowlistRecord{SchemaVersion: AllowlistSchemaV1, Key: "bad", NodeKey: "main", Manager: ManagerSystemd, Unit: "bad.service", Operations: []Operation{OperationLogs}, LogLimits: LogLimits{MaxLines: MaximumLogLines + 1}}, wantErr: true,
		},
		{
			name: "archive identity address mismatch",
			record: AllowlistRecord{SchemaVersion: AllowlistSchemaV1, Key: "project-api", NodeKey: "main", Manager: ManagerSystemd, Unit: "loom-project-api.service", Operations: []Operation{OperationStatus}, ProjectArchiveIdentity: &ProjectArchiveServiceIdentity{
				ProviderKey: "project-api", ProviderAddress: "other@project-api", ProviderID: ids.NewProviderID(), RuntimeProfileDigest: "sha256:" + strings.Repeat("a", 64),
			}}, wantErr: true,
		},
		{
			name: "archive identity provider key mismatch",
			record: AllowlistRecord{SchemaVersion: AllowlistSchemaV1, Key: "project-api", NodeKey: "main", Manager: ManagerSystemd, Unit: "loom-project-api.service", Operations: []Operation{OperationStatus}, ProjectArchiveIdentity: &ProjectArchiveServiceIdentity{
				ProviderKey: "other", ProviderAddress: "main@other", ProviderID: ids.NewProviderID(), RuntimeProfileDigest: "sha256:" + strings.Repeat("a", 64),
			}}, wantErr: true,
		},
		{
			name: "archive identity provider id invalid",
			record: AllowlistRecord{SchemaVersion: AllowlistSchemaV1, Key: "project-api", NodeKey: "main", Manager: ManagerSystemd, Unit: "loom-project-api.service", Operations: []Operation{OperationStatus}, ProjectArchiveIdentity: &ProjectArchiveServiceIdentity{
				ProviderKey: "project-api", ProviderAddress: "main@project-api", ProviderID: "prov_other", RuntimeProfileDigest: "sha256:" + strings.Repeat("a", 64),
			}}, wantErr: true,
		},
		{
			name: "archive identity digest invalid",
			record: AllowlistRecord{SchemaVersion: AllowlistSchemaV1, Key: "project-api", NodeKey: "main", Manager: ManagerSystemd, Unit: "loom-project-api.service", Operations: []Operation{OperationStatus}, ProjectArchiveIdentity: &ProjectArchiveServiceIdentity{
				ProviderKey: "project-api", ProviderAddress: "main@project-api", ProviderID: ids.NewProviderID(), RuntimeProfileDigest: "sha256:not-canonical",
			}}, wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record, err := NormalizeAndValidateAllowlistRecord(test.record)
			if test.wantErr {
				if err == nil {
					t.Fatalf("expected error; record=%#v", record)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if record.LogLimits.MaxLines != DefaultLogLines {
				t.Fatalf("default log limits missing: %#v", record.LogLimits)
			}
		})
	}
}

func TestEffectiveOperationsUsesRequestedAllowlistIntersection(t *testing.T) {
	got := EffectiveOperations(
		[]Operation{OperationRestart, OperationLogs, OperationRestart, OperationStart},
		[]Operation{OperationStatus, OperationRestart, OperationLogs},
	)
	want := []Operation{OperationRestart, OperationLogs}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("effective operations = %v, want %v", got, want)
	}
}

func TestProjectAndSystemRegistrationInputs(t *testing.T) {
	runtime := RuntimeProfileInput{Manager: ManagerSystemd, Unit: "loom-example.service", ServiceClass: ServiceClassProject}
	project := ProjectRegistrationInput{
		ProjectID: validProjectID, ScopeKey: "projects/example", TargetNode: "main", SourcePath: ".loom/contracts/services/example.yaml",
		Service: ServiceIdentity{Key: "example", DisplayName: "Example", Class: ServiceClassProject}, Runtime: runtime,
	}
	if err := project.Validate(); err != nil {
		t.Fatalf("project input: %v", err)
	}
	project.Runtime.ServiceClass = ServiceClassConnector
	if err := project.Validate(); err == nil {
		t.Fatal("project input accepted mismatched service classes")
	}
	system := SystemRegistrationInput{
		SourceKey: "nix-example", ScopeKey: "system", TargetNode: "main",
		Service: ServiceIdentity{Key: "example", DisplayName: "Example", Class: ServiceClassSystem},
		Runtime: RuntimeProfileInput{
			Manager: ManagerSystemd, Unit: "loom-example.service", ServiceClass: ServiceClassSystem,
			Health: RuntimeHealth{Kind: HealthKindHTTP, ProbeRef: "example.private-health"},
		},
	}
	if err := system.Validate(); err != nil {
		t.Fatalf("system input: %v", err)
	}
	system.Service.Class = ServiceClassProject
	if err := system.Validate(); err == nil {
		t.Fatal("system input accepted a project service class")
	}
}

func TestProjectRegistrationValidatesProjectIdentity(t *testing.T) {
	base := ProjectRegistrationInput{
		ProjectID: validProjectID, ScopeKey: "projects/example", TargetNode: "main", SourcePath: ".loom/contracts/services/example.yaml",
		Service: ServiceIdentity{Key: "example", DisplayName: "Example", Class: ServiceClassProject},
		Runtime: RuntimeProfileInput{Manager: ManagerSystemd, Unit: "loom-example.service", ServiceClass: ServiceClassProject},
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("canonical project identity rejected: %v", err)
	}

	tests := []struct {
		name      string
		projectID string
	}{
		{name: "empty", projectID: ""},
		{name: "whitespace", projectID: " "},
		{name: "embedded space", projectID: "project_01ARZ3NDEKTSV4RRFFQ69G5FA "},
		{name: "newline", projectID: validProjectID + "\n"},
		{name: "tab", projectID: validProjectID + "\t"},
		{name: "path separator", projectID: "project/01ARZ3NDEKTSV4RRFFQ69G5FAV"},
		{name: "backslash", projectID: "project\\01ARZ3NDEKTSV4RRFFQ69G5FAV"},
		{name: "wrong prefix", projectID: "proj_01ARZ3NDEKTSV4RRFFQ69G5FAV"},
		{name: "not a ULID", projectID: "project_example"},
		{name: "excessive length", projectID: "project_" + strings.Repeat("A", 1024)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			input.ProjectID = test.projectID
			if err := input.Validate(); err == nil {
				t.Fatal("project registration accepted an invalid project_id")
			}
		})
	}
}

func TestProjectRegistrationRequiresCanonicalContractSourcePath(t *testing.T) {
	base := ProjectRegistrationInput{
		ProjectID: validProjectID, ScopeKey: "projects/example", TargetNode: "main", SourcePath: ".loom/contracts/services/example.yaml",
		Service: ServiceIdentity{Key: "example", DisplayName: "Example", Class: ServiceClassProject},
		Runtime: RuntimeProfileInput{Manager: ManagerSystemd, Unit: "loom-example.service", ServiceClass: ServiceClassProject},
	}
	tests := []struct {
		name       string
		sourcePath string
		wantErr    bool
	}{
		{name: "canonical", sourcePath: ".loom/contracts/services/example.yaml"},
		{name: "empty", sourcePath: "", wantErr: true},
		{name: "POSIX absolute", sourcePath: "/.loom/contracts/services/example.yaml", wantErr: true},
		{name: "Windows drive absolute", sourcePath: `C:\project\.loom\contracts\services\example.yaml`, wantErr: true},
		{name: "Windows UNC", sourcePath: `\\server\share\.loom\contracts\services\example.yaml`, wantErr: true},
		{name: "backslashes", sourcePath: `.loom\contracts\services\example.yaml`, wantErr: true},
		{name: "leading traversal", sourcePath: "../.loom/contracts/services/example.yaml", wantErr: true},
		{name: "embedded traversal", sourcePath: ".loom/contracts/services/../example.yaml", wantErr: true},
		{name: "double separator", sourcePath: ".loom//contracts/services/example.yaml", wantErr: true},
		{name: "current directory component", sourcePath: "./.loom/contracts/services/example.yaml", wantErr: true},
		{name: "wrong service key", sourcePath: ".loom/contracts/services/other.yaml", wantErr: true},
		{name: "leading whitespace", sourcePath: " .loom/contracts/services/example.yaml", wantErr: true},
		{name: "trailing whitespace", sourcePath: ".loom/contracts/services/example.yaml ", wantErr: true},
		{name: "control character", sourcePath: ".loom/contracts/services/example.yaml\n", wantErr: true},
		{name: "unbounded", sourcePath: ".loom/contracts/services/" + strings.Repeat("x", 4096) + ".yaml", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			input.SourcePath = test.sourcePath
			err := input.Validate()
			if test.wantErr && err == nil {
				t.Fatal("project registration accepted a non-canonical source_path")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("canonical source_path rejected: %v", err)
			}
		})
	}
}

func TestRegistrationIdentifiersMustAlreadyBeCanonical(t *testing.T) {
	base := SystemRegistrationInput{
		SourceKey: "nix-example", ScopeKey: "system/services", TargetNode: "main",
		Service: ServiceIdentity{Key: "example", DisplayName: "Example", Class: ServiceClassSystem},
		Runtime: RuntimeProfileInput{Manager: ManagerSystemd, Unit: "loom-example.service", ServiceClass: ServiceClassSystem},
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("canonical identifiers rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*SystemRegistrationInput)
	}{
		{name: "source key padded", mutate: func(input *SystemRegistrationInput) { input.SourceKey = " nix-example " }},
		{name: "source key uppercase", mutate: func(input *SystemRegistrationInput) { input.SourceKey = "Nix-example" }},
		{name: "scope key padded", mutate: func(input *SystemRegistrationInput) { input.ScopeKey = " system/services " }},
		{name: "scope key uppercase", mutate: func(input *SystemRegistrationInput) { input.ScopeKey = "System/services" }},
		{name: "target node padded", mutate: func(input *SystemRegistrationInput) { input.TargetNode = " main " }},
		{name: "target node uppercase", mutate: func(input *SystemRegistrationInput) { input.TargetNode = "Main" }},
		{name: "service key padded", mutate: func(input *SystemRegistrationInput) { input.Service.Key = " example " }},
		{name: "service key uppercase", mutate: func(input *SystemRegistrationInput) { input.Service.Key = "Example" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.mutate(&input)
			if err := input.Validate(); err == nil {
				t.Fatal("registration accepted a non-canonical identifier")
			}
		})
	}

	project := ProjectRegistrationInput{
		ProjectID: validProjectID, ScopeKey: "projects/example", TargetNode: "main", SourcePath: ".loom/contracts/services/example.yaml",
		Service: ServiceIdentity{Key: "example", DisplayName: "Example", Class: ServiceClassProject},
		Runtime: RuntimeProfileInput{Manager: ManagerSystemd, Unit: "loom-example.service", ServiceClass: ServiceClassProject},
	}
	project.ScopeKey = " projects/example "
	if err := project.Validate(); err == nil {
		t.Fatal("project registration accepted a padded scope key")
	}
}

func TestProjectRegistrationRejectsSystemAndTrustedProbeAuthority(t *testing.T) {
	base := ProjectRegistrationInput{
		ProjectID: validProjectID, ScopeKey: "projects/example", TargetNode: "main", SourcePath: ".loom/contracts/services/example.yaml",
		Service: ServiceIdentity{Key: "example", DisplayName: "Example", Class: ServiceClassProject},
		Runtime: RuntimeProfileInput{Manager: ManagerSystemd, Unit: "loom-example.service", ServiceClass: ServiceClassProject, Health: RuntimeHealth{Kind: HealthKindManager}},
	}
	tests := []struct {
		name   string
		mutate func(*ProjectRegistrationInput)
	}{
		{
			name: "system class",
			mutate: func(input *ProjectRegistrationInput) {
				input.Service.Class = ServiceClassSystem
				input.Runtime.ServiceClass = ServiceClassSystem
			},
		},
		{
			name: "http health",
			mutate: func(input *ProjectRegistrationInput) {
				input.Runtime.Health = RuntimeHealth{Kind: HealthKindHTTP, ProbeRef: "example.private-health"}
			},
		},
		{
			name: "probe ref with manager health",
			mutate: func(input *ProjectRegistrationInput) {
				input.Runtime.Health = RuntimeHealth{Kind: HealthKindManager, ProbeRef: "example.private-health"}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.mutate(&input)
			if err := input.Validate(); err == nil {
				t.Fatal("project registration accepted node-configuration authority")
			}
		})
	}
}

func TestRegistrationMetadataIsBoundedAndControlSafe(t *testing.T) {
	valid := SystemRegistrationInput{
		SourceKey: "nix-example", ScopeKey: "system", TargetNode: "main",
		Service: ServiceIdentity{Key: "example", DisplayName: "Example 服务", Description: "Reviewed system service.\nOperator-owned.", Class: ServiceClassSystem},
		Runtime: RuntimeProfileInput{Manager: ManagerSystemd, Unit: "loom-example.service", ServiceClass: ServiceClassSystem},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid Unicode metadata rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*SystemRegistrationInput)
	}{
		{name: "display name too long", mutate: func(input *SystemRegistrationInput) {
			input.Service.DisplayName = strings.Repeat("x", maxServiceDisplayNameBytes+1)
		}},
		{name: "description too long", mutate: func(input *SystemRegistrationInput) {
			input.Service.Description = strings.Repeat("x", maxServiceDescriptionBytes+1)
		}},
		{name: "display name invalid UTF-8", mutate: func(input *SystemRegistrationInput) { input.Service.DisplayName = string([]byte{0xff}) }},
		{name: "description invalid UTF-8", mutate: func(input *SystemRegistrationInput) { input.Service.Description = string([]byte{0xff}) }},
		{name: "display name control", mutate: func(input *SystemRegistrationInput) { input.Service.DisplayName = "Example\x00Service" }},
		{name: "display name newline", mutate: func(input *SystemRegistrationInput) { input.Service.DisplayName = "Example\nService" }},
		{name: "display name tab", mutate: func(input *SystemRegistrationInput) { input.Service.DisplayName = "Example\tService" }},
		{name: "description control", mutate: func(input *SystemRegistrationInput) { input.Service.Description = "Example\x1bService" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			test.mutate(&input)
			if err := input.Validate(); err == nil {
				t.Fatal("registration accepted unsafe provider metadata")
			}
		})
	}
}
