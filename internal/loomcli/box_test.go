package loomcli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/dropzone"
	"loom.local/loom/internal/lane"
)

func TestBoxStatusMissingJSONAndHuman(t *testing.T) {
	t.Setenv("LOOM_BOX_PATH", "")
	t.Setenv("LOOM_BOX_PROFILE", "")
	root := filepath.Join(t.TempDir(), "LOOM Box")

	stdout, stderr, err := executeRootCommand("--json", "box", "status", "--path", root, "--profile", "workspace")
	if err != nil {
		t.Fatalf("box status json returned error: %v stderr=%s", err, stderr)
	}
	var status box.Status
	if decodeErr := json.Unmarshal([]byte(stdout), &status); decodeErr != nil {
		t.Fatalf("decode status json: %v output=%s", decodeErr, stdout)
	}
	if strings.Contains(strings.ToLower(stdout), "dropzone") {
		t.Fatalf("workspace Box status JSON exposed retired Dropzone fields: %s", stdout)
	}
	if status.State != "missing" || status.Initialized {
		t.Fatalf("unexpected missing status: %#v", status)
	}
	if status.RootPath != root || status.Profile != box.ProfileWorkspace || status.DropzoneState != "" {
		t.Fatalf("unexpected status fields: %#v", status)
	}

	stdout, stderr, err = executeRootCommand("box", "status", "--path", root, "--profile", "workspace")
	if err != nil {
		t.Fatalf("box status human returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{"LOOM Box: missing", "Profile: workspace", "loom-lane", "loom box init"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "Dropzone") {
		t.Fatalf("workspace Box status exposed retired Dropzone wording:\n%s", stdout)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("box status should not create root, stat err=%v", err)
	}
}

func TestBoxInitCreatesBoxAndIsIdempotent(t *testing.T) {
	t.Setenv("LOOM_BOX_PATH", "")
	t.Setenv("LOOM_BOX_PROFILE", "")
	root := filepath.Join(t.TempDir(), "LOOM Box")

	stdout, stderr, err := executeRootCommand("--json", "box", "init", "--path", root, "--profile", "workspace")
	if err != nil {
		t.Fatalf("box init json returned error: %v stderr=%s", err, stderr)
	}
	var result box.InitResult
	if decodeErr := json.Unmarshal([]byte(stdout), &result); decodeErr != nil {
		t.Fatalf("decode init json: %v output=%s", decodeErr, stdout)
	}
	if strings.Contains(strings.ToLower(stdout), "dropzone") {
		t.Fatalf("workspace Box init JSON exposed retired Dropzone fields: %s", stdout)
	}
	if result.DryRun || result.StatusAfter.State != "ok" || len(result.CreatedDirs) == 0 || len(result.CreatedFiles) == 0 {
		t.Fatalf("unexpected init result: %#v", result)
	}
	if _, err := os.Stat(filepath.Join(root, ".loom", "box.yaml")); err != nil {
		t.Fatalf("expected box.yaml: %v", err)
	}

	stdout, stderr, err = executeRootCommand("box", "init", "--path", root, "--profile", "workspace")
	if err != nil {
		t.Fatalf("second box init returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{"LOOM Box init: created", "Existing directories:", "Existing files:", "Status: ok", "Workspace LOOM Lane status path:"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "Dropzone") {
		t.Fatalf("workspace Box init exposed retired Dropzone wording:\n%s", stdout)
	}
}

func TestBoxRepairCreatesLaneAndLaneStatusReportsPendingItems(t *testing.T) {
	t.Setenv("LOOM_BOX_PATH", "")
	t.Setenv("LOOM_BOX_PROFILE", "")
	root := filepath.Join(t.TempDir(), "LOOM Box")

	stdout, stderr, err := executeRootCommand("box", "repair", "--path", root, "--profile", "workspace")
	if err != nil {
		t.Fatalf("box repair returned error: %v stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "LOOM Box repair: applied") || !strings.Contains(stdout, box.DefaultLaneDirName) {
		t.Fatalf("box repair output should mention Lane:\n%s", stdout)
	}
	if _, err := os.Stat(filepath.Join(root, box.DefaultLaneDirName)); err != nil {
		t.Fatalf("loom-lane was not created: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, box.DefaultLaneDirName, "payload.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatalf("write Lane payload: %v", err)
	}

	stdout, stderr, err = executeRootCommand("--json", "lane", "status", "--path", root, "--profile", "workspace")
	if err != nil {
		t.Fatalf("lane status returned error: %v stderr=%s", err, stderr)
	}
	var status lane.Status
	if decodeErr := json.Unmarshal([]byte(stdout), &status); decodeErr != nil {
		t.Fatalf("decode lane status: %v output=%s", decodeErr, stdout)
	}
	if status.State != lane.StatePending || status.PendingItems != 1 || status.PendingBytes != int64(len("payload")) {
		t.Fatalf("unexpected lane status: %#v", status)
	}
}

func TestLaneAcknowledgePendingCommandKeepsFileAndClearsAttention(t *testing.T) {
	t.Setenv("LOOM_BOX_PATH", "")
	t.Setenv("LOOM_BOX_PROFILE", "")
	boxStateRoot := useIsolatedCLIBoxState(t)
	root := filepath.Join(t.TempDir(), "LOOM Box")
	if _, stderr, err := executeRootCommand("box", "repair", "--path", root, "--profile", "workspace"); err != nil {
		t.Fatalf("box repair returned error: %v stderr=%s", err, stderr)
	}
	laneFile := filepath.Join(root, box.DefaultLaneDirName, "payload.txt")
	if err := os.WriteFile(laneFile, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write Lane payload: %v", err)
	}

	stdout, stderr, err := executeRootCommand("--json", "lane", "acknowledge-pending", "payload.txt", "--path", root, "--profile", "workspace")
	if err != nil {
		t.Fatalf("lane acknowledge-pending returned error: %v stderr=%s", err, stderr)
	}
	var result lane.AttentionResult
	if decodeErr := json.Unmarshal([]byte(stdout), &result); decodeErr != nil {
		t.Fatalf("decode acknowledgement result: %v output=%s", decodeErr, stdout)
	}
	if result.AttentionStatus != lane.AttentionStatusAcknowledged {
		t.Fatalf("unexpected acknowledgement result: %#v", result)
	}
	if _, err := os.Stat(laneFile); err != nil {
		t.Fatalf("acknowledgement should not remove Lane file: %v", err)
	}

	stdout, stderr, err = executeRootCommand("--json", "lane", "status", "--path", root, "--profile", "workspace")
	if err != nil {
		t.Fatalf("lane status returned error: %v stderr=%s", err, stderr)
	}
	var status lane.Status
	if decodeErr := json.Unmarshal([]byte(stdout), &status); decodeErr != nil {
		t.Fatalf("decode lane status: %v output=%s", decodeErr, stdout)
	}
	if status.PendingItems != 1 || status.ActivePendingItems != 0 || status.AcknowledgedPendingItems != 1 {
		t.Fatalf("unexpected acknowledged lane status: %#v", status)
	}
	if _, err := os.Stat(filepath.Join(boxStateRoot, "lane", "attention.json")); err != nil {
		t.Fatalf("canonical Lane attention state was not written outside Box: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".loom", "state")); !os.IsNotExist(err) {
		t.Fatalf("fresh canonical Lane command created Box runtime state: %v", err)
	}
}

func TestLaneLegacyOnlyMutationContinuityAndDivergenceFailClosed(t *testing.T) {
	boxStateRoot := useIsolatedCLIBoxState(t)
	root := filepath.Join(t.TempDir(), "LOOM Box")
	if _, stderr, err := executeRootCommand("box", "repair", "--path", root, "--profile", "workspace"); err != nil {
		t.Fatalf("box repair returned error: %v stderr=%s", err, stderr)
	}
	legacyStateRoot := filepath.Join(root, ".loom", "state")
	if err := os.MkdirAll(filepath.Join(legacyStateRoot, "lane"), 0o755); err != nil {
		t.Fatal(err)
	}
	laneFile := filepath.Join(root, box.DefaultLaneDirName, "legacy.txt")
	if err := os.WriteFile(laneFile, []byte("legacy payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, stderr, err := executeRootCommand("--json", "lane", "acknowledge-pending", "legacy.txt", "--path", root, "--profile", "workspace"); err != nil {
		t.Fatalf("legacy-only Lane mutation returned error: %v stderr=%s", err, stderr)
	}
	if _, err := os.Stat(filepath.Join(legacyStateRoot, "lane", "attention.json")); err != nil {
		t.Fatalf("legacy-only Lane mutation did not stay on legacy root: %v", err)
	}
	if _, err := os.Stat(boxStateRoot); !os.IsNotExist(err) {
		t.Fatalf("legacy-only Lane mutation split writes into canonical root: %v", err)
	}

	for path, payload := range map[string]string{
		filepath.Join(legacyStateRoot, "lane", "batches", "conflict.json"): "legacy\n",
		filepath.Join(boxStateRoot, "lane", "batches", "conflict.json"):    "canonical\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cwd := t.TempDir()
	previousCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previousCWD) })
	_, _, err = executeRootCommand("--json", "lane", "status", "--source-only", "--path", root, "--profile", "workspace")
	if err == nil || !strings.Contains(err.Error(), "runtime state is unavailable") {
		t.Fatalf("divergent Lane status did not fail closed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cwd, "lane")); !os.IsNotExist(err) {
		t.Fatalf("divergent Lane status accessed a relative fallback path: %v", err)
	}
}

func TestLaneStatusCLISelectsSourceOnlyAndExactProfiles(t *testing.T) {
	t.Setenv("LOOM_BOX_PATH", "")
	t.Setenv("LOOM_BOX_PROFILE", "")
	root := filepath.Join(t.TempDir(), "LOOM Box")
	if _, stderr, err := executeRootCommand("box", "repair", "--path", root, "--profile", "workspace"); err != nil {
		t.Fatalf("box repair returned error: %v stderr=%s", err, stderr)
	}
	laneRoot := filepath.Join(root, box.DefaultLaneDirName)
	if err := os.MkdirAll(filepath.Join(laneRoot, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(laneRoot, "node_modules", "pkg", "index.js"), []byte("dependency"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(laneRoot, ".loomignore"), []byte("custom.tmp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(laneRoot, "custom.tmp"), []byte("custom"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := executeRootCommand("--json", "lane", "status", "--path", root, "--profile", "workspace", "--source-only")
	if err != nil {
		t.Fatalf("source-only status returned error: %v stderr=%s", err, stderr)
	}
	var sourceOnly lane.Status
	if err := json.Unmarshal([]byte(stdout), &sourceOnly); err != nil {
		t.Fatal(err)
	}
	if sourceOnly.Profile != "source_only" || sourceOnly.PendingFiles != 1 || sourceOnly.IgnoredFileCount < 2 {
		t.Fatalf("unexpected source-only CLI status: %#v", sourceOnly)
	}

	stdout, stderr, err = executeRootCommand("--json", "lane", "status", "--path", root, "--profile", "workspace", "--exact")
	if err != nil {
		t.Fatalf("exact status returned error: %v stderr=%s", err, stderr)
	}
	var exact lane.Status
	if err := json.Unmarshal([]byte(stdout), &exact); err != nil {
		t.Fatal(err)
	}
	if exact.Profile != "exact" || exact.PendingFiles != 3 || len(exact.Warnings) == 0 {
		t.Fatalf("unexpected exact CLI status: %#v", exact)
	}
}

func TestBoxInitDryRunDoesNotWrite(t *testing.T) {
	t.Setenv("LOOM_BOX_PATH", "")
	t.Setenv("LOOM_BOX_PROFILE", "")
	root := filepath.Join(t.TempDir(), "LOOM Box")

	stdout, stderr, err := executeRootCommand("--json", "box", "init", "--path", root, "--profile", "main", "--dry-run")
	if err != nil {
		t.Fatalf("box init dry-run returned error: %v stderr=%s", err, stderr)
	}
	var result box.InitResult
	if decodeErr := json.Unmarshal([]byte(stdout), &result); decodeErr != nil {
		t.Fatalf("decode dry-run json: %v output=%s", decodeErr, stdout)
	}
	if !result.DryRun || len(result.PlannedDirs) == 0 || len(result.PlannedFiles) == 0 {
		t.Fatalf("unexpected dry-run result: %#v", result)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("dry-run should not create root, stat err=%v", err)
	}
}

func TestBoxWatchPlanCommandLocalJSON(t *testing.T) {
	t.Setenv("LOOM_BOX_PATH", "")
	t.Setenv("LOOM_BOX_PROFILE", "")
	root := filepath.Join(t.TempDir(), "LOOM Box")

	if _, stderr, err := executeRootCommand("--json", "box", "init", "--path", root, "--profile", "workspace"); err != nil {
		t.Fatalf("box init returned error: %v stderr=%s", err, stderr)
	}
	stdout, stderr, err := executeRootCommand("--json", "box", "watch-plan", "--path", root, "--profile", "workspace")
	if err != nil {
		t.Fatalf("box watch-plan returned error: %v stderr=%s", err, stderr)
	}
	var plan box.WatchPlan
	if decodeErr := json.Unmarshal([]byte(stdout), &plan); decodeErr != nil {
		t.Fatalf("decode watch plan json: %v output=%s", decodeErr, stdout)
	}
	if plan.RootPath != root || plan.Profile != box.ProfileWorkspace || len(plan.WatchedRoots) != 2 {
		t.Fatalf("unexpected watch plan: %#v", plan)
	}
	keys := []string{plan.WatchedRoots[0].BackendRootKey, plan.WatchedRoots[1].BackendRootKey}
	if strings.Join(keys, ",") != "loom_box__documents,loom_box__notes" {
		t.Fatalf("watch plan keys = %v", keys)
	}
	for _, root := range plan.WatchedRoots {
		if strings.Contains(root.BackendRootKey, "dropzone") || strings.Contains(root.BackendRootKey, "projects") {
			t.Fatalf("watch plan should exclude projects/dropzone: %#v", root)
		}
	}
	if len(plan.Commands) == 0 || !strings.Contains(plan.Commands[0].Shell, "loom-node-agent watched-roots apply-plan") {
		t.Fatalf("expected node-agent apply command: %#v", plan.Commands)
	}
}

func TestBoxDropzoneCommandsInspectHistoricalRecordsReadOnly(t *testing.T) {
	t.Setenv("LOOM_BOX_PATH", "")
	t.Setenv("LOOM_BOX_PROFILE", "")
	boxStateRoot := useIsolatedCLIBoxState(t)
	root := filepath.Join(t.TempDir(), "LOOM Box")
	if _, stderr, err := executeRootCommand("--json", "box", "init", "--path", root, "--profile", "workspace"); err != nil {
		t.Fatalf("box init returned error: %v stderr=%s", err, stderr)
	}
	transfersRoot := filepath.Join(boxStateRoot, "dropzone", "transfers")
	if err := os.MkdirAll(transfersRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join("..", "dropzone", "testdata", "historical", "transfer-v0.4.2.json"))
	if err != nil {
		t.Fatal(err)
	}
	recordPath := filepath.Join(transfersRoot, "drop_historical_transfer.json")
	if err := os.WriteFile(recordPath, fixture, 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := executeRootCommand("--json", "box", "dropzone", "status", "--path", root, "--profile", "workspace")
	if err != nil {
		t.Fatalf("dropzone status returned error: %v stderr=%s", err, stderr)
	}
	var status dropzone.Status
	if err := json.Unmarshal([]byte(stdout), &status); err != nil {
		t.Fatalf("decode status: %v output=%s", err, stdout)
	}
	if status.RuntimeState != dropzone.RuntimeRetired || status.Counts[dropzone.StatusAccepted] != 1 || status.PolicyEnabled {
		t.Fatalf("unexpected historical Dropzone status: %#v", status)
	}

	stdout, stderr, err = executeRootCommand("--json", "box", "dropzone", "inspect", "drop_historical_transfer", "--path", root, "--profile", "workspace")
	if err != nil {
		t.Fatalf("dropzone inspect returned error: %v stderr=%s", err, stderr)
	}
	var inspected boxDropzoneInspectResult
	if err := json.Unmarshal([]byte(stdout), &inspected); err != nil {
		t.Fatalf("decode inspect: %v output=%s", err, stdout)
	}
	if inspected.Record.TransferID != "drop_historical_transfer" || inspected.Record.Status != dropzone.StatusAccepted {
		t.Fatalf("unexpected inspected record: %#v", inspected.Record)
	}

	for _, command := range [][]string{
		{"box", "dropzone", "retry", "drop_historical_transfer"},
		{"box", "dropzone", "pause", "drop_historical_transfer"},
		{"box", "dropzone", "resume", "drop_historical_transfer"},
		{"box", "dropzone", "clean", "--accepted"},
		{"box", "dropzone", "send", "payload.bin"},
	} {
		if _, _, err := executeRootCommand(command...); err == nil {
			t.Fatalf("retired mutation command remained available: %v", command)
		}
	}
	after, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(fixture) {
		t.Fatal("historical transfer record changed during read-only inspection")
	}
	if _, err := os.Stat(filepath.Join(root, "Dropzone")); !os.IsNotExist(err) {
		t.Fatalf("historical inspection created a Dropzone directory: %v", err)
	}
}
func TestBoxStatusReadsValidContract(t *testing.T) {
	t.Setenv("LOOM_BOX_PATH", "")
	t.Setenv("LOOM_BOX_PROFILE", "")
	root := filepath.Join(t.TempDir(), "LOOM Box")
	writeBoxFixture(t, root, box.ProfileMain)

	stdout, stderr, err := executeRootCommand("--json", "box", "status", "--path", root)
	if err != nil {
		t.Fatalf("box status returned error: %v stderr=%s", err, stderr)
	}
	var status box.Status
	if decodeErr := json.Unmarshal([]byte(stdout), &status); decodeErr != nil {
		t.Fatalf("decode status json: %v output=%s", decodeErr, stdout)
	}
	for _, retired := range []string{"dropzone", `"lane_state"`, `"lane"`} {
		if strings.Contains(strings.ToLower(stdout), retired) {
			t.Fatalf("main Box status JSON exposed retired intake %q: %s", retired, stdout)
		}
	}
	if status.State != "ok" || !status.Initialized || status.ContractState != "valid" {
		t.Fatalf("unexpected valid status: %#v", status)
	}
	if status.Profile != box.ProfileMain || status.DropzoneState != "" || status.Lane != nil || status.LaneState != "" {
		t.Fatalf("main status should omit retired intake state: %#v", status)
	}

	stdout, stderr, err = executeRootCommand("box", "status", "--path", root)
	if err != nil {
		t.Fatalf("box status human returned error: %v stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "Main intake: Storage Imports") {
		t.Fatalf("main status omitted current intake destination:\n%s", stdout)
	}
	for _, retired := range []string{"Dropzone", "dropzone", "LOOM Lane pending", "Workspace LOOM Lane"} {
		if strings.Contains(stdout, retired) {
			t.Fatalf("main status exposed retired intake %q:\n%s", retired, stdout)
		}
	}
}

func TestBoxRepairDryRunJSONReportsExistingContractIdentity(t *testing.T) {
	t.Setenv("LOOM_BOX_PATH", "")
	t.Setenv("LOOM_BOX_PROFILE", "")
	root := filepath.Join(t.TempDir(), "LOOM Box")
	writeBoxFixture(t, root, box.ProfileWorkspace)

	stdout, stderr, err := executeRootCommand("--json", "box", "repair", "--path", root, "--dry-run")
	if err != nil {
		t.Fatalf("box repair dry-run returned error: %v stderr=%s", err, stderr)
	}
	var result box.InitResult
	if decodeErr := json.Unmarshal([]byte(stdout), &result); decodeErr != nil {
		t.Fatalf("decode repair json: %v output=%s", decodeErr, stdout)
	}
	if result.Profile != box.ProfileWorkspace || result.OwnerNode != "test-node" {
		t.Fatalf("top-level repair identity should come from existing contract: %#v", result)
	}
	if result.StatusBefore.Profile != box.ProfileWorkspace || result.StatusAfter.Profile != box.ProfileWorkspace {
		t.Fatalf("status identities should stay contract-aligned: %#v", result)
	}
}

func TestBoxPathUsesConfigAndFlagPrecedence(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "loom.env")
	configBox := filepath.Join(dir, "Config Box")
	flagBox := filepath.Join(dir, "Flag Box")
	if err := os.WriteFile(configPath, []byte("LOOM_BOX_PATH="+configBox+"\nLOOM_BOX_PROFILE=main\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	stdout, stderr, err := executeRootCommand("--config", configPath, "box", "path")
	if err != nil {
		t.Fatalf("box path returned error: %v stderr=%s", err, stderr)
	}
	if strings.TrimSpace(stdout) != configBox {
		t.Fatalf("box path = %q, want %q", strings.TrimSpace(stdout), configBox)
	}

	stdout, stderr, err = executeRootCommand("--config", configPath, "--json", "box", "path", "--path", flagBox, "--profile", "workspace")
	if err != nil {
		t.Fatalf("box path json returned error: %v stderr=%s", err, stderr)
	}
	var result box.PathResult
	if decodeErr := json.Unmarshal([]byte(stdout), &result); decodeErr != nil {
		t.Fatalf("decode path json: %v output=%s", decodeErr, stdout)
	}
	if result.RootPath != flagBox || result.PathSource != "flag" || result.Profile != box.ProfileWorkspace || result.ProfileSource != "flag" {
		t.Fatalf("flag precedence failed: %#v", result)
	}
}

func writeBoxFixture(t *testing.T, root string, profile string) {
	t.Helper()
	useIsolatedCLIBoxState(t)
	dropzoneStatus := box.DropzoneTransferFuture
	if profile == box.ProfileMain {
		dropzoneStatus = box.DropzoneTransferInactive
	}
	for _, rel := range []string{
		"Projects",
		"Notes",
		"Documents",
		box.DefaultLaneDirName,
		"Dropzone",
		".loom/policies",
		".loom/state/dropzone",
		".loom/state/lane",
		".loom/state/lane/batches",
		".loom/state/lane/sent",
		".loom/state/lane/logs",
	} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(rel)), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
	}
	for _, rel := range []string{".loom/policies/notes.watch.yaml", ".loom/policies/documents.watch.yaml", ".loom/policies/lane.transfer.yaml", ".loom/policies/dropzone.transfer.yaml"} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte("schema_version: test\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	contract := `schema_version: loom.box.v0.4.1
box_id: box_test
owner_node: test-node
profile: ` + profile + `
root_path: ` + root + `
areas:
  projects:
    path: Projects
    enabled: true
  notes:
    path: Notes
    enabled: true
  documents:
    path: Documents
    enabled: true
  lane:
    path: loom-lane
    enabled: true
  dropzone:
    path: Dropzone
    enabled: false
    transfer_status: ` + dropzoneStatus + `
default_project_path: Projects
policies:
  notes: .loom/policies/notes.watch.yaml
  documents: .loom/policies/documents.watch.yaml
  lane: .loom/policies/lane.transfer.yaml
  dropzone: .loom/policies/dropzone.transfer.yaml
`
	if err := os.WriteFile(filepath.Join(root, ".loom", "box.yaml"), []byte(contract), 0o644); err != nil {
		t.Fatalf("write box.yaml: %v", err)
	}
}

func useIsolatedCLIBoxState(t *testing.T) string {
	t.Helper()
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	boxStateRoot := filepath.Join(runtimeRoot, "box-state")
	configPath := filepath.Join(t.TempDir(), "loom.env")
	payload := "LOOM_DATA_DIR=" + runtimeRoot + "\nLOOM_BOX_STATE_ROOT=" + boxStateRoot + "\n"
	if err := os.WriteFile(configPath, []byte(payload), 0o600); err != nil {
		t.Fatalf("write isolated CLI config: %v", err)
	}
	t.Setenv("LOOM_CONFIG_FILE", configPath)
	return boxStateRoot
}
