package loomcli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/spf13/cobra"

	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/response"
)

func TestRootCommandSilencesCobraErrors(t *testing.T) {
	cmd := NewRootCommand()
	if !cmd.SilenceErrors {
		t.Fatal("root command should silence Cobra error printing")
	}
}

func TestRootCommandIncludesCapabilityCommands(t *testing.T) {
	cmd := NewRootCommand()
	for _, args := range [][]string{
		{"docs", "status"},
		{"docs", "search", "project activation"},
		{"docs", "inspect", "Projects"},
		{"docs", "related", "Projects"},
		{"agent", "pack", "status"},
		{"agent", "pack", "validate"},
		{"agent", "pack", "scaffold-workspace"},
		{"providers", "list"},
		{"services", "list"},
		{"service", "inspect", "main@example"},
		{"service", "status", "main@example"},
		{"service", "start", "main@example", "--yes"},
		{"service", "stop", "main@example", "--yes"},
		{"service", "restart", "main@example", "--yes"},
		{"service", "logs", "main@example", "--lines", "20"},
		{"service", "register", "plan", "--contract", "example"},
		{"provider", "inspect", "main@system"},
		{"provider-advertisements", "list"},
		{"provider-advertisement", "inspect", "provider_advertisement_test"},
		{"provider-advertisement", "approve", "provider_advertisement_test"},
		{"capabilities", "list"},
		{"capability", "inspect", "main@system.status.read"},
		{"capability", "call", "main@system.status.read"},
		{"capability", "runtime-bindings", "list"},
		{"capability", "runtime-binding", "inspect", "runtime_binding_test"},
		{"capability", "runtime-binding", "register", "endpv_test", "--kind", "script"},
		{"modules", "list"},
		{"module", "register", "."},
		{"module", "install", "loom.project-cockpit"},
		{"module", "inspect", "loom.project-cockpit"},
		{"module", "installation", "inspect", "module_installation_test"},
		{"module", "enable", "module_installation_test"},
		{"module", "disable", "module_installation_test"},
		{"module", "health", "module_installation_test"},
		{"module", "providers", "module_installation_test"},
		{"module", "capabilities", "module_installation_test"},
		{"module", "capability", "expose", "module_installation_test", "main@loom-project-cockpit.status.read"},
		{"module", "capability", "disable", "module_installation_test", "main@loom-project-cockpit.status.read"},
		{"module", "backup-export", "module_installation_test"},
		{"module", "backups", "module_installation_test"},
		{"module", "backup", "inspect", "module_backup_export_test"},
		{"module", "version", "inspect", "module_version_test"},
		{"box", "init"},
		{"box", "repair"},
		{"box", "status"},
		{"box", "path"},
		{"box", "watch-plan"},
		{"box", "watch-apply"},
		{"box", "watch-status"},
		{"project", "scaffold", "Example", "--owner-node", "main"},
		{"project", "validate", "."},
		{"project", "plan", "."},
		{"project", "register", "."},
		{"project", "status", "example"},
		{"project", "activate", "example"},
		{"projects", "list"},
		{"projects", "inspect", "example"},
		{"routes", "list"},
		{"route", "inspect", "route_test"},
		{"capability-calls", "list"},
		{"capability-call", "inspect", "capability_call_test"},
		{"maintenance", "backup", "status"},
		{"maintenance", "backup", "run", "--once"},
		{"maintenance", "backup", "list"},
		{"maintenance", "backup", "verify", "maintenance_operation_test"},
		{"maintenance", "object-store", "status"},
		{"maintenance", "object-store", "scan", "--sample"},
		{"maintenance", "retention", "dry-run"},
		{"maintenance", "retention", "apply", "--yes"},
		{"storage", "tree"},
		{"storage", "list"},
		{"storage", "inspect", "storage_entry_test"},
		{"storage", "inventory"},
		{"storage", "cleanup", "plan"},
		{"storage", "cleanup", "apply", "--plan", "/tmp/cleanup-plan.json"},
		{"storage", "export", "status"},
		{"storage", "export", "rebuild"},
		{"notes", "overview"},
		{"notes", "roots", "list"},
		{"notes", "roots", "reconcile", "--dry-run"},
		{"notes", "objects", "list"},
		{"notes", "objects", "show", "knowledge_object_test"},
		{"notes", "objects", "reconcile", "--dry-run"},
		{"notes", "search", "runtime"},
		{"notes", "projection", "status"},
		{"notes", "projection", "rebuild", "--dry-run"},
		{"notes", "reprocess", "object", "knowledge_object_test"},
		{"notes", "reprocess", "root", "notes_source_root_test"},
		{"notes", "reprocess", "project", "example"},
		{"notes", "reprocess", "node", "main"},
		{"notes", "reprocess", "file-class", "markdown"},
		{"notes", "reprocess", "stale", "--pipeline", "markdown_text_v1"},
		{"notes", "run", "indexer", "--once"},
		{"indexes", "status"},
		{"indexes", "queue", "list"},
		{"indexes", "queue", "show", "index_status_test"},
		{"indexes", "failures"},
		{"indexes", "retry", "index_status_test"},
		{"indexes", "retry-failed"},
		{"indexes", "rebuild", "object", "object_test"},
		{"indexes", "explain", "object", "object_test"},
		{"indexes", "run", "text", "--once"},
		{"objects", "list"},
		{"objects", "inspect", "object_test"},
		{"jobs", "queue"},
		{"jobs", "failures"},
		{"jobs", "status"},
		{"jobs", "run", "next", "--once"},
		{"job", "outputs", "job_test"},
		{"job", "cancel", "job_test"},
		{"job", "retry", "job_test"},
		{"runners", "list"},
		{"runners", "status"},
		{"runner", "inspect", "main-local-runner"},
		{"automations", "list"},
		{"automation", "inspect", "automation_test"},
		{"integrations", "create"},
		{"integrations", "list"},
		{"integration", "inspect", "gmail_smoke"},
		{"integration", "disable", "gmail_smoke"},
		{"integration", "revoke", "gmail_smoke"},
		{"integration", "auth", "create", "gmail_smoke"},
		{"integration", "auth", "list", "gmail_smoke"},
		{"integration", "auth", "revoke", "integration_auth_profile_test"},
		{"direct-event", "endpoints", "create"},
		{"direct-event", "endpoints", "list"},
		{"direct-event", "endpoint", "inspect", "gmail-url"},
		{"direct-event", "endpoint", "preview", "gmail-url"},
		{"direct-event", "endpoint", "pause", "gmail-url"},
		{"direct-event", "endpoint", "resume", "gmail-url"},
		{"direct-event", "endpoint", "disable", "gmail-url"},
		{"direct-event", "ingest", "gmail-url"},
		{"direct-event", "inspect", "direct_event_test"},
		{"direct-event", "raw", "direct_event_test"},
		{"direct-events", "list"},
		{"direct-events", "failures"},
		{"direct-events", "status"},
		{"schedules", "create"},
		{"schedules", "list"},
		{"schedules", "status"},
		{"schedule", "inspect", "schedule_test"},
		{"schedule", "fire", "schedule_test"},
		{"schedule", "pause", "schedule_test"},
		{"schedule", "resume", "schedule_test"},
		{"schedule", "disable", "schedule_test"},
		{"schedule", "fires", "schedule_test"},
		{"invocations", "list"},
		{"invocations", "failures"},
		{"invocation", "inspect", "invocation_test"},
		{"worker", "inspect", "main.automation_scheduler"},
		{"worker", "inspect", "main.automation_dispatcher"},
		{"worker", "inspect", "main.direct_event_ingest"},
		{"worker", "run", "main.automation_scheduler", "--once"},
		{"worker", "run", "main.automation_dispatcher", "--once"},
		{"worker", "run", "main.direct_event_ingest", "--once"},
		{"worker", "inspect", "main.job_sweeper"},
		{"worker", "run", "main.job_sweeper", "--once"},
		{"backup", "status"},
		{"backup", "create", "--production"},
		{"backup", "create", "--production", "--dry-run"},
		{"backup", "list"},
		{"backup", "verify", "maintenance_operation_test"},
		{"backup", "restore-drill", "/tmp/loom-backup-test", "--dry-run"},
		{"backup", "coverage"},
		{"backup", "contracts", "plan"},
		{"backup", "contract", "migrate-ignore-policy"},
		{"cloud", "status"},
		{"cloud", "doctor"},
		{"cloud", "snapshot", "status"},
		{"cloud", "snapshot", "push", "--backup", "latest", "--dry-run"},
		{"cloud", "snapshot", "list"},
		{"cloud", "snapshot", "verify", "latest"},
		{"cloud", "snapshot", "fetch", "latest", "--to", "/tmp/loom-cloud-fetch"},
		{"cloud", "snapshot", "restore-drill", "latest", "--dry-run"},
		{"cloud", "snapshot", "retention", "status"},
		{"cloud", "snapshot", "retention", "plan"},
		{"cloud", "snapshot", "retention", "apply", "--confirm"},
		{"support", "bundle", "create", "--dry-run"},
		{"support", "acceptance", "cleanup", "--root", "/tmp"},
		{"lane", "status"},
		{"lane", "send"},
		{"lane", "publish", "lane_test"},
		{"lane", "repair", "lane_test"},
		{"update", "status"},
		{"update", "plan", "--release-path", "."},
		{"update", "apply", "--release-path", ".", "--yes", "--allow-non-production", "--skip-backup", "--skip-rebuild", "--skip-health-check"},
		{"update", "rollback", "--service-only", "--to", ".", "--yes", "--allow-non-production", "--skip-rebuild", "--skip-health-check"},
		{"update", "rollback", "--restore-required", "--yes", "--allow-non-production"},
		{"update", "maintenance", "status"},
		{"update", "maintenance", "resume", "--yes", "--allow-non-production"},
		{"update", "manifest", "inspect", "/tmp/update.yaml"},
		{"update", "history"},
		{"watched-roots", "status"},
		{"watched-roots", "findings"},
		{"watched-roots", "failures"},
		{"watched-roots", "backups", "status"},
		{"watched-roots", "backups", "batches"},
		{"watched-roots", "backups", "items"},
		{"enter"},
		{"enter", "--start", "background", "--exit-after-render"},
		{"enter", "--search", "watched root", "--exit-after-render"},
		{"enter", "--preview-action", "raw.workers.list", "--exit-after-render"},
		{"enter", "--run-action", "worker.selfcheck.run_once", "--confirm", "--exit-after-render"},
		{"enter", "--command-preview", "$ health", "--exit-after-render"},
		{"enter", "--command-complete", "$ cap", "--exit-after-render"},
		{"enter", "--command-run", "$ enter", "--exit-after-render"},
		{"setup", "plan"},
		{"setup", "status"},
		{"setup", "doctor"},
		{"setup", "repair", "--dry-run", "--fix", "all-safe"},
		{"setup", "manifest", "path"},
		{"setup", "manifest", "inspect"},
		{"bootstrap", "ssh", "loom-workspace", "--dry-run"},
		{"completion", "zsh"},
		{"completion", "bash"},
		{"completion", "fish"},
		{"completion", "powershell"},
	} {
		if found, _, err := cmd.Find(args); err != nil || found == nil {
			t.Fatalf("Find(%v) returned command=%v err=%v", args, found, err)
		}
	}
}

