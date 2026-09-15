package portal

import (
	"fmt"
	"strings"
	"unicode"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/lane"
	"loom.local/loom/internal/loomcli/actions"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/search"
	loomsync "loom.local/loom/internal/sync"
	"loom.local/loom/internal/update"
	"loom.local/loom/internal/workers"
)

const (
	SelectableKindRecord = "record"
	SelectableKindAction = "action"
)

const (
	portalActionGroupRecordKind       = "portal_action_group"
	portalActionGroupActionRecordKind = "portal_action_group_action"
	portalActionDirectRecordKind      = "portal_action_direct"
)

type portalActionPresentation string

const (
	portalActionPresentationPrimary    portalActionPresentation = "primary_user"
	portalActionPresentationContextual portalActionPresentation = "contextual_maintenance"
	portalActionPresentationOperator   portalActionPresentation = "operator_debug"
)

type SelectableItem struct {
	Kind               string
	Label              string
	Description        string
	Screen             string
	RowIndex           int
	ActionID           string
	ActionSection      string
	ActionSectionLabel string
	RecordKind         string
	RecordRef          string
	RecordLabel        string
	PrimaryAction      *PortalAction
	RelatedActions     []PortalAction
	DisabledReason     string
}

type portalActionSectionDefinition struct {
	Key   string
	Label string
}

type portalActionGroupDefinition struct {
	Key         string
	SectionKey  string
	Label       string
	Description string
}

func ScreenActionItems(screen string) []SelectableItem {
	return StaticScreenActionItems(screen)
}

func StaticScreenActionItems(screen string) []SelectableItem {
	screen = NormalizeScreen(screen)
	actionIDs := []string{}
	switch screen {
	case ScreenBackground:
		actionIDs = []string{
			"worker.selfcheck.run_once",
			"worker.indexer_text.run_once",
		}
	case ScreenAutomations:
		actionIDs = []string{
			"worker.automation_scheduler.run_once",
			"worker.automation_dispatcher.run_once",
			"worker.direct_event_ingest.run_once",
		}
	case ScreenJobs:
		actionIDs = []string{
			"worker.indexer_text.run_once",
			"worker.job_runner.run_once",
			"worker.job_sweeper.run_once",
		}
	}
	items := make([]SelectableItem, 0, len(actionIDs))
	registry := actions.DefaultRegistry()
	for idx, actionID := range actionIDs {
		action, ok := registry.Get(actionID)
		if !ok {
			continue
		}
		portalAction := PortalActionFromRegistry(action)
		items = append(items, SelectableItem{
			Kind:          SelectableKindAction,
			Label:         portalAction.Label,
			Screen:        screen,
			RowIndex:      idx,
			ActionID:      actionID,
			RecordRef:     portalAction.TargetRef,
			PrimaryAction: actionPtr(portalAction),
		})
	}
	return items
}

func ScreenSelectableItems(state ScreenState) []SelectableItem {
	records := ScreenRecordItems(state)
	if NormalizeScreen(state.Screen) == ScreenCapabilities {
		return records
	}
	actions := screenTopAvailableActionsForRecords(state, records)
	items := make([]SelectableItem, 0, len(records)+len(actions))
	for _, action := range actions {
		action := action
		items = append(items, SelectableItem{
			Kind:           SelectableKindAction,
			Label:          action.Label,
			Description:    action.Description,
			Screen:         NormalizeScreen(state.Screen),
			RowIndex:       len(items),
			ActionID:       action.ID,
			RecordKind:     "portal_action",
			RecordRef:      action.TargetRef,
			RecordLabel:    firstNonEmpty(action.TargetLabel, action.TargetRef),
			PrimaryAction:  actionPtr(action),
			DisabledReason: action.DisabledReason,
		})
	}
	for _, item := range records {
		item.RowIndex = len(items)
		items = append(items, item)
	}
	return items
}

func ScreenRecordItems(state ScreenState) []SelectableItem {
	state.Screen = NormalizeScreen(state.Screen)
	var items []SelectableItem
	switch state.Screen {
	case ScreenHome:
		items = attentionSelectableItems(state)
	case ScreenDoctor:
		items = doctorSelectableItems(state)
	case ScreenTimeline:
		items = timelineSelectableItems(state)
	case ScreenBox:
		items = boxSelectableItems(state)
	case ScreenStorage:
		items = storageSelectableItems(state)
	case ScreenBackground:
		items = backgroundSelectableItems(state)
	case ScreenProjects:
		items = projectsSelectableItems(state)
	case ScreenServices:
		items = servicesSelectableItems(state)
	case ScreenAutomations:
		items = automationsSelectableItems(state)
	case ScreenJobs:
		items = jobsSelectableItems(state)
	case ScreenNotes:
		items = notesSelectableItems(state)
	case ScreenNodes:
		items = nodesSelectableItems(state)
	case ScreenCapabilities:
		items = capabilitiesSelectableItems(state)
	case ScreenDatabase:
		items = databaseSelectableItems(state)
	default:
		return nil
	}
	return applyMainAvailabilityToSelectableItems(items, state.MainAvailability)
}

func applyMainAvailabilityToSelectableItems(items []SelectableItem, availability MainAvailability) []SelectableItem {
	for index := range items {
		if items[index].PrimaryAction != nil {
			action := applyMainAvailabilityToAction(*items[index].PrimaryAction, availability)
			items[index].PrimaryAction = actionPtr(action)
			items[index].DisabledReason = firstNonEmpty(items[index].DisabledReason, action.DisabledReason)
		}
		for actionIndex := range items[index].RelatedActions {
			items[index].RelatedActions[actionIndex] = applyMainAvailabilityToAction(items[index].RelatedActions[actionIndex], availability)
		}
	}
	return items
}

func screenActionOffset(state ScreenState) int {
	if NormalizeScreen(state.Screen) == ScreenCapabilities {
		return 0
	}
	records := ScreenRecordItems(state)
	return len(screenTopAvailableActionsForRecords(state, records))
}

func selectedRecordItem(state ScreenState) (SelectableItem, bool) {
	records := ScreenRecordItems(state)
	if len(records) == 0 {
		return SelectableItem{}, false
	}
	index := state.SelectedIndex - screenActionOffset(state)
	if index < 0 || index >= len(records) {
		return SelectableItem{}, false
	}
	return records[index], true
}

func recordVisualIndex(state ScreenState, recordIndex int) int {
	return screenActionOffset(state) + recordIndex
}

func ScreenAvailableActions(state ScreenState) []PortalAction {
	return ScreenAvailableActionsForRecords(state, ScreenRecordItems(state))
}

func screenTopAvailableActionsForRecords(state ScreenState, records []SelectableItem) []PortalAction {
	actions := ScreenAvailableActionsForRecords(state, records)
	if state.RawDetails {
		return actions
	}
	if operationalActionGroupsSupported(state.Screen) {
		return nil
	}
	result := make([]PortalAction, 0, len(actions))
	for _, action := range actions {
		if portalActionVisibleInTopArea(state, action) {
			result = append(result, action)
		}
	}
	return result
}

func operationalActionGroupsSupported(screen string) bool {
	switch NormalizeScreen(screen) {
	case ScreenBox, ScreenStorage, ScreenBackground, ScreenProjects, ScreenServices, ScreenAutomations, ScreenJobs, ScreenNotes, ScreenNodes, ScreenDatabase:
		return true
	default:
		return false
	}
}

func withOperationalActionGroups(state ScreenState, records []SelectableItem) []SelectableItem {
	return withOperationalActionGroupsFromCandidates(state, records, records)
}

func withOperationalActionGroupsFromCandidates(state ScreenState, records []SelectableItem, candidates []SelectableItem) []SelectableItem {
	screen := NormalizeScreen(state.Screen)
	if state.RawDetails || !operationalActionGroupsSupported(screen) {
		return normalizeSelectableRows(records)
	}
	actions := operationalActionGroupCandidateActions(state, candidates)
	topActionIDs := portalActionIDSet(ScreenAvailableActionsForRecords(state, candidates))
	groups := operationalActionGroupItems(state, screen, state.ExpandedActionGroup, actions, topActionIDs)
	if len(groups) == 0 {
		return normalizeSelectableRows(records)
	}
	result := make([]SelectableItem, 0, len(groups)+len(records))
	result = append(result, groups...)
	result = append(result, records...)
	return normalizeSelectableRows(result)
}

func operationalActionGroupCandidateActions(state ScreenState, records []SelectableItem) []PortalAction {
	seen := map[string]bool{}
	result := []PortalAction{}
	add := func(action PortalAction) {
		if strings.TrimSpace(action.ID) == "" || seen[action.ID] {
			return
		}
		seen[action.ID] = true
		result = append(result, action)
	}
	for _, item := range records {
		if item.PrimaryAction != nil {
			add(*item.PrimaryAction)
		}
		for _, action := range item.RelatedActions {
			add(action)
		}
	}
	for _, action := range ScreenAvailableActionsForRecords(state, records) {
		add(action)
	}
	return result
}

func operationalActionGroupItems(state ScreenState, screen string, expandedGroup string, actions []PortalAction, topActionIDs map[string]bool) []SelectableItem {
	definitions := operationalActionGroupDefinitions(screen)
	sections := operationalActionSectionDefinitions(screen)
	if len(definitions) == 0 && len(sections) == 0 {
		return nil
	}
	definitionByKey := portalActionGroupDefinitionByKey(definitions)
	sectionByKey := portalActionSectionDefinitionByKey(sections)
	byKey := map[string][]PortalAction{}
	directBySection := map[string][]PortalAction{}
	for _, action := range actions {
		key := operationalActionGroupKeyForAction(screen, action)
		if key != "" {
			if _, ok := definitionByKey[key]; !ok {
				key = ""
			}
		}
		if key != "" {
			byKey[key] = append(byKey[key], action)
			continue
		}
		if !topActionIDs[action.ID] || !portalActionVisibleInTopArea(state, action) {
			continue
		}
		section := operationalActionSectionForAction(screen, action)
		if section == "" {
			section = portalActionDefaultSectionKey(screen)
		}
		if _, ok := sectionByKey[section]; !ok {
			definition := portalActionSectionDefinition{Key: section, Label: portalActionSectionLabel(section)}
			sectionByKey[section] = definition
			sections = append(sections, definition)
		}
		directBySection[section] = append(directBySection[section], action)
	}
	items := []SelectableItem{}
	for _, section := range sections {
		for _, definition := range definitions {
			if definition.SectionKey != section.Key {
				continue
			}
			groupActions := byKey[definition.Key]
			if len(groupActions) == 0 {
				continue
			}
			ref := operationalActionGroupRef(screen, definition.Key)
			items = append(items, SelectableItem{
				Kind:               SelectableKindRecord,
				Label:              definition.Label,
				Description:        definition.Description,
				Screen:             screen,
				ActionSection:      section.Key,
				ActionSectionLabel: section.Label,
				RecordKind:         portalActionGroupRecordKind,
				RecordRef:          ref,
				RecordLabel:        definition.Label,
				RelatedActions:     groupActions,
			})
			if expandedGroup == ref {
				for _, action := range groupActions {
					items = append(items, operationalInlineActionItem(screen, ref, section, action))
				}
			}
		}
		for _, action := range directBySection[section.Key] {
			items = append(items, operationalDirectActionItem(screen, section, action))
		}
	}
	return items
}

func portalActionIDSet(actions []PortalAction) map[string]bool {
	result := map[string]bool{}
	for _, action := range actions {
		if strings.TrimSpace(action.ID) == "" {
			continue
		}
		result[action.ID] = true
	}
	return result
}

func portalActionGroupDefinitionByKey(definitions []portalActionGroupDefinition) map[string]portalActionGroupDefinition {
	result := map[string]portalActionGroupDefinition{}
	for _, definition := range definitions {
		result[definition.Key] = definition
	}
	return result
}

func portalActionSectionDefinitionByKey(definitions []portalActionSectionDefinition) map[string]portalActionSectionDefinition {
	result := map[string]portalActionSectionDefinition{}
	for _, definition := range definitions {
		result[definition.Key] = definition
	}
	return result
}

func operationalActionSectionDefinitions(screen string) []portalActionSectionDefinition {
	switch NormalizeScreen(screen) {
	case ScreenBox:
		return []portalActionSectionDefinition{
			{Key: "box_setup", Label: "Box Setup"},
			{Key: "watch_policy", Label: "Watch Policy"},
			{Key: "projects", Label: "Projects"},
			{Key: "loom_lane", Label: "LOOM Lane"},
		}
	case ScreenStorage:
		return []portalActionSectionDefinition{
			{Key: "storage_view", Label: "Physical Roots And Catalog"},
			{Key: "protection", Label: "Protection"},
			{Key: "cloud", Label: "Cloud"},
			{Key: "mac_mount", Label: "Mac Mount"},
			{Key: "archives_recovery", Label: "Archives And Recovery"},
		}
	case ScreenBackground:
		return []portalActionSectionDefinition{
			{Key: "workers", Label: "Workers"},
			{Key: "backups", Label: "Backups"},
			{Key: "cloud", Label: "Cloud"},
			{Key: "maintenance", Label: "Database Maintenance"},
			{Key: "object_store", Label: "Object Store Maintenance"},
			{Key: "logs", Label: "Logs"},
			{Key: "inspect", Label: "Inspect"},
		}
	case ScreenProjects:
		return []portalActionSectionDefinition{
			{Key: "lifecycle", Label: "Lifecycle"},
			{Key: "exports", Label: "Project Export"},
			{Key: "facets", Label: "Facets"},
			{Key: "contracts", Label: "Contracts"},
			{Key: "storage_notes", Label: "Storage And Notes"},
			{Key: "health", Label: "Health"},
		}
	case ScreenServices:
		return []portalActionSectionDefinition{
			{Key: "lifecycle", Label: "Lifecycle"},
			{Key: "inspect", Label: "Inspect"},
			{Key: "diagnostics", Label: "Diagnostics"},
		}
	case ScreenAutomations:
		return []portalActionSectionDefinition{
			{Key: "run_workers", Label: "Run Workers"},
			{Key: "controls", Label: "Controls"},
			{Key: "inspect", Label: "Inspect"},
			{Key: "logs_payloads", Label: "Logs And Payloads"},
		}
	case ScreenJobs:
		return []portalActionSectionDefinition{
			{Key: "run_workers", Label: "Run Workers"},
			{Key: "search_indexing", Label: "Search Indexing"},
			{Key: "repair", Label: "Repair"},
			{Key: "inspect", Label: "Inspect"},
			{Key: "logs_outputs", Label: "Logs And Outputs"},
		}
	case ScreenNotes:
		return []portalActionSectionDefinition{
			{Key: "search", Label: "Search"},
			{Key: "process", Label: "Process"},
			{Key: "inspect", Label: "Inspect"},
			{Key: "policies", Label: "Policies"},
			{Key: "repair", Label: "Repair"},
			{Key: "projection", Label: "Projection"},
		}
	case ScreenNodes:
		return []portalActionSectionDefinition{
			{Key: "protected_folders", Label: "Protected Folders"},
			{Key: "node_health", Label: "Node Health"},
			{Key: "watched_roots", Label: "Watched Roots"},
			{Key: "sync", Label: "Sync"},
			{Key: "private_backups", Label: "Private Backups"},
			{Key: "deletion_requests", Label: "Deletion Requests"},
			{Key: "diagnostics", Label: "Diagnostics"},
		}
	case ScreenDatabase:
		return []portalActionSectionDefinition{
			{Key: "search_indexing", Label: "Search And Indexing"},
			{Key: "objects", Label: "Objects"},
			{Key: "deletion_requests", Label: "Deletion Requests"},
			{Key: "sync_diagnostics", Label: "Sync Diagnostics"},
			{Key: "workers", Label: "Workers"},
			{Key: "raw_diagnostics", Label: "Raw Diagnostics"},
		}
	default:
		return nil
	}
}

func portalActionDefaultSectionKey(screen string) string {
	sections := operationalActionSectionDefinitions(screen)
	if len(sections) == 0 {
		return "actions"
	}
	return sections[0].Key
}

func portalActionSectionLabel(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return "Actions"
	}
	parts := strings.FieldsFunc(key, func(r rune) bool {
		return r == '_' || r == '-' || r == '.'
	})
	for idx, part := range parts {
		parts[idx] = titleFromToken(part)
	}
	return strings.Join(parts, " ")
}

