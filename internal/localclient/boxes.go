package localclient

import (
	"context"
	"net/http"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/response"
)

func (c Client) Boxes(ctx context.Context, correlationID string) (response.Envelope[boxregistry.Catalog], error) {
	return doJSON[boxregistry.Catalog](c, ctx, http.MethodGet, "/v1/boxes", correlationID, nil)
}