func TestCapabilityCallWaitTimeoutDefaultCoversJobRunnerCadence(t *testing.T) {
	cmd := NewRootCommand()
	callCmd, _, err := cmd.Find([]string{"capability", "call", "main@system.status.read"})
	if err != nil {
		t.Fatalf("find capability call command: %v", err)
	}
	flag := callCmd.Flags().Lookup("timeout-seconds")
	if flag == nil {
		t.Fatal("timeout-seconds flag is missing")
	}
	if flag.DefValue != "120" {
		t.Fatalf("timeout-seconds default = %q, want 120", flag.DefValue)
	}
}

func TestEnterRejectsJSONMachineMode(t *testing.T) {
	cmd := NewRootCommand()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{"--json", "enter"})

	if err := cmd.Execute(); err == nil {
		t.Fatal("expected enter to reject JSON mode")
	}
	if stderr.Len() != 0 {
		t.Fatalf("json portal error should not write stderr, got: %s", stderr.String())
	}
	var envelope response.ErrorEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("portal error was not JSON: %v output=%q", err, stdout.String())
	}
	if envelope.Error.Code != "portal.machine_mode_unsupported" {
		t.Fatalf("code = %q, want portal.machine_mode_unsupported", envelope.Error.Code)
	}
}

func TestEnterHelpListsAllPortalStartScreens(t *testing.T) {
	stdout, stderr, err := executeRootCommand("enter", "--help")
	if err != nil {
		t.Fatalf("enter --help returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{"home", "timeline", "box", "storage", "projects", "database", "background", "automations", "jobs", "nodes", "capabilities"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("enter --help missing start screen %q:\n%s", want, stdout)
		}
	}
}

