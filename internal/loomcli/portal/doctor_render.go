package portal

import (
	"fmt"
	"sort"
	"strings"
)

func renderDoctor(builder *strings.Builder, state ScreenState) {
	data := state.Data.Doctor
	if data.GeneratedAt.IsZero() && len(data.Areas) == 0 && len(data.Findings) == 0 {
		data = BuildDoctorData(Snapshot{})
	}
	renderDoctorStatus(builder, data)
	renderDoctorFindings(builder, state, data)
	renderDoctorSelectedFinding(builder, state, data)
	renderDoctorAreas(builder, state, data)
	renderDoctorPartialErrors(builder, data)
	renderDoctorRawSources(builder, state, data)
}

func renderDoctorStatus(builder *strings.Builder, data DoctorData) {
	renderHealthStrip(builder,
		portalHealthItem{Label: "status", Status: data.Status, Detail: data.Summary, Domain: portalDomainDiagnostics},
		portalHealthItem{Label: "critical", Status: doctorCountStatus(data.Totals.Critical), Detail: fmt.Sprintf("%d", data.Totals.Critical)},
		portalHealthItem{Label: "warning", Status: doctorCountStatus(data.Totals.Warning), Detail: fmt.Sprintf("%d", data.Totals.Warning)},
		portalHealthItem{Label: "partial loads", Status: doctorCountStatus(len(data.PartialErrors)), Detail: fmt.Sprintf("%d", len(data.PartialErrors))},
	)
}

func renderDoctorFindings(builder *strings.Builder, state ScreenState, data DoctorData) {
	renderPrimarySection(builder, "Current Issues")
	if len(data.Findings) == 0 {
		renderEmpty(builder, "No active issues found. Doctor will show prioritized repair paths when LOOM needs attention.")
		return
	}
	currentSeverity := ""
	for idx, finding := range data.Findings {
		severity := normalizeDoctorSeverity(finding.Severity)
		if severity != currentSeverity {
			currentSeverity = severity
			fmt.Fprintf(builder, "  %s\n", portalRenderContext().Styles.Muted.Render(doctorSeverityGroupLabel(severity)))
		}
		fmt.Fprintf(builder, "  %s %s %-18s %-34s %s\n",
			renderSelectedMarker(idx, state.SelectedIndex),
			renderStatus(severity),
			trimForWidth(doctorAreaLabel(finding.Area), 18),
			trimForWidth(finding.Title, 34),
			trimForWidth(finding.SafeNextAction, 42),
		)
	}
}

func renderDoctorSelectedFinding(builder *strings.Builder, state ScreenState, data DoctorData) {
	finding, ok := selectedDoctorFinding(data, state.SelectedIndex)
	if !ok {
		return
	}
	renderDetailsSection(builder, "Selected Finding")
	renderKeyValue(builder, "issue", finding.Title)
	renderKeyValue(builder, "area", doctorAreaLabel(finding.Area)+"  "+renderStatus(finding.Severity))
	if finding.Explanation != "" {
		renderKeyValue(builder, "detail", finding.Explanation)
	}
	if finding.Impact != "" {
		renderKeyValue(builder, "impact", finding.Impact)
	}
	renderKeyValue(builder, "next", finding.SafeNextAction)
	renderKeyValue(builder, "target", doctorTargetLabel(finding))
	actions := doctorRelatedActionsForFinding(finding, state.RawDetails)
	if len(actions) == 0 {
		renderEmpty(builder, "No direct action is exposed for this finding. Open the target surface and inspect details.")
		return
	}
	renderDoctorActionRows(builder, actions)
}

func renderDoctorAreas(builder *strings.Builder, state ScreenState, data DoctorData) {
	renderSummarySection(builder, "System Areas")
	for _, area := range data.Areas {
		if area.FindingCount == 0 && !state.RawDetails {
			continue
		}
		fmt.Fprintf(builder, "  %s %-20s %s\n",
			renderStatus(area.Status),
			trimForWidth(area.Label, 20),
			area.Summary,
		)
	}
}

