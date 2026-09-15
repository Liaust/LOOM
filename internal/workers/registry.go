package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	workerKindPattern = regexp.MustCompile(`^[a-z][a-z0-9_:-]{0,127}$`)
	workerKeyPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9_.:-]{0,191}$`)
)

const KindProvenanceArchivist = "provenance_archivist"
const KindProjectContextRefresh = "project_context_refresh"

type Registry struct {
	runtimes    map[string]Runtime
	descriptors map[string]KindDescriptor
}

func NewRegistry() *Registry {
	return &Registry{
		runtimes:    map[string]Runtime{},
		descriptors: map[string]KindDescriptor{},
	}
}

func (r *Registry) Register(runtime Runtime) error {
	if runtime == nil {
		return fmt.Errorf("%w: runtime is required", ErrInvalid)
	}
	kind := strings.TrimSpace(runtime.Kind())
	if err := ValidateWorkerKind(kind); err != nil {
		return err
	}
	if _, exists := r.runtimes[kind]; exists {
		return fmt.Errorf("%w: worker runtime %q is already registered", ErrConflict, kind)
	}

	descriptor, err := normalizeKindDescriptor(runtime.Describe())
	if err != nil {
		return err
	}
	if descriptor.WorkerKind != kind {
		return fmt.Errorf("%w: runtime kind %q does not match descriptor kind %q", ErrInvalid, kind, descriptor.WorkerKind)
	}
	if _, err := normalizeJSONObject(runtime.DefaultConfig(), "default_config_json"); err != nil {
		return err
	}

	r.runtimes[kind] = runtime
	r.descriptors[kind] = descriptor
	return nil
}

func (r *Registry) Get(kind string) (Runtime, bool) {
	if r == nil {
		return nil, false
	}
	runtime, ok := r.runtimes[strings.TrimSpace(kind)]
	return runtime, ok
}

func (r *Registry) Descriptors() []KindDescriptor {
	if r == nil {
		return nil
	}
	kinds := make([]string, 0, len(r.descriptors))
	for kind := range r.descriptors {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)

	out := make([]KindDescriptor, 0, len(kinds))
	for _, kind := range kinds {
		out = append(out, r.descriptors[kind])
	}
	return out
}

func (r *Registry) DefaultInstances() []InstanceDescriptor {
	if r == nil {
		return nil
	}
	kinds := make([]string, 0, len(r.runtimes))
	for kind := range r.runtimes {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)

	var out []InstanceDescriptor
	for _, kind := range kinds {
		provider, ok := r.runtimes[kind].(DefaultInstanceProvider)
		if !ok {
			continue
		}
		out = append(out, provider.DefaultInstances()...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].WorkerKey < out[j].WorkerKey
	})
	return out
}

func ValidateWorkerKind(kind string) error {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return fmt.Errorf("%w: worker kind is required", ErrInvalid)
	}
	if !workerKindPattern.MatchString(kind) {
		return fmt.Errorf("%w: worker kind %q is invalid", ErrInvalid, kind)
	}
	return nil
}

func ValidateWorkerKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("%w: worker key is required", ErrInvalid)
	}
	if !workerKeyPattern.MatchString(key) {
		return fmt.Errorf("%w: worker key %q is invalid", ErrInvalid, key)
	}
	return nil
}

func normalizeKindDescriptor(descriptor KindDescriptor) (KindDescriptor, error) {
	descriptor.WorkerKind = strings.TrimSpace(descriptor.WorkerKind)
	if err := ValidateWorkerKind(descriptor.WorkerKind); err != nil {
		return KindDescriptor{}, err
	}
	descriptor.DisplayName = strings.TrimSpace(descriptor.DisplayName)
	if descriptor.DisplayName == "" {
		return KindDescriptor{}, fmt.Errorf("%w: display_name is required for worker kind %q", ErrInvalid, descriptor.WorkerKind)
	}
	descriptor.Description = strings.TrimSpace(descriptor.Description)
	descriptor.RuntimeOwner = strings.TrimSpace(descriptor.RuntimeOwner)
	if !validRuntimeOwner(descriptor.RuntimeOwner) {
		return KindDescriptor{}, fmt.Errorf("%w: runtime_owner %q is invalid", ErrInvalid, descriptor.RuntimeOwner)
	}
	descriptor.RuntimePackage = strings.TrimSpace(descriptor.RuntimePackage)
	descriptor.Status = strings.TrimSpace(descriptor.Status)
	if descriptor.Status == "" {
		descriptor.Status = KindStatusActive
	}
	if !validKindStatus(descriptor.Status) {
		return KindDescriptor{}, fmt.Errorf("%w: worker kind status %q is invalid", ErrInvalid, descriptor.Status)
	}
	if len(descriptor.SupportedLocalities) == 0 {
		return KindDescriptor{}, fmt.Errorf("%w: supported_localities is required for worker kind %q", ErrInvalid, descriptor.WorkerKind)
	}
	for i, locality := range descriptor.SupportedLocalities {
		locality = strings.TrimSpace(locality)
		if !validLocality(locality) {
			return KindDescriptor{}, fmt.Errorf("%w: locality %q is invalid", ErrInvalid, locality)
		}
		descriptor.SupportedLocalities[i] = locality
	}

	var err error
	if descriptor.DefaultTickPolicyJSON, err = normalizeJSONObject(descriptor.DefaultTickPolicyJSON, "default_tick_policy_json"); err != nil {
		return KindDescriptor{}, err
	}
	if err := validateManualOnlyWorkerPolicy(descriptor.WorkerKind, descriptor.DefaultTickPolicyJSON); err != nil {
		return KindDescriptor{}, err
	}
	if descriptor.DefaultConcurrencyPolicyJSON, err = normalizeJSONObject(descriptor.DefaultConcurrencyPolicyJSON, "default_concurrency_policy_json"); err != nil {
		return KindDescriptor{}, err
	}
	if descriptor.DefaultRetryPolicyJSON, err = normalizeJSONObject(descriptor.DefaultRetryPolicyJSON, "default_retry_policy_json"); err != nil {
		return KindDescriptor{}, err
	}
	if descriptor.DefaultTimeoutPolicyJSON, err = normalizeJSONObject(descriptor.DefaultTimeoutPolicyJSON, "default_timeout_policy_json"); err != nil {
		return KindDescriptor{}, err
	}
	if descriptor.DefaultResourceLimitsJSON, err = normalizeJSONObject(descriptor.DefaultResourceLimitsJSON, "default_resource_limits_json"); err != nil {
		return KindDescriptor{}, err
	}
	if descriptor.ConfigSchemaJSON, err = normalizeJSONObject(descriptor.ConfigSchemaJSON, "config_schema_json"); err != nil {
		return KindDescriptor{}, err
	}
	if descriptor.CheckpointSchemaJSON, err = normalizeJSONObject(descriptor.CheckpointSchemaJSON, "checkpoint_schema_json"); err != nil {
		return KindDescriptor{}, err
	}
	if descriptor.ResultSchemaJSON, err = normalizeJSONObject(descriptor.ResultSchemaJSON, "result_schema_json"); err != nil {
		return KindDescriptor{}, err
	}
	if descriptor.Metadata, err = normalizeJSONObject(descriptor.Metadata, "metadata"); err != nil {
		return KindDescriptor{}, err
	}
	return descriptor, nil
}

func validateManualOnlyWorkerPolicy(kind string, raw json.RawMessage) error {
	if strings.TrimSpace(kind) != KindProvenanceArchivist {
		return nil
	}
	policy, err := ParseTickPolicy(raw)
	if err != nil {
		return err
	}
	if policy.Mode != TickModeManual || policy.RunOnStartup || policy.NextAfter(time.Now().UTC()) != nil {
		return fmt.Errorf("%w: worker kind %s is manual-only and cannot be scheduled", ErrInvalid, KindProvenanceArchivist)
	}
	return nil
}

func normalizeJSONObject(raw json.RawMessage, field string) (json.RawMessage, error) {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("%w: %s is invalid JSON: %w", ErrInvalid, field, err)
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("%w: %s must be a JSON object", ErrInvalid, field)
	}
	return raw, nil
}

func validRuntimeOwner(value string) bool {
	switch value {
	case RuntimeOwnerLoomd, RuntimeOwnerNodeAgent, RuntimeOwnerTrustedModule:
		return true
	default:
		return false
	}
}

func validKindStatus(value string) bool {
	switch value {
	case KindStatusActive, KindStatusDeprecated, KindStatusDisabled:
		return true
	default:
		return false
	}
}

func validLocality(value string) bool {
	switch value {
	case LocalityMainOwned, LocalityNodeAgentOwned, LocalityExternalReported:
		return true
	default:
		return false
	}
}

func ValidateConfigObject(ctx context.Context, runtime Runtime, config json.RawMessage) error {
	config, err := normalizeJSONObject(config, "config_json")
	if err != nil {
		return err
	}
	return runtime.ValidateConfig(ctx, config)
}
