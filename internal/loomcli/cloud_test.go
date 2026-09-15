package loomcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/cloudstorage"
	"loom.local/loom/internal/config"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/restoreauthority"
	"loom.local/loom/internal/workers"
)

func TestCloudRestoreDrillContextBoundsAndInheritsCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx, done := cloudSnapshotRestoreDrillContext(parent)
	defer done()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= 119*time.Minute || time.Until(deadline) > 2*time.Hour {
		t.Fatal("restore CLI deadline is not two hours")
	}
	cancel()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("restore CLI discarded cancellation")
	}
	shortParent, stop := context.WithTimeout(context.Background(), time.Minute)
	defer stop()
	shortCtx, shortStop := cloudSnapshotRestoreDrillContext(shortParent)
	defer shortStop()
	want, _ := shortParent.Deadline()
	if got, _ := shortCtx.Deadline(); !got.Equal(want) {
		t.Fatal("restore CLI extended parent deadline")
	}
}

func TestCloudRestoreDrillDefaultDelegatesExactRequestAndRendersOutput(t *testing.T) {
	t.Parallel()
	requests := make(chan localclient.CloudSnapshotRestoreDrillInput, 6)
	socket, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/cloud/snapshot/restore-drill/live" {
			t.Errorf("wrong restore route: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
			return
		}
		var input localclient.CloudSnapshotRestoreDrillInput
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		requests <- input
		_ = json.NewEncoder(w).Encode(response.Success("corr_service_restore", cloudstorage.CloudRestoreDrillResult{Status: "succeeded", Ref: input.Ref, DryRun: input.DryRun}))
	})
	defer shutdown()
	for _, format := range []string{"--json", "--plain", ""} {
		for _, dryRun := range []bool{false, true} {
			args := []string{"--socket", socket, "cloud", "snapshot", "restore-drill", "history-exact", "--node-id", "main", "--target-database", "loom_restore_drill_exact", "--provenance-target-database", "loom_provenance_restore_drill_exact"}
			if format != "" {
				args = append(args, format)
			}
			if dryRun {
				args = append(args, "--dry-run")
			}
			stdout, stderr, err := executeRootCommand(args...)
			if err != nil {
				t.Fatalf("restore err=%v stdout=%s stderr=%s", err, stdout, stderr)
			}
			got := <-requests
			if got.ConfigPath != cloudstorage.DefaultConfigPath || got.Ref != "history-exact" || got.NodeID != "main" || got.TargetDatabase != "loom_restore_drill_exact" || got.ProvenanceTargetDatabase != "loom_provenance_restore_drill_exact" || got.DryRun != dryRun {
				t.Fatalf("delegated selectors = %+v", got)
			}
			if !strings.Contains(stdout, "succeeded") {
				t.Fatalf("missing result: %s", stdout)
			}
			if format == "--json" {
				var envelope response.Envelope[cloudstorage.CloudRestoreDrillResult]
				if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
					t.Fatal(err)
				}
				if envelope.Meta.CorrelationID != "corr_service_restore" || envelope.Data.DryRun != dryRun {
					t.Fatal("daemon envelope changed")
				}
			}
		}
	}
}

func TestCloudRestoreDrillLoadsLocalCloudConfigOnlyWithExplicitLocal(t *testing.T) {
	t.Parallel()
	socket, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(response.Success("corr_service", cloudstorage.CloudRestoreDrillResult{Status: "succeeded"}))
	})
	defer shutdown()
	for _, local := range []bool{false, true} {
		calls := 0
		localFailure := errors.New("local credential loader must not run during delegation")
		cmd := cloudSnapshotRestoreDrillCommand(&options{socketPath: socket, jsonOutput: true}, func(_ *options, path string) (config.Config, cloudstorage.Config, error) {
			calls++
			return config.Config{}, cloudstorage.Config{}, localFailure
		})
		var output bytes.Buffer
		cmd.SetOut(&output)
		cmd.SetErr(&output)
		args := []string{"history-exact"}
		if local {
			args = append(args, "--local")
		}
		cmd.SetArgs(args)
		err := cmd.Execute()
		if local {
			if calls != 1 || !errors.Is(err, localFailure) {
				t.Fatalf("explicit local calls=%d err=%v", calls, err)
			}
		} else if calls != 0 || err != nil {
			t.Fatalf("default delegation loaded local config: calls=%d err=%v", calls, err)
		}
	}
}

