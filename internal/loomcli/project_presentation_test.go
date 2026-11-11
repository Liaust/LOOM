package loomcli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectwatch"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/storagearchive"
)

func TestProjectRequestWaitOnlyReportsOutstandingHumanRequest(t *testing.T) {
	for _, opts := range []options{{}, {jsonOutput: true}, {plainOutput: true}} {
		cmd := &cobra.Command{}
		var stdout, stderr bytes.Buffer
		cmd.SetOut(&stdout)
		cmd.SetErr(&stderr)
		stop := startProjectRequestWait(cmd, &opts, "operation_exact", "archive", time.Millisecond, time.Hour)
		time.Sleep(30 * time.Millisecond)
		stop()
		if stdout.Len() != 0 {
			t.Fatal("progress polluted machine output")
		}
		if opts.jsonOutput || opts.plainOutput {
			if stderr.Len() != 0 {
				t.Fatal("machine mode progress")
			}
		} else if !strings.Contains(stderr.String(), "operation_exact") || !strings.Contains(stderr.String(), "Completion is not yet confirmed") {
			t.Fatal(stderr.String())
		}
	}
}

func TestProjectAdministrativeSummariesDoNotRepeatRawConfiguration(t *testing.T) {
	detail := projects.ProjectRegistrationDetail{Registration: &projects.ProjectContractRegistration{Contract: json.RawMessage(`{"private_config":"UNWANTED"}`)}}
	plan := projectwatch.ProjectWatchPlan{Detail: &detail, WatchedRoots: make([]pc.ProjectWatchedRootItem, 50)}
	for i := range plan.WatchedRoots {
		plan.WatchedRoots[i].ConfigJSON = json.RawMessage(`{"raw":"UNWANTED"}`)
	}
	archive := storagearchive.ProjectArchiveInspectResult{Project: detail}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/watch-plan"):
			response.WriteJSON(w, 200, response.Success("test", plan))
		case strings.HasSuffix(r.URL.Path, "/archive/inspect"):
			response.WriteJSON(w, 200, response.Success("test", archive))
		default:
			response.WriteJSON(w, 200, response.Success("test", detail))
		}
	}))
	defer server.Close()
	cfg := writeProjectBackendInstallManifest(t, server.URL)
	for _, args := range [][]string{{"watch-plan", "test"}, {"archive", "inspect", "test"}, {"registration-status", "test"}} {
		full, _, err := executeRootCommand(append([]string{"--config", cfg, "--json", "project"}, args...)...)
		if err != nil || !strings.Contains(full, "UNWANTED") {
			t.Fatal("full contract changed", err, full)
		}
		out, _, err := executeRootCommand(append(append([]string{"--config", cfg, "--json", "project"}, args...), "--summary")...)
		if err != nil || !json.Valid([]byte(out)) || strings.Contains(out, "UNWANTED") || !strings.Contains(out, "full_command") {
			t.Fatal("invalid summary", err, out)
		}
	}
	view := projectWatchPlanSummary(plan, "test")
	if view["roots_omitted"] != 26 || view["root_count"] != 50 {
		t.Fatal(view)
	}
	if view["root_state_basis"] != "compiled_intent_not_live_node_observation" || !strings.Contains(view["usage"].(string), "not current node ACKs") {
		t.Fatal("compiled root state presented as live evidence", view)
	}
}

func TestWatchOnlyReadinessIsScopedWithoutInventingProjectHealth(t *testing.T) {
	result := pc.DeclarationResult{State: pc.DeclarationOperationSucceeded, Effects: []pc.DeclarationEffect{pc.DeclarationWatches}}
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	if err := renderDeclarationResult(cmd, &options{jsonOutput: true}, result, nil); err != nil {
		t.Fatal(err)
	}
	var view struct {
		EvidenceScope string                `json:"evidence_scope"`
		ReadinessNote string                `json:"readiness_note"`
		NextAction    declarationNextAction `json:"next_action"`
	}
	if err := json.Unmarshal(out.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.EvidenceScope != "watch_configuration_only" || view.NextAction.Kind != "none" || !strings.Contains(view.ReadinessNote, "not failed") {
		t.Fatal(out.String())
	}
	if result.Readiness.Healthy.State == pc.DeclarationSatisfied || result.Readiness.Protected.State == pc.DeclarationSatisfied {
		t.Fatal("invented health/protection")
	}
	result.State = pc.DeclarationOperationPartial
	if declarationResultNextAction(result, nil).Kind == "none" {
		t.Fatal("unfinished work hidden")
	}
}
