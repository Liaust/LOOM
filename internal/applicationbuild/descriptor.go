package applicationbuild

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	sr "loom.local/loom/internal/serviceregistry"
)

// Manifest is producer output, not an installable application contract. Binding
// names intentionally carry no credential values, protected paths or authority.
type Manifest struct {
	Program              *Program                    `json:"program,omitempty"`
	SchemaVersion        string                      `json:"schema_version"`
	Source               SourceIdentity              `json:"source"`
	PythonModule         string                      `json:"python_module"`
	Files                []SourceFile                `json:"files"`
	Artifact             sr.ApplicationArtifact      `json:"artifact"`
	Configuration        sr.ApplicationConfiguration `json:"configuration"`
	DataParameters       []string                    `json:"data_parameters"`
	CredentialParameters []string                    `json:"credential_parameters"`
}

// Descriptor deliberately has its own producer schema. The integrator converts
// project-backed identity into artifact v2; it must not relabel this as v1 or
// invent a repository ID. Shared metadata uses the registry's canonical types.
type Descriptor struct {
	Actions               map[string]sr.ApplicationActionDefinition `json:"actions,omitempty"`
	SchemaVersion         string                                    `json:"schema_version"`
	Source                SourceIdentity                            `json:"source"`
	SourceDigest          string                                    `json:"source_digest"`
	ToolchainDigest       string                                    `json:"toolchain_digest"`
	BuildDigest           string                                    `json:"build_digest"`
	Artifact              sr.ApplicationArtifact                    `json:"artifact"`
	StoreRoot             string                                    `json:"store_root"`
	Launcher              string                                    `json:"launcher"`
	Executable            string                                    `json:"executable"`
	Closure               []sr.ApplicationClosurePath               `json:"closure"`
	ClosureDigest         string                                    `json:"closure_digest"`
	Configuration         sr.ApplicationConfigurationSchema         `json:"configuration"`
	ConfigurationDigest   string                                    `json:"configuration_digest"`
	DataFormat            string                                    `json:"data_format"`
	CompatibleDataFormats []string                                  `json:"compatible_data_formats"`
}

type Output struct {
	Manifest         Manifest        `json:"manifest"`
	Descriptor       Descriptor      `json:"descriptor"`
	ManifestJSON     json.RawMessage `json:"manifest_json"`
	DescriptorJSON   json.RawMessage `json:"descriptor_json"`
	ManifestDigest   string          `json:"manifest_digest"`
	DescriptorDigest string          `json:"descriptor_digest"`
	Reused           bool            `json:"reused"`
}

func assemble(identity SourceIdentity, module string, files []SourceFile, sourceDigest, toolchainDigest, buildDigest string, actual realization, schema sr.ApplicationConfigurationSchema, literals sr.ApplicationConfiguration, data, credentials []string, dataFormat string, compatible []string, platform string, actions map[string]Action) Output {
	closure := append([]sr.ApplicationClosurePath{}, actual.Closure...)
	closureDigest := sr.ApplicationClosureDigest(closure)
	configurationDigest := sr.ApplicationConfigurationSchemaDigest(schema)
	artifact := sr.ApplicationArtifact{Ref: "application." + identity.Resource, Platform: platform,
		Digest: hashJSON(struct{ Build, Root, Closure, Configuration string }{buildDigest, actual.StoreRoot, closureDigest, configurationDigest})}
	descriptor := Descriptor{
		SchemaVersion: "application.build.artifact.v1", Source: identity,
		SourceDigest: sourceDigest, ToolchainDigest: toolchainDigest, BuildDigest: buildDigest,
		Artifact: artifact, StoreRoot: actual.StoreRoot,
		Launcher: actual.StoreRoot + "/bin/loom-application-launcher", Executable: actual.Executable,
		Closure: closure, ClosureDigest: closureDigest, Configuration: schema, ConfigurationDigest: configurationDigest,
		DataFormat: dataFormat, CompatibleDataFormats: compatible,
		Actions: map[string]sr.ApplicationActionDefinition{},
	}
	for name, action := range actions {
		descriptor.Actions[name] = sr.ApplicationActionDefinition{ReadOnly: action.ReadOnly}
	}
	manifest := Manifest{SchemaVersion: "application.build.manifest.v1", Source: identity, PythonModule: module,
		Files: files, Artifact: artifact, Configuration: literals, DataParameters: data, CredentialParameters: credentials}
	manifestJSON, _ := json.Marshal(manifest)
	descriptorJSON, _ := json.Marshal(descriptor)
	return Output{Manifest: manifest, Descriptor: descriptor, ManifestJSON: manifestJSON, DescriptorJSON: descriptorJSON,
		ManifestDigest: hashBytes(manifestJSON), DescriptorDigest: hashBytes(descriptorJSON)}
}

