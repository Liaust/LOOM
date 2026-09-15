package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/cloudstorage"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/restoreauthority"
)

func TestCloudRestoreDrillEndpointRejectsAuthorityAndMalformedRequests(t *testing.T) {
	t.Parallel()
	server := NewServer(Services{}, slog.Default()).Handler()
	for _, test := range []struct{ name, method, body, code string }{
		{"method", http.MethodGet, "", "method.not_allowed"},
		{"empty", http.MethodPost, "", "request.invalid_json"},
		{"null", http.MethodPost, "null", "cloud.restore_ref_required"},
		{"no-ref", http.MethodPost, `{}`, "cloud.restore_ref_required"},
		{"trailing", http.MethodPost, `{"ref":"latest"} {}`, "request.invalid_json"},
		{"trailing-invalid", http.MethodPost, `{"ref":"latest"} x`, "request.invalid_json"},
		{"oversized", http.MethodPost, `{"ref":"` + strings.Repeat("x", 16<<10) + `"}`, "request.invalid_json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			res := httptest.NewRecorder()
			server.ServeHTTP(res, httptest.NewRequest(test.method, "/v1/cloud/snapshot/restore-drill/live", strings.NewReader(test.body)))
			assertCloudRestoreError(t, res, test.code)
		})
	}
	for _, field := range []string{"state_dir", "to", "owner", "active_database", "provenance_owner", "provenance_active_database", "database_url", "provenance_database_url", "socket_path", "config", "keep", "keep_database", "runner", "restore_authority"} {
		t.Run(field, func(t *testing.T) {
			res := httptest.NewRecorder()
			server.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/v1/cloud/snapshot/restore-drill/live", strings.NewReader(`{"ref":"latest","`+field+`":"credential-MUST-NOT-LEAK"}`)))
			assertCloudRestoreError(t, res, "request.invalid_json")
			if strings.Contains(res.Body.String(), "credential-MUST-NOT-LEAK") {
				t.Fatal("request content leaked")
			}
		})
	}
}

func TestCloudRestoreDrillEndpointEnforcesExactConfigAndDisposableTargets(t *testing.T) {
	t.Parallel()
	runtimeConfig := config.Config{RestoreOperationalDatabase: "loom_restore_drill_active", RestoreProvenanceDatabase: "loom_provenance_restore_drill_active"}
	server := NewServer(Services{RuntimeConfig: runtimeConfig}, slog.Default()).Handler()
	for _, path := range []string{"/tmp/alternate-cloud.json", "/etc/loom/cloud/../cloud/config.json", " /etc/loom/cloud/config.json", "/etc/loom/cloud/config.json "} {
		res := httptest.NewRecorder()
		body, _ := json.Marshal(map[string]any{"ref": "latest", "config_path": path})
		server.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/v1/cloud/snapshot/restore-drill/live", bytes.NewReader(body)))
		assertCloudRestoreError(t, res, "cloud.config_override_forbidden")
	}
	for _, configPath := range []string{"", cloudstorage.DefaultConfigPath} {
		for _, targets := range [][2]string{
			{"loom_main", ""}, {"postgres", ""}, {"template0", ""}, {"template1", ""},
			{"loom_restore_drill_active", ""}, {"", "loom_provenance_restore_drill_active"},
			{"loom_provenance_restore_drill_wrong_kind", ""}, {"", "loom_restore_drill_wrong_kind"},
			{"loom_restore_drill_" + strings.Repeat("x", 64), ""}, {"loom_restore_drill_a;DROP", ""},
		} {
			body, _ := json.Marshal(map[string]any{"ref": "latest", "config_path": configPath, "target_database": targets[0], "provenance_target_database": targets[1], "dry_run": true})
			res := httptest.NewRecorder()
			server.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/v1/cloud/snapshot/restore-drill/live", bytes.NewReader(body)))
			assertCloudRestoreError(t, res, "cloud.restore_target_invalid")
		}
	}
}

