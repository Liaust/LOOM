package backupcontracts

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreCreateListDisableAndDelete(t *testing.T) {
	root := t.TempDir()
	now := func() time.Time { return time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC) }
	input := MutateInput{
		BoxRoot: root,
		Contract: validContractWith(func(contract *Contract) {
			contract.Key = "field-data"
			contract.Target = TargetSpec{Scope: TargetScopeOwnerNodeAbsolute, Path: filepath.Join(root, "Field Data")}
		}),
		Actor: "loom portal",
		Now:   now,
	}
	result, err := Create(input)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if !result.Created || result.Updated || result.Action != "create" {
		t.Fatalf("unexpected create result: %#v", result)
	}
	if result.Path != filepath.Join(root, ".loom", "contracts", "backup", "field-data.yaml") {
		t.Fatalf("unexpected path: %s", result.Path)
	}
	info, err := os.Stat(result.Path)
	if err != nil {
		t.Fatalf("stat created contract: %v", err)
	}
	if info.Mode().Perm() != contractFileMode {
		t.Fatalf("contract mode = %v", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(result.Path))
	if err != nil {
		t.Fatalf("stat created contract directory: %v", err)
	}
	if dirInfo.Mode().Perm() != contractDirectoryMode {
		t.Fatalf("contract directory mode = %v", dirInfo.Mode().Perm())
	}

	duplicate, err := Create(input)
	if err == nil {
		t.Fatalf("expected duplicate create to fail, got %#v", duplicate)
	}

	input.Replace = true
	input.Contract.DisplayName = "Field Data Backup"
	updated, err := Create(input)
	if err != nil {
		t.Fatalf("replace Create returned error: %v", err)
	}
	if !updated.Updated || updated.Created || updated.Action != "update" {
		t.Fatalf("unexpected update result: %#v", updated)
	}
	if info, err := os.Stat(result.Path); err != nil {
		t.Fatalf("stat updated contract: %v", err)
	} else if info.Mode().Perm() != contractFileMode {
		t.Fatalf("updated contract mode = %v", info.Mode().Perm())
	}

	if err := os.WriteFile(filepath.Join(root, ".loom", "contracts", "backup", "invalid.yaml"), []byte("schema_version: wrong\n"), 0o600); err != nil {
		t.Fatalf("write invalid contract: %v", err)
	}
	listed, err := List(root, "")
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("expected valid and invalid entries, got %#v", listed)
	}
	if listed[0].Key != "field-data" || listed[0].Error != "" {
		t.Fatalf("valid entry unexpected: %#v", listed[0])
	}
	if listed[1].Key != "invalid" || listed[1].Error == "" {
		t.Fatalf("invalid entry unexpected: %#v", listed[1])
	}

	disabled, err := Disable(DisableInput{
		BoxRoot: root,
		Key:     "field-data",
		Actor:   "loom portal",
		Now:     func() time.Time { return time.Date(2026, 7, 7, 11, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("Disable returned error: %v", err)
	}
	if disabled.Contract.Status != StatusDisabled || disabled.Contract.Metadata.DisabledAt != "2026-07-07T11:00:00Z" {
		t.Fatalf("unexpected disabled contract: %#v", disabled.Contract)
	}
	if info, err := os.Stat(result.Path); err != nil {
		t.Fatalf("stat disabled contract: %v", err)
	} else if info.Mode().Perm() != contractFileMode {
		t.Fatalf("disabled contract mode = %v", info.Mode().Perm())
	}

	enabled, err := Enable(EnableInput{
		BoxRoot: root,
		Key:     "field-data",
		Actor:   "loom portal",
		Now:     func() time.Time { return time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("Enable returned error: %v", err)
	}
	if enabled.Contract.Status != StatusActive || enabled.Contract.Metadata.DisabledAt != "" || enabled.Contract.Metadata.DisabledBy != "" {
		t.Fatalf("unexpected enabled contract: %#v", enabled.Contract)
	}
	if enabled.Contract.Metadata.CreatedAt != result.Contract.Metadata.CreatedAt || enabled.Contract.Metadata.CreatedBy != result.Contract.Metadata.CreatedBy {
		t.Fatalf("enable changed creation metadata: %#v", enabled.Contract.Metadata)
	}
	if _, err := os.Stat(filepath.Join(root, "Field Data")); !os.IsNotExist(err) {
		t.Fatalf("lifecycle rewrite must not create or delete source path, stat err=%v", err)
	}

	deleted, err := Delete(DeleteInput{BoxRoot: root, Key: "field-data"})
	if err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}
	if !deleted.Deleted {
		t.Fatalf("delete result should be marked deleted: %#v", deleted)
	}
	if _, err := os.Stat(result.Path); !os.IsNotExist(err) {
		t.Fatalf("expected deleted file to be gone, stat err=%v", err)
	}
}

func TestStoreDisableRepairsRestrictiveContractMode(t *testing.T) {
	root := t.TempDir()
	result, err := Create(MutateInput{
		BoxRoot: root,
		Contract: validContractWith(func(contract *Contract) {
			contract.Key = "field-data"
			contract.Target = TargetSpec{Scope: TargetScopeOwnerNodeAbsolute, Path: filepath.Join(root, "Field Data")}
		}),
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if err := os.Chmod(result.Path, 0o600); err != nil {
		t.Fatalf("make contract restrictive: %v", err)
	}
	if _, err := Disable(DisableInput{BoxRoot: root, Key: "field-data"}); err != nil {
		t.Fatalf("Disable returned error: %v", err)
	}
	info, err := os.Stat(result.Path)
	if err != nil {
		t.Fatalf("stat repaired contract: %v", err)
	}
	if info.Mode().Perm() != contractFileMode {
		t.Fatalf("repaired contract mode = %v", info.Mode().Perm())
	}
}

func TestStoreDryRunDoesNotWrite(t *testing.T) {
	root := t.TempDir()
	result, err := Create(MutateInput{
		BoxRoot:  root,
		DryRun:   true,
		Contract: validContractWith(nil),
	})
	if err != nil {
		t.Fatalf("dry-run Create returned error: %v", err)
	}
	if !result.DryRun || !result.Created || result.RenderedYAML == "" {
		t.Fatalf("unexpected dry-run result: %#v", result)
	}
	if _, err := os.Stat(result.Path); !os.IsNotExist(err) {
		t.Fatalf("dry-run should not write file, stat err=%v", err)
	}
}

func TestContractFilePathRejectsEscapes(t *testing.T) {
	if _, err := ContractFilePath(t.TempDir(), "../outside", "documents"); err == nil {
		t.Fatal("expected escaping directory to fail")
	}
	if _, err := ContractFilePath(t.TempDir(), "", "../documents"); err == nil {
		t.Fatal("expected escaping key to fail")
	}
}
