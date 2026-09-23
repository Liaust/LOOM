package nodeagent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/correlation"
	"loom.local/loom/internal/idempotency"
	"loom.local/loom/internal/response"
	loomsync "loom.local/loom/internal/sync"
)

func TestStreamedSyncClientAndFileInspection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.pdf")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	size := int64(loomsync.MaxInlineObjectUploadBytes + 1)
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	sample, info, hash, err := inspectSyncFile(path)
	if err != nil || len(sample) != 512 || info.Size() != size {
		t.Fatalf("inspect: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/node-agent/sync/object-upload" || r.Header.Get(correlation.Header) != "test-stream" || r.Header.Get(idempotency.Header) != "stream-key" {
			t.Error("request identity changed")
		}
		m, err := r.MultipartReader()
		if err != nil {
			t.Error(err)
			return
		}
		p, err := m.NextPart()
		if err != nil || p.FormName() != "metadata" {
			t.Error("missing metadata")
			return
		}
		var in loomsync.SyncedObjectInput
		if err := json.NewDecoder(p).Decode(&in); err != nil || in.ContentBase64 != "" || in.SizeBytes != size {
			t.Error("incorrect metadata")
			return
		}
		p, err = m.NextPart()
		if err != nil || p.FormName() != "content" {
			t.Error("missing content")
			return
		}
		h := sha256.New()
		n, err := io.Copy(h, p)
		if err != nil || n != size || fmt.Sprintf("sha256:%x", h.Sum(nil)) != hash {
			t.Errorf("stream content: %d %v", n, err)
		}
		if _, err := m.NextPart(); err != io.EOF {
			t.Errorf("trailing part: %v", err)
		}
		_ = json.NewEncoder(w).Encode(response.Success("test-stream", loomsync.SyncedObjectResult{}))
	}))
	defer server.Close()
	f, err = os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	client := Client{BaseURL: server.URL, HTTPClient: server.Client()}
	if _, err := client.UploadSyncedObjectStream(context.Background(), "test-stream", "stream-key", loomsync.SyncedObjectInput{SizeBytes: size, HashURI: hash}, f); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(path), "link.pdf")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := inspectSyncFile(link); err == nil {
		t.Fatal("followed symlink")
	}
}
