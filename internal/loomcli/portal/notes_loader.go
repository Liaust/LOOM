package portal

import (
	"context"
	"encoding/json"
	"fmt"

	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/notesworkspacesync"
	"loom.local/loom/internal/response"
)

type notesPipelineStatusClient interface {
	GetKnowledgeNotesPipelineStatus(context.Context, string) (response.Envelope[knowledge.PipelineOverallStatus], error)
}

func loadNotesScreen(ctx context.Context, client Client, correlationID string, fallback Snapshot) ScreenLoadResult {
	builder := newScreenLoadBuilder(ScreenNotes, fallback)
	data := NotesData{}
	envelope, err := client.GetKnowledgeNotesOverview(ctx, correlationID, knowledge.NotesOverviewInput{IncludeInactive: true, SourceLifecycle: knowledge.SourceLifecycleFilterActive})
	if err != nil {
		builder.addPartial("notes_overview", err)
	} else {
		data.Overview = envelope.Data
	}
	if pipelineClient, ok := client.(notesPipelineStatusClient); ok {
		pipelinesEnvelope, err := pipelineClient.GetKnowledgeNotesPipelineStatus(ctx, correlationID)
		if err != nil {
			builder.addPartial("notes_pipelines", err)
		} else {
			data.Pipelines = pipelinesEnvelope.Data
			data.PipelinesAvailable = true
			if progress := data.Pipelines.Progress; progress != nil {
				for _, failure := range progress.PartialErrors {
					builder.addPartial("notes_pipelines", fmt.Errorf("%s", failure))
				}
				if len(data.Pipelines.NativePublication) > 0 {
					var native notesworkspacesync.WorkspaceProgress
					if err := json.Unmarshal(data.Pipelines.NativePublication, &native); err != nil {
						builder.addPartial("notes_native_publication", fmt.Errorf("invalid native publication receipt"))
					} else {
						for _, failure := range native.PartialErrors {
							builder.addPartial("notes_native_publication", fmt.Errorf("%s", failure))
						}
					}
				}
			}
		}
	}
	// The unified progress receipt already carries embedding stage observations.
	// Legacy vector totals scan history; retain that reader only for older servers.
	if data.Pipelines.Progress != nil {
		builder.data.Notes = data
		return builder.result()
	}
	embeddingsEnvelope, err := client.GetKnowledgeNotesEmbeddingStatus(ctx, correlationID)
	if err != nil {
		builder.addPartial("notes_embeddings", err)
	} else {
		data.Embeddings = embeddingsEnvelope.Data
		data.EmbeddingsAvailable = true
	}
	builder.data.Notes = data
	return builder.result()
}
