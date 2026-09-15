package portal

import (
	"fmt"
	"strings"

	"loom.local/loom/internal/loomcli/ui"
)

func RenderCommandPalette(mode ui.Mode, state CommandModeState, width int, height int) string {
	ctx := renderContext{Mode: mode, Styles: NewPortalStyles(mode), Width: width, Height: height, Logo: SelectPortalLogo(width, nil)}
	restore := setActiveRenderContext(ctx)
	defer restore()
	var builder strings.Builder
	renderTitle(&builder, mode, "LOOM Command")
	builder.WriteString(portalRenderContext().Styles.Muted.Render("Run LOOM commands inside the portal"))
	builder.WriteString("\n\n")
	renderSearchBox(&builder, state.Input, width)
	preview := state.Preview
	if preview.ParseError != "" {
		renderSection(&builder, "Problem")
		fmt.Fprintf(&builder, "  %s\n", preview.ParseError)
	} else if preview.CanonicalCommand != "loom" {
		renderSection(&builder, "Preview")
		fmt.Fprintf(&builder, "  Canonical: %s\n", preview.CanonicalCommand)
		fmt.Fprintf(&builder, "  Classification: %s\n", renderRisk(preview.Classification.portalRisk()))
		fmt.Fprintf(&builder, "  Dependency: %s\n", firstNonEmpty(string(preview.ExecutionDependency), string(ExecutionDependencyMain)))
		if preview.AliasApplied != "" {
			fmt.Fprintf(&builder, "  Alias: %s\n", preview.AliasApplied)
		}
		if preview.EffectSummary != "" {
			fmt.Fprintf(&builder, "  Effect: %s\n", preview.EffectSummary)
		}
		if reason := commandBlockedReason(preview); reason != "" {
			fmt.Fprintf(&builder, "  %s: %s\n", renderStatus("blocked"), reason)
		}
	}
	if len(state.Suggestions) > 0 {
		renderCommandSuggestions(&builder, state)
	}
	renderFooterFor(&builder, footerCommand)
	return builder.String()
}

func RenderCommandPreview(mode ui.Mode, preview CommandPreview, rawDetails bool) string {
	restore := setActiveRenderContext(renderContext{Mode: mode, Styles: NewPortalStyles(mode), Width: mode.TTY.Width, Height: mode.TTY.Height, Logo: SelectPortalLogo(mode.TTY.Width, nil)})
	defer restore()
	var builder strings.Builder
	renderTitle(&builder, mode, "Command Preview")
	renderCommandPreviewBody(&builder, preview)
	renderCommandRawDetails(&builder, preview, rawDetails)
	renderFooterFor(&builder, footerPreview)
	return builder.String()
}

func RenderCommandConfirmation(mode ui.Mode, preview CommandPreview, rawDetails bool) string {
	restore := setActiveRenderContext(renderContext{Mode: mode, Styles: NewPortalStyles(mode), Width: mode.TTY.Width, Height: mode.TTY.Height, Logo: SelectPortalLogo(mode.TTY.Width, nil)})
	defer restore()
	var builder strings.Builder
	renderTitle(&builder, mode, "Confirm Command")
	renderCommandPreviewBody(&builder, preview)
	builder.WriteString("\nEnter confirms. Esc cancels.\n")
	renderCommandRawDetails(&builder, preview, rawDetails)
	renderFooterFor(&builder, footerConfirm)
	return builder.String()
}

func RenderCommandRunning(mode ui.Mode, preview CommandPreview) string {
	restore := setActiveRenderContext(renderContext{Mode: mode, Styles: NewPortalStyles(mode), Width: mode.TTY.Width, Height: mode.TTY.Height, Logo: SelectPortalLogo(mode.TTY.Width, nil)})
	defer restore()
	var builder strings.Builder
	renderTitle(&builder, mode, "Running Command")
	fmt.Fprintf(&builder, "Command: %s\n", preview.CanonicalCommand)
	fmt.Fprintf(&builder, "Status: %s\n", renderStatus("running"))
	return builder.String()
}

