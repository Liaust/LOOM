package portal

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	DoctorStatusOK       = "ok"
	DoctorStatusWarning  = "warning"
	DoctorStatusCritical = "critical"

	DoctorSeverityInfo     = "info"
	DoctorSeverityWarning  = "warning"
	DoctorSeverityCritical = "critical"

	DoctorAreaSystem       = "system"
	DoctorAreaDatabase     = "database"
	DoctorAreaWorkers      = "workers"
	DoctorAreaJobs         = "jobs_indexing"
	DoctorAreaAutomation   = "automation"
	DoctorAreaNodes        = "nodes_sync"
	DoctorAreaBoxLane      = "box_lane"
	DoctorAreaStorage      = "storage"
	DoctorAreaCloudBackup  = "cloud_backup"
	DoctorAreaNotes        = "notes"
	DoctorAreaProjects     = "projects"
	DoctorAreaPortal       = "portal"
	DoctorAreaUnknown      = "unknown"
	DoctorSourceEmptyState = "doctor.empty_state"
	DoctorSourcePortalLoad = "portal.partial_load"
	DoctorTargetKindScreen = "screen"
	DoctorTargetKindRecord = "record"
	DoctorTargetKindAction = "action"
)

type DoctorData struct {
	GeneratedAt   time.Time
	Status        string
	Summary       string
	Totals        DoctorTotals
	Areas         []DoctorArea
	Findings      []DoctorFinding
	PartialErrors []DoctorPartialError
}

type DoctorTotals struct {
	Total    int
	Critical int
	Warning  int
	Info     int
}

type DoctorArea struct {
	Key          string
	Label        string
	Status       string
	Summary      string
	FindingCount int
	TargetScreen string
}

type DoctorFinding struct {
	ID                      string
	Area                    string
	Severity                string
	Title                   string
	Explanation             string
	Impact                  string
	SafeNextAction          string
	TargetKind              string
	TargetRef               string
	TargetLabel             string
	InspectActionID         string
	SafeRepairActionID      string
	DangerousRepairActionID string
	SourceKind              string
	SourceID                string
	ObservedAt              time.Time
	RawDetails              map[string]string
}

type DoctorPartialError struct {
	Source  string
	Message string
}

func BuildDoctorData(snapshot Snapshot) DoctorData {
	generatedAt := snapshot.CapturedAt
	if generatedAt.IsZero() {
		generatedAt = time.Now().UTC()
	}
	home := BuildHomeData(snapshot)
	findings := make([]DoctorFinding, 0, len(home.Attention)+1)
	if snapshot.MainAvailability.State == MainAvailabilityOffline {
		findings = append(findings, mainOfflineDoctorFinding(snapshot.MainAvailability, generatedAt))
	}
	for _, item := range home.Attention {
		if item.ID == "main.offline" {
			continue
		}
		findings = append(findings, doctorFindingFromAttention(item, generatedAt))
	}
	partials := snapshot.PartialErrors
	if snapshot.MainAvailability.State == MainAvailabilityOffline {
		partials = withoutPartialSource(partials, "main_availability")
	}
	return BuildDoctorDataFromFindings(generatedAt, findings, partials)
}

func mainOfflineDoctorFinding(availability MainAvailability, generatedAt time.Time) DoctorFinding {
	checkedAt := availability.CheckedAt
	if checkedAt.IsZero() {
		checkedAt = generatedAt
	}
	target := firstNonEmpty(availability.Target, "configured main target")
	return DoctorFinding{
		ID:             "doctor.nodes_sync.main_offline",
		Area:           DoctorAreaNodes,
		Severity:       DoctorSeverityWarning,
		Title:          "Main node is unreachable",
		Explanation:    "Main could not be reached at " + target + ". Last checked " + checkedAt.Local().Format("15:04:05") + ".",
		Impact:         "Main-owned surfaces and actions are unavailable; local Box and workspace Lane state remains readable.",
		SafeNextAction: "Restore network or main availability and press r.",
		TargetKind:     DoctorTargetKindRecord,
		TargetRef:      target,
		TargetLabel:    target,
		SourceKind:     "main_availability",
		SourceID:       firstNonEmpty(availability.Source, "health"),
		ObservedAt:     checkedAt,
		RawDetails: map[string]string{
			"transport":  string(availability.Transport),
			"target":     target,
			"checked_at": checkedAt.Format(time.RFC3339),
			"cause":      availability.Cause,
		},
	}
}

