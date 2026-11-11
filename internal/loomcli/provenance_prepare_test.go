package loomcli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/provenance"
	"loom.local/loom/internal/response"
)

func TestProvenanceReviewPrepareIsReadOnlyAndSavesExactBatch(t *testing.T) {
	id := provenance.SemanticID("11111111-1111-4111-8111-111111111111")
	source := provenance.SemanticID("22222222-2222-4222-8222-222222222222")
	registration := provenance.CandidateRegistration{SchemaVersion: "1.0", Claim: "Keep the synthetic summary in Markdown.", RecordKind: "decision", RecordContext: "Lab only", Domain: "lab", Visibility: "private", AssertionPosture: "source_claim", Temporal: provenance.TemporalInterpretation{Interpretation: "Synthetic project choice"}, Producer: provenance.ProducerIdentity{ProducerID: "mina", ProducerKind: "working_agent", TaskID: "original-capture"}}
	submitted, _ := json.Marshal(registration)
	candidate := provenance.CandidateWithSources{CandidateLifecycleProjection: provenance.CandidateLifecycleProjection{SchemaVersion: "1.0", EffectiveState: "pending", Candidate: provenance.Candidate{ID: id, Claim: registration.Claim, Domain: "lab", Visibility: "private", Submitted: submitted}, SourceReferenceIDs: []provenance.SemanticID{source}},
		Sources: provenance.LinkedSources{SchemaVersion: provenance.LinkedSourcesSchemaVersion, ParentKind: "candidate", ParentID: id, Posture: "stored_resolution", Returned: 1, Items: []provenance.LinkedSourceItem{{SourceReferenceID: source, StoredSourceReceipt: &provenance.StoredSourceReceipt{SchemaVersion: "1.0", Status: "resolved"}}}}}
	calls := 0
	socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/v1/provenance/candidates/"+string(id) || r.URL.Query().Get("sources") != "true" || r.URL.Query().Get("limit") != "50" {
			t.Errorf("prepare performed unexpected operation: %s %s", r.Method, r.URL)
		}
		response.WriteJSON(w, http.StatusOK, response.Success("prepare", candidate))
	})
	defer stop()
	root := t.TempDir()
	producerFile, output := filepath.Join(root, "reviewer.json"), filepath.Join(root, "review.json")
	if err := os.WriteFile(producerFile, []byte(`{"producer_id":"mina","producer_kind":"working_agent","task_id":"actual-review"}`), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--socket", socket, "--json", "provenance", "review", "prepare", string(id), "--producer-file", producerFile, "--out", output}
	out, _, err := executeRootCommand(args...)
	var summary struct {
		Mutation     bool
		RecordID     string `json:"record_id"`
		ApplyCommand string `json:"apply_command"`
	}
	if err != nil || json.Unmarshal([]byte(out), &summary) != nil || summary.Mutation || summary.RecordID == "" || !strings.Contains(summary.ApplyCommand, "--idempotency-key review-") || calls != 1 {
		t.Fatalf("invalid preparation: %s %v", out, err)
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var saved provenance.ManualOperationsRequest
	if json.Unmarshal(raw, &saved) != nil || provenance.ValidateManualOperationsRequest(saved) != nil || saved.Producer.TaskID != "actual-review" || len(saved.Operations) != 1 {
		t.Fatalf("invalid saved batch: %s", raw)
	}
	var accept provenance.AcceptCandidateOperation
	json.Unmarshal(saved.Operations[0], &accept)
	if accept.Accepted.ProducerHistory[0].TaskID != "original-capture" || accept.Accepted.ProducerHistory[1].TaskID != "actual-review" || accept.Accepted.SourceReferenceIDs[0] != source {
		t.Fatal("capture/review attribution or sources changed")
	}
	info, _ := os.Stat(output)
	if info.Mode().Perm() != 0600 {
		t.Fatal("prepared file is not private")
	}
	if _, _, err := executeRootCommand(args...); err == nil {
		t.Fatal("existing saved batch overwritten")
	}
	after, _ := os.ReadFile(output)
	if string(after) != string(raw) {
		t.Fatal("failed preparation changed saved replay")
	}
	candidate.EffectiveState = "accepted"
	args[len(args)-1] = filepath.Join(root, "terminal.json")
	if out, _, err := executeRootCommand(args...); err == nil || !strings.Contains(out, "pending candidate") {
		t.Fatalf("terminal candidate not refused usefully: %s %v", out, err)
	}
}

func TestProvenanceRelationshipGetUsesExistingExactRoute(t *testing.T) {
	id := provenance.SemanticID("33333333-3333-4333-8333-333333333333")
	context := strings.Repeat("Scoped history. ", 100)
	data := provenance.RelationshipLifecycleProjection{SchemaVersion: "1.0", EffectiveState: "active", Relationship: provenance.Relationship{ID: id, RelationshipType: "supersedes", FromRecordID: "44444444-4444-4444-8444-444444444444", ToRecordID: "55555555-5555-4555-8555-555555555555", Context: &context, CreatedAt: time.Now().UTC(), Payload: json.RawMessage(`{"large_diagnostic":"not in routine output"}`)}}
	socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v1/provenance/relationships/"+string(id) {
			t.Fatal("relationship reader did not use existing route")
		}
		response.WriteJSON(w, http.StatusOK, response.Success("relationship", data))
	})
	defer stop()
	out, _, err := executeRootCommand("--socket", socket, "--json", "provenance", "relationship", "get", string(id))
	if err != nil || !strings.Contains(out, `"relationship_type":"supersedes"`) || !strings.Contains(out, `"effective_state":"active"`) || strings.Contains(out, "large_diagnostic") || !strings.Contains(out, `"truncated":true`) {
		t.Fatalf("relationship routine result: %s %v", out, err)
	}
	out, _, err = executeRootCommand("--socket", socket, "--json", "provenance", "relationship", "get", string(id), "--details")
	if err != nil || !strings.Contains(out, "large_diagnostic") {
		t.Fatal("details lost full representation")
	}
}
