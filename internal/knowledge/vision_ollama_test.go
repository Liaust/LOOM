package knowledge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaVisionRuntimeNormalizesDescription(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"response":"  a factual image  ","model":"vision-test"}`))
	}))
	defer server.Close()
	result, err := (OllamaVisionRuntime{Endpoint: server.URL, Model: "vision-test", Client: server.Client()}).Describe(context.Background(), VisionRequest{Image: []byte("bounded"), Prompt: ImageDescriptionPrompt()})
	if err != nil {
		t.Fatal(err)
	}
	if result.Description != "a factual image" || result.Model != "vision-test" {
		t.Fatalf("result=%#v", result)
	}
}
