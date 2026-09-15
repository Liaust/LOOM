package portal

import (
	"context"

	"loom.local/loom/internal/knowledge"
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
		}
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