func TestCloudRestoreDrillDefaultRefusesOverridesBeforeLocalLoadOrDelegation(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		args []string
		code string
	}{
		{[]string{"--cloud-config", "/tmp/unreadable-cloud.json"}, "cloud.config_override_forbidden"},
		{[]string{"--cloud-config", "/etc/loom/cloud/../cloud/config.json"}, "cloud.config_override_forbidden"},
		{[]string{"--cloud-config", " /etc/loom/cloud/config.json"}, "cloud.config_override_forbidden"},
		{[]string{"--state-dir", "/tmp/arbitrary-server-path"}, "cloud.state_dir_override_forbidden"},
	} {
		cmd := cloudSnapshotRestoreDrillCommand(&options{socketPath: "/missing-daemon.sock", jsonOutput: true}, func(_ *options, _ string) (config.Config, cloudstorage.Config, error) {
			t.Fatal("override reached local configuration")
			return config.Config{}, cloudstorage.Config{}, nil
		})
		var output bytes.Buffer
		cmd.SetOut(&output)
		cmd.SetErr(&output)
		cmd.SetArgs(append([]string{"history-exact"}, test.args...))
		if err := cmd.Execute(); err == nil {
			t.Fatal("override was accepted")
		}
		if !strings.Contains(output.String(), test.code) {
			t.Fatalf("wrong override error: %s", output.String())
		}
	}
}

func TestCloudRestoreDrillExplicitLocalUsesDisposableConfigWithoutDaemon(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := cloudstorage.DefaultConfig()
	cfg.StateDir = filepath.Join(dir, "config-state")
	configPath := filepath.Join(dir, "cloud.json")
	raw, _ := json.Marshal(cfg)
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := executeRootCommand("--socket", filepath.Join(dir, "missing-daemon.sock"), "--json", "cloud", "snapshot", "restore-drill", "history-exact", "--local", "--cloud-config", configPath, "--state-dir", filepath.Join(dir, "staging"), "--dry-run")
	if err == nil || err.Error() != "cloud storage is disabled" {
		t.Fatalf("local config was not used: err=%v stdout=%s stderr=%s", err, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "staging")); !os.IsNotExist(err) {
		t.Fatalf("disabled local drill created staging: %v", err)
	}
}

func TestCloudRestoreDrillDelegatedTypedFailureDoesNotFallbackOrLeak(t *testing.T) {
	t.Parallel()
	socket, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		response.WriteJSON(w, http.StatusInternalServerError, response.Failure("corr_restore_service_failure", loomerrors.Wrap("backup.restore_authority.database_create_failed", "backup", "loom_restore_drill_exact", "Strict restore authority failed at the database_create stage.", errors.New("credential-MUST-NOT-LEAK"))))
	})
	defer shutdown()
	for _, format := range []string{"--json", "--plain", "--verbose"} {
		stdout, stderr, err := executeRootCommand("--socket", socket, format, "cloud", "snapshot", "restore-drill", "history-exact")
		if err == nil {
			t.Fatal("typed failure became success")
		}
		combined := stdout + stderr
		for _, want := range []string{"backup.restore_authority.database_create_failed", "loom_restore_drill_exact", "corr_restore_service_failure", "database_create stage"} {
			if !strings.Contains(combined, want) {
				t.Fatalf("lost %s: %s", want, combined)
			}
		}
		if strings.Contains(combined, "credential-MUST-NOT-LEAK") || strings.Contains(combined, "runtime.error") {
			t.Fatalf("typed failure truth lost: %s", combined)
		}
	}
}