func cloudRestoreRuntimeConfig() config.Config {
	return config.Config{
		NodeID: "service-node", ProvenanceDBURL: "postgresql://owner:credential-MUST-NOT-LEAK@localhost/loom_provenance",
		RestoreOperationalDatabase: "loom_main", RestoreOperationalOwner: "loom",
		RestoreProvenanceDatabase: "loom_provenance", RestoreProvenanceOwner: "loom_provenance",
		RestoreAuthoritySocketPath:  "/tmp/disposable-restore-authority.sock",
		RestoreAuthoritySocketOwner: strconv.Itoa(os.Getuid()), RestoreAuthoritySocketGroup: strconv.Itoa(os.Getgid()),
		RestoreActivatorUser: strconv.Itoa(os.Getuid()), RestoreExecutorUser: "executor-not-resolved-by-client",
	}
}

func TestCloudRestoreDrillEndpointUsesServiceContextAndPreservesSelectors(t *testing.T) {
	t.Parallel()
	configPath := writeBorgCloudConfig(t, "/fixture/borg-not-executed")
	loaded, err := cloudstorage.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, dryRun := range []bool{false, true} {
		for _, explicit := range []bool{false, true} {
			t.Run(fmt.Sprintf("dry=%t/explicit=%t", dryRun, explicit), func(t *testing.T) {
				runtimeConfig := cloudRestoreRuntimeConfig()
				if dryRun {
					runtimeConfig.RestoreAuthoritySocketOwner = "missing-socket-owner"
					runtimeConfig.RestoreAuthoritySocketGroup = "missing-socket-group"
					runtimeConfig.RestoreActivatorUser = "missing-activator"
				}
				input := localclient.CloudSnapshotRestoreDrillInput{ConfigPath: configPath, Ref: "history-exact", DryRun: dryRun}
				if explicit {
					input.NodeID = "selected-node"
					input.TargetDatabase = "loom_restore_drill_exact"
					input.ProvenanceTargetDatabase = "loom_provenance_restore_drill_exact"
				}
				body, _ := json.Marshal(input)
				calls := 0
				server := NewServer(Services{RuntimeConfig: runtimeConfig, AllowCloudConfigOverride: true}, slog.Default())
				res := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/v1/cloud/snapshot/restore-drill/live", bytes.NewReader(body))
				server.cloudSnapshotRestoreDrillLive(res, req, func(ctx context.Context, got cloudstorage.CloudRestoreDrillInput) (cloudstorage.CloudRestoreDrillResult, error) {
					calls++
					deadline, ok := ctx.Deadline()
					if !ok || time.Until(deadline) <= 119*time.Minute || time.Until(deadline) > 2*time.Hour {
						t.Fatal("daemon deadline is not bounded to two hours")
					}
					wantNode := runtimeConfig.NodeID
					if explicit {
						wantNode = input.NodeID
					}
					if got.NodeID != wantNode || got.Ref != input.Ref || got.TargetDatabase != input.TargetDatabase || got.ProvenanceTargetDatabase != input.ProvenanceTargetDatabase || got.DryRun != dryRun {
						t.Fatal("restore selectors changed")
					}
					if got.Config.Snapshots.Borg.Binary != "/fixture/borg-not-executed" || got.StateDir != loaded.Config.StateDir || got.ProvenanceDatabaseURL != runtimeConfig.ProvenanceDBURL || got.Owner != "loom" || got.ProvenanceOwner != "loom_provenance" || got.ActiveDatabase != "loom_main" || got.ProvenanceActiveDatabase != "loom_provenance" {
						t.Fatal("restore did not use exact service configuration")
					}
					if got.KeepDatabase || got.Runner != nil || got.ProvenanceRestore != nil || (got.RestoreAuthority == nil) != dryRun {
						t.Fatal("restore authority/cleanup wiring changed")
					}
					if !dryRun {
						if _, ok := got.RestoreAuthority.(*restoreauthority.Client); !ok {
							t.Fatal("service bypassed the socket authority")
						}
					}
					return cloudstorage.CloudRestoreDrillResult{Status: "succeeded", Ref: got.Ref, DryRun: dryRun}, nil
				})
				if res.Code != http.StatusOK || calls != 1 {
					t.Fatalf("status=%d calls=%d body=%s", res.Code, calls, res.Body.String())
				}
				if strings.Contains(res.Body.String(), "credential-MUST-NOT-LEAK") {
					t.Fatal("service connection leaked")
				}
			})
		}
	}
}

