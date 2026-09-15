package localclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"loom.local/loom/internal/correlation"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/response"
)

// Keep published source identity/files visible even when connection failed.
func scaffoldJSON(c Client, ctx context.Context, cid string, input pc.ScaffoldOptions) (response.Envelope[pc.ScaffoldResult], error) {
	var result response.Envelope[pc.ScaffoldResult]
	raw, err := json.Marshal(input)
	if err != nil {
		return result, err
	}
	const path = "/v1/project-scaffolds"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.requestURL(path), bytes.NewReader(raw))
	if err != nil {
		return result, err
	}
	req.Header.Set(correlation.Header, correlation.Normalize(cid))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	const limit = 2 << 20
	raw, err = io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(raw) > limit {
		return result, fmt.Errorf("project create response unreadable or oversized")
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && result.OK {
		return result, nil
	}
	var failure response.ErrorEnvelope
	if json.Unmarshal(raw, &failure) != nil || failure.Error.Code == "" {
		return result, fmt.Errorf("project create failed with status %d", resp.StatusCode)
	}
	return result, &RequestError{Method: http.MethodPost, Path: path, StatusCode: resp.StatusCode, Envelope: failure}
}
