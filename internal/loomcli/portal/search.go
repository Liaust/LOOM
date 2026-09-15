package portal

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/loomcli/actions"
	"loom.local/loom/internal/loomcli/ui"
)

type PortalSearchResult struct {
	Action      PortalAction
	Category    string
	Title       string
	Description string
	Disabled    bool
	Reason      string
	Score       int
}

type PortalSearchMode string

const (
	PortalSearchModeNavigation PortalSearchMode = "navigation"
	PortalSearchModeScoped     PortalSearchMode = "scoped"
	PortalSearchModeCommand    PortalSearchMode = "command"
)

type PortalSearchInput struct {
	Raw           string
	Mode          PortalSearchMode
	Scope         string
	ScopeRaw      string
	ScopeFilter   string
	ScopeComplete bool
	Query         string
}

func SearchActions(registry actions.Registry, query string, limit int) []actions.SearchResult {
	return registry.Search(query, limit)
}

func SearchPortalActions(registry actions.Registry, query string, limit int, includeRaw bool) []PortalSearchResult {
	return rankPortalSearchEntries(query, limit, registrySearchEntries(registry, includeRaw))
}

func SearchPortalActionsForState(registry actions.Registry, state ScreenState, query string, limit int, includeRaw bool) []PortalSearchResult {
	entries := registrySearchEntries(registry, includeRaw)
	entries = append(entries, screenSearchEntries(state)...)
	return rankPortalSearchEntries(query, limit, entries)
}

func SearchPortal(registry actions.Registry, state ScreenState, query string, limit int) []PortalSearchResult {
	input := ParsePortalSearchInput(query)
	switch input.Mode {
	case PortalSearchModeCommand:
		return nil
	case PortalSearchModeScoped:
		return SearchPortalScoped(registry, state, input, limit)
	default:
		return SearchPortalNavigation(registry, input.Query, limit)
	}
}

func SearchPortalNavigation(registry actions.Registry, query string, limit int) []PortalSearchResult {
	entries := navigationSearchEntries(registry)
	return rankPortalSearchEntries(query, limit, entries)
}

func SearchPortalScoped(registry actions.Registry, state ScreenState, input PortalSearchInput, limit int) []PortalSearchResult {
	if !input.ScopeComplete {
		return rankPortalSearchEntries(input.ScopeRaw, limit, searchScopeEntries(input.ScopeRaw))
	}
	entries := scopedSearchEntries(registry, state, input.Scope, input.ScopeFilter, input.Query)
	return rankPortalSearchEntries(input.Query, limit, entries)
}

func ParsePortalSearchInput(raw string) PortalSearchInput {
	trimmed := strings.TrimSpace(raw)
	input := PortalSearchInput{
		Raw:   raw,
		Mode:  PortalSearchModeNavigation,
		Query: trimmed,
	}
	if isCommandModeInput(raw) {
		input.Mode = PortalSearchModeCommand
		input.Query = trimmed
		return input
	}
	leftTrimmed := strings.TrimLeft(raw, " \t")
	if !strings.HasPrefix(leftTrimmed, "#") {
		return input
	}
	body := strings.TrimLeft(strings.TrimPrefix(leftTrimmed, "#"), " \t")
	bodyTrimmed := strings.TrimSpace(body)
	input.Mode = PortalSearchModeScoped
	if bodyTrimmed == "" {
		return input
	}
	scope, rest, ok := strings.Cut(body, " ")
	input.ScopeRaw = strings.TrimSpace(scope)
	if ok {
		input.ScopeComplete = true
		input.Scope, input.ScopeFilter = normalizeSearchScopeWithFilter(scope)
		input.Query = strings.TrimSpace(rest)
		return input
	}
	input.Scope, input.ScopeFilter = normalizeSearchScopeWithFilter(scope)
	return input
}

type portalSearchScope struct {
	ID          string
	Title       string
	Description string
	Keywords    []string
	Screen      string
}

