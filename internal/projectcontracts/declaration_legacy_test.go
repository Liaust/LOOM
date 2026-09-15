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
)

// Synthetic source bytes with the semantic values in the dated WebDAV receipt.
// These are not reconstructed live files and must not use the live hashes.
const legacyReferenceProject = `kind: loom.project
schema_version: project.contract.v0.4
project: {id: project_01ARZ3NDEKTSV4RRFFQ69G5FAV, slug: legacy-references, name: Legacy references, owner_node: main, status: draft}
facets: {notes: true, repos: true, backup_policy: true, portal: true}
provider_defaults: {scripts_provider: project, workflows_provider: project}
policies: {backup: .loom/contracts/backup.yaml}
portal: {display_group: projects, summary: ""}
metadata: {scaffold_preset: minimal, scaffolded_by: loom}
`
const legacyReferenceNotes = `kind: loom.notes
schema_version: notes.contract.v0.3
notes: {status: draft, sync: true, index: true, backup: true, root_key: notes, path: ., include: ["**/*"], exclude: []}
`
const legacyReferenceRepos = `kind: loom.repos
schema_version: repos.contract.v0.4
repos:
  status: draft
  defaults: {sync: false, backup: true, index: false, include: ["**/*"], exclude: []}
  watch_roots: [{key: repos, path: ., display_name: Project Repositories}]
  members: []
`
const legacyReferenceBackup = `kind: loom.project_backup_policy
schema_version: backup.policy.v0.3
backup:
  enabled: true
  defaults: {safe_root: project, mode: incremental_raw, max_file_bytes: 1048576, max_batch_bytes: 1048576, max_pending_items: 10000, max_pending_bytes: 1073741824, include_deletion_markers: true, on_limit: degrade_and_require_manual_action}
  roots:
    - {key: notes, path: notes, include: ["**/*"], exclude: []}
    - {key: repos, path: repos, include: ["**/*"], exclude: []}
    - key: project
      path: .
      include: [.loom/project.yaml, '.loom/contracts/**', '.loom/agents/**', '.loom/templates/**', '.loom/tools/**', .loom/.gitignore, .loomignore, README.md, AGENTS.md, 'notes/**', 'scripts/**', 'connectors/**', 'schedules/**', 'direct_events/**', 'modules/**']
      exclude: []
`

