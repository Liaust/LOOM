package loomcli

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/response"
	"loom.local/loom/internal/workers"
)

func TestWorkerRunCommandContextKeepsClientDeadlineLongerThanCloudWorkerDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)

	got, cancelRun := workerRunCommandContext(cmd, "main.cloud_snapshot_upload")
	defer cancelRun()
	deadline, hasDeadline := got.Deadline()
	if !hasDeadline {
		t.Fatal("worker run command has no bounded client deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 2*time.Hour || remaining > 3*time.Hour {
		t.Fatalf("worker run client deadline remaining = %s, want more than the 2h cloud worker deadline and at most 3h", remaining)
	}
}

func TestWorkerRunCommandContextDoesNotChangeGenericWorkerLifetime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)

	got, cancelRun := workerRunCommandContext(cmd, "main.main_backup")
	defer cancelRun()
	if got != ctx {
		t.Fatal("generic worker run command changed the caller context")
	}
	if _, hasDeadline := got.Deadline(); hasDeadline {
		t.Fatal("generic worker run command added a cloud-specific deadline")
	}
}

func TestWorkerRepairStaleCommand(t *testing.T) {
	result := workers.StaleRunRepairResult{
		TimedOutRuns:      1,
		ExpiredLeases:     1,
		RepairedInstances: 1,
		RunIDs:            []string{"worker_run_test"},
	}
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/workers/repair-stale" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
		response.WriteJSON(w, http.StatusOK, response.Success("corr_test", result))
	})
	defer shutdown()

	stdout, stderr, err := executeRootCommand("--socket", socketPath, "worker", "repair-stale", "--yes")
	if err != nil {
		t.Fatalf("worker repair-stale returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{"Stale worker repair", "Timed out runs: 1", "Repaired instances: 1", "worker_run_test"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("repair output missing %q:\n%s", want, stdout)
		}
	}
}

func TestWorkerPolicyInspectAndDryRunCommands(t *testing.T) {
	manualFingerprint := "sha256:" + strings.Repeat("a", 64)
	intervalFingerprint := "sha256:" + strings.Repeat("b", 64)
	dailyFingerprint := "sha256:" + strings.Repeat("f", 64)
	requests := 0
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/workers/main.main_backup/policy" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
		state := workers.WorkerPolicyState{
			WorkerInstanceID:  "worker_instance_test",
			WorkerKey:         "main.main_backup",
			Locality:          workers.LocalityMainOwned,
			LifecycleStatus:   workers.LifecycleActive,
			Enabled:           true,
			Policy:            workers.TickPolicy{SchemaVersion: workers.TickPolicySchemaVersion, Mode: workers.TickModeManual},
			PolicyFingerprint: manualFingerprint,
		}
		if requests == 1 {
			if r.Method != http.MethodGet {
				t.Fatalf("inspect method = %s", r.Method)
			}
			response.WriteJSON(w, http.StatusOK, response.Success("corr_test", state))
			return
		}
		if r.Method != http.MethodPost || r.Header.Get("X-Loom-Idempotency-Key") != "" {
			t.Fatalf("dry-run request = %s idempotency=%q", r.Method, r.Header.Get("X-Loom-Idempotency-Key"))
		}
		var input workers.SetWorkerPolicyInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if !input.DryRun || input.Confirm || input.ExpectedPolicyFingerprint != manualFingerprint {
			t.Fatalf("dry-run safety input = %#v", input)
		}
		fingerprint := intervalFingerprint
		switch requests {
		case 2:
			if input.Policy.Mode != workers.TickModeInterval || input.Policy.IntervalSeconds != 30 {
				t.Fatalf("interval dry-run input = %#v", input)
			}
		case 3:
			if input.Policy.Mode != workers.TickModeDailyLocal || input.Policy.LocalTime != "03:15" || input.Policy.Timezone != "Europe/Amsterdam" || input.Policy.IntervalSeconds != 0 || input.Policy.RunOnStartup {
				t.Fatalf("daily dry-run input = %#v", input)
			}
			fingerprint = dailyFingerprint
		default:
			t.Fatalf("unexpected request count %d", requests)
		}
		next := state
		next.Policy = input.Policy
		next.PolicyFingerprint = fingerprint
		response.WriteJSON(w, http.StatusOK, response.Success("corr_test", workers.SetWorkerPolicyResult{
			WorkerInstanceID: state.WorkerInstanceID,
			WorkerKey:        state.WorkerKey,
			DryRun:           true,
			Changed:          true,
			Old:              state,
			New:              next,
			NextRunEvidence:  workers.WorkerPolicyNextRunEvidence{DerivationBasis: "interval policy schedules from apply time plus 30 seconds"},
		}))
	})
	defer shutdown()

	stdout, stderr, err := executeRootCommand("--socket", socketPath, "worker", "policy", "inspect", "main.main_backup")
	if err != nil {
		t.Fatalf("inspect returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{"Worker: main.main_backup", "Policy: manual", "Fingerprint: " + manualFingerprint} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("inspect output missing %q:\n%s", want, stdout)
		}
	}
	stdout, stderr, err = executeRootCommand("--socket", socketPath, "worker", "policy", "set", "main.main_backup", "--dry-run", "--mode", "interval", "--every", "30s", "--expected-policy-fingerprint", manualFingerprint)
	if err != nil {
		t.Fatalf("dry-run returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{"Mode: dry-run", "Old policy: manual", "New policy: interval every 30s", "New fingerprint: " + intervalFingerprint} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, stdout)
		}
	}
	stdout, stderr, err = executeRootCommand("--socket", socketPath, "worker", "policy", "set", "main.main_backup", "--dry-run", "--mode", "daily_local", "--local-time", "03:15", "--timezone", "Europe/Amsterdam", "--expected-policy-fingerprint", manualFingerprint)
	if err != nil {
		t.Fatalf("daily dry-run returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{"Mode: dry-run", "New policy: daily_local at 03:15 Europe/Amsterdam", "New fingerprint: " + dailyFingerprint} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("daily dry-run output missing %q:\n%s", want, stdout)
		}
	}
}

