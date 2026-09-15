package portal

import (
	"fmt"
	"sort"
	"strings"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/loomcli/ui"
)

type CapabilityExplorerRowKind string

const (
	CapabilityExplorerRowScope         CapabilityExplorerRowKind = "scope"
	CapabilityExplorerRowProvider      CapabilityExplorerRowKind = "provider"
	CapabilityExplorerRowCapability    CapabilityExplorerRowKind = "capability"
	CapabilityExplorerRowAdvertisement CapabilityExplorerRowKind = "provider_advertisement"
)

type CapabilityExplorerRow struct {
	Kind            CapabilityExplorerRowKind
	Scope           string
	ProviderAddress string
	Address         string
	Label           string
	Description     string
	Status          string
	ProviderCount   int
	CapabilityCount int
	Provider        *capabilities.ProviderListItem
	Capability      *capabilities.CapabilityListItem
	Advertisement   *capabilities.ProviderAdvertisement
}

type capabilityTaskGroup struct {
	Label        string
	Domain       portalDomain
	Capabilities []capabilities.CapabilityListItem
}

func defaultCapabilityExplorerState() CapabilityExplorerState {
	return CapabilityExplorerState{Level: CapabilityExplorerNodes}
}

func normalizeCapabilityExplorerState(data CapabilitiesData) CapabilityExplorerState {
	explorer := data.Explorer
	if explorer.Level == "" {
		explorer.Level = CapabilityExplorerNodes
	}
	switch explorer.Level {
	case CapabilityExplorerProviders:
		if strings.TrimSpace(explorer.SelectedScope) == "" {
			explorer.Level = CapabilityExplorerNodes
			explorer.SelectedProvider = ""
			explorer.ExpandedRef = ""
		}
	case CapabilityExplorerCapabilities:
		if strings.TrimSpace(explorer.SelectedProvider) == "" {
			explorer.Level = CapabilityExplorerNodes
			explorer.SelectedScope = ""
			explorer.ExpandedRef = ""
		} else if strings.TrimSpace(explorer.SelectedScope) == "" {
			explorer.SelectedScope = scopeFromAddress(explorer.SelectedProvider)
		}
	case CapabilityExplorerNodes:
		explorer.SelectedScope = ""
		explorer.SelectedProvider = ""
		explorer.ExpandedRef = ""
	default:
		explorer.Level = CapabilityExplorerNodes
		explorer.SelectedScope = ""
		explorer.SelectedProvider = ""
		explorer.ExpandedRef = ""
	}
	return explorer
}

func capabilityExplorerRows(data CapabilitiesData) []CapabilityExplorerRow {
	explorer := normalizeCapabilityExplorerState(data)
	switch explorer.Level {
	case CapabilityExplorerProviders:
		return capabilityProviderRows(data, explorer.SelectedScope)
	case CapabilityExplorerCapabilities:
		return capabilityEndpointRows(data, explorer.SelectedProvider)
	default:
		return capabilityScopeRows(data)
	}
}

func capabilityScopeRows(data CapabilitiesData) []CapabilityExplorerRow {
	scopeSet := map[string]bool{}
	for _, provider := range data.Providers {
		scope := scopeFromAddress(provider.Provider.CompactAddress)
		if scope != "" {
			scopeSet[scope] = true
		}
	}
	for _, capability := range data.Capabilities {
		scope := scopeFromAddress(capability.CapabilityEndpoint.CompactAddress)
		if scope != "" {
			scopeSet[scope] = true
		}
	}
	scopes := make([]string, 0, len(scopeSet))
	for scope := range scopeSet {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)

	rows := make([]CapabilityExplorerRow, 0, len(scopes))
	for _, scope := range scopes {
		providers := providersForScope(data, scope)
		capabilities := capabilitiesForScope(data, scope)
		rows = append(rows, CapabilityExplorerRow{
			Kind:            CapabilityExplorerRowScope,
			Scope:           scope,
			Address:         scope + "@",
			Label:           nodeFriendlyName(scope),
			Description:     fmt.Sprintf("%d providers, %d capabilities", len(providers), len(capabilities)),
			ProviderCount:   len(providers),
			CapabilityCount: len(capabilities),
		})
	}
	return rows
}

