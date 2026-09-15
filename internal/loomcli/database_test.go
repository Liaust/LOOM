package loomcli

import (
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

func TestDatabaseCompactDryRunWritesPlanFile(t *testing.T) {
	planPath := filepath.Join(t.TempDir(), "plans", "compact.json")
	cutoff := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/maintenance/db/compact" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
		var input maintenance.DatabaseCompactInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode compact request: %v", err)
		}
		if !input.DryRun || input.Confirm {
			t.Fatalf("dry-run request = %#v, want dry-run without confirm", input)
		}
		response.WriteJSON(w, http.StatusOK, response.Success("corr_test", maintenance.DatabaseCompactResult{
			Status:              "planned",
			PlanID:              "dbcompact_test",
			PlanHash:            "sha256:test",
			DryRun:              true,
			RecentSuccessDays:   14,
			CutoffAt:            cutoff,
			AllowedTables:       []string{"events.events", "workers.worker_leases"},
			RowLimits:           map[string]int64{"events.events": 10, "workers.worker_leases": 0},
			MaxRowsPerBatch:     5,
			MaxTotalRows:        10,
			Candidates:          map[string]int64{"events.events": 10},
			PhysicalStorageNote: "Logical row deletion may not immediately reduce PostgreSQL table or database file size; autovacuum can reuse freed space, while VACUUM FULL/table rewrites are intentionally out of scope.",
		}))
	})
	defer shutdown()

	stdout, stderr, err := executeRootCommand("--socket", socketPath, "database", "compact", "--dry-run", "--out", planPath)
	if err != nil {
		t.Fatalf("database compact dry-run returned error: %v stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "Plan: dbcompact_test sha256:test") || !strings.Contains(stdout, "Note: Logical row deletion") {
		t.Fatalf("dry-run output missing plan/note:\n%s", stdout)
	}
	raw, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read written plan: %v", err)
	}
	var written maintenance.DatabaseCompactResult
	if err := json.Unmarshal(raw, &written); err != nil {
		t.Fatalf("decode written plan: %v\n%s", err, string(raw))
	}
	if written.PlanHash != "sha256:test" || !written.DryRun || written.MaxTotalRows != 10 {
		t.Fatalf("unexpected written plan: %#v", written)
	}
}

func TestDatabaseCompactPlanConfirmSendsReviewedPlan(t *testing.T) {
	planPath := filepath.Join(t.TempDir(), "compact-plan.json")
	planJSON := `{"status":"planned","plan_hash":"sha256:test","cutoff_at":"2026-07-01T12:00:00Z","recent_success_days":14}`
	if err := os.WriteFile(planPath, []byte(planJSON+"\n"), 0o600); err != nil {
		t.Fatalf("write plan fixture: %v", err)
	}
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/maintenance/db/compact" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
		var input maintenance.DatabaseCompactInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode compact apply request: %v", err)
		}
		if input.DryRun || !input.Confirm || len(input.Plan) == 0 {
			t.Fatalf("apply request missing confirm/reviewed plan: %#v", input)
		}
		if !strings.Contains(string(input.Plan), `"plan_hash":"sha256:test"`) {
			t.Fatalf("reviewed plan not sent in request: %s", input.Plan)
		}
		response.WriteJSON(w, http.StatusOK, response.Success("corr_test", maintenance.DatabaseCompactResult{
			Status:         "applied",
			PlanID:         "dbcompact_test",
			PlanHash:       "sha256:test",
			DryRun:         false,
			Confirmed:      true,
			Deleted:        map[string]int64{"events.events": 3},
			CompletedAt:    ptrTime(time.Date(2026, 7, 1, 12, 5, 0, 0, time.UTC)),
			EvidenceStatus: "recorded",
		}))
	})
	defer shutdown()

	stdout, stderr, err := executeRootCommand("--socket", socketPath, "database", "compact", "--plan", planPath, "--confirm")
	if err != nil {
		t.Fatalf("database compact apply returned error: %v stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "Database compaction: applied") || !strings.Contains(stdout, "Evidence: recorded") {
		t.Fatalf("apply output missing status/evidence:\n%s", stdout)
	}
}

func ptrTime(value time.Time) *time.Time {
	return &value
}
