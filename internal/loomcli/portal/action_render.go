package portal

import (
	"fmt"
	"sort"
	"strings"

	"loom.local/loom/internal/loomcli/actions"
	"loom.local/loom/internal/loomcli/ui"
)

func RenderPortalActionPreview(mode ui.Mode, state ActionPanelState) string {
	restore := setActiveRenderContext(renderContext{Mode: mode, Styles: NewPortalStyles(mode), Width: mode.TTY.Width, Height: mode.TTY.Height, Logo: SelectPortalLogo(mode.TTY.Width, nil)})
	defer restore()
	action := state.Action
	var builder strings.Builder
	title := "Action Preview"
	if state.Lifecycle == ActionLifecycleNeedsInput {
		title = "Action Form"
	}
	renderTitle(&builder, mode, title)
	renderSection(&builder, "Action")
	fmt.Fprintf(&builder, "  %s\n", action.Label)
	fmt.Fprintf(&builder, "  Interaction: %s\n", action.InteractionLabel())
	renderSection(&builder, "Effect")
	fmt.Fprintf(&builder, "  %s\n", firstNonEmpty(action.Description, "-"))
	renderSection(&builder, "Target")
	fmt.Fprintf(&builder, "  %s\n", firstNonEmpty(action.TargetLabel, action.TargetRef, "-"))
	renderSection(&builder, "Risk")
	fmt.Fprintf(&builder, "  Risk: %s\n", renderRisk(action.Risk))
	if action.Disabled() {
		fmt.Fprintf(&builder, "  %s: %s\n", renderStatus("disabled"), firstNonEmpty(action.DisabledReason, "This action is disabled."))
	}
	renderSection(&builder, "Confirmation")
	if action.RequiresConfirmation() {
		builder.WriteString("  Confirmation: required before execution\n")
		if action.ConfirmationPolicy.Prompt != "" {
			fmt.Fprintf(&builder, "  %s\n", action.ConfirmationPolicy.Prompt)
		}
	} else {
		builder.WriteString("  Confirmation: not required\n")
	}
	renderActionInputs(&builder, state)
	renderActionRawDetails(&builder, action, state.RawDetails)
	renderFooterFor(&builder, footerPreview)
	return builder.String()
}

func RenderPortalActionConfirmation(mode ui.Mode, state ActionPanelState) string {
	restore := setActiveRenderContext(renderContext{Mode: mode, Styles: NewPortalStyles(mode), Width: mode.TTY.Width, Height: mode.TTY.Height, Logo: SelectPortalLogo(mode.TTY.Width, nil)})
	defer restore()
	action := state.Action
	var builder strings.Builder
	renderTitle(&builder, mode, "Confirm Action")
	fmt.Fprintf(&builder, "Action: %s\n", action.Label)
	fmt.Fprintf(&builder, "Target: %s\n", firstNonEmpty(action.TargetLabel, action.TargetRef, "-"))
	fmt.Fprintf(&builder, "Risk: %s\n", renderRisk(action.Risk))
	fmt.Fprintf(&builder, "Effect: %s\n", action.Description)
	if action.ConfirmationPolicy.Prompt != "" {
		fmt.Fprintf(&builder, "Prompt: %s\n", action.ConfirmationPolicy.Prompt)
	}
	renderActionInputs(&builder, state)
	builder.WriteString("\nEnter confirms. Esc cancels.\n")
	renderActionRawDetails(&builder, action, state.RawDetails)
	renderFooterFor(&builder, footerConfirm)
	return builder.String()
}

func RenderPortalActionRunning(mode ui.Mode, state ActionPanelState) string {
	restore := setActiveRenderContext(renderContext{Mode: mode, Styles: NewPortalStyles(mode), Width: mode.TTY.Width, Height: mode.TTY.Height, Logo: SelectPortalLogo(mode.TTY.Width, nil)})
	defer restore()
	var builder strings.Builder
	title := "Running Action"
	status := "running"
	if state.Lifecycle == ActionLifecyclePreflighting {
		title = "Checking Folder"
		status = "preflighting"
	}
	if state.Lifecycle == ActionLifecycleMutation {
		title = "Saving Protection"
		status = "mutation"
	}
	renderTitle(&builder, mode, title)
	fmt.Fprintf(&builder, "Action: %s\n", state.Action.Label)
	fmt.Fprintf(&builder, "Target: %s\n", firstNonEmpty(state.Action.TargetLabel, state.Action.TargetRef, "-"))
	fmt.Fprintf(&builder, "Status: %s\n", renderStatus(status))
	return builder.String()
}

