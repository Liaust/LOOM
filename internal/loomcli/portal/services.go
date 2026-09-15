package portal

import (
	"fmt"
	"sort"
	"strings"

	"loom.local/loom/internal/serviceregistry"
)

const portalServiceLogLineLimit = 20

func renderServices(builder *strings.Builder, state ScreenState) {
	data := state.Data.Services
	records := ScreenRecordItems(state)
	row := renderTopAvailableActions(builder, state, records)
	renderOperationalActionGroups(builder, state, records, &row)

	attention := 0
	active := 0
	for _, service := range data.Services {
		if service.RegistryState == serviceregistry.ProviderStateActive {
			active++
		}
		if service.RegistryState != serviceregistry.ProviderStateActive || service.ProcessState == serviceregistry.ProcessStateFailed || service.ProcessState == serviceregistry.ProcessStateUnavailable || service.HealthStatus == "unhealthy" || service.HealthStatus == "offline" {
			attention++
		}
	}
	renderSummarySection(builder, "Service Inventory")
	renderMetricLine(builder, fmt.Sprintf("total=%d", len(data.Services)), fmt.Sprintf("active=%d", active), fmt.Sprintf("attention=%d", attention))
	if len(data.Services) == 0 {
		renderEmpty(builder, "No registered services were returned by main.")
		return
	}

	nodes := map[string][]serviceregistry.ServiceListItem{}
	for _, service := range data.Services {
		node := firstNonEmpty(service.NodeID, "unknown node")
		nodes[node] = append(nodes[node], service)
	}
	keys := make([]string, 0, len(nodes))
	for node := range nodes {
		keys = append(keys, node)
	}
	sort.Strings(keys)
	for _, node := range keys {
		renderPrimarySection(builder, "Node: "+node)
		for _, service := range nodes[node] {
			ref := firstNonEmpty(service.ProviderAddress, service.ProviderID)
			marker := " "
			if item, ok := selectedRecordItem(state); ok && item.RecordRef == ref {
				marker = portalRenderContext().Styles.SelectedMarker.Render(">")
			}
			fmt.Fprintf(builder, "%s %-24s registry=%s  process=%s  health=%s\n", marker, firstNonEmpty(service.DisplayName, service.ProviderKey, ref), renderStatus(string(service.RegistryState)), renderStatus(string(service.ProcessState)), renderStatus(firstNonEmpty(service.HealthStatus, "unknown")))
		}
	}

	selected := selectedServiceForRender(state, data)
	renderServiceDetail(builder, data, selected)
}

func selectedServiceForRender(state ScreenState, data ServicesData) serviceregistry.ServiceListItem {
	if item, ok := selectedRecordItem(state); ok {
		for _, service := range data.Services {
			if item.RecordRef == firstNonEmpty(service.ProviderAddress, service.ProviderID) {
				return service
			}
		}
	}
	return data.Services[0]
}

func renderServiceDetail(builder *strings.Builder, data ServicesData, service serviceregistry.ServiceListItem) {
	renderDetailsSection(builder, "Selected Service")
	renderKeyValue(builder, "service", firstNonEmpty(service.DisplayName, service.ProviderKey, service.ProviderAddress))
	renderKeyValue(builder, "owner scope", firstNonEmpty(service.ScopeID, "-"))
	renderKeyValue(builder, "node", firstNonEmpty(service.NodeID, "-"))
	renderKeyValue(builder, "registry state", renderStatus(string(service.RegistryState)))
	renderKeyValue(builder, "observed process", renderStatus(string(service.ProcessState)))
	renderKeyValue(builder, "health", renderStatus(firstNonEmpty(service.HealthStatus, "unknown")))
	renderKeyValue(builder, "last observation", timePtrOrDash(service.LastObservedAt))
	inspection, ok := serviceInspection(data, service)
	if !ok {
		renderKeyValue(builder, "unit", "unavailable")
		renderEmpty(builder, "Service detail is unavailable; no healthy state is inferred.")
		return
	}
	renderKeyValue(builder, "unit", firstNonEmpty(inspection.RuntimeProfile.Unit, "-"))
	renderKeyValue(builder, "manager", firstNonEmpty(string(inspection.RuntimeProfile.Manager), "-"))
	renderServiceReferences(builder, inspection.References)
	lines := data.BoundedLogLines[firstNonEmpty(service.ProviderAddress, service.ProviderID)]
	if len(lines) > portalServiceLogLineLimit {
		lines = lines[:portalServiceLogLineLimit]
	}
	renderDetailsSection(builder, "Bounded Logs")
	if len(lines) == 0 {
		renderEmpty(builder, "No log result is loaded. Use Diagnostics > Read Logs for bounded, redacted output.")
		return
	}
	for _, line := range lines {
		fmt.Fprintf(builder, "  %s\n", line)
	}
}

func renderServiceReferences(builder *strings.Builder, refs serviceregistry.RuntimeReferences) {
	for _, row := range []struct {
		label  string
		values []string
	}{{"protection refs", refs.Protection}, {"exposure refs", refs.Exposure}, {"credential refs", refs.Credentials}} {
		value := "-"
		if len(row.values) > 0 {
			value = strings.Join(row.values, ", ")
		}
		renderKeyValue(builder, row.label, value)
	}
}
