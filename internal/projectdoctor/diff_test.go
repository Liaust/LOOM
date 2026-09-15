package projectdoctor

import (
	"testing"

	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
)

func TestBuildDiffDetectsChangedScheduleTarget(t *testing.T) {
	analysis := projectcontracts.Analysis{
		Report: projectcontracts.ValidationReport{ProjectRoot: "/tmp/project"},
		Plan: projectcontracts.ProjectPlan{
			Project: projectcontracts.PlanProject{Slug: "diff-smoke"},
			Schedules: []projectcontracts.ScheduleFacetItem{{
				Key:                "daily",
				BackendScheduleKey: "project-diff-smoke-daily",
				ManifestHash:       "sha256:local",
				TargetCapability:   "main@diff-smoke.new_target",
			}},
		},
	}
	detail := projects.ProjectRegistrationDetail{
		Project: projects.ProjectDetail{Project: projects.Project{ProjectID: "project_test", Slug: "diff-smoke"}},
		Registration: &projects.ProjectContractRegistration{
			ProjectContractRegistrationID: "project_contract_registration_test",
			ContractHash:                  "sha256:registered",
		},
		ScheduleRegistrations: []projects.ProjectScheduleRegistration{{
			ScheduleKey:        "daily",
			BackendScheduleKey: "project-diff-smoke-daily",
			ScheduleHash:       "sha256:local",
			TargetCapability:   "main@diff-smoke.old_target",
		}},
	}
	report := BuildDiff(analysis, &detail)
	if report.Summary.Changed == 0 {
		t.Fatalf("expected changed schedule target, summary=%#v items=%#v", report.Summary, report.Items)
	}
}

func TestBuildDiffTreatsUnregisteredProjectAsMissing(t *testing.T) {
	analysis := projectcontracts.Analysis{
		Loaded: &projectcontracts.LoadedProject{Raw: []byte("kind: loom.project\n")},
		Report: projectcontracts.ValidationReport{ProjectRoot: "/tmp/project"},
		Plan: projectcontracts.ProjectPlan{
			Project: projectcontracts.PlanProject{Slug: "diff-smoke"},
			Scripts: []projectcontracts.ScriptFacetItem{{Key: "hello", CapabilityAddress: "main@diff-smoke.hello", ExposureHash: "sha256:hello"}},
		},
	}
	report := BuildDiff(analysis, nil)
	if report.Summary.Missing == 0 || report.Summary.Added == 0 {
		t.Fatalf("expected missing registration and added local items, summary=%#v items=%#v", report.Summary, report.Items)
	}
}

func TestBuildDiffDetectsChangedScriptExposureHash(t *testing.T) {
	analysis := projectcontracts.Analysis{
		Plan: projectcontracts.ProjectPlan{
			Project: projectcontracts.PlanProject{Slug: "diff-smoke"},
			Scripts: []projectcontracts.ScriptFacetItem{{
				Key:               "hello",
				CapabilityAddress: "main@diff-smoke.hello",
				ExposureHash:      "sha256:local",
			}},
		},
	}
	detail := projects.ProjectRegistrationDetail{
		Project: projects.ProjectDetail{Project: projects.Project{ProjectID: "project_test", Slug: "diff-smoke"}},
		Registration: &projects.ProjectContractRegistration{
			ProjectContractRegistrationID: "project_contract_registration_test",
			ContractHash:                  "sha256:registered",
		},
		ScriptExposures: []projects.ProjectScriptExposure{{
			ScriptKey:         "hello",
			CapabilityAddress: "main@diff-smoke.hello",
			ExposureHash:      "sha256:registered",
		}},
	}

	report := BuildDiff(analysis, &detail)
	if report.Summary.Changed == 0 {
		t.Fatalf("expected changed script exposure, summary=%#v items=%#v", report.Summary, report.Items)
	}
}

func TestBuildDiffDetectsWatchedRootPolicyDrift(t *testing.T) {
	analysis := projectcontracts.Analysis{
		Plan: projectcontracts.ProjectPlan{
			Project: projectcontracts.PlanProject{Slug: "diff-smoke"},
			WatchedRoots: []projectcontracts.ProjectWatchedRootItem{{
				Key:        "notes",
				ConfigHash: "sha256:local",
				SyncMode:   "selected_files",
				BackupMode: "none",
				IndexMode:  "markdown_text",
				DeleteMode: "tombstone",
			}},
		},
	}
	detail := projects.ProjectRegistrationDetail{
		Project: projects.ProjectDetail{Project: projects.Project{ProjectID: "project_test", Slug: "diff-smoke"}},
		Registration: &projects.ProjectContractRegistration{
			ProjectContractRegistrationID: "project_contract_registration_test",
			ContractHash:                  "sha256:registered",
		},
		WatchedRootRegistrations: []projects.ProjectWatchedRootRegistration{{
			LocalRootKey: "notes",
			ConfigHash:   "sha256:registered",
			SyncMode:     "none",
			BackupMode:   "incremental_raw",
			IndexMode:    "none",
			DeleteMode:   "local_state_only",
		}},
	}

	report := BuildDiff(analysis, &detail)
	if report.Summary.Changed == 0 {
		t.Fatalf("expected changed watched-root policy, summary=%#v items=%#v", report.Summary, report.Items)
	}
}

func TestBuildDiffDetectsWorkflowRegistrationDrift(t *testing.T) {
	analysis := projectcontracts.Analysis{
		Plan: projectcontracts.ProjectPlan{
			Project: projectcontracts.PlanProject{Slug: "diff-smoke"},
			Workflows: []projectcontracts.WorkflowFacetItem{{
				Key:                "example_workflow",
				WorkflowID:         "example_workflow",
				ManifestPath:       "workflows/example_workflow/loom.workflow.yaml",
				ManifestHash:       "sha256:local",
				PackageHash:        "sha256:local-package",
				ImplementationKind: projectcontracts.WorkflowImplementationWorkflow,
				CapabilityAddress:  "main@diff-smoke.example_workflow",
			}},
		},
	}
	detail := projects.ProjectRegistrationDetail{
		Project: projects.ProjectDetail{Project: projects.Project{ProjectID: "project_test", Slug: "diff-smoke"}},
		Registration: &projects.ProjectContractRegistration{
			ProjectContractRegistrationID: "project_contract_registration_test",
			ContractHash:                  "sha256:registered",
			RegistrationPlan: mustProjectPlanJSON(t, projectcontracts.ProjectPlan{
				Project: projectcontracts.PlanProject{Slug: "diff-smoke"},
				Workflows: []projectcontracts.WorkflowFacetItem{{
					Key:                "example_workflow",
					WorkflowID:         "example_workflow",
					ManifestPath:       "workflows/example_workflow/loom.workflow.yaml",
					ManifestHash:       "sha256:registered",
					ImplementationKind: projectcontracts.WorkflowImplementationWorkflow,
					CapabilityAddress:  "main@diff-smoke.example_workflow",
				}},
			}),
		},
		WorkflowRegistrations: []projects.ProjectWorkflowRegistration{{
			WorkflowKey:          "example_workflow",
			WorkflowManifestHash: "sha256:registered",
			ImplementationKind:   projectcontracts.WorkflowImplementationWorkflow,
			CapabilityAddress:    "main@diff-smoke.example_workflow",
			ActivationStatus:     projects.ProjectWorkflowRegistrationStatusActive,
		}},
	}

	report := BuildDiff(analysis, &detail)
	if report.Summary.Changed == 0 {
		t.Fatalf("expected changed workflow registration, summary=%#v items=%#v", report.Summary, report.Items)
	}
}