func capabilityProviderRows(data CapabilitiesData, scope string) []CapabilityExplorerRow {
	providers := providersForScope(data, scope)
	rows := make([]CapabilityExplorerRow, 0, len(providers))
	for _, provider := range providers {
		provider := provider
		address := provider.Provider.CompactAddress
		caps := capabilitiesForProvider(data, address)
		rows = append(rows, CapabilityExplorerRow{
			Kind:            CapabilityExplorerRowProvider,
			Scope:           scopeFromAddress(address),
			ProviderAddress: address,
			Address:         address,
			Label:           firstNonEmpty(provider.Provider.DisplayName, address, provider.Provider.ProviderKey, provider.Provider.ProviderID),
			Description:     fmt.Sprintf("%s node  %s  %d capabilities", nodeFriendlyName(scopeFromAddress(address)), firstNonEmpty(provider.Provider.ProviderType, "provider"), len(caps)),
			Status:          firstNonEmpty(provider.Provider.Status, provider.HealthStatus, "-"),
			ProviderCount:   1,
			CapabilityCount: len(caps),
			Provider:        &provider,
		})
	}
	return rows
}

func capabilityEndpointRows(data CapabilitiesData, providerAddress string) []CapabilityExplorerRow {
	caps := capabilitiesForProvider(data, providerAddress)
	rows := make([]CapabilityExplorerRow, 0, len(caps))
	for _, capability := range caps {
		capability := capability
		address := capability.CapabilityEndpoint.CompactAddress
		rows = append(rows, CapabilityExplorerRow{
			Kind:            CapabilityExplorerRowCapability,
			Scope:           scopeFromAddress(address),
			ProviderAddress: providerAddressFromCapabilityAddress(address),
			Address:         address,
			Label:           firstNonEmpty(address, capability.DisplayName, capability.ClassName, capability.CapabilityEndpoint.EndpointName, capability.CapabilityEndpoint.CapabilityEndpointID),
			Description:     firstNonEmpty(capability.DisplayName, capability.Description, capability.ClassName, capability.CapabilityEndpoint.EndpointName),
			Status:          firstNonEmpty(capability.CapabilityEndpoint.Status, "-"),
			CapabilityCount: 1,
			Capability:      &capability,
		})
	}
	return rows
}

func capabilityAdvertisementRows(data CapabilitiesData) []CapabilityExplorerRow {
	rows := make([]CapabilityExplorerRow, 0, len(data.Advertisements))
	for _, ad := range data.Advertisements {
		ad := ad
		rows = append(rows, CapabilityExplorerRow{
			Kind:          CapabilityExplorerRowAdvertisement,
			Address:       firstNonEmpty(ad.ProviderAdvertisementID, "-"),
			Label:         firstNonEmpty(ad.ProviderAdvertisementID, "-"),
			Description:   firstNonEmpty(ad.OriginNodeID, "-"),
			Status:        firstNonEmpty(ad.Status, "-"),
			Advertisement: &ad,
		})
	}
	return rows
}

func capabilityAdvertisementAttentionRows(data CapabilitiesData) []CapabilityExplorerRow {
	all := capabilityAdvertisementRows(data)
	rows := make([]CapabilityExplorerRow, 0, len(all))
	for _, row := range all {
		if capabilityAdvertisementNeedsAttention(row.Status) {
			rows = append(rows, row)
		}
	}
	return rows
}

func capabilityAdvertisementNeedsAttention(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "pending", "received", "rejected", "failed", "error", "invalid", "stale":
		return true
	default:
		return false
	}
}

