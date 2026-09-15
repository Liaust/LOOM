package serviceregistry

import (
	"reflect"
	"strings"
	"testing"

	"loom.local/loom/internal/projectcontracts"
)

func TestMarshalRuntimeProfileIsDeterministicAndBounded(t *testing.T) {
	input := RuntimeProfileInput{
		Manager: ManagerSystemd, Unit: "loom-project-api.service", ServiceClass: ServiceClassProject,
		Operations: []Operation{OperationLogs, OperationStatus, OperationRestart, OperationStatus},
		Health:     RuntimeHealth{Kind: HealthKindManager},
		References: RuntimeReferences{
			Protection:  []string{"project-api-data", "archive", "project-api-data"},
			Exposure:    []string{"nix:project-api-private"},
			Credentials: []string{"project_api.runtime_token"},
		},
	}
	first, err := MarshalRuntimeProfile(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := MarshalRuntimeProfile(input)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":"loom.service_runtime.v1","manager":"systemd","unit":"loom-project-api.service","service_class":"project","operations":["status","restart","logs"],"health":{"kind":"manager"},"references":{"protection":["archive","project-api-data"],"exposure":["nix:project-api-private"],"credentials":["project_api.runtime_token"]}}`
	if string(first) != want || string(second) != want {
		t.Fatalf("runtime profile was not deterministic:\nfirst=%s\nsecond=%s\nwant=%s", first, second, want)
	}
	for _, forbidden := range []string{"command", "executable", "package", "port", "secret", "caddy", "firewall"} {
		if strings.Contains(string(first), forbidden) {
			t.Fatalf("runtime profile contains forbidden provisioning field %q: %s", forbidden, first)
		}
	}
}

func TestRuntimeProfileDefaultsToInspectOnly(t *testing.T) {
	payload, err := MarshalRuntimeProfile(RuntimeProfileInput{Manager: ManagerLaunchd, Unit: "local.loom.example", ServiceClass: ServiceClassSystem})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"operations":["status","logs"]`) {
		t.Fatalf("inspect-only default missing: %s", payload)
	}
}

func TestContractOperationPresenceSurvivesRuntimeProfileProjection(t *testing.T) {
	tests := []struct {
		name       string
		operations string
		want       []Operation
	}{
		{name: "absent defaults inspect-only", want: []Operation{OperationStatus, OperationLogs}},
		{
			name: "explicit all false stays empty",
			operations: `operations:
  status: false
  start: false
  stop: false
  restart: false
  logs: false
`,
			want: []Operation{},
		},
		{
			name: "explicit selection stays selected",
			operations: `operations:
  status: true
  restart: true
`,
			want: []Operation{OperationStatus, OperationRestart},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := `kind: service_registration
schema_version: loom.service.v0.1
service:
  key: project-api
  name: Project API
  target_node: main
runtime:
  manager: systemd
  unit: loom-project-api.service
health:
  kind: manager
` + test.operations
			contract, err := projectcontracts.ParseServiceRegistrationContract([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			requested := contract.RequestedOperations()
			operations := make([]Operation, len(requested))
			for index, operation := range requested {
				operations[index] = Operation(operation)
			}
			profile, err := BuildRuntimeProfile(RuntimeProfileInput{
				Manager: Manager(contract.Runtime.Manager), Unit: contract.Runtime.Unit,
				ServiceClass: ServiceClass(contract.Service.Class), Operations: operations,
				Health: RuntimeHealth{Kind: HealthKind(contract.Health.Kind)},
			})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(profile.Operations, test.want) {
				t.Fatalf("operations = %#v, want %#v; requested nil=%t profile nil=%t", profile.Operations, test.want, requested == nil, profile.Operations == nil)
			}
		})
	}
}

func TestRuntimeProfileRejectsUnboundedInputs(t *testing.T) {
	valid := RuntimeProfileInput{Manager: ManagerSystemd, Unit: "loom-example.service", ServiceClass: ServiceClassProject, Operations: []Operation{OperationStatus}}
	tests := []struct {
		name   string
		mutate func(*RuntimeProfileInput)
	}{
		{name: "manager", mutate: func(input *RuntimeProfileInput) { input.Manager = "shell" }},
		{name: "unit path", mutate: func(input *RuntimeProfileInput) { input.Unit = "/etc/systemd/system/example.service" }},
		{name: "operation", mutate: func(input *RuntimeProfileInput) { input.Operations = []Operation{"exec"} }},
		{name: "credential URI", mutate: func(input *RuntimeProfileInput) { input.References.Credentials = []string{"pass://secret/value"} }},
		{name: "exposure path", mutate: func(input *RuntimeProfileInput) { input.References.Exposure = []string{"/etc/caddy/Caddyfile"} }},
		{name: "raw HTTP probe", mutate: func(input *RuntimeProfileInput) {
			input.Health = RuntimeHealth{Kind: HealthKindHTTP, ProbeRef: "http://127.0.0.1:8080/health"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			test.mutate(&input)
			if _, err := BuildRuntimeProfile(input); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