func operationalActionGroupDefinitions(screen string) []portalActionGroupDefinition {
	switch NormalizeScreen(screen) {
	case ScreenBox:
		return []portalActionGroupDefinition{
			{Key: "lane_send", SectionKey: "loom_lane", Label: "Send Lane...", Description: "Inspect the transfer plan or choose source-only and exact policy modes."},
			{Key: "loom_lane", SectionKey: "loom_lane", Label: "Lane Records...", Description: "Acknowledge or archive lane transfer records."},
		}
	case ScreenProjects:
		return []portalActionGroupDefinition{
			{Key: "project_export", SectionKey: "exports", Label: "Export Project...", Description: "Create human, portable, or archival tar exports locally or download them from main."},
		}
	case ScreenServices:
		return []portalActionGroupDefinition{
			{Key: "service_lifecycle", SectionKey: "lifecycle", Label: "Lifecycle...", Description: "Start, stop, or restart an allowlisted service."},
			{Key: "service_inspect", SectionKey: "inspect", Label: "Inspect...", Description: "Inspect service status and registration state."},
			{Key: "service_diagnostics", SectionKey: "diagnostics", Label: "Diagnostics...", Description: "Read bounded, redacted service logs."},
		}
	case ScreenStorage:
		return []portalActionGroupDefinition{
			{Key: "protection_checks", SectionKey: "protection", Label: "Protection Checks...", Description: "Inspect safety, retention, and safe-delete checks."},
			{Key: "archives_recovery", SectionKey: "archives_recovery", Label: "Archives And Recovery...", Description: "Fetch, restore, archive, or inspect retained storage entries."},
		}
	case ScreenBackground:
		return []portalActionGroupDefinition{
			{Key: "run", SectionKey: "workers", Label: "Run...", Description: "Run maintenance jobs or workers."},
			{Key: "check", SectionKey: "backups", Label: "Check...", Description: "Verify backups and refresh live cloud status."},
			{Key: "logs", SectionKey: "logs", Label: "Logs...", Description: "Inspect worker run history."},
			{Key: "inspect", SectionKey: "inspect", Label: "Inspect...", Description: "Inspect operational records and status."},
		}
	case ScreenJobs:
		return []portalActionGroupDefinition{
			{Key: "run", SectionKey: "run_workers", Label: "Run...", Description: "Run job and index workers."},
			{Key: "indexing", SectionKey: "search_indexing", Label: "Indexing...", Description: "Inspect and explain search index work."},
			{Key: "retry", SectionKey: "repair", Label: "Repair...", Description: "Retry, cancel, acknowledge, or archive job and index work."},
			{Key: "check", SectionKey: "inspect", Label: "Check...", Description: "Inspect jobs, runners, and index state."},
			{Key: "logs", SectionKey: "logs_outputs", Label: "Logs...", Description: "Inspect job logs, outputs, and worker run history."},
		}
	case ScreenAutomations:
		return []portalActionGroupDefinition{
			{Key: "run", SectionKey: "run_workers", Label: "Run...", Description: "Run automation workers."},
			{Key: "controls", SectionKey: "controls", Label: "Controls...", Description: "Fire, pause, resume, or test automation inputs."},
			{Key: "check", SectionKey: "inspect", Label: "Check...", Description: "Inspect schedules, endpoints, and automation records."},
			{Key: "logs", SectionKey: "logs_payloads", Label: "Logs...", Description: "Inspect fires, events, invocations, and payloads."},
		}
	case ScreenNotes:
		return []portalActionGroupDefinition{
			{Key: "process", SectionKey: "process", Label: "Process...", Description: "Run the coordinator or globally admitted heavy executor."},
			{Key: "inspect", SectionKey: "inspect", Label: "Inspect...", Description: "Inspect pipeline, stage, root, object, projection, and reconciliation state."},
			{Key: "policies", SectionKey: "policies", Label: "Policies...", Description: "Inspect or change OCR, image-description, and embedding policy."},
			{Key: "repair", SectionKey: "repair", Label: "Repair...", Description: "Inspect failures before retrying bounded pipeline work."},
		}
	case ScreenNodes:
		return []portalActionGroupDefinition{
			{Key: "protected_folders", SectionKey: "protected_folders", Label: "Protection Controls...", Description: "Inspect, enable, disable, recheck, retry, or delete protected-folder contracts."},
			{Key: "node_health", SectionKey: "node_health", Label: "Node Health...", Description: "Inspect nodes and node health."},
			{Key: "watched_roots", SectionKey: "watched_roots", Label: "Watched Roots...", Description: "Inspect watched roots and root findings."},
			{Key: "sync", SectionKey: "sync", Label: "Sync...", Description: "Inspect sync batches, conflicts, and replicas."},
			{Key: "private_backups", SectionKey: "private_backups", Label: "Private Backups...", Description: "Inspect private backup operations and batches."},
			{Key: "deletion_requests", SectionKey: "deletion_requests", Label: "Deletion Requests...", Description: "Review, approve, deny, complete, or inspect deletion requests."},
			{Key: "diagnostics", SectionKey: "diagnostics", Label: "Diagnostics...", Description: "Inspect node summary and diagnostic records."},
		}
	case ScreenDatabase:
		return []portalActionGroupDefinition{
			{Key: "indexing", SectionKey: "search_indexing", Label: "Indexing...", Description: "Run, retry, explain, rebuild, or inspect text index work."},
			{Key: "objects", SectionKey: "objects", Label: "Inspect Objects...", Description: "Inspect object metadata and object search result records."},
			{Key: "deletion_requests", SectionKey: "deletion_requests", Label: "Deletion Requests...", Description: "Review, approve, deny, complete, or inspect deletion requests."},
			{Key: "sync", SectionKey: "sync_diagnostics", Label: "Sync Diagnostics...", Description: "Inspect sync conflicts, batches, replicas, and private backups."},
			{Key: "workers", SectionKey: "workers", Label: "Run Workers...", Description: "Run database or index workers."},
			{Key: "raw_diagnostics", SectionKey: "raw_diagnostics", Label: "Raw Diagnostics...", Description: "Inspect raw object-store diagnostic records."},
		}
	default:
		return nil
	}
}

func operationalActionSectionForAction(screen string, action PortalAction) string {
	screen = NormalizeScreen(screen)
	switch screen {
	case ScreenBox:
		switch action.Executor.Kind {
		case PortalExecutorBoxInit, PortalExecutorStorageInspect:
			return "box_setup"
		case PortalExecutorBoxWatchApply:
			return "watch_policy"
		case PortalExecutorBoxProjectScaffold:
			return "projects"
		case PortalExecutorBoxLaneSend, PortalExecutorBoxLanePlan, PortalExecutorBoxLanePendingAck, PortalExecutorBoxLaneTransferAck, PortalExecutorBoxLaneTransferArchive:
			return "loom_lane"
		case PortalExecutorBoxIgnoreInspect:
			return "watch_policy"
		case PortalExecutorRecordInspect:
			if strings.Contains(action.ID, "watch") || strings.Contains(action.TargetKind, "watch") {
				return "watch_policy"
			}
		}
	case ScreenStorage:
		switch action.Executor.Kind {
		case PortalExecutorStorageRetentionStatus, PortalExecutorMainDocumentsStatus, PortalExecutorStorageSafeToDelete:
			return "protection"
		case PortalExecutorCloudCooldownInspect, PortalExecutorCloudStatusLive:
			return "cloud"
		case PortalExecutorStorageFetch, PortalExecutorStorageRestore, PortalExecutorStorageArchive, PortalExecutorStorageInspect:
			return "archives_recovery"
		}
		if strings.Contains(strings.ToLower(action.ID), "mount") || strings.Contains(strings.ToLower(action.Label), "mount") {
			return "mac_mount"
		}
	case ScreenProjects:
		switch action.Executor.Kind {
		case PortalExecutorProjectExportLocal, PortalExecutorProjectExportBackend:
			return "exports"
		case PortalExecutorProjectAddFacet, PortalExecutorProjectFacetInspect:
			return "facets"
		case PortalExecutorProjectActivate, PortalExecutorProjectDeactivate:
			if strings.Contains(action.ID, "facet") || strings.Contains(action.TargetKind, "facet") {
				return "facets"
			}
			return "lifecycle"
		case PortalExecutorProjectValidateLocal, PortalExecutorProjectValidateBackend, PortalExecutorProjectRegisterBackend, PortalExecutorProjectRegistrationPlan:
			return "contracts"
		case PortalExecutorProjectWatchPlan, PortalExecutorProjectSyncStatus, PortalExecutorProjectBackupStatus:
			return "storage_notes"
		case PortalExecutorProjectDoctor, PortalExecutorProjectDiff, PortalExecutorProjectAutomationHealth:
			return "health"
		case PortalExecutorProjectInspect, PortalExecutorProjectArchiveInspect, PortalExecutorProjectArchiveRestore, PortalExecutorProjectScaffoldBackend, PortalExecutorProjectScaffoldCleanup, PortalExecutorProjectArchive:
			return "lifecycle"
		}
	case ScreenServices:
		switch serviceOperationFromAction(action) {
		case "start", "stop", "restart":
			return "lifecycle"
		case "logs":
			return "diagnostics"
		default:
			return "inspect"
		}
	case ScreenNotes:
		if strings.HasPrefix(action.ID, "notes.pipeline.process.") {
			return "process"
		}
		if strings.HasPrefix(action.ID, "notes.pipeline.policy.") || strings.HasPrefix(action.ID, "notes.embeddings.") {
			return "policies"
		}
		if strings.HasPrefix(action.ID, "notes.pipeline.repair.") {
			return "repair"
		}
		if strings.HasPrefix(action.ID, "notes.pipeline.inspect.") {
			return "inspect"
		}
		switch action.Executor.Kind {
		case PortalExecutorNotesEmbeddingsToggle:
			return "policies"
		case PortalExecutorWorkerRunOnce:
			return "process"
		case PortalExecutorIndexRetry, PortalExecutorIndexRetryFailed, PortalExecutorIndexRebuildObject:
			return "repair"
		case PortalExecutorIndexInspect:
			return "inspect"
		case PortalExecutorRecordInspect, PortalExecutorObjectInspect:
			return "inspect"
		}
	case ScreenDatabase:
		switch action.Executor.Kind {
		case PortalExecutorWorkerRunOnce:
			return "workers"
		case PortalExecutorIndexInspect, PortalExecutorIndexExplainObject, PortalExecutorIndexRetry, PortalExecutorIndexRetryFailed, PortalExecutorIndexRebuildObject:
			return "search_indexing"
		case PortalExecutorObjectInspect:
			return "objects"
		case PortalExecutorDeletionRequestInspect, PortalExecutorDeletionRequestReview, PortalExecutorDeletionRequestApprove, PortalExecutorDeletionRequestDeny, PortalExecutorDeletionRequestComplete:
			return "deletion_requests"
		case PortalExecutorRecordInspect:
			switch action.TargetKind {
			case "sync_batch", "sync_conflict", "sync_replica", "private_backup":
				return "sync_diagnostics"
			default:
				return "raw_diagnostics"
			}
		}
	case ScreenNodes:
		switch action.Executor.Kind {
		case PortalExecutorProtectedFolderProtect, PortalExecutorProtectedFolderInspect, PortalExecutorProtectedFolderEnable, PortalExecutorProtectedFolderDisable, PortalExecutorProtectedFolderRecheck, PortalExecutorProtectedFolderRetry, PortalExecutorProtectedFolderDelete:
			return "protected_folders"
		case PortalExecutorNodeInspect, PortalExecutorNodeHealth:
			return "node_health"
		case PortalExecutorDeletionRequestInspect, PortalExecutorDeletionRequestReview, PortalExecutorDeletionRequestApprove, PortalExecutorDeletionRequestDeny, PortalExecutorDeletionRequestComplete:
			return "deletion_requests"
		case PortalExecutorRecordInspect:
			switch action.TargetKind {
			case "watched_root", "watched_root_finding", "watched_root_finding_group", "watched_root_backup_status", "watched_root_backup_batch":
				return "watched_roots"
			case "sync_batch", "sync_conflict", "sync_replica":
				return "sync"
			case "private_backup":
				return "private_backups"
			default:
				return "diagnostics"
			}
		}
	case ScreenBackground, ScreenAutomations, ScreenJobs:
		key := operationalActionGroupKeyForAction(screen, action)
		if key != "" {
			for _, definition := range operationalActionGroupDefinitions(screen) {
				if definition.Key == key {
					return definition.SectionKey
				}
			}
		}
	}
	return portalActionDefaultSectionKey(screen)
}

func operationalActionGroupKeyForAction(screen string, action PortalAction) string {
	switch NormalizeScreen(screen) {
	case ScreenBox:
		switch action.Executor.Kind {
		case PortalExecutorBoxLanePlan:
			return "lane_send"
		case PortalExecutorBoxLaneSend:
			if action.Executor.Payload["lane_profile"] != string(filepolicy.ProfileFaithful) {
				return "lane_send"
			}
		case PortalExecutorBoxLanePendingAck, PortalExecutorBoxLaneTransferAck, PortalExecutorBoxLaneTransferArchive:
			return "loom_lane"
		}
	case ScreenProjects:
		switch action.Executor.Kind {
		case PortalExecutorProjectExportLocal, PortalExecutorProjectExportBackend:
			return "project_export"
		}
	case ScreenServices:
		switch serviceOperationFromAction(action) {
		case "start", "stop", "restart":
			return "service_lifecycle"
		case "logs":
			return "service_diagnostics"
		default:
			return "service_inspect"
		}
	case ScreenStorage:
		switch action.Executor.Kind {
		case PortalExecutorStorageSafeToDelete:
			return "protection_checks"
		case PortalExecutorStorageFetch, PortalExecutorStorageRestore, PortalExecutorStorageArchive:
			return "archives_recovery"
		}
	case ScreenBackground:
		switch action.Executor.Kind {
		case PortalExecutorWorkerRunOnce, PortalExecutorMaintenanceBackupRun, PortalExecutorMaintenanceObjectScan:
			return "run"
		case PortalExecutorMaintenanceBackupVerify, PortalExecutorCloudStatusLive:
			return "check"
		case PortalExecutorWorkerRunsInspect:
			return "logs"
		case PortalExecutorWorkerInspect, PortalExecutorMaintenanceInspect, PortalExecutorCloudCooldownInspect, PortalExecutorObjectInspect, PortalExecutorRecordInspect:
			return "inspect"
		}
	case ScreenJobs:
		switch action.Executor.Kind {
		case PortalExecutorWorkerRunOnce:
			return "run"
		case PortalExecutorIndexInspect, PortalExecutorIndexExplainObject:
			return "indexing"
		case PortalExecutorJobRetry, PortalExecutorJobCancel, PortalExecutorJobAttentionAcknowledge, PortalExecutorJobAttentionArchive, PortalExecutorIndexRetry, PortalExecutorIndexRetryFailed, PortalExecutorIndexRebuildObject:
			return "retry"
		case PortalExecutorJobInspect, PortalExecutorWorkerInspect, PortalExecutorRecordInspect:
			return "check"
		case PortalExecutorJobLogsInspect, PortalExecutorJobOutputsInspect, PortalExecutorWorkerRunsInspect:
			return "logs"
		}
	case ScreenAutomations:
		switch action.Executor.Kind {
		case PortalExecutorWorkerRunOnce:
			return "run"
		case PortalExecutorScheduleFireNow, PortalExecutorSchedulePause, PortalExecutorScheduleResume, PortalExecutorUnsupported:
			return "controls"
		case PortalExecutorScheduleInspect, PortalExecutorDirectEndpointInspect, PortalExecutorRecordInspect, PortalExecutorWorkerInspect:
			return "check"
		case PortalExecutorScheduleFireInspect, PortalExecutorDirectEventInspect, PortalExecutorDirectEventRawPayload, PortalExecutorInvocationInspect, PortalExecutorWorkerRunsInspect:
			return "logs"
		}
	case ScreenNotes:
		if strings.HasPrefix(action.ID, "notes.pipeline.process.") {
			return "process"
		}
		if strings.HasPrefix(action.ID, "notes.pipeline.policy.") || strings.HasPrefix(action.ID, "notes.embeddings.") {
			return "policies"
		}
		if strings.HasPrefix(action.ID, "notes.pipeline.repair.") {
			return "repair"
		}
		if strings.HasPrefix(action.ID, "notes.pipeline.inspect.") {
			return "inspect"
		}
		switch action.Executor.Kind {
		case PortalExecutorWorkerRunOnce:
			return "process"
		case PortalExecutorIndexRetry, PortalExecutorIndexRetryFailed, PortalExecutorIndexRebuildObject:
			return "repair"
		case PortalExecutorIndexInspect:
			return "inspect"
		case PortalExecutorRecordInspect, PortalExecutorObjectInspect:
			return "inspect"
		}
	case ScreenNodes:
		switch action.Executor.Kind {
		case PortalExecutorProtectedFolderProtect:
			return ""
		case PortalExecutorProtectedFolderInspect, PortalExecutorProtectedFolderEnable, PortalExecutorProtectedFolderDisable, PortalExecutorProtectedFolderRecheck, PortalExecutorProtectedFolderRetry, PortalExecutorProtectedFolderDelete:
			return "protected_folders"
		case PortalExecutorNodeInspect, PortalExecutorNodeHealth:
			return "node_health"
		case PortalExecutorDeletionRequestInspect, PortalExecutorDeletionRequestReview, PortalExecutorDeletionRequestApprove, PortalExecutorDeletionRequestDeny, PortalExecutorDeletionRequestComplete:
			return "deletion_requests"
		case PortalExecutorRecordInspect:
			switch action.TargetKind {
			case "watched_root", "watched_root_finding", "watched_root_finding_group", "watched_root_backup_status", "watched_root_backup_batch":
				return "watched_roots"
			case "sync_batch", "sync_conflict", "sync_replica":
				return "sync"
			case "private_backup":
				return "private_backups"
			default:
				return "diagnostics"
			}
		}
	case ScreenDatabase:
		switch action.Executor.Kind {
		case PortalExecutorWorkerRunOnce:
			return "workers"
		case PortalExecutorIndexInspect, PortalExecutorIndexExplainObject, PortalExecutorIndexRetry, PortalExecutorIndexRetryFailed, PortalExecutorIndexRebuildObject:
			return "indexing"
		case PortalExecutorObjectInspect:
			return "objects"
		case PortalExecutorDeletionRequestInspect, PortalExecutorDeletionRequestReview, PortalExecutorDeletionRequestApprove, PortalExecutorDeletionRequestDeny, PortalExecutorDeletionRequestComplete:
			return "deletion_requests"
		case PortalExecutorRecordInspect:
			switch action.TargetKind {
			case "sync_batch", "sync_conflict", "sync_replica", "private_backup":
				return "sync"
			case "index_failure", "index_status", "index_queue":
				return "indexing"
			case "deletion_request":
				return "deletion_requests"
			default:
				return "raw_diagnostics"
			}
		}
	}
	return ""
}

