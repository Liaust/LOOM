package loomcli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/idempotency"
	"loom.local/loom/internal/provenance"
	"loom.local/loom/internal/response"
)

func TestProvenanceReviewedOperationsCLI(t *testing.T) {
	calls := 0
	socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/v1/provenance/operations" || r.Header.Get(idempotency.Header) != "review-retry" {
			t.Errorf("unexpected request: %s %s %s", r.Method, r.URL.Path, r.Header.Get(idempotency.Header))
		}
		var input provenance.ManualOperationsRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input.Producer.ProducerID != "mina" || len(input.Operations) != 1 || !strings.Contains(string(input.Operations[0]), "accept_candidate") {
			t.Fatalf("altered review: %#v", input)
		}
		response.WriteJSON(w, http.StatusOK, response.Success("corr_review", provenance.AuthorizedManualOperations{}))
	})
	defer stop()
	file := filepath.Join(t.TempDir(), "review.json")
	if err := os.WriteFile(file, []byte(`{"scope":{"domain":"project","visibility":"private"},"producer":{"producer_id":"mina","producer_kind":"working_agent","task_id":"review"},"operations":[{"operation_type":"accept_candidate","candidate_id":"22222222-2222-4222-8222-222222222222"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tail := range [][]string{
		{"--file", file, "--idempotency-key", "review-retry"},
		{"--file", file, "--yes"},
		{"--file", file + ".missing", "--idempotency-key", "review-retry", "--yes"},
	} {
		out, _, _ := executeRootCommand(append([]string{"--socket", socket, "--json", "provenance", "operations", "apply"}, tail...)...)
		if !strings.Contains(out, "provenance.invalid_request") {
			t.Fatalf("missing refusal: %s", out)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid request reached backend")
	}
	out, stderr, err := executeRootCommand("--socket", socket, "--json", "provenance", "operations", "apply", "--file", file, "--idempotency-key", "review-retry", "--yes")
	if err != nil || !strings.Contains(out, `"ok":true`) || calls != 1 {
		t.Fatalf("out=%s stderr=%s err=%v calls=%d", out, stderr, err, calls)
	}
}

func TestProvenanceCandidateListCLI(t *testing.T) {
	socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/v1/provenance/candidates" || q.Get("limit") != "3" || q.Get("after_id") != "22222222-2222-4222-8222-222222222222" || q.Get("after_time") != "2026-09-29T00:00:00Z" {
			t.Fatalf("request=%s", r.URL)
		}
		response.WriteJSON(w, http.StatusOK, response.Success("corr_page", provenance.Page[provenance.CandidateSummary]{}))
	})
	defer stop()
	out, stderr, err := executeRootCommand("--socket", socket, "--json", "provenance", "candidate", "list", "--limit", "3", "--after-time", "2026-09-29T00:00:00Z", "--after-id", "22222222-2222-4222-8222-222222222222")
	if err != nil || !strings.Contains(out, `"ok":true`) {
		t.Fatalf("out=%s stderr=%s err=%v", out, stderr, err)
	}
}