func TestCloudStatusCommandExposesQuietingFlags(t *testing.T) {
	t.Parallel()

	cmd := NewRootCommand()
	statusCmd, _, err := cmd.Find([]string{"cloud", "status"})
	if err != nil {
		t.Fatalf("Find cloud status returned error: %v", err)
	}
	for _, name := range []string{"live", "force-live", "cached", "local"} {
		if statusCmd.Flags().Lookup(name) == nil {
			t.Fatalf("cloud status missing --%s flag", name)
		}
	}

	doctorCmd, _, err := cmd.Find([]string{"cloud", "doctor"})
	if err != nil {
		t.Fatalf("Find cloud doctor returned error: %v", err)
	}
	for _, name := range []string{"live", "force-live", "local"} {
		if doctorCmd.Flags().Lookup(name) == nil {
			t.Fatalf("cloud doctor missing --%s flag", name)
		}
	}

	backendStatusCmd, _, err := cmd.Find([]string{"cloud", "snapshot", "backend", "status"})
	if err != nil {
		t.Fatalf("Find cloud snapshot backend status returned error: %v", err)
	}
	for _, name := range []string{"live", "force-live", "cached", "local"} {
		if backendStatusCmd.Flags().Lookup(name) == nil {
			t.Fatalf("cloud snapshot backend status missing --%s flag", name)
		}
	}

	backendInitCmd, _, err := cmd.Find([]string{"cloud", "snapshot", "backend", "init"})
	if err != nil {
		t.Fatalf("Find cloud snapshot backend init returned error: %v", err)
	}
	for _, name := range []string{"confirm", "local"} {
		if backendInitCmd.Flags().Lookup(name) == nil {
			t.Fatalf("cloud snapshot backend init missing --%s flag", name)
		}
	}

	snapshotStatusCmd, _, err := cmd.Find([]string{"cloud", "snapshot", "status"})
	if err != nil {
		t.Fatalf("Find cloud snapshot status returned error: %v", err)
	}
	for _, name := range []string{"live", "force-live", "local"} {
		if snapshotStatusCmd.Flags().Lookup(name) == nil {
			t.Fatalf("cloud snapshot status missing --%s flag", name)
		}
	}

	snapshotListCmd, _, err := cmd.Find([]string{"cloud", "snapshot", "list"})
	if err != nil {
		t.Fatalf("Find cloud snapshot list returned error: %v", err)
	}
	if snapshotListCmd.Flags().Lookup("local") == nil {
		t.Fatal("cloud snapshot list missing --local flag")
	}

	snapshotVerifyCmd, _, err := cmd.Find([]string{"cloud", "snapshot", "verify", "latest"})
	if err != nil {
		t.Fatalf("Find cloud snapshot verify returned error: %v", err)
	}
	for _, name := range []string{"local", "profile", "max-duration"} {
		if snapshotVerifyCmd.Flags().Lookup(name) == nil {
			t.Fatalf("cloud snapshot verify missing --%s flag", name)
		}
	}

	for _, args := range [][]string{
		{"cloud", "snapshot", "retention", "status"},
		{"cloud", "snapshot", "retention", "plan"},
		{"cloud", "snapshot", "retention", "apply"},
	} {
		retentionCmd, _, err := cmd.Find(args)
		if err != nil {
			t.Fatalf("Find %v returned error: %v", args, err)
		}
		if retentionCmd.Flags().Lookup("local") == nil {
			t.Fatalf("%v missing --local flag", args)
		}
	}
	applyCmd, _, err := cmd.Find([]string{"cloud", "snapshot", "retention", "apply"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"plan", "confirm-digest", "compact"} {
		if applyCmd.Flags().Lookup(name) == nil {
			t.Fatalf("retention apply missing --%s flag", name)
		}
	}

	restoreCmd, _, err := cmd.Find([]string{"cloud", "snapshot", "restore-drill", "history-test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"target-database", "provenance-target-database", "keep", "dry-run", "local"} {
		if restoreCmd.Flags().Lookup(name) == nil {
			t.Fatalf("restore drill missing --%s flag", name)
		}
	}
	if restoreCmd.Flags().Lookup("keep").DefValue != "false" {
		t.Fatalf("restore drill --keep default = %q", restoreCmd.Flags().Lookup("keep").DefValue)
	}
	for _, forbidden := range []string{"active-database", "owner", "provenance-owner"} {
		if restoreCmd.Flags().Lookup(forbidden) != nil {
			t.Fatalf("restore drill exposes caller-selected --%s", forbidden)
		}
	}
}

func TestCloudRestoreDrillWiresReviewedAuthorityConfigurationWithoutSerializingConnection(t *testing.T) {
	secretURL := "postgresql://loom_provenance:credential-MUST-NOT-LEAK@localhost/loom_provenance"
	input := cloudstorage.CloudRestoreDrillInput{}
	runtimeConfig := config.Config{
		ProvenanceDBURL:             secretURL,
		RestoreAuthoritySocketPath:  "/tmp/restore-authority.sock",
		RestoreAuthoritySocketOwner: strconv.Itoa(os.Getuid()),
		RestoreAuthoritySocketGroup: strconv.Itoa(os.Getgid()),
		RestoreActivatorUser:        strconv.Itoa(os.Getuid()),
		RestoreExecutorUser:         "postgres",
		RestoreOperationalDatabase:  "loom_main", RestoreOperationalOwner: "loom",
		RestoreProvenanceDatabase: "loom_provenance", RestoreProvenanceOwner: "loom_provenance",
	}
	if err := configureCloudRestoreAuthority(&input, runtimeConfig, "loom_provenance_restore_drill_cli"); err != nil {
		t.Fatal(err)
	}
	if input.RestoreAuthority == nil || input.ActiveDatabase != "loom_main" || input.Owner != "loom" || input.ProvenanceDatabaseURL != secretURL || input.ProvenanceTargetDatabase != "loom_provenance_restore_drill_cli" || input.ProvenanceActiveDatabase != "loom_provenance" || input.ProvenanceOwner != "loom_provenance" || input.ProvenanceRestore != nil {
		t.Fatalf("CLI restore authority wiring = %#v", input)
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secretURL) || strings.Contains(string(raw), "credential-MUST-NOT-LEAK") || strings.Contains(string(raw), "postgresql://") {
		t.Fatalf("CLI restore input serialized credentials: %s", raw)
	}
}

func TestCloudRestoreDrillResolvesSocketActivatorIndependentlyFromExecutor(t *testing.T) {
	input := cloudstorage.CloudRestoreDrillInput{}
	runtimeConfig := config.Config{
		RestoreAuthoritySocketPath:  "/tmp/restore-authority.sock",
		RestoreAuthoritySocketOwner: strconv.Itoa(os.Getuid()),
		RestoreAuthoritySocketGroup: strconv.Itoa(os.Getgid()),
		RestoreActivatorUser:        "activator-not-present-on-this-host",
		RestoreExecutorUser:         strconv.Itoa(os.Getuid()),
		RestoreOperationalDatabase:  "loom_main",
		RestoreOperationalOwner:     "loom",
		RestoreProvenanceDatabase:   "loom_provenance",
		RestoreProvenanceOwner:      "loom_provenance",
	}
	if err := configureCloudRestoreAuthority(&input, runtimeConfig, "loom_provenance_restore_drill_activator"); err == nil || !strings.Contains(err.Error(), "resolve Unix user") {
		t.Fatalf("socket activator identity was not resolved independently from executor: %v", err)
	}
}

func TestCloudRestoreDrillDryRunDoesNotRequireLocalAuthorityIdentity(t *testing.T) {
	input := cloudstorage.CloudRestoreDrillInput{DryRun: true}
	runtimeConfig := config.Config{
		RestoreAuthoritySocketPath:  "/run/loom-restore-authority/restore-authority.sock",
		RestoreAuthoritySocketOwner: "identity-not-present-on-this-host",
		RestoreAuthoritySocketGroup: "group-not-present-on-this-host",
		RestoreActivatorUser:        "activator-not-present-on-this-host",
		RestoreExecutorUser:         "executor-not-present-on-this-host",
		RestoreOperationalDatabase:  "loom_main",
		RestoreOperationalOwner:     "loom",
		RestoreProvenanceDatabase:   "loom_provenance",
		RestoreProvenanceOwner:      "loom_provenance",
	}
	if err := configureCloudRestoreAuthority(&input, runtimeConfig, "loom_provenance_restore_drill_dry"); err != nil {
		t.Fatal(err)
	}
	if input.RestoreAuthority != nil || input.ActiveDatabase != "loom_main" || input.ProvenanceTargetDatabase != "loom_provenance_restore_drill_dry" {
		t.Fatalf("dry-run authority wiring = %#v", input)
	}
}

func TestCloudRestoreDrillKeepIsRefusedBeforeConfigurationOrFetch(t *testing.T) {
	stdout, stderr, err := executeRootCommand("--json", "cloud", "snapshot", "restore-drill", "history-test", "--keep")
	if err == nil || !strings.Contains(err.Error(), "mandatory restore authority cleanup") {
		t.Fatalf("--keep output=%q stderr=%q err=%v", stdout, stderr, err)
	}
}

func TestCloudRestoreFailureCLIExposesStableTypedMetadataWithoutRuntimeDetail(t *testing.T) {
	target := "loom_restore_drill_cli_failure"
	authorityErr := &restoreauthority.AuthorityError{
		Stage:   restoreauthority.FailureStageDatabaseCreate,
		Code:    restoreauthority.ErrorDatabaseCreateFailed,
		Message: "Disposable database creation failed.",
		Result: restoreauthority.Result{
			Status: "failed", Kind: restoreauthority.KindOperational, Database: target,
			FailureStage:     restoreauthority.FailureStageDatabaseCreate,
			ErrorCode:        restoreauthority.ErrorDatabaseCreateFailed,
			CleanupAttempted: true, CleanupSucceeded: true,
		},
		Cause: errors.New("postgresql://owner:credential-MUST-NOT-LEAK@localhost/database /private/staging/dump"),
	}
	typed := loomerrors.Wrap(
		"backup.restore_authority.database_create_failed", "backup", target,
		"Strict restore authority failed at the database_create stage.", authorityErr,
	)

	for _, jsonOutput := range []bool{true, false} {
		cmd := &cobra.Command{Use: "restore-drill"}
		var stdout, stderr bytes.Buffer
		cmd.SetOut(&stdout)
		cmd.SetErr(&stderr)
		opts := &options{jsonOutput: jsonOutput, plainOutput: !jsonOutput}
		if err := renderError(cmd, opts, "corr_typed_restore_failure", typed); err == nil {
			t.Fatal("renderError did not return the typed failure")
		}
		combined := stdout.String() + stderr.String()
		for _, want := range []string{
			"backup.restore_authority.database_create_failed", "backup", target, "corr_typed_restore_failure",
		} {
			if !strings.Contains(combined, want) {
				t.Fatalf("json=%t output does not contain %q: %s", jsonOutput, want, combined)
			}
		}
		for _, forbidden := range []string{"credential-MUST-NOT-LEAK", "postgresql://", "/private/staging"} {
			if strings.Contains(combined, forbidden) {
				t.Fatalf("json=%t output leaked %q: %s", jsonOutput, forbidden, combined)
			}
		}
	}
}

func TestReadCloudRetentionPlanAcceptsExactPlanAndEnvelope(t *testing.T) {
	plan := cloudstorage.SnapshotRetentionPlan{Status: cloudstorage.RetentionStatusPlanned, PlanDigest: strings.Repeat("a", 64)}
	for _, payload := range []any{plan, map[string]any{"data": plan}} {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "plan.json")
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		loaded, err := readCloudRetentionPlan(path)
		if err != nil || loaded.PlanDigest != plan.PlanDigest {
			t.Fatalf("loaded plan = %#v err=%v", loaded, err)
		}
	}
}

func TestRenderCloudStatusIncludesCachedRemoteState(t *testing.T) {
	t.Parallel()

	lastFailure := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	nextProbe := lastFailure.Add(30 * time.Minute)
	report := cloudstorage.StatusReport{
		Status: "cooling_down",
		Mode:   "cached",
		Config: cloudstorage.ConfigSummary{
			Path:            "/var/lib/loom/cloud/config.json",
			Exists:          true,
			RemoteName:      "loom-cloud",
			RemoteRoot:      "loom",
			SnapshotBackend: "borg",
		},
		RemoteState: &cloudstorage.RemoteState{
			State:               cloudstorage.RemoteStateCoolingDown,
			LastFailureAt:       &lastFailure,
			NextLiveCheckAfter:  &nextProbe,
			LastErrorClass:      cloudstorage.RemoteErrorConnectionRefused,
			LiveProbeSkipReason: cloudstorage.RemoteStateCoolingDown,
		},
	}
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)

	renderCloudStatus(cmd, report)
	rendered := out.String()
	for _, want := range []string{
		"Mode: cached",
		"Remote state: cooling_down",
		"Last failure: 2026-06-20T10:00:00Z",
		"Next live check: 2026-06-20T10:30:00Z",
		"Last error class: connection_refused",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("render missing %q:\n%s", want, rendered)
		}
	}
}

