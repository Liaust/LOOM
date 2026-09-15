package httpapi

import (
	"archive/tar"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectdoctor"
	"loom.local/loom/internal/projectexport"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectwatch"
	"loom.local/loom/internal/response"
)

func TestProjectLocalLoomAcceptanceThroughHTTPClient(t *testing.T) {
	root := t.TempDir()
	resolved := box.Resolved{RootPath: root, Profile: box.ProfileMain, OwnerNode: "main", NodeRole: "main"}
	if _, err := box.Init(box.InitInput{Resolved: resolved}); err != nil {
		t.Fatalf("box init failed: %v", err)
	}
	server := httptest.NewServer(NewServer(Services{
		RuntimeConfig: config.Config{NodeID: "main", NodeRole: "main", BoxPath: root, BoxProfile: box.ProfileMain},
		ProjectWatch:  projectwatch.NewService(projectwatch.Deps{}),
	}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler())
	defer server.Close()
	client, err := localclient.NewHTTP(server.URL)
	if err != nil {
		t.Fatalf("new local client: %v", err)
	}
	ctx := context.Background()

	scaffoldEnvelope, err := client.ScaffoldProject(ctx, "corr_acceptance", projectcontracts.ScaffoldOptions{
		Name:      "Project Local Acceptance",
		OwnerNode: "main",
		Preset:    projectcontracts.PresetMinimal,
		Facets:    []string{"docs"},
	})
	if err != nil {
		t.Fatalf("scaffold through HTTP client: %v", err)
	}
	scaffold := scaffoldEnvelope.Data
	if scaffold.ContractPath != filepath.Join(scaffold.ProjectRoot, filepath.FromSlash(projectcontracts.CanonicalRootContractPath)) {
		t.Fatalf("canonical contract path = %q", scaffold.ContractPath)
	}

	facetsEnvelope, err := client.AddProjectFacets(ctx, "corr_acceptance", projectcontracts.AddProjectFacetsOptions{
		ProjectRef: scaffold.Slug,
		Facets:     []string{"notes", "scripts"},
	})
	if err != nil {
		t.Fatalf("add facets through HTTP client: %v", err)
	}
	if got := strings.Join(facetsEnvelope.Data.AddedFacets, ","); got != "notes,scripts" {
		t.Fatalf("added facets = %q, want notes,scripts", got)
	}

	analysisEnvelope, err := client.AnalyzeProjectContractBackend(ctx, "corr_acceptance", projectcontracts.BackendAnalysisInput{ProjectRef: scaffold.Slug})
	if err != nil {
		t.Fatalf("validate/plan/doctor through HTTP client: %v", err)
	}
	analysis := analysisEnvelope.Data.Analysis
	if analysis.Loaded == nil || analysis.Loaded.Layout != projectcontracts.ProjectLayoutCanonical || !analysis.Report.OK || !analysis.Plan.Registerable {
		t.Fatalf("unexpected backend analysis: %#v", analysisEnvelope.Data)
	}
	registrationInput, err := buildProjectContractRegistrationInputHTTP(analysis, "acceptance")
	if err != nil {
		t.Fatalf("prepare registration: %v", err)
	}
	if registrationInput.ContractPath != scaffold.ContractPath {
		t.Fatalf("registration contract path = %q, want %q", registrationInput.ContractPath, scaffold.ContractPath)
	}
	watchEnvelope, err := client.BuildProjectWatchPlan(ctx, "corr_acceptance", scaffold.Slug, projectwatch.BuildPlanInput{ProjectRoot: scaffold.ProjectRoot})
	if err != nil {
		t.Fatalf("watch plan through HTTP client: %v", err)
	}
	if watchEnvelope.Data.ContractHash == "" || watchEnvelope.Data.ProjectRoot != scaffold.ProjectRoot {
		t.Fatalf("unexpected watch plan: %#v", watchEnvelope.Data)
	}
	exportOutput := filepath.Join(t.TempDir(), "backend-portable.tar")
	exportResult, err := client.ExportProject(ctx, "corr_acceptance", projectexport.Request{ProjectRef: scaffold.Slug, Mode: projectexport.ModePortable}, exportOutput, false)
	if err != nil {
		t.Fatalf("export through HTTP client: %v", err)
	}
	if exportResult.OutputPath != exportOutput || exportResult.Mode != projectexport.ModePortable || exportResult.ArchiveChecksum == "" {
		t.Fatalf("unexpected caller-local export result: %#v", exportResult)
	}
	exportFile, err := os.Open(exportOutput)
	if err != nil {
		t.Fatalf("open downloaded export: %v", err)
	}
	exportReader := tar.NewReader(exportFile)
	foundContract := false
	for {
		header, nextErr := exportReader.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			t.Fatalf("read downloaded export: %v", nextErr)
		}
		if header.Name == projectcontracts.CanonicalRootContractPath {
			foundContract = true
		}
	}
	_ = exportFile.Close()
	if !foundContract {
		t.Fatal("backend export bytes did not contain canonical project contract")
	}

	for _, path := range []string{
		projectcontracts.CanonicalRootContractPath,
		".loom/agents/project.md",
		".loom/agents/surfaces/notes.md",
		".loom/agents/surfaces/scripts.md",
		".loom/tools/validate-project.sh",
		"AGENTS.md",
	} {
		if _, err := os.Stat(filepath.Join(scaffold.ProjectRoot, filepath.FromSlash(path))); err != nil {
			t.Fatalf("expected acceptance file %s: %v", path, err)
		}
	}
	for _, path := range []string{
		projectcontracts.LegacyRootContractPath,
		"policies",
		"notes/README.md",
		"notes/AGENTS.md",
		"scripts/README.md",
		"scripts/AGENTS.md",
		"tests/validate_project.sh",
	} {
		if _, err := os.Stat(filepath.Join(scaffold.ProjectRoot, filepath.FromSlash(path))); !os.IsNotExist(err) {
			t.Fatalf("unexpected scaffold clutter %s, stat err=%v", path, err)
		}
	}
	agentEntry, err := os.ReadFile(filepath.Join(scaffold.ProjectRoot, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read root agent entry: %v", err)
	}
	for _, target := range []string{".loom/project.yaml", ".loom/agents/project.md", ".loom/agents/surfaces/"} {
		if !strings.Contains(string(agentEntry), target) {
			t.Fatalf("root AGENTS.md missing %q:\n%s", target, agentEntry)
		}
	}

	projectsRoot := box.Inspect(resolved).DefaultProjectPath
	legacyRoot := filepath.Join(projectsRoot, "legacy-acceptance")
	writeLegacyProjectContractHTTP(t, legacyRoot, "active")
	dryRun, err := client.MigrateProjectLayout(ctx, "corr_acceptance", projectcontracts.LayoutMigrationOptions{ProjectRef: "legacy-acceptance"})
	if err != nil || !dryRun.Data.DryRun || len(dryRun.Data.Actions) == 0 {
		t.Fatalf("legacy migration dry-run = %#v, err=%v", dryRun.Data, err)
	}
	applied, err := client.MigrateProjectLayout(ctx, "corr_acceptance", projectcontracts.LayoutMigrationOptions{ProjectRef: "legacy-acceptance", Apply: true, Yes: true})
	if err != nil || !applied.Data.Applied || applied.Data.AfterLayout != projectcontracts.ProjectLayoutCanonical {
		t.Fatalf("legacy migration apply = %#v, err=%v", applied.Data, err)
	}
	idempotent, err := client.MigrateProjectLayout(ctx, "corr_acceptance", projectcontracts.LayoutMigrationOptions{ProjectRef: "legacy-acceptance"})
	if err != nil || len(idempotent.Data.Actions) != 0 || idempotent.Data.BeforeLayout != projectcontracts.ProjectLayoutCanonical {
		t.Fatalf("idempotent migration = %#v, err=%v", idempotent.Data, err)
	}

	archivedRoot := filepath.Join(projectsRoot, "archived-acceptance")
	writeLegacyProjectContractHTTP(t, archivedRoot, "archived")
	_, err = client.MigrateProjectLayout(ctx, "corr_acceptance", projectcontracts.LayoutMigrationOptions{ProjectRef: "archived-acceptance", Apply: true, Yes: true})
	requestErr, ok := err.(*localclient.RequestError)
	if !ok || requestErr.StatusCode != http.StatusConflict || requestErr.Envelope.Error.Code != "project_runtime.archived" {
		t.Fatalf("archived migration error = %#v, want project_runtime.archived conflict", err)
	}
}

func TestBackendRegistrationInputRepositorySourceScenarios(t *testing.T) {
	tests := []struct {
		name        string
		analysis    func(*testing.T) projectcontracts.Analysis
		wantID      bool
		wantSource  bool
		wantMembers int
	}{
		{name: "v0.3 without repositories", analysis: backendLegacyRegistrationAnalysis},
		{name: "v0.4 empty repositories", analysis: func(t *testing.T) projectcontracts.Analysis { return backendV04RegistrationAnalysis(t, false) }, wantID: true, wantSource: true},
		{name: "v0.4 populated repositories", analysis: func(t *testing.T) projectcontracts.Analysis { return backendV04RegistrationAnalysis(t, true) }, wantID: true, wantSource: true, wantMembers: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			analysis := test.analysis(t)
			input, err := buildProjectContractRegistrationInputHTTP(analysis, "backend.test")
			if err != nil {
				t.Fatalf("build backend registration input: %v", err)
			}
			if (input.Project.ID != "") != test.wantID {
				t.Fatalf("project ID = %q, want present %t", input.Project.ID, test.wantID)
			}
			if input.Project.ID != analysis.Plan.Project.ID {
				t.Fatalf("project ID = %q, want analysis plan ID %q", input.Project.ID, analysis.Plan.Project.ID)
			}
			if (input.RepositorySource != nil) != test.wantSource {
				t.Fatalf("repository source = %#v, want present %t", input.RepositorySource, test.wantSource)
			}
			if test.wantSource && (analysis.Report.RepositorySource == nil ||
				input.RepositorySource.ContractPath != analysis.Report.RepositorySource.ContractPath ||
				input.RepositorySource.ContractHash != analysis.Report.RepositorySource.ContractHash ||
				input.RepositorySource.ContractSchemaVersion != analysis.Report.RepositorySource.ContractSchemaVersion) {
				t.Fatalf("backend input source %#v does not match analysis source %#v", input.RepositorySource, analysis.Report.RepositorySource)
			}
			if len(analysis.Plan.RepositoryMembers) != test.wantMembers {
				t.Fatalf("repository members = %d, want %d", len(analysis.Plan.RepositoryMembers), test.wantMembers)
			}
		})
	}
}

