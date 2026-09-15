package filesystemmeta

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDetectPathRegularZeroByteAndExecutable(t *testing.T) {
	root := acceptanceRoot(t)
	path := filepath.Join(root, "script")
	if err := os.WriteFile(path, nil, 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("chmod script: %v", err)
	}

	got, err := DetectPath(path, DetectOptions{RootPath: root, IncludeXattrNames: true})
	if err != nil {
		t.Fatalf("DetectPath returned error: %v", err)
	}
	if got.Kind != ObjectKindRegularFile {
		t.Fatalf("kind = %q, want %q", got.Kind, ObjectKindRegularFile)
	}
	if got.LogicalSizeBytes != 0 {
		t.Fatalf("size = %d, want 0", got.LogicalSizeBytes)
	}
	if !got.Executable {
		t.Fatal("executable bit was not detected")
	}
	if got.SourceMode&0o111 == 0 {
		t.Fatalf("source mode = %#o, want executable bit", got.SourceMode)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.SourceModifiedAt == nil || !got.SourceModifiedAt.Equal(info.ModTime().UTC()) || got.SourceModifiedBasis != SourceTimeBasisFilesystemMtime {
		t.Fatalf("source modification time = %#v, want %s from filesystem mtime", got, info.ModTime().UTC())
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"source_modified_at"`) || !strings.Contains(string(raw), SourceTimeBasisFilesystemMtime) {
		t.Fatalf("raw observation omits source time: %s", raw)
	}
	if got.SourceCreatedAt != nil && got.SourceCreatedBasis != SourceTimeBasisFilesystemBirthtime {
		t.Fatalf("source creation basis = %q", got.SourceCreatedBasis)
	}
}

func TestObservationLegacyJSONWithoutSourceTimesRemainsReadable(t *testing.T) {
	t.Parallel()
	var observation Observation
	if err := json.Unmarshal([]byte(`{"kind":"regular_file","logical_size_bytes":4}`), &observation); err != nil {
		t.Fatalf("legacy observation did not decode: %v", err)
	}
	if observation.Kind != ObjectKindRegularFile || observation.SourceModifiedAt != nil || observation.SourceCreatedAt != nil {
		t.Fatalf("unexpected legacy observation: %#v", observation)
	}
}

func TestDetectPathDirectoryAndPackageDirectory(t *testing.T) {
	root := acceptanceRoot(t)
	empty := filepath.Join(root, "Empty")
	pkg := filepath.Join(root, "Draft.pages")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatalf("mkdir empty: %v", err)
	}
	if err := os.Mkdir(pkg, 0o755); err != nil {
		t.Fatalf("mkdir package: %v", err)
	}

	got, err := DetectPath(empty, DetectOptions{RootPath: root})
	if err != nil {
		t.Fatalf("DetectPath empty returned error: %v", err)
	}
	if got.Kind != ObjectKindDirectory || got.IsPackage {
		t.Fatalf("empty observation = %#v", got)
	}

	got, err = DetectPath(pkg, DetectOptions{RootPath: root})
	if err != nil {
		t.Fatalf("DetectPath package returned error: %v", err)
	}
	if got.Kind != ObjectKindPackage || !got.IsPackage || got.PackageKind != "pages_document" {
		t.Fatalf("package observation = %#v", got)
	}
	if !contains(got.Risks, FidelityRiskMetadataOnly) {
		t.Fatalf("package risks = %#v, want metadata_only", got.Risks)
	}
}

func TestDetectPathSymlinkAndBrokenSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions are platform-dependent on Windows")
	}
	root := acceptanceRoot(t)
	target := filepath.Join(root, "target.txt")
	if err := os.WriteFile(target, []byte("ok"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink("target.txt", link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	broken := filepath.Join(root, "broken.txt")
	if err := os.Symlink("missing.txt", broken); err != nil {
		t.Fatalf("broken symlink: %v", err)
	}

	got, err := DetectPath(link, DetectOptions{RootPath: root})
	if err != nil {
		t.Fatalf("DetectPath symlink returned error: %v", err)
	}
	if got.Kind != ObjectKindSymlink || got.SymlinkTarget != "target.txt" {
		t.Fatalf("symlink observation = %#v", got)
	}

	got, err = DetectPath(broken, DetectOptions{RootPath: root})
	if err != nil {
		t.Fatalf("DetectPath broken symlink returned error: %v", err)
	}
	if got.Kind != ObjectKindSymlink || got.SymlinkTarget != "missing.txt" {
		t.Fatalf("broken symlink observation = %#v", got)
	}
}

func TestDetectPathExternalSymlinkRisk(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions are platform-dependent on Windows")
	}
	root := acceptanceRoot(t)
	link := filepath.Join(root, "external")
	if err := os.Symlink(filepath.Dir(root), link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	got, err := DetectPath(link, DetectOptions{RootPath: root})
	if err != nil {
		t.Fatalf("DetectPath returned error: %v", err)
	}
	if got.Kind != ObjectKindSymlink {
		t.Fatalf("kind = %q, want symlink", got.Kind)
	}
	if !contains(got.Risks, FidelityRiskExternalReference) {
		t.Fatalf("risks = %#v, want external_reference", got.Risks)
	}
}

func TestDetectPathGeneratedAppleMetadataAndHidden(t *testing.T) {
	root := acceptanceRoot(t)
	path := filepath.Join(root, ".hidden", "._note.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir hidden: %v", err)
	}
	if err := os.WriteFile(path, []byte("sidecar"), 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	got, err := DetectPath(path, DetectOptions{RootPath: root})
	if err != nil {
		t.Fatalf("DetectPath returned error: %v", err)
	}
	if !got.Hidden {
		t.Fatal("hidden path was not detected")
	}
	if !got.GeneratedMetadata {
		t.Fatal("AppleDouble sidecar was not detected")
	}
	if !contains(got.Risks, FidelityRiskMetadataOnly) {
		t.Fatalf("risks = %#v, want metadata_only", got.Risks)
	}
}

func TestPathNameHelpers(t *testing.T) {
	if !IsGeneratedAppleMetadata(".DS_Store") || !IsGeneratedAppleMetadata("._file") {
		t.Fatal("Apple metadata helper failed")
	}
	if IsGeneratedAppleMetadata("report.md") {
		t.Fatal("normal file classified as Apple metadata")
	}
	if UnicodeFormSummary("plain") != "ascii" {
		t.Fatalf("unicode summary for ascii = %q", UnicodeFormSummary("plain"))
	}
	if CasefoldKey("Folder/Report.MD") != "folder/report.md" {
		t.Fatalf("casefold key = %q", CasefoldKey("Folder/Report.MD"))
	}
}

func TestDetectPathHardLinkCandidate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hard link behavior is platform-dependent on Windows")
	}
	root := acceptanceRoot(t)
	first := filepath.Join(root, "first.bin")
	second := filepath.Join(root, "second.bin")
	if err := os.WriteFile(first, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write first: %v", err)
	}
	if err := os.Link(first, second); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}

	got, err := DetectPath(first, DetectOptions{RootPath: root})
	if err != nil {
		t.Fatalf("DetectPath returned error: %v", err)
	}
	if got.LinkCount == nil || *got.LinkCount < 2 {
		t.Fatalf("link count = %#v, want >= 2", got.LinkCount)
	}
	if !got.IsHardLink {
		t.Fatalf("hard-link candidate not detected: %#v", got)
	}
}

func TestDetectPathSparseFileCandidate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sparse file allocation is platform-dependent on Windows")
	}
	root := acceptanceRoot(t)
	path := filepath.Join(root, "sparse.bin")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create sparse: %v", err)
	}
	if err := file.Truncate(8 * 1024 * 1024); err != nil {
		_ = file.Close()
		t.Fatalf("truncate sparse: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close sparse: %v", err)
	}

	got, err := DetectPath(path, DetectOptions{RootPath: root})
	if err != nil {
		t.Fatalf("DetectPath returned error: %v", err)
	}
	if got.AllocatedBytes == nil {
		t.Skip("allocated byte count unavailable on this filesystem")
	}
	if *got.AllocatedBytes < got.LogicalSizeBytes && !got.IsSparse {
		t.Fatalf("allocated=%d logical=%d but sparse not detected", *got.AllocatedBytes, got.LogicalSizeBytes)
	}
}

func acceptanceRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".loom-acceptance", "v0.6.8-slice-02")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir acceptance root: %v", err)
	}
	return root
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
