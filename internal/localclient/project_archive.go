package localclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"

	"loom.local/loom/internal/correlation"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/storagearchive"
)

func (c Client) ReviewProjectPhysicalArchive(ctx context.Context, corr, ref, reason string) (response.Envelope[storagearchive.ProjectPhysicalPlanReview], error) {
	return projectArchiveJSON[storagearchive.ProjectPhysicalPlanReview](c, ctx, corr, ref, "plan", map[string]string{"reason": reason})
}
func (c Client) ReviewProjectPhysicalRestore(ctx context.Context, corr, ref, reason string) (response.Envelope[storagearchive.ProjectPhysicalPlanReview], error) {
	return projectArchiveJSON[storagearchive.ProjectPhysicalPlanReview](c, ctx, corr, ref, "restore/plan", map[string]string{"reason": reason})
}
func (c Client) ApplyProjectPhysicalArchive(ctx context.Context, corr, ref string, input storagearchive.ProjectPhysicalApplyRequest) (response.Envelope[storagearchive.ProjectPhysicalMutationSummary], error) {
	return projectArchiveJSON[storagearchive.ProjectPhysicalMutationSummary](c, ctx, corr, ref, "apply", input)
}
func (c Client) ApplyProjectPhysicalRestore(ctx context.Context, corr, ref string, input storagearchive.ProjectPhysicalApplyRequest) (response.Envelope[storagearchive.ProjectPhysicalMutationSummary], error) {
	return projectArchiveJSON[storagearchive.ProjectPhysicalMutationSummary](c, ctx, corr, ref, "restore/apply", input)
}
func (c Client) RecoverProjectPhysicalArchive(ctx context.Context, corr, ref string, input storagearchive.ProjectPhysicalRecoverRequest) (response.Envelope[storagearchive.ProjectPhysicalMutationSummary], error) {
	return projectArchiveJSON[storagearchive.ProjectPhysicalMutationSummary](c, ctx, corr, ref, "recover", input)
}
func (c Client) RecoverProjectPhysicalRestore(ctx context.Context, corr, ref string, input storagearchive.ProjectPhysicalRecoverRequest) (response.Envelope[storagearchive.ProjectPhysicalMutationSummary], error) {
	return projectArchiveJSON[storagearchive.ProjectPhysicalMutationSummary](c, ctx, corr, ref, "restore/recover", input)
}

// Unlike generic doJSON, return bounded partial data alongside a typed failure.
// An unreadable response is uncertainty, not evidence that nothing moved.
func projectArchiveJSON[T any](c Client, ctx context.Context, corr, ref, action string, input any) (response.Envelope[T], error) {
	var envelope response.Envelope[T]
	payload, err := json.Marshal(input)
	if err != nil || len(payload) > storagearchive.MaximumWorkspaceSurfaceRequestBytes || ref == "" {
		return envelope, errors.New("invalid bounded project archive request")
	}
	path := "/v1/projects/" + url.PathEscape(ref) + "/archive/" + action
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.requestURL(path), bytes.NewReader(payload))
	if err != nil {
		return envelope, errors.New("project archive transport unavailable")
	}
	req.Header.Set(correlation.Header, correlation.Normalize(corr))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return envelope, errors.New("project archive outcome unavailable; inspect before retrying")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, storagearchive.MaximumWorkspaceSurfaceResponseBytes+1))
	if err != nil || len(data) > storagearchive.MaximumWorkspaceSurfaceResponseBytes || json.Unmarshal(data, &envelope) != nil {
		return response.Envelope[T]{}, errors.New("invalid bounded project archive response; inspect before retrying")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var failure response.ErrorEnvelope
		if json.Unmarshal(data, &failure) != nil || failure.OK || failure.Error.Code == "" {
			return response.Envelope[T]{}, errors.New("project archive outcome unavailable; inspect before retrying")
		}
		envelope.OK = false
		return envelope, &RequestError{Method: http.MethodPost, Path: path, StatusCode: resp.StatusCode, Envelope: failure}
	}
	var present struct {
		Data json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(data, &present)
	if !envelope.OK || len(present.Data) == 0 || bytes.Equal(present.Data, []byte("null")) {
		envelope.OK = false
		return envelope, errors.New("project archive response did not confirm success")
	}
	return envelope, nil
}