func backendLegacyRegistrationAnalysis(t *testing.T) projectcontracts.Analysis {
	t.Helper()
	root := filepath.Join(t.TempDir(), "legacy-backend-registration")
	writeLegacyProjectContractHTTP(t, root, "active")
	analysis := projectcontracts.Analyze(root)
	if !analysis.Report.OK {
		t.Fatalf("analysis failed: %#v", analysis.Report.Diagnostics)
	}
	return analysis
}

func backendV04RegistrationAnalysis(t *testing.T, populated bool) projectcontracts.Analysis {
	t.Helper()
	options := projectcontracts.ScaffoldOptions{
		Name:      "Backend Registration",
		Slug:      "backend-registration",
		OwnerNode: "main",
		Preset:    projectcontracts.PresetMinimal,
		Facets:    []string{"repos"},
		Directory: t.TempDir(),
	}
	if populated {
		options.RepositoryMembers = []projectcontracts.RepoMemberSpec{{Key: "backend", Path: "backend", Role: projectcontracts.RepositoryRolePrimary}}
	}
	result, err := projectcontracts.ScaffoldProject(options)
	if err != nil {
		t.Fatalf("scaffold fixture: %v", err)
	}
	analysis := projectcontracts.Analyze(result.ProjectRoot)
	if !analysis.Report.OK {
		t.Fatalf("analysis failed: %#v", analysis.Report.Diagnostics)
	}
	return analysis
}