func operationalActionGroupRef(screen string, key string) string {
	return NormalizeScreen(screen) + "." + strings.TrimSpace(key)
}

func operationalInlineActionItem(screen string, groupRef string, section portalActionSectionDefinition, action PortalAction) SelectableItem {
	return SelectableItem{
		Kind:               SelectableKindAction,
		Label:              action.Label,
		Description:        action.Description,
		Screen:             NormalizeScreen(screen),
		ActionID:           action.ID,
		ActionSection:      section.Key,
		ActionSectionLabel: section.Label,
		RecordKind:         portalActionGroupActionRecordKind,
		RecordRef:          action.ID,
		RecordLabel:        groupRef,
		PrimaryAction:      actionPtr(action),
		DisabledReason:     action.DisabledReason,
	}
}

func operationalDirectActionItem(screen string, section portalActionSectionDefinition, action PortalAction) SelectableItem {
	return SelectableItem{
		Kind:               SelectableKindAction,
		Label:              action.Label,
		Description:        action.Description,
		Screen:             NormalizeScreen(screen),
		ActionID:           action.ID,
		ActionSection:      section.Key,
		ActionSectionLabel: section.Label,
		RecordKind:         portalActionDirectRecordKind,
		RecordRef:          action.ID,
		RecordLabel:        firstNonEmpty(action.TargetLabel, action.TargetRef),
		PrimaryAction:      actionPtr(action),
		DisabledReason:     action.DisabledReason,
	}
}

func normalizeSelectableRows(items []SelectableItem) []SelectableItem {
	for idx := range items {
		items[idx].RowIndex = idx
	}
	return items
}

func selectableIsOperationalActionGroup(item SelectableItem) bool {
	return item.RecordKind == portalActionGroupRecordKind
}

func selectableIsOperationalActionGroupAction(item SelectableItem) bool {
	return item.RecordKind == portalActionGroupActionRecordKind
}

func selectableIsOperationalActionDirect(item SelectableItem) bool {
	return item.RecordKind == portalActionDirectRecordKind
}

func selectableIsOperationalActionPresentation(item SelectableItem) bool {
	return selectableIsOperationalActionGroup(item) || selectableIsOperationalActionGroupAction(item) || selectableIsOperationalActionDirect(item)
}

func portalActionVisibleInTopArea(state ScreenState, action PortalAction) bool {
	if strings.TrimSpace(action.ID) == "" {
		return false
	}
	if state.RawDetails {
		return true
	}
	return portalActionPresentationFor(action) != portalActionPresentationOperator
}

func portalActionPresentationFor(action PortalAction) portalActionPresentation {
	if action.Executor.Kind == PortalExecutorUnsupported || action.Risk == ActionRiskBlocked {
		return portalActionPresentationOperator
	}
	if portalActionIsStaticWorkerShortcut(action) {
		return portalActionPresentationOperator
	}
	switch action.Executor.Kind {
	case PortalExecutorWorkerRunOnce,
		PortalExecutorMaintenanceBackupRun,
		PortalExecutorMaintenanceBackupVerify,
		PortalExecutorMaintenanceObjectScan,
		PortalExecutorIndexRetry,
		PortalExecutorIndexRetryFailed,
		PortalExecutorIndexRebuildObject,
		PortalExecutorCloudStatusLive:
		return portalActionPresentationContextual
	case PortalExecutorNavigate,
		PortalExecutorProtectedFolderProtect,
		PortalExecutorProtectedFolderInspect,
		PortalExecutorNotesEmbeddingsToggle,
		PortalExecutorWorkerInspect,
		PortalExecutorWorkerRunsInspect,
		PortalExecutorIndexInspect,
		PortalExecutorIndexExplainObject,
		PortalExecutorMaintenanceInspect,
		PortalExecutorCloudCooldownInspect,
		PortalExecutorObjectInspect,
		PortalExecutorRecordInspect,
		PortalExecutorScheduleInspect,
		PortalExecutorScheduleFireInspect,
		PortalExecutorDirectEndpointInspect,
		PortalExecutorDirectEventInspect,
		PortalExecutorDirectEventRawPayload,
		PortalExecutorInvocationInspect,
		PortalExecutorProviderInspect,
		PortalExecutorProviderHealth,
		PortalExecutorProviderAdInspect,
		PortalExecutorCapabilityInspect,
		PortalExecutorCapabilityUsageDocs,
		PortalExecutorCapabilityCallInspect,
		PortalExecutorRuntimeBindingInspect,
		PortalExecutorProjectInspect,
		PortalExecutorProjectValidateLocal,
		PortalExecutorProjectValidateBackend,
		PortalExecutorProjectRegistrationPlan,
		PortalExecutorProjectDoctor,
		PortalExecutorProjectDiff,
		PortalExecutorProjectArchiveInspect,
		PortalExecutorProjectAutomationHealth,
		PortalExecutorProjectFacetInspect,
		PortalExecutorProjectWatchPlan,
		PortalExecutorProjectSyncStatus,
		PortalExecutorProjectBackupStatus,
		PortalExecutorStorageInspect,
		PortalExecutorStorageSafeToDelete,
		PortalExecutorStorageRetentionStatus,
		PortalExecutorMainDocumentsStatus,
		PortalExecutorJobInspect,
		PortalExecutorJobLogsInspect,
		PortalExecutorJobOutputsInspect,
		PortalExecutorNodeInspect,
		PortalExecutorNodeHealth:
		return portalActionPresentationPrimary
	default:
		return portalActionPresentationContextual
	}
}

func portalActionIsStaticWorkerShortcut(action PortalAction) bool {
	if action.Executor.Kind != PortalExecutorWorkerRunOnce {
		return false
	}
	switch action.ID {
	case "worker.selfcheck.run_once",
		"worker.indexer_text.run_once",
		"worker.automation_scheduler.run_once",
		"worker.automation_dispatcher.run_once",
		"worker.direct_event_ingest.run_once",
		"worker.job_runner.run_once",
		"worker.job_sweeper.run_once":
		return true
	default:
		return false
	}
}

func ScreenAvailableActionsForRecords(state ScreenState, records []SelectableItem) []PortalAction {
	state.Screen = NormalizeScreen(state.Screen)
	seen := map[string]bool{}
	result := []PortalAction{}
	add := func(action PortalAction) {
		if action.ID == "" {
			return
		}
		action = applyMainAvailabilityToAction(action, state.MainAvailability)
		if !seen[action.ID] {
			seen[action.ID] = true
			result = append(result, action)
		}
	}

	if state.Screen != ScreenProjects {
		for _, item := range records {
			for _, action := range item.RelatedActions {
				add(action)
			}
		}
	}

	switch state.Screen {
	case ScreenHome:
		if len(state.Data.Home.Attention) > 0 {
			add(NewSupportBundleCommandHintAction(ScreenHome))
		}
	case ScreenDoctor:
		add(NewSupportBundleCommandHintAction(ScreenDoctor))
	case ScreenBox:
		add(NewBoxInspectAction(state.Data.Box.Status))
		add(NewBoxInitAction(state.Data.Box.Status))
		if portalHasWorkspaceLane(state.Data.Box.Status) {
			add(NewBoxLaneSendAction(state.Data.Box.Status))
			add(NewBoxLanePlanAction(state.Data.Box.Status))
			add(NewBoxLaneProfileSendAction(state.Data.Box.Status, filepolicy.ProfileSourceOnly))
			add(NewBoxLaneProfileSendAction(state.Data.Box.Status, filepolicy.ProfileExact))
		}
		add(NewBoxIgnoreInspectAction(state.Data.Box.Status))
		add(NewBoxWatchPlanAction(state.Data.Box.Status))
		add(NewBoxWatchStatusAction(state.Data.Box.Status))
		add(NewBoxWatchApplyAction(state.Data.Box.Status))
		add(NewBoxProjectScaffoldAction(state.Data.Box.Status))
	case ScreenStorage:
		add(NewStorageRetentionStatusAction(state.Data.Storage.RetentionStatus))
		add(NewMainDocumentsStatusAction(state.Data.Storage.MainDocumentsStatus))
		add(NewStorageMountHelperAction())
		if state.Data.Storage.CloudStatusAvailable {
			add(NewCloudCooldownInspectAction(state.Data.Storage.CloudStatus, ScreenStorage))
			add(NewCloudStatusLiveAction(state.Data.Storage.CloudStatus, ScreenStorage))
		}
	case ScreenBackground:
		archivedProjects := archivedProjectIndexFromProjects(state.Data.Background.ArchivedProjects)
		for _, worker := range state.Data.Background.Workers {
			if archivedProjects.workerArchived(worker) {
				continue
			}
			add(NewWorkerRunOnceAction(worker, ScreenBackground))
		}
		if state.Data.Background.CloudStatusAvailable {
			add(NewCloudCooldownInspectAction(state.Data.Background.CloudStatus, ScreenBackground))
			add(NewCloudStatusLiveAction(state.Data.Background.CloudStatus, ScreenBackground))
		}
		add(NewMaintenanceBackupRunAction())
		add(NewMaintenanceObjectStoreScanAction())
	case ScreenProjects:
		for _, action := range projectActionsForCurrentView(state.Data.Projects) {
			add(action)
		}
	case ScreenAutomations:
		for _, item := range StaticScreenActionItems(ScreenAutomations) {
			if action, ok := PortalActionFromSelectableItem(item); ok {
				add(action)
			}
		}
	case ScreenJobs:
		archivedProjects := archivedProjectIndexFromProjects(state.Data.Jobs.ArchivedProjects)
		add(NewIndexRetryFailedAction(search.IndexRetryFailedInput{Limit: 50}))
		for _, worker := range state.Data.Jobs.Workers {
			if archivedProjects.workerArchived(worker) {
				continue
			}
			switch worker.WorkerKind {
			case workers.KindIndexerText, workers.KindJobRunner, workers.KindJobSweeper:
				add(NewWorkerRunOnceAction(worker, ScreenJobs))
			}
		}
		for _, item := range StaticScreenActionItems(ScreenJobs) {
			if action, ok := PortalActionFromSelectableItem(item); ok {
				add(action)
			}
		}
	case ScreenNotes:
		if state.Data.Notes.PipelinesAvailable {
			add(NewNotesPipelineWorkerAction("main.knowledge_indexer", "Run Pipeline Coordinator Once"))
			add(NewNotesPipelineWorkerAction("main.knowledge_heavy", "Run Heavy Executor Once"))
			add(NewNotesPipelineStatusAction(state.Data.Notes.Pipelines))
			add(NewNotesPipelinePolicyInspectAction(state.Data.Notes.Pipelines.Policy))
			add(NewNotesPipelineFailuresAction(state.Data.Notes.Pipelines.Operations.Blocked))
		}
		if state.Data.Notes.EmbeddingsAvailable {
			add(NewNotesEmbeddingsToggleAction(state.Data.Notes.Embeddings))
		}
	case ScreenNodes:
		add(NewProtectFolderAction(state.Data.Nodes.Nodes))
	case ScreenDatabase:
		add(NewDatabaseDiagnosticsInspectAction(state.Data.Database))
		add(databaseScopedAction(NewIndexRetryFailedAction(search.IndexRetryFailedInput{Limit: 50})))
		hasIndexer := false
		for _, worker := range state.Data.Database.Workers {
			if worker.WorkerKind == workers.KindIndexerText {
				hasIndexer = true
				add(NewWorkerRunOnceAction(worker, ScreenDatabase))
			}
		}
		if !hasIndexer {
			if action, ok := actions.DefaultRegistry().Get("worker.indexer_text.run_once"); ok {
				add(databaseScopedAction(PortalActionFromRegistry(action)))
			}
		}
	}

	return result
}

func timelineSelectableItems(state ScreenState) []SelectableItem {
	data := state.Data.Timeline
	events := data.DisplayEvents()
	items := make([]SelectableItem, 0, len(events))
	for idx, event := range events {
		inspect := NewTimelineEventInspectAction(event)
		items = append(items, SelectableItem{
			Kind:          SelectableKindRecord,
			Label:         firstNonEmpty(event.Title, event.ID),
			Description:   firstNonEmpty(event.Summary, event.ErrorMessage, event.TargetRef),
			Screen:        ScreenTimeline,
			RowIndex:      idx,
			RecordKind:    "timeline_event",
			RecordRef:     event.ID,
			RecordLabel:   firstNonEmpty(event.Title, event.ID),
			PrimaryAction: actionPtr(inspect),
		})
	}
	return items
}

func boxSelectableItems(state ScreenState) []SelectableItem {
	data := state.Data.Box
	status := data.Status
	items := []SelectableItem{}
	row := 0
	add := func(item SelectableItem) {
		item.Screen = ScreenBox
		item.RowIndex = row
		items = append(items, item)
		row++
	}
	add(SelectableItem{
		Kind:           SelectableKindRecord,
		Label:          "LOOM Box",
		Description:    firstNonEmpty(status.RootPath, "resolved local Box"),
		RecordKind:     "box",
		RecordRef:      status.RootPath,
		RecordLabel:    "LOOM Box",
		PrimaryAction:  actionPtr(NewBoxInspectAction(status)),
		RelatedActions: []PortalAction{NewBoxInitAction(status), NewBoxWatchPlanAction(status), NewBoxWatchStatusAction(status), NewBoxProjectScaffoldAction(status)},
	})
	if portalHasWorkspaceLane(status) {
		for _, pending := range status.Lane.Items {
			inspect := NewBoxLanePendingInspectAction(pending, status)
			related := []PortalAction{NewBoxLaneSendAction(status)}
			if pending.AttentionStatus != lane.AttentionStatusAcknowledged {
				related = append([]PortalAction{NewBoxLanePendingAcknowledgeAction(pending, status)}, related...)
			}
			add(SelectableItem{
				Kind:           SelectableKindRecord,
				Label:          pending.RelativePath,
				Description:    fmt.Sprintf("lane %s %s", pending.Kind, storageFormatBytes(pending.Bytes)),
				RecordKind:     "box_lane_pending",
				RecordRef:      pending.RelativePath,
				RecordLabel:    pending.RelativePath,
				PrimaryAction:  actionPtr(inspect),
				RelatedActions: related,
			})
		}
		if status.Lane.LastTransfer != nil {
			transfer := *status.Lane.LastTransfer
			related := []PortalAction{}
			if boxLaneTransferRepairStatus(transfer) {
				related = append(related, NewBoxLaneTransferRepairAction(transfer, status))
			}
			if boxLaneTransferAttentionStatus(transfer.Status) && transfer.AttentionStatus == lane.AttentionStatusActive {
				related = append(related,
					NewBoxLaneTransferAttentionAction(transfer, status, false),
					NewBoxLaneTransferAttentionAction(transfer, status, true),
				)
			}
			add(SelectableItem{
				Kind:           SelectableKindRecord,
				Label:          firstNonEmpty(transfer.BatchID, "Lane transfer"),
				Description:    fmt.Sprintf("lane transfer %s %s", firstNonEmpty(transfer.Status, "-"), storageFormatBytes(transfer.TotalBytes)),
				RecordKind:     "box_lane_transfer",
				RecordRef:      transfer.BatchID,
				RecordLabel:    firstNonEmpty(transfer.BatchID, "Lane transfer"),
				PrimaryAction:  actionPtr(NewBoxLaneTransferInspectAction(transfer, status)),
				RelatedActions: related,
			})
		}
	}
	for _, area := range boxVisibleAreas(status, state.RawDetails) {
		inspect := NewBoxPathInspectAction("folder", area, status)
		related := []PortalAction{}
		switch area.Key {
		case box.AreaProjects:
			related = append(related, NewBoxProjectScaffoldAction(status))
		case box.AreaNotes, box.AreaDocuments, box.AreaLaunchpad:
			related = append(related, NewBoxWatchPlanAction(status), NewBoxWatchStatusAction(status))
		}
		add(SelectableItem{
			Kind:           SelectableKindRecord,
			Label:          titleFromToken(area.Key),
			Description:    fmt.Sprintf("%s %s", area.Status, area.RelativePath),
			RecordKind:     "box_area",
			RecordRef:      area.Key,
			RecordLabel:    area.Key,
			PrimaryAction:  actionPtr(inspect),
			RelatedActions: related,
		})
	}
	for _, policy := range boxVisiblePolicies(status, state.RawDetails) {
		inspect := NewBoxPathInspectAction("policy", policy, status)
		add(SelectableItem{
			Kind:           SelectableKindRecord,
			Label:          titleFromToken(policy.Key) + " Policy",
			Description:    fmt.Sprintf("%s %s", policy.Status, policy.RelativePath),
			RecordKind:     "box_policy",
			RecordRef:      policy.Key,
			RecordLabel:    policy.Key,
			PrimaryAction:  actionPtr(inspect),
			RelatedActions: []PortalAction{NewBoxWatchPlanAction(status), NewBoxWatchStatusAction(status)},
		})
	}
	for _, root := range data.WatchPlan.WatchedRoots {
		ref := firstNonEmpty(root.BackendRootKey, root.Key)
		action := NewPortalRecordInspectAction("box", ScreenBox, "box_watch_root", ref, firstNonEmpty(root.DisplayName, ref), map[string]string{
			"area":               root.Key,
			"backend_root_key":   root.BackendRootKey,
			"worker_key":         root.WorkerKey,
			"sync_mode":          root.SyncMode,
			"index_mode":         root.IndexMode,
			"backup_mode":        root.BackupMode,
			"root_relative_path": root.RootRelativePath,
		})
		action.RawCommand = []string{"loom", "box", "watch-plan", "--path", status.RootPath, "--profile", status.Profile}
		action.RefreshScreen = ScreenBox
		add(SelectableItem{
			Kind:          SelectableKindRecord,
			Label:         firstNonEmpty(root.DisplayName, ref),
			Description:   fmt.Sprintf("sync=%s index=%s backup=%s", root.SyncMode, root.IndexMode, root.BackupMode),
			RecordKind:    "box_watch_root",
			RecordRef:     ref,
			RecordLabel:   firstNonEmpty(root.DisplayName, ref),
			PrimaryAction: actionPtr(action),
		})
	}
	if data.WatchStatusAvailable {
		for _, registration := range data.WatchStatus.Registrations {
			if !state.RawDetails && (boxAreaIsInternal(registration.AreaKey) || !boxPolicyIsUserFacing(registration.AreaKey)) {
				continue
			}
			ref := firstNonEmpty(registration.BackendRootKey, registration.AreaKey, registration.BoxWatchRootRegistrationID)
			action := NewPortalRecordInspectAction("box", ScreenBox, "box_watch_registration", ref, registration.AreaKey, map[string]string{
				"area":              registration.AreaKey,
				"backend_root_key":  registration.BackendRootKey,
				"activation_status": registration.ActivationStatus,
				"owner_node":        firstNonEmpty(registration.OwnerNodeKey, registration.NodeID),
				"worker_key":        registration.WorkerKey,
				"sync_mode":         registration.SyncMode,
				"index_mode":        registration.IndexMode,
				"backup_mode":       registration.BackupMode,
			})
			action.RawCommand = []string{"loom", "box", "watch-status", "--path", status.RootPath, "--profile", status.Profile}
			action.RefreshScreen = ScreenBox
			add(SelectableItem{
				Kind:          SelectableKindRecord,
				Label:         registration.AreaKey,
				Description:   registration.ActivationStatus + " " + registration.BackendRootKey,
				RecordKind:    "box_watch_registration",
				RecordRef:     ref,
				RecordLabel:   registration.AreaKey,
				PrimaryAction: actionPtr(action),
			})
		}
	}
	return withOperationalActionGroups(state, items)
}

