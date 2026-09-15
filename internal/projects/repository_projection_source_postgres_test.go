package projects_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"loom.local/loom/internal/ids"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectregistration"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectstate"
	"loom.local/loom/internal/repostate"
)

func TestDeclarationRepositoryProjectionSourcePostgres(t *testing.T) {
	database, req := projectRepositoryTransactionDatabase(t)
	service := projects.NewService(database)
	versions := projects.SupportedProjectRepositorySourceVersions()
	for _, pair := range versions {
		paths := []string{"api"}
		if pair.ProjectContract == projects.ProjectRepositoryProjectSchemaV05 {
			paths = []string{"custom-code", "repos/api"}
		}
		for _, memberPath := range paths {
			for _, legacyRef := range []string{".loom/contracts/repos.yaml", "repos/loom.repos.yaml"} {
				t.Run(pair.ProjectContract+"/"+pair.ReposContract+"/"+memberPath+"/"+legacyRef, func(t *testing.T) {
					root := t.TempDir()
					var input projects.RegisterProjectContractInput
					var navigation, sourceRef string
					if pair.ProjectContract == projects.ProjectRepositoryProjectSchemaV05 {
						input = declarationRegistrationInput(t, root, ids.NewProjectID(), 0)
						var document map[string]any
						if err := json.Unmarshal(input.Contract, &document); err != nil {
							t.Fatal(err)
						}
						document["resources"] = map[string]any{"api": map[string]any{"kind": "repository", "repository": map[string]any{"path": memberPath, "role": "component"}}}
						navigation, sourceRef = memberPath, ".loom/project.yaml"
						projectionFixtureGit(t, root, navigation)
						projectionFixtureWrite(t, input.ContractPath, marshalJSON(t, document))
						var err error
						input, err = projectregistration.BuildInput(pc.Analyze(root), "source-fidelity-test")
						if err != nil {
							t.Fatal(err)
						}
					} else {
						fixture := newRepositoryRegistrationFixture("source-fidelity", pair.ProjectContract, pair.ReposContract, []repositoryMemberFixture{{ID: newRepositoryID(), Key: "api", Path: memberPath, Role: "component"}})
						fixture.ProjectRoot, fixture.ProjectPath, fixture.ReposPath = root, filepath.Join(root, ".loom/project.yaml"), filepath.Join(root, legacyRef)
						input = repositoryRegistrationInput(t, fixture)
						navigation, sourceRef = "repos/"+memberPath, legacyRef
						projectionFixtureGit(t, root, navigation)
						projectionFixtureWrite(t, fixture.ProjectPath, input.Contract)
						projectionFixtureWrite(t, fixture.ReposPath, marshalJSON(t, map[string]any{"kind": "loom.repos", "schema_version": pair.ReposContract, "repos": map[string]any{"members": fixture.Members}}))
					}
					if _, err := service.RegisterProjectContract(t.Context(), req, input); err != nil {
						t.Fatal(err)
					}
					state, err := service.ReadProjectRepositoryState(t.Context(), input.Project.Slug)
					if err != nil {
						t.Fatal(err)
					}
					if state.Source == nil || state.Source.ProjectContractPath != input.ContractPath || state.Source.ReposContractPath != filepath.Join(root, sourceRef) {
						t.Fatalf("registered source locations not propagated: %#v", state.Source)
					}
					projectID := state.Project.ProjectID
					beforeDB := projectionRegistrySnapshot(t, database, projectID)
					beforeFiles := projectionFileSnapshot(t, root)
					observer := projectstate.Service{Reader: service, LocalNode: "main"}
					for repeat := 0; repeat < 2; repeat++ {
						source, err := observer.ObserveProjectForProvenance(t.Context(), projectID)
						if err != nil || len(source.Repositories) != 1 {
							t.Fatalf("source=%#v err=%v", source, err)
						}
						member := source.Repositories[0]
						physicalRoot, err := filepath.EvalSymlinks(filepath.Join(root, navigation))
						if err != nil {
							t.Fatal(err)
						}
						if !member.Owned || !member.Available || member.RepositoryRoot != physicalRoot || member.NavigationPath != navigation || member.MembershipSourcePath != sourceRef || member.Projection.RelativeSource != memberPath || member.SourceVersion != state.Members[0].ObservationRevision || member.Projection.SourceBindingDigest != state.Members[0].SourceBindingDigest || member.Projection.ObservationPosture != projects.ProjectRepositoryObservationObserved {
							t.Fatalf("source fidelity mismatch: %#v", member)
						}
						projected := (repostate.ProvenanceAdapter{}).ProjectForProvenance(t.Context(), member)
						if !projected.Available || projected.Extraction.TrackingStatus != repostate.TrackingNotEnabled {
							t.Fatalf("optional .repo posture changed: %#v", projected)
						}
						found := false
						for _, field := range projected.Extraction.DiscoveryFields {
							if field.Name == "navigation_path" {
								found = true
								if field.Value != navigation || field.Source.Path != sourceRef || field.Source.Digest != state.Members[0].SourceBindingDigest {
									t.Fatalf("actual extractor attribution=%#v", field)
								}
							}
						}
						if !found {
							t.Fatal("actual extractor omitted navigation")
						}
						for _, portable := range []any{state, source.Projection, projected.Extraction} {
							payload, err := json.Marshal(portable)
							if err != nil || strings.Contains(string(payload), root) || strings.Contains(string(payload), physicalRoot) {
								t.Fatalf("portable JSON leaked root: %s err=%v", payload, err)
							}
						}
					}
					if after := projectionRegistrySnapshot(t, database, projectID); !reflect.DeepEqual(beforeDB, after) {
						t.Fatalf("observation mutated registry/history/events: before=%v after=%v", beforeDB, after)
					}
					if after := projectionFileSnapshot(t, root); !reflect.DeepEqual(beforeFiles, after) {
						t.Fatal("observation mutated source/Git or created .repo")
					}
					// Corruption lives only in this newly created disposable fixture row.
					columns := []string{"project_contract_path", "repos_contract_path"}
					if pair.ProjectContract == projects.ProjectRepositoryProjectSchemaV05 {
						columns = []string{"project_root"}
					}
					for _, column := range columns {
						var original string
						if err := database.QueryRow(`SELECT `+column+` FROM projects.project_repository_sources WHERE project_id=$1`, projectID).Scan(&original); err != nil {
							t.Fatal(err)
						}
						assignment := column + "=$2"
						if pair.ProjectContract == projects.ProjectRepositoryProjectSchemaV05 {
							// Preserve the canonical single-source shape while making
							// its location disagree with the accepted snapshot/registration.
							assignment = "project_root=$2,project_contract_path=$2||'/.loom/project.yaml',repos_contract_path=$2||'/.loom/project.yaml'"
						}
						if _, err := database.Exec(`UPDATE projects.project_repository_sources SET `+assignment+` WHERE project_id=$1`, projectID, root+"/outside-binding.yaml"); err != nil {
							t.Fatal(err)
						}
						_, readErr := observer.ObserveProjectForProvenance(t.Context(), projectID)
						if _, err := database.Exec(`UPDATE projects.project_repository_sources SET `+assignment+` WHERE project_id=$1`, projectID, original); err != nil {
							t.Fatal(err)
						}
						if readErr == nil {
							t.Fatal("corrupt source binding accepted")
						}
					}
				})
			}
		}
	}
}

