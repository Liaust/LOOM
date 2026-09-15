package knowledge

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
)

const (
	EmbeddingQueryPromptPrefix = "Represent this sentence for searching relevant passages:"
)

type EmbeddingRuntime interface {
	Embed(context.Context, EmbeddingRuntimeRequest) (EmbeddingRuntimeResponse, error)
	Health(context.Context) EmbeddingRuntimeHealth
}

type EmbeddingRuntimeRequest struct {
	Model    string
	Inputs   []string
	Truncate bool
}

type EmbeddingRuntimeResponse struct {
	Model      string
	Embeddings [][]float32
	Dimensions int
}

type EmbeddingRuntimeHealth struct {
	Available  bool   `json:"available"`
	RuntimeKey string `json:"runtime_key"`
	ModelKey   string `json:"model_key,omitempty"`
	Endpoint   string `json:"endpoint,omitempty"`
	Status     string `json:"status"`
	ErrorKind  string `json:"error_kind,omitempty"`
	Error      string `json:"error,omitempty"`
}

type EmbeddingRuntimeErrorKind string

const (
	EmbeddingRuntimeErrorUnavailable     EmbeddingRuntimeErrorKind = "unavailable"
	EmbeddingRuntimeErrorTimeout         EmbeddingRuntimeErrorKind = "timeout"
	EmbeddingRuntimeErrorBadStatus       EmbeddingRuntimeErrorKind = "bad_status"
	EmbeddingRuntimeErrorInvalidRequest  EmbeddingRuntimeErrorKind = "invalid_request"
	EmbeddingRuntimeErrorInvalidResponse EmbeddingRuntimeErrorKind = "invalid_response"
)

type EmbeddingRuntimeError struct {
	Kind       EmbeddingRuntimeErrorKind
	StatusCode int
	Message    string
	Err        error
}

func (e *EmbeddingRuntimeError) Error() string {
	if e == nil {
		return ""
	}
	message := strings.TrimSpace(e.Message)
	if message == "" {
		message = string(e.Kind)
	}
	if e.StatusCode > 0 {
		message = fmt.Sprintf("%s: status %d", message, e.StatusCode)
	}
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", message, e.Err)
	}
	return message
}

func (e *EmbeddingRuntimeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func IsEmbeddingRuntimeTimeout(err error) bool {
	var runtimeErr *EmbeddingRuntimeError
	return errors.As(err, &runtimeErr) && runtimeErr.Kind == EmbeddingRuntimeErrorTimeout
}

func IsEmbeddingRuntimeUnavailable(err error) bool {
	var runtimeErr *EmbeddingRuntimeError
	return errors.As(err, &runtimeErr) && runtimeErr.Kind == EmbeddingRuntimeErrorUnavailable
}

func classifyEmbeddingHTTPError(err error) EmbeddingRuntimeErrorKind {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return EmbeddingRuntimeErrorTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return EmbeddingRuntimeErrorTimeout
	}
	return EmbeddingRuntimeErrorUnavailable
}

func BuildEmbeddingQueryInput(query string) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", fmt.Errorf("%w: embedding query is required", ErrInvalid)
	}
	return EmbeddingQueryPromptPrefix + " " + query, nil
}

func normalizeEmbeddingRuntimeRequest(request EmbeddingRuntimeRequest) (EmbeddingRuntimeRequest, error) {
	request.Model = strings.TrimSpace(request.Model)
	if request.Model == "" {
		request.Model = EmbeddingModelMXBAIEmbedLarge
	}
	if len(request.Inputs) == 0 {
		return EmbeddingRuntimeRequest{}, fmt.Errorf("%w: embedding inputs are required", ErrInvalid)
	}
	normalized := make([]string, 0, len(request.Inputs))
	for _, input := range request.Inputs {
		input = strings.TrimSpace(input)
		if input == "" {
			return EmbeddingRuntimeRequest{}, fmt.Errorf("%w: embedding inputs must not be empty", ErrInvalid)
		}
		normalized = append(normalized, input)
	}
	request.Inputs = normalized
	return request, nil
}

func embeddingHealthFromError(runtimeKey, modelKey, endpoint string, err error) EmbeddingRuntimeHealth {
	health := EmbeddingRuntimeHealth{
		Available:  false,
		RuntimeKey: runtimeKey,
		ModelKey:   modelKey,
		Endpoint:   endpoint,
		Status:     "unavailable",
		Error:      err.Error(),
	}
	var runtimeErr *EmbeddingRuntimeError
	if errors.As(err, &runtimeErr) {
		health.ErrorKind = string(runtimeErr.Kind)
		if runtimeErr.Kind == EmbeddingRuntimeErrorTimeout {
			health.Status = "timeout"
		}
	}
	return health
}

func defaultEmbeddingHTTPClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return http.DefaultClient
}
