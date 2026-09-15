package projectcontracts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/capabilities"
)

const (
	DirectEventActivationStatusDisabled = "disabled"
	DirectEventActivationStatusPending  = "pending_later_slice"
)

func validateDirectEventFacet(loaded LoadedProject, contract ProjectContract, add func(Diagnostic)) []DirectEventFacetItem {
	if !contract.Facets["direct_events"] {
		return nil
	}
	root := filepath.Join(loaded.RootPath, "direct_events")
	entries, err := projectFacetEntries(loaded, "direct_events")
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		add(Diagnostic{Severity: SeverityError, Code: "direct_event.facet_read_failed", Message: "could not read direct_events folder: " + err.Error(), File: root, Field: "facets.direct_events"})
		return nil
	}

	items := []DirectEventFacetItem{}
	seen := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || strings.HasPrefix(name, ".") || ignoredDirectEventDir(name) {
			continue
		}
		packageLoaded, packageRoot := projectFacetPackage(loaded, "direct_events", name)
		item := validateDirectEventPackage(packageLoaded, contract, name, packageRoot, add)
		if item.Key == "" {
			continue
		}
		if previous := seen[item.Key]; previous != "" {
			add(Diagnostic{
				Severity:   SeverityError,
				Code:       "direct_event.key_duplicate",
				Message:    "direct event key is declared more than once: " + item.Key,
				File:       item.ManifestPath,
				Field:      "event.key",
				Suggestion: "use unique project-local direct-event keys; previous declaration was in " + previous,
			})
		}
		seen[item.Key] = item.ManifestPath
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Key < items[j].Key
	})
	if len(items) == 0 {
		add(Diagnostic{
			Severity:   SeverityWarning,
			Code:       "direct_event.none_found",
			Message:    "direct_events facet is enabled but no direct-event packages were found",
			File:       root,
			Field:      "facets.direct_events",
			Suggestion: "create direct_events/<event>/loom.direct_event.yaml or disable the direct_events facet",
		})
	}
	return items
}

func validateDirectEventPackage(loaded LoadedProject, contract ProjectContract, folderName, eventRoot string, add func(Diagnostic)) DirectEventFacetItem {
	manifestPath := filepath.Join(eventRoot, "loom.direct_event.yaml")
	folder := filepath.ToSlash(mustRelPath(loaded.RootPath, eventRoot))
	item := DirectEventFacetItem{
		Key:              folderName,
		Folder:           folder,
		ManifestPath:     filepath.ToSlash(manifestPath),
		ContractStatus:   ProjectStatusDraft,
		ActivationStatus: DirectEventActivationStatusDisabled,
		ResponseMode:     automation.DirectEventResponseAccepted,
	}
	if !pathExists(manifestPath) {
		add(Diagnostic{
			Severity:   SeverityError,
			Code:       "direct_event.manifest_missing",
			Message:    "direct-event package is missing loom.direct_event.yaml",
			File:       manifestPath,
			Field:      "direct_events." + folderName,
			Suggestion: "add loom.direct_event.yaml or remove the direct-event package folder",
		})
		return item
	}

	event, payload, err := LoadDirectEventContract(manifestPath)
	if err != nil {
		add(Diagnostic{
			Severity: SeverityError,
			Code:     "direct_event.manifest_invalid",
			Message:  "direct-event manifest is invalid: " + err.Error(),
			File:     manifestPath,
			Field:    "direct_events." + folderName,
		})
		return item
	}
	item.ManifestHash = hashBytesURI(payload)
	item.Metadata = event.Metadata
	normalized := normalizeDirectEventContract(event)
	item.Key = normalized.Event.Key
	item.IntegrationKey = normalized.Integration.Key
	item.IntegrationMainAuthLevel = normalized.Integration.MainAuthLevel
	item.BackendIntegrationKey = ProjectDirectEventIntegrationKey(contract.Project.Slug, normalized.Integration.Key)
	item.EndpointSlug = normalized.Endpoint.Slug
	item.BackendEndpointSlug = ProjectDirectEventEndpointSlug(contract.Project.Slug, normalized.Endpoint.Slug)
	item.EndpointPath = "/v1/direct-events/ingest/" + item.BackendEndpointSlug
	item.DisplayName = normalized.Event.DisplayName
	item.Description = firstNonEmptyString(normalized.Event.Description, normalized.Endpoint.Description)
	item.EventType = normalized.Endpoint.EventType
	item.ContractStatus = directEventContractStatus(normalized)
	item.ResponseMode = normalized.Response.Mode
	item.SyncWaitTimeoutSecs = normalized.Response.SyncWaitTimeoutSeconds
	item.TimeoutSeconds = normalized.Timeout.Seconds
	item.MaxAttempts = normalized.Retry.MaxAttempts
	if item.ContractStatus != "disabled" {
		item.ActivationStatus = DirectEventActivationStatusPending
	}

	validateDirectEventContractBasics(normalized, manifestPath, add)
	validateDirectEventTarget(&item, manifestPath, normalized, add)
	validateDirectEventAuth(&item, manifestPath, normalized, add)
	validateDirectEventProfiles(&item, manifestPath, normalized, add)
	validateDirectEventExamples(&item, eventRoot, manifestPath, normalized, add)
	return item
}

