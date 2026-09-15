package projectdoctor

import (
	"encoding/json"
	"sort"
	"strings"

	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
)

type DiffStatus string

const (
	DiffUnchanged DiffStatus = "unchanged"
	DiffAdded     DiffStatus = "added"
	DiffRemoved   DiffStatus = "removed"
	DiffChanged   DiffStatus = "changed"
	DiffMissing   DiffStatus = "missing"
)

type DiffItem struct {
	Key            string     `json:"key"`
	Kind           string     `json:"kind"`
	Status         DiffStatus `json:"status"`
	LocalRef       string     `json:"local_ref,omitempty"`
	RegisteredRef  string     `json:"registered_ref,omitempty"`
	LocalHash      string     `json:"local_hash,omitempty"`
	RegisteredHash string     `json:"registered_hash,omitempty"`
	Summary        string     `json:"summary"`
}

type DiffSummary struct {
	Unchanged int `json:"unchanged"`
	Added     int `json:"added"`
	Removed   int `json:"removed"`
	Changed   int `json:"changed"`
	Missing   int `json:"missing"`
}

func (s DiffSummary) HasChanges() bool {
	return s.Changed > 0 || s.Added > 0 || s.Removed > 0 || s.Missing > 0
}

type DiffReport struct {
	ProjectRef string      `json:"project_ref,omitempty"`
	LocalRoot  string      `json:"local_root,omitempty"`
	Summary    DiffSummary `json:"summary"`
	Items      []DiffItem  `json:"items"`
}

func BuildDiff(analysis projectcontracts.Analysis, detail *projects.ProjectRegistrationDetail) DiffReport {
	report := DiffReport{
		ProjectRef: firstNonEmpty(localProjectRef(&analysis), detailProjectRef(detail)),
		LocalRoot:  analysis.Report.ProjectRoot,
	}
	add := func(item DiffItem) {
		item.Key = strings.TrimSpace(item.Key)
		item.Kind = strings.TrimSpace(item.Kind)
		item.Summary = strings.TrimSpace(item.Summary)
		report.Items = append(report.Items, item)
	}

	if analysis.Loaded != nil {
		localHash := contractHash(analysis.Loaded.Raw)
		if detail == nil || detail.Registration == nil {
			add(DiffItem{Key: "contract", Kind: "contract", Status: DiffMissing, LocalHash: localHash, Summary: "Local project contract is not registered."})
		} else if localHash == detail.Registration.ContractHash {
			add(DiffItem{Key: "contract", Kind: "contract", Status: DiffUnchanged, LocalHash: localHash, RegisteredHash: detail.Registration.ContractHash, Summary: "Contract hash matches registered snapshot."})
		} else {
			add(DiffItem{Key: "contract", Kind: "contract", Status: DiffChanged, LocalHash: localHash, RegisteredHash: detail.Registration.ContractHash, Summary: "Local contract hash differs from registered snapshot."})
		}
	}

	diffFacets(&report, add, analysis.Plan.Facets, detail)
	diffScripts(&report, add, analysis.Plan.Scripts, detail)
	diffSchedules(&report, add, analysis.Plan.Schedules, detail)
	diffDirectEvents(&report, add, analysis.Plan.DirectEvents, detail)
	diffWatchedRoots(&report, add, analysis.Plan.WatchedRoots, detail)
	diffConnectors(&report, add, analysis.Plan.Connectors, detail)
	diffModules(&report, add, analysis.Plan.Modules, detail)
	diffWorkflows(&report, add, analysis.Plan.Workflows, detail)

	sort.SliceStable(report.Items, func(i, j int) bool {
		if diffStatusRank(report.Items[i].Status) != diffStatusRank(report.Items[j].Status) {
			return diffStatusRank(report.Items[i].Status) < diffStatusRank(report.Items[j].Status)
		}
		if report.Items[i].Kind != report.Items[j].Kind {
			return report.Items[i].Kind < report.Items[j].Kind
		}
		return report.Items[i].Key < report.Items[j].Key
	})
	report.Summary = summarizeDiff(report.Items)
	return report
}

