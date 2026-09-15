package watchedroots

import (
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/filesystemconnector"
)

func TestClassifyPathIncludesAndExcludesByPolicy(t *testing.T) {
	t.Parallel()
	root := testValidatedRoot(t, RootConfig{
		RootKey:     "notes",
		SafeRootKey: "slice09",
		Include:     []string{"**/*.md"},
		Exclude:     []string{"**/*.tmp"},
	})
	included := ClassifyPath(PathObservation{
		RelativePath: "Project.md",
		Exists:       true,
		Kind:         PathKindFile,
		Safe:         true,
		SizeBytes:    12,
	}, root)
	if !included.Included || included.ReasonCode != ReasonIncludedByPattern || included.MatchedInclude != "**/*.md" {
		t.Fatalf("unexpected included classification %#v", included)
	}
	excluded := ClassifyPath(PathObservation{
		RelativePath: "Project.tmp",
		Exists:       true,
		Kind:         PathKindFile,
		Safe:         true,
		SizeBytes:    12,
	}, root)
	if excluded.Included || excluded.ReasonCode != ReasonExcludedByPattern || excluded.MatchedExclude != "**/*.tmp" {
		t.Fatalf("unexpected excluded classification %#v", excluded)
	}
}

func TestClassifyPathHandlesHiddenSymlinkAndLargeFiles(t *testing.T) {
	t.Parallel()
	root := testValidatedRoot(t, RootConfig{
		RootKey:     "notes",
		SafeRootKey: "slice09",
		Include:     []string{"**/*"},
		Scan: ScanConfig{
			MaxHashFileBytes: 8,
		},
	})
	hidden := ClassifyPath(PathObservation{
		RelativePath: ".obsidian/workspace.json",
		Exists:       true,
		Kind:         PathKindFile,
		Hidden:       true,
		Safe:         true,
		SizeBytes:    1,
	}, root)
	if hidden.Included || hidden.ReasonCode != ReasonExcludedHidden {
		t.Fatalf("unexpected hidden classification %#v", hidden)
	}
	acceptance := ClassifyPath(PathObservation{
		RelativePath: ".loom-acceptance/slice-01/probe.md",
		Exists:       true,
		Kind:         PathKindFile,
		Hidden:       true,
		Safe:         true,
		SizeBytes:    1,
	}, root)
	if !acceptance.Included || acceptance.ReasonCode != ReasonIncludedByPattern {
		t.Fatalf("hidden acceptance path should remain included for smoke tests: %#v", acceptance)
	}
	symlink := ClassifyPath(PathObservation{
		RelativePath: "link.md",
		Exists:       true,
		Kind:         PathKindSymlink,
		Symlink:      true,
		Safe:         true,
	}, root)
	if symlink.Included || symlink.ReasonCode != ReasonSkippedSymlink {
		t.Fatalf("unexpected symlink classification %#v", symlink)
	}
	large := ClassifyPath(PathObservation{
		RelativePath: "large.md",
		Exists:       true,
		Kind:         PathKindFile,
		Safe:         true,
		SizeBytes:    9,
	}, root)
	if large.Included || large.ReasonCode != ReasonSkippedTooLarge {
		t.Fatalf("unexpected large classification %#v", large)
	}
}

