package supportbundle

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"loom.local/loom/internal/loomcli/portal"
)

type DoctorDataProvider func(context.Context, Options) (portal.DoctorData, error)

type DoctorSummary struct {
	Status           string                      `json:"status"`
	Summary          string                      `json:"summary,omitempty"`
	GeneratedAt      string                      `json:"generated_at,omitempty"`
	TotalFindings    int                         `json:"total_findings"`
	SeverityCounts   map[string]int              `json:"severity_counts"`
	AreaCounts       map[string]int              `json:"area_counts"`
	Areas            []DoctorAreaSummary         `json:"areas,omitempty"`
	TopFindings      []DoctorFindingSummary      `json:"top_findings,omitempty"`
	PartialLoadCount int                         `json:"partial_load_count"`
	PartialLoads     []portal.DoctorPartialError `json:"partial_loads,omitempty"`
}

type DoctorAreaSummary struct {
	Key          string `json:"key"`
	Label        string `json:"label,omitempty"`
	Status       string `json:"status,omitempty"`
	Summary      string `json:"summary,omitempty"`
	FindingCount int    `json:"finding_count"`
	TargetScreen string `json:"target_screen,omitempty"`
}

type DoctorFindingSummary struct {
	ID             string `json:"id"`
	Area           string `json:"area,omitempty"`
	Severity       string `json:"severity,omitempty"`
	Title          string `json:"title,omitempty"`
	Impact         string `json:"impact,omitempty"`
	SafeNextAction string `json:"safe_next_action,omitempty"`
	TargetKind     string `json:"target_kind,omitempty"`
	TargetRef      string `json:"target_ref,omitempty"`
	TargetLabel    string `json:"target_label,omitempty"`
	InspectAction  string `json:"inspect_action,omitempty"`
	SafeRepair     string `json:"safe_repair,omitempty"`
	SourceKind     string `json:"source_kind,omitempty"`
	SourceID       string `json:"source_id,omitempty"`
}

func collectDoctor(ctx context.Context, collection CollectionContext) (CollectorOutput, error) {
	if collection.Options.DoctorDataProvider == nil {
		return CollectorOutput{}, fmt.Errorf("doctor data provider unavailable")
	}
	data, err := collection.Options.DoctorDataProvider(ctx, collection.Options)
	if err != nil {
		return CollectorOutput{}, err
	}
	summary := summarizeDoctorData(data, collection.Options.MaxItems)
	jsonData, err := marshalJSON(summary)
	if err != nil {
		return CollectorOutput{}, err
	}
	return CollectorOutput{Files: []File{
		{
			Path:         "summaries/doctor.json",
			ContentType:  "application/json",
			PrivacyClass: PrivacyDiagnosticSummary,
			Data:         jsonData,
		},
		{
			Path:         "human/attention.txt",
			ContentType:  "text/plain",
			PrivacyClass: PrivacyDiagnosticSummary,
			Data:         []byte(renderDoctorAttention(summary)),
		},
	}}, nil
}

func summarizeDoctorData(data portal.DoctorData, maxItems int) DoctorSummary {
	if maxItems <= 0 {
		maxItems = DefaultMaxItems
	}
	summary := DoctorSummary{
		Status:           data.Status,
		Summary:          data.Summary,
		TotalFindings:    data.Totals.Total,
		SeverityCounts:   doctorSeverityCounts(data),
		AreaCounts:       doctorAreaCounts(data),
		PartialLoadCount: len(data.PartialErrors),
		PartialLoads:     data.PartialErrors,
	}
	if !data.GeneratedAt.IsZero() {
		summary.GeneratedAt = data.GeneratedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	for _, area := range data.Areas {
		summary.Areas = append(summary.Areas, DoctorAreaSummary{
			Key:          area.Key,
			Label:        area.Label,
			Status:       area.Status,
			Summary:      area.Summary,
			FindingCount: area.FindingCount,
			TargetScreen: area.TargetScreen,
		})
	}
	findings := append([]portal.DoctorFinding{}, data.Findings...)
	sort.SliceStable(findings, func(i, j int) bool {
		if severityRank(findings[i].Severity) == severityRank(findings[j].Severity) {
			return findings[i].ID < findings[j].ID
		}
		return severityRank(findings[i].Severity) > severityRank(findings[j].Severity)
	})
	if len(findings) > maxItems {
		findings = findings[:maxItems]
	}
	for _, finding := range findings {
		summary.TopFindings = append(summary.TopFindings, DoctorFindingSummary{
			ID:             finding.ID,
			Area:           finding.Area,
			Severity:       finding.Severity,
			Title:          finding.Title,
			Impact:         finding.Impact,
			SafeNextAction: finding.SafeNextAction,
			TargetKind:     finding.TargetKind,
			TargetRef:      finding.TargetRef,
			TargetLabel:    finding.TargetLabel,
			InspectAction:  finding.InspectActionID,
			SafeRepair:     finding.SafeRepairActionID,
			SourceKind:     finding.SourceKind,
			SourceID:       finding.SourceID,
		})
	}
	return summary
}

func doctorSeverityCounts(data portal.DoctorData) map[string]int {
	return map[string]int{
		"critical": data.Totals.Critical,
		"warning":  data.Totals.Warning,
		"info":     data.Totals.Info,
	}
}

func doctorAreaCounts(data portal.DoctorData) map[string]int {
	counts := map[string]int{}
	for _, area := range data.Areas {
		counts[area.Key] = area.FindingCount
	}
	return counts
}

func renderDoctorAttention(summary DoctorSummary) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "LOOM Doctor: %s\n", firstNonEmpty(summary.Status, "unknown"))
	if summary.Summary != "" {
		fmt.Fprintf(&builder, "%s\n", summary.Summary)
	}
	fmt.Fprintf(&builder, "Findings: total=%d critical=%d warning=%d info=%d\n",
		summary.TotalFindings,
		summary.SeverityCounts["critical"],
		summary.SeverityCounts["warning"],
		summary.SeverityCounts["info"],
	)
	if len(summary.TopFindings) == 0 {
		fmt.Fprintf(&builder, "No active Doctor findings.\n")
		return builder.String()
	}
	fmt.Fprintf(&builder, "Top findings:\n")
	for _, finding := range summary.TopFindings {
		fmt.Fprintf(&builder, "- %s %s: %s\n", finding.Severity, finding.Area, finding.Title)
		if finding.SafeNextAction != "" {
			fmt.Fprintf(&builder, "  next: %s\n", finding.SafeNextAction)
		}
	}
	return builder.String()
}

func severityRank(severity string) int {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case portal.DoctorSeverityCritical:
		return 3
	case portal.DoctorSeverityWarning:
		return 2
	case portal.DoctorSeverityInfo:
		return 1
	default:
		return 0
	}
}
