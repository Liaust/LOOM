package loomcli

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"loom.local/loom/internal/response"
)

func TestWatchedRootBackupStatusDiagnosticCLI(t *testing.T) {
	hint := "Inspect loom watched-roots status with the same filters. For a known declared project, use loom project plan <project> to inspect enrollment prerequisites."
	failure := response.ErrorEnvelope{OK: false, Error: response.ErrorBody{Code: "watched_roots.backup_target_not_found", Summary: "No matching reported watched root exists for these filters.", Domain: "watched_roots", Target: "status", Hint: hint, CorrelationID: "corr_frozen_backup_status"}, Meta: response.Meta{CorrelationID: "corr_frozen_backup_status", Source: "main-authoritative", Freshness: "live", GeneratedAt: "2026-09-13T00:00:00Z"}}
	socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v1/watched-roots/backups/status" || r.URL.Query().Get("project") != "synthetic-project" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		w.WriteHeader(404)
		_ = json.NewEncoder(w).Encode(failure)
	})
	defer stop()
	for _, format := range []string{"json", "human"} {
		t.Run(format, func(t *testing.T) {
			args := []string{"--socket", socket, "watched-roots", "backups", "status", "--project", "synthetic-project"}
			if format == "json" {
				args = append(args, "--json")
			}
			stdout, stderr, err := executeRootCommand(args...)
			if err == nil {
				t.Fatal("missing target exited successfully")
			}
			if format == "json" {
				var got response.ErrorEnvelope
				if err := json.Unmarshal([]byte(stdout), &got); err != nil || !reflect.DeepEqual(got, failure) {
					t.Fatalf("error envelope changed: %s %v", stdout, err)
				}
			} else if stdout != "" || !strings.Contains(stderr, failure.Error.Code) || !strings.Contains(stderr, "corr_frozen_backup_status") || !strings.Contains(stderr, hint) {
				t.Fatalf("human diagnostic lost fields: stdout=%q stderr=%q", stdout, stderr)
			}
		})
	}
}
