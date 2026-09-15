package projectactivation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/events"
	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/modules"
	"loom.local/loom/internal/projectaccess"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/scripts"
	"loom.local/loom/internal/serviceregistry"
	"loom.local/loom/internal/workflows"
)

const ProjectArchiveServiceActionMetadataSchemaVersion = "project.deactivation.service_action.v1"

// ProjectArchiveServiceActionMetadata is the authority-bearing identity sealed
// into both reviewed service dry-runs and their matching apply actions. Summary
// text is deliberately excluded from this contract.
type ProjectArchiveServiceActionMetadata struct {
	SchemaVersion        string `json:"schema_version"`
	OwnerNode            string `json:"owner_node"`
	ProviderKey          string `json:"provider_key"`
	ProviderAddress      string `json:"provider_address"`
	ProviderID           string `json:"provider_id"`
	RuntimeProfileDigest string `json:"runtime_profile_digest"`
	AllowlistKey         string `json:"allowlist_key"`
	Manager              string `json:"manager"`
	Unit                 string `json:"unit"`
}

var (
	ErrNoRegisteredContract                 = errors.New("project has no registered project contract")
	ErrScriptsFacetInactive                 = errors.New("scripts facet is not enabled and present")
	ErrProjectRootUnreadable                = errors.New("project root is not readable")
	ErrProjectRuntimeAccessBlocked          = errors.New("project runtime access blocked")
	ErrContractStale                        = errors.New("project contract is stale")
	ErrRemoteProjectScriptsUnsupported      = errors.New("project script activation is local-only")
	ErrWorkflowsFacetInactive               = errors.New("workflows facet is not enabled and present")
	ErrWorkflowRuntimeUnsupported           = errors.New("workflow runtime is not activatable")
	ErrWorkflowScriptShimUnavailable        = errors.New("script-backed workflow shim is not available")
	ErrRemoteProjectWorkflowsUnsupported    = errors.New("project workflow activation is local-only")
	ErrSchedulesFacetInactive               = errors.New("schedules facet is not enabled and present")
	ErrScheduleTargetUnavailable            = errors.New("project schedule target is unavailable")
	ErrDirectEventsFacetInactive            = errors.New("direct_events facet is not enabled and present")
	ErrDirectEventTargetUnavailable         = errors.New("project direct event target is unavailable")
	ErrDirectEventCredentialRequired        = errors.New("project direct event credential profile is required")
	ErrProjectCredentialUnavailable         = errors.New("project credential unavailable")
	ErrConnectorsFacetInactive              = errors.New("connectors facet is not enabled and present")
	ErrConnectorRuntimeUnsupported          = errors.New("project connector runtime is not supported in this slice")
	ErrConnectorScriptActivationUnsupported = errors.New("project connector script activation is local-only")
	ErrModulesFacetInactive                 = errors.New("modules facet is not enabled and present")
	ErrServicesFacetInactive                = errors.New("services facet is not enabled and present")
	ErrRemoteProjectModulesUnsupported      = errors.New("project module activation is local-only")
	ErrProjectModuleActivationUnsupported   = ErrRemoteProjectModulesUnsupported
	ErrModuleRegistrationUnavailable        = errors.New("project module package registration failed")
)

type Deps struct {
	Applications serviceregistry.ApplicationPublicationReader
	Projects     ProjectService
	Scripts      ScriptService
	Workflows    WorkflowService
	Capabilities CapabilityService
	Automation   AutomationService
	Modules      ModuleService
	Events       EventService
	Allowlists   serviceregistry.AllowlistResolver
	Now          func() time.Time
}

type Service struct {
	Applications serviceregistry.ApplicationPublicationReader
	Projects     ProjectService
	Scripts      ScriptService
	Workflows    WorkflowService
	Capabilities CapabilityService
	Automation   AutomationService
	Modules      ModuleService
	Events       EventService
	Allowlists   serviceregistry.AllowlistResolver
	Now          func() time.Time
}

type ProjectService interface {
	GetProjectRegistrationStatus(context.Context, string) (projects.ProjectRegistrationDetail, error)
	ActivateProjectBase(context.Context, requestctx.Context, string) (projects.ProjectRegistrationDetail, error)
	UpsertProjectScriptExposure(context.Context, requestctx.Context, projects.UpsertProjectScriptExposureInput) (projects.ProjectScriptExposure, error)
	UpsertProjectScheduleRegistration(context.Context, requestctx.Context, projects.UpsertProjectScheduleRegistrationInput) (projects.ProjectScheduleRegistration, error)
	UpsertProjectDirectEventRegistration(context.Context, requestctx.Context, projects.UpsertProjectDirectEventRegistrationInput) (projects.ProjectDirectEventRegistration, error)
	UpsertProjectConnectorRegistration(context.Context, requestctx.Context, projects.UpsertProjectConnectorRegistrationInput) (projects.ProjectConnectorRegistration, error)
	UpsertProjectModuleRegistration(context.Context, requestctx.Context, projects.UpsertProjectModuleRegistrationInput) (projects.ProjectModuleRegistration, error)
	UpsertProjectWorkflowRegistration(context.Context, requestctx.Context, projects.UpsertProjectWorkflowRegistrationInput) (projects.ProjectWorkflowRegistration, error)
	MarkStaleProjectModuleRegistrations(context.Context, requestctx.Context, string, string, []string) error
	MarkProjectFacetActivated(context.Context, requestctx.Context, string, string, string, json.RawMessage) error
	MarkProjectFacetDeactivated(context.Context, requestctx.Context, string, string, string, json.RawMessage) error
	MarkProjectScriptExposuresDeactivated(context.Context, requestctx.Context, string, []string, json.RawMessage) error
	MarkProjectScheduleRegistrationsDeactivated(context.Context, requestctx.Context, string, []string, json.RawMessage) error
	MarkProjectDirectEventRegistrationsDeactivated(context.Context, requestctx.Context, string, []string, json.RawMessage) error
	MarkProjectWatchedRootsDeactivated(context.Context, requestctx.Context, string, []string, json.RawMessage) error
	MarkProjectConnectorRegistrationsDeactivated(context.Context, requestctx.Context, string, []string, json.RawMessage) error
	MarkProjectModuleRegistrationsDeactivated(context.Context, requestctx.Context, string, []string, json.RawMessage) error
	MarkProjectWorkflowRegistrationsDeactivated(context.Context, requestctx.Context, string, []string, json.RawMessage) error
}

type ScriptService interface {
	RegisterScript(context.Context, requestctx.Context, scripts.RegisterInput) (scripts.RegisterResult, error)
}

type WorkflowService interface {
	RegisterWorkflow(context.Context, requestctx.Context, workflows.RegisterInput) (workflows.RegisterResult, error)
}

type CapabilityService interface {
	EnsureProvider(context.Context, requestctx.Context, capabilities.RegisterProviderInput) (capabilities.Provider, bool, error)
	UpsertProviderHealth(context.Context, requestctx.Context, string, capabilities.ProviderHealthInput) (capabilities.ProviderHealth, error)
	EnsureCapabilityClass(context.Context, requestctx.Context, capabilities.RegisterCapabilityClassInput) (capabilities.CapabilityClass, bool, error)
	EnsureCapabilityEndpoint(context.Context, requestctx.Context, capabilities.RegisterCapabilityEndpointInput) (capabilities.CapabilityEndpoint, bool, error)
	EnsureEndpointVersion(context.Context, requestctx.Context, capabilities.RegisterEndpointVersionInput) (capabilities.EndpointVersion, bool, error)
	RegisterRuntimeBinding(context.Context, requestctx.Context, capabilities.RegisterRuntimeBindingInput) (capabilities.RuntimeBindingInspection, error)
	EnsureUsageDocument(context.Context, requestctx.Context, capabilities.RegisterUsageDocumentInput) (capabilities.UsageDocument, bool, error)
	DisableCapabilityEndpoint(context.Context, requestctx.Context, string, json.RawMessage) (capabilities.CapabilityEndpoint, error)
	DisableRuntimeBinding(context.Context, requestctx.Context, string, json.RawMessage) (capabilities.EndpointRuntimeBinding, error)
	ListProviders(context.Context, capabilities.ProviderFilter) ([]capabilities.ProviderListItem, error)
	InspectProvider(context.Context, string) (capabilities.ProviderInspection, error)
	InspectCapability(context.Context, string) (capabilities.CapabilityInspection, error)
}

type AutomationService interface {
	EnsureSchedule(context.Context, requestctx.Context, automation.CreateScheduleInput) (automation.ScheduleDetail, bool, error)
	DisableSchedule(context.Context, requestctx.Context, string, automation.UpdateScheduleStatusInput) (automation.ScheduleDetail, error)
	EnsureIntegration(context.Context, requestctx.Context, automation.CreateIntegrationInput) (automation.IntegrationDetail, bool, error)
	EnsureIntegrationAuthProfile(context.Context, requestctx.Context, string, automation.CreateIntegrationAuthProfileInput) (automation.IntegrationAuthProfile, bool, error)
	EnsureDirectEventEndpoint(context.Context, requestctx.Context, automation.CreateDirectEventEndpointInput) (automation.DirectEventEndpointDetail, bool, error)
	DisableDirectEventEndpoint(context.Context, requestctx.Context, string, automation.UpdateDirectEventEndpointStatusInput) (automation.DirectEventEndpointDetail, error)
}

type ModuleService interface {
	RegisterPackage(context.Context, requestctx.Context, modules.RegisterPackageInput) (modules.ModuleRegistration, error)
}

type EventService interface {
	Append(context.Context, events.AppendInput) (events.Event, error)
}

type projectArchiveLockAcquirer interface {
	AcquireProjectArchiveLocks(context.Context, []string) (func() error, error)
}

func NewService(deps Deps) Service {
	return Service{
		Applications: deps.Applications,
		Projects:     deps.Projects,
		Scripts:      deps.Scripts,
		Workflows:    deps.Workflows,
		Capabilities: deps.Capabilities,
		Automation:   deps.Automation,
		Modules:      deps.Modules,
		Events:       deps.Events,
		Allowlists:   deps.Allowlists,
		Now:          deps.Now,
	}
}

func (s Service) Activate(ctx context.Context, req requestctx.Context, ref string, input projects.ActivateProjectInput) (projects.ProjectRegistrationDetail, error) {
	detail, err := s.Projects.GetProjectRegistrationStatus(ctx, ref)
	if err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	if err := ensureProjectActivationMutable(detail, input.Facet); err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	switch normalizeFacet(input.Facet) {
	case "":
		return s.activateProjectBase(ctx, req, ref)
	case "all":
		return s.ActivateAllRuntimeFacets(ctx, req, ref, input)
	case "scripts":
		return s.ActivateScripts(ctx, req, ref, input)
	case "workflows":
		return s.ActivateWorkflows(ctx, req, ref, input)
	case "schedules":
		return s.ActivateSchedules(ctx, req, ref, input)
	case "direct_events":
		return s.ActivateDirectEvents(ctx, req, ref, input)
	case "connectors":
		return s.ActivateConnectors(ctx, req, ref, input)
	case "modules":
		return s.ActivateModules(ctx, req, ref, input)
	case "services":
		return s.ActivateServices(ctx, req, ref, input)
	default:
		return projects.ProjectRegistrationDetail{}, projects.ErrFacetActivationUnsupported
	}
}

func (s Service) acquireProjectActivationLock(ctx context.Context, ref, facet string) (func() error, error) {
	locker, ok := s.Projects.(projectArchiveLockAcquirer)
	if !ok {
		return func() error { return nil }, nil
	}
	detail, err := s.Projects.GetProjectRegistrationStatus(ctx, ref)
	if err != nil {
		return nil, err
	}
	project := detail.Project.Project
	keys := projects.ProjectArchiveLockKeys(project.ProjectID, project.Slug, "", nil, []string{normalizeFacet(facet)})
	return locker.AcquireProjectArchiveLocks(ctx, keys)
}

func (s Service) activateProjectBase(ctx context.Context, req requestctx.Context, ref string) (projects.ProjectRegistrationDetail, error) {
	release, err := s.acquireProjectActivationLock(ctx, ref, "base")
	if err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	defer func() { _ = release() }()
	return s.Projects.ActivateProjectBase(ctx, req, ref)
}

func ensureProjectActivationMutable(detail projects.ProjectRegistrationDetail, facet string) error {
	resourceRef := normalizeFacet(facet)
	if resourceRef == "" {
		resourceRef = "base"
	}
	return projects.EnsureProjectRegistrationMutable(detail, "project_activation", resourceRef)
}

func (s Service) ActivateAllRuntimeFacets(ctx context.Context, req requestctx.Context, ref string, input projects.ActivateProjectInput) (projects.ProjectRegistrationDetail, error) {
	detail, err := s.activateProjectBase(ctx, req, ref)
	if err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	projectRef := ref
	if detail.Project.Project.ProjectID != "" {
		projectRef = detail.Project.Project.ProjectID
	}
	for _, facet := range []string{"scripts", "workflows", "connectors", "modules", "services", "schedules", "direct_events"} {
		current, err := s.Projects.GetProjectRegistrationStatus(ctx, projectRef)
		if err != nil {
			return projects.ProjectRegistrationDetail{}, err
		}
		if !facetActivatableForAll(current.Facets, facet) {
			detail = current
			continue
		}
		facetInput := input
		facetInput.Facet = facet
		switch facet {
		case "scripts":
			detail, err = s.ActivateScripts(ctx, req, projectRef, facetInput)
		case "workflows":
			detail, err = s.ActivateWorkflows(ctx, req, projectRef, facetInput)
		case "connectors":
			detail, err = s.ActivateConnectors(ctx, req, projectRef, facetInput)
		case "modules":
			detail, err = s.ActivateModules(ctx, req, projectRef, facetInput)
		case "services":
			detail, err = s.ActivateServices(ctx, req, projectRef, facetInput)
		case "schedules":
			detail, err = s.ActivateSchedules(ctx, req, projectRef, facetInput)
		case "direct_events":
			detail, err = s.ActivateDirectEvents(ctx, req, projectRef, facetInput)
		}
		if err != nil {
			return projects.ProjectRegistrationDetail{}, fmt.Errorf("activate project facet %s: %w", facet, err)
		}
		if detail.Project.Project.ProjectID != "" {
			projectRef = detail.Project.Project.ProjectID
		}
	}
	return detail, nil
}

func (s Service) Deactivate(ctx context.Context, req requestctx.Context, ref string, input projects.DeactivateProjectInput) (projects.ProjectDeactivationResult, error) {
	return s.deactivate(ctx, req, ref, input, false)
}

// DeactivateForPhysicalArchive is the explicit archive-only deactivation path.
// Service actions produced through this path bind the reviewed allowlist and
// exact provider/runtime identity before either a dry-run is accepted or any
// registry mutation is attempted.
func (s Service) DeactivateForPhysicalArchive(ctx context.Context, req requestctx.Context, ref string, input projects.DeactivateProjectInput) (projects.ProjectDeactivationResult, error) {
	return s.deactivate(ctx, req, ref, input, true)
}

func (s Service) deactivate(ctx context.Context, req requestctx.Context, ref string, input projects.DeactivateProjectInput, physicalArchive bool) (projects.ProjectDeactivationResult, error) {
	facet := normalizeFacet(input.Facet)
	if facet == "" {
		return projects.ProjectDeactivationResult{}, fmt.Errorf("project deactivation facet is required")
	}
	detail, err := s.Projects.GetProjectRegistrationStatus(ctx, ref)
	if err != nil {
		return projects.ProjectDeactivationResult{}, err
	}
	if detail.Registration == nil {
		return projects.ProjectDeactivationResult{}, ErrNoRegisteredContract
	}
	result := projects.ProjectDeactivationResult{
		Detail: detail,
		Facet:  facet,
		DryRun: input.DryRun,
	}
	metadata := mustJSONObject(map[string]any{
		"source":       "project.deactivate",
		"project_id":   detail.Project.Project.ProjectID,
		"project_slug": detail.Project.Project.Slug,
		"facet":        facet,
		"reason":       strings.TrimSpace(input.Reason),
	})
	switch facet {
	case "scripts":
		return s.deactivateScripts(ctx, req, detail, input, result, metadata)
	case "schedules":
		return s.deactivateSchedules(ctx, req, detail, input, result, metadata)
	case "direct_events":
		return s.deactivateDirectEvents(ctx, req, detail, input, result, metadata)
	case "connectors":
		return s.deactivateConnectors(ctx, req, detail, input, result, metadata)
	case "modules":
		return s.deactivateModules(ctx, req, detail, input, result, metadata)
	case "watched_roots":
		return s.deactivateWatchedRoots(ctx, req, detail, input, result, metadata)
	case "workflows":
		return s.deactivateWorkflows(ctx, req, detail, input, result, metadata)
	case "services":
		return s.deactivateServices(ctx, req, detail, input, result, metadata, physicalArchive)
	default:
		return projects.ProjectDeactivationResult{}, projects.ErrFacetActivationUnsupported
	}
}

func (s Service) ActivateServices(ctx context.Context, req requestctx.Context, ref string, input projects.ActivateProjectInput) (projects.ProjectRegistrationDetail, error) {
	release, err := s.acquireProjectActivationLock(ctx, ref, "services")
	if err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	defer func() { _ = release() }()
	detail, err := s.Projects.GetProjectRegistrationStatus(ctx, ref)
	if err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	if err := ensureProjectActivationMutable(detail, "services"); err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	if detail.Registration == nil {
		return projects.ProjectRegistrationDetail{}, ErrNoRegisteredContract
	}
	if !servicesFacetActivatable(detail.Facets) {
		return projects.ProjectRegistrationDetail{}, ErrServicesFacetInactive
	}
	_, analysis, err := loadProjectActivationAnalysis(detail, input.ProjectRoot)
	if err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	if detail.Registration.ActivationStatus != projects.ProjectActivationStatusBaseActive {
		if _, err := s.Projects.ActivateProjectBase(ctx, req, detail.Project.Project.ProjectID); err != nil {
			return projects.ProjectRegistrationDetail{}, err
		}
	}
	registrations := make([]serviceregistry.ProjectRegistrationPlanInput, 0, len(analysis.Plan.Services))
	for _, item := range analysis.Plan.Services {
		contract := item.Contract
		operations := make([]serviceregistry.Operation, 0, len(contract.RequestedOperations()))
		for _, operation := range contract.RequestedOperations() {
			operations = append(operations, serviceregistry.Operation(operation))
		}
		registrations = append(registrations, serviceregistry.ProjectRegistrationPlanInput{
			ProviderKey: item.ProviderKey, ProjectSlug: detail.Project.Project.Slug, ScopeRef: detail.Project.Project.ProjectScopeID,
			Registration: serviceregistry.ProjectRegistrationInput{
				ProjectID: detail.Project.Project.ProjectID, ScopeKey: serviceregistry.ProjectRegistrationScopeKey(detail.Project.Project.Slug),
				TargetNode: contract.Service.TargetNode, SourcePath: item.ContractPath,
				Service: serviceregistry.ServiceIdentity{Key: contract.Service.Key, DisplayName: contract.Service.Name, Description: contract.Service.Description, Class: serviceregistry.ServiceClass(contract.Service.Class)},
				Runtime: serviceregistry.RuntimeProfileInput{
					Manager: serviceregistry.Manager(contract.Runtime.Manager), Unit: contract.Runtime.Unit,
					ServiceClass: serviceregistry.ServiceClass(contract.Service.Class), Operations: operations,
					Health:     serviceregistry.RuntimeHealth{Kind: serviceregistry.HealthKind(contract.Health.Kind)},
					References: serviceregistry.RuntimeReferences{Protection: contract.References.Protection, Exposure: contract.References.Exposure, Credentials: contract.References.Credentials},
				},
			},
		})
	}
	reconciled, err := serviceregistry.ReconcileProject(ctx, req, s.Capabilities, s.Allowlists, detail.Project.Project.Slug, registrations)
	if err != nil {
		return projects.ProjectRegistrationDetail{}, fmt.Errorf("reconcile project services: %w", err)
	}
	addresses := make([]string, 0, len(reconciled.Registrations))
	for _, registration := range reconciled.Registrations {
		addresses = append(addresses, registration.Provider.CompactAddress)
	}
	if err := s.Projects.MarkProjectFacetActivated(ctx, req, detail.Project.Project.ProjectID, detail.Registration.ProjectContractRegistrationID, "services", mustJSONObject(map[string]any{
		"activated_at": s.now().Format(time.RFC3339), "provider_count": len(addresses), "provider_addresses": addresses,
		"disabled_removed": reconciled.Disabled, "provisioning": "external", "source": "project.activate",
	})); err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	return s.Projects.GetProjectRegistrationStatus(ctx, detail.Project.Project.ProjectID)
}