func portalSearchScopes() []portalSearchScope {
	return []portalSearchScope{
		{ID: "doctor", Title: "#doctor", Description: "Active LOOM issues, grouped findings, safe next actions, and repair routing.", Keywords: []string{"doctor", "health", "repair", "attention", "problems", "fix", "issues"}, Screen: ScreenDoctor},
		{ID: "box", Title: "#box", Description: "LOOM Box folders, profile-local workspace intake, watch policy, and current actions.", Keywords: []string{"box", "loom box", "documents", "notes", "projects", "folder"}, Screen: ScreenBox},
		{ID: "notes", Title: "#notes", Description: "LOOM Notes source roots, file types, projection, and search context.", Keywords: []string{"notes", "loom notes", "knowledge notes", "markdown", "pdf", "images", "projection", "knowledge"}, Screen: ScreenNotes},
		{ID: "projects", Title: "#projects", Description: "Projects, facets, exposed capabilities, automations, and data policy.", Keywords: []string{"project", "projects", "facet", "contract", "registration", "project status", "project capability"}, Screen: ScreenProjects},
		{ID: "capabilities", Title: "#capabilities", Description: "Providers and callable capability addresses.", Keywords: []string{"cap", "caps", "capability", "capabilities", "provider", "providers", "tools", "main@system"}, Screen: ScreenCapabilities},
		{ID: "storage", Title: "#storage", Description: "LOOM Main storage tree, node backups, main documents, and retrieval actions.", Keywords: []string{"storage", "main storage", "loom main", "documents", "archive", "backup", "fetch", "safe delete"}, Screen: ScreenStorage},
		{ID: "database", Title: "#database", Description: "Object store diagnostics, text-index state, sync records, and repair context.", Keywords: []string{"object", "objects", "database", "db", "diagnostics", "sync", "index", "indexes", "search"}, Screen: ScreenDatabase},
		{ID: "workers", Title: "#workers", Description: "Workers, maintenance, backups, cloud upload workers, and worker actions.", Keywords: []string{"worker", "workers", "background", "selfcheck", "indexer", "cloud", "snapshot", "hetzner"}, Screen: ScreenBackground},
		{ID: "automation", Title: "#automation", Description: "Schedules, automations, direct events, integrations, and invocations.", Keywords: []string{"schedule", "schedules", "automation", "automations", "direct event", "events", "integration", "invocation"}, Screen: ScreenAutomations},
		{ID: "jobs", Title: "#jobs", Description: "Jobs, queued work, failed work, and job actions.", Keywords: []string{"job", "jobs", "queue", "failed", "runner"}, Screen: ScreenJobs},
		{ID: "nodes", Title: "#nodes", Description: "Nodes, watched roots, sync, and workspace backups.", Keywords: []string{"node", "nodes", "watched root", "workspace", "backup"}, Screen: ScreenNodes},
	}
}

func searchScopeEntries(query string) []portalSearchEntry {
	scopes := portalSearchScopes()
	entries := make([]portalSearchEntry, 0, len(scopes))
	for _, scope := range scopes {
		if !searchScopeMatches(query, scope) {
			continue
		}
		description := scope.Description
		if filterLabel := searchScopeFilterHint(query, scope.ID); filterLabel != "" {
			description = fmt.Sprintf("%s Filter: %s.", description, filterLabel)
		}
		action := PortalAction{
			ID:           "search.scope." + scope.ID,
			Label:        scope.Title,
			Description:  description,
			Domain:       "portal_search",
			SourceScreen: ScreenHome,
			TargetKind:   "search_scope",
			TargetRef:    scope.ID,
			TargetLabel:  scope.Title,
			Risk:         ActionRiskInspect,
			State:        ActionAvailable,
			InputValues:  map[string]string{},
			Executor:     PortalActionExecutor{Kind: PortalExecutorNavigate, Target: scope.Screen},
		}
		action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
		keywords := append([]string{scope.ID, scope.Title, description}, scope.Keywords...)
		entries = append(entries, portalSearchEntry{
			Action:      action,
			Category:    "scope",
			Title:       scope.Title,
			Description: description,
			SearchKey:   "scope:" + scope.ID,
			Candidate: ui.Candidate{
				ID:          action.ID,
				Title:       scope.Title,
				Description: description,
				Domain:      "scope",
				Keywords:    keywords,
				Attention:   100,
			},
		})
	}
	return entries
}

func searchScopeMatches(query string, scope portalSearchScope) bool {
	query = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(query, "#")))
	if query == "" {
		return true
	}
	fields := []string{scope.ID, strings.TrimPrefix(scope.Title, "#")}
	fields = append(fields, scope.Keywords...)
	if canonical, _, ok := searchScopeAliasMatch(query); ok && canonical == scope.ID {
		return true
	}
	for _, field := range fields {
		field = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(field, "#")))
		if field == "" {
			continue
		}
		if strings.HasPrefix(field, query) {
			return true
		}
	}
	return false
}

type portalSearchScopeAlias struct {
	Alias     string
	Canonical string
	Filter    string
	Label     string
}

