package knowledge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type OllamaVisionRuntime struct {
	Endpoint string
	Model    string
	Client   *http.Client
}

func (runtime OllamaVisionRuntime) Describe(ctx context.Context, request VisionRequest) (VisionResult, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(runtime.Endpoint), "/")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:11434"
	}
	model := strings.TrimSpace(runtime.Model)
	if model == "" {
		return VisionResult{}, fmt.Errorf("%w: vision model is not configured", ErrInvalid)
	}
	payload, _ := json.Marshal(map[string]any{"model": model, "prompt": request.Prompt, "images": []string{base64.StdEncoding.EncodeToString(request.Image)}, "stream": false})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/api/generate", bytes.NewReader(payload))
	if err != nil {
		return VisionResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := runtime.Client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(req)
	if err != nil {
		return VisionResult{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return VisionResult{}, err
	}
	if response.StatusCode/100 != 2 {
		return VisionResult{}, fmt.Errorf("vision runtime returned status %d", response.StatusCode)
	}
	var decoded struct {
		Response string `json:"response"`
		Model    string `json:"model"`
	}
	if err = json.Unmarshal(body, &decoded); err != nil {
		return VisionResult{}, err
	}
	description := normalizeArtifactText(decoded.Response)
	if description == "" {
		return VisionResult{}, fmt.Errorf("%w: vision runtime returned empty description", ErrInvalid)
	}
	if len(description) > 4000 {
		description = description[:4000]
	}
	return VisionResult{Description: description, RuntimeKey: "ollama", RuntimeVersion: "ollama.generate.v1", Model: firstNonEmpty(decoded.Model, model)}, nil
}
