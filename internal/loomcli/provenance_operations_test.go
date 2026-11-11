package loomcli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/idempotency"
	"loom.local/loom/internal/provenance"
	"loom.local/loom/internal/response"
)

func TestProvenanceCandidateRegisterCLI(t *testing.T) {
	var example provenance.CandidateRegistrationRequest
	if err := json.Unmarshal([]byte(provenanceRegistrationExample), &example); err != nil {
		t.Fatal(err)
	}
	if err := provenance.ValidateCandidateRegistrationRequest("capture-retry", example); err != nil {
		t.Fatalf("invalid help example: %v", err)
	}
	calls := 0
	const candidateID = "22222222-2222-4222-8222-222222222222"
	socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/v1/provenance/candidates" || r.Header.Get(idempotency.Header) != "capture-retry" {
			t.Errorf("request=%s %s", r.Method, r.URL)
		}
		var input provenance.CandidateRegistrationRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		got, _ := json.Marshal(input)
		want, _ := json.Marshal(example)
		if !bytes.Equal(got, want) {
			t.Errorf("changed producer/evidence: %s", got)
		}
		response.WriteJSON(w, http.StatusOK, response.SuccessWithIdempotency("corr_capture", "capture-retry", provenance.AuthorizedCandidateRegistration{Receipt: provenance.CandidateRegistrationTransportBatchReceipt{
			SchemaVersion: "1.0", Replayed: calls > 1, Receipts: []provenance.CandidateRegistrationTransportReceipt{{CandidateID: candidateID, State: "pending", EffectiveState: "pending", Replayed: calls > 1}},
		}}))
	})
	defer stop()
	file := filepath.Join(t.TempDir(), "candidate.json")
	if err := os.WriteFile(file, []byte(provenanceRegistrationExample), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tail := range [][]string{
		{"--file", file, "--idempotency-key", "capture-retry"},
		{"--file", file, "--yes"},
		{"--file", file + ".missing", "--idempotency-key", "capture-retry", "--yes"},
	} {
		out, _, err := executeRootCommand(append([]string{"--socket", socket, "--json", "provenance", "candidate", "register"}, tail...)...)
		if err == nil || !strings.Contains(out, "provenance.invalid_request") || !json.Valid([]byte(out)) {
			t.Fatalf("refusal=%s %v", out, err)
		}
	}
	for _, invalid := range []string{`{"candidates":[]}`, `{"candidates":[],"unknown":true}`, `{"candidates":[]} {}`, `null`, strings.Repeat(" ", provenance.MaximumFoundationRequestBytes+1)} {
		if err := os.WriteFile(file, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		out, _, err := executeRootCommand("--socket", socket, "--json", "provenance", "candidate", "register", "--file", file, "--idempotency-key", "capture-retry", "--yes")
		if err == nil || !json.Valid([]byte(out)) || !strings.Contains(out, "provenance.invalid_request") {
			t.Fatalf("invalid delivered: %s %v", out, err)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid input caused %d mutations", calls)
	}
	if err := os.WriteFile(file, []byte(provenanceRegistrationExample), 0600); err != nil {
		t.Fatal(err)
	}
	for replay := 0; replay < 2; replay++ {
		out, stderr, err := executeRootCommand("--socket", socket, "--json", "provenance", "candidate", "register", "--file", file, "--idempotency-key", "capture-retry", "--yes")
		var env response.Envelope[provenance.AuthorizedCandidateRegistration]
		if err != nil || json.Unmarshal([]byte(out), &env) != nil || !env.OK || env.Data.Receipt.Receipts[0].CandidateID != candidateID || env.Data.Receipt.Replayed != (replay == 1) {
			t.Fatalf("out=%s stderr=%s err=%v", out, stderr, err)
		}
	}
	if calls != 2 {
		t.Fatalf("replay calls=%d", calls)
	}
}

func TestProvenanceCandidateRegisterDeliveryErrorsCLI(t *testing.T) {
	file := filepath.Join(t.TempDir(), "candidate.json")
	if err := os.WriteFile(file, []byte(provenanceRegistrationExample), 0600); err != nil {
		t.Fatal(err)
	}
	for _, status := range []int{http.StatusBadRequest, http.StatusInternalServerError, http.StatusOK, 0} {
		socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
			if status == 0 {
				if h, ok := w.(http.Hijacker); ok {
					conn, _, _ := h.Hijack()
					conn.Close()
				}
				return
			}
			w.WriteHeader(status)
			io.WriteString(w, `{"ok":false,"error":{"code":"provenance.invalid_request","summary":"rejected"},"meta":{"correlation_id":"corr_reject"}}`)
		})
		out, _, err := executeRootCommand("--socket", socket, "--json", "provenance", "candidate", "register", "--file", file, "--idempotency-key", "exact-retry", "--yes")
		stop()
		code := "provenance.delivery_uncertain"
		if status == http.StatusBadRequest {
			code = "provenance.invalid_request"
		}
		if err == nil || !json.Valid([]byte(out)) || !strings.Contains(out, code) {
			t.Fatalf("status=%d out=%s err=%v", status, out, err)
		}
		if status != http.StatusBadRequest && !strings.Contains(out, "exact-retry") {
			t.Fatalf("lost retry key: %s", out)
		}
	}
}

func TestProvenanceReviewListCLI(t *testing.T) {
	calls := 0
	checkpoint := `[{"object_kind":"case","object_id":"22222222-2222-4222-8222-222222222222","change_token":"0123456789abcdef0123456789abcdef"}]`
	file := filepath.Join(t.TempDir(), "reviewed.json")
	if err := os.WriteFile(file, []byte(checkpoint), 0600); err != nil {
		t.Fatal(err)
	}
	socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		q := r.URL.Query()
		if r.Method != "GET" || r.URL.Path != "/v1/provenance/review" || q.Get("limit") != "10" || len(q["exclude_domain"]) != 2 || q.Get("reviewed") != checkpoint || r.Header.Get(idempotency.Header) != "" {
			t.Errorf("request=%s", r.URL)
		}
		response.WriteJSON(w, http.StatusOK, response.Success("corr_review", provenance.ReviewResponse{SchemaVersion: "1.0", AsOf: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC), Items: []provenance.ReviewItem{}}))
	})
	defer stop()
	for _, tail := range [][]string{{"--limit", "101"}, {"--cursor", "invalid"}, {"--reviewed-file", file + ".missing"}} {
		out, _, err := executeRootCommand(append([]string{"--socket", socket, "--json", "provenance", "review", "list"}, tail...)...)
		if err == nil || !strings.Contains(out, "provenance.invalid_request") {
			t.Fatalf("invalid read: %s %v", out, err)
		}
	}
	if calls != 0 {
		t.Fatal("invalid review reached backend")
	}
	out, _, err := executeRootCommand("--socket", socket, "--json", "provenance", "review", "list", "--exclude-domain", "fixture", "--exclude-domain", "second-exact-fixture", "--reviewed-file", file)
	if err != nil || !strings.Contains(out, `"items":[]`) || !strings.Contains(out, `"truncated":false`) {
		t.Fatalf("read=%s %v", out, err)
	}
}

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