func portalSearchScopeAliases() []portalSearchScopeAlias {
	return []portalSearchScopeAlias{
		{Alias: "object", Canonical: "database", Filter: "objects", Label: "objects"},
		{Alias: "objects", Canonical: "database", Filter: "objects", Label: "objects"},
		{Alias: "db", Canonical: "database", Label: "database"},
		{Alias: "index", Canonical: "database", Filter: "indexes", Label: "indexes"},
		{Alias: "indexes", Canonical: "database", Filter: "indexes", Label: "indexes"},
		{Alias: "indices", Canonical: "database", Filter: "indexes", Label: "indexes"},
		{Alias: "archive", Canonical: "storage", Filter: "archives", Label: "archives"},
		{Alias: "archives", Canonical: "storage", Filter: "archives", Label: "archives"},
		{Alias: "main-archive", Canonical: "storage", Filter: "archives", Label: "archives"},
		{Alias: "main_archive", Canonical: "storage", Filter: "archives", Label: "archives"},
		{Alias: "transfer", Canonical: "storage", Filter: "transfers", Label: "transfers"},
		{Alias: "transfers", Canonical: "storage", Filter: "transfers", Label: "transfers"},
		{Alias: "cloud", Canonical: "workers", Filter: "cloud", Label: "cloud workers"},
		{Alias: "schedule", Canonical: "automation", Filter: "schedules", Label: "schedules"},
		{Alias: "schedules", Canonical: "automation", Filter: "schedules", Label: "schedules"},
		{Alias: "automations", Canonical: "automation", Label: "automation"},
		{Alias: "note", Canonical: "notes", Label: "notes"},
		{Alias: "notes", Canonical: "notes", Label: "notes"},
		{Alias: "loom-notes", Canonical: "notes", Label: "notes"},
		{Alias: "loom_notes", Canonical: "notes", Label: "notes"},
		{Alias: "health", Canonical: "doctor", Label: "health"},
		{Alias: "repair", Canonical: "doctor", Label: "repair"},
		{Alias: "attention", Canonical: "doctor", Label: "attention"},
		{Alias: "problem", Canonical: "doctor", Label: "problems"},
		{Alias: "problems", Canonical: "doctor", Label: "problems"},
		{Alias: "fix", Canonical: "doctor", Label: "fix"},
	}
}

func searchScopeAliasMatch(query string) (string, string, bool) {
	query = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(query, "#")))
	if query == "" {
		return "", "", false
	}
	for _, alias := range portalSearchScopeAliases() {
		if strings.HasPrefix(alias.Alias, query) || strings.HasPrefix(query, alias.Alias) {
			return alias.Canonical, alias.Filter, true
		}
	}
	return "", "", false
}

func searchScopeFilterHint(query string, canonical string) string {
	query = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(query, "#")))
	for _, alias := range portalSearchScopeAliases() {
		if alias.Canonical == canonical && alias.Filter != "" && (strings.HasPrefix(alias.Alias, query) || strings.HasPrefix(query, alias.Alias)) {
			return alias.Label
		}
	}
	return ""
}

func normalizeSearchScope(scope string) string {
	canonical, _ := normalizeSearchScopeWithFilter(scope)
	return canonical
}

func normalizeSearchScopeWithFilter(scope string) (string, string) {
	normalized := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(scope, "#")))
	if canonical, filter, ok := searchScopeAliasMatch(normalized); ok {
		return canonical, filter
	}
	switch normalized {
	case "doctor", "health", "repair", "attention", "problem", "problems", "fix":
		return "doctor", ""
	case "box", "loom-box", "loombox", "documents", "launchpad":
		return "box", ""
	case "project", "projects", "facet", "facets":
		return "projects", ""
	case "cap", "caps", "capability", "capabilities", "provider", "providers":
		return "capabilities", ""
	case "worker", "workers", "background":
		return "workers", ""
	case "database", "knowledge":
		return "database", ""
	case "note", "notes", "loom-notes", "loom_notes", "knowledge-notes", "knowledge_notes":
		return "notes", ""
	case "storage", "main-storage", "main_storage", "loom-main", "loom_main", "mainstorage":
		return "storage", ""
	case "automation":
		return "automation", ""
	case "job", "jobs":
		return "jobs", ""
	case "node", "nodes":
		return "nodes", ""
	default:
		return normalized, ""
	}
}

func searchScopeScreen(scope string) string {
	switch normalizeSearchScope(scope) {
	case "doctor":
		return ScreenDoctor
	case "box":
		return ScreenBox
	case "projects":
		return ScreenProjects
	case "capabilities":
		return ScreenCapabilities
	case "workers":
		return ScreenBackground
	case "database":
		return ScreenDatabase
	case "notes":
		return ScreenNotes
	case "storage":
		return ScreenStorage
	case "automation":
		return ScreenAutomations
	case "jobs":
		return ScreenJobs
	case "nodes":
		return ScreenNodes
	default:
		return ""
	}
}

type portalSearchEntry struct {
	Action      PortalAction
	Category    string
	Title       string
	Description string
	Disabled    bool
	Reason      string
	SearchKey   string
	Candidate   ui.Candidate
}

func registrySearchEntries(registry actions.Registry, includeRaw bool) []portalSearchEntry {
	entries := []portalSearchEntry{}
	for _, action := range registry.All() {
		portalAction := PortalActionFromRegistry(action)
		if !includeRaw && shouldHideRawSearchAction(portalAction) {
			continue
		}
		candidate := action.Candidate()
		normalizeNavigationSearchEntry(&portalAction, &candidate)
		entries = append(entries, portalSearchEntry{
			Action:      portalAction,
			Category:    portalSearchCategory(portalAction),
			Title:       portalAction.Label,
			Description: portalAction.Description,
			Disabled:    portalAction.Disabled(),
			Reason:      portalAction.DisabledReason,
			Candidate:   candidate,
		})
	}
	return entries
}