func TestCloudStatusLocalJSONUsesLocalMetadata(t *testing.T) {
	t.Parallel()

	stdout, stderr, err := executeRootCommand("--json", "cloud", "status", "--local", "--cloud-config", filepath.Join(t.TempDir(), "missing-cloud.json"))
	if err != nil {
		t.Fatalf("cloud status --local returned error: %v stderr=%s", err, stderr)
	}
	var envelope response.Envelope[cloudstorage.StatusReport]
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("decode response: %v\n%s", err, stdout)
	}
	if envelope.Meta.Source != "local-cli" {
		t.Fatalf("meta source = %q, want local-cli", envelope.Meta.Source)
	}
	if envelope.Meta.Freshness != "local" {
		t.Fatalf("meta freshness = %q, want local", envelope.Meta.Freshness)
	}
}

func TestCloudSnapshotBackendInitRequiresConfirmJSON(t *testing.T) {
	t.Parallel()

	stdout, stderr, err := executeRootCommand("--json", "cloud", "snapshot", "backend", "init")
	if err == nil {
		t.Fatal("backend init without --confirm should return an error")
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("json error should not write stderr, got: %s", stderr)
	}
	var envelope response.ErrorEnvelope
	if decodeErr := json.Unmarshal([]byte(stdout), &envelope); decodeErr != nil {
		t.Fatalf("decode error response: %v output=%s", decodeErr, stdout)
	}
	if envelope.Error.Code != "cloud.snapshot_backend_init_confirm_required" {
		t.Fatalf("error code = %q, want cloud.snapshot_backend_init_confirm_required", envelope.Error.Code)
	}
}