func TestBackendProjectExportRejectsRootsOutsideCanonicalProjectsBoundary(t *testing.T) {
	boxRoot := t.TempDir()
	resolved := box.Resolved{RootPath: boxRoot, Profile: box.ProfileMain, OwnerNode: "main", NodeRole: "main"}
	if _, err := box.Init(box.InitInput{Resolved: resolved}); err != nil {
		t.Fatalf("box init failed: %v", err)
	}
	projectsRoot := box.Inspect(resolved).DefaultProjectPath
	outsideParent := t.TempDir()
	outsideProject, err := projectcontracts.ScaffoldProject(projectcontracts.ScaffoldOptions{
		Name:      "Outside Project",
		Slug:      "outside-project",
		OwnerNode: "main",
		Directory: outsideParent,
		Preset:    projectcontracts.PresetMinimal,
	})
	if err != nil {
		t.Fatalf("scaffold outside project: %v", err)
	}
	directLink := filepath.Join(projectsRoot, "outside-link")
	if err := os.Symlink(outsideProject.ProjectRoot, directLink); err != nil {
		t.Fatalf("create project-root symlink: %v", err)
	}
	ancestorLink := filepath.Join(projectsRoot, "outside-parent")
	if err := os.Symlink(outsideParent, ancestorLink); err != nil {
		t.Fatalf("create ancestor symlink: %v", err)
	}

	serverValue := NewServer(Services{
		RuntimeConfig: config.Config{NodeID: "main", NodeRole: "main", BoxPath: boxRoot, BoxProfile: box.ProfileMain},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	serverValue.projectRegistrationStatus = func(_ context.Context, ref string) (projects.ProjectRegistrationDetail, error) {
		if ref != "registered-outside" {
			return projects.ProjectRegistrationDetail{}, sql.ErrNoRows
		}
		return projects.ProjectRegistrationDetail{Registration: &projects.ProjectContractRegistration{ProjectRoot: directLink}}, nil
	}
	server := httptest.NewServer(serverValue.Handler())
	defer server.Close()
	client, err := localclient.NewHTTP(server.URL)
	if err != nil {
		t.Fatalf("new local client: %v", err)
	}

	tests := []struct {
		name    string
		request projectexport.Request
	}{
		{name: "request supplied symlink root", request: projectexport.Request{ProjectRoot: directLink, Mode: projectexport.ModePortable}},
		{name: "project ref fallback symlink root", request: projectexport.Request{ProjectRef: "outside-link", Mode: projectexport.ModePortable}},
		{name: "registered symlink root", request: projectexport.Request{ProjectRef: "registered-outside", Mode: projectexport.ModePortable}},
		{name: "canonical root escapes through symlinked ancestor", request: projectexport.Request{ProjectRoot: filepath.Join(ancestorLink, outsideProject.Slug), Mode: projectexport.ModePortable}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "rejected.tar")
			_, exportErr := client.ExportProject(context.Background(), "corr_boundary", test.request, output, false)
			if exportErr == nil {
				t.Fatal("backend export accepted a project root outside the canonical Projects boundary")
			}
			requestErr, ok := exportErr.(*localclient.RequestError)
			if !ok || requestErr.Envelope.Error.Code != "project.export_target_invalid" {
				t.Fatalf("backend export boundary error = %#v", exportErr)
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatalf("rejected backend export left output behind: %v", err)
			}
		})
	}
}

