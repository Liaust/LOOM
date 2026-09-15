package knowledge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/projectcontracts"
)

func declarationKnowledgeAnalysis(t *testing.T) projectcontracts.Analysis {
	t.Helper()
	root := t.TempDir()
	for _, folder := range []string{".loom", "journal", "notes", "repos", "unrelated"} {
		if err := os.MkdirAll(filepath.Join(root, folder), 0700); err != nil {
			t.Fatal(err)
		}
	}
	source := "kind: loom.project\nschema_version: project.contract.v0.5\nproject:\n  id: " + ids.NewProjectID() + "\n  slug: declaration-proof\n  name: Declaration proof\n  owner_node: main\nresources:\n  reading:\n    kind: knowledge\n    knowledge: {path: journal, category: notes}\n"
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