func navigationSearchEntries(registry actions.Registry) []portalSearchEntry {
	entries := []portalSearchEntry{}
	seen := map[string]bool{}
	add := func(entry portalSearchEntry) {
		if entry.Action.ID == "" || seen[entry.Action.ID] {
			return
		}
		seen[entry.Action.ID] = true
		entries = append(entries, entry)
	}
	for _, entry := range registrySearchEntries(registry, false) {
		if entry.Action.Executor.Kind != PortalExecutorNavigate && !strings.HasSuffix(entry.Action.ID, ".open") {
			continue
		}
		entry.Category = "screen"
		entry.Candidate.Domain = "screen"
		entry.Candidate.Attention += 100
		add(entry)
	}
	for _, screen := range Screens() {
		action := PortalAction{
			ID:            screen.ID + ".open",
			Label:         screen.Title,
			Description:   screenDescription(screen.ID),
			Domain:        "portal",
			SourceScreen:  ScreenHome,
			TargetKind:    "screen",
			TargetRef:     screen.ID,
			TargetLabel:   screen.Title,
			Risk:          ActionRiskInspect,
			State:         ActionAvailable,
			InputValues:   map[string]string{},
			Executor:      PortalActionExecutor{Kind: PortalExecutorNavigate, Target: screen.ID},
			RefreshScreen: screen.ID,
		}
		action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
		attention := 100
		if screen.ID == ScreenDoctor {
			attention = 140
		} else if screen.ID == ScreenNotes {
			attention = 130
		}
		keywords := []string{screen.ID, screen.Title, screenDescription(screen.ID)}
		if screen.ID == ScreenDoctor {
			keywords = append(keywords, "doctor", "health", "repair", "attention", "problems", "fix", "issue", "issues")
		}
		if screen.ID == ScreenDatabase {
			keywords = append(keywords, "object diagnostics", "object store diagnostics", "index failure", "index failures", "failed indexes")
		}
		add(portalSearchEntry{
			Action:      action,
			Category:    "screen",
			Title:       screen.Title,
			Description: screenDescription(screen.ID),
			Candidate: ui.Candidate{
				ID:          action.ID,
				Title:       screen.Title,
				Description: screenDescription(screen.ID),
				Domain:      "screen",
				Keywords:    keywords,
				Attention:   attention,
			},
		})
	}
	add(supportBundleNavigationSearchEntry())
	return entries
}

func supportBundleNavigationSearchEntry() portalSearchEntry {
	action := NewSupportBundleCommandHintAction(ScreenDoctor)
	description := "Create a redacted diagnostic bundle for support handoff from the CLI."
	keywords := []string{
		"support",
		"bundle",
		"support bundle",
		"diagnostic bundle",
		"diagnostics",
		"handoff",
		"troubleshooting",
		"loom support bundle create",
	}
	return portalSearchEntry{
		Action:      action,
		Category:    "command",
		Title:       action.Label,
		Description: description,
		Candidate: ui.Candidate{
			ID:          action.ID,
			Title:       action.Label,
			Description: description,
			Domain:      "command",
			Keywords:    keywords,
			Attention:   125,
		},
	}
}

func normalizeNavigationSearchEntry(action *PortalAction, candidate *ui.Candidate) {
	if action == nil || candidate == nil || action.Executor.Kind != PortalExecutorNavigate {
		return
	}
	screen := NormalizeScreen(action.Executor.Target)
	switch screen {
	case ScreenDoctor:
		title := screenTitle(ScreenDoctor)
		description := screenDescription(ScreenDoctor)
		action.Label = title
		action.Description = description
		action.TargetLabel = title
		candidate.Title = title
		candidate.Description = description
		candidate.Keywords = append(candidate.Keywords, "doctor", "health", "repair", "attention", "problems", "fix", "issue", "issues")
		candidate.Attention += 40
	case ScreenDatabase:
		title := screenTitle(ScreenDatabase)
		description := screenDescription(ScreenDatabase)
		action.Label = title
		action.Description = description
		action.TargetLabel = title
		candidate.Title = title
		candidate.Description = description
		candidate.Keywords = append(candidate.Keywords, "object store diagnostics", "diagnostics", "index diagnostics", "index failure", "index failures", "failed indexes")
	case ScreenNotes:
		title := screenTitle(ScreenNotes)
		description := screenDescription(ScreenNotes)
		action.Label = title
		action.Description = description
		action.TargetLabel = title
		candidate.Title = title
		candidate.Description = description
		candidate.Keywords = append(candidate.Keywords, "notes", "loom notes", "knowledge notes")
		candidate.Attention += 30
	}
}