func LoadDirectEventContract(path string) (DirectEventContract, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return DirectEventContract{}, nil, err
	}
	var contract DirectEventContract
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&contract); err != nil {
		return DirectEventContract{}, raw, err
	}
	return contract, raw, nil
}

func normalizeDirectEventContract(contract DirectEventContract) DirectEventContract {
	contract.Kind = strings.TrimSpace(contract.Kind)
	contract.SchemaVersion = strings.TrimSpace(contract.SchemaVersion)
	contract.Event.Key = strings.ToLower(strings.TrimSpace(contract.Event.Key))
	contract.Event.DisplayName = strings.TrimSpace(contract.Event.DisplayName)
	if contract.Event.DisplayName == "" {
		contract.Event.DisplayName = contract.Event.Key
	}
	contract.Event.Description = strings.TrimSpace(contract.Event.Description)
	contract.Event.Status = strings.ToLower(strings.TrimSpace(contract.Event.Status))
	if contract.Event.Status == "" {
		contract.Event.Status = ProjectStatusDraft
	}
	contract.Integration.Key = strings.ToLower(strings.TrimSpace(contract.Integration.Key))
	contract.Integration.DisplayName = strings.TrimSpace(contract.Integration.DisplayName)
	if contract.Integration.DisplayName == "" {
		contract.Integration.DisplayName = contract.Integration.Key
	}
	contract.Integration.Description = strings.TrimSpace(contract.Integration.Description)
	contract.Endpoint.Slug = strings.ToLower(strings.TrimSpace(contract.Endpoint.Slug))
	if contract.Endpoint.Slug == "" {
		contract.Endpoint.Slug = contract.Event.Key
	}
	contract.Endpoint.DisplayName = strings.TrimSpace(contract.Endpoint.DisplayName)
	if contract.Endpoint.DisplayName == "" {
		contract.Endpoint.DisplayName = contract.Event.DisplayName
	}
	contract.Endpoint.Description = strings.TrimSpace(contract.Endpoint.Description)
	contract.Endpoint.EventType = strings.TrimSpace(contract.Endpoint.EventType)
	contract.Endpoint.Status = strings.ToLower(strings.TrimSpace(contract.Endpoint.Status))
	if contract.Endpoint.Status == "" {
		contract.Endpoint.Status = contract.Event.Status
	}
	contract.Target.Capability = strings.TrimSpace(contract.Target.Capability)
	contract.Response.Mode = strings.TrimSpace(contract.Response.Mode)
	if contract.Response.Mode == "" {
		contract.Response.Mode = automation.DirectEventResponseAccepted
	}
	for i := range contract.Auth.Profiles {
		contract.Auth.Profiles[i].Name = strings.TrimSpace(contract.Auth.Profiles[i].Name)
		contract.Auth.Profiles[i].Kind = strings.TrimSpace(contract.Auth.Profiles[i].Kind)
		if contract.Auth.Profiles[i].Kind == "" {
			contract.Auth.Profiles[i].Kind = automation.IntegrationAuthPrivateNetwork
		}
		if contract.Auth.Profiles[i].Name == "" {
			contract.Auth.Profiles[i].Name = contract.Auth.Profiles[i].Kind
		}
		contract.Auth.Profiles[i].ExistingRef = strings.TrimSpace(contract.Auth.Profiles[i].ExistingRef)
		contract.Auth.Profiles[i].Token = strings.TrimSpace(contract.Auth.Profiles[i].Token)
	}
	if len(contract.Auth.Profiles) == 0 {
		contract.Auth.Profiles = []DirectEventAuthProfileSpec{{
			Name:            "private_network",
			Kind:            automation.IntegrationAuthPrivateNetwork,
			CreateIfMissing: true,
		}}
	}
	if contract.Mapping.Fields == nil {
		contract.Mapping.Fields = map[string]DirectEventMappingFieldSpec{}
	}
	if contract.Mapping.Defaults == nil {
		contract.Mapping.Defaults = map[string]any{}
	}
	contract.Idempotency.Strategy = strings.TrimSpace(contract.Idempotency.Strategy)
	if contract.Idempotency.Strategy == "" {
		contract.Idempotency.Strategy = automation.DirectEventIdempotencyNone
	}
	contract.Idempotency.Path = strings.TrimSpace(contract.Idempotency.Path)
	contract.Idempotency.Header = strings.TrimSpace(contract.Idempotency.Header)
	contract.Examples.Payload = strings.TrimSpace(contract.Examples.Payload)
	if contract.Examples.Payload == "" {
		contract.Examples.Payload = "examples/payload.json"
	}
	contract.Examples.ExpectedMappedInput = strings.TrimSpace(contract.Examples.ExpectedMappedInput)
	if contract.Examples.ExpectedMappedInput == "" {
		contract.Examples.ExpectedMappedInput = "examples/expected_mapped_input.json"
	}
	return contract
}