func diffFacets(report *DiffReport, add func(DiffItem), local []projectcontracts.PlanFacet, detail *projects.ProjectRegistrationDetail) {
	registered := map[string]projects.ProjectContractFacet{}
	if detail != nil {
		for _, item := range detail.Facets {
			registered[item.FacetKey] = item
		}
	}
	seen := map[string]bool{}
	for _, item := range local {
		seen[item.Key] = true
		reg, ok := registered[item.Key]
		if !ok {
			add(DiffItem{Key: item.Key, Kind: "facet", Status: DiffAdded, LocalRef: item.Folder, Summary: "Facet exists locally but not in registered snapshot."})
			continue
		}
		if item.Enabled != reg.Enabled || item.Present != reg.Present || item.Placeholder != reg.Placeholder || item.Folder != reg.Folder {
			add(DiffItem{Key: item.Key, Kind: "facet", Status: DiffChanged, LocalRef: item.Folder, RegisteredRef: reg.Folder, Summary: "Facet metadata differs from registered snapshot."})
			continue
		}
		add(DiffItem{Key: item.Key, Kind: "facet", Status: DiffUnchanged, LocalRef: item.Folder, RegisteredRef: reg.Folder, Summary: "Facet matches registered snapshot."})
	}
	for key, item := range registered {
		if !seen[key] {
			add(DiffItem{Key: key, Kind: "facet", Status: DiffRemoved, RegisteredRef: item.Folder, Summary: "Facet exists in registered snapshot but not locally."})
		}
	}
}

func diffScripts(report *DiffReport, add func(DiffItem), local []projectcontracts.ScriptFacetItem, detail *projects.ProjectRegistrationDetail) {
	registered := map[string]projects.ProjectScriptExposure{}
	if detail != nil {
		for _, item := range detail.ScriptExposures {
			registered[item.ScriptKey] = item
		}
	}
	seen := map[string]bool{}
	for _, item := range local {
		if item.Key == "" {
			continue
		}
		seen[item.Key] = true
		reg, ok := registered[item.Key]
		if !ok {
			add(DiffItem{Key: item.Key, Kind: "script", Status: DiffAdded, LocalRef: item.CapabilityAddress, LocalHash: firstNonEmpty(item.ExposureHash, item.PackageHash, item.ManifestHash), Summary: "Script exposure exists locally but not in registered state."})
			continue
		}
		localHash := firstNonEmpty(item.ExposureHash, item.PackageHash, item.ManifestHash)
		registeredHash := firstNonEmpty(reg.ExposureHash, reg.CapabilityAddress)
		if localHash != "" && reg.ExposureHash != "" && localHash != reg.ExposureHash {
			add(DiffItem{Key: item.Key, Kind: "script", Status: DiffChanged, LocalRef: item.CapabilityAddress, RegisteredRef: reg.CapabilityAddress, LocalHash: localHash, RegisteredHash: registeredHash, Summary: "Script exposure hash differs."})
			continue
		}
		add(DiffItem{Key: item.Key, Kind: "script", Status: DiffUnchanged, LocalRef: item.CapabilityAddress, RegisteredRef: reg.CapabilityAddress, LocalHash: localHash, RegisteredHash: registeredHash, Summary: "Script exposure matches registered state."})
	}
	for key, item := range registered {
		if !seen[key] {
			add(DiffItem{Key: key, Kind: "script", Status: DiffRemoved, RegisteredRef: item.CapabilityAddress, RegisteredHash: item.ExposureHash, Summary: "Registered script exposure is not present locally."})
		}
	}
}

func diffSchedules(report *DiffReport, add func(DiffItem), local []projectcontracts.ScheduleFacetItem, detail *projects.ProjectRegistrationDetail) {
	registered := map[string]projects.ProjectScheduleRegistration{}
	if detail != nil {
		for _, item := range detail.ScheduleRegistrations {
			registered[item.ScheduleKey] = item
		}
	}
	seen := map[string]bool{}
	for _, item := range local {
		seen[item.Key] = true
		reg, ok := registered[item.Key]
		if !ok {
			add(DiffItem{Key: item.Key, Kind: "schedule", Status: DiffAdded, LocalRef: item.BackendScheduleKey, LocalHash: item.ManifestHash, Summary: "Schedule exists locally but not in registered state."})
			continue
		}
		if item.ManifestHash != reg.ScheduleHash || item.TargetCapability != reg.TargetCapability || item.InputHash != reg.InputHash {
			add(DiffItem{Key: item.Key, Kind: "schedule", Status: DiffChanged, LocalRef: item.BackendScheduleKey, RegisteredRef: reg.BackendScheduleKey, LocalHash: firstNonEmpty(item.ManifestHash, item.InputHash), RegisteredHash: firstNonEmpty(reg.ScheduleHash, reg.InputHash), Summary: "Schedule manifest, input, or target differs."})
			continue
		}
		add(DiffItem{Key: item.Key, Kind: "schedule", Status: DiffUnchanged, LocalRef: item.BackendScheduleKey, RegisteredRef: reg.BackendScheduleKey, LocalHash: item.ManifestHash, RegisteredHash: reg.ScheduleHash, Summary: "Schedule matches registered state."})
	}
	for key, item := range registered {
		if !seen[key] {
			add(DiffItem{Key: key, Kind: "schedule", Status: DiffRemoved, RegisteredRef: item.BackendScheduleKey, RegisteredHash: item.ScheduleHash, Summary: "Registered schedule is not present locally."})
		}
	}
}

