package httpapi

import (
	"context"
	"io"
	"log/slog"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/workspacefiles"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRetainedOperatorRequiresActualRootTransport(t *testing.T) {
	s := NewServer(Services{Files: &workspacefiles.Service{}, RetainedRelocations: &storagearchive.RetainedRelocationService{}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, action := range []string{"operator_plan", "operator_prepare"} {
		r := httptest.NewRequest("POST", "/v1/files/retained-move", strings.NewReader(`{"action":"`+action+`"}`))
		r = r.WithContext(context.WithValue(r.Context(), localFileTransportKey{}, true))
		r.Header.Set("X-Loom-Root", "true")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 403 || !strings.Contains(w.Body.String(), "files.operator_required") {
			t.Fatalf("%s: %d %s", action, w.Code, w.Body.String())
		}
	}
}
