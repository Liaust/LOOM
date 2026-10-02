package localclient

import (
	"context"
	"net/http"

	"loom.local/loom/internal/hermesschedules"
	"loom.local/loom/internal/response"
)

func (c Client) HermesSchedules(ctx context.Context, correlationID string) (response.Envelope[hermesschedules.Observation], error) {
	return doJSON[hermesschedules.Observation](c, ctx, http.MethodGet, "/v1/schedules/hermes", correlationID, nil)
}
