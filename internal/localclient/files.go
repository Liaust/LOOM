package localclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"loom.local/loom/internal/correlation"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/workspaceactions"
	"loom.local/loom/internal/workspacefiles"
	"loom.local/loom/internal/workspacenotes"
)

func (c Client) FileOperationReview(ctx context.Context, correlationID string, in workspaceactions.Input) (response.Envelope[workspaceactions.Review], error) {
	query := url.Values{"action": {in.Action}, "root": {in.Root}, "path": {in.Path}, "to_root": {in.ToRoot}, "to_path": {in.ToPath}}
	return doJSON[workspaceactions.Review](c, ctx, http.MethodGet, "/v1/files/operations?"+query.Encode(), correlationID, nil)
}

func (c Client) RetainedFileMove(ctx context.Context, correlationID string, input map[string]any) (response.Envelope[json.RawMessage], error) {
	if input["action"] == "status" {
		return doJSON[json.RawMessage](c, ctx, http.MethodGet, "/v1/files/retained-move?id="+url.QueryEscape(fmt.Sprint(input["id"])), correlationID, nil)
	}
	return doJSON[json.RawMessage](c, ctx, http.MethodPost, "/v1/files/retained-move", correlationID, input)
}

func (c Client) FileMetadataRefresh(ctx context.Context, correlationID, action, root, relative, id string) (response.Envelope[workspacenotes.Refresh], error) {
	if action == "status" || action == "latest" {
		query := url.Values{"action": {"refresh_" + action}, "root": {root}, "id": {id}}
		return doJSON[workspacenotes.Refresh](c, ctx, http.MethodGet, "/v1/files/links?"+query.Encode(), correlationID, nil)
	}
	return doJSON[workspacenotes.Refresh](c, ctx, http.MethodPost, "/v1/files/links", correlationID, map[string]string{"action": "refresh_" + action, "root": root, "path": relative, "id": id})
}

func (c Client) FileRoots(ctx context.Context, correlationID string) (response.Envelope[[]workspacefiles.Root], error) {
	return doJSON[[]workspacefiles.Root](c, ctx, http.MethodGet, "/v1/files/roots?box_context=v1", correlationID, nil)
}

func (c Client) ApplicationFileRoots(ctx context.Context, correlationID, project string, cursor, limit int) (response.Envelope[workspacefiles.ApplicationRootsPage], error) {
	q := url.Values{"project_id": {project}, "cursor": {strconv.Itoa(cursor)}, "limit": {strconv.Itoa(limit)}}
	return doJSON[workspacefiles.ApplicationRootsPage](c, ctx, http.MethodGet, "/v1/files/application-data?"+q.Encode(), correlationID, nil)
}

func (c Client) FileEntries(ctx context.Context, correlationID string, input workspacefiles.ListInput) (response.Envelope[workspacefiles.Page], error) {
	query := url.Values{"root": {input.Root}, "path": {input.Path}}
	if input.Limit != 0 {
		query.Set("limit", strconv.Itoa(input.Limit))
	}
	if input.Cursor != "" {
		query.Set("cursor", input.Cursor)
	}
	if input.ShowIgnored {
		query.Set("show_ignored", "true")
	}
	return doJSON[workspacefiles.Page](c, ctx, http.MethodGet, "/v1/files/entries?"+query.Encode(), correlationID, nil)
}

func (c Client) FileStat(ctx context.Context, correlationID, root, relative string) (response.Envelope[workspacefiles.Entry], error) {
	query := url.Values{"root": {root}, "path": {relative}}
	return doJSON[workspacefiles.Entry](c, ctx, http.MethodGet, "/v1/files/stat?"+query.Encode(), correlationID, nil)
}

func (c Client) FileArchiveMembers(ctx context.Context, correlationID, root, relative string) (response.Envelope[workspacefiles.ArchiveListing], error) {
	q := url.Values{"root": {root}, "path": {relative}}
	return doJSON[workspacefiles.ArchiveListing](c, ctx, http.MethodGet, "/v1/files/archive-members?"+q.Encode(), correlationID, nil)
}
func (c Client) ReadArchiveMember(ctx context.Context, correlationID, root, relative, member, revision string, destination io.Writer) error {
	q := url.Values{"root": {root}, "path": {relative}, "member": {member}, "revision": {revision}}
	endpoint := "/v1/files/archive-content?" + q.Encode()
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, c.requestURL(endpoint), nil)
	if e != nil {
		return e
	}
	c.applyBoxContext(req)
	req.Header.Set(correlation.Header, correlation.Normalize(correlationID))
	resp, e := c.client.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var envelope response.ErrorEnvelope
		json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&envelope)
		return &RequestError{Method: http.MethodGet, Path: endpoint, StatusCode: resp.StatusCode, Envelope: envelope}
	}
	_, e = io.Copy(destination, resp.Body)
	return e
}

func (c Client) FileExplain(ctx context.Context, correlationID, root, relative string) (response.Envelope[workspacefiles.Explanation], error) {
	query := url.Values{"root": {root}, "path": {relative}}
	return doJSON[workspacefiles.Explanation](c, ctx, http.MethodGet, "/v1/files/explain?"+query.Encode(), correlationID, nil)
}

// ReadFile streams bytes into a caller-owned writer, never a base64 JSON body.
// length=0 requests the whole file; a positive length requests a bounded range.
func (c Client) ReadFile(ctx context.Context, correlationID, root, relative string, offset, length int64, destination io.Writer) error {
	if offset < 0 || length < 0 {
		return fmt.Errorf("file offset and length must be non-negative")
	}
	query := url.Values{"root": {root}, "path": {relative}}
	path := "/v1/files/content?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.requestURL(path), nil)
	if err != nil {
		return err
	}
	c.applyBoxContext(req)
	req.Header.Set(correlation.Header, correlation.Normalize(correlationID))
	if offset > 0 || length > 0 {
		end := ""
		if length > 0 {
			if offset > (1<<63-1)-(length-1) {
				return fmt.Errorf("file range overflows")
			}
			end = strconv.FormatInt(offset+length-1, 10)
		}
		req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-"+end)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		var envelope response.ErrorEnvelope
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&envelope)
		return &RequestError{Method: http.MethodGet, Path: path, StatusCode: resp.StatusCode, Envelope: envelope}
	}
	if (offset > 0 || length > 0) && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("file server did not honor the requested range")
	}
	reader := io.Reader(resp.Body)
	if length > 0 {
		reader = io.LimitReader(reader, length)
	}
	_, err = io.Copy(destination, reader)
	return err
}
