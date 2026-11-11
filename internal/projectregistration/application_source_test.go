package projectregistration

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestSourceApplicationRegistersTypedSettingsAndActions(t *testing.T) {
	root := t.TempDir()
	writeMapperFixture(t, filepath.Join(root, ".loom/project.yaml"), `kind: loom.project
schema_version: project.contract.v0.6
project: {id: `+mapperProjectID+`, slug: source-worker, name: Source worker, owner_node: main}
resources:
  worker:
    kind: application
    application:
      source: {path: ., python_module: intake.worker, files: [intake/defaults.json]}
      config:
        schema: intake.worker.v1
        values: {poll_seconds: 30, large: 9007199254740993, enabled: false, label: private}
      data: {database: {path: .data}}
      network: outbound_https
      actions:
        status: {python_module: intake.worker, args: [status], read_only: true}
`)
	analysis := requireMapperAnalysis(t, root)
	if _, err := BuildInput(analysis, "source.test"); err != nil {
		t.Fatal(err)
	}
	if got := analysis.Plan.Declaration.Document.Resources["worker"].Application.Config.Values["large"]; got != json.Number("9007199254740993") {
		t.Fatalf("integer precision lost: %#v", got)
	}
	// A changed numeric value must remain a mismatch, not be ignored merely
	// because equivalent YAML/JSON numeric representations are accepted.
	analysis.Plan.Declaration.Document.Resources["worker"].Application.Config.Values["poll_seconds"] = 31
	if _, err := BuildInput(analysis, "source.test"); err == nil {
		t.Fatal("substituted setting accepted")
	}
}

func TestSourceApplicationRegistersNativeSelectorsWithoutLosingTokens(t *testing.T) {
	root := t.TempDir()
	writeMapperFixture(t, filepath.Join(root, "repos/server/Cargo.toml"), "[package]\nname = \"server\"\nversion = \"0.1.0\"\n")
	writeMapperFixture(t, filepath.Join(root, ".loom/project.yaml"), `kind: loom.project
schema_version: project.contract.v0.6
project: {id: `+mapperProjectID+`, slug: native-worker, name: Native worker, owner_node: main}
resources:
  worker:
    kind: application
    application:
      source: {path: repos/server, build: {kind: cargo, package: server, features: [http]}}
      run: {binary: server, args: [serve, --config, {config_file: true}]}
      config: {values: {large: 9007199254740993}}
      actions: {status: {args: [status, {config_file: true}], read_only: true}}
`)
	analysis := requireMapperAnalysis(t, root)
	input, err := BuildInput(analysis, "native.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(input.RegistrationPlan) == 0 {
		t.Fatal("registration lost declaration plan")
	}
	a := analysis.Plan.Declaration.Document.Resources["worker"].Application
	if a.Source.Build.Package != "server" || !a.Run.Args[2].ConfigFile || !a.Actions["status"].Args[1].ConfigFile || a.Config.Values["large"] != json.Number("9007199254740993") {
		t.Fatal("typed native declaration/config changed")
	}
	a.Run.Binary = "substituted"
	if _, err := BuildInput(analysis, "native.test"); err == nil {
		t.Fatal("substituted executable accepted")
	}
}