func TestCloudSnapshotPushDryRunUsesManualWorkerWithoutLocalBackupGeneration(t *testing.T) {
	t.Parallel()

	stdout, stderr, err := executeRootCommand(
		"--json",
		"cloud", "snapshot", "push",
		"--allow-non-production",
		"--dry-run",
	)
	if err != nil {
		t.Fatalf("snapshot push dry-run returned error: %v stderr=%s", err, stderr)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("json dry-run should not write stderr, got: %s", stderr)
	}
	var envelope response.Envelope[map[string]any]
	if decodeErr := json.Unmarshal([]byte(stdout), &envelope); decodeErr != nil {
		t.Fatalf("decode dry-run response: %v output=%s", decodeErr, stdout)
	}
	if envelope.Data["worker"] != "main.cloud_snapshot_upload" || envelope.Data["phase"] != "direct_borg_archive" || envelope.Data["creates_complete_local_user_data_generation"] != false {
		t.Fatalf("dry-run plan = %#v", envelope.Data)
	}
}

func TestCloudSnapshotPushCommandContextOutlivesCloudWorkerDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	got, cancelPush := cloudSnapshotPushCommandContext(ctx)
	defer cancelPush()
	deadline, hasDeadline := got.Deadline()
	if !hasDeadline {
		t.Fatal("cloud snapshot push has no bounded client deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 2*time.Hour || remaining > 3*time.Hour {
		t.Fatalf("cloud snapshot push client deadline remaining = %s, want more than the 2h worker deadline and at most 3h", remaining)
	}
}