func diffDirectEvents(report *DiffReport, add func(DiffItem), local []projectcontracts.DirectEventFacetItem, detail *projects.ProjectRegistrationDetail) {
	registered := map[string]projects.ProjectDirectEventRegistration{}
	if detail != nil {
		for _, item := range detail.DirectEventRegistrations {
			registered[item.EventKey] = item
		}
	}
	seen := map[string]bool{}
	for _, item := range local {
		seen[item.Key] = true
		reg, ok := registered[item.Key]
		if !ok {
			add(DiffItem{Key: item.Key, Kind: "direct_event", Status: DiffAdded, LocalRef: item.BackendEndpointSlug, LocalHash: item.ManifestHash, Summary: "Direct event exists locally but not in registered state."})
			continue
		}
		if item.ManifestHash != reg.EventHash || item.TargetCapability != reg.TargetCapability || item.ExpectedInputHash != reg.ExpectedInputHash || item.PayloadExampleHash != reg.PayloadExampleHash {
			add(DiffItem{Key: item.Key, Kind: "direct_event", Status: DiffChanged, LocalRef: item.BackendEndpointSlug, RegisteredRef: reg.BackendEndpointSlug, LocalHash: firstNonEmpty(item.ManifestHash, item.ExpectedInputHash, item.PayloadExampleHash), RegisteredHash: firstNonEmpty(reg.EventHash, reg.ExpectedInputHash, reg.PayloadExampleHash), Summary: "Direct event manifest, mapping, payload, or target differs."})
			continue
		}
		add(DiffItem{Key: item.Key, Kind: "direct_event", Status: DiffUnchanged, LocalRef: item.BackendEndpointSlug, RegisteredRef: reg.BackendEndpointSlug, LocalHash: item.ManifestHash, RegisteredHash: reg.EventHash, Summary: "Direct event matches registered state."})
	}
	for key, item := range registered {
		if !seen[key] {
			add(DiffItem{Key: key, Kind: "direct_event", Status: DiffRemoved, RegisteredRef: item.BackendEndpointSlug, RegisteredHash: item.EventHash, Summary: "Registered direct event is not present locally."})
		}
	}
}

func diffWatchedRoots(report *DiffReport, add func(DiffItem), local []projectcontracts.ProjectWatchedRootItem, detail *projects.ProjectRegistrationDetail) {
	registered := map[string]projects.ProjectWatchedRootRegistration{}
	if detail != nil {
		for _, item := range detail.WatchedRootRegistrations {
			registered[item.LocalRootKey] = item
		}
	}
	seen := map[string]bool{}
	for _, item := range local {
		seen[item.Key] = true
		reg, ok := registered[item.Key]
		if !ok {
			add(DiffItem{Key: item.Key, Kind: "watched_root", Status: DiffAdded, LocalRef: item.BackendRootKey, LocalHash: item.ConfigHash, Summary: "Watched root policy exists locally but not in registered state."})
			continue
		}
		if item.ConfigHash != reg.ConfigHash || item.SyncMode != reg.SyncMode || item.BackupMode != reg.BackupMode || item.IndexMode != reg.IndexMode || item.DeleteMode != reg.DeleteMode {
			add(DiffItem{Key: item.Key, Kind: "watched_root", Status: DiffChanged, LocalRef: item.BackendRootKey, RegisteredRef: reg.BackendRootKey, LocalHash: item.ConfigHash, RegisteredHash: reg.ConfigHash, Summary: "Watched root config or modes differ."})
			continue
		}
		add(DiffItem{Key: item.Key, Kind: "watched_root", Status: DiffUnchanged, LocalRef: item.BackendRootKey, RegisteredRef: reg.BackendRootKey, LocalHash: item.ConfigHash, RegisteredHash: reg.ConfigHash, Summary: "Watched root policy matches registered state."})
	}
	for key, item := range registered {
		if !seen[key] {
			add(DiffItem{Key: key, Kind: "watched_root", Status: DiffRemoved, RegisteredRef: item.BackendRootKey, RegisteredHash: item.ConfigHash, Summary: "Registered watched root is not present locally."})
		}
	}
}

