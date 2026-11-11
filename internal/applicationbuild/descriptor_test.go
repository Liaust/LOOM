package applicationbuild

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func TestConfigurationRefusesLossyOrSecretBranches(t *testing.T) {
	for name, value := range map[string]any{
		"float": float64(3), "fraction": json.Number("1.5"), "exponent": json.Number("1e3"),
		"overflow": json.Number("9223372036854775808"), "unsigned-overflow": uint64(math.MaxInt64) + 1,
		"null": nil, "object": map[string]any{"secret": "value"}, "array": []string{"value"},
		"control": "line\nline", "interpolation": "${TOKEN}", "backtick": "`command`",
	} {
		t.Run(name, func(t *testing.T) {
			s, input, runner := fixture(t)
			input.Config = map[string]any{"setting": value}
			if _, err := s.Build(t.Context(), input); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("invalid config accepted: %v", err)
			}
			if len(runner.commands) != 0 {
				t.Fatal("invalid config reached runner")
			}
		})
	}
	for _, change := range []func(*Input){
		func(in *Input) { in.DataParameters = append(in.DataParameters, "limit") },
		func(in *Input) { in.CredentialParameters = append(in.CredentialParameters, "database") },
		func(in *Input) { in.CredentialParameters = []string{"oauth", "oauth"} },
		func(in *Input) { in.CredentialParameters = []string{"pass://value"} },
		func(in *Input) { in.ConfigurationSchema = "invalid" },
	} {
		s, input, _ := fixture(t)
		change(&input)
		if _, err := s.Build(t.Context(), input); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid binding accepted: %v", err)
		}
	}
}

func TestProjectBackedAndRepositoryIdentity(t *testing.T) {
	s, input, _ := fixture(t)
	input.Source.RepositoryID = "repo_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	input.Source.Revision = "1234567890123456789012345678901234567890"
	output, err := s.Build(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if output.Descriptor.Source != input.Source {
		t.Fatal("existing repository identity was not preserved")
	}
	input.Source.RepositoryID = "fake-repository"
	if _, err := s.Snapshot(t.Context(), input); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid repository identity accepted: %v", err)
	}
}

func TestInvalidModuleAndEntrypoint(t *testing.T) {
	for _, module := range []string{"app.main;evil", "app.${evil}", "app/more", ".app", "app..main", "app.missing"} {
		s, input, _ := fixture(t)
		input.PythonModule = module
		if _, err := s.Snapshot(t.Context(), input); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid module %q accepted: %v", module, err)
		}
	}
}