func TestCloudRestoreDrillEndpointPreservesTypedFailureAndCancellationWithoutSecrets(t *testing.T) {
	t.Parallel()
	configPath := writeBorgCloudConfig(t, "/fixture/borg-not-executed")
	secret := "postgresql://owner:credential-MUST-NOT-LEAK@localhost/database /private/staging/dump"
	typed := loomerrors.Wrap("backup.restore_authority.database_create_failed", "backup", "loom_restore_drill_exact", "Strict restore authority failed at the database_create stage.", errors.New(secret))
	for _, failure := range []error{fmt.Errorf("wrapped: %w", typed), errors.New(secret)} {
		var logs bytes.Buffer
		server := NewServer(Services{RuntimeConfig: cloudRestoreRuntimeConfig(), AllowCloudConfigOverride: true}, slog.New(slog.NewTextHandler(&logs, nil)))
		body, _ := json.Marshal(localclient.CloudSnapshotRestoreDrillInput{ConfigPath: configPath, Ref: "history-exact"})
		ctx, cancel := context.WithCancel(context.Background())
		req := httptest.NewRequest(http.MethodPost, "/v1/cloud/snapshot/restore-drill/live", bytes.NewReader(body)).WithContext(ctx)
		req.Header.Set(correlation.Header, "corr_restore_service_failure")
		res := httptest.NewRecorder()
		server.cloudSnapshotRestoreDrillLive(res, req, func(runCtx context.Context, input cloudstorage.CloudRestoreDrillInput) (cloudstorage.CloudRestoreDrillResult, error) {
			cancel()
			if !errors.Is(runCtx.Err(), context.Canceled) {
				t.Fatal("daemon discarded request cancellation")
			}
			return cloudstorage.CloudRestoreDrillResult{}, failure
		})
		cancel()
		want := "cloud.snapshot_restore_drill_failed"
		if errors.Is(failure, typed) {
			want = typed.Code
		}
		assertCloudRestoreError(t, res, want)
		var envelope response.ErrorEnvelope
		if err := json.Unmarshal(res.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if errors.Is(failure, typed) && (envelope.Error.Domain != typed.Domain || envelope.Error.Target != typed.Target || envelope.Error.Summary != typed.Summary) {
			t.Fatal("typed failure metadata changed")
		}
		if envelope.Meta.CorrelationID != "corr_restore_service_failure" {
			t.Fatal("correlation changed")
		}
		for _, forbidden := range []string{"credential-MUST-NOT-LEAK", "postgresql://", "/private/staging"} {
			if strings.Contains(res.Body.String()+logs.String(), forbidden) {
				t.Fatal("raw restore cause leaked")
			}
		}
	}
}

func TestCloudRestoreDrillServiceResolvesOnlySocketActivator(t *testing.T) {
	runtimeConfig := cloudRestoreRuntimeConfig()
	runtimeConfig.RestoreActivatorUser = "activator-not-present-on-this-host"
	runtimeConfig.RestoreExecutorUser = strconv.Itoa(os.Getuid())
	input := cloudstorage.CloudRestoreDrillInput{}
	if err := configureCloudRestoreDrillAuthority(&input, runtimeConfig, ""); err == nil || !strings.Contains(err.Error(), "resolve Unix user") {
		t.Fatal("executor substituted for missing socket activator")
	}
}

func assertCloudRestoreError(t *testing.T, res *httptest.ResponseRecorder, code string) {
	t.Helper()
	var envelope response.ErrorEnvelope
	if err := json.Unmarshal(res.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if res.Code < 400 || envelope.OK || envelope.Error.Code != code {
		t.Fatalf("status=%d body=%s want=%s", res.Code, res.Body.String(), code)
	}
}

func TestCloudStatusLiveEndpointUsesDaemonRuntime(t *testing.T) {
	t.Parallel()

	configPath := writeDisabledCloudConfig(t)
	server := NewServer(Services{
		RuntimeConfig:            config.Config{NodeID: "main", NodeKind: "main"},
		AllowCloudConfigOverride: true,
	}, slog.Default()).Handler()

	req := httptest.NewRequest(http.MethodPost, "/v1/cloud/status/live", strings.NewReader(`{"config_path":"`+configPath+`","force_live":true}`))
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status code = %d, body: %s", res.Code, res.Body.String())
	}
	var envelope response.Envelope[cloudstorage.StatusReport]
	if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !envelope.OK {
		t.Fatalf("envelope not ok: %#v", envelope)
	}
	if envelope.Data.Status != "disabled" {
		t.Fatalf("cloud status = %q, want disabled", envelope.Data.Status)
	}
	if envelope.Data.Mode != string(cloudstorage.StatusModeLive) {
		t.Fatalf("mode = %q, want live", envelope.Data.Mode)
	}
}

func TestCloudStatusLiveEndpointCanUseCachedMode(t *testing.T) {
	t.Parallel()

	configPath := writeDisabledCloudConfig(t)
	server := NewServer(Services{
		RuntimeConfig:            config.Config{NodeID: "main", NodeKind: "main"},
		AllowCloudConfigOverride: true,
	}, slog.Default()).Handler()

	req := httptest.NewRequest(http.MethodPost, "/v1/cloud/status/live", strings.NewReader(`{"config_path":"`+configPath+`","cached":true}`))
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status code = %d, body: %s", res.Code, res.Body.String())
	}
	var envelope response.Envelope[cloudstorage.StatusReport]
	if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Data.Mode != string(cloudstorage.StatusModeCached) {
		t.Fatalf("mode = %q, want cached", envelope.Data.Mode)
	}
}

