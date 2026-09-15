package projectcontracts

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	ServiceRegistrationKind      = "service_registration"
	ServiceRegistrationSchemaV01 = "loom.service.v0.1"
	ServiceManagerSystemd        = "systemd"
	ServiceManagerLaunchd        = "launchd"
	ServiceClassProject          = "project"
	ServiceClassModule           = "module"
	ServiceClassConnector        = "connector"
	ServiceClassSystem           = "system"
	ServiceHealthKindManager     = "manager"
	ServiceOperationStatus       = "status"
	ServiceOperationStart        = "start"
	ServiceOperationStop         = "stop"
	ServiceOperationRestart      = "restart"
	ServiceOperationLogs         = "logs"
	maxServiceDescriptionBytes   = 2048
	maxServiceReferenceCount     = 32
	maxServiceReferenceBytes     = 128
)

var (
	serviceRegistrationKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
	serviceNodeKeyPattern         = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
	systemdServiceUnitPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@:-]{0,126}\.service$`)
	launchdServiceLabelPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,127}$`)
	serviceProtectionRefPattern   = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,80}$`)
	serviceExposureRefPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	serviceCredentialRefPattern   = regexp.MustCompile(`^[a-z][a-z0-9_-]*(?:\.[a-z][a-z0-9_-]*)+$`)
)

// ServiceRegistrationContract describes an already-provisioned service. Its
// closed shape intentionally has no command, executable, package, listener,
// unit-body, secret, environment, storage-delete, Caddy, or firewall fields.
type ServiceRegistrationContract struct {
	Kind          string                    `json:"kind" yaml:"kind"`
	SchemaVersion string                    `json:"schema_version" yaml:"schema_version"`
	Service       ServiceRegistrationSpec   `json:"service" yaml:"service"`
	Runtime       ServiceRuntimeSpec        `json:"runtime" yaml:"runtime"`
	Operations    ServiceOperationSelection `json:"operations,omitempty" yaml:"operations"`
	Health        ServiceHealthSpec         `json:"health,omitempty" yaml:"health"`
	References    ServiceReferenceSpec      `json:"references,omitempty" yaml:"references"`
}

type ServiceRegistrationSpec struct {
	Key         string `json:"key" yaml:"key"`
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description"`
	TargetNode  string `json:"target_node" yaml:"target_node"`
	Class       string `json:"class,omitempty" yaml:"class"`
}

type ServiceRuntimeSpec struct {
	Manager string `json:"manager" yaml:"manager"`
	Unit    string `json:"unit" yaml:"unit"`
}

// Pointers distinguish an absent operations block, which defaults to the
// inspect-only status/logs pair, from an explicitly disabled operation.
type ServiceOperationSelection struct {
	Status  *bool `json:"status,omitempty" yaml:"status"`
	Start   *bool `json:"start,omitempty" yaml:"start"`
	Stop    *bool `json:"stop,omitempty" yaml:"stop"`
	Restart *bool `json:"restart,omitempty" yaml:"restart"`
	Logs    *bool `json:"logs,omitempty" yaml:"logs"`
}

type ServiceHealthSpec struct {
	Kind string `json:"kind,omitempty" yaml:"kind"`
}

type ServiceReferenceSpec struct {
	Protection  []string `json:"protection,omitempty" yaml:"protection"`
	Exposure    []string `json:"exposure,omitempty" yaml:"exposure"`
	Credentials []string `json:"credentials,omitempty" yaml:"credentials"`
}

type ServiceContractValidationError struct {
	Problems []string
}

func (e ServiceContractValidationError) Error() string {
	return "invalid service registration contract: " + strings.Join(e.Problems, "; ")
}

func LoadServiceRegistrationContract(path string) (ServiceRegistrationContract, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ServiceRegistrationContract{}, nil, err
	}
	contract, err := ParseServiceRegistrationContract(raw)
	return contract, raw, err
}

