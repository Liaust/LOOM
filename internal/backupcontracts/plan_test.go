package backupcontracts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentwatchedroots "loom.local/loom/internal/nodeagent/watchedroots"
)

func TestPlanWatchedRootsCompilesActiveContracts(t *testing.T) {
	root := t.TempDir()
	if _, err := Create(MutateInput{
		BoxRoot: root,
		Contract: validContractWith(func(contract *Contract) {
			contract.Key = "archive"
			contract.DisplayName = "Archive"
			contract.Target = TargetSpec{Scope: TargetScopeBoxRelative, Path: "Archive"}
		}),
	}); err != nil {
		t.Fatalf("Create active contract: %v", err)
	}
	if _, err := Create(MutateInput{
		BoxRoot: root,
		Contract: validContractWith(func(contract *Contract) {
			contract.Key = "disabled-root"
			contract.Status = StatusDisabled
		}),
	}); err != nil {
		t.Fatalf("Create disabled contract: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".loom", "contracts", "backup", "invalid.yaml"), []byte("schema_version: nope\n"), 0o600); err != nil {
		t.Fatalf("write invalid contract: %v", err)
	}

	result, err := PlanWatchedRoots(WatchPlanOptions{BoxRoot: root, BoxID: "box_test", OwnerNode: "macbook"})
	if err != nil {
		t.Fatalf("PlanWatchedRoots returned error: %v", err)
	}
	if len(result.Items) != 1 || len(result.Skipped) != 1 || len(result.Problems) != 1 {
		t.Fatalf("unexpected plan result: %#v", result)
	}
	item := result.Items[0]
	if item.Key != "backup_archive" || item.BackendRootKey != "loom_box_backup__archive" || item.SafeRootKey != "loom_box" {
		t.Fatalf("unexpected watched-root keys: %#v", item)
	}
	if item.RootRelativePath != "Archive" || item.SyncMode != agentwatchedroots.SyncModeNone || item.BackupMode != agentwatchedroots.BackupModeIncrementalRaw || item.IndexMode != agentwatchedroots.IndexModeNone {
		t.Fatalf("unexpected watched-root policy: %#v", item)
	}
	var config agentwatchedroots.RootConfig
	if err := json.Unmarshal(item.ConfigJSON, &config); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if config.SafeRootKey != "loom_box" || config.RootRelativePath != "Archive" || config.BackupPolicy.MaxFileBytes != DefaultMaxFileBytes {
		t.Fatalf("unexpected config: %#v", config)
	}
	if config.IgnorePolicy.Profile != "managed" || !config.IgnorePolicy.DiscoverUserRules || config.IgnorePolicy.PolicyRootRelativePath != "." || config.Scan.HiddenPolicy != agentwatchedroots.HiddenPolicyPolicyControlled {
		t.Fatalf("new backup contract did not compile managed ignore policy: %#v", config)
	}
	if strings.Contains(string(item.ConfigJSON), "credential") || strings.Contains(string(item.ConfigJSON), "file_content") || strings.Contains(string(item.ConfigJSON), "secret_value") {
		t.Fatalf("portable desired config carried credential or file-content fields: %s", item.ConfigJSON)
	}
	if item.Metadata["source"] != MetadataSource || item.Metadata["contract_key"] != "archive" {
		t.Fatalf("unexpected metadata: %#v", item.Metadata)
	}
}

func TestWatchedRootItemForAbsoluteTargetUsesPrivateSafeRoot(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "External")
	contract := validContractWith(func(contract *Contract) {
		contract.Key = "field-data"
		contract.Target = TargetSpec{Scope: TargetScopeOwnerNodeAbsolute, Path: target}
	})
	item, err := WatchedRootItem(contract, filepath.Join(root, ".loom", "contracts", "backup", "field-data.yaml"), WatchPlanOptions{BoxRoot: root, BoxID: "box_test", OwnerNode: "macbook"})
	if err != nil {
		t.Fatalf("WatchedRootItem returned error: %v", err)
	}
	if item.SafeRootKey != "backup_field-data" || item.RootRelativePath != "." {
		t.Fatalf("absolute target should use dedicated safe root: %#v", item)
	}
	if item.Metadata["safe_root_absolute_path"] != target || item.Metadata["safe_root_private_backup_only"] != true {
		t.Fatalf("absolute target metadata missing: %#v", item.Metadata)
	}
	if !IsBackupRootItem(item) || !IsBackupAreaKey(item.Key) {
		t.Fatalf("backup root helpers did not recognize item: %#v", item)
	}
}

func TestWatchedRootItemPreservesExplicitContractOwner(t *testing.T) {
	contract := validContractWith(func(contract *Contract) {
		contract.Key = "main-archive"
		contract.OwnerNode = "main"
		contract.Target = TargetSpec{Scope: TargetScopeOwnerNodeAbsolute, Path: "/srv/archive"}
	})
	item, err := WatchedRootItem(contract, "/main-box/.loom/contracts/backup/main-archive.yaml", WatchPlanOptions{
		BoxRoot:   "/main-box",
		BoxID:     "box_main",
		OwnerNode: "macbook",
	})
	if err != nil {
		t.Fatal(err)
	}
	if item.OwnerNode != "main" || item.Metadata["owner_node"] != "main" {
		t.Fatalf("explicit contract owner was overwritten: %#v", item)
	}
}

func TestWatchedRootItemDefaultsMissingOwnerToBoxOwner(t *testing.T) {
	contract := validContractWith(func(contract *Contract) {
		contract.Key = "defaulted-owner"
		contract.OwnerNode = ""
	})
	item, err := WatchedRootItem(contract, "", WatchPlanOptions{BoxRoot: "/box", BoxID: "box_main", OwnerNode: "macbook"})
	if err != nil {
		t.Fatal(err)
	}
	if item.OwnerNode != "macbook" {
		t.Fatalf("owner = %q, want macbook", item.OwnerNode)
	}
}