func (s Service) deactivateServices(ctx context.Context, req requestctx.Context, detail projects.ProjectRegistrationDetail, input projects.DeactivateProjectInput, result projects.ProjectDeactivationResult, metadata json.RawMessage, physicalArchive bool) (projects.ProjectDeactivationResult, error) {
	const pageSize = 200
	providers := make([]capabilities.ProviderListItem, 0)
	for offset := 0; ; offset += pageSize {
		page, err := s.Capabilities.ListProviders(ctx, capabilities.ProviderFilter{ProjectRef: detail.Project.Project.Slug, ProviderType: capabilities.ProviderTypeService, Limit: pageSize, Offset: offset})
		if err != nil {
			return result, err
		}
		providers = append(providers, page...)
		if len(page) < pageSize {
			break
		}
	}
	type archiveTarget struct {
		inspection capabilities.ProviderInspection
		metadata   json.RawMessage
	}
	archiveTargets := make(map[string]archiveTarget, len(providers))
	if physicalArchive {
		for _, item := range providers {
			inspection, actionMetadata, err := s.projectArchiveServiceActionMetadata(ctx, item)
			if err != nil {
				return result, fmt.Errorf("seal service deactivation target %s: %w", item.ProviderKey, err)
			}
			archiveTargets[item.ProviderID] = archiveTarget{inspection: inspection, metadata: actionMetadata}
		}
	}
	for _, item := range providers {
		var inspection capabilities.ProviderInspection
		var actionMetadata json.RawMessage
		var err error
		if physicalArchive {
			target := archiveTargets[item.ProviderID]
			inspection, actionMetadata = target.inspection, target.metadata
		} else if !input.DryRun && item.Status != capabilities.ProviderStatusDisabled {
			inspection, err = s.Capabilities.InspectProvider(ctx, item.ProviderID)
			if err != nil {
				return result, err
			}
		}
		if item.Status == capabilities.ProviderStatusDisabled {
			action := deactivationAction(item.ProviderKey, "service", item.CompactAddress, "already_disabled", "Service provider is already disabled.")
			if physicalArchive {
				action.Metadata = actionMetadata
			}
			result.Actions = append(result.Actions, action)
			continue
		}
		status := "would_disable"
		if !input.DryRun {
			for _, endpoint := range inspection.Endpoints {
				capability, err := s.Capabilities.InspectCapability(ctx, endpoint.CapabilityEndpointID)
				if err != nil {
					return result, err
				}
				if capability.RuntimeBinding != nil && capability.RuntimeBinding.Status == capabilities.RuntimeBindingStatusActive {
					if _, err := s.Capabilities.DisableRuntimeBinding(ctx, req, capability.RuntimeBinding.RuntimeBindingID, metadata); err != nil {
						return result, err
					}
				}
				if endpoint.Status == capabilities.EndpointStatusActive {
					if _, err := s.Capabilities.DisableCapabilityEndpoint(ctx, req, endpoint.CapabilityEndpointID, metadata); err != nil {
						return result, err
					}
				}
			}
			_, _, err = s.Capabilities.EnsureProvider(ctx, req, capabilities.RegisterProviderInput{
				ProviderKey: item.ProviderKey, CompactAddress: item.CompactAddress, DisplayName: item.DisplayName, Description: item.Description,
				ProviderType: item.ProviderType, NodeRef: item.NodeID, ScopeRef: item.ScopeID, Version: item.Version,
				Status: capabilities.ProviderStatusDisabled, RuntimeProfileJSON: item.RuntimeProfileJSON,
				DocumentationRefsJSON: item.DocumentationRefsJSON, Metadata: item.Metadata,
			})
			if err != nil {
				return result, err
			}
			status = "disabled"
		}
		result.Changed = true
		action := deactivationAction(item.ProviderKey, "service", item.CompactAddress, status, "Disable service provider registry access without changing the host unit or data.")
		if physicalArchive {
			action.Metadata = actionMetadata
		}
		result.Actions = append(result.Actions, action)
	}
	if !input.DryRun {
		if err := s.Projects.MarkProjectFacetDeactivated(ctx, req, detail.Project.Project.ProjectID, detail.Registration.ProjectContractRegistrationID, "services", metadata); err != nil {
			return result, err
		}
		updated, err := s.Projects.GetProjectRegistrationStatus(ctx, detail.Project.Project.ProjectID)
		if err != nil {
			return result, err
		}
		result.Detail = updated
	}
	return result, nil
}

func (s Service) projectArchiveServiceActionMetadata(ctx context.Context, item capabilities.ProviderListItem) (capabilities.ProviderInspection, json.RawMessage, error) {
	if s.Capabilities == nil {
		return capabilities.ProviderInspection{}, nil, fmt.Errorf("capability registry is required")
	}
	inspection, err := s.Capabilities.InspectProvider(ctx, item.ProviderID)
	if err != nil {
		return capabilities.ProviderInspection{}, nil, err
	}
	provider := inspection.Provider
	if provider.ProviderID != item.ProviderID || provider.ProviderKey != item.ProviderKey || provider.CompactAddress != item.CompactAddress ||
		provider.ProviderType != capabilities.ProviderTypeService || provider.NodeID != item.NodeID || provider.ScopeID != item.ScopeID || provider.Status != item.Status {
		return capabilities.ProviderInspection{}, nil, fmt.Errorf("provider inspection does not match the listed service identity")
	}
	var source struct {
		Source string `json:"source"`
	}
	if json.Unmarshal(provider.Metadata, &source) == nil && source.Source == serviceregistry.ApplicationRegistrationSource {
		target, err := serviceregistry.ManagedApplicationArchiveTarget(ctx, s.Applications, provider)
		if err != nil {
			return capabilities.ProviderInspection{}, nil, err
		}
		raw, err := json.Marshal(ProjectArchiveServiceActionMetadata{SchemaVersion: ProjectArchiveServiceActionMetadataSchemaVersion, OwnerNode: target.OwnerNode, ProviderKey: target.ProviderKey, ProviderAddress: target.ProviderAddress, ProviderID: target.ProviderID, RuntimeProfileDigest: target.RuntimeProfileDigest, AllowlistKey: target.AllowlistKey, Manager: target.Manager, Unit: target.Unit})
		return inspection, raw, err
	}
	if s.Allowlists == nil {
		return capabilities.ProviderInspection{}, nil, fmt.Errorf("reviewed service allowlist resolver is required")
	}
	address, err := capabilities.ParseProviderAddress(provider.CompactAddress)
	if err != nil || address.ProviderKey != provider.ProviderKey {
		return capabilities.ProviderInspection{}, nil, fmt.Errorf("service provider address is not canonical")
	}
	profile, canonicalProfile, err := canonicalProjectArchiveRuntimeProfile(provider.RuntimeProfileJSON)
	if err != nil {
		return capabilities.ProviderInspection{}, nil, err
	}
	record, err := s.Allowlists.ResolveServiceAllowlist(ctx, address.ScopePath, profile)
	if err != nil {
		return capabilities.ProviderInspection{}, nil, err
	}
	record, err = serviceregistry.NormalizeAndValidateAllowlistRecord(record)
	if err != nil {
		return capabilities.ProviderInspection{}, nil, fmt.Errorf("reviewed service allowlist record is invalid: %w", err)
	}
	if record.NodeKey != address.ScopePath || record.Manager != profile.Manager || record.Unit != profile.Unit ||
		!projectArchiveServiceOperationAllowed(record.Operations, serviceregistry.OperationStop) || !projectArchiveServiceOperationAllowed(record.Operations, serviceregistry.OperationStatus) {
		return capabilities.ProviderInspection{}, nil, fmt.Errorf("reviewed service allowlist does not authorize exact stop and status observation")
	}
	digest := sha256.Sum256(canonicalProfile)
	runtimeDigest := fmt.Sprintf("sha256:%x", digest[:])
	identity := record.ProjectArchiveIdentity
	if identity == nil || identity.ProviderKey != provider.ProviderKey || identity.ProviderAddress != provider.CompactAddress ||
		identity.ProviderID != provider.ProviderID || identity.RuntimeProfileDigest != runtimeDigest {
		return capabilities.ProviderInspection{}, nil, fmt.Errorf("reviewed service allowlist archive identity does not match provider inspection")
	}
	actionMetadata, err := json.Marshal(ProjectArchiveServiceActionMetadata{
		SchemaVersion:        ProjectArchiveServiceActionMetadataSchemaVersion,
		OwnerNode:            record.NodeKey,
		ProviderKey:          provider.ProviderKey,
		ProviderAddress:      provider.CompactAddress,
		ProviderID:           provider.ProviderID,
		RuntimeProfileDigest: runtimeDigest,
		AllowlistKey:         record.Key,
		Manager:              string(record.Manager),
		Unit:                 record.Unit,
	})
	if err != nil {
		return capabilities.ProviderInspection{}, nil, err
	}
	return inspection, actionMetadata, nil
}

func canonicalProjectArchiveRuntimeProfile(raw json.RawMessage) (serviceregistry.RuntimeProfile, []byte, error) {
	var profile serviceregistry.RuntimeProfile
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&profile); err != nil {
		return serviceregistry.RuntimeProfile{}, nil, fmt.Errorf("decode service runtime profile: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return serviceregistry.RuntimeProfile{}, nil, fmt.Errorf("service runtime profile must contain one object")
	}
	canonical, err := serviceregistry.BuildRuntimeProfile(serviceregistry.RuntimeProfileInput{
		Manager: profile.Manager, Unit: profile.Unit, ServiceClass: profile.ServiceClass,
		Operations: profile.Operations, Health: profile.Health, References: profile.References,
	})
	if err != nil || !reflect.DeepEqual(profile, canonical) {
		return serviceregistry.RuntimeProfile{}, nil, fmt.Errorf("service runtime profile is not canonical")
	}
	canonicalRaw, err := json.Marshal(canonical)
	if err != nil {
		return serviceregistry.RuntimeProfile{}, nil, err
	}
	return canonical, canonicalRaw, nil
}

func projectArchiveServiceOperationAllowed(operations []serviceregistry.Operation, want serviceregistry.Operation) bool {
	for _, operation := range operations {
		if operation == want {
			return true
		}
	}
	return false
}

func DecodeProjectArchiveServiceActionMetadata(raw json.RawMessage) (ProjectArchiveServiceActionMetadata, error) {
	if len(raw) == 0 || len(raw) > 16*1024 {
		return ProjectArchiveServiceActionMetadata{}, fmt.Errorf("project archive service action metadata is empty or oversized")
	}
	var metadata ProjectArchiveServiceActionMetadata
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return ProjectArchiveServiceActionMetadata{}, fmt.Errorf("decode project archive service action metadata: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ProjectArchiveServiceActionMetadata{}, fmt.Errorf("project archive service action metadata must contain one object")
	}
	canonical, err := json.Marshal(metadata)
	if err != nil || !bytes.Equal(raw, canonical) {
		return ProjectArchiveServiceActionMetadata{}, fmt.Errorf("project archive service action metadata is not canonical")
	}
	address, addressErr := capabilities.ParseProviderAddress(metadata.ProviderAddress)
	if metadata.SchemaVersion != ProjectArchiveServiceActionMetadataSchemaVersion || addressErr != nil ||
		address.ScopePath != metadata.OwnerNode || address.ProviderKey != metadata.ProviderKey || metadata.AllowlistKey == "" ||
		metadata.Manager == "" || metadata.Unit == "" || !strings.HasPrefix(metadata.RuntimeProfileDigest, "sha256:") {
		return ProjectArchiveServiceActionMetadata{}, fmt.Errorf("project archive service action metadata identity is invalid")
	}
	return metadata, nil
}

func loadProjectActivationAnalysis(detail projects.ProjectRegistrationDetail, overrideRoot string) (string, projectcontracts.Analysis, error) {
	var zero projectcontracts.Analysis
	projectRoot := strings.TrimSpace(overrideRoot)
	if projectRoot == "" && detail.Registration != nil {
		projectRoot = strings.TrimSpace(detail.Registration.ProjectRoot)
	}
	if projectRoot == "" {
		return "", zero, fmt.Errorf("%w: no project root was provided or registered", ErrProjectRootUnreadable)
	}

	resolvedRoot := projectRoot
	if !filepath.IsAbs(resolvedRoot) {
		abs, err := filepath.Abs(resolvedRoot)
		if err != nil {
			return "", zero, fmt.Errorf("%w: could not resolve project root %q: %v", ErrProjectRootUnreadable, projectRoot, err)
		}
		resolvedRoot = abs
	}
	resolvedRoot = filepath.Clean(resolvedRoot)

	if info, err := os.Stat(resolvedRoot); err != nil {
		return "", zero, fmt.Errorf("%w: resolved project root %q from %q: %v", ErrProjectRootUnreadable, resolvedRoot, projectRoot, err)
	} else if !info.IsDir() {
		return "", zero, fmt.Errorf("%w: resolved project root %q from %q is not a directory", ErrProjectRootUnreadable, resolvedRoot, projectRoot)
	}

	analysis := projectcontracts.Analyze(resolvedRoot)
	if analysis.Loaded == nil {
		return "", analysis, fmt.Errorf("project contract could not be loaded from resolved project root %q", resolvedRoot)
	}
	if analysis.Report.Summary.Errors > 0 || !analysis.Report.OK {
		return "", analysis, fmt.Errorf("project contract at resolved project root %q has validation errors", resolvedRoot)
	}
	sum := sha256.Sum256(analysis.Loaded.Raw)
	if contractHash := fmt.Sprintf("sha256:%x", sum[:]); contractHash != detail.Registration.ContractHash {
		return "", analysis, fmt.Errorf("%w: resolved project root %q registered contract hash is %s but current hash is %s", ErrContractStale, resolvedRoot, detail.Registration.ContractHash, contractHash)
	}
	if checks := projectaccess.Blocking(projectaccess.Analyze(analysis)); len(checks) > 0 {
		return "", analysis, fmt.Errorf("%w: %s", ErrProjectRuntimeAccessBlocked, projectaccess.BlockingSummary(checks))
	}
	return resolvedRoot, analysis, nil
}

func resolveProjectCredentialBindings(projectRoot string, analysis projectcontracts.Analysis, spec projectcontracts.CredentialSpec) ([]jobs.CredentialBinding, error) {
	if len(spec.Required) == 0 {
		return nil, nil
	}
	if analysis.Loaded == nil {
		return nil, fmt.Errorf("%w: project contract is not loaded", ErrProjectCredentialUnavailable)
	}
	policy, policyPath, err := projectcontracts.LoadCredentialPolicy(projectRoot, analysis.Loaded.Contract)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProjectCredentialUnavailable, err)
	}
	refs := policy.ReferenceMap()
	bindings := make([]jobs.CredentialBinding, 0, len(spec.Required))
	for _, requirement := range spec.Required {
		requirement.Ref = strings.TrimSpace(requirement.Ref)
		requirement.ExposeAs = strings.TrimSpace(requirement.ExposeAs)
		requirement.Kind = strings.TrimSpace(requirement.Kind)
		ref, ok := refs[requirement.Ref]
		if !ok {
			return nil, fmt.Errorf("%w: %s is not declared in %s", ErrProjectCredentialUnavailable, requirement.Ref, policyPath)
		}
		if ref.Status == "optional" {
			continue
		}
		switch ref.Source.Kind {
		case "env":
			if strings.TrimSpace(ref.Source.Env) == "" {
				return nil, fmt.Errorf("%w: %s has no source.env", ErrProjectCredentialUnavailable, requirement.Ref)
			}
			if value, ok := os.LookupEnv(ref.Source.Env); !ok || value == "" {
				return nil, fmt.Errorf("%w: %s requires env %s", ErrProjectCredentialUnavailable, requirement.Ref, ref.Source.Env)
			}
			bindings = append(bindings, jobs.CredentialBinding{
				Ref:      requirement.Ref,
				Kind:     defaultString(requirement.Kind, "env"),
				ExposeAs: requirement.ExposeAs,
				Source: jobs.CredentialBindingSource{
					Kind: "env",
					Env:  ref.Source.Env,
				},
			})
		case "file":
			if strings.TrimSpace(ref.Source.Path) == "" {
				return nil, fmt.Errorf("%w: %s has no source.path", ErrProjectCredentialUnavailable, requirement.Ref)
			}
			handle, err := os.Open(ref.Source.Path)
			if err != nil {
				return nil, fmt.Errorf("%w: %s requires readable file %s: %v", ErrProjectCredentialUnavailable, requirement.Ref, ref.Source.Path, err)
			}
			_ = handle.Close()
			bindings = append(bindings, jobs.CredentialBinding{
				Ref:      requirement.Ref,
				Kind:     defaultString(requirement.Kind, "file"),
				ExposeAs: requirement.ExposeAs,
				Source: jobs.CredentialBindingSource{
					Kind: "file",
					Path: ref.Source.Path,
				},
			})
		default:
			return nil, fmt.Errorf("%w: %s has unsupported source kind %s", ErrProjectCredentialUnavailable, requirement.Ref, ref.Source.Kind)
		}
	}
	return bindings, nil
}