func ParseServiceRegistrationContract(raw []byte) (ServiceRegistrationContract, error) {
	contract, err := DecodeServiceRegistrationContract(raw)
	if err != nil {
		return ServiceRegistrationContract{}, err
	}
	contract = NormalizeServiceRegistrationContract(contract)
	if err := ValidateServiceRegistrationContract(contract); err != nil {
		return ServiceRegistrationContract{}, err
	}
	return contract, nil
}

func DecodeServiceRegistrationContract(raw []byte) (ServiceRegistrationContract, error) {
	var contract ServiceRegistrationContract
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&contract); err != nil {
		return ServiceRegistrationContract{}, fmt.Errorf("decode service registration contract: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return ServiceRegistrationContract{}, fmt.Errorf("decode service registration contract: multiple YAML documents are not allowed")
		}
		return ServiceRegistrationContract{}, fmt.Errorf("decode service registration contract: %w", err)
	}
	return contract, nil
}

func NormalizeServiceRegistrationContract(contract ServiceRegistrationContract) ServiceRegistrationContract {
	contract.Kind = strings.TrimSpace(contract.Kind)
	contract.SchemaVersion = strings.TrimSpace(contract.SchemaVersion)
	contract.Service.Key = strings.ToLower(strings.TrimSpace(contract.Service.Key))
	contract.Service.Name = strings.TrimSpace(contract.Service.Name)
	contract.Service.Description = strings.TrimSpace(contract.Service.Description)
	contract.Service.TargetNode = strings.ToLower(strings.TrimSpace(contract.Service.TargetNode))
	contract.Service.Class = strings.ToLower(strings.TrimSpace(contract.Service.Class))
	if contract.Service.Class == "" {
		contract.Service.Class = ServiceClassProject
	}
	contract.Runtime.Manager = strings.ToLower(strings.TrimSpace(contract.Runtime.Manager))
	contract.Runtime.Unit = strings.TrimSpace(contract.Runtime.Unit)
	contract.Health.Kind = strings.ToLower(strings.TrimSpace(contract.Health.Kind))
	if contract.Health.Kind == "" {
		contract.Health.Kind = ServiceHealthKindManager
	}
	contract.Operations = normalizeServiceOperations(contract.Operations)
	contract.References.Protection = normalizeServiceReferences(contract.References.Protection)
	contract.References.Exposure = normalizeServiceReferences(contract.References.Exposure)
	contract.References.Credentials = normalizeServiceReferences(contract.References.Credentials)
	return contract
}

func ValidateServiceRegistrationContract(contract ServiceRegistrationContract) error {
	problems := make([]string, 0)
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	if contract.Kind != ServiceRegistrationKind {
		add("kind must be %q", ServiceRegistrationKind)
	}
	if contract.SchemaVersion != ServiceRegistrationSchemaV01 {
		add("schema_version must be %q", ServiceRegistrationSchemaV01)
	}
	if !serviceRegistrationKeyPattern.MatchString(contract.Service.Key) {
		add("service.key must match %s", serviceRegistrationKeyPattern.String())
	}
	if contract.Service.Name == "" {
		add("service.name is required")
	} else if !boundedSingleLinePlainText(contract.Service.Name, 128) {
		add("service.name must be single-line valid UTF-8 bounded text without control characters")
	}
	if !boundedDescriptionText(contract.Service.Description, maxServiceDescriptionBytes) {
		add("service.description must be valid UTF-8 bounded text without unsafe control characters")
	}
	if !serviceNodeKeyPattern.MatchString(contract.Service.TargetNode) {
		add("service.target_node must match %s", serviceNodeKeyPattern.String())
	}
	if !validServiceClass(contract.Service.Class) {
		add("service.class must be project, module, connector, or system")
	}
	if !validServiceManager(contract.Runtime.Manager) {
		add("runtime.manager must be systemd or launchd")
	} else if err := validateServiceUnit(contract.Runtime.Manager, contract.Runtime.Unit); err != nil {
		add("runtime.unit %v", err)
	}
	if contract.Health.Kind != ServiceHealthKindManager {
		add("health.kind must be manager; HTTP targets are accepted only from trusted node configuration")
	}
	validateServiceReferenceList("references.protection", contract.References.Protection, serviceProtectionRefPattern, &problems)
	validateServiceReferenceList("references.exposure", contract.References.Exposure, serviceExposureRefPattern, &problems)
	validateServiceReferenceList("references.credentials", contract.References.Credentials, serviceCredentialRefPattern, &problems)

	if len(problems) > 0 {
		return ServiceContractValidationError{Problems: problems}
	}
	return nil
}

