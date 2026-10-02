package localclient

import (
	"context"
	"loom.local/loom/internal/notesworkspacesync"
	"loom.local/loom/internal/response"
	"net/http"
	"net/url"
)

func (c Client) ListNotesConflicts(ctx context.Context, correlation, after string) (response.Envelope[notesworkspacesync.ConflictPage], error) {
	path := "/v1/knowledge/notes/conflicts"
	if after != "" {
		path += "?" + url.Values{"after": {after}}.Encode()
	}
	return doJSON[notesworkspacesync.ConflictPage](c, ctx, http.MethodGet, path, correlation, nil)
}
func (c Client) ShowNotesConflict(ctx context.Context, correlation, id string) (response.Envelope[notesworkspacesync.ConflictView], error) {
	return doJSON[notesworkspacesync.ConflictView](c, ctx, http.MethodGet, "/v1/knowledge/notes/conflicts?"+url.Values{"id": {id}}.Encode(), correlation, nil)
}
func (c Client) ResolveNotesConflict(ctx context.Context, correlation string, input notesworkspacesync.ResolveConflictInput) (response.Envelope[notesworkspacesync.ResolutionResult], error) {
	if err := input.Validate(); err != nil {
		return response.Envelope[notesworkspacesync.ResolutionResult]{}, err
	}
	return doJSON[notesworkspacesync.ResolutionResult](c, ctx, http.MethodPost, "/v1/knowledge/notes/conflicts", correlation, input)
}