func (s Service) ActivateScripts(ctx context.Context, req requestctx.Context, ref string, input projects.ActivateProjectInput) (projects.ProjectRegistrationDetail, error) {
	release, err := s.acquireProjectActivationLock(ctx, ref, "scripts")
	if err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	defer func() { _ = release() }()
	detail, err := s.Projects.GetProjectRegistrationStatus(ctx, ref)
	if err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	if err := ensureProjectActivationMutable(detail, "scripts"); err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	if detail.Registration == nil {
		return projects.ProjectRegistrationDetail{}, ErrNoRegisteredContract
	}
	if !scriptsFacetActivatable(detail.Facets) {
		return projects.ProjectRegistrationDetail{}, ErrScriptsFacetInactive
	}
	if detail.Project.Project.HomeNodeID == nil || strings.TrimSpace(*detail.Project.Project.HomeNodeID) != strings.TrimSpace(req.OriginNodeID) {
		return projects.ProjectRegistrationDetail{}, ErrRemoteProjectScriptsUnsupported
	}

	projectRoot, analysis, err := loadProjectActivationAnalysis(detail, input.ProjectRoot)
	if err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}

	if detail.Registration.ActivationStatus != projects.ProjectActivationStatusBaseActive {
		if _, err := s.Projects.ActivateProjectBase(ctx, req, detail.Project.Project.ProjectID); err != nil {
			return projects.ProjectRegistrationDetail{}, err
		}
	}

	existingExposures := map[string]projects.ProjectScriptExposure{}
	for _, exposure := range detail.ScriptExposures {
		existingExposures[exposure.ScriptKey] = exposure
	}
	seenScripts := map[string]bool{}
	capabilityAddresses := []string{}
	for _, item := range analysis.Plan.Scripts {
		if strings.TrimSpace(item.ManifestPath) == "" {
			continue
		}
		seenScripts[item.Key] = true
		scriptSlug := projectcontracts.ProjectScriptSlug(detail.Project.Project.Slug, item.Key)
		registerResult, err := s.Scripts.RegisterScript(ctx, req, scripts.RegisterInput{
			ManifestPath: item.ManifestPath,
			ProjectRef:   detail.Project.Project.Slug,
			SlugOverride: scriptSlug,
			Activate:     true,
			Metadata:     mustJSONObject(map[string]any{"source": "project.activate", "project_id": detail.Project.Project.ProjectID, "project_slug": detail.Project.Project.Slug, "script_key": item.Key}),
		})
		if err != nil {
			return projects.ProjectRegistrationDetail{}, fmt.Errorf("register project script %s: %w", item.Key, err)
		}
		if !item.Exposed {
			previous := existingExposures[item.Key]
			if previous.ActivationStatus == projects.ProjectScriptExposureStatusActive {
				if err := s.disableProjectScriptCapability(ctx, req, previous); err != nil {
					return projects.ProjectRegistrationDetail{}, fmt.Errorf("disable previous script exposure %s: %w", item.Key, err)
				}
			}
			if _, err := s.Projects.UpsertProjectScriptExposure(ctx, req, projects.UpsertProjectScriptExposureInput{
				ProjectContractRegistrationID: detail.Registration.ProjectContractRegistrationID,
				ProjectID:                     detail.Project.Project.ProjectID,
				ScriptKey:                     item.Key,
				ScriptFolder:                  item.Folder,
				ScriptManifestPath:            item.ManifestPath,
				ExposurePath:                  item.ExposurePath,
				ExposureHash:                  item.ExposureHash,
				ExposureEnabled:               false,
				ProviderID:                    ptrStringValue(previous.ProviderID),
				ProviderAddress:               previous.ProviderAddress,
				ScriptID:                      registerResult.Script.ScriptID,
				ScriptVersionID:               registerResult.Version.ScriptVersionID,
				CapabilityEndpointID:          ptrStringValue(previous.CapabilityEndpointID),
				CapabilityEndpointVersionID:   ptrStringValue(previous.CapabilityEndpointVersionID),
				RuntimeBindingID:              ptrStringValue(previous.RuntimeBindingID),
				CapabilityAddress:             previous.CapabilityAddress,
				ActivationStatus:              projects.ProjectScriptExposureStatusDisabled,
				Metadata:                      mustJSONObject(map[string]any{"source": "project.activate", "backend_script_slug": scriptSlug}),
			}); err != nil {
				return projects.ProjectRegistrationDetail{}, err
			}
			continue
		}
		if item.Exposure == nil {
			return projects.ProjectRegistrationDetail{}, fmt.Errorf("script %s is exposed without parsed exposure contract", item.Key)
		}
		credentialBindings, err := resolveProjectCredentialBindings(projectRoot, analysis, item.Credentials)
		if err != nil {
			return projects.ProjectRegistrationDetail{}, fmt.Errorf("script %s credentials: %w", item.Key, err)
		}

		provider, _, err := s.Capabilities.EnsureProvider(ctx, req, capabilities.RegisterProviderInput{
			ProviderKey:           item.ProviderKey,
			CompactAddress:        item.ProviderAddress,
			DisplayName:           detail.Project.Project.Name,
			Description:           "Project script capabilities for " + detail.Project.Project.Name,
			ProviderType:          capabilities.ProviderTypeService,
			NodeRef:               req.OriginNodeID,
			ScopeRef:              detail.Project.Project.ProjectScopeID,
			Version:               "0.3.0",
			Status:                capabilities.ProviderStatusActive,
			RuntimeProfileJSON:    mustJSONObject(map[string]any{"runtime": "project_scripts", "project_id": detail.Project.Project.ProjectID}),
			DocumentationRefsJSON: json.RawMessage(`[]`),
			Metadata:              mustJSONObject(map[string]any{"source": "project.activate", "project_id": detail.Project.Project.ProjectID, "facet": "scripts"}),
		})
		if err != nil {
			return projects.ProjectRegistrationDetail{}, fmt.Errorf("ensure provider for %s: %w", item.Key, err)
		}
		now := s.now()
		if _, err := s.Capabilities.UpsertProviderHealth(ctx, req, provider.ProviderID, capabilities.ProviderHealthInput{
			HealthStatus:       capabilities.HealthStatusOK,
			AvailabilityStatus: capabilities.AvailabilityStatusAvailable,
			Message:            "Project script provider activated.",
			LastCheckedAt:      &now,
			LastOKAt:           &now,
			DetailsJSON:        mustJSONObject(map[string]any{"source": "project.activate", "facet": "scripts"}),
		}); err != nil {
			return projects.ProjectRegistrationDetail{}, fmt.Errorf("upsert provider health for %s: %w", item.Key, err)
		}

		class, _, err := s.Capabilities.EnsureCapabilityClass(ctx, req, capabilities.RegisterCapabilityClassInput{
			Namespace:                     item.Exposure.Capability.ClassNamespace,
			Name:                          item.Exposure.Capability.ClassName,
			Version:                       "0.3.0",
			DisplayName:                   defaultString(item.Exposure.Expose.DisplayName, item.Manifest.Name),
			Description:                   defaultString(item.Exposure.Expose.Description, item.Manifest.Description),
			Form:                          item.Form,
			InputSchemaJSON:               mustJSONObject(item.Exposure.Capability.InputSchema),
			OutputSchemaJSON:              mustJSONObject(item.Exposure.Capability.OutputSchema),
			DefaultRiskLevel:              item.RiskLevel,
			DefaultPolicyRequirementsJSON: json.RawMessage(`{}`),
			Status:                        capabilities.CapabilityClassStatusActive,
			Metadata:                      mustJSONObject(map[string]any{"source": "project.activate", "project_id": detail.Project.Project.ProjectID, "script_key": item.Key}),
		})
		if err != nil {
			return projects.ProjectRegistrationDetail{}, fmt.Errorf("ensure capability class for %s: %w", item.Key, err)
		}

		endpoint, _, err := s.Capabilities.EnsureCapabilityEndpoint(ctx, req, capabilities.RegisterCapabilityEndpointInput{
			ProviderRef:                 provider.ProviderID,
			CapabilityClassRef:          class.CapabilityClassID,
			EndpointName:                item.Endpoint,
			CompactAddress:              item.CapabilityAddress,
			Form:                        item.Form,
			InputSchemaJSON:             mustJSONObject(item.Exposure.Capability.InputSchema),
			OutputSchemaJSON:            mustJSONObject(item.Exposure.Capability.OutputSchema),
			RiskLevel:                   item.RiskLevel,
			ExecutionAuthorizationLevel: item.Exposure.Capability.ExecutionAuthorizationLevel,
			SideEffectsJSON:             mustJSONObject(map[string]any{"effects": item.Exposure.Capability.SideEffects}),
			PolicyRequirementsJSON:      json.RawMessage(`{}`),
			CredentialRequirementsJSON:  jsonOrEmptyObject(item.CredentialJSON),
			ApprovalRequirementsJSON:    mustJSONObject(map[string]any{"required": item.Exposure.Capability.RequiresApproval}),
			JobBehaviorJSON:             mustJSONObject(map[string]any{"execution_mode": item.ExecutionMode, "wait_timeout_seconds": item.WaitTimeoutSeconds}),
			SessionBehaviorJSON:         json.RawMessage(`{}`),
			StreamBehaviorJSON:          json.RawMessage(`{}`),
			LeaseBehaviorJSON:           json.RawMessage(`{}`),
			Status:                      capabilities.EndpointStatusActive,
			Metadata:                    mustJSONObject(map[string]any{"source": "project.activate", "project_id": detail.Project.Project.ProjectID, "script_key": item.Key}),
		})
		if err != nil {
			return projects.ProjectRegistrationDetail{}, fmt.Errorf("ensure capability endpoint for %s: %w", item.Key, err)
		}

		version, _, err := s.Capabilities.EnsureEndpointVersion(ctx, req, capabilities.RegisterEndpointVersionInput{
			CapabilityEndpointRef:       endpoint.CapabilityEndpointID,
			VersionLabel:                projectcontracts.ScriptVersionLabel(item.Manifest.Version, item.PackageHash),
			ImplementationHash:          item.PackageHash,
			ManifestJSON:                projectScriptEndpointManifest(item, registerResult),
			InputSchemaJSON:             mustJSONObject(item.Exposure.Capability.InputSchema),
			OutputSchemaJSON:            mustJSONObject(item.Exposure.Capability.OutputSchema),
			RiskLevel:                   item.RiskLevel,
			ExecutionAuthorizationLevel: item.Exposure.Capability.ExecutionAuthorizationLevel,
			PolicyRequirementsJSON:      json.RawMessage(`{}`),
			CredentialRequirementsJSON:  jsonOrEmptyObject(item.CredentialJSON),
			ApprovalRequirementsJSON:    mustJSONObject(map[string]any{"required": item.Exposure.Capability.RequiresApproval}),
			Status:                      capabilities.EndpointVersionStatusActive,
			ApprovedByActorID:           req.ActorID,
			ApprovedAt:                  &now,
			Metadata:                    mustJSONObject(map[string]any{"source": "project.activate", "project_id": detail.Project.Project.ProjectID, "script_key": item.Key}),
		})
		if err != nil {
			return projects.ProjectRegistrationDetail{}, fmt.Errorf("ensure endpoint version for %s: %w", item.Key, err)
		}

		runtimeConfig := map[string]any{
			"script_ref":           registerResult.Script.ScriptID,
			"project_ref":          detail.Project.Project.Slug,
			"execution_mode":       item.ExecutionMode,
			"wait_timeout_seconds": item.WaitTimeoutSeconds,
		}
		if len(credentialBindings) > 0 {
			runtimeConfig["credential_bindings"] = credentialBindings
		}
		runtimeBinding, err := s.Capabilities.RegisterRuntimeBinding(ctx, req, capabilities.RegisterRuntimeBindingInput{
			EndpointVersionRef: version.CapabilityEndpointVersionID,
			RuntimeKind:        capabilities.RuntimeKindScript,
			RuntimeConfigJSON:  mustJSONObject(runtimeConfig),
			InputMappingJSON:   json.RawMessage(`{}`),
			OutputMappingJSON:  json.RawMessage(`{}`),
			Status:             capabilities.RuntimeBindingStatusActive,
			ApprovedByActorID:  req.ActorID,
			ApprovedAt:         &now,
			Metadata:           mustJSONObject(map[string]any{"source": "project.activate", "project_id": detail.Project.Project.ProjectID, "script_key": item.Key}),
		})
		if err != nil {
			return projects.ProjectRegistrationDetail{}, fmt.Errorf("register script runtime binding for %s: %w", item.Key, err)
		}

		if _, err := s.Projects.UpsertProjectScriptExposure(ctx, req, projects.UpsertProjectScriptExposureInput{
			ProjectContractRegistrationID: detail.Registration.ProjectContractRegistrationID,
			ProjectID:                     detail.Project.Project.ProjectID,
			ScriptKey:                     item.Key,
			ScriptFolder:                  item.Folder,
			ScriptManifestPath:            item.ManifestPath,
			ExposurePath:                  item.ExposurePath,
			ExposureHash:                  item.ExposureHash,
			ExposureEnabled:               true,
			ProviderID:                    provider.ProviderID,
			ProviderAddress:               provider.CompactAddress,
			ScriptID:                      registerResult.Script.ScriptID,
			ScriptVersionID:               registerResult.Version.ScriptVersionID,
			CapabilityEndpointID:          endpoint.CapabilityEndpointID,
			CapabilityEndpointVersionID:   version.CapabilityEndpointVersionID,
			RuntimeBindingID:              runtimeBinding.Binding.RuntimeBindingID,
			CapabilityAddress:             endpoint.CompactAddress,
			ActivationStatus:              projects.ProjectScriptExposureStatusActive,
			Metadata:                      mustJSONObject(map[string]any{"source": "project.activate", "backend_script_slug": scriptSlug}),
		}); err != nil {
			return projects.ProjectRegistrationDetail{}, err
		}
		if _, err := s.Events.Append(ctx, events.AppendInput{
			EventType:  events.TypeProjectScriptExposed,
			EventLevel: "audit",
			Request:    req,
			ScopeID:    detail.Project.Project.ProjectScopeID,
			TargetKind: "capability_endpoint",
			TargetID:   endpoint.CapabilityEndpointID,
			Status:     projects.ProjectScriptExposureStatusActive,
			Result:     "ok",
			Payload: map[string]any{
				"project_id":                       detail.Project.Project.ProjectID,
				"project_slug":                     detail.Project.Project.Slug,
				"project_contract_registration_id": detail.Registration.ProjectContractRegistrationID,
				"facet_key":                        "scripts",
				"script_key":                       item.Key,
				"script_id":                        registerResult.Script.ScriptID,
				"script_version_id":                registerResult.Version.ScriptVersionID,
				"provider_address":                 provider.CompactAddress,
				"capability_address":               endpoint.CompactAddress,
				"capability_endpoint_id":           endpoint.CapabilityEndpointID,
				"capability_endpoint_version_id":   version.CapabilityEndpointVersionID,
				"runtime_binding_id":               runtimeBinding.Binding.RuntimeBindingID,
				"source":                           "project.activate",
			},
			VisibilityClass: "internal",
		}); err != nil {
			return projects.ProjectRegistrationDetail{}, err
		}
		capabilityAddresses = append(capabilityAddresses, endpoint.CompactAddress)
	}
	for _, previous := range existingExposures {
		if seenScripts[previous.ScriptKey] {
			continue
		}
		stale, err := s.Projects.UpsertProjectScriptExposure(ctx, req, projects.UpsertProjectScriptExposureInput{
			ProjectContractRegistrationID: detail.Registration.ProjectContractRegistrationID,
			ProjectID:                     detail.Project.Project.ProjectID,
			ScriptKey:                     previous.ScriptKey,
			ScriptFolder:                  previous.ScriptFolder,
			ScriptManifestPath:            previous.ScriptManifestPath,
			ExposurePath:                  previous.ExposurePath,
			ExposureHash:                  previous.ExposureHash,
			ExposureEnabled:               previous.ExposureEnabled,
			ProviderID:                    ptrStringValue(previous.ProviderID),
			ProviderAddress:               previous.ProviderAddress,
			ScriptID:                      ptrStringValue(previous.ScriptID),
			ScriptVersionID:               ptrStringValue(previous.ScriptVersionID),
			CapabilityEndpointID:          ptrStringValue(previous.CapabilityEndpointID),
			CapabilityEndpointVersionID:   ptrStringValue(previous.CapabilityEndpointVersionID),
			RuntimeBindingID:              ptrStringValue(previous.RuntimeBindingID),
			CapabilityAddress:             previous.CapabilityAddress,
			ActivationStatus:              projects.ProjectScriptExposureStatusStale,
			Metadata:                      mustJSONObject(map[string]any{"source": "project.activate", "reason": "script_not_found"}),
		})
		if err != nil {
			return projects.ProjectRegistrationDetail{}, err
		}
		if _, err := s.Events.Append(ctx, events.AppendInput{
			EventType:  events.TypeProjectScriptExposureStale,
			EventLevel: "audit",
			Request:    req,
			ScopeID:    detail.Project.Project.ProjectScopeID,
			TargetKind: "project_script_exposure",
			TargetID:   stale.ProjectScriptExposureID,
			Status:     projects.ProjectScriptExposureStatusStale,
			Result:     "ok",
			Payload: map[string]any{
				"project_id":                       detail.Project.Project.ProjectID,
				"project_slug":                     detail.Project.Project.Slug,
				"project_contract_registration_id": detail.Registration.ProjectContractRegistrationID,
				"facet_key":                        "scripts",
				"script_key":                       previous.ScriptKey,
				"capability_address":               previous.CapabilityAddress,
				"source":                           "project.activate",
			},
			VisibilityClass: "internal",
		}); err != nil {
			return projects.ProjectRegistrationDetail{}, err
		}
	}

	now := s.now()
	if err := s.Projects.MarkProjectFacetActivated(ctx, req, detail.Project.Project.ProjectID, detail.Registration.ProjectContractRegistrationID, "scripts", mustJSONObject(map[string]any{
		"activated_at":          now.Format(time.RFC3339),
		"activated_by_actor_id": req.ActorID,
		"script_count":          len(analysis.Plan.Scripts),
		"exposed_count":         len(capabilityAddresses),
		"capability_addresses":  capabilityAddresses,
		"source":                "project.activate",
	})); err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	return s.Projects.GetProjectRegistrationStatus(ctx, detail.Project.Project.ProjectID)
}

func (s Service) deactivateScripts(ctx context.Context, req requestctx.Context, detail projects.ProjectRegistrationDetail, input projects.DeactivateProjectInput, result projects.ProjectDeactivationResult, metadata json.RawMessage) (projects.ProjectDeactivationResult, error) {
	keys := []string{}
	for _, exposure := range detail.ScriptExposures {
		keys = append(keys, exposure.ScriptKey)
		if exposure.ActivationStatus == projects.ProjectScriptExposureStatusDisabled {
			result.Actions = append(result.Actions, deactivationAction(exposure.ScriptKey, "script", exposure.CapabilityAddress, "already_disabled", "Script capability is already disabled."))
			continue
		}
		status := "disabled"
		if input.DryRun {
			status = "would_disable"
		} else if err := s.disableProjectScriptCapability(ctx, req, exposure); err != nil {
			return result, err
		}
		result.Changed = true
		result.Actions = append(result.Actions, deactivationAction(exposure.ScriptKey, "script", exposure.CapabilityAddress, status, "Disable project script capability endpoint and runtime binding."))
	}
	if len(keys) == 0 {
		result.Actions = append(result.Actions, deactivationAction("scripts", "script", "", "skipped", "No project script exposures are registered."))
		return result, nil
	}
	if !input.DryRun {
		if err := s.Projects.MarkProjectScriptExposuresDeactivated(ctx, req, detail.Project.Project.ProjectID, keys, metadata); err != nil {
			return result, err
		}
		if err := s.Projects.MarkProjectFacetDeactivated(ctx, req, detail.Project.Project.ProjectID, detail.Registration.ProjectContractRegistrationID, "scripts", metadata); err != nil {
			return result, err
		}
		refreshed, err := s.Projects.GetProjectRegistrationStatus(ctx, detail.Project.Project.ProjectID)
		if err != nil {
			return result, err
		}
		result.Detail = refreshed
	}
	return result, nil
}

func (s Service) deactivateSchedules(ctx context.Context, req requestctx.Context, detail projects.ProjectRegistrationDetail, input projects.DeactivateProjectInput, result projects.ProjectDeactivationResult, metadata json.RawMessage) (projects.ProjectDeactivationResult, error) {
	keys := []string{}
	for _, registration := range detail.ScheduleRegistrations {
		keys = append(keys, registration.ScheduleKey)
		if registration.ActivationStatus == projects.ProjectScheduleRegistrationStatusDisabled {
			result.Actions = append(result.Actions, deactivationAction(registration.ScheduleKey, "schedule", registration.BackendScheduleKey, "already_disabled", "Schedule registration is already disabled."))
			continue
		}
		status := "disabled"
		if input.DryRun {
			status = "would_disable"
		} else if registration.ScheduleID != nil && strings.TrimSpace(*registration.ScheduleID) != "" {
			if _, err := s.Automation.DisableSchedule(ctx, req, *registration.ScheduleID, automation.UpdateScheduleStatusInput{Reason: input.Reason, Metadata: metadata}); err != nil {
				return result, err
			}
		}
		result.Changed = true
		result.Actions = append(result.Actions, deactivationAction(registration.ScheduleKey, "schedule", registration.BackendScheduleKey, status, "Disable project schedule and linked automation."))
	}
	if len(keys) == 0 {
		result.Actions = append(result.Actions, deactivationAction("schedules", "schedule", "", "skipped", "No project schedules are registered."))
		return result, nil
	}
	if !input.DryRun {
		if err := s.Projects.MarkProjectScheduleRegistrationsDeactivated(ctx, req, detail.Project.Project.ProjectID, keys, metadata); err != nil {
			return result, err
		}
		if err := s.Projects.MarkProjectFacetDeactivated(ctx, req, detail.Project.Project.ProjectID, detail.Registration.ProjectContractRegistrationID, "schedules", metadata); err != nil {
			return result, err
		}
		refreshed, err := s.Projects.GetProjectRegistrationStatus(ctx, detail.Project.Project.ProjectID)
		if err != nil {
			return result, err
		}
		result.Detail = refreshed
	}
	return result, nil
}

func (s Service) deactivateDirectEvents(ctx context.Context, req requestctx.Context, detail projects.ProjectRegistrationDetail, input projects.DeactivateProjectInput, result projects.ProjectDeactivationResult, metadata json.RawMessage) (projects.ProjectDeactivationResult, error) {
	keys := []string{}
	for _, registration := range detail.DirectEventRegistrations {
		keys = append(keys, registration.EventKey)
		if registration.ActivationStatus == projects.ProjectDirectEventRegistrationStatusDisabled {
			result.Actions = append(result.Actions, deactivationAction(registration.EventKey, "direct_event", registration.BackendEndpointSlug, "already_disabled", "Direct event registration is already disabled."))
			continue
		}
		status := "disabled"
		if input.DryRun {
			status = "would_disable"
		} else if registration.EndpointID != nil && strings.TrimSpace(*registration.EndpointID) != "" {
			if _, err := s.Automation.DisableDirectEventEndpoint(ctx, req, *registration.EndpointID, automation.UpdateDirectEventEndpointStatusInput{Reason: input.Reason, Metadata: metadata}); err != nil {
				return result, err
			}
		}
		result.Changed = true
		result.Actions = append(result.Actions, deactivationAction(registration.EventKey, "direct_event", registration.BackendEndpointSlug, status, "Disable project direct event endpoint and linked automation."))
	}
	if len(keys) == 0 {
		result.Actions = append(result.Actions, deactivationAction("direct_events", "direct_event", "", "skipped", "No project direct events are registered."))
		return result, nil
	}
	if !input.DryRun {
		if err := s.Projects.MarkProjectDirectEventRegistrationsDeactivated(ctx, req, detail.Project.Project.ProjectID, keys, metadata); err != nil {
			return result, err
		}
		if err := s.Projects.MarkProjectFacetDeactivated(ctx, req, detail.Project.Project.ProjectID, detail.Registration.ProjectContractRegistrationID, "direct_events", metadata); err != nil {
			return result, err
		}
		refreshed, err := s.Projects.GetProjectRegistrationStatus(ctx, detail.Project.Project.ProjectID)
		if err != nil {
			return result, err
		}
		result.Detail = refreshed
	}
	return result, nil
}

func (s Service) deactivateConnectors(ctx context.Context, req requestctx.Context, detail projects.ProjectRegistrationDetail, input projects.DeactivateProjectInput, result projects.ProjectDeactivationResult, metadata json.RawMessage) (projects.ProjectDeactivationResult, error) {
	keys := []string{}
	for _, registration := range detail.ConnectorRegistrations {
		keys = append(keys, registration.ConnectorKey)
		if registration.ActivationStatus == projects.ProjectConnectorRegistrationStatusDisabled {
			result.Actions = append(result.Actions, deactivationAction(registration.ConnectorKey, "connector", registration.ProviderAddress, "already_disabled", "Connector registration is already disabled."))
			continue
		}
		status := "disabled"
		if input.DryRun {
			status = "would_disable"
		} else if err := s.disableProjectConnectorCapabilities(ctx, req, registration); err != nil {
			return result, err
		}
		result.Changed = true
		result.Actions = append(result.Actions, deactivationAction(registration.ConnectorKey, "connector", registration.ProviderAddress, status, "Disable project-owned connector capability endpoints and runtime bindings."))
	}
	if len(keys) == 0 {
		result.Actions = append(result.Actions, deactivationAction("connectors", "connector", "", "skipped", "No project connectors are registered."))
		return result, nil
	}
	if !input.DryRun {
		if err := s.Projects.MarkProjectConnectorRegistrationsDeactivated(ctx, req, detail.Project.Project.ProjectID, keys, metadata); err != nil {
			return result, err
		}
		if err := s.Projects.MarkProjectFacetDeactivated(ctx, req, detail.Project.Project.ProjectID, detail.Registration.ProjectContractRegistrationID, "connectors", metadata); err != nil {
			return result, err
		}
		refreshed, err := s.Projects.GetProjectRegistrationStatus(ctx, detail.Project.Project.ProjectID)
		if err != nil {
			return result, err
		}
		result.Detail = refreshed
	}
	return result, nil
}

