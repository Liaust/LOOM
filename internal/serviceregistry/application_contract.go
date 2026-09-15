package serviceregistry

import (
	"bytes"
	"errors"
	"io"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
	pc "loom.local/loom/internal/projectcontracts"
)

const ApplicationContractSchema = "application.contract.v1"

// ApplicationManifest is a closed, non-executable configuration contract for
// one reviewed artifact. No shell, environment, Nix, secret value, or
// arbitrary command is accepted. E2 must implement the reviewed package adapter.
type ApplicationManifest struct {
	Kind          string                                         `json:"kind" yaml:"kind"`
	SchemaVersion string                                         `json:"schema_version" yaml:"schema_version"`
	Artifact      ApplicationArtifact                            `json:"artifact" yaml:"artifact"`
	Process       ApplicationProcess                             `json:"process" yaml:"process"`
	Listener      *ApplicationListener                           `json:"listener" yaml:"listener"`
	Config        ApplicationConfiguration                       `json:"config" yaml:"config"`
	Health        ApplicationHealth                              `json:"health" yaml:"health"`
	Data          map[pc.ResourceKey]ApplicationDataRequirements `json:"data" yaml:"data"`
}

type ApplicationArtifact struct {
	Ref      string `json:"ref" yaml:"ref"`
	Digest   string `json:"digest" yaml:"digest"`
	Platform string `json:"platform" yaml:"platform"`
}
type ApplicationProcess struct {
	Manager        Manager `json:"manager" yaml:"manager"`
	User           string  `json:"user" yaml:"user"`
	Restart        string  `json:"restart" yaml:"restart"`
	MemoryMaxBytes uint64  `json:"memory_max_bytes" yaml:"memory_max_bytes"`
}
type ApplicationListener struct {
	Address string `json:"address" yaml:"address"`
	Port    uint16 `json:"port" yaml:"port"`
}

// The artifact owns this schema's keys and required parameters; LOOM only owns
// bounded value types and reference semantics, not an application protocol list.
type ApplicationConfiguration struct {
	Schema string                          `json:"schema" yaml:"schema"`
	Values map[string]ApplicationParameter `json:"values" yaml:"values"`
}
type ApplicationConfigurationSchema struct {
	Schema     string                              `json:"schema"`
	Parameters map[string]ApplicationParameterSpec `json:"parameters"`
}
type ApplicationParameterType string

const (
	ApplicationString              ApplicationParameterType = "string"
	ApplicationInteger             ApplicationParameterType = "integer"
	ApplicationBoolean             ApplicationParameterType = "boolean"
	ApplicationDataReference       ApplicationParameterType = "data_ref"
	ApplicationCredentialReference ApplicationParameterType = "credential_ref"
)

type ApplicationParameterSpec struct {
	Type     ApplicationParameterType `json:"type"`
	Required bool                     `json:"required"`
}

// Exactly one pointer is present, including an explicit false, zero or empty
// literal. Credential input has only a reference branch, never a secret value.
type ApplicationParameter struct {
	Type          ApplicationParameterType `json:"type" yaml:"type"`
	String        *string                  `json:"string,omitempty" yaml:"string,omitempty"`
	Integer       *int64                   `json:"integer,omitempty" yaml:"integer,omitempty"`
	Boolean       *bool                    `json:"boolean,omitempty" yaml:"boolean,omitempty"`
	DataRef       *pc.ResourceKey          `json:"data_ref,omitempty" yaml:"data_ref,omitempty"`
	CredentialRef *string                  `json:"credential_ref,omitempty" yaml:"credential_ref,omitempty"`
}

// YAML normally coerces numeric/boolean scalars into Go strings. Keep config
// input types exact in both YAML and JSON; custom decoding is closed too.
func (v *ApplicationParameter) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return errors.New("invalid parameter")
	}
	tags := map[string]string{"type": "!!str", "string": "!!str", "integer": "!!int", "boolean": "!!bool", "data_ref": "!!str", "credential_ref": "!!str"}
	for i := 0; i < len(n.Content); i += 2 {
		tag, ok := tags[n.Content[i].Value]
		if !ok || n.Content[i+1].Kind != yaml.ScalarNode || n.Content[i+1].Tag != tag {
			return errors.New("invalid parameter fields or scalar types")
		}
	}
	type plain ApplicationParameter
	var parsed plain
	if n.Decode(&parsed) != nil {
		return errors.New("invalid parameter scalar")
	}
	*v = ApplicationParameter(parsed)
	return nil
}

