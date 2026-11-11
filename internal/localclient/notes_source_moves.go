package localclient

import (
	"context"
	"loom.local/loom/internal/notesworkspacesync"
	"loom.local/loom/internal/response"
	"net/http"
)

func (c Client) NotesSourceMove(ctx context.Context, correlation string, input notesworkspacesync.SourceMoveInput) (response.Envelope[notesworkspacesync.SourceMoveView], error) {
	if err := input.Validate(); err != nil {
		return response.Envelope[notesworkspacesync.SourceMoveView]{}, err
	}
	return doJSON[notesworkspacesync.SourceMoveView](c, ctx, http.MethodPost, "/v1/knowledge/notes/moves", correlation, input)
}