func providersForScope(data CapabilitiesData, scope string) []capabilities.ProviderListItem {
	providers := []capabilities.ProviderListItem{}
	for _, provider := range data.Providers {
		if scopeFromAddress(provider.Provider.CompactAddress) == scope {
			providers = append(providers, provider)
		}
	}
	sort.SliceStable(providers, func(i, j int) bool {
		return providers[i].Provider.CompactAddress < providers[j].Provider.CompactAddress
	})
	return providers
}

func capabilitiesForScope(data CapabilitiesData, scope string) []capabilities.CapabilityListItem {
	caps := []capabilities.CapabilityListItem{}
	for _, capability := range data.Capabilities {
		if scopeFromAddress(capability.CapabilityEndpoint.CompactAddress) == scope {
			caps = append(caps, capability)
		}
	}
	sort.SliceStable(caps, func(i, j int) bool {
		return caps[i].CapabilityEndpoint.CompactAddress < caps[j].CapabilityEndpoint.CompactAddress
	})
	return caps
}

func capabilitiesForProvider(data CapabilitiesData, providerAddress string) []capabilities.CapabilityListItem {
	caps := []capabilities.CapabilityListItem{}
	for _, capability := range data.Capabilities {
		if firstNonEmpty(capability.ProviderAddress, providerAddressFromCapabilityAddress(capability.CapabilityEndpoint.CompactAddress)) == providerAddress {
			caps = append(caps, capability)
		}
	}
	sort.SliceStable(caps, func(i, j int) bool {
		return caps[i].CapabilityEndpoint.CompactAddress < caps[j].CapabilityEndpoint.CompactAddress
	})
	return caps
}

func capabilityProviderByAddress(data CapabilitiesData, address string) (capabilities.ProviderListItem, bool) {
	for _, provider := range data.Providers {
		if provider.Provider.CompactAddress == address {
			return provider, true
		}
	}
	return capabilities.ProviderListItem{}, false
}

func scopeFromAddress(address string) string {
	scope, _, ok := strings.Cut(strings.TrimSpace(address), "@")
	if !ok {
		return ""
	}
	return scope
}

func providerAddressFromCapabilityAddress(address string) string {
	value := strings.TrimSpace(address)
	at := strings.Index(value, "@")
	if at < 0 {
		return ""
	}
	dot := strings.Index(value[at+1:], ".")
	if dot < 0 {
		return value
	}
	return value[:at+1+dot]
}

func capabilityProviderKeyFromAddress(address string) string {
	provider := providerAddressFromCapabilityAddress(address)
	_, key, ok := strings.Cut(provider, "@")
	if !ok {
		return ""
	}
	return key
}

func capabilityAddressSearchEntries(data CapabilitiesData, query string) []portalSearchEntry {
	query = strings.ToLower(strings.TrimSpace(query))
	entries := []portalSearchEntry{}
	seen := map[string]bool{}
	add := func(entry portalSearchEntry) {
		id := entry.Action.ID
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		entries = append(entries, entry)
	}

	for _, provider := range data.Providers {
		address := provider.Provider.CompactAddress
		if address == "" || !capabilityAddressMatches(query, address, provider.Provider.DisplayName, provider.Provider.ProviderKey, provider.Provider.ProviderType) {
			continue
		}
		action := NewProviderInspectAction(provider, ScreenCapabilities)
		add(portalSearchEntry{
			Action:      action,
			Category:    "provider",
			Title:       address,
			Description: firstNonEmpty(provider.Provider.DisplayName, provider.Provider.Description, "Provider"),
			Disabled:    action.Disabled(),
			Reason:      action.DisabledReason,
			SearchKey:   "provider:" + address,
			Candidate: ui.Candidate{
				ID:          "provider:" + address,
				Title:       address,
				Description: firstNonEmpty(provider.Provider.DisplayName, provider.Provider.Description),
				Domain:      "provider",
				Keywords:    []string{address, provider.Provider.DisplayName, provider.Provider.ProviderKey, provider.Provider.ProviderType},
				Attention:   60,
			},
		})
	}

	for _, capability := range data.Capabilities {
		address := capability.CapabilityEndpoint.CompactAddress
		if address == "" || !capabilityAddressMatches(query, address, capability.DisplayName, capability.ClassName, capability.ProviderAddress, capability.ProviderKey, capability.Description) {
			continue
		}
		action := NewCapabilityInspectAction(capability, ScreenCapabilities)
		add(portalSearchEntry{
			Action:      action,
			Category:    "capability",
			Title:       address,
			Description: firstNonEmpty(capability.DisplayName, capability.Description, capability.ClassName, "Capability"),
			Disabled:    action.Disabled(),
			Reason:      action.DisabledReason,
			SearchKey:   "capability:" + address,
			Candidate: ui.Candidate{
				ID:          "capability:" + address,
				Title:       address,
				Description: firstNonEmpty(capability.DisplayName, capability.Description, capability.ClassName),
				Domain:      "capability",
				Keywords:    []string{address, capability.DisplayName, capability.ClassName, capability.ProviderAddress, capability.ProviderKey, capability.CapabilityEndpoint.EndpointName},
				Attention:   80,
			},
		})
	}

	return entries
}