func (s Service) deactivateModules(ctx context.Context, req requestctx.Context, detail projects.ProjectRegistrationDetail, input projects.DeactivateProjectInput, result projects.ProjectDeactivationResult, metadata json.RawMessage) (projects.ProjectDeactivationResult, error) {
	keys := []string{}
	for _, registration := range detail.ModuleRegistrations {
		keys = append(keys, registration.ModuleKey)
		if registration.ActivationStatus == projects.ProjectModuleRegistrationStatusDisabled {
			result.Actions = append(result.Actions, deactivationAction(registration.ModuleKey, "module", registration.ModuleID, "already_disabled", "Module registration is already disabled."))
			continue
		}
		status := "disabled"
		if input.DryRun {
			status = "would_disable"
		}
		result.Changed = true
		result.Actions = append(result.Actions, deactivationAction(registration.ModuleKey, "module", registration.ModuleID, status, "Mark project module registration disabled; module packages are preserved."))
	}
	if len(keys) == 0 {
		result.Actions = append(result.Actions, deactivationAction("modules", "module", "", "skipped", "No project modules are registered."))
		return result, nil
	}
	if !input.DryRun {
		if err := s.Projects.MarkProjectModuleRegistrationsDeactivated(ctx, req, detail.Project.Project.ProjectID, keys, metadata); err != nil {
			return result, err
		}
		if err := s.Projects.MarkProjectFacetDeactivated(ctx, req, detail.Project.Project.ProjectID, detail.Registration.ProjectContractRegistrationID, "modules", metadata); err != nil {
			return result, err
		}
		refreshed, err := s.Projects.GetProjectRegistrationStatus(ctx, detail.Project.Project.ProjectID)
		if err != nil {
			return result, err
		}
		result.Detail = refreshed
	}
	return result, nil
}

func (s Service) deactivateWatchedRoots(ctx context.Context, req requestctx.Context, detail projects.ProjectRegistrationDetail, input projects.DeactivateProjectInput, result projects.ProjectDeactivationResult, metadata json.RawMessage) (projects.ProjectDeactivationResult, error) {
	keys := []string{}
	ownerFacets := watchedRootOwnerFacets(detail)
	for _, registration := range detail.WatchedRootRegistrations {
		keys = append(keys, registration.LocalRootKey)
		if registration.ActivationStatus == projects.ProjectWatchedRootRegistrationStatusDisabled {
			result.Actions = append(result.Actions, deactivationAction(registration.LocalRootKey, "watched_root", registration.BackendRootKey, "already_disabled", "Watched root registration is already disabled in LOOM."))
			continue
		}
		status := "disabled"
		if input.DryRun {
			status = "would_disable"
		}
		result.Changed = true
		summary := fmt.Sprintf("Main registration is disabled; on the owner node run loom-node-agent watched-roots disable %s --reason \"project archived\" --yes to stop its supervisor while preserving evidence.", registration.BackendRootKey)
		result.Actions = append(result.Actions, deactivationAction(registration.LocalRootKey, "watched_root", registration.BackendRootKey, status, summary))
	}
	for _, facet := range ownerFacets {
		if facet.disabled {
			result.Actions = append(result.Actions, deactivationAction(facet.key, "project_facet", facet.key, "already_disabled", "Watched-root owner facet is already disabled."))
			continue
		}
		status := "disabled"
		if input.DryRun {
			status = "would_disable"
		}
		result.Changed = true
		result.Actions = append(result.Actions, deactivationAction(facet.key, "project_facet", facet.key, status, "Mark watched-root owner facet disabled."))
	}
	if len(keys) == 0 {
		result.Actions = append(result.Actions, deactivationAction("watched_roots", "watched_root", "", "skipped", "No project watched roots are registered."))
		return result, nil
	}
	if !input.DryRun {
		if err := s.Projects.MarkProjectWatchedRootsDeactivated(ctx, req, detail.Project.Project.ProjectID, keys, metadata); err != nil {
			return result, err
		}
		for _, facet := range ownerFacets {
			if facet.disabled {
				continue
			}
			if err := s.Projects.MarkProjectFacetDeactivated(ctx, req, detail.Project.Project.ProjectID, detail.Registration.ProjectContractRegistrationID, facet.key, metadata); err != nil {
				return result, err
			}
		}
		refreshed, err := s.Projects.GetProjectRegistrationStatus(ctx, detail.Project.Project.ProjectID)
		if err != nil {
			return result, err
		}
		result.Detail = refreshed
	}
	return result, nil
}

func (s Service) deactivateWorkflows(ctx context.Context, req requestctx.Context, detail projects.ProjectRegistrationDetail, input projects.DeactivateProjectInput, result projects.ProjectDeactivationResult, metadata json.RawMessage) (projects.ProjectDeactivationResult, error) {
	keys := []string{}
	for _, registration := range detail.WorkflowRegistrations {
		keys = append(keys, registration.WorkflowKey)
		if registration.ActivationStatus == projects.ProjectWorkflowRegistrationStatusDisabled {
			result.Actions = append(result.Actions, deactivationAction(registration.WorkflowKey, "workflow", registration.CapabilityAddress, "already_disabled", "Workflow capability is already disabled."))
			continue
		}
		status := "disabled"
		if input.DryRun {
			status = "would_disable"
		} else if err := s.disableProjectWorkflowCapability(ctx, req, registration); err != nil {
			return result, err
		}
		result.Changed = true
		result.Actions = append(result.Actions, deactivationAction(registration.WorkflowKey, "workflow", registration.CapabilityAddress, status, "Disable project workflow capability endpoint and runtime binding."))
	}
	if len(keys) == 0 {
		result.Actions = append(result.Actions, deactivationAction("workflows", "workflow", "", "skipped", "No project workflows are registered."))
		return result, nil
	}
	if !input.DryRun {
		if err := s.Projects.MarkProjectWorkflowRegistrationsDeactivated(ctx, req, detail.Project.Project.ProjectID, keys, metadata); err != nil {
			return result, err
		}
		if err := s.Projects.MarkProjectFacetDeactivated(ctx, req, detail.Project.Project.ProjectID, detail.Registration.ProjectContractRegistrationID, "workflows", metadata); err != nil {
			return result, err
		}
		refreshed, err := s.Projects.GetProjectRegistrationStatus(ctx, detail.Project.Project.ProjectID)
		if err != nil {
			return result, err
		}
		result.Detail = refreshed
	}
	return result, nil
}

func (s Service) ActivateWorkflows(ctx context.Context, req requestctx.Context, ref string, input projects.ActivateProjectInput) (projects.ProjectRegistrationDetail, error) {
	release, err := s.acquireProjectActivationLock(ctx, ref, "workflows")
	if err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	defer func() { _ = release() }()
	detail, err := s.Projects.GetProjectRegistrationStatus(ctx, ref)
	if err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	if err := ensureProjectActivationMutable(detail, "workflows"); err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	if detail.Registration == nil {
		return projects.ProjectRegistrationDetail{}, ErrNoRegisteredContract
	}
	if !workflowsFacetActivatable(detail.Facets) {
		return projects.ProjectRegistrationDetail{}, ErrWorkflowsFacetInactive
	}
	if detail.Project.Project.HomeNodeID == nil || strings.TrimSpace(*detail.Project.Project.HomeNodeID) != strings.TrimSpace(req.OriginNodeID) {
		return projects.ProjectRegistrationDetail{}, ErrRemoteProjectWorkflowsUnsupported
	}

	projectRoot, analysis, err := loadProjectActivationAnalysis(detail, input.ProjectRoot)
	if err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}

	if detail.Registration.ActivationStatus != projects.ProjectActivationStatusBaseActive {
		if _, err := s.Projects.ActivateProjectBase(ctx, req, detail.Project.Project.ProjectID); err != nil {
			return projects.ProjectRegistrationDetail{}, err
		}
	}

	if len(analysis.Plan.Workflows) == 0 {
		return projects.ProjectRegistrationDetail{}, fmt.Errorf("%w: no workflow contracts discovered", ErrWorkflowRuntimeUnsupported)
	}
	activeScripts := activeProjectScriptExposuresByAddress(detail.ScriptExposures)
	existingRegistrations := map[string]projects.ProjectWorkflowRegistration{}
	for _, registration := range detail.WorkflowRegistrations {
		existingRegistrations[registration.WorkflowKey] = registration
	}

	workflowIDs := []string{}
	capabilityAddresses := []string{}
	seenWorkflows := map[string]bool{}
	executableWorkflowCount := 0
	scriptShimCount := 0
	placeholderCount := 0
	blockedCount := 0
	for _, workflow := range analysis.Plan.Workflows {
		if workflow.ManifestPath == "" {
			continue
		}
		if workflow.ContractStatus == "disabled" {
			placeholderCount++
			continue
		}
		workflowKey := defaultString(workflow.Key, workflow.WorkflowID)
		seenWorkflows[workflowKey] = true
		workflowIDs = append(workflowIDs, workflow.WorkflowID)
		switch workflow.ImplementationKind {
		case projectcontracts.WorkflowImplementationWorkflow:
			if workflow.ContractStatus != "active" || !workflow.Executable {
				placeholderCount++
				continue
			}
			if s.Workflows == nil {
				return projects.ProjectRegistrationDetail{}, fmt.Errorf("%w: workflow service is required", ErrWorkflowRuntimeUnsupported)
			}
			if !workflow.ExposeEnabled || strings.TrimSpace(workflow.CapabilityAddress) == "" {
				blockedCount++
				continue
			}
			credentialBindings, err := resolveProjectCredentialBindings(projectRoot, analysis, workflow.Credentials)
			if err != nil {
				return projects.ProjectRegistrationDetail{}, fmt.Errorf("workflow %s credentials: %w", workflow.WorkflowID, err)
			}
			capabilityAddress, err := s.activateExecutableWorkflow(ctx, req, detail, workflow, credentialBindings)
			if err != nil {
				return projects.ProjectRegistrationDetail{}, err
			}
			executableWorkflowCount++
			capabilityAddresses = append(capabilityAddresses, capabilityAddress)
		case projectcontracts.WorkflowImplementationScript:
			if !workflow.ExposeEnabled || strings.TrimSpace(workflow.CapabilityAddress) == "" {
				blockedCount++
				continue
			}
			scriptCapability := strings.TrimSpace(workflow.ScriptCapabilityAddress)
			if scriptCapability == "" {
				scriptCapability = strings.TrimSpace(workflow.CapabilityAddress)
			}
			exposure, ok := activeScripts[scriptCapability]
			if !ok || exposure.ScriptID == nil || strings.TrimSpace(*exposure.ScriptID) == "" {
				return projects.ProjectRegistrationDetail{}, fmt.Errorf("%w: workflow %s requires active script capability %s", ErrWorkflowScriptShimUnavailable, workflow.WorkflowID, scriptCapability)
			}
			credentialBindings, err := resolveProjectCredentialBindings(projectRoot, analysis, workflow.Credentials)
			if err != nil {
				return projects.ProjectRegistrationDetail{}, fmt.Errorf("workflow %s credentials: %w", workflow.WorkflowID, err)
			}
			capabilityAddress, err := s.activateScriptBackedWorkflow(ctx, req, detail, workflow, exposure, credentialBindings)
			if err != nil {
				return projects.ProjectRegistrationDetail{}, err
			}
			scriptShimCount++
			capabilityAddresses = append(capabilityAddresses, capabilityAddress)
		default:
			placeholderCount++
		}
	}
	for _, previous := range existingRegistrations {
		if seenWorkflows[previous.WorkflowKey] {
			continue
		}
		if _, err := s.Projects.UpsertProjectWorkflowRegistration(ctx, req, projects.UpsertProjectWorkflowRegistrationInput{
			ProjectContractRegistrationID: detail.Registration.ProjectContractRegistrationID,
			ProjectID:                     detail.Project.Project.ProjectID,
			WorkflowKey:                   previous.WorkflowKey,
			WorkflowFolder:                previous.WorkflowFolder,
			WorkflowManifestPath:          previous.WorkflowManifestPath,
			WorkflowManifestHash:          previous.WorkflowManifestHash,
			ImplementationKind:            previous.ImplementationKind,
			RuntimeKind:                   previous.RuntimeKind,
			WorkflowID:                    ptrStringValue(previous.WorkflowID),
			WorkflowVersionID:             ptrStringValue(previous.WorkflowVersionID),
			ProviderID:                    ptrStringValue(previous.ProviderID),
			ProviderAddress:               previous.ProviderAddress,
			CapabilityEndpointID:          ptrStringValue(previous.CapabilityEndpointID),
			CapabilityEndpointVersionID:   ptrStringValue(previous.CapabilityEndpointVersionID),
			RuntimeBindingID:              ptrStringValue(previous.RuntimeBindingID),
			CapabilityAddress:             previous.CapabilityAddress,
			ActivationStatus:              projects.ProjectWorkflowRegistrationStatusStale,
			Metadata:                      mustJSONObject(map[string]any{"source": "project.activate", "reason": "workflow_not_found"}),
		}); err != nil {
			return projects.ProjectRegistrationDetail{}, err
		}
	}
	if len(capabilityAddresses) == 0 {
		return projects.ProjectRegistrationDetail{}, fmt.Errorf("%w: no executable or script-backed workflow endpoints were registered", ErrWorkflowRuntimeUnsupported)
	}

	now := s.now()
	if err := s.Projects.MarkProjectFacetActivated(ctx, req, detail.Project.Project.ProjectID, detail.Registration.ProjectContractRegistrationID, "workflows", mustJSONObject(map[string]any{
		"activated_at":              now.Format(time.RFC3339),
		"activated_by_actor_id":     req.ActorID,
		"workflow_count":            len(analysis.Plan.Workflows),
		"executable_workflow_count": executableWorkflowCount,
		"script_shim_count":         scriptShimCount,
		"placeholder_count":         placeholderCount,
		"blocked_count":             blockedCount,
		"workflow_ids":              workflowIDs,
		"capability_addresses":      capabilityAddresses,
		"source":                    "project.activate",
	})); err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	return s.Projects.GetProjectRegistrationStatus(ctx, detail.Project.Project.ProjectID)
}

func (s Service) activateExecutableWorkflow(ctx context.Context, req requestctx.Context, detail projects.ProjectRegistrationDetail, item projectcontracts.WorkflowFacetItem, credentialBindings []jobs.CredentialBinding) (string, error) {
	workflowSlug := projectcontracts.ProjectWorkflowSlug(detail.Project.Project.Slug, item.Key)
	registerResult, err := s.Workflows.RegisterWorkflow(ctx, req, workflows.RegisterInput{
		ManifestPath: item.ManifestPath,
		ProjectRef:   detail.Project.Project.Slug,
		SlugOverride: workflowSlug,
		Activate:     true,
		Metadata:     mustJSONObject(map[string]any{"source": "project.activate", "project_id": detail.Project.Project.ProjectID, "project_slug": detail.Project.Project.Slug, "workflow_key": item.Key}),
	})
	if err != nil {
		return "", fmt.Errorf("register project workflow %s: %w", item.Key, err)
	}
	runtimeConfig := map[string]any{
		"workflow_ref":         registerResult.Workflow.WorkflowID,
		"project_ref":          detail.Project.Project.Slug,
		"execution_mode":       jobs.ScriptRunWaitForCompletion,
		"wait_timeout_seconds": workflowWaitTimeout(item),
	}
	if len(credentialBindings) > 0 {
		runtimeConfig["credential_bindings"] = credentialBindings
	}
	return s.activateWorkflowEndpoint(ctx, req, detail, item, workflowEndpointRuntime{
		RuntimeKind:        capabilities.RuntimeKindWorkflow,
		RuntimeConfigJSON:  mustJSONObject(runtimeConfig),
		WorkflowID:         registerResult.Workflow.WorkflowID,
		WorkflowVersionID:  registerResult.Version.WorkflowVersionID,
		VersionLabel:       projectcontracts.ScriptVersionLabel(item.Version, item.PackageHash),
		ImplementationHash: item.PackageHash,
		ManifestJSON:       projectWorkflowEndpointManifest(item, registerResult),
		Metadata:           mustJSONObject(map[string]any{"source": "project.activate", "backend_workflow_slug": workflowSlug}),
	})
}

func (s Service) activateScriptBackedWorkflow(ctx context.Context, req requestctx.Context, detail projects.ProjectRegistrationDetail, item projectcontracts.WorkflowFacetItem, exposure projects.ProjectScriptExposure, credentialBindings []jobs.CredentialBinding) (string, error) {
	if exposure.ScriptID == nil || strings.TrimSpace(*exposure.ScriptID) == "" {
		return "", fmt.Errorf("%w: workflow %s script exposure has no script id", ErrWorkflowScriptShimUnavailable, item.WorkflowID)
	}
	runtimeConfig := map[string]any{
		"script_ref":           *exposure.ScriptID,
		"project_ref":          detail.Project.Project.Slug,
		"execution_mode":       jobs.ScriptRunWaitForCompletion,
		"wait_timeout_seconds": workflowWaitTimeout(item),
	}
	if len(credentialBindings) > 0 {
		runtimeConfig["credential_bindings"] = credentialBindings
	}
	return s.activateWorkflowEndpoint(ctx, req, detail, item, workflowEndpointRuntime{
		RuntimeKind:        capabilities.RuntimeKindScript,
		RuntimeConfigJSON:  mustJSONObject(runtimeConfig),
		VersionLabel:       projectcontracts.ScriptVersionLabel(defaultString(item.Version, "0.1.0"), item.ManifestHash),
		ImplementationHash: item.ManifestHash,
		ManifestJSON:       projectWorkflowScriptShimEndpointManifest(item, exposure),
		Metadata:           mustJSONObject(map[string]any{"source": "project.activate", "script_capability_address": exposure.CapabilityAddress}),
	})
}

type workflowEndpointRuntime struct {
	RuntimeKind        string
	RuntimeConfigJSON  json.RawMessage
	WorkflowID         string
	WorkflowVersionID  string
	VersionLabel       string
	ImplementationHash string
	ManifestJSON       json.RawMessage
	Metadata           json.RawMessage
}

