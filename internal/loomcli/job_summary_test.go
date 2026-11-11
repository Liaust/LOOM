package loomcli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/response"
)

func TestJobSummaryCurrentResultAndFullCompatibility(t *testing.T) {
	exit := 0
	detail := jobs.JobDetail{Job: jobs.Job{JobID: "job_fixture", Status: "completed", AttemptCount: 2,
		MaxAttempts: 1, ExitCode: &exit, OutputJSON: json.RawMessage(`{"outputs":{"outcome":"completed","attempt":2,"thread_id":"thread_current","question":"","not_a_summary_field":"private input"}}`),
		ArtifactRefsJSON: json.RawMessage(`[{"artifact_key":"answer","artifact_id":"artifact_current"}]`)},
		Attempts: []jobs.Attempt{{AttemptNumber: 1, Status: "failed"}, {AttemptNumber: 2, Status: "completed"}},
		Outputs:  []jobs.JobOutput{{OutputKey: "outcome", ValueJSON: json.RawMessage(`"failed"`)}, {OutputKey: "answer", ValueJSON: json.RawMessage(`"old answer"`)}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/jobs/job_fixture" {
			t.Errorf("unexpected route: %s", r.URL.Path)
		}
		response.WriteJSON(w, 200, response.Success("original-correlation", detail))
	}))
	defer server.Close()
	cfg := writeProjectBackendInstallManifest(t, server.URL)
	run := func(flags ...string) string {
		t.Helper()
		out, _, err := executeRootCommand(append([]string{"--config", cfg, "job", "inspect", "job_fixture"}, flags...)...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	var summary response.Envelope[jobSummary]
	raw := run("--json", "--summary")
	if err := json.Unmarshal([]byte(raw), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Meta.CorrelationID != "original-correlation" || summary.Data.Outcome != "completed" || !summary.Data.AboveLimit || summary.Data.RetainedOutputs != 2 || len(summary.Data.Artifacts) != 1 {
		t.Fatalf("wrong current summary: %s", raw)
	}
	for _, hidden := range []string{"private input", "old answer", `"failed"`, "workdir_path", "input_json"} {
		if strings.Contains(raw, hidden) {
			t.Fatalf("summary leaked retained or unselected detail: %s", raw)
		}
	}
	if summary.Data.Artifacts[0].Read != "loom artifact read -- 'artifact_current'" {
		t.Fatal("missing exact current answer", summary.Data)
	}
	human := run("--summary")
	for _, want := range []string{"automatic limit 1", "not an automatic retry allowance", `Agent outcome: "completed"`, "artifact_current", "Full detail: loom job inspect"} {
		if !strings.Contains(human, want) {
			t.Fatalf("missing %q: %s", want, human)
		}
	}
	var full response.Envelope[jobs.JobDetail]
	err := json.Unmarshal([]byte(run("--json")), &full)
	wantJSON, _ := json.Marshal(detail)
	gotJSON, _ := json.Marshal(full.Data)
	if err != nil || !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("existing full contract changed: %+v %v", full, err)
	}
	if !strings.Contains(run(), "executed attempts exceed the automatic limit") {
		t.Fatal("full view still has unexplained attempt budget")
	}
}

func TestJobSummaryQuestionBoundsAndClearedRetry(t *testing.T) {
	question := strings.Repeat("why? ", 200)
	output, _ := json.Marshal(map[string]any{"outputs": map[string]any{"outcome": "needs_input", "question": question, "attempt": 1}})
	detail := jobs.JobDetail{Job: jobs.Job{JobID: "job_question", Status: "failed", ManualAction: true, AttemptCount: 1, MaxAttempts: 1,
		OutputJSON: output, ArtifactRefsJSON: json.RawMessage(`[]`)}}
	s := summarizeJob(detail)
	if s.Outcome != "needs_input" || !s.ManualAction || len([]rune(s.Question)) > 512 || !reflect.DeepEqual(s.Truncated, []string{"question"}) {
		t.Fatalf("question lost or unbounded: %+v", s)
	}
	detail.Job.Status, detail.Job.ManualAction = "queued", false
	detail.Job.OutputJSON = json.RawMessage(`{}`)
	detail.Outputs = []jobs.JobOutput{{OutputKey: "outcome", ValueJSON: json.RawMessage(`"needs_input"`)}}
	s = summarizeJob(detail)
	if s.Outcome != "" || s.Question != "" || s.ManualAction || len(s.Artifacts) != 0 {
		t.Fatal("queued retry inherited old outcome", s)
	}
	detail.Job.OutputJSON = json.RawMessage(`{"outputs":{"outcome":"completed","attempt":2}}`)
	if s = summarizeJob(detail); !s.UnverifiedResult || s.Outcome != "" {
		t.Fatal("mismatched attempt was claimed current", s)
	}
	detail.Job.ArtifactRefsJSON = json.RawMessage(`{"unexpected":"object"}`)
	if s = summarizeJob(detail); !s.UnverifiedRefs || len(s.Artifacts) != 0 {
		t.Fatal("invalid artifact inventory was presented as verified", s)
	}
	if jobReadCommand("artifact read", "bad\nreference") != "" || jobReadCommand("job inspect", "a'b") != "loom job inspect -- 'a'\\''b'" {
		t.Fatal("unsafe exact navigation")
	}
}

func TestJobSummaryAcknowledgementPreservesClarificationAndRetryGuard(t *testing.T) {
	for _, state := range []string{"active", "acknowledged", "archived"} {
		t.Run(state, func(t *testing.T) {
			detail := jobs.JobDetail{Job: jobs.Job{JobID: "job_question", Status: "failed", ManualAction: true,
				AttemptCount: 1, MaxAttempts: 1, FailureAttentionStatus: state,
				FailureAttentionNote: "Resolved through the same-thread continuation job_next.",
				OutputJSON:           json.RawMessage(`{"outputs":{"outcome":"needs_input","question":"Which scope?","attempt":1}}`)}}
			s := summarizeJob(detail)
			if s.AttentionStatus != state || s.AttentionNote != detail.Job.FailureAttentionNote || !s.ManualAction || s.Outcome != "needs_input" || s.Status != "failed" {
				t.Fatalf("attention changed historical result or retry guard: %+v", s)
			}
			var out bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&out)
			renderJobSummary(cmd, s)
			if !strings.Contains(out.String(), "Failure attention: "+state) || !strings.Contains(out.String(), "job_next") {
				t.Fatal("acknowledgement is not visible", out.String())
			}
			if strings.Contains(out.String(), "not a new pending request") != (state != "active") {
				t.Fatal("active attention was suppressed or acknowledged attention remains ambiguous", out.String())
			}
			raw, _ := json.Marshal(s)
			if !strings.Contains(string(raw), `"failure_attention_status":"`+state+`"`) || !strings.Contains(string(raw), `"manual_action_required":true`) {
				t.Fatal("compact JSON dropped independent attention/retry states", string(raw))
			}
		})
	}
	detail := jobs.JobDetail{Job: jobs.Job{FailureAttentionNote: strings.Repeat("x", 600)}}
	s := summarizeJob(detail)
	if len([]rune(s.AttentionNote)) != 512 || !reflect.DeepEqual(s.Truncated, []string{"failure_attention_note"}) {
		t.Fatal("attention note is not bounded", s)
	}
}
