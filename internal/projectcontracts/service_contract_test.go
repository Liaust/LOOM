package projectcontracts

import (
	"reflect"
	"strings"
	"testing"
)

const validProjectServiceYAML = `kind: service_registration
schema_version: loom.service.v0.1
service:
  key: project-api
  name: Project API
  description: Local API for the project.
  target_node: main
runtime:
  manager: systemd
  unit: loom-project-api.service
operations:
  status: true
  start: true
  stop: true
  restart: true
  logs: true
health:
  kind: manager
references:
  protection:
    - project-api-data
  exposure:
    - nix:project-api-private
  credentials:
    - project_api.runtime_token
`

func TestParseServiceRegistrationContractValidDeclarations(t *testing.T) {
	tests := []struct {
		name      string
		yaml      string
		wantClass string
		wantMgr   string
		wantUnit  string
		wantOps   []string
	}{
		{
			name:      "project systemd",
			yaml:      validProjectServiceYAML,
			wantClass: ServiceClassProject, wantMgr: ServiceManagerSystemd, wantUnit: "loom-project-api.service",
			wantOps: []string{"status", "start", "stop", "restart", "logs"},
		},
		{
			name: "project Unicode name and multiline description",
			yaml: strings.Replace(
				strings.Replace(validProjectServiceYAML, "name: Project API", "name: Project API 服务", 1),
				"description: Local API for the project.", "description: |-\n    Local API for the project.\n    Operator-owned.", 1,
			),
			wantClass: ServiceClassProject, wantMgr: ServiceManagerSystemd, wantUnit: "loom-project-api.service",
			wantOps: []string{"status", "start", "stop", "restart", "logs"},
		},
		{
			name: "system launchd",
			yaml: `kind: service_registration
schema_version: loom.service.v0.1
service:
  key: node-agent
  name: Node Agent
  target_node: macbook
  class: system
runtime:
  manager: launchd
  unit: local.loom.node-agent
health:
  kind: manager
`,
			wantClass: ServiceClassSystem, wantMgr: ServiceManagerLaunchd, wantUnit: "local.loom.node-agent",
			wantOps: []string{"status", "logs"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contract, err := ParseServiceRegistrationContract([]byte(test.yaml))
			if err != nil {
				t.Fatal(err)
			}
			if contract.Service.Class != test.wantClass || contract.Runtime.Manager != test.wantMgr || contract.Runtime.Unit != test.wantUnit {
				t.Fatalf("normalized contract mismatch: %#v", contract)
			}
			if got := contract.RequestedOperations(); !reflect.DeepEqual(got, test.wantOps) {
				t.Fatalf("operations = %v, want %v", got, test.wantOps)
			}
		})
	}
}

func TestParseServiceRegistrationContractRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		old  string
		new  string
	}{
		{name: "schema", old: "loom.service.v0.1", new: "loom.service.v9"},
		{name: "manager", old: "manager: systemd", new: "manager: shell"},
		{name: "systemd unit", old: "unit: loom-project-api.service", new: "unit: ../../etc/shadow"},
		{name: "launchd unit", old: "manager: systemd\n  unit: loom-project-api.service", new: "manager: launchd\n  unit: /Library/LaunchDaemons/evil.plist"},
		{name: "class", old: "target_node: main", new: "target_node: main\n  class: deployment"},
		{name: "health", old: "kind: manager", new: "kind: http"},
		{name: "credential ref", old: "project_api.runtime_token", new: "pass://secret/value"},
		{name: "name newline", old: "name: Project API", new: `name: "Project\nAPI"`},
		{name: "name tab", old: "name: Project API", new: `name: "Project\tAPI"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseServiceRegistrationContract([]byte(strings.Replace(validProjectServiceYAML, test.old, test.new, 1)))
			if err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestStrictServiceRegistrationContractRejectsProvisioningFields(t *testing.T) {
	tests := []struct {
		name  string
		field string
	}{
		{name: "operation", field: "  execute: true\n"},
		{name: "inline command", field: "command: systemctl restart anything\n"},
		{name: "secret", field: "secret: raw-value\n"},
		{name: "caddy", field: "caddy: reverse_proxy localhost:8080\n"},
		{name: "package", field: "package: https://example.invalid/service.tar\n"},
		{name: "executable", field: "executable: /usr/local/bin/service\n"},
		{name: "port", field: "port: 8080\n"},
		{name: "unit body", field: "unit_text: '[Service]'\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			yaml := validProjectServiceYAML + test.field
			if test.name == "operation" {
				yaml = strings.Replace(validProjectServiceYAML, "  status: true\n", "  status: true\n"+test.field, 1)
			}
			_, err := ParseServiceRegistrationContract([]byte(yaml))
			if err == nil {
				t.Fatal("strict decoding accepted forbidden field")
			}
			if !strings.Contains(err.Error(), "field") {
				t.Fatalf("error did not identify a strict-field failure: %v", err)
			}
		})
	}
}

func TestServiceRegistrationContractRejectsMultipleDocuments(t *testing.T) {
	_, err := ParseServiceRegistrationContract([]byte(validProjectServiceYAML + "---\nkind: service_registration\n"))
	if err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("multiple document error = %v", err)
	}
}
