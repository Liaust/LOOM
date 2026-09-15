package portal

import (
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"loom.local/loom/internal/loomcli/ui"
)

type PortalStyles struct {
	Enabled            bool
	Text               lipgloss.Style
	Muted              lipgloss.Style
	Disabled           lipgloss.Style
	Logo               lipgloss.Style
	Title              lipgloss.Style
	Section            lipgloss.Style
	SectionPrimary     lipgloss.Style
	SectionAttention   lipgloss.Style
	SectionSummary     lipgloss.Style
	SectionDetails     lipgloss.Style
	SectionDiagnostics lipgloss.Style
	Subsection         lipgloss.Style
	ActionSubsection   lipgloss.Style
	Footer             lipgloss.Style
	Border             lipgloss.Style
	BorderFocus        lipgloss.Style
	SelectedMarker     lipgloss.Style
	SelectedRow        lipgloss.Style
	StatusSuccess      lipgloss.Style
	StatusProgress     lipgloss.Style
	StatusDanger       lipgloss.Style
	StatusMuted        lipgloss.Style
	StatusInfo         lipgloss.Style
	DomainNotes        lipgloss.Style
	DomainStorage      lipgloss.Style
	DomainProjects     lipgloss.Style
	DomainAutomation   lipgloss.Style
	DomainJobs         lipgloss.Style
	DomainNetwork      lipgloss.Style
	DomainBackup       lipgloss.Style
	DomainDiagnostics  lipgloss.Style
	Raw                lipgloss.Style
	Code               lipgloss.Style
}

type renderContext struct {
	Mode   ui.Mode
	Styles PortalStyles
	Width  int
	Height int
	Logo   PortalLogo
}

type statusClass string

const (
	statusClassSuccess  statusClass = "success"
	statusClassProgress statusClass = "progress"
	statusClassDanger   statusClass = "danger"
	statusClassMuted    statusClass = "muted"
	statusClassInfo     statusClass = "info"
)

var activeRenderContext = renderContext{Styles: NewPortalStyles(ui.Mode{})}

func NewPortalStyles(mode ui.Mode) PortalStyles {
	if !mode.CanColor() {
		return PortalStyles{}
	}
	theme, err := ui.ResolveTheme(mode.ThemeName)
	if err != nil {
		return PortalStyles{}
	}
	renderer := lipgloss.NewRenderer(io.Discard)
	switch mode.TTY.ColorProfile {
	case ui.ColorANSI:
		renderer.SetColorProfile(termenv.ANSI)
	case ui.ColorANSI256:
		renderer.SetColorProfile(termenv.ANSI256)
	default:
		renderer.SetColorProfile(termenv.TrueColor)
	}
	role := func(role ui.Role) lipgloss.Color {
		return lipgloss.Color(theme.Color(role))
	}
	style := func() lipgloss.Style {
		return renderer.NewStyle()
	}
	return PortalStyles{
		Enabled:            true,
		Text:               style().Foreground(role(ui.RoleTextPrimary)),
		Muted:              style().Foreground(role(ui.RoleTextMuted)),
		Disabled:           style().Foreground(role(ui.RoleTextDisabled)),
		Logo:               style().Bold(true).Foreground(role(ui.RoleBrand)),
		Title:              style().Bold(true).Foreground(role(ui.RoleBrand)),
		Section:            style().Bold(true).Foreground(role(ui.RoleAccent)),
		SectionPrimary:     style().Bold(true).Foreground(role(ui.RoleAccent)),
		SectionAttention:   style().Bold(true).Foreground(role(ui.RoleDanger)),
		SectionSummary:     style().Bold(true).Foreground(role(ui.RoleInfo)),
		SectionDetails:     style().Foreground(role(ui.RoleTextMuted)),
		SectionDiagnostics: style().Foreground(role(ui.RoleTextMuted)),
		Subsection:         style().Foreground(role(ui.RoleTextMuted)),
		ActionSubsection:   style().Bold(true).Foreground(role(ui.RoleInfo)),
		Footer:             style().Foreground(role(ui.RoleFooter)),
		Border:             style().Foreground(role(ui.RoleBorder)),
		BorderFocus:        style().Foreground(role(ui.RoleBorderFocus)),
		SelectedMarker:     style().Bold(true).Foreground(role(ui.RoleSelection)),
		SelectedRow:        style().Bold(true).Foreground(role(ui.RoleTextPrimary)).Background(role(ui.RoleSelection)),
		StatusSuccess:      style().Foreground(role(ui.RoleSuccess)),
		StatusProgress:     style().Foreground(role(ui.RoleWarning)),
		StatusDanger:       style().Bold(true).Foreground(role(ui.RoleDanger)),
		StatusMuted:        style().Foreground(role(ui.RoleTextMuted)),
		StatusInfo:         style().Foreground(role(ui.RoleInfo)),
		DomainNotes:        style().Foreground(role(ui.RoleInfo)),
		DomainStorage:      style().Foreground(role(ui.RoleSuccess)),
		DomainProjects:     style().Foreground(role(ui.RoleWarning)),
		DomainAutomation:   style().Foreground(role(ui.RoleAccent)),
		DomainJobs:         style().Foreground(role(ui.RoleWarning)),
		DomainNetwork:      style().Foreground(role(ui.RoleInfo)),
		DomainBackup:       style().Foreground(role(ui.RoleSuccess)),
		DomainDiagnostics:  style().Foreground(role(ui.RoleTextMuted)),
		Raw:                style().Foreground(role(ui.RoleTextMuted)),
		Code:               style().Foreground(role(ui.RoleCode)),
	}
}

