package localclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"loom.local/loom/internal/correlation"
	"loom.local/loom/internal/idempotency"
	"loom.local/loom/internal/provenance"
	"loom.local/loom/internal/response"
)

var ErrProvenanceResponseTooLarge = errors.New("provenance response exceeds the local-client bound")

func (c Client) ProvenanceHealth(ctx context.Context, correlationID string) (response.Envelope[provenance.RuntimeReadiness], error) {
	return doProvenanceJSON[provenance.RuntimeReadiness](c, ctx, http.MethodGet, "/v1/provenance/health", correlationID, nil, false)
}

func (c Client) SearchProvenance(ctx context.Context, correlationID string, request provenance.SearchRequest) (response.Envelope[provenance.SearchResponse], error) {
	return doProvenanceJSON[provenance.SearchResponse](c, ctx, http.MethodPost, "/v1/provenance/search", correlationID, request, false)
}

func (c Client) ListProvenanceRepositories(ctx context.Context, correlationID string, request provenance.RepositoryProjectionListRequest) (response.Envelope[provenance.RepositoryProjectionList], error) {
	values := url.Values{}
	if request.Query != "" {
		values.Set("query", request.Query)
	}
	if request.Project != "" {
		values.Set("project", request.Project)
	}
	if request.Topic != "" {
		values.Set("topic", request.Topic)
	}
	if request.Role != "" {
		values.Set("role", request.Role)
	}
	if request.TrackingStatus != "" {
		values.Set("tracking_status", string(request.TrackingStatus))
	}
	if request.Limit > 0 {
		values.Set("limit", strconv.Itoa(request.Limit))
	}
	path := "/v1/provenance/repos"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doProvenanceJSON[provenance.RepositoryProjectionList](c, ctx, http.MethodGet, path, correlationID, nil, false)
}

func (c Client) GetProvenanceRepository(ctx context.Context, correlationID, repositoryID string) (response.Envelope[provenance.RepositoryCard], error) {
	path := "/v1/provenance/repos/" + url.PathEscape(strings.TrimSpace(repositoryID))
	return doProvenanceJSON[provenance.RepositoryCard](c, ctx, http.MethodGet, path, correlationID, nil, false)
}

func (c Client) SyncProvenanceProject(ctx context.Context, correlationID, projectRef string) (response.Envelope[provenance.AuthorizedProjectProjectionSync], error) {
	path := "/v1/provenance/projects/" + url.PathEscape(strings.TrimSpace(projectRef)) + "/sync"
	return doProvenanceJSON[provenance.AuthorizedProjectProjectionSync](c, ctx, http.MethodPost, path, correlationID, map[string]any{}, true)
}

func (c Client) GetProvenanceProject(ctx context.Context, correlationID, projectID string, snapshotID provenance.SemanticID) (response.Envelope[provenance.ProjectProjectionSnapshot], error) {
	path := "/v1/provenance/projects/" + url.PathEscape(strings.TrimSpace(projectID))
	if snapshotID != "" {
		path += "?" + url.Values{"snapshot_id": {string(snapshotID)}}.Encode()
	}
	return doProvenanceJSON[provenance.ProjectProjectionSnapshot](c, ctx, http.MethodGet, path, correlationID, nil, false)
}

func (c Client) ListProvenanceCandidates(ctx context.Context, correlationID string, request provenance.PageRequest) (response.Envelope[provenance.Page[provenance.CandidateSummary]], error) {
	return doProvenanceJSON[provenance.Page[provenance.CandidateSummary]](c, ctx, http.MethodGet, provenancePagePath("/v1/provenance/candidates", request), correlationID, nil, false)
}

func (c Client) GetProvenanceCandidate(ctx context.Context, correlationID string, id provenance.SemanticID, limit int) (response.Envelope[provenance.CandidateLifecycleProjection], error) {
	return doProvenanceJSON[provenance.CandidateLifecycleProjection](c, ctx, http.MethodGet, provenanceExactPath("/v1/provenance/candidates/", id, limit), correlationID, nil, false)
}

