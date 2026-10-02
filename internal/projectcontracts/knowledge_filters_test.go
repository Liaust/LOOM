package projectcontracts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/nodeagent/watchedroots"
)

func TestKnowledgeFiltersEnrollment(t *testing.T) {
	for _, allocation := range []bool{false, true} {
		t.Run(map[bool]string{false: "project_folder", true: "application_data"}[allocation], func(t *testing.T) {
			root := declarationWebDAVRoot(t)
			d, err := ParseProjectDeclaration(fixtureRead(t, "webdav.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			delete(d.Resources, "retained")
			d.Resources["webdav"].Application.Data["library"] = ApplicationData{BindingRef: "webdav.library"}
			k := d.Resources["reading"].Knowledge
			k.Path, k.Protection = "material", ""
			if allocation {
				k.Path = ""
				k.ApplicationData = &KnowledgeApplicationData{Application: "webdav", Data: "library", Subpath: "notebooks"}
			}
			k.Include = []string{"**/*.[pP][dD][fF]"}
			k.Exclude = []string{"**/partial/**", "**/*.part.*"}
			raw, err := json.Marshal(d)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, CanonicalRootContractPath), raw, 0600); err != nil {
				t.Fatal(err)
			}
			a := Analyze(root)
			if !a.Report.OK || len(a.Plan.WatchedRoots) != 1 {
				t.Fatalf("enrollment: %+v", a.Report.Diagnostics)
			}
			watch := a.Plan.WatchedRoots[0]
			var config watchedroots.RootConfig
			if err := json.Unmarshal(watch.ConfigJSON, &config); err != nil {
				t.Fatal(err)
			}
			if !stringSlicesEqual(config.Include, k.Include) || !stringSlicesEqual(config.Exclude, k.Exclude) {
				t.Fatalf("lost filters: %+v", config)
			}
			source := watch.Metadata["knowledge_source"].(map[string]any)
			if !declarationJSONEqual(source["include"], k.Include) || !declarationJSONEqual(source["exclude"], k.Exclude) {
				t.Fatalf("lost Notes metadata: %+v", source)
			}
			for name, want := range map[string]bool{"Physics.pdf": true, "physics/Notes.PDF": true, "Physics.goodnotes": false, "Physics.pdf.part": false, "partial/Physics.pdf": false, "Physics.part.pdf": false} {
				included, _ := watchedroots.MatchAny(config.Include, name)
				excluded, _ := watchedroots.MatchAny(config.Exclude, name)
				if got := included && !excluded; got != want {
					t.Errorf("%s admitted=%v, want %v", name, got, want)
				}
			}
			if watch.BackupMode != "none" || a.Loaded.Declaration.Resources["webdav"].Application.Data["library"].BindingRef != "webdav.library" {
				t.Fatal("changed application backup ownership")
			}
			if err := ValidateDeclarationEnrollment(*a.Loaded, a.Report, a.Plan); err != nil {
				t.Fatal(err)
			}
			source["include"] = []string{"**/*"}
			if err := ValidateDeclarationEnrollment(*a.Loaded, a.Report, a.Plan); err == nil {
				t.Fatal("filter evidence substitution accepted")
			}
		})
	}
}

func TestKnowledgeFiltersValidate(t *testing.T) {
	for _, field := range []string{"include", "exclude"} {
		for _, pattern := range []string{"", "../private/**", "/private/**", "["} {
			t.Run(field+"/"+pattern, func(t *testing.T) {
				d, err := ParseProjectDeclaration(fixtureRead(t, "webdav.yaml"))
				if err != nil {
					t.Fatal(err)
				}
				k := d.Resources["reading"].Knowledge
				if field == "include" {
					k.Include = []string{pattern}
				} else {
					k.Exclude = []string{pattern}
				}
				raw, err := json.Marshal(d)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := ParseProjectDeclaration(raw); err == nil || !strings.Contains(err.Error(), "resources.reading.knowledge."+field) {
					t.Fatalf("missing field diagnostic: %v", err)
				}
			})
		}
	}
}

func TestKnowledgeFiltersProtectionInheritance(t *testing.T) {
	for _, tc := range []struct {
		name, fields, policy string
		ok                   bool
	}{
		{"omitted", "", enrollmentPolicy, true},
		{"matching", ", include: ['**/*.md'], exclude: ['private/**']", enrollmentPolicy, true},
		{"different_include", ", include: ['**/*.pdf']", enrollmentPolicy, false},
		{"different_exclude", ", exclude: ['temporary/**']", enrollmentPolicy, false},
		{"new_exclude", ", exclude: ['temporary/**']", strings.Replace(enrollmentPolicy, "exclude: ['private/**']", "exclude: []", 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resources := strings.Replace(enrollmentKnowledge, "category: notes", "category: notes, protection: retained"+tc.fields, 1) + enrollmentProtection
			a := Analyze(declarationEnrollmentFixture(t, resources, tc.policy))
			if a.Report.OK != tc.ok {
				t.Fatalf("ok=%v: %+v", a.Report.OK, a.Report.Diagnostics)
			}
			if !tc.ok {
				return
			}
			watch := a.Plan.WatchedRoots[0]
			if !stringSlicesEqual(watch.Include, []string{"**/*.md"}) || !stringSlicesEqual(watch.Exclude, []string{"private/**"}) || watch.BackupMode != "incremental_raw" {
				t.Fatal("changed shared backup filters")
			}
			if err := ValidateDeclarationEnrollment(*a.Loaded, a.Report, a.Plan); err != nil {
				t.Fatal(err)
			}
		})
	}
}
