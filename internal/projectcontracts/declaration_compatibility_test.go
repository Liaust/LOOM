package projectcontracts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodeagent/watchedroots"
)

const compatibilityPolicy = `kind: loom.project_backup_policy
schema_version: backup.policy.v0.3
backup:
  enabled: true
  defaults:
    safe_root: project
    max_file_bytes: 4096
    max_batch_bytes: 8192
    max_pending_items: 7
    max_pending_bytes: 16384
    include_deletion_markers: false
    on_limit: degrade_and_require_manual_action
    exclude: ['private/**']
  roots:
    - key: controls
      path: .
      include: [loom.project.yaml, .loom/project.yaml]
    - key: notes_owner
      path: notes
      include: ['**/*.md']
    - key: repos_owner
      path: repos
      exclude: ['**/.git/**']
`

func compatibilityProtection(key, ref, target string) string {
	return fmt.Sprintf("  %s:\n    kind: protection\n    protection: {policy_ref: %s, path: %q}\n", key, ref, target)
}

func compatibilityRepository(state string, explicit bool) string {
	field := ""
	if explicit {
		field = fmt.Sprintf(", state_root: %q", state)
	}
	return "  repo:\n    kind: repository\n    repository: {path: repos, role: primary" + field + "}\n"
}

func TestDeclarationCompatibilityStateRoot(t *testing.T) {
	for _, state := range []string{"", ".repo", "development/state", ".repo/custom"} {
		t.Run(fmt.Sprintf("%q", state), func(t *testing.T) {
			root := declarationEnrollmentFixture(t, compatibilityRepository(state, true), "")
			before := dmFiles(t, root)
			a := Analyze(root)
			if !a.Report.OK || a.Report.Declaration == nil {
				t.Fatalf("explicit state intent: %+v", a.Report.Diagnostics)
			}
			if len(a.Report.Declaration.Repositories) != 1 || a.Report.Declaration.Repositories[0].StateRoot != state {
				t.Fatal("compiler dropped development-state intent")
			}
			if !reflect.DeepEqual(before, dmFiles(t, root)) {
				t.Fatal("compiler created files/Git/state root")
			}
			for _, path := range []string{"repos/.repo", "repos/.git", "repos/development"} {
				if _, e := os.Lstat(filepath.Join(root, path)); !os.IsNotExist(e) {
					t.Fatal("implicit development state")
				}
			}
		})
	}
}

func TestDeclarationCompatibilityStateOmission(t *testing.T) {
	omitted := declarationEnrollmentFixture(t, compatibilityRepository("", false), "")
	empty := declarationEnrollmentFixture(t, compatibilityRepository("", true), "")
	a, b := Analyze(omitted), Analyze(empty)
	if !a.Report.OK || !b.Report.OK {
		t.Fatalf("empty is omitted intent: %+v %+v", a.Report.Diagnostics, b.Report.Diagnostics)
	}
	aj, _ := json.Marshal(a.Report.Declaration.Document)
	bj, _ := json.Marshal(b.Report.Declaration.Document)
	ay, _ := yaml.Marshal(a.Report.Declaration.Document)
	by, _ := yaml.Marshal(b.Report.Declaration.Document)
	if !bytes.Equal(aj, bj) || !bytes.Equal(ay, by) || bytes.Contains(aj, []byte("state_root")) || bytes.Contains(ay, []byte("state_root")) {
		t.Fatal("empty state_root changed existing document bytes")
	}
	if a.Report.Declaration.Repositories[0].StateRoot != "" {
		t.Fatal("omission defaults development state")
	}
}

func TestDeclarationCompatibilityStateRefusals(t *testing.T) {
	for _, state := range []string{".", "..", "../state", "a/../state", "./state", "state/", " state", "state ", "/tmp/state", "C:/state", "a\\b", "a\n", ".git", ".loom", ".project", "a/.GIT", "a/.LoOm/state", "a/.PROJECT"} {
		t.Run(fmt.Sprintf("%q", state), func(t *testing.T) {
			a := Analyze(declarationEnrollmentFixture(t, compatibilityRepository(state, true), ""))
			if a.Report.OK {
				t.Fatal("unsafe or normalized state_root admitted")
			}
		})
	}
}