func projectsSelectableItems(state ScreenState) []SelectableItem {
	data := state.Data.Projects
	explorer := normalizeProjectExplorerState(data.Explorer)
	builder := newProjectSelectableBuilder(state, data)

	switch explorer.Level {
	case ProjectExplorerDetail:
		projectDetailSelectableItems(builder)
	case ProjectExplorerStructure:
		projectStructureSelectableItems(builder)
	case ProjectExplorerRuntime:
		projectRuntimeSelectableItems(builder)
	case ProjectExplorerStorage:
		projectStorageSelectableItems(builder)
	case ProjectExplorerTimeline:
		projectTimelineSelectableItems(builder)
	case ProjectExplorerArchive:
		projectArchiveSelectableItems(builder)
	default:
		projectHomeSelectableItems(builder)
	}
	return withOperationalActionGroups(state, builder.items)
}

type projectSelectableBuilder struct {
	state ScreenState
	data  ProjectsData
	items []SelectableItem
	row   int
}

func newProjectSelectableBuilder(state ScreenState, data ProjectsData) *projectSelectableBuilder {
	data.Explorer = normalizeProjectExplorerState(data.Explorer)
	return &projectSelectableBuilder{state: state, data: data}
}

func (b *projectSelectableBuilder) add(item SelectableItem) {
	item.Screen = ScreenProjects
	item.RowIndex = b.row
	b.items = append(b.items, item)
	b.row++
}

func (b *projectSelectableBuilder) addRecord(item SelectableItem) {
	if projectRecordActionsQuarantined(b.data) {
		item.PrimaryAction = nil
		item.RelatedActions = nil
	}
	b.add(item)
	if b.data.Explorer.ExpandedRef != item.RecordRef {
		return
	}
	actions := []PortalAction{}
	if item.PrimaryAction != nil {
		actions = append(actions, *item.PrimaryAction)
	}
	actions = append(actions, item.RelatedActions...)
	for _, action := range actions {
		b.add(projectInlineActionItem(action, item.RecordRef))
	}
}

func projectHomeSelectableItems(builder *projectSelectableBuilder) {
	data := builder.data
	for _, project := range data.Projects {
		inspect := NewProjectInspectAction(project, ScreenProjects)
		ref := projectRef(project)
		builder.addRecord(SelectableItem{
			Kind:          SelectableKindRecord,
			Label:         projectLabel(project),
			Screen:        ScreenProjects,
			RecordKind:    "project",
			RecordRef:     ref,
			RecordLabel:   projectLabel(project),
			PrimaryAction: actionPtr(inspect),
		})
	}
}

func projectDetailSelectableItems(builder *projectSelectableBuilder) {
	data := builder.data
	builder.addRecord(projectSectionSelectableItem(data, ProjectExplorerStructure, "Structure", "Facets, contracts, validation, and registration plan."))
	builder.addRecord(projectSectionSelectableItem(data, ProjectExplorerRuntime, "Runtime", "Capabilities, workflows, schedules, direct events, and bindings."))
	storage := projectSectionSelectableItem(data, ProjectExplorerStorage, "Storage", "Watched roots, sync status, backup status, and findings.")
	storage.RelatedActions = append(storage.RelatedActions, NewProjectExportActions(projectActionDetail(data), ScreenProjects)...)
	builder.addRecord(storage)
	builder.addRecord(projectSectionSelectableItem(data, ProjectExplorerTimeline, "Timeline", "Project jobs, invocations, calls, and recent activity."))
	if selectedProjectArchived(data) {
		builder.addRecord(projectSectionSelectableItem(data, ProjectExplorerArchive, "Archive", "Archived project retention and reactivation context."))
	}
}

func projectStructureSelectableItems(builder *projectSelectableBuilder) {
	data := builder.data
	projectRepositorySelectableItems(builder)
	for _, facet := range data.RegistrationDetail.Facets {
		inspect := NewProjectFacetInspectAction(data.RegistrationDetail, facet, ScreenProjects)
		ref := data.SelectedProjectRef + "." + facet.FacetKey
		related := []PortalAction{}
		if facet.Enabled && facet.Present {
			related = append(related, NewProjectActivateAction(data.RegistrationDetail, facet.FacetKey, ScreenProjects))
			if facet.FacetStatus == projects.ProjectFacetStatusActivated {
				related = append(related, NewProjectDeactivateAction(data.RegistrationDetail, facet.FacetKey, ScreenProjects))
			}
		}
		builder.addRecord(SelectableItem{
			Kind:           SelectableKindRecord,
			Label:          facet.FacetKey,
			Description:    fmt.Sprintf("present=%t enabled=%t placeholder=%t", facet.Present, facet.Enabled, facet.Placeholder),
			Screen:         ScreenProjects,
			RecordKind:     "project_facet",
			RecordRef:      ref,
			RecordLabel:    facet.FacetKey,
			PrimaryAction:  actionPtr(inspect),
			RelatedActions: related,
		})
	}
}

func projectRuntimeSelectableItems(builder *projectSelectableBuilder) {
	data := builder.data
	for _, workflow := range data.Workflows {
		inspect := NewProjectWorkflowInspectAction(data.RegistrationDetail, workflow, ScreenProjects)
		related := []PortalAction{
			NewProjectWorkflowCapabilityInspectAction(data.RegistrationDetail, workflow, ScreenProjects),
			NewProjectWorkflowCapabilityCallAction(data.RegistrationDetail, workflow, ScreenProjects),
			NewProjectWorkflowJobsAction(data.RegistrationDetail, workflow, ScreenProjects),
		}
		ref := data.SelectedProjectRef + ".workflow." + firstNonEmpty(workflow.WorkflowID, workflow.Name)
		label := firstNonEmpty(workflow.Name, workflow.WorkflowID, ref)
		builder.addRecord(SelectableItem{
			Kind:           SelectableKindRecord,
			Label:          label,
			Description:    firstNonEmpty(workflow.CapabilityAddress, workflow.RuntimeKind, workflow.ImplementationKind, "workflow placeholder"),
			Screen:         ScreenProjects,
			RecordKind:     "project_workflow",
			RecordRef:      ref,
			RecordLabel:    label,
			PrimaryAction:  actionPtr(inspect),
			RelatedActions: related,
		})
	}
	for _, provider := range data.Providers {
		inspect := NewProviderInspectAction(provider, ScreenProjects)
		ref := firstNonEmpty(provider.Provider.CompactAddress, provider.Provider.ProviderKey, provider.Provider.ProviderID)
		builder.addRecord(SelectableItem{
			Kind:           SelectableKindRecord,
			Label:          providerLabel(provider),
			Description:    firstNonEmpty(provider.Provider.ProviderType, provider.Provider.Description),
			Screen:         ScreenProjects,
			RecordKind:     "project_provider",
			RecordRef:      ref,
			RecordLabel:    providerLabel(provider),
			PrimaryAction:  actionPtr(inspect),
			RelatedActions: []PortalAction{NewProviderHealthAction(provider, ScreenProjects)},
		})
	}
	for _, capability := range data.Capabilities {
		inspect := NewCapabilityInspectAction(capability, ScreenProjects)
		ref := firstNonEmpty(capability.CapabilityEndpoint.CompactAddress, capability.CapabilityEndpoint.CapabilityEndpointID)
		related := []PortalAction{NewCapabilityUsageDocsAction(capability, ScreenProjects), NewCapabilityCallAction(capability, ScreenProjects)}
		if binding, ok := projectRuntimeBindingForCapability(data, capability); ok {
			related = append(related, NewRuntimeBindingInspectAction(binding, ScreenProjects))
		}
		builder.addRecord(SelectableItem{
			Kind:           SelectableKindRecord,
			Label:          ref,
			Description:    firstNonEmpty(capability.DisplayName, capability.Description, capability.ClassName),
			Screen:         ScreenProjects,
			RecordKind:     "project_capability",
			RecordRef:      ref,
			RecordLabel:    ref,
			PrimaryAction:  actionPtr(inspect),
			RelatedActions: related,
		})
	}
	for _, binding := range data.RuntimeBindings {
		inspect := NewRuntimeBindingInspectAction(binding, ScreenProjects)
		ref := binding.Binding.RuntimeBindingID
		builder.addRecord(SelectableItem{
			Kind:          SelectableKindRecord,
			Label:         firstNonEmpty(binding.Endpoint.CompactAddress, ref),
			Description:   firstNonEmpty(binding.Binding.RuntimeKind, binding.Binding.Status),
			Screen:        ScreenProjects,
			RecordKind:    "project_runtime_binding",
			RecordRef:     ref,
			RecordLabel:   firstNonEmpty(binding.Endpoint.CompactAddress, ref),
			PrimaryAction: actionPtr(inspect),
		})
	}
	for _, schedule := range data.Schedules {
		inspect := NewScheduleInspectAction(schedule, ScreenProjects)
		ref := firstNonEmpty(schedule.ScheduleID, schedule.ScheduleKey)
		builder.addRecord(SelectableItem{
			Kind:          SelectableKindRecord,
			Label:         firstNonEmpty(schedule.DisplayName, schedule.ScheduleKey, schedule.ScheduleID),
			Screen:        ScreenProjects,
			RecordKind:    "project_schedule",
			RecordRef:     ref,
			RecordLabel:   firstNonEmpty(schedule.DisplayName, schedule.ScheduleKey, schedule.ScheduleID),
			PrimaryAction: actionPtr(inspect),
		})
	}
	for _, endpoint := range data.DirectEventEndpoints {
		inspect := NewDirectEventEndpointInspectAction(endpoint, ScreenProjects)
		ref := firstNonEmpty(endpoint.EndpointID, endpoint.EndpointSlug)
		builder.addRecord(SelectableItem{
			Kind:           SelectableKindRecord,
			Label:          firstNonEmpty(endpoint.DisplayName, endpoint.EndpointSlug, endpoint.EndpointID),
			Screen:         ScreenProjects,
			RecordKind:     "project_direct_event_endpoint",
			RecordRef:      ref,
			RecordLabel:    firstNonEmpty(endpoint.DisplayName, endpoint.EndpointSlug, endpoint.EndpointID),
			PrimaryAction:  actionPtr(inspect),
			RelatedActions: []PortalAction{NewDirectEventEndpointTestAction(endpoint, ScreenProjects)},
		})
	}
	for _, event := range data.DirectEvents {
		inspect := NewDirectEventInspectAction(event, ScreenProjects)
		ref := event.DirectEventID
		builder.addRecord(SelectableItem{
			Kind:           SelectableKindRecord,
			Label:          firstNonEmpty(event.ExternalEventID, ref),
			Description:    event.Status,
			Screen:         ScreenProjects,
			RecordKind:     "project_direct_event",
			RecordRef:      ref,
			RecordLabel:    firstNonEmpty(event.ExternalEventID, ref),
			PrimaryAction:  actionPtr(inspect),
			RelatedActions: []PortalAction{NewDirectEventRawPayloadAction(event, ScreenProjects)},
		})
	}
}

func projectTimelineSelectableItems(builder *projectSelectableBuilder) {
	data := builder.data
	for _, invocation := range data.Invocations {
		inspect := NewInvocationInspectAction(invocation, ScreenProjects)
		ref := invocation.InvocationID
		builder.addRecord(SelectableItem{
			Kind:          SelectableKindRecord,
			Label:         firstNonEmpty(invocation.TargetCapability, ref),
			Description:   invocation.Status,
			Screen:        ScreenProjects,
			RecordKind:    "project_invocation",
			RecordRef:     ref,
			RecordLabel:   firstNonEmpty(invocation.TargetCapability, ref),
			PrimaryAction: actionPtr(inspect),
		})
	}
	for _, invocation := range data.InvocationFailures {
		inspect := NewInvocationInspectAction(invocation, ScreenProjects)
		ref := invocation.InvocationID
		builder.addRecord(SelectableItem{
			Kind:          SelectableKindRecord,
			Label:         firstNonEmpty(invocation.TargetCapability, ref),
			Description:   firstNonEmpty(invocation.Status, "failed"),
			Screen:        ScreenProjects,
			RecordKind:    "project_invocation",
			RecordRef:     ref,
			RecordLabel:   firstNonEmpty(invocation.TargetCapability, ref),
			PrimaryAction: actionPtr(inspect),
		})
	}
	for _, job := range data.Jobs {
		inspect := NewJobInspectAction(job, ScreenProjects)
		ref := job.JobID
		related := jobRelatedActions(job, ScreenProjects, false)
		builder.addRecord(SelectableItem{
			Kind:           SelectableKindRecord,
			Label:          jobLabel(job),
			Description:    job.Status,
			Screen:         ScreenProjects,
			RecordKind:     "project_job",
			RecordRef:      ref,
			RecordLabel:    jobLabel(job),
			PrimaryAction:  actionPtr(inspect),
			RelatedActions: related,
		})
	}
	for _, job := range data.FailedJobs {
		inspect := NewJobInspectAction(job, ScreenProjects)
		ref := job.JobID
		related := jobRelatedActions(job, ScreenProjects, false)
		builder.addRecord(SelectableItem{
			Kind:           SelectableKindRecord,
			Label:          jobLabel(job),
			Description:    firstNonEmpty(job.Status, "failed"),
			Screen:         ScreenProjects,
			RecordKind:     "project_job",
			RecordRef:      ref,
			RecordLabel:    jobLabel(job),
			PrimaryAction:  actionPtr(inspect),
			RelatedActions: related,
		})
	}
	for _, call := range data.CapabilityCalls {
		inspect := NewCapabilityCallInspectAction(call, ScreenProjects)
		ref := call.CapabilityCallID
		builder.addRecord(SelectableItem{
			Kind:          SelectableKindRecord,
			Label:         firstNonEmpty(call.Operation, ref),
			Description:   call.Status,
			Screen:        ScreenProjects,
			RecordKind:    "project_capability_call",
			RecordRef:     ref,
			RecordLabel:   firstNonEmpty(call.Operation, ref),
			PrimaryAction: actionPtr(inspect),
		})
	}
}