func validateDirectEventContractBasics(contract DirectEventContract, manifestPath string, add func(Diagnostic)) {
	if contract.Kind != DirectEventContractKind {
		add(Diagnostic{Severity: SeverityError, Code: "direct_event.kind_invalid", Message: "direct-event contract kind must be " + DirectEventContractKind, File: manifestPath, Field: "kind", Suggestion: "set kind: loom.direct_event"})
	}
	if contract.SchemaVersion != DirectEventSchemaV03 {
		add(Diagnostic{Severity: SeverityError, Code: "direct_event.schema_version_invalid", Message: "direct-event contract schema_version must be " + DirectEventSchemaV03, File: manifestPath, Field: "schema_version", Suggestion: "set schema_version: direct_event.contract.v0.3"})
	}
	if contract.Event.Key == "" {
		add(Diagnostic{Severity: SeverityError, Code: "direct_event.key_required", Message: "event.key is required", File: manifestPath, Field: "event.key"})
	} else if !scheduleKeyPattern.MatchString(contract.Event.Key) {
		add(Diagnostic{Severity: SeverityError, Code: "direct_event.key_invalid", Message: "event.key must match ^[a-z][a-z0-9_-]{0,80}$", File: manifestPath, Field: "event.key"})
	}
	if contract.Integration.Key == "" {
		add(Diagnostic{Severity: SeverityError, Code: "direct_event.integration_key_required", Message: "integration.key is required", File: manifestPath, Field: "integration.key"})
	} else if !scheduleKeyPattern.MatchString(contract.Integration.Key) {
		add(Diagnostic{Severity: SeverityError, Code: "direct_event.integration_key_invalid", Message: "integration.key must match ^[a-z][a-z0-9_-]{0,80}$", File: manifestPath, Field: "integration.key"})
	}
	if contract.Endpoint.Slug == "" {
		add(Diagnostic{Severity: SeverityError, Code: "direct_event.endpoint_slug_required", Message: "endpoint.slug is required", File: manifestPath, Field: "endpoint.slug"})
	} else if !scheduleKeyPattern.MatchString(contract.Endpoint.Slug) {
		add(Diagnostic{Severity: SeverityError, Code: "direct_event.endpoint_slug_invalid", Message: "endpoint.slug must match ^[a-z][a-z0-9_-]{0,80}$", File: manifestPath, Field: "endpoint.slug"})
	}
	if contract.Endpoint.EventType == "" {
		add(Diagnostic{Severity: SeverityError, Code: "direct_event.event_type_required", Message: "endpoint.event_type is required", File: manifestPath, Field: "endpoint.event_type"})
	}
	if !validDirectEventContractStatus(contract.Event.Status) {
		add(Diagnostic{Severity: SeverityError, Code: "direct_event.status_invalid", Message: "event.status is not supported", File: manifestPath, Field: "event.status", Suggestion: "use draft, active, paused, or disabled"})
	}
	if !validDirectEventContractStatus(contract.Endpoint.Status) {
		add(Diagnostic{Severity: SeverityError, Code: "direct_event.endpoint_status_invalid", Message: "endpoint.status is not supported", File: manifestPath, Field: "endpoint.status", Suggestion: "use draft, active, paused, or disabled"})
	}
	if contract.Integration.MainAuthLevel < 0 || contract.Integration.MainAuthLevel > 5 {
		add(Diagnostic{Severity: SeverityError, Code: "direct_event.main_auth_level_invalid", Message: "integration.main_auth_level must be between 1 and 5, or 0 for default", File: manifestPath, Field: "integration.main_auth_level"})
	}
}

