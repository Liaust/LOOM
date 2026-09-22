package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	loomsync "loom.local/loom/internal/sync"
)

func TestSyncedObjectUploadRejectsOversizeJSONBeforeDatabase(t *testing.T) {
	// Whitespace streams past the body budget without allocating a huge fixture.
	r := httptest.NewRequest(http.MethodPost, "/v1/node-agent/sync/object-upload", io.LimitReader(repeatedSpace{}, loomsync.MaxInlineObjectUploadRequestBytes+1))
	w := httptest.NewRecorder()
	(Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}).handleNodeAgentSyncObjectUpload(w, r)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "request.invalid_json") {
		t.Fatalf("oversize request: %d %s", w.Code, w.Body.String())
	}
}

type repeatedSpace struct{}

func (repeatedSpace) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	return len(p), nil
}
