package portal

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectstate"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/storagearchive"
)

func portalPhysicalArchiveState(t *testing.T, restorePhase projects.ProjectPhysicalRestorePhase) json.RawMessage {
	t.Helper()
	at := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	digest := "sha256:" + strings.Repeat("a", 64)
	state := projects.ProjectPhysicalArchiveState{
		SchemaVersion: projects.ProjectPhysicalArchiveStateSchemaVersion, ProjectID: "project_test", ProjectSlug: "test",
		Status: "archived", Phase: projects.ProjectArchivePhaseComplete, MutationBlocked: true,
		OperationID: "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAY", PlanDigest: digest, WorkspacePlanDigest: digest,
		ActivePath: "/private/box/Projects/test", ArchivePath: "/private/storage/archive/projects/test/project",
		ActorID: "actor_test", Reason: "fixture", StartedAt: at, RuntimeDeactivatedAt: &at, RuntimeQuiescenceDigest: digest,
		WorkspaceCompletedAt: &at, ArchiveManifestDigest: digest, ArchivedAt: &at,
	}
	if restorePhase != "" {
		state.Restore = &projects.ProjectPhysicalRestoreState{SchemaVersion: projects.ProjectPhysicalRestoreStateSchemaVersion,
			OperationID: "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAZ", PlanDigest: digest, WorkspacePlanDigest: digest,
			Phase: restorePhase, Status: "in_progress", MutationBlocked: true, ActivationState: "inactive", StartedAt: at}
		state.Restore.Request.ActorID, state.Restore.Request.OriginNodeID, state.Restore.Request.CorrelationID = "actor_test", "node_test", "corr_test"
		if restorePhase == projects.ProjectRestorePhaseComplete {
			state.Restore.Status, state.Restore.EventID = "restored", "event_01ARZ3NDEKTSV4RRFFQ69G5FAZ"
			state.Restore.ActiveManifestDigest, state.Restore.WorkspaceCompletedAt, state.Restore.RestoredAt = digest, &at, &at
		}
	}
	if err := projects.ValidateProjectPhysicalArchiveState(state); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestProjectPhysicalArchivePortalQuarantinesRestoredAndInvalidState(t *testing.T) {
	for _, raw := range []json.RawMessage{portalPhysicalArchiveState(t, projects.ProjectRestorePhasePending), portalPhysicalArchiveState(t, projects.ProjectRestorePhaseComplete), json.RawMessage(`{"schema_version":"project.physical_archive_state.v1"}`)} {
		project := projects.Project{ProjectID: "project_test", Slug: "test", Status: "active", ArchiveState: raw}
		detail := projects.ProjectRegistrationDetail{Project: projects.ProjectDetail{Project: project}, Registration: &projects.ProjectContractRegistration{}}
		for _, action := range []PortalAction{NewProjectActivateAction(detail, "scripts", ScreenProjects), NewProjectAddFacetAction(detail, ScreenProjects), NewProjectMigrateLayoutAction(detail, nil, ScreenProjects), NewProjectArchiveAction(detail, ScreenProjects)} {
			if action.State != ActionDisabled {
				t.Fatalf("runtime guard missing: %+v", action)
			}
		}
		index := archivedProjectIndexFromProjects([]projects.Project{project})
		if !index.matchesProjectRef(project.ProjectID) {
			t.Fatal("restored inactive runtime escaped quarantine")
		}
		if projectArchived(project) {
			t.Fatal("active custody mislabeled archived")
		}
	}
}

type physicalArchivePortalClient struct{ *fakePortalClient }

func (c *physicalArchivePortalClient) InspectProjectArchive(context.Context, string, string) (response.Envelope[storagearchive.ProjectArchiveInspectResult], error) {
	return response.Success("corr_test", storagearchive.ProjectArchiveInspectResult{Physical: &storagearchive.ProjectPhysicalArchiveInspection{ProjectID: "project_test", OperationID: "op_test", Phase: "complete", MutationBlocked: true, ActivationState: storagearchive.WorkspaceActivationInactive, EvidenceStatus: "verified", NextAction: "activation_not_available"}, RuntimeManifestPath: "/private/legacy"}), nil
}

func TestProjectPhysicalArchivePortalInspectionIsInactiveAndRedacted(t *testing.T) {
	e := ActionExecutor{Client: &physicalArchivePortalClient{fakePortalClient: &fakePortalClient{}}, CorrelationID: "corr_test"}
	result := e.executeProjectArchiveInspect(context.Background(), PortalAction{ID: "inspect", TargetRef: "test"})
	data, _ := json.Marshal(result)
	if result.Status != ActionLifecycleSucceeded || !strings.Contains(string(data), "activation_not_available") || !strings.Contains(string(data), "inactive") || strings.Contains(string(data), "/private") {
		t.Fatalf("portal inspection: %s", data)
	}
}

func TestProjectActivateActionDisablesAutomationFacetWhenLocalTargetInactive(t *testing.T) {
	plan, err := json.Marshal(projectcontracts.ProjectPlan{
		Project: projectcontracts.PlanProject{Slug: "portal-project", Name: "Portal Project", OwnerNode: "main"},
		Scripts: []projectcontracts.ScriptFacetItem{{
			Key:               "run",
			CapabilityAddress: "main@portal-project.run",
		}},
		Schedules: []projectcontracts.ScheduleFacetItem{{
			Key:              "daily",
			ManifestPath:     "schedules/daily/loom.schedule.yaml",
			TargetCapability: "main@portal-project.run",
			ContractStatus:   "draft",
		}},
		DirectEvents: []projectcontracts.DirectEventFacetItem{{
			Key:              "gmail",
			ManifestPath:     "direct_events/gmail/loom.direct_event.yaml",
			TargetCapability: "main@portal-project.run",
			EventType:        "gmail.message.received",
		}},
	})
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}
	detail := projects.ProjectRegistrationDetail{
		Project: projects.ProjectDetail{Project: projects.Project{
			ProjectID: "project_test",
			Slug:      "portal-project",
			Name:      "Portal Project",
		}},
		Registration: &projects.ProjectContractRegistration{RegistrationPlan: plan},
	}

	action := NewProjectActivateAction(detail, "schedules", ScreenProjects)
	if action.State != ActionDisabled || !strings.Contains(action.DisabledReason, "Activate scripts first") {
		t.Fatalf("schedule activation action state=%s reason=%q", action.State, action.DisabledReason)
	}
	action = NewProjectActivateAction(detail, "direct_events", ScreenProjects)
	if action.State != ActionDisabled || !strings.Contains(action.DisabledReason, "Activate scripts first") {
		t.Fatalf("direct-event activation action state=%s reason=%q", action.State, action.DisabledReason)
	}
	if strings.Join(action.RawCommand, " ") != "loom project activate portal-project --facet direct-events" {
		t.Fatalf("direct-event raw command = %#v", action.RawCommand)
	}
	allAction := NewProjectActivateAction(detail, "all", ScreenProjects)
	if allAction.State == ActionDisabled {
		t.Fatalf("activate all should remain available, reason=%q", allAction.DisabledReason)
	}

	detail.ScriptExposures = []projects.ProjectScriptExposure{{
		ScriptKey:         "run",
		CapabilityAddress: "main@portal-project.run",
		ActivationStatus:  projects.ProjectScriptExposureStatusActive,
	}}
	action = NewProjectActivateAction(detail, "schedules", ScreenProjects)
	if action.State == ActionDisabled {
		t.Fatalf("schedule activation should be available after target activation, reason=%q", action.DisabledReason)
	}
	action = NewProjectActivateAction(detail, "direct_events", ScreenProjects)
	if action.State == ActionDisabled {
		t.Fatalf("direct-event activation should be available after target activation, reason=%q", action.DisabledReason)
	}
}

