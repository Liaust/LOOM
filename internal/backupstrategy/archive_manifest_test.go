package backupstrategy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"loom.local/loom/internal/provenance"
)

type directArchiveProvenanceFixture struct {
	PackageDir     string
	PackageID      string
	ManifestSHA256 string
	Verification   DirectArchiveProvenanceVerification
}

func TestDirectArchiveEvidenceModifiedUnixNanoTruncatesProductionTimestamp(t *testing.T) {
	createdAt, err := time.Parse(time.RFC3339Nano, "2026-09-01T01:00:00.460330646Z")
	if err != nil {
		t.Fatal(err)
	}
	const want int64 = 1788224400460330000
	if got := DirectArchiveEvidenceModifiedUnixNano(createdAt); got != want {
		t.Fatalf("generated evidence mtime = %d; want %d", got, want)
	}
	if got := directArchiveModifiedUnixNano(createdAt); got != 1788224400460331000 {
		t.Fatalf("ordinary Borg-list mtime normalization changed: got %d", got)
	}
}

func TestDirectArchiveV1ManifestEncodingGolden(t *testing.T) {
	manifest := DirectArchiveManifest{
		Schema: DirectArchiveManifestSchema, Backend: DirectArchiveBackendBorg,
		Repository: "/repo", ArchiveName: "__loom-direct-node-ref", NodeID: "node", ArchiveRef: "ref",
		CreatedAt: time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC), ExclusionPolicy: DirectArchiveExclusionPolicy,
		Exclusions: []DirectArchiveExclusion{}, Roots: []DirectArchiveRootManifest{},
		OperationalPackage:   DirectArchiveOperationalPackageManifest{Entries: []DirectArchiveEntry{}},
		ProvenancePackage:    DirectArchiveProvenancePackageManifest{Entries: []DirectArchiveEntry{}},
		SourceSnapshotSHA256: "snapshot",
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	const golden = `{
  "schema": "loom.direct_archive.manifest.v1",
  "backend": "borg",
  "repository": "/repo",
  "archive_name": "__loom-direct-node-ref",
  "node_id": "node",
  "archive_ref": "ref",
  "created_at": "2026-08-29T10:00:00Z",
  "exclusion_policy": "path_prefix_v1",
  "exclusions": [],
  "roots": [],
  "operational_package": {
    "source_path": "",
    "archive_path": "",
    "package_id": "",
    "manifest_sha256": "",
    "schema_head": 0,
    "created_at": "0001-01-01T00:00:00Z",
    "artifact_count": 0,
    "entry_count": 0,
    "total_bytes": 0,
    "entries_sha256": "",
    "entries": []
  },
  "provenance_package": {
    "source_path": "",
    "archive_path": "",
    "package_id": "",
    "manifest_sha256": "",
    "schema_head": 0,
    "graph_digest": "",
    "completed_at": "0001-01-01T00:00:00Z",
    "dump_size_bytes": 0,
    "entry_count": 0,
    "total_bytes": 0,
    "entries_sha256": "",
    "entries": []
  },
  "source_snapshot_sha256": "snapshot"
}
`
	if string(raw) != golden {
		t.Fatalf("v1 manifest encoding changed:\n%s", raw)
	}
}

