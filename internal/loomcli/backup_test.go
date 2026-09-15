package loomcli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/backupcoverage"
	"loom.local/loom/internal/box"
	"loom.local/loom/internal/response"
)

func TestBackupCreateDryRunDoesNotRequireDaemon(t *testing.T) {
	cmd := NewRootCommand()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{"--json", "backup", "create", "--production", "--dry-run"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("backup create dry-run returned error: %v stderr=%s", err, stderr.String())
	}
	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			Status  string `json:"status"`
			Backend string `json:"backend"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("dry-run output was not JSON: %v output=%q", err, stdout.String())
	}
	if !envelope.OK || envelope.Data.Status != "planned" || envelope.Data.Backend != "maintenance.main_backup.operational_package" {
		t.Fatalf("unexpected dry-run envelope: %#v", envelope)
	}
	for _, want := range []string{"PostgreSQL custom-format dump", "redacted service, install, and active release configuration", "migration, update, and health state", "authenticated operational package manifest", "complete local user-data generation"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("dry-run output missing %q: %s", want, stdout.String())
		}
	}
	for _, forbidden := range []string{"canonical Box Documents snapshot", "canonical Box Notes snapshot", "storage-archive snapshot", "Lane Imports complete-physical snapshot", "v0.9 backup manifest"} {
		if strings.Contains(stdout.String(), forbidden) {
			t.Fatalf("dry-run still advertises full-generation capture %q: %s", forbidden, stdout.String())
		}
	}
}

func TestBackupCoverageDefaultsToMainBackedDaemon(t *testing.T) {
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/backup/coverage" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
		response.WriteJSON(w, http.StatusOK, response.Success("corr_test", backupcoverage.Report{
			SchemaVersion: backupcoverage.SchemaVersion,
			Mode:          backupcoverage.ModeMainBacked,
			Status:        backupcoverage.OverallOK,
			Entries:       []backupcoverage.Entry{},
		}))
	})
	defer shutdown()

	stdout, stderr, err := executeRootCommand("--socket", socketPath, "--json", "backup", "coverage")
	if err != nil {
		t.Fatalf("backup coverage returned error: %v stderr=%s", err, stderr)
	}
	var envelope response.Envelope[backupcoverage.Report]
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("coverage output was not JSON: %v output=%q", err, stdout)
	}
	if envelope.Data.Mode != backupcoverage.ModeMainBacked {
		t.Fatalf("mode = %q, want %q", envelope.Data.Mode, backupcoverage.ModeMainBacked)
	}
}

func TestBackupContractsPlanUsesMainBackedCoverage(t *testing.T) {
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/backup/coverage" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
		response.WriteJSON(w, http.StatusOK, response.Success("corr_test", backupcoverage.Report{
			SchemaVersion: backupcoverage.SchemaVersion,
			Mode:          backupcoverage.ModeMainBacked,
			Status:        backupcoverage.OverallOK,
			Entries: []backupcoverage.Entry{
				{Key: "postgres", Label: "PostgreSQL", Category: "runtime_config", Status: backupcoverage.StatusCovered, Critical: true},
			},
		}))
	})
	defer shutdown()

	stdout, stderr, err := executeRootCommand("--socket", socketPath, "--json", "backup", "contracts", "plan")
	if err != nil {
		t.Fatalf("backup contracts plan returned error: %v stderr=%s", err, stderr)
	}
	var envelope response.Envelope[backupcoverage.ContractPlan]
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("contracts output was not JSON: %v output=%q", err, stdout)
	}
	if envelope.Data.Mode != backupcoverage.ModeMainBacked || envelope.Data.Summary.Satisfied != 1 {
		t.Fatalf("unexpected contract plan: %#v", envelope.Data)
	}
}

func TestBackupContractsListUsesDaemonEndpoint(t *testing.T) {
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/backup/contracts" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
		response.WriteJSON(w, http.StatusOK, response.Success("corr_test", backupcontracts.ListResult{
			Contracts: []backupcontracts.ContractRecord{{Key: "field-data", Path: "/box/.loom/contracts/backup/field-data.yaml", Status: backupcontracts.StatusActive}},
		}))
	})
	defer shutdown()

	stdout, stderr, err := executeRootCommand("--socket", socketPath, "--json", "backup", "contracts", "list")
	if err != nil {
		t.Fatalf("backup contracts list returned error: %v stderr=%s", err, stderr)
	}
	var envelope response.Envelope[backupcontracts.ListResult]
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("contracts list output was not JSON: %v output=%q", err, stdout)
	}
	if len(envelope.Data.Contracts) != 1 || envelope.Data.Contracts[0].Key != "field-data" {
		t.Fatalf("unexpected contract list: %#v", envelope.Data)
	}
}

func TestBackupContractsCreateDryRunSendsDaemonRequest(t *testing.T) {
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/backup/contracts" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
		var input backupcontracts.CreateRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if input.Key != "field-data" || input.TargetPath != "/srv/field-data" || !input.DryRun {
			t.Fatalf("unexpected create request: %#v", input)
		}
		response.WriteJSON(w, http.StatusOK, response.Success("corr_test", backupcontracts.LifecycleResult{
			Action: "create",
			DryRun: true,
			Path:   "/box/.loom/contracts/backup/field-data.yaml",
			Contract: backupcontracts.Contract{
				SchemaVersion: backupcontracts.SchemaVersion,
				Key:           "field-data",
				OwnerNode:     "main",
				Status:        backupcontracts.StatusActive,
				Target:        backupcontracts.TargetSpec{Scope: backupcontracts.TargetScopeOwnerNodeAbsolute, Path: "/srv/field-data"},
				Backup:        backupcontracts.BackupPolicy{Mode: backupcontracts.BackupModeIncrementalRaw},
			},
		}))
	})
	defer shutdown()

	stdout, stderr, err := executeRootCommand("--socket", socketPath, "--json", "backup", "contracts", "create", "field-data", "--path", "/srv/field-data", "--dry-run")
	if err != nil {
		t.Fatalf("backup contracts create dry-run returned error: %v stderr=%s", err, stderr)
	}
	var envelope response.Envelope[backupcontracts.LifecycleResult]
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("contracts create output was not JSON: %v output=%q", err, stdout)
	}
	if envelope.Data.Action != "create" || !envelope.Data.DryRun || envelope.Data.Contract.Key != "field-data" {
		t.Fatalf("unexpected create result: %#v", envelope.Data)
	}
}

func TestBackupContractsStatusAndPreflightRenderTruthfully(t *testing.T) {
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/backup/contracts":
			response.WriteJSON(w, http.StatusOK, response.Success("corr_test", backupcontracts.ProtectedFolderListResult{
				Folders: []backupcontracts.ProtectedFolderRecord{{
					Key: "photos", Lifecycle: backupcontracts.ProtectedFolderStatusWaitingForNode, OwnerNodeKey: "macbook",
					Contract: backupcontracts.Contract{Key: "photos", OwnerNode: "macbook", Target: backupcontracts.TargetSpec{Path: "/Users/test/Photos"}},
				}},
				Counts: map[string]int{backupcontracts.ProtectedFolderStatusWaitingForNode: 1},
			}))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/backup/contracts/preflights":
			var input backupcontracts.PreflightCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if input.NodeRef != "macbook" || input.Path != "/Users/test/Photos" {
				t.Fatalf("unexpected preflight input %#v", input)
			}
			response.WriteJSON(w, http.StatusAccepted, response.Success("corr_test", backupcontracts.PreflightRecord{PreflightID: "backup_preflight_test", TargetNodeID: "node_mac", RequestedPath: input.Path, Status: backupcontracts.PreflightStatusPending}))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	})
	defer shutdown()

	stdout, stderr, err := executeRootCommand("--socket", socketPath, "backup", "contracts", "status")
	if err != nil {
		t.Fatalf("status error=%v stderr=%s", err, stderr)
	}
	for _, want := range []string{"waiting_for_node", "photos", "macbook", "/Users/test/Photos", "waiting=1"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("status output missing %q:\n%s", want, stdout)
		}
	}

	stdout, stderr, err = executeRootCommand("--socket", socketPath, "backup", "contracts", "preflight", "--node", "macbook", "--path", "/Users/test/Photos")
	if err != nil {
		t.Fatalf("preflight error=%v stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "queued; waiting for owner node") || !strings.Contains(stdout, "Node: node_mac") {
		t.Fatalf("preflight output is not truthful:\n%s", stdout)
	}
}

func TestBackupContractPreflightRendersManagedPolicyEvidence(t *testing.T) {
	cmd := NewRootCommand()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	envelope := response.Success("corr_test", backupcontracts.PreflightRecord{
		PreflightID: "backup_preflight_policy", TargetNodeID: "node_mac", RequestedPath: "/Users/test/Project", Status: backupcontracts.PreflightStatusCompleted,
		Result: &backupcontracts.PreflightResult{Readable: true, Truncated: true, Policy: backupcontracts.PreflightPolicyEvidence{Profile: "managed", PolicyFiles: []string{".loomignore", "nested/.loomignore"}, IncludedCount: 4, IncludedBytes: 100, IgnoredCount: 6, IgnoredBytes: 200}, Findings: []backupcontracts.PreflightFinding{{Severity: "warning", Code: "preflight.scan_truncated", Message: "Estimate is partial."}}},
	})
	if err := renderBackupPreflightEnvelope(cmd, &options{}, envelope); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Policy profile: managed", "Policy files: .loomignore, nested/.loomignore", "Estimate truncated: true", "Finding [warning]: Estimate is partial.", "Ignored: 6 files, 200 bytes"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("policy evidence missing %q:\n%s", want, out.String())
		}
	}
}

func TestBackupContractsLifecycleCommandsUseTypedRoutes(t *testing.T) {
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/backup/contracts":
			var input backupcontracts.CreateRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if input.OwnerNode != "macbook" || input.PreflightID != "backup_preflight_done" {
				t.Fatalf("create input=%#v", input)
			}
			response.WriteJSON(w, http.StatusAccepted, response.Success("corr_test", backupcontracts.LifecycleResult{Action: "create", Contract: backupcontracts.Contract{Key: "photos", Target: backupcontracts.TargetSpec{Path: input.TargetPath}}, DesiredStateRegistered: true, NodeApplyQueued: true, NodeApplied: false, DesiredRevision: 1}))
		case "/v1/backup/contracts/photos/enable":
			response.WriteJSON(w, http.StatusAccepted, response.Success("corr_test", backupcontracts.LifecycleResult{Action: "enable", Contract: backupcontracts.Contract{Key: "photos"}, DesiredStateRegistered: true, NodeApplyQueued: true, DesiredRevision: 2}))
		case "/v1/backup/contracts/photos/recheck":
			response.WriteJSON(w, http.StatusAccepted, response.Success("corr_test", backupcontracts.PreflightRecord{PreflightID: "backup_preflight_recheck", TargetNodeID: "node_mac", RequestedPath: "/Users/test/Photos", Status: backupcontracts.PreflightStatusPending}))
		case "/v1/backup/contracts/photos/retry-activation":
			response.WriteJSON(w, http.StatusAccepted, response.Success("corr_test", backupcontracts.ReconcileQueueResult{NodeID: "node_mac", DesiredRevision: 2, DesiredHash: "sha256:test"}))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	})
	defer shutdown()

	stdout, stderr, err := executeRootCommand("--socket", socketPath, "backup", "contracts", "create", "photos", "--node", "macbook", "--path", "/Users/test/Photos", "--preflight", "backup_preflight_done", "--idempotency-key", "create-photos")
	if err != nil {
		t.Fatalf("create error=%v stderr=%s", err, stderr)
	}
	for _, want := range []string{"Node work queued: true", "Node applied: false", "Protection state: queued"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("create output missing %q:\n%s", want, stdout)
		}
	}
	for _, args := range [][]string{{"enable", "photos"}, {"recheck", "photos"}, {"retry-activation", "photos"}} {
		commandArgs := append([]string{"--socket", socketPath, "backup", "contracts"}, args...)
		if _, stderr, err := executeRootCommand(commandArgs...); err != nil {
			t.Fatalf("%s error=%v stderr=%s", args[0], err, stderr)
		}
	}
}

func TestBackupContractsDeleteRequiresConfirmation(t *testing.T) {
	_, stderr, err := executeRootCommand("backup", "contracts", "delete", "field-data")
	if err == nil {
		t.Fatal("expected delete without --yes to fail")
	}
	if !strings.Contains(stderr, "Pass --yes") {
		t.Fatalf("stderr missing confirmation message: %s", stderr)
	}
}

func TestBackupContractMigrateIgnorePolicyDryRun(t *testing.T) {
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/backup/contracts/migrate-ignore-policy" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var input backupcontracts.MigrateIgnorePolicyRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode migration request: %v", err)
		}
		if !input.DryRun || input.Apply || input.Yes {
			t.Fatalf("unexpected migration input: %#v", input)
		}
		response.WriteJSON(w, http.StatusOK, response.Success("corr_test", backupcontracts.MigrateIgnorePolicyResponse{
			Migration: backupcontracts.MigrateIgnorePolicyResult{
				DryRun:       true,
				ChangedCount: 1,
				Items: []backupcontracts.IgnorePolicyMigrationItem{{
					Key:      "field-data",
					Status:   backupcontracts.MigrationStatusMigrated,
					ToSchema: backupcontracts.SchemaVersion,
				}},
			},
		}))
	})
	defer shutdown()

	stdout, stderr, err := executeRootCommand("--socket", socketPath, "--json", "backup", "contract", "migrate-ignore-policy", "--dry-run")
	if err != nil {
		t.Fatalf("migration dry-run returned error: %v stderr=%s", err, stderr)
	}
	var envelope response.Envelope[backupcontracts.MigrateIgnorePolicyResponse]
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("migration output was not JSON: %v output=%q", err, stdout)
	}
	if envelope.Data.Migration.ChangedCount != 1 {
		t.Fatalf("unexpected migration output: %#v", envelope.Data)
	}
}

func TestBackupContractMigrateIgnorePolicyApplyRequiresYes(t *testing.T) {
	_, stderr, err := executeRootCommand("backup", "contract", "migrate-ignore-policy", "--apply")
	if err == nil || !strings.Contains(stderr, "--yes") {
		t.Fatalf("expected --yes error, err=%v stderr=%s", err, stderr)
	}
}

func TestBackupCoverageOverridesUseLocalMode(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{
		"object-store",
		"private-backups",
		filepath.Join("box", "Documents"),
		filepath.Join("box", "Notes"),
		filepath.Join("generated", "notes"),
		filepath.Join("storage-archive", "objects"),
		filepath.Join("storage-archive", "manifests"),
	} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	stdout, stderr, err := executeRootCommand(
		"--json",
		"backup",
		"coverage",
		"--data-dir", root,
		"--main-box-path", filepath.Join(root, "box"),
		"--box-notes-root", filepath.Join(root, "box", "Notes"),
		"--backup-root", filepath.Join(root, "backups", "main"),
	)
	if err != nil {
		t.Fatalf("backup coverage returned error: %v stderr=%s", err, stderr)
	}
	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			SchemaVersion string `json:"schema_version"`
			Mode          string `json:"mode"`
			Status        string `json:"status"`
			Summary       struct {
				Covered int `json:"covered"`
			} `json:"summary"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("coverage output was not JSON: %v output=%q", err, stdout)
	}
	if !envelope.OK || envelope.Data.SchemaVersion == "" || envelope.Data.Summary.Covered == 0 {
		t.Fatalf("unexpected coverage envelope: %#v", envelope)
	}
	if envelope.Data.Mode != backupcoverage.ModeLocal {
		t.Fatalf("mode = %q, want %q", envelope.Data.Mode, backupcoverage.ModeLocal)
	}
}