func (s Service) activateWorkflowEndpoint(ctx context.Context, req requestctx.Context, detail projects.ProjectRegistrationDetail, item projectcontracts.WorkflowFacetItem, runtime workflowEndpointRuntime) (string, error) {
	now := s.now()
	provider, _, err := s.Capabilities.EnsureProvider(ctx, req, capabilities.RegisterProviderInput{
		ProviderKey:           item.ProviderKey,
		CompactAddress:        item.ProviderAddress,
		DisplayName:           detail.Project.Project.Name + " Workflows",
		Description:           "Project workflow capabilities for " + detail.Project.Project.Name,
		ProviderType:          capabilities.ProviderTypeWorkflowRunner,
		NodeRef:               req.OriginNodeID,
		ScopeRef:              detail.Project.Project.ProjectScopeID,
		Version:               "0.3.1",
		Status:                capabilities.ProviderStatusActive,
		RuntimeProfileJSON:    mustJSONObject(map[string]any{"runtime": "project_workflows", "project_id": detail.Project.Project.ProjectID}),
		DocumentationRefsJSON: json.RawMessage(`[]`),
		Metadata:              mustJSONObject(map[string]any{"source": "project.activate", "project_id": detail.Project.Project.ProjectID, "facet": "workflows"}),
	})
	if err != nil {
		return "", fmt.Errorf("ensure workflow provider for %s: %w", item.Key, err)
	}
	if _, err := s.Capabilities.UpsertProviderHealth(ctx, req, provider.ProviderID, capabilities.ProviderHealthInput{
		HealthStatus:       capabilities.HealthStatusOK,
		AvailabilityStatus: capabilities.AvailabilityStatusAvailable,
		Message:            "Project workflow provider activated.",
		LastCheckedAt:      &now,
		LastOKAt:           &now,
		DetailsJSON:        mustJSONObject(map[string]any{"source": "project.activate", "facet": "workflows"}),
	}); err != nil {
		return "", fmt.Errorf("upsert workflow provider health for %s: %w", item.Key, err)
	}

	class, _, err := s.Capabilities.EnsureCapabilityClass(ctx, req, capabilities.RegisterCapabilityClassInput{
		Namespace:                     item.Capability.ClassNamespace,
		Name:                          item.Capability.ClassName,
		Version:                       "0.3.1",
		DisplayName:                   defaultString(item.Name, item.WorkflowID),
		Description:                   item.Description,
		Form:                          defaultString(item.Capability.Form, capabilities.CapabilityFormJob),
		InputSchemaJSON:               mustJSONObject(item.Capability.InputSchema),
		OutputSchemaJSON:              mustJSONObject(item.Capability.OutputSchema),
		DefaultRiskLevel:              defaultString(item.Capability.RiskLevel, capabilities.RiskLevelMedium),
		DefaultPolicyRequirementsJSON: json.RawMessage(`{}`),
		Status:                        capabilities.CapabilityClassStatusActive,
		Metadata:                      mustJSONObject(map[string]any{"source": "project.activate", "project_id": detail.Project.Project.ProjectID, "workflow_key": item.Key}),
	})
	if err != nil {
		return "", fmt.Errorf("ensure workflow capability class for %s: %w", item.Key, err)
	}

	endpoint, _, err := s.Capabilities.EnsureCapabilityEndpoint(ctx, req, capabilities.RegisterCapabilityEndpointInput{
		ProviderRef:                 provider.ProviderID,
		CapabilityClassRef:          class.CapabilityClassID,
		EndpointName:                item.Endpoint,
		CompactAddress:              item.CapabilityAddress,
		Form:                        defaultString(item.Capability.Form, capabilities.CapabilityFormJob),
		InputSchemaJSON:             mustJSONObject(item.Capability.InputSchema),
		OutputSchemaJSON:            mustJSONObject(item.Capability.OutputSchema),
		RiskLevel:                   defaultString(item.Capability.RiskLevel, capabilities.RiskLevelMedium),
		ExecutionAuthorizationLevel: workflowAuthorizationLevel(item),
		SideEffectsJSON:             mustJSONObject(map[string]any{"effects": item.Capability.SideEffects}),
		PolicyRequirementsJSON:      json.RawMessage(`{}`),
		CredentialRequirementsJSON:  jsonOrEmptyObject(item.CredentialJSON),
		ApprovalRequirementsJSON:    mustJSONObject(map[string]any{"required": item.Capability.RequiresApproval}),
		JobBehaviorJSON:             mustJSONObject(map[string]any{"execution_mode": jobs.ScriptRunWaitForCompletion, "wait_timeout_seconds": workflowWaitTimeout(item)}),
		SessionBehaviorJSON:         json.RawMessage(`{}`),
		StreamBehaviorJSON:          json.RawMessage(`{}`),
		LeaseBehaviorJSON:           json.RawMessage(`{}`),
		Status:                      capabilities.EndpointStatusActive,
		Metadata:                    mustJSONObject(map[string]any{"source": "project.activate", "project_id": detail.Project.Project.ProjectID, "workflow_key": item.Key}),
	})
	if err != nil {
		return "", fmt.Errorf("ensure workflow endpoint for %s: %w", item.Key, err)
	}

	version, _, err := s.Capabilities.EnsureEndpointVersion(ctx, req, capabilities.RegisterEndpointVersionInput{
		CapabilityEndpointRef:       endpoint.CapabilityEndpointID,
		VersionLabel:                runtime.VersionLabel,
		ImplementationHash:          runtime.ImplementationHash,
		ManifestJSON:                runtime.ManifestJSON,
		InputSchemaJSON:             mustJSONObject(item.Capability.InputSchema),
		OutputSchemaJSON:            mustJSONObject(item.Capability.OutputSchema),
		RiskLevel:                   defaultString(item.Capability.RiskLevel, capabilities.RiskLevelMedium),
		ExecutionAuthorizationLevel: workflowAuthorizationLevel(item),
		PolicyRequirementsJSON:      json.RawMessage(`{}`),
		CredentialRequirementsJSON:  jsonOrEmptyObject(item.CredentialJSON),
		ApprovalRequirementsJSON:    mustJSONObject(map[string]any{"required": item.Capability.RequiresApproval}),
		Status:                      capabilities.EndpointVersionStatusActive,
		ApprovedByActorID:           req.ActorID,
		ApprovedAt:                  &now,
		Metadata:                    mustJSONObject(map[string]any{"source": "project.activate", "project_id": detail.Project.Project.ProjectID, "workflow_key": item.Key, "runtime_kind": runtime.RuntimeKind}),
	})
	if err != nil {
		return "", fmt.Errorf("ensure workflow endpoint version for %s: %w", item.Key, err)
	}

	runtimeBinding, err := s.Capabilities.RegisterRuntimeBinding(ctx, req, capabilities.RegisterRuntimeBindingInput{
		EndpointVersionRef: version.CapabilityEndpointVersionID,
		RuntimeKind:        runtime.RuntimeKind,
		RuntimeConfigJSON:  runtime.RuntimeConfigJSON,
		InputMappingJSON:   json.RawMessage(`{}`),
		OutputMappingJSON:  json.RawMessage(`{}`),
		Status:             capabilities.RuntimeBindingStatusActive,
		ApprovedByActorID:  req.ActorID,
		ApprovedAt:         &now,
		Metadata:           mustJSONObject(map[string]any{"source": "project.activate", "project_id": detail.Project.Project.ProjectID, "workflow_key": item.Key, "runtime_kind": runtime.RuntimeKind}),
	})
	if err != nil {
		return "", fmt.Errorf("register workflow runtime binding for %s: %w", item.Key, err)
	}

	usageDocumentIDs := []string{}
	for _, doc := range item.UsageDocuments {
		usageDoc, err := s.ensureWorkflowUsageDocument(ctx, req, detail, item, doc, endpoint.CapabilityEndpointID, endpoint.CompactAddress, now)
		if err != nil {
			return "", fmt.Errorf("ensure workflow usage document %s: %w", doc.Path, err)
		}
		usageDocumentIDs = append(usageDocumentIDs, usageDoc.CapabilityUsageDocumentID)
	}

	if _, err := s.Projects.UpsertProjectWorkflowRegistration(ctx, req, projects.UpsertProjectWorkflowRegistrationInput{
		ProjectContractRegistrationID: detail.Registration.ProjectContractRegistrationID,
		ProjectID:                     detail.Project.Project.ProjectID,
		WorkflowKey:                   defaultString(item.Key, item.WorkflowID),
		WorkflowFolder:                item.Folder,
		WorkflowManifestPath:          item.ManifestPath,
		WorkflowManifestHash:          item.ManifestHash,
		ImplementationKind:            item.ImplementationKind,
		RuntimeKind:                   runtime.RuntimeKind,
		WorkflowID:                    runtime.WorkflowID,
		WorkflowVersionID:             runtime.WorkflowVersionID,
		ProviderID:                    provider.ProviderID,
		ProviderAddress:               provider.CompactAddress,
		CapabilityEndpointID:          endpoint.CapabilityEndpointID,
		CapabilityEndpointVersionID:   version.CapabilityEndpointVersionID,
		RuntimeBindingID:              runtimeBinding.Binding.RuntimeBindingID,
		CapabilityAddress:             endpoint.CompactAddress,
		ActivationStatus:              projects.ProjectWorkflowRegistrationStatusActive,
		Metadata: mergeWorkflowRegistrationMetadata(runtime.Metadata, map[string]any{
			"usage_document_ids": usageDocumentIDs,
			"runtime_kind":       runtime.RuntimeKind,
		}),
	}); err != nil {
		return "", err
	}
	return endpoint.CompactAddress, nil
}

func (s Service) ActivateSchedules(ctx context.Context, req requestctx.Context, ref string, input projects.ActivateProjectInput) (projects.ProjectRegistrationDetail, error) {
	release, err := s.acquireProjectActivationLock(ctx, ref, "schedules")
	if err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	defer func() { _ = release() }()
	detail, err := s.Projects.GetProjectRegistrationStatus(ctx, ref)
	if err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	if err := ensureProjectActivationMutable(detail, "schedules"); err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	if s.Automation == nil {
		return projects.ProjectRegistrationDetail{}, fmt.Errorf("automation service is required")
	}
	if detail.Registration == nil {
		return projects.ProjectRegistrationDetail{}, ErrNoRegisteredContract
	}
	if !schedulesFacetActivatable(detail.Facets) {
		return projects.ProjectRegistrationDetail{}, ErrSchedulesFacetInactive
	}

	_, analysis, err := loadProjectActivationAnalysis(detail, input.ProjectRoot)
	if err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	if err := ensureScheduleTargetsReady(detail, analysis.Report); err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}

	if detail.Registration.ActivationStatus != projects.ProjectActivationStatusBaseActive {
		if _, err := s.Projects.ActivateProjectBase(ctx, req, detail.Project.Project.ProjectID); err != nil {
			return projects.ProjectRegistrationDetail{}, err
		}
	}

	existingRegistrations := map[string]projects.ProjectScheduleRegistration{}
	for _, registration := range detail.ScheduleRegistrations {
		existingRegistrations[registration.ScheduleKey] = registration
	}
	seenSchedules := map[string]bool{}
	backendKeys := []string{}
	pausedCount := 0
	for _, item := range analysis.Plan.Schedules {
		if strings.TrimSpace(item.ManifestPath) == "" {
			continue
		}
		seenSchedules[item.Key] = true
		previous := existingRegistrations[item.Key]
		if item.ContractStatus == "disabled" {
			if previous.ScheduleID != nil && strings.TrimSpace(*previous.ScheduleID) != "" {
				if _, err := s.Automation.DisableSchedule(ctx, req, *previous.ScheduleID, automation.UpdateScheduleStatusInput{
					Reason:   "project schedule contract disabled",
					Metadata: mustJSONObject(map[string]any{"source": "project.activate", "facet": "schedules", "schedule_key": item.Key}),
				}); err != nil {
					return projects.ProjectRegistrationDetail{}, fmt.Errorf("disable project schedule %s: %w", item.Key, err)
				}
			}
			if _, err := s.Projects.UpsertProjectScheduleRegistration(ctx, req, projects.UpsertProjectScheduleRegistrationInput{
				ProjectContractRegistrationID: detail.Registration.ProjectContractRegistrationID,
				ProjectID:                     detail.Project.Project.ProjectID,
				ScheduleKey:                   item.Key,
				BackendScheduleKey:            item.BackendScheduleKey,
				ScheduleFolder:                item.Folder,
				ScheduleManifestPath:          item.ManifestPath,
				ScheduleHash:                  item.ManifestHash,
				InputPath:                     item.InputPath,
				InputHash:                     item.InputHash,
				TargetCapability:              item.TargetCapability,
				AutomationID:                  ptrStringValue(previous.AutomationID),
				ScheduleID:                    ptrStringValue(previous.ScheduleID),
				ActivationStatus:              projects.ProjectScheduleRegistrationStatusDisabled,
				Metadata:                      mustJSONObject(map[string]any{"source": "project.activate", "reason": "schedule_contract_disabled"}),
			}); err != nil {
				return projects.ProjectRegistrationDetail{}, err
			}
			continue
		}

		scheduleDetail, created, err := s.Automation.EnsureSchedule(ctx, req, automation.CreateScheduleInput{
			ScheduleKey:        item.BackendScheduleKey,
			DisplayName:        item.DisplayName,
			Description:        item.Description,
			TargetCapability:   item.TargetCapability,
			InputJSON:          item.InputJSON,
			ScheduleKind:       item.ScheduleKind,
			ScheduleExpr:       item.ScheduleExpr,
			Timezone:           item.Timezone,
			ScopeRef:           detail.Project.Project.ProjectScopeID,
			ProjectRef:         detail.Project.Project.Slug,
			RunAsActorRef:      scheduleRunAsActorRef(item.RunAs, req),
			MisfirePolicy:      item.MisfirePolicy,
			LatenessWindowSecs: item.LatenessWindowSecs,
			ConcurrencyPolicy:  item.ConcurrencyPolicy,
			ApprovalPolicy:     item.ApprovalPolicy,
			TimeoutSeconds:     item.TimeoutSeconds,
			MaxAttempts:        item.MaxAttempts,
			Status:             automation.ScheduleStatusPaused,
			Metadata: mustJSONObject(map[string]any{
				"source":                           "project.activate",
				"project_id":                       detail.Project.Project.ProjectID,
				"project_slug":                     detail.Project.Project.Slug,
				"project_contract_registration_id": detail.Registration.ProjectContractRegistrationID,
				"facet":                            "schedules",
				"schedule_key":                     item.Key,
				"schedule_manifest_path":           item.ManifestPath,
				"schedule_hash":                    item.ManifestHash,
				"input_path":                       item.InputPath,
				"input_hash":                       item.InputHash,
			}),
		})
		if err != nil {
			return projects.ProjectRegistrationDetail{}, fmt.Errorf("%w: ensure project schedule %s: %w", ErrScheduleTargetUnavailable, item.Key, err)
		}
		if _, err := s.Projects.UpsertProjectScheduleRegistration(ctx, req, projects.UpsertProjectScheduleRegistrationInput{
			ProjectContractRegistrationID: detail.Registration.ProjectContractRegistrationID,
			ProjectID:                     detail.Project.Project.ProjectID,
			ScheduleKey:                   item.Key,
			BackendScheduleKey:            item.BackendScheduleKey,
			ScheduleFolder:                item.Folder,
			ScheduleManifestPath:          item.ManifestPath,
			ScheduleHash:                  item.ManifestHash,
			InputPath:                     item.InputPath,
			InputHash:                     item.InputHash,
			TargetCapability:              item.TargetCapability,
			AutomationID:                  scheduleDetail.Automation.AutomationID,
			ScheduleID:                    scheduleDetail.Schedule.ScheduleID,
			ActivationStatus:              projects.ProjectScheduleRegistrationStatusPaused,
			Metadata:                      mustJSONObject(map[string]any{"source": "project.activate", "backend_schedule_key": item.BackendScheduleKey, "created": created}),
		}); err != nil {
			return projects.ProjectRegistrationDetail{}, err
		}
		backendKeys = append(backendKeys, scheduleDetail.Schedule.ScheduleKey)
		if scheduleDetail.Schedule.Status == automation.ScheduleStatusPaused {
			pausedCount++
		}
	}

	for _, previous := range existingRegistrations {
		if seenSchedules[previous.ScheduleKey] {
			continue
		}
		if previous.ScheduleID != nil && strings.TrimSpace(*previous.ScheduleID) != "" {
			if _, err := s.Automation.DisableSchedule(ctx, req, *previous.ScheduleID, automation.UpdateScheduleStatusInput{
				Reason:   "project schedule contract removed",
				Metadata: mustJSONObject(map[string]any{"source": "project.activate", "facet": "schedules", "schedule_key": previous.ScheduleKey}),
			}); err != nil {
				return projects.ProjectRegistrationDetail{}, fmt.Errorf("disable stale project schedule %s: %w", previous.ScheduleKey, err)
			}
		}
		if _, err := s.Projects.UpsertProjectScheduleRegistration(ctx, req, projects.UpsertProjectScheduleRegistrationInput{
			ProjectContractRegistrationID: detail.Registration.ProjectContractRegistrationID,
			ProjectID:                     detail.Project.Project.ProjectID,
			ScheduleKey:                   previous.ScheduleKey,
			BackendScheduleKey:            previous.BackendScheduleKey,
			ScheduleFolder:                previous.ScheduleFolder,
			ScheduleManifestPath:          previous.ScheduleManifestPath,
			ScheduleHash:                  previous.ScheduleHash,
			InputPath:                     previous.InputPath,
			InputHash:                     previous.InputHash,
			TargetCapability:              previous.TargetCapability,
			AutomationID:                  ptrStringValue(previous.AutomationID),
			ScheduleID:                    ptrStringValue(previous.ScheduleID),
			ActivationStatus:              projects.ProjectScheduleRegistrationStatusStale,
			Metadata:                      mustJSONObject(map[string]any{"source": "project.activate", "reason": "schedule_not_found"}),
		}); err != nil {
			return projects.ProjectRegistrationDetail{}, err
		}
	}

	now := s.now()
	if err := s.Projects.MarkProjectFacetActivated(ctx, req, detail.Project.Project.ProjectID, detail.Registration.ProjectContractRegistrationID, "schedules", mustJSONObject(map[string]any{
		"activated_at":          now.Format(time.RFC3339),
		"activated_by_actor_id": req.ActorID,
		"schedule_count":        len(analysis.Plan.Schedules),
		"paused_count":          pausedCount,
		"backend_schedule_keys": backendKeys,
		"source":                "project.activate",
	})); err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	return s.Projects.GetProjectRegistrationStatus(ctx, detail.Project.Project.ProjectID)
}

func (s Service) ActivateDirectEvents(ctx context.Context, req requestctx.Context, ref string, input projects.ActivateProjectInput) (projects.ProjectRegistrationDetail, error) {
	release, err := s.acquireProjectActivationLock(ctx, ref, "direct_events")
	if err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	defer func() { _ = release() }()
	detail, err := s.Projects.GetProjectRegistrationStatus(ctx, ref)
	if err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	if err := ensureProjectActivationMutable(detail, "direct_events"); err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	if s.Automation == nil {
		return projects.ProjectRegistrationDetail{}, fmt.Errorf("automation service is required")
	}
	if detail.Registration == nil {
		return projects.ProjectRegistrationDetail{}, ErrNoRegisteredContract
	}
	if !directEventsFacetActivatable(detail.Facets) {
		return projects.ProjectRegistrationDetail{}, ErrDirectEventsFacetInactive
	}

	_, analysis, err := loadProjectActivationAnalysis(detail, input.ProjectRoot)
	if err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	if err := ensureDirectEventTargetsReady(detail, analysis.Report); err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}

	if detail.Registration.ActivationStatus != projects.ProjectActivationStatusBaseActive {
		if _, err := s.Projects.ActivateProjectBase(ctx, req, detail.Project.Project.ProjectID); err != nil {
			return projects.ProjectRegistrationDetail{}, err
		}
	}

	existingRegistrations := map[string]projects.ProjectDirectEventRegistration{}
	for _, registration := range detail.DirectEventRegistrations {
		existingRegistrations[registration.EventKey] = registration
	}
	seenEvents := map[string]bool{}
	endpointSlugs := []string{}
	pausedCount := 0
	for _, item := range analysis.Plan.DirectEvents {
		if strings.TrimSpace(item.ManifestPath) == "" {
			continue
		}
		seenEvents[item.Key] = true
		previous := existingRegistrations[item.Key]
		if item.ContractStatus == "disabled" {
			if previous.EndpointID != nil && strings.TrimSpace(*previous.EndpointID) != "" {
				if _, err := s.Automation.DisableDirectEventEndpoint(ctx, req, *previous.EndpointID, automation.UpdateDirectEventEndpointStatusInput{
					Reason:   "project direct event contract disabled",
					Metadata: mustJSONObject(map[string]any{"source": "project.activate", "facet": "direct_events", "event_key": item.Key}),
				}); err != nil {
					return projects.ProjectRegistrationDetail{}, fmt.Errorf("disable project direct event %s: %w", item.Key, err)
				}
			}
			if _, err := s.Projects.UpsertProjectDirectEventRegistration(ctx, req, projects.UpsertProjectDirectEventRegistrationInput{
				ProjectContractRegistrationID: detail.Registration.ProjectContractRegistrationID,
				ProjectID:                     detail.Project.Project.ProjectID,
				EventKey:                      item.Key,
				IntegrationKey:                item.IntegrationKey,
				BackendIntegrationKey:         item.BackendIntegrationKey,
				EndpointSlug:                  item.EndpointSlug,
				BackendEndpointSlug:           item.BackendEndpointSlug,
				EndpointPath:                  item.EndpointPath,
				EventFolder:                   item.Folder,
				EventManifestPath:             item.ManifestPath,
				EventHash:                     item.ManifestHash,
				PayloadExamplePath:            item.PayloadExamplePath,
				PayloadExampleHash:            item.PayloadExampleHash,
				ExpectedInputPath:             item.ExpectedInputPath,
				ExpectedInputHash:             item.ExpectedInputHash,
				EventType:                     item.EventType,
				TargetCapability:              item.TargetCapability,
				ResponseMode:                  item.ResponseMode,
				IntegrationID:                 ptrStringValue(previous.IntegrationID),
				AuthProfileID:                 ptrStringValue(previous.AuthProfileID),
				EndpointID:                    ptrStringValue(previous.EndpointID),
				AutomationID:                  ptrStringValue(previous.AutomationID),
				ActivationStatus:              projects.ProjectDirectEventRegistrationStatusDisabled,
				Metadata:                      mustJSONObject(map[string]any{"source": "project.activate", "reason": "direct_event_contract_disabled"}),
			}); err != nil {
				return projects.ProjectRegistrationDetail{}, err
			}
			continue
		}

		integrationDetail, _, err := s.Automation.EnsureIntegration(ctx, req, automation.CreateIntegrationInput{
			IntegrationKey:  item.BackendIntegrationKey,
			DisplayName:     defaultString(item.IntegrationKey, item.BackendIntegrationKey),
			Description:     "Project direct event integration for " + detail.Project.Project.Name,
			MainAuthLevel:   item.IntegrationMainAuthLevel,
			AllowedProjects: mustJSONArray([]string{detail.Project.Project.ProjectID, detail.Project.Project.Slug}),
			Metadata: mustJSONObject(map[string]any{
				"source":                           "project.activate",
				"project_id":                       detail.Project.Project.ProjectID,
				"project_slug":                     detail.Project.Project.Slug,
				"project_contract_registration_id": detail.Registration.ProjectContractRegistrationID,
				"facet":                            "direct_events",
				"event_key":                        item.Key,
				"integration_key":                  item.IntegrationKey,
			}),
		})
		if err != nil {
			return projects.ProjectRegistrationDetail{}, fmt.Errorf("ensure project direct-event integration %s: %w", item.Key, err)
		}

		authProfileRefs, primaryAuthProfileID, err := s.ensureDirectEventAuthProfiles(ctx, req, integrationDetail.Integration.IntegrationID, item, detail)
		if err != nil {
			return projects.ProjectRegistrationDetail{}, err
		}

		endpointDetail, created, err := s.Automation.EnsureDirectEventEndpoint(ctx, req, automation.CreateDirectEventEndpointInput{
			EndpointSlug:         item.BackendEndpointSlug,
			DisplayName:          item.DisplayName,
			Description:          item.Description,
			Status:               automation.DirectEventEndpointStatusPaused,
			IntegrationRef:       integrationDetail.Integration.IntegrationID,
			EventType:            item.EventType,
			TargetCapability:     item.TargetCapability,
			ResponseMode:         item.ResponseMode,
			MappingProfile:       item.MappingProfileJSON,
			IdempotencyProfile:   item.IdempotencyProfileJSON,
			CommunicationProfile: item.CommunicationProfileJSON,
			StorageProfile:       item.StorageProfileJSON,
			TimeoutSeconds:       item.TimeoutSeconds,
			MaxAttempts:          item.MaxAttempts,
			AuthProfileRefs:      mustJSONArray(authProfileRefs),
			ScopeRef:             detail.Project.Project.ProjectScopeKey,
			ProjectRef:           detail.Project.Project.Slug,
			Metadata: mustJSONObject(map[string]any{
				"source":                           "project.activate",
				"project_id":                       detail.Project.Project.ProjectID,
				"project_slug":                     detail.Project.Project.Slug,
				"project_contract_registration_id": detail.Registration.ProjectContractRegistrationID,
				"facet":                            "direct_events",
				"event_key":                        item.Key,
				"event_manifest_path":              item.ManifestPath,
				"event_hash":                       item.ManifestHash,
				"payload_example_path":             item.PayloadExamplePath,
				"payload_example_hash":             item.PayloadExampleHash,
				"expected_input_path":              item.ExpectedInputPath,
				"expected_input_hash":              item.ExpectedInputHash,
			}),
		})
		if err != nil {
			return projects.ProjectRegistrationDetail{}, fmt.Errorf("%w: ensure project direct event %s: %w", ErrDirectEventTargetUnavailable, item.Key, err)
		}
		if _, err := s.Projects.UpsertProjectDirectEventRegistration(ctx, req, projects.UpsertProjectDirectEventRegistrationInput{
			ProjectContractRegistrationID: detail.Registration.ProjectContractRegistrationID,
			ProjectID:                     detail.Project.Project.ProjectID,
			EventKey:                      item.Key,
			IntegrationKey:                item.IntegrationKey,
			BackendIntegrationKey:         item.BackendIntegrationKey,
			EndpointSlug:                  item.EndpointSlug,
			BackendEndpointSlug:           item.BackendEndpointSlug,
			EndpointPath:                  endpointDetail.Endpoint.EndpointPath,
			EventFolder:                   item.Folder,
			EventManifestPath:             item.ManifestPath,
			EventHash:                     item.ManifestHash,
			PayloadExamplePath:            item.PayloadExamplePath,
			PayloadExampleHash:            item.PayloadExampleHash,
			ExpectedInputPath:             item.ExpectedInputPath,
			ExpectedInputHash:             item.ExpectedInputHash,
			EventType:                     item.EventType,
			TargetCapability:              item.TargetCapability,
			ResponseMode:                  item.ResponseMode,
			IntegrationID:                 integrationDetail.Integration.IntegrationID,
			AuthProfileID:                 primaryAuthProfileID,
			EndpointID:                    endpointDetail.Endpoint.EndpointID,
			AutomationID:                  endpointDetail.Automation.AutomationID,
			ActivationStatus:              projects.ProjectDirectEventRegistrationStatusPaused,
			Metadata:                      mustJSONObject(map[string]any{"source": "project.activate", "backend_endpoint_slug": item.BackendEndpointSlug, "created": created}),
		}); err != nil {
			return projects.ProjectRegistrationDetail{}, err
		}
		endpointSlugs = append(endpointSlugs, endpointDetail.Endpoint.EndpointSlug)
		if endpointDetail.Endpoint.Status == automation.DirectEventEndpointStatusPaused {
			pausedCount++
		}
	}

	for _, previous := range existingRegistrations {
		if seenEvents[previous.EventKey] {
			continue
		}
		if previous.EndpointID != nil && strings.TrimSpace(*previous.EndpointID) != "" {
			if _, err := s.Automation.DisableDirectEventEndpoint(ctx, req, *previous.EndpointID, automation.UpdateDirectEventEndpointStatusInput{
				Reason:   "project direct event contract removed",
				Metadata: mustJSONObject(map[string]any{"source": "project.activate", "facet": "direct_events", "event_key": previous.EventKey}),
			}); err != nil {
				return projects.ProjectRegistrationDetail{}, fmt.Errorf("disable stale project direct event %s: %w", previous.EventKey, err)
			}
		}
		if _, err := s.Projects.UpsertProjectDirectEventRegistration(ctx, req, projects.UpsertProjectDirectEventRegistrationInput{
			ProjectContractRegistrationID: detail.Registration.ProjectContractRegistrationID,
			ProjectID:                     detail.Project.Project.ProjectID,
			EventKey:                      previous.EventKey,
			IntegrationKey:                previous.IntegrationKey,
			BackendIntegrationKey:         previous.BackendIntegrationKey,
			EndpointSlug:                  previous.EndpointSlug,
			BackendEndpointSlug:           previous.BackendEndpointSlug,
			EndpointPath:                  previous.EndpointPath,
			EventFolder:                   previous.EventFolder,
			EventManifestPath:             previous.EventManifestPath,
			EventHash:                     previous.EventHash,
			PayloadExamplePath:            previous.PayloadExamplePath,
			PayloadExampleHash:            previous.PayloadExampleHash,
			ExpectedInputPath:             previous.ExpectedInputPath,
			ExpectedInputHash:             previous.ExpectedInputHash,
			EventType:                     previous.EventType,
			TargetCapability:              previous.TargetCapability,
			ResponseMode:                  previous.ResponseMode,
			IntegrationID:                 ptrStringValue(previous.IntegrationID),
			AuthProfileID:                 ptrStringValue(previous.AuthProfileID),
			EndpointID:                    ptrStringValue(previous.EndpointID),
			AutomationID:                  ptrStringValue(previous.AutomationID),
			ActivationStatus:              projects.ProjectDirectEventRegistrationStatusStale,
			Metadata:                      mustJSONObject(map[string]any{"source": "project.activate", "reason": "direct_event_not_found"}),
		}); err != nil {
			return projects.ProjectRegistrationDetail{}, err
		}
	}

	now := s.now()
	if err := s.Projects.MarkProjectFacetActivated(ctx, req, detail.Project.Project.ProjectID, detail.Registration.ProjectContractRegistrationID, "direct_events", mustJSONObject(map[string]any{
		"activated_at":           now.Format(time.RFC3339),
		"activated_by_actor_id":  req.ActorID,
		"direct_event_count":     len(analysis.Plan.DirectEvents),
		"paused_count":           pausedCount,
		"backend_endpoint_slugs": endpointSlugs,
		"source":                 "project.activate",
	})); err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	return s.Projects.GetProjectRegistrationStatus(ctx, detail.Project.Project.ProjectID)
}

