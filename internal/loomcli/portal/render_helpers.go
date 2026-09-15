package portal

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"loom.local/loom/internal/loomcli/ui"
)

type portalSectionTier string

const (
	portalSectionPrimary     portalSectionTier = "primary"
	portalSectionAttention   portalSectionTier = "attention"
	portalSectionSummary     portalSectionTier = "summary"
	portalSectionDetails     portalSectionTier = "details"
	portalSectionDiagnostics portalSectionTier = "diagnostics"
)

type portalDomain string

const (
	portalDomainNotes       portalDomain = "notes"
	portalDomainStorage     portalDomain = "storage"
	portalDomainProjects    portalDomain = "projects"
	portalDomainAutomation  portalDomain = "automation"
	portalDomainJobs        portalDomain = "jobs"
	portalDomainNetwork     portalDomain = "network"
	portalDomainBackup      portalDomain = "backup"
	portalDomainDiagnostics portalDomain = "diagnostics"
)

type portalHealthItem struct {
	Label  string
	Status string
	Detail string
	Domain portalDomain
}

type footerContext string

const (
	footerBrowsing  footerContext = "browsing"
	footerSearch    footerContext = "search"
	footerPreview   footerContext = "preview"
	footerConfirm   footerContext = "confirm"
	footerResult    footerContext = "result"
	footerBoot      footerContext = "boot"
	footerDatabase  footerContext = "database"
	footerDBSearch  footerContext = "database_search"
	footerNotes     footerContext = "notes"
	footerCommand   footerContext = "command"
	footerCmdResult footerContext = "command_result"
)

func renderSection(builder *strings.Builder, title string) {
	renderSectionWithTier(builder, portalSectionPrimary, title)
}

func renderSectionWithTier(builder *strings.Builder, tier portalSectionTier, title string) {
	if builder.Len() > 0 {
		builder.WriteString("\n")
	}
	builder.WriteString(portalSectionStyle(tier).Render(title))
	builder.WriteString("\n")
}

func renderAttentionSection(builder *strings.Builder, title string) {
	renderSectionWithTier(builder, portalSectionAttention, title)
}

func renderPrimarySection(builder *strings.Builder, title string) {
	renderSectionWithTier(builder, portalSectionPrimary, title)
}

func renderSummarySection(builder *strings.Builder, title string) {
	renderSectionWithTier(builder, portalSectionSummary, title)
}

func renderDetailsSection(builder *strings.Builder, title string) {
	renderSectionWithTier(builder, portalSectionDetails, title)
}

func renderDiagnosticsSection(builder *strings.Builder, title string) {
	renderSectionWithTier(builder, portalSectionDiagnostics, title)
}

func portalSectionStyle(tier portalSectionTier) interface{ Render(...string) string } {
	styles := portalRenderContext().Styles
	switch tier {
	case portalSectionAttention:
		return styles.SectionAttention
	case portalSectionSummary:
		return styles.SectionSummary
	case portalSectionDetails:
		return styles.SectionDetails
	case portalSectionDiagnostics:
		return styles.SectionDiagnostics
	case portalSectionPrimary:
		return styles.SectionPrimary
	}
	return styles.Section
}

func renderKeyValue(builder *strings.Builder, key, value string) {
	renderWrappedTextLine(builder, "  ", "    ", fmt.Sprintf("%s: %s", key, value), nil)
}

func renderMetricLine(builder *strings.Builder, metrics ...string) {
	if len(metrics) == 0 {
		return
	}
	renderWrappedPartsLine(builder, "  ", "    ", "  ", metrics...)
}

func renderEmpty(builder *strings.Builder, message string) {
	renderWrappedTextLine(builder, "  ", "  ", message, portalRenderContext().Styles.Muted)
}

func renderHealthStrip(builder *strings.Builder, items ...portalHealthItem) {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		label := strings.TrimSpace(item.Label)
		if label == "" {
			continue
		}
		part := label
		if item.Domain != "" {
			part = renderDomainBadge(item.Domain) + " " + part
		}
		if strings.TrimSpace(item.Status) != "" {
			part += "=" + renderStatus(item.Status)
		}
		if strings.TrimSpace(item.Detail) != "" {
			part += " " + portalRenderContext().Styles.Muted.Render(strings.TrimSpace(item.Detail))
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return
	}
	renderWrappedPartsLine(builder, "  ", "    ", "  ", parts...)
}