func TestClassifyPathNarrowsMarkdownTextPolicyPerFile(t *testing.T) {
	t.Parallel()
	root := testValidatedRoot(t, RootConfig{
		RootKey:     "notes",
		SafeRootKey: "slice09",
		Include:     []string{"**/*"},
		SyncPolicy: SyncPolicy{
			Mode:       SyncModeSelectedFiles,
			ProjectRef: "project_test",
		},
		IndexPolicy: IndexPolicy{Mode: IndexModeMarkdownText},
		BackupPolicy: BackupPolicy{
			Mode:          BackupModeIncrementalRaw,
			MaxFileBytes:  1024 * 1024,
			MaxBatchBytes: 1024 * 1024,
		},
	})
	note := ClassifyPath(PathObservation{
		RelativePath: "Project.md",
		Exists:       true,
		Kind:         PathKindFile,
		Safe:         true,
		SizeBytes:    12,
	}, root)
	if !note.Included || note.Policies.Sync != SyncModeSelectedFiles || note.Policies.Index != IndexModeMarkdownText || note.Policies.Backup != BackupModeIncrementalRaw {
		t.Fatalf("unexpected markdown policy classification %#v", note)
	}
	pdf := ClassifyPath(PathObservation{
		RelativePath: "Attachments/Paper.pdf",
		Exists:       true,
		Kind:         PathKindFile,
		Safe:         true,
		SizeBytes:    12,
	}, root)
	if !pdf.Included {
		t.Fatalf("pdf attachment should still be included for backup/catalog: %#v", pdf)
	}
	if pdf.Policies.Sync != SyncModeNone || pdf.Policies.Index != IndexModeMetadataOnly || pdf.Policies.Backup != BackupModeIncrementalRaw {
		t.Fatalf("unexpected pdf policy classification %#v", pdf)
	}
}

func TestClassifyManagedPolicyRetainsGitAndHiddenUserState(t *testing.T) {
	root := testValidatedRoot(t, RootConfig{
		RootKey:      "documents",
		SafeRootKey:  "slice09",
		Include:      []string{"**/*"},
		IgnorePolicy: IgnorePolicy{Profile: "managed", DiscoverUserRules: true},
	})
	for _, pathValue := range []string{".git/config", ".env", ".secrets/token", ".data/app.db", ".custom/state"} {
		classification := ClassifyPath(PathObservation{RelativePath: pathValue, Exists: true, Kind: PathKindFile, Hidden: true, Safe: true}, root)
		if !classification.Included {
			t.Fatalf("managed policy excluded %s: %#v", pathValue, classification)
		}
	}
	dependency := ClassifyPath(PathObservation{RelativePath: "node_modules/pkg/index.js", Exists: true, Kind: PathKindFile, Safe: true}, root)
	if dependency.Included || dependency.ReasonCode != ReasonExcludedFilePolicy || dependency.PolicyRuleSource != "reconstructible" {
		t.Fatalf("managed dependency classification: %#v", dependency)
	}
}

func TestClassifyPathKeepsMetadataOnlySyncForNonTextWithoutBackup(t *testing.T) {
	t.Parallel()
	root := testValidatedRoot(t, RootConfig{
		RootKey:     "notes",
		SafeRootKey: "slice09",
		Include:     []string{"**/*"},
		SyncPolicy: SyncPolicy{
			Mode:       SyncModeSelectedFiles,
			ProjectRef: "project_test",
		},
		IndexPolicy: IndexPolicy{Mode: IndexModeMarkdownText},
		BackupPolicy: BackupPolicy{
			Mode: BackupModeNone,
		},
	})
	pdf := ClassifyPath(PathObservation{
		RelativePath: "Attachments/Paper.pdf",
		Exists:       true,
		Kind:         PathKindFile,
		Safe:         true,
		SizeBytes:    12,
	}, root)
	if !pdf.Included {
		t.Fatalf("pdf attachment should still be included for metadata-only sync: %#v", pdf)
	}
	if pdf.Policies.Sync != SyncModeSelectedFiles || pdf.Policies.Index != IndexModeMetadataOnly || pdf.Policies.Backup != BackupModeNone {
		t.Fatalf("unexpected pdf policy classification %#v", pdf)
	}
}

func testValidatedRoot(t *testing.T, config RootConfig) ValidatedRoot {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "root"), 0o700); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	config.RootRelativePath = "root"
	validated, err := ValidateRootConfig(config, filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{
		filesystemconnector.DefaultSafeRoot("slice09", dir),
	}})
	if err != nil {
		t.Fatalf("ValidateRootConfig failed: %v", err)
	}
	return validated
}