func projectStorageSelectableItems(builder *projectSelectableBuilder) {
	data := builder.data
	for _, root := range data.WatchedRoots {
		inspect := NewWatchedRootInspectAction(root, ScreenProjects)
		ref := root.Root.NodeID + "/" + root.Root.RootKey
		builder.addRecord(SelectableItem{
			Kind:          SelectableKindRecord,
			Label:         firstNonEmpty(root.Root.DisplayName, ref),
			Description:   root.Root.Status,
			Screen:        ScreenProjects,
			RecordKind:    "project_watched_root",
			RecordRef:     ref,
			RecordLabel:   firstNonEmpty(root.Root.DisplayName, ref),
			PrimaryAction: actionPtr(inspect),
		})
	}
	for _, finding := range data.WatchedRootFindings {
		inspect := NewWatchedRootFindingInspectAction(finding, ScreenProjects)
		ref := firstNonEmpty(finding.WatchedRootFindingID, finding.NodeID+"/"+finding.RootKey+"/"+finding.FindingKey)
		builder.addRecord(SelectableItem{
			Kind:          SelectableKindRecord,
			Label:         firstNonEmpty(finding.Summary, ref),
			Description:   firstNonEmpty(finding.Severity, finding.Status),
			Screen:        ScreenProjects,
			RecordKind:    "project_watched_root_finding",
			RecordRef:     ref,
			RecordLabel:   firstNonEmpty(finding.Summary, ref),
			PrimaryAction: actionPtr(inspect),
		})
	}
	if data.RegistrationDetail.Project.Project.ProjectID != "" || data.SelectedProjectRef != "" {
		syncAction := NewProjectSyncStatusAction(data.RegistrationDetail, ScreenProjects)
		backupAction := NewProjectBackupStatusAction(data.RegistrationDetail, ScreenProjects)
		builder.addRecord(SelectableItem{
			Kind:          SelectableKindRecord,
			Label:         "Sync Status",
			Description:   fmt.Sprintf("%d roots", data.SyncStatus.SyncRoots),
			Screen:        ScreenProjects,
			RecordKind:    "project_sync_status",
			RecordRef:     data.SelectedProjectRef + ".sync_status",
			RecordLabel:   "Sync Status",
			PrimaryAction: actionPtr(syncAction),
		})
		builder.addRecord(SelectableItem{
			Kind:          SelectableKindRecord,
			Label:         "Backup Status",
			Description:   fmt.Sprintf("%d roots", data.BackupStatus.BackupRoots),
			Screen:        ScreenProjects,
			RecordKind:    "project_backup_status",
			RecordRef:     data.SelectedProjectRef + ".backup_status",
			RecordLabel:   "Backup Status",
			PrimaryAction: actionPtr(backupAction),
		})
	}
}

func projectArchiveSelectableItems(builder *projectSelectableBuilder) {
	projectRepositorySelectableItems(builder)
	projectStorageSelectableItems(builder)
	projectTimelineSelectableItems(builder)
}

func projectRepositorySelectableItems(builder *projectSelectableBuilder) {
	data := builder.data
	for _, repository := range data.RepositoryStatus.Repositories {
		inspect := NewProjectRepositoryInspectAction(data.SelectedProjectRef, data.RepositoryStatus.Source, repository, ScreenProjects)
		builder.addRecord(SelectableItem{
			Kind:          SelectableKindRecord,
			Label:         firstNonEmpty(repository.Key, repository.RepositoryID),
			Description:   fmt.Sprintf("role=%s path=%s freshness=%s", repository.Role, repository.RelativePath, repository.ObservationPosture),
			Screen:        ScreenProjects,
			RecordKind:    "project_repository",
			RecordRef:     data.SelectedProjectRef + ".repository." + repository.RepositoryID,
			RecordLabel:   firstNonEmpty(repository.Key, repository.RepositoryID),
			PrimaryAction: actionPtr(inspect),
		})
	}
}

func projectRecordActionsQuarantined(data ProjectsData) bool {
	level := normalizeProjectExplorerState(data.Explorer).Level
	return selectedProjectArchived(data) && level != ProjectExplorerHome
}

func projectSectionSelectableItem(data ProjectsData, level ProjectExplorerLevel, label string, description string) SelectableItem {
	ref := data.SelectedProjectRef + "." + string(level)
	return SelectableItem{
		Kind:        SelectableKindRecord,
		Label:       label,
		Description: description,
		Screen:      ScreenProjects,
		RecordKind:  projectSectionRecordKind(level),
		RecordRef:   ref,
		RecordLabel: label,
	}
}

func projectSectionRecordKind(level ProjectExplorerLevel) string {
	return "project_section_" + string(level)
}

func projectExplorerLevelFromSectionRecordKind(kind string) (ProjectExplorerLevel, bool) {
	level := ProjectExplorerLevel(strings.TrimPrefix(kind, "project_section_"))
	switch level {
	case ProjectExplorerStructure, ProjectExplorerRuntime, ProjectExplorerStorage, ProjectExplorerTimeline, ProjectExplorerArchive:
		return level, true
	default:
		return "", false
	}
}

func projectInlineActionItem(action PortalAction, parentRef string) SelectableItem {
	return SelectableItem{
		Kind:           SelectableKindAction,
		Label:          action.Label,
		Description:    action.Description,
		ActionID:       action.ID,
		RecordKind:     "project_inline_action",
		RecordRef:      action.ID,
		RecordLabel:    parentRef,
		PrimaryAction:  actionPtr(action),
		DisabledReason: action.DisabledReason,
	}
}

func projectRuntimeBindingForCapability(data ProjectsData, capability capabilities.CapabilityListItem) (capabilities.RuntimeBindingInspection, bool) {
	endpointID := capability.CapabilityEndpoint.CapabilityEndpointID
	address := capability.CapabilityEndpoint.CompactAddress
	for _, binding := range data.RuntimeBindings {
		if binding.Endpoint.CapabilityEndpointID == endpointID || binding.Endpoint.CompactAddress == address {
			return binding, true
		}
	}
	return capabilities.RuntimeBindingInspection{}, false
}

func ScreenActions(state ScreenState) []PortalAction {
	seen := map[string]bool{}
	result := []PortalAction{}
	add := func(action PortalAction) {
		if action.ID == "" {
			return
		}
		action = applyMainAvailabilityToAction(action, state.MainAvailability)
		if !seen[action.ID] {
			seen[action.ID] = true
			result = append(result, action)
		}
	}
	for _, item := range ScreenRecordItems(state) {
		if item.PrimaryAction != nil {
			add(*item.PrimaryAction)
		}
		for _, action := range item.RelatedActions {
			add(action)
		}
	}
	for _, action := range ScreenAvailableActions(state) {
		add(action)
	}
	if NormalizeScreen(state.Screen) == ScreenProjects {
		for _, action := range projectAllDomainActions(state.Data.Projects) {
			add(action)
		}
	}
	if NormalizeScreen(state.Screen) == ScreenCapabilities {
		for _, action := range allCapabilityDomainActions(state.Data.Capabilities) {
			add(action)
		}
	}
	return result
}

func projectActionsForCurrentView(data ProjectsData) []PortalAction {
	level := normalizeProjectExplorerState(data.Explorer).Level
	if level != ProjectExplorerHome && selectedProjectArchived(data) {
		return archivedProjectActions(data)
	}
	switch level {
	case ProjectExplorerDetail:
		return projectDetailActions(data)
	case ProjectExplorerStructure:
		return projectStructureActions(data)
	case ProjectExplorerRuntime:
		return projectRuntimeActions(data)
	case ProjectExplorerStorage:
		return projectStorageActions(data)
	case ProjectExplorerTimeline:
		return projectTimelineActions(data)
	case ProjectExplorerArchive:
		return archivedProjectActions(data)
	default:
		return projectHomeActions(data)
	}
}

func projectHomeActions(data ProjectsData) []PortalAction {
	return []PortalAction{NewProjectScaffoldOnBackendAction(ScreenProjects, data.Box.Status)}
}

func projectDetailActions(data ProjectsData) []PortalAction {
	detail := projectActionDetail(data)
	project := detail.Project.Project
	actions := []PortalAction{
		NewProjectInspectAction(project, ScreenProjects),
		NewProjectAutomationHealthAction(detail, ScreenProjects),
		NewProjectRegistrationPlanAction(detail, ScreenProjects),
		NewProjectRegisterBackendAction(detail, ScreenProjects),
		NewProjectAddFacetAction(detail, ScreenProjects),
		NewProjectDoctorAction(detail, ScreenProjects),
		NewProjectActivateAction(detail, "", ScreenProjects),
		NewProjectArchiveAction(detail, ScreenProjects),
	}
	return actions
}

func projectStructureActions(data ProjectsData) []PortalAction {
	detail := projectActionDetail(data)
	actions := []PortalAction{
		NewProjectValidateBackendAction(detail, ScreenProjects),
		NewProjectRepositoryStatusAction(data.RepositoryStatus, ScreenProjects),
	}
	actions = appendProjectActionIfAvailable(actions, NewProjectValidateLocalAction(detail, ScreenProjects))
	actions = append(actions,
		NewProjectRegisterBackendAction(detail, ScreenProjects),
		NewProjectRegistrationPlanAction(detail, ScreenProjects),
		NewProjectAddFacetAction(detail, ScreenProjects),
		NewProjectRegenerateMissingContractsAction(detail, ScreenProjects),
		NewProjectDoctorAction(detail, ScreenProjects),
	)
	actions = appendProjectActionIfAvailable(actions, NewProjectMigrateLayoutAction(detail, data.BackendAnalysis, ScreenProjects))
	return actions
}

func projectRuntimeActions(data ProjectsData) []PortalAction {
	detail := projectActionDetail(data)
	return []PortalAction{
		NewProjectAutomationHealthAction(detail, ScreenProjects),
		NewProjectRegisterRuntimeAction(detail, ScreenProjects),
		NewProjectValidateRuntimeAction(detail, ScreenProjects),
		NewProjectDisableRuntimeAction(detail, ScreenProjects),
		NewProjectActivateAction(detail, "scripts", ScreenProjects),
		NewProjectActivateAction(detail, "workflows", ScreenProjects),
	}
}

func projectStorageActions(data ProjectsData) []PortalAction {
	detail := projectActionDetail(data)
	return []PortalAction{
		NewProjectWatchPlanAction(detail, ScreenProjects),
		NewProjectSyncStatusAction(detail, ScreenProjects),
		NewProjectBackupStatusAction(detail, ScreenProjects),
		NewProjectActivateAction(detail, "watched_roots", ScreenProjects),
		NewProjectArchiveAction(detail, ScreenProjects),
	}
}

func projectTimelineActions(data ProjectsData) []PortalAction {
	detail := projectActionDetail(data)
	return []PortalAction{
		NewProjectAutomationHealthAction(detail, ScreenProjects),
		NewProjectDoctorAction(detail, ScreenProjects),
	}
}

func archivedProjectActions(data ProjectsData) []PortalAction {
	detail := projectActionDetail(data)
	actions := []PortalAction{
		NewProjectRepositoryStatusAction(data.RepositoryStatus, ScreenProjects),
		NewProjectInspectArchiveAction(detail, ScreenProjects),
		NewProjectRestoreReactivateAction(detail, ScreenProjects),
	}
	for _, repository := range data.RepositoryStatus.Repositories {
		actions = append(actions, NewProjectRepositoryInspectAction(data.SelectedProjectRef, data.RepositoryStatus.Source, repository, ScreenProjects))
	}
	return actions
}

func projectAllDomainActions(data ProjectsData) []PortalAction {
	detail := projectActionDetail(data)
	actions := []PortalAction{}
	if selectedProjectArchived(data) {
		actions = append(actions, archivedProjectActions(data)...)
		return actions
	}
	actions = append(actions, projectHomeActions(data)...)
	actions = append(actions,
		NewProjectInspectAction(detail.Project.Project, ScreenProjects),
		NewProjectRepositoryStatusAction(data.RepositoryStatus, ScreenProjects),
		NewProjectAutomationHealthAction(detail, ScreenProjects),
		NewProjectValidateBackendAction(detail, ScreenProjects),
		NewProjectRegisterBackendAction(detail, ScreenProjects),
		NewProjectRegistrationPlanAction(detail, ScreenProjects),
		NewProjectDoctorAction(detail, ScreenProjects),
		NewProjectDiffAction(detail, ScreenProjects),
		NewProjectActivateAction(detail, "", ScreenProjects),
		NewProjectActivateAction(detail, "all", ScreenProjects),
		NewProjectWatchPlanAction(detail, ScreenProjects),
		NewProjectSyncStatusAction(detail, ScreenProjects),
		NewProjectBackupStatusAction(detail, ScreenProjects),
		NewProjectAddFacetAction(detail, ScreenProjects),
		NewProjectRegenerateMissingContractsAction(detail, ScreenProjects),
		NewProjectRegisterRuntimeAction(detail, ScreenProjects),
		NewProjectValidateRuntimeAction(detail, ScreenProjects),
		NewProjectDisableRuntimeAction(detail, ScreenProjects),
		NewProjectArchiveAction(detail, ScreenProjects),
		NewProjectInspectArchiveAction(detail, ScreenProjects),
		NewProjectRestoreReactivateAction(detail, ScreenProjects),
	)
	actions = append(actions, NewProjectExportActions(detail, ScreenProjects)...)
	actions = appendProjectActionIfAvailable(actions, NewProjectMigrateLayoutAction(detail, data.BackendAnalysis, ScreenProjects))
	actions = appendProjectActionIfAvailable(actions, NewProjectValidateLocalAction(detail, ScreenProjects))
	actions = appendProjectActionIfAvailable(actions, NewProjectScaffoldCleanupAction(detail, ScreenProjects))
	for _, facet := range detail.Facets {
		actions = append(actions, NewProjectFacetInspectAction(detail, facet, ScreenProjects))
		if facet.Enabled && facet.Present {
			actions = append(actions, NewProjectActivateAction(detail, facet.FacetKey, ScreenProjects))
			if facet.FacetStatus == projects.ProjectFacetStatusActivated {
				actions = append(actions, NewProjectDeactivateAction(detail, facet.FacetKey, ScreenProjects))
			}
		}
	}
	for _, repository := range data.RepositoryStatus.Repositories {
		actions = append(actions, NewProjectRepositoryInspectAction(data.SelectedProjectRef, data.RepositoryStatus.Source, repository, ScreenProjects))
	}
	return actions
}

func appendProjectActionIfAvailable(actions []PortalAction, action PortalAction) []PortalAction {
	if action.Disabled() {
		return actions
	}
	return append(actions, action)
}

func projectActionDetail(data ProjectsData) projects.ProjectRegistrationDetail {
	detail := data.RegistrationDetail
	if detail.Project.Project.ProjectID == "" {
		if data.SelectedProject.Project.ProjectID != "" {
			detail.Project = data.SelectedProject
		} else if len(data.Projects) > 0 {
			selectedRef := firstNonEmpty(data.SelectedProjectRef, data.Explorer.SelectedProjectRef)
			for _, project := range data.Projects {
				if projectRef(project) == selectedRef || project.ProjectID == selectedRef {
					detail.Project.Project = project
					return detail
				}
			}
			detail.Project.Project = data.Projects[0]
		}
	}
	return detail
}

func allCapabilityDomainActions(data CapabilitiesData) []PortalAction {
	archivedProjects := archivedProjectIndexFromProjects(data.ArchivedProjects)
	actions := []PortalAction{}
	for _, provider := range data.Providers {
		actions = append(actions, NewProviderInspectAction(provider, ScreenCapabilities), NewProviderHealthAction(provider, ScreenCapabilities))
	}
	for _, capability := range data.Capabilities {
		actions = append(actions, NewCapabilityInspectAction(capability, ScreenCapabilities), NewCapabilityUsageDocsAction(capability, ScreenCapabilities))
		if !archivedProjects.capabilityArchived(capability) {
			actions = append(actions, NewCapabilityCallAction(capability, ScreenCapabilities))
		}
	}
	for _, advertisement := range data.Advertisements {
		actions = append(actions, NewProviderAdvertisementInspectAction(advertisement, ScreenCapabilities))
	}
	return actions
}

func SelectedPortalAction(state ScreenState) (PortalAction, bool) {
	items := ScreenSelectableItems(state)
	if len(items) == 0 {
		return PortalAction{}, false
	}
	item := items[clampIndex(state.SelectedIndex, len(items))]
	if item.PrimaryAction == nil {
		return PortalAction{}, false
	}
	return *item.PrimaryAction, true
}

func PortalActionFromSelectableItem(item SelectableItem) (PortalAction, bool) {
	if item.PrimaryAction != nil {
		return *item.PrimaryAction, true
	}
	if item.Kind != SelectableKindAction || item.ActionID == "" {
		return PortalAction{}, false
	}
	action, ok := actions.DefaultRegistry().Get(item.ActionID)
	if !ok {
		return PortalAction{}, false
	}
	return PortalActionFromRegistry(action), true
}

