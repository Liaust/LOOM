package projects_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/ids"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectregistration"
	"loom.local/loom/internal/projects"
)

func declarationRegistrationInput(t *testing.T, root, id string, count int) projects.RegisterProjectContractInput {
	t.Helper()
	resources := map[string]any{}
	for i := 0; i < count; i++ {
		key := fmt.Sprintf("repo%d", i)
		if err := os.MkdirAll(filepath.Join(root, key), 0700); err != nil {
			t.Fatal(err)
		}
		resources[key] = map[string]any{"kind": "repository", "repository": map[string]any{"path": key, "role": "component"}}
	}
	raw, err := json.Marshal(map[string]any{"kind": "loom.project", "schema_version": pc.ProjectSchemaV05, "project": map[string]any{"id": id, "slug": "declaration-" + strings.ToLower(id[8:]), "name": "Declaration", "owner_node": "main", "status": "active"}, "resources": resources})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(root, ".loom"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, ".loom", "project.yaml"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	analysis := pc.Analyze(root)
	input, err := projectregistration.BuildInput(analysis, "declaration-owner-test")
	if err != nil {
		t.Fatalf("real compiler input: %v; report=%+v", err, analysis.Report)
	}
	return input
}

func TestDeclarationCompatibilityOwnerStateEvidence(t *testing.T) {
	root, id := t.TempDir(), ids.NewProjectID()
	_ = declarationRegistrationInput(t, root, id, 1)
	file := filepath.Join(root, ".loom/project.yaml")
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["resources"].(map[string]any)["repo0"].(map[string]any)["repository"].(map[string]any)["state_root"] = ".repo"
	raw, _ = json.Marshal(doc)
	if err = os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	a := pc.Analyze(root)
	input, err := projectregistration.BuildInput(a, "compatibility-owner")
	if err != nil {
		t.Fatalf("state_root compiler: %v %+v", err, a.Report.Diagnostics)
	}
	payload, err := projects.BuildDeclarationRegistrationPayload(input)
	if err != nil || len(payload.Repositories) != 1 || payload.Repositories[0].StateRoot != ".repo" {
		t.Fatalf("owner dropped state intent: %+v %v", payload, err)
	}
	mutate := func(raw json.RawMessage, member bool) json.RawMessage {
		var v map[string]any
		if e := json.Unmarshal(raw, &v); e != nil {
			t.Fatal(e)
		}
		if member {
			v["declaration"].(map[string]any)["repositories"].([]any)[0].(map[string]any)["state_root"] = "other-state"
		} else {
			v["resources"].(map[string]any)["repo0"].(map[string]any)["repository"].(map[string]any)["state_root"] = "other-state"
		}
		out, _ := json.Marshal(v)
		return out
	}
	for _, mode := range []string{"report", "plan", "both_compiler_sets", "source"} {
		t.Run(mode, func(t *testing.T) {
			changed := input
			if mode == "report" || mode == "both_compiler_sets" {
				changed.ValidationReport = mutate(input.ValidationReport, true)
			}
			if mode == "plan" || mode == "both_compiler_sets" {
				changed.RegistrationPlan = mutate(input.RegistrationPlan, true)
			}
			if mode == "source" {
				changed.Contract = mutate(input.Contract, false)
			}
			if _, e := projects.BuildDeclarationRegistrationPayload(changed); e == nil {
				t.Fatal("mismatched state intent accepted")
			}
		})
	}
}

func TestDeclarationRegistrationWholeSetPostgres(t *testing.T) {
	database, req := projectRepositoryTransactionDatabase(t)
	service := projects.NewService(database)
	ctx := context.Background()
	for count := 0; count <= 2; count++ {
		t.Run(fmt.Sprintf("%d_idless", count), func(t *testing.T) {
			root, id := t.TempDir(), ids.NewProjectID()
			input := declarationRegistrationInput(t, root, id, count)
			first, err := service.RegisterProjectContract(ctx, req, input)
			if err != nil {
				t.Fatal(err)
			}
			if first.RepositorySource == nil || first.RepositorySource.MemberCount != count || first.RepositorySource.SourceRevision != 1 {
				t.Fatalf("first=%+v", first)
			}
			var before string
			if err = database.QueryRow(`SELECT source_snapshot_json::text FROM projects.project_repository_sources WHERE project_id=$1`, id).Scan(&before); err != nil {
				t.Fatal(err)
			}
			second, err := service.RegisterProjectContract(ctx, req, input)
			if err != nil || !second.Unchanged || len(second.EventIDs) != 0 {
				t.Fatalf("replay=%+v err=%v", second, err)
			}
			var after string
			if err = database.QueryRow(`SELECT source_snapshot_json::text FROM projects.project_repository_sources WHERE project_id=$1`, id).Scan(&after); err != nil || before != after {
				t.Fatalf("identity changed: %v", err)
			}
			var versions, paths bool
			if err = database.QueryRow(`SELECT project_contract_schema_version='project.contract.v0.5' AND repos_contract_schema_version=project_contract_schema_version, project_contract_path=repos_contract_path AND project_contract_digest=repos_contract_digest FROM projects.project_repository_sources WHERE project_id=$1`, id).Scan(&versions, &paths); err != nil || !versions || !paths {
				t.Fatalf("single source: %v", err)
			}
			if count > 0 {
				if _, err = service.RegisterProjectContract(ctx, req, declarationRegistrationInput(t, root, id, 0)); err != nil {
					t.Fatal(err)
				}
				if _, err = service.RegisterProjectContract(ctx, req, input); err != nil {
					t.Fatal(err)
				}
				if err = database.QueryRow(`SELECT source_snapshot_json::text FROM projects.project_repository_sources WHERE project_id=$1`, id).Scan(&after); err != nil || before != after {
					t.Fatalf("retired key identity changed: %v", err)
				}
			}
		})
	}
}

func TestDeclarationRepositoryReadRegistrationBindingPostgres(t *testing.T) {
	database, req := projectRepositoryTransactionDatabase(t)
	service := projects.NewService(database)
	for _, count := range []int{0, 1, 2} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			root, id := t.TempDir(), ids.NewProjectID()
			input := declarationRegistrationInput(t, root, id, count)
			if _, err := service.RegisterProjectContract(t.Context(), req, input); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"registration_path", "registration_hash", "registration_project", "source_hash", "source_path"} {
				t.Run(name, func(t *testing.T) {
					tx, err := database.BeginTx(t.Context(), nil)
					if err != nil {
						t.Fatal(err)
					}
					// Commit one deliberate disposable corruption; restore its exact original
					// row from captured JSON after checking the public redacted read owner.
					table := "project_contract_registrations"
					field := "contract_path"
					value := root + "/.loom/forged.yaml"
					switch name {
					case "registration_hash":
						field = "contract_hash"
						value = "sha256:" + strings.Repeat("0", 64)
					case "registration_project":
						field = "project_root"
						value = root + "/foreign"
					case "source_hash":
						table = "project_repository_sources"
						field = "project_contract_digest"
						value = "sha256:" + strings.Repeat("0", 64)
					case "source_path":
						table = "project_repository_sources"
						field = "project_root"
						value = root + "/foreign"
					}
					var original string
					if err = tx.QueryRowContext(t.Context(), `SELECT `+field+` FROM projects.`+table+` WHERE project_id=$1`, id).Scan(&original); err != nil {
						t.Fatal(err)
					}
					clause := field + "=$2"
					if name == "source_hash" {
						clause = "project_contract_digest=$2,repos_contract_digest=$2"
					}
					if name == "source_path" {
						clause = "project_root=$2,project_contract_path=$2||'/.loom/project.yaml',repos_contract_path=$2||'/.loom/project.yaml'"
					}
					_, err = tx.ExecContext(t.Context(), `UPDATE projects.`+table+` SET `+clause+` WHERE project_id=$1`, id, value)
					if err != nil {
						_ = tx.Rollback()
						if table != "project_repository_sources" {
							t.Fatal(err)
						}
						return
					}
					if err = tx.Commit(); err != nil {
						t.Fatal(err)
					}
					_, readErr := service.ReadProjectRepositoryState(t.Context(), id)
					if _, err = database.Exec(`UPDATE projects.`+table+` SET `+clause+` WHERE project_id=$1`, id, original); err != nil {
						t.Fatal(err)
					}
					if readErr == nil {
						t.Fatal("substituted evidence escaped into redacted owner view")
					}
					if _, err = service.ReadProjectRepositoryState(t.Context(), id); err != nil {
						t.Fatalf("restored source: %v", err)
					}
				})
			}
			model, err := service.ReadProjectRepositoryState(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(model.Source)
			for _, field := range []string{"project_contract_path", "repos_contract_path", "project_contract_digest", "repos_contract_digest"} {
				if strings.Contains(string(encoded), field) {
					t.Fatal("new private evidence exposed in public source DTO")
				}
			}
			if count == 0 {
				var original []byte
				if err = database.QueryRow(`SELECT to_jsonb(s) FROM projects.project_repository_sources s WHERE project_id=$1`, id).Scan(&original); err != nil {
					t.Fatal(err)
				}
				if _, err = database.Exec(`DELETE FROM projects.project_repository_sources WHERE project_id=$1`, id); err != nil {
					t.Fatal(err)
				}
				_, missingErr := service.ReadProjectRepositoryState(t.Context(), id)
				if _, err = database.Exec(`INSERT INTO projects.project_repository_sources SELECT * FROM jsonb_populate_record(NULL::projects.project_repository_sources,$1::jsonb)`, original); err != nil {
					t.Fatal(err)
				}
				if missingErr == nil {
					t.Fatal("zero-member declaration silently lost its source")
				}
			}

		})
	}
}

