package projectcontracts

import "sort"

func BuildPlan(loaded *LoadedProject, report ValidationReport) ProjectPlan {
	plan := ProjectPlan{
		SchemaVersion:       PlanSchemaV03,
		GeneratedAt:         report.GeneratedAt,
		ProjectRoot:         report.ProjectRoot,
		ContractPath:        report.ContractPath,
		Registerable:        report.Registerable,
		Project:             report.Project,
		DerivedProviders:    append([]PlanProvider{}, report.DerivedProviders...),
		Facets:              append([]PlanFacet{}, report.Facets...),
		PolicyRefs:          append([]PlanPolicyRef{}, report.PolicyRefs...),
		Notes:               append([]NotesFacetItem{}, report.Notes...),
		Repos:               append([]RepoFacetItem{}, report.Repos...),
		RepositoryMembers:   append([]RepoMemberSpec{}, report.RepositoryMembers...),
		RepositorySource:    cloneRepositorySourceSnapshot(report.RepositorySource),
		Scripts:             append([]ScriptFacetItem{}, report.Scripts...),
		Workflows:           append([]WorkflowFacetItem{}, report.Workflows...),
		Connectors:          append([]ConnectorFacetItem{}, report.Connectors...),
		Modules:             append([]ModuleFacetItem{}, report.Modules...),
		Schedules:           append([]ScheduleFacetItem{}, report.Schedules...),
		DirectEvents:        append([]DirectEventFacetItem{}, report.DirectEvents...),
		Services:            append([]ServiceFacetItem{}, report.Services...),
		WatchedRoots:        append([]ProjectWatchedRootItem{}, report.WatchedRoots...),
		UnsupportedFeatures: unsupportedFeatures(report.Facets, report.Workflows),
		Diagnostics:         append([]Diagnostic{}, report.Diagnostics...),
		Summary:             report.Summary,
	}
	if loaded == nil {
		return plan
	}
	if report.Declaration != nil {
		plan.Declaration = cloneDeclarationCompilation(report.Declaration)
		plan.WatchedRoots = cloneDeclarationWatchedRoots(report.WatchedRoots)
		plan.Actions, plan.UnsupportedFeatures = declarationPlanActions(plan.Declaration)
		if !plan.Registerable {
			for i := range plan.Actions {
				if plan.Actions[i].Status != "unsupported" {
					plan.Actions[i].Status = "blocked"
				}
			}
		}
		return plan
	}
	plan.Actions = planActions(plan)
	return plan
}

func cloneRepositorySourceSnapshot(source *RepositorySourceSnapshot) *RepositorySourceSnapshot {
	if source == nil {
		return nil
	}
	cloned := *source
	return &cloned
}

func unsupportedFeatures(facets []PlanFacet, workflows []WorkflowFacetItem) []PlanUnsupportedFeature {
	out := []PlanUnsupportedFeature{}
	workflowFacetEnabled := false
	for _, facet := range facets {
		if facet.Key == "workflows" && facet.Enabled {
			workflowFacetEnabled = true
			break
		}
	}
	if workflowFacetEnabled {
		for _, workflow := range workflows {
			if workflow.ManifestPath == "" || workflow.Executable || workflow.ImplementationKind == WorkflowImplementationScript {
				continue
			}
			out = append(out, PlanUnsupportedFeature{
				Feature: "workflows",
				Reason:  "placeholder or legacy workflow contracts are design-only until converted to executable workflow.contract.v0.3.1 packages",
			})
			break
		}
	}
	return out
}

