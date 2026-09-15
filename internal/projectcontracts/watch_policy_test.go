package projectcontracts

import (
	"encoding/json"
	"strings"
	"testing"

	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/nodeagent/watchedroots"
)

func TestProjectMaterialWatchPolicyExplicitDeclarations(t *testing.T) {
	root := watchPolicyProjectFixture(t, "\nfacets:\n  notes: true\n")
	legacy := "kind: loom.notes\nschema_version: notes.contract.v0.3\nnotes:\n  status: draft\n  sync: true\n  index: true\n"
	writeFile(t, root, "notes/loom.notes.yaml", legacy, 0o600)
	before := Analyze(root)
	if !before.Report.OK {
		t.Fatal(before.Report.Diagnostics)
	}
	old := findWatchedRoot(t, before.Report.WatchedRoots, "watch_smoke__notes")
	material := "material:\n  - key: narrative\n    category: docs\n    path: docs\n    enabled: true\n  - key: research\n    category: research\n    path: research/papers\n    enabled: true\n"
	writeFile(t, root, "notes/loom.notes.yaml", legacy+material, 0o600)
	after := Analyze(root)
	if !after.Report.OK {
		t.Fatal(after.Report.Diagnostics)
	}
	if len(after.Report.WatchedRoots) != 3 {
		t.Fatalf("unexpected roots: %#v", after.Report.WatchedRoots)
	}
	if string(findWatchedRoot(t, after.Report.WatchedRoots, "watch_smoke__notes").ConfigJSON) != string(old.ConfigJSON) {
		t.Fatal("legacy config changed")
	}
	for _, key := range []string{"narrative", "research"} {
		item := findWatchedRoot(t, after.Report.WatchedRoots, "watch_smoke__"+key)
		if !containsString(item.SourceKinds, "project_material") || containsString(item.SourceKinds, "notes_contract") || item.BackupMode != "none" {
			t.Fatalf("bad material root: %#v", item)
		}
		if item.Metadata["knowledge_source"].(map[string]any)["enabled"] != true {
			t.Fatal("missing compiled declaration")
		}
	}
	for name, spec := range map[string]string{
		"broad":          "key: material\n    category: docs\n    path: .\n    enabled: true",
		"code":           "key: material\n    category: docs\n    path: repos\n    enabled: true",
		"escape":         "key: material\n    category: docs\n    path: docs/../../outside\n    enabled: true",
		"unknown":        "key: material\n    category: code\n    path: code\n    enabled: true",
		"legacy-overlap": "key: material\n    category: notes\n    path: notes/subdir\n    enabled: true",
		"duplicate-key":  "key: notes\n    category: docs\n    path: docs\n    enabled: true",
	} {
		t.Run(name, func(t *testing.T) {
			writeFile(t, root, "notes/loom.notes.yaml", legacy+"material:\n  - "+spec+"\n", 0o600)
			if Analyze(root).Report.OK {
				t.Fatal("accepted unsafe declaration")
			}
		})
	}
	writeFile(t, root, "notes/loom.notes.yaml", legacy+strings.ReplaceAll(material, "enabled: true", "enabled: false"), 0o600)
	disabled := Analyze(root)
	if !disabled.Report.OK || len(disabled.Report.WatchedRoots) != 1 {
		t.Fatalf("disabled material emitted roots: %#v", disabled.Report)
	}
}