func TestWorkerPolicyApplyRequiresAndSendsSafetyEvidence(t *testing.T) {
	oldFingerprint := "sha256:" + strings.Repeat("c", 64)
	newFingerprint := "sha256:" + strings.Repeat("d", 64)
	for name, args := range map[string][]string{
		"confirmation":           {"worker", "policy", "set", "main.main_backup", "--mode", "manual", "--expected-policy-fingerprint", oldFingerprint},
		"reason":                 {"worker", "policy", "set", "main.main_backup", "--yes", "--mode", "manual", "--expected-policy-fingerprint", oldFingerprint, "--idempotency-key", "idem"},
		"idempotency":            {"worker", "policy", "set", "main.main_backup", "--yes", "--mode", "manual", "--expected-policy-fingerprint", oldFingerprint, "--reason", "restore"},
		"short interval":         {"worker", "policy", "set", "main.main_backup", "--dry-run", "--mode", "interval", "--every", "1s", "--expected-policy-fingerprint", oldFingerprint},
		"fractional interval":    {"worker", "policy", "set", "main.main_backup", "--dry-run", "--mode", "interval", "--every", "1500ms", "--expected-policy-fingerprint", oldFingerprint},
		"missing interval":       {"worker", "policy", "set", "main.main_backup", "--dry-run", "--mode", "interval", "--expected-policy-fingerprint", oldFingerprint},
		"manual with interval":   {"worker", "policy", "set", "main.main_backup", "--dry-run", "--mode", "manual", "--every", "30s", "--expected-policy-fingerprint", oldFingerprint},
		"manual with startup":    {"worker", "policy", "set", "main.main_backup", "--dry-run", "--mode", "manual", "--run-on-startup", "--expected-policy-fingerprint", oldFingerprint},
		"manual with timezone":   {"worker", "policy", "set", "main.main_backup", "--dry-run", "--mode", "manual", "--timezone", "Europe/Amsterdam", "--expected-policy-fingerprint", oldFingerprint},
		"interval with timezone": {"worker", "policy", "set", "main.main_backup", "--dry-run", "--mode", "interval", "--every", "30s", "--timezone", "Europe/Amsterdam", "--expected-policy-fingerprint", oldFingerprint},
		"daily missing timezone": {"worker", "policy", "set", "main.main_backup", "--dry-run", "--mode", "daily_local", "--local-time", "03:15", "--expected-policy-fingerprint", oldFingerprint},
		"daily malformed time":   {"worker", "policy", "set", "main.main_backup", "--dry-run", "--mode", "daily_local", "--local-time", "3:15", "--timezone", "Europe/Amsterdam", "--expected-policy-fingerprint", oldFingerprint},
		"daily with interval":    {"worker", "policy", "set", "main.main_backup", "--dry-run", "--mode", "daily_local", "--local-time", "03:15", "--timezone", "Europe/Amsterdam", "--every", "24h", "--expected-policy-fingerprint", oldFingerprint},
		"malformed fingerprint":  {"worker", "policy", "set", "main.main_backup", "--dry-run", "--mode", "manual", "--expected-policy-fingerprint", "sha256:not-a-digest"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := executeRootCommand(args...)
			if err == nil {
				t.Fatal("unsafe apply unexpectedly succeeded")
			}
		})
	}

	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/workers/main.main_backup/policy" || r.Header.Get("X-Loom-Idempotency-Key") != "idem-restore" {
			t.Fatalf("apply request = %s %s idempotency=%q", r.Method, r.URL.Path, r.Header.Get("X-Loom-Idempotency-Key"))
		}
		var input workers.SetWorkerPolicyInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if !input.Confirm || input.DryRun || input.Reason != "restore captured policy" || input.ExpectedPolicyFingerprint != oldFingerprint || input.Policy.Mode != workers.TickModeManual {
			t.Fatalf("apply input = %#v", input)
		}
		response.WriteJSON(w, http.StatusOK, response.Success("corr_test", workers.SetWorkerPolicyResult{
			WorkerInstanceID: "worker_instance_test", WorkerKey: "main.main_backup", Applied: true, Changed: true,
			Old: workers.WorkerPolicyState{WorkerKey: "main.main_backup", Policy: workers.TickPolicy{Mode: workers.TickModeInterval, IntervalSeconds: 30}, PolicyFingerprint: oldFingerprint},
			New: workers.WorkerPolicyState{WorkerKey: "main.main_backup", Policy: workers.TickPolicy{Mode: workers.TickModeManual}, PolicyFingerprint: newFingerprint},
		}))
	})
	defer shutdown()
	stdout, stderr, err := executeRootCommand("--socket", socketPath, "worker", "policy", "set", "main.main_backup", "--yes", "--mode", "manual", "--expected-policy-fingerprint", oldFingerprint, "--reason", "restore captured policy", "--idempotency-key", "idem-restore")
	if err != nil {
		t.Fatalf("apply returned error: %v stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "Mode: apply") || !strings.Contains(stdout, "New fingerprint: "+newFingerprint) {
		t.Fatalf("apply output = %s", stdout)
	}
}

func TestWorkerPolicyRejectsManualIntervalFlagsBeforeTransport(t *testing.T) {
	fingerprint := "sha256:" + strings.Repeat("e", 64)
	requests := 0
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		t.Fatalf("manual policy conflict reached transport: %s %s", r.Method, r.URL.Path)
	})
	defer shutdown()

	for name, extra := range map[string][]string{
		"every":          {"--every", "30s"},
		"run on startup": {"--run-on-startup"},
		"local time":     {"--local-time", "03:15"},
		"timezone":       {"--timezone", "Europe/Amsterdam"},
	} {
		t.Run(name, func(t *testing.T) {
			args := []string{"--socket", socketPath, "worker", "policy", "set", "main.main_backup", "--dry-run", "--mode", "manual", "--expected-policy-fingerprint", fingerprint}
			args = append(args, extra...)
			_, stderr, err := executeRootCommand(args...)
			if err == nil || !strings.Contains(stderr, "Manual mode cannot be combined") {
				t.Fatalf("error=%v stderr=%q", err, stderr)
			}
		})
	}
	if requests != 0 {
		t.Fatalf("manual policy conflicts made %d transport requests", requests)
	}
}
