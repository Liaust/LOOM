package response

import (
	"encoding/json"
	"net/http"
	"time"

	loomerrors "loom.local/loom/internal/errors"
)

type Meta struct {
	CorrelationID  string   `json:"correlation_id"`
	Source         string   `json:"source,omitempty"`
	Freshness      string   `json:"freshness,omitempty"`
	GeneratedAt    string   `json:"generated_at"`
	Redactions     []string `json:"redactions,omitempty"`
	IdempotencyKey string   `json:"idempotency_key,omitempty"`
}

type Envelope[T any] struct {
	OK   bool `json:"ok"`
	Data T    `json:"data"`
	Meta Meta `json:"meta"`
}

type ErrorBody struct {
	Code          string `json:"code"`
	Summary       string `json:"summary"`
	Domain        string `json:"domain,omitempty"`
	Target        string `json:"target,omitempty"`
	Hint          string `json:"hint,omitempty"`
	CorrelationID string `json:"correlation_id"`
}

type ErrorEnvelope struct {
	OK    bool      `json:"ok"`
	Error ErrorBody `json:"error"`
	Meta  Meta      `json:"meta"`
}

func NewMeta(correlationID string) Meta {
	return Meta{
		CorrelationID: correlationID,
		Source:        "main-authoritative",
		Freshness:     "live",
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Redactions:    []string{},
	}
}

func Success[T any](correlationID string, data T) Envelope[T] {
	return Envelope[T]{
		OK:   true,
		Data: data,
		Meta: NewMeta(correlationID),
	}
}

func SuccessWithIdempotency[T any](correlationID, idempotencyKey string, data T) Envelope[T] {
	meta := NewMeta(correlationID)
	meta.IdempotencyKey = idempotencyKey
	return Envelope[T]{
		OK:   true,
		Data: data,
		Meta: meta,
	}
}

func Failure(correlationID string, err error) ErrorEnvelope {
	body := ErrorBody{
		Code:          "runtime.error",
		Summary:       "The request failed.",
		Domain:        "runtime",
		CorrelationID: correlationID,
	}

	if loomErr, ok := err.(*loomerrors.Error); ok {
		body.Code = loomErr.Code
		body.Summary = loomErr.Summary
		body.Domain = loomErr.Domain
		body.Target = loomErr.Target
	}

	return ErrorEnvelope{
		OK:    false,
		Error: body,
		Meta:  NewMeta(correlationID),
	}
}

func FailureWithIdempotency(correlationID, idempotencyKey string, err error) ErrorEnvelope {
	envelope := Failure(correlationID, err)
	envelope.Meta.IdempotencyKey = idempotencyKey
	return envelope
}

func WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