func TestCloudSnapshotStatusLiveEndpointSkipsInventoryWhenCloudDisabled(t *testing.T) {
	t.Parallel()

	configPath := writeDisabledCloudConfig(t)
	server := NewServer(Services{
		RuntimeConfig:            config.Config{NodeID: "main", NodeKind: "main"},
		AllowCloudConfigOverride: true,
	}, slog.Default()).Handler()

	req := httptest.NewRequest(http.MethodPost, "/v1/cloud/snapshot/status/live", strings.NewReader(`{"config_path":"`+configPath+`","node_id":"main"}`))
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status code = %d, body: %s", res.Code, res.Body.String())
	}
	var envelope response.Envelope[struct {
		Cloud     cloudstorage.StatusReport         `json:"cloud"`
		Snapshots *cloudstorage.SnapshotListResult  `json:"snapshots,omitempty"`
		Producer  maintenance.CloudProtectionStatus `json:"producer"`
	}]
	if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !envelope.OK {
		t.Fatalf("envelope not ok: %#v", envelope)
	}
	if envelope.Data.Cloud.Status != "disabled" {
		t.Fatalf("cloud status = %q, want disabled", envelope.Data.Cloud.Status)
	}
	if envelope.Data.Snapshots != nil {
		t.Fatalf("disabled cloud should not list snapshots: %#v", envelope.Data.Snapshots)
	}
	if envelope.Data.Producer.Available || envelope.Data.Producer.WorkerState != "unavailable" {
		t.Fatalf("producer status = %#v", envelope.Data.Producer)
	}
}

func TestCloudSnapshotRunEndpointRejectsUnsafeShapesBeforeWorkerExecution(t *testing.T) {
	server := NewServer(Services{}, slog.Default()).Handler()
	for _, test := range []struct {
		method, body string
		status       int
	}{
		{http.MethodGet, "", http.StatusMethodNotAllowed},
		{http.MethodPost, `{`, http.StatusBadRequest},
		{http.MethodPost, `{"unknown":true}`, http.StatusBadRequest},
	} {
		req := httptest.NewRequest(test.method, "/v1/cloud/snapshot/run", strings.NewReader(test.body))
		res := httptest.NewRecorder()
		server.ServeHTTP(res, req)
		if res.Code != test.status {
			t.Fatalf("%s body=%q status=%d want=%d response=%s", test.method, test.body, res.Code, test.status, res.Body.String())
		}
	}
}