func (contract ServiceRegistrationContract) RequestedOperations() []string {
	operations := contract.Operations
	result := make([]string, 0, 5)
	for _, item := range []struct {
		name    string
		enabled *bool
	}{
		{ServiceOperationStatus, operations.Status},
		{ServiceOperationStart, operations.Start},
		{ServiceOperationStop, operations.Stop},
		{ServiceOperationRestart, operations.Restart},
		{ServiceOperationLogs, operations.Logs},
	} {
		if item.enabled != nil && *item.enabled {
			result = append(result, item.name)
		}
	}
	return result
}

func normalizeServiceOperations(operations ServiceOperationSelection) ServiceOperationSelection {
	if operations.Status == nil && operations.Start == nil && operations.Stop == nil && operations.Restart == nil && operations.Logs == nil {
		return ServiceOperationSelection{
			Status: boolPointer(true), Start: boolPointer(false), Stop: boolPointer(false),
			Restart: boolPointer(false), Logs: boolPointer(true),
		}
	}
	if operations.Status == nil {
		operations.Status = boolPointer(false)
	}
	if operations.Start == nil {
		operations.Start = boolPointer(false)
	}
	if operations.Stop == nil {
		operations.Stop = boolPointer(false)
	}
	if operations.Restart == nil {
		operations.Restart = boolPointer(false)
	}
	if operations.Logs == nil {
		operations.Logs = boolPointer(false)
	}
	return operations
}

func normalizeServiceReferences(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func validateServiceReferenceList(field string, values []string, pattern *regexp.Regexp, problems *[]string) {
	if len(values) > maxServiceReferenceCount {
		*problems = append(*problems, fmt.Sprintf("%s has more than %d entries", field, maxServiceReferenceCount))
	}
	for _, value := range values {
		if len(value) > maxServiceReferenceBytes || !pattern.MatchString(value) || strings.Contains(value, "..") || strings.Contains(value, "://") {
			*problems = append(*problems, fmt.Sprintf("%s contains invalid non-secret reference %q", field, value))
		}
	}
}

func validServiceManager(value string) bool {
	return value == ServiceManagerSystemd || value == ServiceManagerLaunchd
}

func validServiceClass(value string) bool {
	switch value {
	case ServiceClassProject, ServiceClassModule, ServiceClassConnector, ServiceClassSystem:
		return true
	default:
		return false
	}
}

func validateServiceUnit(manager, unit string) error {
	if unit == "" {
		return fmt.Errorf("is required")
	}
	if strings.ContainsAny(unit, "/\\\x00\r\n\t ") || strings.Contains(unit, "..") {
		return fmt.Errorf("contains a path, traversal, whitespace, or control character")
	}
	switch manager {
	case ServiceManagerSystemd:
		if !systemdServiceUnitPattern.MatchString(unit) {
			return fmt.Errorf("must be a bounded .service unit name")
		}
	case ServiceManagerLaunchd:
		if !launchdServiceLabelPattern.MatchString(unit) {
			return fmt.Errorf("must be a bounded user LaunchAgent label")
		}
	}
	return nil
}

func boundedSingleLinePlainText(value string, maxBytes int) bool {
	if !utf8.ValidString(value) || len(value) > maxBytes {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func boundedDescriptionText(value string, maxBytes int) bool {
	if !utf8.ValidString(value) || len(value) > maxBytes {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return false
		}
	}
	return true
}

func boolPointer(value bool) *bool {
	return &value
}
