package projects_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectregistration"
	"loom.local/loom/internal/projects"
)

const (
	testProjectID = "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	testRepoIDA   = "repo_01ARZ3NDEKTSV4RRFFQ69G5FAA"
	testRepoIDB   = "repo_01ARZ3NDEKTSV4RRFFQ69G5FAB"
	testRepoIDC   = "repo_01ARZ3NDEKTSV4RRFFQ69G5FAC"
)

func TestBuildProjectRepositoryCanonicalSourceIsDeterministic(t *testing.T) {
	input := testProjectRepositorySourceInput()
	first, err := projects.BuildProjectRepositoryCanonicalSource(input)
	if err != nil {
		t.Fatal(err)
	}

	secondInput := input
	secondInput.Members = []projects.ProjectRepositoryValidatedMember{input.Members[1], input.Members[0]}
	secondInput.RegistrationPlan = json.RawMessage(`{
		"z_options":{"z":2,"absolute_path":"/srv/loom/projects/example/scripts/job","a":1},
		"repository_members":[{"role":"primary","path":"backend","key":"backend","id":"repo_01ARZ3NDEKTSV4RRFFQ69G5FAA"},{"role":"component","path":"portal","key":"portal","id":"repo_01ARZ3NDEKTSV4RRFFQ69G5FAB"}],
		"project":{"status":"active","owner_node":"main","name":"Example","slug":"example","id":"project_01ARZ3NDEKTSV4RRFFQ69G5FAV"},
		"contract_path":"/srv/loom/projects/example/.loom/project.yaml",
		"project_root":"/srv/loom/projects/example",
		"generated_at":"2036-01-01T00:00:00Z",
		"schema_version":"project.plan.v0.3"
	}`)
	second, err := projects.BuildProjectRepositoryCanonicalSource(secondInput)
	if err != nil {
		t.Fatal(err)
	}

	if first.SemanticDigest != second.SemanticDigest || first.LocationDigest != second.LocationDigest {
		t.Fatalf("canonical digests changed: first=%s/%s second=%s/%s", first.SemanticDigest, first.LocationDigest, second.SemanticDigest, second.LocationDigest)
	}
	if !reflect.DeepEqual(first.Snapshot, second.Snapshot) || !reflect.DeepEqual(first.SnapshotJSON, second.SnapshotJSON) || !reflect.DeepEqual(first.ObservationBindings, second.ObservationBindings) {
		t.Fatalf("canonical source changed:\nfirst=%s\nsecond=%s", first.SnapshotJSON, second.SnapshotJSON)
	}
	if got := string(first.Snapshot.StableRegistrationPlan); strings.Contains(got, "generated_at") || strings.Contains(got, "/srv/loom/projects/example") || !strings.Contains(got, `"contract_path":"$project_contract"`) || !strings.Contains(got, `"z_options":{"a":1,"absolute_path":"scripts/job","z":2}`) {
		t.Fatalf("stable registration plan = %s", got)
	}
	if len(first.Snapshot.Members) != 2 || first.Snapshot.Members[0].RepositoryID != testRepoIDA || first.Snapshot.Members[1].RepositoryID != testRepoIDB {
		t.Fatalf("members are not complete and sorted: %#v", first.Snapshot.Members)
	}
	if first.Snapshot.Members[0].ObservationBindingDigest != first.ObservationBindings[0].Digest {
		t.Fatalf("snapshot binding does not match result binding")
	}
}

func TestBuildProjectRepositoryCanonicalSourceRealCompiledPlanRelocation(t *testing.T) {
	root, beforeInput := realCompiledProjectRepositorySourceInput(t)
	beforePlan := decodeProjectPlanForTest(t, beforeInput.RegistrationPlan)
	beforeCommand := requireCompiledWatchedRootCommand(t, &beforePlan)
	if !stringSliceContains(beforeCommand.Command, root) || !strings.Contains(beforeCommand.Shell, root) {
		t.Fatalf("real compiled watched-root command does not contain project root: %#v", beforeCommand)
	}
	beforePlanJSON := append(json.RawMessage(nil), beforeInput.RegistrationPlan...)
	before := mustBuildProjectRepositorySource(t, beforeInput)
	if !reflect.DeepEqual(beforeInput.RegistrationPlan, beforePlanJSON) {
		t.Fatal("canonical source build changed the live compiled registration plan")
	}

	movedRoot := filepath.Join(t.TempDir(), "relocated-compiled-plan")
	if err := os.Rename(root, movedRoot); err != nil {
		t.Fatalf("move complete project root: %v", err)
	}
	afterAnalysis := projectcontracts.Analyze(movedRoot)
	afterInput := projectRepositorySourceInputFromAnalysis(t, afterAnalysis)
	afterPlan := decodeProjectPlanForTest(t, afterInput.RegistrationPlan)
	afterCommand := requireCompiledWatchedRootCommand(t, &afterPlan)
	if !stringSliceContains(afterCommand.Command, movedRoot) || !strings.Contains(afterCommand.Shell, movedRoot) {
		t.Fatalf("relocated real compiled watched-root command does not contain new root: %#v", afterCommand)
	}
	after := mustBuildProjectRepositorySource(t, afterInput)

	if before.SemanticDigest != after.SemanticDigest {
		t.Fatalf("relocation changed real compiled-plan semantics: before=%s after=%s\nbefore plan=%s\nafter plan=%s", before.SemanticDigest, after.SemanticDigest, before.Snapshot.StableRegistrationPlan, after.Snapshot.StableRegistrationPlan)
	}
	if before.LocationDigest == after.LocationDigest {
		t.Fatal("relocation did not change location digest")
	}
	if len(before.ObservationBindings) != 1 || len(after.ObservationBindings) != 1 || before.ObservationBindings[0].Digest == after.ObservationBindings[0].Digest {
		t.Fatalf("relocation did not change the real member observation binding: before=%#v after=%#v", before.ObservationBindings, after.ObservationBindings)
	}
	change, err := projects.ClassifyProjectRepositorySourceChange(&before, after)
	if err != nil {
		t.Fatal(err)
	}
	if change.Classification != projects.ProjectRepositorySourceClassificationRelocation || change.HistoryChangeKind != projects.ProjectRepositorySourceRelocation {
		t.Fatalf("real compiled-plan relocation classification = %#v", change)
	}
	if !reflect.DeepEqual(change.Summary.ContentChanges, []string{}) || !reflect.DeepEqual(change.Summary.LocationChanges, []string{"project_contract_path", "project_root", "repos_contract_path"}) {
		t.Fatalf("real compiled-plan relocation summary = %#v", change.Summary)
	}
	stablePlan := string(before.Snapshot.StableRegistrationPlan)
	if strings.Contains(stablePlan, root) || strings.Contains(stablePlan, movedRoot) || !strings.Contains(stablePlan, `"--project-root","."`) || !strings.Contains(stablePlan, `--project-root .`) {
		t.Fatalf("stable real compiled plan did not derive a relocated watched-root command: %s", stablePlan)
	}
}