func TestBackendProjectExportPreservesInternalProjectSymlinks(t *testing.T) {
	boxRoot := t.TempDir()
	resolved := box.Resolved{RootPath: boxRoot, Profile: box.ProfileMain, OwnerNode: "main", NodeRole: "main"}
	if _, err := box.Init(box.InitInput{Resolved: resolved}); err != nil {
		t.Fatalf("box init failed: %v", err)
	}
	projectsRoot := box.Inspect(resolved).DefaultProjectPath
	project, err := projectcontracts.ScaffoldProject(projectcontracts.ScaffoldOptions{
		Name:      "Internal Symlink",
		Slug:      "internal-symlink",
		OwnerNode: "main",
		Directory: projectsRoot,
		Preset:    projectcontracts.PresetMinimal,
	})
	if err != nil {
		t.Fatalf("scaffold accepted project: %v", err)
	}
	readme := filepath.Join(project.ProjectRoot, "README.md")
	if err := os.WriteFile(readme, []byte("read me\n"), 0o644); err != nil {
		t.Fatalf("write symlink target: %v", err)
	}
	if err := os.Symlink("README.md", filepath.Join(project.ProjectRoot, "readme-link")); err != nil {
		t.Fatalf("create internal project symlink: %v", err)
	}

	server := httptest.NewServer(NewServer(Services{
		RuntimeConfig: config.Config{NodeID: "main", NodeRole: "main", BoxPath: boxRoot, BoxProfile: box.ProfileMain},
	}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler())
	defer server.Close()
	client, err := localclient.NewHTTP(server.URL)
	if err != nil {
		t.Fatalf("new local client: %v", err)
	}
	output := filepath.Join(t.TempDir(), "portable.tar")
	if _, err := client.ExportProject(context.Background(), "corr_internal_symlink", projectexport.Request{ProjectRef: project.Slug, Mode: projectexport.ModePortable}, output, false); err != nil {
		t.Fatalf("backend export rejected an internal project symlink: %v", err)
	}
	archive, err := os.Open(output)
	if err != nil {
		t.Fatalf("open backend export: %v", err)
	}
	defer archive.Close()
	reader := tar.NewReader(archive)
	found := false
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read backend export: %v", err)
		}
		if header.Name == "readme-link" {
			found = header.Typeflag == tar.TypeSymlink && header.Linkname == "README.md"
		}
	}
	if !found {
		t.Fatal("backend export did not preserve the safe internal project symlink")
	}
}