func planActions(plan ProjectPlan) []PlanAction {
	actions := []PlanAction{}
	if plan.Project.Slug != "" {
		actions = append(actions, PlanAction{
			Action:      "would_create_or_update_project",
			Status:      "ready",
			TargetKind:  "project",
			TargetRef:   plan.Project.Slug,
			Description: "Slice 04 will create or update the backend project.",
		})
		actions = append(actions, PlanAction{
			Action:      "would_register_project_contract",
			Status:      "ready",
			TargetKind:  "project_contract",
			TargetRef:   plan.ContractPath,
			Description: "Slice 04 will persist project contract registration metadata.",
		})
	}
	for _, provider := range plan.DerivedProviders {
		actions = append(actions, PlanAction{
			Action:      "would_register_project_provider",
			Status:      "ready",
			TargetKind:  "provider",
			TargetRef:   provider.CompactAddress,
			Description: "Later slices will register project-owned providers when facets expose capabilities.",
		})
	}
	for _, facet := range plan.Facets {
		if !facet.Enabled {
			continue
		}
		actions = append(actions, PlanAction{
			Action:      "would_scan_facet",
			Status:      "ready",
			TargetKind:  "facet",
			TargetRef:   facet.Key,
			Description: descriptionForFacet(facet.Key),
		})
	}
	for _, service := range plan.Services {
		actions = append(actions, PlanAction{
			Action:      "would_register_service_provider",
			Status:      "ready",
			TargetKind:  "provider",
			TargetRef:   service.ProviderAddress,
			Description: "Register the already-provisioned service; provisioning remains external.",
		})
	}
	for _, script := range plan.Scripts {
		if script.ManifestPath == "" {
			continue
		}
		actions = append(actions, PlanAction{
			Action:      "would_register_script",
			Status:      "ready",
			TargetKind:  "script",
			TargetRef:   script.Key,
			Description: "Slice 05 will register this project script package.",
		})
		if !script.Exposed {
			actions = append(actions, PlanAction{
				Action:      "would_skip_disabled_script_exposure",
				Status:      "disabled",
				TargetKind:  "script_exposure",
				TargetRef:   script.Key,
				Description: "This script exposure is disabled or absent.",
			})
			continue
		}
		for _, credential := range script.Credentials.Required {
			actions = append(actions, PlanAction{
				Action:      "would_require_runtime_credential",
				Status:      "pending_verification",
				TargetKind:  "credential",
				TargetRef:   credential.Ref,
				Description: "A later activation verifier will require this credential before exposing the script capability.",
			})
		}
		actions = append(actions,
			PlanAction{
				Action:      "would_register_capability_class",
				Status:      "ready",
				TargetKind:  "capability_class",
				TargetRef:   script.Exposure.Capability.ClassNamespace + "." + script.Exposure.Capability.ClassName,
				Description: "Slice 05 will register the project script capability class.",
			},
			PlanAction{
				Action:      "would_register_capability_endpoint",
				Status:      "ready",
				TargetKind:  "capability_endpoint",
				TargetRef:   script.CapabilityAddress,
				Description: "Slice 05 will register the project-owned script capability endpoint.",
			},
			PlanAction{
				Action:      "would_register_endpoint_version",
				Status:      "ready",
				TargetKind:  "endpoint_version",
				TargetRef:   ScriptVersionLabel(script.Manifest.Version, script.PackageHash),
				Description: "Slice 05 will register an endpoint version tied to the script package hash.",
			},
			PlanAction{
				Action:      "would_register_script_runtime_binding",
				Status:      "ready",
				TargetKind:  "runtime_binding",
				TargetRef:   script.CapabilityAddress,
				Description: "Slice 05 will bind the endpoint version to the script runtime executor.",
			},
		)
	}
	for _, workflow := range plan.Workflows {
		if workflow.ManifestPath == "" {
			continue
		}
		if workflow.ContractStatus == "disabled" {
			actions = append(actions, PlanAction{
				Action:      "would_skip_disabled_workflow",
				Status:      "disabled",
				TargetKind:  "workflow",
				TargetRef:   firstNonEmptyString(workflow.WorkflowID, workflow.Key),
				Description: "This workflow contract is disabled.",
			})
			continue
		}
		actions = append(actions, PlanAction{
			Action:      "would_validate_workflow_contract",
			Status:      "ready",
			TargetKind:  "workflow",
			TargetRef:   firstNonEmptyString(workflow.WorkflowID, workflow.Key),
			Description: "Validation records workflow contracts and separates executable packages from design-only workflow intent.",
		})
		if workflow.ImplementationKind == WorkflowImplementationScript {
			actions = append(actions, PlanAction{
				Action:      "would_link_script_backed_workflow",
				Status:      "ready",
				TargetKind:  "workflow",
				TargetRef:   firstNonEmptyString(workflow.WorkflowID, workflow.Key),
				Description: "Script-backed workflow shims point to an existing script capability; no workflow runtime is created.",
			})
			continue
		}
		if workflow.Executable {
			if workflow.ContractStatus != ProjectStatusActive {
				actions = append(actions, PlanAction{
					Action:      "would_skip_draft_workflow_activation",
					Status:      "draft",
					TargetKind:  "workflow",
					TargetRef:   firstNonEmptyString(workflow.WorkflowID, workflow.Key),
					Description: "Executable workflow contracts validate and plan while draft, but activation only registers active workflows.",
				})
				continue
			}
			actions = append(actions, PlanAction{
				Action:      "would_register_workflow_package",
				Status:      "ready",
				TargetKind:  "workflow",
				TargetRef:   firstNonEmptyString(workflow.WorkflowID, workflow.Key),
				Description: "v0.3.1 will register this executable workflow package for workflow-run jobs.",
			})
			if workflow.ExposeEnabled {
				for _, credential := range workflow.Credentials.Required {
					actions = append(actions, PlanAction{
						Action:      "would_require_runtime_credential",
						Status:      "pending_verification",
						TargetKind:  "credential",
						TargetRef:   credential.Ref,
						Description: "A later activation verifier will require this credential before exposing the workflow capability.",
					})
				}
				actions = append(actions,
					PlanAction{
						Action:      "would_register_workflow_capability_endpoint",
						Status:      "ready",
						TargetKind:  "capability_endpoint",
						TargetRef:   workflow.CapabilityAddress,
						Description: "v0.3.1 will expose this workflow package as a callable capability URL.",
					},
					PlanAction{
						Action:      "would_register_workflow_runtime_binding",
						Status:      "ready",
						TargetKind:  "runtime_binding",
						TargetRef:   workflow.CapabilityAddress,
						Description: "v0.3.1 will bind the endpoint version to the workflow runtime executor.",
					},
				)
			}
			continue
		}
		actions = append(actions, PlanAction{
			Action:      "would_block_first_class_workflow_runtime",
			Status:      "unsupported",
			TargetKind:  "workflow",
			TargetRef:   firstNonEmptyString(workflow.WorkflowID, workflow.Key),
			Description: "First-class workflow execution is reserved but not implemented in v0.3.",
		})
	}
	for _, schedule := range plan.Schedules {
		if schedule.ManifestPath == "" {
			continue
		}
		if schedule.ContractStatus == "disabled" {
			actions = append(actions, PlanAction{
				Action:      "would_skip_disabled_schedule",
				Status:      "disabled",
				TargetKind:  "schedule",
				TargetRef:   schedule.Key,
				Description: "This project schedule is disabled in its source contract.",
			})
			continue
		}
		actions = append(actions,
			PlanAction{
				Action:      "would_register_schedule",
				Status:      "ready",
				TargetKind:  "schedule",
				TargetRef:   schedule.BackendScheduleKey,
				Description: "Slice 06 will register this project schedule through the automation scheduler.",
			},
			PlanAction{
				Action:      "would_pause_project_schedule",
				Status:      "ready",
				TargetKind:  "schedule",
				TargetRef:   schedule.BackendScheduleKey,
				Description: "Project schedules are registered as paused until the user explicitly resumes or manually fires them.",
			},
		)
	}
	for _, event := range plan.DirectEvents {
		if event.ManifestPath == "" {
			continue
		}
		if event.ContractStatus == "disabled" {
			actions = append(actions, PlanAction{
				Action:      "would_skip_disabled_direct_event",
				Status:      "disabled",
				TargetKind:  "direct_event_endpoint",
				TargetRef:   event.BackendEndpointSlug,
				Description: "This project direct event is disabled in its source contract.",
			})
			continue
		}
		actions = append(actions,
			PlanAction{
				Action:      "would_register_integration",
				Status:      "ready",
				TargetKind:  "integration",
				TargetRef:   event.BackendIntegrationKey,
				Description: "Slice 07 will register this project-owned direct-event integration.",
			},
			PlanAction{
				Action:      "would_register_direct_event_endpoint",
				Status:      "ready",
				TargetKind:  "direct_event_endpoint",
				TargetRef:   event.BackendEndpointSlug,
				Description: "Slice 07 will register this project direct event through the automation direct-event substrate.",
			},
			PlanAction{
				Action:      "would_pause_project_direct_event",
				Status:      "ready",
				TargetKind:  "direct_event_endpoint",
				TargetRef:   event.BackendEndpointSlug,
				Description: "Project direct events are registered as paused until the user explicitly resumes or test-ingests them.",
			},
		)
	}
	for _, connector := range plan.Connectors {
		if connector.ManifestPath == "" {
			continue
		}
		if connector.ContractStatus == "disabled" {
			actions = append(actions, PlanAction{
				Action:      "would_skip_disabled_connector",
				Status:      "disabled",
				TargetKind:  "connector",
				TargetRef:   connector.Key,
				Description: "This connector provider is disabled in its source contract.",
			})
			continue
		}
		actions = append(actions, PlanAction{
			Action:      "would_register_connector_provider",
			Status:      "ready",
			TargetKind:  "provider",
			TargetRef:   connector.ProviderAddress,
			Description: "Slice 09 will register this connector as a normal capability provider.",
		})
		if connector.RuntimeKind != "script" {
			actions = append(actions, PlanAction{
				Action:      "would_block_unsupported_connector_runtime",
				Status:      "blocked",
				TargetKind:  "connector",
				TargetRef:   connector.Key,
				Description: "Only script-backed connector endpoints are activatable in Slice 09.",
			})
			continue
		}
		for _, capability := range connector.Capabilities {
			if capability.ContractStatus == "disabled" {
				actions = append(actions, PlanAction{
					Action:      "would_skip_disabled_connector_capability",
					Status:      "disabled",
					TargetKind:  "capability_endpoint",
					TargetRef:   capability.CapabilityAddress,
					Description: "This connector capability is disabled in its source contract.",
				})
				continue
			}
			if capability.RuntimeKind != "script" {
				actions = append(actions, PlanAction{
					Action:      "would_block_unsupported_connector_runtime",
					Status:      "blocked",
					TargetKind:  "capability_endpoint",
					TargetRef:   capability.CapabilityAddress,
					Description: "Only script-backed connector endpoints are activatable in Slice 09.",
				})
				continue
			}
			actions = append(actions,
				PlanAction{
					Action:      "would_register_connector_script",
					Status:      "ready",
					TargetKind:  "script",
					TargetRef:   capability.RuntimeScript,
					Description: "Slice 09 will register this connector script package through the existing script registry.",
				},
				PlanAction{
					Action:      "would_register_connector_capability_class",
					Status:      "ready",
					TargetKind:  "capability_class",
					TargetRef:   capability.ClassNamespace + "." + capability.ClassName,
					Description: "Slice 09 will register the connector capability class.",
				},
				PlanAction{
					Action:      "would_register_connector_capability_endpoint",
					Status:      "ready",
					TargetKind:  "capability_endpoint",
					TargetRef:   capability.CapabilityAddress,
					Description: "Slice 09 will register the connector capability endpoint.",
				},
				PlanAction{
					Action:      "would_register_connector_endpoint_version",
					Status:      "ready",
					TargetKind:  "endpoint_version",
					TargetRef:   ScriptVersionLabel(connector.Version, capability.ImplementationHash),
					Description: "Slice 09 will register an endpoint version tied to the connector implementation hash.",
				},
				PlanAction{
					Action:      "would_register_connector_runtime_binding",
					Status:      "ready",
					TargetKind:  "runtime_binding",
					TargetRef:   capability.CapabilityAddress,
					Description: "Slice 09 will bind the endpoint version to the script runtime executor.",
				},
			)
		}
		for _, doc := range connector.UsageDocuments {
			actions = append(actions, PlanAction{
				Action:      "would_register_connector_usage_document",
				Status:      "ready",
				TargetKind:  "usage_document",
				TargetRef:   doc.Path,
				Description: "Slice 09 will register connector usage documentation with the capability registry.",
			})
		}
	}
	for _, module := range plan.Modules {
		if module.ManifestPath == "" {
			continue
		}
		if !module.RegistrationEnabled {
			actions = append(actions, PlanAction{
				Action:      "would_skip_disabled_module",
				Status:      "disabled",
				TargetKind:  "module",
				TargetRef:   firstNonEmptyString(module.ModuleID, module.Key),
				Description: "This module package is disabled or registration is disabled in its project wrapper.",
			})
			continue
		}
		actions = append(actions,
			PlanAction{
				Action:      "would_register_module_package",
				Status:      "ready",
				TargetKind:  "module_package",
				TargetRef:   module.ModuleID,
				Description: "Slice 11 will register this project module package through the existing module substrate.",
			},
			PlanAction{
				Action:      "would_record_project_module_registration",
				Status:      "ready",
				TargetKind:  "project_module_registration",
				TargetRef:   module.ModuleID,
				Description: "Slice 11 will persist the ownership link between this project and the registered module version.",
			},
		)
		if module.InstallPlan.InstallAfterRegister {
			actions = append(actions, PlanAction{
				Action:      "would_plan_module_install",
				Status:      "manual",
				TargetKind:  "module",
				TargetRef:   module.ModuleID,
				Description: "Project module registration does not implicitly install or enable modules.",
			})
		}
		if module.InstallPlan.EnableAfterInstall {
			actions = append(actions, PlanAction{
				Action:      "would_plan_module_enable",
				Status:      "manual",
				TargetKind:  "module",
				TargetRef:   module.ModuleID,
				Description: "Project module registration does not implicitly enable modules.",
			})
		}
		if module.ExposurePlan.ExposeAfterEnable || len(module.ExposurePlan.Capabilities) > 0 {
			actions = append(actions, PlanAction{
				Action:      "would_plan_module_capability_exposure",
				Status:      "manual",
				TargetKind:  "module",
				TargetRef:   module.ModuleID,
				Description: "Project module registration does not implicitly expose module capabilities.",
			})
		}
	}
	for _, note := range plan.Notes {
		actions = append(actions, PlanAction{
			Action:      "would_validate_notes_policy",
			Status:      "ready",
			TargetKind:  "notes",
			TargetRef:   note.RootKey,
			Description: "Validation parses the resolved notes singleton contract and derives watched-root intent without creating node-agent config.",
		})
	}
	for _, repo := range plan.Repos {
		actions = append(actions, PlanAction{
			Action:      "would_validate_repo_policy",
			Status:      "ready",
			TargetKind:  "repo_root",
			TargetRef:   repo.Key,
			Description: "Validation parses the resolved repos singleton contract and derives backup-first watched-root intent without creating node-agent config.",
		})
	}
	for _, root := range plan.WatchedRoots {
		actions = append(actions,
			PlanAction{
				Action:      "would_generate_watched_root_config",
				Status:      "ready",
				TargetKind:  "watched_root",
				TargetRef:   root.BackendRootKey,
				Description: "Project policies compile to watched-root config JSON, but validation does not apply it.",
			},
			PlanAction{
				Action:      "would_register_project_watched_root",
				Status:      "ready",
				TargetKind:  "project_watched_root",
				TargetRef:   root.BackendRootKey,
				Description: "Part 2 will persist this desired watched-root state on the main node.",
			},
			PlanAction{
				Action:      "would_require_node_agent_safe_root",
				Status:      "manual",
				TargetKind:  "filesystem_safe_root",
				TargetRef:   root.SafeRootKey,
				Description: "The owner node must expose the project folder as a filesystem safe root before applying watched-root config.",
			},
			PlanAction{
				Action:      "would_apply_node_agent_watched_root",
				Status:      "manual",
				TargetKind:  "watched_root",
				TargetRef:   root.BackendRootKey,
				Description: "The generated node-agent recipe applies this watched root locally on the owner node.",
			},
		)
	}
	sort.SliceStable(actions, func(i, j int) bool {
		if actionRank(actions[i].Action) != actionRank(actions[j].Action) {
			return actionRank(actions[i].Action) < actionRank(actions[j].Action)
		}
		if actions[i].TargetKind != actions[j].TargetKind {
			return actions[i].TargetKind < actions[j].TargetKind
		}
		return actions[i].TargetRef < actions[j].TargetRef
	})
	return actions
}

