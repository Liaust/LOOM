package projectregistration

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/projectcontracts"
)

const mapperProjectID = "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"

func TestManagedApplicationDescriptorRegistrationSource(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		wantErr string
	}{
		{name: "valid"},
		{name: "wrong_schema", wantErr: "schema mismatch"},
		{name: "missing", wantErr: "missing declaration source snapshot"},
		{name: "tampered", wantErr: "snapshot hash mismatch"},
		{name: "unexpected", wantErr: "unexpected declaration source"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			root := t.TempDir()
			writeMapperFixture(t, filepath.Join(root, "repos/server/README.md"), "Server source")
			writeMapperFixture(t, filepath.Join(root, ".loom/project.yaml"), "kind: loom.project\nschema_version: project.contract.v0.5\nproject: {id: "+mapperProjectID+", slug: managed-app, name: Managed app, owner_node: main}\nresources:\n  code: {kind: repository, repository: {path: repos/server, role: primary}}\n  app:\n    kind: application\n    application: {repository: code, manifest: .loom/applications/app.json, artifact_descriptor: .loom/applications/artifact.json, endpoint: {exposure: loopback}}\n")
			writeMapperFixture(t, filepath.Join(root, ".loom/applications/app.json"), `{"kind":"loom.application","schema_version":"application.contract.v1"}`)
			const descriptor = `{"schema_version":"application.artifact.v1"}`
			descriptorPath := filepath.Join(root, ".loom/applications/artifact.json")
			writeMapperFixture(t, descriptorPath, descriptor)
			analysis := requireMapperAnalysis(t, root)
			index := -1
			for i, source := range analysis.Plan.Declaration.Sources {
				if source.Ref == ".loom/applications/artifact.json" {
					index = i
				}
			}
			if index < 0 {
				t.Fatal("descriptor source missing")
			}
			switch scenario.name {
			case "wrong_schema":
				analysis.Plan.Declaration.Sources[index].SchemaVersion = "application.artifact.v9"
			case "missing":
				analysis.Plan.Declaration.Sources = append(analysis.Plan.Declaration.Sources[:index], analysis.Plan.Declaration.Sources[index+1:]...)
			case "tampered":
				analysis.Plan.Declaration.Sources[index].Raw = []byte("changed bytes")
			case "unexpected":
				analysis.Plan.Declaration.Sources[index].Ref = ".loom/applications/unrequested.json"
			}
			// Align both snapshots to exercise the source checks, not report/plan parity.
			analysis.Report.Declaration.Sources = analysis.Plan.Declaration.Sources
			writeMapperFixture(t, descriptorPath, "changed after analysis")
			input, err := BuildInput(analysis, "managed-application.test")
			if scenario.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), scenario.wantErr) {
					t.Fatalf("expected %q, got %v", scenario.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var plan projectcontracts.ProjectPlan
			if err := json.Unmarshal(input.RegistrationPlan, &plan); err != nil {
				t.Fatal(err)
			}
			if plan.Declaration == nil || len(plan.Declaration.Sources) <= index || !bytes.Equal(plan.Declaration.Sources[index].Raw, []byte(descriptor)) {
				t.Fatal("captured descriptor bytes lost from registration plan")
			}
		})
	}
}

