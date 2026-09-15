package projectcontracts

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"loom.local/loom/internal/filesystemconnector"
	"loom.local/loom/internal/nodeagent/watchedroots"
)

// Integrator-owned regression: backup admission must reach the real classifier.
func TestProjectWatchPolicyFidelityDeclaredBackupAdmits64MiB(t *testing.T) {
	scaffold, err := ScaffoldProject(ScaffoldOptions{Name: "Policy Fidelity", Slug: "policy-fidelity", OwnerNode: "main", Preset: PresetMinimal, Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(scaffold.ProjectRoot, ".loom/contracts/backup.yaml")
	policy, err := os.ReadFile(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	policy = []byte(strings.ReplaceAll(string(policy), ": 1048576\n", ": 268435456\n"))
	if err := os.WriteFile(policyPath, policy, 0o600); err != nil {
		t.Fatal(err)
	}
	analysis := Analyze(scaffold.ProjectRoot)
	if !analysis.Report.OK {
		t.Fatalf("fixture must validate: %#v", analysis.Report.Diagnostics)
	}
	item := findWatchedRoot(t, analysis.Report.WatchedRoots, "policy_fidelity__notes")
	var config watchedroots.RootConfig
	if err := json.Unmarshal(item.ConfigJSON, &config); err != nil {
		t.Fatal(err)
	}
	if config.BackupPolicy.MaxFileBytes != 256*1024*1024 {
		t.Fatalf("declared backup limit lost: %d", config.BackupPolicy.MaxFileBytes)
	}
	safe := filesystemconnector.DefaultSafeRoot("project", scaffold.ProjectRoot)
	safe.MaxFileBytes = 512 * 1024 * 1024
	root, err := watchedroots.ValidateRootConfig(config, filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{safe}})
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(scaffold.ProjectRoot, "notes", "attachment.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(64 * 1024 * 1024); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	info, err := file.Stat()
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("fixture metadata: %v, close: %v", err, closeErr)
	}
	classification := watchedroots.ClassifyPath(watchedroots.PathObservation{RelativePath: "attachment.bin", Exists: true, Kind: watchedroots.PathKindFile, Safe: true, SizeBytes: info.Size()}, root)
	if !classification.Included || classification.Policies.Backup != watchedroots.BackupModeIncrementalRaw {
		t.Fatalf("256 MiB backup declaration rejected a 64 MiB file: scan_limit=%d result=%#v", root.Config.Scan.MaxHashFileBytes, classification)
	}
}

func TestProjectWatchPolicyFidelityEnabledLimits(t *testing.T) {
	const mib = int64(1024 * 1024)
	for _, tc := range []struct {
		name, backup, sync                string
		backupBytes, syncBytes, scanBytes int64
	}{
		{"backup_only", "incremental_raw", "none", 256 * mib, mib, 256 * mib},
		{"sync_only", "none", "selected_files", mib, 64 * mib, 64 * mib},
		{"both_backup_larger", "incremental_raw", "selected_files", 256 * mib, 64 * mib, 256 * mib},
		{"both_sync_larger", "incremental_raw", "selected_files", 64 * mib, 256 * mib, 256 * mib},
		{"disabled_modes", "none", "none", 256 * mib, 256 * mib, 50 * mib},
		{"disabled_backup_larger", "none", "selected_files", 256 * mib, 64 * mib, 64 * mib},
		{"disabled_sync_larger", "incremental_raw", "none", 64 * mib, 256 * mib, 64 * mib},
		{"below_scan_default", "incremental_raw", "selected_files", mib, 2 * mib, 50 * mib},
		{"at_scan_default", "incremental_raw", "selected_files", 50 * mib, 50 * mib, 50 * mib},
		{"one_byte_above_default", "incremental_raw", "selected_files", 50*mib + 1, mib, 50*mib + 1},
		{"normalized_sync_default", "none", "selected_files", mib, 0, 50 * mib},
		{"metadata_backup", "metadata_only", "none", 64 * mib, mib, 64 * mib},
		{"private_backup", "private_raw", "none", 64 * mib, mib, 64 * mib},
		{"snapshot_backup", "raw_snapshot", "none", 64 * mib, mib, 64 * mib},
		{"int64_limit", "incremental_raw", "selected_files", math.MaxInt64, math.MaxInt64 - 1, math.MaxInt64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project, item, config := fidelityPolicyFixture(t, tc.backup, tc.sync, tc.backupBytes, tc.syncBytes)
			if config.Scan.MaxHashFileBytes != tc.scanBytes {
				t.Fatalf("scan allowance = %d, want %d", config.Scan.MaxHashFileBytes, tc.scanBytes)
			}
			wantSync := tc.syncBytes
			if wantSync == 0 {
				wantSync = watchedroots.NormalizeRootConfig(watchedroots.RootConfig{}).SyncPolicy.MaxFileBytes
			}
			if config.BackupPolicy.Mode != tc.backup || config.SyncPolicy.Mode != tc.sync || config.BackupPolicy.MaxFileBytes != tc.backupBytes || config.SyncPolicy.MaxFileBytes != wantSync || config.BackupPolicy.MaxBatchBytes != tc.backupBytes {
				t.Fatalf("operation policy changed: backup=%#v sync=%#v", config.BackupPolicy, config.SyncPolicy)
			}
			defaults := watchedroots.NormalizeRootConfig(watchedroots.RootConfig{Scan: watchedroots.ScanConfig{HiddenPolicy: watchedroots.HiddenPolicyPolicyControlled}})
			defaults.Scan.MaxHashFileBytes = tc.scanBytes
			if !reflect.DeepEqual(config.Scan, defaults.Scan) || !reflect.DeepEqual(config.Watch, defaults.Watch) || !reflect.DeepEqual(config.FidelityPolicy, defaults.FidelityPolicy) {
				t.Fatalf("scan cadence, watching or fidelity changed: %#v", config)
			}
			replay := Analyze(project)
			if !replay.Report.OK {
				t.Fatal(replay.Report.Diagnostics)
			}
			again := findWatchedRoot(t, replay.Report.WatchedRoots, item.BackendRootKey)
			if string(item.ConfigJSON) != string(again.ConfigJSON) || item.ConfigHash != again.ConfigHash || item.ConfigHash != watchedroots.ConfigHash(config) {
				t.Fatal("replay or serialized config changed the compiled identity")
			}
			// Classifier observations carry synthetic sizes; no large payload is read.
			root := fidelityAdmitRoot(t, project, config, tc.scanBytes)
			obs := watchedroots.PathObservation{RelativePath: "attachment.bin", Exists: true, Kind: watchedroots.PathKindFile, Safe: true, SizeBytes: tc.scanBytes}
			if result := watchedroots.ClassifyPath(obs, root); !result.Included {
				t.Fatalf("file exactly at scan allowance refused: %#v", result)
			}
			if tc.scanBytes < math.MaxInt64 {
				obs.SizeBytes++
				if result := watchedroots.ClassifyPath(obs, root); result.Included || result.ReasonCode != watchedroots.ReasonSkippedTooLarge {
					t.Fatalf("file above scan allowance admitted: %#v", result)
				}
			}
		})
	}
}

func TestProjectWatchPolicyFidelityDisabledDeclarationsAndUnrelatedRoots(t *testing.T) {
	project, _, before := fidelityPolicyFixture(t, "incremental_raw", "selected_files", 1024*1024, 1024*1024)
	// A separate Notes root must keep its complete config when another root grows.
	projectPath := filepath.Join(project, ".loom/project.yaml")
	raw, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, project, ".loom/project.yaml", strings.Replace(string(raw), "facets:\n", "facets:\n  notes: true\n", 1), 0o600)
	writeFile(t, project, "notes/loom.notes.yaml", validNotesContract(), 0o600)
	baseline := Analyze(project)
	if !baseline.Report.OK {
		t.Fatal(baseline.Report.Diagnostics)
	}
	notes := findWatchedRoot(t, baseline.Report.WatchedRoots, "watch_smoke__notes")
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("enabled_%t", enabled), func(t *testing.T) {
			fidelityWritePolicies(t, project, "incremental_raw", "selected_files", 256*1024*1024, 64*1024*1024, enabled)
			analysis := Analyze(project)
			if !analysis.Report.OK {
				t.Fatal(analysis.Report.Diagnostics)
			}
			again := findWatchedRoot(t, analysis.Report.WatchedRoots, notes.BackendRootKey)
			if string(again.ConfigJSON) != string(notes.ConfigJSON) || again.ConfigHash != notes.ConfigHash {
				t.Fatal("unrelated Notes root configuration changed")
			}
			if !enabled {
				if len(analysis.Report.WatchedRoots) != 1 {
					t.Fatalf("disabled declarations emitted roots: %#v", analysis.Report.WatchedRoots)
				}
				return
			}
			var after watchedroots.RootConfig
			if err := json.Unmarshal(findWatchedRoot(t, analysis.Report.WatchedRoots, "watch_smoke__payload").ConfigJSON, &after); err != nil {
				t.Fatal(err)
			}
			before.Scan.MaxHashFileBytes = 256 * 1024 * 1024
			before.BackupPolicy.MaxFileBytes = 256 * 1024 * 1024
			before.BackupPolicy.MaxBatchBytes = 256 * 1024 * 1024
			before.SyncPolicy.MaxFileBytes = 64 * 1024 * 1024
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("raising limits changed other compiled settings: before=%#v after=%#v", before, after)
			}
		})
	}
}

