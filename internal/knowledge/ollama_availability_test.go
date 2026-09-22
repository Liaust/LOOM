package knowledge

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaModelAvailability(t *testing.T) {
	for _, tc := range []struct {
		name, body       string
		status           int
		available, model bool
	}{
		{"installed", `{"models":[{"name":"mxbai-embed-large:latest"}]}`, 200, true, true},
		{"other model", `{"models":[{"name":"qwen3-vl:2b"}]}`, 200, true, false},
		{"empty runtime", `{"models":[]}`, 200, true, false},
		{"not Ollama", `{}`, 200, false, false},
		{"bad JSON", `{`, 200, false, false},
		{"unavailable", `{"models":[]}`, 503, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/tags" {
					t.Errorf("path = %s", r.URL.Path)
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			available, models := ollamaModelAvailability(context.Background(), server.URL)
			if available != tc.available || models[ollamaModelName("mxbai-embed-large")] != tc.model {
				t.Fatalf("available=%v models=%v", available, models)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if available, _ := ollamaModelAvailability(ctx, "http://127.0.0.1:1"); available {
		t.Fatal("cancelled/unreachable runtime reported available")
	}
}