func TestWatchPolicyScaffoldResearchProducesWatchedRoots(t *testing.T) {
	result, err := ScaffoldProject(ScaffoldOptions{
		Name:      "Research Notes",
		Slug:      "research-notes",
		OwnerNode: "workspace",
		Preset:    PresetResearch,
		Directory: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("ScaffoldProject returned error: %v", err)
	}
	analysis := Analyze(result.ProjectRoot)
	if !analysis.Report.OK {
		t.Fatalf("expected scaffold to validate, diagnostics=%#v", analysis.Report.Diagnostics)
	}
	if len(analysis.Report.Notes) != 1 {
		t.Fatalf("expected one notes item, got %#v", analysis.Report.Notes)
	}
	if len(analysis.Report.WatchedRoots) != 2 {
		t.Fatalf("expected notes and project watched roots, got %#v", analysis.Report.WatchedRoots)
	}
	notes := findWatchedRoot(t, analysis.Report.WatchedRoots, "research_notes__notes")
	if notes.SyncMode != "selected_files" || notes.IndexMode != "markdown_text" || notes.BackupMode != "incremental_raw" {
		t.Fatalf("unexpected notes watched root modes: %#v", notes)
	}
	if !containsString(notes.SourceKinds, "notes_contract") || !containsString(notes.SourceKinds, "sync_policy") {
		t.Fatalf("expected notes root to merge notes and sync sources: %#v", notes.SourceKinds)
	}
	if len(notes.AgentCommands) != 1 {
		t.Fatalf("expected exact apply-plan command, got %#v", notes.AgentCommands)
	}
	if shell := notes.AgentCommands[0].Shell; !strings.Contains(shell, "watched-roots apply-plan /tmp/loom-project-watch-plan.json") || !strings.Contains(shell, "--root research_notes__notes") {
		t.Fatalf("generated command should use exact apply-plan root filter, got %#v", notes.AgentCommands)
	}
	var config map[string]any
	if err := json.Unmarshal(notes.ConfigJSON, &config); err != nil {
		t.Fatalf("config json should decode: %v", err)
	}
	if config["schema_version"] != "watched_root.config.v0.2" {
		t.Fatalf("config schema = %#v", config["schema_version"])
	}
	project := findWatchedRoot(t, analysis.Report.WatchedRoots, "research_notes__project")
	if project.SyncMode != "none" || project.BackupMode != "incremental_raw" {
		t.Fatalf("unexpected project backup root modes: %#v", project)
	}
	assertPlanAction(t, analysis.Plan.Actions, "would_generate_watched_root_config", "research_notes__notes")
	assertPlanAction(t, analysis.Plan.Actions, "would_apply_node_agent_watched_root", "research_notes__project")
	payload, err := json.Marshal(analysis.Plan)
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}
	if !strings.Contains(string(payload), `"watched_roots"`) {
		t.Fatalf("plan json missing watched roots: %s", payload)
	}
}

