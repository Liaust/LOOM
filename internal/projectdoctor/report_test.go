package projectdoctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectstate"
)

func TestBuildReportDetectsStaleLocalContract(t *testing.T) {
	raw := []byte("kind: loom.project\n")
	analysis := projectcontracts.Analysis{
		Loaded: &projectcontracts.LoadedProject{Raw: raw},
		Report: projectcontracts.ValidationReport{
			OK:           true,
			Registerable: true,
			Project:      projectcontracts.PlanProject{Slug: "doctor-smoke"},
			Summary:      projectcontracts.DiagnosticSummary{},
		},
		Plan: projectcontracts.ProjectPlan{Project: projectcontracts.PlanProject{Slug: "doctor-smoke"}},
	}
	detail := projects.ProjectRegistrationDetail{
		Project: projects.ProjectDetail{Project: projects.Project{ProjectID: "project_test", Slug: "doctor-smoke"}},
		Registration: &projects.ProjectContractRegistration{
			ProjectContractRegistrationID: "project_contract_registration_test",
			ContractHash:                  "sha256:0000000000000000000000000000000000000000000000000000000000000000",
			ActivationStatus:              projects.ProjectActivationStatusBaseActive,
		},
	}
	report := BuildReport(ReportInput{Local: &analysis, Detail: &detail})
	if report.Summary.Blocked == 0 {
		t.Fatalf("expected stale contract to produce a blocked check: %#v", report.Summary)
	}
	if !HasFailures(report) {
		t.Fatalf("expected HasFailures to be true")
	}
}

func TestBuildBackendAnalysisResultDetectsCurrentContract(t *testing.T) {
	raw := []byte("kind: loom.project\n")
	hash := contractHash(raw)
	analysis := projectcontracts.Analysis{
		Loaded: &projectcontracts.LoadedProject{Raw: raw, RootPath: "/srv/projects/current"},
		Report: projectcontracts.ValidationReport{
			OK:           true,
			Registerable: true,
			ProjectRoot:  "/srv/projects/current",
			Project:      projectcontracts.PlanProject{Slug: "current"},
			Summary:      projectcontracts.DiagnosticSummary{},
		},
		Plan: projectcontracts.ProjectPlan{Project: projectcontracts.PlanProject{Slug: "current"}},
	}
	detail := projects.ProjectRegistrationDetail{
		Project: projects.ProjectDetail{Project: projects.Project{ProjectID: "project_current", Slug: "current"}},
		Registration: &projects.ProjectContractRegistration{
			ProjectContractRegistrationID: "project_contract_registration_current",
			ContractHash:                  hash,
			ActivationStatus:              projects.ProjectActivationStatusBaseActive,
		},
	}

	result := BuildBackendAnalysisResult(analysis, &detail)
	if !result.Current || result.DriftStatus != BackendDriftCurrent || result.NextAction != "no registration needed" {
		t.Fatalf("unexpected current backend analysis result: %#v", result)
	}
}

func TestBuildBackendAnalysisResultDetectsStaleContract(t *testing.T) {
	raw := []byte("kind: loom.project\n")
	analysis := projectcontracts.Analysis{
		Loaded: &projectcontracts.LoadedProject{Raw: raw, RootPath: "/srv/projects/stale"},
		Report: projectcontracts.ValidationReport{
			OK:           true,
			Registerable: true,
			ProjectRoot:  "/srv/projects/stale",
			Project:      projectcontracts.PlanProject{Slug: "stale"},
			Summary:      projectcontracts.DiagnosticSummary{},
		},
		Plan: projectcontracts.ProjectPlan{Project: projectcontracts.PlanProject{Slug: "stale"}},
	}
	detail := projects.ProjectRegistrationDetail{
		Project: projects.ProjectDetail{Project: projects.Project{ProjectID: "project_stale", Slug: "stale"}},
		Registration: &projects.ProjectContractRegistration{
			ProjectContractRegistrationID: "project_contract_registration_stale",
			ContractHash:                  "sha256:0000000000000000000000000000000000000000000000000000000000000000",
			ActivationStatus:              projects.ProjectActivationStatusBaseActive,
		},
	}

	result := BuildBackendAnalysisResult(analysis, &detail)
	if result.Current || result.DriftStatus != BackendDriftStale || result.NextAction != "re-register contract from backend" {
		t.Fatalf("unexpected stale backend analysis result: %#v", result)
	}
	if result.Diff.Summary.Changed == 0 {
		t.Fatalf("expected stale hash to produce changed diff item: %#v", result.Diff)
	}
}