func diffConnectors(report *DiffReport, add func(DiffItem), local []projectcontracts.ConnectorFacetItem, detail *projects.ProjectRegistrationDetail) {
	registered := map[string]projects.ProjectConnectorRegistration{}
	if detail != nil {
		for _, item := range detail.ConnectorRegistrations {
			registered[item.ConnectorKey] = item
		}
	}
	seen := map[string]bool{}
	for _, item := range local {
		seen[item.Key] = true
		reg, ok := registered[item.Key]
		if !ok {
			add(DiffItem{Key: item.Key, Kind: "connector", Status: DiffAdded, LocalRef: item.ProviderAddress, LocalHash: item.ManifestHash, Summary: "Connector exists locally but not in registered state."})
			continue
		}
		if item.ManifestHash != reg.ConnectorHash || item.ProviderAddress != reg.ProviderAddress || item.RuntimeKind != reg.RuntimeKind || len(item.Capabilities) != reg.CapabilityCount {
			add(DiffItem{Key: item.Key, Kind: "connector", Status: DiffChanged, LocalRef: item.ProviderAddress, RegisteredRef: reg.ProviderAddress, LocalHash: item.ManifestHash, RegisteredHash: reg.ConnectorHash, Summary: "Connector provider, runtime, capability count, or hash differs."})
			continue
		}
		add(DiffItem{Key: item.Key, Kind: "connector", Status: DiffUnchanged, LocalRef: item.ProviderAddress, RegisteredRef: reg.ProviderAddress, LocalHash: item.ManifestHash, RegisteredHash: reg.ConnectorHash, Summary: "Connector matches registered state."})
	}
	for key, item := range registered {
		if !seen[key] {
			add(DiffItem{Key: key, Kind: "connector", Status: DiffRemoved, RegisteredRef: item.ProviderAddress, RegisteredHash: item.ConnectorHash, Summary: "Registered connector is not present locally."})
		}
	}
}

func diffModules(report *DiffReport, add func(DiffItem), local []projectcontracts.ModuleFacetItem, detail *projects.ProjectRegistrationDetail) {
	registered := map[string]projects.ProjectModuleRegistration{}
	if detail != nil {
		for _, item := range detail.ModuleRegistrations {
			registered[item.ModuleKey] = item
		}
	}
	seen := map[string]bool{}
	for _, item := range local {
		seen[item.Key] = true
		reg, ok := registered[item.Key]
		if !ok {
			add(DiffItem{Key: item.Key, Kind: "module", Status: DiffAdded, LocalRef: item.ModuleID, LocalHash: item.PackageHash, Summary: "Module exists locally but not in registered state."})
			continue
		}
		if item.PackageHash != reg.ModulePackageHash || item.ManifestHash != reg.ModuleManifestHash || item.Version != reg.ModuleVersion {
			add(DiffItem{Key: item.Key, Kind: "module", Status: DiffChanged, LocalRef: item.ModuleID, RegisteredRef: reg.ModuleID, LocalHash: firstNonEmpty(item.PackageHash, item.ManifestHash), RegisteredHash: firstNonEmpty(reg.ModulePackageHash, reg.ModuleManifestHash), Summary: "Module package, manifest, or version differs."})
			continue
		}
		add(DiffItem{Key: item.Key, Kind: "module", Status: DiffUnchanged, LocalRef: item.ModuleID, RegisteredRef: reg.ModuleID, LocalHash: item.PackageHash, RegisteredHash: reg.ModulePackageHash, Summary: "Module matches registered state."})
	}
	for key, item := range registered {
		if !seen[key] {
			add(DiffItem{Key: key, Kind: "module", Status: DiffRemoved, RegisteredRef: item.ModuleID, RegisteredHash: item.ModulePackageHash, Summary: "Registered module is not present locally."})
		}
	}
}

