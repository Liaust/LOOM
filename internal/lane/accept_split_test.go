package lane

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type resolvingLaneCatalog struct {
	fakeLaneCatalog
	resolve func(string, string, string) (string, error)
	calls   []string
}

func (catalog *resolvingLaneCatalog) ResolveLaneCustodyMember(_ context.Context, sourceNode, batch, member string) (string, error) {
	catalog.calls = append(catalog.calls, member)
	return catalog.resolve(sourceNode, batch, member)
}

type directoryVerifyingLaneCatalog struct {
	*resolvingLaneCatalog
	verify func(string, string, string, time.Time, time.Time) error
}

func (catalog *directoryVerifyingLaneCatalog) VerifyLaneCustodyDirectoryMutation(_ context.Context, sourceNode, batch, relative string, original, observed time.Time) error {
	return catalog.verify(sourceNode, batch, relative, original, observed)
}

type committedLaneFixture struct {
	input        AcceptInput
	root         string
	moved        string
	manifest     []byte
	manifestInfo os.FileInfo
}

func newCommittedLaneFixture(t *testing.T, populate ...func(string)) committedLaneFixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	remote := filepath.Join(base, "runtime")
	withLaneRemoteBoundary(t, remote)
	input := AcceptInput{
		AcceptedPath: filepath.Join(remote, "staging", "macbook", "lane_split"),
		RemoteRoot:   remote, ImportsRoot: filepath.Join(base, "imports"),
		SourceNodeKey: "macbook", BatchID: "lane_split", AcceptedDate: "2026-11-05",
	}
	for _, relative := range []string{"collection/nested", "collection/empty", "stays"} {
		if err := os.MkdirAll(filepath.Join(input.AcceptedPath, relative), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for relative, content := range map[string]string{"collection/nested/data.txt": "retained payload", "stays/other.txt": "unmoved"} {
		if err := os.WriteFile(filepath.Join(input.AcceptedPath, relative), []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("nested/data.txt", filepath.Join(input.AcceptedPath, "collection/link")); err != nil {
		t.Fatal(err)
	}
	for _, addEntries := range populate {
		addEntries(input.AcceptedPath)
	}
	// A resolver must not influence the first promotion or bless staging paths.
	catalog := &resolvingLaneCatalog{resolve: func(_, _, _ string) (string, error) {
		t.Fatal("resolver consulted for an uncommitted batch")
		return "", nil
	}}
	result, err := Accept(context.Background(), catalog, input)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(result.AcceptedPath, laneCustodyManifestName)
	manifest, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(marker)
	if err != nil {
		t.Fatal(err)
	}
	return committedLaneFixture{input: input, root: result.AcceptedPath, moved: filepath.Join(base, "retained", "named-collection"), manifest: manifest, manifestInfo: info}
}

func (fixture committedLaneFixture) split(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(fixture.moved), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(fixture.root, "collection"), fixture.moved); err != nil {
		t.Fatal(err)
	}
}

func newNestedCommittedLaneFixture(t *testing.T) (committedLaneFixture, time.Time, time.Time) {
	t.Helper()
	original := time.Date(2018, 1, 2, 3, 4, 5, 0, time.UTC)
	fixture := newCommittedLaneFixture(t, func(root string) {
		if err := os.Chtimes(filepath.Join(root, "collection"), original, original); err != nil {
			t.Fatal(err)
		}
	})
	if err := os.MkdirAll(filepath.Dir(fixture.moved), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(fixture.root, "collection/nested"), fixture.moved); err != nil {
		t.Fatal(err)
	}
	parent, err := os.Lstat(filepath.Join(fixture.root, "collection"))
	if err != nil {
		t.Fatal(err)
	}
	if parent.ModTime().Equal(original) {
		t.Fatal("nested removal did not change the original parent's mtime")
	}
	return fixture, original, parent.ModTime().UTC()
}

func (fixture committedLaneFixture) resolveNested(sourceNode, batch, member string) (string, error) {
	if sourceNode != fixture.input.SourceNodeKey || batch != fixture.input.BatchID {
		return "", errors.New("resolver received a different original identity")
	}
	if member == "collection/nested" {
		return fixture.moved, nil
	}
	if strings.HasPrefix(member, "collection/nested/") {
		return filepath.Join(fixture.moved, filepath.FromSlash(strings.TrimPrefix(member, "collection/nested/"))), nil
	}
	return "", nil
}

func (fixture committedLaneFixture) resolve(sourceNode, batch, member string) (string, error) {
	if sourceNode != fixture.input.SourceNodeKey || batch != fixture.input.BatchID {
		return "", errors.New("resolver received a different original identity")
	}
	if member == "collection" {
		return fixture.moved, nil
	}
	if strings.HasPrefix(member, "collection/") {
		return filepath.Join(fixture.moved, filepath.FromSlash(strings.TrimPrefix(member, "collection/"))), nil
	}
	return "", nil
}

func (fixture committedLaneFixture) assertManifestUnchanged(t *testing.T) {
	t.Helper()
	marker := filepath.Join(fixture.root, laneCustodyManifestName)
	contents, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(marker)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(contents, fixture.manifest) || !os.SameFile(info, fixture.manifestInfo) || info.Mode() != fixture.manifestInfo.Mode() || !info.ModTime().Equal(fixture.manifestInfo.ModTime()) {
		t.Fatal("replay changed the original custody manifest bytes, inode, mode or mtime")
	}
}

func TestAcceptCommittedLaneUnsplitResolverCompatibility(t *testing.T) {
	fixture := newCommittedLaneFixture(t)
	for _, resolverEnabled := range []bool{false, true} {
		legacy := &fakeLaneCatalog{}
		var catalog StorageCatalog = legacy
		if resolverEnabled {
			resolved := &resolvingLaneCatalog{resolve: func(_, _, _ string) (string, error) { return "", nil }}
			catalog = resolved
			legacy = &resolved.fakeLaneCatalog
		}
		result, err := Accept(context.Background(), catalog, fixture.input)
		if err != nil {
			t.Fatalf("resolver=%v: %v", resolverEnabled, err)
		}
		if !result.PromotionIdempotent || result.FilesCataloged != 2 || result.TotalBytes != 23 || result.DirectoriesObserved != 4 {
			t.Fatalf("resolver=%v: unexpected replay: %#v", resolverEnabled, result)
		}
		if len(legacy.inputs) != 2 {
			t.Fatalf("resolver=%v: cataloged %d members", resolverEnabled, len(legacy.inputs))
		}
		fixture.assertManifestUnchanged(t)
	}
	fixture.split(t)
	if _, err := Accept(context.Background(), &fakeLaneCatalog{}, fixture.input); err == nil {
		t.Fatal("legacy catalog accepted a split batch without a resolver")
	}
}

func TestAcceptCommittedLaneSplitReplaysOriginalMembers(t *testing.T) {
	fixture := newCommittedLaneFixture(t)
	fixture.split(t)
	for attempt := 0; attempt < 2; attempt++ {
		catalog := &resolvingLaneCatalog{resolve: fixture.resolve}
		result, err := Accept(context.Background(), catalog, fixture.input)
		if err != nil {
			t.Fatal(err)
		}
		if result.AcceptedPath != fixture.root || result.PromotionMethod != "existing_committed" || !result.PromotionIdempotent || result.FilesCataloged != 2 || result.TotalBytes != 23 || result.DirectoriesObserved != 4 {
			t.Fatalf("split replay = %#v", result)
		}
		if len(catalog.calls) != 7 {
			t.Fatalf("resolver missed original directory/file/link members: %v", catalog.calls)
		}
		for _, input := range catalog.inputs {
			expected, err := fixture.resolve(input.SourceNodeKey, input.BatchID, input.RelativeLanePath)
			if err != nil {
				t.Fatal(err)
			}
			if expected == "" {
				expected = filepath.Join(fixture.root, input.RelativeLanePath)
			}
			if input.FinalCustodyPath != expected || input.ChecksumValue == "" {
				t.Fatalf("catalog lost original identity or resolved location: %#v", input)
			}
		}
		if !fakeLaneObservationExists(catalog.observations, "collection/link", "symlink") {
			t.Fatal("split replay lost the original symlink observation")
		}
		fixture.assertManifestUnchanged(t)
	}
}

func TestCommittedCustodyNestedDirectoryMutationAuthorized(t *testing.T) {
	fixture, original, observed := newNestedCommittedLaneFixture(t)
	verifications := 0
	catalog := &directoryVerifyingLaneCatalog{
		resolvingLaneCatalog: &resolvingLaneCatalog{resolve: fixture.resolveNested},
		verify: func(sourceNode, batch, relative string, before, after time.Time) error {
			verifications++
			if sourceNode != fixture.input.SourceNodeKey || batch != fixture.input.BatchID || relative != "collection" || !before.Equal(original) || !after.Equal(observed) {
				t.Fatalf("wrong directory mutation identity/times: %s %s %s %s %s", sourceNode, batch, relative, before, after)
			}
			return nil
		},
	}
	for attempt := 0; attempt < 2; attempt++ {
		var manifest CommittedCustodyManifest
		manifest, paths, err := VerifyCommittedCustody(context.Background(), fixture.root, fixture.input.SourceNodeKey, fixture.input.BatchID, fixture.input.AcceptedDate, catalog)
		if err != nil {
			t.Fatal(err)
		}
		if paths["collection"] != filepath.Join(fixture.root, "collection") || paths["collection/nested"] != fixture.moved || paths["collection/nested/data.txt"] != filepath.Join(fixture.moved, "data.txt") {
			t.Fatalf("wrong nested member resolution: %v", paths)
		}
		var parent CommittedCustodyEntry
		for _, entry := range manifest.Entries {
			if entry.RelativePath == "collection" {
				parent = entry
			}
		}
		if !parent.ModifiedAt.Equal(original) {
			t.Fatal("returned manifest replaced the original directory mtime")
		}
		result, err := Accept(context.Background(), catalog, fixture.input)
		if err != nil {
			t.Fatal(err)
		}
		if !result.PromotionIdempotent || result.FilesCataloged != 2 || result.DirectoriesObserved != 4 || result.TotalBytes != 23 {
			t.Fatalf("nested replay = %#v", result)
		}
		fixture.assertManifestUnchanged(t)
	}
	if verifications != 4 {
		t.Fatalf("expected one qualification per verification/replay, got %d", verifications)
	}
}

func TestCommittedCustodyNestedDirectoryMutationRequiresExactVerifier(t *testing.T) {
	for _, mismatch := range []string{"missing verifier", "source node", "batch", "relative path", "original mtime", "observed mtime"} {
		t.Run(mismatch, func(t *testing.T) {
			fixture, original, observed := newNestedCommittedLaneFixture(t)
			base := &resolvingLaneCatalog{resolve: fixture.resolveNested}
			var catalog interface {
				StorageCatalog
				LaneCustodyMemberResolver
			} = base
			wrongEvidence := errors.New("no completed relocation matches this exact directory mutation")
			if mismatch != "missing verifier" {
				node, batch, relative := fixture.input.SourceNodeKey, fixture.input.BatchID, "collection"
				switch mismatch {
				case "source node":
					node = "another-node"
				case "batch":
					batch = "another-batch"
				case "relative path":
					relative = "collection/nested"
				case "original mtime":
					original = original.Add(time.Nanosecond)
				case "observed mtime":
					observed = observed.Add(time.Nanosecond)
				}
				catalog = &directoryVerifyingLaneCatalog{resolvingLaneCatalog: base, verify: func(sourceNode, batchID, member string, before, after time.Time) error {
					if sourceNode != node || batchID != batch || member != relative || !before.Equal(original) || !after.Equal(observed) {
						return wrongEvidence
					}
					return nil
				}}
			}
			_, _, err := VerifyCommittedCustody(context.Background(), fixture.root, fixture.input.SourceNodeKey, fixture.input.BatchID, fixture.input.AcceptedDate, catalog)
			if err == nil || mismatch != "missing verifier" && !errors.Is(err, wrongEvidence) {
				t.Fatalf("unqualified parent mtime passed verification or error was lost: %v", err)
			}
			if _, err := Accept(context.Background(), catalog, fixture.input); err == nil {
				t.Fatal("unqualified parent mtime passed replay")
			}
			if len(base.inputs) != 0 {
				t.Fatal("unqualified parent mtime reached catalog registration")
			}
			fixture.assertManifestUnchanged(t)
		})
	}
}

func TestCommittedCustodyDirectoryVerifierCannotWaiveOtherChanges(t *testing.T) {
	for _, mutation := range []string{"directory mode", "file mtime", "unexpected member", "post-verification drift"} {
		t.Run(mutation, func(t *testing.T) {
			fixture, _, observed := newNestedCommittedLaneFixture(t)
			parent := filepath.Join(fixture.root, "collection")
			var err error
			switch mutation {
			case "directory mode":
				err = os.Chmod(parent, 0o700)
			case "file mtime":
				err = os.Chtimes(filepath.Join(fixture.moved, "data.txt"), observed.Add(time.Hour), observed.Add(time.Hour))
			case "unexpected member":
				err = os.WriteFile(filepath.Join(parent, "extra"), []byte("not committed"), 0o640)
			}
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			catalog := &directoryVerifyingLaneCatalog{
				resolvingLaneCatalog: &resolvingLaneCatalog{resolve: fixture.resolveNested},
				verify: func(_, _, relative string, _, _ time.Time) error {
					calls++
					if relative != "collection" {
						t.Fatalf("non-parent mutation reached verifier: %q", relative)
					}
					if mutation == "post-verification drift" {
						return os.Chtimes(parent, observed.Add(time.Hour), observed.Add(time.Hour))
					}
					return nil
				},
			}
			if _, err := Accept(context.Background(), catalog, fixture.input); err == nil {
				t.Fatal("directory verifier waived another inventory change")
			}
			expectedCalls := 0
			if mutation == "file mtime" || mutation == "post-verification drift" {
				expectedCalls = 1
			}
			if calls != expectedCalls || len(catalog.inputs) != 0 {
				t.Fatalf("wrong qualification/catalog behavior: calls=%d inputs=%d", calls, len(catalog.inputs))
			}
			fixture.assertManifestUnchanged(t)
		})
	}
}

func TestVerifyCommittedCustodyStrictIdentityAndHash(t *testing.T) {
	fixture := newCommittedLaneFixture(t)
	for _, resolved := range []bool{false, true} {
		var resolver LaneCustodyMemberResolver
		if resolved {
			resolver = &directoryVerifyingLaneCatalog{
				resolvingLaneCatalog: &resolvingLaneCatalog{resolve: func(_, _, _ string) (string, error) { return "", nil }},
				verify: func(_, _, _ string, _, _ time.Time) error {
					t.Fatal("unchanged directory reached mutation verifier")
					return nil
				},
			}
		}
		manifest, paths, err := VerifyCommittedCustody(context.Background(), fixture.root, fixture.input.SourceNodeKey, fixture.input.BatchID, fixture.input.AcceptedDate, resolver)
		if err != nil || manifest.FileCount != 2 || resolved && len(paths) != 7 || !resolved && paths != nil {
			t.Fatalf("strict unsplit verification: manifest=%#v paths=%v err=%v", manifest, paths, err)
		}
		for _, identity := range [][3]string{{"another-node", fixture.input.BatchID, fixture.input.AcceptedDate}, {fixture.input.SourceNodeKey, "another-batch", fixture.input.AcceptedDate}, {fixture.input.SourceNodeKey, fixture.input.BatchID, "2026-11-06"}} {
			if _, _, err := VerifyCommittedCustody(context.Background(), fixture.root, identity[0], identity[1], identity[2], resolver); err == nil || !strings.Contains(err.Error(), "identity mismatch") {
				t.Fatalf("wrong identity was accepted or wrong error: %v", err)
			}
		}
		fixture.assertManifestUnchanged(t)
	}
	manifest, err := readLaneCustodyManifest(filepath.Join(fixture.root, laneCustodyManifestName))
	if err != nil {
		t.Fatal(err)
	}
	corrupted := bytes.ReplaceAll(fixture.manifest, []byte(manifest.InventoryHash), []byte("sha256:"+strings.Repeat("0", 64)))
	if err := os.WriteFile(filepath.Join(fixture.root, laneCustodyManifestName), corrupted, 0o600); err != nil {
		t.Fatal(err)
	}
	resolver := &resolvingLaneCatalog{resolve: func(_, _, _ string) (string, error) {
		t.Fatal("invalid inventory hash reached resolver")
		return "", nil
	}}
	if _, _, err := VerifyCommittedCustody(context.Background(), fixture.root, fixture.input.SourceNodeKey, fixture.input.BatchID, fixture.input.AcceptedDate, resolver); err == nil || !strings.Contains(err.Error(), "inventory hash mismatch") {
		t.Fatalf("invalid inventory hash was accepted or wrong error: %v", err)
	}
}

func TestAcceptCommittedLaneResolverPreservesCatalogFilters(t *testing.T) {
	fixture := newCommittedLaneFixture(t, func(root string) {
		for _, relative := range []string{"collection/._ignored/child", "collection/Thing.app/contents", "collection/Thing.app-sibling", "collection/.hidden/visible", laneReadmeControlPath} {
			pathValue := filepath.Join(root, relative)
			if err := os.MkdirAll(filepath.Dir(pathValue), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(pathValue, []byte("filter fixture"), 0o640); err != nil {
				t.Fatal(err)
			}
		}
	})
	fixture.input.SkipAppleDouble = true
	legacy := &fakeLaneCatalog{}
	before, err := Accept(context.Background(), legacy, fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	fixture.split(t)
	resolved := &resolvingLaneCatalog{resolve: fixture.resolve}
	after, err := Accept(context.Background(), resolved, fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	if after.FilesCataloged != before.FilesCataloged || after.FilesSkipped != before.FilesSkipped || after.DirectoriesObserved != before.DirectoriesObserved || after.TotalBytes != before.TotalBytes || len(resolved.inputs) != len(legacy.inputs) {
		t.Fatalf("resolved replay changed filters: before=%#v after=%#v", before, after)
	}
	expected := make(map[string]string)
	for _, input := range legacy.inputs {
		expected[input.RelativeLanePath] = input.ChecksumValue
	}
	for _, input := range resolved.inputs {
		if expected[input.RelativeLanePath] != input.ChecksumValue {
			t.Fatalf("resolved replay changed catalog membership: %#v", input)
		}
		if input.RelativeLanePath == "collection/.hidden/visible" && !input.FilesystemObservation.Hidden {
			t.Fatal("resolved replay lost original ancestor hiddenness")
		}
	}
	fixture.assertManifestUnchanged(t)
}

func TestAcceptCommittedLaneSplitRejectsTampering(t *testing.T) {
	for _, kind := range []string{"bytes", "file mode", "directory mode", "mtime", "link target", "missing", "file becomes symlink"} {
		t.Run(kind, func(t *testing.T) {
			fixture := newCommittedLaneFixture(t)
			fixture.split(t)
			payload := filepath.Join(fixture.moved, "nested/data.txt")
			info, err := os.Lstat(payload)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "bytes":
				err = os.WriteFile(payload, []byte("tampered payload"), info.Mode().Perm())
				if err == nil {
					err = os.Chtimes(payload, info.ModTime(), info.ModTime())
				}
			case "file mode":
				err = os.Chmod(payload, 0o600)
			case "directory mode":
				err = os.Chmod(fixture.moved, 0o700)
			case "mtime":
				err = os.Chtimes(payload, info.ModTime().Add(time.Hour), info.ModTime().Add(time.Hour))
			case "link target":
				link := filepath.Join(fixture.moved, "link")
				err = os.Remove(link)
				if err == nil {
					err = os.Symlink("/not-a-custody-target", link)
				}
			case "missing":
				err = os.Remove(payload)
			case "file becomes symlink":
				err = os.Remove(payload)
				if err == nil {
					err = os.Symlink(filepath.Join(fixture.root, "stays/other.txt"), payload)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			catalog := &resolvingLaneCatalog{resolve: fixture.resolve}
			if _, err := Accept(context.Background(), catalog, fixture.input); err == nil {
				t.Fatal("tampered split member was accepted")
			}
			if len(catalog.inputs) != 0 {
				t.Fatal("tampering reached catalog registration")
			}
			fixture.assertManifestUnchanged(t)
		})
	}
}

func TestAcceptCommittedLaneSplitRejectsUnexpectedMembers(t *testing.T) {
	for _, location := range []string{"batch root", "remaining directory", "moved directory", "stale moved prefix"} {
		t.Run(location, func(t *testing.T) {
			fixture := newCommittedLaneFixture(t)
			fixture.split(t)
			var extra string
			switch location {
			case "batch root":
				extra = filepath.Join(fixture.root, "extra")
			case "remaining directory":
				extra = filepath.Join(fixture.root, "stays/extra")
			case "moved directory":
				extra = filepath.Join(fixture.moved, "nested/extra")
			case "stale moved prefix":
				extra = filepath.Join(fixture.root, "collection")
			}
			if err := os.WriteFile(extra, []byte("not committed"), 0o640); err != nil {
				t.Fatal(err)
			}
			catalog := &resolvingLaneCatalog{resolve: fixture.resolve}
			if _, err := Accept(context.Background(), catalog, fixture.input); err == nil || !strings.Contains(err.Error(), "unexpected remaining member") {
				t.Fatalf("unexpected member accepted or wrong refusal: %v", err)
			}
			if len(catalog.inputs) != 0 {
				t.Fatal("unexpected member reached catalog registration")
			}
			fixture.assertManifestUnchanged(t)
		})
	}
}

func TestAcceptCommittedLaneSplitRejectsUnsafeResolvers(t *testing.T) {
	for _, kind := range []string{"relative", "unclean", "NUL", "root", "duplicate", "ancestor symlink", "terminal symlink", "error"} {
		t.Run(kind, func(t *testing.T) {
			fixture := newCommittedLaneFixture(t)
			fixture.split(t)
			payload := filepath.Join(fixture.moved, "nested/data.txt")
			unsafe := ""
			resolverErr := errors.New("location evidence is not committed")
			switch kind {
			case "relative":
				unsafe = "relative/data.txt"
			case "unclean":
				unsafe = filepath.Dir(payload) + "/../nested/data.txt"
			case "NUL":
				unsafe = payload + "\x00"
			case "root":
				unsafe = "/"
			case "duplicate":
				unsafe = filepath.Join(fixture.moved, "empty")
			case "ancestor symlink":
				alias := filepath.Join(filepath.Dir(fixture.moved), "alias")
				if err := os.Symlink(filepath.Dir(payload), alias); err != nil {
					t.Fatal(err)
				}
				unsafe = filepath.Join(alias, "data.txt")
			case "terminal symlink":
				unsafe = filepath.Join(filepath.Dir(fixture.moved), "alias-file")
				if err := os.Symlink(payload, unsafe); err != nil {
					t.Fatal(err)
				}
			}
			catalog := &resolvingLaneCatalog{resolve: func(sourceNode, batch, member string) (string, error) {
				if member == "collection/nested/data.txt" {
					if kind == "error" {
						return "", resolverErr
					}
					return unsafe, nil
				}
				return fixture.resolve(sourceNode, batch, member)
			}}
			_, err := Accept(context.Background(), catalog, fixture.input)
			if err == nil || kind == "error" && !errors.Is(err, resolverErr) {
				t.Fatalf("unsafe resolver accepted or error lost: %v", err)
			}
			if len(catalog.inputs) != 0 {
				t.Fatal("unsafe resolver reached catalog registration")
			}
			fixture.assertManifestUnchanged(t)
		})
	}
}