func TestRegistrationInputMapsV03AndV04RepositorySourceEvidence(t *testing.T) {
	tests := []struct {
		name        string
		analysis    func(*testing.T) projectcontracts.Analysis
		wantID      string
		wantSource  bool
		wantMembers int
	}{
		{name: "v0.3 without repositories", analysis: legacyAnalysisWithoutRepositories, wantMembers: 0},
		{name: "v0.4 empty repositories", analysis: func(t *testing.T) projectcontracts.Analysis { return v04Analysis(t, false) }, wantID: mapperProjectID, wantSource: true, wantMembers: 0},
		{name: "v0.4 populated repositories", analysis: func(t *testing.T) projectcontracts.Analysis { return v04Analysis(t, true) }, wantID: mapperProjectID, wantSource: true, wantMembers: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			analysis := test.analysis(t)
			input, err := BuildInput(analysis, "mapper.test")
			if err != nil {
				t.Fatalf("BuildInput returned error: %v", err)
			}
			if input.Project.ID != test.wantID {
				t.Fatalf("project ID = %q, want %q", input.Project.ID, test.wantID)
			}
			if (input.RepositorySource != nil) != test.wantSource {
				t.Fatalf("repository source = %#v, want present %t", input.RepositorySource, test.wantSource)
			}
			if test.wantSource && (analysis.Report.RepositorySource == nil ||
				input.RepositorySource.ContractPath != analysis.Report.RepositorySource.ContractPath ||
				input.RepositorySource.ContractHash != analysis.Report.RepositorySource.ContractHash ||
				input.RepositorySource.ContractSchemaVersion != analysis.Report.RepositorySource.ContractSchemaVersion) {
				t.Fatalf("mapped source %#v does not equal validated source %#v", input.RepositorySource, analysis.Report.RepositorySource)
			}
			if len(analysis.Plan.RepositoryMembers) != test.wantMembers {
				t.Fatalf("analysis members = %d, want %d", len(analysis.Plan.RepositoryMembers), test.wantMembers)
			}
		})
	}
}