func capabilityAddressMatches(query string, fields ...string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	for _, field := range fields {
		field = strings.ToLower(strings.TrimSpace(field))
		if field == "" {
			continue
		}
		if strings.HasPrefix(field, query) || strings.Contains(field, query) {
			return true
		}
	}
	return false
}

func renderCapabilityTaskSummary(builder *strings.Builder, data CapabilitiesData) {
	groups := capabilityTaskGroups(data)
	renderPrimarySection(builder, "Tools By Task")
	if len(groups) == 0 {
		renderEmpty(builder, "No capabilities returned by the backend.")
		return
	}
	for _, group := range groups {
		fmt.Fprintf(builder, "  %s %s  tools=%d\n", renderDomainBadge(group.Domain), group.Label, len(group.Capabilities))
		limit := len(group.Capabilities)
		if limit > 3 {
			limit = 3
		}
		for _, capability := range group.Capabilities[:limit] {
			endpoint := capability.CapabilityEndpoint
			fmt.Fprintf(builder, "      %s  %s  risk=%s  auth=%d\n",
				firstNonEmpty(endpoint.CompactAddress, capability.DisplayName, capability.ClassName, endpoint.EndpointName, endpoint.CapabilityEndpointID),
				renderStatus(firstNonEmpty(endpoint.Status, "-")),
				firstNonEmpty(endpoint.RiskLevel, "-"),
				endpoint.ExecutionAuthorizationLevel,
			)
		}
		if hidden := len(group.Capabilities) - limit; hidden > 0 {
			renderEmpty(builder, fmt.Sprintf("%d more %s tool(s). Open the explorer for provider details.", hidden, strings.ToLower(group.Label)))
		}
	}
}

func capabilityTaskGroups(data CapabilitiesData) []capabilityTaskGroup {
	order := []struct {
		key    string
		label  string
		domain portalDomain
	}{
		{key: "projects", label: "Projects", domain: portalDomainProjects},
		{key: "notes", label: "Notes", domain: portalDomainNotes},
		{key: "storage", label: "Storage", domain: portalDomainStorage},
		{key: "automation", label: "Automation", domain: portalDomainAutomation},
		{key: "backup", label: "Backup", domain: portalDomainBackup},
		{key: "network", label: "Network", domain: portalDomainNetwork},
		{key: "diagnostics", label: "Diagnostics", domain: portalDomainDiagnostics},
	}
	groups := map[string]*capabilityTaskGroup{}
	for _, item := range order {
		groups[item.key] = &capabilityTaskGroup{Label: item.label, Domain: item.domain}
	}
	for _, capability := range data.Capabilities {
		key := capabilityTaskKey(capability)
		groups[key].Capabilities = append(groups[key].Capabilities, capability)
	}
	result := make([]capabilityTaskGroup, 0, len(order))
	for _, item := range order {
		group := groups[item.key]
		if len(group.Capabilities) == 0 {
			continue
		}
		sort.SliceStable(group.Capabilities, func(i, j int) bool {
			left := group.Capabilities[i].CapabilityEndpoint.CompactAddress
			right := group.Capabilities[j].CapabilityEndpoint.CompactAddress
			return left < right
		})
		result = append(result, *group)
	}
	return result
}

