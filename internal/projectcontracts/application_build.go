package projectcontracts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// ApplicationBuild is a closed selector, never an application-supplied command.
// An absent selector is resolved by bounded source inspection, not validation.
type ApplicationBuild struct {
	Kind              string   `json:"kind" yaml:"kind"`
	Package           string   `json:"package,omitempty" yaml:"package,omitempty"`
	Features          []string `json:"features,omitempty" yaml:"features,omitempty"`
	NoDefaultFeatures bool     `json:"no_default_features,omitempty" yaml:"no_default_features,omitempty"`
	Attribute         string   `json:"attribute,omitempty" yaml:"attribute,omitempty"`
}

type ApplicationRun struct {
	PythonModule string                `json:"python_module,omitempty" yaml:"python_module,omitempty"`
	Binary       string                `json:"binary,omitempty" yaml:"binary,omitempty"`
	Args         []ApplicationArgument `json:"args,omitempty" yaml:"args,omitempty"`
}

// Arguments preserve literal argv values. The only substitution is a typed
// reference to the resolved configuration file, never arbitrary interpolation.
type ApplicationArgument struct {
	Literal    *string
	ConfigFile bool
}

func LiteralApplicationArgument(value string) ApplicationArgument {
	return ApplicationArgument{Literal: &value}
}

func (a ApplicationArgument) Valid() bool {
	if a.Literal != nil {
		return !a.ConfigFile && len(*a.Literal) <= 4096 && !strings.ContainsRune(*a.Literal, 0)
	}
	return a.ConfigFile
}

func (a ApplicationArgument) MarshalJSON() ([]byte, error) {
	if !a.Valid() {
		return nil, fmt.Errorf("invalid application argument")
	}
	if a.Literal != nil {
		return json.Marshal(*a.Literal)
	}
	return []byte(`{"config_file":true}`), nil
}

func (a *ApplicationArgument) UnmarshalJSON(raw []byte) error {
	if len(raw) > 32*1024 {
		return fmt.Errorf("application argument too large")
	}
	var value ApplicationArgument
	if len(raw) > 0 && raw[0] == '"' {
		if err := json.Unmarshal(raw, &value.Literal); err != nil {
			return err
		}
	} else {
		// The evidence decoder rejects duplicate keys before typed decoding.
		if _, err := DecodeDeclarationEvidenceJSON(raw); err != nil {
			return err
		}
		var token struct {
			ConfigFile bool `json:"config_file"`
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&token); err != nil {
			return err
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return fmt.Errorf("invalid application argument tail")
		}
		value.ConfigFile = token.ConfigFile
	}
	if !value.Valid() {
		return fmt.Errorf("expected literal argument or {config_file: true}")
	}
	*a = value
	return nil
}

func (a ApplicationArgument) MarshalYAML() (any, error) {
	if !a.Valid() {
		return nil, fmt.Errorf("invalid application argument")
	}
	if a.Literal != nil {
		return *a.Literal, nil
	}
	return map[string]bool{"config_file": true}, nil
}

func (a *ApplicationArgument) UnmarshalYAML(node *yaml.Node) error {
	var value ApplicationArgument
	if node.Kind == yaml.ScalarNode && node.Tag == "!!str" {
		value = LiteralApplicationArgument(node.Value)
	} else if node.Kind == yaml.MappingNode && len(node.Content) == 2 &&
		node.Content[0].Value == "config_file" && node.Content[1].Tag == "!!bool" &&
		node.Content[1].Value == "true" {
		value.ConfigFile = true
	} else {
		return fmt.Errorf("expected literal argument or {config_file: true}")
	}
	if !value.Valid() {
		return fmt.Errorf("invalid application argument")
	}
	*a = value
	return nil
}

var applicationProgramName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.+-]{0,127}$`)
var applicationPackageName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]{0,127}$`)
var applicationFeatureName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_+./?-]{0,127}$`)

// ApplicationBuildKind validates selector/alias conflicts without reading files.
// Empty means source inspection must infer a supported producer unambiguously.
func ApplicationBuildKind(a ApplicationDeclaration) (string, string) {
	if a.Source == nil {
		return "", "declaration.application_source"
	}
	s := a.Source
	if s.PythonModule != "" && s.NixAttribute != "" {
		return "", "declaration.application_entrypoint_conflict"
	}
	kind := ""
	if s.PythonModule != "" {
		kind = "python"
	}
	if s.NixAttribute != "" {
		kind = "nix"
	}
	if s.Build != nil {
		b := s.Build
		if b.Kind != "python" && b.Kind != "cargo" && b.Kind != "nix" {
			return "", "declaration.application_build_kind"
		}
		if kind != "" || (b.Kind != "cargo" && (b.Package != "" || len(b.Features) != 0 || b.NoDefaultFeatures)) ||
			(b.Kind != "nix" && b.Attribute != "") {
			return "", "declaration.application_build_conflict"
		}
		if b.Package != "" && !applicationPackageName.MatchString(b.Package) ||
			b.Attribute != "" && !applicationNixAttribute.MatchString(b.Attribute) || len(b.Features) > 64 {
			return "", "declaration.application_build_selector"
		}
		seen := map[string]bool{}
		for _, feature := range b.Features {
			if !applicationFeatureName.MatchString(feature) || seen[feature] {
				return "", "declaration.application_build_selector"
			}
			seen[feature] = true
		}
		kind = b.Kind
	}
	if a.Run != nil {
		r := a.Run
		if r.PythonModule != "" && (s.PythonModule != "" || r.Binary != "" || (kind != "" && kind != "python")) ||
			r.Binary != "" && kind == "python" {
			return "", "declaration.application_entrypoint_conflict"
		}
		if r.PythonModule != "" && !applicationPythonModule.MatchString(r.PythonModule) ||
			r.Binary != "" && !applicationProgramName.MatchString(r.Binary) || !validApplicationArguments(r.Args) {
			return "", "declaration.application_entrypoint"
		}
		if r.PythonModule != "" && kind == "" {
			kind = "python"
		}
	}
	if kind == "python" && s.PythonModule == "" && (a.Run == nil || a.Run.PythonModule == "") {
		return "", "declaration.application_entrypoint"
	}
	return kind, ""
}

func validApplicationArguments(args []ApplicationArgument) bool {
	if len(args) > 32 {
		return false
	}
	for _, arg := range args {
		if !arg.Valid() {
			return false
		}
	}
	return true
}