func TestRegistrationInputConsumesRepositorySourceEvidenceWithoutReopeningContract(t *testing.T) {
	analysis := v04Analysis(t, true)
	want := *analysis.Report.RepositorySource
	if err := os.WriteFile(filepath.FromSlash(want.ContractPath), []byte("kind: changed-after-analysis\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	input, err := BuildInput(analysis, "mapper.test")
	if err != nil {
		t.Fatalf("BuildInput returned error after source mutation: %v", err)
	}
	if input.RepositorySource == nil || input.RepositorySource.ContractHash != want.ContractHash || input.RepositorySource.ContractSchemaVersion != want.ContractSchemaVersion {
		t.Fatalf("mapper reopened or replaced validated evidence: got %#v want %#v", input.RepositorySource, want)
	}
}

func TestRegistrationInputRejectsIncompleteMismatchedAndUnsupportedRepositorySourceEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*projectcontracts.Analysis)
		want   string
	}{
		{name: "missing plan snapshot", mutate: func(a *projectcontracts.Analysis) { a.Plan.RepositorySource = nil }, want: "snapshots do not match"},
		{name: "missing declared source", mutate: func(a *projectcontracts.Analysis) { a.Report.RepositorySource = nil; a.Plan.RepositorySource = nil }, want: "source metadata is required"},
		{name: "changed path", mutate: func(a *projectcontracts.Analysis) { a.Plan.RepositorySource.ContractPath += ".changed" }, want: "snapshots do not match"},
		{name: "changed hash", mutate: func(a *projectcontracts.Analysis) {
			a.Plan.RepositorySource.ContractHash = "sha256:" + strings.Repeat("c", 64)
		}, want: "snapshots do not match"},
		{name: "changed version", mutate: func(a *projectcontracts.Analysis) {
			a.Plan.RepositorySource.ContractSchemaVersion = projectcontracts.ReposSchemaV03
		}, want: "snapshots do not match"},
		{name: "unsupported version", mutate: func(a *projectcontracts.Analysis) {
			a.Report.RepositorySource.ContractSchemaVersion = "repos.contract.v9"
			a.Plan.RepositorySource.ContractSchemaVersion = "repos.contract.v9"
		}, want: "unsupported"},
		{name: "missing v0.4 project id", mutate: func(a *projectcontracts.Analysis) {
			a.Loaded.Contract.Project.ID = ""
			a.Report.Project.ID = ""
			a.Plan.Project.ID = ""
		}, want: "requires project.id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			analysis := v04Analysis(t, false)
			test.mutate(&analysis)
			if _, err := BuildInput(analysis, "mapper.test"); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("BuildInput error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func legacyAnalysisWithoutRepositories(t *testing.T) projectcontracts.Analysis {
	t.Helper()
	root := t.TempDir()
	writeMapperFixture(t, filepath.Join(root, projectcontracts.CanonicalRootContractPath), `kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: legacy-registration
  name: Legacy Registration
  owner_node: main
`)
	return requireMapperAnalysis(t, root)
}

func v04Analysis(t *testing.T, populated bool) projectcontracts.Analysis {
	t.Helper()
	root := t.TempDir()
	writeMapperFixture(t, filepath.Join(root, projectcontracts.CanonicalRootContractPath), `kind: loom.project
schema_version: project.contract.v0.4
project:
  id: `+mapperProjectID+`
  slug: mapped-registration
  name: Mapped Registration
  owner_node: main
facets:
  repos: true
`)
	if err := os.MkdirAll(filepath.Join(root, "repos"), 0o755); err != nil {
		t.Fatal(err)
	}
	members := "  members: []\n"
	if populated {
		members = `  members:
    - id: repo_01ARZ3NDEKTSV4RRFFQ69G5FAV
      key: backend
      path: backend
      role: primary
`
	}
	writeMapperFixture(t, filepath.Join(root, ".loom", "contracts", "repos.yaml"), `kind: loom.repos
schema_version: repos.contract.v0.4
repos:
  status: active
  watch_roots: []
`+members)
	return requireMapperAnalysis(t, root)
}

func requireMapperAnalysis(t *testing.T, root string) projectcontracts.Analysis {
	t.Helper()
	analysis := projectcontracts.Analyze(root)
	if !analysis.Report.OK || !analysis.Plan.Registerable {
		t.Fatalf("analysis failed: %#v", analysis.Report.Diagnostics)
	}
	return analysis
}

func writeMapperFixture(t *testing.T, path, raw string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDeclarationRegistrationPreservesFullSourceWithoutReopening(t *testing.T) {
	root := t.TempDir()
	raw := []byte("kind: loom.project\nschema_version: project.contract.v0.5\nproject:\n  id: " + mapperProjectID + "\n  slug: source-project\n  name: Source project\n  owner_node: main\nresources:\n  code:\n    kind: repository\n    repository: {path: custom-code, role: primary}\n")
	if err := os.MkdirAll(filepath.Join(root, ".loom"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "custom-code"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".loom/project.yaml")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	a := projectcontracts.Analyze(root)
	if !a.Report.OK {
		t.Fatalf("analyze: %+v", a.Report.Diagnostics)
	}
	if err := os.WriteFile(path, []byte("changed after analysis"), 0600); err != nil {
		t.Fatal(err)
	}
	input, err := BuildInput(a, "declaration.test")
	if err != nil {
		t.Fatal(err)
	}
	var d projectcontracts.ProjectDeclaration
	if err := json.Unmarshal(input.Contract, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Resources) != 1 || d.Resources["code"].Repository.Path != "custom-code" || d.Project.ID != mapperProjectID || input.ContractSchemaVersion != projectcontracts.ProjectSchemaV05 || input.RepositorySource != nil {
		t.Fatalf("lost or remapped source: %+v", input)
	}
	if !bytes.Contains(input.RegistrationPlan, []byte("would_register_repository")) {
		t.Fatal("repository intent omitted")
	}
	a.Plan.Declaration.Sources[0].Hash = "sha256:wrong"
	if _, err := BuildInput(a, "declaration.test"); err == nil {
		t.Fatal("modified evidence accepted")
	}
}

func TestDeclarationRegistrationRejectsMismatchedDisplayIdentity(t *testing.T) {
	root := t.TempDir()
	result, err := projectcontracts.ScaffoldProject(projectcontracts.ScaffoldOptions{Name: "Identity test", Slug: "identity-test", OwnerNode: "main", Directory: root, Mode: projectcontracts.ScaffoldModeDeclaration})
	if err != nil {
		t.Fatal(err)
	}
	a := projectcontracts.Analyze(result.ProjectRoot)
	a.Report.Project.Name = "Other name"
	a.Plan.Project.Name = "Other name"
	if _, err := BuildInput(a, "declaration.test"); err == nil {
		t.Fatal("declaration identity differs from registered name")
	}
}

func TestDeclarationRegistrationApplicationSourceSchema(t *testing.T) {
	root := t.TempDir()
	writeMapperFixture(t, filepath.Join(root, "repos/server/README.md"), "Server source")
	writeMapperFixture(t, filepath.Join(root, ".loom/project.yaml"), `kind: loom.project
schema_version: project.contract.v0.5
project: {id: `+mapperProjectID+`, slug: application-project, name: Application project, owner_node: main}
resources:
  code: {kind: repository, repository: {path: repos/server, role: primary}}
  webdav:
    kind: application
    application: {repository: code, manifest: .loom/applications/webdav.json, endpoint: {exposure: loopback}}
`)
	manifestPath := filepath.Join(root, ".loom/applications/webdav.json")
	manifest := `{"kind":"loom.application","schema_version":"application.contract.v1"}`
	writeMapperFixture(t, manifestPath, manifest)
	analysis := requireMapperAnalysis(t, root)
	index := -1
	for i, source := range analysis.Plan.Declaration.Sources {
		if source.Ref == ".loom/applications/webdav.json" {
			index = i
			if source.SchemaVersion != "application.contract.v1" || string(source.Raw) != manifest {
				t.Fatalf("incorrect captured application source: %+v", source)
			}
		}
	}
	if index < 0 {
		t.Fatal("application source missing")
	}
	// Registration consumes the validated snapshot, not a second filesystem read.
	writeMapperFixture(t, manifestPath, "changed after analysis")
	input, err := BuildInput(analysis, "application.test")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(input.RegistrationPlan, []byte("application.contract.v1")) {
		t.Fatal("application schema lost from registration plan")
	}
	for _, version := range []string{"", "application.contract.v9"} {
		analysis.Plan.Declaration.Sources[index].SchemaVersion = version
		analysis.Report.Declaration.Sources[index].SchemaVersion = version
		if _, err := BuildInput(analysis, "application.test"); err == nil || !strings.Contains(err.Error(), "schema mismatch") {
			t.Fatalf("schema %q: %v", version, err)
		}
	}
}

func TestDeclarationEnrollmentMappingRejectsRootSubstitution(t *testing.T) {
	root := t.TempDir()
	for _, folder := range []string{".loom", "journal"} {
		if err := os.MkdirAll(filepath.Join(root, folder), 0700); err != nil {
			t.Fatal(err)
		}
	}
	raw := []byte("kind: loom.project\nschema_version: project.contract.v0.5\nproject:\n  id: " + mapperProjectID + "\n  slug: source-project\n  name: Source project\n  owner_node: main\nresources:\n  reading:\n    kind: knowledge\n    knowledge: {path: journal, category: notes}\n")
	if err := os.WriteFile(filepath.Join(root, ".loom/project.yaml"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	a := projectcontracts.Analyze(root)
	if !a.Report.OK {
		t.Fatal(a.Report.Diagnostics)
	}
	if err := os.WriteFile(filepath.Join(root, ".loom/project.yaml"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildInput(a, "enrollment.test"); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"metadata", "path", "config", "missing", "owner"} {
		t.Run(field, func(t *testing.T) {
			copyRaw, _ := json.Marshal(a)
			var changed projectcontracts.Analysis
			_ = json.Unmarshal(copyRaw, &changed)
			changed.Loaded.Raw = append([]byte{}, a.Loaded.Raw...)
			if _, err := BuildInput(changed, "enrollment.test"); err != nil {
				t.Fatal(err)
			}
			// Loaded.Raw has its own source bytes and is deliberately carried verbatim.
			for _, roots := range [][]projectcontracts.ProjectWatchedRootItem{changed.Report.WatchedRoots, changed.Plan.WatchedRoots} {
				switch field {
				case "metadata":
					roots[0].Metadata["knowledge_source"].(map[string]any)["root_relative_path"] = "other"
				case "path":
					roots[0].RootRelativePath = "other"
				case "config":
					roots[0].ConfigHash = "sha256:" + strings.Repeat("0", 64)
				case "owner":
					roots[0].OwnerNode = "other"
				}
			}
			if field == "missing" {
				changed.Report.WatchedRoots = nil
				changed.Plan.WatchedRoots = nil
			}
			if _, err := BuildInput(changed, "enrollment.test"); err == nil {
				t.Fatal("forged report and plan accepted")
			}
		})
	}
}

func legacyMapperAnalysis(t *testing.T) projectcontracts.Analysis {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".loom/contracts"), 0700); err != nil {
		t.Fatal(err)
	}
	old := `{"kind":"loom.project","schema_version":"project.contract.v0.4","project":{"id":"project_01ARZ3NDEKTSV4RRFFQ69G5FAV","slug":"legacy-mapper","name":"Legacy mapper","owner_node":"main","status":"draft"},"facets":{}}`
	if err := os.WriteFile(filepath.Join(root, ".loom/contracts/retained.yaml"), []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	raw := `{"kind":"loom.project","schema_version":"project.contract.v0.5","project":{"id":"project_01ARZ3NDEKTSV4RRFFQ69G5FAV","slug":"legacy-mapper","name":"Legacy mapper","owner_node":"main","status":"draft"},"resources":{},"legacy_contracts":{"project":{"ref":".loom/contracts/retained.yaml","schema_version":"project.contract.v0.4","digest":"sha256:` + fmt.Sprintf("%x", sha256.Sum256([]byte(old))) + `"}}}`
	if err := os.WriteFile(filepath.Join(root, projectcontracts.CanonicalRootContractPath), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	analysis := projectcontracts.Analyze(root)
	if !analysis.Report.OK {
		t.Fatalf("valid pure reference source failed: %+v", analysis.Report.Diagnostics)
	}
	return analysis
}

func TestDeclarationLegacyReferenceMapperGuard(t *testing.T) {
	analysis := legacyMapperAnalysis(t)
	for _, forged := range []bool{false, true} {
		if forged {
			analysis.Report.Registerable = true
			analysis.Plan.Registerable = true
			analysis.Loaded.Declaration = nil
		}
		_, err := BuildInput(analysis, "legacy-reference-test")
		if err == nil || !strings.Contains(err.Error(), "legacy_owner_transition_required") {
			t.Fatalf("mapper guard forged=%t: %v", forged, err)
		}
	}
}

func TestDeclarationLegacyOwnerIntentMapping(t *testing.T) {
	a := legacyMapperAnalysis(t)
	before, _ := json.Marshal(a)
	// Mapping must use the captured sources even when local bytes disappear.
	if err := os.Remove(a.Loaded.ContractPath); err != nil {
		t.Fatal(err)
	}
	intent, err := BuildDeclarationIntent(a, "owner-intent-test")
	input := intent.Input
	if err != nil || input.Project.ID != mapperProjectID || input.ContractHash != fmt.Sprintf("sha256:%x", sha256.Sum256(a.Loaded.Raw)) || !bytes.Contains(input.Contract, []byte("legacy_contracts")) {
		t.Fatalf("pure intent: %+v %v", input.Project, err)
	}
	after, _ := json.Marshal(a)
	if !bytes.Equal(before, after) || !bytes.Contains(input.ValidationReport, []byte(`"registerable":false`)) || !bytes.Contains(input.RegistrationPlan, []byte(`"registerable":false`)) {
		t.Fatal("intent mapping changed truthful evidence")
	}
	if _, err = BuildInput(a, "direct"); err == nil {
		t.Fatal("pure intent authorized direct registration")
	}
	for name, change := range map[string]func(*projectcontracts.Analysis){
		"report_flag":         func(a *projectcontracts.Analysis) { a.Report.Registerable = true },
		"plan_flag":           func(a *projectcontracts.Analysis) { a.Plan.Registerable = true },
		"invalid":             func(a *projectcontracts.Analysis) { a.Report.OK = false },
		"missing_loaded":      func(a *projectcontracts.Analysis) { a.Loaded = nil },
		"missing_declaration": func(a *projectcontracts.Analysis) { a.Loaded.Declaration = nil },
		"raw":                 func(a *projectcontracts.Analysis) { a.Loaded.Raw = []byte(`{}`) },
		"identity":            func(a *projectcontracts.Analysis) { a.Plan.Project.ID = "project_other" },
		"source":              func(a *projectcontracts.Analysis) { a.Plan.Declaration.Sources[0].Raw = []byte("tampered") },
		"closure":             func(a *projectcontracts.Analysis) { a.Plan.Declaration.Sources = a.Plan.Declaration.Sources[:1] },
		"compiler":            func(a *projectcontracts.Analysis) { a.Plan.Declaration.LegacyContracts = nil },
	} {
		t.Run(name, func(t *testing.T) {
			a := legacyMapperAnalysis(t)
			change(&a)
			if _, err := BuildDeclarationIntent(a, "hostile"); err == nil {
				t.Fatal("tampered intent accepted")
			}
		})
	}
}