func validateDirectEventTarget(item *DirectEventFacetItem, manifestPath string, contract DirectEventContract, add func(Diagnostic)) {
	if contract.Target.Capability == "" {
		add(Diagnostic{Severity: SeverityError, Code: "direct_event.target_required", Message: "target.capability is required", File: manifestPath, Field: "target.capability"})
		return
	}
	normalized, err := capabilities.NormalizeAddress(contract.Target.Capability)
	if err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "direct_event.target_invalid", Message: "target capability address is invalid: " + err.Error(), File: manifestPath, Field: "target.capability"})
		return
	}
	item.TargetCapability = normalized
}

func validateDirectEventAuth(item *DirectEventFacetItem, manifestPath string, contract DirectEventContract, add func(Diagnostic)) {
	seen := map[string]bool{}
	for idx, profile := range contract.Auth.Profiles {
		field := fmt.Sprintf("auth.profiles[%d]", idx)
		profileItem := DirectEventAuthProfileItem{
			Name:            profile.Name,
			Kind:            profile.Kind,
			CreateIfMissing: profile.CreateIfMissing,
			ExistingRef:     profile.ExistingRef,
		}
		item.AuthProfiles = append(item.AuthProfiles, profileItem)
		if profile.Name == "" {
			add(Diagnostic{Severity: SeverityError, Code: "direct_event.auth_name_required", Message: "auth profile name is required", File: manifestPath, Field: field + ".name"})
		}
		if seen[profile.Name] {
			add(Diagnostic{Severity: SeverityError, Code: "direct_event.auth_name_duplicate", Message: "auth profile name is duplicated: " + profile.Name, File: manifestPath, Field: field + ".name"})
		}
		seen[profile.Name] = true
		switch profile.Kind {
		case automation.IntegrationAuthBearerHeader, automation.IntegrationAuthQueryToken, automation.IntegrationAuthPrivateNetwork:
		default:
			add(Diagnostic{Severity: SeverityError, Code: "direct_event.auth_kind_invalid", Message: "unsupported auth profile kind: " + profile.Kind, File: manifestPath, Field: field + ".kind"})
		}
		if profile.Token != "" {
			add(Diagnostic{Severity: SeverityError, Code: "direct_event.secret_value_forbidden", Message: "direct-event contracts must not contain token values", File: manifestPath, Field: field + ".token", Suggestion: "store secret values in LOOM credentials or create auth profiles through the CLI"})
		}
		if profile.Kind != automation.IntegrationAuthPrivateNetwork && profile.CreateIfMissing && profile.ExistingRef == "" {
			add(Diagnostic{Severity: SeverityWarning, Code: "direct_event.token_auth_requires_existing_profile", Message: "token auth cannot be auto-created from project files in this slice", File: manifestPath, Field: field, Suggestion: "create an auth profile through loom integrations auth create, then set existing_ref"})
		}
	}
}