func scopedSearchEntries(registry actions.Registry, state ScreenState, scope string, filter string, query string) []portalSearchEntry {
	scope = normalizeSearchScope(scope)
	switch scope {
	case "capabilities":
		return capabilityAddressSearchEntries(state.Data.Capabilities, query)
	case "storage":
		return scopedStorageSearchEntries(state, filter, query)
	case "database":
		return scopedDatabaseSearchEntries(state, filter, query)
	case "doctor", "box", "projects", "notes", "workers", "automation", "jobs", "nodes":
		return scopedScreenRecordEntries(state, scope, filter)
	default:
		return scopedSearchHelpEntries(registry, scope)
	}
}

func scopedStorageSearchEntries(state ScreenState, filter string, query string) []portalSearchEntry {
	items := storageSearchSelectableItems(state, filter, query)
	entries := []portalSearchEntry{}
	seen := map[string]bool{}
	for _, item := range items {
		if item.PrimaryAction == nil {
			continue
		}
		action := *item.PrimaryAction
		title := firstNonEmpty(item.RecordLabel, item.Label, action.TargetLabel)
		description := firstNonEmpty(item.Description, action.Description)
		entry := portalSearchEntryForSelectable(item, action, title, description, 95)
		key := firstNonEmpty(entry.SearchKey, item.RecordRef, action.ID)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		entries = append(entries, entry)
	}
	return entries
}

func scopedDatabaseSearchEntries(state ScreenState, filter string, query string) []portalSearchEntry {
	items := databaseSearchSelectableItems(state, filter, query)
	entries := []portalSearchEntry{}
	seen := map[string]bool{}
	for _, item := range items {
		if item.PrimaryAction == nil {
			continue
		}
		action := *item.PrimaryAction
		title := firstNonEmpty(item.RecordLabel, item.Label, action.TargetLabel)
		description := firstNonEmpty(item.Description, action.Description)
		entry := portalSearchEntryForSelectable(item, action, title, description, 95)
		key := firstNonEmpty(entry.SearchKey, item.RecordRef, action.ID)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		entries = append(entries, entry)
	}
	return entries
}

func scopedScreenRecordEntries(state ScreenState, scope string, filter string) []portalSearchEntry {
	items := ScreenRecordItems(state)
	entries := []portalSearchEntry{}
	seen := map[string]bool{}
	for _, item := range items {
		if !selectableMatchesSearchScope(item, scope) || !selectableMatchesSearchFilter(item, filter) || item.PrimaryAction == nil {
			continue
		}
		action := *item.PrimaryAction
		title := selectablePrimarySearchTitle(item, action)
		description := firstNonEmpty(item.Description, action.Description)
		entry := portalSearchEntryForSelectable(item, action, title, description, 80)
		key := firstNonEmpty(entry.SearchKey, entry.Candidate.ID, action.ID)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		entries = append(entries, entry)
	}
	return entries
}

func scopedSearchHelpEntries(registry actions.Registry, scope string) []portalSearchEntry {
	if screen := searchScopeScreen(scope); screen != "" {
		return navigationSearchEntries(registry)
	}
	return []portalSearchEntry{}
}

