package localclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"loom.local/loom/internal/correlation"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/response"
)

// DeclarationRequestError retains the standard transport metadata and the
// durable operation result. Errors.As also exposes the existing RequestError.
type DeclarationRequestError struct {
	*RequestError
	Result *pc.DeclarationResult
	Detail pc.DeclarationError
}

func (e *DeclarationRequestError) Unwrap() error { return e.RequestError }

func (c Client) PlanProjectDeclaration(ctx context.Context, cid string, input pc.DeclarationPlanRequest) (response.Envelope[pc.DeclarationPlan], error) {
	return declarationJSON[pc.DeclarationPlan](c, ctx, http.MethodPost, "/v1/project-declaration-plans", cid, input)
}
func (c Client) ApplyProjectDeclaration(ctx context.Context, cid string, input pc.DeclarationApplyRequest) (response.Envelope[pc.DeclarationResult], error) {
	return declarationJSON[pc.DeclarationResult](c, ctx, http.MethodPost, "/v1/project-declaration-operations", cid, input)
}
func (c Client) GetProjectDeclarationOperation(ctx context.Context, cid, operation string) (response.Envelope[pc.DeclarationResult], error) {
	return declarationJSON[pc.DeclarationResult](c, ctx, http.MethodGet, "/v1/project-declaration-operations/"+url.PathEscape(operation), cid, nil)
}
func (c Client) GetProjectDeclarationStatus(ctx context.Context, cid, project, node string) (response.Envelope[pc.DeclarationStatus], error) {
	path := "/v1/projects/" + url.PathEscape(project) + "/declaration-status"
	if node != "" {
		path += "?" + url.Values{"node_ref": {node}}.Encode()
	}
	return declarationJSON[pc.DeclarationStatus](c, ctx, http.MethodGet, path, cid, nil)
}

func (c Client) GetProjectConversionAssessment(ctx context.Context, cid, project, node string) (response.Envelope[pc.DeclarationMigrationAssessment], error) {
	query := url.Values{"conversion_preview": {"true"}}
	if node != "" {
		query.Set("node_ref", node)
	}
	return declarationJSON[pc.DeclarationMigrationAssessment](c, ctx, http.MethodGet, "/v1/projects/"+url.PathEscape(project)+"/declaration-status?"+query.Encode(), cid, nil)
}

// This bounded, declaration-specific decoder does not change the legacy wire
// envelope. In particular non-2xx results must survive alongside their error.
func declarationJSON[T any](c Client, ctx context.Context, method, path, cid string, input any) (response.Envelope[T], error) {
	var envelope response.Envelope[T]
	var raw []byte
	var err error
	if input != nil {
		raw, err = json.Marshal(input)
		if err != nil {
			return envelope, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.requestURL(path), bytes.NewReader(raw))
	if err != nil {
		return envelope, err
	}
	req.Header.Set(correlation.Header, correlation.Normalize(cid))
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return envelope, err
	}
	defer resp.Body.Close()
	const limit = 8 << 20
	raw, err = io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(raw) > limit {
		return envelope, fmt.Errorf("declaration response unreadable or oversized")
	}
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return envelope, fmt.Errorf("declaration response is not a valid envelope")
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && envelope.OK {
		if _, assessment := any(envelope.Data).(pc.DeclarationMigrationAssessment); assessment {
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&envelope) != nil {
				return response.Envelope[T]{}, declarationProtocolError(method, path, resp.StatusCode, envelope.Meta)
			}
		}
		if !validDeclarationResponse(envelope.Data) {
			return response.Envelope[T]{}, declarationProtocolError(method, path, resp.StatusCode, envelope.Meta)
		}
		return envelope, nil
	}
	var failure struct {
		response.ErrorEnvelope
		Data             *pc.DeclarationResult `json:"data"`
		DeclarationError pc.DeclarationError   `json:"declaration_error"`
	}
	if json.Unmarshal(raw, &failure) != nil || failure.OK || failure.Error.Code == "" {
		return envelope, fmt.Errorf("declaration request failed with status %d", resp.StatusCode)
	}
	if failure.Data != nil && !validDeclarationResponse(*failure.Data) {
		return response.Envelope[T]{}, declarationProtocolError(method, path, resp.StatusCode, envelope.Meta)
	}
	return envelope, &DeclarationRequestError{RequestError: &RequestError{Method: method, Path: path, StatusCode: resp.StatusCode, Envelope: failure.ErrorEnvelope}, Result: failure.Data, Detail: failure.DeclarationError}
}

func validDeclarationResponse(value any) bool {
	switch v := value.(type) {
	case pc.DeclarationMigrationAssessment:
		return pc.ValidDeclarationMigrationAssessment(v)
	case pc.DeclarationPlan:
		return v.SchemaVersion == pc.DeclarationPlanSchemaV05
	case pc.DeclarationResult:
		return v.SchemaVersion == pc.DeclarationResultSchemaV05
	case pc.DeclarationStatus:
		return v.SchemaVersion == pc.DeclarationStatusSchemaV05
	}
	return false
}
func declarationProtocolError(method, path string, status int, meta response.Meta) error {
	return &DeclarationRequestError{RequestError: &RequestError{Method: method, Path: path, StatusCode: status, Envelope: response.ErrorEnvelope{Error: response.ErrorBody{Code: string(pc.DeclarationUnsupported), Domain: "projects", Target: "declaration", Summary: "response_schema_unsupported", CorrelationID: meta.CorrelationID}, Meta: meta}}, Detail: pc.DeclarationError{Code: pc.DeclarationUnsupported, CauseCode: "response_schema_unsupported", Message: "The endpoint did not return the declaration response schema.", CompletedEffects: []string{}}}
}
