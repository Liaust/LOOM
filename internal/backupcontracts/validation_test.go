package backupcontracts

import (
	"strings"
	"testing"
)

func TestNormalizeAndRenderDefaults(t *testing.T) {
	contract := Contract{
		Key:         "documents",
		DisplayName: "Documents",
		OwnerNode:   "macbook",
		Target: TargetSpec{
			Path: "Documents",
		},
	}
	payload, err := Render(contract)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}
	content := string(payload)
	for _, want := range []string{
		"schema_version: loom.backup.contract.v1",
		"status: active",
		"scope: box_relative",
		"mode: incremental_raw",
		"max_file_bytes: 52428800",
		"profile: managed",
		"discover_user_rules: true",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("rendered contract missing %q:\n%s", want, content)
		}
	}
	parsed, err := Parse(payload)
	if err != nil {
		t.Fatalf("Parse rendered contract: %v", err)
	}
	if parsed.Target.Scope != TargetScopeBoxRelative || parsed.Backup.Mode != BackupModeIncrementalRaw {
		t.Fatalf("defaults not preserved: %#v", parsed)
	}
	if len(parsed.Exclude) != 0 || parsed.Ignore == nil || parsed.Ignore.Profile != "managed" || !parsed.Ignore.DiscoverUserRules {
		t.Fatalf("new contract should store profile intent without expanded defaults: %#v", parsed)
	}
	if strings.Contains(content, "node_modules") || strings.Contains(content, ".venv") || strings.Contains(content, ".git") {
		t.Fatalf("v1 YAML expanded managed policy instead of retaining stable intent:\n%s", content)
	}
}

func TestParseAcceptsLegacySchemaCompatibility(t *testing.T) {
	contract := validContractWith(func(contract *Contract) {
		contract.SchemaVersion = LegacySchemaVersion
		contract.Ignore = nil
		contract.Exclude = nil
	})
	payload, err := Render(contract)
	if err != nil {
		t.Fatalf("Render legacy contract: %v", err)
	}
	parsed, err := Parse(payload)
	if err != nil {
		t.Fatalf("Parse legacy contract: %v", err)
	}
	if parsed.SchemaVersion != LegacySchemaVersion || len(parsed.Exclude) != len(DefaultExcludePatterns()) {
		t.Fatalf("legacy compatibility changed: %#v", parsed)
	}
}

func TestValidateRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name     string
		contract Contract
		want     string
	}{
		{
			name:     "key",
			contract: validContractWith(func(contract *Contract) { contract.Key = "Bad Key" }),
			want:     "key",
		},
		{
			name: "relative traversal",
			contract: validContractWith(func(contract *Contract) {
				contract.Target = TargetSpec{Scope: TargetScopeBoxRelative, Path: "../Secrets"}
			}),
			want: "must not contain",
		},
		{
			name: "absolute scope requires absolute path",
			contract: validContractWith(func(contract *Contract) {
				contract.Target = TargetSpec{Scope: TargetScopeOwnerNodeAbsolute, Path: "Documents"}
			}),
			want: "must be absolute",
		},
		{
			name: "unknown mode",
			contract: validContractWith(func(contract *Contract) {
				contract.Backup.Mode = "mirror"
			}),
			want: "backup.mode",
		},
		{
			name: "malformed include",
			contract: validContractWith(func(contract *Contract) {
				contract.Include = []string{"bad\x00pattern"}
			}),
			want: "contains NUL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Render(tt.contract)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not contain %q", err, tt.want)
			}
		})
	}
}

func validContractWith(edit func(*Contract)) Contract {
	contract := Contract{
		SchemaVersion: SchemaVersion,
		Key:           "documents",
		DisplayName:   "Documents",
		OwnerNode:     "macbook",
		Status:        StatusActive,
		Target: TargetSpec{
			Scope: TargetScopeBoxRelative,
			Path:  "Documents",
		},
		Include: []string{"**/*"},
		Exclude: []string{".loom/**"},
		Backup: BackupPolicy{
			Mode:          BackupModeIncrementalRaw,
			MaxFileBytes:  DefaultMaxFileBytes,
			MaxBatchBytes: DefaultMaxBatchBytes,
		},
	}
	if edit != nil {
		edit(&contract)
	}
	return contract
}