func TestCloudSnapshotPushEmitsExactBackupOperationSelectorMetadata(t *testing.T) {
	t.Parallel()

	selected := ids.NewMaintenanceOperationID()
	requests := make(chan workers.RunOnceInput, 2)
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/cloud/snapshot/run" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var input workers.RunOnceInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		requests <- input
		_ = json.NewEncoder(w).Encode(response.Success("corr_test", workers.RunOnceResult{Run: workers.WorkerRun{WorkerRunID: "worker_run_cloud", ResultSummaryJSON: json.RawMessage(`{"status":"succeeded"}`)}}))
	})
	defer shutdown()

	for _, test := range []struct {
		name     string
		args     []string
		selected string
	}{
		{name: "selected", args: []string{"--backup-operation-id", selected}, selected: selected},
		{name: "default_latest", args: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := []string{"--socket", socketPath, "--json", "cloud", "snapshot", "push", "--allow-non-production"}
			args = append(args, test.args...)
			if stdout, stderr, err := executeRootCommand(args...); err != nil {
				t.Fatalf("snapshot push: %v stdout=%s stderr=%s", err, stdout, stderr)
			}
			input := <-requests
			var metadata map[string]any
			if err := json.Unmarshal(input.Metadata, &metadata); err != nil {
				t.Fatal(err)
			}
			if metadata["source"] != "loom_cloud_cli" || metadata["production_requested"] != false {
				t.Fatalf("request metadata = %#v", metadata)
			}
			gotSelected, present := metadata["backup_operation_id"]
			if test.selected == "" {
				if present || len(metadata) != 2 {
					t.Fatalf("default request metadata = %#v", metadata)
				}
			} else if !present || gotSelected != test.selected || len(metadata) != 3 {
				t.Fatalf("selected request metadata = %#v", metadata)
			}
			if strings.Contains(string(input.Metadata), "package_path") || strings.Contains(string(input.Metadata), "manifest_sha256") {
				t.Fatalf("request metadata exposed package authority: %s", input.Metadata)
			}
		})
	}
}

func TestCloudSnapshotPushRejectsInvalidBackupOperationSelectorBeforeWorkerCall(t *testing.T) {
	t.Parallel()

	workerCalls := 0
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		workerCalls++
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer shutdown()
	valid := ids.NewMaintenanceOperationID()
	for _, value := range []string{"", " " + valid, "maintenance_operation_bad", "maintenance_finding_01M19BDYH0HSGQ2JW9B21NXZ38"} {
		_, _, err := executeRootCommand("--socket", socketPath, "cloud", "snapshot", "push", "--allow-non-production", "--backup-operation-id="+value)
		if err == nil || !strings.Contains(err.Error(), "exact maintenance operation ID") {
			t.Fatalf("selector %q error = %v", value, err)
		}
	}
	if workerCalls != 0 {
		t.Fatalf("invalid selectors reached worker %d times", workerCalls)
	}
}

func TestCloudSnapshotPushRejectsHistoricalArbitraryRootFlags(t *testing.T) {
	t.Parallel()

	_, _, err := executeRootCommand("cloud", "snapshot", "push", "--allow-non-production", "--dry-run", "--backup-root", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "unknown flag: --backup-root") {
		t.Fatalf("historical arbitrary backup root flag error = %v", err)
	}
}

