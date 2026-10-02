package localclient

import (
	"context"
	"net/http"
	"net/url"

	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/notesworkspacesync"
	"loom.local/loom/internal/response"
)

func (c Client) LocateKnowledgeNotesPassage(ctx context.Context, correlationID string, input knowledge.NotesPassageInput) (response.Envelope[notesworkspacesync.Navigation], error) {
	if err := knowledge.ValidateNotesPassageInput(input); err != nil {
		return response.Envelope[notesworkspacesync.Navigation]{}, err
	}
	query := url.Values{"object_id": {input.KnowledgeObjectID}, "version_id": {input.KnowledgeObjectVersionID}, "source_hash": {input.SourceHash}}
	if input.SourceLifecycle != "" {
		query.Set("source_lifecycle", string(input.SourceLifecycle))
	}
	return doJSON[notesworkspacesync.Navigation](c, ctx, http.MethodGet, "/v1/knowledge/notes/passages/"+url.PathEscape(input.KnowledgeChunkID)+"/workspace?"+query.Encode(), correlationID, nil)
}