func TestBuildProjectRepositoryCanonicalSourceRejectsRealCompiledPlanCommandShellMismatch(t *testing.T) {
	_, input := realCompiledProjectRepositorySourceInput(t)
	plan := decodeProjectPlanForTest(t, input.RegistrationPlan)
	command := requireCompiledWatchedRootCommand(t, &plan)
	command.Shell += " "
	input.RegistrationPlan = marshalJSONForTest(t, plan)

	if _, err := projects.BuildProjectRepositoryCanonicalSource(input); !errors.Is(err, projects.ErrInvalidProjectRepositorySourceInput) || !strings.Contains(err.Error(), "shell does not exactly represent command") {
		t.Fatalf("command/shell mismatch error = %v", err)
	}
}

func TestBuildProjectRepositoryCanonicalSourceDoesNotNormalizeUnrelatedEmbeddedRoot(t *testing.T) {
	root, beforeInput := realCompiledProjectRepositorySourceInput(t)
	addRegistrationPlanFieldForTest(t, &beforeInput, "operator_note", "inspect "+root+"/repos without changing it")
	before := mustBuildProjectRepositorySource(t, beforeInput)
	if !strings.Contains(string(before.Snapshot.StableRegistrationPlan), "inspect "+root+"/repos without changing it") {
		t.Fatalf("unrelated embedded-root string was rewritten: %s", before.Snapshot.StableRegistrationPlan)
	}

	movedRoot := filepath.Join(t.TempDir(), "relocated-embedded-root")
	if err := os.Rename(root, movedRoot); err != nil {
		t.Fatalf("move complete project root: %v", err)
	}
	afterInput := projectRepositorySourceInputFromAnalysis(t, projectcontracts.Analyze(movedRoot))
	addRegistrationPlanFieldForTest(t, &afterInput, "operator_note", "inspect "+movedRoot+"/repos without changing it")
	after := mustBuildProjectRepositorySource(t, afterInput)
	if before.SemanticDigest == after.SemanticDigest {
		t.Fatal("unrelated embedded-root semantic string was normalized as relocation-only content")
	}
	change, err := projects.ClassifyProjectRepositorySourceChange(&before, after)
	if err != nil {
		t.Fatal(err)
	}
	if change.Classification != projects.ProjectRepositorySourceClassificationSemanticChangeAndRelocation {
		t.Fatalf("unrelated embedded-root classification = %q", change.Classification)
	}
}

func TestDecodeProjectRepositoryCanonicalSourceRejectsRealCompiledPlanTampering(t *testing.T) {
	_, input := realCompiledProjectRepositorySourceInput(t)
	built := mustBuildProjectRepositorySource(t, input)

	for _, test := range []struct {
		name   string
		mutate func(*testing.T, map[string]any)
	}{
		{
			name: "shell no longer represents structured command",
			mutate: func(t *testing.T, plan map[string]any) {
				command := requireGenericWatchedRootCommand(t, plan)
				command["shell"] = command["shell"].(string) + " "
			},
		},
		{
			name: "coherent command and shell semantic mutation",
			mutate: func(t *testing.T, plan map[string]any) {
				command := requireGenericWatchedRootCommand(t, plan)
				arguments := command["command"].([]any)
				arguments[len(arguments)-1] = "tampered-root-key"
				text := make([]string, len(arguments))
				for index, argument := range arguments {
					text[index] = argument.(string)
				}
				command["shell"] = strings.Join(text, " ")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var plan map[string]any
			if err := json.Unmarshal(built.Snapshot.StableRegistrationPlan, &plan); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, plan)
			mutated := built.Snapshot
			mutated.StableRegistrationPlan = marshalJSONForTest(t, plan)
			if _, err := projects.DecodeProjectRepositoryCanonicalSource(marshalJSONForTest(t, mutated), built.SemanticDigest, built.LocationDigest); !errors.Is(err, projects.ErrInvalidProjectRepositorySourceInput) {
				t.Fatalf("stored-plan tampering error = %v", err)
			}
		})
	}
}