func renderDoctorPartialErrors(builder *strings.Builder, data DoctorData) {
	if len(data.PartialErrors) > 0 {
		renderDetailsSection(builder, "Partial Loads")
		for _, partial := range data.PartialErrors {
			fmt.Fprintf(builder, "  %s %s: %s\n", renderStatus(DoctorSeverityWarning), firstNonEmpty(partial.Source, "unknown"), partial.Message)
		}
	}
}

func renderDoctorRawSources(builder *strings.Builder, state ScreenState, data DoctorData) {
	if !state.RawDetails {
		return
	}
	renderDetailsSection(builder, "Doctor Sources")
	fmt.Fprintf(builder, "  generated_at=%s areas=%d findings=%d partial_errors=%d\n",
		timeOrDash(data.GeneratedAt),
		len(data.Areas),
		len(data.Findings),
		len(data.PartialErrors),
	)
	for _, finding := range data.Findings {
		fmt.Fprintf(builder, "  finding=%s source=%s/%s target=%s/%s inspect=%s safe_repair=%s dangerous_repair=%s\n",
			finding.ID,
			firstNonEmpty(finding.SourceKind, "-"),
			firstNonEmpty(finding.SourceID, "-"),
			firstNonEmpty(finding.TargetKind, "-"),
			firstNonEmpty(finding.TargetRef, "-"),
			firstNonEmpty(finding.InspectActionID, "-"),
			firstNonEmpty(finding.SafeRepairActionID, "-"),
			firstNonEmpty(finding.DangerousRepairActionID, "-"),
		)
		if len(finding.RawDetails) > 0 {
			fmt.Fprintf(builder, "      raw=%s\n", doctorRawDetailsString(finding.RawDetails))
		}
	}
}

func renderDoctorActionRows(builder *strings.Builder, actions []PortalAction) {
	fmt.Fprintf(builder, "  actions:\n")
	for _, action := range actions {
		status := renderRisk(action.Risk)
		if action.Disabled() {
			status = renderStatus("disabled")
		}
		confirmation := "no confirmation"
		if action.RequiresConfirmation() {
			confirmation = "confirmation"
		}
		fmt.Fprintf(builder, "    - %s  %s  %s  target=%s\n",
			action.Label,
			status,
			portalRenderContext().Styles.Muted.Render(confirmation),
			firstNonEmpty(action.TargetLabel, action.TargetRef, "-"),
		)
	}
}

func selectedDoctorFinding(data DoctorData, selected int) (DoctorFinding, bool) {
	if len(data.Findings) == 0 {
		return DoctorFinding{}, false
	}
	selected = clampIndex(selected, len(data.Findings))
	return data.Findings[selected], true
}

func doctorCountStatus(count int) string {
	if count > 0 {
		return DoctorStatusWarning
	}
	return DoctorStatusOK
}

func doctorSeverityGroupLabel(severity string) string {
	switch normalizeDoctorSeverity(severity) {
	case DoctorSeverityCritical:
		return "Critical"
	case DoctorSeverityWarning:
		return "Warnings"
	default:
		return "Info"
	}
}

func doctorTargetLabel(finding DoctorFinding) string {
	switch strings.TrimSpace(finding.TargetKind) {
	case DoctorTargetKindScreen:
		return screenTitle(firstNonEmpty(finding.TargetRef, doctorAreaTargetScreen(finding.Area)))
	case DoctorTargetKindRecord:
		return firstNonEmpty(finding.TargetLabel, finding.TargetRef, "-")
	case DoctorTargetKindAction:
		return firstNonEmpty(finding.TargetLabel, finding.TargetRef, "-")
	default:
		return firstNonEmpty(finding.TargetLabel, finding.TargetRef, "-")
	}
}

func doctorRawDetailsString(details map[string]string) string {
	keys := make([]string, 0, len(details))
	for key := range details {
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		value := strings.TrimSpace(details[key])
		if value == "" {
			value = "-"
		}
		parts = append(parts, key+"="+value)
	}
	return strings.Join(parts, " ")
}
