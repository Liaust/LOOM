package serviceregistry

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/projectcontracts"
)

const (
	ManagerSystemd Manager = "systemd"
	ManagerLaunchd Manager = "launchd"

	ServiceClassProject   ServiceClass = "project"
	ServiceClassModule    ServiceClass = "module"
	ServiceClassConnector ServiceClass = "connector"
	ServiceClassSystem    ServiceClass = "system"

	OperationStatus  Operation = "status"
	OperationStart   Operation = "start"
	OperationStop    Operation = "stop"
	OperationRestart Operation = "restart"
	OperationLogs    Operation = "logs"

	RiskLow  RiskLevel = "low"
	RiskHigh RiskLevel = "high"

	CapabilityFormQuery   CapabilityForm = "query"
	CapabilityFormCommand CapabilityForm = "command"

	ProviderStateRegistered ProviderRegistryState = "registered"
	ProviderStateActive     ProviderRegistryState = "active"
	ProviderStateDisabled   ProviderRegistryState = "disabled"
	ProviderStateDeprecated ProviderRegistryState = "deprecated"
	ProviderStateRevoked    ProviderRegistryState = "revoked"

	ProcessStateUnknown     ObservedProcessState = "unknown"
	ProcessStateRunning     ObservedProcessState = "running"
	ProcessStateStopped     ObservedProcessState = "stopped"
	ProcessStateFailed      ObservedProcessState = "failed"
	ProcessStateUnavailable ObservedProcessState = "unavailable"

	HealthKindManager HealthKind = "manager"
	HealthKindHTTP    HealthKind = "http"

	AllowlistSchemaV1 = "loom.service_allowlist.v1"

	maxServiceDisplayNameBytes = 128
	maxServiceDescriptionBytes = 2048

	LifecyclePolicyServiceOperations LifecyclePolicy = "service_operations"
	LifecyclePolicyInspectOnlyCore   LifecyclePolicy = "inspect_only_core"
)

