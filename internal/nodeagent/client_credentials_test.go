package nodeagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"loom.local/loom/internal/filetransfer"
	"loom.local/loom/internal/response"
)

func TestNodeCredentialHeadersCoverTransferJSONAndChunks(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer fixture-node-token" {
			t.Error("missing enrolled node authorization")
		}
		if strings.Contains(r.URL.String(), "fixture-node-token") {
			t.Error("token leaked into URL")
		}
		_ = json.NewEncoder(w).Encode(response.Success("fixture", map[string]any{}))
	}))
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client = client.WithNodeCredential("fixture-node-token")
	ctx := context.Background()
	if _, err := client.CreateFileTransfer(ctx, "", "", filetransfer.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetFileTransfer(ctx, "", "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.UploadFileTransferChunk(ctx, "", "", "fixture", 0, []byte("sample"), "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CompleteFileTransfer(ctx, "", "", "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.AbortFileTransfer(ctx, "", "", "fixture", "fixture"); err != nil {
		t.Fatal(err)
	}
	if requests != 5 {
		t.Fatalf("requests=%d, want 5", requests)
	}
	client.HTTPClient = nil
	raw, err := json.Marshal(client)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "fixture-node-token") {
		t.Error("token leaked into serialized client")
	}
}