func BuildDoctorDataFromFindings(generatedAt time.Time, findings []DoctorFinding, partials []SnapshotError) DoctorData {
	data := DoctorData{
		GeneratedAt:   generatedAt,
		Findings:      normalizeDoctorFindings(findings),
		PartialErrors: doctorPartialErrors(partials),
	}
	data.Totals = doctorTotals(data.Findings)
	data.Status = doctorStatusFromTotals(data.Totals)
	data.Areas = doctorAreas(data.Findings)
	data.Summary = doctorSummary(data)
	return data
}

func normalizeDoctorSeverity(severity string) string {
	switch strings.TrimSpace(strings.ToLower(severity)) {
	case "critical", "fatal", "error", "failed", "failure":
		return DoctorSeverityCritical
	case "warning", "warn", "degraded", "stale", "partial", "manual_action":
		return DoctorSeverityWarning
	case "info", "ok", "healthy":
		return DoctorSeverityInfo
	default:
		if strings.TrimSpace(severity) == "" {
			return DoctorSeverityInfo
		}
		return DoctorSeverityWarning
	}
}

func doctorFindingFromAttention(item AttentionItem, generatedAt time.Time) DoctorFinding {
	item = applyAttentionRepairSuggestion(item)
	observedAt := item.CreatedAt
	if observedAt.IsZero() {
		observedAt = generatedAt
	}
	area := doctorAreaForAttention(item)
	id := stableDoctorAttentionID(area, item.ID)
	safeRepairID, dangerousRepairID := doctorRepairActionIDs(item.RepairActionID)
	return DoctorFinding{
		ID:                      id,
		Area:                    area,
		Severity:                item.Severity,
		Title:                   firstNonEmpty(item.Title, "LOOM issue needs attention"),
		Explanation:             firstNonEmpty(item.Reason, "Existing portal attention requires review."),
		Impact:                  doctorImpactForAttention(item),
		SafeNextAction:          firstNonEmpty(item.Suggestion, "Open the related surface and inspect details"),
		TargetKind:              firstNonEmpty(item.TargetKind, DoctorTargetKindScreen),
		TargetRef:               firstNonEmpty(item.TargetRef, doctorAreaTargetScreen(area)),
		TargetLabel:             screenTitle(firstNonEmpty(item.TargetRef, doctorAreaTargetScreen(area))),
		InspectActionID:         doctorInspectActionID(item, area),
		SafeRepairActionID:      safeRepairID,
		DangerousRepairActionID: dangerousRepairID,
		SourceKind:              "home_attention",
		SourceID:                item.ID,
		ObservedAt:              observedAt,
		RawDetails: map[string]string{
			"attention_id":      item.ID,
			"domain":            item.Domain,
			"failure_class":     item.FailureClass,
			"related_action_id": item.RelatedActionID,
			"repair_action_id":  item.RepairActionID,
			"target_kind":       item.TargetKind,
			"target_ref":        item.TargetRef,
		},
	}
}

func stableDoctorAttentionID(area string, attentionID string) string {
	id := strings.TrimSpace(attentionID)
	if id == "" {
		id = "attention"
	}
	id = strings.NewReplacer(" ", "_", "/", "_", ":", "_").Replace(strings.ToLower(id))
	return strings.Join([]string{"doctor", normalizeDoctorArea(area), id}, ".")
}

