package knowledge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/projectcontracts"
)

func declarationKnowledgeAnalysis(t *testing.T, policy ...string) projectcontracts.Analysis {
	t.Helper()
	root := t.TempDir()
	for _, folder := range []string{".loom", "journal", "notes", "repos", "unrelated"} {
		if err := os.MkdirAll(filepath.Join(root, folder), 0700); err != nil {
			t.Fatal(err)
		}
	}
	source := "kind: loom.project\nschema_version: project.contract.v0.5\nproject:\n  id: " + ids.NewProjectID() + "\n  slug: declaration-proof\n  name: Declaration proof\n  owner_node: main\nresources:\n  reading:\n    kind: knowledge\n    knowledge: {path: journal, category: notes}\n"
	if len(policy) > 0 {
		source = strings.Replace(source, "category: notes}", "category: notes, "+policy[0]+"}", 1)
	}
	if err := os.WriteFile(filepath.Join(root, ".loom/project.yaml"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	a := projectcontracts.Analyze(root)
	if !a.Report.OK || len(a.Report.WatchedRoots) != 1 {
		t.Fatalf("analysis: %+v", a.Report.Diagnostics)
	}
	return a
}
func declarationKnowledgeRegistration(t *testing.T, a projectcontracts.Analysis) ProjectWatchedRootRegistration {
	t.Helper()
	item := a.Report.WatchedRoots[0]
	return ProjectWatchedRootRegistration{ProjectWatchedRootRegistrationID: ids.NewProjectWatchedRootRegistrationID(), ProjectContractRegistrationID: ids.NewProjectContractRegistrationID(), ProjectID: a.Report.Project.ID, ProjectRoot: a.Report.ProjectRoot, NodeID: ids.NewNodeID(), OwnerNodeKey: item.OwnerNode, LocalRootKey: item.Key, BackendRootKey: item.BackendRootKey, RootRelativePath: item.RootRelativePath, SourceKinds: mustJSON(t, item.SourceKinds), Metadata: mustJSON(t, item.Metadata), ActivationStatus: "reported"}
}
func TestDeclarationEnrollmentConsumerIdentity(t *testing.T) {
	a := declarationKnowledgeAnalysis(t)
	reg := declarationKnowledgeRegistration(t, a)
	s := NewService(nil)
	for activation, status := range map[string]string{"registered": "blocked", "pending_agent_apply": "blocked", "applied": "blocked", "reported": "active", "disabled": "disabled", "stale": "stale", "blocked": "blocked"} {
		reg.ActivationStatus = activation
		roots, skip := s.BuildSourceRootCandidates(SourceRootReconcileInput{ProjectRegistrations: []ProjectWatchedRootRegistration{reg}})
		if len(skip) != 0 || len(roots) != 1 || roots[0].Root.Status != status || roots[0].Root.SourcePath != filepath.Join(a.Report.ProjectRoot, "journal") {
			t.Fatalf("%s: %+v %+v", activation, roots, skip)
		}
	}
	reg.ActivationStatus = "reported"
	for name, mutate := range map[string]func(*ProjectWatchedRootRegistration){
		"path":    func(r *ProjectWatchedRootRegistration) { r.RootRelativePath = "unrelated" },
		"project": func(r *ProjectWatchedRootRegistration) { r.ProjectID = ids.NewProjectID() },
		"root":    func(r *ProjectWatchedRootRegistration) { r.ProjectRoot = filepath.Join(r.ProjectRoot, "other") },
		"owner":   func(r *ProjectWatchedRootRegistration) { r.OwnerNodeKey = "other" },
		"key":     func(r *ProjectWatchedRootRegistration) { r.LocalRootKey = "other" },
		"backend": func(r *ProjectWatchedRootRegistration) { r.BackendRootKey = "other" },
		"downgrade": func(r *ProjectWatchedRootRegistration) {
			r.LocalRootKey = "notes"
			r.SourceKinds = mustJSON(t, []string{"notes_contract"})
		},
		"duplicate": func(r *ProjectWatchedRootRegistration) {
			r.Metadata = []byte(strings.Replace(string(r.Metadata), `"enabled":true`, `"enabled":false,"enabled":true`, 1))
		},
		"source_hash": func(r *ProjectWatchedRootRegistration) {
			m := jsonObject(r.Metadata)
			m["knowledge_source"].(map[string]any)["source_hash"] = "sha256:" + strings.Repeat("0", 64)
			r.Metadata = mustJSON(t, m)
		},
		"schema": func(r *ProjectWatchedRootRegistration) {
			m := jsonObject(r.Metadata)
			m["knowledge_source"].(map[string]any)["schema_version"] = "project.contract.v0.6"
			r.Metadata = mustJSON(t, m)
		},
		"unversioned": func(r *ProjectWatchedRootRegistration) {
			m := jsonObject(r.Metadata)
			delete(m["knowledge_source"].(map[string]any), "schema_version")
			r.Metadata = mustJSON(t, m)
		},
		"unknown": func(r *ProjectWatchedRootRegistration) {
			m := jsonObject(r.Metadata)
			m["knowledge_source"].(map[string]any)["extra"] = true
			r.Metadata = mustJSON(t, m)
		},
		"category": func(r *ProjectWatchedRootRegistration) {
			m := jsonObject(r.Metadata)
			m["knowledge_source"].(map[string]any)["declaration"] = "docs"
			r.Metadata = mustJSON(t, m)
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := reg
			mutate(&changed)
			roots, _ := s.BuildSourceRootCandidates(SourceRootReconcileInput{ProjectRegistrations: []ProjectWatchedRootRegistration{changed}})
			for _, r := range roots {
				if r.Root.Status == SourceRootStatusActive {
					t.Fatal("substitution active")
				}
			}
		})
	}
}

func declarationKnowledgeV06Analysis(t *testing.T, rootPath bool) projectcontracts.Analysis {
	t.Helper()
	a := declarationKnowledgeAnalysis(t)
	file := filepath.Join(a.Report.ProjectRoot, projectcontracts.CanonicalRootContractPath)
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	source := strings.Replace(string(raw), projectcontracts.ProjectSchemaV05, projectcontracts.ProjectSchemaV06, 1)
	if rootPath {
		source = strings.Replace(source, "path: journal", "path: .", 1)
	}
	source += "files:\n  rules:\n    - {path: journal/reference, search: metadata, enrichment: []}\n"
	if err := os.WriteFile(file, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	a = projectcontracts.Analyze(a.Report.ProjectRoot)
	if !a.Report.OK || len(a.Report.WatchedRoots) != 1 {
		t.Fatalf("v0.6 analysis: %+v", a.Report.Diagnostics)
	}
	return a
}

func TestDeclarationEnrollmentV06Evidence(t *testing.T) {
	for _, rootPath := range []bool{false, true} {
		a := declarationKnowledgeV06Analysis(t, rootPath)
		reg := declarationKnowledgeRegistration(t, a)
		if !declarationRegistrationMatches(reg) {
			t.Fatalf("v0.6 registration rejected: %s", reg.Metadata)
		}
		roots, skipped := NewService(nil).BuildSourceRootCandidates(SourceRootReconcileInput{ProjectRegistrations: []ProjectWatchedRootRegistration{reg}})
		if len(skipped) != 0 || len(roots) != 1 || roots[0].Root.Status != SourceRootStatusActive {
			t.Fatalf("v0.6 source not admitted: %+v %+v", roots, skipped)
		}
		if rootPath && roots[0].Root.SourcePath != a.Report.ProjectRoot {
			t.Fatalf("root path not canonical: %s", roots[0].Root.SourcePath)
		}
		workspace, err := independentWorkspacePolicy(roots[0].Root)
		if err != nil || workspace == nil || len(workspace.Include) == 0 {
			t.Fatalf("v0.6 independent workspace unavailable: %+v %v", workspace, err)
		}
		for name, mutate := range map[string]func(map[string]any){
			"metadata_version": func(m map[string]any) {
				m["knowledge_source"].(map[string]any)["schema_version"] = projectcontracts.ProjectSchemaV05
			},
			"snapshot_version": func(m map[string]any) {
				m["declaration_sources"].([]any)[0].(map[string]any)["schema_version"] = projectcontracts.ProjectSchemaV05
			},
			"snapshot_hash": func(m map[string]any) {
				m["declaration_sources"].([]any)[0].(map[string]any)["hash"] = "sha256:" + strings.Repeat("0", 64)
			},
			"policy_omitted":           func(m map[string]any) { delete(m["knowledge_source"].(map[string]any), "policy") },
			"policy_replaced":          func(m map[string]any) { m["knowledge_source"].(map[string]any)["policy"] = map[string]any{} },
			"include_replaced":         func(m map[string]any) { m["knowledge_source"].(map[string]any)["include"] = []string{"**/*.md"} },
			"exclude_omitted_defaults": func(m map[string]any) { m["knowledge_source"].(map[string]any)["exclude"] = []string{} },
			"duplicate_source": func(m map[string]any) {
				m["declaration_sources"] = append(m["declaration_sources"].([]any), m["declaration_sources"].([]any)[0])
			},
		} {
			t.Run(name, func(t *testing.T) {
				m := jsonObject(reg.Metadata)
				mutate(m)
				if _, valid := declarationKnowledgeMetadata(mustJSON(t, m)); valid {
					t.Fatal("substituted declaration evidence accepted")
				}
			})
		}
	}
	// Relabeling both metadata and snapshot must not relabel the captured bytes.
	a := declarationKnowledgeAnalysis(t)
	reg := declarationKnowledgeRegistration(t, a)
	m := jsonObject(reg.Metadata)
	m["knowledge_source"].(map[string]any)["schema_version"] = projectcontracts.ProjectSchemaV06
	m["declaration_sources"].([]any)[0].(map[string]any)["schema_version"] = projectcontracts.ProjectSchemaV06
	if _, valid := declarationKnowledgeMetadata(mustJSON(t, m)); valid {
		t.Fatal("v0.5 bytes relabeled as v0.6")
	}
}

func TestDeclarationEnrollmentV06IndexSelectorDefaults(t *testing.T) {
	for _, exclusions := range []string{"exclude: []", "include: ['papers/**'], exclude: ['papers/generated/**']"} {
		a := declarationKnowledgeV06Analysis(t, false)
		file := filepath.Join(a.Report.ProjectRoot, projectcontracts.CanonicalRootContractPath)
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		raw = []byte(strings.Replace(string(raw), "category: notes}", "category: notes, "+exclusions+"}", 1))
		if err := os.WriteFile(file, raw, 0600); err != nil {
			t.Fatal(err)
		}
		a = projectcontracts.Analyze(a.Report.ProjectRoot)
		if !a.Report.OK || len(a.Report.WatchedRoots) != 1 {
			t.Fatalf("explicit index selectors: %+v", a.Report.Diagnostics)
		}
		reg := declarationKnowledgeRegistration(t, a)
		if !declarationRegistrationMatches(reg) {
			t.Fatalf("explicit selectors rejected: %s", reg.Metadata)
		}
		if exclusions == "exclude: []" {
			source, _ := declarationKnowledgeMetadata(reg.Metadata)
			if len(source["exclude"].([]any)) != 0 {
				t.Fatal("explicit empty exclusions acquired defaults")
			}
		}
	}
}

func TestDeclarationEnrollmentVersionSQLFences(t *testing.T) {
	query := declarationRegistrationAuthoritySQL("r", "false")
	for _, fence := range []string{
		"accepted.contract_schema_version IN ('project.contract.v0.5','project.contract.v0.6')",
		"accepted.contract_json->>'schema_version'=accepted.contract_schema_version",
		"expected.value#>>'{metadata,knowledge_source,schema_version}'=accepted.contract_schema_version",
		"declared.root_relative_path='.' THEN ''",
	} {
		if !strings.Contains(query, fence) {
			t.Fatalf("missing version/path fence: %s", fence)
		}
	}
	if !strings.Contains(declarationUnversionedRootSQL("r"), "contract_schema_version IN ('project.contract.v0.5','project.contract.v0.6')") {
		t.Fatal("v0.6 declaration can downgrade to unversioned membership")
	}
	statement := notesStatementEnrollmentSQL("r", "o", true)
	if !strings.Contains(statement, "o.metadata#>>'{source_root,knowledge_source,schema_version}'=r.statement_declaration->>'schema_version'") ||
		!strings.Contains(statement, "r.statement_declaration#>>'{source,schema_version}'=r.statement_declaration->>'schema_version'") {
		t.Fatal("statement reader can mix source versions")
	}
}

func TestSourceAuditV06RevisionPreservesOnlyMatchingVersions(t *testing.T) {
	node, project := "node", "project"
	root := SourceRoot{NotesSourceRootID: "root", RootKind: RootKindProjectMaterial, Status: SourceRootStatusActive,
		NodeID: &node, NodeKey: "main", ProjectID: &project, BackendRootKey: "key", SourcePath: "/project", RootRelativePath: "."}
	metadata := func(version, hash string) json.RawMessage {
		return mustJSON(t, map[string]any{"registration_metadata": map[string]any{"knowledge_source": map[string]any{
			"schema_version": version, "source_hash": "sha256:" + strings.Repeat(hash, 64), "include": []string{"**/*"},
		}}})
	}
	root.Metadata = metadata(projectcontracts.ProjectSchemaV06, "a")
	next := root
	next.Metadata = metadata(projectcontracts.ProjectSchemaV06, "b")
	if !sourceAuditRefreshEligible(root, next) {
		t.Fatal("v0.6 audit-only update restarted source admission")
	}
	before, _ := admissionSourceGeneration([]SourceRoot{root})
	after, _ := admissionSourceGeneration([]SourceRoot{next})
	if before != after {
		t.Fatal("v0.6 audit-only update changed admission generation")
	}
	next.Metadata = metadata(projectcontracts.ProjectSchemaV05, "b")
	if sourceAuditRefreshEligible(root, next) {
		t.Fatal("mixed declaration versions were treated as audit-only")
	}
}
