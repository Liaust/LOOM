package backupcontracts

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMigrateIgnorePolicyGoldenLegacyAndIdempotence(t *testing.T) {
	root := t.TempDir()
	contract := legacyMigrationContract(t, root, "documents", append(DefaultExcludePatterns(), "private-cache/**"))
	contract.Metadata = Metadata{CreatedAt: "2026-01-02T03:04:05Z", UpdatedAt: "2026-02-03T04:05:06Z", CreatedBy: "the operator"}
	pathValue := writeMigrationContract(t, root, contract)

	dryRun, err := MigrateIgnorePolicy(MigrateIgnorePolicyInput{BoxRoot: root, DryRun: true})
	if err != nil {
		t.Fatalf("dry-run migration: %v", err)
	}
	if dryRun.ChangedCount != 1 || dryRun.Items[0].Status != MigrationStatusMigrated || len(dryRun.Items[0].RemovedExcludes) != len(DefaultExcludePatterns()) {
		t.Fatalf("unexpected dry-run: %#v", dryRun)
	}
	if got, err := os.ReadFile(pathValue); err != nil || string(got) != dryRun.Items[0].BeforeYAML {
		t.Fatalf("dry run changed source: err=%v got=%s", err, got)
	}

	applied, err := MigrateIgnorePolicy(MigrateIgnorePolicyInput{BoxRoot: root, Apply: true, Yes: true})
	if err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	if !applied.Applied || applied.ChangedCount != 1 {
		t.Fatalf("unexpected apply: %#v", applied)
	}
	migrated, _, err := LoadFile(pathValue)
	if err != nil {
		t.Fatalf("load migrated contract: %v", err)
	}
	if migrated.SchemaVersion != SchemaVersion || migrated.Ignore == nil || migrated.Ignore.Profile != "managed" || !migrated.Ignore.DiscoverUserRules {
		t.Fatalf("profile migration missing: %#v", migrated)
	}
	if !reflect.DeepEqual(migrated.Exclude, []string{"private-cache/**"}) {
		t.Fatalf("custom excludes were not preserved: %#v", migrated.Exclude)
	}
	if migrated.Metadata != contract.Metadata {
		t.Fatalf("metadata changed: got %#v want %#v", migrated.Metadata, contract.Metadata)
	}

	repeated, err := MigrateIgnorePolicy(MigrateIgnorePolicyInput{BoxRoot: root, Apply: true, Yes: true})
	if err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	if repeated.ChangedCount != 0 || repeated.Items[0].Status != MigrationStatusUnchanged {
		t.Fatalf("migration is not idempotent: %#v", repeated)
	}
}

func TestMigrateIgnorePolicyLeavesAmbiguousOriginUnchanged(t *testing.T) {
	root := t.TempDir()
	defaults := DefaultExcludePatterns()
	pathValue := writeMigrationContract(t, root, legacyMigrationContract(t, root, "ambiguous", []string{defaults[0], "custom/**"}))
	before, _ := os.ReadFile(pathValue)
	result, err := MigrateIgnorePolicy(MigrateIgnorePolicyInput{BoxRoot: root, Apply: true, Yes: true})
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if result.ChangedCount != 0 || result.AttentionCount != 1 || result.Items[0].Status != MigrationStatusAttention || !strings.Contains(result.Items[0].Reason, "ambiguous") {
		t.Fatalf("unexpected ambiguity result: %#v", result)
	}
	after, _ := os.ReadFile(pathValue)
	if string(after) != string(before) {
		t.Fatalf("ambiguous contract changed:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestMigrateIgnorePolicyDoesNotResolveOrCopyCredentialReferences(t *testing.T) {
	root := t.TempDir()
	writeMigrationContract(t, root, legacyMigrationContract(t, root, "documents", DefaultExcludePatterns()))
	credentialsPath := filepath.Join(root, ".loom", "contracts", "credentials.yaml")
	if err := os.MkdirAll(filepath.Dir(credentialsPath), 0o750); err != nil {
		t.Fatal(err)
	}
	credentials := []byte("schema_version: loom.credentials.v1\nreferences:\n  sync: pass://LOOM/Sync/password\n")
	if err := os.WriteFile(credentialsPath, credentials, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateIgnorePolicy(MigrateIgnorePolicyInput{BoxRoot: root, Apply: true, Yes: true}); err != nil {
		t.Fatalf("migration: %v", err)
	}
	got, err := os.ReadFile(credentialsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(credentials) {
		t.Fatalf("credential reference data changed: %s", got)
	}
}

func legacyMigrationContract(t *testing.T, root, key string, excludes []string) Contract {
	t.Helper()
	return validContractWith(func(contract *Contract) {
		contract.SchemaVersion = LegacySchemaVersion
		contract.Key = key
		contract.Ignore = nil
		contract.Target = TargetSpec{Scope: TargetScopeOwnerNodeAbsolute, Path: filepath.Join(root, key)}
		contract.Exclude = append([]string{}, excludes...)
	})
}

func writeMigrationContract(t *testing.T, root string, contract Contract) string {
	t.Helper()
	pathValue, err := ContractFilePath(root, "", contract.Key)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := Render(contract)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(pathValue), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := writeContractFile(pathValue, payload); err != nil {
		t.Fatal(err)
	}
	return pathValue
}
