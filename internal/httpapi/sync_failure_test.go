package httpapi

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"loom.local/loom/internal/objects"
	"loom.local/loom/internal/response"
)

func TestSyncObjectFailureOnlyClassifiesExplicitMissingProject(t *testing.T) {
	s := Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("wrapped: %w", objects.ErrProjectNotFound), http.StatusUnprocessableEntity, "sync.project_not_found"},
		{sql.ErrNoRows, http.StatusBadRequest, "sync.object_upload_failed"},
	} {
		w := httptest.NewRecorder()
		s.writeSyncObjectError(w, "test", "object", tc.err)
		var e response.ErrorEnvelope
		if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		if w.Code != tc.status || e.Error.Code != tc.code {
			t.Fatalf("%d %+v", w.Code, e)
		}
	}
}
