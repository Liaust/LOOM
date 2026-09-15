package projects

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestProjectRepositoryAdvisoryLockKeysAreSortedAndUnique(t *testing.T) {
	got := sortedUniqueProjectRepositoryLockKeys([]string{"id:repo_b", " id:repo_a ", "id:repo_b", "", "id:repo_c"})
	want := []string{"id:repo_a", "id:repo_b", "id:repo_c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sorted lock keys = %#v, want %#v", got, want)
	}
}

func TestRepositoryRegistrationBuildsMembersOnlyFromMatchingValidatorOutput(t *testing.T) {
	input := validProjectRepositoryContractRegistrationInput()
	normalized, err := normalizeRegisterProjectContractInput(input)
	if err != nil {
		t.Fatalf("normalize valid repository source: %v", err)
	}
	source, err := buildProjectRepositoryValidatedSourceInput(normalized, normalized.Project.ID)
	if err != nil {
		t.Fatalf("build validated repository source: %v", err)
	}
	if len(source.Members) != 2 || source.Members[0].Key != "backend" || source.Members[1].Key != "portal" {
		t.Fatalf("validated members = %#v", source.Members)
	}

	partial := validProjectRepositoryContractRegistrationInput()
	partial.RegistrationPlan = json.RawMessage(strings.Replace(string(partial.RegistrationPlan), `,{"id":"repo_01ARZ3NDEKTSV4RRFFQ69G5FAW","key":"portal","path":"portal","role":"component"}`, "", 1))
	if _, err := normalizeRegisterProjectContractInput(partial); err == nil || !strings.Contains(err.Error(), "complete repository member set") {
		t.Fatalf("partial validator plan error = %v", err)
	}
}

func TestRepositorySourceExplicitEmptyV04RegistrationRemainsSourceBound(t *testing.T) {
	input := validProjectRepositoryContractRegistrationInput()
	for _, raw := range []*json.RawMessage{&input.ValidationReport, &input.RegistrationPlan} {
		var document map[string]any
		if err := json.Unmarshal(*raw, &document); err != nil {
			t.Fatal(err)
		}
		document["repos"] = []any{}
		document["repository_members"] = []any{}
		updated, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		*raw = updated
	}
	normalized, err := normalizeRegisterProjectContractInput(input)
	if err != nil {
		t.Fatalf("normalize explicit empty v0.4 source: %v", err)
	}
	validated, err := buildProjectRepositoryValidatedSourceInput(normalized, normalized.Project.ID)
	if err != nil {
		t.Fatalf("build explicit empty v0.4 source: %v", err)
	}
	if normalized.RepositorySource == nil || len(validated.Members) != 0 || validated.ReposContractDigest != input.RepositorySource.ContractHash {
		t.Fatalf("empty v0.4 source lost binding: normalized=%#v validated=%#v", normalized.RepositorySource, validated)
	}
}

func TestRepositoryRegistrationRequiresAcceptedValidatorEnvelopeAndSource(t *testing.T) {
	missingSource := validProjectRepositoryContractRegistrationInput()
	missingSource.RepositorySource = nil
	if _, err := normalizeRegisterProjectContractInput(missingSource); err == nil || !strings.Contains(err.Error(), "repository source metadata is required") {
		t.Fatalf("missing repository source error = %v", err)
	}

	unknownReport := validProjectRepositoryContractRegistrationInput()
	unknownReport.ValidationReport = json.RawMessage(strings.Replace(string(unknownReport.ValidationReport), projectRepositoryValidationReportSchemaV03, "project.validation_report.v9", 1))
	if _, err := normalizeRegisterProjectContractInput(unknownReport); err == nil || !strings.Contains(err.Error(), "accepted validator report") {
		t.Fatalf("unknown validation report schema error = %v", err)
	}

	unknownPlan := validProjectRepositoryContractRegistrationInput()
	unknownPlan.RegistrationPlan = json.RawMessage(strings.Replace(string(unknownPlan.RegistrationPlan), projectRepositoryRegistrationPlanSchemaV03, "project.plan.v9", 1))
	if _, err := normalizeRegisterProjectContractInput(unknownPlan); err == nil || !strings.Contains(err.Error(), "registration plan schemas") {
		t.Fatalf("unknown registration plan schema error = %v", err)
	}
}

