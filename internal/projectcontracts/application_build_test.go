package projectcontracts

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestApplicationBuildSelectorsAndArguments(t *testing.T) {
	for _, source := range []string{
		"source: {path: repos/server}",
		"source: {path: repos/server, build: {kind: cargo, package: server, features: [http], no_default_features: true}}\nrun: {binary: server, args: [serve, --config, {config_file: true}]}\nactions: {status: {args: [status, {config_file: true}], read_only: true}}",
		"source: {path: ., build: {kind: python}}\nrun: {python_module: app.main}",
		"source: {path: repos/server, build: {kind: nix, attribute: server}}\nrun: {binary: server}",
		"source: {path: ., nix_attribute: server}\nrun: {args: [serve]}",
	} {
		var application ApplicationDeclaration
		if err := yaml.Unmarshal([]byte(source), &application); err != nil {
			t.Fatal(err)
		}
		if code := ValidateApplicationSource(application); code != "" {
			t.Fatalf("valid selector refused: %s: %s", source, code)
		}
		raw, err := json.Marshal(application)
		if err != nil {
			t.Fatal(err)
		}
		var restored ApplicationDeclaration
		if err := json.Unmarshal(raw, &restored); err != nil || ValidateApplicationSource(restored) != "" {
			t.Fatalf("new declaration did not round trip: %v", err)
		}
		if next, err := json.Marshal(restored); err != nil || !bytes.Equal(raw, next) {
			t.Fatal("new selectors/arguments changed on evidence round trip")
		}
	}
}

func TestApplicationBuildRefusesConflictsAndWrongUnion(t *testing.T) {
	for _, source := range []string{
		"source: {path: ., python_module: app.main, nix_attribute: server}",
		"source: {path: ., python_module: app.main, build: {kind: python}}",
		"source: {path: ., python_module: app.main}\nrun: {python_module: app.other}",
		"source: {path: ., build: {kind: cargo, attribute: server}}",
		"source: {path: ., build: {kind: cargo, features: [http, http]}}",
		"source: {path: ., build: {kind: shell}}",
		"source: {path: ., build: {kind: python, package: server}}\nrun: {python_module: app.main}",
		"source: {path: ., build: {kind: python}}",
		"source: {path: ., build: {kind: cargo}}\nrun: {python_module: app.main}",
		"source: {path: ., build: {kind: nix, attribute: ../server}}",
		"source: {path: ., build: {kind: cargo}}\nrun: {binary: bin/server}",
		"source: {path: ., build: {kind: cargo}}\nactions: {status: {python_module: app.main}}",
	} {
		var application ApplicationDeclaration
		if err := yaml.Unmarshal([]byte(source), &application); err == nil && ValidateApplicationSource(application) == "" {
			t.Fatalf("ambiguous/invalid build accepted: %s", source)
		}
	}
}

func TestApplicationArgumentClosedToken(t *testing.T) {
	for _, raw := range []string{
		`null`, `false`, `42`, `{}`, `{"config_file":false}`,
		`{"config_file":true,"other":"x"}`, `{"config_file":true,"config_file":true}`,
		`{"env":"TOKEN"}`, `{"config_file":"true"}`, `"nul\u0000arg"`,
	} {
		var arg ApplicationArgument
		if err := json.Unmarshal([]byte(raw), &arg); err == nil {
			t.Fatalf("invalid JSON argument accepted: %s", raw)
		}
	}
	for _, raw := range []string{
		`null`, `true`, `42`, `{config_file: false}`, `{config_file: "true"}`,
		`{config_file: true, other: x}`, `{config_file: true, config_file: true}`,
	} {
		var arg ApplicationArgument
		if err := yaml.Unmarshal([]byte(raw), &arg); err == nil && arg.Valid() {
			t.Fatalf("invalid YAML argument accepted: %s", raw)
		}
	}
	for _, raw := range []string{`""`, `"literal $VALUE; not a command"`, `{"config_file":true}`} {
		var arg ApplicationArgument
		if err := json.Unmarshal([]byte(raw), &arg); err != nil {
			t.Fatal(err)
		}
		if next, err := json.Marshal(arg); err != nil || string(next) != raw {
			t.Fatalf("argv value changed: %s / %s / %v", raw, next, err)
		}
	}
	if _, err := json.Marshal(ApplicationArgument{Literal: new(string), ConfigFile: true}); err == nil {
		t.Fatal("ambiguous in-memory token serialized")
	}
	// JSON escapes may be larger than the bounded literal without changing argv.
	long := LiteralApplicationArgument(strings.Repeat("&", 4096))
	raw, err := json.Marshal(long)
	var restored ApplicationArgument
	if err != nil || json.Unmarshal(raw, &restored) != nil || restored.Literal == nil || *restored.Literal != *long.Literal {
		t.Fatal("valid escaped argv value failed its own round trip")
	}
}

func TestApplicationBuildDeclarationShapeAndLegacyIdentity(t *testing.T) {
	raw := strings.Replace(string(fixtureRead(t, "minimal.yaml")), "resources: {}", `resources:
  server:
    kind: application
    application:
      source: {path: repos/server, build: {kind: cargo}}
      run: {binary: server, args: [serve, {config_file: true}]}
      actions: {status: {args: [status], read_only: true}}
`, 1)
	if _, err := ParseProjectDeclaration([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		strings.Replace(raw, "kind: cargo", "kind: cargo, command: arbitrary", 1),
		strings.Replace(raw, "{config_file: true}", "{config_file: true, other: x}", 1),
		strings.Replace(raw, "{config_file: true}", "{config_file: true, config_file: true}", 1),
	} {
		if _, err := ParseProjectDeclaration([]byte(invalid)); err == nil {
			t.Fatal("closed argument/build shape bypassed")
		}
	}
	const historical = `{"source":{"path":".","python_module":"app.main"},"actions":{"status":{"python_module":"app.main","args":["--status","literal $VALUE; not a command"],"read_only":true}},"repository":"","manifest":""}`
	var legacy ApplicationDeclaration
	if err := json.Unmarshal([]byte(historical), &legacy); err != nil || ValidateApplicationSource(legacy) != "" {
		t.Fatalf("historical Python declaration unreadable: %v", err)
	}
	encoded, err := json.Marshal(legacy)
	if err != nil || string(encoded) != historical {
		t.Fatalf("legacy serialized identity changed: %s / %v", encoded, err)
	}
}
