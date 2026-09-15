package loomcli

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectdoctor"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectwatch"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/setup"
	"loom.local/loom/internal/storagearchive"
)

func TestProjectPhysicalArchiveInspectCommand(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/projects/test/archive/inspect" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success("corr_test", storagearchive.ProjectArchiveInspectResult{
			Physical:            &storagearchive.ProjectPhysicalArchiveInspection{ProjectID: "project_test", OperationID: "operation_test", Phase: "complete", Status: "restored", MutationBlocked: true, ActivationState: storagearchive.WorkspaceActivationInactive, EvidenceStatus: "verified", NextAction: "activation_not_available"},
			RuntimeManifestPath: "/private/legacy/must-not-render",
		}))
	}))
	defer server.Close()
	configPath := writeProjectBackendInstallManifest(t, server.URL)
	stdout, stderr, err := executeRootCommand("--config", configPath, "project", "archive", "inspect", "test")
	if err != nil {
		t.Fatalf("inspect: %v %s", err, stderr)
	}
	for _, want := range []string{"Physical operation: operation_test", "Evidence: verified", "Runtime: inactive", "Mutation blocked: true", "activation_not_available"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("missing %q: %s", want, stdout)
		}
	}
	if strings.Contains(stdout, "/private") || strings.Contains(stdout, "Runtime manifest:") {
		t.Fatal("physical renderer used legacy manifest")
	}
}