func TestRepositorySourceEvidenceMustMatchReportPlanAndRegistrationInputBeforeSQL(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *RegisterProjectContractInput)
		want   string
	}{
		{name: "missing report snapshot", mutate: func(t *testing.T, input *RegisterProjectContractInput) {
			removeRepositorySourceSnapshot(t, &input.ValidationReport)
		}, want: "validator report repository source snapshot is required"},
		{name: "missing plan snapshot", mutate: func(t *testing.T, input *RegisterProjectContractInput) {
			removeRepositorySourceSnapshot(t, &input.RegistrationPlan)
		}, want: "registration plan repository source snapshot is required"},
		{name: "changed input path", mutate: func(_ *testing.T, input *RegisterProjectContractInput) {
			input.RepositorySource.ContractPath += ".changed"
		}, want: "contract path does not match"},
		{name: "changed input hash", mutate: func(_ *testing.T, input *RegisterProjectContractInput) {
			input.RepositorySource.ContractHash = "sha256:" + strings.Repeat("c", 64)
		}, want: "contract hash does not match"},
		{name: "changed input version", mutate: func(_ *testing.T, input *RegisterProjectContractInput) {
			input.RepositorySource.ContractSchemaVersion = ProjectRepositoryReposSchemaV03
		}, want: "schema version does not match"},
		{name: "report plan mismatch", mutate: func(t *testing.T, input *RegisterProjectContractInput) {
			mutateRepositorySourceSnapshot(t, &input.RegistrationPlan, "contract_path", input.RepositorySource.ContractPath+".changed")
		}, want: "registration plan repository source contract path does not match"},
		{name: "missing v0.4 project id", mutate: func(t *testing.T, input *RegisterProjectContractInput) {
			input.Project.ID = ""
			removeProjectID(t, &input.Contract)
			removeProjectID(t, &input.ValidationReport)
			removeProjectID(t, &input.RegistrationPlan)
		}, want: "project.contract.v0.4 repository source requires project.id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validProjectRepositoryContractRegistrationInput()
			test.mutate(t, &input)
			if _, err := normalizeRegisterProjectContractInput(input); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("normalization error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestRepositorySourceV03WithoutRepositoriesFacetRemainsCompatible(t *testing.T) {
	input := RegisterProjectContractInput{
		ProjectRoot:           "/tmp/legacy-registration",
		ContractPath:          "/tmp/legacy-registration/.loom/project.yaml",
		ContractHash:          "sha256:" + strings.Repeat("a", 64),
		ContractSchemaVersion: ProjectRepositoryProjectSchemaV03,
		Contract:              json.RawMessage(`{"kind":"loom.project","schema_version":"project.contract.v0.3","project":{"slug":"legacy-registration","name":"Legacy Registration","owner_node":"main","status":"active"}}`),
		ValidationReport:      json.RawMessage(`{"schema_version":"project.validation_report.v0.3","project_root":"/tmp/legacy-registration","contract_path":"/tmp/legacy-registration/.loom/project.yaml","ok":true,"registerable":true,"project":{"slug":"legacy-registration","name":"Legacy Registration","owner_node":"main","status":"active"}}`),
		RegistrationPlan:      json.RawMessage(`{"schema_version":"project.plan.v0.3","project_root":"/tmp/legacy-registration","contract_path":"/tmp/legacy-registration/.loom/project.yaml","registerable":true,"project":{"slug":"legacy-registration","name":"Legacy Registration","owner_node":"main","status":"active"}}`),
		Project:               ProjectContractProjectInput{Slug: "legacy-registration", Name: "Legacy Registration", OwnerNode: "main", Status: "active"},
		DerivedProviders:      json.RawMessage(`[]`),
		Facets:                []ProjectContractFacetInput{},
		PolicyRefs:            json.RawMessage(`[]`),
		Metadata:              json.RawMessage(`{}`),
	}
	normalized, err := normalizeRegisterProjectContractInput(input)
	if err != nil {
		t.Fatalf("normalize legacy registration input: %v", err)
	}
	if normalized.Project.ID != "" || normalized.RepositorySource != nil {
		t.Fatalf("legacy registration identity/source = %q/%#v, want empty/nil", normalized.Project.ID, normalized.RepositorySource)
	}
}

func TestRepositoryRegistrationRejectsUnknownVersionsAndArchivedMutationBeforeSQL(t *testing.T) {
	unknown := validProjectRepositoryContractRegistrationInput()
	unknown.RepositorySource.ContractSchemaVersion = "repos.contract.v9"
	mutateRepositorySourceSnapshot(t, &unknown.ValidationReport, "contract_schema_version", "repos.contract.v9")
	mutateRepositorySourceSnapshot(t, &unknown.RegistrationPlan, "contract_schema_version", "repos.contract.v9")
	if _, err := normalizeRegisterProjectContractInput(unknown); err == nil || !strings.Contains(err.Error(), "unsupported source versions") {
		t.Fatalf("unknown source version error = %v", err)
	}

	archived := validProjectRepositoryContractRegistrationInput()
	archived.Project.Status = "archived"
	var contract map[string]any
	if err := json.Unmarshal(archived.Contract, &contract); err != nil {
		t.Fatal(err)
	}
	contract["project"].(map[string]any)["status"] = "archived"
	archived.Contract, _ = json.Marshal(contract)
	for _, target := range []*json.RawMessage{&archived.ValidationReport, &archived.RegistrationPlan} {
		var document map[string]any
		if err := json.Unmarshal(*target, &document); err != nil {
			t.Fatal(err)
		}
		document["project"].(map[string]any)["status"] = "archived"
		*target, _ = json.Marshal(document)
	}
	if _, err := normalizeRegisterProjectContractInput(archived); err == nil || !strings.Contains(err.Error(), "archived project source") {
		t.Fatalf("archived source error = %v", err)
	}
}

func mutateRepositorySourceSnapshot(t *testing.T, raw *json.RawMessage, field string, value any) {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(*raw, &document); err != nil {
		t.Fatal(err)
	}
	source, ok := document["repository_source"].(map[string]any)
	if !ok {
		t.Fatalf("repository source snapshot missing from %s", string(*raw))
	}
	source[field] = value
	updated, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	*raw = updated
}

func removeRepositorySourceSnapshot(t *testing.T, raw *json.RawMessage) {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(*raw, &document); err != nil {
		t.Fatal(err)
	}
	delete(document, "repository_source")
	updated, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	*raw = updated
}

func removeProjectID(t *testing.T, raw *json.RawMessage) {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(*raw, &document); err != nil {
		t.Fatal(err)
	}
	project, ok := document["project"].(map[string]any)
	if !ok {
		t.Fatalf("project missing from %s", string(*raw))
	}
	delete(project, "id")
	updated, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	*raw = updated
}

func validProjectRepositoryContractRegistrationInput() RegisterProjectContractInput {
	const (
		projectID     = "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"
		projectRoot   = "/tmp/repository-transaction"
		projectPath   = "/tmp/repository-transaction/.loom/project.yaml"
		reposPath     = "/tmp/repository-transaction/repos/repos.yaml"
		projectDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		reposDigest   = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	project := `{"id":"` + projectID + `","slug":"repository-transaction","name":"Repository Transaction","owner_node":"main","status":"active"}`
	members := `[{"id":"repo_01ARZ3NDEKTSV4RRFFQ69G5FAV","key":"backend","path":"backend","role":"primary","state_root":".repo"},{"id":"repo_01ARZ3NDEKTSV4RRFFQ69G5FAW","key":"portal","path":"portal","role":"component"}]`
	repos := `[{"contract_path":"` + reposPath + `","contract_hash":"` + reposDigest + `"}]`
	repositorySource := `{"contract_path":"` + reposPath + `","contract_hash":"` + reposDigest + `","contract_schema_version":"repos.contract.v0.4"}`
	return RegisterProjectContractInput{
		ProjectRoot:           projectRoot,
		ContractPath:          projectPath,
		ContractHash:          projectDigest,
		ContractSchemaVersion: ProjectRepositoryProjectSchemaV04,
		Contract:              json.RawMessage(`{"kind":"loom.project","schema_version":"project.contract.v0.4","project":` + project + `}`),
		ValidationReport:      json.RawMessage(`{"schema_version":"project.validation_report.v0.3","project_root":"` + projectRoot + `","contract_path":"` + projectPath + `","ok":true,"registerable":true,"project":` + project + `,"repos":` + repos + `,"repository_members":` + members + `,"repository_source":` + repositorySource + `}`),
		RegistrationPlan:      json.RawMessage(`{"schema_version":"project.plan.v0.3","generated_at":"2026-08-29T12:00:00Z","project_root":"` + projectRoot + `","contract_path":"` + projectPath + `","registerable":true,"project":` + project + `,"repos":` + repos + `,"repository_members":` + members + `,"repository_source":` + repositorySource + `}`),
		Project: ProjectContractProjectInput{
			ID: projectID, Slug: "repository-transaction", Name: "Repository Transaction", OwnerNode: "main", Status: "active",
		},
		RepositorySource: &RegisterProjectRepositorySourceInput{ContractPath: reposPath, ContractHash: reposDigest, ContractSchemaVersion: ProjectRepositoryReposSchemaV04},
		DerivedProviders: json.RawMessage(`[]`),
		Facets:           []ProjectContractFacetInput{{Key: "repos", Folder: "repos", Enabled: true, Present: true}},
		PolicyRefs:       json.RawMessage(`[]`),
		Metadata:         json.RawMessage(`{}`),
	}
}