func TestBuildProjectRepositoryCanonicalSourceCanonicalizesEquivalentJSONNumbers(t *testing.T) {
	tests := []struct {
		name      string
		forms     []string
		canonical string
	}{
		{name: "integral spellings", forms: []string{"1", "1.0", "1e0", "10e-1"}, canonical: "1"},
		{name: "decimal spellings", forms: []string{"1.23", "1.2300", "123e-2", "0.123e1", "12.3e-1"}, canonical: "123e-2"},
		{name: "positive exponent spellings", forms: []string{"1000", "1e3", "10e2", "10000e-1"}, canonical: "1e3"},
		{name: "negative decimal spellings", forms: []string{"-0.125", "-125e-3", "-1.25e-1", "-1250e-4"}, canonical: "-125e-3"},
		{name: "negative zero is zero", forms: []string{"0", "-0", "-0.0", "0e100", "-0e-100"}, canonical: "0"},
		{
			name: "large exact integer beyond float64 precision",
			forms: []string{
				"90071992547409931234567890123456789",
				"900719925474099312345678901234567890e-1",
			},
			canonical: "90071992547409931234567890123456789",
		},
		{
			name: "large exact decimal beyond float64 precision",
			forms: []string{
				"9007199254740993.12500",
				"9007199254740993125e-3",
				"900719925474099312500e-5",
			},
			canonical: "9007199254740993125e-3",
		},
		{name: "bounded positive scale", forms: []string{"1e4095", "10e4094"}, canonical: "1e4095"},
		{name: "bounded negative scale", forms: []string{"1e-4096", "10e-4097"}, canonical: "1e-4096"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var first projects.ProjectRepositoryCanonicalSource
			for index, form := range test.forms {
				input := testProjectRepositorySourceInput()
				input.RegistrationPlan = json.RawMessage(`{"numeric_policy":` + form + `}`)
				got := mustBuildProjectRepositorySource(t, input)
				if plan := string(got.Snapshot.StableRegistrationPlan); plan != `{"numeric_policy":`+test.canonical+`}` {
					t.Fatalf("form %q stable plan = %s", form, plan)
				}
				if index == 0 {
					first = got
					continue
				}
				if got.SemanticDigest != first.SemanticDigest || !reflect.DeepEqual(got.Snapshot.StableRegistrationPlan, first.Snapshot.StableRegistrationPlan) {
					t.Fatalf("form %q changed canonical identity: got=%s/%s want=%s/%s", form, got.Snapshot.StableRegistrationPlan, got.SemanticDigest, first.Snapshot.StableRegistrationPlan, first.SemanticDigest)
				}
			}
		})
	}
}

func TestDecodeProjectRepositoryCanonicalSourceRestoresJSONBNormalizedNumbers(t *testing.T) {
	input := testProjectRepositorySourceInput()
	input.RegistrationPlan = json.RawMessage(`{"numeric_policy":1.0,"nested":[10e-1,-0.0],"label":"a"}`)
	built := mustBuildProjectRepositorySource(t, input)

	var jsonbShape map[string]any
	if err := json.Unmarshal(built.SnapshotJSON, &jsonbShape); err != nil {
		t.Fatal(err)
	}
	jsonbRoundTrip, err := json.Marshal(jsonbShape)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := projects.DecodeProjectRepositoryCanonicalSource(jsonbRoundTrip, built.SemanticDigest, built.LocationDigest)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored, built) {
		t.Fatalf("JSONB-like numeric round trip changed source:\ngot=%#v\nwant=%#v", restored, built)
	}

	relexedSnapshot := built.Snapshot
	relexedSnapshot.StableRegistrationPlan = json.RawMessage(`{
		"numeric_policy": 1.000,
		"nested": [1e0, -0e10],
		"label": "\u0061"
	}`)
	relexedJSON, err := json.Marshal(relexedSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	restored, err = projects.DecodeProjectRepositoryCanonicalSource(relexedJSON, built.SemanticDigest, built.LocationDigest)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored, built) {
		t.Fatalf("equivalent stored number lexemes changed source:\ngot=%#v\nwant=%#v", restored, built)
	}
	if _, err := projects.DecodeProjectRepositoryCanonicalSource(relexedJSON, digestOf('f'), built.LocationDigest); !errors.Is(err, projects.ErrInvalidProjectRepositorySourceInput) {
		t.Fatalf("forged digest after numeric recanonicalization error = %v", err)
	}
}

func TestDecodeProjectRepositoryCanonicalSourceRejectsStoredPlanSemanticMutations(t *testing.T) {
	built := mustBuildProjectRepositorySource(t, testProjectRepositorySourceInput())
	tests := []struct {
		name   string
		mutate func(*testing.T, string) string
	}{
		{
			name: "reintroduced generated field",
			mutate: func(_ *testing.T, plan string) string {
				return strings.Replace(plan, "{", `{"generated_at":"tampered",`, 1)
			},
		},
		{
			name: "absolute path substitution",
			mutate: func(_ *testing.T, plan string) string {
				return strings.Replace(plan, `"absolute_path":"scripts/job"`, `"absolute_path":"/srv/loom/projects/example/scripts/job"`, 1)
			},
		},
		{
			name: "contract token substitution",
			mutate: func(_ *testing.T, plan string) string {
				return strings.Replace(plan, `"contract_path":"$project_contract"`, `"contract_path":".loom/project.yaml"`, 1)
			},
		},
		{
			name: "added semantic field",
			mutate: func(_ *testing.T, plan string) string {
				return strings.Replace(plan, "{", `{"added_semantic":true,`, 1)
			},
		},
		{
			name: "duplicate stored key",
			mutate: func(_ *testing.T, plan string) string {
				return strings.Replace(plan, "{", `{"schema_version":"tampered",`, 1)
			},
		},
		{
			name: "removed semantic field",
			mutate: func(t *testing.T, plan string) string {
				t.Helper()
				var fields map[string]json.RawMessage
				if err := json.Unmarshal([]byte(plan), &fields); err != nil {
					t.Fatal(err)
				}
				delete(fields, "schema_version")
				raw, err := json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
				return string(raw)
			},
		},
		{
			name: "changed semantic value",
			mutate: func(_ *testing.T, plan string) string {
				return strings.Replace(plan, `"status":"active"`, `"status":"tampered"`, 1)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutatedPlan := test.mutate(t, string(built.Snapshot.StableRegistrationPlan))
			if mutatedPlan == string(built.Snapshot.StableRegistrationPlan) {
				t.Fatal("test mutation did not change the stable plan")
			}
			mutatedSnapshot := built.Snapshot
			mutatedSnapshot.StableRegistrationPlan = json.RawMessage(mutatedPlan)
			mutatedJSON, err := json.Marshal(mutatedSnapshot)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := projects.DecodeProjectRepositoryCanonicalSource(mutatedJSON, built.SemanticDigest, built.LocationDigest); !errors.Is(err, projects.ErrInvalidProjectRepositorySourceInput) {
				t.Fatalf("semantic mutation error = %v, want invalid source input", err)
			}
		})
	}
}