func capabilityTaskKey(capability capabilities.CapabilityListItem) string {
	value := strings.ToLower(strings.Join([]string{
		capability.CapabilityEndpoint.CompactAddress,
		capability.CapabilityEndpoint.EndpointName,
		capability.ClassName,
		capability.DisplayName,
		capability.Description,
		capability.ProviderAddress,
		capability.ProviderKey,
	}, " "))
	switch {
	case strings.Contains(value, "project"):
		return "projects"
	case strings.Contains(value, "notes") || strings.Contains(value, "knowledge"):
		return "notes"
	case strings.Contains(value, "storage") || strings.Contains(value, "object") || strings.Contains(value, "sync") || strings.Contains(value, "dropzone"):
		return "storage"
	case strings.Contains(value, "automation") || strings.Contains(value, "schedule") || strings.Contains(value, "event"):
		return "automation"
	case strings.Contains(value, "backup") || strings.Contains(value, "cloud"):
		return "backup"
	case strings.Contains(value, "node") || strings.Contains(value, "network") || strings.Contains(value, "provider"):
		return "network"
	default:
		return "diagnostics"
	}
}

func capabilityScopedSearchEntriesFromCandidates(candidates []capabilities.CapabilityCandidate) []portalSearchEntry {
	entries := make([]portalSearchEntry, 0, len(candidates))
	for _, candidate := range candidates {
		action := PortalAction{
			ID:            fmt.Sprintf("capability.endpoint.%s.inspect", safeActionID(firstNonEmpty(candidate.CapabilityEndpointID, candidate.CompactAddress))),
			Label:         "Inspect Capability",
			Description:   "Inspect capability schema, risk, provider, endpoint, and docs.",
			Domain:        "capabilities",
			SourceScreen:  ScreenCapabilities,
			TargetKind:    "capability",
			TargetRef:     firstNonEmpty(candidate.CapabilityEndpointID, candidate.CompactAddress),
			TargetLabel:   firstNonEmpty(candidate.CompactAddress, candidate.DisplayName, candidate.CapabilityEndpointID),
			Risk:          ActionRiskInspect,
			State:         ActionAvailable,
			InputValues:   map[string]string{},
			Executor:      PortalActionExecutor{Kind: PortalExecutorCapabilityInspect, Target: firstNonEmpty(candidate.CapabilityEndpointID, candidate.CompactAddress)},
			RawCommand:    []string{"loom", "capability", "inspect", firstNonEmpty(candidate.CapabilityEndpointID, candidate.CompactAddress)},
			RawDetails:    map[string]string{"address": candidate.CompactAddress, "provider_address": candidate.ProviderAddress, "class": candidate.ClassName},
			RefreshScreen: ScreenCapabilities,
		}
		action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
		entries = append(entries, portalSearchEntry{
			Action:      action,
			Category:    "capability",
			Title:       firstNonEmpty(candidate.CompactAddress, candidate.DisplayName, candidate.CapabilityEndpointID),
			Description: firstNonEmpty(candidate.DisplayName, candidate.Description, candidate.ClassName),
			Disabled:    action.Disabled(),
			Reason:      action.DisabledReason,
			Candidate: ui.Candidate{
				ID:          action.ID,
				Title:       firstNonEmpty(candidate.CompactAddress, candidate.DisplayName, candidate.CapabilityEndpointID),
				Description: firstNonEmpty(candidate.DisplayName, candidate.Description, candidate.ClassName),
				Domain:      "capability",
				Keywords:    []string{candidate.CompactAddress, candidate.ProviderAddress, candidate.DisplayName, candidate.ClassName},
				Attention:   80 + int(candidate.Score),
			},
		})
	}
	return entries
}
