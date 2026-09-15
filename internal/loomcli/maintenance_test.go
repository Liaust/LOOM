package loomcli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/response"
)

func TestMaintenanceRetentionApplyRequiresYesBeforeDaemon(t *testing.T) {
	cmd := NewRootCommand()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{"maintenance", "retention", "apply"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("maintenance retention apply without --yes returned nil error")
	} else if !strings.Contains(err.Error(), "requires --yes") {
		t.Fatalf("error missing --yes refusal: %v", err)
	}
	if strings.Contains(stderr.String(), "transport") || strings.Contains(stdout.String(), "transport") {
		t.Fatalf("local refusal should not contact daemon: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestMaintenanceRetentionDryRunAndApplyUsePlanFiles(t *testing.T) {
	planPath := filepath.Join(t.TempDir(), "retention-plan.json")
	call := 0
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/maintenance/db/compact" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
		call++
		var input maintenance.DatabaseCompactInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode retention request: %v", err)
		}
		switch call {
		case 1:
			if !input.DryRun || input.Confirm || input.MaxTotalRows != 250 {
				t.Fatalf("retention dry-run request = %#v", input)
			}
			response.WriteJSON(w, http.StatusOK, response.Success("corr_test", maintenance.DatabaseCompactResult{
				Status:            "planned",
				PlanID:            "dbcompact_test",
				PlanHash:          "sha256:test",
				DryRun:            true,
				RecentSuccessDays: 14,
				CutoffAt:          time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC),
				MaxRowsPerBatch:   25,
				MaxTotalRows:      250,
				Candidates:        map[string]int64{"events.events": 100},
			}))
		case 2:
			if input.DryRun || !input.Confirm || len(input.Plan) == 0 || input.PlanHash != "sha256:test" || input.Reason != "operator review" {
				t.Fatalf("retention apply request missing reviewed fields: %#v", input)
			}
			response.WriteJSON(w, http.StatusOK, response.Success("corr_test", maintenance.DatabaseCompactResult{
				Status:    "applied",
				PlanID:    "dbcompact_test",
				PlanHash:  "sha256:test",
				DryRun:    false,
				Confirmed: true,
			}))
		default:
			t.Fatalf("unexpected call %d", call)
		}
	})
	defer shutdown()

	stdout, stderr, err := executeRootCommand("--socket", socketPath, "maintenance", "retention", "dry-run", "--max-total-rows", "250", "--out", planPath)
	if err != nil {
		t.Fatalf("retention dry-run returned error: %v stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "Maintenance retention dry-run") {
		t.Fatalf("retention dry-run output missing summary:\n%s", stdout)
	}
	if _, err := os.Stat(planPath); err != nil {
		t.Fatalf("plan file was not written: %v", err)
	}

	stdout, stderr, err = executeRootCommand("--socket", socketPath, "maintenance", "retention", "apply", "--yes", "--plan", planPath, "--plan-hash", "sha256:test", "--reason", "operator review")
	if err != nil {
		t.Fatalf("retention apply returned error: %v stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "Maintenance retention apply: applied") {
		t.Fatalf("retention apply output missing summary:\n%s", stdout)
	}
}