func TestCloudLiveEndpointRejectsConfigOverrideByDefault(t *testing.T) {
	t.Parallel()

	server := NewServer(Services{RuntimeConfig: config.Config{NodeID: "main"}}, slog.Default()).Handler()
	req := httptest.NewRequest(http.MethodPost, "/v1/cloud/status/live", strings.NewReader(`{"config_path":"/tmp/alternate-cloud.json"}`))
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want 400 body: %s", res.Code, res.Body.String())
	}
}

func TestCloudSnapshotListLiveEndpointRejectsConfigOverrideByDefault(t *testing.T) {
	t.Parallel()

	server := NewServer(Services{RuntimeConfig: config.Config{NodeID: "main"}}, slog.Default()).Handler()
	req := httptest.NewRequest(http.MethodPost, "/v1/cloud/snapshot/list/live", strings.NewReader(`{"config_path":"/tmp/alternate-cloud.json"}`))
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want 400 body: %s", res.Code, res.Body.String())
	}
}

func TestCloudSnapshotVerifyLiveEndpointRejectsConfigOverrideByDefault(t *testing.T) {
	t.Parallel()

	server := NewServer(Services{RuntimeConfig: config.Config{NodeID: "main"}}, slog.Default()).Handler()
	req := httptest.NewRequest(http.MethodPost, "/v1/cloud/snapshot/verify/live", strings.NewReader(`{"config_path":"/tmp/alternate-cloud.json","ref":"latest"}`))
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want 400 body: %s", res.Code, res.Body.String())
	}
}