func TestProjectWatchPolicyFidelityOperationBoundaries(t *testing.T) {
	// Different operation caps below scan admission prove that admission alone
	// does not authorize an oversized backup or sync output.
	const syncBytes, backupBytes = int64(1024 * 1024), int64(64 * 1024 * 1024)
	project, _, config := fidelityPolicyFixture(t, "incremental_raw", "selected_files", backupBytes, syncBytes)
	root := fidelityAdmitRoot(t, project, config, 256*1024*1024)
	for _, size := range []int64{syncBytes, syncBytes + 1, backupBytes, backupBytes + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			result := watchedroots.ClassifyPath(watchedroots.PathObservation{RelativePath: "attachment.bin", Exists: true, Kind: watchedroots.PathKindFile, Safe: true, SizeBytes: size}, root)
			if result.Included != (size <= backupBytes) {
				t.Fatalf("wrong scan admission: %#v", result)
			}
			// Model an already cataloged observation to exercise output limits too,
			// including a state now over the scan cap; no output is executed.
			store := watchedroots.NewStore(t.TempDir())
			if err := store.SavePathState(watchedroots.PathState{RootKey: config.RootKey, RelativePath: "attachment.bin", Status: watchedroots.PathStatusIncluded, Kind: watchedroots.PathKindFile, SizeBytes: size, ContentHashURI: "sha256:" + strings.Repeat("a", 64), HashStatus: watchedroots.HashStatusComputed}); err != nil {
				t.Fatal(err)
			}
			plan, err := watchedroots.PlanOutputs(store, root, "test-policy-fidelity")
			if err != nil {
				t.Fatal(err)
			}
			wantSync, wantBackup := watchedroots.OutputActionSyncObject, watchedroots.OutputActionBackupFile
			if size > syncBytes {
				wantSync = watchedroots.OutputActionSkipped
			}
			if size > backupBytes {
				wantBackup = watchedroots.OutputActionBackupSkipped
			}
			if len(plan.Actions) != 2 || plan.Actions[0].ActionKind != wantSync || plan.Actions[1].ActionKind != wantBackup || plan.Actions[1].BackupMaxFileBytes != backupBytes || plan.Actions[1].BackupMaxBatchBytes != backupBytes {
				t.Fatalf("operation cap was not preserved: %#v", plan)
			}
			if size > syncBytes && plan.Actions[0].ReasonCode != watchedroots.ReasonSkippedTooLarge {
				t.Fatalf("unexpected sync refusal: %#v", plan.Actions[0])
			}
			if size > backupBytes && plan.Actions[1].ReasonCode != watchedroots.FindingBackupFileTooLarge {
				t.Fatalf("unexpected backup refusal: %#v", plan.Actions[1])
			}
		})
	}
}