func TestCloudSnapshotVerifyLocalMissingConfigUsesSpecificError(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "missing-cloud.json")
	stdout, stderr, err := executeRootCommand(
		"--json",
		"cloud", "snapshot", "verify", "latest",
		"--local",
		"--cloud-config", configPath,
	)
	if err == nil {
		t.Fatal("snapshot verify --local without config should return an error")
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("json error should not write stderr, got: %s", stderr)
	}
	var envelope response.ErrorEnvelope
	if decodeErr := json.Unmarshal([]byte(stdout), &envelope); decodeErr != nil {
		t.Fatalf("decode error response: %v output=%s", decodeErr, stdout)
	}
	if envelope.Error.Code != "cloud.snapshot_verify_local_config_missing" {
		t.Fatalf("error code = %q, want cloud.snapshot_verify_local_config_missing", envelope.Error.Code)
	}
	if envelope.Error.Domain != "cloud" {
		t.Fatalf("error domain = %q, want cloud", envelope.Error.Domain)
	}
	if envelope.Error.Target != configPath {
		t.Fatalf("error target = %q, want %q", envelope.Error.Target, configPath)
	}
	if !strings.Contains(envelope.Error.Summary, "omit --local") {
		t.Fatalf("error summary missing daemon guidance: %q", envelope.Error.Summary)
	}
}

func TestCloudSnapshotVerifyLocalServiceContextErrorUsesSpecificCode(t *testing.T) {
	t.Parallel()

	cause := errors.New("borg --lock-wait 5 list --json failed: open cloud remote lock: open /var/lib/loom/cloud/locks/storagebox.lock: permission denied")
	err := cloudSnapshotVerifyLocalError("latest", cause)
	var coded *loomerrors.Error
	if !errors.As(err, &coded) {
		t.Fatalf("wrapped error does not expose code metadata: %T %[1]v", err)
	}
	if coded.Code != "cloud.snapshot_verify_service_context_required" {
		t.Fatalf("error code = %q, want cloud.snapshot_verify_service_context_required", coded.Code)
	}
	if coded.Domain != "cloud" {
		t.Fatalf("error domain = %q, want cloud", coded.Domain)
	}
	if coded.Target != "latest" {
		t.Fatalf("error target = %q, want latest", coded.Target)
	}
	if hint := cloudstorage.ServiceContextHint(err); hint == "" {
		t.Fatal("wrapped local verify error should retain service-context hint")
	}
}

func TestCloudSnapshotVerifyProfilesAreExclusiveAndTyped(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"cloud", "snapshot", "verify", "latest", "--profile", "rolling_repository", "--max-duration", "1m"},
		{"cloud", "snapshot", "verify", "--profile", "rolling_repository"},
		{"cloud", "snapshot", "verify", "exact", "--profile", "metadata", "--max-duration", "1m"},
		{"cloud", "snapshot", "verify", "latest", "--profile", "archive_data"},
		{"cloud", "snapshot", "verify", "exact", "--profile", "archive_data", "--max-duration", "1m"},
		{"cloud", "snapshot", "verify", "exact", "--profile", "full"},
	} {
		if _, _, err := executeRootCommand(args...); err == nil {
			t.Fatalf("invalid profile args were accepted: %v", args)
		}
	}

	var requestBody string
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		payload, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		requestBody = string(payload)
		response.WriteJSON(w, http.StatusOK, response.Success("corr_test", cloudstorage.SnapshotVerifyResult{
			Status: cloudstorage.SnapshotStatusSucceeded, Profile: cloudstorage.SnapshotVerifyProfileRollingRepository,
			Coverage: cloudstorage.SnapshotVerifyCoverageRepositoryTimeBounded, MaxDurationSeconds: 600,
			Backend: cloudstorage.SnapshotBackendBorg, Repository: "test-repository", RemoteURI: "borg:test-repository",
			CheckedAt: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), Checks: map[string]string{"borg_repository_check": cloudstorage.SnapshotStatusSucceeded},
		}))
	})
	defer shutdown()
	stdout, stderr, err := executeRootCommand("--socket", socketPath, "--json", "cloud", "snapshot", "verify", "--profile", "rolling_repository", "--max-duration", "10m")
	if err != nil || strings.TrimSpace(stderr) != "" {
		t.Fatalf("rolling verify failed: err=%v stderr=%s stdout=%s", err, stderr, stdout)
	}
	if !strings.Contains(requestBody, `"profile":"rolling_repository"`) || !strings.Contains(requestBody, `"max_duration_seconds":600`) || strings.Contains(requestBody, `"ref"`) {
		t.Fatalf("rolling request body = %s", requestBody)
	}
	var envelope response.Envelope[cloudstorage.SnapshotVerifyResult]
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Profile != cloudstorage.SnapshotVerifyProfileRollingRepository || envelope.Data.Coverage != cloudstorage.SnapshotVerifyCoverageRepositoryTimeBounded {
		t.Fatalf("rolling response = %#v", envelope.Data)
	}
}

