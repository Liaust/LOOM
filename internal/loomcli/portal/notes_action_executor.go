package portal

import (
	"context"
	"fmt"
	"strconv"

	"loom.local/loom/internal/knowledge"
)

func (e ActionExecutor) executeNotesEmbeddingsToggle(ctx context.Context, action PortalAction) PortalActionResult {
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	enabled, err := strconv.ParseBool(action.Executor.Payload["enabled"])
	if err != nil {
		return failedActionResult(action, "portal.action_input_invalid", "Embedding enabled state is invalid.")
	}

	if enabled {
		envelope, err := e.Client.EnableKnowledgeNotesEmbeddings(ctx, e.CorrelationID)
		if err != nil {
			return failedActionResult(action, "portal.action_failed", err.Error())
		}
		return successActionResult(action, envelope.Meta.CorrelationID, "Notes embeddings enabled.", notesEmbeddingsToggleResultFields(envelope.Data))
	}

	envelope, err := e.Client.DisableKnowledgeNotesEmbeddings(ctx, e.CorrelationID)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	return successActionResult(action, envelope.Meta.CorrelationID, "Notes embeddings disabled.", notesEmbeddingsToggleResultFields(envelope.Data))
}

func notesEmbeddingsToggleResultFields(result knowledge.SetEmbeddingsEnabledResult) []ActionResultField {
	return []ActionResultField{
		{Label: "Enabled", Value: fmt.Sprintf("%t", result.Settings.Enabled)},
		{Label: "Runtime", Value: firstNonEmpty(result.Settings.RuntimeKey, "-")},
		{Label: "Model", Value: firstNonEmpty(result.Settings.ModelKey, "-")},
		{Label: "Queued", Value: fmt.Sprintf("%d", result.Queued)},
		{Label: "Active Vectors", Value: fmt.Sprintf("%d", result.Status.ActiveVectors)},
		{Label: "Processing", Value: fmt.Sprintf("%d", result.Status.Queue.Processing)},
		{Label: "Failed", Value: fmt.Sprintf("%d", result.Status.Queue.Failed+result.Status.Objects.Failed)},
	}
}
