package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"loom.local/loom/internal/notesworkspacesync"
)

func TestNotesRecoveryInventoryRequiresRootPeer(t *testing.T) {
	calls := 0
	refreshes := 0
	server := NewServer(Services{NotesRecovery: func(context.Context) (notesworkspacesync.EnrollmentInventory, error) {
		calls++
		return notesworkspacesync.EnrollmentInventory{SchemaVersion: notesworkspacesync.RecoveryInventorySchema}, nil
	}, NotesRecoveryPrepare: func(context.Context) (notesworkspacesync.EnrollmentInventory, error) {
		refreshes++
		return notesworkspacesync.EnrollmentInventory{SchemaVersion: notesworkspacesync.RecoveryInventorySchema}, nil
	}}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, root := range []bool{false, true} {
			req := httptest.NewRequest(method, "/v1/backup/notes-inventory", nil)
			req.Header.Set("X-Root-Peer", "true") // Headers cannot impersonate Unix peer credentials.
			req = req.WithContext(context.WithValue(req.Context(), localRootTransportKey{}, root))
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			want := http.StatusForbidden
			if root {
				want = http.StatusOK
			}
			if rec.Code != want {
				t.Fatalf("root=%v status=%d body=%s", root, rec.Code, rec.Body.String())
			}
		}
	}
	if calls != 1 || refreshes != 1 {
		t.Fatalf("unexpected read/refresh calls: %d/%d", calls, refreshes)
	}
}
