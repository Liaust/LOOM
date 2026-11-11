package projectcontracts

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDeclarationSourcesMatchExceptWorkState(t *testing.T) {
	root := declarationWebDAVRoot(t)
	file := filepath.Join(root, CanonicalRootContractPath)
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	before := Analyze(root)
	if !before.Report.OK {
		t.Fatal(before.Report.Diagnostics)
	}
	applied := before.Report.Declaration.Sources
	for _, state := range []string{"passive", "working", ""} {
		source := raw
		if state != "" {
			source = bytes.Replace(raw, []byte("project:\n"), []byte("project:\n  work_state: "+state+"\n"), 1)
		}
		if err := os.WriteFile(file, source, 0600); err != nil {
			t.Fatal(err)
		}
		current := Analyze(root)
		if !current.Report.OK {
			t.Fatal(current.Report.Diagnostics)
		}
		accepted := current.Report.Declaration.Sources
		if !DeclarationSourcesMatchExceptWorkState(accepted, applied) || !DeclarationSourcesMatchExceptWorkState(applied, accepted) {
			t.Fatal("classification-only source successor refused", state)
		}
		for i, prior := range applied {
			if prior.Ref == CanonicalRootContractPath && state != "" && prior.Hash == accepted[i].Hash {
				t.Fatal("classification source change hidden by hash normalization")
			}
		}
		applied = accepted
	}
}

func TestDeclarationWorkStateSourceEquivalenceRefusesDrift(t *testing.T) {
	root := declarationWebDAVRoot(t)
	a := Analyze(root)
	if !a.Report.OK {
		t.Fatal(a.Report.Diagnostics)
	}
	applied := a.Report.Declaration.Sources
	clone := func() []DeclarationSourceSnapshot {
		out := append([]DeclarationSourceSnapshot(nil), applied...)
		for i := range out {
			out[i].Raw = append([]byte(nil), out[i].Raw...)
		}
		return out
	}
	rootIndex := -1
	for i, source := range applied {
		if source.Ref == CanonicalRootContractPath {
			rootIndex = i
		}
	}
	if rootIndex < 0 {
		t.Fatal("missing fixture source")
	}
	for _, field := range []string{"status", "owner", "identity", "name", "path", "privacy", "processing", "schedule"} {
		t.Run(field, func(t *testing.T) {
			current := clone()
			d, err := ParseProjectDeclaration(current[rootIndex].Raw)
			if err != nil {
				t.Fatal(err)
			}
			d.Project.WorkState = "passive"
			switch field {
			case "status":
				d.Project.Status = "paused"
			case "owner":
				d.Project.OwnerNode = "other"
			case "identity":
				d.Project.ID = "project_01ARZ3NDEKTSV4RRFFQ69G5FAX"
			case "name":
				d.Project.Name += " changed"
			case "path", "privacy", "processing":
				for key, resource := range d.Resources {
					if resource.Knowledge == nil {
						continue
					}
					switch field {
					case "path":
						resource.Knowledge.Path += "-other"
					case "privacy":
						resource.Knowledge.Exclude = []string{"private/**"}
					case "processing":
						resource.Knowledge.Processing = &KnowledgeProcessingPolicy{OCR: "off"}
					}
					d.Resources[key] = resource
				}
			case "schedule":
				d.Resources["extra_schedule"] = ResourceDeclaration{Kind: DeclarationSchedule, Schedule: &ScheduleDeclaration{Target: "main@system.health.read", Every: "5m", Status: "disabled"}}
			}
			raw, err := yaml.Marshal(d)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseProjectDeclaration(raw); err != nil {
				t.Fatalf("drift fixture must remain a valid declaration: %v", err)
			}
			current[rootIndex] = declarationSnapshot(CanonicalRootContractPath, ProjectSchemaV05, raw)
			if DeclarationSourcesMatchExceptWorkState(current, applied) {
				t.Fatal("non-classification declaration drift admitted")
			}
		})
	}
	for _, change := range []string{"dependency", "missing", "extra", "duplicate", "raw", "revision", "schema", "reference", "root_schema", "invalid_state", "empty"} {
		t.Run(change, func(t *testing.T) {
			current := clone()
			switch change {
			case "dependency":
				i := (rootIndex + 1) % len(current)
				current[i] = declarationSnapshot(current[i].Ref, current[i].SchemaVersion, append(current[i].Raw, '\n'))
			case "missing":
				current = current[:len(current)-1]
			case "extra":
				current = append(current, declarationSnapshot(".loom/extra.yaml", "extra.v1", []byte("{}")))
			case "duplicate":
				current = append(current, current[0])
			case "raw":
				current[rootIndex].Raw = append(current[rootIndex].Raw, '\n')
			case "revision":
				current[rootIndex].Revision = "unbound"
			case "schema":
				current[(rootIndex+1)%len(current)].SchemaVersion = "changed.v1"
			case "reference":
				current[rootIndex].Ref = "../project.yaml"
			case "root_schema":
				current[rootIndex].SchemaVersion = ProjectSchemaV04
			case "invalid_state":
				raw := bytes.Replace(current[rootIndex].Raw, []byte("project:\n"), []byte("project:\n  work_state: archived\n"), 1)
				current[rootIndex] = declarationSnapshot(CanonicalRootContractPath, ProjectSchemaV05, raw)
			case "empty":
				current = nil
			}
			if DeclarationSourcesMatchExceptWorkState(current, applied) || DeclarationSourcesMatchExceptWorkState(applied, current) {
				t.Fatal("changed or unbound source closure admitted")
			}
		})
	}
}
