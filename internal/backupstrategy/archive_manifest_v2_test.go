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
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"loom.local/loom/internal/provenance"
)

func TestDirectArchiveV2EmptyBoxRootsPreserveLegacyValidation(t *testing.T) {
	fixture := newOperationalFixture(t)
	operational := createOperationalFixture(t, fixture, "operational-empty-box", time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC), nil)
	root := filepath.Join(t.TempDir(), "empty")
	if err := os.Mkdir(root, 0o750); err != nil {
		t.Fatal(err)
	}
	input := directArchiveV2TestInput(root, operational, fixture.schemaHead)
	for _, name := range []string{"box_topics", "box_library", "application_data", "box_notes", "box_projects", "canonical"} {
		t.Run(name, func(t *testing.T) {
			request := input.Request
			request.Roots = []DirectArchiveRoot{{Name: name, Path: root}}
			_, err := normalizeDirectArchiveRequestV2(request)
			allowEmpty := name == "box_topics" || name == "box_library" || name == "application_data"
			if (err == nil) != allowEmpty {
				t.Fatalf("v2 empty root %s: %v", name, err)
			}
			legacy := DirectArchiveRequest{
				Schema: DirectArchiveRequestSchema, NodeID: request.NodeID, ArchiveRef: request.ArchiveRef,
				ArchiveClass: request.ArchiveClass, CreatedAt: request.CreatedAt, Roots: request.Roots,
				OperationalPackage: request.OperationalPackage, ProvenancePackage: request.ProvenancePackage,
			}
			if _, err := normalizeDirectArchiveRequest(legacy); err == nil {
				t.Fatalf("legacy empty root unexpectedly allowed: %s", name)
			}
		})
	}
}