func legacyReferenceFixture(t *testing.T) (string, Analysis) {
	t.Helper()
	root := t.TempDir()
	for _, path := range []string{"notes", "repos"} {
		if err := os.MkdirAll(filepath.Join(root, path), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for path, raw := range map[string]string{CanonicalRootContractPath: legacyReferenceProject, ".loom/contracts/notes.yaml": legacyReferenceNotes, ".loom/contracts/repos.yaml": legacyReferenceRepos, ".loom/contracts/backup.yaml": legacyReferenceBackup} {
		dmWrite(t, root, path, []byte(raw))
	}
	before := Analyze(root)
	if !before.Report.OK {
		t.Fatalf("invalid synthetic legacy source: %+v", before.Report.Diagnostics)
	}
	dmWrite(t, root, ".loom/contracts/retained-project-v04.yaml", []byte(legacyReferenceProject))
	raw := fmt.Sprintf(`kind: loom.project
schema_version: project.contract.v0.5
project: {id: project_01ARZ3NDEKTSV4RRFFQ69G5FAV, slug: legacy-references, name: Legacy references, owner_node: main, status: draft}
resources:
  notes_backup: {kind: protection, protection: {policy_ref: .loom/contracts/backup.yaml, path: notes}}
  repos_backup: {kind: protection, protection: {policy_ref: .loom/contracts/backup.yaml, path: repos}}
  project_backup: {kind: protection, protection: {policy_ref: .loom/contracts/backup.yaml, path: .}}
legacy_contracts:
  project: {ref: .loom/contracts/retained-project-v04.yaml, schema_version: project.contract.v0.4, digest: %s}
  notes: {key: imported_notes, ref: .loom/contracts/notes.yaml, schema_version: notes.contract.v0.3, digest: %s, protection: notes_backup}
  repos: {key: imported_repos, ref: .loom/contracts/repos.yaml, schema_version: repos.contract.v0.4, digest: %s, protection: repos_backup}
`, dmHash([]byte(legacyReferenceProject)), dmHash([]byte(legacyReferenceNotes)), dmHash([]byte(legacyReferenceRepos)))
	dmWrite(t, root, CanonicalRootContractPath, []byte(raw))
	return root, before
}

func TestDeclarationLegacyReferenceOmission(t *testing.T) {
	raw, err := os.ReadFile("testdata/declarations_v05/minimal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	d, err := ParseProjectDeclaration(raw)
	if err != nil {
		t.Fatal(err)
	}
	assertLegacyOmissionBytes(t, d)
	marshaled, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(marshaled, []byte("legacy_contracts")) {
		t.Fatal("omission introduced legacy semantics")
	}
	// Existing immutable minimal source and all old compiler fixtures stay unchanged.
	root := t.TempDir()
	dmWrite(t, root, CanonicalRootContractPath, raw)
	for _, ref := range []string{".loom/contracts/notes.yaml", ".loom/contracts/repos.yaml", ".loom/contracts/retained-project-v04.yaml"} {
		dmWrite(t, root, ref, []byte("unknown: broken legacy source\n"))
	}
	before := dmFiles(t, root)
	a := Analyze(root)
	if !a.Report.OK || !a.Report.Registerable || !reflect.DeepEqual(before, dmFiles(t, root)) {
		t.Fatal("ordinary default changed")
	}
}

func TestDeclarationLegacyReferenceParity(t *testing.T) {
	t.Run("qualified_variants", legacyReferenceParityVariants)
	root, legacy := legacyReferenceFixture(t)
	before := dmFiles(t, root)
	a := Analyze(root)
	if !a.Report.OK || a.Report.Declaration == nil {
		t.Fatalf("valid pure reference representation refused: %+v", a.Report.Diagnostics)
	}
	if a.Report.Registerable || a.Plan.Registerable {
		t.Fatal("owner transition became registerable")
	}
	if len(a.Report.DerivedProviders) != 1 || !reflect.DeepEqual(a.Report.DerivedProviders, legacy.Report.DerivedProviders) {
		t.Fatalf("provider normalization/dedup changed: %+v", a.Report.DerivedProviders)
	}
	if len(a.Report.Declaration.Repositories) != 0 || len(a.Report.WatchedRoots) != 3 {
		t.Fatal("empty membership lost independent watches")
	}
	for i, got := range a.Report.WatchedRoots {
		want := legacy.Report.WatchedRoots[i]
		if !reflect.DeepEqual(migrationRoot(got), migrationRoot(want)) {
			t.Fatalf("full owner config changed:\n%+v\n%+v", got, want)
		}
		metadata := map[string]any{}
		for key, value := range got.Metadata {
			if key != "declaration_sources" {
				metadata[key] = value
			}
		}
		if !declarationJSONEqual(metadata, want.Metadata) {
			t.Fatalf("original contribution order/attribution changed: %+v %+v", metadata, want.Metadata)
		}
	}
	if !reflect.DeepEqual(before, dmFiles(t, root)) {
		t.Fatal("reference compilation created or changed source files")
	}
}

func TestDeclarationLegacyReferenceSourceOnly(t *testing.T) {
	root, _ := legacyReferenceFixture(t)
	a := Analyze(root)
	if !a.Report.OK || a.Report.Declaration == nil {
		t.Fatalf("pure compilation unavailable: %+v", a.Report.Diagnostics)
	}
	for _, tc := range []struct {
		name   string
		change func(*DeclarationCompilation)
	}{
		{"document", func(c *DeclarationCompilation) { c.Document.Project.Name = "changed" }},
		{"snapshot_hash", func(c *DeclarationCompilation) { c.Sources[0].Hash = dmHash([]byte("changed")) }},
		{"unselected_snapshot", func(c *DeclarationCompilation) {
			c.Sources = append(c.Sources, declarationSnapshot(".loom/contracts/unselected.yaml", ProjectSchemaV04, []byte(legacyReferenceProject)))
		}},
		{"compiled_notes", func(c *DeclarationCompilation) { c.LegacyContracts.Notes[0].Index = false }},
		{"providers", func(c *DeclarationCompilation) { c.LegacyContracts.DerivedProviders = nil }},
		{"contributors", func(c *DeclarationCompilation) { c.LegacyContracts.Contributors = nil }},
		{"protection", func(c *DeclarationCompilation) { c.Protection[0].Enabled = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			compiled := cloneDeclarationCompilation(a.Report.Declaration)
			tc.change(compiled)
			if _, err := CompileDeclarationEnrollment(*a.Loaded, *compiled); err == nil {
				t.Fatal("forged source/compiler evidence accepted")
			}
		})
	}
	for _, s := range a.Report.Declaration.Sources {
		path := filepath.Join(root, filepath.FromSlash(s.Ref))
		if err := os.Rename(path, path+".held-away"); err != nil {
			t.Fatal(err)
		}
	}
	roots, err := CompileDeclarationEnrollment(*a.Loaded, *a.Report.Declaration)
	if err != nil || !declarationJSONEqual(roots, a.Report.WatchedRoots) {
		t.Fatalf("source-only compile reopened files or changed evidence: %v", err)
	}
}

func TestDeclarationLegacyReferenceRefusals(t *testing.T) {
	t.Run("retained_semantics", legacyReferenceSemanticRefusals)
	for _, tc := range []struct{ name, old, new string }{
		{"digest", "digest: sha256:", "digest: wrong:"},
		{"duplicate_block_field", "legacy_contracts:\n", "legacy_contracts:\n  project: {}\n"},
		{"alias", "legacy_contracts:\n", "legacy_contracts: &retained\n"},
		{"self_reference", "ref: .loom/contracts/retained-project-v04.yaml", "ref: .loom/project.yaml"},
		{"unsafe_reference", "ref: .loom/contracts/notes.yaml", "ref: ../notes.yaml"},
		{"unknown_field", "legacy_contracts:\n", "legacy_contracts:\n  magic: true\n"},
		{"missing_project", "  project: {ref:", "  unexpected: {ref:"},
		{"wrong_schema", "schema_version: notes.contract.v0.3, digest:", "schema_version: notes.contract.v0.4, digest:"},
		{"key_collision", "key: imported_notes", "key: notes_backup"},
		{"duplicate_key", "key: imported_repos", "key: imported_notes"},
		{"wrong_attachment", "protection: notes_backup", "protection: repos_backup"},
		{"identity_drift", "name: Legacy references, owner_node: main, status: draft}", "name: Changed, owner_node: main, status: draft}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := legacyReferenceFixture(t)
			path := filepath.Join(root, CanonicalRootContractPath)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			changed := strings.Replace(string(raw), tc.old, tc.new, 1)
			if changed == string(raw) {
				t.Fatal("ineffective fixture mutation")
			}
			dmWrite(t, root, CanonicalRootContractPath, []byte(changed))
			if Analyze(root).Report.OK {
				t.Fatal("invalid reference accepted")
			}
		})
	}
}

// Mutate retained bytes and update the exact reference digest so these exercise
// semantic validation rather than only detecting an outdated hash.
func legacyReferenceChangeSource(t *testing.T, root, ref, old, replacement string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, ref))
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(raw), old, replacement, 1)
	if changed == string(raw) {
		t.Fatal("ineffective source mutation")
	}
	dmWrite(t, root, ref, []byte(changed))
	active, err := os.ReadFile(filepath.Join(root, CanonicalRootContractPath))
	if err != nil {
		t.Fatal(err)
	}
	dmWrite(t, root, CanonicalRootContractPath, []byte(strings.ReplaceAll(string(active), dmHash(raw), dmHash([]byte(changed)))))
}