func TestBuildBackendAnalysisResultDetectsUnregisteredContract(t *testing.T) {
	raw := []byte("kind: loom.project\n")
	analysis := projectcontracts.Analysis{
		Loaded: &projectcontracts.LoadedProject{Raw: raw, RootPath: "/srv/projects/unregistered"},
		Report: projectcontracts.ValidationReport{
			OK:           true,
			Registerable: true,
			ProjectRoot:  "/srv/projects/unregistered",
			Project:      projectcontracts.PlanProject{Slug: "unregistered"},
			Summary:      projectcontracts.DiagnosticSummary{},
		},
		Plan: projectcontracts.ProjectPlan{Project: projectcontracts.PlanProject{Slug: "unregistered"}},
	}

	result := BuildBackendAnalysisResult(analysis, nil)
	if result.Current || result.DriftStatus != BackendDriftUnregistered || result.NextAction != "register contract from backend" {
		t.Fatalf("unexpected unregistered backend analysis result: %#v", result)
	}
}

func TestSummarizeCountsStatuses(t *testing.T) {
	summary := Summarize([]Check{
		{Status: StatusOK},
		{Status: StatusWarning},
		{Status: StatusError},
		{Status: StatusBlocked},
		{Status: StatusUnknown},
		{Status: StatusSkipped},
	})
	if summary.OK != 1 || summary.Warnings != 1 || summary.Errors != 1 || summary.Blocked != 1 || summary.Unknown != 1 || summary.Skipped != 1 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestBuildReportIncludesBoundedRepositoryStateAndNeverCanonicalizesWorktrees(t *testing.T) {
	state := projectstate.ProjectProjection{
		Project:     projectstate.ProjectIdentityProjection{ProjectID: "project_test", Slug: "test", Lifecycle: "active"},
		Source:      projectstate.SourceProjection{Posture: projectstate.SourcePostureRegistered, SourceRevision: 2, SemanticDigest: "sha256:semantic", LocationDigest: "sha256:location"},
		Observation: projectstate.ObservationSummary{Posture: projects.ProjectRepositoryObservationObserved, MemberCount: 1, Observed: 1},
		Members: []projectstate.RepositoryProjection{{
			RepositoryID: "repo_test", RepositoryOwnerProjectID: "project_test", Key: "primary", Role: projects.ProjectRepositoryRolePrimary,
			RelativeSource: "repos/primary", SourceBindingDigest: "sha256:binding", ObservationPosture: projects.ProjectRepositoryObservationObserved,
			DevelopmentState: projectstate.DevelopmentStateProjection{Posture: projectstate.DevelopmentStateNotEnabled},
			Git:              &projectstate.GitProjection{Worktree: true, WorktreeCanonicalProjectState: false, Head: "0123456789abcdef0123456789abcdef01234567", Dirty: projectstate.GitDirtyProjection{Dirty: true, UntrackedChanges: true}},
			Problems:         []projectstate.Problem{},
		}},
	}
	report := BuildReport(ReportInput{RepositoryState: &state})
	if !hasCheck(report.Checks, "repositories.repo_test", StatusWarning) || !hasCheck(report.Checks, "repositories.summary", StatusOK) {
		t.Fatalf("repository checks = %#v", report.Checks)
	}
	for _, check := range report.Checks {
		if check.Key != "repositories.repo_test" {
			continue
		}
		text := string(check.Metadata)
		if strings.Contains(text, "/registered/") || strings.Contains(text, `"git_worktree_canonical_project_state":true`) || !strings.Contains(text, `"relative_source":"repos/primary"`) {
			t.Fatalf("repository doctor metadata leaked or canonicalized worktree state: %s", text)
		}
	}
}

func TestBuildReportBlocksInvalidRepositoryBacklinkAndTypesRemoteUnavailable(t *testing.T) {
	state := projectstate.ProjectProjection{
		Project:     projectstate.ProjectIdentityProjection{ProjectID: "project_test"},
		Observation: projectstate.ObservationSummary{Posture: projects.ProjectRepositoryObservationNotObserved, MemberCount: 2, NotObserved: 1, RemoteUnavailable: 1},
		Members: []projectstate.RepositoryProjection{
			{RepositoryID: "repo_invalid", ObservationPosture: projects.ProjectRepositoryObservationObserved, DevelopmentState: projectstate.DevelopmentStateProjection{Posture: projectstate.DevelopmentStateMismatch, ReasonCode: "identity_backlink_mismatch"}, Problems: []projectstate.Problem{}},
			{RepositoryID: "repo_remote", ObservationPosture: projects.ProjectRepositoryObservationRemoteUnavailable, ReasonCode: "owner_node_not_local", DevelopmentState: projectstate.DevelopmentStateProjection{Posture: projectstate.DevelopmentStateNotObserved}, Problems: []projectstate.Problem{{Code: "remote_unavailable", Severity: projectstate.ProblemSeverityWarning, Summary: "Repository owner node is not observable through the current local backend."}}},
		},
	}
	report := BuildReport(ReportInput{RepositoryState: &state})
	if !hasCheck(report.Checks, "repositories.repo_invalid", StatusBlocked) || !hasCheck(report.Checks, "repositories.repo_remote", StatusUnknown) {
		t.Fatalf("repository problem checks = %#v", report.Checks)
	}
}

func TestBuildReportIncludesRuntimeAccessChecks(t *testing.T) {
	result, err := projectcontracts.ScaffoldProject(projectcontracts.ScaffoldOptions{
		Name:      "Access Smoke",
		Slug:      "access-smoke",
		OwnerNode: "main",
		Facets:    []string{"scripts"},
		Directory: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("ScaffoldProject returned error: %v", err)
	}
	manifestPath := filepath.Join(result.ProjectRoot, "scripts", "hello_world", "loom.script.yaml")
	replaceProjectDoctorFile(t, manifestPath, "mode: read_only", "mode: read_write")
	packageRoot := filepath.Join(result.ProjectRoot, "scripts", "hello_world")
	if err := os.Chmod(packageRoot, 0o555); err != nil {
		t.Fatalf("chmod package root: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(packageRoot, 0o755) })

	analysis := projectcontracts.Analyze(result.ProjectRoot)
	report := BuildReport(ReportInput{Local: &analysis})
	if !hasCheck(report.Checks, "runtime_access.scripts.hello_world.package", StatusBlocked) {
		t.Fatalf("expected blocked runtime access check, checks=%#v", report.Checks)
	}
}

func TestBuildReportChecksActiveWorkflowRuntimeRows(t *testing.T) {
	workflowID := "workflow_test"
	versionID := "workflow_version_test"
	endpointID := "capability_endpoint_test"
	endpointVersionID := "capability_endpoint_version_test"
	runtimeBindingID := "capability_runtime_binding_test"
	detail := projects.ProjectRegistrationDetail{
		Project: projects.ProjectDetail{Project: projects.Project{ProjectID: "project_test", Slug: "workflow-smoke"}},
		Registration: &projects.ProjectContractRegistration{
			ProjectContractRegistrationID: "project_contract_registration_test",
			ContractHash:                  "sha256:registered",
			ActivationStatus:              projects.ProjectActivationStatusBaseActive,
			RegistrationPlan: mustProjectPlanJSON(t, projectcontracts.ProjectPlan{
				Project: projectcontracts.PlanProject{Slug: "workflow-smoke"},
				Workflows: []projectcontracts.WorkflowFacetItem{{
					Key:                "example_workflow",
					WorkflowID:         "example_workflow",
					Name:               "Example Workflow",
					ManifestPath:       "workflows/example_workflow/loom.workflow.yaml",
					ManifestHash:       "sha256:manifest",
					ImplementationKind: projectcontracts.WorkflowImplementationWorkflow,
					ContractStatus:     "active",
					ExposeEnabled:      true,
					CapabilityAddress:  "main@workflow-smoke.example_workflow",
				}},
			}),
		},
		WorkflowRegistrations: []projects.ProjectWorkflowRegistration{{
			WorkflowKey:                 "example_workflow",
			WorkflowManifestHash:        "sha256:manifest",
			ImplementationKind:          projectcontracts.WorkflowImplementationWorkflow,
			RuntimeKind:                 capabilities.RuntimeKindWorkflow,
			WorkflowID:                  &workflowID,
			WorkflowVersionID:           &versionID,
			CapabilityEndpointID:        &endpointID,
			CapabilityEndpointVersionID: &endpointVersionID,
			RuntimeBindingID:            &runtimeBindingID,
			CapabilityAddress:           "main@workflow-smoke.example_workflow",
			ActivationStatus:            projects.ProjectWorkflowRegistrationStatusActive,
		}},
	}

	report := BuildReport(ReportInput{Detail: &detail})
	if !hasCheck(report.Checks, "workflows.example_workflow", StatusOK) {
		t.Fatalf("expected active workflow runtime check, checks=%#v", report.Checks)
	}
}

func TestBuildReportBlocksWorkflowMissingRuntimeRows(t *testing.T) {
	detail := projects.ProjectRegistrationDetail{
		Project: projects.ProjectDetail{Project: projects.Project{ProjectID: "project_test", Slug: "workflow-smoke"}},
		Registration: &projects.ProjectContractRegistration{
			ProjectContractRegistrationID: "project_contract_registration_test",
			ContractHash:                  "sha256:registered",
			ActivationStatus:              projects.ProjectActivationStatusBaseActive,
			RegistrationPlan: mustProjectPlanJSON(t, projectcontracts.ProjectPlan{
				Project: projectcontracts.PlanProject{Slug: "workflow-smoke"},
				Workflows: []projectcontracts.WorkflowFacetItem{{
					Key:                "example_workflow",
					WorkflowID:         "example_workflow",
					ManifestPath:       "workflows/example_workflow/loom.workflow.yaml",
					ImplementationKind: projectcontracts.WorkflowImplementationWorkflow,
					ContractStatus:     "active",
					ExposeEnabled:      true,
					CapabilityAddress:  "main@workflow-smoke.example_workflow",
				}},
			}),
		},
		WorkflowRegistrations: []projects.ProjectWorkflowRegistration{{
			WorkflowKey:        "example_workflow",
			ImplementationKind: projectcontracts.WorkflowImplementationWorkflow,
			RuntimeKind:        capabilities.RuntimeKindWorkflow,
			CapabilityAddress:  "main@workflow-smoke.example_workflow",
			ActivationStatus:   projects.ProjectWorkflowRegistrationStatusActive,
		}},
	}

	report := BuildReport(ReportInput{Detail: &detail})
	if !hasCheck(report.Checks, "workflows.example_workflow", StatusBlocked) {
		t.Fatalf("expected blocked workflow runtime check, checks=%#v", report.Checks)
	}
}

func mustProjectPlanJSON(t *testing.T, plan projectcontracts.ProjectPlan) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}
	return raw
}

func replaceProjectDoctorFile(t *testing.T, path, old, new string) {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	next := strings.Replace(string(payload), old, new, 1)
	if next == string(payload) {
		t.Fatalf("expected replacement in %s", path)
	}
	if err := os.WriteFile(path, []byte(next), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func hasCheck(checks []Check, key string, status Status) bool {
	for _, check := range checks {
		if check.Key == key && check.Status == status {
			return true
		}
	}
	return false
}