func TestProjectValidateCommandLocalHumanOutput(t *testing.T) {
	root := writeProjectContractFixture(t, `
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: gmail-automation
  name: Gmail Automation
  owner_node: macbook
facets:
  scripts: true
`)
	if err := os.Mkdir(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}

	stdout, stderr, err := executeRootCommand("project", "validate", root)
	if err != nil {
		t.Fatalf("validate returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{
		"Project contract: ok",
		"Project: gmail-automation",
		"Owner node: macbook",
		"Provider: macbook@gmail-automation",
		"Diagnostics: 0 errors, 2 warnings",
		"warning contract.layout_legacy",
		"warning script.none_found",
		"Layout: legacy",
		"Contract: " + filepath.Join(root, projectcontracts.LegacyRootContractPath),
		"Compatibility: migrate with `loom project migrate-layout " + root + " --dry-run`.",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestProjectExportCommandWritesPortableArchive(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".loom"), 0o755); err != nil {
		t.Fatal(err)
	}
	contract := "kind: loom.project\nschema_version: project.contract.v0.3\nproject:\n  slug: export-command\n  name: Export Command\n  owner_node: main\n"
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(projectcontracts.CanonicalRootContractPath)), []byte(contract), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("portable\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "project.tar")
	stdout, stderr, err := executeRootCommand("project", "export", root, "--mode", "portable", "--out", output)
	if err != nil {
		t.Fatalf("export returned error: %v stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "Project export: ") || !strings.Contains(stdout, "Mode: portable") {
		t.Fatalf("unexpected output:\n%s", stdout)
	}
	archiveFile, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	defer archiveFile.Close()
	reader := tar.NewReader(archiveFile)
	foundContract := false
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Name == projectcontracts.CanonicalRootContractPath {
			foundContract = true
		}
	}
	if !foundContract {
		t.Fatal("portable archive does not contain canonical project contract")
	}
}

func TestProjectInspectShowsRegisteredContractLayout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/projects/osint-tools":
			response.WriteJSON(w, http.StatusOK, response.Success("corr_inspect", projects.ProjectDetail{Project: projects.Project{
				ProjectID:       "project_osint_tools",
				ProjectScopeID:  "scope_osint_tools",
				ProjectScopeKey: "project.osint-tools",
				Slug:            "osint-tools",
				Name:            "OSINT Tools",
				Status:          "active",
			}}))
		case "/v1/project-contract-analyses":
			root := "/home/loomadmin/loom-box/Projects/osint-tools"
			response.WriteJSON(w, http.StatusOK, response.Success("corr_inspect", projectdoctor.BackendAnalysisResult{
				Analysis: projectcontracts.Analysis{Loaded: &projectcontracts.LoadedProject{
					RootPath:     root,
					ContractPath: filepath.Join(root, filepath.FromSlash(projectcontracts.CanonicalRootContractPath)),
					Layout:       projectcontracts.ProjectLayoutCanonical,
				}},
			}))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	configPath := writeProjectBackendInstallManifest(t, server.URL)

	stdout, stderr, err := executeRootCommand("--config", configPath, "project", "inspect", "osint-tools")
	if err != nil {
		t.Fatalf("project inspect returned error: %v stderr=%s stdout=%s", err, stderr, stdout)
	}
	for _, want := range []string{
		"Project: osint-tools",
		"Contract: /home/loomadmin/loom-box/Projects/osint-tools/" + projectcontracts.CanonicalRootContractPath,
		"Layout: canonical",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestProjectValidateCommandJSONAndFailure(t *testing.T) {
	root := writeProjectContractFixture(t, `
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: Invalid Slug
  name: Gmail Automation
  owner_node: macbook
`)
	stdout, _, err := executeRootCommand("--json", "project", "validate", root)
	if err == nil {
		t.Fatal("expected invalid contract to return error")
	}
	var report projectcontracts.ValidationReport
	if decodeErr := json.Unmarshal([]byte(stdout), &report); decodeErr != nil {
		t.Fatalf("decode report json: %v output=%s", decodeErr, stdout)
	}
	if report.OK {
		t.Fatal("invalid report should have ok=false")
	}
	if report.Summary.Errors == 0 {
		t.Fatalf("expected errors in report: %#v", report)
	}
}

func TestProjectValidateStrictFailsWarnings(t *testing.T) {
	root := writeProjectContractFixture(t, `
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: gmail-automation
  name: Gmail Automation
  owner_node: macbook
facets:
  workflows: true
`)
	if _, _, err := executeRootCommand("project", "validate", root); err != nil {
		t.Fatalf("warning-only validation should succeed by default: %v", err)
	}
	if _, _, err := executeRootCommand("project", "validate", root, "--strict"); err == nil {
		t.Fatal("strict warning-only validation should fail")
	}
}

func TestProjectPlanCommandJSON(t *testing.T) {
	root := writeProjectContractFixture(t, `
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: gmail-automation
  name: Gmail Automation
  owner_node: macbook
facets:
  scripts: true
`)
	if err := os.Mkdir(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}

	stdout, stderr, err := executeRootCommand("--json", "project", "plan", root)
	if err != nil {
		t.Fatalf("plan returned error: %v stderr=%s", err, stderr)
	}
	var plan projectcontracts.ProjectPlan
	if decodeErr := json.Unmarshal([]byte(stdout), &plan); decodeErr != nil {
		t.Fatalf("decode plan json: %v output=%s", decodeErr, stdout)
	}
	if !plan.Registerable {
		t.Fatalf("expected registerable plan: %#v", plan)
	}
	if len(plan.Actions) == 0 {
		t.Fatal("expected plan actions")
	}
}

func TestProjectWatchPlanCommandLocalJSON(t *testing.T) {
	dir := t.TempDir()
	result, err := projectcontracts.ScaffoldProject(projectcontracts.ScaffoldOptions{
		Name:      "Research Notes",
		Slug:      "research-notes",
		OwnerNode: "workspace",
		Preset:    projectcontracts.PresetResearch,
		Directory: dir,
	})
	if err != nil {
		t.Fatalf("ScaffoldProject returned error: %v", err)
	}

	stdout, stderr, err := executeRootCommand("--json", "project", "watch-plan", result.ProjectRoot)
	if err != nil {
		t.Fatalf("watch-plan returned error: %v stderr=%s", err, stderr)
	}
	var plan projectwatch.ProjectWatchPlan
	if decodeErr := json.Unmarshal([]byte(stdout), &plan); decodeErr != nil {
		t.Fatalf("decode watch plan json: %v output=%s", decodeErr, stdout)
	}
	if len(plan.WatchedRoots) != 2 || len(plan.Commands) == 0 {
		t.Fatalf("expected watched roots and commands, got %#v", plan)
	}
	if plan.WatchedRoots[0].ConfigJSON == nil || plan.ProjectRoot != result.ProjectRoot {
		t.Fatalf("unexpected watch plan payload: %#v", plan)
	}
}

func TestProjectWorkflowsListCommandLocalJSONAndHuman(t *testing.T) {
	dir := t.TempDir()
	result, err := projectcontracts.ScaffoldProject(projectcontracts.ScaffoldOptions{
		Name:      "Workflow Project",
		Slug:      "workflow-project",
		OwnerNode: "main",
		Facets:    []string{"workflows"},
		Directory: dir,
	})
	if err != nil {
		t.Fatalf("ScaffoldProject returned error: %v", err)
	}

	stdout, stderr, err := executeRootCommand("--json", "project", "workflows", "list", result.ProjectRoot)
	if err != nil {
		t.Fatalf("workflow list returned error: %v stderr=%s", err, stderr)
	}
	var payload projectWorkflowListResult
	if decodeErr := json.Unmarshal([]byte(stdout), &payload); decodeErr != nil {
		t.Fatalf("decode workflow list json: %v output=%s", decodeErr, stdout)
	}
	if payload.Project != "workflow-project" || payload.Source != "local_analysis" || len(payload.Workflows) != 1 {
		t.Fatalf("unexpected workflow list result: %#v", payload)
	}
	if payload.Workflows[0].WorkflowID != "example_workflow" || payload.Workflows[0].ImplementationKind != projectcontracts.WorkflowImplementationWorkflow {
		t.Fatalf("unexpected workflow item: %#v", payload.Workflows[0])
	}

	stdout, stderr, err = executeRootCommand("project", "workflows", "list", result.ProjectRoot)
	if err != nil {
		t.Fatalf("workflow list human returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{"Project workflows: workflow-project", "example_workflow", "workflow"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}

	stdout, stderr, err = executeRootCommand("--json", "project", "workflows", "inspect", result.ProjectRoot, "example_workflow")
	if err != nil {
		t.Fatalf("workflow inspect json returned error: %v stderr=%s", err, stderr)
	}
	var inspect projectWorkflowInspectResult
	if decodeErr := json.Unmarshal([]byte(stdout), &inspect); decodeErr != nil {
		t.Fatalf("decode workflow inspect json: %v output=%s", decodeErr, stdout)
	}
	if inspect.WorkflowID != "example_workflow" || inspect.ImplementationKind != projectcontracts.WorkflowImplementationWorkflow {
		t.Fatalf("unexpected workflow inspect result: %#v", inspect)
	}

	stdout, stderr, err = executeRootCommand("project", "workflows", "inspect", result.ProjectRoot, "example_workflow")
	if err != nil {
		t.Fatalf("workflow inspect human returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{"Workflow: example_workflow", "Input schema:", "Output schema:", "loom capability call"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("workflow inspect stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestProjectScaffoldCommandHumanOutput(t *testing.T) {
	dir := t.TempDir()
	stdout, stderr, err := executeRootCommand(
		"project", "scaffold", "Gmail Automation",
		"--owner-node", "main",
		"--preset", "automation",
		"--directory", dir,
	)
	if err != nil {
		t.Fatalf("scaffold returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{
		"Project scaffold: created",
		"Project: gmail-automation",
		"Owner node: main",
		"Preset: automation",
		"Validation: 0 errors, 0 warnings",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "gmail-automation", filepath.FromSlash(projectcontracts.CanonicalRootContractPath))); err != nil {
		t.Fatalf("expected scaffolded root contract: %v", err)
	}
	agents, err := os.ReadFile(filepath.Join(dir, "gmail-automation", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read scaffolded agent entry point: %v", err)
	}
	for _, want := range []string{"search-loom-docs", "manage-loom-projects", "loom project validate .", "Proton Pass"} {
		if !strings.Contains(string(agents), want) {
			t.Fatalf("scaffolded AGENTS.md missing %q:\n%s", want, agents)
		}
	}
}

func TestProjectScaffoldCommandJSON(t *testing.T) {
	dir := t.TempDir()
	stdout, stderr, err := executeRootCommand(
		"--json",
		"project", "scaffold", "Research Notes",
		"--owner-node", "macbook",
		"--preset", "research",
		"--directory", dir,
	)
	if err != nil {
		t.Fatalf("scaffold returned error: %v stderr=%s", err, stderr)
	}
	var result projectcontracts.ScaffoldResult
	if decodeErr := json.Unmarshal([]byte(stdout), &result); decodeErr != nil {
		t.Fatalf("decode scaffold json: %v output=%s", decodeErr, stdout)
	}
	if !result.OK || result.Slug != "research-notes" || len(result.Files) == 0 {
		t.Fatalf("unexpected scaffold result: %#v", result)
	}
	if result.BoxDefault || result.DirSource != projectcontracts.ScaffoldDirectoryExplicit {
		t.Fatalf("explicit directory should not use Box default: %#v", result)
	}
}

func TestProjectMigrateLayoutCommandDefaultsToDryRunAndRequiresConfirmation(t *testing.T) {
	root := writeProjectContractFixture(t, `
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: migrate-layout-cli
  name: Migrate Layout CLI
  owner_node: main
`)
	stdout, stderr, err := executeRootCommand("project", "migrate-layout", root)
	if err != nil {
		t.Fatalf("dry-run migrate-layout returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{"Project layout migration: planned", "Layout: legacy", "Actions:", "loom project validate"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(projectcontracts.CanonicalRootContractPath))); !os.IsNotExist(err) {
		t.Fatalf("default dry-run should not write canonical contract, stat err=%v", err)
	}

	if _, _, err := executeRootCommand("project", "migrate-layout", root, "--apply"); err == nil {
		t.Fatal("expected --apply without --yes to fail")
	}
	stdout, stderr, err = executeRootCommand("project", "migrate-layout", root, "--apply", "--yes")
	if err != nil {
		t.Fatalf("apply migrate-layout returned error: %v stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "Project layout migration: applied") || !strings.Contains(stdout, "Layout: legacy -> canonical") {
		t.Fatalf("unexpected apply output:\n%s", stdout)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(projectcontracts.CanonicalRootContractPath))); err != nil {
		t.Fatalf("expected canonical contract after apply: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, projectcontracts.LegacyRootContractPath)); !os.IsNotExist(err) {
		t.Fatalf("expected legacy contract removal, stat err=%v", err)
	}
}

func TestProjectMigrateLayoutCommandBackendUsesRemoteEndpoint(t *testing.T) {
	var sawRequest bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawRequest = true
		if r.Method != http.MethodPost || r.URL.Path != "/v1/project-layout-migrations" {
			t.Fatalf("unexpected backend request %s %s", r.Method, r.URL.Path)
		}
		var input projectcontracts.LayoutMigrationOptions
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode backend migration body: %v", err)
		}
		if input.ProjectRef != "legacy-layout" || !input.Apply || !input.Yes || input.ProjectRoot != "" {
			t.Fatalf("unexpected backend migration body: %#v", input)
		}
		response.WriteJSON(w, http.StatusOK, response.Success("corr_layout", projectcontracts.LayoutMigrationResult{
			OK:           true,
			Applied:      true,
			ProjectRoot:  "/home/loomadmin/loom-box/Projects/legacy-layout",
			BeforeLayout: projectcontracts.ProjectLayoutLegacy,
			AfterLayout:  projectcontracts.ProjectLayoutCanonical,
			Actions: []projectcontracts.LayoutMigrationAction{{
				Order:       1,
				Operation:   "move_contract",
				Kind:        "root_contract",
				Source:      projectcontracts.LegacyRootContractPath,
				Destination: projectcontracts.CanonicalRootContractPath,
				Status:      "applied",
			}},
			NextActions: []string{"loom project validate legacy-layout --backend"},
		}))
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "loom.env")
	if err := os.WriteFile(configPath, []byte(strings.Join([]string{
		"LOOM_NODE_ID=macbook",
		"LOOM_NODE_KIND=workspace",
		"LOOM_NODE_ROLE=workspace",
		"LOOM_RUNTIME_CLASS=workspace",
		"LOOM_MAIN_URL=" + server.URL,
		"",
	}, "\n")), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	stdout, stderr, err := executeRootCommand(
		"--config", configPath,
		"project", "migrate-layout", "legacy-layout",
		"--backend", "--apply", "--yes",
	)
	if err != nil {
		t.Fatalf("backend migrate-layout returned error: %v stderr=%s stdout=%s", err, stderr, stdout)
	}
	if !sawRequest {
		t.Fatal("backend migrate-layout did not reach test server")
	}
	for _, want := range []string{
		"Project layout migration: applied",
		"Layout: legacy -> canonical",
		"Backend: " + server.URL,
		"Correlation: corr_layout",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestProjectScaffoldBackendRegisterCommand(t *testing.T) {
	var sawScaffold bool
	var sawRegister bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/project-scaffolds":
			sawScaffold = true
			var input projectcontracts.ScaffoldOptions
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode scaffold body: %v", err)
			}
			if input.Name != "Remote Main Project" || input.OwnerNode != "main" || input.Preset != projectcontracts.PresetAutomation {
				t.Fatalf("unexpected scaffold body: %#v", input)
			}
			response.WriteJSON(w, http.StatusOK, response.Success("corr_test", projectcontracts.ScaffoldResult{
				OK:           true,
				ProjectRoot:  "/home/loomadmin/loom-box/Projects/remote-main-project",
				ParentDir:    "/home/loomadmin/loom-box/Projects",
				ContractPath: "/home/loomadmin/loom-box/Projects/remote-main-project/.loom/project.yaml",
				Name:         "Remote Main Project",
				Slug:         "remote-main-project",
				OwnerNode:    "main",
				Preset:       projectcontracts.PresetAutomation,
				Facets:       []string{"notes", "scripts"},
				Validation:   projectcontracts.ScaffoldValidationSummary{State: projectcontracts.ScaffoldValidationPassed, OK: true},
			}))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/project-contract-registrations/from-backend":
			sawRegister = true
			var input projects.RegisterProjectContractFromBackendInput
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode backend register body: %v", err)
			}
			if input.ProjectRef != "remote-main-project" || input.ProjectRoot != "/home/loomadmin/loom-box/Projects/remote-main-project" {
				t.Fatalf("unexpected backend register body: %#v", input)
			}
			response.WriteJSON(w, http.StatusOK, response.Success("corr_test", projects.RegisterProjectContractResult{
				Detail: projects.ProjectRegistrationDetail{
					Project: projects.ProjectDetail{Project: projects.Project{
						ProjectID: "project_remote_main",
						Slug:      "remote-main-project",
						Name:      "Remote Main Project",
						Status:    "active",
					}},
					Registration: &projects.ProjectContractRegistration{
						ProjectRoot:          "/home/loomadmin/loom-box/Projects/remote-main-project",
						ContractPath:         "/home/loomadmin/loom-box/Projects/remote-main-project/.loom/project.yaml",
						RegistrationStatus:   projects.ProjectRegistrationStatusRegistered,
						RegistrationRevision: 1,
					},
				},
				Updated: true,
			}))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	configPath := filepath.Join(t.TempDir(), "loom.env")
	if err := os.WriteFile(configPath, nil, 0o600); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	t.Setenv("LOOM_MAIN_URL", server.URL)

	stdout, stderr, err := executeRootCommand(
		"--config", configPath,
		"project", "scaffold", "Remote Main Project",
		"--backend",
		"--owner-node", "main",
		"--preset", projectcontracts.PresetAutomation,
		"--register",
	)
	if err != nil {
		t.Fatalf("backend scaffold/register returned error: %v stderr=%s", err, stderr)
	}
	if !sawScaffold || !sawRegister {
		t.Fatalf("expected scaffold and register requests, scaffold=%t register=%t", sawScaffold, sawRegister)
	}
	for _, want := range []string{
		"Project scaffold: created",
		"Project registration: updated",
		"Registration: registered",
		"Revision: 1",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestProjectScaffoldCommandDefaultsIntoBoxProjects(t *testing.T) {
	boxRoot := initCLIBoxFixture(t, box.ProfileWorkspace)
	t.Setenv("LOOM_BOX_PATH", boxRoot)
	t.Setenv("LOOM_BOX_PROFILE", box.ProfileWorkspace)

	stdout, stderr, err := executeRootCommand(
		"--json",
		"project", "scaffold", "Box Smoke Project",
		"--owner-node", "macbook",
		"--preset", "minimal",
	)
	if err != nil {
		t.Fatalf("scaffold returned error: %v stderr=%s", err, stderr)
	}
	var result projectcontracts.ScaffoldResult
	if decodeErr := json.Unmarshal([]byte(stdout), &result); decodeErr != nil {
		t.Fatalf("decode scaffold json: %v output=%s", decodeErr, stdout)
	}
	wantRoot := filepath.Join(boxRoot, "Projects", "box-smoke-project")
	if !result.OK || result.ProjectRoot != wantRoot || !result.BoxDefault || result.DirSource != projectcontracts.ScaffoldDirectoryBox {
		t.Fatalf("unexpected Box scaffold result: %#v", result)
	}
	if result.ParentDir != filepath.Join(boxRoot, "Projects") || result.BoxRoot != boxRoot || result.BoxProfile != box.ProfileWorkspace {
		t.Fatalf("unexpected Box metadata: %#v", result)
	}
	if _, err := os.Stat(filepath.Join(wantRoot, filepath.FromSlash(projectcontracts.CanonicalRootContractPath))); err != nil {
		t.Fatalf("expected scaffolded project under Box Projects: %v", err)
	}
}

func TestProjectScaffoldCommandHumanOutputShowsBoxDefault(t *testing.T) {
	boxRoot := initCLIBoxFixture(t, box.ProfileWorkspace)
	t.Setenv("LOOM_BOX_PATH", boxRoot)
	t.Setenv("LOOM_BOX_PROFILE", box.ProfileWorkspace)

	stdout, stderr, err := executeRootCommand(
		"project", "scaffold", "Human Box Project",
		"--owner-node", "macbook",
		"--preset", "minimal",
	)
	if err != nil {
		t.Fatalf("scaffold returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{
		"Project scaffold: created",
		"Path: " + filepath.Join(boxRoot, "Projects", "human-box-project"),
		"Box: " + boxRoot,
		"Directory source: box_default",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestProjectScaffoldCommandFailsClearlyWhenBoxMissing(t *testing.T) {
	useIsolatedCLIBoxState(t)
	boxRoot := filepath.Join(t.TempDir(), "Missing LOOM Box")
	t.Setenv("LOOM_BOX_PATH", boxRoot)
	t.Setenv("LOOM_BOX_PROFILE", box.ProfileWorkspace)

	stdout, stderr, err := executeRootCommand(
		"project", "scaffold", "Missing Box Project",
		"--owner-node", "macbook",
	)
	if err == nil {
		t.Fatalf("expected missing Box scaffold to fail, stdout=%s stderr=%s", stdout, stderr)
	}
	if !strings.Contains(err.Error(), "LOOM Box is not initialized") || !strings.Contains(err.Error(), "pass --directory") {
		t.Fatalf("unexpected missing Box error: %v", err)
	}
	if !strings.Contains(stderr, "LOOM Box is not initialized") || !strings.Contains(stderr, "loom box init") {
		t.Fatalf("stderr should explain missing Box recovery, got: %s", stderr)
	}
	if _, statErr := os.Stat(boxRoot); !os.IsNotExist(statErr) {
		t.Fatalf("missing Box scaffold should not create Box root, stat err=%v", statErr)
	}
}

func TestProjectScaffoldCommandExplicitDirectoryOverridesMissingBox(t *testing.T) {
	boxRoot := filepath.Join(t.TempDir(), "Missing LOOM Box")
	dir := t.TempDir()
	t.Setenv("LOOM_BOX_PATH", boxRoot)
	t.Setenv("LOOM_BOX_PROFILE", box.ProfileWorkspace)

	stdout, stderr, err := executeRootCommand(
		"--json",
		"project", "scaffold", "Explicit Outside Box",
		"--owner-node", "macbook",
		"--directory", dir,
	)
	if err != nil {
		t.Fatalf("explicit directory scaffold returned error: %v stderr=%s", err, stderr)
	}
	var result projectcontracts.ScaffoldResult
	if decodeErr := json.Unmarshal([]byte(stdout), &result); decodeErr != nil {
		t.Fatalf("decode scaffold json: %v output=%s", decodeErr, stdout)
	}
	wantRoot := filepath.Join(dir, "explicit-outside-box")
	if result.ProjectRoot != wantRoot || result.BoxDefault || result.DirSource != projectcontracts.ScaffoldDirectoryExplicit {
		t.Fatalf("explicit directory should override Box default: %#v", result)
	}
	if _, err := os.Stat(filepath.Join(wantRoot, filepath.FromSlash(projectcontracts.CanonicalRootContractPath))); err != nil {
		t.Fatalf("expected explicit scaffold root contract: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(boxRoot, "Projects", "explicit-outside-box")); !os.IsNotExist(statErr) {
		t.Fatalf("explicit directory should not write into missing Box, stat err=%v", statErr)
	}
}

func TestProjectScaffoldCommandExplicitFacetsOverridePreset(t *testing.T) {
	dir := t.TempDir()
	stdout, stderr, err := executeRootCommand(
		"--json",
		"project", "scaffold", "Small Script",
		"--owner-node", "main",
		"--preset", "automation",
		"--facets", "notes,scripts,direct-events",
		"--directory", dir,
	)
	if err != nil {
		t.Fatalf("scaffold returned error: %v stderr=%s", err, stderr)
	}
	var result projectcontracts.ScaffoldResult
	if decodeErr := json.Unmarshal([]byte(stdout), &result); decodeErr != nil {
		t.Fatalf("decode scaffold json: %v output=%s", decodeErr, stdout)
	}
	if got := strings.Join(result.Facets, ","); got != "notes,scripts,direct_events" {
		t.Fatalf("facets = %s", got)
	}
}

func TestProjectScaffoldCommandRejectsInvalidInputBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := executeRootCommand(
		"project", "scaffold", "Bad Owner",
		"--owner-node", "Main",
		"--directory", dir,
	); err == nil {
		t.Fatal("expected invalid owner to fail")
	}
	if _, err := os.Stat(filepath.Join(dir, "bad-owner")); !os.IsNotExist(err) {
		t.Fatalf("invalid owner should not write project root, stat err=%v", err)
	}

	if _, _, err := executeRootCommand(
		"project", "scaffold", "Bad Facet",
		"--owner-node", "main",
		"--facets", "notes,nope",
		"--directory", dir,
	); err == nil {
		t.Fatal("expected invalid facet to fail")
	}
	if _, err := os.Stat(filepath.Join(dir, "bad-facet")); !os.IsNotExist(err) {
		t.Fatalf("invalid facet should not write project root, stat err=%v", err)
	}
}

func TestProjectScaffoldCommandDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	stdout, stderr, err := executeRootCommand(
		"project", "scaffold", "Dry Run Project",
		"--owner-node", "main",
		"--directory", dir,
		"--dry-run",
	)
	if err != nil {
		t.Fatalf("dry-run scaffold returned error: %v stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "Project scaffold: planned") || !strings.Contains(stdout, "Files:") {
		t.Fatalf("unexpected dry-run output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "Validation: planned_only") {
		t.Fatalf("dry-run output should explain validation did not run:\n%s", stdout)
	}
	if _, err := os.Stat(filepath.Join(dir, "dry-run-project")); !os.IsNotExist(err) {
		t.Fatalf("dry run should not write project root, stat err=%v", err)
	}
}

func TestProjectScaffoldCommandDryRunJSONValidationState(t *testing.T) {
	dir := t.TempDir()
	stdout, stderr, err := executeRootCommand(
		"--json",
		"project", "scaffold", "Dry JSON Project",
		"--owner-node", "main",
		"--directory", dir,
		"--dry-run",
	)
	if err != nil {
		t.Fatalf("dry-run scaffold returned error: %v stderr=%s", err, stderr)
	}
	if strings.Contains(stdout, `"ok":false`) {
		t.Fatalf("dry-run JSON should not contain validation ok=false with no diagnostics:\n%s", stdout)
	}
	var result projectcontracts.ScaffoldResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode scaffold dry-run json: %v output=%s", err, stdout)
	}
	if result.Validation.State != projectcontracts.ScaffoldValidationPlannedOnly {
		t.Fatalf("validation state = %q, want %q", result.Validation.State, projectcontracts.ScaffoldValidationPlannedOnly)
	}
}

func TestProjectFacetAddCommandAddsScripts(t *testing.T) {
	dir := t.TempDir()
	scaffold, err := projectcontracts.ScaffoldProject(projectcontracts.ScaffoldOptions{
		Name:      "Facet Add CLI",
		OwnerNode: "main",
		Directory: dir,
		Facets:    []string{"docs"},
	})
	if err != nil {
		t.Fatalf("scaffold project: %v", err)
	}
	stdout, stderr, err := executeRootCommand(
		"project", "facet", "add", scaffold.ProjectRoot, "scripts",
	)
	if err != nil {
		t.Fatalf("facet add returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{
		"Project facets: updated",
		"Added facets: scripts",
		"loom project validate " + scaffold.ProjectRoot,
		"loom project plan " + scaffold.ProjectRoot,
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
	analysis := projectcontracts.Analyze(scaffold.ProjectRoot)
	if !analysis.Report.OK || len(analysis.Report.Scripts) != 1 {
		t.Fatalf("project should include script facet after command, diagnostics=%#v scripts=%#v", analysis.Report.Diagnostics, analysis.Report.Scripts)
	}
}

func TestProjectFacetAddCommandDryRunJSON(t *testing.T) {
	dir := t.TempDir()
	scaffold, err := projectcontracts.ScaffoldProject(projectcontracts.ScaffoldOptions{
		Name:      "Facet Add Dry JSON",
		OwnerNode: "main",
		Directory: dir,
		Facets:    []string{"docs"},
	})
	if err != nil {
		t.Fatalf("scaffold project: %v", err)
	}
	stdout, stderr, err := executeRootCommand(
		"--json",
		"project", "facet", "add", scaffold.ProjectRoot, "scripts",
		"--dry-run",
	)
	if err != nil {
		t.Fatalf("facet add dry-run returned error: %v stderr=%s", err, stderr)
	}
	var result projectcontracts.AddProjectFacetsResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode facet add dry-run json: %v output=%s", err, stdout)
	}
	if !result.DryRun || result.Validation.State != projectcontracts.ScaffoldValidationPassed || !result.Validation.OK || !testStringSliceContains(result.AddedFacets, "scripts") {
		t.Fatalf("unexpected dry-run facet add result: %#v", result)
	}
	if _, err := os.Stat(filepath.Join(scaffold.ProjectRoot, "scripts")); !os.IsNotExist(err) {
		t.Fatalf("dry-run should not create scripts folder, stat err=%v", err)
	}
}

func TestProjectFacetAddCommandBackendUsesRemoteEndpoint(t *testing.T) {
	var sawRequest bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawRequest = true
		if r.Method != http.MethodPost || r.URL.Path != "/v1/project-facet-additions" {
			t.Errorf("unexpected backend request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		var input projectcontracts.AddProjectFacetsOptions
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Errorf("decode backend facet add body: %v", err)
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if input.ProjectRef != "osint-tools" || strings.Join(input.Facets, ",") != "notes" || !input.DryRun {
			t.Errorf("unexpected backend facet add body: %#v", input)
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success("corr_http", projectcontracts.AddProjectFacetsResult{
			OK:              true,
			DryRun:          true,
			ProjectRoot:     "/home/loomadmin/loom-box/Projects/osint-tools",
			ContractPath:    "/home/loomadmin/loom-box/Projects/osint-tools/.loom/project.yaml",
			Slug:            "osint-tools",
			Name:            "osint-tools",
			OwnerNode:       "main",
			RequestedFacets: []string{"notes"},
			AddedFacets:     []string{"notes"},
			Facets:          []string{"notes", "scripts"},
			Files:           []projectcontracts.ScaffoldFileResult{{Path: projectcontracts.CanonicalRootContractPath, Kind: "root_contract", Action: "planned_update"}},
			Validation:      projectcontracts.ScaffoldValidationSummary{State: projectcontracts.ScaffoldValidationPlannedOnly},
		}))
	}))
	defer server.Close()
	t.Setenv("LOOM_MAIN_URL", server.URL)

	configPath := filepath.Join(t.TempDir(), "loom.env")
	if err := os.WriteFile(configPath, []byte(strings.Join([]string{
		"LOOM_NODE_ID=macbook",
		"LOOM_NODE_KIND=workspace",
		"LOOM_NODE_ROLE=workspace",
		"LOOM_RUNTIME_CLASS=workspace",
		"LOOM_MAIN_URL=" + server.URL,
		"",
	}, "\n")), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	stdout, stderr, err := executeRootCommand(
		"--config", configPath,
		"project", "facet", "add", "osint-tools", "notes",
		"--backend",
		"--dry-run",
	)
	if err != nil {
		t.Fatalf("backend facet add returned error: %v stderr=%s stdout=%s", err, stderr, stdout)
	}
	if !sawRequest {
		t.Fatal("backend facet add did not reach test server")
	}
	for _, want := range []string{
		"Project facets: planned",
		"Project: osint-tools",
		"Added facets: notes",
		"Backend: " + server.URL,
		"Correlation: corr_http",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestProjectFacetAddCommandBackendApplyRegistersContract(t *testing.T) {
	var sawFacetAdd bool
	var sawRegister bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/project-facet-additions":
			sawFacetAdd = true
			var input projectcontracts.AddProjectFacetsOptions
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode backend facet add body: %v", err)
			}
			if input.ProjectRef != "osint-tools" || strings.Join(input.Facets, ",") != "notes" || input.DryRun {
				t.Fatalf("unexpected backend facet add body: %#v", input)
			}
			response.WriteJSON(w, http.StatusOK, response.Success("corr_http", projectcontracts.AddProjectFacetsResult{
				OK:              true,
				ProjectRoot:     "/home/loomadmin/loom-box/Projects/osint-tools",
				ContractPath:    "/home/loomadmin/loom-box/Projects/osint-tools/.loom/project.yaml",
				Slug:            "osint-tools",
				Name:            "osint-tools",
				OwnerNode:       "main",
				RequestedFacets: []string{"notes"},
				AddedFacets:     []string{"notes"},
				Facets:          []string{"notes", "scripts"},
				Files:           []projectcontracts.ScaffoldFileResult{{Path: projectcontracts.CanonicalRootContractPath, Kind: "root_contract", Action: "updated"}},
				Validation:      projectcontracts.ScaffoldValidationSummary{State: projectcontracts.ScaffoldValidationPassed, OK: true},
			}))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/project-contract-registrations/from-backend":
			sawRegister = true
			var input projects.RegisterProjectContractFromBackendInput
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode backend register body: %v", err)
			}
			if input.ProjectRef != "osint-tools" || input.ProjectRoot != "/home/loomadmin/loom-box/Projects/osint-tools" {
				t.Fatalf("unexpected backend register body: %#v", input)
			}
			response.WriteJSON(w, http.StatusOK, response.Success("corr_http", projects.RegisterProjectContractResult{
				Detail: projects.ProjectRegistrationDetail{
					Project: projects.ProjectDetail{Project: projects.Project{
						ProjectID: "project_osint_tools",
						Slug:      "osint-tools",
						Name:      "OSINT Tools",
						Status:    "active",
					}},
					Registration: &projects.ProjectContractRegistration{
						ProjectRoot:          "/home/loomadmin/loom-box/Projects/osint-tools",
						ContractPath:         "/home/loomadmin/loom-box/Projects/osint-tools/.loom/project.yaml",
						RegistrationStatus:   projects.ProjectRegistrationStatusRegistered,
						RegistrationRevision: 2,
					},
				},
				Updated: true,
			}))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	t.Setenv("LOOM_MAIN_URL", server.URL)

	configPath := filepath.Join(t.TempDir(), "loom.env")
	if err := os.WriteFile(configPath, []byte(strings.Join([]string{
		"LOOM_NODE_ID=macbook",
		"LOOM_NODE_KIND=workspace",
		"LOOM_NODE_ROLE=workspace",
		"LOOM_RUNTIME_CLASS=workspace",
		"LOOM_MAIN_URL=" + server.URL,
		"",
	}, "\n")), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	stdout, stderr, err := executeRootCommand(
		"--config", configPath,
		"project", "facet", "add", "osint-tools", "notes",
		"--backend",
	)
	if err != nil {
		t.Fatalf("backend facet add apply returned error: %v stderr=%s stdout=%s", err, stderr, stdout)
	}
	if !sawFacetAdd || !sawRegister {
		t.Fatalf("expected facet add and register requests, facet_add=%t register=%t", sawFacetAdd, sawRegister)
	}
	for _, want := range []string{
		"Project facets: updated",
		"Project registration: updated",
		"Registration: registered",
		"Revision: 2",
		"Backend: " + server.URL,
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestProjectDiffBackendCommandUsesRemoteAnalysis(t *testing.T) {
	var sawAnalysis bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/project-contract-analyses" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		sawAnalysis = true
		var input projectcontracts.BackendAnalysisInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode backend analysis body: %v", err)
		}
		if input.ProjectRef != "osint-tools" {
			t.Fatalf("unexpected backend analysis body: %#v", input)
		}
		response.WriteJSON(w, http.StatusOK, response.Success("corr_test", projectdoctor.BackendAnalysisResult{
			ProjectRef:  "osint-tools",
			ProjectRoot: "/home/loomadmin/loom-box/Projects/osint-tools",
			Analysis: projectcontracts.Analysis{
				Report: projectcontracts.ValidationReport{OK: true, Registerable: true, Project: projectcontracts.PlanProject{Slug: "osint-tools"}},
				Plan:   projectcontracts.ProjectPlan{Registerable: true, Project: projectcontracts.PlanProject{Slug: "osint-tools"}},
			},
			Diff: projectdoctor.DiffReport{
				ProjectRef: "osint-tools",
				LocalRoot:  "/home/loomadmin/loom-box/Projects/osint-tools",
				Summary:    projectdoctor.DiffSummary{Changed: 1},
				Items: []projectdoctor.DiffItem{{
					Key:     "contract",
					Kind:    "contract",
					Status:  projectdoctor.DiffChanged,
					Summary: "Backend contract hash differs from registered snapshot.",
				}},
			},
			Report:      projectdoctor.Report{ProjectRef: "osint-tools", Source: "project.backend_analysis", Summary: projectdoctor.Summary{Blocked: 1}},
			DriftStatus: projectdoctor.BackendDriftStale,
			Current:     false,
			NextAction:  "re-register contract from backend",
		}))
	}))
	defer server.Close()

	configPath := writeProjectBackendInstallManifest(t, server.URL)

	stdout, stderr, err := executeRootCommand("--config", configPath, "project", "diff", "osint-tools", "--backend")
	if err != nil {
		t.Fatalf("backend diff returned error: %v stderr=%s stdout=%s", err, stderr, stdout)
	}
	if !sawAnalysis {
		t.Fatal("backend diff did not reach analysis endpoint")
	}
	for _, want := range []string{"Project diff", "Project: osint-tools", "Summary: 1 changed", "contract"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestProjectDoctorBackendCommandUsesRemoteAnalysisReport(t *testing.T) {
	var sawAnalysis bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/project-contract-analyses" {
			http.NotFound(w, r)
			return
		}
		sawAnalysis = true
		response.WriteJSON(w, http.StatusOK, response.Success("corr_test", projectdoctor.BackendAnalysisResult{
			ProjectRef: "osint-tools",
			Analysis: projectcontracts.Analysis{
				Loaded: &projectcontracts.LoadedProject{
					RootPath:     "/home/loomadmin/loom-box/Projects/osint-tools",
					ContractPath: "/home/loomadmin/loom-box/Projects/osint-tools/" + projectcontracts.CanonicalRootContractPath,
					Layout:       projectcontracts.ProjectLayoutCanonical,
				},
				Report: projectcontracts.ValidationReport{OK: true, Registerable: true, Project: projectcontracts.PlanProject{Slug: "osint-tools"}},
				Plan:   projectcontracts.ProjectPlan{Registerable: true, Project: projectcontracts.PlanProject{Slug: "osint-tools"}},
			},
			Diff: projectdoctor.DiffReport{ProjectRef: "osint-tools", Summary: projectdoctor.DiffSummary{Unchanged: 1}},
			Report: projectdoctor.Report{
				ProjectRef: "osint-tools",
				Source:     "project.backend_analysis",
				Summary:    projectdoctor.Summary{OK: 2},
				Checks: []projectdoctor.Check{{
					Key:     "registration.current",
					Title:   "Contract Drift",
					Status:  projectdoctor.StatusOK,
					Summary: "Backend contract hash matches the registered contract hash.",
				}},
			},
			DriftStatus: projectdoctor.BackendDriftCurrent,
			Current:     true,
			NextAction:  "no registration needed",
		}))
	}))
	defer server.Close()

	configPath := writeProjectBackendInstallManifest(t, server.URL)

	stdout, stderr, err := executeRootCommand("--config", configPath, "project", "doctor", "osint-tools", "--backend")
	if err != nil {
		t.Fatalf("backend doctor returned error: %v stderr=%s stdout=%s", err, stderr, stdout)
	}
	if !sawAnalysis {
		t.Fatal("backend doctor did not reach analysis endpoint")
	}
	for _, want := range []string{"Project doctor: ok", "Project: osint-tools", "Contract Drift", "Layout: canonical", "Contract: /home/loomadmin/loom-box/Projects/osint-tools/" + projectcontracts.CanonicalRootContractPath} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestProjectArchiveCommandsUseRemoteArchiveEndpoints(t *testing.T) {
	var sawArchive, sawInspect, sawRestore bool
	detail := projectArchiveCommandTestDetail()
	manifest := storagearchive.ProjectRuntimeArchiveManifest{
		ProjectRuntimeArchiveID: "project_runtime_archive_test",
		ProjectSlug:             "osint-tools",
		SourceKind:              "storage_view",
		SourceRef:               "main/Backups/Projects/osint-tools/current",
		TargetPath:              "main/Archive/Projects/osint-tools",
		RuntimeOwnershipRule:    "runtime ownership preserved",
		SuccessorPolicy:         storagearchive.ProjectRuntimeSuccessorPolicy{Status: "not_migrated"},
	}
	archiveState := json.RawMessage(`{"status":"archived","project_runtime_archive_id":"project_runtime_archive_test","runtime_manifest_path":"/srv/loom/archive/osint-tools/runtime.json","source_ref":"main/Backups/Projects/osint-tools/current","target_path":"main/Archive/Projects/osint-tools","safe_to_delete":true}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/projects/osint-tools/archive":
			sawArchive = true
			var input storagearchive.ProjectArchiveInput
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode archive body: %v", err)
			}
			if !input.DryRun || input.TargetPath != "main/Archive/Projects/osint-tools" {
				t.Fatalf("unexpected archive input: %#v", input)
			}
			response.WriteJSON(w, http.StatusOK, response.Success("corr_test", storagearchive.ProjectArchiveResult{
				Project:             detail,
				RuntimeManifest:     manifest,
				RuntimeManifestPath: "/srv/loom/archive/osint-tools/runtime.json",
				ArchiveState:        archiveState,
				DryRun:              true,
				SafeToDelete:        true,
			}))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/projects/osint-tools/archive/inspect":
			sawInspect = true
			response.WriteJSON(w, http.StatusOK, response.Success("corr_test", storagearchive.ProjectArchiveInspectResult{
				Project:             detail,
				ArchiveState:        archiveState,
				RuntimeManifest:     &manifest,
				RuntimeManifestPath: "/srv/loom/archive/osint-tools/runtime.json",
			}))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/projects/osint-tools/archive/restore":
			sawRestore = true
			var input storagearchive.ProjectArchiveRestoreInput
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode restore body: %v", err)
			}
			if !input.DryRun || input.ToNode != "macbook" {
				t.Fatalf("unexpected restore input: %#v", input)
			}
			response.WriteJSON(w, http.StatusOK, response.Success("corr_test", storagearchive.ProjectArchiveRestorePlan{
				Project:      detail,
				ArchiveState: archiveState,
				Steps:        []storagearchive.ProjectArchivePlanStep{{Key: "storage", Kind: "restore", Status: "would_restore", Summary: "Restore project files."}},
				DryRun:       true,
			}))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	configPath := writeProjectBackendInstallManifest(t, server.URL)
	stdout, stderr, err := executeRootCommand("--config", configPath, "project", "archive", "osint-tools", "--dry-run", "--to", "main/Archive/Projects/osint-tools")
	if err != nil {
		t.Fatalf("project archive returned error: %v stderr=%s stdout=%s", err, stderr, stdout)
	}
	if !strings.Contains(stdout, "Project archive: dry-run") || !strings.Contains(stdout, "loom project archive restore osint-tools --dry-run") {
		t.Fatalf("archive output missing expected guidance:\n%s", stdout)
	}
	stdout, stderr, err = executeRootCommand("--config", configPath, "project", "archive", "inspect", "osint-tools")
	if err != nil {
		t.Fatalf("project archive inspect returned error: %v stderr=%s stdout=%s", err, stderr, stdout)
	}
	if !strings.Contains(stdout, "Project archive inspect") || !strings.Contains(stdout, "project_runtime_archive_test") {
		t.Fatalf("inspect output missing expected fields:\n%s", stdout)
	}
	stdout, stderr, err = executeRootCommand("--config", configPath, "project", "archive", "restore", "osint-tools", "--dry-run", "--to-node", "macbook")
	if err != nil {
		t.Fatalf("project archive restore returned error: %v stderr=%s stdout=%s", err, stderr, stdout)
	}
	if !strings.Contains(stdout, "Project archive restore plan") || !strings.Contains(stdout, "would_restore") {
		t.Fatalf("restore output missing expected fields:\n%s", stdout)
	}
	if !sawArchive || !sawInspect || !sawRestore {
		t.Fatalf("expected archive, inspect, and restore endpoints, saw archive=%t inspect=%t restore=%t", sawArchive, sawInspect, sawRestore)
	}
}

func TestProjectScaffoldCleanupCommandRemovesUntouchedExamples(t *testing.T) {
	dir := t.TempDir()
	scaffold, err := projectcontracts.ScaffoldProject(projectcontracts.ScaffoldOptions{
		Name:      "Cleanup CLI",
		OwnerNode: "main",
		Directory: dir,
		Facets:    []string{"scripts", "workflows", "schedules", "direct_events"},
	})
	if err != nil {
		t.Fatalf("scaffold project: %v", err)
	}
	stdout, stderr, err := executeRootCommand("project", "scaffold", "cleanup", scaffold.ProjectRoot)
	if err != nil {
		t.Fatalf("scaffold cleanup returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{
		"Project scaffold cleanup: cleaned",
		"Packages: 4 removed",
		"loom project validate " + scaffold.ProjectRoot,
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
	for _, rel := range []string{
		"scripts/hello_world",
		"workflows/example_workflow",
		"schedules/example_schedule",
		"direct_events/example_event",
	} {
		if _, err := os.Stat(filepath.Join(scaffold.ProjectRoot, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed, stat err=%v", rel, err)
		}
	}
}

func TestProjectScaffoldCleanupCommandDryRunJSON(t *testing.T) {
	dir := t.TempDir()
	scaffold, err := projectcontracts.ScaffoldProject(projectcontracts.ScaffoldOptions{
		Name:      "Cleanup CLI Dry",
		OwnerNode: "main",
		Directory: dir,
		Facets:    []string{"scripts", "workflows", "schedules", "direct_events"},
	})
	if err != nil {
		t.Fatalf("scaffold project: %v", err)
	}
	stdout, stderr, err := executeRootCommand("--json", "project", "scaffold", "cleanup", scaffold.ProjectRoot, "--dry-run")
	if err != nil {
		t.Fatalf("scaffold cleanup dry-run returned error: %v stderr=%s", err, stderr)
	}
	var result projectcontracts.ScaffoldCleanupResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode scaffold cleanup json: %v output=%s", err, stdout)
	}
	if !result.DryRun || len(result.Removed) != 4 || result.Validation.State != projectcontracts.ScaffoldValidationPlannedOnly {
		t.Fatalf("unexpected cleanup dry-run result: %#v", result)
	}
	if _, err := os.Stat(filepath.Join(scaffold.ProjectRoot, "scripts", "hello_world")); err != nil {
		t.Fatalf("dry-run should leave scaffold examples in place: %v", err)
	}
}

func TestProjectScaffoldCommandExistingDestinationRequiresForce(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := executeRootCommand(
		"project", "scaffold", "Existing Project",
		"--owner-node", "main",
		"--directory", dir,
	); err != nil {
		t.Fatalf("initial scaffold returned error: %v", err)
	}
	if _, _, err := executeRootCommand(
		"project", "scaffold", "Existing Project",
		"--owner-node", "main",
		"--directory", dir,
	); err == nil {
		t.Fatal("expected existing destination to fail without force")
	}
	if _, _, err := executeRootCommand(
		"project", "scaffold", "Existing Project",
		"--owner-node", "main",
		"--directory", dir,
		"--force",
	); err != nil {
		t.Fatalf("forced scaffold returned error: %v", err)
	}
}

func initCLIBoxFixture(t *testing.T, profile string) string {
	t.Helper()
	boxStateRoot := useIsolatedCLIBoxState(t)
	root := filepath.Join(t.TempDir(), "LOOM Box")
	resolved := box.Resolved{
		RootPath:         root,
		PathSource:       "test",
		Profile:          profile,
		ProfileSource:    "test",
		OwnerNode:        "macbook",
		NodeRole:         "workspace",
		RuntimeStateRoot: boxStateRoot,
		LegacyStateRoot:  filepath.Join(root, ".loom", "state"),
	}
	if _, err := box.Init(box.InitInput{Resolved: resolved}); err != nil {
		t.Fatalf("init test Box: %v", err)
	}
	return root
}

func TestBuildRegisterProjectContractInput(t *testing.T) {
	root := writeProjectContractFixture(t, `
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: gmail-automation
  name: Gmail Automation
  owner_node: main
facets:
  notes: true
`)
	if err := os.Mkdir(filepath.Join(root, "notes"), 0o755); err != nil {
		t.Fatalf("mkdir notes: %v", err)
	}

	analysis := projectcontracts.Analyze(root)
	input, err := buildRegisterProjectContractInput(analysis)
	if err != nil {
		t.Fatalf("build register input: %v", err)
	}
	if input.Project.Slug != "gmail-automation" || input.Project.OwnerNode != "main" {
		t.Fatalf("unexpected project input: %#v", input.Project)
	}
	if !strings.HasPrefix(input.ContractHash, "sha256:") || len(input.ContractHash) != len("sha256:")+64 {
		t.Fatalf("unexpected contract hash: %s", input.ContractHash)
	}
	if len(input.Facets) != 1 || input.Facets[0].Key != "notes" || !input.Facets[0].Present {
		t.Fatalf("unexpected facets: %#v", input.Facets)
	}
	if !json.Valid(input.Contract) || !json.Valid(input.ValidationReport) || !json.Valid(input.RegistrationPlan) {
		t.Fatal("expected valid JSON snapshots")
	}

	input2, err := buildRegisterProjectContractInput(projectcontracts.Analyze(root))
	if err != nil {
		t.Fatalf("second build register input: %v", err)
	}
	if input.ContractHash != input2.ContractHash {
		t.Fatalf("hash changed for unchanged contract: %s != %s", input.ContractHash, input2.ContractHash)
	}
}

func TestBuildRegisterProjectContractInputRepositorySourceScenarios(t *testing.T) {
	tests := []struct {
		name        string
		analysis    func(*testing.T) projectcontracts.Analysis
		wantID      string
		wantSource  bool
		wantMembers int
	}{
		{name: "v0.3 without repositories", analysis: cliLegacyRegistrationAnalysis},
		{name: "v0.4 empty repositories", analysis: func(t *testing.T) projectcontracts.Analysis { return cliV04RegistrationAnalysis(t, false) }, wantID: "project_01ARZ3NDEKTSV4RRFFQ69G5FAV", wantSource: true},
		{name: "v0.4 populated repositories", analysis: func(t *testing.T) projectcontracts.Analysis { return cliV04RegistrationAnalysis(t, true) }, wantID: "project_01ARZ3NDEKTSV4RRFFQ69G5FAV", wantSource: true, wantMembers: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			analysis := test.analysis(t)
			input, err := buildRegisterProjectContractInput(analysis)
			if err != nil {
				t.Fatalf("build register input: %v", err)
			}
			if input.Project.ID != test.wantID {
				t.Fatalf("project ID = %q, want %q", input.Project.ID, test.wantID)
			}
			if (input.RepositorySource != nil) != test.wantSource {
				t.Fatalf("repository source = %#v, want present %t", input.RepositorySource, test.wantSource)
			}
			if test.wantSource && (analysis.Report.RepositorySource == nil ||
				input.RepositorySource.ContractPath != analysis.Report.RepositorySource.ContractPath ||
				input.RepositorySource.ContractHash != analysis.Report.RepositorySource.ContractHash ||
				input.RepositorySource.ContractSchemaVersion != analysis.Report.RepositorySource.ContractSchemaVersion) {
				t.Fatalf("direct input source %#v does not match analysis source %#v", input.RepositorySource, analysis.Report.RepositorySource)
			}
			if len(analysis.Plan.RepositoryMembers) != test.wantMembers {
				t.Fatalf("repository members = %d, want %d", len(analysis.Plan.RepositoryMembers), test.wantMembers)
			}
		})
	}
}

func cliLegacyRegistrationAnalysis(t *testing.T) projectcontracts.Analysis {
	t.Helper()
	root := writeProjectContractFixture(t, `
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: legacy-direct-registration
  name: Legacy Direct Registration
  owner_node: main
`)
	return projectcontracts.Analyze(root)
}

func cliV04RegistrationAnalysis(t *testing.T, populated bool) projectcontracts.Analysis {
	t.Helper()
	root := t.TempDir()
	projectPath := filepath.Join(root, projectcontracts.CanonicalRootContractPath)
	if err := os.MkdirAll(filepath.Dir(projectPath), 0o755); err != nil {
		t.Fatal(err)
	}
	projectRaw := `kind: loom.project
schema_version: project.contract.v0.4
project:
  id: project_01ARZ3NDEKTSV4RRFFQ69G5FAV
  slug: direct-registration
  name: Direct Registration
  owner_node: main
facets:
  repos: true
`
	if err := os.WriteFile(projectPath, []byte(projectRaw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "repos"), 0o755); err != nil {
		t.Fatal(err)
	}
	members := "  members: []\n"
	if populated {
		members = `  members:
    - id: repo_01ARZ3NDEKTSV4RRFFQ69G5FAV
      key: backend
      path: backend
      role: primary
`
	}
	reposPath := filepath.Join(root, ".loom", "contracts", "repos.yaml")
	if err := os.MkdirAll(filepath.Dir(reposPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reposPath, []byte(`kind: loom.repos
schema_version: repos.contract.v0.4
repos:
  status: active
  watch_roots: []
`+members), 0o600); err != nil {
		t.Fatal(err)
	}
	analysis := projectcontracts.Analyze(root)
	if !analysis.Report.OK {
		t.Fatalf("analysis failed: %#v", analysis.Report.Diagnostics)
	}
	return analysis
}

func TestBuildRegisterProjectContractInputHashChangesAfterContractEdit(t *testing.T) {
	root := writeProjectContractFixture(t, `
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: gmail-automation
  name: Gmail Automation
  description: First version
  owner_node: main
facets:
  notes: true
`)
	if err := os.Mkdir(filepath.Join(root, "notes"), 0o755); err != nil {
		t.Fatalf("mkdir notes: %v", err)
	}

	input, err := buildRegisterProjectContractInput(projectcontracts.Analyze(root))
	if err != nil {
		t.Fatalf("build first register input: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, projectcontracts.LegacyRootContractPath), []byte(strings.TrimSpace(`
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: gmail-automation
  name: Gmail Automation
  description: Second version
  owner_node: main
facets:
  notes: true
`)+"\n"), 0o600); err != nil {
		t.Fatalf("rewrite project contract: %v", err)
	}

	updated, err := buildRegisterProjectContractInput(projectcontracts.Analyze(root))
	if err != nil {
		t.Fatalf("build updated register input: %v", err)
	}
	if input.ContractHash == updated.ContractHash {
		t.Fatalf("hash should change after contract mutation: %s", input.ContractHash)
	}
}

func TestProjectRegisterCommandFailsInvalidContractBeforeTransport(t *testing.T) {
	root := writeProjectContractFixture(t, `
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: Invalid Slug
  name: Gmail Automation
  owner_node: main
`)
	stdout, _, err := executeRootCommand("project", "register", root)
	if err == nil {
		t.Fatal("expected invalid register to fail")
	}
	if !strings.Contains(stdout, "project.slug_invalid") {
		t.Fatalf("expected validation output before transport, got:\n%s", stdout)
	}
}

func TestProjectRegisterStrictFailsWarningsBeforeTransport(t *testing.T) {
	root := writeProjectContractFixture(t, `
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: gmail-automation
  name: Gmail Automation
  owner_node: main
facets:
  workflows: true
`)
	stdout, _, err := executeRootCommand("project", "register", root, "--strict")
	if err == nil {
		t.Fatal("expected strict warning-only register to fail")
	}
	if !strings.Contains(stdout, "facet.folder_missing") {
		t.Fatalf("expected strict validation output before transport, got:\n%s", stdout)
	}
}

func TestNormalizeProjectRootOverride(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)

	got, err := normalizeProjectRootOverride(".")
	if err != nil {
		t.Fatalf("normalize relative root returned error: %v", err)
	}
	if got != filepath.Clean(root) {
		t.Fatalf("relative root = %q, want %q", got, filepath.Clean(root))
	}

	got, err = normalizeProjectRootOverride("")
	if err != nil {
		t.Fatalf("normalize empty root returned error: %v", err)
	}
	if got != "" {
		t.Fatalf("empty root = %q, want empty", got)
	}

	absolute := filepath.Join(root, "child", "..")
	got, err = normalizeProjectRootOverride(absolute)
	if err != nil {
		t.Fatalf("normalize absolute root returned error: %v", err)
	}
	if got != filepath.Clean(absolute) {
		t.Fatalf("absolute root = %q, want %q", got, filepath.Clean(absolute))
	}
}

func executeRootCommand(args ...string) (string, string, error) {
	cmd := NewRootCommand()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func testStringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func writeProjectContractFixture(t *testing.T, payload string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, projectcontracts.LegacyRootContractPath), []byte(strings.TrimSpace(payload)+"\n"), 0o600); err != nil {
		t.Fatalf("write project contract: %v", err)
	}
	return root
}

func projectArchiveCommandTestDetail() projects.ProjectRegistrationDetail {
	homeNode := "main"
	return projects.ProjectRegistrationDetail{
		Project: projects.ProjectDetail{Project: projects.Project{
			ProjectID:       "project_osint_tools",
			ProjectScopeID:  "scope_osint_tools",
			ProjectScopeKey: "project:osint-tools",
			Slug:            "osint-tools",
			Name:            "OSINT Tools",
			Status:          "active",
			HomeNodeID:      &homeNode,
		}},
		Registration: &projects.ProjectContractRegistration{
			ProjectContractRegistrationID: "project_contract_registration_osint_tools",
			ProjectID:                     "project_osint_tools",
			ProjectRoot:                   "/home/loomadmin/loom-box/Projects/osint-tools",
			ContractPath:                  "/home/loomadmin/loom-box/Projects/osint-tools/loom.project.yaml",
			ContractHash:                  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			RegistrationStatus:            "registered",
			ActivationStatus:              projects.ProjectActivationStatusBaseActive,
			RegistrationRevision:          1,
		},
	}
}

func writeProjectBackendInstallManifest(t *testing.T, mainURL string) string {
	t.Helper()
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "install.yaml")
	if err := setup.WriteManifest(manifestPath, setup.InstallManifest{
		SchemaVersion:     setup.ManifestSchemaVersion,
		NodeKey:           "macbook",
		NodeID:            "node_macbook",
		NodeKind:          "workspace",
		NodeRole:          "primary_workspace",
		RuntimeClass:      "workspace_full",
		MainURL:           mainURL,
		DataDir:           filepath.Join(dir, "data"),
		BoxPath:           filepath.Join(dir, "loom-box"),
		BoxProfile:        "workspace",
		MigrationsDir:     "migrations",
		BootstrapMode:     "none",
		ObjectStorePath:   filepath.Join(dir, "data", "object-store"),
		MainDocumentsPath: filepath.Join(dir, "data", "main-documents"),
		StorageExportRoot: filepath.Join(dir, "data", "storage-views", "main-export"),
	}); err != nil {
		t.Fatalf("write install manifest: %v", err)
	}
	return manifestPath
}

func TestProjectCreateCallerLocalOutputAndLegacyScaffold(t *testing.T) {
	parent := t.TempDir()
	stdout, stderr, err := executeRootCommand("--json", "project", "create", "Local plain", "--owner-node", "main", "--directory", parent)
	if err != nil {
		t.Fatalf("create: %v %s", err, stderr)
	}
	var result projectcontracts.ScaffoldResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.ExecutionLocation != "caller_local" || result.OwnerNode != "main" || result.Mode != projectcontracts.ScaffoldModeDeclaration {
		t.Fatalf("location: %+v", result)
	}
	stdout, stderr, err = executeRootCommand("--json", "project", "scaffold", "Legacy plain", "--owner-node", "main", "--preset", "minimal", "--directory", parent)
	if err != nil {
		t.Fatalf("legacy scaffold: %v %s", err, stderr)
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatal(err)
	}
	loaded, err := projectcontracts.LoadProject(result.ProjectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Contract.SchemaVersion != projectcontracts.ProjectSchemaV04 || !loaded.Contract.Facets["notes"] || !loaded.Contract.Facets["backup_policy"] {
		t.Fatal("legacy preset default changed")
	}
}

func TestProjectCreateBackendResolution(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodPost || r.URL.Path != "/v1/project-scaffolds" {
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(400)
			return
		}
		var input projectcontracts.ScaffoldOptions
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		if input.Mode != projectcontracts.ScaffoldModeDeclaration || input.Directory != "" || input.OwnerNode != "main" || !input.DryRun {
			t.Errorf("input: %+v", input)
		}
		response.WriteJSON(w, http.StatusOK, response.Success("corr_test", projectcontracts.ScaffoldResult{OK: true, Mode: projectcontracts.ScaffoldModeDeclaration, ProjectRoot: "/backend/box/Projects/backend-plain", OwnerNode: "main", DirSource: projectcontracts.ScaffoldDirectoryBox, BoxDefault: true, Files: []projectcontracts.ScaffoldFileResult{}}))
	}))
	defer server.Close()
	configPath := writeProjectBackendInstallManifest(t, server.URL)
	stdout, stderr, err := executeRootCommand("--json", "--config", configPath, "project", "create", "Backend plain", "--owner-node", "main", "--backend", "--dry-run")
	if err != nil {
		t.Fatalf("backend: %v %s", err, stderr)
	}
	var result projectcontracts.ScaffoldResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatal(err)
	}
	if !called || result.ExecutionLocation != "configured_backend" || result.ProjectRoot != "/backend/box/Projects/backend-plain" {
		t.Fatalf("location: %+v", result)
	}
}

func TestProjectCreateRegistrationFailurePreservesCreatedFiles(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/v1/project-scaffolds" {
			response.WriteJSON(w, http.StatusServiceUnavailable, struct {
				response.ErrorEnvelope
				Data projectcontracts.ScaffoldResult `json:"data"`
			}{response.ErrorEnvelope{Error: response.ErrorBody{Code: "project.context_pending", Summary: "Registration unavailable"}}, projectcontracts.ScaffoldResult{SourceState: "source_created", ContextState: "context_pending", Mode: projectcontracts.ScaffoldModeDeclaration, ProjectID: "project_01ARZ3NDEKTSV4RRFFQ69G5FAV", ProjectRoot: "/fixture/created", Slug: "created", Files: []projectcontracts.ScaffoldFileResult{{Path: ".loom/project.yaml", Action: "created"}}}})
			return
		}
		http.Error(w, "fixture registration unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	configPath := writeProjectBackendInstallManifest(t, server.URL)
	stdout, _, err := executeRootCommand("--json", "--config", configPath, "project", "create", "Created", "--owner-node", "main", "--backend", "--register")
	if err == nil || calls != 1 {
		t.Fatalf("registration refusal not returned: %v calls=%d", err, calls)
	}
	var partial projectcontracts.ScaffoldResult
	if err := json.NewDecoder(strings.NewReader(stdout)).Decode(&partial); err != nil {
		t.Fatal(err)
	}
	if partial.ContextState != "context_pending" || partial.ProjectRoot != "/fixture/created" || len(partial.Files) != 1 || partial.Files[0].Action != "created" {
		t.Fatalf("partial effects lost: %s", stdout)
	}
}