func selectableMatchesSearchScope(item SelectableItem, scope string) bool {
	if item.Kind == SelectableKindAction && item.PrimaryAction != nil {
		action := *item.PrimaryAction
		switch scope {
		case "doctor":
			return action.TargetKind == "doctor_finding" || strings.EqualFold(action.Domain, "doctor")
		case "box":
			return action.TargetKind == "box" || strings.HasPrefix(action.TargetKind, "box_") || strings.EqualFold(action.Domain, "box")
		case "projects":
			return action.TargetKind == "project" || strings.HasPrefix(action.TargetKind, "project_") || strings.EqualFold(action.Domain, "projects")
		case "workers":
			return action.TargetKind == "worker" || strings.EqualFold(action.Domain, "workers") || strings.Contains(action.ID, "worker.")
		case "database":
			return action.TargetKind == "object" || strings.EqualFold(action.Domain, "database") || strings.EqualFold(action.Domain, "search")
		case "notes":
			return action.TargetKind == "notes_root" || action.TargetKind == "notes_search_result" || strings.EqualFold(action.Domain, "notes")
		case "storage":
			return strings.EqualFold(action.Domain, "storage") || strings.HasPrefix(action.TargetKind, "storage_") || strings.HasPrefix(action.ID, "storage.")
		case "automation":
			return strings.EqualFold(action.Domain, "automation") || strings.Contains(action.ID, "schedule") || strings.Contains(action.ID, "direct_event")
		case "jobs":
			return action.TargetKind == "job" || strings.EqualFold(action.Domain, "jobs") || strings.Contains(action.ID, "job.")
		case "nodes":
			return action.TargetKind == "node" || strings.EqualFold(action.Domain, "nodes")
		default:
			return false
		}
	}
	switch scope {
	case "doctor":
		return item.RecordKind == "doctor_finding" || strings.EqualFold(item.Screen, ScreenDoctor)
	case "box":
		return item.RecordKind == "box" || strings.HasPrefix(item.RecordKind, "box_")
	case "projects":
		return item.RecordKind == "project" || strings.HasPrefix(item.RecordKind, "project_")
	case "notes":
		return item.RecordKind == "notes_root" || item.RecordKind == "notes_search_result"
	case "workers":
		return item.RecordKind == "worker" || item.RecordKind == "maintenance_finding" || strings.HasPrefix(item.RecordKind, "maintenance_")
	case "database":
		return item.RecordKind == "object" || item.RecordKind == "search_result" || strings.Contains(item.RecordKind, "sync") || strings.Contains(item.RecordKind, "backup")
	case "storage":
		return item.RecordKind == "storage_entry" || item.RecordKind == "storage_node" || item.RecordKind == "storage_archive" || item.RecordKind == "storage_transfer" || strings.HasPrefix(item.RecordKind, "storage_")
	case "automation":
		return item.RecordKind == "schedule" || item.RecordKind == "automation" || item.RecordKind == "integration" || strings.HasPrefix(item.RecordKind, "direct_event") || strings.Contains(item.RecordKind, "invocation")
	case "jobs":
		return item.RecordKind == "job" || item.RecordKind == "queued_job" || item.RecordKind == "failed_job" || item.RecordKind == "runner"
	case "nodes":
		return item.RecordKind == "node" ||
			item.RecordKind == "node_summary" ||
			strings.HasPrefix(item.RecordKind, "watched_root") ||
			strings.Contains(item.RecordKind, "sync") ||
			strings.Contains(item.RecordKind, "backup") ||
			strings.Contains(item.RecordKind, "deletion")
	default:
		return false
	}
}

func selectableMatchesSearchFilter(item SelectableItem, filter string) bool {
	filter = strings.ToLower(strings.TrimSpace(filter))
	if filter == "" {
		return true
	}
	action := PortalAction{}
	if item.PrimaryAction != nil {
		action = *item.PrimaryAction
	}
	fields := strings.ToLower(strings.Join([]string{
		item.Kind,
		item.Label,
		item.Description,
		item.RecordKind,
		item.RecordRef,
		item.RecordLabel,
		action.ID,
		action.Label,
		action.Description,
		action.Domain,
		action.TargetKind,
		action.TargetRef,
		action.TargetLabel,
	}, " "))
	switch filter {
	case "objects":
		return item.RecordKind == "object" || item.RecordKind == "search_result"
	case "indexes":
		return strings.Contains(item.RecordKind, "index") || strings.EqualFold(action.Domain, "search") || strings.Contains(action.ID, "index")
	case "archives":
		return item.RecordKind == "storage_archive" || strings.Contains(fields, "archive")
	case "transfers":
		return item.RecordKind == "storage_transfer" || strings.Contains(fields, "transfer")
	case "cloud":
		return strings.Contains(fields, "cloud") || strings.Contains(fields, "snapshot") || strings.Contains(fields, "hetzner")
	case "schedules":
		return item.RecordKind == "schedule" || item.RecordKind == "schedule_fire" || strings.Contains(fields, "schedule")
	default:
		return strings.Contains(fields, filter)
	}
}

func screenSearchEntries(state ScreenState) []portalSearchEntry {
	items := ScreenSelectableItems(state)
	entries := []portalSearchEntry{}
	seen := map[string]bool{}
	add := func(entry portalSearchEntry) {
		if entry.Action.ID == "" || seen[entry.Action.ID] {
			return
		}
		seen[entry.Action.ID] = true
		entries = append(entries, entry)
	}
	for _, item := range items {
		if item.PrimaryAction != nil {
			action := *item.PrimaryAction
			add(portalSearchEntryForSelectable(item, action, selectablePrimarySearchTitle(item, action), selectablePrimarySearchDescription(item, action), 90))
		}
		for _, action := range item.RelatedActions {
			title := action.Label
			if target := firstNonEmpty(action.TargetLabel, item.Label, action.TargetRef); target != "" && target != action.Label {
				title = fmt.Sprintf("%s: %s", action.Label, target)
			}
			add(portalSearchEntryForSelectable(item, action, title, action.Description, 120))
		}
	}
	return entries
}

func portalSearchEntryForSelectable(item SelectableItem, action PortalAction, title string, description string, attention int) portalSearchEntry {
	category := selectableSearchCategory(item, action)
	searchKey := selectableSearchKey(item, action)
	keywords := []string{
		item.RecordKind,
		item.RecordRef,
		item.RecordLabel,
		action.Label,
		action.Description,
		action.Domain,
		action.TargetKind,
		action.TargetRef,
		action.TargetLabel,
	}
	return portalSearchEntry{
		Action:      action,
		Category:    category,
		Title:       firstNonEmpty(title, action.Label),
		Description: firstNonEmpty(description, action.Description),
		Disabled:    action.Disabled(),
		Reason:      action.DisabledReason,
		SearchKey:   searchKey,
		Candidate: ui.Candidate{
			ID:          searchKey,
			Title:       firstNonEmpty(title, action.Label),
			Description: firstNonEmpty(description, action.Description),
			Domain:      category,
			Keywords:    keywords,
			Attention:   attention,
		},
	}
}