func validateDirectEventProfiles(item *DirectEventFacetItem, manifestPath string, contract DirectEventContract, add func(Diagnostic)) {
	switch contract.Response.Mode {
	case automation.DirectEventResponseAccepted, automation.DirectEventResponseSyncWait:
	default:
		add(Diagnostic{Severity: SeverityError, Code: "direct_event.response_mode_invalid", Message: "unsupported response mode: " + contract.Response.Mode, File: manifestPath, Field: "response.mode"})
	}
	communicationProfile := automation.CommunicationProfile{
		ResponseMode:           contract.Response.Mode,
		SyncWaitTimeoutSeconds: contract.Response.SyncWaitTimeoutSeconds,
	}
	if contract.Response.Mode == automation.DirectEventResponseAccepted {
		communicationProfile.SyncWaitTimeoutSeconds = 0
	}
	if contract.Response.Mode == automation.DirectEventResponseSyncWait && communicationProfile.SyncWaitTimeoutSeconds > 300 {
		add(Diagnostic{Severity: SeverityError, Code: "direct_event.sync_wait_timeout_invalid", Message: "response.sync_wait_timeout_seconds must be <= 300", File: manifestPath, Field: "response.sync_wait_timeout_seconds"})
	}
	item.CommunicationProfileJSON = directEventProfileJSON(communicationProfile)

	mappingJSON, err := compileDirectEventMappingProfile(contract.Mapping)
	if err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "direct_event.mapping_invalid", Message: "direct-event mapping is invalid: " + err.Error(), File: manifestPath, Field: "mapping"})
	} else if normalized, _, err := automation.NormalizeMappingProfile(mappingJSON); err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "direct_event.mapping_invalid", Message: "direct-event mapping is invalid: " + err.Error(), File: manifestPath, Field: "mapping"})
	} else {
		item.MappingProfileJSON = normalized
	}

	idempotencyJSON, err := compileDirectEventIdempotencyProfile(contract.Idempotency)
	if err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "direct_event.idempotency_invalid", Message: "direct-event idempotency profile is invalid: " + err.Error(), File: manifestPath, Field: "idempotency"})
	} else {
		item.IdempotencyProfileJSON = idempotencyJSON
	}

	timeoutProfile, err := automation.NormalizeTimeoutProfile(directEventProfileJSON(automation.TimeoutProfile{TimeoutSeconds: contract.Timeout.Seconds}))
	if err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "direct_event.timeout_invalid", Message: "direct-event timeout profile is invalid: " + err.Error(), File: manifestPath, Field: "timeout.seconds"})
	} else {
		item.TimeoutSeconds = timeoutProfile.TimeoutSeconds
	}
	retryProfile, err := automation.NormalizeRetryProfile(directEventProfileJSON(automation.RetryProfile{MaxAttempts: contract.Retry.MaxAttempts}))
	if err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "direct_event.retry_invalid", Message: "direct-event retry profile is invalid: " + err.Error(), File: manifestPath, Field: "retry.max_attempts"})
	} else {
		item.MaxAttempts = retryProfile.MaxAttempts
	}
	if contract.Storage.PayloadLimitBytes < 0 || contract.Storage.PayloadLimitBytes > 10<<20 {
		add(Diagnostic{Severity: SeverityError, Code: "direct_event.storage_invalid", Message: "storage.payload_limit_bytes must be <= 10485760", File: manifestPath, Field: "storage.payload_limit_bytes"})
	}
	storage := map[string]any{}
	if contract.Storage.PayloadLimitBytes > 0 {
		storage["payload_limit_bytes"] = contract.Storage.PayloadLimitBytes
	}
	storage["store_raw_body"] = contract.Storage.StoreRawBody
	item.StorageProfileJSON = directEventProfileJSON(storage)
}