func backgroundSelectableItems(state ScreenState) []SelectableItem {
	data := state.Data.Background
	archivedProjects := archivedProjectIndexFromProjects(data.ArchivedProjects)
	allItems := []SelectableItem{}
	visibleItems := []SelectableItem{}
	addCandidate := func(item SelectableItem, visible bool) {
		allItems = append(allItems, item)
		if visible || state.RawDetails {
			visibleItems = append(visibleItems, item)
		}
	}
	addWorker := func(worker workers.WorkerListItem) {
		inspect := NewWorkerInspectAction(worker, ScreenBackground)
		related := []PortalAction{NewWorkerRunsAction(worker, ScreenBackground)}
		if !archivedProjects.workerArchived(worker) {
			related = append(related, NewWorkerRunOnceAction(worker, ScreenBackground))
		}
		addCandidate(SelectableItem{
			Kind:           SelectableKindRecord,
			Label:          firstNonEmpty(worker.DisplayName, worker.WorkerKey),
			Screen:         ScreenBackground,
			RecordKind:     "worker",
			RecordRef:      worker.WorkerKey,
			RecordLabel:    workerDisplayName(worker),
			PrimaryAction:  actionPtr(inspect),
			RelatedActions: related,
		}, true)
	}
	if hasBackupStatus(data.BackupStatus) {
		inspect := NewMaintenanceBackupStatusInspectAction(data.BackupStatus)
		addCandidate(SelectableItem{
			Kind:           SelectableKindRecord,
			Label:          "Main Backups",
			Screen:         ScreenBackground,
			RecordKind:     "maintenance_backup_status",
			RecordRef:      "maintenance.backup",
			RecordLabel:    "Main Backups",
			PrimaryAction:  actionPtr(inspect),
			RelatedActions: []PortalAction{NewMaintenanceBackupRunAction()},
		}, true)
	}
	for _, backup := range data.BackupOperations {
		inspect := NewMaintenanceBackupOperationInspectAction(backup)
		addCandidate(SelectableItem{
			Kind:           SelectableKindRecord,
			Label:          firstNonEmpty(backup.Operation.MaintenanceOperationID, backup.Operation.OperationKey),
			Screen:         ScreenBackground,
			RecordKind:     "maintenance_backup_operation",
			RecordRef:      backup.Operation.MaintenanceOperationID,
			RecordLabel:    firstNonEmpty(backup.Operation.MaintenanceOperationID, backup.Operation.OperationKey),
			PrimaryAction:  actionPtr(inspect),
			RelatedActions: []PortalAction{NewMaintenanceBackupVerifyAction(backup)},
		}, true)
	}
	if hasUpdateStatus(data.UpdateStatus) {
		inspect := NewUpdateStatusInspectAction(data.UpdateStatus)
		addCandidate(SelectableItem{
			Kind:          SelectableKindRecord,
			Label:         "Production Updates",
			Screen:        ScreenBackground,
			RecordKind:    "update_status",
			RecordRef:     firstNonEmpty(data.UpdateStatus.ActiveManifestPath, "update.status"),
			RecordLabel:   "Production Updates",
			PrimaryAction: actionPtr(inspect),
		}, true)
	}
	for _, group := range groupWorkers(data.Workers) {
		for _, worker := range workersWithAttention(group.Workers) {
			addWorker(worker)
		}
	}
	for _, group := range groupWorkers(data.Workers) {
		for _, worker := range workersWithoutAttention(group.Workers) {
			addWorker(worker)
		}
	}
	for _, finding := range data.MaintenanceFindings {
		inspect := NewMaintenanceFindingInspectAction(finding)
		addCandidate(SelectableItem{
			Kind:          SelectableKindRecord,
			Label:         firstNonEmpty(finding.Summary, finding.FindingKey),
			Screen:        ScreenBackground,
			RecordKind:    "maintenance_finding",
			RecordRef:     firstNonEmpty(finding.FindingKey, finding.MaintenanceFindingID),
			RecordLabel:   firstNonEmpty(finding.Summary, finding.FindingKey),
			PrimaryAction: actionPtr(inspect),
		}, false)
	}
	if hasDBStatus(data.DBStatus) {
		inspect := NewMaintenanceDBInspectAction(data.DBStatus)
		addCandidate(SelectableItem{
			Kind:          SelectableKindRecord,
			Label:         "Database Maintenance",
			Screen:        ScreenBackground,
			RecordKind:    "maintenance_db",
			RecordRef:     "maintenance.db",
			RecordLabel:   "Database Maintenance",
			PrimaryAction: actionPtr(inspect),
		}, true)
	}
	if hasObjectStoreStatus(data.ObjectStoreStatus) {
		inspect := NewMaintenanceObjectStoreInspectAction(data.ObjectStoreStatus)
		addCandidate(SelectableItem{
			Kind:           SelectableKindRecord,
			Label:          "Object Store",
			Screen:         ScreenBackground,
			RecordKind:     "maintenance_object_store",
			RecordRef:      "maintenance.object_store",
			RecordLabel:    "Object Store",
			PrimaryAction:  actionPtr(inspect),
			RelatedActions: []PortalAction{NewMaintenanceObjectStoreScanAction()},
		}, true)
	}
	return withOperationalActionGroupsFromCandidates(state, visibleItems, allItems)
}

func automationsSelectableItems(state ScreenState) []SelectableItem {
	data := state.Data.Automations
	archived := newArchivedAutomationContext(data)
	allItems := []SelectableItem{}
	visibleItems := []SelectableItem{}
	add := func(kind, ref, label string, primary PortalAction, related []PortalAction, visible bool) {
		item := SelectableItem{Kind: SelectableKindRecord, Screen: ScreenAutomations, RecordKind: kind, RecordRef: ref, Label: label, RecordLabel: label, PrimaryAction: actionPtr(primary), RelatedActions: related}
		allItems = append(allItems, item)
		if visible || state.RawDetails {
			visibleItems = append(visibleItems, item)
		}
	}
	for _, invocation := range data.InvocationFailures {
		inspect := NewInvocationInspectAction(invocation, ScreenAutomations)
		add("invocation_failure", invocation.InvocationID, invocation.InvocationID, inspect, nil, false)
	}
	for _, event := range data.DirectEventFailures {
		inspect := NewDirectEventInspectAction(event, ScreenAutomations)
		add("direct_event_failure", event.DirectEventID, event.DirectEventID, inspect, []PortalAction{NewDirectEventRawPayloadAction(event, ScreenAutomations)}, false)
	}
	for _, automation := range data.Automations {
		inspect := NewAutomationInspectAction(automation, ScreenAutomations)
		add("automation", automation.AutomationID, firstNonEmpty(automation.DisplayName, automation.AutomationKey, automation.AutomationID), inspect, nil, true)
	}
	for _, schedule := range data.Schedules {
		inspect := NewScheduleInspectAction(schedule, ScreenAutomations)
		related := []PortalAction{}
		if !archived.scheduleArchived(schedule) {
			related = append(related, NewScheduleFireNowAction(schedule, ScreenAutomations), NewSchedulePauseAction(schedule, ScreenAutomations), NewScheduleResumeAction(schedule, ScreenAutomations))
		}
		add("schedule", schedule.ScheduleID, firstNonEmpty(schedule.DisplayName, schedule.ScheduleKey, schedule.ScheduleID), inspect, related, true)
	}
	for _, integration := range data.Integrations {
		inspect := NewIntegrationInspectAction(integration, ScreenAutomations)
		add("integration", integration.IntegrationID, firstNonEmpty(integration.DisplayName, integration.IntegrationKey, integration.IntegrationID), inspect, nil, false)
	}
	for _, fire := range data.ScheduleFires {
		inspect := NewScheduleFireInspectAction(fire, ScreenAutomations)
		add("schedule_fire", fire.ScheduleFireID, fire.ScheduleFireID, inspect, nil, false)
	}
	for _, endpoint := range data.DirectEventEndpoints {
		inspect := NewDirectEventEndpointInspectAction(endpoint, ScreenAutomations)
		related := []PortalAction{}
		if !archived.directEndpointArchived(endpoint) {
			related = append(related, NewDirectEventEndpointTestAction(endpoint, ScreenAutomations))
		}
		add("direct_event_endpoint", endpoint.EndpointID, firstNonEmpty(endpoint.DisplayName, endpoint.EndpointSlug, endpoint.EndpointID), inspect, related, true)
	}
	for _, event := range data.DirectEvents {
		inspect := NewDirectEventInspectAction(event, ScreenAutomations)
		add("direct_event", event.DirectEventID, event.DirectEventID, inspect, []PortalAction{NewDirectEventRawPayloadAction(event, ScreenAutomations)}, false)
	}
	for _, invocation := range data.Invocations {
		inspect := NewInvocationInspectAction(invocation, ScreenAutomations)
		add("invocation", invocation.InvocationID, invocation.InvocationID, inspect, nil, false)
	}
	return withOperationalActionGroupsFromCandidates(state, visibleItems, allItems)
}

func jobsSelectableItems(state ScreenState) []SelectableItem {
	data := state.Data.Jobs
	archivedProjects := archivedProjectIndexFromProjects(data.ArchivedProjects)
	allItems := []SelectableItem{}
	visibleItems := []SelectableItem{}
	addCandidate := func(item SelectableItem, visible bool) {
		allItems = append(allItems, item)
		if visible || state.RawDetails {
			visibleItems = append(visibleItems, item)
		}
	}
	addJobRecord := func(kind string, job jobs.Job, visible bool) {
		inspect := NewJobInspectAction(job, ScreenJobs)
		ref := job.JobID
		label := firstNonEmpty(job.JobID, job.JobType)
		related := jobRelatedActions(job, ScreenJobs, archivedProjects.jobArchived(job))
		addCandidate(SelectableItem{Kind: SelectableKindRecord, Screen: ScreenJobs, RecordKind: kind, RecordRef: ref, Label: label, RecordLabel: label, PrimaryAction: actionPtr(inspect), RelatedActions: related}, visible)
	}
	for _, job := range data.FailedJobs {
		addJobRecord("failed_job", job, false)
	}
	for _, job := range jobsWithStatus(data.Jobs, jobs.StatusRunning) {
		addJobRecord("job", job, true)
	}
	for _, job := range data.QueuedJobs {
		addJobRecord("queued_job", job, true)
	}
	for _, runner := range data.Runners {
		inspect := NewRunnerInspectAction(runner, ScreenJobs)
		addCandidate(SelectableItem{Kind: SelectableKindRecord, Screen: ScreenJobs, RecordKind: "runner", RecordRef: runner.RunnerID, Label: firstNonEmpty(runner.RunnerKey, runner.RunnerID), RecordLabel: firstNonEmpty(runner.RunnerKey, runner.RunnerID), PrimaryAction: actionPtr(inspect)}, true)
	}
	for _, job := range jobsWithoutStatus(data.Jobs, jobs.StatusRunning) {
		addJobRecord("job", job, false)
	}
	for _, failure := range data.IndexFailures {
		inspect := NewIndexInspectAction(failure, ScreenJobs)
		addCandidate(SelectableItem{
			Kind:           SelectableKindRecord,
			Label:          firstNonEmpty(failure.IndexStatusID, failure.ObjectID),
			Screen:         ScreenJobs,
			RecordKind:     "index_failure",
			RecordRef:      failure.IndexStatusID,
			RecordLabel:    firstNonEmpty(failure.IndexStatusID, failure.ObjectID),
			PrimaryAction:  actionPtr(inspect),
			RelatedActions: indexRelatedActions(failure),
		}, false)
	}
	for _, item := range data.IndexQueue {
		inspect := NewIndexInspectAction(item, ScreenJobs)
		addCandidate(SelectableItem{
			Kind:           SelectableKindRecord,
			Label:          firstNonEmpty(item.IndexStatusID, item.ObjectID),
			Screen:         ScreenJobs,
			RecordKind:     "index_queue",
			RecordRef:      item.IndexStatusID,
			RecordLabel:    firstNonEmpty(item.IndexStatusID, item.ObjectID),
			PrimaryAction:  actionPtr(inspect),
			RelatedActions: indexRelatedActions(item),
		}, false)
	}
	for _, status := range data.IndexStatuses {
		inspect := NewIndexInspectAction(status, ScreenJobs)
		addCandidate(SelectableItem{
			Kind:           SelectableKindRecord,
			Label:          firstNonEmpty(status.IndexStatusID, status.ObjectID),
			Screen:         ScreenJobs,
			RecordKind:     "index_status",
			RecordRef:      status.IndexStatusID,
			RecordLabel:    firstNonEmpty(status.IndexStatusID, status.ObjectID),
			PrimaryAction:  actionPtr(inspect),
			RelatedActions: indexRelatedActions(status),
		}, false)
	}
	return withOperationalActionGroupsFromCandidates(state, visibleItems, allItems)
}

func jobRelatedActions(job jobs.Job, screen string, archivedContext bool) []PortalAction {
	related := []PortalAction{NewJobLogsInspectAction(job, screen), NewJobOutputsInspectAction(job, screen)}
	if jobs.JobNeedsFailureAttention(job) {
		related = append(related, NewJobAcknowledgeAttentionAction(job, screen), NewJobArchiveAttentionAction(job, screen))
	}
	if !archivedContext {
		related = append(related, NewJobRetryAction(job, screen), NewJobCancelAction(job, screen))
	}
	return related
}

func deletionRequestRelatedActions(request loomsync.DeletionRequest, screen string) []PortalAction {
	status := loomsync.NormalizeDeletionRequestStatus(request.Status)
	if status == loomsync.DeletionRequestStatusDenied || status == loomsync.DeletionRequestStatusCompleted {
		return nil
	}
	return []PortalAction{
		NewDeletionRequestReviewAction(request, screen),
		NewDeletionRequestApproveAction(request, screen),
		NewDeletionRequestDenyAction(request, screen),
		NewDeletionRequestCompleteAction(request, screen),
	}
}

func nodesSelectableItems(state ScreenState) []SelectableItem {
	items := nodesDefaultSelectableItems(state)
	if !state.RawDetails {
		candidates := nodesDetailedActionCandidateItems(state, items)
		return withOperationalActionGroupsFromCandidates(state, nodesDefaultVisibleSelectableItems(items), candidates)
	}
	return withOperationalActionGroups(state, nodesDetailedActionCandidateItems(state, items))
}

func nodesDetailedActionCandidateItems(state ScreenState, items []SelectableItem) []SelectableItem {
	items = append([]SelectableItem(nil), items...)
	row := len(items)
	data := state.Data.Nodes
	for _, node := range data.Nodes {
		inspect := NewNodeInspectAction(node, ScreenNodes)
		addNodeSelectable(&items, &row, "node", node.NodeID, firstNonEmpty(node.NodeKey, node.NodeID), inspect, []PortalAction{NewNodeHealthAction(node, ScreenNodes)}, "Raw node registration")
	}
	for _, root := range data.WatchedRoots {
		inspect := NewWatchedRootInspectAction(root, ScreenNodes)
		addNodeSelectable(&items, &row, "watched_root", root.Root.NodeID+"/"+root.Root.RootKey, root.Root.NodeID+"/"+root.Root.RootKey, inspect, nil, "Raw watched-root registration")
	}
	for _, finding := range data.WatchedRootFindings {
		inspect := NewWatchedRootFindingInspectAction(finding, ScreenNodes)
		addNodeSelectable(&items, &row, "watched_root_finding", finding.NodeID+"/"+finding.RootKey, finding.Summary, inspect, nil, "Raw watched-root finding")
	}
	if data.BackupStatusSelectableCount() > 0 {
		inspect := NewWatchedRootBackupStatusInspectAction(data.BackupStatus, ScreenNodes)
		addNodeSelectable(&items, &row, "watched_root_backup_status", data.BackupStatus.Root.NodeID+"/"+data.BackupStatus.Root.RootKey, data.BackupStatus.Root.NodeID+"/"+data.BackupStatus.Root.RootKey, inspect, nil, "Raw watched-root backup status")
	}
	for _, batch := range data.BackupBatches {
		inspect := NewWatchedRootBackupBatchInspectAction(batch, ScreenNodes)
		addNodeSelectable(&items, &row, "watched_root_backup_batch", batch.WatchedRootBackupBatchID, batch.WatchedRootBackupBatchID, inspect, nil, "Raw watched-root backup batch")
	}
	for _, batch := range data.SyncBatches {
		inspect := NewSyncBatchInspectAction(batch, ScreenNodes)
		addNodeSelectable(&items, &row, "sync_batch", batch.SyncBatchID, batch.SyncBatchID, inspect, nil, "Raw sync batch")
	}
	for _, conflict := range data.SyncConflicts {
		inspect := NewSyncConflictInspectAction(conflict, ScreenNodes)
		addNodeSelectable(&items, &row, "sync_conflict", conflict.SyncConflictID, conflict.SyncConflictID, inspect, nil, "Raw sync conflict")
	}
	for _, replica := range data.SyncReplicas {
		inspect := NewSyncReplicaInspectAction(replica, ScreenNodes)
		addNodeSelectable(&items, &row, "sync_replica", replica.ReplicaID, replica.ReplicaID, inspect, nil, "Raw sync replica")
	}
	for _, backup := range data.PrivateBackups {
		inspect := NewPrivateBackupInspectAction(backup, ScreenNodes)
		addNodeSelectable(&items, &row, "private_backup", backup.PrivateBackupOperationID, backup.PrivateBackupOperationID, inspect, nil, "Raw private backup")
	}
	for _, request := range data.DeletionRequests {
		inspect := NewDeletionRequestInspectAction(request, ScreenNodes)
		addNodeSelectable(&items, &row, "deletion_request", request.DeletionRequestID, request.DeletionRequestID, inspect, deletionRequestRelatedActions(request, ScreenNodes), "Raw deletion request")
	}
	return items
}