func TestBuildProjectRepositoryCanonicalSourceRejectsMalformedOrPathologicalJSONNumbers(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "leading zero", value: "01"},
		{name: "missing fraction", value: "1."},
		{name: "missing exponent", value: "1e"},
		{name: "leading plus", value: "+1"},
		{name: "too many significant digits", value: strings.Repeat("1", 4097)},
		{name: "positive expanded range", value: "1e4096"},
		{name: "negative expanded range", value: "1e-4097"},
		{name: "huge exponent", value: "1e999999999999999999999999"},
		{name: "pathological fraction", value: "0." + strings.Repeat("0", 8192) + "1"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := testProjectRepositorySourceInput()
			input.RegistrationPlan = json.RawMessage(`{"numeric_policy":` + test.value + `}`)
			if _, err := projects.BuildProjectRepositoryCanonicalSource(input); !errors.Is(err, projects.ErrInvalidProjectRepositorySourceInput) {
				t.Fatalf("value %q error = %v, want invalid source input", test.value, err)
			}
		})
	}
}

func TestBuildProjectRepositoryCanonicalSourceDigestDimensions(t *testing.T) {
	baseInput := testProjectRepositorySourceInput()
	base, err := projects.BuildProjectRepositoryCanonicalSource(baseInput)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name            string
		mutate          func(*projects.ProjectRepositoryValidatedSourceInput)
		semanticChanged bool
		locationChanged bool
		bindingChanged  bool
	}{
		{
			name: "member order is irrelevant",
			mutate: func(input *projects.ProjectRepositoryValidatedSourceInput) {
				input.Members[0], input.Members[1] = input.Members[1], input.Members[0]
			},
		},
		{
			name: "project contract content",
			mutate: func(input *projects.ProjectRepositoryValidatedSourceInput) {
				input.ProjectContractDigest = digestOf('c')
			},
			semanticChanged: true,
		},
		{
			name: "repos contract content",
			mutate: func(input *projects.ProjectRepositoryValidatedSourceInput) {
				input.ReposContractDigest = digestOf('d')
			},
			semanticChanged: true,
		},
		{
			name: "stable registration plan content",
			mutate: func(input *projects.ProjectRepositoryValidatedSourceInput) {
				input.RegistrationPlan = json.RawMessage(strings.Replace(string(input.RegistrationPlan), `"status":"active"`, `"status":"paused"`, 1))
			},
			semanticChanged: true,
		},
		{
			name: "project root relocation",
			mutate: func(input *projects.ProjectRepositoryValidatedSourceInput) {
				relocateProjectRepositorySourceInput(input, "/srv/loom/projects/example-moved")
			},
			locationChanged: true,
			bindingChanged:  true,
		},
		{
			name: "project contract path relocation",
			mutate: func(input *projects.ProjectRepositoryValidatedSourceInput) {
				input.ProjectContractPath = input.ProjectRoot + "/loom.project.yaml"
				input.RegistrationPlan = json.RawMessage(strings.Replace(string(input.RegistrationPlan), input.ProjectRoot+"/.loom/project.yaml", input.ProjectContractPath, 1))
			},
			locationChanged: true,
		},
		{
			name: "repos contract path relocation",
			mutate: func(input *projects.ProjectRepositoryValidatedSourceInput) {
				input.ReposContractPath = input.ProjectRoot + "/.loom/contracts/repos.yaml"
			},
			locationChanged: true,
		},
		{
			name: "owner node location",
			mutate: func(input *projects.ProjectRepositoryValidatedSourceInput) {
				input.OwnerNode = "workspace"
			},
			locationChanged: true,
			bindingChanged:  true,
		},
		{
			name: "project source version",
			mutate: func(input *projects.ProjectRepositoryValidatedSourceInput) {
				input.Versions.ProjectContract = projects.ProjectRepositoryProjectSchemaV03
			},
			semanticChanged: true,
			bindingChanged:  true,
		},
		{
			name: "repos source version",
			mutate: func(input *projects.ProjectRepositoryValidatedSourceInput) {
				input.Versions.ReposContract = projects.ProjectRepositoryReposSchemaV03
			},
			semanticChanged: true,
			bindingChanged:  true,
		},
		{
			name: "member key",
			mutate: func(input *projects.ProjectRepositoryValidatedSourceInput) {
				input.Members[0].Key = "api"
			},
			semanticChanged: true,
		},
		{
			name: "repository identity",
			mutate: func(input *projects.ProjectRepositoryValidatedSourceInput) {
				input.Members[0].RepositoryID = testRepoIDC
			},
			semanticChanged: true,
			bindingChanged:  true,
		},
		{
			name: "member path",
			mutate: func(input *projects.ProjectRepositoryValidatedSourceInput) {
				input.Members[0].Path = "backend-renamed"
			},
			semanticChanged: true,
			bindingChanged:  true,
		},
		{
			name: "member state root",
			mutate: func(input *projects.ProjectRepositoryValidatedSourceInput) {
				input.Members[0].StateRoot = ".repo-v2"
			},
			semanticChanged: true,
			bindingChanged:  true,
		},
		{
			name: "member role",
			mutate: func(input *projects.ProjectRepositoryValidatedSourceInput) {
				input.Members[0].Role = projects.ProjectRepositoryRoleComponent
			},
			semanticChanged: true,
			bindingChanged:  true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := cloneProjectRepositorySourceInput(baseInput)
			test.mutate(&input)
			got, err := projects.BuildProjectRepositoryCanonicalSource(input)
			if err != nil {
				t.Fatal(err)
			}
			if changed := got.SemanticDigest != base.SemanticDigest; changed != test.semanticChanged {
				t.Errorf("semantic changed = %v, want %v", changed, test.semanticChanged)
			}
			if changed := got.LocationDigest != base.LocationDigest; changed != test.locationChanged {
				t.Errorf("location changed = %v, want %v", changed, test.locationChanged)
			}
			if changed := got.ObservationBindings[0].Digest != base.ObservationBindings[0].Digest; changed != test.bindingChanged {
				t.Errorf("first member binding changed = %v, want %v", changed, test.bindingChanged)
			}
		})
	}
}