func configurationMetadata(in Input) (sr.ApplicationConfigurationSchema, sr.ApplicationConfiguration, []string, []string, error) {
	schema := in.ConfigurationSchema
	if schema == "" {
		schema = "application." + in.Source.Resource
	}
	specs := sr.ApplicationConfigurationSchema{Schema: schema, Parameters: map[string]sr.ApplicationParameterSpec{}}
	literals := sr.ApplicationConfiguration{Schema: schema, Values: map[string]sr.ApplicationParameter{}}
	if !refPattern.MatchString(schema) || len(schema) > 128 || len(in.Config)+len(in.DataParameters)+len(in.CredentialParameters) > 64 || len(in.DataParameters) > 16 || len(in.CredentialParameters) > 16 {
		return specs, literals, nil, nil, ErrInvalidInput
	}
	for key, raw := range in.Config {
		value, ok := literal(raw)
		if !keyPattern.MatchString(key) || !ok {
			return specs, literals, nil, nil, ErrInvalidInput
		}
		literals.Values[key] = value
		specs.Parameters[key] = sr.ApplicationParameterSpec{Type: value.Type, Required: true}
	}
	for _, group := range []struct {
		names []string
		kind  sr.ApplicationParameterType
	}{
		{in.DataParameters, sr.ApplicationDataReference}, {in.CredentialParameters, sr.ApplicationCredentialReference},
	} {
		for _, key := range group.names {
			if _, exists := specs.Parameters[key]; exists || !keyPattern.MatchString(key) {
				return specs, literals, nil, nil, ErrInvalidInput
			}
			specs.Parameters[key] = sr.ApplicationParameterSpec{Type: group.kind, Required: true}
		}
	}
	return specs, literals, sortedUnique(in.DataParameters), sortedUnique(in.CredentialParameters), nil
}

var interpolationPattern = regexp.MustCompile(`\$(?:[A-Za-z_]|[({])`)

func literal(raw any) (sr.ApplicationParameter, bool) {
	switch value := raw.(type) {
	case string:
		valid := len(value) <= 4096 && !strings.ContainsFunc(value, unicode.IsControl) && !strings.Contains(value, "`") && !interpolationPattern.MatchString(value)
		return sr.ApplicationParameter{Type: sr.ApplicationString, String: &value}, valid
	case bool:
		return sr.ApplicationParameter{Type: sr.ApplicationBoolean, Boolean: &value}, true
	case json.Number:
		parsed, err := strconv.ParseInt(string(value), 10, 64)
		return sr.ApplicationParameter{Type: sr.ApplicationInteger, Integer: &parsed}, err == nil && strconv.FormatInt(parsed, 10) == string(value)
	case int:
		return literal(int64(value))
	case int8:
		return literal(int64(value))
	case int16:
		return literal(int64(value))
	case int32:
		return literal(int64(value))
	case int64:
		return sr.ApplicationParameter{Type: sr.ApplicationInteger, Integer: &value}, true
	case uint:
		return literal(uint64(value))
	case uint8:
		return literal(int64(value))
	case uint16:
		return literal(int64(value))
	case uint32:
		return literal(int64(value))
	case uint64:
		if value <= math.MaxInt64 {
			return literal(int64(value))
		}
	}
	return sr.ApplicationParameter{}, false
}

func sortedUnique(values []string) []string {
	result := append([]string{}, values...)
	sort.Strings(result)
	return slices.Compact(result)
}

func hashBytes(raw []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) }
func hashJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	} // All callers use closed, JSON-safe producer types.
	return hashBytes(raw)
}