func TestEnterRejectsNoninteractiveWithoutANSI(t *testing.T) {
	t.Setenv("LOOM_NONINTERACTIVE", "1")
	cmd := NewRootCommand()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{"--socket", "/tmp/loom-test.sock", "enter"})

	if err := cmd.Execute(); err == nil {
		t.Fatal("expected enter to reject noninteractive mode")
	}
	if stdout.Len() != 0 {
		t.Fatalf("noninteractive portal error should not write stdout, got: %s", stdout.String())
	}
	output := stderr.String()
	if !strings.Contains(output, "portal.unavailable") {
		t.Fatalf("expected portal unavailable error, got:\n%s", output)
	}
	if strings.Contains(output, "\x1b[") {
		t.Fatalf("noninteractive portal error should not contain ANSI: %q", output)
	}
}

func TestRootCommandIncludesCLIUXFlags(t *testing.T) {
	cmd := NewRootCommand()
	for _, name := range []string{"no-interactive", "no-color", "no-animation", "theme"} {
		if cmd.PersistentFlags().Lookup(name) == nil {
			t.Fatalf("missing persistent flag %s", name)
		}
	}
}

func TestRenderErrorIncludesCloudServiceContextHint(t *testing.T) {
	cmd := &cobra.Command{Use: "cloud status"}
	stderr := &bytes.Buffer{}
	cmd.SetErr(stderr)

	err := renderError(cmd, &options{}, "corr_test", errors.New("failed to read private key file: open /var/lib/loom/cloud/id_ed25519: permission denied"))
	if err == nil {
		t.Fatal("renderError should return the original error")
	}
	output := stderr.String()
	for _, want := range []string{
		"Error:",
		"Hint: Live cloud checks require the LOOM service context",
		"Correlation: corr_test",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("rendered error missing %q:\n%s", want, output)
		}
	}
}

