package filesystemconnector

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSafeListReturnsSortedRelativeEntriesAndHidesHidden(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "b.txt"), "b")
	writeTestFile(t, filepath.Join(dir, "a.txt"), "a")
	writeTestFile(t, filepath.Join(dir, ".secret.txt"), "secret")

	result, err := SafeList(Config{SafeRoots: []SafeRoot{DefaultSafeRoot("slice13", dir)}}, SafeListInput{
		Root:       "slice13",
		Path:       ".",
		MaxEntries: 10,
	})
	if err != nil {
		t.Fatalf("SafeList failed: %v", err)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("expected 2 visible entries, got %#v", result.Entries)
	}
	if result.Entries[0].RelativePath != "a.txt" || result.Entries[1].RelativePath != "b.txt" {
		t.Fatalf("entries are not sorted relative paths: %#v", result.Entries)
	}
}

func TestSafeListCanIncludeHiddenWhenRequested(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, ".secret.txt"), "secret")
	includeHidden := true

	result, err := SafeList(Config{SafeRoots: []SafeRoot{DefaultSafeRoot("slice13", dir)}}, SafeListInput{
		Root:          "slice13",
		Path:          ".",
		IncludeHidden: &includeHidden,
	})
	if err != nil {
		t.Fatalf("SafeList failed: %v", err)
	}
	if len(result.Entries) != 1 || result.Entries[0].RelativePath != ".secret.txt" || !result.Entries[0].Hidden {
		t.Fatalf("unexpected hidden listing result: %#v", result.Entries)
	}
}

func TestPathPolicyRejectsAbsoluteAndEscapePaths(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	config := Config{SafeRoots: []SafeRoot{DefaultSafeRoot("slice13", dir)}}

	_, err := ReadMetadata(config, ReadMetadataInput{Root: "slice13", Path: "/etc/passwd"})
	assertPolicyCode(t, err, ErrorAbsolutePathDenied)

	_, err = ReadMetadata(config, ReadMetadataInput{Root: "slice13", Path: "../outside.txt"})
	assertPolicyCode(t, err, ErrorPathEscapeDenied)

	_, err = ReadMetadata(config, ReadMetadataInput{Root: "missing", Path: "file.txt"})
	assertPolicyCode(t, err, ErrorUnknownRoot)
}

func TestPathPolicyRejectsSymlinkEscape(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior differs on windows")
	}
	dir := t.TempDir()
	outside := t.TempDir()
	writeTestFile(t, filepath.Join(outside, "outside.txt"), "outside")
	if err := os.Symlink(filepath.Join(outside, "outside.txt"), filepath.Join(dir, "escape.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	_, err := ReadMetadata(Config{SafeRoots: []SafeRoot{DefaultSafeRoot("slice13", dir)}}, ReadMetadataInput{
		Root: "slice13",
		Path: "escape.txt",
	})
	assertPolicyCode(t, err, ErrorPathEscapeDenied)
}

func TestReadMetadataAndPrepareIngestDoNotExposeAbsolutePath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "note.md"), "# Note\n")
	config := Config{SafeRoots: []SafeRoot{DefaultSafeRoot("slice13", dir)}}

	metadata, err := ReadMetadata(config, ReadMetadataInput{Root: "slice13", Path: "note.md"})
	if err != nil {
		t.Fatalf("ReadMetadata failed: %v", err)
	}
	if metadata.Path != "note.md" || metadata.Kind != "file" || metadata.MimeType != "text/markdown" {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
	if metadata.SourceModifiedBasis != "source_filesystem_mtime" {
		t.Fatalf("source modification basis = %q", metadata.SourceModifiedBasis)
	}

	prepared, err := PrepareIngest(config, IngestFileInput{Root: "slice13", Path: "note.md"})
	if err != nil {
		t.Fatalf("PrepareIngest failed: %v", err)
	}
	if prepared.LogicalPath != "filesystem://slice13/note.md" || prepared.HashURI == "" || len(prepared.Content) == 0 {
		t.Fatalf("unexpected prepared ingest: %#v", prepared)
	}
	if prepared.SourceMtimeBasis != "source_filesystem_mtime" || prepared.SourceMtime.IsZero() {
		t.Fatalf("prepared source time = %#v", prepared)
	}
}

func TestPrepareIngestRejectsDirectoryLargeAndPrivateRoot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "large.txt"), "0123456789")
	config := Config{SafeRoots: []SafeRoot{
		{
			RootKey:       "slice13",
			AbsolutePath:  dir,
			AllowList:     true,
			AllowMetadata: true,
			AllowIngest:   true,
			MaxFileBytes:  4,
		},
		{
			RootKey:           "private",
			AbsolutePath:      dir,
			PrivateBackupOnly: true,
			MaxFileBytes:      DefaultMaxFileBytes,
		},
	}}

	_, err := PrepareIngest(config, IngestFileInput{Root: "slice13", Path: "."})
	assertPolicyCode(t, err, ErrorNotFile)

	_, err = PrepareIngest(config, IngestFileInput{Root: "slice13", Path: "large.txt"})
	assertPolicyCode(t, err, ErrorFileTooLarge)

	_, err = SafeList(config, SafeListInput{Root: "private", Path: "."})
	assertPolicyCode(t, err, ErrorPrivateRootDenied)
}

func writeTestFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertPolicyCode(t *testing.T, err error, want string) {
	t.Helper()
	var policyErr *PolicyError
	if !errors.As(err, &policyErr) {
		t.Fatalf("expected policy error %s, got %v", want, err)
	}
	if policyErr.Code != want {
		t.Fatalf("expected policy code %s, got %s (%v)", want, policyErr.Code, err)
	}
}