func renderUnavailableScreen(builder *strings.Builder, state ScreenState) {
	availability := state.MainAvailability
	renderPrimarySection(builder, "Unavailable")
	renderHealthStrip(builder, portalHealthItem{
		Label:  "main",
		Status: firstNonEmpty(string(availability.State), string(MainAvailabilityOffline)),
		Detail: firstNonEmpty(availability.Target, "main target unavailable"),
		Domain: portalDomainNetwork,
	})
	renderEmpty(builder, "Main is offline")
	renderEmpty(builder, "This surface requires the main node.")
	renderEmpty(builder, "Press r to retry the connection.")
}

func renderDomainBadge(domain portalDomain) string {
	normalized := portalDomain(strings.TrimSpace(strings.ToLower(string(domain))))
	if normalized == "" {
		normalized = portalDomainDiagnostics
	}
	label := "[" + string(normalized) + "]"
	styles := portalRenderContext().Styles
	switch normalized {
	case portalDomainNotes:
		return styles.DomainNotes.Render(label)
	case portalDomainStorage:
		return styles.DomainStorage.Render(label)
	case portalDomainProjects:
		return styles.DomainProjects.Render(label)
	case portalDomainAutomation:
		return styles.DomainAutomation.Render(label)
	case portalDomainJobs:
		return styles.DomainJobs.Render(label)
	case portalDomainNetwork:
		return styles.DomainNetwork.Render(label)
	case portalDomainBackup:
		return styles.DomainBackup.Render(label)
	case portalDomainDiagnostics:
		return styles.DomainDiagnostics.Render(label)
	default:
		return styles.StatusInfo.Render(label)
	}
}

func renderLifecycleBadge(status string) string {
	status = strings.TrimSpace(strings.ToLower(strings.ReplaceAll(status, "-", "_")))
	if status == "" {
		status = "unknown"
	}
	return renderStatusBadge(status)
}

func renderLoading(builder *strings.Builder, title string) {
	fmt.Fprintf(builder, "%s %s...\n", portalRenderContext().Styles.StatusProgress.Render("Loading"), title)
}

func renderScreenError(builder *strings.Builder, title string, state ScreenState) {
	fmt.Fprintf(builder, "%s %s.\n", portalRenderContext().Styles.StatusDanger.Render("Could not load"), title)
	if state.Error != "" {
		fmt.Fprintf(builder, "  %s\n", state.Error)
	}
}

func renderPartialErrors(builder *strings.Builder, partials []SnapshotError) {
	if len(partials) == 0 {
		return
	}
	renderSection(builder, "Partial Data")
	for _, partial := range partials {
		fmt.Fprintf(builder, "  %s: %s\n", renderStatus("partial"), portalRenderContext().Styles.Muted.Render(partial.Source+" unavailable: "+partial.Message))
	}
}

func renderFooter(builder *strings.Builder) {
	renderFooterFor(builder, footerBrowsing)
}

func renderFooterFor(builder *strings.Builder, context footerContext) {
	labels := []string{
		"Keys: / search  # scoped  $ command  j/k move  trackpad/pgup/pgdn scroll",
		"      enter open  space actions  esc back  r refresh  tab details  ? help  q quit",
	}
	switch context {
	case footerSearch:
		labels = []string{"Keys: enter open  esc close  j/k move  trackpad/pgup/pgdn scroll"}
	case footerPreview:
		labels = []string{"Keys: enter continue  esc back  trackpad/pgup/pgdn scroll  tab raw details"}
	case footerConfirm:
		labels = []string{"Keys: enter confirm  esc cancel  trackpad/pgup/pgdn scroll  tab raw details"}
	case footerResult:
		labels = []string{"Keys: enter close  esc close  trackpad/pgup/pgdn scroll  tab raw details"}
	case footerCmdResult:
		labels = []string{"Keys: enter close  esc close  r rerun  trackpad/pgup/pgdn scroll  tab raw details"}
	case footerBoot:
		labels = []string{"Keys: enter skip  q quit"}
	case footerDatabase:
		labels = []string{
			"Keys: / search  # scoped  $ command  s object diagnostics search  j/k move  trackpad/pgup/pgdn scroll",
			"      enter open  space actions  esc back  r refresh  tab details  ? help  q quit",
		}
	case footerDBSearch:
		labels = []string{"Keys: enter search/open  esc close  j/k move  trackpad/pgup/pgdn scroll"}
	case footerNotes:
		labels = []string{
			"Keys: / search  # scoped  $ command  s notes search  j/k move  trackpad/pgup/pgdn scroll",
			"      enter open  space actions  esc back  r refresh  tab details  ? help  q quit",
		}
	case footerCommand:
		labels = []string{"Keys: enter preview/run  tab complete  j/k move  trackpad/pgup/pgdn scroll  esc close"}
	}
	builder.WriteString("\n")
	for _, label := range labels {
		trimmed := strings.TrimLeft(label, " ")
		firstPrefix := label[:len(label)-len(trimmed)]
		renderWrappedTextLine(builder, firstPrefix, "      ", trimmed, portalRenderContext().Styles.Footer)
	}
}

