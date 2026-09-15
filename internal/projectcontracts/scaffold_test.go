package projectcontracts

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	"loom.local/loom/internal/modules"
	"loom.local/loom/internal/scripts"
)

func TestSupportedPresetsAreDeterministic(t *testing.T) {
	presets := SupportedPresets()
	names := []string{}
	for _, preset := range presets {
		names = append(names, preset.Name)
		if len(preset.Facets) == 0 {
			t.Fatalf("preset %s has no facets", preset.Name)
		}
		normalized, err := NormalizeFacetList(preset.Facets)
		if err != nil {
			t.Fatalf("preset %s has invalid facets: %v", preset.Name, err)
		}
		if strings.Join(normalized, ",") != strings.Join(preset.Facets, ",") {
			t.Fatalf("preset %s facets are not canonical: %#v", preset.Name, preset.Facets)
		}
	}
	want := "minimal,research,automation,connector,module"
	if got := strings.Join(names, ","); got != want {
		t.Fatalf("preset order = %s, want %s", got, want)
	}
}

func TestDeriveProjectSlug(t *testing.T) {
	tests := map[string]string{
		"Gmail Automation":     "gmail-automation",
		"Obsidian: Connector!": "obsidian-connector",
		"  Many   Spaces  ":    "many-spaces",
	}
	for input, want := range tests {
		if got := DeriveProjectSlug(input); got != want {
			t.Fatalf("DeriveProjectSlug(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestScaffoldMinimalProjectValidates(t *testing.T) {
	dir := t.TempDir()
	result, err := ScaffoldProject(ScaffoldOptions{
		Name:      "Neuroscience Notes",
		OwnerNode: "macbook",
		Preset:    PresetMinimal,
		Directory: dir,
	})
	if err != nil {
		t.Fatalf("ScaffoldProject returned error: %v", err)
	}
	if !result.OK || !result.Validation.OK {
		t.Fatalf("scaffold should be ok: %#v", result)
	}
	assertFile(t, result.ProjectRoot, CanonicalRootContractPath)
	assertFile(t, result.ProjectRoot, "README.md")
	assertFile(t, result.ProjectRoot, "AGENTS.md")
	assertFile(t, result.ProjectRoot, ".loom/.gitignore")
	assertFile(t, result.ProjectRoot, ".loom/agents/project.md")
	assertFile(t, result.ProjectRoot, ".loom/agents/surfaces/notes.md")
	assertFile(t, result.ProjectRoot, ".loom/tools/validate-project.sh")
	assertFile(t, result.ProjectRoot, ".loom/contracts/notes.yaml")
	assertFile(t, result.ProjectRoot, ".loom/templates/note.md")
	assertFile(t, result.ProjectRoot, ".loom/templates/dated-file.yaml")
	assertFile(t, result.ProjectRoot, ".loom/templates/dataset.yaml")
	assertFile(t, result.ProjectRoot, ".loom/agent-packs/repo-development/README.md")
	assertFile(t, result.ProjectRoot, ".loom/agent-packs/repo-development/templates/.repo/repo.yaml")
	assertNoFile(t, result.ProjectRoot, LegacyRootContractPath)
	assertNoFile(t, result.ProjectRoot, ".repo")
	assertNoFile(t, result.ProjectRoot, "notes/README.md")
	assertNoFile(t, result.ProjectRoot, "notes/AGENTS.md")
	assertNoFile(t, result.ProjectRoot, "policies")
	analysis := Analyze(result.ProjectRoot)
	if !analysis.Report.OK {
		t.Fatalf("generated project did not validate: %#v", analysis.Report.Diagnostics)
	}
}

func TestScaffoldIncludesOptionalRepositoryDevelopmentPackWithoutInstallingIt(t *testing.T) {
	result, err := ScaffoldProject(ScaffoldOptions{
		Name: "Repository Pack", OwnerNode: "main", Preset: PresetModule, Directory: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("scaffold repository pack project: %v", err)
	}

	const packRoot = ".loom/agent-packs/repo-development"
	wantFiles := []string{
		"README.md",
		"templates/.repo/README.md",
		"templates/.repo/REPOSITORY.md",
		"templates/.repo/STATE.md",
		"templates/.repo/ROADMAP.md",
		"templates/.repo/repo.yaml",
		"templates/.repo/protocols/REPOSITORY_STATE_PROTOCOL.md",
		"templates/.repo/protocols/WORKTREE_OWNERSHIP.md",
		"templates/.repo/protocols/CODEX_WORKFLOW.md",
		"templates/.repo/protocols/ORCA_WORKFLOW.md",
		"templates/.repo/protocols/SLICE_AUTOPILOT_PROTOCOL.md",
		"templates/.repo/protocols/CODE_REVIEW_PROTOCOL.md",
		"templates/.repo/templates/future/future.yaml",
		"templates/.repo/templates/initiative/initiative.yaml",
		"templates/.repo/templates/feature/feature.yaml",
		"templates/.repo/templates/feature/implementation_slices.md",
		"templates/.repo/templates/feature/worktree_progress.md",
		"templates/.repo/templates/feature/handoff.md",
		"templates/.repo/templates/decision/decision.yaml",
		"templates/.repo/templates/release/release.yaml",
	}
	for _, relative := range wantFiles {
		assertFile(t, result.ProjectRoot, packRoot+"/"+relative)
	}
	assertNoFile(t, result.ProjectRoot, ".repo")
	assertNoFile(t, result.ProjectRoot, "repos/.repo")

	manifest := readFile(t, result.ProjectRoot, packRoot+"/templates/.repo/repo.yaml")
	for _, want := range []string{
		"schema_version: repo.state.v1",
		"id: " + result.ProjectID,
		"slug: " + result.Slug,
		"tracking: git",
		"state_root: .repo",
		"<repository-id>",
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("repository manifest template missing %q:\n%s", want, manifest)
		}
	}

	rootAgents := readFile(t, result.ProjectRoot, "AGENTS.md")
	projectAgents := readFile(t, result.ProjectRoot, ".loom/agents/project.md")
	for name, content := range map[string]string{"root AGENTS.md": rootAgents, "project guidance": projectAgents} {
		for _, want := range []string{packRoot + "/", "repository-leading agent", "explicit"} {
			if !strings.Contains(content, want) {
				t.Fatalf("%s missing opt-in guidance %q:\n%s", name, want, content)
			}
		}
	}

	ownership := readFile(t, result.ProjectRoot, packRoot+"/templates/.repo/protocols/WORKTREE_OWNERSHIP.md")
	for _, want := range []string{"One harness owns each worktree", "Neither harness adopts", "Git branch", "never canonical repository state"} {
		if !strings.Contains(ownership, want) {
			t.Fatalf("worktree ownership protocol missing %q:\n%s", want, ownership)
		}
	}
	codex := readFile(t, result.ProjectRoot, packRoot+"/templates/.repo/protocols/CODEX_WORKFLOW.md")
	orca := readFile(t, result.ProjectRoot, packRoot+"/templates/.repo/protocols/ORCA_WORKFLOW.md")
	if !strings.Contains(codex, "Codex-native") || !strings.Contains(codex, "`openai-docs`") || !strings.Contains(codex, "does not duplicate product commands") || strings.Contains(codex, "```sh") {
		t.Fatalf("Codex guidance must delegate command details to native guidance:\n%s", codex)
	}
	for _, want := range []string{"`orca-cli`", "`orchestration`", "version-matched guidance", "does not\nduplicate subcommands or flags"} {
		if !strings.Contains(orca, want) {
			t.Fatalf("ORCA guidance missing native pointer %q:\n%s", want, orca)
		}
	}
	if strings.Contains(orca, "```sh") {
		t.Fatalf("ORCA guidance must not duplicate command documentation:\n%s", orca)
	}

	err = filepath.WalkDir(filepath.Join(result.ProjectRoot, filepath.FromSlash(packRoot)), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		content := string(raw)
		if strings.HasSuffix(entry.Name(), ".yaml") {
			var decoded any
			if decodeErr := yaml.Unmarshal(raw, &decoded); decodeErr != nil {
				t.Fatalf("pack YAML template %s is malformed: %v", path, decodeErr)
			}
		}
		for _, forbidden := range []string{"/Users/", "/home/", "file://", "session_id:", "terminal_id:"} {
			if strings.Contains(content, forbidden) {
				t.Fatalf("pack file %s contains non-portable value %q", path, forbidden)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("inspect repository development pack: %v", err)
	}

	packFiles := 0
	for _, file := range result.Files {
		if file.Kind == "repository_development_pack" {
			packFiles++
			if !strings.HasPrefix(file.Path, packRoot+"/") {
				t.Fatalf("repository development pack escaped target root: %#v", file)
			}
		}
	}
	if packFiles < len(wantFiles) {
		t.Fatalf("scaffold reported %d repository development pack files, want at least %d", packFiles, len(wantFiles))
	}
}

func TestProjectContentTemplatesPreservePlaceholdersAndRenderOneInstant(t *testing.T) {
	result, err := ScaffoldProject(ScaffoldOptions{
		Name: "Dated Content", OwnerNode: "main", Preset: PresetResearch, Directory: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("ScaffoldProject returned error: %v", err)
	}
	fixed := time.Date(2026, 8, 17, 14, 15, 16, 987654321, time.FixedZone("CEST", 2*60*60))
	clockCalls := 0
	clock := func() time.Time {
		clockCalls++
		return fixed
	}
	wantTimestamp := "2026-08-17T12:15:16Z"
	for _, test := range []struct {
		path  string
		input ProjectContentTemplateInput
	}{
		{path: ".loom/templates/note.md", input: ProjectContentTemplateInput{Title: "Field Notes"}},
		{path: ".loom/templates/dated-file.yaml", input: ProjectContentTemplateInput{Path: "files/evidence.txt"}},
		{path: ".loom/templates/dataset.yaml", input: ProjectContentTemplateInput{Title: "Signals", DatasetKey: "signals"}},
	} {
		source := readFile(t, result.ProjectRoot, test.path)
		if !strings.Contains(source, ".CreatedAt") || !strings.Contains(source, ".UpdatedAt") {
			t.Fatalf("template %s expanded timestamps during project scaffold:\n%s", test.path, source)
		}
		beforeCalls := clockCalls
		rendered, err := RenderProjectContentTemplate(test.path, source, test.input, clock)
		if err != nil {
			t.Fatalf("RenderProjectContentTemplate(%s): %v", test.path, err)
		}
		if clockCalls != beforeCalls+1 {
			t.Fatalf("template %s clock calls = %d, want one additional call", test.path, clockCalls-beforeCalls)
		}
		assertRenderedTemplateTimes(t, test.path, rendered, wantTimestamp)
		repeated, err := RenderProjectContentTemplate(test.path, source, test.input, func() time.Time { return fixed })
		if err != nil {
			t.Fatalf("repeat RenderProjectContentTemplate(%s): %v", test.path, err)
		}
		if repeated != rendered {
			t.Fatalf("fixed-clock rendering is not deterministic for %s", test.path)
		}
	}
	analysis := Analyze(result.ProjectRoot)
	if !analysis.Report.OK {
		t.Fatalf("project with portable content templates did not validate: %#v", analysis.Report.Diagnostics)
	}
}

func assertRenderedTemplateTimes(t *testing.T, name, rendered, want string) {
	t.Helper()
	payload := rendered
	if strings.HasSuffix(name, ".md") {
		parts := strings.SplitN(rendered, "---\n", 3)
		if len(parts) != 3 {
			t.Fatalf("rendered Markdown %s has invalid frontmatter:\n%s", name, rendered)
		}
		payload = parts[1]
	}
	var decoded map[string]any
	if err := yaml.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("rendered template %s is invalid YAML: %v\n%s", name, err, rendered)
	}
	for _, field := range []string{"created_at", "updated_at"} {
		value, ok := decoded[field].(string)
		if !ok || value != want {
			t.Fatalf("rendered template %s %s = %#v, want %s", name, field, decoded[field], want)
		}
		if _, err := time.Parse(time.RFC3339, value); err != nil {
			t.Fatalf("rendered template %s %s is not RFC3339: %v", name, field, err)
		}
	}
}

func TestScaffoldPresetsUseCanonicalHumanFirstLayout(t *testing.T) {
	for _, preset := range []string{PresetMinimal, PresetResearch, PresetAutomation, PresetConnector, PresetModule} {
		t.Run(preset, func(t *testing.T) {
			result, err := ScaffoldProject(ScaffoldOptions{
				Name:      "Layout " + preset,
				OwnerNode: "main",
				Preset:    preset,
				Directory: t.TempDir(),
			})
			if err != nil {
				t.Fatalf("ScaffoldProject returned error: %v", err)
			}
			assertFile(t, result.ProjectRoot, CanonicalRootContractPath)
			assertNoFile(t, result.ProjectRoot, LegacyRootContractPath)
			assertNoFile(t, result.ProjectRoot, "policies")
			assertNoFile(t, result.ProjectRoot, "tests/validate_project.sh")
			if got := readFile(t, result.ProjectRoot, ".loom/.gitignore"); got != "state/\ntmp/\n" {
				t.Fatalf("unexpected .loom/.gitignore: %q", got)
			}
			if agents := readFile(t, result.ProjectRoot, "AGENTS.md"); !strings.Contains(agents, "loom-managed: project-agent-entry/v1") || !strings.Contains(agents, ".loom/agents/project.md") {
				t.Fatalf("root AGENTS.md is not the managed entry point:\n%s", agents)
			}
			for _, facet := range result.Facets {
				assertFile(t, result.ProjectRoot, ".loom/agents/surfaces/"+facet+".md")
				if folder := facetFolders[facet]; folder != "" && !strings.Contains(folder, "/") {
					assertNoFile(t, result.ProjectRoot, folder+"/AGENTS.md")
					assertNoFile(t, result.ProjectRoot, folder+"/README.md")
				}
			}
		})
	}
}

func TestScaffoldRootAgentInstructionsAreGeneric(t *testing.T) {
	first, err := ScaffoldProject(ScaffoldOptions{
		Name: "Research Alpha", OwnerNode: "main", Preset: PresetResearch, Directory: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("scaffold first project: %v", err)
	}
	second, err := ScaffoldProject(ScaffoldOptions{
		Name: "Automation Beta", OwnerNode: "macbook", Preset: PresetAutomation, Directory: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("scaffold second project: %v", err)
	}
	firstAgents := readFile(t, first.ProjectRoot, "AGENTS.md")
	secondAgents := readFile(t, second.ProjectRoot, "AGENTS.md")
	if firstAgents != secondAgents {
		t.Fatalf("root instructions must be byte-identical across projects\nfirst:\n%s\nsecond:\n%s", firstAgents, secondAgents)
	}
	for _, want := range []string{
		".loom/project.yaml", ".loom/agents/project.md", ".loom/agents/surfaces/",
		".loom/agent-packs/repo-development/", "repository-leading agent", "never copy, update, or apply",
		"search-loom-docs", "manage-loom-projects", "loom project validate .",
		"loom project plan .", "canonical project sources only", "Proton Pass", "logical references",
	} {
		if !strings.Contains(firstAgents, want) {
			t.Fatalf("root AGENTS.md missing %q:\n%s", want, firstAgents)
		}
	}
	if strings.Contains(firstAgents, "pass://") {
		t.Fatalf("root AGENTS.md must not claim deferred Proton bindings are implemented:\n%s", firstAgents)
	}
	firstProject := readFile(t, first.ProjectRoot, ".loom/agents/project.md")
	for _, want := range []string{"research-alpha", "main", "Lifecycle state: `draft`", "Enabled facets:", ".loom/agent-packs/repo-development/", "explicitly opt in", "Never apply or upgrade"} {
		if !strings.Contains(firstProject, want) {
			t.Fatalf("project guidance missing %q:\n%s", want, firstProject)
		}
	}
}

func TestScaffoldProjectFilesAreGroupWritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are platform-specific")
	}
	dir := t.TempDir()
	result, err := ScaffoldProject(ScaffoldOptions{
		Name:      "Shared Project",
		OwnerNode: "main",
		Preset:    PresetAutomation,
		Directory: dir,
	})
	if err != nil {
		t.Fatalf("ScaffoldProject returned error: %v", err)
	}
	for _, rel := range []string{".", "scripts", "scripts/hello_world"} {
		info, err := os.Stat(filepath.Join(result.ProjectRoot, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("stat %s: %v", rel, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s should be a directory", rel)
		}
		if info.Mode().Perm()&0o020 == 0 {
			t.Fatalf("expected %s to be group-writable, mode=%s", rel, info.Mode())
		}
	}
	for _, rel := range []string{CanonicalRootContractPath, "README.md", "scripts/hello_world/loom.script.yaml"} {
		info, err := os.Stat(filepath.Join(result.ProjectRoot, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("stat %s: %v", rel, err)
		}
		if info.Mode().Perm()&0o020 == 0 {
			t.Fatalf("expected %s to be group-writable, mode=%s", rel, info.Mode())
		}
	}
	info, err := os.Stat(filepath.Join(result.ProjectRoot, "scripts", "hello_world", "run.sh"))
	if err != nil {
		t.Fatalf("stat executable: %v", err)
	}
	if info.Mode().Perm()&0o030 != 0o030 {
		t.Fatalf("expected executable scaffold file to be group-writable/executable, mode=%s", info.Mode())
	}
}

func TestEnsureScaffoldDirectoryAllowsAccessibleSharedRootWhenChmodDenied(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, ".loom", "contracts")
	chmod := func(path string, mode fs.FileMode) error {
		if path == root {
			return fs.ErrPermission
		}
		return os.Chmod(path, mode)
	}

	if err := ensureScaffoldDirectoryWithChmod(root, target, chmod); err != nil {
		t.Fatalf("accessible shared root should not require ownership: %v", err)
	}
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		t.Fatalf("target directory was not created: info=%v err=%v", info, err)
	}
}

func TestEnsureScaffoldDirectoryRejectsInaccessibleRootWhenChmodDenied(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatalf("make root read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })

	err := ensureScaffoldDirectoryWithChmod(root, root, func(string, fs.FileMode) error {
		return fs.ErrPermission
	})
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("inaccessible root error = %v, want permission denied", err)
	}
}

func TestScaffoldResultCarriesDirectoryMetadata(t *testing.T) {
	dir := t.TempDir()
	result, err := ScaffoldProject(ScaffoldOptions{
		Name:            "Box Metadata",
		OwnerNode:       "macbook",
		Preset:          PresetMinimal,
		Directory:       filepath.Join(dir, "Projects"),
		DirectorySource: ScaffoldDirectoryBox,
		BoxDefaultUsed:  true,
		BoxRoot:         dir,
		BoxProfile:      "workspace",
		BoxContractPath: filepath.Join(dir, ".loom", "box.yaml"),
	})
	if err != nil {
		t.Fatalf("ScaffoldProject returned error: %v", err)
	}
	if !result.BoxDefault || result.DirSource != ScaffoldDirectoryBox {
		t.Fatalf("expected Box directory metadata: %#v", result)
	}
	if result.BoxRoot != dir || result.BoxProfile != "workspace" || result.ParentDir != filepath.Join(dir, "Projects") {
		t.Fatalf("unexpected scaffold metadata: %#v", result)
	}
	if result.ProjectRoot != filepath.Join(dir, "Projects", "box-metadata") {
		t.Fatalf("unexpected project root: %s", result.ProjectRoot)
	}
}

func TestScaffoldAutomationPresetWritesExpectedKits(t *testing.T) {
	dir := t.TempDir()
	result, err := ScaffoldProject(ScaffoldOptions{
		Name:      "Gmail Automation",
		OwnerNode: "main",
		Preset:    PresetAutomation,
		Directory: dir,
	})
	if err != nil {
		t.Fatalf("ScaffoldProject returned error: %v", err)
	}
	for _, path := range []string{
		"scripts/hello_world/loom.script.yaml",
		"scripts/hello_world/loom.exposure.yaml",
		"schedules/example_schedule/loom.schedule.yaml",
		"direct_events/example_event/loom.direct_event.yaml",
		".loom/contracts/sync.yaml",
		".loom/contracts/backup.yaml",
		".loom/contracts/workers.yaml",
		".loom/contracts/credentials.yaml",
		".loom/agents/surfaces/secrets.md",
		".loom/tools/validate-project.sh",
	} {
		assertFile(t, result.ProjectRoot, path)
	}
	assertExecutable(t, result.ProjectRoot, "scripts/hello_world/run.sh")
	if _, err := scripts.LoadManifest(filepath.Join(result.ProjectRoot, "scripts", "hello_world", "loom.script.yaml")); err != nil {
		t.Fatalf("scaffolded script manifest should validate: %v", err)
	}
	exposure := readFile(t, result.ProjectRoot, "scripts/hello_world/loom.exposure.yaml")
	if !strings.Contains(exposure, "wait_timeout_seconds: 120") {
		t.Fatalf("scaffolded script exposure should wait long enough for the default job runner tick:\n%s", exposure)
	}
	contract := readFile(t, result.ProjectRoot, CanonicalRootContractPath)
	if !strings.Contains(contract, "direct_events: true") || !strings.Contains(contract, "credentials: .loom/contracts/credentials.yaml") {
		t.Fatalf("root contract missing expected automation fields:\n%s", contract)
	}
	backupPolicy := readFile(t, result.ProjectRoot, ".loom/contracts/backup.yaml")
	if !strings.Contains(backupPolicy, "- .loom/project.yaml") || strings.Contains(backupPolicy, "- loom.project.yaml") {
		t.Fatalf("backup policy should include the canonical root contract only:\n%s", backupPolicy)
	}
}

func TestScaffoldConnectorAndModulePresets(t *testing.T) {
	dir := t.TempDir()
	connector, err := ScaffoldProject(ScaffoldOptions{
		Name:      "Obsidian Connector",
		OwnerNode: "macbook",
		Preset:    PresetConnector,
		Directory: dir,
	})
	if err != nil {
		t.Fatalf("connector scaffold returned error: %v", err)
	}
	assertFile(t, connector.ProjectRoot, "connectors/example_connector/loom.connector.yaml")
	assertFile(t, connector.ProjectRoot, "connectors/example_connector/scripts/ping/loom.script.yaml")
	assertExecutable(t, connector.ProjectRoot, "connectors/example_connector/scripts/ping/run.sh")
	analysis := Analyze(connector.ProjectRoot)
	if !analysis.Report.OK {
		t.Fatalf("connector scaffold should validate: %#v", analysis.Report.Diagnostics)
	}
	if len(analysis.Report.Connectors) != 1 || analysis.Report.Connectors[0].ProviderAddress != "macbook@example_connector" {
		t.Fatalf("connector scaffold not discovered: %#v", analysis.Report.Connectors)
	}

	module, err := ScaffoldProject(ScaffoldOptions{
		Name:      "Project Cockpit Module",
		OwnerNode: "main",
		Preset:    PresetModule,
		Directory: dir,
	})
	if err != nil {
		t.Fatalf("module scaffold returned error: %v", err)
	}
	assertFile(t, module.ProjectRoot, "modules/example_module/module.json")
	assertFile(t, module.ProjectRoot, "modules/example_module/loom.module_project.yaml")
	if _, err := modules.LoadPackage(filepath.Join(module.ProjectRoot, "modules", "example_module")); err != nil {
		t.Fatalf("scaffolded module package should validate: %v", err)
	}
	analysis = Analyze(module.ProjectRoot)
	if !analysis.Report.OK {
		t.Fatalf("module scaffold should validate: %#v", analysis.Report.Diagnostics)
	}
	if len(analysis.Report.Modules) != 1 {
		t.Fatalf("module scaffold not discovered: %#v", analysis.Report.Modules)
	}
	if got := analysis.Report.Modules[0].ModuleID; got != "loom.project-cockpit-module" {
		t.Fatalf("module id = %q", got)
	}
	if !analysis.Report.Modules[0].RegistrationEnabled {
		t.Fatalf("scaffolded module should be project-registrable: %#v", analysis.Report.Modules[0])
	}
	if !strings.Contains(readFile(t, module.ProjectRoot, "modules/example_module/loom.module_project.yaml"), "schema_version: module_project.contract.v0.3") {
		t.Fatal("module project wrapper should use the canonical schema version")
	}
}

func TestScaffoldWorkflowFacetWritesDiscoverableExecutable(t *testing.T) {
	dir := t.TempDir()
	result, err := ScaffoldProject(ScaffoldOptions{
		Name:      "Workflow Project",
		Slug:      "workflow-project",
		OwnerNode: "main",
		Facets:    []string{"workflows"},
		Directory: dir,
	})
	if err != nil {
		t.Fatalf("workflow scaffold returned error: %v", err)
	}
	assertFile(t, result.ProjectRoot, "workflows/example_workflow/loom.workflow.yaml")
	assertExecutable(t, result.ProjectRoot, "workflows/example_workflow/run.sh")
	analysis := Analyze(result.ProjectRoot)
	if !analysis.Report.OK {
		t.Fatalf("workflow scaffold should validate: %#v", analysis.Report.Diagnostics)
	}
	if len(analysis.Report.Workflows) != 1 {
		t.Fatalf("workflow scaffold not discovered: %#v", analysis.Report.Workflows)
	}
	workflow := analysis.Report.Workflows[0]
	if workflow.WorkflowID != "example_workflow" || workflow.ImplementationKind != WorkflowImplementationWorkflow || !workflow.Executable {
		t.Fatalf("unexpected scaffold workflow: %#v", workflow)
	}
	if workflow.ActivationStatus != WorkflowActivationStatusExecutable {
		t.Fatalf("workflow should be executable activation candidate: %#v", workflow)
	}
}

func TestScaffoldExplicitFacetsOverridePreset(t *testing.T) {
	dir := t.TempDir()
	result, err := ScaffoldProject(ScaffoldOptions{
		Name:      "Small Script",
		OwnerNode: "main",
		Preset:    PresetAutomation,
		Facets:    []string{"notes", "scripts", "tests"},
		Directory: dir,
	})
	if err != nil {
		t.Fatalf("ScaffoldProject returned error: %v", err)
	}
	if got := strings.Join(result.Facets, ","); got != "notes,scripts,tests" {
		t.Fatalf("facets = %s", got)
	}
	assertFile(t, result.ProjectRoot, "scripts/hello_world/loom.script.yaml")
	assertNoFile(t, result.ProjectRoot, "direct_events/example_event/loom.direct_event.yaml")
}

func TestScaffoldRefusesOverwriteUnlessForced(t *testing.T) {
	dir := t.TempDir()
	result, err := ScaffoldProject(ScaffoldOptions{
		Name:      "Existing Project",
		OwnerNode: "main",
		Directory: dir,
	})
	if err != nil {
		t.Fatalf("initial scaffold returned error: %v", err)
	}
	if _, err := ScaffoldProject(ScaffoldOptions{
		Name:      "Existing Project",
		OwnerNode: "main",
		Directory: dir,
	}); err == nil {
		t.Fatal("expected scaffold to refuse overwrite")
	}
	forced, err := ScaffoldProject(ScaffoldOptions{
		Name:      "Existing Project",
		OwnerNode: "main",
		Directory: dir,
		Force:     true,
	})
	if err != nil {
		t.Fatalf("forced scaffold returned error: %v", err)
	}
	if !hasFileAction(forced.Files, CanonicalRootContractPath, "overwritten") {
		t.Fatalf("forced scaffold should overwrite root contract: %#v", forced.Files)
	}
	if result.ProjectRoot != forced.ProjectRoot {
		t.Fatalf("forced scaffold root changed: %s != %s", result.ProjectRoot, forced.ProjectRoot)
	}
}

func TestScaffoldPreflightsConflictsBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "partial-project")
	if err := os.MkdirAll(filepath.Join(root, ".loom", "contracts"), 0o755); err != nil {
		t.Fatalf("mkdir conflict parent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".loom", "contracts", "notes.yaml"), []byte("existing\n"), 0o644); err != nil {
		t.Fatalf("write conflict: %v", err)
	}
	if _, err := ScaffoldProject(ScaffoldOptions{
		Name:      "Partial Project",
		OwnerNode: "main",
		Directory: dir,
	}); err == nil {
		t.Fatal("expected scaffold to fail before writing")
	}
	assertNoFile(t, root, "README.md")
	assertNoFile(t, root, "AGENTS.md")
}

func TestScaffoldDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	result, err := ScaffoldProject(ScaffoldOptions{
		Name:      "Dry Run Project",
		OwnerNode: "main",
		Directory: dir,
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("dry run returned error: %v", err)
	}
	if len(result.Files) == 0 {
		t.Fatal("dry run should report planned files")
	}
	if _, err := os.Stat(result.ProjectRoot); !os.IsNotExist(err) {
		t.Fatalf("dry run should not create project root, stat err=%v", err)
	}
	if result.Validation.State != ScaffoldValidationPlannedOnly {
		t.Fatalf("dry run validation state = %q, want %q", result.Validation.State, ScaffoldValidationPlannedOnly)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal dry-run result: %v", err)
	}
	if strings.Contains(string(raw), `"validation":{"state":"planned_only","ok":false`) || strings.Contains(string(raw), `"ok":false,"errors":0,"warnings":0`) {
		t.Fatalf("dry-run JSON should not report validation ok=false with no diagnostics: %s", raw)
	}
}

func TestScaffoldDryRunValidatesExistingContract(t *testing.T) {
	dir := t.TempDir()
	if _, err := ScaffoldProject(ScaffoldOptions{
		Name:      "Existing Dry Run",
		OwnerNode: "main",
		Directory: dir,
	}); err != nil {
		t.Fatalf("initial scaffold returned error: %v", err)
	}
	result, err := ScaffoldProject(ScaffoldOptions{
		Name:      "Existing Dry Run",
		OwnerNode: "main",
		Directory: dir,
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("dry-run scaffold returned error: %v", err)
	}
	if result.Validation.State != ScaffoldValidationPassed || !result.Validation.OK || result.Validation.Errors != 0 {
		t.Fatalf("dry-run should validate existing contract: %#v", result.Validation)
	}
}

func TestAddProjectFacetsAddsScriptsWithoutOverwritingRootDocs(t *testing.T) {
	dir := t.TempDir()
	scaffold, err := ScaffoldProject(ScaffoldOptions{
		Name:      "Docs Only",
		OwnerNode: "main",
		Directory: dir,
		Facets:    []string{"docs"},
	})
	if err != nil {
		t.Fatalf("scaffold docs-only project: %v", err)
	}
	customReadme := "custom project readme\n"
	customAgents := "custom project agents\n"
	if err := os.WriteFile(filepath.Join(scaffold.ProjectRoot, "README.md"), []byte(customReadme), 0o644); err != nil {
		t.Fatalf("customize readme: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scaffold.ProjectRoot, "AGENTS.md"), []byte(customAgents), 0o644); err != nil {
		t.Fatalf("customize agents: %v", err)
	}

	result, err := AddProjectFacets(AddProjectFacetsOptions{ProjectRoot: scaffold.ProjectRoot, Facets: []string{"scripts"}})
	if err != nil {
		t.Fatalf("add scripts facet: %v", err)
	}
	if !containsString(result.AddedFacets, "scripts") {
		t.Fatalf("added facets = %#v", result.AddedFacets)
	}
	assertFile(t, scaffold.ProjectRoot, "scripts/hello_world/loom.script.yaml")
	assertFile(t, scaffold.ProjectRoot, "scripts/hello_world/loom.exposure.yaml")
	if got := readFile(t, scaffold.ProjectRoot, "README.md"); got != customReadme {
		t.Fatalf("root README was overwritten:\n%s", got)
	}
	if got := readFile(t, scaffold.ProjectRoot, "AGENTS.md"); got != customAgents {
		t.Fatalf("root AGENTS was overwritten:\n%s", got)
	}
	analysis := Analyze(scaffold.ProjectRoot)
	if !analysis.Report.OK {
		t.Fatalf("project should validate after adding scripts: %#v", analysis.Report.Diagnostics)
	}
	if len(analysis.Report.Scripts) != 1 {
		t.Fatalf("scripts not discovered after add-facet: %#v", analysis.Report.Scripts)
	}
}

func TestAddProjectFacetsSupportsMultipleFacets(t *testing.T) {
	dir := t.TempDir()
	scaffold, err := ScaffoldProject(ScaffoldOptions{
		Name:      "Multi Facet",
		OwnerNode: "main",
		Directory: dir,
		Facets:    []string{"docs"},
	})
	if err != nil {
		t.Fatalf("scaffold project: %v", err)
	}

	result, err := AddProjectFacets(AddProjectFacetsOptions{ProjectRoot: scaffold.ProjectRoot, Facets: []string{"scripts", "workflows"}})
	if err != nil {
		t.Fatalf("add multiple facets: %v", err)
	}
	if !containsString(result.AddedFacets, "scripts") || !containsString(result.AddedFacets, "workflows") {
		t.Fatalf("added facets = %#v", result.AddedFacets)
	}
	assertFile(t, scaffold.ProjectRoot, "scripts/hello_world/loom.script.yaml")
	assertFile(t, scaffold.ProjectRoot, "workflows/example_workflow/loom.workflow.yaml")

	analysis := Analyze(scaffold.ProjectRoot)
	if !analysis.Report.OK {
		t.Fatalf("project should validate after adding multiple facets: %#v", analysis.Report.Diagnostics)
	}
	if len(analysis.Report.Scripts) != 1 || len(analysis.Report.Workflows) != 1 {
		t.Fatalf("unexpected discovered facets: scripts=%#v workflows=%#v", analysis.Report.Scripts, analysis.Report.Workflows)
	}
}

func TestAddProjectFacetsSkipsExistingFilesUnlessForced(t *testing.T) {
	dir := t.TempDir()
	scaffold, err := ScaffoldProject(ScaffoldOptions{
		Name:      "Partial Scripts",
		OwnerNode: "main",
		Directory: dir,
		Facets:    []string{"docs"},
	})
	if err != nil {
		t.Fatalf("scaffold project: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(scaffold.ProjectRoot, "scripts"), 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	custom := "custom scripts readme\n"
	if err := os.WriteFile(filepath.Join(scaffold.ProjectRoot, "scripts", "README.md"), []byte(custom), 0o644); err != nil {
		t.Fatalf("write custom scripts readme: %v", err)
	}

	result, err := AddProjectFacets(AddProjectFacetsOptions{ProjectRoot: scaffold.ProjectRoot, Facets: []string{"scripts"}})
	if err != nil {
		t.Fatalf("add scripts facet: %v", err)
	}
	if got := readFile(t, scaffold.ProjectRoot, "scripts/README.md"); got != custom {
		t.Fatalf("scripts README should be skipped by default, got:\n%s", got)
	}
	if actionForFile(result.Files, "scripts/README.md") != "" {
		t.Fatalf("scripts README should not be scaffold-managed, files=%#v", result.Files)
	}

	forced, err := AddProjectFacets(AddProjectFacetsOptions{ProjectRoot: scaffold.ProjectRoot, Facets: []string{"scripts"}, Force: true})
	if err != nil {
		t.Fatalf("force add scripts facet: %v", err)
	}
	if got := readFile(t, scaffold.ProjectRoot, "scripts/README.md"); got != custom {
		t.Fatalf("scripts README should remain user-owned with --force")
	}
	if actionForFile(forced.Files, "scripts/README.md") != "" {
		t.Fatalf("force should not manage scripts README, files=%#v", forced.Files)
	}
}

func TestAddProjectFacetsDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	scaffold, err := ScaffoldProject(ScaffoldOptions{
		Name:      "Dry Add Facet",
		OwnerNode: "main",
		Directory: dir,
		Facets:    []string{"docs"},
	})
	if err != nil {
		t.Fatalf("scaffold project: %v", err)
	}
	before := readFile(t, scaffold.ProjectRoot, CanonicalRootContractPath)
	result, err := AddProjectFacets(AddProjectFacetsOptions{ProjectRoot: scaffold.ProjectRoot, Facets: []string{"scripts"}, DryRun: true})
	if err != nil {
		t.Fatalf("dry-run add scripts: %v", err)
	}
	if result.Validation.State != ScaffoldValidationPassed || !result.Validation.OK {
		t.Fatalf("dry-run validation state = %q", result.Validation.State)
	}
	if _, err := os.Stat(filepath.Join(scaffold.ProjectRoot, "scripts")); !os.IsNotExist(err) {
		t.Fatalf("dry-run should not create scripts folder, stat err=%v", err)
	}
	if after := readFile(t, scaffold.ProjectRoot, CanonicalRootContractPath); after != before {
		t.Fatalf("dry-run should not update root contract")
	}
}

func TestAddProjectFacetsBootstrapsMissingProjectRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "backend-only-project")

	dryRun, err := AddProjectFacets(AddProjectFacetsOptions{
		ProjectRef:            "backend-only-project",
		ProjectRoot:           root,
		Facets:                []string{"notes"},
		DryRun:                true,
		BootstrapMissing:      true,
		BootstrapName:         "Backend Only Project",
		BootstrapSlug:         "backend-only-project",
		BootstrapOwnerNodeKey: "main",
	})
	if err != nil {
		t.Fatalf("dry-run bootstrap facet add returned error: %v", err)
	}
	if !dryRun.DryRun || dryRun.ProjectRoot != root || !containsString(dryRun.AddedFacets, "notes") {
		t.Fatalf("unexpected dry-run bootstrap result: %#v", dryRun)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("dry-run should not create project root, stat err=%v", err)
	}

	applied, err := AddProjectFacets(AddProjectFacetsOptions{
		ProjectRef:            "backend-only-project",
		ProjectRoot:           root,
		Facets:                []string{"notes"},
		BootstrapMissing:      true,
		BootstrapName:         "Backend Only Project",
		BootstrapSlug:         "backend-only-project",
		BootstrapOwnerNodeKey: "main",
	})
	if err != nil {
		t.Fatalf("apply bootstrap facet add returned error: %v", err)
	}
	if !applied.OK || applied.DryRun || applied.ProjectRoot != root || !containsString(applied.AddedFacets, "notes") {
		t.Fatalf("unexpected apply bootstrap result: %#v", applied)
	}
	assertFile(t, root, CanonicalRootContractPath)
	assertFile(t, root, ".loom/agents/surfaces/notes.md")
	assertNoFile(t, root, "notes/README.md")
}

func TestCleanupScaffoldExamplesRemovesUntouchedPackages(t *testing.T) {
	dir := t.TempDir()
	scaffold, err := ScaffoldProject(ScaffoldOptions{
		Name:      "Cleanup Examples",
		OwnerNode: "main",
		Directory: dir,
		Facets:    []string{"scripts", "workflows", "schedules", "direct_events"},
	})
	if err != nil {
		t.Fatalf("scaffold project: %v", err)
	}

	result, err := CleanupScaffoldExamples(ScaffoldCleanupOptions{ProjectRoot: scaffold.ProjectRoot})
	if err != nil {
		t.Fatalf("cleanup scaffold examples: %v", err)
	}
	if !result.OK || len(result.Removed) != 4 || len(result.Skipped) != 0 {
		t.Fatalf("unexpected cleanup result: %#v", result)
	}
	for _, rel := range []string{
		"scripts/hello_world",
		"workflows/example_workflow",
		"schedules/example_schedule",
		"direct_events/example_event",
	} {
		assertNoFile(t, scaffold.ProjectRoot, rel)
	}
	for _, rel := range []string{
		"scripts",
		"workflows",
		"schedules",
		"direct_events",
	} {
		assertDirectory(t, scaffold.ProjectRoot, rel)
	}
	analysis := Analyze(scaffold.ProjectRoot)
	if !analysis.Report.OK {
		t.Fatalf("project should validate after cleanup: %#v", analysis.Report.Diagnostics)
	}
}

func TestCleanupScaffoldExamplesSkipsEditedPackage(t *testing.T) {
	dir := t.TempDir()
	scaffold, err := ScaffoldProject(ScaffoldOptions{
		Name:      "Cleanup Edited",
		OwnerNode: "main",
		Directory: dir,
		Facets:    []string{"scripts", "workflows", "schedules", "direct_events"},
	})
	if err != nil {
		t.Fatalf("scaffold project: %v", err)
	}
	runPath := filepath.Join(scaffold.ProjectRoot, "scripts", "hello_world", "run.sh")
	if err := os.WriteFile(runPath, []byte("#!/usr/bin/env bash\necho edited\n"), 0o755); err != nil {
		t.Fatalf("edit scaffold script: %v", err)
	}

	result, err := CleanupScaffoldExamples(ScaffoldCleanupOptions{ProjectRoot: scaffold.ProjectRoot})
	if err != nil {
		t.Fatalf("cleanup scaffold examples: %v", err)
	}
	if len(result.Skipped) != 1 || result.Skipped[0].Path != "scripts/hello_world" {
		t.Fatalf("edited script package should be skipped: %#v", result.Skipped)
	}
	assertFile(t, scaffold.ProjectRoot, "scripts/hello_world/run.sh")
	assertNoFile(t, scaffold.ProjectRoot, "workflows/example_workflow")
	assertNoFile(t, scaffold.ProjectRoot, "schedules/example_schedule")
	assertNoFile(t, scaffold.ProjectRoot, "direct_events/example_event")
}

func TestCleanupScaffoldExamplesDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	scaffold, err := ScaffoldProject(ScaffoldOptions{
		Name:      "Cleanup Dry Run",
		OwnerNode: "main",
		Directory: dir,
		Facets:    []string{"scripts", "workflows", "schedules", "direct_events"},
	})
	if err != nil {
		t.Fatalf("scaffold project: %v", err)
	}
	result, err := CleanupScaffoldExamples(ScaffoldCleanupOptions{ProjectRoot: scaffold.ProjectRoot, DryRun: true})
	if err != nil {
		t.Fatalf("dry-run cleanup scaffold examples: %v", err)
	}
	if !result.DryRun || len(result.Removed) != 4 || result.Validation.State != ScaffoldValidationPlannedOnly {
		t.Fatalf("unexpected dry-run cleanup result: %#v", result)
	}
	assertFile(t, scaffold.ProjectRoot, "scripts/hello_world/loom.script.yaml")
	assertFile(t, scaffold.ProjectRoot, "workflows/example_workflow/loom.workflow.yaml")
	assertFile(t, scaffold.ProjectRoot, "schedules/example_schedule/loom.schedule.yaml")
	assertFile(t, scaffold.ProjectRoot, "direct_events/example_event/loom.direct_event.yaml")
}

func TestScaffoldRejectsInvalidInputs(t *testing.T) {
	for _, opts := range []ScaffoldOptions{
		{Name: "", OwnerNode: "main"},
		{Name: "Bad Owner", OwnerNode: "Main"},
		{Name: "Bad Facet", OwnerNode: "main", Facets: []string{"nope"}},
		{Name: "Bad Preset", OwnerNode: "main", Preset: "unknown"},
	} {
		if _, err := ScaffoldProject(opts); err == nil {
			t.Fatalf("expected scaffold options to fail: %#v", opts)
		}
	}
}

func actionForFile(files []ScaffoldFileResult, path string) string {
	for _, file := range files {
		if file.Path == path {
			return file.Action
		}
	}
	return ""
}

func assertFile(t *testing.T, root, rel string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
		t.Fatalf("expected file %s: %v", rel, err)
	}
}

func assertDirectory(t *testing.T, root, rel string) {
	t.Helper()
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("expected directory %s: %v", rel, err)
	}
	if !info.IsDir() {
		t.Fatalf("expected %s to be a directory", rel)
	}
}

func assertNoFile(t *testing.T, root, rel string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); !os.IsNotExist(err) {
		t.Fatalf("expected file %s to be absent, stat err=%v", rel, err)
	}
}

func assertExecutable(t *testing.T, root, rel string) {
	t.Helper()
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("expected executable %s: %v", rel, err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("expected %s to be executable, mode=%s", rel, info.Mode())
	}
}

func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(payload)
}

func hasFileAction(files []ScaffoldFileResult, path, action string) bool {
	for _, file := range files {
		if file.Path == path && file.Action == action {
			return true
		}
	}
	return false
}
