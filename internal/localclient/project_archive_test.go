package localclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"loom.local/loom/internal/response"
	"loom.local/loom/internal/storagearchive"
)

func TestProjectPhysicalClientFailureIsBoundedAndRetainsPartialData(t *testing.T) {
	for _, mode := range []string{"partial", "oversize", "raw", "trailing", "unconfirmed", "empty_success"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/projects/test/archive/restore/apply" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("incorrect typed route")
				}
				if mode != "unconfirmed" && mode != "empty_success" {
					w.WriteHeader(409)
				}
				switch mode {
				case "empty_success":
					fmt.Fprint(w, `{"ok":true}`)
				case "oversize":
					fmt.Fprint(w, strings.Repeat("private", storagearchive.MaximumWorkspaceSurfaceResponseBytes))
				case "raw":
					fmt.Fprint(w, "private /root/key failure")
				case "trailing":
					fmt.Fprint(w, `{"ok":false} private /root/key`)
				default:
					_ = json.NewEncoder(w).Encode(struct {
						response.ErrorEnvelope
						Data storagearchive.ProjectPhysicalMutationSummary `json:"data"`
					}{response.ErrorEnvelope{Error: response.ErrorBody{Code: "project_archive.operation_failed"}}, storagearchive.ProjectPhysicalMutationSummary{OperationID: "retained_operation", Phase: "project_state_pending", Recoverable: true}})
				}
			}))
			defer server.Close()
			client, _ := NewHTTP(server.URL)
			out, err := client.ApplyProjectPhysicalRestore(context.Background(), "corr", "test", storagearchive.ProjectPhysicalApplyRequest{})
			if err == nil || out.OK || strings.Contains(err.Error(), "private") {
				t.Fatalf("unsafe failure %v %+v", err, out)
			}
			if mode == "partial" {
				var typed *RequestError
				if !errors.As(err, &typed) || out.Data.Phase != "project_state_pending" || !out.Data.Recoverable {
					t.Fatal("partial truth lost")
				}
			}
		})
	}
}

func TestProjectPhysicalClientRequestBound(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	c, _ := NewHTTP(server.URL)
	if _, err := c.ReviewProjectPhysicalArchive(context.Background(), "corr", "test", strings.Repeat("x", storagearchive.MaximumWorkspaceSurfaceRequestBytes)); err == nil || requests != 0 {
		t.Fatal("oversized request sent")
	}
}