func TestDirectArchiveManifestBindsExactRootsExclusionsAndVerifiedPackage(t *testing.T) {
	fixture := newOperationalFixture(t)
	operational := createOperationalFixture(t, fixture, "operational-direct-001", time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC), nil)
	boxRoot := filepath.Join(t.TempDir(), "box")
	laneRoot := filepath.Join(t.TempDir(), "lane")
	for _, root := range []string{boxRoot, laneRoot} {
		if err := os.Mkdir(root, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	keptPath := filepath.Join(boxRoot, "Documents", "kept.txt")
	if err := os.MkdirAll(filepath.Dir(keptPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keptPath, []byte("canonical bytes\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	mtime := time.Date(2026, 8, 29, 9, 15, 0, 123_000_000, time.UTC)
	if err := os.Chtimes(keptPath, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("kept.txt", filepath.Join(filepath.Dir(keptPath), "kept-link")); err != nil {
		t.Fatal(err)
	}
	excludedPath := filepath.Join(boxRoot, ".loom-acceptance", "ignored.bin")
	if err := os.MkdirAll(filepath.Dir(excludedPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(excludedPath, []byte("excluded bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(laneRoot, "custody.bin"), []byte("lane custody"), 0o600); err != nil {
		t.Fatal(err)
	}

	schemaHead := fixture.schemaHead
	provenancePackage := createDirectArchiveProvenanceFixture(t, "provenance-direct-001")
	request := DirectArchiveRequest{
		Schema: DirectArchiveRequestSchema, NodeID: "loom-main", ArchiveRef: "history-20260829T100000Z",
		CreatedAt:  time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC),
		Roots:      []DirectArchiveRoot{{Name: "lane", Path: laneRoot}, {Name: "box", Path: boxRoot}},
		Exclusions: []DirectArchiveExclusion{{Root: "box", RelativePath: ".loom-acceptance"}},
		OperationalPackage: DirectArchiveOperationalPackage{
			Path: operational.PackageDir, ManifestSHA256: operational.ManifestSHA256,
			PackageID: operational.Verification.PackageID, ExpectedSchemaHead: &schemaHead,
		},
		ProvenancePackage: directArchiveProvenanceRequest(provenancePackage),
	}
	input := DirectArchiveManifestPrepareInput{
		Request: request, Backend: DirectArchiveBackendBorg,
		Repository: "/tmp/disposable-borg", ArchiveName: "__loom-direct-loom-main-history-20260829T100000Z",
	}
	prepared, err := PrepareDirectArchiveManifest(context.Background(), input)
	if err != nil {
		t.Fatalf("PrepareDirectArchiveManifest returned error: %v", err)
	}
	if prepared.ManifestSHA256 == "" || prepared.Manifest.SourceSnapshotSHA256 == "" || len(prepared.Manifest.Roots) != 2 {
		t.Fatalf("manifest identity is incomplete: %#v", prepared.Manifest)
	}
	if prepared.Manifest.Roots[0].Name != "box" || prepared.Manifest.Roots[0].SourcePath != boxRoot || prepared.Manifest.Roots[0].ArchivePath != directArchivePath(boxRoot) {
		t.Fatalf("exact box root not bound: %#v", prepared.Manifest.Roots[0])
	}
	entries := make(map[string]DirectArchiveEntry)
	for _, entry := range prepared.Manifest.Roots[0].Entries {
		entries[entry.Path] = entry
	}
	kept := entries["Documents/kept.txt"]
	wantHash := sha256.Sum256([]byte("canonical bytes\n"))
	if kept.Type != "file" || kept.SHA256 != hex.EncodeToString(wantHash[:]) || kept.Mode != 0o640 || kept.ModifiedUnixNano != mtime.UnixNano() {
		t.Fatalf("file fidelity evidence = %#v", kept)
	}
	link := entries["Documents/kept-link"]
	if link.Type != "symlink" || link.LinkTarget != "kept.txt" {
		t.Fatalf("symlink fidelity evidence = %#v", link)
	}
	if _, ok := entries[".loom-acceptance"]; ok {
		t.Fatal("excluded subtree entered archive manifest")
	}
	if op := prepared.Manifest.OperationalPackage; op.SourcePath != operational.PackageDir || op.ManifestSHA256 != operational.ManifestSHA256 || op.PackageID != operational.Verification.PackageID || op.ArtifactCount != 7 || op.EntryCount != int64(len(op.Entries)) || op.EntryCount != 10 || op.EntriesSHA256 == "" {
		t.Fatalf("operational package evidence = %#v", op)
	}
	if !directArchiveEntriesContainSHA(prepared.Manifest.OperationalPackage.Entries, OperationalManifestFile, operational.ManifestSHA256) {
		t.Fatal("direct archive manifest does not bind exact verified operational manifest bytes")
	}
	if prov := prepared.Manifest.ProvenancePackage; prov.SourcePath != provenancePackage.PackageDir || prov.ManifestSHA256 != provenancePackage.ManifestSHA256 || prov.PackageID != provenancePackage.PackageID || prov.SchemaHead != provenance.SchemaHead || prov.GraphDigest != provenancePackage.Verification.GraphDigest || prov.EntryCount != 3 {
		t.Fatalf("provenance package evidence = %#v", prov)
	}
	if !directArchiveEntriesContainSHA(prepared.Manifest.ProvenancePackage.Entries, provenance.RecoveryManifestFile, provenancePackage.ManifestSHA256) {
		t.Fatal("direct archive manifest does not bind exact verified provenance manifest bytes")
	}

	replay, err := PrepareDirectArchiveManifest(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if replay.ManifestSHA256 != prepared.ManifestSHA256 || !bytes.Equal(replay.ManifestBytes, prepared.ManifestBytes) {
		t.Fatal("same request and sources did not produce the same manifest identity")
	}
	parsed, err := ParseAuthenticatedDirectArchiveManifest(prepared.ManifestBytes, prepared.HashFileBytes)
	if err != nil || parsed.ManifestSHA256 != prepared.ManifestSHA256 {
		t.Fatalf("authenticated manifest parse = %#v err=%v", parsed, err)
	}
	tampered := append([]byte(nil), prepared.ManifestBytes...)
	tampered[len(tampered)-2] ^= 1
	if _, err := ParseAuthenticatedDirectArchiveManifest(tampered, prepared.HashFileBytes); !errors.Is(err, ErrDirectArchiveRequestInvalid) {
		t.Fatalf("tampered manifest error = %v", err)
	}
}

func TestDirectArchiveManifestDetectsSourceInstability(t *testing.T) {
	fixture := newOperationalFixture(t)
	operational := createOperationalFixture(t, fixture, "operational-direct-002", time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC), nil)
	root := filepath.Join(t.TempDir(), "canonical")
	if err := os.Mkdir(root, 0o750); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(root, "mutable.txt")
	if err := os.WriteFile(payload, []byte("before"), 0o640); err != nil {
		t.Fatal(err)
	}
	input := directArchiveManifestTestInput(root, operational, fixture.schemaHead)
	prepared, err := PrepareDirectArchiveManifest(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte("after"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := VerifyDirectArchiveSourceStability(context.Background(), input, prepared); !errors.Is(err, ErrDirectArchiveSourceChanged) {
		t.Fatalf("source stability error = %v", err)
	}
	if _, err := os.Stat(operational.PackageDir); err != nil {
		t.Fatalf("operational package changed during instability detection: %v", err)
	}
}

func TestDirectArchiveManifestFailsClosedForUnsafeRootsAndUnverifiedPackage(t *testing.T) {
	fixture := newOperationalFixture(t)
	operational := createOperationalFixture(t, fixture, "operational-direct-003", time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC), nil)
	root := filepath.Join(t.TempDir(), "canonical")
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o750); err != nil {
		t.Fatal(err)
	}
	base := directArchiveManifestTestInput(root, operational, fixture.schemaHead)

	tests := []struct {
		name   string
		mutate func(*DirectArchiveManifestPrepareInput)
	}{
		{name: "overlapping roots", mutate: func(input *DirectArchiveManifestPrepareInput) {
			input.Request.Roots = append(input.Request.Roots, DirectArchiveRoot{Name: "nested", Path: filepath.Join(root, "nested")})
		}},
		{name: "unknown exclusion root", mutate: func(input *DirectArchiveManifestPrepareInput) {
			input.Request.Exclusions = []DirectArchiveExclusion{{Root: "unknown", RelativePath: "tmp"}}
		}},
		{name: "different operational manifest", mutate: func(input *DirectArchiveManifestPrepareInput) {
			input.Request.OperationalPackage.ManifestSHA256 = string(bytes.Repeat([]byte("0"), sha256.Size*2))
		}},
		{name: "provenance package id path mismatch", mutate: func(input *DirectArchiveManifestPrepareInput) {
			input.Request.ProvenancePackage.PackageID = "different-provenance-package"
		}},
		{name: "missing provenance verifier", mutate: func(input *DirectArchiveManifestPrepareInput) {
			input.Request.ProvenancePackage.Verify = nil
		}},
		{name: "different provenance manifest", mutate: func(input *DirectArchiveManifestPrepareInput) {
			input.Request.ProvenancePackage.ManifestSHA256 = string(bytes.Repeat([]byte("0"), sha256.Size*2))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			input.Request.Roots = append([]DirectArchiveRoot(nil), base.Request.Roots...)
			input.Request.Exclusions = append([]DirectArchiveExclusion(nil), base.Request.Exclusions...)
			test.mutate(&input)
			if _, err := PrepareDirectArchiveManifest(context.Background(), input); !errors.Is(err, ErrDirectArchiveRequestInvalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestDirectArchiveManifestKeepsExactProvenanceDump0600Contract(t *testing.T) {
	fixture := newOperationalFixture(t)
	operational := createOperationalFixture(t, fixture, "operational-direct-provenance-mode", time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC), nil)
	root := filepath.Join(t.TempDir(), "canonical")
	if err := os.Mkdir(root, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "payload"), []byte("canonical"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := directArchiveManifestTestInput(root, operational, fixture.schemaHead)
	dumpPath := filepath.Join(input.Request.ProvenancePackage.Path, provenance.RecoveryDumpFile)
	if err := os.Chmod(dumpPath, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareDirectArchiveManifest(context.Background(), input); err == nil || !strings.Contains(err.Error(), "provenance package dump entry mismatch") {
		t.Fatalf("0640 provenance dump error = %v", err)
	}
	if err := os.Chmod(dumpPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareDirectArchiveManifest(context.Background(), input); err != nil {
		t.Fatalf("exact 0600 provenance dump rejected: %v", err)
	}
}

func TestDirectArchiveEntryRejectsInvalidUTF8Identity(t *testing.T) {
	digest := sha256.Sum256([]byte("payload"))
	file := DirectArchiveEntry{
		Path: "valid", Type: "file", SizeBytes: 7, SHA256: hex.EncodeToString(digest[:]), Mode: 0o600,
	}
	file.Path = string([]byte{'b', 'a', 'd', '-', 0xff})
	if err := validateDirectArchiveEntry(file); !errors.Is(err, ErrDirectArchiveRequestInvalid) {
		t.Fatalf("invalid UTF-8 path error = %v", err)
	}
	linkTarget := string([]byte{'b', 'a', 'd', '-', 0xff})
	linkDigest := sha256.Sum256([]byte(linkTarget))
	link := DirectArchiveEntry{
		Path: "link", Type: "symlink", SizeBytes: int64(len(linkTarget)), SHA256: hex.EncodeToString(linkDigest[:]), Mode: 0o777, LinkTarget: linkTarget,
	}
	if err := validateDirectArchiveEntry(link); !errors.Is(err, ErrDirectArchiveRequestInvalid) {
		t.Fatalf("invalid UTF-8 symlink target error = %v", err)
	}
}

func TestVerifyExtractedDirectArchiveV2UserEntryKeepsExternalSymlinksInert(t *testing.T) {
	workspace := t.TempDir()
	userRootPath := filepath.Join(workspace, "restore", "user-root")
	outsideDir := filepath.Join(workspace, "outside")
	if err := os.MkdirAll(filepath.Join(userRootPath, "nested"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outsideDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outsidePath := filepath.Join(outsideDir, "sentinel")
	const sentinel = "outside bytes must remain untouched\n"
	if err := os.WriteFile(outsidePath, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}

	absoluteLink := filepath.Join(userRootPath, "absolute-link")
	if err := os.Symlink(outsidePath, absoluteLink); err != nil {
		t.Fatal(err)
	}
	relativeLink := filepath.Join(userRootPath, "nested", "relative-link")
	relativeTarget, err := filepath.Rel(filepath.Dir(relativeLink), outsidePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(relativeTarget, relativeLink); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(outsideDir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(outsideDir, 0o700) })
	for _, test := range []struct {
		path     string
		relative string
		target   string
	}{
		{path: absoluteLink, relative: "user-root/absolute-link", target: outsidePath},
		{path: relativeLink, relative: "user-root/nested/relative-link", target: relativeTarget},
	} {
		target, isSymlink, err := verifyExtractedDirectArchiveV2UserEntry(test.path, test.relative, "user-root")
		if err != nil || !isSymlink || target != test.target {
			t.Fatalf("inert link %q rejected: %v", test.relative, err)
		}
		if restoredTarget, err := os.Readlink(test.path); err != nil || restoredTarget != test.target {
			t.Fatalf("link %q target = %q err=%v; want %q", test.relative, restoredTarget, err, test.target)
		}
	}
	if err := os.Chmod(outsideDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if payload, err := os.ReadFile(outsidePath); err != nil || string(payload) != sentinel {
		t.Fatalf("outside sentinel = %q err=%v", payload, err)
	}

	rootLink := filepath.Join(workspace, "root-link")
	if err := os.Symlink(userRootPath, rootLink); err != nil {
		t.Fatal(err)
	}
	if _, _, err := verifyExtractedDirectArchiveV2UserEntry(rootLink, "user-root", "user-root"); err == nil || !strings.Contains(err.Error(), "not a real directory") {
		t.Fatalf("declared symlink root error = %v", err)
	}
	fifo := filepath.Join(userRootPath, "special")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := verifyExtractedDirectArchiveV2UserEntry(fifo, "user-root/special", "user-root"); err == nil || !strings.Contains(err.Error(), "unsupported file type") {
		t.Fatalf("special user entry error = %v", err)
	}
}

func directArchiveManifestTestInput(root string, operational OperationalPackageCreateResult, schemaHead int64) DirectArchiveManifestPrepareInput {
	provenancePackage := createDirectArchiveProvenanceFixtureForInput(root, "provenance-package")
	return DirectArchiveManifestPrepareInput{
		Request: DirectArchiveRequest{
			Schema: DirectArchiveRequestSchema, NodeID: "loom-main", ArchiveRef: "history-20260829T100000Z",
			CreatedAt: time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC),
			Roots:     []DirectArchiveRoot{{Name: "canonical", Path: root}},
			OperationalPackage: DirectArchiveOperationalPackage{
				Path: operational.PackageDir, ManifestSHA256: operational.ManifestSHA256,
				PackageID: operational.Verification.PackageID, ExpectedSchemaHead: &schemaHead,
			},
			ProvenancePackage: directArchiveProvenanceRequest(provenancePackage),
		},
		Backend: DirectArchiveBackendBorg, Repository: "/tmp/disposable-borg",
		ArchiveName: "__loom-direct-loom-main-history-20260829T100000Z",
	}
}

func createDirectArchiveProvenanceFixtureForInput(root, packageID string) directArchiveProvenanceFixture {
	packageDir := filepath.Join(filepath.Dir(root), packageID)
	if err := os.Mkdir(packageDir, 0o700); err != nil {
		panic(err)
	}
	manifestBytes := []byte("isolated provenance recovery manifest\n")
	dumpBytes := []byte("isolated provenance recovery dump\n")
	if err := os.WriteFile(filepath.Join(packageDir, provenance.RecoveryManifestFile), manifestBytes, 0o600); err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(packageDir, provenance.RecoveryDumpFile), dumpBytes, 0o600); err != nil {
		panic(err)
	}
	digest := sha256.Sum256(manifestBytes)
	manifestSHA := hex.EncodeToString(digest[:])
	verification := DirectArchiveProvenanceVerification{
		ManifestSHA256: manifestSHA,
		SchemaHead:     provenance.SchemaHead,
		GraphDigest:    "sha256:" + string(bytes.Repeat([]byte("c"), sha256.Size*2)),
		CompletedAt:    time.Date(2026, 8, 29, 8, 30, 0, 0, time.UTC),
		DumpSizeBytes:  int64(len(dumpBytes)),
	}
	return directArchiveProvenanceFixture{PackageDir: packageDir, PackageID: packageID, ManifestSHA256: manifestSHA, Verification: verification}
}

func createDirectArchiveProvenanceFixture(t *testing.T, packageID string) directArchiveProvenanceFixture {
	t.Helper()
	root := filepath.Join(t.TempDir(), "canonical-placeholder")
	return createDirectArchiveProvenanceFixtureForInput(root, packageID)
}

func directArchiveProvenanceRequest(fixture directArchiveProvenanceFixture) DirectArchiveProvenancePackage {
	return DirectArchiveProvenancePackage{
		Path: fixture.PackageDir, ManifestSHA256: fixture.ManifestSHA256, PackageID: fixture.PackageID,
		Verify: func(_ context.Context, packageDir, manifestSHA256 string) (DirectArchiveProvenanceVerification, error) {
			if packageDir != fixture.PackageDir || manifestSHA256 != fixture.ManifestSHA256 {
				return DirectArchiveProvenanceVerification{}, errors.New("provenance package identity mismatch")
			}
			return fixture.Verification, nil
		},
	}
}