func TestDeclarationLegacyReferenceDirectGuard(t *testing.T) {
	for _, scenario := range []string{"ordinary_flags", "nil_source", "misleading_schema"} {
		t.Run(scenario, func(t *testing.T) {
			input := declarationRegistrationInput(t, t.TempDir(), "project_01ARZ3NDEKTSV4RRFFQ69G5FAV", 0)
			var document map[string]any
			if err := json.Unmarshal(input.Contract, &document); err != nil {
				t.Fatal(err)
			}
			document["legacy_contracts"] = map[string]any{"project": map[string]any{"ref": ".loom/contracts/retained-project-v04.yaml", "schema_version": pc.ProjectSchemaV04, "digest": "sha256:" + strings.Repeat("a", 64)}}
			raw, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			input.Contract = raw
			if scenario != "ordinary_flags" {
				input.RepositorySource = nil
			}
			if scenario == "misleading_schema" {
				input.ContractSchemaVersion = pc.ProjectSchemaV04
			}
			_, err = projects.BuildDeclarationRegistrationPayload(input)
			if err == nil || !strings.Contains(err.Error(), "legacy_owner_transition_required") {
				t.Fatalf("actual compatibility source bypassed guard: %v", err)
			}
		})
	}
}

func TestDeclarationLegacyPredecessorRepresentation(t *testing.T) {
	p := projects.DeclarationLegacyPredecessor{SchemaVersion: projects.DeclarationLegacyPredecessorSchema, ProjectID: ids.NewProjectID(), RegistrationID: ids.NewProjectContractRegistrationID(), RegistrationRevision: 1, NodeID: ids.NewNodeID(), NodeKey: "main", ProjectRoot: "/fixture/project", PhysicalRoot: "/fixture/project", ContractPath: "/fixture/project/.loom/project.yaml", ContractHash: "sha256:" + strings.Repeat("a", 64), ContractSchema: pc.ProjectSchemaV04, Members: []projects.DeclarationLegacyMember{}}
	p.Contract, _ = json.Marshal(map[string]any{"kind": "loom.project", "schema_version": pc.ProjectSchemaV04, "project": map[string]string{"id": p.ProjectID, "owner_node": p.NodeKey}})
	seal := func(value *projects.DeclarationLegacyPredecessor) {
		value.Digest = ""
		raw, _ := json.Marshal(value)
		decoded, err := pc.DecodeDeclarationEvidenceJSON(raw)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ = json.Marshal(decoded)
		value.Digest = fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	}
	seal(&p)
	if err := projects.ValidateDeclarationLegacyPredecessor(&p); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"foreign_schema", "project_id", "registration_id", "node_id", "revision", "relative_root", "unclean_root", "contract_path", "contract_hash", "schema", "nil_members", "source_identity", "digest"} {
		t.Run(scenario, func(t *testing.T) {
			value := p
			switch scenario {
			case "foreign_schema":
				value.SchemaVersion = "unknown"
			case "project_id":
				value.ProjectID = "project_other"
			case "registration_id":
				value.RegistrationID = "registration_other"
			case "node_id":
				value.NodeID = "node_other"
			case "revision":
				value.RegistrationRevision = 0
			case "relative_root":
				value.PhysicalRoot = "relative"
			case "unclean_root":
				value.PhysicalRoot = "/fixture/../project"
			case "contract_path":
				value.ContractPath = "/another/project.yaml"
			case "contract_hash":
				value.ContractHash = "bad"
			case "schema":
				value.ContractSchema = pc.ProjectSchemaV05
			case "nil_members":
				value.Members = nil
			case "source_identity":
				value.Contract = []byte(strings.Replace(string(value.Contract), "main", "foreign", 1))
			case "digest":
				value.Digest = "sha256:" + strings.Repeat("f", 64)
			}
			if scenario != "digest" {
				seal(&value)
			}
			if projects.ValidateDeclarationLegacyPredecessor(&value) == nil {
				t.Fatal("invalid predecessor representation accepted")
			}
		})
	}
	// A well-formed historical representation still cannot enter the direct route.
	input := declarationRegistrationInput(t, t.TempDir(), p.ProjectID, 0)
	if _, err := projects.BuildDeclarationLegacyRegistrationIntent(input, &p); err == nil {
		t.Fatal("ordinary source turned into legacy intent")
	}
}