func renderSelectedMarker(index, selected int) string {
	if index == selected {
		return portalRenderContext().Styles.SelectedMarker.Render(">")
	}
	return " "
}

func renderSearchBox(builder *strings.Builder, query string, width int) {
	renderSearchBoxWithTitle(builder, "Command Palette", "type to search", query, width)
}

func renderSearchBoxWithTitle(builder *strings.Builder, title string, placeholder string, query string, width int) {
	if width <= 0 {
		width = 72
	}
	boxWidth := width
	if boxWidth > 80 {
		boxWidth = 80
	}
	if boxWidth < 32 {
		boxWidth = 32
	}
	innerWidth := boxWidth - 4
	label := " " + firstNonEmpty(title, "Command Palette") + " "
	topFill := innerWidth - len(label)
	if topFill < 0 {
		topFill = 0
	}
	top := "+" + label + strings.Repeat("-", topFill) + "+"
	searchValue := strings.TrimSpace(query)
	if searchValue == "" {
		searchValue = portalRenderContext().Styles.Muted.Render(firstNonEmpty(placeholder, "type to search"))
	}
	line := " Search  " + searchValue
	line = trimForWidth(line, innerWidth)
	padding := innerWidth - len(stripANSI(line))
	if padding < 0 {
		padding = 0
	}
	bottom := "+" + strings.Repeat("-", innerWidth) + "+"
	border := portalRenderContext().Styles.BorderFocus
	builder.WriteString(border.Render(top))
	builder.WriteByte('\n')
	builder.WriteString(border.Render("| "))
	builder.WriteString(line)
	builder.WriteString(strings.Repeat(" ", padding))
	builder.WriteString(border.Render(" |"))
	builder.WriteByte('\n')
	builder.WriteString(border.Render(bottom))
	builder.WriteString("\n\n")
}

func stripANSI(value string) string {
	var builder strings.Builder
	inEscape := false
	inCSI := false
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if inEscape {
			if inCSI {
				if ch >= '@' && ch <= '~' {
					inEscape = false
					inCSI = false
				}
				continue
			}
			if ch == '[' {
				inCSI = true
				continue
			}
			if ch >= '@' && ch <= '~' {
				inEscape = false
			}
			continue
		}
		if ch == 0x1b {
			inEscape = true
			continue
		}
		builder.WriteByte(ch)
	}
	return builder.String()
}

type portalTextRenderer interface {
	Render(...string) string
}

func portalLineWidth(value string) int {
	return lipgloss.Width(value)
}

func portalWrapWidth() int {
	width := portalRenderContext().Width
	if width <= 0 {
		width = 80
	}
	if width < 20 {
		width = 20
	}
	return width
}

func renderWrappedTextLine(builder *strings.Builder, firstPrefix string, continuationPrefix string, text string, renderer portalTextRenderer) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	width := portalWrapWidth()
	lines := wrapANSIText(text, width-portalLineWidth(firstPrefix))
	for idx, line := range lines {
		prefix := firstPrefix
		if idx > 0 {
			prefix = continuationPrefix
		}
		value := prefix + line
		if renderer != nil {
			value = renderer.Render(value)
		}
		builder.WriteString(value)
		builder.WriteByte('\n')
	}
}

func renderWrappedPartsLine(builder *strings.Builder, firstPrefix string, continuationPrefix string, separator string, parts ...string) {
	width := portalWrapWidth()
	line := firstPrefix
	hasContent := false
	resetLine := func() {
		line = continuationPrefix
		hasContent = false
	}
	flush := func() {
		if !hasContent {
			return
		}
		builder.WriteString(line)
		builder.WriteByte('\n')
		resetLine()
	}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		wrapped := wrapANSIText(part, width-portalLineWidth(continuationPrefix))
		for idx, piece := range wrapped {
			joiner := ""
			if hasContent && idx == 0 {
				joiner = separator
			}
			if hasContent && portalLineWidth(line+joiner+piece) <= width {
				line += joiner + piece
				continue
			}
			if hasContent {
				flush()
			}
			line += piece
			hasContent = true
			if idx < len(wrapped)-1 {
				flush()
			}
		}
	}
	flush()
}