func TestClassifyProjectRepositorySourceChange(t *testing.T) {
	baseInput := testProjectRepositorySourceInput()
	base := mustBuildProjectRepositorySource(t, baseInput)

	tests := []struct {
		name        string
		current     *projects.ProjectRepositoryCanonicalSource
		mutate      func(*projects.ProjectRepositoryValidatedSourceInput)
		wantClass   projects.ProjectRepositorySourceClassification
		wantHistory projects.ProjectRepositorySourceChangeKind
	}{
		{name: "first registration", wantClass: projects.ProjectRepositorySourceClassificationFirstRegistration, wantHistory: projects.ProjectRepositorySourceFirstRegistration},
		{name: "identical replay", current: &base, wantClass: projects.ProjectRepositorySourceClassificationIdenticalReplay},
		{
			name:    "source relocation",
			current: &base,
			mutate: func(input *projects.ProjectRepositoryValidatedSourceInput) {
				relocateProjectRepositorySourceInput(input, "/srv/loom/projects/example-moved")
			},
			wantClass:   projects.ProjectRepositorySourceClassificationRelocation,
			wantHistory: projects.ProjectRepositorySourceRelocation,
		},
		{
			name:        "semantic change",
			current:     &base,
			mutate:      func(input *projects.ProjectRepositoryValidatedSourceInput) { input.Members[0].Key = "api" },
			wantClass:   projects.ProjectRepositorySourceClassificationSemanticChange,
			wantHistory: projects.ProjectRepositorySourceSemanticChange,
		},
		{
			name:    "semantic change and relocation",
			current: &base,
			mutate: func(input *projects.ProjectRepositoryValidatedSourceInput) {
				input.Members[0].Path = "backend-renamed"
				relocateProjectRepositorySourceInput(input, "/srv/loom/projects/example-moved")
			},
			wantClass:   projects.ProjectRepositorySourceClassificationSemanticChangeAndRelocation,
			wantHistory: projects.ProjectRepositorySourceSemanticChangeAndRelocation,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := cloneProjectRepositorySourceInput(baseInput)
			if test.mutate != nil {
				test.mutate(&input)
			}
			next := mustBuildProjectRepositorySource(t, input)
			change, err := projects.ClassifyProjectRepositorySourceChange(test.current, next)
			if err != nil {
				t.Fatal(err)
			}
			if change.Classification != test.wantClass || change.HistoryChangeKind != test.wantHistory {
				t.Fatalf("change = %#v, want class %q history %q", change, test.wantClass, test.wantHistory)
			}
		})
	}
}

func TestDecodeProjectRepositoryCanonicalSourceRecanonicalizesJSONBAndRejectsTampering(t *testing.T) {
	built := mustBuildProjectRepositorySource(t, testProjectRepositorySourceInput())
	var jsonbShape map[string]any
	if err := json.Unmarshal(built.SnapshotJSON, &jsonbShape); err != nil {
		t.Fatal(err)
	}
	reordered, err := json.Marshal(jsonbShape)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(reordered, built.SnapshotJSON) {
		t.Fatal("test did not reorder the snapshot object")
	}
	restored, err := projects.DecodeProjectRepositoryCanonicalSource(reordered, built.SemanticDigest, built.LocationDigest)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored, built) {
		t.Fatalf("restored source differs from build:\ngot=%#v\nwant=%#v", restored, built)
	}

	tamperedSnapshot := built.Snapshot
	tamperedSnapshot.Members = append([]projects.ProjectRepositorySourceMemberSnapshot(nil), built.Snapshot.Members...)
	tamperedSnapshot.Members[0].ObservationBindingDigest = digestOf('f')
	tamperedJSON, err := json.Marshal(tamperedSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projects.DecodeProjectRepositoryCanonicalSource(tamperedJSON, built.SemanticDigest, built.LocationDigest); !errors.Is(err, projects.ErrInvalidProjectRepositorySourceInput) {
		t.Fatalf("tampered binding error = %v", err)
	}

	jsonbShape["unexpected"] = true
	unknownFieldJSON, err := json.Marshal(jsonbShape)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projects.DecodeProjectRepositoryCanonicalSource(unknownFieldJSON, built.SemanticDigest, built.LocationDigest); !errors.Is(err, projects.ErrInvalidProjectRepositorySourceInput) {
		t.Fatalf("unknown field error = %v", err)
	}
}

func TestProjectRepositoryChangeSummaryIsDeterministicAndBounded(t *testing.T) {
	input := testProjectRepositorySourceInput()
	input.Members = make([]projects.ProjectRepositoryValidatedMember, 0, projects.ProjectRepositoryChangeSummaryMemberLimit+13)
	for index := 0; index < projects.ProjectRepositoryChangeSummaryMemberLimit+13; index++ {
		input.Members = append(input.Members, projects.ProjectRepositoryValidatedMember{
			RepositoryID: fmt.Sprintf("repo_%026d", index),
			Key:          fmt.Sprintf("repo-%03d", index),
			Path:         fmt.Sprintf("repo-%03d", index),
			Role:         projects.ProjectRepositoryRoleComponent,
		})
	}
	next := mustBuildProjectRepositorySource(t, input)
	change, err := projects.ClassifyProjectRepositorySourceChange(nil, next)
	if err != nil {
		t.Fatal(err)
	}
	membership := change.Summary.MembershipChanges
	if membership.AddedCount != projects.ProjectRepositoryChangeSummaryMemberLimit+13 || len(membership.AddedRepositoryIDs) != projects.ProjectRepositoryChangeSummaryMemberLimit || membership.OmittedCount != 13 || !membership.Truncated {
		t.Fatalf("bounded membership summary = %#v", membership)
	}
	if !sortStringsAreStrictlyIncreasing(membership.AddedRepositoryIDs) {
		t.Fatalf("bounded member ids are not deterministic: %#v", membership.AddedRepositoryIDs)
	}
	raw, err := json.Marshal(change.Summary)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), fmt.Sprintf("repo_%026d", projects.ProjectRepositoryChangeSummaryMemberLimit+12)) {
		t.Fatalf("bounded summary leaked an omitted repository: %s", raw)
	}
}

