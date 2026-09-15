package portal

import (
	"encoding/json"
	"sort"
	"strings"

	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/workers"
)

type archivedProjectIndex struct {
	projectRefs     map[string]bool
	scopeRefs       map[string]bool
	addressRefs     map[string]bool
	addressPrefixes []string
}

func archivedProjectIndexFromProjects(projectsList []projects.Project) archivedProjectIndex {
	index := newArchivedProjectIndex()
	for _, project := range projectsList {
		if !projectRuntimeQuarantined(project) {
			continue
		}
		index.addProject(project)
	}
	return index
}

func projectRefIndexFromProjects(projectsList []projects.Project) archivedProjectIndex {
	index := newArchivedProjectIndex()
	for _, project := range projectsList {
		index.addProject(project)
	}
	return index
}

func newArchivedProjectIndex() archivedProjectIndex {
	index := archivedProjectIndex{
		projectRefs: map[string]bool{},
		scopeRefs:   map[string]bool{},
		addressRefs: map[string]bool{},
	}
	return index
}

func projectArchived(project projects.Project) bool {
	return strings.EqualFold(strings.TrimSpace(project.Status), "archived")
}

func projectRuntimeQuarantined(project projects.Project) bool {
	return projects.EnsureProjectMutable(project, "portal", "project") != nil
}

func projectRuntimeQuarantineReason(project projects.Project) string {
	if state, ok := projects.ParseProjectPhysicalArchiveState(project.ArchiveState); ok && state.Restore != nil {
		return "Project restore keeps runtime inactive; recovery and a separate activation/fence-release operation are required."
	}
	if projectArchived(project) {
		return "Archived projects must be restored before mutation; inspect the exact archive evidence before proceeding."
	}
	return "Project archive state blocks mutation; inspect the exact archive operation before proceeding."
}

func (index *archivedProjectIndex) addProject(project projects.Project) {
	if index.projectRefs == nil {
		index.projectRefs = map[string]bool{}
	}
	if index.scopeRefs == nil {
		index.scopeRefs = map[string]bool{}
	}
	if index.addressRefs == nil {
		index.addressRefs = map[string]bool{}
	}
	for _, ref := range []string{
		project.ProjectID,
		projectRef(project),
		project.Slug,
		project.Name,
	} {
		index.addProjectRef(ref)
	}
	for _, ref := range []string{
		project.ProjectScopeID,
		project.ProjectScopeKey,
	} {
		index.addScopeRef(ref)
	}
	node := strings.TrimSpace(ptrOrDash(project.HomeNodeID))
	slug := strings.TrimSpace(project.Slug)
	if node == "-" {
		node = ""
	}
	for _, ref := range []string{
		node + "@" + slug,
		"project." + slug,
		"project:" + slug,
	} {
		index.addAddressRef(ref)
	}
	for _, prefix := range []string{
		node + "@" + slug + ".",
		node + "." + slug + ".",
		slug + ".",
	} {
		index.addAddressPrefix(prefix)
	}
}

func (index *archivedProjectIndex) addProjectRef(ref string) {
	if normalized := normalizeArchiveRef(ref); normalized != "" {
		index.projectRefs[normalized] = true
	}
}

func (index *archivedProjectIndex) addScopeRef(ref string) {
	if normalized := normalizeArchiveRef(ref); normalized != "" {
		index.scopeRefs[normalized] = true
	}
}

func (index *archivedProjectIndex) addAddressRef(ref string) {
	normalized := normalizeArchiveRef(ref)
	if normalized == "" || strings.HasPrefix(normalized, "@") || strings.HasPrefix(normalized, ".") {
		return
	}
	index.addressRefs[normalized] = true
}

func (index *archivedProjectIndex) addAddressPrefix(prefix string) {
	normalized := normalizeArchiveRef(prefix)
	if normalized == "" || strings.HasPrefix(normalized, "@") || strings.HasPrefix(normalized, ".") {
		return
	}
	index.addressPrefixes = append(index.addressPrefixes, normalized)
}

func (index archivedProjectIndex) empty() bool {
	return len(index.projectRefs) == 0 && len(index.scopeRefs) == 0 && len(index.addressRefs) == 0 && len(index.addressPrefixes) == 0
}

func (index archivedProjectIndex) matchesProjectRef(ref string) bool {
	normalized := normalizeArchiveRef(ref)
	return normalized != "" && index.projectRefs[normalized]
}

func (index archivedProjectIndex) matchesProjectRefPtr(ref *string) bool {
	if ref == nil {
		return false
	}
	return index.matchesProjectRef(*ref)
}

func (index archivedProjectIndex) matchesScopeRef(ref string) bool {
	normalized := normalizeArchiveRef(ref)
	return normalized != "" && index.scopeRefs[normalized]
}

