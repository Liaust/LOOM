package serviceregistry

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const ServiceRuntimeProfileSchemaV1 = "loom.service_runtime.v1"

var (
	profileProtectionRefPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,80}$`)
	profileExposureRefPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	profileCredentialRefPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*(?:\.[a-z][a-z0-9_-]*)+$`)
)

type RuntimeProfileInput struct {
	Manager      Manager           `json:"manager"`
	Unit         string            `json:"unit"`
	ServiceClass ServiceClass      `json:"service_class"`
	Operations   []Operation       `json:"operations,omitempty"`
	Health       RuntimeHealth     `json:"health,omitempty"`
	References   RuntimeReferences `json:"references,omitempty"`
}

type RuntimeProfile struct {
	SchemaVersion string            `json:"schema_version"`
	Manager       Manager           `json:"manager"`
	Unit          string            `json:"unit"`
	ServiceClass  ServiceClass      `json:"service_class"`
	Operations    []Operation       `json:"operations"`
	Health        RuntimeHealth     `json:"health"`
	References    RuntimeReferences `json:"references"`
}

type RuntimeHealth struct {
	Kind     HealthKind `json:"kind"`
	ProbeRef string     `json:"probe_ref,omitempty"`
}

type RuntimeReferences struct {
	Protection  []string `json:"protection"`
	Exposure    []string `json:"exposure"`
	Credentials []string `json:"credentials"`
}

func BuildRuntimeProfile(input RuntimeProfileInput) (RuntimeProfile, error) {
	operationsAbsent := input.Operations == nil
	input.Manager = Manager(strings.ToLower(strings.TrimSpace(string(input.Manager))))
	input.Unit = strings.TrimSpace(input.Unit)
	input.ServiceClass = ServiceClass(strings.ToLower(strings.TrimSpace(string(input.ServiceClass))))
	input.Operations = normalizeOperations(input.Operations)
	if operationsAbsent {
		input.Operations = []Operation{OperationStatus, OperationLogs}
	}
	input.Health.Kind = HealthKind(strings.ToLower(strings.TrimSpace(string(input.Health.Kind))))
	if input.Health.Kind == "" {
		input.Health.Kind = HealthKindManager
	}
	input.Health.ProbeRef = strings.TrimSpace(input.Health.ProbeRef)
	input.References.Protection = normalizeProfileReferences(input.References.Protection)
	input.References.Exposure = normalizeProfileReferences(input.References.Exposure)
	input.References.Credentials = normalizeProfileReferences(input.References.Credentials)

	if !validManager(input.Manager) {
		return RuntimeProfile{}, fmt.Errorf("runtime profile manager must be systemd or launchd")
	}
	if err := validateUnit(input.Manager, input.Unit); err != nil {
		return RuntimeProfile{}, err
	}
	if !validServiceClass(input.ServiceClass) {
		return RuntimeProfile{}, fmt.Errorf("runtime profile service_class is invalid")
	}
	for _, operation := range input.Operations {
		if _, err := StandardOperationPolicy(operation); err != nil {
			return RuntimeProfile{}, err
		}
	}
	switch input.Health.Kind {
	case HealthKindManager:
		if input.Health.ProbeRef != "" {
			return RuntimeProfile{}, fmt.Errorf("manager health cannot carry a probe_ref")
		}
	case HealthKindHTTP:
		if !trustedHealthRefPattern.MatchString(input.Health.ProbeRef) {
			return RuntimeProfile{}, fmt.Errorf("http health requires a bounded trusted node probe_ref")
		}
	default:
		return RuntimeProfile{}, fmt.Errorf("runtime profile health.kind must be manager or http")
	}
	if err := validateProfileReferences("protection", input.References.Protection, profileProtectionRefPattern); err != nil {
		return RuntimeProfile{}, err
	}
	if err := validateProfileReferences("exposure", input.References.Exposure, profileExposureRefPattern); err != nil {
		return RuntimeProfile{}, err
	}
	if err := validateProfileReferences("credentials", input.References.Credentials, profileCredentialRefPattern); err != nil {
		return RuntimeProfile{}, err
	}

	return RuntimeProfile{
		SchemaVersion: ServiceRuntimeProfileSchemaV1,
		Manager:       input.Manager,
		Unit:          input.Unit,
		ServiceClass:  input.ServiceClass,
		Operations:    append([]Operation{}, input.Operations...),
		Health:        input.Health,
		References: RuntimeReferences{
			Protection:  nonNilStrings(input.References.Protection),
			Exposure:    nonNilStrings(input.References.Exposure),
			Credentials: nonNilStrings(input.References.Credentials),
		},
	}, nil
}

func MarshalRuntimeProfile(input RuntimeProfileInput) ([]byte, error) {
	profile, err := BuildRuntimeProfile(input)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(profile)
	if err != nil {
		return nil, fmt.Errorf("marshal service runtime profile: %w", err)
	}
	return payload, nil
}

func normalizeProfileReferences(values []string) []string {
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

func validateProfileReferences(kind string, values []string, pattern *regexp.Regexp) error {
	if len(values) > 32 {
		return fmt.Errorf("runtime profile %s references exceed 32 entries", kind)
	}
	for _, value := range values {
		if len(value) > 128 || !pattern.MatchString(value) || strings.Contains(value, "..") || strings.Contains(value, "://") {
			return fmt.Errorf("runtime profile %s reference %q is invalid", kind, value)
		}
	}
	return nil
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return append([]string{}, values...)
}