func TestPortalProjectRepositoryStateAndArchivedActionsStayReadOnly(t *testing.T) {
	observedAt := time.Date(2026, 8, 30, 14, 0, 0, 0, time.UTC)
	repository := localclient.ProjectRepositoryItem{
		RepositoryID:             "repo_portal",
		RepositoryOwnerProjectID: "project_archived",
		Key:                      "backend",
		RelativePath:             "services/backend",
		Role:                     "primary",
		MembershipLifecycle:      "archived",
		RepositoryLifecycle:      "archived",
		ObservationPosture:       "observed",
		ObservedAt:               &observedAt,
		DevelopmentState:         localclient.ProjectRepositoryDevelopmentState{Posture: projectstate.DevelopmentStateEnabled, RelativePath: ".repo/repo.yaml"},
		Git: &localclient.ProjectRepositoryGit{
			CurrentBranch:                 "main",
			Worktree:                      true,
			WorktreeCanonicalProjectState: false,
			Dirty:                         localclient.ProjectRepositoryGitDirty{Dirty: true},
		},
	}
	data := ProjectsData{
		Projects:           []projects.Project{{ProjectID: "project_archived", Slug: "archived", Status: "archived"}},
		SelectedProjectRef: "archived",
		SelectedProject: projects.ProjectDetail{Project: projects.Project{
			ProjectID: "project_archived",
			Slug:      "archived",
			Status:    "archived",
		}},
		RegistrationDetail: projects.ProjectRegistrationDetail{Project: projects.ProjectDetail{Project: projects.Project{
			ProjectID: "project_archived",
			Slug:      "archived",
			Status:    "archived",
		}}},
		RepositoryStateReady: true,
		RepositoryStatus: localclient.ProjectRepositoryStatusResult{
			SchemaVersion: projectstate.SchemaVersion,
			Project:       localclient.ProjectRepositoryProject{ProjectID: "project_archived", Slug: "archived", Lifecycle: "archived"},
			Source: localclient.ProjectRepositorySource{
				Posture:                      projectstate.SourcePostureRegistered,
				ProjectContractSchemaVersion: "project.contract.v0.4",
				ReposContractSchemaVersion:   "repos.contract.v0.4",
				SourceRevision:               4,
			},
			Observation:  projectstate.ObservationSummary{Posture: projects.ProjectRepositoryObservationObserved, MemberCount: 1, Observed: 1},
			Repositories: []localclient.ProjectRepositoryItem{repository},
			Page:         localclient.ProjectRepositoryPage{Limit: 100},
			ObservedAt:   observedAt,
		},
		Explorer: ProjectExplorerState{Level: ProjectExplorerArchive},
	}
	state := NewScreenState(ScreenProjects)
	state.Data.Projects = data
	var builder strings.Builder
	renderProjectRepositoryState(&builder, state, data)
	output := builder.String()
	for _, want := range []string{"Repositories", "project.contract.v0.4", "backend", "role=primary", "path=services/backend", "git=main/dirty/worktree(noncanonical)", ".repo=enabled"} {
		if !strings.Contains(output, want) {
			t.Fatalf("repository render missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "not-rendered") {
		t.Fatalf("repository render exposed an unplanned digest:\n%s", output)
	}

	actions := archivedProjectActions(data)
	wantStatus := false
	wantInspect := false
	for _, action := range actions {
		if action.TargetKind != "project_repository" && action.TargetKind != "project_repository_status" {
			continue
		}
		if action.Risk != ActionRiskInspect || action.Executor.Kind != PortalExecutorRecordInspect {
			t.Fatalf("archived repository action is not inspect-only: %#v", action)
		}
		joined := strings.Join(action.RawCommand, " ")
		if strings.Contains(joined, " add ") || strings.Contains(joined, " remove ") || strings.Contains(joined, " register ") || strings.Contains(joined, " mutate ") {
			t.Fatalf("archived repository action exposes mutation: %s", joined)
		}
		if action.TargetKind == "project_repository_status" {
			wantStatus = true
		}
		if action.TargetKind == "project_repository" {
			wantInspect = true
			if action.Executor.Payload["git_worktree_canonical_project_state"] != "false" {
				t.Fatalf("worktree canonicality payload=%q", action.Executor.Payload["git_worktree_canonical_project_state"])
			}
		}
	}
	if !wantStatus || !wantInspect {
		t.Fatalf("archived repository actions status=%t inspect=%t actions=%#v", wantStatus, wantInspect, actions)
	}
}
