package knowledge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOllamaEmbedRequestShapeAndResponseParsing(t *testing.T) {
	var received ollamaEmbedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/embed" {
			t.Fatalf("path = %s, want /api/embed", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"mxbai-embed-large","embeddings":[[0.1,0.2],[0.3,0.4]]}`))
	}))
	defer server.Close()

	runtime, err := NewOllamaEmbeddingRuntime(OllamaEmbeddingOptions{
		Endpoint: server.URL,
		Model:    EmbeddingModelMXBAIEmbedLarge,
	})
	if err != nil {
		t.Fatalf("NewOllamaEmbeddingRuntime returned error: %v", err)
	}
	response, err := runtime.Embed(context.Background(), EmbeddingRuntimeRequest{
		Inputs: []string{"first passage", "second passage"},
	})
	if err != nil {
		t.Fatalf("Embed returned error: %v", err)
	}

	if received.Model != EmbeddingModelMXBAIEmbedLarge {
		t.Fatalf("request model = %q, want %q", received.Model, EmbeddingModelMXBAIEmbedLarge)
	}
	if received.Truncate {
		t.Fatal("request truncate = true, want false")
	}
	if len(received.Input) != 2 || received.Input[0] != "first passage" || received.Input[1] != "second passage" {
		t.Fatalf("request input = %#v, want raw passages", received.Input)
	}
	if response.Model != EmbeddingModelMXBAIEmbedLarge {
		t.Fatalf("response model = %q, want %q", response.Model, EmbeddingModelMXBAIEmbedLarge)
	}
	if response.Dimensions != 2 {
		t.Fatalf("Dimensions = %d, want 2", response.Dimensions)
	}
	if len(response.Embeddings) != 2 || response.Embeddings[0][0] != float32(0.1) {
		t.Fatalf("embeddings = %#v, want parsed float32 vectors", response.Embeddings)
	}
}

func TestOllamaEmbedRejectsBadResponseShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"embeddings":[[0.1],[0.2]]}`))
	}))
	defer server.Close()

	runtime, err := NewOllamaEmbeddingRuntime(OllamaEmbeddingOptions{Endpoint: server.URL})
	if err != nil {
		t.Fatalf("NewOllamaEmbeddingRuntime returned error: %v", err)
	}
	if _, err := runtime.Embed(context.Background(), EmbeddingRuntimeRequest{Inputs: []string{"one"}}); err == nil {
		t.Fatal("Embed accepted embedding count mismatch")
	}
}

func TestOllamaRuntimeUnavailableAndTimeoutClassification(t *testing.T) {
	unavailableServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model unavailable", http.StatusServiceUnavailable)
	}))
	defer unavailableServer.Close()
	unavailableRuntime, err := NewOllamaEmbeddingRuntime(OllamaEmbeddingOptions{Endpoint: unavailableServer.URL})
	if err != nil {
		t.Fatalf("NewOllamaEmbeddingRuntime returned error: %v", err)
	}
	if _, err := unavailableRuntime.Embed(context.Background(), EmbeddingRuntimeRequest{Inputs: []string{"one"}}); !IsEmbeddingRuntimeUnavailable(err) {
		t.Fatalf("Embed unavailable error = %v, want unavailable classification", err)
	}

	timeoutServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte(`{"embeddings":[[0.1]]}`))
	}))
	defer timeoutServer.Close()
	timeoutRuntime, err := NewOllamaEmbeddingRuntime(OllamaEmbeddingOptions{
		Endpoint: timeoutServer.URL,
		HTTPClient: &http.Client{
			Timeout: 5 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("NewOllamaEmbeddingRuntime returned error: %v", err)
	}
	if _, err := timeoutRuntime.Embed(context.Background(), EmbeddingRuntimeRequest{Inputs: []string{"one"}}); !IsEmbeddingRuntimeTimeout(err) {
		t.Fatalf("Embed timeout error = %v, want timeout classification", err)
	}
}

func TestOllamaHealthUsesLocalStatusEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Fatalf("path = %s, want /api/tags", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer server.Close()

	runtime, err := NewOllamaEmbeddingRuntime(OllamaEmbeddingOptions{Endpoint: server.URL})
	if err != nil {
		t.Fatalf("NewOllamaEmbeddingRuntime returned error: %v", err)
	}
	health := runtime.Health(context.Background())
	if !health.Available || health.Status != "available" {
		t.Fatalf("health = %#v, want available", health)
	}
}