func legacyReferenceSemanticRefusals(t *testing.T) {
	const project = ".loom/contracts/retained-project-v04.yaml"
	const notes = ".loom/contracts/notes.yaml"
	const repos = ".loom/contracts/repos.yaml"
	const backup = ".loom/contracts/backup.yaml"
	for _, tc := range []struct{ name, ref, old, new string }{
		{"project_kind", project, "kind: loom.project", "kind: wrong.project"},
		{"project_schema", project, "project.contract.v0.4", "project.contract.v0.5"},
		{"project_unknown", project, "facets:", "unknown: true\nfacets:"},
		{"project_recursion", project, "facets:", "legacy_contracts: {}\nfacets:"},
		{"project_alias", project, "facets:", "facets: &switches"},
		{"project_duplicate", project, "facets:", "facets: {}\nfacets:"},
		{"provider_nondefault", project, "scripts_provider: project", "scripts_provider: elsewhere"},
		{"metadata_semantics", project, "scaffold_preset: minimal", "unknown: value"},
		{"portal_unknown", project, "display_group: projects", "unexpected: projects"},
		{"disabled_facet", project, "notes: true", "notes: false"},
		{"unselected_notes", project, "notes: true, ", ""},
		{"unselected_repos", project, "repos: true, ", ""},
		{"notes_kind", notes, "kind: loom.notes", "kind: wrong.notes"},
		{"notes_schema", notes, "notes.contract.v0.3", "notes.contract.v0.4"},
		{"notes_unknown", notes, "notes:", "unknown: true\nnotes:"},
		{"notes_metadata", notes, "notes:", "metadata: {other: true}\nnotes:"},
		{"notes_material", notes, "notes:", "material: [{key: docs, category: docs, path: docs, enabled: true}]\nnotes:"},
		{"notes_path", notes, "path: .", "path: ../repos"},
		{"notes_key", notes, "root_key: notes", "root_key: other"},
		{"repos_members", repos, "members: []", "members: [{id: repo_01ARZ3NDEKTSV4RRFFQ69G5FAV, key: added, path: ., role: primary}]"},
		{"repos_implicit_members", repos, "  members: []\n", ""},
		{"repos_v03", repos, "repos.contract.v0.4", "repos.contract.v0.3"},
		{"repos_defaults_sync", repos, "sync: false", "sync: true"},
		{"repos_defaults_index", repos, "index: false", "index: true"},
		{"repos_watch_sync", repos, "display_name: Project Repositories", "display_name: Project Repositories, sync: true"},
		{"repos_watch_index", repos, "display_name: Project Repositories", "display_name: Project Repositories, index: true"},
		{"repos_key_collision", repos, "key: repos", "key: notes"},
		{"backup_source_kind", backup, "kind: loom.project_backup_policy", "kind: wrong.policy"},
		{"backup_missing_exact_path", backup, "path: notes", "path: different"},
		{"backup_owner_key", backup, "key: notes", "key: different"},
		{"backup_owner_safe_root", backup, "safe_root: project", "safe_root: elsewhere"},
		{"backup_pattern_conflict", backup, `include: ["**/*"]`, `include: ["**/*.md"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := legacyReferenceFixture(t)
			legacyReferenceChangeSource(t, root, tc.ref, tc.old, tc.new)
			before := dmFiles(t, root)
			if Analyze(root).Report.OK {
				t.Fatal("unsupported retained semantics accepted")
			}
			if !reflect.DeepEqual(before, dmFiles(t, root)) {
				t.Fatal("refusal mutated files")
			}
		})
	}
	for _, name := range []string{"missing", "directory", "symlink", "alternative_conflict", "empty", "null", "missing_notes_reference", "native_owner", "missing_backup_root", "additional_disabled_policy"} {
		t.Run(name, func(t *testing.T) {
			root, _ := legacyReferenceFixture(t)
			switch name {
			case "missing", "directory", "symlink":
				if err := os.Remove(filepath.Join(root, project)); err != nil {
					t.Fatal(err)
				}
				if name == "directory" {
					if err := os.Mkdir(filepath.Join(root, project), 0700); err != nil {
						t.Fatal(err)
					}
				}
				if name == "symlink" {
					if err := os.Symlink("notes.yaml", filepath.Join(root, project)); err != nil {
						t.Fatal(err)
					}
				}
			case "alternative_conflict":
				dmWrite(t, root, "notes/loom.notes.yaml", []byte(strings.Replace(legacyReferenceNotes, "status: draft", "status: active", 1)))
			default:
				raw, err := os.ReadFile(filepath.Join(root, CanonicalRootContractPath))
				if err != nil {
					t.Fatal(err)
				}
				var d map[string]any
				if err := yaml.Unmarshal(raw, &d); err != nil {
					t.Fatal(err)
				}
				switch name {
				case "empty":
					d["legacy_contracts"] = map[string]any{}
				case "null":
					d["legacy_contracts"] = nil
				case "missing_notes_reference":
					delete(d["legacy_contracts"].(map[string]any), "notes")
				case "native_owner":
					d["resources"].(map[string]any)["added"] = map[string]any{"kind": "knowledge", "knowledge": map[string]any{"category": "notes", "path": "notes"}}
				case "additional_disabled_policy":
					dmWrite(t, root, ".loom/contracts/additional-backup.yaml", []byte(strings.Replace(legacyReferenceBackup, "enabled: true", "enabled: false", 1)))
					d["resources"].(map[string]any)["additional"] = map[string]any{"kind": "protection", "protection": map[string]any{"policy_ref": ".loom/contracts/additional-backup.yaml", "path": "."}}
				case "missing_backup_root":
					delete(d["resources"].(map[string]any), "project_backup")
				}
				raw, err = yaml.Marshal(d)
				if err != nil {
					t.Fatal(err)
				}
				dmWrite(t, root, CanonicalRootContractPath, raw)
			}
			if Analyze(root).Report.OK {
				t.Fatal("invalid source/ref/owner accepted")
			}
		})
	}
}

func legacyReferenceParityVariants(t *testing.T) {
	for _, variant := range []string{"equal_alternates", "empty_providers", "paused_notes", "disabled_notes", "notes_modes", "legacy_notes_location"} {
		t.Run(variant, func(t *testing.T) {
			root, _ := legacyReferenceFixture(t)
			switch variant {
			case "equal_alternates":
				dmWrite(t, root, "notes/loom.notes.yaml", []byte("# retained equivalent\n"+legacyReferenceNotes))
				dmWrite(t, root, "repos/loom.repos.yaml", []byte("# retained equivalent\n"+legacyReferenceRepos))
			case "empty_providers":
				legacyReferenceChangeSource(t, root, ".loom/contracts/retained-project-v04.yaml", "{scripts_provider: project, workflows_provider: project}", "{}")
			case "paused_notes":
				legacyReferenceChangeSource(t, root, ".loom/contracts/notes.yaml", "status: draft", "status: paused")
			case "disabled_notes":
				legacyReferenceChangeSource(t, root, ".loom/contracts/notes.yaml", "status: draft", "status: disabled")
			case "notes_modes":
				legacyReferenceChangeSource(t, root, ".loom/contracts/notes.yaml", "sync: true, index: true, backup: true", "sync: false, index: false, backup: false")
			case "legacy_notes_location":
				if err := os.Rename(filepath.Join(root, ".loom/contracts/notes.yaml"), filepath.Join(root, "notes/loom.notes.yaml")); err != nil {
					t.Fatal(err)
				}
				legacyReferenceChangeSource(t, root, CanonicalRootContractPath, "ref: .loom/contracts/notes.yaml", "ref: notes/loom.notes.yaml")
			}
			active, err := os.ReadFile(filepath.Join(root, CanonicalRootContractPath))
			if err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(filepath.Join(root, ".loom/contracts/retained-project-v04.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			dmWrite(t, root, CanonicalRootContractPath, original)
			old := Analyze(root)
			dmWrite(t, root, CanonicalRootContractPath, active)
			current := Analyze(root)
			if !old.Report.OK || !current.Report.OK || current.Report.Registerable {
				t.Fatalf("valid source variant refused: %+v", current.Report.Diagnostics)
			}
			if !declarationJSONEqual(old.Report.DerivedProviders, current.Report.DerivedProviders) || len(old.Report.WatchedRoots) != len(current.Report.WatchedRoots) {
				t.Fatal("provider/watch count changed")
			}
			for i, r := range current.Report.WatchedRoots {
				delete(r.Metadata, "declaration_sources")
				if !declarationJSONEqual(r, old.Report.WatchedRoots[i]) {
					t.Fatal("full original owner configuration or attribution changed")
				}
			}
		})
	}
}
