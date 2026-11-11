package projects

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
)

func declarationReaderInput(t *testing.T, version string, files json.RawMessage) RegisterProjectContractInput {
	t.Helper()
	project := ProjectContractProjectInput{ID: "project_01ARZ3NDEKTSV4RRFFQ69G5FAV", Slug: "reader-test", Name: "Reader test", OwnerNode: "main", Status: "active"}
	repository := projectRepositoryValidatorMember{RepositoryID: "repo_01ARZ3NDEKTSV4RRFFQ69G5FAA", Key: "code", Path: "code", Role: "primary"}
	document := map[string]any{
		"kind": "loom.project", "schema_version": version, "project": project,
		"resources": map[string]any{"code": map[string]any{"kind": "repository", "repository": map[string]string{"id": repository.RepositoryID, "path": repository.Path, "role": repository.Role}}},
	}
	if files != nil {
		document["files"] = files
	}
	marshal := func(v any) json.RawMessage {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	raw := marshal(document)
	hash := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	root := filepath.Join(t.TempDir(), "project")
	contractPath := filepath.Join(root, ".loom", "project.yaml")
	compilation := map[string]any{
		"document": raw, "repositories": []projectRepositoryValidatorMember{repository}, "errors": []any{},
		"sources": []any{map[string]any{"ref": ".loom/project.yaml", "schema_version": version, "hash": hash, "revision": hash, "raw": []byte(raw)}},
	}
	evidence := map[string]any{"schema_version": projectRepositoryValidationReportSchemaV03, "ok": true, "registerable": true, "project_root": root, "contract_path": contractPath, "project": project, "declaration": compilation}
	report := marshal(evidence)
	evidence["schema_version"] = projectRepositoryRegistrationPlanSchemaV03
	return RegisterProjectContractInput{
		ProjectRoot: root, ContractPath: contractPath, ContractHash: hash, ContractSchemaVersion: version, Contract: raw,
		Project: project, ValidationReport: report, RegistrationPlan: marshal(evidence), Metadata: json.RawMessage(`{}`),
		DerivedProviders: json.RawMessage(`[]`), PolicyRefs: json.RawMessage(`[]`),
	}
}

func TestDeclarationReaderV06PreservesFilesAndSourceIdentity(t *testing.T) {
	files := json.RawMessage(`{"rules":[{"path":"notes","search":"text","protect":false,"enrichment":[]}]}`)
	input := declarationReaderInput(t, ProjectRepositoryProjectSchemaV06, files)
	input, err := normalizeRegisterProjectContractInput(input)
	if err != nil {
		t.Fatal(err)
	}
	var document declarationDocument
	if !declarationDecode(input.Contract, &document) || document.Files == nil || !declarationEqual(document.Files, files) {
		t.Fatalf("files dropped or changed: %+v", document.Files)
	}
	source, err := buildDeclarationRepositorySource(input, input.Project.ID)
	if err != nil || source.Versions != projectRepositorySourceV06V06 {
		t.Fatalf("source version: %+v %v", source.Versions, err)
	}
	canonical, err := BuildProjectRepositoryCanonicalSource(source)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeProjectRepositoryCanonicalSource(canonical.SnapshotJSON, canonical.SemanticDigest, canonical.LocationDigest)
	if err != nil || decoded.Snapshot.SourceVersions != projectRepositorySourceV06V06 || !declarationEqual(decoded, canonical) {
		t.Fatalf("v0.6 source round trip: %+v %v", decoded.Snapshot.SourceVersions, err)
	}
	for _, mutate := range []string{"path", "digest", "mixed_version", "root_repository"} {
		t.Run(mutate, func(t *testing.T) {
			changed := source
			changed.Members = append([]ProjectRepositoryValidatedMember(nil), source.Members...)
			switch mutate {
			case "path":
				changed.ReposContractPath = filepath.Join(changed.ProjectRoot, "repos.yaml")
			case "digest":
				changed.ReposContractDigest = fmt.Sprintf("sha256:%064x", 1)
			case "mixed_version":
				changed.Versions.ReposContract = ProjectRepositoryProjectSchemaV05
			case "root_repository":
				changed.Members[0].Path = "."
			}
			if _, err := BuildProjectRepositoryCanonicalSource(changed); err == nil {
				t.Fatal("noncanonical declaration source accepted")
			}
		})
	}
}

func TestDeclarationReaderFilesStrictVersionsAndShape(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		version string
		files   string
		valid   bool
	}{
		{"old_omitted", ProjectRepositoryProjectSchemaV05, "", true},
		{"new_omitted", ProjectRepositoryProjectSchemaV06, "", true},
		{"new_empty", ProjectRepositoryProjectSchemaV06, `{"rules":[]}`, true},
		{"old_empty", ProjectRepositoryProjectSchemaV05, `{"rules":[]}`, false},
		{"old_null", ProjectRepositoryProjectSchemaV05, `null`, false},
		{"new_null", ProjectRepositoryProjectSchemaV06, `null`, false},
		{"missing_rules", ProjectRepositoryProjectSchemaV06, `{}`, false},
		{"null_rules", ProjectRepositoryProjectSchemaV06, `{"rules":null}`, false},
		{"unknown_policy_field", ProjectRepositoryProjectSchemaV06, `{"rules":[],"metadata":{}}`, false},
		{"unknown_rule_field", ProjectRepositoryProjectSchemaV06, `{"rules":[{"path":"notes","metadata":true}]}`, false},
		{"bad_search", ProjectRepositoryProjectSchemaV06, `{"rules":[{"path":"notes","search":"all"}]}`, false},
		{"bad_protect", ProjectRepositoryProjectSchemaV06, `{"rules":[{"path":"notes","protect":"true"}]}`, false},
		{"null_search", ProjectRepositoryProjectSchemaV06, `{"rules":[{"path":"notes","search":null}]}`, false},
		{"null_protect", ProjectRepositoryProjectSchemaV06, `{"rules":[{"path":"notes","protect":null}]}`, false},
		{"duplicate_rules", ProjectRepositoryProjectSchemaV06, `{"rules":[],"rules":[]}`, false},
		{"null_enrichment", ProjectRepositoryProjectSchemaV06, `{"rules":[{"path":"notes","enrichment":null}]}`, false},
		{"escape_path", ProjectRepositoryProjectSchemaV06, `{"rules":[{"path":"../notes","enrichment":[]}]}`, false},
		{"unknown_version", "project.contract.v0.7", "", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var files json.RawMessage
			if scenario.files != "" {
				files = json.RawMessage(scenario.files)
			}
			input := declarationReaderInput(t, scenario.version, files)
			if scenario.name == "unknown_version" {
				input.RepositorySource = &RegisterProjectRepositorySourceInput{ContractPath: input.ContractPath, ContractHash: input.ContractHash, ContractSchemaVersion: scenario.version}
			}
			_, err := normalizeRegisterProjectContractInput(input)
			if (err == nil) != scenario.valid {
				t.Fatalf("valid=%t err=%v", scenario.valid, err)
			}
		})
	}
}