func (c Client) ListProvenanceRecords(ctx context.Context, correlationID string, request provenance.PageRequest) (response.Envelope[provenance.Page[provenance.RecordSummary]], error) {
	return doProvenanceJSON[provenance.Page[provenance.RecordSummary]](c, ctx, http.MethodGet, provenancePagePath("/v1/provenance/records", request), correlationID, nil, false)
}

func (c Client) GetProvenanceRecord(ctx context.Context, correlationID string, id provenance.SemanticID, limit int) (response.Envelope[provenance.RecordLifecycleProjection], error) {
	return doProvenanceJSON[provenance.RecordLifecycleProjection](c, ctx, http.MethodGet, provenanceExactPath("/v1/provenance/records/", id, limit), correlationID, nil, false)
}

func (c Client) ListProvenanceRelationships(ctx context.Context, correlationID string, request provenance.PageRequest) (response.Envelope[provenance.Page[provenance.RelationshipSummary]], error) {
	return doProvenanceJSON[provenance.Page[provenance.RelationshipSummary]](c, ctx, http.MethodGet, provenancePagePath("/v1/provenance/relationships", request), correlationID, nil, false)
}

func (c Client) GetProvenanceRelationship(ctx context.Context, correlationID string, id provenance.SemanticID, limit int) (response.Envelope[provenance.RelationshipLifecycleProjection], error) {
	return doProvenanceJSON[provenance.RelationshipLifecycleProjection](c, ctx, http.MethodGet, provenanceExactPath("/v1/provenance/relationships/", id, limit), correlationID, nil, false)
}

func (c Client) ListProvenanceCases(ctx context.Context, correlationID string, request provenance.PageRequest) (response.Envelope[provenance.Page[provenance.ResolutionCaseSummary]], error) {
	return doProvenanceJSON[provenance.Page[provenance.ResolutionCaseSummary]](c, ctx, http.MethodGet, provenancePagePath("/v1/provenance/cases", request), correlationID, nil, false)
}

func (c Client) GetProvenanceCase(ctx context.Context, correlationID string, id provenance.SemanticID, limit int) (response.Envelope[provenance.ResolutionCaseLifecycleProjection], error) {
	return doProvenanceJSON[provenance.ResolutionCaseLifecycleProjection](c, ctx, http.MethodGet, provenanceExactPath("/v1/provenance/cases/", id, limit), correlationID, nil, false)
}

func (c Client) RegisterProvenanceCandidates(ctx context.Context, correlationID string, input provenance.CandidateRegistrationRequest) (response.Envelope[provenance.AuthorizedCandidateRegistration], error) {
	return doProvenanceJSON[provenance.AuthorizedCandidateRegistration](c, ctx, http.MethodPost, "/v1/provenance/candidates", correlationID, input, true)
}

func (c Client) ApplyProvenanceOperations(ctx context.Context, correlationID string, input provenance.ManualOperationsRequest) (response.Envelope[provenance.AuthorizedManualOperations], error) {
	return doProvenanceJSON[provenance.AuthorizedManualOperations](c, ctx, http.MethodPost, "/v1/provenance/operations", correlationID, input, true)
}

func provenancePagePath(base string, request provenance.PageRequest) string {
	values := url.Values{}
	if request.Limit > 0 {
		values.Set("limit", strconv.Itoa(request.Limit))
	}
	if request.AfterTime != nil {
		values.Set("after_time", request.AfterTime.UTC().Format(time.RFC3339Nano))
	}
	if request.AfterID != nil {
		values.Set("after_id", string(*request.AfterID))
	}
	if request.Domain != "" {
		values.Set("domain", request.Domain)
	}
	if request.Visibility != "" {
		values.Set("visibility", request.Visibility)
	}
	if encoded := values.Encode(); encoded != "" {
		return base + "?" + encoded
	}
	return base
}

func provenanceExactPath(base string, id provenance.SemanticID, limit int) string {
	path := base + url.PathEscape(string(id))
	if limit > 0 {
		path += "?limit=" + strconv.Itoa(limit)
	}
	return path
}