func doctorAreaForAttention(item AttentionItem) string {
	text := attentionSearchText(item)
	domain := strings.ToLower(strings.TrimSpace(item.Domain))
	switch {
	case strings.HasPrefix(item.ID, "partial."):
		return DoctorAreaPortal
	case containsAny(domain, "system"):
		return DoctorAreaSystem
	case containsAny(domain, "runtime", "database") || strings.Contains(text, "migration") || strings.Contains(text, "bootstrap"):
		return DoctorAreaDatabase
	case containsAny(domain, "worker", "maintenance"):
		if containsAny(text, "backup", "cloud") {
			return DoctorAreaCloudBackup
		}
		return DoctorAreaWorkers
	case containsAny(domain, "job", "search") || strings.Contains(text, "index"):
		return DoctorAreaJobs
	case containsAny(domain, "automation", "schedule", "direct event", "invocation"):
		return DoctorAreaAutomation
	case containsAny(domain, "network", "watched") || containsAny(text, "node", "watched root", "sync"):
		return DoctorAreaNodes
	case containsAny(text, "box", "lane"):
		return DoctorAreaBoxLane
	case containsAny(domain, "storage") || containsAny(text, "storage", "retention", "fidelity", "mount"):
		return DoctorAreaStorage
	case containsAny(domain, "cloud", "backup") || containsAny(text, "cloud", "backup"):
		return DoctorAreaCloudBackup
	case containsAny(domain, "notes", "knowledge") || containsAny(text, "notes", "projection"):
		return DoctorAreaNotes
	case containsAny(domain, "project") || strings.Contains(text, "project"):
		return DoctorAreaProjects
	default:
		return DoctorAreaUnknown
	}
}

func doctorImpactForAttention(item AttentionItem) string {
	switch normalizeDoctorSeverity(item.Severity) {
	case DoctorSeverityCritical:
		return "This can block normal LOOM operation until inspected."
	case DoctorSeverityWarning:
		return "This can reduce reliability or leave work waiting for operator attention."
	default:
		return "This is informational context for the current LOOM state."
	}
}

func doctorStatusFromTotals(totals DoctorTotals) string {
	switch {
	case totals.Critical > 0:
		return DoctorStatusCritical
	case totals.Warning > 0:
		return DoctorStatusWarning
	default:
		return DoctorStatusOK
	}
}

func doctorStatusForSeverity(severity string) string {
	switch normalizeDoctorSeverity(severity) {
	case DoctorSeverityCritical:
		return DoctorStatusCritical
	case DoctorSeverityWarning:
		return DoctorStatusWarning
	default:
		return DoctorStatusOK
	}
}

func doctorAreaLabel(area string) string {
	switch normalizeDoctorArea(area) {
	case DoctorAreaSystem:
		return "System"
	case DoctorAreaDatabase:
		return "Database"
	case DoctorAreaWorkers:
		return "Workers"
	case DoctorAreaJobs:
		return "Jobs And Indexing"
	case DoctorAreaAutomation:
		return "Automation"
	case DoctorAreaNodes:
		return "Nodes And Sync"
	case DoctorAreaBoxLane:
		return "Box And Workspace Lane"
	case DoctorAreaStorage:
		return "Storage"
	case DoctorAreaCloudBackup:
		return "Cloud And Backup"
	case DoctorAreaNotes:
		return "Notes"
	case DoctorAreaProjects:
		return "Projects"
	case DoctorAreaPortal:
		return "Portal"
	default:
		return "Other"
	}
}

func doctorAreaTargetScreen(area string) string {
	switch normalizeDoctorArea(area) {
	case DoctorAreaDatabase:
		return ScreenDatabase
	case DoctorAreaWorkers, DoctorAreaCloudBackup:
		return ScreenBackground
	case DoctorAreaJobs:
		return ScreenJobs
	case DoctorAreaAutomation:
		return ScreenAutomations
	case DoctorAreaNodes:
		return ScreenNodes
	case DoctorAreaBoxLane:
		return ScreenBox
	case DoctorAreaStorage:
		return ScreenStorage
	case DoctorAreaNotes:
		return ScreenNotes
	case DoctorAreaProjects:
		return ScreenProjects
	default:
		return ScreenHome
	}
}