func TestDeclarationReaderRejectsMixedEvidenceVersions(t *testing.T) {
	for _, scenario := range []string{"input_version", "repository_version", "root_snapshot"} {
		t.Run(scenario, func(t *testing.T) {
			input := declarationReaderInput(t, ProjectRepositoryProjectSchemaV06, nil)
			switch scenario {
			case "input_version":
				input.ContractSchemaVersion = ProjectRepositoryProjectSchemaV05
			case "repository_version":
				input.RepositorySource = &RegisterProjectRepositorySourceInput{ContractPath: input.ContractPath, ContractHash: input.ContractHash, ContractSchemaVersion: ProjectRepositoryProjectSchemaV05}
			case "root_snapshot":
				for _, raw := range []*json.RawMessage{&input.ValidationReport, &input.RegistrationPlan} {
					var evidence map[string]any
					if err := json.Unmarshal(*raw, &evidence); err != nil {
						t.Fatal(err)
					}
					evidence["declaration"].(map[string]any)["sources"].([]any)[0].(map[string]any)["schema_version"] = ProjectRepositoryProjectSchemaV05
					*raw, _ = json.Marshal(evidence)
				}
			}
			if _, err := normalizeRegisterProjectContractInput(input); err == nil {
				t.Fatal("v0.6 evidence accepted with v0.5 identity")
			}
		})
	}
}