func TestDeclarationCompatibilityMigrationStateRoot(t *testing.T) {
	for _, state := range []string{".repo", "development/state"} {
		t.Run(state, func(t *testing.T) {
			f := dmMake(t, "repositories")
			ref := ".loom/contracts/repos.yaml"
			dmReplace(&f.Input, ref, "      role: primary", "      role: primary\n      state_root: "+state)
			for i := range f.Input.Membership.Members {
				if f.Input.Membership.Members[i].Role == RepositoryRolePrimary {
					f.Input.Membership.Members[i].StateRoot = state
				}
			}
			dmRebind(&f.Input)
			dmWrite(t, f.Root, ref, f.Input.Sources[dmIndex(&f.Input, ref)].Raw)
			f.Analysis = Analyze(f.Root)
			f.Files = dmFiles(t, f.Root)
			out := PreviewDeclarationMigration(f.Input)
			dmParity(t, f, out)
			if out.Identity.Members[0].StateRoot != state {
				t.Fatal("registered state intent lost")
			}
			for i := range f.Input.Membership.Members {
				f.Input.Membership.Members[i].StateRoot = ""
			}
			dmRebind(&f.Input)
			dmAssertRefusal(t, PreviewDeclarationMigration(f.Input), "membership_conflict")
		})
	}
}

func TestDeclarationCompatibilityMigrationStateRefusals(t *testing.T) {
	for _, state := range []string{".", "./state", " state ", "a/../state", "/state", ".git", ".loom", ".project", "a\n"} {
		t.Run(fmt.Sprintf("%q", state), func(t *testing.T) {
			f := dmMake(t, "repositories")
			dmReplace(&f.Input, ".loom/contracts/repos.yaml", "      role: primary", fmt.Sprintf("      role: primary\n      state_root: %q", state))
			dmRebind(&f.Input)
			dmAssertRefusal(t, PreviewDeclarationMigration(f.Input), "state_root_unrepresentable")
		})
	}
}

// Expected owners come from the unchanged legacy normalizer with the exact
// same bytes and project identity, never from the candidate compilation.
func compatibilityLegacy(t *testing.T, root, policy string) Analysis {
	t.Helper()
	raw, e := os.ReadFile(filepath.Join(root, CanonicalRootContractPath))
	if e != nil {
		t.Fatal(e)
	}
	d, e := ParseProjectDeclaration(raw)
	if e != nil {
		t.Fatal(e)
	}
	legacy := ProjectContract{Kind: "loom.project", SchemaVersion: ProjectSchemaV04, Project: d.Project}
	b, e := json.Marshal(legacy)
	if e != nil {
		t.Fatal(e)
	}
	var doc map[string]any
	_ = json.Unmarshal(b, &doc)
	doc["policies"] = map[string]string{"backup": ".loom/policies/backup.yaml"}
	b, _ = json.Marshal(doc)
	other := declarationTestRoot(t, b)
	for _, dir := range []string{"notes", "repos", ".loom/policies"} {
		if e = os.MkdirAll(filepath.Join(other, dir), 0700); e != nil {
			t.Fatal(e)
		}
	}
	if e = os.WriteFile(filepath.Join(other, ".loom/policies/backup.yaml"), []byte(policy), 0600); e != nil {
		t.Fatal(e)
	}
	a := Analyze(other)
	if !a.Report.OK {
		t.Fatal(a.Report.Diagnostics)
	}
	return a
}

func TestDeclarationCompatibilityRootProtection(t *testing.T) {
	ref := ".loom/policies/backup.yaml"
	for _, disabled := range []bool{false, true} {
		for _, shape := range []string{"standalone", "separate", "attached", "shared", "omitted"} {
			t.Run(fmt.Sprintf("%s/disabled_%v", shape, disabled), func(t *testing.T) {
				policy := compatibilityPolicy
				if disabled {
					policy = strings.Replace(policy, "enabled: true", "enabled: false", 1)
				}
				resources := compatibilityProtection("root", ref, ".")
				want := 1
				switch shape {
				case "separate":
					resources += compatibilityProtection("notes", ref, "notes") + compatibilityProtection("repos", ref, "repos")
					want = 3
				case "attached":
					resources += "  notes:\n    kind: repository\n    repository: {path: notes, role: component, protection: root}\n  repos:\n    kind: repository\n    repository: {path: repos, role: primary, protection: root}\n"
					want = 3
				case "shared":
					resources += compatibilityProtection("same", ref, ".")
				case "omitted":
					resources = compatibilityProtection("root", ref, "")
					want = 0
				}
				root := declarationEnrollmentFixture(t, resources, policy)
				before := dmFiles(t, root)
				a := Analyze(root)
				if !a.Report.OK {
					t.Fatalf("explicit representation: %+v", a.Report.Diagnostics)
				}
				if disabled {
					want = 0
				}
				if len(a.Plan.WatchedRoots) != want {
					t.Fatalf("got %d roots, want %d", len(a.Plan.WatchedRoots), want)
				}
				legacy := compatibilityLegacy(t, root, policy)
				for _, item := range a.Plan.WatchedRoots {
					found := false
					for _, old := range legacy.Plan.WatchedRoots {
						if old.Key == item.Key {
							found = true
							if !reflect.DeepEqual(dmPortable(t, old), dmPortable(t, item)) {
								t.Fatalf("owner configuration changed: %+v / %+v", old, item)
							}
							metadata := map[string]any{}
							for k, v := range item.Metadata {
								if k != "declaration_sources" {
									metadata[k] = v
								}
							}
							if !declarationJSONEqual(old.Metadata, metadata) {
								t.Fatal("owner metadata changed")
							}
						}
					}
					if !found {
						t.Fatal("new owner invented")
					}
				}
				if !reflect.DeepEqual(a.Report.Declaration, a.Plan.Declaration) {
					t.Fatal("exact compiler evidence copy changed filter slices")
				}
				if e := ValidateDeclarationEnrollment(*a.Loaded, a.Report, a.Plan); e != nil {
					t.Fatal(e)
				}
				if !reflect.DeepEqual(before, dmFiles(t, root)) {
					t.Fatal("source or payload changed")
				}
			})
		}
	}
}