func wrapANSIText(value string, width int) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if width <= 0 || portalLineWidth(value) <= width {
		return []string{value}
	}
	words := strings.Fields(value)
	if len(words) == 0 {
		return nil
	}
	lines := []string{}
	current := ""
	for _, word := range words {
		for _, piece := range splitLongANSIWord(word, width) {
			if current == "" {
				current = piece
				continue
			}
			if portalLineWidth(current+" "+piece) <= width {
				current += " " + piece
				continue
			}
			lines = append(lines, current)
			current = piece
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

func splitLongANSIWord(word string, width int) []string {
	if width <= 0 || portalLineWidth(word) <= width {
		return []string{word}
	}
	plain := stripANSI(word)
	if plain == "" {
		return []string{word}
	}
	chunks := []string{}
	current := ""
	for _, r := range plain {
		next := current + string(r)
		if current != "" && portalLineWidth(next) > width {
			chunks = append(chunks, current)
			current = string(r)
			continue
		}
		current = next
	}
	if current != "" {
		chunks = append(chunks, current)
	}
	return chunks
}

func usableWidth(width int, reserved int) int {
	if width <= 0 {
		return 80 - reserved
	}
	usable := width - reserved
	if usable < 20 {
		return 20
	}
	return usable
}

func RenderHelp(mode ui.Mode) string {
	ctx := renderContext{Mode: mode, Styles: NewPortalStyles(mode), Width: mode.TTY.Width, Height: mode.TTY.Height, Logo: SelectPortalLogo(mode.TTY.Width, nil)}
	restore := setActiveRenderContext(ctx)
	defer restore()
	var builder strings.Builder
	renderTitle(&builder, mode, "Keys")
	builder.WriteString("/ search screens and navigation\n")
	builder.WriteString("start screens: " + portalScreenIDsHelp() + "\n")
	builder.WriteString("screen groups: " + portalScreenGroupsHelp() + "\n")
	builder.WriteString("#box searches LOOM Box folders, profile-local workspace intake, and watch policy\n")
	builder.WriteString("#notes searches LOOM Notes roots, file types, projection, and search context\n")
	builder.WriteString("#projects searches projects, facets, and project-owned records\n")
	builder.WriteString("#capabilities main@system searches capability addresses\n")
	builder.WriteString("#doctor, #storage, #database, #workers, #automation, #jobs, #nodes search large families\n")
	builder.WriteString("#objects, #archives, #transfers, #cloud, #schedules, and #indexes are scoped aliases\n")
	builder.WriteString("timeline opens running work and grouped operation history with status\n")
	builder.WriteString("type $ in search for raw LOOM command mode\n")
	builder.WriteString("guided actions open as previews or forms before they run\n")
	builder.WriteString("s object diagnostics search on Object Store Diagnostics\n")
	builder.WriteString("j/k move\n")
	builder.WriteString("trackpad/wheel, pgup/pgdn, or ctrl+u/ctrl+d scroll\n")
	builder.WriteString("g/G jump to top/bottom outside text input\n")
	builder.WriteString("space expand capability/provider actions\n")
	builder.WriteString("enter open / preview / confirm / close\n")
	builder.WriteString("esc back / cancel / close\n")
	builder.WriteString("h/backspace up one capability explorer level\n")
	builder.WriteString("r refresh; on command result rerun\n")
	builder.WriteString("tab details / complete in command mode\n")
	builder.WriteString("? help\n")
	builder.WriteString("q quit\n")
	return builder.String()
}

func portalScreenIDsHelp() string {
	screens := Screens()
	values := make([]string, 0, len(screens))
	for _, screen := range screens {
		values = append(values, screen.ID)
	}
	return strings.Join(values, ", ")
}

func portalScreenGroupsHelp() string {
	groups := ScreenGroups()
	values := make([]string, 0, len(groups))
	for _, group := range groups {
		screenIDs := make([]string, 0, len(group.Screens))
		for _, screen := range group.Screens {
			screenIDs = append(screenIDs, screen.ID)
		}
		values = append(values, group.Title+"("+strings.Join(screenIDs, ", ")+")")
	}
	return strings.Join(values, "; ")
}

func trimForWidth(value string, width int) string {
	value = strings.TrimSpace(value)
	if width <= 0 || len(value) <= width {
		return value
	}
	if width <= 3 {
		return value[:width]
	}
	return value[:width-3] + "..."
}

func fitPortalViewToHeight(output string, height int) string {
	if height <= 0 {
		return output
	}
	lines := strings.Split(output, "\n")
	if len(lines) <= height {
		return output
	}
	if height <= 1 {
		return "... output clipped"
	}
	clipped := append([]string{}, lines[:height]...)
	clipped[height-1] = portalRenderContext().Styles.Muted.Render("... output clipped; use scoped search or resize terminal")
	return strings.Join(clipped, "\n")
}