func TestProjectFacetAdditionsEndpointUsesBackendBoxProject(t *testing.T) {
	root := t.TempDir()
	resolved := box.Resolved{RootPath: root, Profile: box.ProfileMain, OwnerNode: "main", NodeRole: "main"}
	if _, err := box.Init(box.InitInput{Resolved: resolved}); err != nil {
		t.Fatalf("box init failed: %v", err)
	}
	status := box.Inspect(resolved)
	if status.State != "ok" {
		t.Fatalf("box status = %s", status.State)
	}
	scaffold, err := projectcontracts.ScaffoldProject(projectcontracts.ScaffoldOptions{
		Name:      "OSINT Tools",
		Slug:      "osint-tools",
		OwnerNode: "main",
		Directory: status.DefaultProjectPath,
		Facets:    []string{"scripts"},
	})
	if err != nil {
		t.Fatalf("scaffold project: %v", err)
	}
	server := NewServer(Services{
		RuntimeConfig: config.Config{
			NodeID:     "main",
			NodeRole:   "main",
			BoxPath:    root,
			BoxProfile: box.ProfileMain,
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()

	req := httptest.NewRequest(http.MethodPost, "/v1/project-facet-additions", strings.NewReader(`{"project_ref":"osint-tools","facets":["notes"],"dry_run":true}`))
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("dry-run status = %d body=%s", res.Code, res.Body.String())
	}
	var dryRun response.Envelope[projectcontracts.AddProjectFacetsResult]
	if err := json.NewDecoder(res.Body).Decode(&dryRun); err != nil {
		t.Fatalf("decode dry-run envelope: %v", err)
	}
	if !dryRun.Data.DryRun || dryRun.Data.ProjectRoot != scaffold.ProjectRoot || !testStringSliceContainsHTTP(dryRun.Data.AddedFacets, "notes") {
		t.Fatalf("unexpected dry-run result: %#v", dryRun.Data)
	}
	if _, err := os.Stat(filepath.Join(scaffold.ProjectRoot, "notes")); !os.IsNotExist(err) {
		t.Fatalf("dry-run should not create notes folder, stat err=%v", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/project-facet-additions", strings.NewReader(`{"project_ref":"osint-tools","facets":["notes"]}`))
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("apply status = %d body=%s", res.Code, res.Body.String())
	}
	var applied response.Envelope[projectcontracts.AddProjectFacetsResult]
	if err := json.NewDecoder(res.Body).Decode(&applied); err != nil {
		t.Fatalf("decode apply envelope: %v", err)
	}
	if applied.Data.DryRun || applied.Data.ProjectRoot != scaffold.ProjectRoot || !testStringSliceContainsHTTP(applied.Data.AddedFacets, "notes") {
		t.Fatalf("unexpected apply result: %#v", applied.Data)
	}
	if _, err := os.Stat(filepath.Join(scaffold.ProjectRoot, ".loom", "contracts", "notes.yaml")); err != nil {
		t.Fatalf("expected canonical notes contract after apply: %v", err)
	}
	if _, err := os.Stat(filepath.Join(scaffold.ProjectRoot, "notes", "README.md")); !os.IsNotExist(err) {
		t.Fatalf("facet addition should not create a generic notes README, stat err=%v", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/project-contract-analyses", strings.NewReader(`{"project_ref":"osint-tools"}`))
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("analysis status = %d body=%s", res.Code, res.Body.String())
	}
	var analysis response.Envelope[projectdoctor.BackendAnalysisResult]
	if err := json.NewDecoder(res.Body).Decode(&analysis); err != nil {
		t.Fatalf("decode analysis envelope: %v", err)
	}
	if analysis.Data.Analysis.Loaded == nil || analysis.Data.Analysis.Loaded.RootPath != scaffold.ProjectRoot || !analysis.Data.Analysis.Report.Registerable {
		t.Fatalf("unexpected analysis result: %#v", analysis.Data)
	}
	if analysis.Data.DriftStatus != projectdoctor.BackendDriftUnregistered || analysis.Data.Current {
		t.Fatalf("expected unregistered drift without DB registration, got %#v", analysis.Data)
	}
}

func TestProjectFacetAdditionsEndpointBootstrapsMissingBackendProject(t *testing.T) {
	root := t.TempDir()
	resolved := box.Resolved{RootPath: root, Profile: box.ProfileMain, OwnerNode: "main", NodeRole: "main"}
	if _, err := box.Init(box.InitInput{Resolved: resolved}); err != nil {
		t.Fatalf("box init failed: %v", err)
	}
	status := box.Inspect(resolved)
	if status.State != "ok" {
		t.Fatalf("box status = %s", status.State)
	}
	projectRoot := filepath.Join(status.DefaultProjectPath, "backend-only")
	server := NewServer(Services{
		RuntimeConfig: config.Config{
			NodeID:     "main",
			NodeRole:   "main",
			BoxPath:    root,
			BoxProfile: box.ProfileMain,
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()

	req := httptest.NewRequest(http.MethodPost, "/v1/project-facet-additions", strings.NewReader(`{"project_ref":"backend-only","facets":["notes"],"dry_run":true}`))
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("dry-run status = %d body=%s", res.Code, res.Body.String())
	}
	var dryRun response.Envelope[projectcontracts.AddProjectFacetsResult]
	if err := json.NewDecoder(res.Body).Decode(&dryRun); err != nil {
		t.Fatalf("decode dry-run envelope: %v", err)
	}
	if !dryRun.Data.DryRun || dryRun.Data.ProjectRoot != projectRoot || !testStringSliceContainsHTTP(dryRun.Data.AddedFacets, "notes") {
		t.Fatalf("unexpected dry-run result: %#v", dryRun.Data)
	}
	if _, err := os.Stat(projectRoot); !os.IsNotExist(err) {
		t.Fatalf("dry-run should not create project root, stat err=%v", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/project-facet-additions", strings.NewReader(`{"project_ref":"backend-only","facets":["notes"]}`))
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("apply status = %d body=%s", res.Code, res.Body.String())
	}
	var applied response.Envelope[projectcontracts.AddProjectFacetsResult]
	if err := json.NewDecoder(res.Body).Decode(&applied); err != nil {
		t.Fatalf("decode apply envelope: %v", err)
	}
	if applied.Data.DryRun || applied.Data.ProjectRoot != projectRoot || !testStringSliceContainsHTTP(applied.Data.AddedFacets, "notes") {
		t.Fatalf("unexpected apply result: %#v", applied.Data)
	}
	if _, err := os.Stat(filepath.Join(projectRoot, ".loom", "contracts", "notes.yaml")); err != nil {
		t.Fatalf("expected canonical notes contract after apply: %v", err)
	}
}

func TestProjectLayoutMigrationsEndpointDryRunApplyAndRegistrationPath(t *testing.T) {
	root := t.TempDir()
	resolved := box.Resolved{RootPath: root, Profile: box.ProfileMain, OwnerNode: "main", NodeRole: "main"}
	if _, err := box.Init(box.InitInput{Resolved: resolved}); err != nil {
		t.Fatalf("box init failed: %v", err)
	}
	status := box.Inspect(resolved)
	if status.State != "ok" {
		t.Fatalf("box status = %s", status.State)
	}
	projectRoot := filepath.Join(status.DefaultProjectPath, "legacy-layout")
	writeLegacyProjectContractHTTP(t, projectRoot, "active")

	server := NewServer(Services{
		RuntimeConfig: config.Config{
			NodeID:     "main",
			NodeRole:   "main",
			BoxPath:    root,
			BoxProfile: box.ProfileMain,
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()

	req := httptest.NewRequest(http.MethodPost, "/v1/project-layout-migrations", strings.NewReader(`{"project_ref":"legacy-layout"}`))
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("dry-run status = %d body=%s", res.Code, res.Body.String())
	}
	var dryRun response.Envelope[projectcontracts.LayoutMigrationResult]
	if err := json.NewDecoder(res.Body).Decode(&dryRun); err != nil {
		t.Fatalf("decode dry-run envelope: %v", err)
	}
	if !dryRun.Data.OK || !dryRun.Data.DryRun || dryRun.Data.Applied || dryRun.Data.BeforeLayout != projectcontracts.ProjectLayoutLegacy || dryRun.Data.ProjectRoot != projectRoot || len(dryRun.Data.Actions) == 0 {
		t.Fatalf("unexpected dry-run result: %#v", dryRun.Data)
	}
	if strings.Join(dryRun.Data.NextActions, "\n") != strings.Join([]string{
		"loom project validate legacy-layout --backend",
		"loom project diff legacy-layout --backend",
		"loom project register legacy-layout --backend",
	}, "\n") {
		t.Fatalf("unexpected backend next actions: %#v", dryRun.Data.NextActions)
	}
	if _, err := os.Stat(filepath.Join(projectRoot, filepath.FromSlash(projectcontracts.CanonicalRootContractPath))); !os.IsNotExist(err) {
		t.Fatalf("dry-run should not create canonical contract, stat err=%v", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/project-layout-migrations", strings.NewReader(`{"project_ref":"legacy-layout","apply":true,"yes":true}`))
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("apply status = %d body=%s", res.Code, res.Body.String())
	}
	var applied response.Envelope[projectcontracts.LayoutMigrationResult]
	if err := json.NewDecoder(res.Body).Decode(&applied); err != nil {
		t.Fatalf("decode apply envelope: %v", err)
	}
	if !applied.Data.OK || applied.Data.DryRun || !applied.Data.Applied || applied.Data.BeforeLayout != projectcontracts.ProjectLayoutLegacy || applied.Data.AfterLayout != projectcontracts.ProjectLayoutCanonical || applied.Data.RecordPath == "" {
		t.Fatalf("unexpected apply result: %#v", applied.Data)
	}
	canonicalPath := filepath.Join(projectRoot, filepath.FromSlash(projectcontracts.CanonicalRootContractPath))
	if _, err := os.Stat(canonicalPath); err != nil {
		t.Fatalf("expected canonical contract after apply: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectRoot, projectcontracts.LegacyRootContractPath)); !os.IsNotExist(err) {
		t.Fatalf("expected legacy contract removal after apply, stat err=%v", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/project-contract-analyses", strings.NewReader(`{"project_ref":"legacy-layout"}`))
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("analysis status = %d body=%s", res.Code, res.Body.String())
	}
	var analysis response.Envelope[projectdoctor.BackendAnalysisResult]
	if err := json.NewDecoder(res.Body).Decode(&analysis); err != nil {
		t.Fatalf("decode analysis envelope: %v", err)
	}
	registrationInput, err := buildProjectContractRegistrationInputHTTP(analysis.Data.Analysis, "test")
	if err != nil {
		t.Fatalf("build registration input: %v", err)
	}
	if registrationInput.ContractPath != canonicalPath {
		t.Fatalf("registration contract path = %q, want %q", registrationInput.ContractPath, canonicalPath)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/project-layout-migrations", strings.NewReader(`{"project_ref":"legacy-layout"}`))
	res = httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("idempotent dry-run status = %d body=%s", res.Code, res.Body.String())
	}
	var idempotent response.Envelope[projectcontracts.LayoutMigrationResult]
	if err := json.NewDecoder(res.Body).Decode(&idempotent); err != nil {
		t.Fatalf("decode idempotent envelope: %v", err)
	}
	if !idempotent.Data.OK || idempotent.Data.BeforeLayout != projectcontracts.ProjectLayoutCanonical || len(idempotent.Data.Actions) != 0 {
		t.Fatalf("unexpected idempotent result: %#v", idempotent.Data)
	}
}

func TestProjectLayoutMigrationsEndpointRejectsArchivedApply(t *testing.T) {
	root := t.TempDir()
	resolved := box.Resolved{RootPath: root, Profile: box.ProfileMain, OwnerNode: "main", NodeRole: "main"}
	if _, err := box.Init(box.InitInput{Resolved: resolved}); err != nil {
		t.Fatalf("box init failed: %v", err)
	}
	status := box.Inspect(resolved)
	projectRoot := filepath.Join(status.DefaultProjectPath, "archived-layout")
	writeLegacyProjectContractHTTP(t, projectRoot, "archived")
	server := NewServer(Services{
		RuntimeConfig: config.Config{NodeID: "main", NodeRole: "main", BoxPath: root, BoxProfile: box.ProfileMain},
	}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()

	req := httptest.NewRequest(http.MethodPost, "/v1/project-layout-migrations", strings.NewReader(`{"project_ref":"archived-layout","apply":true,"yes":true}`))
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "project_runtime.archived") {
		t.Fatalf("archived apply status = %d body=%s", res.Code, res.Body.String())
	}
	if _, err := os.Stat(filepath.Join(projectRoot, filepath.FromSlash(projectcontracts.CanonicalRootContractPath))); !os.IsNotExist(err) {
		t.Fatalf("archived apply should not create canonical contract, stat err=%v", err)
	}
}

func writeLegacyProjectContractHTTP(t *testing.T, root, status string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir legacy project: %v", err)
	}
	raw := strings.Join([]string{
		"kind: loom.project",
		"schema_version: project.contract.v0.3",
		"project:",
		"  slug: " + filepath.Base(root),
		"  name: " + filepath.Base(root),
		"  owner_node: main",
		"  status: " + status,
		"facets:",
		"  notes: true",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(root, projectcontracts.LegacyRootContractPath), []byte(raw), 0o644); err != nil {
		t.Fatalf("write legacy project contract: %v", err)
	}
}

func testStringSliceContainsHTTP(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