func doProvenanceJSON[T any](c Client, parent context.Context, method, path, correlationID string, body any, mutation bool) (response.Envelope[T], error) {
	if c.client == nil {
		return response.Envelope[T]{}, errors.New("local client transport is not configured")
	}
	if mutation && strings.TrimSpace(c.idempotencyKey) == "" {
		return response.Envelope[T]{}, errors.New("provenance mutation requires an idempotency key")
	}
	ctx, cancel := context.WithTimeout(parent, provenance.FoundationRequestTimeout)
	defer cancel()

	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return response.Envelope[T]{}, err
		}
		if len(payload) > provenance.MaximumFoundationRequestBytes {
			return response.Envelope[T]{}, fmt.Errorf("provenance request exceeds %d bytes", provenance.MaximumFoundationRequestBytes)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.requestURL(path), bytes.NewReader(payload))
	if err != nil {
		return response.Envelope[T]{}, err
	}
	req.Header.Set(correlation.Header, correlation.Normalize(correlationID))
	if mutation {
		req.Header.Set(idempotency.Header, strings.TrimSpace(c.idempotencyKey))
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	result, err := c.client.Do(req)
	if err != nil {
		return response.Envelope[T]{}, err
	}
	defer result.Body.Close()
	bounded, err := io.ReadAll(io.LimitReader(result.Body, provenance.MaximumFoundationResponseBytes+1))
	if err != nil {
		return response.Envelope[T]{}, err
	}
	if len(bounded) > provenance.MaximumFoundationResponseBytes {
		return response.Envelope[T]{}, ErrProvenanceResponseTooLarge
	}
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		var envelope response.ErrorEnvelope
		if err := json.Unmarshal(bounded, &envelope); err == nil && !envelope.OK && envelope.Error.Code != "" {
			return response.Envelope[T]{}, &RequestError{Method: method, Path: path, StatusCode: result.StatusCode, Envelope: envelope}
		}
		return response.Envelope[T]{}, fmt.Errorf("loomd request %s %s failed with status %d", method, path, result.StatusCode)
	}
	var envelope response.Envelope[T]
	if err := json.Unmarshal(bounded, &envelope); err != nil {
		return response.Envelope[T]{}, err
	}
	return envelope, nil
}

func (c Client) GetProvenanceCandidateWithSources(ctx context.Context, correlationID string, id provenance.SemanticID, limit int) (response.Envelope[provenance.CandidateWithSources], error) {
	result, err := doProvenanceJSON[provenance.CandidateWithSources](c, ctx, http.MethodGet, provenanceSourcesPath("/v1/provenance/candidates/", id, limit), correlationID, nil, false)
	if err == nil {
		err = validateProvenanceSources(result.Data.Sources, "candidate", id)
	}
	return result, err
}

func (c Client) GetProvenanceRecordWithSources(ctx context.Context, correlationID string, id provenance.SemanticID, limit int) (response.Envelope[provenance.RecordWithSources], error) {
	result, err := doProvenanceJSON[provenance.RecordWithSources](c, ctx, http.MethodGet, provenanceSourcesPath("/v1/provenance/records/", id, limit), correlationID, nil, false)
	if err == nil {
		err = validateProvenanceSources(result.Data.Sources, "record", id)
	}
	return result, err
}

func provenanceSourcesPath(base string, id provenance.SemanticID, limit int) string {
	path := provenanceExactPath(base, id, limit)
	if strings.Contains(path, "?") {
		return path + "&sources=true"
	}
	return path + "?sources=true"
}

// Refuse an older server that silently ignores the opt-in query.
func validateProvenanceSources(sources provenance.LinkedSources, kind string, id provenance.SemanticID) error {
	if sources.SchemaVersion != provenance.LinkedSourcesSchemaVersion || sources.ParentKind != kind || sources.ParentID != id || sources.Posture != "stored_resolution" || sources.Items == nil || sources.Returned != len(sources.Items) {
		return errors.New("provenance linked source expansion is missing or invalid")
	}
	return nil
}