func (index archivedProjectIndex) matchesScopeRefPtr(ref *string) bool {
	if ref == nil {
		return false
	}
	return index.matchesScopeRef(*ref)
}

func (index archivedProjectIndex) matchesAddress(ref string) bool {
	normalized := normalizeArchiveRef(ref)
	if normalized == "" {
		return false
	}
	if index.matchesProjectRef(normalized) || index.matchesScopeRef(normalized) {
		return true
	}
	if index.addressRefs[normalized] {
		return true
	}
	for _, prefix := range index.addressPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

func (index archivedProjectIndex) matchesTargetProfile(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var profile automation.TargetProfile
	if err := json.Unmarshal(raw, &profile); err != nil {
		return false
	}
	return index.matchesProjectRef(profile.ProjectRef) ||
		index.matchesScopeRef(profile.ScopeRef) ||
		index.matchesAddress(profile.CapabilityRef)
}

func (index archivedProjectIndex) automationArchived(record automation.Automation) bool {
	return index.matchesProjectRefPtr(record.ProjectID) ||
		index.matchesScopeRefPtr(record.ScopeID) ||
		index.matchesTargetProfile(record.TargetProfileJSON)
}

func (index archivedProjectIndex) scheduleArchived(schedule automation.Schedule, archivedAutomationIDs map[string]bool) bool {
	return index.matchesProjectRefPtr(schedule.ProjectID) ||
		index.matchesScopeRefPtr(schedule.ScopeID) ||
		index.matchesTargetProfile(schedule.TargetProfileJSON) ||
		archivedAutomationIDs[normalizeArchiveRef(schedule.AutomationID)]
}

func (index archivedProjectIndex) capabilityArchived(capability capabilities.CapabilityListItem) bool {
	return index.matchesAddress(capability.CapabilityEndpoint.CompactAddress) ||
		index.matchesAddress(capability.ProviderAddress) ||
		index.matchesAddress(capability.ProviderKey) ||
		index.matchesAddress(capability.CapabilityEndpoint.ProviderID) ||
		index.matchesAddress(capability.CapabilityEndpoint.CapabilityEndpointID)
}

func (index archivedProjectIndex) jobArchived(job jobs.Job) bool {
	if index.matchesScopeRefPtr(job.ScopeID) {
		return true
	}
	if job.TargetKind != nil && strings.EqualFold(strings.TrimSpace(*job.TargetKind), "project") && index.matchesProjectRefPtr(job.TargetID) {
		return true
	}
	return index.matchesProjectRefPtr(job.TargetID) ||
		index.matchesAddress(stringPtrOrDash(job.ScriptID)) ||
		index.matchesAddress(stringPtrOrDash(job.WorkflowID)) ||
		index.matchesAddress(stringPtrOrDash(job.WorkdirPath))
}

func (index archivedProjectIndex) workerArchived(worker workers.WorkerListItem) bool {
	return index.matchesAddress(worker.WorkerKey) || index.matchesAddress(worker.WorkerInstanceID)
}

type archivedAutomationContext struct {
	index                  archivedProjectIndex
	knownProjects          archivedProjectIndex
	projectInventoryLoaded bool
	automations            map[string]bool
	schedules              map[string]bool
	directEndpoints        map[string]bool
}

func newArchivedAutomationContext(data AutomationsData) archivedAutomationContext {
	ctx := archivedAutomationContext{
		index:                  archivedProjectIndexFromProjects(data.ArchivedProjects),
		automations:            map[string]bool{},
		schedules:              map[string]bool{},
		directEndpoints:        map[string]bool{},
		projectInventoryLoaded: len(data.Projects) > 0 || len(data.StaleProjectRefs) > 0,
	}
	if len(data.Projects) > 0 {
		ctx.knownProjects = projectRefIndexFromProjects(data.Projects)
	}
	for _, ref := range data.StaleProjectRefs {
		ctx.index.addProjectRef(ref)
	}
	for _, automation := range data.Automations {
		if ctx.index.automationArchived(automation) || ctx.automationHasStaleProjectRef(automation) {
			setArchiveRef(ctx.automations, automation.AutomationID)
		}
	}
	for _, schedule := range data.Schedules {
		if ctx.index.scheduleArchived(schedule, ctx.automations) || ctx.scheduleHasStaleProjectRef(schedule) {
			setArchiveRef(ctx.schedules, schedule.ScheduleID)
			setArchiveRef(ctx.schedules, schedule.ScheduleKey)
			setArchiveRef(ctx.automations, schedule.AutomationID)
		}
	}
	for _, endpoint := range data.DirectEventEndpoints {
		if ctx.automations[normalizeArchiveRef(endpoint.AutomationID)] {
			setArchiveRef(ctx.directEndpoints, endpoint.EndpointID)
			setArchiveRef(ctx.directEndpoints, endpoint.EndpointSlug)
		}
	}
	return ctx
}

func (ctx archivedAutomationContext) scheduleArchived(schedule automation.Schedule) bool {
	return ctx.schedules[normalizeArchiveRef(schedule.ScheduleID)] ||
		ctx.schedules[normalizeArchiveRef(schedule.ScheduleKey)] ||
		ctx.index.scheduleArchived(schedule, ctx.automations)
}

func (ctx archivedAutomationContext) directEndpointArchived(endpoint automation.DirectEventEndpoint) bool {
	return ctx.directEndpoints[normalizeArchiveRef(endpoint.EndpointID)] ||
		ctx.directEndpoints[normalizeArchiveRef(endpoint.EndpointSlug)] ||
		ctx.automations[normalizeArchiveRef(endpoint.AutomationID)]
}

func (ctx archivedAutomationContext) automationHasStaleProjectRef(record automation.Automation) bool {
	return ctx.projectRefIsStale(record.ProjectID) ||
		ctx.targetProfileHasStaleProjectRef(record.TargetProfileJSON)
}

func (ctx archivedAutomationContext) scheduleHasStaleProjectRef(schedule automation.Schedule) bool {
	return ctx.projectRefIsStale(schedule.ProjectID) ||
		ctx.targetProfileHasStaleProjectRef(schedule.TargetProfileJSON)
}

func (ctx archivedAutomationContext) projectRefIsStale(ref *string) bool {
	if !ctx.projectInventoryLoaded || ref == nil {
		return false
	}
	return ctx.projectRefValueIsStale(*ref)
}

func (ctx archivedAutomationContext) targetProfileHasStaleProjectRef(raw json.RawMessage) bool {
	if !ctx.projectInventoryLoaded || len(raw) == 0 {
		return false
	}
	var profile automation.TargetProfile
	if err := json.Unmarshal(raw, &profile); err != nil {
		return false
	}
	return ctx.projectRefValueIsStale(profile.ProjectRef)
}

func (ctx archivedAutomationContext) projectRefValueIsStale(ref string) bool {
	normalized := normalizeArchiveRef(ref)
	if normalized == "" {
		return false
	}
	if ctx.knownProjects.empty() {
		return true
	}
	return !ctx.knownProjects.matchesProjectRef(normalized)
}

func staleAutomationProjectRefs(data AutomationsData) []string {
	if len(data.StaleProjectRefs) > 0 {
		return normalizedUniqueRefs(data.StaleProjectRefs)
	}
	if len(data.Projects) == 0 {
		return nil
	}
	knownProjects := projectRefIndexFromProjects(data.Projects)
	stale := map[string]bool{}
	addIfStale := func(ref string) {
		normalized := normalizeArchiveRef(ref)
		if normalized == "" || knownProjects.matchesProjectRef(normalized) {
			return
		}
		stale[normalized] = true
	}
	addProfileProjectRef := func(raw json.RawMessage) {
		if len(raw) == 0 {
			return
		}
		var profile automation.TargetProfile
		if err := json.Unmarshal(raw, &profile); err != nil {
			return
		}
		addIfStale(profile.ProjectRef)
	}
	for _, automation := range data.Automations {
		addIfStale(automationProjectRefValue(automation.ProjectID))
		addProfileProjectRef(automation.TargetProfileJSON)
	}
	for _, schedule := range data.Schedules {
		addIfStale(automationProjectRefValue(schedule.ProjectID))
		addProfileProjectRef(schedule.TargetProfileJSON)
	}
	for _, invocation := range data.Invocations {
		addIfStale(automationProjectRefValue(invocation.ProjectID))
	}
	for _, invocation := range data.InvocationFailures {
		addIfStale(automationProjectRefValue(invocation.ProjectID))
	}
	return normalizedUniqueRefsFromMap(stale)
}

func automationProjectRefValue(ref *string) string {
	if ref == nil {
		return ""
	}
	return *ref
}

func normalizedUniqueRefs(refs []string) []string {
	values := map[string]bool{}
	for _, ref := range refs {
		if normalized := normalizeArchiveRef(ref); normalized != "" {
			values[normalized] = true
		}
	}
	return normalizedUniqueRefsFromMap(values)
}

func normalizedUniqueRefsFromMap(values map[string]bool) []string {
	refs := make([]string, 0, len(values))
	for ref := range values {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs
}

func normalizeArchiveRef(ref string) string {
	return strings.ToLower(strings.TrimSpace(ref))
}

func setArchiveRef(values map[string]bool, ref string) {
	if normalized := normalizeArchiveRef(ref); normalized != "" {
		values[normalized] = true
	}
}