type ApplicationHealth struct {
	Kind           HealthKind `json:"kind" yaml:"kind"`
	Method         string     `json:"method" yaml:"method"`
	Path           string     `json:"path" yaml:"path"`
	ExpectedStatus int        `json:"expected_status" yaml:"expected_status"`
	TimeoutSeconds int        `json:"timeout_seconds" yaml:"timeout_seconds"`
}
type ApplicationDataRequirements struct {
	MonitorThresholdBytes uint64 `json:"monitor_threshold_bytes" yaml:"monitor_threshold_bytes"`
	QuotaBytes            uint64 `json:"quota_bytes" yaml:"quota_bytes"`
}

var (
	applicationInterpolationPattern = regexp.MustCompile(`\$(?:[A-Za-z_]|[({])`)
	applicationKeyPattern           = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
	applicationRefPattern           = regexp.MustCompile(`^[a-z][a-z0-9_-]*(\.[a-z][a-z0-9_-]*)+$`)
	applicationDigestPattern        = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	applicationProjectPattern       = regexp.MustCompile(`^project_[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	applicationRepoPattern          = regexp.MustCompile(`^repo_[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
)

// ParseApplicationManifest accepts one strict YAML or JSON document. Decoder
// diagnostics are deliberately not returned: they can contain supplied values.
func ParseApplicationManifest(raw []byte) (ApplicationManifest, error) {
	var m ApplicationManifest
	if applicationDecode(raw, &m) != nil {
		return ApplicationManifest{}, errors.New("application manifest: invalid closed document")
	}
	if m.Listener != nil && m.Listener.Address == "" {
		m.Listener.Address = "127.0.0.1"
	}
	valid := m.Kind == "loom.application" && m.SchemaVersion == ApplicationContractSchema && applicationRef(m.Artifact.Ref) && applicationDigestPattern.MatchString(m.Artifact.Digest) && (m.Artifact.Platform == "aarch64-linux" || m.Artifact.Platform == "x86_64-linux")
	valid = valid && m.Process.Manager == ManagerSystemd && applicationKeyPattern.MatchString(m.Process.User) && m.Process.User != "root" && m.Process.User != "loom" && m.Process.User != "orca" && m.Process.MemoryMaxBytes > 0 && (m.Process.Restart == "on-failure" || m.Process.Restart == "always")
	if m.Listener != nil {
		valid = valid && (m.Listener.Address == "127.0.0.1" || m.Listener.Address == "::1") && m.Listener.Port >= 1024
	}
	valid = valid && applicationRef(m.Config.Schema) && m.Config.Values != nil && len(m.Config.Values) <= 64 && m.Data != nil && len(m.Data) <= 16
	for key, value := range m.Config.Values {
		valid = valid && applicationKeyPattern.MatchString(key) && applicationParameterValid(value)
	}
	for key := range m.Data {
		valid = valid && applicationKeyPattern.MatchString(string(key))
	}
	switch m.Health.Kind {
	case HealthKindManager:
		valid = valid && m.Health.Method == "" && m.Health.Path == "" && m.Health.ExpectedStatus == 0 && m.Health.TimeoutSeconds == 0
	case HealthKindHTTP:
		valid = valid && m.Listener != nil && applicationHealthPath(m.Health.Path) && (m.Health.Method == "GET" || m.Health.Method == "HEAD" || m.Health.Method == "OPTIONS") && ((m.Health.ExpectedStatus >= 200 && m.Health.ExpectedStatus <= 399) || m.Health.ExpectedStatus == 401 || m.Health.ExpectedStatus == 403) && m.Health.TimeoutSeconds > 0 && m.Health.TimeoutSeconds <= 60
	default:
		valid = false
	}
	if !valid {
		return ApplicationManifest{}, errors.New("application manifest: unsupported or invalid typed requirement")
	}
	return m, nil
}

func applicationRef(v string) bool { return len(v) <= 128 && applicationRefPattern.MatchString(v) }
func applicationText(v string) bool {
	return v != "" && len(v) <= 4096 && strings.TrimSpace(v) == v && !strings.ContainsFunc(v, unicode.IsControl)
}
func applicationRelative(v string, metadata bool) bool {
	if !applicationText(v) || path.IsAbs(v) || path.Clean(v) != v || v == "." || strings.ContainsAny(v, `\:`) {
		return false
	}
	for _, p := range strings.Split(v, "/") {
		if p == ".." || p == ".git" || p == ".repo" || (!metadata && p == ".loom") {
			return false
		}
	}
	return true
}
func applicationAbsolute(v string) bool {
	return applicationText(v) && path.IsAbs(v) && path.Clean(v) == v && v != "/" && !strings.ContainsAny(v, `\:`)
}
func applicationOverlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// Shared local decoder deliberately refuses aliases, duplicate keys (including
// JSON escaped duplicates), nulls, non-string keys, custom tags and extra docs.
func applicationDecode(raw []byte, out any) error {
	if len(raw) == 0 || len(raw) > 1024*1024 {
		return errors.New("invalid source size")
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var n yaml.Node
	if err := dec.Decode(&n); err != nil {
		return errors.New("invalid document")
	}
	var tail yaml.Node
	if dec.Decode(&tail) != io.EOF {
		return errors.New("multiple documents")
	}
	var walk func(*yaml.Node) bool
	walk = func(n *yaml.Node) bool {
		if n.Anchor != "" || n.Alias != nil || n.Tag == "!!null" {
			return false
		}
		switch n.Tag {
		case "", "!!map", "!!seq", "!!str", "!!int", "!!bool":
		default:
			return false
		}
		if n.Kind == yaml.MappingNode {
			seen := map[string]bool{}
			for k := 0; k < len(n.Content); k += 2 {
				key := n.Content[k]
				if key.Tag != "!!str" || seen[key.Value] {
					return false
				}
				seen[key.Value] = true
			}
		}
		for _, c := range n.Content {
			if !walk(c) {
				return false
			}
		}
		return true
	}
	if !walk(&n) {
		return errors.New("invalid document shape")
	}
	dec = yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return errors.New("invalid document fields")
	}
	return nil
}

func applicationParameterType(t ApplicationParameterType) bool {
	switch t {
	case ApplicationString, ApplicationInteger, ApplicationBoolean, ApplicationDataReference, ApplicationCredentialReference:
		return true
	}
	return false
}
func applicationParameterValid(v ApplicationParameter) bool {
	count := 0
	for _, present := range []bool{v.String != nil, v.Integer != nil, v.Boolean != nil, v.DataRef != nil, v.CredentialRef != nil} {
		if present {
			count++
		}
	}
	if count != 1 {
		return false
	}
	switch v.Type {
	case ApplicationString:
		return v.String != nil && len(*v.String) <= 4096 && !strings.ContainsFunc(*v.String, unicode.IsControl) && !applicationInterpolationPattern.MatchString(*v.String) && !strings.Contains(*v.String, "`")
	case ApplicationInteger:
		return v.Integer != nil
	case ApplicationBoolean:
		return v.Boolean != nil
	case ApplicationDataReference:
		return v.DataRef != nil && applicationKeyPattern.MatchString(string(*v.DataRef))
	case ApplicationCredentialReference:
		return v.CredentialRef != nil && applicationRef(*v.CredentialRef)
	}
	return false
}
func applicationHealthPath(v string) bool {
	u, err := url.Parse(v)
	if !applicationText(v) || len(v) > 2048 || err != nil || u == nil || u.Scheme != "" || u.Host != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") || path.Clean(v) != v || strings.ContainsAny(v, "%\\?#") {
		return false
	}
	return true
}

func applicationConfigurationBindings(c ApplicationConfiguration, s ApplicationConfigurationSchema) (map[pc.ResourceKey]bool, map[string]bool, string) {
	data := map[pc.ResourceKey]bool{}
	credentials := map[string]bool{}
	if !applicationRef(s.Schema) || c.Schema != s.Schema || s.Parameters == nil || len(s.Parameters) > 64 {
		return data, credentials, "artifact.configuration_schema"
	}
	for key, spec := range s.Parameters {
		if !applicationKeyPattern.MatchString(key) || !applicationParameterType(spec.Type) {
			return data, credentials, "artifact.configuration_schema"
		}
		v, present := c.Values[key]
		if !present {
			if spec.Required {
				return data, credentials, "artifact.configuration_parameters"
			}
			continue
		}
		if v.Type != spec.Type || !applicationParameterValid(v) {
			return data, credentials, "artifact.configuration_value"
		}
	}
	for key, v := range c.Values {
		if _, ok := s.Parameters[key]; !ok {
			return data, credentials, "artifact.configuration_parameters"
		}
		switch v.Type {
		case ApplicationDataReference:
			data[*v.DataRef] = true
		case ApplicationCredentialReference:
			credentials[*v.CredentialRef] = true
		}
	}
	return data, credentials, ""
}