func TestWatchPolicyModuleScaffoldProducesRepoBackupRoot(t *testing.T) {
	result, err := ScaffoldProject(ScaffoldOptions{
		Name:      "Project Cockpit Module",
		Slug:      "project-cockpit-module",
		OwnerNode: "main",
		Preset:    PresetModule,
		Directory: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("ScaffoldProject returned error: %v", err)
	}
	assertFile(t, result.ProjectRoot, ".loom/contracts/repos.yaml")
	analysis := Analyze(result.ProjectRoot)
	if !analysis.Report.OK {
		t.Fatalf("expected scaffold to validate, diagnostics=%#v", analysis.Report.Diagnostics)
	}
	repos := findWatchedRoot(t, analysis.Report.WatchedRoots, "project_cockpit_module__repos")
	if repos.SyncMode != "none" || repos.IndexMode != "none" || repos.BackupMode != "incremental_raw" {
		t.Fatalf("repo root should be backup-only by default: %#v", repos)
	}
	if len(repos.Exclude) != 0 {
		t.Fatalf("managed defaults must not be expanded into repo excludes: %#v", repos.Exclude)
	}
	var config watchedroots.RootConfig
	if err := json.Unmarshal(repos.ConfigJSON, &config); err != nil {
		t.Fatalf("decode repo config: %v", err)
	}
	if config.IgnorePolicy.Profile != string(filepolicy.ProfileManaged) || !config.IgnorePolicy.DiscoverUserRules || config.Scan.HiddenPolicy != watchedroots.HiddenPolicyPolicyControlled {
		t.Fatalf("unexpected repo ignore policy: %#v", config)
	}
}

func TestWatchPolicyLegacyNotesContractValidates(t *testing.T) {
	root := watchPolicyProjectFixture(t, `
facets:
  notes: true
`)
	writeFile(t, root, "notes/loom.notes.yaml", `
kind: loom.notes
schema_version: notes.contract.v0.3
notes:
  status: draft
  sync: true
  index: true
`, 0o600)
	analysis := Analyze(root)
	if !analysis.Report.OK {
		t.Fatalf("legacy notes contract should validate: %#v", analysis.Report.Diagnostics)
	}
	if len(analysis.Report.WatchedRoots) != 1 {
		t.Fatalf("expected one watched root, got %#v", analysis.Report.WatchedRoots)
	}
	notes := findWatchedRoot(t, analysis.Report.WatchedRoots, "watch_smoke__notes")
	if notes.BackupMode != "none" || notes.SyncMode != "selected_files" || notes.IndexMode != "markdown_text" {
		t.Fatalf("legacy notes contract should default to markdown sync/index without implicit backup: %#v", notes)
	}
	if !containsString(notes.Include, "**/*") {
		t.Fatalf("legacy notes contract should broadly include files for sync/catalog: %#v", notes.Include)
	}
}

func TestWatchPolicyDiagnostics(t *testing.T) {
	t.Run("missing notes contract warns", func(t *testing.T) {
		root := watchPolicyProjectFixture(t, `
facets:
  notes: true
`)
		mkdir(t, root, "notes")
		analysis := Analyze(root)
		if !analysis.Report.OK {
			t.Fatalf("missing notes contract should warn, not fail: %#v", analysis.Report.Diagnostics)
		}
		assertDiagnostic(t, analysis.Report.Diagnostics, "notes.contract_missing")
	})

	t.Run("invalid notes kind", func(t *testing.T) {
		root := watchPolicyProjectFixture(t, `
facets:
  notes: true
`)
		writeFile(t, root, "notes/loom.notes.yaml", strings.Replace(validNotesContract(), "kind: loom.notes", "kind: loom.bad", 1), 0o600)
		analysis := Analyze(root)
		if analysis.Report.OK {
			t.Fatal("expected invalid notes kind to fail")
		}
		assertDiagnostic(t, analysis.Report.Diagnostics, "notes.kind_invalid")
	})

	t.Run("invalid repos kind", func(t *testing.T) {
		root := watchPolicyProjectFixture(t, `
facets:
  repos: true
`)
		writeFile(t, root, "repos/loom.repos.yaml", strings.Replace(validReposContract(), "kind: loom.repos", "kind: loom.bad", 1), 0o600)
		analysis := Analyze(root)
		if analysis.Report.OK {
			t.Fatal("expected invalid repos kind to fail")
		}
		assertDiagnostic(t, analysis.Report.Diagnostics, "repos.kind_invalid")
	})

	t.Run("invalid sync policy kind", func(t *testing.T) {
		root := watchPolicyProjectFixture(t, `
facets:
  sync_policy: true
policies:
  sync: policies/sync.yaml
`)
		writeFile(t, root, "policies/sync.yaml", strings.Replace(validSyncPolicy("notes", "notes"), "kind: loom.project_sync_policy", "kind: loom.bad", 1), 0o600)
		analysis := Analyze(root)
		if analysis.Report.OK {
			t.Fatal("expected invalid sync policy kind to fail")
		}
		assertDiagnostic(t, analysis.Report.Diagnostics, "sync_policy.kind_invalid")
	})

	t.Run("invalid backup policy kind", func(t *testing.T) {
		root := watchPolicyProjectFixture(t, `
facets:
  backup_policy: true
policies:
  backup: policies/backup.yaml
`)
		writeFile(t, root, "policies/backup.yaml", strings.Replace(validBackupPolicy("project", "."), "kind: loom.project_backup_policy", "kind: loom.bad", 1), 0o600)
		analysis := Analyze(root)
		if analysis.Report.OK {
			t.Fatal("expected invalid backup policy kind to fail")
		}
		assertDiagnostic(t, analysis.Report.Diagnostics, "backup_policy.kind_invalid")
	})

	t.Run("path escape", func(t *testing.T) {
		root := watchPolicyProjectFixture(t, `
facets:
  notes: true
`)
		writeFile(t, root, "notes/loom.notes.yaml", strings.Replace(validNotesContract(), "path: .", "path: ../outside", 1), 0o600)
		analysis := Analyze(root)
		if analysis.Report.OK {
			t.Fatal("expected path escape to fail")
		}
		assertDiagnostic(t, analysis.Report.Diagnostics, "notes.path_unsafe")
	})

	t.Run("invalid pattern", func(t *testing.T) {
		root := watchPolicyProjectFixture(t, `
facets:
  notes: true
`)
		writeFile(t, root, "notes/loom.notes.yaml", strings.Replace(validNotesContract(), `- "**/*.md"`, `- "["`, 1), 0o600)
		analysis := Analyze(root)
		if analysis.Report.OK {
			t.Fatal("expected invalid include pattern to fail")
		}
		assertDiagnostic(t, analysis.Report.Diagnostics, "notes.include_invalid")
	})

	t.Run("markdown index without sync", func(t *testing.T) {
		root := watchPolicyProjectFixture(t, `
facets:
  sync_policy: true
policies:
  sync: policies/sync.yaml
`)
		writeFile(t, root, "policies/sync.yaml", `
kind: loom.project_sync_policy
schema_version: sync.policy.v0.3
sync:
  enabled: true
  roots:
    - key: notes
      path: notes
      mode: none
      index: true
`, 0o600)
		analysis := Analyze(root)
		if analysis.Report.OK {
			t.Fatal("expected markdown index without sync to fail")
		}
		assertDiagnostic(t, analysis.Report.Diagnostics, "watched_root.config_invalid")
	})

	t.Run("duplicate incompatible roots", func(t *testing.T) {
		root := watchPolicyProjectFixture(t, `
facets:
  sync_policy: true
policies:
  sync: policies/sync.yaml
`)
		writeFile(t, root, "policies/sync.yaml", validSyncPolicy("notes", "notes")+`
    - key: notes
      path: docs
      mode: markdown
      index: markdown_text
`, 0o600)
		analysis := Analyze(root)
		if analysis.Report.OK {
			t.Fatal("expected duplicate incompatible root to fail")
		}
		assertDiagnostic(t, analysis.Report.Diagnostics, "watched_root.duplicate_conflict")
	})
}

func TestProjectWatchedRootKeyIsStableAndBounded(t *testing.T) {
	got := ProjectWatchedRootKey("gmail-automation", "notes")
	if got != "gmail_automation__notes" {
		t.Fatalf("ProjectWatchedRootKey = %q", got)
	}
	long := ProjectWatchedRootKey("project-"+strings.Repeat("a", 100), "notes_"+strings.Repeat("b", 100))
	if len(long) > maxProjectWatchedRootKeyLength {
		t.Fatalf("key length = %d, want <= %d: %q", len(long), maxProjectWatchedRootKeyLength, long)
	}
	if long != ProjectWatchedRootKey("project-"+strings.Repeat("a", 100), "notes_"+strings.Repeat("b", 100)) {
		t.Fatal("watched-root key should be stable")
	}
}

func watchPolicyProjectFixture(t *testing.T, extra string) string {
	t.Helper()
	return projectFixture(t, `
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: watch-smoke
  name: Watch Smoke
  owner_node: main
`+extra)
}

func validNotesContract() string {
	return `
kind: loom.notes
schema_version: notes.contract.v0.3
notes:
  status: draft
  sync: true
  index: true
  backup: false
  root_key: notes
  path: .
  include:
    - "**/*.md"
  exclude:
    - ".obsidian/**"
`
}

func validReposContract() string {
	return `
kind: loom.repos
schema_version: repos.contract.v0.3
repos:
  status: draft
  roots:
    - key: repos
      path: .
`
}

func validSyncPolicy(key, path string) string {
	return `
kind: loom.project_sync_policy
schema_version: sync.policy.v0.3
sync:
  enabled: true
  defaults:
    safe_root: project
    mode: markdown
    index: markdown_text
  roots:
    - key: ` + key + `
      path: ` + path + `
      include:
        - "**/*.md"
      exclude:
        - ".obsidian/**"
`
}

func validBackupPolicy(key, path string) string {
	return `
kind: loom.project_backup_policy
schema_version: backup.policy.v0.3
backup:
  enabled: true
  roots:
    - key: ` + key + `
      path: ` + path + `
      mode: incremental_raw
`
}

func findWatchedRoot(t *testing.T, roots []ProjectWatchedRootItem, key string) ProjectWatchedRootItem {
	t.Helper()
	for _, root := range roots {
		if root.BackendRootKey == key {
			return root
		}
	}
	t.Fatalf("missing watched root %s in %#v", key, roots)
	return ProjectWatchedRootItem{}
}