func normalizeDoctorArea(area string) string {
	value := strings.TrimSpace(strings.ToLower(area))
	switch value {
	case DoctorAreaSystem, "runtime", "health":
		return DoctorAreaSystem
	case DoctorAreaDatabase, "db", "migrations", "bootstrap":
		return DoctorAreaDatabase
	case DoctorAreaWorkers, "worker", "background", "maintenance":
		return DoctorAreaWorkers
	case DoctorAreaJobs, "jobs", "job", "index", "indexes", "indexing", "search":
		return DoctorAreaJobs
	case DoctorAreaAutomation, "automations", "schedule", "schedules", "direct_events", "direct event", "invocations":
		return DoctorAreaAutomation
	case DoctorAreaNodes, "nodes", "node", "sync", "watched_roots", "watched roots":
		return DoctorAreaNodes
	case DoctorAreaBoxLane, "box", "lane":
		return DoctorAreaBoxLane
	case DoctorAreaStorage, "main_storage", "storage_export", "retention":
		return DoctorAreaStorage
	case DoctorAreaCloudBackup, "cloud", "backup", "backups":
		return DoctorAreaCloudBackup
	case DoctorAreaNotes, "note", "knowledge":
		return DoctorAreaNotes
	case DoctorAreaProjects, "project":
		return DoctorAreaProjects
	case DoctorAreaPortal:
		return DoctorAreaPortal
	default:
		if value == "" {
			return DoctorAreaUnknown
		}
		return value
	}
}

func normalizeDoctorFindings(findings []DoctorFinding) []DoctorFinding {
	normalized := make([]DoctorFinding, 0, len(findings))
	for _, finding := range findings {
		finding.Area = normalizeDoctorArea(finding.Area)
		finding.Severity = normalizeDoctorSeverity(finding.Severity)
		finding.ID = strings.TrimSpace(finding.ID)
		if finding.ID == "" {
			finding.ID = stableDoctorFindingID(finding)
		}
		finding.Title = strings.TrimSpace(finding.Title)
		if finding.Title == "" {
			finding.Title = "LOOM issue needs attention"
		}
		finding.SafeNextAction = strings.TrimSpace(finding.SafeNextAction)
		if finding.SafeNextAction == "" {
			finding.SafeNextAction = "Open the related surface and inspect details"
		}
		if finding.TargetKind == "" {
			finding.TargetKind = DoctorTargetKindScreen
		}
		if finding.TargetRef == "" {
			finding.TargetRef = doctorAreaTargetScreen(finding.Area)
		}
		normalized = append(normalized, finding)
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		if doctorSeverityRank(normalized[i].Severity) != doctorSeverityRank(normalized[j].Severity) {
			return doctorSeverityRank(normalized[i].Severity) < doctorSeverityRank(normalized[j].Severity)
		}
		if normalized[i].Area != normalized[j].Area {
			return normalized[i].Area < normalized[j].Area
		}
		return normalized[i].ID < normalized[j].ID
	})
	return normalized
}

func stableDoctorFindingID(finding DoctorFinding) string {
	parts := []string{
		"doctor",
		normalizeDoctorArea(finding.Area),
		strings.TrimSpace(finding.SourceKind),
		strings.TrimSpace(finding.SourceID),
		strings.TrimSpace(finding.TargetKind),
		strings.TrimSpace(finding.TargetRef),
		strings.TrimSpace(finding.Title),
	}
	cleaned := []string{}
	for _, part := range parts {
		part = strings.Trim(strings.ToLower(part), " \t\r\n")
		part = strings.NewReplacer(" ", "_", "/", "_", ":", "_", ".", "_").Replace(part)
		if part != "" {
			cleaned = append(cleaned, part)
		}
	}
	return strings.Join(cleaned, ".")
}