func nodesDefaultVisibleSelectableItems(items []SelectableItem) []SelectableItem {
	result := make([]SelectableItem, 0, len(items))
	for _, item := range items {
		switch item.RecordKind {
		case "node", "node_summary", "protected_folder", "watched_root":
			result = append(result, item)
		}
	}
	return result
}

func nodesDefaultSelectableItems(state ScreenState) []SelectableItem {
	data := state.Data.Nodes
	items := []SelectableItem{}
	row := 0
	for _, group := range nodeFindingGroups(data.WatchedRootFindings) {
		if !nodeSeverityIsActionable(group.Severity) {
			continue
		}
		action := NewNodeFindingGroupInspectAction(data, group)
		addNodeSelectable(&items, &row, "watched_root_finding_group", group.Ref, firstNonEmpty(group.Summary, group.Kind, "Watched-root finding"), action, nil, fmt.Sprintf("%s %s", nodeDisplayName(data, group.NodeRef), firstNonEmpty(group.Severity, "finding")))
	}
	for _, conflict := range data.SyncConflicts {
		inspect := NewSyncConflictInspectAction(conflict, ScreenNodes)
		addNodeSelectable(&items, &row, "sync_conflict", conflict.SyncConflictID, firstNonEmpty(conflict.Summary, "Sync conflict"), inspect, nil, fmt.Sprintf("%s sync conflict", nodeDisplayName(data, conflict.OriginNodeID)))
	}
	for _, request := range data.DeletionRequests {
		if !nodeDeletionRequestIsActionable(request) {
			continue
		}
		inspect := NewDeletionRequestInspectAction(request, ScreenNodes)
		addNodeSelectable(&items, &row, "deletion_request", request.DeletionRequestID, firstNonEmpty(request.TargetRef, "Deletion request"), inspect, deletionRequestRelatedActions(request, ScreenNodes), fmt.Sprintf("%s deletion request", nodeDisplayName(data, request.OriginNodeID)))
	}
	for _, summary := range nodePortalSummaries(data) {
		if node, ok := nodeRecordForSummary(data, summary); ok {
			inspect := NewNodeInspectAction(node, ScreenNodes)
			addNodeSelectable(&items, &row, "node", firstNonEmpty(node.NodeID, node.NodeKey), summary.Label, inspect, []PortalAction{NewNodeHealthAction(node, ScreenNodes)}, nodeSummaryDescription(summary))
			continue
		}
		action := NewNodeSummaryInspectAction(summary)
		addNodeSelectable(&items, &row, "node_summary", summary.Ref, summary.Label, action, nil, nodeSummaryDescription(summary))
	}
	for _, record := range orderedProtectedFolders(data.ProtectedFolders) {
		inspect, related := protectedFolderActions(record)
		label := firstNonEmpty(record.Contract.DisplayName, record.Key)
		addNodeSelectable(&items, &row, "protected_folder", record.Key, label, inspect, related, fmt.Sprintf("%s %s", nodeDisplayName(data, firstNonEmpty(record.OwnerNodeKey, record.OwnerNodeID, record.Contract.OwnerNode)), firstNonEmpty(record.Lifecycle, "unknown")))
	}
	for _, group := range watchedRootsByPurpose(data) {
		for _, root := range group.Roots {
			inspect := NewWatchedRootInspectAction(root, ScreenNodes)
			addNodeSelectable(&items, &row, "watched_root", root.Root.NodeID+"/"+root.Root.RootKey, watchedRootPurposefulLabel(data, root.Root), inspect, nil, fmt.Sprintf("%s shared root", group.Label))
		}
	}
	return items
}

func addNodeSelectable(items *[]SelectableItem, row *int, kind, ref, label string, primary PortalAction, related []PortalAction, description string) {
	*items = append(*items, SelectableItem{
		Kind:           SelectableKindRecord,
		Screen:         ScreenNodes,
		RowIndex:       *row,
		RecordKind:     kind,
		RecordRef:      ref,
		Label:          label,
		Description:    description,
		RecordLabel:    label,
		PrimaryAction:  actionPtr(primary),
		RelatedActions: related,
	})
	(*row)++
}

func nodeRecordForSummary(data NodesData, summary nodePortalSummary) (nodeRecord nodes.Node, ok bool) {
	for _, node := range data.Nodes {
		if summary.Ref != "" && (node.NodeID == summary.Ref || node.NodeKey == summary.Ref) {
			return node, true
		}
	}
	return nodes.Node{}, false
}

func nodeSummaryDescription(summary nodePortalSummary) string {
	return strings.Join([]string{
		"watchers=" + fmt.Sprintf("%d", summary.WatchedRoots),
		"findings=" + fmt.Sprintf("%d", summary.OpenFindings),
		"sync_conflicts=" + fmt.Sprintf("%d", summary.SyncConflicts),
		"backup=" + firstNonEmpty(summary.BackupStatus, "not reported"),
	}, " ")
}

func NewNodeSummaryInspectAction(summary nodePortalSummary) PortalAction {
	payload := map[string]string{
		"node":              summary.Label,
		"node_ref":          firstNonEmpty(summary.Ref, "-"),
		"kind":              firstNonEmpty(summary.Kind, "-"),
		"role":              firstNonEmpty(summary.Role, "-"),
		"runtime":           firstNonEmpty(summary.Runtime, "-"),
		"status":            firstNonEmpty(summary.Status, "-"),
		"presence":          firstNonEmpty(summary.Presence, "-"),
		"agent":             nodeAgentLabel(summary),
		"watched_roots":     fmt.Sprintf("%d", summary.WatchedRoots),
		"open_findings":     fmt.Sprintf("%d", summary.OpenFindings),
		"sync_conflicts":    fmt.Sprintf("%d", summary.SyncConflicts),
		"deletion_requests": fmt.Sprintf("%d", summary.DeletionRequests),
		"private_backups":   fmt.Sprintf("%d", summary.PrivateBackups),
		"backup_status":     firstNonEmpty(summary.BackupStatus, "-"),
	}
	return NewPortalRecordInspectAction("nodes", ScreenNodes, "node_summary", firstNonEmpty(summary.Ref, summary.Label), summary.Label, payload)
}

func NewNodeFindingGroupInspectAction(data NodesData, group nodeFindingGroup) PortalAction {
	payload := map[string]string{
		"node":       nodeDisplayName(data, group.NodeRef),
		"node_ref":   firstNonEmpty(group.NodeRef, "-"),
		"root":       firstNonEmpty(titleFromToken(group.RootRef), group.RootRef, "-"),
		"root_ref":   firstNonEmpty(group.RootRef, "-"),
		"severity":   firstNonEmpty(group.Severity, "-"),
		"kind":       firstNonEmpty(group.Kind, "-"),
		"summary":    firstNonEmpty(group.Summary, "-"),
		"count":      fmt.Sprintf("%d", group.Count),
		"latest":     shortTimeOrDash(group.Latest),
		"finding_id": firstNonEmpty(group.Prototype.WatchedRootFindingID, "-"),
	}
	return NewPortalRecordInspectAction("nodes", ScreenNodes, "watched_root_finding_group", group.Ref, firstNonEmpty(group.Summary, group.Kind, "Watched-root finding"), payload)
}

func capabilitiesSelectableItems(state ScreenState) []SelectableItem {
	data := state.Data.Capabilities
	explorer := normalizeCapabilityExplorerState(data)
	archivedProjects := archivedProjectIndexFromProjects(data.ArchivedProjects)
	items := []SelectableItem{}
	row := 0
	add := func(item SelectableItem) {
		item.Screen = ScreenCapabilities
		item.RowIndex = row
		items = append(items, item)
		row++
	}

	for _, explorerRow := range capabilityExplorerRows(data) {
		switch explorerRow.Kind {
		case CapabilityExplorerRowScope:
			add(SelectableItem{
				Kind:        SelectableKindRecord,
				Label:       explorerRow.Label,
				Description: explorerRow.Description,
				RecordKind:  "capability_scope",
				RecordRef:   explorerRow.Scope,
				RecordLabel: explorerRow.Label,
			})
		case CapabilityExplorerRowProvider:
			related := []PortalAction{}
			if explorerRow.Provider != nil {
				related = []PortalAction{NewProviderInspectAction(*explorerRow.Provider, ScreenCapabilities), NewProviderHealthAction(*explorerRow.Provider, ScreenCapabilities)}
			}
			ref := explorerRow.ProviderAddress
			add(SelectableItem{
				Kind:           SelectableKindRecord,
				Label:          firstNonEmpty(ref, explorerRow.Label),
				Description:    explorerRow.Description,
				RecordKind:     "provider",
				RecordRef:      ref,
				RecordLabel:    firstNonEmpty(ref, explorerRow.Label),
				RelatedActions: related,
			})
			if explorer.ExpandedRef == ref {
				for _, action := range related {
					add(capabilityInlineActionItem(action, ref))
				}
			}
		case CapabilityExplorerRowCapability:
			if explorerRow.Capability == nil {
				continue
			}
			inspect := NewCapabilityInspectAction(*explorerRow.Capability, ScreenCapabilities)
			ref := firstNonEmpty(explorerRow.Address, explorerRow.Capability.CapabilityEndpoint.CompactAddress, explorerRow.Capability.CapabilityEndpoint.CapabilityEndpointID)
			related := []PortalAction{NewCapabilityUsageDocsAction(*explorerRow.Capability, ScreenCapabilities)}
			if !archivedProjects.capabilityArchived(*explorerRow.Capability) {
				related = append(related, NewCapabilityCallAction(*explorerRow.Capability, ScreenCapabilities))
			}
			add(SelectableItem{
				Kind:           SelectableKindRecord,
				Label:          firstNonEmpty(ref, explorerRow.Label),
				Description:    explorerRow.Description,
				RecordKind:     "capability",
				RecordRef:      ref,
				RecordLabel:    firstNonEmpty(ref, explorerRow.Label),
				PrimaryAction:  actionPtr(inspect),
				RelatedActions: related,
			})
			if explorer.ExpandedRef == ref {
				for _, action := range append([]PortalAction{inspect}, related...) {
					add(capabilityInlineActionItem(action, ref))
				}
			}
		}
	}

	return items
}

func capabilityInlineActionItem(action PortalAction, address string) SelectableItem {
	return SelectableItem{
		Kind:           SelectableKindAction,
		Label:          action.Label,
		Description:    action.Description,
		ActionID:       action.ID,
		RecordKind:     "capability_inline_action",
		RecordRef:      action.ID,
		RecordLabel:    address,
		PrimaryAction:  actionPtr(action),
		DisabledReason: action.DisabledReason,
	}
}

