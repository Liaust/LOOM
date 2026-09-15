package localclient

import (
	"fmt"

	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/response"
)

type RequestError struct {
	Method     string
	Path       string
	StatusCode int
	Envelope   response.ErrorEnvelope
}

func (e *RequestError) Error() string {
	if e == nil {
		return ""
	}
	body := e.Envelope.Error
	if body.Code != "" || body.Summary != "" {
		return fmt.Sprintf("loomd request %s %s failed with status %d: %s: %s", e.Method, e.Path, e.StatusCode, body.Code, body.Summary)
	}
	return fmt.Sprintf("loomd request %s %s failed with status %d", e.Method, e.Path, e.StatusCode)
}

func (e *RequestError) LoomError() *loomerrors.Error {
	if e == nil {
		return loomerrors.New("transport.unavailable", "runtime", "loomd", "Could not reach local loomd.")
	}
	body := e.Envelope.Error
	if body.Code == "" {
		body.Code = "transport.unavailable"
	}
	if body.Domain == "" {
		body.Domain = "runtime"
	}
	if body.Summary == "" {
		body.Summary = "The daemon request failed."
	}
	return loomerrors.New(body.Code, body.Domain, body.Target, body.Summary)
}

func (e *RequestError) CorrelationID(fallback string) string {
	if e == nil {
		return fallback
	}
	if e.Envelope.Error.CorrelationID != "" {
		return e.Envelope.Error.CorrelationID
	}
	if e.Envelope.Meta.CorrelationID != "" {
		return e.Envelope.Meta.CorrelationID
	}
	return fallback
}