func TestProjectWatchPolicyFidelitySafetyRefusals(t *testing.T) {
	project, _, config := fidelityPolicyFixture(t, "incremental_raw", "selected_files", 256*1024*1024, 64*1024*1024)
	root := fidelityAdmitRoot(t, project, config, 512*1024*1024)
	for _, tc := range []struct {
		name, path, reason string
		safe, symlink      bool
	}{
		{"excluded", "blocked/attachment.bin", watchedroots.ReasonExcludedFilePolicy, true, false},
		{"symlink", "link.bin", watchedroots.ReasonSkippedSymlink, true, true},
		{"unsafe", "../outside.bin", watchedroots.ReasonSkippedPathEscape, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := watchedroots.ClassifyPath(watchedroots.PathObservation{RelativePath: tc.path, Exists: true, Kind: watchedroots.PathKindFile, Safe: tc.safe, Symlink: tc.symlink, SizeBytes: 64 * 1024 * 1024}, root)
			if result.Included || result.ReasonCode != tc.reason {
				t.Fatalf("unsafe/excluded observation admitted: %#v", result)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		change func(*watchedroots.RootConfig, *filesystemconnector.SafeRoot)
		want   string
	}{
		{"backup_exceeds_node", func(_ *watchedroots.RootConfig, s *filesystemconnector.SafeRoot) { s.MaxFileBytes = 128 * 1024 * 1024 }, "backup policy max_file_bytes exceeds safe root"},
		{"sync_exceeds_node", func(c *watchedroots.RootConfig, s *filesystemconnector.SafeRoot) {
			c.BackupPolicy.MaxFileBytes = 1024 * 1024
			s.MaxFileBytes = 50 * 1024 * 1024
		}, "sync policy max_file_bytes exceeds safe root"},
		{"private_root", func(_ *watchedroots.RootConfig, s *filesystemconnector.SafeRoot) { s.PrivateBackupOnly = true }, "private-backup-only safe roots cannot enable"},
		{"unknown_root", func(c *watchedroots.RootConfig, _ *filesystemconnector.SafeRoot) { c.SafeRootKey = "unknown" }, "is not configured"},
		{"escaping_root", func(c *watchedroots.RootConfig, _ *filesystemconnector.SafeRoot) { c.RootRelativePath = "../outside" }, "invalid watched-root path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := config
			safe := filesystemconnector.DefaultSafeRoot("project", project)
			safe.MaxFileBytes = 512 * 1024 * 1024
			tc.change(&candidate, &safe)
			if _, err := watchedroots.ValidateRootConfig(candidate, filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{safe}}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("node admission = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestProjectWatchPolicyFidelityInvalidDeclarations(t *testing.T) {
	for _, tc := range []struct {
		name, backup, sync string
		backupBytes        int64
		want               string
	}{
		{"missing_backup_limit", "incremental_raw", "none", 0, "max_file_bytes must be explicit"},
		{"negative_backup_limit", "incremental_raw", "none", -1, "max_file_bytes must be explicit"},
		{"unknown_backup_mode", "unknown", "none", 256 * 1024 * 1024, "unsupported backup policy mode"},
		{"unknown_sync_mode", "none", "unknown", 256 * 1024 * 1024, "unsupported sync policy mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := watchPolicyProjectFixture(t, "\nfacets:\n  sync_policy: true\n  backup_policy: true\n")
			mkdir(t, project, "payload")
			fidelityWritePolicies(t, project, tc.backup, tc.sync, tc.backupBytes, 64*1024*1024, true)
			analysis := Analyze(project)
			if analysis.Report.OK || len(analysis.Report.WatchedRoots) != 0 {
				t.Fatalf("invalid declaration emitted a config: %#v", analysis.Report)
			}
			for _, diagnostic := range analysis.Report.Diagnostics {
				if diagnostic.Code == "watched_root.config_invalid" && strings.Contains(diagnostic.Message, tc.want) {
					return
				}
			}
			t.Fatalf("missing expected diagnostic %q: %#v", tc.want, analysis.Report.Diagnostics)
		})
	}
}

func fidelityPolicyFixture(t *testing.T, backupMode, syncMode string, backupBytes, syncBytes int64) (string, ProjectWatchedRootItem, watchedroots.RootConfig) {
	t.Helper()
	project := watchPolicyProjectFixture(t, "\nfacets:\n  sync_policy: true\n  backup_policy: true\n")
	mkdir(t, project, "payload")
	fidelityWritePolicies(t, project, backupMode, syncMode, backupBytes, syncBytes, true)
	analysis := Analyze(project)
	if !analysis.Report.OK {
		t.Fatal(analysis.Report.Diagnostics)
	}
	item := findWatchedRoot(t, analysis.Report.WatchedRoots, "watch_smoke__payload")
	var config watchedroots.RootConfig
	if err := json.Unmarshal(item.ConfigJSON, &config); err != nil {
		t.Fatal(err)
	}
	return project, item, config
}

func fidelityWritePolicies(t *testing.T, project, backupMode, syncMode string, backupBytes, syncBytes int64, enabled bool) {
	t.Helper()
	for _, policy := range []struct {
		name, mode string
		maxBytes   int64
		extra      string
	}{
		{"backup", backupMode, backupBytes, fmt.Sprintf("      max_batch_bytes: %d\n", backupBytes)},
		{"sync", syncMode, syncBytes, "      index: none\n"},
	} {
		writeFile(t, project, ".loom/contracts/"+policy.name+".yaml", fmt.Sprintf(`kind: loom.project_%s_policy
schema_version: %s.policy.v0.3
%s:
  enabled: %t
  roots:
    - key: payload
      path: payload
      mode: %s
      max_file_bytes: %d
      exclude: ["blocked/**"]
%s`, policy.name, policy.name, policy.name, enabled, policy.mode, policy.maxBytes, policy.extra), 0o600)
	}
}

func fidelityAdmitRoot(t *testing.T, project string, config watchedroots.RootConfig, maxBytes int64) watchedroots.ValidatedRoot {
	t.Helper()
	safe := filesystemconnector.DefaultSafeRoot("project", project)
	// Disabled modes can retain larger caps; existing node validation checks
	// backup caps independently, so give it enough room without changing scan.
	safe.MaxFileBytes = max(maxBytes, config.BackupPolicy.MaxFileBytes, config.SyncPolicy.MaxFileBytes)
	root, err := watchedroots.ValidateRootConfig(config, filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{safe}})
	if err != nil {
		t.Fatal(err)
	}
	return root
}