func TestCloudRestoreDrillFetchFailureReceiptLocatorSurvivesAllFormats(t *testing.T) {
	t.Parallel()
	calls := 0
	socket, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		response.WriteJSON(w, http.StatusInternalServerError, response.Failure("corr_fetch_failure", loomerrors.Wrap("cloud.fetch_extraction_failed", "cloud", "restore-drill", "Cloud restore failed at fetch_extraction before database operations. Failure evidence saved in 20260906T120000Z-history-fixture-abc/restore-failure.json.", errors.New("canary-secret"))))
	})
	defer shutdown()
	for _, format := range []string{"--json", "--plain", "--verbose"} {
		stdout, stderr, err := executeRootCommand("--socket", socket, format, "cloud", "snapshot", "restore-drill", "history-fixture")
		if err == nil {
			t.Fatal("nonzero fetch became success")
		}
		combined := stdout + stderr
		for _, want := range []string{"cloud.fetch_extraction_failed", "fetch_extraction", "corr_fetch_failure", "20260906T120000Z-history-fixture-abc/restore-failure.json"} {
			if !strings.Contains(combined, want) {
				t.Fatalf("missing %s: %s", want, combined)
			}
		}
		if strings.Contains(combined, "canary-secret") {
			t.Fatal("cause leaked")
		}
	}
	if calls != 3 {
		t.Fatal("implicit retry or fallback")
	}
}

func TestCloudRestoreCleanupCLIDelegatesExactConfirmation(t *testing.T) {
	requests := make(chan cloudstorage.RestoreCleanupApplyInput, 8)
	socket, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || !strings.HasPrefix(r.URL.Path, "/v1/cloud/snapshot/restore-cleanup/") {
			t.Error("unexpected route")
		}
		var input cloudstorage.RestoreCleanupApplyInput
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if err := d.Decode(&input); err != nil {
			t.Error(err)
		}
		requests <- input
		_ = json.NewEncoder(w).Encode(response.Success("corr_cleanup_service", cloudstorage.RestoreCleanupResult{Status: "planned", Attempt: input.Attempt, PlanDigest: strings.Repeat("a", 64), DryRun: input.DryRun}))
	})
	defer shutdown()
	for _, format := range []string{"--json", "--plain", ""} {
		for _, verb := range []string{"plan", "apply"} {
			args := []string{"--socket", socket, "cloud", "snapshot", "restore-cleanup", verb, "exact-attempt"}
			if format != "" {
				args = append(args, format)
			}
			if verb == "apply" {
				args = append(args, "--confirm-digest", strings.Repeat("a", 64), "--yes", "--dry-run")
			}
			out, stderr, err := executeRootCommand(args...)
			if err != nil {
				t.Fatal(out, stderr, err)
			}
			input := <-requests
			if input.Attempt != "exact-attempt" || (verb == "apply" && (!input.Yes || !input.DryRun || input.ConfirmDigest != strings.Repeat("a", 64))) {
				t.Fatal("confirmation wire", input)
			}
			if !strings.Contains(out, "planned") {
				t.Fatal("missing result", out)
			}
		}
	}
	for _, args := range [][]string{{"--state-dir", "/tmp/private"}, {"--cloud-config", "/tmp/private"}, {"--local"}, {"--keep"}} {
		_, _, err := executeRootCommand(append([]string{"--socket", socket, "cloud", "snapshot", "restore-cleanup", "apply", "exact-attempt"}, args...)...)
		if err == nil {
			t.Fatal("unscoped flag accepted")
		}
	}
	if len(requests) != 0 {
		t.Fatal("invalid flags reached daemon")
	}
}

func TestCloudRestoreCleanupCLIRefusalNoFallback(t *testing.T) {
	calls := 0
	socket, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		response.WriteJSON(w, 409, response.Failure("corr_cleanup_refused", loomerrors.New("cloud.restore_cleanup_refused", "cloud", "restore-cleanup", "Cleanup refused.")))
	})
	defer shutdown()
	out, _, err := executeRootCommand("--socket", socket, "--json", "cloud", "snapshot", "restore-cleanup", "apply", "exact-attempt", "--confirm-digest", strings.Repeat("a", 64), "--yes")
	if err == nil || calls != 1 || !strings.Contains(out, "cloud.restore_cleanup_refused") || !strings.Contains(out, "corr_cleanup_refused") {
		t.Fatal("refusal or correlation lost", out, err)
	}
}