func TestRenderErrorJSONIncludesCloudServiceContextHint(t *testing.T) {
	cmd := &cobra.Command{Use: "cloud snapshot list"}
	stdout := &bytes.Buffer{}
	cmd.SetOut(stdout)

	err := renderError(cmd, &options{jsonOutput: true}, "corr_test", errors.New("failed to read private key file: open /var/lib/loom/cloud/id_ed25519: permission denied"))
	if err == nil {
		t.Fatal("renderError should return the original error")
	}

	var envelope response.ErrorEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("error output was not JSON: %v", err)
	}
	if !strings.Contains(envelope.Error.Hint, "Live cloud checks require the LOOM service context") {
		t.Fatalf("json cloud hint missing or wrong: %#v", envelope.Error)
	}
	if envelope.Error.CorrelationID != "corr_test" {
		t.Fatalf("correlation = %q, want corr_test", envelope.Error.CorrelationID)
	}
}

func TestRenderErrorDoesNotShowCloudHintForNonCloudPeerAuth(t *testing.T) {
	cmd := &cobra.Command{Use: "update apply"}
	stderr := &bytes.Buffer{}
	cmd.SetErr(stderr)

	err := renderError(cmd, &options{}, "corr_test", errors.New("failed to connect to `user=loom database=loom_main`: FATAL: Peer authentication failed for user \"loom\""))
	if err == nil {
		t.Fatal("renderError should return the original error")
	}
	output := stderr.String()
	if strings.Contains(output, "Cloud authentication failed") || strings.Contains(output, "Live cloud checks require") {
		t.Fatalf("non-cloud peer auth failure should not render cloud hint:\n%s", output)
	}
	if !strings.Contains(output, "Correlation: corr_test") {
		t.Fatalf("rendered error missing correlation:\n%s", output)
	}
}