func diffWorkflows(report *DiffReport, add func(DiffItem), local []projectcontracts.WorkflowFacetItem, detail *projects.ProjectRegistrationDetail) {
	registered := map[string]projectcontracts.WorkflowFacetItem{}
	registrations := map[string]projects.ProjectWorkflowRegistration{}
	if detail != nil && detail.Registration != nil {
		var plan projectcontracts.ProjectPlan
		if len(detail.Registration.RegistrationPlan) > 0 && json.Unmarshal(detail.Registration.RegistrationPlan, &plan) == nil {
			for _, item := range plan.Workflows {
				registered[firstNonEmpty(item.WorkflowID, item.Key)] = item
			}
		}
	}
	if detail != nil {
		for _, item := range detail.WorkflowRegistrations {
			key := firstNonEmpty(item.WorkflowKey, ptrStringValue(item.WorkflowID))
			if key != "" {
				registrations[key] = item
			}
		}
	}
	seen := map[string]bool{}
	for _, item := range local {
		key := firstNonEmpty(item.WorkflowID, item.Key)
		if key == "" {
			continue
		}
		seen[key] = true
		reg, ok := registered[key]
		row, rowOK := registrations[key]
		if !ok && !rowOK {
			add(DiffItem{Key: key, Kind: "workflow", Status: DiffAdded, LocalRef: item.ManifestPath, LocalHash: firstNonEmpty(item.PackageHash, item.ManifestHash), Summary: "Workflow exists locally but not in registered workflow state."})
			continue
		}
		registeredHash := ""
		registeredRef := ""
		registeredImplementation := ""
		registeredScriptCapability := ""
		if ok {
			registeredHash = reg.ManifestHash
			registeredRef = firstNonEmpty(reg.CapabilityAddress, reg.ScriptCapabilityAddress, reg.ManifestPath)
			registeredImplementation = reg.ImplementationKind
			registeredScriptCapability = reg.ScriptCapabilityAddress
		}
		if rowOK {
			registeredHash = firstNonEmpty(row.WorkflowManifestHash, registeredHash)
			registeredRef = firstNonEmpty(row.CapabilityAddress, registeredRef)
			registeredImplementation = firstNonEmpty(row.ImplementationKind, registeredImplementation)
		}
		if item.ManifestHash != registeredHash || item.ImplementationKind != registeredImplementation || item.ScriptCapabilityAddress != registeredScriptCapability {
			add(DiffItem{Key: key, Kind: "workflow", Status: DiffChanged, LocalRef: firstNonEmpty(item.CapabilityAddress, item.ManifestPath), RegisteredRef: registeredRef, LocalHash: firstNonEmpty(item.PackageHash, item.ManifestHash), RegisteredHash: registeredHash, Summary: "Workflow manifest, implementation, or callable registration differs."})
			continue
		}
		add(DiffItem{Key: key, Kind: "workflow", Status: DiffUnchanged, LocalRef: firstNonEmpty(item.CapabilityAddress, item.ManifestPath), RegisteredRef: registeredRef, LocalHash: firstNonEmpty(item.PackageHash, item.ManifestHash), RegisteredHash: registeredHash, Summary: "Workflow matches registered plan and runtime registration."})
	}
	for key, item := range registered {
		if !seen[key] {
			seen[key] = true
			add(DiffItem{Key: key, Kind: "workflow", Status: DiffRemoved, RegisteredRef: firstNonEmpty(item.CapabilityAddress, item.ManifestPath), RegisteredHash: item.ManifestHash, Summary: "Registered workflow is not present locally."})
		}
	}
	for key, item := range registrations {
		if !seen[key] {
			add(DiffItem{Key: key, Kind: "workflow", Status: DiffRemoved, RegisteredRef: firstNonEmpty(item.CapabilityAddress, ptrStringValue(item.WorkflowID), item.WorkflowManifestPath), RegisteredHash: item.WorkflowManifestHash, Summary: "Project workflow registration is not present locally."})
		}
	}
}

func summarizeDiff(items []DiffItem) DiffSummary {
	var summary DiffSummary
	for _, item := range items {
		switch item.Status {
		case DiffUnchanged:
			summary.Unchanged++
		case DiffAdded:
			summary.Added++
		case DiffRemoved:
			summary.Removed++
		case DiffChanged:
			summary.Changed++
		case DiffMissing:
			summary.Missing++
		}
	}
	return summary
}

func diffStatusRank(status DiffStatus) int {
	switch status {
	case DiffChanged:
		return 0
	case DiffAdded:
		return 1
	case DiffRemoved:
		return 2
	case DiffMissing:
		return 3
	case DiffUnchanged:
		return 4
	default:
		return 5
	}
}