func newRenderContext(input RenderInput) renderContext {
	width := input.Width
	if width <= 0 {
		width = input.Mode.TTY.Width
	}
	height := input.Height
	if height <= 0 {
		height = input.Mode.TTY.Height
	}
	logo := input.Logo
	if len(logo.Lines) == 0 {
		logo = SelectPortalLogoForViewport(width, height, nil)
	}
	return renderContext{
		Mode:   input.Mode,
		Styles: NewPortalStyles(input.Mode),
		Width:  width,
		Height: height,
		Logo:   logo,
	}
}

func setActiveRenderContext(ctx renderContext) func() {
	previous := activeRenderContext
	activeRenderContext = ctx
	return func() {
		activeRenderContext = previous
	}
}

func portalRenderContext() renderContext {
	return activeRenderContext
}

func classifyStatus(status string) statusClass {
	normalized := strings.TrimSpace(strings.ToLower(strings.ReplaceAll(status, "-", "_")))
	switch normalized {
	case "ok", "healthy", "active", "accepted", "cataloged", "published_to_storage_view", "local_cleanup_done", "completed", "complete", "succeeded", "indexed", "synced", "allowed", "available", "open":
		return statusClassSuccess
	case "pending", "prepared", "transferring", "transferred", "accepted_on_main", "queued", "running", "starting", "rebuilding", "indexing", "extracting", "chunking", "received", "in_flight", "lagging", "partial", "degraded", "due", "warning", "warn", "stale", "missed", "attention", "needs_attention":
		return statusClassProgress
	case "failed", "failure", "promotion_failed", "catalog_failed", "source_cleanup_failed", "obsolete_accepted_migration_required", "local_cleanup_withheld", "error", "critical", "danger", "denied", "conflicted", "unhealthy", "manual_action", "manual_action_required", "timed_out", "cancelled", "corrupt", "missing", "unavailable":
		return statusClassDanger
	case "disabled", "paused", "skipped", "skipped_unsupported", "disabled_by_policy", "private_no_index", "private_backup_only", "not_requested", "retired", "unknown", "-", "_":
		return statusClassMuted
	default:
		if normalized == "" {
			return statusClassMuted
		}
		return statusClassInfo
	}
}

func renderStatus(status string) string {
	if strings.TrimSpace(status) == "" {
		status = "-"
	}
	styles := portalRenderContext().Styles
	switch classifyStatus(status) {
	case statusClassSuccess:
		return styles.StatusSuccess.Render(status)
	case statusClassProgress:
		return styles.StatusProgress.Render(status)
	case statusClassDanger:
		return styles.StatusDanger.Render(status)
	case statusClassMuted:
		return styles.StatusMuted.Render(status)
	default:
		return styles.StatusInfo.Render(status)
	}
}

func renderRisk(risk PortalActionRisk) string {
	styles := portalRenderContext().Styles
	value := string(risk)
	switch risk {
	case ActionRiskInspect:
		return styles.StatusMuted.Render(value)
	case ActionRiskSafeRun:
		return styles.StatusProgress.Render(value)
	case ActionRiskSensitive, ActionRiskDangerous, ActionRiskBlocked:
		return styles.StatusDanger.Render(value)
	default:
		return styles.StatusInfo.Render(value)
	}
}

func renderCategory(category string) string {
	category = strings.TrimSpace(strings.ToLower(category))
	if category == "" {
		category = "action"
	}
	label := "[" + category + "]"
	styles := portalRenderContext().Styles
	switch category {
	case "screen":
		return styles.StatusProgress.Render(label)
	case "worker", "index", "maintenance", "database":
		return styles.StatusInfo.Render(label)
	case "raw":
		return styles.Raw.Render(label)
	default:
		return styles.Muted.Render(label)
	}
}

func renderStatusBadge(status string) string {
	if strings.TrimSpace(status) == "" {
		status = "-"
	}
	label := "[" + status + "]"
	styles := portalRenderContext().Styles
	switch classifyStatus(status) {
	case statusClassSuccess:
		return styles.StatusSuccess.Render(label)
	case statusClassProgress:
		return styles.StatusProgress.Render(label)
	case statusClassDanger:
		return styles.StatusDanger.Render(label)
	case statusClassMuted:
		return styles.StatusMuted.Render(label)
	default:
		return styles.StatusInfo.Render(label)
	}
}