func TestDirectArchiveV2EnvelopeDoesNotReadOrHashUserData(t *testing.T) {
	fixture := newOperationalFixture(t)
	operational := createOperationalFixture(t, fixture, "operational-v2-no-read", time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC), nil)
	root := filepath.Join(t.TempDir(), "canonical")
	if err := os.Mkdir(root, 0o750); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(root, "payload.bin")
	if err := os.WriteFile(payload, bytes.Repeat([]byte("a"), 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(root, "ignored-special-node"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := directArchiveV2TestInput(root, operational, fixture.schemaHead)
	prepared, err := PrepareDirectArchiveManifestV2(context.Background(), input)
	if err != nil {
		t.Fatalf("PrepareDirectArchiveManifestV2 returned error: %v", err)
	}
	if prepared.ManifestV2 == nil || prepared.Manifest.Schema != "" || len(prepared.ManifestV2.Roots) != 1 {
		t.Fatalf("v2 result is incomplete or confused with v1: %#v", prepared)
	}
	if err := os.WriteFile(payload, bytes.Repeat([]byte("b"), 8192), 0o600); err != nil {
		t.Fatal(err)
	}
	added := filepath.Join(root, "added-after-first-prepare")
	if err := os.WriteFile(added, []byte("new user data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "ignored-special-node")); err != nil {
		t.Fatal(err)
	}
	replay, err := PrepareDirectArchiveManifestV2(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if replay.ManifestSHA256 != prepared.ManifestSHA256 || !bytes.Equal(replay.ManifestBytes, prepared.ManifestBytes) {
		t.Fatal("v2 envelope changed after user-data content changed")
	}
	if bytes.Contains(prepared.ManifestBytes, []byte("payload.bin")) || bytes.Contains(prepared.ManifestBytes, []byte("ignored-special-node")) {
		t.Fatal("v2 envelope contains recursively discovered user-data entries")
	}
	parsed, err := ParseAuthenticatedDirectArchiveManifest(prepared.ManifestBytes, prepared.HashFileBytes)
	if err != nil || parsed.ManifestV2 == nil || parsed.ManifestV2.VerificationProfile != DirectArchiveVerificationProfileRoutineIncrementalV1 {
		t.Fatalf("authenticated v2 parse = %#v err=%v", parsed, err)
	}
}

func TestDirectArchiveV2EnvelopeBindsExactConfigurationAndPackageEvidence(t *testing.T) {
	fixture := newOperationalFixture(t)
	operational := createOperationalFixture(t, fixture, "operational-v2-bind", time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC), nil)
	root := filepath.Join(t.TempDir(), "canonical")
	if err := os.Mkdir(root, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "payload"), []byte("user data"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := directArchiveV2TestInput(root, operational, fixture.schemaHead)
	prepared, err := PrepareDirectArchiveManifestV2(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	assertChanged := func(name string, input DirectArchiveManifestPrepareInputV2) {
		t.Helper()
		changed, err := PrepareDirectArchiveManifestV2(context.Background(), input)
		if err != nil {
			t.Fatalf("%s: prepare: %v", name, err)
		}
		if changed.ManifestSHA256 == prepared.ManifestSHA256 || bytes.Equal(changed.ManifestBytes, prepared.ManifestBytes) {
			t.Fatalf("%s did not change authenticated v2 envelope", name)
		}
	}

	repository := cloneDirectArchiveV2Input(base)
	repository.Repository = "/tmp/other-disposable-borg"
	assertChanged("repository", repository)

	identity := cloneDirectArchiveV2Input(base)
	identity.Request.ArchiveRef = "history-20260829T110000Z"
	identity.ArchiveName = "__loom-direct-user-data-loom-main-history-20260829T110000Z"
	assertChanged("archive identity", identity)

	exclusions := cloneDirectArchiveV2Input(base)
	exclusions.Request.Exclusions = []DirectArchiveExclusion{{Root: "canonical", RelativePath: ".loom-acceptance"}}
	assertChanged("exclusions", exclusions)

	secondRoot := filepath.Join(t.TempDir(), "lane")
	if err := os.Mkdir(secondRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secondRoot, "entry"), []byte("lane"), 0o600); err != nil {
		t.Fatal(err)
	}
	roots := cloneDirectArchiveV2Input(base)
	roots.Request.Roots = append(roots.Request.Roots, DirectArchiveRoot{Name: "lane", Path: secondRoot})
	assertChanged("root configuration", roots)

	operational2 := createOperationalFixture(t, fixture, "operational-v2-bind-2", time.Date(2026, 8, 29, 8, 5, 0, 0, time.UTC), nil)
	packageEvidence := cloneDirectArchiveV2Input(base)
	packageEvidence.Request.OperationalPackage.Path = operational2.PackageDir
	packageEvidence.Request.OperationalPackage.PackageID = operational2.Verification.PackageID
	packageEvidence.Request.OperationalPackage.ManifestSHA256 = operational2.ManifestSHA256
	assertChanged("operational package evidence", packageEvidence)

	provenanceEvidence := cloneDirectArchiveV2Input(base)
	originalVerifier := provenanceEvidence.Request.ProvenancePackage.Verify
	provenanceEvidence.Request.ProvenancePackage.Verify = func(ctx context.Context, packageDir, manifestSHA string) (DirectArchiveProvenanceVerification, error) {
		verification, err := originalVerifier(ctx, packageDir, manifestSHA)
		verification.GraphDigest = "sha256:" + string(bytes.Repeat([]byte("d"), sha256.Size*2))
		return verification, err
	}
	assertChanged("provenance package evidence", provenanceEvidence)
}

func TestDirectArchiveV2ManifestRejectsUnknownFieldsSchemasAliasesOverlapsAndForgedHashes(t *testing.T) {
	fixture := newOperationalFixture(t)
	operational := createOperationalFixture(t, fixture, "operational-v2-parse", time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC), nil)
	root := filepath.Join(t.TempDir(), "canonical")
	if err := os.Mkdir(root, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "payload"), []byte("user data"), 0o600); err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareDirectArchiveManifestV2(context.Background(), directArchiveV2TestInput(root, operational, fixture.schemaHead))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseAuthenticatedDirectArchiveManifest(prepared.ManifestBytes, []byte("0  manifest.json\n")); !errors.Is(err, ErrDirectArchiveRequestInvalid) {
		t.Fatalf("forged outer hash error = %v", err)
	}

	unknownSchema := mutateDirectArchiveV2JSON(t, prepared.ManifestBytes, func(value map[string]any) {
		value["schema"] = "loom.direct_archive.manifest.v999"
	})
	assertDirectArchiveV2ParseRejected(t, unknownSchema)
	unknownField := mutateDirectArchiveV2JSON(t, prepared.ManifestBytes, func(value map[string]any) {
		value["unexpected"] = true
	})
	assertDirectArchiveV2ParseRejected(t, unknownField)
	pathAlias := mutateDirectArchiveV2JSON(t, prepared.ManifestBytes, func(value map[string]any) {
		roots := value["roots"].([]any)
		rootValue := roots[0].(map[string]any)
		rootValue["source_path"] = rootValue["source_path"].(string) + "/."
	})
	assertDirectArchiveV2ParseRejected(t, pathAlias)
	overlap := mutateDirectArchiveV2JSON(t, prepared.ManifestBytes, func(value map[string]any) {
		roots := value["roots"].([]any)
		rootValue := roots[0].(map[string]any)
		roots = append(roots, map[string]any{
			"name": "nested", "source_path": filepath.Join(root, "nested"),
			"archive_path": directArchivePath(filepath.Join(root, "nested")),
		})
		value["roots"] = roots
		_ = rootValue
	})
	assertDirectArchiveV2ParseRejected(t, overlap)
	forgedEntries := mutateDirectArchiveV2JSON(t, prepared.ManifestBytes, func(value map[string]any) {
		pkg := value["operational_package"].(map[string]any)
		pkg["entries_sha256"] = string(bytes.Repeat([]byte("0"), sha256.Size*2))
	})
	assertDirectArchiveV2ParseRejected(t, forgedEntries)
	forgedMode := *prepared.ManifestV2
	forgedMode.OperationalPackage.Entries = append([]DirectArchiveEntry(nil), prepared.ManifestV2.OperationalPackage.Entries...)
	manifestEntry := directArchiveEntryByPath(forgedMode.OperationalPackage.Entries, OperationalManifestFile)
	manifestEntry.Mode = 0o640
	forgedMode.OperationalPackage.EntriesSHA256, err = directArchiveEntriesDigest(forgedMode.OperationalPackage.Entries)
	if err != nil {
		t.Fatal(err)
	}
	forgedModeBytes, err := json.MarshalIndent(forgedMode, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	assertDirectArchiveV2ParseRejected(t, append(forgedModeBytes, '\n'))
	missingEvidence := mutateDirectArchiveV2JSON(t, prepared.ManifestBytes, func(value map[string]any) {
		pkg := value["provenance_package"].(map[string]any)
		pkg["entries"] = []any{}
	})
	assertDirectArchiveV2ParseRejected(t, missingEvidence)
}

func TestDirectArchiveV2AuthenticationAcceptsExactHistoricalProvenanceHeadSix(t *testing.T) {
	fixture := newOperationalFixture(t)
	operational := createOperationalFixture(t, fixture, "operational-v2-historical-provenance", time.Date(2026, 9, 1, 3, 0, 0, 0, time.UTC), nil)
	root := directArchiveV2UserRoot(t)
	input := directArchiveV2TestInput(root, operational, fixture.schemaHead)
	currentVerifier := input.Request.ProvenancePackage.Verify
	input.Request.ProvenancePackage.Verify = func(ctx context.Context, packageDir, manifestSHA string) (DirectArchiveProvenanceVerification, error) {
		verification, err := currentVerifier(ctx, packageDir, manifestSHA)
		verification.SchemaHead = 6
		return verification, err
	}
	prepared, err := PrepareDirectArchiveManifestV2(context.Background(), input)
	if err != nil {
		t.Fatalf("prepare historical head-6 v2 manifest: %v", err)
	}
	parsed, err := ParseAuthenticatedDirectArchiveManifest(prepared.ManifestBytes, prepared.HashFileBytes)
	if err != nil || parsed.ManifestV2 == nil || parsed.ManifestV2.ProvenancePackage.SchemaHead != 6 {
		t.Fatalf("historical authenticated parse = %#v err=%v", parsed, err)
	}

	for _, unsupported := range []int{0, 5, provenance.SchemaHead + 1} {
		forged := mutateDirectArchiveV2JSON(t, prepared.ManifestBytes, func(value map[string]any) {
			value["provenance_package"].(map[string]any)["schema_head"] = unsupported
		})
		assertDirectArchiveV2ParseRejected(t, forged)
	}
}

func TestDirectArchiveV2EnvelopeRejectsUnsafeRootsExclusionsAndResolvedAliases(t *testing.T) {
	fixture := newOperationalFixture(t)
	operational := createOperationalFixture(t, fixture, "operational-v2-unsafe", time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC), nil)
	container := t.TempDir()
	root := filepath.Join(container, "canonical")
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "payload"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := directArchiveV2TestInput(root, operational, fixture.schemaHead)

	symlinkRoot := filepath.Join(container, "root-link")
	if err := os.Symlink(root, symlinkRoot); err != nil {
		t.Fatal(err)
	}
	fileRoot := filepath.Join(container, "root-file")
	if err := os.WriteFile(fileRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	fifoRoot := filepath.Join(container, "root-fifo")
	if err := unix.Mkfifo(fifoRoot, 0o600); err != nil {
		t.Fatal(err)
	}
	parentAlias := filepath.Join(t.TempDir(), "container-alias")
	if err := os.Symlink(container, parentAlias); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*DirectArchiveManifestPrepareInputV2)
	}{
		{name: "symlink root", mutate: func(input *DirectArchiveManifestPrepareInputV2) { input.Request.Roots[0].Path = symlinkRoot }},
		{name: "file root", mutate: func(input *DirectArchiveManifestPrepareInputV2) { input.Request.Roots[0].Path = fileRoot }},
		{name: "special root", mutate: func(input *DirectArchiveManifestPrepareInputV2) { input.Request.Roots[0].Path = fifoRoot }},
		{name: "overlapping roots", mutate: func(input *DirectArchiveManifestPrepareInputV2) {
			input.Request.Roots = append(input.Request.Roots, DirectArchiveRoot{Name: "nested", Path: filepath.Join(root, "nested")})
		}},
		{name: "malformed exclusion", mutate: func(input *DirectArchiveManifestPrepareInputV2) {
			input.Request.Exclusions = []DirectArchiveExclusion{{Root: "canonical", RelativePath: "../escape"}}
		}},
		{name: "overlapping exclusions", mutate: func(input *DirectArchiveManifestPrepareInputV2) {
			input.Request.Exclusions = []DirectArchiveExclusion{{Root: "canonical", RelativePath: "nested"}, {Root: "canonical", RelativePath: "nested/cache"}}
		}},
		{name: "resolved path aliases", mutate: func(input *DirectArchiveManifestPrepareInputV2) {
			input.Request.Roots = append(input.Request.Roots, DirectArchiveRoot{Name: "canonical-alias", Path: filepath.Join(parentAlias, "canonical")})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := cloneDirectArchiveV2Input(base)
			test.mutate(&input)
			if _, err := PrepareDirectArchiveManifestV2(context.Background(), input); !errors.Is(err, ErrDirectArchiveRequestInvalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestDirectArchiveV2EnvelopeRejectsBoundedPackageMutation(t *testing.T) {
	t.Run("operational", func(t *testing.T) {
		fixture := newOperationalFixture(t)
		operational := createOperationalFixture(t, fixture, "operational-v2-mutation", time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC), nil)
		root := directArchiveV2UserRoot(t)
		input := directArchiveV2TestInput(root, operational, fixture.schemaHead)
		artifact := operational.Verification.Manifest.Artifacts[0]
		if err := os.WriteFile(filepath.Join(operational.PackageDir, artifact.Path), []byte("mutated package bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareDirectArchiveManifestV2(context.Background(), input); !errors.Is(err, ErrDirectArchiveRequestInvalid) {
			t.Fatalf("operational package mutation error = %v", err)
		}
	})
	t.Run("provenance", func(t *testing.T) {
		fixture := newOperationalFixture(t)
		operational := createOperationalFixture(t, fixture, "operational-v2-provenance-mutation", time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC), nil)
		root := directArchiveV2UserRoot(t)
		input := directArchiveV2TestInput(root, operational, fixture.schemaHead)
		dumpPath := filepath.Join(input.Request.ProvenancePackage.Path, provenance.RecoveryDumpFile)
		if err := os.WriteFile(dumpPath, []byte("mutated provenance dump bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareDirectArchiveManifestV2(context.Background(), input); !errors.Is(err, ErrDirectArchiveRequestInvalid) {
			t.Fatalf("provenance package mutation error = %v", err)
		}
	})
}

func directArchiveV2UserRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "canonical")
	if err := os.Mkdir(root, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "payload"), []byte("user data"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func directArchiveV2TestInput(root string, operational OperationalPackageCreateResult, schemaHead int64) DirectArchiveManifestPrepareInputV2 {
	provenancePackage := createDirectArchiveProvenanceFixtureForInput(root, "provenance-package-v2")
	return DirectArchiveManifestPrepareInputV2{
		Request: DirectArchiveRequestV2{
			Schema: DirectArchiveRequestSchemaV2, VerificationProfile: DirectArchiveVerificationProfileRoutineIncrementalV1,
			NodeID: "loom-main", ArchiveRef: "history-20260829T100000Z", ArchiveClass: DirectArchiveClassUserData,
			CreatedAt: time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC), Roots: []DirectArchiveRoot{{Name: "canonical", Path: root}},
			Exclusions: []DirectArchiveExclusion{},
			OperationalPackage: DirectArchiveOperationalPackage{
				Path: operational.PackageDir, ManifestSHA256: operational.ManifestSHA256,
				PackageID: operational.Verification.PackageID, ExpectedSchemaHead: &schemaHead,
			},
			ProvenancePackage: directArchiveProvenanceRequest(provenancePackage),
		},
		Backend: DirectArchiveBackendBorg, Repository: "/tmp/disposable-borg",
		ArchiveName: "__loom-direct-user-data-loom-main-history-20260829T100000Z",
	}
}

func cloneDirectArchiveV2Input(input DirectArchiveManifestPrepareInputV2) DirectArchiveManifestPrepareInputV2 {
	input.Request.Roots = append([]DirectArchiveRoot(nil), input.Request.Roots...)
	input.Request.Exclusions = append([]DirectArchiveExclusion(nil), input.Request.Exclusions...)
	return input
}

func mutateDirectArchiveV2JSON(t *testing.T, raw []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	mutate(value)
	result, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(result, '\n')
}

func assertDirectArchiveV2ParseRejected(t *testing.T, raw []byte) {
	t.Helper()
	digest := sha256.Sum256(raw)
	hashFile := []byte(hex.EncodeToString(digest[:]) + "  " + DirectArchiveManifestFile + "\n")
	if _, err := ParseAuthenticatedDirectArchiveManifest(raw, hashFile); !errors.Is(err, ErrDirectArchiveRequestInvalid) {
		t.Fatalf("mutated v2 manifest error = %v\n%s", err, raw)
	}
}