func TestProjectRepositoryChangeSummarySeparatesDimensions(t *testing.T) {
	currentInput := testProjectRepositorySourceInput()
	current := mustBuildProjectRepositorySource(t, currentInput)
	nextInput := cloneProjectRepositorySourceInput(currentInput)
	nextInput.ProjectContractDigest = digestOf('c')
	nextInput.Versions.ReposContract = projects.ProjectRepositoryReposSchemaV03
	nextInput.Members = []projects.ProjectRepositoryValidatedMember{
		{RepositoryID: testRepoIDA, Key: "api", Path: "backend-renamed", Role: projects.ProjectRepositoryRolePrimary, StateRoot: ".repo-v2"},
		{RepositoryID: testRepoIDC, Key: "worker", Path: "worker", Role: projects.ProjectRepositoryRoleComponent},
	}
	relocateProjectRepositorySourceInput(&nextInput, "/srv/loom/projects/example-moved")
	next := mustBuildProjectRepositorySource(t, nextInput)
	change, err := projects.ClassifyProjectRepositorySourceChange(&current, next)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(change.Summary.ContentChanges, []string{"project_contract_digest"}) {
		t.Errorf("content changes = %#v", change.Summary.ContentChanges)
	}
	if !reflect.DeepEqual(change.Summary.VersionChanges, []string{"repos_contract_schema_version"}) {
		t.Errorf("version changes = %#v", change.Summary.VersionChanges)
	}
	if !reflect.DeepEqual(change.Summary.LocationChanges, []string{"project_contract_path", "project_root", "repos_contract_path"}) {
		t.Errorf("location changes = %#v", change.Summary.LocationChanges)
	}
	membership := change.Summary.MembershipChanges
	if !reflect.DeepEqual(membership.AddedRepositoryIDs, []string{testRepoIDC}) || !reflect.DeepEqual(membership.RemovedRepositoryIDs, []string{testRepoIDB}) || membership.ChangedCount != 1 {
		t.Fatalf("membership changes = %#v", membership)
	}
	wantChanged := projects.ProjectRepositoryMemberFieldChange{RepositoryID: testRepoIDA, Fields: []string{"key", "path", "state_root"}}
	if !reflect.DeepEqual(membership.ChangedMembers, []projects.ProjectRepositoryMemberFieldChange{wantChanged}) {
		t.Fatalf("changed members = %#v", membership.ChangedMembers)
	}
}

func TestPlanProjectRepositoryObservationBinding(t *testing.T) {
	source := mustBuildProjectRepositorySource(t, testProjectRepositorySourceInput())
	binding := source.ObservationBindings[0]
	observedAt := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	observed := projects.ProjectRepositoryObservation{
		ProjectID:           binding.ProjectID,
		RepositoryID:        binding.RepositoryID,
		SourceBindingDigest: binding.Digest,
		ObservationPosture:  projects.ProjectRepositoryObservationObserved,
		ReasonCode:          "",
		ObservedAt:          &observedAt,
		Observation:         json.RawMessage(`{"head":"abc123","dirty":false}`),
		ObservationRevision: 7,
	}
	remoteUnavailable := observed
	remoteUnavailable.ObservationPosture = projects.ProjectRepositoryObservationRemoteUnavailable
	remoteUnavailable.ReasonCode = "backend_offline"
	notObserved := observed
	notObserved.ObservationPosture = projects.ProjectRepositoryObservationNotObserved
	notObserved.ReasonCode = "member_missing"
	notObserved.ObservedAt = nil
	notObserved.Observation = json.RawMessage(`{"old":"diagnostic"}`)
	changedBinding := binding
	changedBinding.Digest = digestOf('f')

	tests := []struct {
		name            string
		current         *projects.ProjectRepositoryObservation
		binding         projects.ProjectRepositoryObservationBinding
		wantChanged     bool
		wantInvalidated bool
		wantRevision    int64
	}{
		{name: "initial", binding: binding, wantChanged: true, wantRevision: 1},
		{name: "unchanged observed", current: &observed, binding: binding, wantRevision: 7},
		{name: "changed observed", current: &observed, binding: changedBinding, wantChanged: true, wantInvalidated: true, wantRevision: 8},
		{name: "changed remote unavailable", current: &remoteUnavailable, binding: changedBinding, wantChanged: true, wantInvalidated: true, wantRevision: 8},
		{name: "changed not observed", current: &notObserved, binding: changedBinding, wantChanged: true, wantInvalidated: true, wantRevision: 8},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var before projects.ProjectRepositoryObservation
			if test.current != nil {
				before = cloneObservationForTest(*test.current)
			}
			transition, err := projects.PlanProjectRepositoryObservationBinding(test.current, test.binding)
			if err != nil {
				t.Fatal(err)
			}
			if transition.Changed != test.wantChanged || transition.Invalidated != test.wantInvalidated || transition.Observation.ObservationRevision != test.wantRevision {
				t.Fatalf("transition = %#v", transition)
			}
			if test.current != nil && !reflect.DeepEqual(*test.current, before) {
				t.Fatalf("pure transition mutated its input: before=%#v after=%#v", before, *test.current)
			}
			if test.wantInvalidated {
				if transition.Observation.ObservationPosture != projects.ProjectRepositoryObservationNotObserved || transition.Observation.ObservedAt != nil || transition.Observation.ReasonCode != string(projects.ProjectRepositoryObservationReasonSourceBindingChanged) || string(transition.Observation.Observation) != `{}` {
					t.Fatalf("stale observation survived invalidation: %#v", transition.Observation)
				}
			}
			if !test.wantChanged && !reflect.DeepEqual(transition.Observation, *test.current) {
				t.Fatalf("unchanged observation was not preserved: got=%#v want=%#v", transition.Observation, *test.current)
			}
		})
	}
}