func validateDirectEventExamples(item *DirectEventFacetItem, eventRoot, manifestPath string, contract DirectEventContract, add func(Diagnostic)) {
	payloadPath, payloadJSON, payloadOK := readDirectEventExample(eventRoot, manifestPath, contract.Examples.Payload, []string{"examples/incoming_payload.json"}, "examples.payload", "direct_event.payload_example", add)
	if payloadOK {
		item.PayloadExamplePath = filepath.ToSlash(payloadPath)
		item.ExamplePayloadJSON = payloadJSON
		item.PayloadExampleHash = hashBytesURI(payloadJSON)
	}
	expectedPath, expectedJSON, expectedOK := readDirectEventExample(eventRoot, manifestPath, contract.Examples.ExpectedMappedInput, []string{"examples/mapped_input.json"}, "examples.expected_mapped_input", "direct_event.expected_input", add)
	if expectedOK {
		item.ExpectedInputPath = filepath.ToSlash(expectedPath)
		item.ExpectedInputJSON = expectedJSON
		item.ExpectedInputHash = hashBytesURI(expectedJSON)
	}
	if !payloadOK || !expectedOK || len(item.MappingProfileJSON) == 0 {
		return
	}
	mapped, missing, _, err := automation.ApplyMappingPreview(item.MappingProfileJSON, automation.MappingPreviewInput{BodyJSON: payloadJSON})
	if err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "direct_event.mapping_preview_failed", Message: "direct-event mapping preview failed: " + err.Error(), File: manifestPath, Field: "mapping"})
		return
	}
	if len(missing) > 0 {
		add(Diagnostic{Severity: SeverityError, Code: "direct_event.mapping_required_missing", Message: "direct-event mapping preview is missing required fields: " + strings.Join(missing, ", "), File: manifestPath, Field: "mapping.required"})
	}
	item.MappedInputJSON = mapped
	if !jsonObjectsEqual(mapped, expectedJSON) {
		add(Diagnostic{Severity: SeverityError, Code: "direct_event.mapping_expected_mismatch", Message: "direct-event mapped input does not match expected mapped input example", File: item.ExpectedInputPath, Field: "examples.expected_mapped_input"})
	}
}

func readDirectEventExample(eventRoot, manifestPath, rel string, aliases []string, field, code string, add func(Diagnostic)) (string, json.RawMessage, bool) {
	normalized, err := normalizeRelativePath(rel)
	if err != nil {
		add(Diagnostic{Severity: SeverityError, Code: code + "_path_unsafe", Message: field + " path is unsafe: " + rel, File: manifestPath, Field: field})
		return "", nil, false
	}
	candidates := append([]string{normalized}, aliases...)
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		path := filepath.Join(eventRoot, filepath.FromSlash(candidate))
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var object map[string]any
		if err := json.Unmarshal(raw, &object); err != nil {
			add(Diagnostic{Severity: SeverityError, Code: code + "_invalid_json", Message: field + " must be a JSON object: " + err.Error(), File: path, Field: field})
			return path, nil, false
		}
		encoded, err := json.Marshal(object)
		if err != nil {
			add(Diagnostic{Severity: SeverityError, Code: code + "_invalid_json", Message: field + " could not be normalized: " + err.Error(), File: path, Field: field})
			return path, nil, false
		}
		return path, json.RawMessage(encoded), true
	}
	add(Diagnostic{Severity: SeverityError, Code: code + "_missing", Message: field + " file could not be read: " + rel, File: filepath.Join(eventRoot, filepath.FromSlash(normalized)), Field: field})
	return "", nil, false
}

func compileDirectEventMappingProfile(spec DirectEventMappingSpec) (json.RawMessage, error) {
	profile := automation.MappingProfile{
		FieldMappings: map[string]string{},
		Defaults:      map[string]any{},
		Required:      []string{},
	}
	for key, value := range spec.Defaults {
		profile.Defaults[key] = value
	}
	for _, required := range spec.Required {
		required = strings.TrimSpace(required)
		if required != "" {
			profile.Required = append(profile.Required, required)
		}
	}
	keys := make([]string, 0, len(spec.Fields))
	for dest := range spec.Fields {
		keys = append(keys, dest)
	}
	sort.Strings(keys)
	for _, dest := range keys {
		mapping := spec.Fields[dest]
		dest = strings.TrimSpace(dest)
		if dest == "" {
			return nil, fmt.Errorf("mapping destination cannot be empty")
		}
		expr, err := compileDirectEventMappingExpression(mapping)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", dest, err)
		}
		profile.FieldMappings[dest] = expr
	}
	return directEventProfileJSON(profile), nil
}

