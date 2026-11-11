package projectregistration

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/projectcontracts"
)

func TestDeclarationReaderV06PreservesExplicitRootKnowledge(t *testing.T) {
	for _, resources := range []string{"{}", "{files: {kind: knowledge, knowledge: {path: '.', category: notes}}}"} {
		t.Run(resources, func(t *testing.T) {
			root := t.TempDir()
			writeMapperFixture(t, filepath.Join(root, ".loom/project.yaml"), "kind: loom.project\nschema_version: project.contract.v0.6\nproject: {id: "+mapperProjectID+", slug: files-reader, name: Files reader, owner_node: main}\nfiles:\n  rules:\n    - {path: private, search: none, protect: false, enrichment: []}\nresources: "+resources+"\n")
			a := requireMapperAnalysis(t, root)
			input, err := BuildInput(a, "v06-reader.test")
			if err != nil {
				t.Fatal(err)
			}
			var d projectcontracts.ProjectDeclaration
			if err := json.Unmarshal(input.Contract, &d); err != nil {
				t.Fatal(err)
			}
			wantCount := 1
			if resources == "{}" {
				wantCount = 0
			}
			if d.SchemaVersion != projectcontracts.ProjectSchemaV06 || input.ContractSchemaVersion != d.SchemaVersion || d.Files == nil || len(d.Resources) != wantCount || input.RepositorySource != nil {
				t.Fatalf("source schema, policy or declared resources changed: %+v", d)
			}
			parsed, err := projectcontracts.ParseProjectDeclaration(a.Loaded.Raw)
			if err != nil || !mapperEvidenceEqual(d, parsed) {
				t.Fatalf("mapped document differs from actual source: %v", err)
			}
			if wantCount == 1 && (d.Resources["files"].Knowledge == nil || d.Resources["files"].Knowledge.Path != ".") {
				t.Fatal("explicit root knowledge resource lost")
			}
			a.Loaded.Contract.SchemaVersion = projectcontracts.ProjectSchemaV05
			if _, err := BuildInput(a, "v06-reader.test"); err == nil {
				t.Fatal("v0.6 source claimed v0.5 identity")
			}
		})
	}
}

func TestDeclarationReaderV06ExactSourceClosure(t *testing.T) {
	for _, scenario := range []string{"valid", "missing", "unexpected", "duplicate", "old_root_schema", "different_root_bytes", "noncanonical_root"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			writeMapperFixture(t, filepath.Join(root, ".loom/project.yaml"), "kind: loom.project\nschema_version: project.contract.v0.6\nproject: {id: "+mapperProjectID+", slug: agent-reader, name: Agent reader, owner_node: main}\nresources:\n  helper:\n    kind: agent\n    agent: {instructions_ref: agent.md, model: 'gemma3:4b', provider: ollama}\n")
			writeMapperFixture(t, filepath.Join(root, "agent.md"), "Read this project only.\n")
			a := requireMapperAnalysis(t, root)
			rootIndex, agentIndex := -1, -1
			for i, snapshot := range a.Plan.Declaration.Sources {
				if snapshot.Ref == projectcontracts.CanonicalRootContractPath {
					rootIndex = i
				} else if snapshot.Ref == "agent.md" {
					agentIndex = i
				}
			}
			if rootIndex < 0 || agentIndex < 0 {
				t.Fatal("actual compiler source closure missing")
			}
			switch scenario {
			case "missing":
				a.Plan.Declaration.Sources = append(a.Plan.Declaration.Sources[:agentIndex], a.Plan.Declaration.Sources[agentIndex+1:]...)
			case "unexpected":
				a.Plan.Declaration.Sources[agentIndex].Ref = "metadata.json"
			case "duplicate":
				a.Plan.Declaration.Sources = append(a.Plan.Declaration.Sources, a.Plan.Declaration.Sources[rootIndex])
			case "old_root_schema":
				a.Plan.Declaration.Sources[rootIndex].SchemaVersion = projectcontracts.ProjectSchemaV05
			case "different_root_bytes":
				changed := bytes.Replace(a.Loaded.Raw, []byte("Agent reader"), []byte("Other reader"), 1)
				snapshot := &a.Plan.Declaration.Sources[rootIndex]
				snapshot.Raw = changed
				snapshot.Hash = fmt.Sprintf("sha256:%x", sha256.Sum256(changed))
				snapshot.Revision = snapshot.Hash
			case "noncanonical_root":
				a.Loaded.ContractPath = filepath.Join(root, ".loom/contracts/project.yaml")
				a.Report.ContractPath, a.Plan.ContractPath = a.Loaded.ContractPath, a.Loaded.ContractPath
			}
			a.Report.Declaration.Sources = a.Plan.Declaration.Sources
			_, err := BuildInput(a, "v06-closure.test")
			if (err == nil) != (scenario == "valid") {
				t.Fatalf("scenario=%s err=%v", scenario, err)
			}
		})
	}
}

func TestDeclarationReaderV06RejectsRootRepository(t *testing.T) {
	root := t.TempDir()
	writeMapperFixture(t, filepath.Join(root, ".loom/project.yaml"), "kind: loom.project\nschema_version: project.contract.v0.6\nproject: {id: "+mapperProjectID+", slug: root-repository, name: Root repository, owner_node: main}\nresources: {code: {kind: repository, repository: {path: '.', role: primary}}}\n")
	if _, err := BuildInput(projectcontracts.Analyze(root), "v06-reader.test"); err == nil {
		t.Fatal("root repository accepted as root knowledge")
	}
}