func TestDeclarationCompatibilityRootRefusals(t *testing.T) {
	ref := ".loom/policies/backup.yaml"
	for name, policy := range map[string]string{
		"missing":             strings.Replace(compatibilityPolicy, "      path: .\n", "      path: unrelated\n", 1),
		"duplicate_path":      compatibilityPolicy + "    - key: other\n      path: .\n",
		"duplicate_key":       strings.Replace(compatibilityPolicy, "key: notes_owner", "key: controls", 1),
		"foreign_safe_root":   strings.Replace(compatibilityPolicy, "safe_root: project", "safe_root: box", 1),
		"empty_policy_path":   strings.Replace(compatibilityPolicy, "      path: .\n", "      path: ''\n", 1),
		"unclean_policy_path": strings.Replace(compatibilityPolicy, "      path: .\n", "      path: ./\n", 1),
	} {
		t.Run(name, func(t *testing.T) {
			resources := compatibilityProtection("root", ref, ".") + compatibilityProtection("notes", ref, "notes")
			a := Analyze(declarationEnrollmentFixture(t, resources, policy))
			if a.Report.OK {
				t.Fatal("invalid root owner admitted")
			}
		})
	}
	for _, target := range []string{"./", "../", "/", "a/../", " .", ".git", ".repo", ".loom", "a\\b", "a\n"} {
		t.Run("unsafe/"+fmt.Sprintf("%q", target), func(t *testing.T) {
			if Analyze(declarationEnrollmentFixture(t, compatibilityProtection("root", ref, target), compatibilityPolicy)).Report.OK {
				t.Fatal("unsafe target admitted")
			}
		})
	}
	for name, resources := range map[string]string{
		"repository":  "  repo:\n    kind: repository\n    repository: {path: '.', role: primary}\n",
		"knowledge":   "  knowledge:\n    kind: knowledge\n    knowledge: {path: '.', category: notes}\n",
		"policy_ref":  compatibilityProtection("root", ".", "."),
		"application": "  repo:\n    kind: repository\n    repository: {path: repos, role: primary}\n  app:\n    kind: application\n    application: {repository: repo, manifest: manifest.yaml, data: {data: {path: '.'}}}\n",
	} {
		t.Run(name, func(t *testing.T) {
			if Analyze(declarationEnrollmentFixture(t, resources, compatibilityPolicy)).Report.OK {
				t.Fatal("general path validator relaxed")
			}
		})
	}
}

func TestDeclarationCompatibilityRootOverlap(t *testing.T) {
	ref := ".loom/policies/backup.yaml"
	for _, mode := range []string{"enabled", "disabled", "same_key", "nested"} {
		t.Run(mode, func(t *testing.T) {
			policy := compatibilityPolicy
			left, right := ".", "notes"
			other := strings.Replace(enrollmentPolicy, "path: journal", "path: notes", 1)
			if mode == "disabled" {
				other = strings.Replace(other, "enabled: true", "enabled: false", 1)
			}
			if mode == "same_key" {
				other = strings.Replace(other, "key: journal_owner", "key: controls", 1)
			}
			if mode == "nested" {
				left, right = "journal", "journal/child"
				policy = enrollmentPolicy
				other = strings.Replace(enrollmentPolicy, "path: journal", "path: journal/child", 1)
			}
			root := declarationEnrollmentFixture(t, compatibilityProtection("left", ref, left)+compatibilityProtection("right", ".loom/policies/other.yaml", right), policy)
			dmWrite(t, root, ".loom/policies/other.yaml", []byte(other))
			a := Analyze(root)
			if a.Report.OK != (mode == "disabled") {
				t.Fatalf("overlap %s: %+v", mode, a.Report.Diagnostics)
			}
		})
	}
}