func TestCompletionCommandGeneratesOutput(t *testing.T) {
	cmd := NewRootCommand()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{"completion", "zsh"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("completion command returned error: %v stderr=%s", err, stderr.String())
	}
	if stdout.Len() == 0 {
		t.Fatal("expected completion output")
	}
	if strings.Contains(stdout.String(), "\x1b[") {
		t.Fatalf("completion output should not contain ANSI: %q", stdout.String())
	}
}

func TestRootCommandIncludesPolicyCommands(t *testing.T) {
	cmd := NewRootCommand()
	for _, args := range [][]string{
		{"policy", "explain", "capability:main@system.status.read"},
		{"policy", "decisions", "list"},
		{"policy", "decision", "inspect", "policy_decision_test"},
	} {
		if found, _, err := cmd.Find(args); err != nil || found == nil {
			t.Fatalf("Find(%v) returned command=%v err=%v", args, found, err)
		}
	}
}

func TestRenderErrorHumanOutput(t *testing.T) {
	cmd, _, stderr := bufferedCommand()
	err := loomerrors.New("object.not_found", "objects", "object_missing", "Object was not found.")

	if got := renderError(cmd, &options{}, "corr_test", err); got == nil {
		t.Fatal("expected returned error")
	}

	output := stderr.String()
	for _, want := range []string{
		"Error: object.not_found: Object was not found.",
		"Domain: objects",
		"Target: object_missing",
		"Correlation: corr_test",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("stderr missing %q:\n%s", want, output)
		}
	}
}

func TestRenderErrorJSONOutput(t *testing.T) {
	cmd, stdout, stderr := bufferedCommand()
	err := loomerrors.New("object.not_found", "objects", "object_missing", "Object was not found.")

	if got := renderError(cmd, &options{jsonOutput: true}, "corr_test", err); got == nil {
		t.Fatal("expected returned error")
	}
	if stderr.Len() != 0 {
		t.Fatalf("json errors should not write human stderr, got: %s", stderr.String())
	}

	var envelope response.ErrorEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("error output was not JSON: %v", err)
	}
	if envelope.OK {
		t.Fatal("error envelope should have ok=false")
	}
	if envelope.Error.Code != "object.not_found" {
		t.Fatalf("code = %q, want object.not_found", envelope.Error.Code)
	}
	if envelope.Meta.CorrelationID != "corr_test" {
		t.Fatalf("correlation = %q, want corr_test", envelope.Meta.CorrelationID)
	}
}