func TestBuildProjectRepositoryCanonicalSourceRejectsUnstableInputs(t *testing.T) {
	base := testProjectRepositorySourceInput()
	tests := []struct {
		name   string
		mutate func(*projects.ProjectRepositoryValidatedSourceInput)
	}{
		{name: "relative project root", mutate: func(input *projects.ProjectRepositoryValidatedSourceInput) { input.ProjectRoot = "relative" }},
		{name: "unknown source version", mutate: func(input *projects.ProjectRepositoryValidatedSourceInput) {
			input.Versions.ReposContract = "repos.contract.v9"
		}},
		{name: "invalid contract digest", mutate: func(input *projects.ProjectRepositoryValidatedSourceInput) {
			input.ProjectContractDigest = "sha256:nope"
		}},
		{name: "duplicate member identity", mutate: func(input *projects.ProjectRepositoryValidatedSourceInput) {
			input.Members[1].RepositoryID = input.Members[0].RepositoryID
		}},
		{name: "duplicate member key", mutate: func(input *projects.ProjectRepositoryValidatedSourceInput) {
			input.Members[1].Key = input.Members[0].Key
		}},
		{name: "invalid member path", mutate: func(input *projects.ProjectRepositoryValidatedSourceInput) { input.Members[0].Path = "../escape" }},
		{name: "registration plan array", mutate: func(input *projects.ProjectRepositoryValidatedSourceInput) {
			input.RegistrationPlan = json.RawMessage(`[]`)
		}},
		{name: "registration plan duplicate key", mutate: func(input *projects.ProjectRepositoryValidatedSourceInput) {
			input.RegistrationPlan = json.RawMessage(`{"schema_version":"one","schema_version":"two"}`)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := cloneProjectRepositorySourceInput(base)
			test.mutate(&input)
			if _, err := projects.BuildProjectRepositoryCanonicalSource(input); !errors.Is(err, projects.ErrInvalidProjectRepositorySourceInput) {
				t.Fatalf("error = %v, want invalid source input", err)
			}
		})
	}
}

func TestBuildProjectRepositoryCanonicalSourceIsRaceSafe(t *testing.T) {
	input := testProjectRepositorySourceInput()
	want := mustBuildProjectRepositorySource(t, input)
	const workers = 24
	const iterations = 40
	errCh := make(chan error, workers)
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < iterations; iteration++ {
				got, err := projects.BuildProjectRepositoryCanonicalSource(input)
				if err != nil {
					errCh <- err
					return
				}
				if got.SemanticDigest != want.SemanticDigest || got.LocationDigest != want.LocationDigest || !reflect.DeepEqual(got.SnapshotJSON, want.SnapshotJSON) {
					errCh <- fmt.Errorf("non-deterministic source at iteration %d", iteration)
					return
				}
			}
		}()
	}
	wait.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

func realCompiledProjectRepositorySourceInput(t *testing.T) (string, projects.ProjectRepositoryValidatedSourceInput) {
	t.Helper()
	result, err := projectcontracts.ScaffoldProject(projectcontracts.ScaffoldOptions{
		Name:      "Compiled Plan Relocation",
		Slug:      "compiled-plan-relocation",
		OwnerNode: "main",
		Preset:    projectcontracts.PresetMinimal,
		Facets:    []string{"repos"},
		Directory: t.TempDir(),
		RepositoryMembers: []projectcontracts.RepoMemberSpec{
			{Key: "backend", Path: "backend", Role: projectcontracts.RepositoryRolePrimary, StateRoot: ".repo"},
		},
	})
	if err != nil {
		t.Fatalf("scaffold real compiled-plan fixture: %v", err)
	}
	analysis := projectcontracts.Analyze(result.ProjectRoot)
	return result.ProjectRoot, projectRepositorySourceInputFromAnalysis(t, analysis)
}

func projectRepositorySourceInputFromAnalysis(t *testing.T, analysis projectcontracts.Analysis) projects.ProjectRepositoryValidatedSourceInput {
	t.Helper()
	if analysis.Loaded == nil || !analysis.Report.OK || !analysis.Report.Registerable || !analysis.Plan.Registerable {
		t.Fatalf("real projectcontracts analysis is not registerable: %#v", analysis.Report.Diagnostics)
	}
	registration, err := projectregistration.BuildInput(analysis, "slice-4r-test")
	if err != nil {
		t.Fatalf("map real compiled plan to registration input: %v", err)
	}
	if registration.RepositorySource == nil {
		t.Fatal("real compiled plan omitted repository source")
	}
	members := make([]projects.ProjectRepositoryValidatedMember, 0, len(analysis.Plan.RepositoryMembers))
	for _, member := range analysis.Plan.RepositoryMembers {
		members = append(members, projects.ProjectRepositoryValidatedMember{
			RepositoryID: member.ID,
			Key:          member.Key,
			Path:         member.Path,
			Role:         projects.ProjectRepositoryRole(member.Role),
			StateRoot:    member.StateRoot,
		})
	}
	return projects.ProjectRepositoryValidatedSourceInput{
		ProjectID:             registration.Project.ID,
		ProjectRoot:           registration.ProjectRoot,
		ProjectContractPath:   registration.ContractPath,
		ReposContractPath:     registration.RepositorySource.ContractPath,
		OwnerNode:             registration.Project.OwnerNode,
		Versions:              projects.ProjectRepositorySourceVersions{ProjectContract: registration.ContractSchemaVersion, ReposContract: registration.RepositorySource.ContractSchemaVersion},
		ProjectContractDigest: registration.ContractHash,
		ReposContractDigest:   registration.RepositorySource.ContractHash,
		RegistrationPlan:      append(json.RawMessage(nil), registration.RegistrationPlan...),
		Members:               members,
	}
}

func decodeProjectPlanForTest(t *testing.T, raw json.RawMessage) projectcontracts.ProjectPlan {
	t.Helper()
	var plan projectcontracts.ProjectPlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatalf("decode real compiled project plan: %v", err)
	}
	return plan
}