func TestBackupCoverageLocalIncludesStandaloneBackupContracts(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	for _, dir := range []string{
		"object-store",
		"private-backups",
		filepath.Join("box", "Documents"),
		"storage-retention",
		filepath.Join("storage-archive", "objects"),
		filepath.Join("storage-archive", "manifests"),
		filepath.Join("generated", "notes"),
	} {
		if err := os.MkdirAll(filepath.Join(dataDir, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	boxRoot := filepath.Join(root, "box")
	if _, err := box.Init(box.InitInput{Resolved: box.Resolved{
		RootPath:  boxRoot,
		Profile:   box.ProfileMain,
		OwnerNode: "main",
		NodeRole:  "main",
	}}); err != nil {
		t.Fatalf("initialize Box: %v", err)
	}
	target := filepath.Join(root, "extra-root")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	if _, err := backupcontracts.Create(backupcontracts.MutateInput{
		BoxRoot:          boxRoot,
		DirectoryRelPath: backupcontracts.DefaultDirectoryRelPath,
		Contract: backupcontracts.Contract{
			Key:         "extra-root",
			DisplayName: "Extra Root",
			OwnerNode:   "main",
			Target:      backupcontracts.TargetSpec{Scope: backupcontracts.TargetScopeOwnerNodeAbsolute, Path: target},
		},
		Actor: "test",
	}); err != nil {
		t.Fatalf("create backup contract: %v", err)
	}

	stdout, stderr, err := executeRootCommand(
		"--json",
		"backup",
		"coverage",
		"--data-dir", dataDir,
		"--main-box-path", boxRoot,
		"--backup-root", filepath.Join(dataDir, "backups", "main"),
	)
	if err != nil {
		t.Fatalf("backup coverage returned error: %v stderr=%s", err, stderr)
	}
	var envelope response.Envelope[backupcoverage.Report]
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("coverage output was not JSON: %v output=%q", err, stdout)
	}
	entry := findBackupCoverageEntry(envelope.Data.Entries, "backup-contract-extra-root")
	if entry.Status != backupcoverage.StatusCovered || entry.Category != "standalone_backup_contract" || entry.Path != target {
		t.Fatalf("standalone contract entry = %#v", entry)
	}
}

func TestBackupProductionMainHealthRequiresProductionAndMain(t *testing.T) {
	if !isProductionMainHealth("production", "main", "main") {
		t.Fatal("production main should be accepted")
	}
	for _, tc := range []struct {
		env  string
		node string
		role string
	}{
		{"dev", "main", "main"},
		{"production", "workspace", "workspace"},
		{"staging", "main", "main"},
	} {
		if isProductionMainHealth(tc.env, tc.node, tc.role) {
			t.Fatalf("accepted non-production-main health: %#v", tc)
		}
	}
}

func findBackupCoverageEntry(entries []backupcoverage.Entry, key string) backupcoverage.Entry {
	for _, entry := range entries {
		if entry.Key == key {
			return entry
		}
	}
	return backupcoverage.Entry{}
}