func selectableSearchKey(item SelectableItem, action PortalAction) string {
	recordRef := firstNonEmpty(item.RecordRef, item.RecordLabel, action.TargetRef, action.TargetLabel, action.ID)
	if recordRef == "" {
		return action.ID
	}
	return strings.Join([]string{NormalizeScreen(item.Screen), item.RecordKind, recordRef}, ":")
}

func rankPortalSearchEntries(query string, limit int, entries []portalSearchEntry) []PortalSearchResult {
	if limit <= 0 {
		limit = 8
	}
	candidates := make([]ui.Candidate, 0, len(entries))
	entryByID := map[string]portalSearchEntry{}
	for _, entry := range entries {
		if entry.Action.ID == "" {
			continue
		}
		candidate := entry.Candidate
		if entry.SearchKey != "" {
			candidate.ID = entry.SearchKey
		}
		if candidate.ID == "" {
			candidate.ID = entry.Action.ID
		}
		if _, exists := entryByID[candidate.ID]; exists {
			continue
		}
		candidates = append(candidates, candidate)
		entryByID[candidate.ID] = entry
	}
	ranked := ui.RankFuzzy(query, candidates, limit)
	results := make([]PortalSearchResult, 0, len(ranked))
	seen := map[string]bool{}
	for _, result := range ranked {
		if seen[result.Candidate.ID] {
			continue
		}
		entry, ok := entryByID[result.Candidate.ID]
		if !ok {
			continue
		}
		seen[result.Candidate.ID] = true
		results = append(results, PortalSearchResult{
			Action:      entry.Action,
			Category:    entry.Category,
			Title:       entry.Title,
			Description: entry.Description,
			Disabled:    entry.Disabled,
			Reason:      entry.Reason,
			Score:       result.Score,
		})
	}
	return results
}

func shouldHideRawSearchAction(action PortalAction) bool {
	return strings.HasPrefix(action.ID, "raw.") || strings.EqualFold(action.Domain, "raw")
}

func portalSearchCategory(action PortalAction) string {
	if shouldHideRawSearchAction(action) {
		return "raw"
	}
	if action.Executor.Kind == PortalExecutorNavigate || strings.HasSuffix(action.ID, ".open") {
		return "screen"
	}
	if strings.Contains(action.ID, "worker.") || strings.EqualFold(action.Domain, "workers") {
		return "worker"
	}
	if strings.EqualFold(action.Domain, "projects") {
		return "project"
	}
	if strings.EqualFold(action.Domain, "box") {
		return "box"
	}
	if strings.EqualFold(action.Domain, "automation") {
		return "automation"
	}
	if strings.EqualFold(action.Domain, "capabilities") {
		return "capability"
	}
	if strings.EqualFold(action.Domain, "database") {
		return "database"
	}
	if strings.EqualFold(action.Domain, "notes") {
		return "notes"
	}
	if strings.EqualFold(action.Domain, "storage") {
		return "storage"
	}
	if strings.EqualFold(action.Domain, "search") || strings.HasPrefix(action.ID, "index.") {
		return "index"
	}
	if strings.EqualFold(action.Domain, "maintenance") {
		return "maintenance"
	}
	if strings.EqualFold(action.Domain, "support") {
		return "support"
	}
	return "action"
}

func selectablePrimarySearchTitle(item SelectableItem, action PortalAction) string {
	if item.Kind == SelectableKindAction {
		return action.Label
	}
	return firstNonEmpty(item.RecordLabel, item.Label, action.TargetLabel, action.Label)
}

func selectablePrimarySearchDescription(item SelectableItem, action PortalAction) string {
	if item.Kind == SelectableKindAction {
		return action.Description
	}
	return firstNonEmpty(action.Label, item.Description, action.Description)
}