func actionRank(action string) int {
	switch action {
	case "would_create_or_update_project":
		return 0
	case "would_register_project_contract":
		return 1
	case "would_register_project_provider":
		return 2
	case "would_scan_facet":
		return 3
	case "would_register_script":
		return 4
	case "would_register_connector_provider":
		return 4
	case "would_skip_disabled_script_exposure":
		return 5
	case "would_skip_disabled_connector":
		return 5
	case "would_skip_disabled_connector_capability":
		return 5
	case "would_register_connector_script":
		return 5
	case "would_register_capability_class":
		return 6
	case "would_register_connector_capability_class":
		return 6
	case "would_register_capability_endpoint":
		return 7
	case "would_register_connector_capability_endpoint":
		return 7
	case "would_register_endpoint_version":
		return 8
	case "would_register_connector_endpoint_version":
		return 8
	case "would_register_script_runtime_binding":
		return 9
	case "would_register_connector_runtime_binding":
		return 9
	case "would_register_connector_usage_document":
		return 9
	case "would_block_unsupported_connector_runtime":
		return 9
	case "would_validate_workflow_contract":
		return 10
	case "would_link_script_backed_workflow":
		return 11
	case "would_register_workflow_package":
		return 12
	case "would_register_workflow_capability_endpoint":
		return 13
	case "would_register_workflow_runtime_binding":
		return 14
	case "would_skip_draft_workflow_activation":
		return 15
	case "would_block_first_class_workflow_runtime":
		return 16
	case "would_skip_disabled_workflow":
		return 17
	case "would_register_module_package":
		return 18
	case "would_record_project_module_registration":
		return 19
	case "would_skip_disabled_module":
		return 20
	case "would_plan_module_install":
		return 21
	case "would_plan_module_enable":
		return 22
	case "would_plan_module_capability_exposure":
		return 23
	case "would_register_schedule":
		return 24
	case "would_pause_project_schedule":
		return 25
	case "would_skip_disabled_schedule":
		return 26
	case "would_register_integration":
		return 27
	case "would_register_direct_event_endpoint":
		return 28
	case "would_pause_project_direct_event":
		return 29
	case "would_skip_disabled_direct_event":
		return 30
	case "would_validate_notes_policy":
		return 31
	case "would_validate_repo_policy":
		return 32
	case "would_generate_watched_root_config":
		return 33
	case "would_register_project_watched_root":
		return 34
	case "would_require_node_agent_safe_root":
		return 35
	case "would_apply_node_agent_watched_root":
		return 36
	default:
		return 40
	}
}

func descriptionForFacet(facet string) string {
	switch facet {
	case "notes":
		return "Slice 08 will validate notes policy and generate sync/index watched-root plans."
	case "repos":
		return "Slice 08 will validate repo backup and optional docs sync policy."
	case "scripts":
		return "Slice 05 will validate and register script packages and project-owned capability exposure."
	case "workflows":
		return "v0.3.1 validates executable workflow packages while preserving placeholder workflow intent."
	case "connectors":
		return "Slice 09 will validate connector providers and capability endpoints."
	case "schedules":
		return "Slice 06 will validate and register paused schedules."
	case "direct_events":
		return "Slice 07 will validate integrations, mappings, and paused direct event endpoints."
	case "modules":
		return "Slice 11 will validate and register module packages through the existing module substrate."
	case "sync_policy":
		return "Slice 08 will validate sync desired state and watched-root application plans."
	case "backup_policy":
		return "Slice 08 will validate backup desired state and watched-root application plans."
	case "worker_policy":
		return "Later slices will validate project-scoped worker policy."
	default:
		return "Later slices will validate and register this project facet."
	}
}
