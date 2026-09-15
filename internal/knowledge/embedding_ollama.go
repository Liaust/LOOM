package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type OllamaEmbeddingRuntime struct {
	endpoint string
	model    string
	client   *http.Client
}

type OllamaEmbeddingOptions struct {
	Endpoint   string
	Model      string
	HTTPClient *http.Client
}

type ollamaEmbedRequest struct {
	Model    string   `json:"model"`
	Input    []string `json:"input"`
	Truncate bool     `json:"truncate"`
}

type ollamaEmbedResponse struct {
	Model      string      `json:"model"`
	Embeddings [][]float64 `json:"embeddings"`
	Error      string      `json:"error,omitempty"`
}

func NewOllamaEmbeddingRuntime(options OllamaEmbeddingOptions) (*OllamaEmbeddingRuntime, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(options.Endpoint), "/")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:11434"
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid Ollama endpoint: %v", ErrInvalid, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("%w: Ollama endpoint must use http or https", ErrInvalid)
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return nil, fmt.Errorf("%w: Ollama endpoint host is required", ErrInvalid)
	}
	model := strings.TrimSpace(options.Model)
	if model == "" {
		model = EmbeddingModelMXBAIEmbedLarge
	}
	return &OllamaEmbeddingRuntime{
		endpoint: endpoint,
		model:    model,
		client:   defaultEmbeddingHTTPClient(options.HTTPClient),
	}, nil
}

func (r *OllamaEmbeddingRuntime) Embed(ctx context.Context, request EmbeddingRuntimeRequest) (EmbeddingRuntimeResponse, error) {
	if r == nil {
		return EmbeddingRuntimeResponse{}, &EmbeddingRuntimeError{Kind: EmbeddingRuntimeErrorUnavailable, Message: "Ollama embedding runtime is not configured"}
	}
	if strings.TrimSpace(request.Model) == "" {
		request.Model = r.model
	}
	normalized, err := normalizeEmbeddingRuntimeRequest(request)
	if err != nil {
		return EmbeddingRuntimeResponse{}, &EmbeddingRuntimeError{Kind: EmbeddingRuntimeErrorInvalidRequest, Message: "invalid embedding request", Err: err}
	}
	payload := ollamaEmbedRequest{
		Model:    normalized.Model,
		Input:    normalized.Inputs,
		Truncate: false,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return EmbeddingRuntimeResponse{}, &EmbeddingRuntimeError{Kind: EmbeddingRuntimeErrorInvalidRequest, Message: "encode Ollama embedding request", Err: err}
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return EmbeddingRuntimeResponse{}, &EmbeddingRuntimeError{Kind: EmbeddingRuntimeErrorInvalidRequest, Message: "build Ollama embedding request", Err: err}
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpResponse, err := r.client.Do(httpRequest)
	if err != nil {
		return EmbeddingRuntimeResponse{}, &EmbeddingRuntimeError{Kind: classifyEmbeddingHTTPError(err), Message: "call Ollama embedding runtime", Err: err}
	}
	defer httpResponse.Body.Close()

	responseBody, err := io.ReadAll(httpResponse.Body)
	if err != nil {
		return EmbeddingRuntimeResponse{}, &EmbeddingRuntimeError{Kind: EmbeddingRuntimeErrorInvalidResponse, Message: "read Ollama embedding response", Err: err}
	}
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode > 299 {
		kind := EmbeddingRuntimeErrorBadStatus
		if httpResponse.StatusCode == http.StatusServiceUnavailable || httpResponse.StatusCode == http.StatusNotFound {
			kind = EmbeddingRuntimeErrorUnavailable
		}
		return EmbeddingRuntimeResponse{}, &EmbeddingRuntimeError{
			Kind:       kind,
			StatusCode: httpResponse.StatusCode,
			Message:    strings.TrimSpace(string(responseBody)),
		}
	}

	var decoded ollamaEmbedResponse
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return EmbeddingRuntimeResponse{}, &EmbeddingRuntimeError{Kind: EmbeddingRuntimeErrorInvalidResponse, Message: "decode Ollama embedding response", Err: err}
	}
	if strings.TrimSpace(decoded.Error) != "" {
		return EmbeddingRuntimeResponse{}, &EmbeddingRuntimeError{Kind: EmbeddingRuntimeErrorInvalidResponse, Message: decoded.Error}
	}
	if len(decoded.Embeddings) != len(normalized.Inputs) {
		return EmbeddingRuntimeResponse{}, &EmbeddingRuntimeError{Kind: EmbeddingRuntimeErrorInvalidResponse, Message: "Ollama embedding count does not match input count"}
	}
	embeddings := make([][]float32, 0, len(decoded.Embeddings))
	dimensions := 0
	for _, vector := range decoded.Embeddings {
		if len(vector) == 0 {
			return EmbeddingRuntimeResponse{}, &EmbeddingRuntimeError{Kind: EmbeddingRuntimeErrorInvalidResponse, Message: "Ollama returned an empty embedding vector"}
		}
		if dimensions == 0 {
			dimensions = len(vector)
		}
		if len(vector) != dimensions {
			return EmbeddingRuntimeResponse{}, &EmbeddingRuntimeError{Kind: EmbeddingRuntimeErrorInvalidResponse, Message: "Ollama returned inconsistent embedding dimensions"}
		}
		converted := make([]float32, len(vector))
		for i, value := range vector {
			converted[i] = float32(value)
		}
		embeddings = append(embeddings, converted)
	}
	model := strings.TrimSpace(decoded.Model)
	if model == "" {
		model = normalized.Model
	}
	return EmbeddingRuntimeResponse{
		Model:      model,
		Embeddings: embeddings,
		Dimensions: dimensions,
	}, nil
}

func (r *OllamaEmbeddingRuntime) Health(ctx context.Context) EmbeddingRuntimeHealth {
	if r == nil {
		err := &EmbeddingRuntimeError{Kind: EmbeddingRuntimeErrorUnavailable, Message: "Ollama embedding runtime is not configured"}
		return embeddingHealthFromError(EmbeddingRuntimeOllama, "", "", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.endpoint+"/api/tags", nil)
	if err != nil {
		runtimeErr := &EmbeddingRuntimeError{Kind: EmbeddingRuntimeErrorInvalidRequest, Message: "build Ollama health request", Err: err}
		return embeddingHealthFromError(EmbeddingRuntimeOllama, r.model, r.endpoint, runtimeErr)
	}
	response, err := r.client.Do(request)
	if err != nil {
		runtimeErr := &EmbeddingRuntimeError{Kind: classifyEmbeddingHTTPError(err), Message: "call Ollama health endpoint", Err: err}
		return embeddingHealthFromError(EmbeddingRuntimeOllama, r.model, r.endpoint, runtimeErr)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		runtimeErr := &EmbeddingRuntimeError{
			Kind:       EmbeddingRuntimeErrorBadStatus,
			StatusCode: response.StatusCode,
			Message:    "Ollama health endpoint returned non-success status",
		}
		return embeddingHealthFromError(EmbeddingRuntimeOllama, r.model, r.endpoint, runtimeErr)
	}
	return EmbeddingRuntimeHealth{
		Available:  true,
		RuntimeKey: EmbeddingRuntimeOllama,
		ModelKey:   r.model,
		Endpoint:   r.endpoint,
		Status:     "available",
	}
}
