package localclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"loom.local/loom/internal/response"
	"loom.local/loom/internal/watchedroots"
)

func TestWatchedRootBackupStatusDiagnosticClient(t *testing.T) {
	hint := "Inspect loom watched-roots status with the same filters. For a known declared project, use loom project plan <project> to inspect enrollment prerequisites."
	for _, tc := range []struct {
		status int
		code   string
	}{{404, "watched_roots.backup_target_not_found"}, {500, "watched_roots.backup_status_failed"}, {400, "watched_roots.invalid_filter"}} {
		failure := response.ErrorEnvelope{Error: response.ErrorBody{Code: tc.code, Summary: "Safe summary", Domain: "watched_roots", Target: "status", Hint: hint, CorrelationID: "corr_original"}, Meta: response.Meta{CorrelationID: "corr_original", Source: "main-authoritative"}}
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if r.Method != "GET" || r.URL.Path != "/v1/watched-roots/backups/status" || r.URL.Query().Get("node") != "node selector" || r.URL.Query().Get("project") != "project & selector" || r.URL.Query().Get("root") != "root selector" || r.URL.Query().Get("limit") != "7" {
				t.Errorf("filter changed: %s", r.URL)
			}
			w.WriteHeader(tc.status)
			_ = json.NewEncoder(w).Encode(failure)
		}))
		client, err := NewHTTP(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.GetWatchedRootBackupStatus(context.Background(), "corr_request", watchedroots.BackupFilter{NodeRef: "node selector", ProjectRef: "project & selector", RootKey: "root selector", Limit: 7})
		server.Close()
		var got *RequestError
		if !errors.As(err, &got) || got.StatusCode != tc.status || !reflect.DeepEqual(got.Envelope, failure) || calls != 1 {
			t.Fatalf("lost typed envelope or performed extra calls: %#v calls=%d err=%v", got, calls, err)
		}
	}
}