func projectionFixtureWrite(t *testing.T, path string, contents []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0600); err != nil {
		t.Fatal(err)
	}
}

func projectionFixtureGit(t *testing.T, root, navigation string) {
	t.Helper()
	repository := filepath.Join(root, navigation)
	if err := os.MkdirAll(repository, 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init"}, {"config", "user.name", "Fixture"}, {"config", "user.email", "fixture@example.invalid"}} {
		projectionFixtureRunGit(t, repository, args...)
	}
	projectionFixtureWrite(t, filepath.Join(repository, "README.md"), []byte("Source fidelity fixture\n"))
	projectionFixtureRunGit(t, repository, "add", "README.md")
	projectionFixtureRunGit(t, repository, "commit", "-m", "Fixture")
}

func projectionFixtureRunGit(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_OPTIONAL_LOCKS=0")
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fixture Git: %v: %s", err, out)
	}
}

func projectionFileSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		value := info.Mode().String()
		if info.Mode().IsRegular() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += fmt.Sprintf(":%x", sha256.Sum256(raw))
		}
		result[relative] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func projectionRegistrySnapshot(t *testing.T, database *sql.DB, projectID string) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, table := range []string{"project_contract_registrations", "project_repository_sources", "project_repository_memberships", "project_repository_observations", "project_repository_source_history"} {
		var value string
		if err := database.QueryRowContext(context.Background(), `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM projects.`+table+` t WHERE project_id=$1`, projectID).Scan(&value); err != nil {
			t.Fatal(err)
		}
		result[table] = value
	}
	var events string
	if err := database.QueryRow(`SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM events.events t`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	result["events"] = events
	return result
}
