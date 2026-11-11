package projectcontracts

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestProjectWorkStateValidationAndRoundTrip(t *testing.T) {
	for _, version := range []string{ProjectSchemaV03, ProjectSchemaV04, ProjectSchemaV05} {
		for _, state := range []string{"", "working", "passive", "archived", "paused", "PASSIVE", " passive", "''", "null", "[]"} {
			t.Run(version+"/"+state, func(t *testing.T) {
				source := "kind: loom.project\nschema_version: " + version + "\nproject:\n  slug: work-state\n  name: Work state\n  owner_node: main\n  status: active\n"
				if version != ProjectSchemaV03 {
					source += "  id: project_01ARZ3NDEKTSV4RRFFQ69G5FAV\n"
				}
				if state != "" {
					value := state
					if state == " passive" {
						value = "' passive'"
					}
					source += "  work_state: " + value + "\n"
				}
				if version == ProjectSchemaV05 {
					source += "resources: {}\n"
				}
				a := Analyze(declarationTestRoot(t, []byte(source)))
				valid := state == "" || ValidProjectWorkState(state)
				if a.Report.OK != valid {
					t.Fatalf("validation=%t want %t: %+v", a.Report.OK, valid, a.Report.Diagnostics)
				}
				if !valid {
					if !strings.Contains(a.Report.Diagnostics[0].Message, "project.work_state") || !strings.Contains(a.Report.Diagnostics[0].Message, "working or passive") {
						t.Fatal(a.Report.Diagnostics)
					}
					return
				}
				if a.Report.Project.WorkState != state || a.Plan.Project.WorkState != state || a.Plan.Project.Status != "active" {
					t.Fatal("classification lost or lifecycle changed", a.Plan.Project)
				}
				for _, value := range []any{a.Loaded.Contract, a.Report, a.Plan} {
					raw, err := json.Marshal(value)
					if err != nil || (bytes.Contains(raw, []byte(`"work_state"`)) != (state != "")) {
						t.Fatalf("optional JSON encoding: %s %v", raw, err)
					}
				}
				encoded, err := yaml.Marshal(a.Loaded.Contract.Project)
				var project ProjectSpec
				if err != nil || yaml.Unmarshal(encoded, &project) != nil || project != a.Loaded.Contract.Project {
					t.Fatalf("YAML round trip: %s %v", encoded, err)
				}
				var output bytes.Buffer
				RenderValidation(&output, a.Report)
				RenderPlan(&output, a.Plan, false)
				if (strings.Contains(output.String(), "Work state: "+state+" (metadata only)")) != (state != "") {
					t.Fatal(output.String())
				}
			})
		}
	}
}

func TestDeclarationWorkStateLegacyOmissionAndRuntimeIntent(t *testing.T) {
	d, err := ParseProjectDeclaration(fixtureRead(t, "minimal.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	assertLegacyOmissionBytes(t, d)
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
	for _, state := range []string{"passive", "working", ""} {
		source := raw
		if state != "" {
			source = bytes.Replace(raw, []byte("project:\n"), []byte("project:\n  work_state: "+state+"\n"), 1)
		}
		if err := os.WriteFile(file, source, 0600); err != nil {
			t.Fatal(err)
		}
		after := Analyze(root)
		if !after.Report.OK || after.Plan.Project.WorkState != state || after.Plan.Project.Status != before.Plan.Project.Status {
			t.Fatalf("classification: %+v %+v", after.Plan.Project, after.Report.Diagnostics)
		}
		if len(before.Plan.Actions) != len(after.Plan.Actions) || len(before.Plan.WatchedRoots) != len(after.Plan.WatchedRoots) {
			t.Fatal("classification added operational actions or roots")
		}
		for i, action := range after.Plan.Actions {
			if i == 0 {
				action.Description = before.Plan.Actions[i].Description
			}
			if action != before.Plan.Actions[i] {
				t.Fatal("operational action changed", action)
			}
		}
		for i, watch := range after.Plan.WatchedRoots {
			old := before.Plan.WatchedRoots[i]
			if watch.ConfigHash != old.ConfigHash || !reflect.DeepEqual(watch.ConfigJSON, old.ConfigJSON) {
				t.Fatal("classification changed runtime watch configuration")
			}
		}
		if state == "" && !reflect.DeepEqual(before.Report.Declaration, after.Report.Declaration) {
			t.Fatal("omitted replay changed source closure or digests")
		}
	}
}