func selectableSearchCategory(item SelectableItem, action PortalAction) string {
	switch item.RecordKind {
	case "box", "box_area", "box_policy", "box_watch_root", "box_watch_registration", "box_watch_status", "box_project_default", "box_inline_action":
		return "box"
	case "storage_entry", "storage_node":
		return "storage"
	case "storage_archive":
		return "archive"
	case "storage_transfer":
		return "transfer"
	case "project", "project_facet", "project_capability", "project_capability_call", "project_provider", "project_runtime_binding", "project_schedule", "project_direct_event_endpoint", "project_direct_event", "project_invocation", "project_job", "project_watched_root", "project_watched_root_finding", "project_sync_status", "project_backup_status", "project_workflow", "project_inline_action":
		return "project"
	case "notes_root", "notes_search_result":
		return "notes"
	case "schedule", "schedule_fire", "automation", "integration":
		return "automation"
	case "direct_event_endpoint", "direct_event", "direct_event_failure":
		return "direct event"
	case "invocation", "invocation_failure":
		return "invocation"
	case "provider":
		return "provider"
	case "capability":
		return "capability"
	case "provider_advertisement":
		return "provider"
	case "job", "queued_job", "failed_job":
		return "job"
	case "runner":
		return "job"
	case "node":
		return "node"
	case "watched_root", "watched_root_finding", "watched_root_backup_status", "watched_root_backup_batch":
		return "watched root"
	case "sync_batch", "sync_conflict", "sync_replica":
		return "sync"
	case "private_backup", "deletion_request":
		return "node"
	case "portal_action":
		return portalSearchCategory(action)
	default:
		if strings.HasPrefix(item.RecordKind, "project_section_") {
			return "project"
		}
		return portalSearchCategory(action)
	}
}

type notesSearchLoadedMsg struct {
	Query     string
	Input     knowledge.NotesSearchInput
	ResultSet knowledge.NotesSearchResultSet
	Err       error
	LoadedAt  time.Time
}

func notesSearchCmd(client Client, correlationID, raw string) tea.Cmd {
	input := notesSearchInputFromQuery(raw)
	return func() tea.Msg {
		if client == nil {
			return notesSearchLoadedMsg{Query: strings.TrimSpace(raw), Input: input, Err: ErrMissingClient, LoadedAt: time.Now().UTC()}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		envelope, err := client.SearchKnowledgeNotes(ctx, correlationID, input)
		if err != nil {
			return notesSearchLoadedMsg{Query: strings.TrimSpace(raw), Input: input, Err: err, LoadedAt: time.Now().UTC()}
		}
		return notesSearchLoadedMsg{Query: strings.TrimSpace(raw), Input: input, ResultSet: envelope.Data, LoadedAt: time.Now().UTC()}
	}
}

func (m Model) handleNotesSearchLoaded(msg notesSearchLoadedMsg) (tea.Model, tea.Cmd) {
	state := m.currentScreenState()
	data := NotesSearchData{
		Query:     msg.Query,
		Input:     msg.Input,
		ResultSet: msg.ResultSet,
		Status:    ScreenLoadLoaded,
		LoadedAt:  msg.LoadedAt,
	}
	if data.LoadedAt.IsZero() {
		data.LoadedAt = time.Now().UTC()
	}
	if msg.Err != nil {
		data.Status = ScreenLoadFailed
		data.Error = msg.Err.Error()
	}
	state.Data.Notes.Search = data
	m.setScreenState(ScreenNotes, state)
	return m, nil
}

func notesSearchInputFromQuery(raw string) knowledge.NotesSearchInput {
	return knowledge.ParseNotesSearchInput(raw)
}

func notesSearchInputEqual(left, right knowledge.NotesSearchInput) bool {
	if left.SourceLifecycle != right.SourceLifecycle || left.SourceCategory != right.SourceCategory || left.Limit != right.Limit ||
		strings.TrimSpace(left.Query) != strings.TrimSpace(right.Query) ||
		strings.TrimSpace(left.ProjectID) != strings.TrimSpace(right.ProjectID) ||
		strings.TrimSpace(left.ProjectRef) != strings.TrimSpace(right.ProjectRef) ||
		strings.TrimSpace(left.SourceNodeKey) != strings.TrimSpace(right.SourceNodeKey) ||
		strings.TrimSpace(left.NotesSourceRootID) != strings.TrimSpace(right.NotesSourceRootID) ||
		strings.TrimSpace(left.RootRef) != strings.TrimSpace(right.RootRef) ||
		strings.TrimSpace(left.FileClass) != strings.TrimSpace(right.FileClass) ||
		strings.TrimSpace(left.Path) != strings.TrimSpace(right.Path) ||
		strings.TrimSpace(left.After) != strings.TrimSpace(right.After) ||
		strings.TrimSpace(left.Before) != strings.TrimSpace(right.Before) ||
		strings.TrimSpace(left.Sort) != strings.TrimSpace(right.Sort) ||
		strings.TrimSpace(left.Mode) != strings.TrimSpace(right.Mode) {
		return false
	}
	if len(left.Tags) != len(right.Tags) || len(left.Phrases) != len(right.Phrases) {
		return false
	}
	for idx := range left.Tags {
		if strings.TrimSpace(left.Tags[idx]) != strings.TrimSpace(right.Tags[idx]) {
			return false
		}
	}
	for idx := range left.Phrases {
		if strings.TrimSpace(left.Phrases[idx]) != strings.TrimSpace(right.Phrases[idx]) {
			return false
		}
	}
	return true
}
