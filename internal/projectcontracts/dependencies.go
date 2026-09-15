package projectcontracts

import (
	"strings"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/nodeagent/watchedroots"
)

type LocalCapabilitySource struct {
	Facet           string `json:"facet"`
	Key             string `json:"key,omitempty"`
	ProviderAddress string `json:"provider_address,omitempty"`
}

func LocalProviderSources(report *ValidationReport) map[string]string {
	sources := map[string]string{}
	if report == nil {
		return sources
	}
	register := func(address, source string) {
		address = strings.TrimSpace(address)
		if address == "" {
			return
		}
		if sources[address] == "" {
			sources[address] = source
		}
	}
	if report.Project.OwnerNode != "" && report.Project.Slug != "" {
		if address, err := capabilities.NormalizeProviderAddress(report.Project.OwnerNode + "@" + report.Project.Slug); err == nil {
			register(address, "project")
		}
	}
	for _, provider := range report.DerivedProviders {
		register(provider.CompactAddress, provider.Kind)
	}
	for _, connector := range report.Connectors {
		register(connector.ProviderAddress, "connector:"+connector.Key)
	}
	for _, module := range report.Modules {
		for _, provider := range module.Manifest.Provides.Providers {
			address, err := moduleProviderAddress(module, provider.ProviderKey)
			if err != nil {
				continue
			}
			register(address, "module:"+module.Key)
		}
	}
	return sources
}

func LocalCapabilitySources(report *ValidationReport) map[string]LocalCapabilitySource {
	sources := map[string]LocalCapabilitySource{}
	if report == nil {
		return sources
	}
	register := func(address string, source LocalCapabilitySource) {
		address = strings.TrimSpace(address)
		if address == "" {
			return
		}
		if sources[address].Facet == "" {
			sources[address] = source
		}
	}
	for _, script := range report.Scripts {
		register(script.CapabilityAddress, LocalCapabilitySource{Facet: "scripts", Key: script.Key})
	}
	for _, connector := range report.Connectors {
		for _, capability := range connector.Capabilities {
			register(capability.CapabilityAddress, LocalCapabilitySource{Facet: "connectors", Key: connector.Key, ProviderAddress: connector.ProviderAddress})
		}
	}
	for _, workflow := range report.Workflows {
		register(workflow.CapabilityAddress, LocalCapabilitySource{Facet: "workflows", Key: workflow.Key})
		register(workflow.ScriptCapabilityAddress, LocalCapabilitySource{Facet: "workflows", Key: workflow.Key})
	}
	for _, module := range report.Modules {
		for _, capability := range module.Manifest.Provides.Capabilities {
			providerAddress, err := moduleProviderAddress(module, capability.ProviderKey)
			if err != nil {
				continue
			}
			address, err := capabilities.NormalizeAddress(providerAddress + "." + capability.EndpointName)
			if err != nil {
				continue
			}
			register(address, LocalCapabilitySource{Facet: "modules", Key: module.Key, ProviderAddress: providerAddress})
		}
	}
	return sources
}