func (s Service) ActivateConnectors(ctx context.Context, req requestctx.Context, ref string, input projects.ActivateProjectInput) (projects.ProjectRegistrationDetail, error) {
	release, err := s.acquireProjectActivationLock(ctx, ref, "connectors")
	if err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	defer func() { _ = release() }()
	detail, err := s.Projects.GetProjectRegistrationStatus(ctx, ref)
	if err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	if err := ensureProjectActivationMutable(detail, "connectors"); err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	if s.Scripts == nil {
		return projects.ProjectRegistrationDetail{}, fmt.Errorf("script service is required")
	}
	if s.Capabilities == nil {
		return projects.ProjectRegistrationDetail{}, fmt.Errorf("capability service is required")
	}
	if detail.Registration == nil {
		return projects.ProjectRegistrationDetail{}, ErrNoRegisteredContract
	}
	if !connectorsFacetActivatable(detail.Facets) {
		return projects.ProjectRegistrationDetail{}, ErrConnectorsFacetInactive
	}
	if detail.Project.Project.HomeNodeID == nil || strings.TrimSpace(*detail.Project.Project.HomeNodeID) != strings.TrimSpace(req.OriginNodeID) {
		return projects.ProjectRegistrationDetail{}, ErrConnectorScriptActivationUnsupported
	}

	_, analysis, err := loadProjectActivationAnalysis(detail, input.ProjectRoot)
	if err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}

	if detail.Registration.ActivationStatus != projects.ProjectActivationStatusBaseActive {
		if _, err := s.Projects.ActivateProjectBase(ctx, req, detail.Project.Project.ProjectID); err != nil {
			return projects.ProjectRegistrationDetail{}, err
		}
	}

	existingRegistrations := map[string]projects.ProjectConnectorRegistration{}
	for _, registration := range detail.ConnectorRegistrations {
		existingRegistrations[registration.ConnectorKey] = registration
	}

	seenConnectors := map[string]bool{}
	providerAddresses := []string{}
	capabilityAddresses := []string{}
	now := s.now()
	for _, item := range analysis.Plan.Connectors {
		if strings.TrimSpace(item.ManifestPath) == "" {
			continue
		}
		seenConnectors[item.Key] = true
		previous := existingRegistrations[item.Key]
		if item.ContractStatus == "disabled" {
			if err := s.disableProjectConnectorCapabilities(ctx, req, previous); err != nil {
				return projects.ProjectRegistrationDetail{}, fmt.Errorf("disable previous connector %s: %w", item.Key, err)
			}
			if _, err := s.Projects.UpsertProjectConnectorRegistration(ctx, req, projects.UpsertProjectConnectorRegistrationInput{
				ProjectContractRegistrationID: detail.Registration.ProjectContractRegistrationID,
				ProjectID:                     detail.Project.Project.ProjectID,
				ConnectorKey:                  item.Key,
				ConnectorFolder:               item.Folder,
				ConnectorManifestPath:         item.ManifestPath,
				ConnectorHash:                 item.ManifestHash,
				ProviderKey:                   item.ProviderKey,
				ProviderAddress:               item.ProviderAddress,
				ProviderID:                    ptrStringValue(previous.ProviderID),
				ProviderStatus:                item.ProviderStatus,
				RuntimeKind:                   item.RuntimeKind,
				CapabilityCount:               len(item.Capabilities),
				ActiveCapabilityCount:         0,
				UsageDocumentCount:            len(item.UsageDocuments),
				ActivationStatus:              projects.ProjectConnectorRegistrationStatusDisabled,
				Metadata:                      mustJSONObject(map[string]any{"source": "project.activate", "reason": "connector_contract_disabled"}),
			}); err != nil {
				return projects.ProjectRegistrationDetail{}, err
			}
			continue
		}
		unsupportedRuntime := connectorUnsupportedRuntime(item)
		if unsupportedRuntime != "" {
			blockedRuntime := unsupportedRuntime
			if _, err := s.Projects.UpsertProjectConnectorRegistration(ctx, req, projects.UpsertProjectConnectorRegistrationInput{
				ProjectContractRegistrationID: detail.Registration.ProjectContractRegistrationID,
				ProjectID:                     detail.Project.Project.ProjectID,
				ConnectorKey:                  item.Key,
				ConnectorFolder:               item.Folder,
				ConnectorManifestPath:         item.ManifestPath,
				ConnectorHash:                 item.ManifestHash,
				ProviderKey:                   item.ProviderKey,
				ProviderAddress:               item.ProviderAddress,
				ProviderID:                    ptrStringValue(previous.ProviderID),
				ProviderStatus:                item.ProviderStatus,
				RuntimeKind:                   blockedRuntime,
				CapabilityCount:               len(item.Capabilities),
				ActiveCapabilityCount:         0,
				UsageDocumentCount:            len(item.UsageDocuments),
				ActivationStatus:              projects.ProjectConnectorRegistrationStatusBlocked,
				Metadata:                      mustJSONObject(map[string]any{"source": "project.activate", "reason": "unsupported_connector_runtime", "runtime_kind": blockedRuntime}),
			}); err != nil {
				return projects.ProjectRegistrationDetail{}, err
			}
			return projects.ProjectRegistrationDetail{}, fmt.Errorf("%w: connector %s uses runtime %s", ErrConnectorRuntimeUnsupported, item.Key, blockedRuntime)
		}

		provider, _, err := s.Capabilities.EnsureProvider(ctx, req, capabilities.RegisterProviderInput{
			ProviderKey:           item.ProviderKey,
			CompactAddress:        item.ProviderAddress,
			DisplayName:           item.DisplayName,
			Description:           item.Description,
			ProviderType:          capabilities.ProviderTypeConnector,
			NodeRef:               req.OriginNodeID,
			ScopeRef:              detail.Project.Project.ProjectScopeID,
			Version:               item.Version,
			Status:                capabilities.ProviderStatusActive,
			RuntimeProfileJSON:    mustJSONObject(map[string]any{"runtime": "project_connector", "project_id": detail.Project.Project.ProjectID, "project_slug": detail.Project.Project.Slug, "connector_key": item.Key, "runtime_kind": item.RuntimeKind}),
			DocumentationRefsJSON: json.RawMessage(`[]`),
			Metadata:              mustJSONObject(map[string]any{"source": "project.activate", "project_id": detail.Project.Project.ProjectID, "facet": "connectors", "connector_key": item.Key}),
		})
		if err != nil {
			return projects.ProjectRegistrationDetail{}, fmt.Errorf("ensure connector provider %s: %w", item.ProviderKey, err)
		}
		if _, err := s.Capabilities.UpsertProviderHealth(ctx, req, provider.ProviderID, capabilities.ProviderHealthInput{
			HealthStatus:       capabilities.HealthStatusOK,
			AvailabilityStatus: capabilities.AvailabilityStatusAvailable,
			Message:            "Project connector provider activated.",
			LastCheckedAt:      &now,
			LastOKAt:           &now,
			DetailsJSON:        mustJSONObject(map[string]any{"source": "project.activate", "facet": "connectors", "connector_key": item.Key}),
		}); err != nil {
			return projects.ProjectRegistrationDetail{}, fmt.Errorf("upsert connector provider health %s: %w", item.ProviderKey, err)
		}

		docIDs := []string{}
		for _, doc := range item.UsageDocuments {
			if doc.Target != "provider" {
				continue
			}
			usageDoc, err := s.ensureConnectorUsageDocument(ctx, req, detail, item, doc, capabilities.UsageTargetKindProvider, provider.ProviderID, provider.CompactAddress, now)
			if err != nil {
				return projects.ProjectRegistrationDetail{}, fmt.Errorf("ensure connector provider usage document %s: %w", doc.Path, err)
			}
			docIDs = append(docIDs, usageDoc.CapabilityUsageDocumentID)
		}

		previousEndpoints := connectorEndpointMetadataByAddress(previous.Metadata)
		seenEndpoints := map[string]bool{}
		endpointMetadata := []projectConnectorEndpointMetadata{}
		activeCapabilityCount := 0
		for _, capability := range item.Capabilities {
			if capability.ContractStatus == "disabled" {
				if previousEndpoint, ok := previousEndpoints[capability.CapabilityAddress]; ok {
					if err := s.disableProjectConnectorEndpoint(ctx, req, previousEndpoint, "connector_capability_disabled"); err != nil {
						return projects.ProjectRegistrationDetail{}, fmt.Errorf("disable connector capability %s: %w", capability.CapabilityAddress, err)
					}
				}
				continue
			}

			var registerResult scripts.RegisterResult
			if capability.RuntimeKind == capabilities.RuntimeKindScript {
				scriptSlug := projectcontracts.ProjectConnectorScriptSlug(detail.Project.Project.Slug, item.ProviderKey, capability.Endpoint)
				var err error
				registerResult, err = s.Scripts.RegisterScript(ctx, req, scripts.RegisterInput{
					ManifestPath: capability.ScriptManifestPath,
					ProjectRef:   detail.Project.Project.Slug,
					SlugOverride: scriptSlug,
					Activate:     true,
					Metadata:     mustJSONObject(map[string]any{"source": "project.activate", "project_id": detail.Project.Project.ProjectID, "project_slug": detail.Project.Project.Slug, "connector_key": item.Key, "provider_key": item.ProviderKey, "endpoint": capability.Endpoint}),
				})
				if err != nil {
					return projects.ProjectRegistrationDetail{}, fmt.Errorf("register connector script %s.%s: %w", item.ProviderKey, capability.Endpoint, err)
				}
			}

			class, _, err := s.Capabilities.EnsureCapabilityClass(ctx, req, capabilities.RegisterCapabilityClassInput{
				Namespace:                     capability.ClassNamespace,
				Name:                          capability.ClassName,
				Version:                       item.Version,
				DisplayName:                   capability.DisplayName,
				Description:                   capability.Description,
				Form:                          capability.Form,
				InputSchemaJSON:               mustJSONObject(capability.InputSchema),
				OutputSchemaJSON:              mustJSONObject(capability.OutputSchema),
				DefaultRiskLevel:              capability.RiskLevel,
				DefaultPolicyRequirementsJSON: mustJSONObject(capability.PolicyRequirements),
				Status:                        capabilities.CapabilityClassStatusActive,
				Metadata:                      mustJSONObject(map[string]any{"source": "project.activate", "project_id": detail.Project.Project.ProjectID, "connector_key": item.Key, "endpoint": capability.Endpoint}),
			})
			if err != nil {
				return projects.ProjectRegistrationDetail{}, fmt.Errorf("ensure connector capability class %s: %w", capability.CapabilityAddress, err)
			}

			endpoint, _, err := s.Capabilities.EnsureCapabilityEndpoint(ctx, req, capabilities.RegisterCapabilityEndpointInput{
				ProviderRef:                 provider.ProviderID,
				CapabilityClassRef:          class.CapabilityClassID,
				EndpointName:                capability.Endpoint,
				CompactAddress:              capability.CapabilityAddress,
				Form:                        capability.Form,
				InputSchemaJSON:             mustJSONObject(capability.InputSchema),
				OutputSchemaJSON:            mustJSONObject(capability.OutputSchema),
				RiskLevel:                   capability.RiskLevel,
				ExecutionAuthorizationLevel: capability.ExecutionAuthorizationLevel,
				SideEffectsJSON:             mustJSONObject(map[string]any{"effects": capability.SideEffects}),
				PolicyRequirementsJSON:      mustJSONObject(capability.PolicyRequirements),
				CredentialRequirementsJSON:  mustJSONObject(capability.CredentialRequirements),
				ApprovalRequirementsJSON:    mustJSONObject(map[string]any{"required": capability.RequiresApproval}),
				JobBehaviorJSON:             mustJSONObject(map[string]any{"execution_mode": capability.ExecutionMode, "wait_timeout_seconds": capability.WaitTimeoutSeconds}),
				SessionBehaviorJSON:         json.RawMessage(`{}`),
				StreamBehaviorJSON:          json.RawMessage(`{}`),
				LeaseBehaviorJSON:           json.RawMessage(`{}`),
				Status:                      capabilities.EndpointStatusActive,
				Metadata:                    mustJSONObject(map[string]any{"source": "project.activate", "project_id": detail.Project.Project.ProjectID, "connector_key": item.Key, "endpoint": capability.Endpoint}),
			})
			if err != nil {
				return projects.ProjectRegistrationDetail{}, fmt.Errorf("ensure connector capability endpoint %s: %w", capability.CapabilityAddress, err)
			}

			version, _, err := s.Capabilities.EnsureEndpointVersion(ctx, req, capabilities.RegisterEndpointVersionInput{
				CapabilityEndpointRef:       endpoint.CapabilityEndpointID,
				VersionLabel:                projectcontracts.ScriptVersionLabel(item.Version, capability.ImplementationHash),
				ImplementationHash:          capability.ImplementationHash,
				ManifestJSON:                projectConnectorEndpointManifest(item, capability, registerResult),
				InputSchemaJSON:             mustJSONObject(capability.InputSchema),
				OutputSchemaJSON:            mustJSONObject(capability.OutputSchema),
				RiskLevel:                   capability.RiskLevel,
				ExecutionAuthorizationLevel: capability.ExecutionAuthorizationLevel,
				PolicyRequirementsJSON:      mustJSONObject(capability.PolicyRequirements),
				CredentialRequirementsJSON:  mustJSONObject(capability.CredentialRequirements),
				ApprovalRequirementsJSON:    mustJSONObject(map[string]any{"required": capability.RequiresApproval}),
				Status:                      capabilities.EndpointVersionStatusActive,
				ApprovedByActorID:           req.ActorID,
				ApprovedAt:                  &now,
				Metadata:                    mustJSONObject(map[string]any{"source": "project.activate", "project_id": detail.Project.Project.ProjectID, "connector_key": item.Key, "endpoint": capability.Endpoint}),
			})
			if err != nil {
				return projects.ProjectRegistrationDetail{}, fmt.Errorf("ensure connector endpoint version %s: %w", capability.CapabilityAddress, err)
			}

			runtimeKind := capability.RuntimeKind
			runtimeConfig := capability.RuntimeConfigJSON
			if runtimeKind == capabilities.RuntimeKindScript {
				runtimeConfig = mustJSONObject(map[string]any{
					"script_ref":           registerResult.Script.ScriptID,
					"project_ref":          detail.Project.Project.Slug,
					"execution_mode":       capability.ExecutionMode,
					"wait_timeout_seconds": capability.WaitTimeoutSeconds,
				})
			}
			runtimeBinding, err := s.Capabilities.RegisterRuntimeBinding(ctx, req, capabilities.RegisterRuntimeBindingInput{
				EndpointVersionRef: version.CapabilityEndpointVersionID,
				RuntimeKind:        runtimeKind,
				RuntimeConfigJSON:  runtimeConfig,
				InputMappingJSON:   json.RawMessage(`{}`),
				OutputMappingJSON:  json.RawMessage(`{}`),
				Status:             capabilities.RuntimeBindingStatusActive,
				ApprovedByActorID:  req.ActorID,
				ApprovedAt:         &now,
				Metadata:           mustJSONObject(map[string]any{"source": "project.activate", "project_id": detail.Project.Project.ProjectID, "connector_key": item.Key, "endpoint": capability.Endpoint, "runtime_kind": runtimeKind}),
			})
			if err != nil {
				return projects.ProjectRegistrationDetail{}, fmt.Errorf("register connector runtime binding %s: %w", capability.CapabilityAddress, err)
			}

			for _, doc := range item.UsageDocuments {
				if !connectorUsageDocumentTargetsEndpoint(doc, capability.Endpoint) {
					continue
				}
				usageDoc, err := s.ensureConnectorUsageDocument(ctx, req, detail, item, doc, capabilities.UsageTargetKindCapabilityEndpoint, endpoint.CapabilityEndpointID, endpoint.CompactAddress, now)
				if err != nil {
					return projects.ProjectRegistrationDetail{}, fmt.Errorf("ensure connector endpoint usage document %s: %w", doc.Path, err)
				}
				docIDs = append(docIDs, usageDoc.CapabilityUsageDocumentID)
			}

			meta := projectConnectorEndpointMetadata{
				Endpoint:                    capability.Endpoint,
				CapabilityAddress:           endpoint.CompactAddress,
				ScriptID:                    registerResult.Script.ScriptID,
				ScriptVersionID:             registerResult.Version.ScriptVersionID,
				CapabilityEndpointID:        endpoint.CapabilityEndpointID,
				CapabilityEndpointVersionID: version.CapabilityEndpointVersionID,
				RuntimeBindingID:            runtimeBinding.Binding.RuntimeBindingID,
				ImplementationHash:          capability.ImplementationHash,
				RuntimeKind:                 runtimeKind,
			}
			endpointMetadata = append(endpointMetadata, meta)
			seenEndpoints[endpoint.CompactAddress] = true
			activeCapabilityCount++
			capabilityAddresses = append(capabilityAddresses, endpoint.CompactAddress)
		}

		for address, previousEndpoint := range previousEndpoints {
			if seenEndpoints[address] {
				continue
			}
			if err := s.disableProjectConnectorEndpoint(ctx, req, previousEndpoint, "connector_capability_not_found"); err != nil {
				return projects.ProjectRegistrationDetail{}, fmt.Errorf("disable stale connector capability %s: %w", address, err)
			}
		}

		if _, err := s.Projects.UpsertProjectConnectorRegistration(ctx, req, projects.UpsertProjectConnectorRegistrationInput{
			ProjectContractRegistrationID: detail.Registration.ProjectContractRegistrationID,
			ProjectID:                     detail.Project.Project.ProjectID,
			ConnectorKey:                  item.Key,
			ConnectorFolder:               item.Folder,
			ConnectorManifestPath:         item.ManifestPath,
			ConnectorHash:                 item.ManifestHash,
			ProviderKey:                   item.ProviderKey,
			ProviderAddress:               provider.CompactAddress,
			ProviderID:                    provider.ProviderID,
			ProviderStatus:                provider.Status,
			RuntimeKind:                   item.RuntimeKind,
			CapabilityCount:               len(item.Capabilities),
			ActiveCapabilityCount:         activeCapabilityCount,
			UsageDocumentCount:            len(docIDs),
			ActivationStatus:              projects.ProjectConnectorRegistrationStatusActive,
			Metadata: mustJSONObject(projectConnectorRegistrationMetadata{
				Source:             "project.activate",
				ProviderID:         provider.ProviderID,
				ProviderAddress:    provider.CompactAddress,
				Endpoints:          endpointMetadata,
				UsageDocumentIDs:   docIDs,
				ActivatedAt:        now.Format(time.RFC3339),
				ActivatedByActorID: req.ActorID,
			}),
		}); err != nil {
			return projects.ProjectRegistrationDetail{}, err
		}
		providerAddresses = append(providerAddresses, provider.CompactAddress)
	}

	for _, previous := range existingRegistrations {
		if seenConnectors[previous.ConnectorKey] {
			continue
		}
		if err := s.disableProjectConnectorCapabilities(ctx, req, previous); err != nil {
			return projects.ProjectRegistrationDetail{}, fmt.Errorf("disable stale connector %s: %w", previous.ConnectorKey, err)
		}
		if _, err := s.Projects.UpsertProjectConnectorRegistration(ctx, req, projects.UpsertProjectConnectorRegistrationInput{
			ProjectContractRegistrationID: detail.Registration.ProjectContractRegistrationID,
			ProjectID:                     detail.Project.Project.ProjectID,
			ConnectorKey:                  previous.ConnectorKey,
			ConnectorFolder:               previous.ConnectorFolder,
			ConnectorManifestPath:         previous.ConnectorManifestPath,
			ConnectorHash:                 previous.ConnectorHash,
			ProviderKey:                   previous.ProviderKey,
			ProviderAddress:               previous.ProviderAddress,
			ProviderID:                    ptrStringValue(previous.ProviderID),
			ProviderStatus:                previous.ProviderStatus,
			RuntimeKind:                   previous.RuntimeKind,
			CapabilityCount:               previous.CapabilityCount,
			ActiveCapabilityCount:         previous.ActiveCapabilityCount,
			UsageDocumentCount:            previous.UsageDocumentCount,
			ActivationStatus:              projects.ProjectConnectorRegistrationStatusStale,
			Metadata:                      mustJSONObject(map[string]any{"source": "project.activate", "reason": "connector_not_found"}),
		}); err != nil {
			return projects.ProjectRegistrationDetail{}, err
		}
	}

	if err := s.Projects.MarkProjectFacetActivated(ctx, req, detail.Project.Project.ProjectID, detail.Registration.ProjectContractRegistrationID, "connectors", mustJSONObject(map[string]any{
		"activated_at":          now.Format(time.RFC3339),
		"activated_by_actor_id": req.ActorID,
		"connector_count":       len(analysis.Plan.Connectors),
		"provider_addresses":    providerAddresses,
		"capability_addresses":  capabilityAddresses,
		"source":                "project.activate",
	})); err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	return s.Projects.GetProjectRegistrationStatus(ctx, detail.Project.Project.ProjectID)
}