func TestRenderErrorVerboseCauseIsBoundedAndSanitized(t *testing.T) {
	cmd, _, stderr := bufferedCommand()
	cause := errors.New(`Get "http://operator:password@127.0.0.1:1/health?token=hidden#fragment": ` + strings.Repeat("connection refused ", 40))
	err := loomerrors.Wrap("portal.operation_failed", "portal", "http://127.0.0.1:1", "The portal operation could not complete.", cause)
	if got := renderError(cmd, &options{verboseOutput: true}, "corr_test", err); got == nil {
		t.Fatal("expected returned error")
	}
	output := stderr.String()
	if strings.Count(output, "Cause: ") != 1 || !strings.Contains(output, "http://127.0.0.1:1/health") {
		t.Fatalf("verbose cause missing sanitized URL:\n%s", output)
	}
	for _, secret := range []string{"operator", "password", "token=", "hidden", "fragment"} {
		if strings.Contains(output, secret) {
			t.Fatalf("verbose cause leaked %q:\n%s", secret, output)
		}
	}
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "Cause: ") && len([]rune(strings.TrimPrefix(line, "Cause: "))) > maxCLIErrorCauseLength {
			t.Fatalf("cause exceeds %d runes: %d", maxCLIErrorCauseLength, len([]rune(line)))
		}
	}

	cmd, _, stderr = bufferedCommand()
	_ = renderError(cmd, &options{}, "corr_test", err)
	if strings.Contains(stderr.String(), "Cause: ") {
		t.Fatalf("normal error output should stay concise:\n%s", stderr.String())
	}
}

func TestEnterOfflineOneShotNamesSelectedHTTPAndUnixTargets(t *testing.T) {
	t.Setenv("LOOM_CONFIG_FILE", "")
	t.Setenv("LOOM_MAIN_URL", "")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for refused-port fixture: %v", err)
	}
	httpTarget := "http://" + listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close refused-port fixture: %v", err)
	}
	configPath := filepath.Join(t.TempDir(), "loom.env")
	boxPath := filepath.Join(t.TempDir(), "loom-box")
	if err := os.WriteFile(configPath, []byte(strings.Join([]string{
		"LOOM_NODE_ID=workspace-test",
		"LOOM_NODE_KIND=workspace",
		"LOOM_NODE_ROLE=workspace",
		"LOOM_RUNTIME_CLASS=workspace",
		"LOOM_MAIN_URL=" + httpTarget,
		"LOOM_BOX_PATH=" + boxPath,
		"LOOM_BOX_PROFILE=workspace",
		"",
	}, "\n")), 0o600); err != nil {
		t.Fatalf("write HTTP fixture config: %v", err)
	}
	stdout, stderr, err := executeRootCommand(
		"--config", configPath,
		"--no-animation", "--no-color",
		"enter", "--exit-after-render", "--no-boot-animation", "--start", "home",
	)
	if err != nil {
		t.Fatalf("HTTP offline render returned error: %v stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "offline") || !strings.Contains(stdout, httpTarget) {
		t.Fatalf("HTTP offline render did not name selected target %q:\n%s", httpTarget, stdout)
	}

	socketPath := filepath.Join(t.TempDir(), "missing", "loomd.sock")
	stdout, stderr, err = executeRootCommand(
		"--config", configPath,
		"--socket", socketPath,
		"--no-animation", "--no-color",
		"enter", "--exit-after-render", "--no-boot-animation", "--start", "doctor",
	)
	if err != nil {
		t.Fatalf("Unix offline render returned error: %v stderr=%s", err, stderr)
	}
	pathPrefix := socketPath
	if len(pathPrefix) > 64 {
		pathPrefix = pathPrefix[:64]
	}
	if !strings.Contains(stdout, "Main node is unreachable") || !strings.Contains(stdout, pathPrefix) || !strings.Contains(stdout, "loomd.sock") {
		t.Fatalf("Unix offline render did not name selected socket %q:\n%s", socketPath, stdout)
	}
}

func bufferedCommand() (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	return cmd, &stdout, &stderr
}

