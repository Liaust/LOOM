package httpapi

import (
	"net/http"
	"time"

	"loom.local/loom/internal/hermesschedules"
	"loom.local/loom/internal/response"
)

func (s Server) handleHermesSchedules(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "automation", "hermes", "Method is not allowed.", nil)
		return
	}
	if r.URL.RawQuery != "" {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_query", "automation", "hermes", "Hermes inventory uses the configured source and accepts no query parameters.", nil)
		return
	}
	observation := hermesschedules.Observation{
		Owner: "hermes", Availability: hermesschedules.Unavailable, AttemptedAt: time.Now().UTC(),
	}
	if s.services.HermesSchedules != nil {
		// Failures are represented by availability, never by an empty healthy list
		// or raw reader errors that could contain private paths or payloads.
		observation, _ = s.services.HermesSchedules.Observe(ctx)
	}
	envelope := response.Success(correlationID, observation)
	envelope.Meta.Source = "hermes-native-store"
	envelope.Meta.Freshness = string(observation.Availability)
	response.WriteJSON(w, http.StatusOK, envelope)
}