func (s Service) ActivateModules(ctx context.Context, req requestctx.Context, ref string, input projects.ActivateProjectInput) (projects.ProjectRegistrationDetail, error) {
	release, err := s.acquireProjectActivationLock(ctx, ref, "modules")
	if err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	defer func() { _ = release() }()
	detail, err := s.Projects.GetProjectRegistrationStatus(ctx, ref)
	if err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	if err := ensureProjectActivationMutable(detail, "modules"); err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	if s.Modules == nil {
		return projects.ProjectRegistrationDetail{}, fmt.Errorf("module service is required")
	}
	if detail.Registration == nil {
		return projects.ProjectRegistrationDetail{}, ErrNoRegisteredContract
	}
	if !modulesFacetActivatable(detail.Facets) {
		return projects.ProjectRegistrationDetail{}, ErrModulesFacetInactive
	}
	if detail.Project.Project.HomeNodeID == nil || strings.TrimSpace(*detail.Project.Project.HomeNodeID) != strings.TrimSpace(req.OriginNodeID) {
		return projects.ProjectRegistrationDetail{}, ErrProjectModuleActivationUnsupported
	}

	projectRoot, analysis, err := loadProjectActivationAnalysis(detail, input.ProjectRoot)
	if err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}

	if detail.Registration.ActivationStatus != projects.ProjectActivationStatusBaseActive {
		if _, err := s.Projects.ActivateProjectBase(ctx, req, detail.Project.Project.ProjectID); err != nil {
			return projects.ProjectRegistrationDetail{}, err
		}
	}

	existingRegistrations := map[string]projects.ProjectModuleRegistration{}
	for _, registration := range detail.ModuleRegistrations {
		existingRegistrations[registration.ModuleKey] = registration
	}
	seenModules := map[string]bool{}
	moduleIDs := []string{}
	moduleVersionIDs := []string{}
	registeredCount := 0
	disabledCount := 0
	blockedCount := 0
	for _, item := range analysis.Plan.Modules {
		if strings.TrimSpace(item.ManifestPath) == "" {
			continue
		}
		seenModules[item.Key] = true
		previous := existingRegistrations[item.Key]
		if !item.RegistrationEnabled {
			if _, err := s.Projects.UpsertProjectModuleRegistration(ctx, req, projects.UpsertProjectModuleRegistrationInput{
				ProjectContractRegistrationID: detail.Registration.ProjectContractRegistrationID,
				ProjectID:                     detail.Project.Project.ProjectID,
				ModuleKey:                     item.Key,
				ModuleFolder:                  item.Folder,
				ModuleManifestPath:            item.ManifestPath,
				ModuleProjectContractPath:     item.ProjectContractPath,
				ModuleManifestHash:            item.ManifestHash,
				ModuleProjectContractHash:     item.ProjectContractHash,
				ModulePackageHash:             item.PackageHash,
				ModuleID:                      item.ModuleID,
				ModuleName:                    item.ModuleName,
				ModuleVersion:                 item.Version,
				ModuleKind:                    item.ModuleKind,
				ModulePackageID:               ptrStringValue(previous.ModulePackageID),
				ModuleVersionID:               ptrStringValue(previous.ModuleVersionID),
				RequirementCount:              item.RequirementCount,
				ObjectTypeCount:               item.ObjectTypeCount,
				ProviderCount:                 item.ProviderCount,
				CapabilityCount:               item.CapabilityCount,
				UsageDocumentCount:            item.UsageDocumentCount,
				BackupHookCount:               item.BackupHookCount,
				RegistrationEnabled:           false,
				InstallPlanJSON:               mustJSONObject(item.InstallPlan),
				ExposurePlanJSON:              mustJSONObject(item.ExposurePlan),
				ActivationStatus:              projects.ProjectModuleRegistrationStatusDisabled,
				Metadata:                      mustJSONObject(map[string]any{"source": "project.activate", "reason": "module_registration_disabled"}),
			}); err != nil {
				return projects.ProjectRegistrationDetail{}, err
			}
			disabledCount++
			continue
		}

		packageRoot := filepath.Join(projectRoot, filepath.FromSlash(item.Folder))
		registration, err := s.Modules.RegisterPackage(ctx, req, modules.RegisterPackageInput{
			PackagePath: packageRoot,
			Metadata: mustJSONObject(map[string]any{
				"source":                           "project.activate",
				"project_id":                       detail.Project.Project.ProjectID,
				"project_slug":                     detail.Project.Project.Slug,
				"project_contract_registration_id": detail.Registration.ProjectContractRegistrationID,
				"facet":                            "modules",
				"module_key":                       item.Key,
				"module_project_contract_path":     item.ProjectContractPath,
				"module_project_contract_hash":     item.ProjectContractHash,
			}),
		})
		if err != nil {
			return projects.ProjectRegistrationDetail{}, fmt.Errorf("%w: register project module %s: %w", ErrModuleRegistrationUnavailable, item.Key, err)
		}

		if _, err := s.Projects.UpsertProjectModuleRegistration(ctx, req, projects.UpsertProjectModuleRegistrationInput{
			ProjectContractRegistrationID: detail.Registration.ProjectContractRegistrationID,
			ProjectID:                     detail.Project.Project.ProjectID,
			ModuleKey:                     item.Key,
			ModuleFolder:                  item.Folder,
			ModuleManifestPath:            item.ManifestPath,
			ModuleProjectContractPath:     item.ProjectContractPath,
			ModuleManifestHash:            registration.Version.ManifestHash,
			ModuleProjectContractHash:     item.ProjectContractHash,
			ModulePackageHash:             registration.Package.ContentHash,
			ModuleID:                      registration.Version.ModuleID,
			ModuleName:                    registration.Version.ModuleName,
			ModuleVersion:                 registration.Version.Version,
			ModuleKind:                    registration.Version.ModuleKind,
			ModulePackageID:               registration.Package.ModulePackageID,
			ModuleVersionID:               registration.Version.ModuleVersionID,
			RequirementCount:              len(registration.Requirements),
			ObjectTypeCount:               len(registration.ObjectTypes),
			ProviderCount:                 len(registration.Providers),
			CapabilityCount:               len(registration.Capabilities),
			UsageDocumentCount:            len(registration.UsageDocuments),
			BackupHookCount:               len(registration.BackupHooks),
			RegistrationEnabled:           true,
			InstallPlanJSON:               mustJSONObject(item.InstallPlan),
			ExposurePlanJSON:              mustJSONObject(item.ExposurePlan),
			ActivationStatus:              projects.ProjectModuleRegistrationStatusRegistered,
			Metadata: mustJSONObject(map[string]any{
				"source":                "project.activate",
				"registered":            registration.Registered,
				"idempotent":            registration.Idempotent,
				"message":               registration.Message,
				"install_plan":          item.InstallPlan.Plan,
				"exposure_plan":         item.ExposurePlan.Plan,
				"explicit_install_only": true,
				"explicit_expose_only":  true,
			}),
		}); err != nil {
			return projects.ProjectRegistrationDetail{}, err
		}
		registeredCount++
		moduleIDs = append(moduleIDs, registration.Version.ModuleID)
		moduleVersionIDs = append(moduleVersionIDs, registration.Version.ModuleVersionID)
	}

	for _, previous := range existingRegistrations {
		if seenModules[previous.ModuleKey] {
			continue
		}
		if _, err := s.Projects.UpsertProjectModuleRegistration(ctx, req, projects.UpsertProjectModuleRegistrationInput{
			ProjectContractRegistrationID: detail.Registration.ProjectContractRegistrationID,
			ProjectID:                     detail.Project.Project.ProjectID,
			ModuleKey:                     previous.ModuleKey,
			ModuleFolder:                  previous.ModuleFolder,
			ModuleManifestPath:            previous.ModuleManifestPath,
			ModuleProjectContractPath:     previous.ModuleProjectContractPath,
			ModuleManifestHash:            previous.ModuleManifestHash,
			ModuleProjectContractHash:     previous.ModuleProjectContractHash,
			ModulePackageHash:             previous.ModulePackageHash,
			ModuleID:                      previous.ModuleID,
			ModuleName:                    previous.ModuleName,
			ModuleVersion:                 previous.ModuleVersion,
			ModuleKind:                    previous.ModuleKind,
			ModulePackageID:               ptrStringValue(previous.ModulePackageID),
			ModuleVersionID:               ptrStringValue(previous.ModuleVersionID),
			RequirementCount:              previous.RequirementCount,
			ObjectTypeCount:               previous.ObjectTypeCount,
			ProviderCount:                 previous.ProviderCount,
			CapabilityCount:               previous.CapabilityCount,
			UsageDocumentCount:            previous.UsageDocumentCount,
			BackupHookCount:               previous.BackupHookCount,
			RegistrationEnabled:           previous.RegistrationEnabled,
			InstallPlanJSON:               previous.InstallPlanJSON,
			ExposurePlanJSON:              previous.ExposurePlanJSON,
			ActivationStatus:              projects.ProjectModuleRegistrationStatusStale,
			Metadata:                      mustJSONObject(map[string]any{"source": "project.activate", "reason": "module_not_found"}),
		}); err != nil {
			return projects.ProjectRegistrationDetail{}, err
		}
	}

	now := s.now()
	if err := s.Projects.MarkProjectFacetActivated(ctx, req, detail.Project.Project.ProjectID, detail.Registration.ProjectContractRegistrationID, "modules", mustJSONObject(map[string]any{
		"activated_at":          now.Format(time.RFC3339),
		"activated_by_actor_id": req.ActorID,
		"module_count":          len(analysis.Plan.Modules),
		"registered_count":      registeredCount,
		"disabled_count":        disabledCount,
		"blocked_count":         blockedCount,
		"module_ids":            moduleIDs,
		"module_version_ids":    moduleVersionIDs,
		"source":                "project.activate",
	})); err != nil {
		return projects.ProjectRegistrationDetail{}, err
	}
	return s.Projects.GetProjectRegistrationStatus(ctx, detail.Project.Project.ProjectID)
}

func scriptsFacetActivatable(facets []projects.ProjectContractFacet) bool {
	for _, facet := range facets {
		if facet.FacetKey != "scripts" {
			continue
		}
		return facet.Enabled && facet.Present && !facet.Placeholder
	}
	return false
}

func workflowsFacetActivatable(facets []projects.ProjectContractFacet) bool {
	for _, facet := range facets {
		if facet.FacetKey != "workflows" {
			continue
		}
		return facet.Enabled && facet.Present
	}
	return false
}

func activeProjectScriptCapabilities(exposures []projects.ProjectScriptExposure) map[string]bool {
	active := map[string]bool{}
	for _, exposure := range exposures {
		if exposure.ActivationStatus != projects.ProjectScriptExposureStatusActive {
			continue
		}
		address := strings.TrimSpace(exposure.CapabilityAddress)
		if address == "" {
			continue
		}
		active[address] = true
	}
	return active
}

func activeProjectScriptExposuresByAddress(exposures []projects.ProjectScriptExposure) map[string]projects.ProjectScriptExposure {
	active := map[string]projects.ProjectScriptExposure{}
	for _, exposure := range exposures {
		if exposure.ActivationStatus != projects.ProjectScriptExposureStatusActive {
			continue
		}
		address := strings.TrimSpace(exposure.CapabilityAddress)
		if address == "" {
			continue
		}
		active[address] = exposure
	}
	return active
}

func schedulesFacetActivatable(facets []projects.ProjectContractFacet) bool {
	for _, facet := range facets {
		if facet.FacetKey != "schedules" {
			continue
		}
		return facet.Enabled && facet.Present && !facet.Placeholder
	}
	return false
}

func directEventsFacetActivatable(facets []projects.ProjectContractFacet) bool {
	for _, facet := range facets {
		if facet.FacetKey != "direct_events" {
			continue
		}
		return facet.Enabled && facet.Present && !facet.Placeholder
	}
	return false
}

func connectorsFacetActivatable(facets []projects.ProjectContractFacet) bool {
	for _, facet := range facets {
		if facet.FacetKey != "connectors" {
			continue
		}
		return facet.Enabled && facet.Present && !facet.Placeholder
	}
	return false
}

func modulesFacetActivatable(facets []projects.ProjectContractFacet) bool {
	for _, facet := range facets {
		if facet.FacetKey != "modules" {
			continue
		}
		return facet.Enabled && facet.Present && !facet.Placeholder
	}
	return false
}

func servicesFacetActivatable(facets []projects.ProjectContractFacet) bool {
	for _, facet := range facets {
		if facet.FacetKey == "services" {
			return facet.Enabled && facet.Present && !facet.Placeholder
		}
	}
	return false
}

func facetActivatableForAll(facets []projects.ProjectContractFacet, key string) bool {
	key = normalizeFacet(key)
	for _, facet := range facets {
		if normalizeFacet(facet.FacetKey) != key {
			continue
		}
		return facet.Enabled && facet.Present && !facet.Placeholder && facet.FacetStatus != projects.ProjectFacetStatusActivated
	}
	return false
}

func ensureScheduleTargetsReady(detail projects.ProjectRegistrationDetail, report projectcontracts.ValidationReport) error {
	for _, schedule := range report.Schedules {
		if strings.TrimSpace(schedule.ManifestPath) == "" || strings.TrimSpace(schedule.ContractStatus) == "disabled" {
			continue
		}
		if err := ensureProjectAutomationTargetReady(detail, report, "schedule", schedule.Key, schedule.TargetCapability, ErrScheduleTargetUnavailable); err != nil {
			return err
		}
	}
	return nil
}

func ensureDirectEventTargetsReady(detail projects.ProjectRegistrationDetail, report projectcontracts.ValidationReport) error {
	for _, event := range report.DirectEvents {
		if strings.TrimSpace(event.ManifestPath) == "" || strings.TrimSpace(event.ContractStatus) == "disabled" {
			continue
		}
		if err := ensureProjectAutomationTargetReady(detail, report, "direct event", event.Key, event.TargetCapability, ErrDirectEventTargetUnavailable); err != nil {
			return err
		}
	}
	return nil
}

func ensureProjectAutomationTargetReady(detail projects.ProjectRegistrationDetail, report projectcontracts.ValidationReport, kind, key, target string, sentinel error) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return fmt.Errorf("%w: %s %s has no target capability", sentinel, kind, key)
	}
	address, err := capabilities.ParseAddress(target)
	if err != nil {
		return fmt.Errorf("%w: %s %s target capability %q is invalid: %v", sentinel, kind, key, target, err)
	}
	normalized := address.CompactAddress
	active := activeProjectCapabilitySources(detail)
	if active[normalized].Facet != "" {
		return nil
	}
	localCapabilities := projectcontracts.LocalCapabilitySources(&report)
	if source := localCapabilities[normalized]; source.Facet != "" {
		if localCapabilitySourceActive(detail, source) {
			return nil
		}
		return fmt.Errorf("%w: %s %s target %s is declared by the %s facet but is not active; activate %s first", sentinel, kind, key, normalized, source.Facet, source.Facet)
	}
	localProviders := projectcontracts.LocalProviderSources(&report)
	providerAddress := address.ScopePath + "@" + address.ProviderKey
	if source := localProviders[providerAddress]; source != "" {
		return fmt.Errorf("%w: %s %s target %s uses project-local provider %s, but no local capability contract declares it; add the capability contract or point at an active external capability", sentinel, kind, key, normalized, providerAddress)
	}
	return nil
}

func localCapabilitySourceActive(detail projects.ProjectRegistrationDetail, source projectcontracts.LocalCapabilitySource) bool {
	switch source.Facet {
	case "scripts":
		for _, exposure := range detail.ScriptExposures {
			if exposure.ScriptKey == source.Key && exposure.ActivationStatus == projects.ProjectScriptExposureStatusActive {
				return true
			}
		}
	case "workflows":
		for _, workflow := range detail.WorkflowRegistrations {
			if workflow.WorkflowKey == source.Key && workflow.ActivationStatus == projects.ProjectWorkflowRegistrationStatusActive {
				return true
			}
		}
	case "connectors":
		for _, connector := range detail.ConnectorRegistrations {
			if connector.ConnectorKey == source.Key && connector.ActivationStatus == projects.ProjectConnectorRegistrationStatusActive {
				return true
			}
		}
	case "modules":
		for _, module := range detail.ModuleRegistrations {
			if module.ModuleKey == source.Key && module.ActivationStatus == projects.ProjectModuleRegistrationStatusRegistered {
				return true
			}
		}
	}
	return false
}

type activeProjectCapabilitySource struct {
	Facet           string
	Key             string
	ProviderAddress string
}

