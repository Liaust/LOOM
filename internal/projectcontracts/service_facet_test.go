package projectcontracts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServicesFacetScaffoldAndValidation(t *testing.T) {
	result, err := ScaffoldProject(ScaffoldOptions{Name: "Service Project", Slug: "service-project", OwnerNode: "macbook", Preset: PresetMinimal, Facets: []string{"services"}, Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(result.ProjectRoot, ".loom/contracts/services/example.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("service example missing: %v", err)
	}
	analysis := Analyze(result.ProjectRoot)
	if !analysis.Report.OK || len(analysis.Report.Services) != 1 {
		t.Fatalf("service scaffold validation: ok=%v services=%#v diagnostics=%#v", analysis.Report.OK, analysis.Report.Services, analysis.Report.Diagnostics)
	}
	item := analysis.Report.Services[0]
	if item.ContractPath != ".loom/contracts/services/example.yaml" || item.Provisioning != "external" || item.ProviderAddress != "macbook@service-project-example" {
		t.Fatalf("unexpected service projection: %#v", item)
	}
	plan := BuildPlan(analysis.Loaded, analysis.Report)
	found := false
	for _, action := range plan.Actions {
		if action.Action == "would_register_service_provider" && strings.Contains(action.Description, "external") {
			found = true
		}
	}
	if !found {
		t.Fatalf("registration plan did not report external provisioning: %#v", plan.Actions)
	}
}

func TestServicesFacetRejectsNoncanonicalAndSystemContracts(t *testing.T) {
	tests := []struct {
		name  string
		file  string
		class string
		want  string
	}{
		{name: "wrong key path", file: "wrong.yaml", class: "project", want: "services.contract_path_invalid"},
		{name: "system trust boundary", file: "example.yaml", class: "system", want: "services.system_class_forbidden"},
		{name: "wrong extension", file: "example.yml", class: "project", want: "services.contract_extension_invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			mustWriteTestFile(t, root, CanonicalRootContractPath, "kind: loom.project\nschema_version: project.contract.v0.3\nproject:\n  slug: service-project\n  name: Service Project\n  owner_node: macbook\n  status: active\nfacets:\n  services: true\n")
			mustWriteTestFile(t, root, ".loom/contracts/services/"+test.file, "kind: service_registration\nschema_version: loom.service.v0.1\nservice:\n  key: example\n  name: Example\n  target_node: macbook\n  class: "+test.class+"\nruntime:\n  manager: launchd\n  unit: com.example.service\n")
			analysis := Analyze(root)
			found := false
			for _, diagnostic := range analysis.Report.Diagnostics {
				if diagnostic.Code == test.want {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing %s: %#v", test.want, analysis.Report.Diagnostics)
			}
		})
	}
}

func TestResolveServiceRegistrationContractV05(t *testing.T) {
	for _, scenario := range []string{"selected_only", "missing", "wrong_key", "system", "traversal", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			mustWriteTestFile(t, root, CanonicalRootContractPath, "kind: loom.project\nschema_version: project.contract.v0.5\nproject: {id: project_01ARZ3NDEKTSV4RRFFQ69G5FAV, slug: service-project, name: Service project, owner_node: main}\nresources: {}\n")
			raw := validProjectServiceYAML
			key := "project-api"
			switch scenario {
			case "wrong_key":
				raw = strings.Replace(raw, "key: project-api", "key: different", 1)
			case "system":
				raw = strings.Replace(raw, "service:\n", "service:\n  class: system\n", 1)
			case "traversal":
				key = "../../outside"
			}
			path := ".loom/contracts/services/project-api.yaml"
			if scenario != "missing" {
				mustWriteTestFile(t, root, path, raw)
			}
			if scenario == "symlink" {
				if err := os.Rename(filepath.Join(root, path), filepath.Join(root, "other.yaml")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, "other.yaml"), filepath.Join(root, path)); err != nil {
					t.Fatal(err)
				}
			}
			mustWriteTestFile(t, root, ".loom/contracts/services/unselected.yaml", "invalid unselected contract")
			analysis := Analyze(root)
			if !analysis.Report.OK || len(analysis.Plan.Services) != 0 {
				t.Fatalf("service implicitly enrolled: %+v", analysis.Report)
			}
			item, err := ResolveServiceRegistrationContract(analysis, key)
			if scenario != "selected_only" {
				if err == nil {
					t.Fatalf("accepted %s: %+v", scenario, item)
				}
				return
			}
			if err != nil || item.Key != key || item.ContractPath != path || item.ProviderAddress != "main@service-project-project-api" || item.Provisioning != ServiceProvisioningExternal {
				t.Fatalf("selected service=%+v err=%v", item, err)
			}
		})
	}
}

func TestResolveServiceRegistrationContractLegacy(t *testing.T) {
	root := t.TempDir()
	path := ".loom/contracts/services/project-api.yaml"
	for _, enabled := range []string{"true", "false"} {
		mustWriteTestFile(t, root, CanonicalRootContractPath, "kind: loom.project\nschema_version: project.contract.v0.3\nproject: {slug: service-project, name: Service project, owner_node: main}\nfacets: {services: "+enabled+"}\n")
		mustWriteTestFile(t, root, path, validProjectServiceYAML)
		analysis := Analyze(root)
		item, err := ResolveServiceRegistrationContract(analysis, "project-api")
		if enabled == "true" && (err != nil || item.ContractPath != path) {
			t.Fatalf("legacy selection: %+v %v", item, err)
		}
		if enabled == "false" && err == nil {
			t.Fatal("disabled legacy facet bypassed")
		}
	}
}

func mustWriteTestFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