func RenderPortalActionResult(mode ui.Mode, state ActionPanelState) string {
	restore := setActiveRenderContext(renderContext{Mode: mode, Styles: NewPortalStyles(mode), Width: mode.TTY.Width, Height: mode.TTY.Height, Logo: SelectPortalLogo(mode.TTY.Width, nil)})
	defer restore()
	result := state.Result
	title := "Action Complete"
	if result.Status == ActionLifecycleFailed {
		title = "Action Failed"
	}
	if result.Status == ActionLifecycleCancelled {
		title = "Action Cancelled"
	}
	if result.Status == ActionLifecycleReview {
		title = "Review Folder Check"
		if state.Action.Domain == "projects" {
			title = "Review Project Move"
		}
	}
	if result.Status == ActionLifecycleWaiting {
		title = "Waiting For Node"
	}
	var builder strings.Builder
	renderTitle(&builder, mode, title)
	fmt.Fprintf(&builder, "Action: %s\n", firstNonEmpty(result.Title, state.Action.Label, result.ActionID))
	if result.Summary != "" {
		renderWrappedTextLine(&builder, "Summary: ", "         ", result.Summary, nil)
	}
	for _, field := range result.Fields {
		if field.Label == "" {
			continue
		}
		prefix := field.Label + ": "
		renderWrappedTextLine(&builder, prefix, strings.Repeat(" ", len(prefix)), firstNonEmpty(field.Value, "-"), nil)
	}
	if result.ErrorMessage != "" {
		renderWrappedTextLine(&builder, "Reason: ", "        ", result.ErrorMessage, nil)
	}
	if result.Status == ActionLifecycleReview {
		if result.Blocking {
			builder.WriteString("\nConfirmation is unavailable while blocking findings remain. Esc cancels.\n")
		} else {
			builder.WriteString("\nEnter continues to confirmation. Esc cancels.\n")
		}
	}
	if result.IdempotencyKey != "" && !resultHasField(result, "Idempotency") {
		fmt.Fprintf(&builder, "Idempotency: %s\n", result.IdempotencyKey)
	}
	if state.RawDetails && (len(result.RawCommand) > 0 || result.RawResponse != "" || result.ErrorCode != "" || result.CorrelationID != "") {
		renderSection(&builder, "Raw Details")
		if len(result.RawCommand) > 0 {
			fmt.Fprintf(&builder, "  Raw: %s\n", strings.Join(result.RawCommand, " "))
		}
		if result.CorrelationID != "" {
			fmt.Fprintf(&builder, "  correlation_id=%s\n", result.CorrelationID)
		}
		if result.ErrorCode != "" {
			fmt.Fprintf(&builder, "  error_code=%s\n", result.ErrorCode)
		}
		if result.RawResponse != "" {
			fmt.Fprintf(&builder, "  raw_response=%s\n", result.RawResponse)
		}
	}
	renderFooterFor(&builder, footerResult)
	return builder.String()
}

func RenderActionPreview(mode ui.Mode, action actions.Action) string {
	return RenderPortalActionPreview(mode, ActionPanelState{
		Lifecycle:  ActionLifecyclePreview,
		Action:     PortalActionFromRegistry(action),
		RawDetails: true,
	})
}

func RenderActionExecution(mode ui.Mode, execution ActionExecution) string {
	action := PortalActionFromRegistry(execution.Action)
	return RenderPortalActionResult(mode, ActionPanelState{
		Lifecycle:  ActionLifecycleSucceeded,
		Action:     action,
		RawDetails: true,
		Result: PortalActionResult{
			ActionID:       execution.Action.ID,
			Title:          execution.Action.Title,
			Status:         ActionLifecycleSucceeded,
			Summary:        "Worker run was requested.",
			IdempotencyKey: execution.IdempotencyKey,
			RawCommand:     append([]string{}, execution.Action.RawCommand...),
			Fields: []ActionResultField{
				{Label: "Worker", Value: firstNonEmpty(execution.WorkerKey, execution.Action.ExecutionTarget)},
				{Label: "Run", Value: firstNonEmpty(execution.WorkerRunID, "-")},
				{Label: "Idempotency", Value: execution.IdempotencyKey},
			},
		},
	})
}

func renderActionInputs(builder *strings.Builder, state ActionPanelState) {
	action := state.Action
	if len(action.InputFields) == 0 {
		return
	}
	renderSection(builder, "Inputs")
	editing := state.Lifecycle == ActionLifecyclePreview || state.Lifecycle == ActionLifecycleNeedsInput
	for idx, field := range action.ValidateInput() {
		value := action.FieldValue(field)
		displayValue := actionInputDisplayValue(field, value)
		marker := "  "
		if editing && idx == clampIndex(state.SelectedField, len(action.InputFields)) {
			marker = "> "
		}
		fmt.Fprintf(builder, "%s%s: %s\n", marker, firstNonEmpty(field.Label, field.Name), displayValue)
		if field.Kind == ActionFieldSelect && len(field.Options) > 0 {
			fmt.Fprintf(builder, "    options: %s\n", strings.Join(field.Options, " / "))
		}
		if field.Error != "" {
			fmt.Fprintf(builder, "    error: %s\n", field.Error)
		}
		if state.RawDetails && field.Help != "" {
			fmt.Fprintf(builder, "    %s\n", field.Help)
		}
	}
	if editing {
		builder.WriteString("  Use arrows to choose a field, type to edit, space toggles booleans/selects, Enter continues.\n")
	}
}

func actionInputDisplayValue(field PortalActionField, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		if field.Placeholder != "" {
			return "<" + field.Placeholder + ">"
		}
		return "-"
	}
	if field.Kind == ActionFieldSecret {
		return strings.Repeat("*", len(value))
	}
	return value
}

func renderActionRawDetails(builder *strings.Builder, action PortalAction, visible bool) {
	if !visible {
		return
	}
	renderSection(builder, "Raw Details")
	if len(action.RawCommand) > 0 {
		fmt.Fprintf(builder, "  Raw: %s\n", action.RawCommandString())
	}
	fmt.Fprintf(builder, "  action_id=%s\n", action.ID)
	fmt.Fprintf(builder, "  executor=%s target=%s\n", action.Executor.Kind, action.Executor.Target)
	if len(action.RawDetails) > 0 {
		keys := make([]string, 0, len(action.RawDetails))
		for key := range action.RawDetails {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value := strings.TrimSpace(action.RawDetails[key])
			if value == "" {
				continue
			}
			fmt.Fprintf(builder, "  %s=%s\n", key, value)
		}
	}
}

func resultHasField(result PortalActionResult, label string) bool {
	for _, field := range result.Fields {
		if strings.EqualFold(field.Label, label) {
			return true
		}
	}
	return false
}