func activeProjectCapabilitySources(detail projects.ProjectRegistrationDetail) map[string]activeProjectCapabilitySource {
	active := map[string]activeProjectCapabilitySource{}
	register := func(address string, source activeProjectCapabilitySource) {
		normalized, err := capabilities.NormalizeAddress(address)
		if err != nil || strings.TrimSpace(normalized) == "" {
			return
		}
		active[normalized] = source
	}
	for _, exposure := range detail.ScriptExposures {
		if exposure.ActivationStatus != projects.ProjectScriptExposureStatusActive {
			continue
		}
		register(exposure.CapabilityAddress, activeProjectCapabilitySource{Facet: "scripts", Key: exposure.ScriptKey, ProviderAddress: exposure.ProviderAddress})
	}
	for _, workflow := range detail.WorkflowRegistrations {
		if workflow.ActivationStatus != projects.ProjectWorkflowRegistrationStatusActive {
			continue
		}
		register(workflow.CapabilityAddress, activeProjectCapabilitySource{Facet: "workflows", Key: workflow.WorkflowKey, ProviderAddress: workflow.ProviderAddress})
	}
	for _, connector := range detail.ConnectorRegistrations {
		if connector.ActivationStatus != projects.ProjectConnectorRegistrationStatusActive {
			continue
		}
		for _, address := range connectorCapabilityAddresses(connector) {
			register(address, activeProjectCapabilitySource{Facet: "connectors", Key: connector.ConnectorKey, ProviderAddress: connector.ProviderAddress})
		}
	}
	for _, module := range detail.ModuleRegistrations {
		if module.ActivationStatus != projects.ProjectModuleRegistrationStatusRegistered {
			continue
		}
		for _, address := range moduleCapabilityAddresses(module) {
			register(address, activeProjectCapabilitySource{Facet: "modules", Key: module.ModuleKey})
		}
	}
	return active
}

func connectorCapabilityAddresses(registration projects.ProjectConnectorRegistration) []string {
	addresses := []string{}
	var metadata struct {
		Endpoints []struct {
			CapabilityAddress string `json:"capability_address"`
		} `json:"endpoints"`
		Capabilities []struct {
			CapabilityAddress string `json:"capability_address"`
		} `json:"capabilities"`
	}
	if len(registration.Metadata) > 0 && json.Unmarshal(registration.Metadata, &metadata) == nil {
		for _, endpoint := range metadata.Endpoints {
			addresses = append(addresses, endpoint.CapabilityAddress)
		}
		for _, capability := range metadata.Capabilities {
			addresses = append(addresses, capability.CapabilityAddress)
		}
	}
	return addresses
}

func moduleCapabilityAddresses(registration projects.ProjectModuleRegistration) []string {
	addresses := []string{}
	var metadata struct {
		Capabilities []struct {
			CapabilityAddress string `json:"capability_address"`
		} `json:"capabilities"`
	}
	if len(registration.Metadata) > 0 && json.Unmarshal(registration.Metadata, &metadata) == nil {
		for _, capability := range metadata.Capabilities {
			addresses = append(addresses, capability.CapabilityAddress)
		}
	}
	return addresses
}

func normalizeFacet(facet string) string {
	switch strings.TrimSpace(facet) {
	case "direct-events":
		return "direct_events"
	case "watched-roots":
		return "watched_roots"
	default:
		return strings.TrimSpace(facet)
	}
}

type watchedRootOwnerFacet struct {
	key      string
	disabled bool
}

func watchedRootOwnerFacets(detail projects.ProjectRegistrationDetail) []watchedRootOwnerFacet {
	facetsByKey := map[string]projects.ProjectContractFacet{}
	facetOrder := []string{}
	for _, facet := range detail.Facets {
		key := watchedRootFacetKey(facet.FacetKey)
		if key == "" || key == "watched_roots" {
			continue
		}
		if _, ok := facetsByKey[key]; !ok {
			facetOrder = append(facetOrder, key)
		}
		facetsByKey[key] = facet
	}
	if len(facetsByKey) == 0 {
		return nil
	}

	owners := map[string]bool{}
	for _, registration := range detail.WatchedRootRegistrations {
		for _, candidate := range watchedRootOwnerFacetCandidates(registration) {
			if _, ok := facetsByKey[candidate]; ok {
				owners[candidate] = true
			}
		}
	}
	if len(owners) == 0 {
		if _, ok := facetsByKey["sync_policy"]; ok {
			owners["sync_policy"] = true
		}
	}

	out := []watchedRootOwnerFacet{}
	for _, key := range facetOrder {
		if !owners[key] {
			continue
		}
		facet := facetsByKey[key]
		out = append(out, watchedRootOwnerFacet{
			key:      key,
			disabled: facet.FacetStatus == projects.ProjectFacetStatusDisabled,
		})
	}
	return out
}

func watchedRootOwnerFacetCandidates(registration projects.ProjectWatchedRootRegistration) []string {
	seen := map[string]bool{}
	candidates := []string{}
	add := func(value string) {
		key := watchedRootFacetKey(value)
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		candidates = append(candidates, key)
	}

	sourceKinds := []string{}
	if len(registration.SourceKinds) > 0 {
		_ = json.Unmarshal(registration.SourceKinds, &sourceKinds)
	}
	for _, sourceKind := range sourceKinds {
		switch watchedRootFacetKey(sourceKind) {
		case "notes_contract":
			add("notes")
		case "repos_contract":
			add("repos")
		case "sync_policy":
			add("sync_policy")
		}
	}

	add(registration.LocalRootKey)
	if parts := strings.Split(registration.LocalRootKey, "__"); len(parts) > 1 {
		add(parts[len(parts)-1])
	}
	if firstSegment := strings.Split(strings.Trim(registration.RootRelativePath, "/"), "/")[0]; firstSegment != "" {
		add(firstSegment)
	}
	return candidates
}

func watchedRootFacetKey(value string) string {
	return strings.ToLower(normalizeFacet(strings.TrimSpace(value)))
}

func deactivationAction(key, kind, ref, status, summary string) projects.ProjectDeactivationAction {
	return projects.ProjectDeactivationAction{
		Key:      strings.TrimSpace(key),
		Kind:     strings.TrimSpace(kind),
		Ref:      strings.TrimSpace(ref),
		Status:   strings.TrimSpace(status),
		Summary:  strings.TrimSpace(summary),
		Metadata: json.RawMessage(`{}`),
	}
}

type projectConnectorRegistrationMetadata struct {
	Source             string                             `json:"source"`
	ProviderID         string                             `json:"provider_id,omitempty"`
	ProviderAddress    string                             `json:"provider_address,omitempty"`
	Endpoints          []projectConnectorEndpointMetadata `json:"endpoints,omitempty"`
	UsageDocumentIDs   []string                           `json:"usage_document_ids,omitempty"`
	ActivatedAt        string                             `json:"activated_at,omitempty"`
	ActivatedByActorID string                             `json:"activated_by_actor_id,omitempty"`
}

type projectConnectorEndpointMetadata struct {
	Endpoint                    string `json:"endpoint,omitempty"`
	CapabilityAddress           string `json:"capability_address,omitempty"`
	RuntimeKind                 string `json:"runtime_kind,omitempty"`
	ScriptID                    string `json:"script_id,omitempty"`
	ScriptVersionID             string `json:"script_version_id,omitempty"`
	CapabilityEndpointID        string `json:"capability_endpoint_id,omitempty"`
	CapabilityEndpointVersionID string `json:"capability_endpoint_version_id,omitempty"`
	RuntimeBindingID            string `json:"runtime_binding_id,omitempty"`
	ImplementationHash          string `json:"implementation_hash,omitempty"`
}

func connectorUnsupportedRuntime(item projectcontracts.ConnectorFacetItem) string {
	if item.RuntimeKind != "" && !connectorRuntimeSupported(item.RuntimeKind) {
		return item.RuntimeKind
	}
	for _, capability := range item.Capabilities {
		if capability.ContractStatus == "disabled" {
			continue
		}
		if capability.RuntimeKind != "" && !connectorRuntimeSupported(capability.RuntimeKind) {
			return capability.RuntimeKind
		}
	}
	return ""
}

func connectorRuntimeSupported(kind string) bool {
	switch strings.TrimSpace(kind) {
	case capabilities.RuntimeKindScript, capabilities.RuntimeKindCommand, capabilities.RuntimeKindHTTP:
		return true
	default:
		return false
	}
}

func connectorUsageDocumentTargetsEndpoint(doc projectcontracts.ConnectorUsageDocumentItem, endpoint string) bool {
	switch doc.Target {
	case "endpoint", "capability", "capability_endpoint":
		return strings.TrimSpace(doc.Endpoint) == strings.TrimSpace(endpoint)
	default:
		return false
	}
}

func connectorEndpointMetadataByAddress(raw json.RawMessage) map[string]projectConnectorEndpointMetadata {
	out := map[string]projectConnectorEndpointMetadata{}
	var metadata projectConnectorRegistrationMetadata
	if len(strings.TrimSpace(string(raw))) == 0 {
		return out
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return out
	}
	for _, endpoint := range metadata.Endpoints {
		if strings.TrimSpace(endpoint.CapabilityAddress) == "" {
			continue
		}
		out[endpoint.CapabilityAddress] = endpoint
	}
	return out
}

func (s Service) disableProjectConnectorCapabilities(ctx context.Context, req requestctx.Context, registration projects.ProjectConnectorRegistration) error {
	for _, endpoint := range connectorEndpointMetadataByAddress(registration.Metadata) {
		if err := s.disableProjectConnectorEndpoint(ctx, req, endpoint, "connector_disabled_or_stale"); err != nil {
			return err
		}
	}
	return nil
}

func (s Service) disableProjectConnectorEndpoint(ctx context.Context, req requestctx.Context, endpoint projectConnectorEndpointMetadata, reason string) error {
	metadata := mustJSONObject(map[string]any{
		"source":             "project.activate",
		"capability_address": endpoint.CapabilityAddress,
		"reason":             reason,
	})
	if strings.TrimSpace(endpoint.CapabilityEndpointID) != "" {
		if _, err := s.Capabilities.DisableCapabilityEndpoint(ctx, req, endpoint.CapabilityEndpointID, metadata); err != nil {
			return err
		}
	}
	if strings.TrimSpace(endpoint.RuntimeBindingID) != "" {
		if _, err := s.Capabilities.DisableRuntimeBinding(ctx, req, endpoint.RuntimeBindingID, metadata); err != nil {
			return err
		}
	}
	return nil
}

func (s Service) ensureConnectorUsageDocument(ctx context.Context, req requestctx.Context, detail projects.ProjectRegistrationDetail, item projectcontracts.ConnectorFacetItem, doc projectcontracts.ConnectorUsageDocumentItem, targetKind, targetID, targetAddress string, approvedAt time.Time) (capabilities.UsageDocument, error) {
	body, err := os.ReadFile(doc.Path)
	if err != nil {
		return capabilities.UsageDocument{}, err
	}
	title := filepath.Base(doc.Path)
	usageDoc, _, err := s.Capabilities.EnsureUsageDocument(ctx, req, capabilities.RegisterUsageDocumentInput{
		TargetKind:           targetKind,
		TargetID:             targetID,
		TargetAddress:        targetAddress,
		Title:                title,
		VersionLabel:         item.Version,
		BodyFormat:           capabilities.UsageDocumentFormatMarkdown,
		Body:                 string(body),
		SectionMapJSON:       json.RawMessage(`{}`),
		VisibilityPolicyJSON: json.RawMessage(`{}`),
		ReviewStatus:         capabilities.UsageReviewStatusApproved,
		ContentHash:          doc.ContentHash,
		SourceKind:           capabilities.UsageSourceKindManifest,
		SourceRef:            doc.Path,
		ApprovedByActorID:    req.ActorID,
		ApprovedAt:           &approvedAt,
		Metadata: mustJSONObject(map[string]any{
			"source":        "project.activate",
			"project_id":    detail.Project.Project.ProjectID,
			"project_slug":  detail.Project.Project.Slug,
			"connector_key": item.Key,
			"provider_key":  item.ProviderKey,
			"target":        doc.Target,
			"endpoint":      doc.Endpoint,
		}),
	})
	if err != nil {
		return capabilities.UsageDocument{}, err
	}
	return usageDoc, nil
}

func (s Service) ensureWorkflowUsageDocument(ctx context.Context, req requestctx.Context, detail projects.ProjectRegistrationDetail, item projectcontracts.WorkflowFacetItem, doc scripts.UsageDocument, targetID, targetAddress string, approvedAt time.Time) (capabilities.UsageDocument, error) {
	docPath := workflowUsageDocumentPath(item, doc)
	body, err := os.ReadFile(docPath)
	if err != nil {
		return capabilities.UsageDocument{}, err
	}
	title := filepath.Base(docPath)
	usageDoc, _, err := s.Capabilities.EnsureUsageDocument(ctx, req, capabilities.RegisterUsageDocumentInput{
		TargetKind:           capabilities.UsageTargetKindCapabilityEndpoint,
		TargetID:             targetID,
		TargetAddress:        targetAddress,
		Title:                title,
		VersionLabel:         defaultString(item.Version, "0.1.0"),
		BodyFormat:           capabilities.UsageDocumentFormatMarkdown,
		Body:                 string(body),
		SectionMapJSON:       json.RawMessage(`{}`),
		VisibilityPolicyJSON: json.RawMessage(`{}`),
		ReviewStatus:         capabilities.UsageReviewStatusApproved,
		ContentHash:          hashFileOrEmpty(docPath),
		SourceKind:           capabilities.UsageSourceKindManifest,
		SourceRef:            docPath,
		ApprovedByActorID:    req.ActorID,
		ApprovedAt:           &approvedAt,
		Metadata: mustJSONObject(map[string]any{
			"source":       "project.activate",
			"project_id":   detail.Project.Project.ProjectID,
			"project_slug": detail.Project.Project.Slug,
			"workflow_key": item.Key,
			"target":       doc.Target,
		}),
	})
	if err != nil {
		return capabilities.UsageDocument{}, err
	}
	return usageDoc, nil
}

func workflowUsageDocumentPath(item projectcontracts.WorkflowFacetItem, doc scripts.UsageDocument) string {
	path := strings.TrimSpace(doc.Path)
	if filepath.IsAbs(path) {
		return path
	}
	if strings.TrimSpace(item.ManifestPath) == "" {
		return path
	}
	return filepath.Join(filepath.Dir(item.ManifestPath), filepath.FromSlash(path))
}

func projectConnectorEndpointManifest(item projectcontracts.ConnectorFacetItem, capability projectcontracts.ConnectorCapabilityItem, registerResult scripts.RegisterResult) json.RawMessage {
	return mustJSONObject(map[string]any{
		"kind":                "loom.project_connector_endpoint",
		"connector_key":       item.Key,
		"provider_key":        item.ProviderKey,
		"provider_address":    item.ProviderAddress,
		"endpoint":            capability.Endpoint,
		"runtime_kind":        capability.RuntimeKind,
		"runtime_config_json": capability.RuntimeConfigJSON,
		"script_id":           registerResult.Script.ScriptID,
		"script_version_id":   registerResult.Version.ScriptVersionID,
		"manifest_hash":       item.ManifestHash,
		"script_hash":         capability.ScriptManifestHash,
		"package_hash":        capability.ScriptPackageHash,
		"implementation_hash": capability.ImplementationHash,
	})
}

func projectWorkflowEndpointManifest(item projectcontracts.WorkflowFacetItem, registerResult workflows.RegisterResult) json.RawMessage {
	return mustJSONObject(map[string]any{
		"kind":                "loom.project_workflow_endpoint",
		"workflow_key":        item.Key,
		"workflow_id":         registerResult.Workflow.WorkflowID,
		"workflow_version_id": registerResult.Version.WorkflowVersionID,
		"manifest_hash":       item.ManifestHash,
		"package_hash":        item.PackageHash,
		"runtime_kind":        capabilities.RuntimeKindWorkflow,
	})
}

func projectWorkflowScriptShimEndpointManifest(item projectcontracts.WorkflowFacetItem, exposure projects.ProjectScriptExposure) json.RawMessage {
	return mustJSONObject(map[string]any{
		"kind":                   "loom.project_workflow_script_shim_endpoint",
		"workflow_key":           item.Key,
		"workflow_manifest_hash": item.ManifestHash,
		"script_key":             exposure.ScriptKey,
		"script_id":              ptrStringValue(exposure.ScriptID),
		"script_version_id":      ptrStringValue(exposure.ScriptVersionID),
		"script_capability":      exposure.CapabilityAddress,
		"runtime_kind":           capabilities.RuntimeKindScript,
	})
}

func (s Service) ensureDirectEventAuthProfiles(ctx context.Context, req requestctx.Context, integrationRef string, item projectcontracts.DirectEventFacetItem, detail projects.ProjectRegistrationDetail) ([]string, string, error) {
	refs := []string{}
	primary := ""
	for _, profile := range item.AuthProfiles {
		if strings.TrimSpace(profile.ExistingRef) != "" {
			refs = append(refs, strings.TrimSpace(profile.ExistingRef))
			if primary == "" {
				primary = strings.TrimSpace(profile.ExistingRef)
			}
			continue
		}
		if profile.Kind != automation.IntegrationAuthPrivateNetwork || !profile.CreateIfMissing {
			return nil, "", fmt.Errorf("%w: direct event %s auth profile %s must be created explicitly", ErrDirectEventCredentialRequired, item.Key, profile.Name)
		}
		created, _, err := s.Automation.EnsureIntegrationAuthProfile(ctx, req, integrationRef, automation.CreateIntegrationAuthProfileInput{
			DisplayName:         defaultString(profile.Name, "private_network"),
			AuthKind:            automation.IntegrationAuthPrivateNetwork,
			AllowedEndpointRefs: mustJSONArray([]string{item.BackendEndpointSlug}),
			Metadata: mustJSONObject(map[string]any{
				"source":                           "project.activate",
				"project_id":                       detail.Project.Project.ProjectID,
				"project_slug":                     detail.Project.Project.Slug,
				"project_contract_registration_id": detail.Registration.ProjectContractRegistrationID,
				"facet":                            "direct_events",
				"event_key":                        item.Key,
				"auth_profile":                     profile.Name,
			}),
		})
		if err != nil {
			return nil, "", fmt.Errorf("ensure project direct event auth profile %s: %w", item.Key, err)
		}
		refs = append(refs, created.AuthProfileID)
		if primary == "" {
			primary = created.AuthProfileID
		}
	}
	return refs, primary, nil
}

func scheduleRunAsActorRef(runAs string, req requestctx.Context) string {
	switch strings.ToLower(strings.TrimSpace(runAs)) {
	case "", "scheduler":
		return ""
	case "owner":
		return req.ActorID
	default:
		return strings.TrimSpace(runAs)
	}
}

func (s Service) disableProjectScriptCapability(ctx context.Context, req requestctx.Context, exposure projects.ProjectScriptExposure) error {
	metadata := mustJSONObject(map[string]any{
		"source":                     "project.activate",
		"project_script_exposure_id": exposure.ProjectScriptExposureID,
		"reason":                     "script_exposure_disabled",
	})
	if exposure.CapabilityEndpointID != nil && strings.TrimSpace(*exposure.CapabilityEndpointID) != "" {
		if _, err := s.Capabilities.DisableCapabilityEndpoint(ctx, req, *exposure.CapabilityEndpointID, metadata); err != nil {
			return err
		}
	}
	if exposure.RuntimeBindingID != nil && strings.TrimSpace(*exposure.RuntimeBindingID) != "" {
		if _, err := s.Capabilities.DisableRuntimeBinding(ctx, req, *exposure.RuntimeBindingID, metadata); err != nil {
			return err
		}
	}
	return nil
}

func (s Service) disableProjectWorkflowCapability(ctx context.Context, req requestctx.Context, registration projects.ProjectWorkflowRegistration) error {
	metadata := mustJSONObject(map[string]any{
		"source":                           "project.deactivate",
		"project_workflow_registration_id": registration.ProjectWorkflowRegistrationID,
		"reason":                           "workflow_registration_disabled",
	})
	if registration.CapabilityEndpointID != nil && strings.TrimSpace(*registration.CapabilityEndpointID) != "" {
		if _, err := s.Capabilities.DisableCapabilityEndpoint(ctx, req, *registration.CapabilityEndpointID, metadata); err != nil {
			return err
		}
	}
	if registration.RuntimeBindingID != nil && strings.TrimSpace(*registration.RuntimeBindingID) != "" {
		if _, err := s.Capabilities.DisableRuntimeBinding(ctx, req, *registration.RuntimeBindingID, metadata); err != nil {
			return err
		}
	}
	return nil
}

func projectScriptEndpointManifest(item projectcontracts.ScriptFacetItem, registerResult scripts.RegisterResult) json.RawMessage {
	return mustJSONObject(map[string]any{
		"kind":              "loom.project_script_endpoint",
		"script_key":        item.Key,
		"script_id":         registerResult.Script.ScriptID,
		"script_version_id": registerResult.Version.ScriptVersionID,
		"manifest_hash":     item.ManifestHash,
		"package_hash":      item.PackageHash,
		"exposure_hash":     item.ExposureHash,
	})
}

func workflowWaitTimeout(item projectcontracts.WorkflowFacetItem) int {
	if item.Execution.TimeoutSeconds > 0 {
		return item.Execution.TimeoutSeconds
	}
	return 120
}

func workflowAuthorizationLevel(item projectcontracts.WorkflowFacetItem) int {
	if item.Capability.ExecutionAuthorizationLevel > 0 {
		return item.Capability.ExecutionAuthorizationLevel
	}
	return 3
}

func hashFileOrEmpty(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("sha256:%x", sum[:])
}

func mergeWorkflowRegistrationMetadata(base json.RawMessage, extra map[string]any) json.RawMessage {
	metadata := map[string]any{}
	if len(base) > 0 {
		_ = json.Unmarshal(base, &metadata)
	}
	for key, value := range extra {
		metadata[key] = value
	}
	return mustJSONObject(metadata)
}

func ptrStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func mustJSONObject(value any) json.RawMessage {
	if value == nil {
		return json.RawMessage(`{}`)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(raw)
}

func jsonOrEmptyObject(value json.RawMessage) json.RawMessage {
	if len(value) == 0 || string(value) == "null" {
		return json.RawMessage(`{}`)
	}
	return value
}

func mustJSONArray(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`[]`)
	}
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`[]`)
	}
	return json.RawMessage(raw)
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