func TestBackupStatusDiagnosticGenericHumanHint(t *testing.T) {
	valid := "Inspect loom watched-roots status --project synthetic-project; then loom project plan <project>."
	for _, tc := range []struct{ name, hint string }{{"valid", valid}, {"empty", ""}, {"whitespace", " \n\t "}, {"controls", "loom\nproject\tplan \x1b[31mfixture\x00\r\u202etest"}, {"invalid_utf8", "loom project " + string([]byte{0xff}) + "plan"}, {"oversize", strings.Repeat("界", 300)}, {"exact_ascii", strings.Repeat("x", 505)}, {"over_ascii", strings.Repeat("x", 506)}} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := &cobra.Command{Use: "fixture status"}
			var stderr bytes.Buffer
			cmd.SetErr(&stderr)
			failure := &localclient.RequestError{StatusCode: 404, Envelope: response.ErrorEnvelope{Error: response.ErrorBody{Code: "fixture.missing", Summary: "Missing", Hint: tc.hint, CorrelationID: "corr_hint"}, Meta: response.Meta{CorrelationID: "corr_hint"}}}
			if err := renderError(cmd, &options{}, "corr_request", failure); err == nil {
				t.Fatal("hint rendering swallowed error")
			}
			output := stderr.String()
			var hintLine string
			for _, line := range strings.SplitAfter(output, "\n") {
				if strings.HasPrefix(line, "Hint: ") {
					if hintLine != "" {
						t.Fatal("multiple hint lines")
					}
					hintLine = line
				}
			}
			if tc.name == "empty" || tc.name == "whitespace" {
				if hintLine != "" {
					t.Fatal("empty hint rendered")
				}
				return
			}
			if hintLine == "" || len(hintLine) > 512 || !utf8.ValidString(hintLine) || strings.ContainsAny(hintLine, "\x1b\x00\r\t\u202e") {
				t.Fatalf("unbounded/unsafe hint: %q", hintLine)
			}
			if tc.name == "valid" && hintLine != "Hint: "+valid+"\n" {
				t.Fatalf("valid command text changed: %q", hintLine)
			}
			if tc.name == "exact_ascii" && len(hintLine) != 512 {
				t.Fatalf("exact boundary changed: %d", len(hintLine))
			}
			if (tc.name == "oversize" || tc.name == "over_ascii") && !strings.HasSuffix(hintLine, "…\n") {
				t.Fatal("truncation was silent")
			}
		})
	}
}
func TestBackupStatusDiagnosticHintCloudPriorityAndJSON(t *testing.T) {
	failure := &localclient.RequestError{StatusCode: 500, Envelope: response.ErrorEnvelope{Error: response.ErrorBody{Code: "cloud.failed", Summary: "failed to read private key file: permission denied", Hint: "server hint must lose to the cloud override", CorrelationID: "corr_hint"}, Meta: response.Meta{CorrelationID: "corr_hint"}}}
	cmd := &cobra.Command{Use: "cloud status"}
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	_ = renderError(cmd, &options{}, "corr_request", failure)
	if !strings.Contains(stderr.String(), "Hint: Live cloud checks require the LOOM service context") || strings.Contains(stderr.String(), failure.Envelope.Error.Hint) {
		t.Fatalf("cloud priority changed: %s", stderr.String())
	}
	for _, hint := range []string{"", "original\ncontrol\x1b text", strings.Repeat("界", 300)} {
		original := response.ErrorEnvelope{Error: response.ErrorBody{Code: "fixture.missing", Hint: hint, CorrelationID: "corr_hint"}, Meta: response.Meta{CorrelationID: "corr_hint"}}
		cmd := &cobra.Command{Use: "fixture status"}
		var stdout bytes.Buffer
		cmd.SetOut(&stdout)
		_ = renderError(cmd, &options{jsonOutput: true}, "corr_request", &localclient.RequestError{StatusCode: 404, Envelope: original})
		want, _ := json.Marshal(original)
		if stdout.String() != string(want)+"\n" {
			t.Fatalf("JSON hint was modified: %q", stdout.String())
		}
	}
}