func requireCompiledWatchedRootCommand(t *testing.T, plan *projectcontracts.ProjectPlan) *projectcontracts.ProjectWatchedRootCommand {
	t.Helper()
	if len(plan.WatchedRoots) == 0 || len(plan.WatchedRoots[0].AgentCommands) == 0 {
		t.Fatalf("real compiled plan has no watched-root agent command: %#v", plan.WatchedRoots)
	}
	return &plan.WatchedRoots[0].AgentCommands[0]
}

func requireGenericWatchedRootCommand(t *testing.T, plan map[string]any) map[string]any {
	t.Helper()
	rootValues, ok := plan["watched_roots"].([]any)
	if !ok || len(rootValues) == 0 {
		t.Fatalf("stable plan has no watched roots: %#v", plan["watched_roots"])
	}
	root, ok := rootValues[0].(map[string]any)
	if !ok {
		t.Fatalf("stable watched root has invalid shape: %#v", rootValues[0])
	}
	commandValues, ok := root["agent_commands"].([]any)
	if !ok || len(commandValues) == 0 {
		t.Fatalf("stable watched root has no agent commands: %#v", root["agent_commands"])
	}
	command, ok := commandValues[0].(map[string]any)
	if !ok {
		t.Fatalf("stable agent command has invalid shape: %#v", commandValues[0])
	}
	if _, ok := command["shell"].(string); !ok {
		t.Fatalf("stable agent command shell has invalid shape: %#v", command["shell"])
	}
	if _, ok := command["command"].([]any); !ok {
		t.Fatalf("stable structured command has invalid shape: %#v", command["command"])
	}
	return command
}

func addRegistrationPlanFieldForTest(t *testing.T, input *projects.ProjectRepositoryValidatedSourceInput, key, value string) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(input.RegistrationPlan, &fields); err != nil {
		t.Fatal(err)
	}
	fields[key] = marshalJSONForTest(t, value)
	input.RegistrationPlan = marshalJSONForTest(t, fields)
}

func marshalJSONForTest(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return json.RawMessage(raw)
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func testProjectRepositorySourceInput() projects.ProjectRepositoryValidatedSourceInput {
	root := "/srv/loom/projects/example"
	return projects.ProjectRepositoryValidatedSourceInput{
		ProjectID:           testProjectID,
		ProjectRoot:         root,
		ProjectContractPath: root + "/.loom/project.yaml",
		ReposContractPath:   root + "/repos/loom.repos.yaml",
		OwnerNode:           "main",
		Versions: projects.ProjectRepositorySourceVersions{
			ProjectContract: projects.ProjectRepositoryProjectSchemaV04,
			ReposContract:   projects.ProjectRepositoryReposSchemaV04,
		},
		ProjectContractDigest: digestOf('a'),
		ReposContractDigest:   digestOf('b'),
		RegistrationPlan: json.RawMessage(`{
			"schema_version":"project.plan.v0.3",
			"generated_at":"2026-08-29T12:00:00Z",
			"project_root":"/srv/loom/projects/example",
			"contract_path":"/srv/loom/projects/example/.loom/project.yaml",
			"project":{"id":"project_01ARZ3NDEKTSV4RRFFQ69G5FAV","slug":"example","name":"Example","owner_node":"main","status":"active"},
			"repository_members":[{"id":"repo_01ARZ3NDEKTSV4RRFFQ69G5FAA","key":"backend","path":"backend","role":"primary"},{"id":"repo_01ARZ3NDEKTSV4RRFFQ69G5FAB","key":"portal","path":"portal","role":"component"}],
			"z_options":{"a":1,"absolute_path":"/srv/loom/projects/example/scripts/job","z":2}
		}`),
		Members: []projects.ProjectRepositoryValidatedMember{
			{RepositoryID: testRepoIDA, Key: "backend", Path: "backend", Role: projects.ProjectRepositoryRolePrimary, StateRoot: ".repo"},
			{RepositoryID: testRepoIDB, Key: "portal", Path: "portal", Role: projects.ProjectRepositoryRoleComponent},
		},
	}
}

func cloneProjectRepositorySourceInput(input projects.ProjectRepositoryValidatedSourceInput) projects.ProjectRepositoryValidatedSourceInput {
	cloned := input
	cloned.RegistrationPlan = append(json.RawMessage(nil), input.RegistrationPlan...)
	cloned.Members = append([]projects.ProjectRepositoryValidatedMember(nil), input.Members...)
	return cloned
}

func relocateProjectRepositorySourceInput(input *projects.ProjectRepositoryValidatedSourceInput, nextRoot string) {
	previousRoot := input.ProjectRoot
	input.ProjectRoot = nextRoot
	input.ProjectContractPath = strings.Replace(input.ProjectContractPath, previousRoot, nextRoot, 1)
	input.ReposContractPath = strings.Replace(input.ReposContractPath, previousRoot, nextRoot, 1)
	input.RegistrationPlan = json.RawMessage(strings.ReplaceAll(string(input.RegistrationPlan), previousRoot, nextRoot))
}

func mustBuildProjectRepositorySource(t *testing.T, input projects.ProjectRepositoryValidatedSourceInput) projects.ProjectRepositoryCanonicalSource {
	t.Helper()
	source, err := projects.BuildProjectRepositoryCanonicalSource(input)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func digestOf(value byte) string {
	return "sha256:" + strings.Repeat(string(value), 64)
}

func sortStringsAreStrictlyIncreasing(values []string) bool {
	for index := 1; index < len(values); index++ {
		if values[index-1] >= values[index] {
			return false
		}
	}
	return true
}

func cloneObservationForTest(observation projects.ProjectRepositoryObservation) projects.ProjectRepositoryObservation {
	cloned := observation
	cloned.Observation = append(json.RawMessage(nil), observation.Observation...)
	if observation.ObservedAt != nil {
		value := *observation.ObservedAt
		cloned.ObservedAt = &value
	}
	return cloned
}