func validateCrossFacetDependencies(report *ValidationReport, add func(Diagnostic)) {
	if report == nil {
		return
	}
	localProviders := map[string]bool{}
	providerSources := map[string]string{}
	registerProvider := func(address, source string) {
		address = strings.TrimSpace(address)
		if address == "" {
			return
		}
		localProviders[address] = true
		if providerSources[address] == "" {
			providerSources[address] = source
		}
	}
	for _, provider := range report.DerivedProviders {
		registerProvider(provider.CompactAddress, provider.Kind)
	}
	for _, connector := range report.Connectors {
		if connector.ProviderAddress != "" {
			if localProviders[connector.ProviderAddress] {
				add(Diagnostic{
					Severity:   SeverityError,
					Code:       "connector.provider_collides_with_project_provider",
					Message:    "connector provider address collides with a project-derived provider: " + connector.ProviderAddress,
					File:       connector.ManifestPath,
					Field:      "provider.key",
					Suggestion: "use a connector provider key that is distinct from the project scripts/workflows provider.",
				})
			}
			registerProvider(connector.ProviderAddress, "connector:"+connector.Key)
		}
	}

	localCapabilities := map[string]string{}
	registerCapability := func(address, source string) {
		address = strings.TrimSpace(address)
		if address == "" {
			return
		}
		if localCapabilities[address] == "" {
			localCapabilities[address] = source
		}
	}
	for _, script := range report.Scripts {
		if script.CapabilityAddress != "" {
			registerCapability(script.CapabilityAddress, "scripts")
		}
	}
	for _, connector := range report.Connectors {
		for _, capability := range connector.Capabilities {
			if capability.CapabilityAddress != "" {
				registerCapability(capability.CapabilityAddress, "connectors")
			}
		}
	}
	workflowBlocked := map[string]string{}
	for _, workflow := range report.Workflows {
		if workflow.CapabilityAddress != "" {
			registerCapability(workflow.CapabilityAddress, "workflows")
			if workflow.ImplementationKind != WorkflowImplementationScript && !workflow.Executable {
				workflowBlocked[workflow.CapabilityAddress] = workflow.ManifestPath
			}
		}
		if workflow.ScriptCapabilityAddress != "" {
			registerCapability(workflow.ScriptCapabilityAddress, "workflows")
		}
	}
	for _, module := range report.Modules {
		for _, provider := range module.Manifest.Provides.Providers {
			address, err := moduleProviderAddress(module, provider.ProviderKey)
			if err != nil {
				continue
			}
			if previous := providerSources[address]; previous != "" {
				add(Diagnostic{
					Severity:   SeverityError,
					Code:       "module.provider_collides_with_local_provider",
					Message:    "module provider address collides with another project provider: " + address,
					File:       module.ManifestPath,
					Field:      "provides.providers.provider_key",
					Suggestion: "use a module provider key that is distinct from project scripts, workflows, connectors, and other module providers; previous source was " + previous + ".",
				})
			}
			registerProvider(address, "module:"+module.Key)
		}
		for _, capability := range module.Manifest.Provides.Capabilities {
			providerAddress, err := moduleProviderAddress(module, capability.ProviderKey)
			if err != nil {
				continue
			}
			address, err := capabilities.NormalizeAddress(providerAddress + "." + capability.EndpointName)
			if err != nil {
				continue
			}
			if previous := localCapabilities[address]; previous != "" {
				add(Diagnostic{
					Severity:   SeverityError,
					Code:       "module.capability_collides_with_local_capability",
					Message:    "module capability address collides with another project capability: " + address,
					File:       module.ManifestPath,
					Field:      "provides.capabilities.endpoint_name",
					Suggestion: "use a distinct module provider key or endpoint name; previous source was " + previous + ".",
				})
			}
			registerCapability(address, "modules")
		}
	}

	for _, schedule := range report.Schedules {
		validateTargetCapability(add, targetCheckInput{
			Target:            schedule.TargetCapability,
			File:              schedule.ManifestPath,
			Field:             "target.capability",
			Facet:             "schedule",
			Key:               schedule.Key,
			LocalProviders:    localProviders,
			LocalCapabilities: localCapabilities,
			WorkflowBlocked:   workflowBlocked,
		})
	}
	for _, event := range report.DirectEvents {
		validateTargetCapability(add, targetCheckInput{
			Target:            event.TargetCapability,
			File:              event.ManifestPath,
			Field:             "target.capability",
			Facet:             "direct_event",
			Key:               event.Key,
			LocalProviders:    localProviders,
			LocalCapabilities: localCapabilities,
			WorkflowBlocked:   workflowBlocked,
		})
	}
	for _, root := range report.WatchedRoots {
		if root.SyncMode != "" && root.SyncMode != watchedroots.SyncModeNone && root.IndexMode == watchedroots.IndexModeNone {
			add(Diagnostic{
				Severity:   SeverityWarning,
				Code:       "watched_root.sync_without_index",
				Message:    "watched root enables sync without indexing: " + root.Key,
				Field:      "watched_roots." + root.Key,
				Suggestion: "enable markdown_text or metadata_only indexing when this root should be searchable through LOOM.",
			})
		}
		if root.DeleteMode == "" {
			add(Diagnostic{
				Severity:   SeverityWarning,
				Code:       "watched_root.delete_mode_implicit",
				Message:    "watched root delete mode is implicit: " + root.Key,
				Field:      "watched_roots." + root.Key,
				Suggestion: "set delete policy explicitly in the project sync/backup policy.",
			})
		}
	}
}

func moduleProviderAddress(module ModuleFacetItem, providerKey string) (string, error) {
	targetNode := strings.TrimSpace(module.InstallPlan.TargetNode)
	if targetNode == "" {
		targetNode = "main"
	}
	return capabilities.NormalizeProviderAddress(targetNode + "@" + strings.TrimSpace(providerKey))
}

type targetCheckInput struct {
	Target            string
	File              string
	Field             string
	Facet             string
	Key               string
	LocalProviders    map[string]bool
	LocalCapabilities map[string]string
	WorkflowBlocked   map[string]string
}

func validateTargetCapability(add func(Diagnostic), input targetCheckInput) {
	target := strings.TrimSpace(input.Target)
	if target == "" {
		return
	}
	address, err := capabilities.ParseAddress(target)
	if err != nil {
		add(Diagnostic{
			Severity:   SeverityError,
			Code:       input.Facet + ".target_capability_invalid",
			Message:    input.Facet + " target capability is not a valid capability URL: " + err.Error(),
			File:       input.File,
			Field:      input.Field,
			Suggestion: "use the node@provider.capability format, for example main@project-name.run.",
		})
		return
	}
	normalized := address.CompactAddress
	if file := input.WorkflowBlocked[normalized]; file != "" {
		add(Diagnostic{
			Severity:   SeverityError,
			Code:       input.Facet + ".target_workflow_runtime_unsupported",
			Message:    input.Facet + " target points to a first-class workflow placeholder: " + normalized,
			File:       input.File,
			Field:      input.Field,
			Suggestion: "target a script-backed workflow shim or script/connector capability until the workflow runtime exists; workflow declared in " + file,
		})
		return
	}
	if input.LocalCapabilities[normalized] != "" {
		return
	}
	provider := address.ScopePath + "@" + address.ProviderKey
	if input.LocalProviders[provider] {
		add(Diagnostic{
			Severity:   SeverityInfo,
			Code:       input.Facet + ".target_capability_missing",
			Message:    input.Facet + " target capability uses a project-local provider but was not declared in the local contract: " + normalized,
			File:       input.File,
			Field:      input.Field,
			Suggestion: "expose the target under scripts/ or connectors/ if it is project-owned; otherwise doctor will verify it against backend state.",
		})
	}
}