func compileDirectEventMappingExpression(mapping DirectEventMappingFieldSpec) (string, error) {
	if mapping.Literal != nil {
		return "literal:" + fmt.Sprint(mapping.Literal), nil
	}
	source := strings.ToLower(strings.TrimSpace(mapping.Source))
	expr := strings.TrimSpace(mapping.Expr)
	if source == "" {
		source = "body"
	}
	switch source {
	case "body", "headers", "query":
	default:
		return "", fmt.Errorf("unsupported source %q", source)
	}
	if !strings.HasPrefix(expr, "$.") {
		return "", fmt.Errorf("expr must start with $.")
	}
	return "$." + source + "." + strings.TrimPrefix(expr, "$."), nil
}

func compileDirectEventIdempotencyProfile(spec DirectEventIdempotencySpec) (json.RawMessage, error) {
	strategy := strings.TrimSpace(spec.Strategy)
	if strategy == "" {
		strategy = automation.DirectEventIdempotencyNone
	}
	profile := automation.IdempotencyProfile{
		Strategy: strategy,
		Path:     strings.TrimSpace(spec.Path),
		Header:   strings.TrimSpace(spec.Header),
	}
	switch profile.Strategy {
	case automation.DirectEventIdempotencyNone:
		profile.Path = ""
		profile.Header = ""
	case automation.DirectEventIdempotencyPayloadPath:
		if profile.Path == "" {
			return nil, fmt.Errorf("path is required for payload_path")
		}
	case automation.DirectEventIdempotencyHeader:
		if profile.Header == "" {
			return nil, fmt.Errorf("header is required for header idempotency")
		}
	default:
		return nil, fmt.Errorf("unsupported idempotency strategy: %s", profile.Strategy)
	}
	return directEventProfileJSON(profile), nil
}

func directEventContractStatus(contract DirectEventContract) string {
	if contract.Event.Status == "disabled" || contract.Endpoint.Status == "disabled" {
		return "disabled"
	}
	return contract.Event.Status
}

func validDirectEventContractStatus(status string) bool {
	switch status {
	case ProjectStatusDraft, "active", "paused", "disabled":
		return true
	default:
		return false
	}
}

func ignoredDirectEventDir(name string) bool {
	switch name {
	case "tmp", "result", ".cache", ".git":
		return true
	default:
		return false
	}
}

func directEventSummary(items []DirectEventFacetItem) (discovered int) {
	for _, item := range items {
		if item.ManifestPath != "" {
			discovered++
		}
	}
	return discovered
}

func directEventTargetSummaries(items []DirectEventFacetItem) []string {
	out := []string{}
	for _, item := range items {
		if item.ManifestPath == "" {
			continue
		}
		out = append(out, fmt.Sprintf("%s -> %s %s %s", item.Key, item.TargetCapability, item.ResponseMode, item.ActivationStatus))
	}
	sort.Strings(out)
	return out
}

func ProjectDirectEventIntegrationKey(projectSlug, integrationKey string) string {
	return projectDirectEventBackendKey(projectSlug, integrationKey, "project_integration")
}

func ProjectDirectEventEndpointSlug(projectSlug, endpointSlug string) string {
	return projectDirectEventBackendKey(projectSlug, endpointSlug, "project_event")
}

func projectDirectEventBackendKey(projectSlug, key, fallback string) string {
	projectSlug = strings.TrimSpace(projectSlug)
	key = strings.TrimSpace(key)
	base := strings.ReplaceAll(projectSlug, "-", "_") + "__" + key
	if base == "__" {
		base = fallback
	}
	base = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, strings.ToLower(base))
	if base == "" || base[0] < 'a' || base[0] > 'z' {
		base = "p_" + base
	}
	const maxKeyLength = 81
	if len(base) <= maxKeyLength {
		return base
	}
	sum := sha256.Sum256([]byte(projectSlug + "\x00" + key + "\x00" + fallback))
	suffix := "_" + hex.EncodeToString(sum[:])[:12]
	return strings.TrimRight(base[:maxKeyLength-len(suffix)], "_-") + suffix
}

func directEventProfileJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(raw)
}

func jsonObjectsEqual(left, right json.RawMessage) bool {
	var leftValue any
	var rightValue any
	if err := json.Unmarshal(left, &leftValue); err != nil {
		return false
	}
	if err := json.Unmarshal(right, &rightValue); err != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