func TestCloudSnapshotListLiveEndpointReturnsUninitializedFirstRunState(t *testing.T) {
	t.Parallel()

	borgPath := filepath.Join(t.TempDir(), "fake-borg")
	if err := os.WriteFile(borgPath, []byte("#!/bin/sh\nprintf '%s\\n' 'Repository /borg/loom-main does not exist' >&2\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := writeBorgCloudConfig(t, borgPath)
	server := NewServer(Services{
		RuntimeConfig:            config.Config{NodeID: "main", NodeKind: "main", NodeRole: "main"},
		AllowCloudConfigOverride: true,
	}, slog.Default()).Handler()

	req := httptest.NewRequest(http.MethodPost, "/v1/cloud/snapshot/list/live", strings.NewReader(`{"config_path":"`+configPath+`","node_id":"main"}`))
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status code = %d, body: %s", res.Code, res.Body.String())
	}
	var envelope response.Envelope[cloudstorage.SnapshotListResult]
	if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !envelope.OK || envelope.Data.Status != cloudstorage.SnapshotListStatusUninitialized {
		t.Fatalf("unexpected envelope: %#v", envelope)
	}
	if envelope.Data.Code != cloudstorage.SnapshotBackendUninitializedCode || envelope.Data.RepairHint == "" {
		t.Fatalf("missing first-run code/hint: %#v", envelope.Data)
	}
}

func TestCloudSnapshotVerifyLiveEndpointUsesDaemonRuntime(t *testing.T) {
	t.Parallel()

	borgPath := filepath.Join(t.TempDir(), "fake-borg")
	script := `#!/bin/sh
args="$*"
case "$args" in
  *" list --json"*)
    printf '{"archives":[{"name":"main-20260707T082907Z-K1B5K2QHN62WAANS","time":"2026-07-07T08:33:42Z"}]}\n'
    ;;
  *" info --json ::main-20260707T082907Z-K1B5K2QHN62WAANS"*)
    printf '{}\n'
    ;;
  *" check --archives-only ::main-20260707T082907Z-K1B5K2QHN62WAANS"*)
    printf '{}\n'
    ;;
  *)
    echo "unexpected borg args: $args" >&2
    exit 9
    ;;
esac
`
	if err := os.WriteFile(borgPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := writeBorgCloudConfig(t, borgPath)
	server := NewServer(Services{
		RuntimeConfig:            config.Config{NodeID: "main", NodeKind: "main", NodeRole: "main"},
		AllowCloudConfigOverride: true,
	}, slog.Default()).Handler()

	req := httptest.NewRequest(http.MethodPost, "/v1/cloud/snapshot/verify/live", strings.NewReader(`{"config_path":"`+configPath+`","node_id":"main","ref":"latest"}`))
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status code = %d, body: %s", res.Code, res.Body.String())
	}
	var envelope response.Envelope[cloudstorage.SnapshotVerifyResult]
	if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !envelope.OK {
		t.Fatalf("envelope not ok: %#v", envelope)
	}
	if envelope.Data.Status != cloudstorage.SnapshotStatusSucceeded {
		t.Fatalf("verify status = %q, want succeeded: %#v", envelope.Data.Status, envelope.Data)
	}
	if envelope.Data.Profile != cloudstorage.SnapshotVerifyProfileMetadata || envelope.Data.Coverage != cloudstorage.SnapshotVerifyCoverageArchiveMetadata {
		t.Fatalf("verify assurance result = %#v", envelope.Data)
	}
	if envelope.Data.Ref != "20260707T082907Z-K1B5K2QHN62WAANS" {
		t.Fatalf("verify ref = %q", envelope.Data.Ref)
	}
	if envelope.Data.Checks["borg_info"] != cloudstorage.SnapshotStatusSucceeded {
		t.Fatalf("borg_info check = %q", envelope.Data.Checks["borg_info"])
	}
	if envelope.Data.Checks["borg_check"] != cloudstorage.SnapshotStatusSucceeded {
		t.Fatalf("borg_check check = %q", envelope.Data.Checks["borg_check"])
	}
}

func TestCloudSnapshotVerifyLiveEndpointPreservesProfileExclusionAndRollingBound(t *testing.T) {
	t.Parallel()

	server := NewServer(Services{RuntimeConfig: config.Config{NodeID: "main"}}, slog.Default()).Handler()
	for _, body := range []string{
		`{"profile":"rolling_repository","ref":"latest","max_duration_seconds":60}`,
		`{"profile":"rolling_repository"}`,
		`{"profile":"metadata","max_duration_seconds":60}`,
		`{"profile":"archive_data","ref":"latest"}`,
		`{"profile":"archive_data","ref":"exact","max_duration_seconds":60}`,
		`{"profile":"full","ref":"exact"}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/v1/cloud/snapshot/verify/live", strings.NewReader(body))
		res := httptest.NewRecorder()
		server.ServeHTTP(res, req)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("body %s status = %d, want 400: %s", body, res.Code, res.Body.String())
		}
		var envelope response.ErrorEnvelope
		if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Error.Code != "cloud.snapshot_verify_profile_invalid" {
			t.Fatalf("body %s error code = %q", body, envelope.Error.Code)
		}
	}
}

func TestCloudSnapshotVerifyLiveEndpointRunsOnlyBoundedRollingRepositoryCheck(t *testing.T) {
	t.Parallel()

	borgPath := filepath.Join(t.TempDir(), "fake-borg")
	script := `#!/bin/sh
args="$*"
case "$args" in
  *" check --repository-only --max-duration 17")
    printf '{}\n'
    ;;
  *)
    echo "unexpected borg args: $args" >&2
    exit 9
    ;;
esac
`
	if err := os.WriteFile(borgPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := writeBorgCloudConfig(t, borgPath)
	server := NewServer(Services{
		RuntimeConfig: config.Config{NodeID: "main", NodeKind: "main", NodeRole: "main"}, AllowCloudConfigOverride: true,
	}, slog.Default()).Handler()
	req := httptest.NewRequest(http.MethodPost, "/v1/cloud/snapshot/verify/live", strings.NewReader(`{"config_path":"`+configPath+`","profile":"rolling_repository","max_duration_seconds":17}`))
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status code = %d, body: %s", res.Code, res.Body.String())
	}
	var envelope response.Envelope[cloudstorage.SnapshotVerifyResult]
	if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.OK || envelope.Data.Status != cloudstorage.SnapshotStatusSucceeded || envelope.Data.Profile != cloudstorage.SnapshotVerifyProfileRollingRepository || envelope.Data.Coverage != cloudstorage.SnapshotVerifyCoverageRepositoryTimeBounded || envelope.Data.MaxDurationSeconds != 17 {
		t.Fatalf("rolling verify envelope = %#v", envelope)
	}
	if envelope.Data.Ref != "" || envelope.Data.Archive != "" || envelope.Data.Checks["borg_repository_check"] != cloudstorage.SnapshotStatusSucceeded {
		t.Fatalf("rolling verify claimed archive evidence: %#v", envelope.Data)
	}
}

func TestCloudSnapshotVerifyLiveEndpointRejectsDeepProfileForLegacyBackend(t *testing.T) {
	t.Parallel()

	configPath := writeDisabledCloudConfig(t)
	server := NewServer(Services{RuntimeConfig: config.Config{NodeID: "main"}, AllowCloudConfigOverride: true}, slog.Default()).Handler()
	req := httptest.NewRequest(http.MethodPost, "/v1/cloud/snapshot/verify/live", strings.NewReader(`{"config_path":"`+configPath+`","profile":"rolling_repository","max_duration_seconds":17}`))
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want 400: %s", res.Code, res.Body.String())
	}
	var envelope response.ErrorEnvelope
	if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "cloud.snapshot_verify_profile_invalid" {
		t.Fatalf("error code = %q", envelope.Error.Code)
	}
}

func TestCloudSnapshotBackendInitLiveEndpointRequiresConfirm(t *testing.T) {
	t.Parallel()

	server := NewServer(Services{RuntimeConfig: config.Config{NodeID: "main"}}, slog.Default()).Handler()
	req := httptest.NewRequest(http.MethodPost, "/v1/cloud/snapshot/backend/init/live", strings.NewReader(`{}`))
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want 400 body: %s", res.Code, res.Body.String())
	}
	var envelope response.ErrorEnvelope
	if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Error.Code != "cloud.snapshot_backend_init_confirm_required" {
		t.Fatalf("error code = %q, want cloud.snapshot_backend_init_confirm_required", envelope.Error.Code)
	}
}

func TestCloudSnapshotRetentionApplyLiveEndpointRequiresConfirm(t *testing.T) {
	t.Parallel()

	server := NewServer(Services{RuntimeConfig: config.Config{NodeID: "main"}}, slog.Default()).Handler()
	req := httptest.NewRequest(http.MethodPost, "/v1/cloud/snapshot/retention/apply/live", strings.NewReader(`{"node_id":"main","keep_latest":3}`))
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want 400 body: %s", res.Code, res.Body.String())
	}
	var envelope response.ErrorEnvelope
	if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Error.Code != "cloud.retention_confirm_required" {
		t.Fatalf("error code = %q, want cloud.retention_confirm_required", envelope.Error.Code)
	}
}

func TestCloudSnapshotRetentionApplyLiveEndpointRequiresReviewedBorgPlanDigest(t *testing.T) {
	t.Parallel()

	configPath := writeBorgCloudConfig(t, filepath.Join(t.TempDir(), "unused-borg"))
	server := NewServer(Services{
		RuntimeConfig: config.Config{NodeID: "main", NodeKind: "main"}, AllowCloudConfigOverride: true,
	}, slog.Default()).Handler()
	req := httptest.NewRequest(http.MethodPost, "/v1/cloud/snapshot/retention/apply/live", strings.NewReader(`{"config_path":"`+configPath+`","node_id":"main","confirm":true}`))
	res := httptest.NewRecorder()
	server.ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want 400 body: %s", res.Code, res.Body.String())
	}
	var envelope response.ErrorEnvelope
	if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "cloud.retention_plan_confirmation_required" {
		t.Fatalf("error code = %q", envelope.Error.Code)
	}
}

func writeDisabledCloudConfig(t *testing.T) string {
	t.Helper()
	cfg := cloudstorage.DefaultConfig()
	cfg.Enabled = false
	payload, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal cloud config: %v", err)
	}
	path := filepath.Join(t.TempDir(), "cloud.json")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatalf("write cloud config: %v", err)
	}
	return path
}

func writeBorgCloudConfig(t *testing.T, borgBinary string) string {
	t.Helper()
	dir := t.TempDir()
	passphrasePath := filepath.Join(dir, "borg.passphrase")
	if err := os.WriteFile(passphrasePath, []byte("test-passphrase\n"), 0o600); err != nil {
		t.Fatalf("write passphrase: %v", err)
	}
	cfg := cloudstorage.DefaultConfig()
	cfg.Enabled = true
	cfg.StateDir = filepath.Join(dir, "state")
	cfg.RemoteLockPath = filepath.Join(cfg.StateDir, "locks", "storagebox.lock")
	cfg.Snapshots.Backend = cloudstorage.SnapshotBackendBorg
	cfg.Snapshots.Borg.Binary = borgBinary
	cfg.Snapshots.Borg.Repository = filepath.Join(dir, "repo")
	cfg.Snapshots.Borg.PassphraseFile = passphrasePath
	cfg.Snapshots.Borg.CacheDir = filepath.Join(cfg.StateDir, "borg", "cache")
	cfg.Snapshots.Borg.SecurityDir = filepath.Join(cfg.StateDir, "borg", "security")
	payload, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal cloud config: %v", err)
	}
	path := filepath.Join(dir, "cloud.json")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatalf("write cloud config: %v", err)
	}
	return path
}

func TestCloudRestoreCleanupSelectorOnlyRoutes(t *testing.T) {
	for _, verb := range []string{"plan", "apply"} {
		for _, body := range []string{`{}`, `null`, `{"attempt":"one","root":"/private"}`, `{"attempt":"one","config_path":"/tmp/private.json"}`, `{"attempt":"one","attempt":"two"}`, `{"attempt":"one"} {}`, `{"attempt":"` + strings.Repeat("x", 17000) + `"}`} {
			t.Run(verb+body[:min(35, len(body))], func(t *testing.T) {
				calls := 0
				s := NewServer(Services{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
				s.cloudRestoreCleanupConfig = func() (cloudstorage.Config, error) {
					calls++
					return cloudstorage.Config{}, errors.New("private credential canary")
				}
				req := httptest.NewRequest(http.MethodPost, "/v1/cloud/snapshot/restore-cleanup/"+verb, strings.NewReader(body))
				w := httptest.NewRecorder()
				s.Handler().ServeHTTP(w, req)
				if w.Code != http.StatusBadRequest || calls != 0 || strings.Contains(w.Body.String(), "canary") {
					t.Fatalf("code=%d calls=%d body=%s", w.Code, calls, w.Body.String())
				}
			})
		}
	}
	for _, body := range []string{`{"attempt":"one","yes":true}`, `{"attempt":"one","dry_run":true}`, `{"attempt":"one","confirm_digest":"x"}`} {
		s := NewServer(Services{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/cloud/snapshot/restore-cleanup/plan", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatal("apply field accepted by plan")
		}
	}
	s := NewServer(Services{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	calls := 0
	s.cloudRestoreCleanupConfig = func() (cloudstorage.Config, error) {
		calls++
		return cloudstorage.Config{}, errors.New("private credential canary")
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(method, "/v1/cloud/snapshot/restore-cleanup/apply", nil))
		if w.Code != 405 || calls != 0 {
			t.Fatal("method accepted")
		}
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/cloud/snapshot/restore-cleanup/plan", strings.NewReader(`{"attempt":"one"}`)))
	if calls != 1 || w.Code != 500 || strings.Contains(w.Body.String(), "canary") {
		t.Fatal("configuration boundary", w.Code, w.Body.String())
	}
}