func compatibilityMigrationRoot(t *testing.T, dot bool) dmFixture {
	t.Helper()
	f := dmMake(t, "backup")
	if dot {
		dmReplace(&f.Input, ".loom/contracts/backup.yaml", "path: data/docs", "path: .")
		dmWrite(t, f.Root, ".loom/contracts/backup.yaml", f.Input.Sources[dmIndex(&f.Input, ".loom/contracts/backup.yaml")].Raw)
		f.Analysis = Analyze(f.Root)
		if !f.Analysis.Report.OK {
			t.Fatal(f.Analysis.Report.Diagnostics)
		}
		f.Input.Watches.Roots = nil
		f.Input.Coverage.Coverage = nil
		for _, w := range f.Analysis.Report.WatchedRoots {
			r := dmPortable(t, w)
			f.Input.Watches.Roots = append(f.Input.Watches.Roots, MigrationWatch{Owner: "projectwatch", Revision: "owner-1", Root: r})
			f.Input.Coverage.Coverage = append(f.Input.Coverage.Coverage, MigrationCoverage{Owner: "projectwatch", Revision: "owner-1", Root: r})
		}
		f.Files = dmFiles(t, f.Root)
		dmRebind(&f.Input)
	}
	return f
}

func TestDeclarationCompatibilityMigrationRoot(t *testing.T) {
	f := compatibilityMigrationRoot(t, true)
	before := dmInputSnapshot(f.Input)
	dmParity(t, f, PreviewDeclarationMigration(f.Input))
	if !bytes.Equal(before, dmInputSnapshot(f.Input)) {
		t.Fatal("input changed")
	}
}

func TestDeclarationCompatibilityExternalRootOverlap(t *testing.T) {
	for _, mode := range []string{"owned_dot", "external_dot", "disjoint"} {
		t.Run(mode, func(t *testing.T) {
			f := compatibilityMigrationRoot(t, mode == "owned_dot")
			external := f.Input.Watches.Roots[0].Root
			external.Key = "external_distinct"
			external.BackendRootKey = ProjectWatchedRootKey("other", external.Key)
			external.WorkerKey = noderuntime.WatchedRootWorkerKey(external.BackendRootKey)
			external.Config.RootKey = external.BackendRootKey
			external.Config.RootRelativePath = "external/disjoint"
			if mode == "external_dot" {
				external.Config.RootRelativePath = "."
			}
			if mode == "owned_dot" {
				external.Config.RootRelativePath = "descendant"
			}
			external.ConfigHash = watchedroots.ConfigHash(external.Config)
			if !migrationRootValid(external) {
				t.Fatal("external fixture must be a valid portable root")
			}
			for _, w := range f.Input.Watches.Roots {
				if !migrationRootValid(w.Root) {
					t.Fatal("owned fixture must be a valid portable root")
				}
			}
			f.Input.Coverage.Coverage = append(f.Input.Coverage.Coverage, MigrationCoverage{Owner: "external-owner", Revision: "external-1", External: true, Root: external})
			dmRebind(&f.Input)
			out := PreviewDeclarationMigration(f.Input)
			if mode == "disjoint" {
				dmAssertCandidate(t, out)
				if len(out.Retention.ExternalCoverage) != 1 {
					t.Fatal("external coverage discarded")
				}
			} else {
				dmAssertRefusal(t, out, "coverage_conflict")
			}
		})
	}
}

// Frozen pre-reference minimal wire bytes: adding the optional block must not
// change serialization, source digests, or ordinary creation defaults.
func assertLegacyOmissionBytes(t *testing.T, d ProjectDeclaration) {
	t.Helper()
	expectedJSON := `{"kind":"loom.project","schema_version":"project.contract.v0.5","project":{"id":"project_01ARZ3NDEKTSV4RRFFQ69G5FAV","slug":"plain-project","name":"Plain project","owner_node":"main"},"resources":{}}`
	expectedYAML := "kind: loom.project\nschema_version: project.contract.v0.5\nproject:\n    id: project_01ARZ3NDEKTSV4RRFFQ69G5FAV\n    slug: plain-project\n    name: Plain project\n    description: \"\"\n    owner_node: main\n    status: \"\"\nresources: {}\n"
	actualJSON, jerr := json.Marshal(d)
	actualYAML, yerr := yaml.Marshal(d)
	if jerr != nil || yerr != nil || string(actualJSON) != expectedJSON || string(actualYAML) != expectedYAML || dmHash(actualJSON) != dmHash([]byte(expectedJSON)) || dmHash(actualYAML) != dmHash([]byte(expectedYAML)) {
		t.Fatalf("minimal serialization changed: %s\n%s", actualJSON, actualYAML)
	}
}
