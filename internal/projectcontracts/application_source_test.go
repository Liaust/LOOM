package projectcontracts

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestApplicationConfigExactIntegerEvidence(t *testing.T) {
	var config ApplicationSourceConfig
	if err := yaml.Unmarshal([]byte("schema: intake.v1\nvalues: {large: 9007199254740993, enabled: false}\n"), &config); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip ApplicationSourceConfig
	if err := json.Unmarshal(raw, &roundTrip); err != nil || roundTrip.Values["large"] != json.Number("9007199254740993") {
		t.Fatalf("exact setting changed: %#v, %v", roundTrip, err)
	}
	for _, raw := range []string{
		`{"unknown":true,"values":{}}`,
		`{"values":{"count":1,"count":2}}`,
	} {
		if err := json.Unmarshal([]byte(raw), &roundTrip); err == nil {
			t.Fatal("ambiguous setting accepted", raw)
		}
	}
	for _, raw := range []string{"unknown: true\nvalues: {}", "values: {}\nvalues: {}"} {
		if err := yaml.Unmarshal([]byte(raw), &roundTrip); err == nil {
			t.Fatal("ambiguous YAML accepted", raw)
		}
	}
}

func TestApplicationSourceCompilesWithoutSidecarsOrRepository(t *testing.T) {
	raw := strings.Replace(string(fixtureRead(t, "minimal.yaml")), "resources: {}", `resources:
  intake:
    kind: application
    application:
      source: {path: ., python_module: intake.worker}
      network: outbound_https
      config: {schema: intake.worker.v1, values: {poll_seconds: 5}}
      data:
        database: {path: .data}
`, 1)
	analysis := Analyze(declarationTestRoot(t, []byte(raw)))
	if !analysis.Report.OK || analysis.Loaded == nil || analysis.Loaded.Declaration == nil {
		t.Fatalf("source compile: %+v", analysis.Report.Diagnostics)
	}
	if len(analysis.Plan.Declaration.Repositories) != 0 || len(analysis.Plan.Declaration.Sources) != 1 {
		t.Fatal("source mode manufactured sidecar/repository prerequisites")
	}
}

func TestApplicationSourceRequiresOnlyActualInputs(t *testing.T) {
	a := ApplicationDeclaration{Source: &ApplicationSource{Path: ".", PythonModule: "housing_intake.runtime_service"}, Data: map[ResourceKey]ApplicationData{"database": {Path: ".data"}}, Network: "outbound_https"}
	if code := ValidateApplicationSource(a); code != "" {
		t.Fatal(code)
	}
	for _, mutate := range []func(*ApplicationDeclaration){
		func(a *ApplicationDeclaration) { a.Manifest = "manifest.yaml" },
		func(a *ApplicationDeclaration) { a.Repository = "repo" },
		func(a *ApplicationDeclaration) { a.Network = "unrestricted" },
		func(a *ApplicationDeclaration) {
			a.Source = &ApplicationSource{Path: "../other", PythonModule: "housing_intake.runtime_service"}
		},
		func(a *ApplicationDeclaration) {
			a.Config = &ApplicationSourceConfig{Values: map[string]any{"database": "other"}}
		},
		func(a *ApplicationDeclaration) { a.Actions = map[string]ApplicationAction{"bad": {PythonModule: "os"}} },
	} {
		next := a
		mutate(&next)
		if code := ValidateApplicationSource(next); code == "" {
			t.Fatal("invalid source accepted")
		}
	}
}

func TestRuntimeCredentialReferenceIsOpaque(t *testing.T) {
	for _, ref := range []string{"/tmp/token", "runtime://../token", "runtime://short", "runtime://"} {
		if ValidApplicationRuntimeCredentialReference(ref) {
			t.Fatal(ref)
		}
	}
	if !ValidApplicationRuntimeCredentialReference("runtime://0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef") {
		t.Fatal("valid reference")
	}
}