func RenderCommandResult(mode ui.Mode, result CommandResult, rawDetails bool) string {
	restore := setActiveRenderContext(renderContext{Mode: mode, Styles: NewPortalStyles(mode), Width: mode.TTY.Width, Height: mode.TTY.Height, Logo: SelectPortalLogo(mode.TTY.Width, nil)})
	defer restore()
	title := "Command Complete"
	if result.Status == ActionLifecycleFailed {
		title = "Command Failed"
	}
	if result.Status == ActionLifecycleCancelled {
		title = "Command Cancelled"
	}
	var builder strings.Builder
	renderTitle(&builder, mode, title)
	fmt.Fprintf(&builder, "Input: %s\n", firstNonEmpty(result.Input, "-"))
	fmt.Fprintf(&builder, "Canonical: %s\n", firstNonEmpty(result.CanonicalCommand, commandString(result.CanonicalTokens)))
	fmt.Fprintf(&builder, "Classification: %s\n", renderRisk(result.Classification.portalRisk()))
	if result.Summary != "" {
		fmt.Fprintf(&builder, "Summary: %s\n", result.Summary)
	}
	if result.ErrorMessage != "" {
		renderSection(&builder, "Reason")
		fmt.Fprintf(&builder, "  %s\n", result.ErrorMessage)
	}
	if trimmed := strings.TrimSpace(result.Stdout); trimmed != "" {
		renderSection(&builder, "Output")
		renderCommandBlock(&builder, trimmed, 80)
	}
	if trimmed := strings.TrimSpace(result.Stderr); trimmed != "" {
		renderSection(&builder, "Error Output")
		renderCommandBlock(&builder, trimmed, 40)
	}
	if rawDetails {
		renderSection(&builder, "Raw Details")
		fmt.Fprintf(&builder, "  started=%s completed=%s\n", timeOrDash(result.StartedAt), timeOrDash(result.CompletedAt))
		if result.ErrorCode != "" {
			fmt.Fprintf(&builder, "  error_code=%s\n", result.ErrorCode)
		}
	}
	renderFooterFor(&builder, footerCmdResult)
	return builder.String()
}

func RenderCommandCompletions(mode ui.Mode, input string, suggestions []CommandSuggestion, selected int) string {
	state := CommandModeState{Active: true, Input: input, Preview: BuildCommandPreview(input), Suggestions: suggestions, SelectedSuggestion: selected, CompletionOpen: true}
	return RenderCommandPalette(mode, state, mode.TTY.Width, mode.TTY.Height)
}

func renderCommandPreviewBody(builder *strings.Builder, preview CommandPreview) {
	renderSection(builder, "Command")
	fmt.Fprintf(builder, "  Input: %s\n", firstNonEmpty(preview.Input, "-"))
	fmt.Fprintf(builder, "  Canonical: %s\n", firstNonEmpty(preview.CanonicalCommand, "loom"))
	renderSection(builder, "Classification")
	fmt.Fprintf(builder, "  %s\n", renderRisk(preview.Classification.portalRisk()))
	fmt.Fprintf(builder, "  Dependency: %s\n", firstNonEmpty(string(preview.ExecutionDependency), string(ExecutionDependencyMain)))
	if preview.TargetRef != "" {
		renderSection(builder, "Target")
		fmt.Fprintf(builder, "  %s: %s\n", firstNonEmpty(preview.TargetKind, "target"), preview.TargetRef)
	}
	if preview.EffectSummary != "" {
		renderSection(builder, "Effect")
		fmt.Fprintf(builder, "  %s\n", preview.EffectSummary)
	}
	if preview.RequiresConfirmation {
		renderSection(builder, "Confirmation")
		builder.WriteString("  Confirmation: required before execution\n")
	}
	if preview.MainAvailability.State == MainAvailabilityOffline && preview.ExecutionDependency == ExecutionDependencyMain {
		renderSection(builder, "Unavailable")
		builder.WriteString("  Main is offline. This command requires the main node.\n")
	}
	if reason := commandBlockedReason(preview); reason != "" {
		renderSection(builder, "Blocked")
		fmt.Fprintf(builder, "  %s\n", reason)
	}
}

func renderCommandRawDetails(builder *strings.Builder, preview CommandPreview, visible bool) {
	if !visible {
		return
	}
	renderSection(builder, "Raw Details")
	fmt.Fprintf(builder, "  tokens=%s\n", strings.Join(preview.CanonicalTokens, " "))
	if preview.AliasApplied != "" {
		fmt.Fprintf(builder, "  alias=%s\n", preview.AliasApplied)
	}
}

func renderCommandSuggestions(builder *strings.Builder, state CommandModeState) {
	renderSection(builder, "Completions")
	for idx, suggestion := range state.Suggestions {
		marker := renderSelectedMarker(idx, state.SelectedSuggestion)
		fmt.Fprintf(builder, "  %s %s  %s  %s\n", marker, suggestion.Label, renderCategory(suggestion.Kind), suggestion.InsertText)
		if suggestion.Description != "" {
			fmt.Fprintf(builder, "      %s\n", portalRenderContext().Styles.Muted.Render(suggestion.Description))
		}
	}
}

func renderCommandBlock(builder *strings.Builder, text string, limit int) {
	lines := strings.Split(text, "\n")
	if limit <= 0 || limit > len(lines) {
		limit = len(lines)
	}
	for idx := 0; idx < limit; idx++ {
		fmt.Fprintf(builder, "  %s\n", lines[idx])
	}
	if limit < len(lines) {
		fmt.Fprintf(builder, "  ... truncated %d lines\n", len(lines)-limit)
	}
}
