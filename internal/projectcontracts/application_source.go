package projectcontracts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Source is authored once; packaging, process identity and bindings are derived.
type ApplicationSource struct {
	Build        *ApplicationBuild `json:"build,omitempty" yaml:"build,omitempty"`
	Files        []string          `json:"files,omitempty" yaml:"files,omitempty"`
	Path         string            `json:"path" yaml:"path"`
	PythonModule string            `json:"python_module,omitempty" yaml:"python_module,omitempty"`
	NixAttribute string            `json:"nix_attribute,omitempty" yaml:"nix_attribute,omitempty"`
}

type ApplicationSourceConfig struct {
	Schema string         `json:"schema,omitempty" yaml:"schema,omitempty"`
	Values map[string]any `json:"values,omitempty" yaml:"values,omitempty"`
}

// Keep exact integers identical across YAML source and captured JSON evidence.
// The default interface decoder turns them into float64 during plan cloning.
func (c *ApplicationSourceConfig) UnmarshalJSON(raw []byte) error {
	if _, err := DecodeDeclarationEvidenceJSON(raw); err != nil {
		return err
	}
	type plain ApplicationSourceConfig
	var value plain
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	d.DisallowUnknownFields()
	if err := d.Decode(&value); err != nil {
		return err
	}
	*c = ApplicationSourceConfig(value)
	return nil
}

func (c *ApplicationSourceConfig) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("application config must be a mapping")
	}
	seen := map[string]bool{}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if seen[key] || (key != "schema" && key != "values") {
			return fmt.Errorf("unknown or duplicate application config field")
		}
		seen[key] = true
	}
	type plain ApplicationSourceConfig
	var value plain
	if err := node.Decode(&value); err != nil {
		return err
	}
	for key, v := range value.Values {
		switch n := v.(type) {
		case int:
			value.Values[key] = json.Number(strconv.FormatInt(int64(n), 10))
		case int64:
			value.Values[key] = json.Number(strconv.FormatInt(n, 10))
		case uint64:
			value.Values[key] = json.Number(strconv.FormatUint(n, 10))
		}
	}
	*c = ApplicationSourceConfig(value)
	return nil
}

type ApplicationAction struct {
	PythonModule string                `json:"python_module" yaml:"python_module"`
	Binary       string                `json:"binary,omitempty" yaml:"binary,omitempty"`
	Args         []ApplicationArgument `json:"args,omitempty" yaml:"args,omitempty"`
	ReadOnly     bool                  `json:"read_only,omitempty" yaml:"read_only,omitempty"`
}

var applicationPythonModule = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)
var applicationNixAttribute = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*(\.[A-Za-z_][A-Za-z0-9_-]*)*$`)

func ValidateApplicationSource(a ApplicationDeclaration) string {
	s := a.Source
	if s == nil || (s.Path != "" && s.Path != "." && !declarationPath(s.Path, false)) {
		return "declaration.application_source"
	}
	kind, code := ApplicationBuildKind(a)
	if code != "" {
		return code
	}
	if (s.PythonModule != "" && !applicationPythonModule.MatchString(s.PythonModule)) || (s.NixAttribute != "" && !applicationNixAttribute.MatchString(s.NixAttribute)) {
		return "declaration.application_entrypoint"
	}
	if len(s.Files) > 64 {
		return "declaration.application_source_limit"
	}
	seenFiles := map[string]bool{}
	for _, name := range s.Files {
		if !declarationPath(name, false) || seenFiles[name] {
			return "declaration.application_source_file"
		}
		seenFiles[name] = true
	}
	if a.Repository != "" || a.Manifest != "" || a.ArtifactDescriptor != "" || len(a.Credentials) != 0 {
		return "declaration.application_source_conflict"
	}
	if a.Network != "" && a.Network != "loopback" && a.Network != "outbound_https" {
		return "declaration.application_network"
	}
	if a.Endpoint != nil {
		// Source mode starts with private workers; public HTTP retains its reviewed adapter.
		return "declaration.application_source_endpoint"
	}
	if len(a.CredentialSources) > 16 || len(a.Data) > 16 || len(a.Actions) > 16 {
		return "declaration.application_source_limit"
	}
	for key, ref := range a.CredentialSources {
		if !declarationKey(key) {
			return "declaration.credential_sources"
		}
		if _, _, ok := ApplicationCredentialSourceShare(ref); !ok && !ValidApplicationRuntimeCredentialReference(ref) {
			return "declaration.credential_sources"
		}
	}
	for key, data := range a.Data {
		if !declarationKey(string(key)) || (data.Path == "") == (data.BindingRef == "") || (data.Path != "" && !declarationPath(data.Path, false)) || (data.BindingRef != "" && !validCredentialRef(data.BindingRef)) || data.Protection != "" || data.Browse != nil || (data.Backup != "" && data.Backup != "cloud_history") || (data.Capacity != nil && data.Capacity.PlannedBytes == 0) {
			return "declaration.application_source_data"
		}
		if _, conflict := a.CredentialSources[string(key)]; conflict {
			return "declaration.application_parameter_conflict"
		}
	}
	if a.Config != nil {
		if a.Config.Schema != "" && !validCredentialRef(a.Config.Schema) || len(a.Config.Values) > 64 {
			return "declaration.application_config"
		}
		for key, value := range a.Config.Values {
			if !declarationKey(key) {
				return "declaration.application_config"
			}
			if _, ok := a.Data[ResourceKey(key)]; ok {
				return "declaration.application_parameter_conflict"
			}
			if _, ok := a.CredentialSources[key]; ok {
				return "declaration.application_parameter_conflict"
			}
			switch n := value.(type) {
			case string:
				if len(n) > 4096 || strings.ContainsRune(n, 0) {
					return "declaration.application_config"
				}
			case bool, int, int32, int64:
			case uint, uint32, uint64, json.Number:
				raw, _ := json.Marshal(n)
				if _, err := strconv.ParseInt(string(raw), 10, 64); err != nil {
					return "declaration.application_config"
				}
			default:
				return "declaration.application_config"
			}
		}
	}
	for key, action := range a.Actions {
		if !declarationKey(key) || !validApplicationArguments(action.Args) {
			return "declaration.application_action"
		}
		if kind == "python" {
			module := s.PythonModule
			if a.Run != nil && a.Run.PythonModule != "" {
				module = a.Run.PythonModule
			}
			if action.Binary != "" || !applicationPythonModule.MatchString(action.PythonModule) || strings.Split(action.PythonModule, ".")[0] != strings.Split(module, ".")[0] {
				return "declaration.application_action"
			}
		} else if action.PythonModule != "" || action.Binary != "" && !applicationProgramName.MatchString(action.Binary) {
			return "declaration.application_action"
		}
	}
	return ""
}

func ValidApplicationRuntimeCredentialReference(ref string) bool {
	if !strings.HasPrefix(ref, "runtime://") {
		return false
	}
	id := strings.TrimPrefix(ref, "runtime://")
	if len(id) != 64 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