func NewWorkerInspectAction(worker workers.WorkerListItem, sourceScreen string) PortalAction {
	ref := firstNonEmpty(worker.WorkerKey, worker.WorkerInstanceID)
	action := PortalAction{
		ID:           fmt.Sprintf("worker.%s.inspect", safeActionID(ref)),
		Label:        "Inspect " + workerDisplayName(worker),
		Description:  "Inspect worker status, health, locality, and recent state.",
		Domain:       "workers",
		SourceScreen: NormalizeScreen(sourceScreen),
		TargetKind:   "worker",
		TargetRef:    ref,
		TargetLabel:  workerDisplayName(worker),
		Risk:         ActionRiskInspect,
		State:        ActionAvailable,
		InputValues:  map[string]string{},
		Executor: PortalActionExecutor{
			Kind:   PortalExecutorWorkerInspect,
			Target: ref,
			Payload: map[string]string{
				"worker_key":       worker.WorkerKey,
				"worker_kind":      worker.WorkerKind,
				"health_status":    worker.HealthStatus,
				"lifecycle_status": worker.LifecycleStatus,
			},
		},
		RawCommand:    []string{"loom", "worker", "inspect", ref},
		RawDetails:    map[string]string{"worker_instance_id": worker.WorkerInstanceID, "worker_kind": worker.WorkerKind},
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewWorkerRunsAction(worker workers.WorkerListItem, sourceScreen string) PortalAction {
	ref := firstNonEmpty(worker.WorkerKey, worker.WorkerInstanceID)
	action := PortalAction{
		ID:            fmt.Sprintf("worker.%s.runs", safeActionID(ref)),
		Label:         "Inspect " + workerDisplayName(worker) + " Runs",
		Description:   "Inspect recent worker runs for this worker.",
		Domain:        "workers",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "worker",
		TargetRef:     ref,
		TargetLabel:   workerDisplayName(worker),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorWorkerRunsInspect, Target: ref},
		RawCommand:    []string{"loom", "worker", "runs", ref},
		RawDetails:    map[string]string{"worker_kind": worker.WorkerKind},
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewWorkerRunOnceAction(worker workers.WorkerListItem, sourceScreen string) PortalAction {
	ref := firstNonEmpty(worker.WorkerKey, worker.WorkerInstanceID)
	action := PortalAction{
		ID:            fmt.Sprintf("worker.%s.run_once", safeActionID(ref)),
		Label:         "Run " + workerDisplayName(worker) + " Once",
		Description:   "Request one bounded manual run for this worker.",
		Domain:        "workers",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "worker",
		TargetRef:     ref,
		TargetLabel:   workerDisplayName(worker),
		Risk:          ActionRiskSafeRun,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorWorkerRunOnce, Target: ref, Payload: map[string]string{"worker_kind": worker.WorkerKind}},
		RawCommand:    []string{"loom", "worker", "run", ref, "--once"},
		RawDetails:    map[string]string{"worker_instance_id": worker.WorkerInstanceID, "worker_kind": worker.WorkerKind},
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if reason := workerRunOnceDisabledReason(worker); reason != "" {
		action.State = ActionDisabled
		action.DisabledReason = reason
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewIndexInspectAction(status search.IndexStatus, sourceScreen string) PortalAction {
	ref := firstNonEmpty(status.IndexStatusID, status.ObjectID)
	action := PortalAction{
		ID:            fmt.Sprintf("index.%s.inspect", safeActionID(ref)),
		Label:         "Inspect Index Work",
		Description:   "Inspect index status, object ref, attempts, and failure details.",
		Domain:        "search",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "index_status",
		TargetRef:     ref,
		TargetLabel:   firstNonEmpty(status.IndexStatusID, status.ObjectID),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorIndexInspect, Target: ref, Payload: indexStatusPayload(status)},
		RawCommand:    []string{"loom", "indexes", "queue", "show", ref},
		RawDetails:    indexStatusPayload(status),
		RefreshScreen: ScreenJobs,
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewIndexExplainObjectAction(status search.IndexStatus) PortalAction {
	objectRef := indexObjectRef(status)
	action := PortalAction{
		ID:            fmt.Sprintf("index.%s.explain_object", safeActionID(firstNonEmpty(objectRef, status.IndexStatusID))),
		Label:         "Explain Object Index",
		Description:   "Explain current index state for this object.",
		Domain:        "search",
		SourceScreen:  ScreenJobs,
		TargetKind:    "object",
		TargetRef:     objectRef,
		TargetLabel:   firstNonEmpty(objectRef, status.ObjectID, status.IndexStatusID),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorIndexExplainObject, Target: objectRef, Payload: indexStatusPayload(status)},
		RawCommand:    []string{"loom", "indexes", "explain", "object", objectRef},
		RawDetails:    indexStatusPayload(status),
		RefreshScreen: ScreenJobs,
	}
	if objectRef == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This index record does not include an object ref."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewIndexRetryAction(status search.IndexStatus) PortalAction {
	ref := status.IndexStatusID
	action := PortalAction{
		ID:            fmt.Sprintf("index.%s.retry", safeActionID(firstNonEmpty(ref, status.ObjectID))),
		Label:         "Retry Index Work",
		Description:   "Retry this failed or manually blocked index work item.",
		Domain:        "search",
		SourceScreen:  ScreenJobs,
		TargetKind:    "index_status",
		TargetRef:     ref,
		TargetLabel:   firstNonEmpty(ref, status.ObjectID),
		Risk:          ActionRiskSafeRun,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorIndexRetry, Target: ref, Payload: indexStatusPayload(status)},
		RawCommand:    []string{"loom", "indexes", "retry", ref},
		RawDetails:    indexStatusPayload(status),
		RefreshScreen: ScreenJobs,
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This index record does not include an index status ID."
	} else if !indexStatusRetryable(status) {
		action.State = ActionDisabled
		action.DisabledReason = "This index record is not failed or marked for manual retry."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewIndexRetryFailedAction(input search.IndexRetryFailedInput) PortalAction {
	limit := input.Limit
	if limit <= 0 {
		limit = 50
	}
	action := PortalAction{
		ID:           "index.retry_failed",
		Label:        "Retry Failed Index Work",
		Description:  "Retry failed index work records returned by the backend.",
		Domain:       "search",
		SourceScreen: ScreenJobs,
		TargetKind:   "index_failures",
		TargetRef:    "failed",
		TargetLabel:  "Failed index work",
		Risk:         ActionRiskSafeRun,
		State:        ActionAvailable,
		InputValues:  map[string]string{},
		Executor: PortalActionExecutor{
			Kind:   PortalExecutorIndexRetryFailed,
			Target: "failed",
			Payload: map[string]string{
				"limit":       fmt.Sprintf("%d", limit),
				"object_ref":  input.ObjectRef,
				"project_ref": input.ProjectRef,
				"scope_ref":   input.ScopeRef,
				"index_type":  input.IndexType,
			},
		},
		RawCommand:    []string{"loom", "indexes", "retry-failed"},
		RawDetails:    map[string]string{"limit": fmt.Sprintf("%d", limit)},
		RefreshScreen: ScreenJobs,
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewIndexRebuildObjectAction(status search.IndexStatus) PortalAction {
	objectRef := indexObjectRef(status)
	action := PortalAction{
		ID:            fmt.Sprintf("index.%s.rebuild_object", safeActionID(firstNonEmpty(objectRef, status.IndexStatusID))),
		Label:         "Rebuild Object Index",
		Description:   "Queue fresh index rebuild work for this object.",
		Domain:        "search",
		SourceScreen:  ScreenJobs,
		TargetKind:    "object",
		TargetRef:     objectRef,
		TargetLabel:   firstNonEmpty(objectRef, status.ObjectID, status.IndexStatusID),
		Risk:          ActionRiskSafeRun,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorIndexRebuildObject, Target: objectRef, Payload: indexStatusPayload(status)},
		RawCommand:    []string{"loom", "indexes", "rebuild", "object", objectRef},
		RawDetails:    indexStatusPayload(status),
		RefreshScreen: ScreenJobs,
	}
	if objectRef == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This index record does not include an object ref."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewMaintenanceFindingInspectAction(finding maintenance.Finding) PortalAction {
	ref := firstNonEmpty(finding.FindingKey, finding.MaintenanceFindingID, finding.Summary)
	action := PortalAction{
		ID:            fmt.Sprintf("maintenance.finding.%s.inspect", safeActionID(ref)),
		Label:         "Inspect Maintenance Finding",
		Description:   firstNonEmpty(finding.Summary, "Inspect this maintenance finding."),
		Domain:        "maintenance",
		SourceScreen:  ScreenBackground,
		TargetKind:    "maintenance_finding",
		TargetRef:     ref,
		TargetLabel:   firstNonEmpty(finding.Summary, ref),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorMaintenanceInspect, Target: ref, Payload: maintenanceFindingPayload(finding)},
		RawDetails:    maintenanceFindingPayload(finding),
		RefreshScreen: ScreenBackground,
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewMaintenanceDBInspectAction(status maintenance.DBStatus) PortalAction {
	action := maintenanceInspectAction("maintenance.db.inspect", "Inspect Database Maintenance", "Inspect migration and database maintenance state.", "maintenance_db", "maintenance.db", maintenanceDBPayload(status))
	return action
}

func NewMaintenanceBackupStatusInspectAction(status maintenance.BackupStatus) PortalAction {
	return maintenanceInspectAction("maintenance.backup.inspect", "Inspect Main Backups", "Inspect main-node backup health and recent backup state.", "maintenance_backup_status", "maintenance.backup", maintenanceBackupStatusPayload(status))
}

func NewMaintenanceBackupOperationInspectAction(backup maintenance.BackupOperation) PortalAction {
	ref := firstNonEmpty(backup.Operation.MaintenanceOperationID, backup.Operation.OperationKey)
	return maintenanceInspectAction(fmt.Sprintf("maintenance.backup.%s.inspect", safeActionID(ref)), "Inspect Backup Operation", "Inspect this main backup operation.", "maintenance_backup_operation", ref, maintenanceBackupOperationPayload(backup))
}

func NewMaintenanceBackupRunAction() PortalAction {
	action := PortalAction{
		ID:            "maintenance.backup.run",
		Label:         "Run Main Backup Once",
		Description:   "Request one bounded main-node backup run.",
		Domain:        "maintenance",
		SourceScreen:  ScreenBackground,
		TargetKind:    "maintenance_backup",
		TargetRef:     "main_backup",
		TargetLabel:   "Main backup",
		Risk:          ActionRiskSafeRun,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorMaintenanceBackupRun, Target: "main_backup"},
		RawCommand:    []string{"loom", "maintenance", "backup", "run", "--once"},
		RawDetails:    map[string]string{"operation_kind": maintenance.OperationKindMainBackup},
		RefreshScreen: ScreenBackground,
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewMaintenanceBackupVerifyAction(backup maintenance.BackupOperation) PortalAction {
	ref := backup.Operation.MaintenanceOperationID
	action := PortalAction{
		ID:            fmt.Sprintf("maintenance.backup.%s.verify", safeActionID(firstNonEmpty(ref, backup.Operation.OperationKey))),
		Label:         "Verify Backup",
		Description:   "Verify this registered main backup.",
		Domain:        "maintenance",
		SourceScreen:  ScreenBackground,
		TargetKind:    "maintenance_backup_operation",
		TargetRef:     ref,
		TargetLabel:   firstNonEmpty(ref, backup.Operation.OperationKey),
		Risk:          ActionRiskSafeRun,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorMaintenanceBackupVerify, Target: ref, Payload: maintenanceBackupOperationPayload(backup)},
		RawCommand:    []string{"loom", "maintenance", "backup", "verify", ref},
		RawDetails:    maintenanceBackupOperationPayload(backup),
		RefreshScreen: ScreenBackground,
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This backup operation does not include a maintenance operation ID."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewMaintenanceObjectStoreInspectAction(status maintenance.ObjectStoreStatus) PortalAction {
	return maintenanceInspectAction("maintenance.object_store.inspect", "Inspect Object Store", "Inspect object-store integrity state.", "maintenance_object_store", "maintenance.object_store", maintenanceObjectStorePayload(status))
}

func NewUpdateStatusInspectAction(status update.UpdateStatus) PortalAction {
	return maintenanceInspectAction(
		"maintenance.update.inspect",
		"Inspect Production Updates",
		"Inspect production update state, rollback class, and maintenance-window status.",
		"update_status",
		firstNonEmpty(status.ActiveManifestPath, status.StateDir, "update.status"),
		updateStatusPayload(status),
	)
}

func NewMaintenanceObjectStoreScanAction() PortalAction {
	action := PortalAction{
		ID:            "maintenance.object_store.scan_sample",
		Label:         "Run Object-Store Sample Scan",
		Description:   "Request one bounded sample scan of the object store.",
		Domain:        "maintenance",
		SourceScreen:  ScreenBackground,
		TargetKind:    "maintenance_object_store",
		TargetRef:     "sample",
		TargetLabel:   "Object-store sample",
		Risk:          ActionRiskSafeRun,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorMaintenanceObjectScan, Target: "sample", Payload: map[string]string{"mode": "sample"}},
		RawCommand:    []string{"loom", "maintenance", "object-store", "scan", "--sample"},
		RawDetails:    map[string]string{"mode": "sample"},
		RefreshScreen: ScreenBackground,
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func maintenanceInspectAction(id, label, description, targetKind, targetRef string, payload map[string]string) PortalAction {
	action := PortalAction{
		ID:            id,
		Label:         label,
		Description:   description,
		Domain:        "maintenance",
		SourceScreen:  ScreenBackground,
		TargetKind:    targetKind,
		TargetRef:     targetRef,
		TargetLabel:   label,
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorMaintenanceInspect, Target: targetRef, Payload: payload},
		RawDetails:    payload,
		RefreshScreen: ScreenBackground,
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func WellKnownDynamicActions() []PortalAction {
	return []PortalAction{
		NewIndexRetryFailedAction(search.IndexRetryFailedInput{Limit: 50}),
		NewMaintenanceBackupRunAction(),
		NewMaintenanceObjectStoreScanAction(),
	}
}

func indexRelatedActions(status search.IndexStatus) []PortalAction {
	return []PortalAction{
		NewIndexExplainObjectAction(status),
		NewIndexRetryAction(status),
		NewIndexRebuildObjectAction(status),
	}
}

func workerRunOnceDisabledReason(worker workers.WorkerListItem) string {
	if worker.WorkerKey == "" && worker.WorkerInstanceID == "" {
		return "This worker does not include a backend worker ref."
	}
	switch worker.LifecycleStatus {
	case workers.LifecycleDisabled, workers.LifecycleRetired:
		return "This worker is not active."
	}
	if !worker.Enabled {
		return "This worker is disabled."
	}
	if worker.Paused {
		return "This worker is paused."
	}
	switch worker.Locality {
	case workers.LocalityNodeAgentOwned, workers.LocalityExternalReported:
		return "This worker is not main-owned and cannot be run directly from this portal slice."
	}
	return ""
}

func workerDisplayName(worker workers.WorkerListItem) string {
	if worker.DisplayName != "" {
		return worker.DisplayName
	}
	switch worker.WorkerKind {
	case workers.KindSelfcheck:
		return "Worker Selfcheck"
	case workers.KindDBMaintenance:
		return "Database Maintenance"
	case workers.KindMainBackup:
		return "Main Backup"
	case workers.KindObjectStore:
		return "Object Store Integrity"
	case workers.KindIndexerText:
		return "Text Indexer"
	case workers.KindJobRunner:
		return "Job Runner"
	case workers.KindJobSweeper:
		return "Job Sweeper"
	case workers.KindAutomationScheduler:
		return "Automation Scheduler"
	case workers.KindAutomationDispatcher:
		return "Automation Dispatcher"
	case workers.KindDirectEventIngest:
		return "Direct Event Ingest"
	default:
		return titleFromToken(firstNonEmpty(worker.WorkerKind, worker.WorkerKey, "Worker"))
	}
}

func indexStatusPayload(status search.IndexStatus) map[string]string {
	return map[string]string{
		"index_status_id":   status.IndexStatusID,
		"source_kind":       status.SourceKind,
		"source_id":         status.SourceID,
		"source_version_id": status.SourceVersionID,
		"object_id":         status.ObjectID,
		"object_version_id": status.ObjectVersionID,
		"scope_id":          status.ScopeID,
		"index_type":        status.IndexType,
		"status":            status.Status,
		"attempts":          fmt.Sprintf("%d", status.AttemptCount),
		"last_error_code":   status.LastErrorCode,
		"last_error":        status.LastErrorMessage,
		"last_worker_run":   status.LastWorkerRunID,
		"manual_action":     fmt.Sprintf("%t", status.ManualAction),
		"next_attempt_at":   timePtrOrDash(status.NextAttemptAt),
	}
}

func maintenanceFindingPayload(finding maintenance.Finding) map[string]string {
	return map[string]string{
		"finding_id":   finding.MaintenanceFindingID,
		"finding_key":  finding.FindingKey,
		"worker":       finding.WorkerKey,
		"kind":         finding.FindingKind,
		"severity":     finding.Severity,
		"status":       finding.Status,
		"subject_kind": finding.SubjectKind,
		"subject_id":   finding.SubjectID,
		"summary":      finding.Summary,
	}
}

func maintenanceDBPayload(status maintenance.DBStatus) map[string]string {
	payload := map[string]string{
		"status":            status.Status,
		"migration_status":  status.MigrationStatus,
		"current_version":   fmt.Sprintf("%d", status.CurrentVersion),
		"latest_version":    fmt.Sprintf("%d", status.LatestVersion),
		"pending":           fmt.Sprintf("%d", status.Pending),
		"retention_dry_run": "loom maintenance retention dry-run --json",
	}
	if len(status.Retention.Plans) > 0 {
		payload["retention_plan_rows"] = fmt.Sprintf("%d", len(status.Retention.Plans))
	}
	return payload
}

func maintenanceBackupStatusPayload(status maintenance.BackupStatus) map[string]string {
	payload := map[string]string{
		"status":   status.Status,
		"findings": fmt.Sprintf("open=%d critical=%d error=%d warning=%d", status.OpenFindings.Open, status.OpenFindings.Critical, status.OpenFindings.Error, status.OpenFindings.Warning),
	}
	if status.LatestSuccessful != nil {
		payload["latest_successful"] = status.LatestSuccessful.Operation.MaintenanceOperationID
	}
	if status.LatestFailed != nil {
		payload["latest_failed"] = status.LatestFailed.Operation.MaintenanceOperationID
	}
	return payload
}

func maintenanceBackupOperationPayload(backup maintenance.BackupOperation) map[string]string {
	return map[string]string{
		"operation_id":  backup.Operation.MaintenanceOperationID,
		"operation_key": backup.Operation.OperationKey,
		"kind":          backup.Operation.OperationKind,
		"status":        backup.Operation.Status,
		"started_at":    timeOrDash(backup.Operation.StartedAt),
		"finished_at":   timePtrOrDash(backup.Operation.FinishedAt),
		"artifacts":     fmt.Sprintf("%d", len(backup.Artifacts)),
	}
}

func maintenanceObjectStorePayload(status maintenance.ObjectStoreStatus) map[string]string {
	return map[string]string{
		"status":   status.Status,
		"total":    fmt.Sprintf("%d", status.BlobCounts.Total),
		"verified": fmt.Sprintf("%d", status.BlobCounts.Verified),
		"pending":  fmt.Sprintf("%d", status.BlobCounts.Pending),
		"missing":  fmt.Sprintf("%d", status.BlobCounts.Missing),
		"corrupt":  fmt.Sprintf("%d", status.BlobCounts.Corrupt),
		"findings": fmt.Sprintf("open=%d critical=%d error=%d warning=%d", status.Findings.Open, status.Findings.Critical, status.Findings.Error, status.Findings.Warning),
	}
}

func updateStatusPayload(status update.UpdateStatus) map[string]string {
	payload := map[string]string{
		"state_dir":             status.StateDir,
		"active_manifest_path":  status.ActiveManifestPath,
		"active_exists":         fmt.Sprintf("%t", status.ActiveExists),
		"history_count":         fmt.Sprintf("%d", len(status.History)),
		"diagnostic_count":      fmt.Sprintf("%d", len(status.Diagnostics)),
		"checked_at":            timeOrDash(status.CheckedAt),
		"maintenance_status":    "-",
		"rollback_class":        "-",
		"restore_required":      "false",
		"service_only_possible": "false",
	}
	if status.Active != nil {
		payload["update_id"] = status.Active.UpdateID
		payload["status"] = status.Active.Status
		payload["active_release"] = status.Active.Active.Path
		payload["target_release"] = status.Active.Target.Path
		payload["backup_ref"] = firstNonEmpty(status.Active.BackupRef, status.Active.BackupPath, "-")
		payload["rollback_class"] = status.Active.Rollback.Class
		payload["restore_required"] = fmt.Sprintf("%t", status.Active.Rollback.RestoreRequired)
		payload["service_only_possible"] = fmt.Sprintf("%t", status.Active.Rollback.ServiceOnlyPossible)
		if status.Active.Maintenance != nil {
			payload["maintenance_status"] = status.Active.Maintenance.Status
		}
	}
	return payload
}

func indexStatusRetryable(status search.IndexStatus) bool {
	return status.IndexStatusID != "" && (status.Status == "failed" || status.ManualAction)
}

func indexObjectRef(status search.IndexStatus) string {
	return firstNonEmpty(status.ObjectID, objectLikeSource(status))
}

func objectLikeSource(status search.IndexStatus) string {
	switch strings.ToLower(status.SourceKind) {
	case "object", "object_version":
		return status.SourceID
	default:
		return ""
	}
}

func hasDBStatus(status maintenance.DBStatus) bool {
	return status.Status != "" || status.MigrationStatus != "" || status.CurrentVersion != 0 || status.LatestVersion != 0
}

func hasBackupStatus(status maintenance.BackupStatus) bool {
	return status.Status != "" || status.LatestSuccessful != nil || status.LatestFailed != nil || status.OpenFindings.Open > 0
}

func hasObjectStoreStatus(status maintenance.ObjectStoreStatus) bool {
	return status.Status != "" || status.BlobCounts.Total > 0 || status.BlobCounts.Missing > 0 || status.BlobCounts.Corrupt > 0 || status.Findings.Open > 0
}

func hasUpdateStatus(status update.UpdateStatus) bool {
	return status.StateDir != "" || status.ActiveManifestPath != "" || status.Active != nil || len(status.History) > 0 || len(status.Diagnostics) > 0
}

func actionPtr(action PortalAction) *PortalAction {
	return &action
}

func safeActionID(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	var builder strings.Builder
	lastUnderscore := false
	for _, r := range value {
		valid := unicode.IsLetter(r) || unicode.IsDigit(r)
		if valid {
			builder.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			builder.WriteByte('_')
			lastUnderscore = true
		}
	}
	result := strings.Trim(builder.String(), "_")
	if result == "" {
		return "unknown"
	}
	return result
}

func titleFromToken(value string) string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == '_' || r == '-' || r == '.' || r == '/'
	})
	for idx, part := range parts {
		if part == "" {
			continue
		}
		parts[idx] = strings.ToUpper(part[:1]) + strings.ToLower(part[1:])
	}
	return strings.Join(parts, " ")
}
