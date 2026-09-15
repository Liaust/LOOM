package projectcontracts

import (
	"testing"

	"loom.local/loom/internal/modules"
)

func TestCrossFacetDependenciesClassifyTargetsAndPolicy(t *testing.T) {
	t.Run("invalid schedule target", func(t *testing.T) {
		diagnostics := collectDependencyDiagnostics(ValidationReport{
			Schedules: []ScheduleFacetItem{{Key: "daily", TargetCapability: "not a capability"}},
		})
		if !hasDiagnostic(diagnostics, "schedule.target_capability_invalid") {
			t.Fatalf("missing invalid target diagnostic: %#v", diagnostics)
		}
	})

	t.Run("same project missing target remains backend-verifiable info", func(t *testing.T) {
		diagnostics := collectDependencyDiagnostics(ValidationReport{
			DerivedProviders: []PlanProvider{{CompactAddress: "main@project-smoke"}},
			Schedules:        []ScheduleFacetItem{{Key: "daily", TargetCapability: "main@project-smoke.missing"}},
		})
		if !hasDiagnostic(diagnostics, "schedule.target_capability_missing") {
			t.Fatalf("missing same-project target diagnostic: %#v", diagnostics)
		}
		for _, diagnostic := range diagnostics {
			if diagnostic.Code == "schedule.target_capability_missing" && diagnostic.Severity != SeverityInfo {
				t.Fatalf("same-project missing target should be informational until doctor can query backend: %#v", diagnostic)
			}
		}
	})

	t.Run("workflow placeholder target blocks automation facets", func(t *testing.T) {
		diagnostics := collectDependencyDiagnostics(ValidationReport{
			Workflows: []WorkflowFacetItem{{
				Key:                "summarize",
				CapabilityAddress:  "main@project-smoke.summarize",
				ImplementationKind: WorkflowImplementationPlaceholder,
				ManifestPath:       "workflows/summarize/loom.workflow.yaml",
			}},
			DirectEvents: []DirectEventFacetItem{{Key: "email", TargetCapability: "main@project-smoke.summarize"}},
		})
		if !hasDiagnostic(diagnostics, "direct_event.target_workflow_runtime_unsupported") {
			t.Fatalf("missing workflow target diagnostic: %#v", diagnostics)
		}
	})

	t.Run("connector provider cannot collide with project provider", func(t *testing.T) {
		diagnostics := collectDependencyDiagnostics(ValidationReport{
			DerivedProviders: []PlanProvider{{CompactAddress: "main@project-smoke"}},
			Connectors:       []ConnectorFacetItem{{Key: "bad", ProviderAddress: "main@project-smoke", ManifestPath: "connectors/bad/loom.connector.yaml"}},
		})
		if !hasDiagnostic(diagnostics, "connector.provider_collides_with_project_provider") {
			t.Fatalf("missing provider collision diagnostic: %#v", diagnostics)
		}
	})

	t.Run("sync roots must be indexed for discoverability", func(t *testing.T) {
		diagnostics := collectDependencyDiagnostics(ValidationReport{
			WatchedRoots: []ProjectWatchedRootItem{{
				Key:       "notes",
				SyncMode:  "selected_files",
				IndexMode: "none",
			}},
		})
		if !hasDiagnostic(diagnostics, "watched_root.sync_without_index") {
			t.Fatalf("missing sync/index diagnostic: %#v", diagnostics)
		}
		if !hasDiagnostic(diagnostics, "watched_root.delete_mode_implicit") {
			t.Fatalf("missing delete policy diagnostic: %#v", diagnostics)
		}
	})

	t.Run("module provider cannot collide with script provider", func(t *testing.T) {
		diagnostics := collectDependencyDiagnostics(ValidationReport{
			DerivedProviders: []PlanProvider{{Kind: "scripts", CompactAddress: "main@project-smoke"}},
			Modules: []ModuleFacetItem{{
				Key:          "example_module",
				ManifestPath: "modules/example_module/module.json",
				InstallPlan:  ModuleProjectInstallPlan{TargetNode: "main"},
				Manifest: modules.Manifest{Provides: modules.ManifestProvides{Providers: []modules.ManifestProvider{{
					ProviderKey: "project-smoke",
					DisplayName: "Project Smoke",
				}}}},
			}},
		})
		if !hasDiagnostic(diagnostics, "module.provider_collides_with_local_provider") {
			t.Fatalf("missing module provider collision diagnostic: %#v", diagnostics)
		}
	})

	t.Run("module capability cannot collide with connector capability", func(t *testing.T) {
		diagnostics := collectDependencyDiagnostics(ValidationReport{
			Connectors: []ConnectorFacetItem{{
				Key:             "example_connector",
				ProviderAddress: "main@example_connector",
				Capabilities: []ConnectorCapabilityItem{{
					CapabilityAddress: "main@example_connector.ping",
				}},
			}},
			Modules: []ModuleFacetItem{{
				Key:          "example_module",
				ManifestPath: "modules/example_module/module.json",
				InstallPlan:  ModuleProjectInstallPlan{TargetNode: "main"},
				Manifest: modules.Manifest{Provides: modules.ManifestProvides{
					Providers: []modules.ManifestProvider{{
						ProviderKey: "example_connector",
						DisplayName: "Example Connector",
					}},
					Capabilities: []modules.ManifestCapability{{
						ProviderKey:                 "example_connector",
						EndpointName:                "ping",
						CapabilityClassNamespace:    "module.example",
						CapabilityClassName:         "Ping",
						DisplayName:                 "Ping",
						Form:                        "function",
						ExecutionAuthorizationLevel: 1,
						RiskLevel:                   "low",
					}},
				}},
			}},
		})
		if !hasDiagnostic(diagnostics, "module.provider_collides_with_local_provider") {
			t.Fatalf("missing module provider collision diagnostic: %#v", diagnostics)
		}
		if !hasDiagnostic(diagnostics, "module.capability_collides_with_local_capability") {
			t.Fatalf("missing module capability collision diagnostic: %#v", diagnostics)
		}
	})
}

func collectDependencyDiagnostics(report ValidationReport) []Diagnostic {
	diagnostics := []Diagnostic{}
	validateCrossFacetDependencies(&report, func(diagnostic Diagnostic) {
		diagnostics = append(diagnostics, diagnostic)
	})
	return diagnostics
}