func doctorSeverityRank(severity string) int {
	switch normalizeDoctorSeverity(severity) {
	case DoctorSeverityCritical:
		return 0
	case DoctorSeverityWarning:
		return 1
	default:
		return 2
	}
}

func doctorTotals(findings []DoctorFinding) DoctorTotals {
	totals := DoctorTotals{Total: len(findings)}
	for _, finding := range findings {
		switch normalizeDoctorSeverity(finding.Severity) {
		case DoctorSeverityCritical:
			totals.Critical++
		case DoctorSeverityWarning:
			totals.Warning++
		default:
			totals.Info++
		}
	}
	return totals
}

func doctorAreas(findings []DoctorFinding) []DoctorArea {
	counts := map[string]DoctorTotals{}
	for _, finding := range findings {
		area := normalizeDoctorArea(finding.Area)
		totals := counts[area]
		totals.Total++
		switch normalizeDoctorSeverity(finding.Severity) {
		case DoctorSeverityCritical:
			totals.Critical++
		case DoctorSeverityWarning:
			totals.Warning++
		default:
			totals.Info++
		}
		counts[area] = totals
	}
	areas := []DoctorArea{}
	for _, key := range doctorAreaOrder() {
		totals := counts[key]
		area := DoctorArea{
			Key:          key,
			Label:        doctorAreaLabel(key),
			Status:       doctorStatusFromTotals(totals),
			FindingCount: totals.Total,
			TargetScreen: doctorAreaTargetScreen(key),
		}
		if totals.Total == 0 {
			area.Summary = "No active findings"
		} else {
			area.Summary = doctorAreaSummary(totals)
		}
		areas = append(areas, area)
	}
	return areas
}

func doctorAreaOrder() []string {
	return []string{
		DoctorAreaSystem,
		DoctorAreaDatabase,
		DoctorAreaWorkers,
		DoctorAreaJobs,
		DoctorAreaAutomation,
		DoctorAreaNodes,
		DoctorAreaBoxLane,
		DoctorAreaStorage,
		DoctorAreaCloudBackup,
		DoctorAreaNotes,
		DoctorAreaProjects,
		DoctorAreaPortal,
	}
}

func doctorAreaSummary(totals DoctorTotals) string {
	parts := []string{}
	if totals.Critical > 0 {
		parts = append(parts, pluralizeCount(totals.Critical, "critical finding", "critical findings"))
	}
	if totals.Warning > 0 {
		parts = append(parts, pluralizeCount(totals.Warning, "warning", "warnings"))
	}
	if totals.Info > 0 {
		parts = append(parts, pluralizeCount(totals.Info, "info", "info"))
	}
	return strings.Join(parts, ", ")
}

func doctorSummary(data DoctorData) string {
	if data.Totals.Total == 0 && len(data.PartialErrors) == 0 {
		return "No active issues found in the current portal snapshot."
	}
	parts := []string{}
	if data.Totals.Critical > 0 {
		parts = append(parts, pluralizeCount(data.Totals.Critical, "critical issue", "critical issues"))
	}
	if data.Totals.Warning > 0 {
		parts = append(parts, pluralizeCount(data.Totals.Warning, "warning", "warnings"))
	}
	if len(data.PartialErrors) > 0 {
		parts = append(parts, pluralizeCount(len(data.PartialErrors), "partial load error", "partial load errors"))
	}
	if len(parts) == 0 {
		parts = append(parts, pluralizeCount(data.Totals.Total, "finding", "findings"))
	}
	return strings.Join(parts, ", ")
}

func doctorPartialErrors(partials []SnapshotError) []DoctorPartialError {
	if len(partials) == 0 {
		return nil
	}
	result := make([]DoctorPartialError, 0, len(partials))
	for _, partial := range partials {
		result = append(result, DoctorPartialError{Source: partial.Source, Message: partial.Message})
	}
	return result
}

func pluralizeCount(count int, singular string, plural string) string {
	if count == 1 {
		return "1 " + singular
	}
	return strconv.Itoa(count) + " " + plural
}