var (
	registryKeyPattern      = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
	scopeKeyPattern         = regexp.MustCompile(`^[a-z][a-z0-9_/-]{0,255}$`)
	systemdUnitPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@:-]{0,126}\.service$`)
	launchdLabelPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,127}$`)
	trustedHealthRefPattern = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{0,127}$`)
	sha256DigestPattern     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type Manager string
type ServiceClass string
type Operation string
type RiskLevel string
type CapabilityForm string
type ProviderRegistryState string
type ObservedProcessState string
type HealthKind string
type LifecyclePolicy string

type OperationPolicy struct {
	Operation                   Operation      `json:"operation"`
	CapabilityName              string         `json:"capability_name"`
	Form                        CapabilityForm `json:"form"`
	RiskLevel                   RiskLevel      `json:"risk_level"`
	ExecutionAuthorizationLevel int            `json:"execution_authorization_level"`
	MutatesState                bool           `json:"mutates_state"`
	RequiresConfirmation        bool           `json:"requires_confirmation"`
}

type ProviderHealthProjection struct {
	HealthStatus       string `json:"health_status"`
	AvailabilityStatus string `json:"availability_status"`
}

func StandardOperationPolicy(operation Operation) (OperationPolicy, error) {
	switch operation {
	case OperationStatus:
		return OperationPolicy{Operation: operation, CapabilityName: "service.status", Form: CapabilityFormQuery, RiskLevel: RiskLow, ExecutionAuthorizationLevel: 1}, nil
	case OperationLogs:
		return OperationPolicy{Operation: operation, CapabilityName: "service.logs", Form: CapabilityFormQuery, RiskLevel: RiskLow, ExecutionAuthorizationLevel: 2}, nil
	case OperationStart, OperationStop, OperationRestart:
		return OperationPolicy{Operation: operation, CapabilityName: "service." + string(operation), Form: CapabilityFormCommand, RiskLevel: RiskHigh, ExecutionAuthorizationLevel: 4, MutatesState: true, RequiresConfirmation: true}, nil
	default:
		return OperationPolicy{}, fmt.Errorf("unsupported service operation %q", operation)
	}
}

func StandardOperations() []Operation {
	return []Operation{OperationStatus, OperationStart, OperationStop, OperationRestart, OperationLogs}
}

func ValidProviderRegistryState(state ProviderRegistryState) bool {
	switch state {
	case ProviderStateRegistered, ProviderStateActive, ProviderStateDisabled, ProviderStateDeprecated, ProviderStateRevoked:
		return true
	default:
		return false
	}
}

func ValidObservedProcessState(state ObservedProcessState) bool {
	switch state {
	case ProcessStateUnknown, ProcessStateRunning, ProcessStateStopped, ProcessStateFailed, ProcessStateUnavailable:
		return true
	default:
		return false
	}
}

// ProjectProcessState maps an observation into the existing provider_health
// vocabulary; it does not alter or infer the provider registry lifecycle.
func ProjectProcessState(state ObservedProcessState) (ProviderHealthProjection, error) {
	switch state {
	case ProcessStateUnknown:
		return ProviderHealthProjection{HealthStatus: "unknown", AvailabilityStatus: "unknown"}, nil
	case ProcessStateRunning:
		return ProviderHealthProjection{HealthStatus: "ok", AvailabilityStatus: "available"}, nil
	case ProcessStateStopped:
		return ProviderHealthProjection{HealthStatus: "degraded", AvailabilityStatus: "unavailable"}, nil
	case ProcessStateFailed:
		return ProviderHealthProjection{HealthStatus: "unhealthy", AvailabilityStatus: "unavailable"}, nil
	case ProcessStateUnavailable:
		return ProviderHealthProjection{HealthStatus: "offline", AvailabilityStatus: "unavailable"}, nil
	default:
		return ProviderHealthProjection{}, fmt.Errorf("invalid observed process state %q", state)
	}
}

type ServiceIdentity struct {
	Key         string       `json:"key"`
	DisplayName string       `json:"display_name"`
	Description string       `json:"description,omitempty"`
	Class       ServiceClass `json:"service_class"`
}

// ProjectRegistrationInput and SystemRegistrationInput are deliberately
// separate. A system registration comes from reviewed node configuration; it
// is never represented by a fabricated project.
type ProjectRegistrationInput struct {
	ProjectID  string              `json:"project_id"`
	ScopeKey   string              `json:"scope_key"`
	TargetNode string              `json:"target_node"`
	Service    ServiceIdentity     `json:"service"`
	Runtime    RuntimeProfileInput `json:"runtime"`
	SourcePath string              `json:"source_path"`
}

type SystemRegistrationInput struct {
	SourceKey  string              `json:"source_key"`
	ScopeKey   string              `json:"scope_key"`
	TargetNode string              `json:"target_node"`
	Service    ServiceIdentity     `json:"service"`
	Runtime    RuntimeProfileInput `json:"runtime"`
}

func (input ProjectRegistrationInput) Validate() error {
	if err := ids.Validate(ids.ProjectPrefix, input.ProjectID); err != nil {
		return fmt.Errorf("project_id must be a valid LOOM project ID: %w", err)
	}
	expectedSourcePath, err := projectcontracts.KeyedProjectContractPath("services", input.Service.Key)
	if err != nil {
		return fmt.Errorf("service key is invalid: %w", err)
	}
	if input.SourcePath != expectedSourcePath {
		return fmt.Errorf("source_path must be the canonical service contract path %q", expectedSourcePath)
	}
	if input.Service.Class == ServiceClassSystem || input.Runtime.ServiceClass == ServiceClassSystem {
		return fmt.Errorf("project registration cannot declare system service_class")
	}
	healthKind := HealthKind(strings.ToLower(strings.TrimSpace(string(input.Runtime.Health.Kind))))
	if healthKind == HealthKindHTTP || strings.TrimSpace(input.Runtime.Health.ProbeRef) != "" {
		return fmt.Errorf("project registration health must use manager observation without a trusted probe_ref")
	}
	return validateRegistrationFields(input.ScopeKey, input.TargetNode, input.Service, input.Runtime)
}

func (input SystemRegistrationInput) Validate() error {
	if !canonicalIdentifier(input.SourceKey, registryKeyPattern) {
		return fmt.Errorf("source_key must be a canonical lowercase bounded registry key")
	}
	if input.Service.Class != ServiceClassSystem {
		return fmt.Errorf("system registration service_class must be system")
	}
	return validateRegistrationFields(input.ScopeKey, input.TargetNode, input.Service, input.Runtime)
}

type AllowlistRecord struct {
	SchemaVersion          string                         `json:"schema_version" yaml:"schema_version"`
	Key                    string                         `json:"key" yaml:"key"`
	NodeKey                string                         `json:"node_key" yaml:"node_key"`
	Manager                Manager                        `json:"manager" yaml:"manager"`
	Unit                   string                         `json:"unit" yaml:"unit"`
	Operations             []Operation                    `json:"operations" yaml:"operations"`
	LifecyclePolicy        LifecyclePolicy                `json:"lifecycle_policy" yaml:"lifecycle_policy"`
	Health                 AllowlistHealth                `json:"health" yaml:"health"`
	LogLimits              LogLimits                      `json:"log_limits" yaml:"log_limits"`
	ProjectArchiveIdentity *ProjectArchiveServiceIdentity `json:"project_archive_identity,omitempty" yaml:"project_archive_identity,omitempty"`
}

// ProjectArchiveServiceIdentity is the node-reviewed projection of the Main
// provider identity that may be stopped for a physical project archive. It is
// optional for ordinary service operations and mandatory for quiescence.
type ProjectArchiveServiceIdentity struct {
	ProviderKey          string `json:"provider_key" yaml:"provider_key"`
	ProviderAddress      string `json:"provider_address" yaml:"provider_address"`
	ProviderID           string `json:"provider_id" yaml:"provider_id"`
	RuntimeProfileDigest string `json:"runtime_profile_digest" yaml:"runtime_profile_digest"`
}

type AllowlistHealth struct {
	Kind     HealthKind `json:"kind" yaml:"kind"`
	ProbeRef string     `json:"probe_ref,omitempty" yaml:"probe_ref"`
}

func NormalizeAndValidateAllowlistRecord(record AllowlistRecord) (AllowlistRecord, error) {
	record.SchemaVersion = strings.TrimSpace(record.SchemaVersion)
	record.Key = strings.ToLower(strings.TrimSpace(record.Key))
	record.NodeKey = strings.ToLower(strings.TrimSpace(record.NodeKey))
	record.Manager = Manager(strings.ToLower(strings.TrimSpace(string(record.Manager))))
	record.Unit = strings.TrimSpace(record.Unit)
	if record.ProjectArchiveIdentity != nil {
		identity := *record.ProjectArchiveIdentity
		identity.ProviderKey = strings.ToLower(strings.TrimSpace(identity.ProviderKey))
		identity.ProviderAddress = strings.ToLower(strings.TrimSpace(identity.ProviderAddress))
		identity.ProviderID = strings.TrimSpace(identity.ProviderID)
		identity.RuntimeProfileDigest = strings.ToLower(strings.TrimSpace(identity.RuntimeProfileDigest))
		record.ProjectArchiveIdentity = &identity
	}
	record.LifecyclePolicy = LifecyclePolicy(strings.ToLower(strings.TrimSpace(string(record.LifecyclePolicy))))
	if record.LifecyclePolicy == "" {
		record.LifecyclePolicy = LifecyclePolicyServiceOperations
	}
	record.Health.Kind = HealthKind(strings.ToLower(strings.TrimSpace(string(record.Health.Kind))))
	if record.Health.Kind == "" {
		record.Health.Kind = HealthKindManager
	}
	record.Health.ProbeRef = strings.TrimSpace(record.Health.ProbeRef)
	record.Operations = normalizeOperations(record.Operations)
	limits, err := record.LogLimits.Normalize()
	if err != nil {
		return AllowlistRecord{}, err
	}
	record.LogLimits = limits

	if record.SchemaVersion != AllowlistSchemaV1 {
		return AllowlistRecord{}, fmt.Errorf("schema_version must be %q", AllowlistSchemaV1)
	}
	if !registryKeyPattern.MatchString(record.Key) || !registryKeyPattern.MatchString(record.NodeKey) {
		return AllowlistRecord{}, fmt.Errorf("key and node_key must be bounded registry keys")
	}
	if !validManager(record.Manager) {
		return AllowlistRecord{}, fmt.Errorf("manager must be systemd or launchd")
	}
	if err := validateUnit(record.Manager, record.Unit); err != nil {
		return AllowlistRecord{}, err
	}
	if record.ProjectArchiveIdentity != nil {
		identity := record.ProjectArchiveIdentity
		address, err := capabilities.ParseProviderAddress(identity.ProviderAddress)
		if err != nil || identity.ProviderKey != record.Key || address.CompactAddress != identity.ProviderAddress || address.ScopePath != record.NodeKey || address.ProviderKey != identity.ProviderKey {
			return AllowlistRecord{}, fmt.Errorf("project archive provider identity does not match the allowlist node")
		}
		if err := ids.Validate(ids.ProviderPrefix, identity.ProviderID); err != nil {
			return AllowlistRecord{}, fmt.Errorf("project archive provider id is invalid: %w", err)
		}
		if !sha256DigestPattern.MatchString(identity.RuntimeProfileDigest) {
			return AllowlistRecord{}, fmt.Errorf("project archive runtime profile digest is invalid")
		}
	}
	if len(record.Operations) == 0 {
		return AllowlistRecord{}, fmt.Errorf("allowlist operations must be explicit")
	}
	for _, operation := range record.Operations {
		if _, err := StandardOperationPolicy(operation); err != nil {
			return AllowlistRecord{}, err
		}
	}
	if record.LifecyclePolicy != LifecyclePolicyServiceOperations && record.LifecyclePolicy != LifecyclePolicyInspectOnlyCore {
		return AllowlistRecord{}, fmt.Errorf("unsupported lifecycle_policy %q", record.LifecyclePolicy)
	}
	if record.LifecyclePolicy == LifecyclePolicyInspectOnlyCore {
		for _, operation := range record.Operations {
			policy, _ := StandardOperationPolicy(operation)
			if policy.MutatesState {
				return AllowlistRecord{}, fmt.Errorf("inspect-only core infrastructure cannot allow %s", operation)
			}
		}
	}
	switch record.Health.Kind {
	case HealthKindManager:
		if record.Health.ProbeRef != "" {
			return AllowlistRecord{}, fmt.Errorf("manager health cannot carry a probe_ref")
		}
	case HealthKindHTTP:
		if !trustedHealthRefPattern.MatchString(record.Health.ProbeRef) {
			return AllowlistRecord{}, fmt.Errorf("http health requires a bounded trusted node probe_ref")
		}
	default:
		return AllowlistRecord{}, fmt.Errorf("health.kind must be manager or http")
	}
	return record, nil
}

func EffectiveOperations(requested, allowed []Operation) []Operation {
	allowedSet := make(map[Operation]struct{}, len(allowed))
	for _, operation := range allowed {
		if _, err := StandardOperationPolicy(operation); err == nil {
			allowedSet[operation] = struct{}{}
		}
	}
	result := make([]Operation, 0, len(requested))
	seen := map[Operation]struct{}{}
	for _, operation := range requested {
		if _, ok := allowedSet[operation]; !ok {
			continue
		}
		if _, ok := seen[operation]; ok {
			continue
		}
		seen[operation] = struct{}{}
		result = append(result, operation)
	}
	sort.Slice(result, func(i, j int) bool { return operationOrder(result[i]) < operationOrder(result[j]) })
	return result
}

type ManagerResult struct {
	Operation    Operation            `json:"operation"`
	Success      bool                 `json:"success"`
	ProcessState ObservedProcessState `json:"process_state"`
	Message      string               `json:"message,omitempty"`
	LogLines     []string             `json:"log_lines,omitempty"`
	ExitCode     *int                 `json:"exit_code,omitempty"`
	Truncated    bool                 `json:"truncated,omitempty"`
}

func validateRegistrationFields(scopeKey, targetNode string, service ServiceIdentity, runtime RuntimeProfileInput) error {
	if !canonicalIdentifier(scopeKey, scopeKeyPattern) {
		return fmt.Errorf("scope_key must be a canonical lowercase scope key")
	}
	if !canonicalIdentifier(targetNode, registryKeyPattern) {
		return fmt.Errorf("target_node must be a canonical lowercase registry key")
	}
	if !canonicalIdentifier(service.Key, registryKeyPattern) {
		return fmt.Errorf("service key must be a canonical lowercase registry key")
	}
	if strings.TrimSpace(service.DisplayName) == "" {
		return fmt.Errorf("service display_name is required")
	}
	if !boundedSingleLineServiceMetadataText(service.DisplayName, maxServiceDisplayNameBytes) {
		return fmt.Errorf("service display_name must be single-line valid UTF-8 bounded text without control characters")
	}
	if !boundedServiceDescriptionText(service.Description, maxServiceDescriptionBytes) {
		return fmt.Errorf("service description must be valid UTF-8 bounded text without unsafe control characters")
	}
	if !validServiceClass(service.Class) {
		return fmt.Errorf("service_class is invalid")
	}
	if runtime.ServiceClass != service.Class {
		return fmt.Errorf("service identity and runtime profile service_class must match")
	}
	_, err := BuildRuntimeProfile(runtime)
	return err
}

func canonicalIdentifier(value string, pattern *regexp.Regexp) bool {
	return value == strings.TrimSpace(value) && value == strings.ToLower(value) && pattern.MatchString(value)
}

func boundedSingleLineServiceMetadataText(value string, maxBytes int) bool {
	if !utf8.ValidString(value) || len(value) > maxBytes {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func boundedServiceDescriptionText(value string, maxBytes int) bool {
	if !utf8.ValidString(value) || len(value) > maxBytes {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) && character != '\n' && character != '\t' {
			return false
		}
	}
	return true
}

func normalizeOperations(operations []Operation) []Operation {
	seen := map[Operation]struct{}{}
	result := make([]Operation, 0, len(operations))
	for _, operation := range operations {
		operation = Operation(strings.ToLower(strings.TrimSpace(string(operation))))
		if _, ok := seen[operation]; ok {
			continue
		}
		seen[operation] = struct{}{}
		result = append(result, operation)
	}
	sort.Slice(result, func(i, j int) bool { return operationOrder(result[i]) < operationOrder(result[j]) })
	return result
}

func operationOrder(operation Operation) int {
	for index, candidate := range StandardOperations() {
		if candidate == operation {
			return index
		}
	}
	return len(StandardOperations())
}

func validManager(manager Manager) bool {
	return manager == ManagerSystemd || manager == ManagerLaunchd
}

func validServiceClass(class ServiceClass) bool {
	switch class {
	case ServiceClassProject, ServiceClassModule, ServiceClassConnector, ServiceClassSystem:
		return true
	default:
		return false
	}
}

func validateUnit(manager Manager, unit string) error {
	if strings.ContainsAny(unit, "/\\\x00\r\n\t ") || strings.Contains(unit, "..") {
		return fmt.Errorf("unit contains a path, traversal, whitespace, or control character")
	}
	switch manager {
	case ManagerSystemd:
		if !systemdUnitPattern.MatchString(unit) {
			return fmt.Errorf("systemd unit must be a bounded .service name")
		}
	case ManagerLaunchd:
		if !launchdLabelPattern.MatchString(unit) {
			return fmt.Errorf("launchd unit must be a bounded user LaunchAgent label")
		}
	}
	return nil
}
