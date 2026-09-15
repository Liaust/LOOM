package backupstrategy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

type invariantAFixture struct {
	SchemaVersion       string   `json:"schema_version"`
	HardLinkGenerations int      `json:"hard_link_generations"`
	HardLinkObjectBytes int      `json:"hard_link_object_bytes"`
	SparseLogicalBytes  int64    `json:"sparse_logical_bytes"`
	Scenarios           []string `json:"scenarios"`
}

func loadInvariantAFixture(t *testing.T) invariantAFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "invariant_a.json"))
	if err != nil {
		t.Fatalf("read Invariant A fixture: %v", err)
	}
	var fixture invariantAFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode Invariant A fixture: %v", err)
	}
	return fixture
}

func TestInvariantAAcceptanceFixtureIsComplete(t *testing.T) {
	fixture := loadInvariantAFixture(t)
	want := []string{
		"active_staging",
		"hard_linked_generations",
		"inventory_digest_change",
		"same_content_distinct_inodes",
		"sparse_file",
		"symlink_or_path_escape",
		"unknown_top_level_entry",
	}
	got := append([]string(nil), fixture.Scenarios...)
	sort.Strings(got)
	if fixture.SchemaVersion != "loom.backupstrategy.invariant_a.v1" || !reflect.DeepEqual(got, want) {
		t.Fatalf("Invariant A fixture changed: schema=%q scenarios=%v", fixture.SchemaVersion, got)
	}
	if fixture.HardLinkGenerations != 30 || fixture.HardLinkObjectBytes < 1<<20 || fixture.SparseLogicalBytes < 8<<20 {
		t.Fatalf("Invariant A scale weakened: %#v", fixture)
	}
}

func TestInventoryCountsThirtyHardLinkedGenerationsOnce(t *testing.T) {
	fixture := loadInvariantAFixture(t)
	root := t.TempDir()
	store := filepath.Join(root, ".imports-objects")
	if err := os.Mkdir(store, 0o750); err != nil {
		t.Fatal(err)
	}
	objectPath := filepath.Join(store, "object.bin")
	if err := os.WriteFile(objectPath, bytes.Repeat([]byte("h"), fixture.HardLinkObjectBytes), 0o640); err != nil {
		t.Fatal(err)
	}
	specs := []ComponentSpec{{RelativePath: ".imports-objects", Class: ComponentSharedStore}}
	for index := 0; index < fixture.HardLinkGenerations; index++ {
		name := fmt.Sprintf("generation-%02d", index)
		if err := os.Mkdir(filepath.Join(root, name), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(objectPath, filepath.Join(root, name, "imports.bin")); err != nil {
			t.Fatal(err)
		}
		specs = append(specs, ComponentSpec{RelativePath: name, Class: ComponentSuccessfulGeneration})
	}

	report, err := (Service{}).Inventory(context.Background(), Config{LocalRoot: root, Components: specs})
	if err != nil {
		t.Fatalf("inventory hard-link fixture: %v", err)
	}
	object := entryByPath(t, report, ".imports-objects/object.bin")
	if object.LinkCount != uint64(fixture.HardLinkGenerations+1) || object.AllocatedBytes == 0 {
		t.Fatalf("shared object identity = %#v", object)
	}
	if report.LocalTotals.LogicalBytes < uint64(fixture.HardLinkGenerations+1)*uint64(fixture.HardLinkObjectBytes) {
		t.Fatalf("logical bytes = %d, want at least %d path bytes", report.LocalTotals.LogicalBytes, (fixture.HardLinkGenerations+1)*fixture.HardLinkObjectBytes)
	}
	if report.LocalTotals.SharedBytes < object.AllocatedBytes {
		t.Fatalf("shared bytes = %d, want object allocation %d", report.LocalTotals.SharedBytes, object.AllocatedBytes)
	}
	if report.LocalTotals.AllocatedBytes >= object.AllocatedBytes*2 {
		t.Fatalf("allocated bytes multiplied hard-link object: total=%d object=%d", report.LocalTotals.AllocatedBytes, object.AllocatedBytes)
	}
	if report.LocalTotals.PotentialReclaimableBytes >= object.AllocatedBytes {
		t.Fatalf("one generation falsely claims shared object reclamation: potential=%d object=%d", report.LocalTotals.PotentialReclaimableBytes, object.AllocatedBytes)
	}
	for index := 0; index < fixture.HardLinkGenerations; index++ {
		component := componentByPath(t, report, fmt.Sprintf("generation-%02d", index))
		if component.Accounting.SharedBytes < object.AllocatedBytes {
			t.Fatalf("generation %d shared bytes = %d, want at least %d", index, component.Accounting.SharedBytes, object.AllocatedBytes)
		}
	}
}

func TestInventoryCountsSameContentWithDistinctInodesTwice(t *testing.T) {
	fixture := loadInvariantAFixture(t)
	root := t.TempDir()
	payload := bytes.Repeat([]byte("c"), fixture.HardLinkObjectBytes)
	for _, name := range []string{"generation-a", "generation-b"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name, "payload.bin"), payload, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	report, err := (Service{}).Inventory(context.Background(), Config{
		LocalRoot: root,
		Components: []ComponentSpec{
			{RelativePath: "generation-a", Class: ComponentSuccessfulGeneration},
			{RelativePath: "generation-b", Class: ComponentSuccessfulGeneration},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	a := entryByPath(t, report, "generation-a/payload.bin")
	b := entryByPath(t, report, "generation-b/payload.bin")
	if a.DeviceID == b.DeviceID && a.Inode == b.Inode {
		t.Fatal("fixture unexpectedly created one inode")
	}
	if a.AllocatedBytes == 0 || b.AllocatedBytes == 0 || report.LocalTotals.AllocatedBytes < a.AllocatedBytes+b.AllocatedBytes {
		t.Fatalf("distinct allocations not both counted: a=%#v b=%#v totals=%#v", a, b, report.LocalTotals)
	}
	if report.LocalTotals.SharedBytes != 0 {
		t.Fatalf("same content was treated as shared inode allocation: %d", report.LocalTotals.SharedBytes)
	}
}

func TestInventorySeparatesSparseLogicalAndAllocatedBytes(t *testing.T) {
	fixture := loadInvariantAFixture(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "generation"), 0o750); err != nil {
		t.Fatal(err)
	}
	pathValue := filepath.Join(root, "generation", "sparse.bin")
	file, err := os.OpenFile(pathValue, os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(fixture.SparseLogicalBytes-1, io.SeekStart); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if _, err := file.Write([]byte{1}); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := (Service{}).Inventory(context.Background(), Config{
		LocalRoot:  root,
		Components: []ComponentSpec{{RelativePath: "generation", Class: ComponentSuccessfulGeneration}},
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := entryByPath(t, report, "generation/sparse.bin")
	if entry.LogicalBytes != uint64(fixture.SparseLogicalBytes) {
		t.Fatalf("sparse logical bytes = %d, want %d", entry.LogicalBytes, fixture.SparseLogicalBytes)
	}
	if entry.AllocatedBytes >= entry.LogicalBytes {
		t.Fatalf("sparse allocation was not distinguished: %#v", entry)
	}
}

func TestInventoryFailsComponentClosedWithoutFollowingSymlinkOrEscape(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"generation", ".imports-objects"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	external := t.TempDir()
	externalPayload := filepath.Join(external, "outside.bin")
	if err := os.WriteFile(externalPayload, bytes.Repeat([]byte("x"), 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, external)
	if err := os.Symlink(externalPayload, filepath.Join(root, "generation", "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalPayload, filepath.Join(root, ".imports-objects", "escape")); err != nil {
		t.Fatal(err)
	}

	report, err := (Service{}).Inventory(context.Background(), Config{
		LocalRoot: root,
		Components: []ComponentSpec{
			{RelativePath: "generation", Class: ComponentSuccessfulGeneration},
			{RelativePath: ".imports-objects", Class: ComponentSharedStore},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"generation", ".imports-objects"} {
		component := componentByPath(t, report, name)
		if component.Safe || component.RestorePoint || component.ReclaimPosture != ReclaimBlocked {
			t.Fatalf("symlinked component %s did not fail closed: %#v", name, component)
		}
		assertFinding(t, component, FindingSymlink)
	}
	if report.CleanupEligible {
		t.Fatal("unsafe component left cleanup eligible")
	}
	if report.LocalTotals.LogicalBytes >= 1<<20 {
		t.Fatalf("external payload appears to have been followed: logical=%d", report.LocalTotals.LogicalBytes)
	}
	if after := snapshotTree(t, external); !reflect.DeepEqual(after, before) {
		t.Fatalf("external target changed: before=%#v after=%#v", before, after)
	}

	_, err = (Service{}).Inventory(context.Background(), Config{
		LocalRoot:  root,
		Components: []ComponentSpec{{RelativePath: filepath.Join("..", filepath.Base(external)), Class: ComponentSuccessfulGeneration}},
	})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("escaped component error = %v, want ErrInvalidConfig", err)
	}

	linkedRoot := filepath.Join(t.TempDir(), "linked-root")
	if err := os.Symlink(root, linkedRoot); err != nil {
		t.Fatal(err)
	}
	_, err = (Service{}).Inventory(context.Background(), Config{LocalRoot: linkedRoot})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("symlinked root error = %v, want ErrInvalidConfig", err)
	}
}

func TestInventoryClassifiesUnknownTopLevelAndBlocksCleanup(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"known", "mystery"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name, "payload.bin"), bytes.Repeat([]byte(name), 4096), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	report, err := (Service{}).Inventory(context.Background(), Config{
		LocalRoot:  root,
		Components: []ComponentSpec{{RelativePath: "known", Class: ComponentSuccessfulGeneration}},
	})
	if err != nil {
		t.Fatal(err)
	}
	unknown := componentByPath(t, report, "mystery")
	if unknown.Class != ComponentUnknown || unknown.ReclaimPosture != ReclaimBlocked || report.CleanupEligible {
		t.Fatalf("unknown posture is not fail closed: component=%#v report=%#v", unknown, report)
	}
	if report.LocalTotals.UnknownBytes == 0 {
		t.Fatal("unknown allocation was not distinguished")
	}
	if !contains(report.CleanupBlockers, BlockerUnknownEntry) {
		t.Fatalf("cleanup blockers = %v", report.CleanupBlockers)
	}
}

func TestInventoryClassifiesActiveStagingAsIncomplete(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".staging-active"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".staging-active", "partial.bin"), bytes.Repeat([]byte("s"), 4096), 0o640); err != nil {
		t.Fatal(err)
	}
	report, err := (Service{}).Inventory(context.Background(), Config{
		LocalRoot:  root,
		Components: []ComponentSpec{{RelativePath: ".staging-active", Class: ComponentIncompleteStaging}},
	})
	if err != nil {
		t.Fatal(err)
	}
	staging := componentByPath(t, report, ".staging-active")
	if staging.Class != ComponentIncompleteStaging || staging.RestorePoint || staging.ReclaimPosture != ReclaimBlocked {
		t.Fatalf("active staging was represented as complete: %#v", staging)
	}
	if report.LocalTotals.StagingBytes == 0 || report.CleanupEligible {
		t.Fatalf("staging posture not reflected in totals: %#v", report)
	}
	if !contains(report.CleanupBlockers, BlockerActiveStaging) {
		t.Fatalf("cleanup blockers = %v", report.CleanupBlockers)
	}
}

func TestInventoryDigestRejectsChangedInventory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "generation"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "generation", "one.bin"), []byte("one"), 0o640); err != nil {
		t.Fatal(err)
	}
	config := Config{LocalRoot: root, Components: []ComponentSpec{{RelativePath: "generation", Class: ComponentSuccessfulGeneration}}}
	service := Service{}
	first, err := service.Inventory(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RequireDigest(context.Background(), config, first.Digest); err != nil {
		t.Fatalf("unchanged inventory rejected: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "generation", "two.bin"), []byte("two"), 0o640); err != nil {
		t.Fatal(err)
	}
	changed, err := service.RequireDigest(context.Background(), config, first.Digest)
	if !errors.Is(err, ErrInventoryChanged) {
		t.Fatalf("changed digest error = %v, want ErrInventoryChanged", err)
	}
	if changed.Digest == first.Digest {
		t.Fatalf("changed inventory retained digest %q", first.Digest)
	}
}

func TestInventoryDigestDetectsSameSizeRewriteWithRestoredMtime(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "generation"), 0o750); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(root, "generation", "payload.bin")
	if err := os.WriteFile(payload, []byte("before"), 0o640); err != nil {
		t.Fatal(err)
	}
	fixedTime := time.Unix(1_700_000_000, 123_000_000)
	if err := os.Chtimes(payload, fixedTime, fixedTime); err != nil {
		t.Fatal(err)
	}
	config := Config{LocalRoot: root, Components: []ComponentSpec{{RelativePath: "generation", Class: ComponentSuccessfulGeneration}}}
	service := Service{}
	first, err := service.Inventory(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	firstEntry := entryByPath(t, first, "generation/payload.bin")
	if firstEntry.ChangedUnixNano == 0 {
		t.Skip("filesystem does not expose change time")
	}
	time.Sleep(5 * time.Millisecond)
	if err := os.WriteFile(payload, []byte("after!"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(payload, fixedTime, fixedTime); err != nil {
		t.Fatal(err)
	}
	changed, err := service.RequireDigest(context.Background(), config, first.Digest)
	if !errors.Is(err, ErrInventoryChanged) {
		t.Fatalf("same-size rewrite with restored mtime error = %v, want ErrInventoryChanged", err)
	}
	if entryByPath(t, changed, "generation/payload.bin").ChangedUnixNano == firstEntry.ChangedUnixNano {
		t.Fatal("change time did not move for rewritten fixture")
	}
}

func TestInventoryDistinguishesProtectedAndNonRestoreComponents(t *testing.T) {
	root := t.TempDir()
	specs := []ComponentSpec{
		{RelativePath: "milestone", Class: ComponentProtectedMilestone},
		{RelativePath: "failed", Class: ComponentImmutableFailedEvidence},
		{RelativePath: "operational", Class: ComponentOperationalPackage},
	}
	for _, spec := range specs {
		if err := os.Mkdir(filepath.Join(root, spec.RelativePath), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, spec.RelativePath, "evidence.bin"), bytes.Repeat([]byte(spec.RelativePath), 4096), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	report, err := (Service{}).Inventory(context.Background(), Config{LocalRoot: root, Components: specs})
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range specs {
		component := componentByPath(t, report, spec.RelativePath)
		if component.ReclaimPosture != ReclaimProtected || component.Accounting.ProtectedBytes == 0 {
			t.Fatalf("protected component %s = %#v", spec.RelativePath, component)
		}
		wantRestore := spec.Class == ComponentProtectedMilestone
		if component.RestorePoint != wantRestore {
			t.Fatalf("component %s restore_point=%t, want %t", spec.RelativePath, component.RestorePoint, wantRestore)
		}
	}
	if report.LocalTotals.ProtectedBytes == 0 || report.LocalTotals.ReclaimableBytes != 0 {
		t.Fatalf("protected totals = %#v", report.LocalTotals)
	}
}

func TestInventoryRemoteAdapterIsDeclaredAndDeterministic(t *testing.T) {
	root := t.TempDir()
	remote := RemoteInventory{
		RepositoryID: "repository-1",
		Accounting: ByteAccounting{
			LogicalBytes:   1_000,
			AllocatedBytes: 100,
			UniqueBytes:    40,
			SharedBytes:    60,
		},
		Archives: []RemoteArchiveSummary{
			{Reference: "archive-b", Class: "user_data", LogicalBytes: 600, StoredBytes: 60, Protected: true},
			{Reference: "archive-a", Class: "milestone", LogicalBytes: 400, StoredBytes: 40, Protected: true},
		},
	}
	beforeArchives := append([]RemoteArchiveSummary(nil), remote.Archives...)
	first, err := (Service{Remote: staticRemoteSource{inventory: remote}}).Inventory(context.Background(), Config{LocalRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(remote.Archives, beforeArchives) {
		t.Fatalf("inventory mutated adapter-owned summaries: before=%#v after=%#v", beforeArchives, remote.Archives)
	}
	remote.Archives[0], remote.Archives[1] = remote.Archives[1], remote.Archives[0]
	second, err := (Service{Remote: staticRemoteSource{inventory: remote}}).Inventory(context.Background(), Config{LocalRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if first.Remote == nil || len(first.Remote.Archives) != 2 || first.Remote.Archives[0].Reference != "archive-a" {
		t.Fatalf("remote summaries are not sorted: %#v", first.Remote)
	}
	if first.Digest != second.Digest {
		t.Fatalf("adapter ordering changed digest: %s != %s", first.Digest, second.Digest)
	}
}

func TestInventoryIsReadOnlyAndDeterministic(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "generation"), 0o751); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "generation", "payload.bin"), []byte("unchanged"), 0o640); err != nil {
		t.Fatal(err)
	}
	config := Config{LocalRoot: root, Components: []ComponentSpec{{RelativePath: "generation", Class: ComponentSuccessfulGeneration}}}
	before := snapshotTree(t, root)
	first, err := (Service{}).Inventory(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	second, err := (Service{}).Inventory(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	after := snapshotTree(t, root)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("inventory mutated tree: before=%#v after=%#v", before, after)
	}
	if first.Digest != second.Digest || !reflect.DeepEqual(first, second) {
		t.Fatalf("inventory is not deterministic:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if first.LocalRootEntry.RelativePath != "." || first.LocalTotals.AllocatedBytes < first.LocalRootEntry.AllocatedBytes || first.LocalTotals.ProtectedBytes < first.LocalRootEntry.AllocatedBytes {
		t.Fatalf("local root overhead is not accounted as retained: root=%#v totals=%#v", first.LocalRootEntry, first.LocalTotals)
	}
}

type staticRemoteSource struct {
	inventory RemoteInventory
}

func (source staticRemoteSource) Inventory(context.Context) (RemoteInventory, error) {
	return source.inventory, nil
}

func componentByPath(t *testing.T, report Inventory, relative string) ComponentInventory {
	t.Helper()
	for _, component := range report.Components {
		if component.RelativePath == filepath.ToSlash(relative) {
			return component
		}
	}
	t.Fatalf("component %q not found in %#v", relative, report.Components)
	return ComponentInventory{}
}

func entryByPath(t *testing.T, report Inventory, relative string) EntryInventory {
	t.Helper()
	for _, component := range report.Components {
		for _, entry := range component.Entries {
			if entry.RelativePath == filepath.ToSlash(relative) {
				return entry
			}
		}
	}
	t.Fatalf("entry %q not found", relative)
	return EntryInventory{}
}

func assertFinding(t *testing.T, component ComponentInventory, code FindingCode) {
	t.Helper()
	for _, finding := range component.Findings {
		if finding.Code == code {
			return
		}
	}
	t.Fatalf("finding %q absent from %#v", code, component.Findings)
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type treeSnapshotEntry struct {
	Mode       fs.FileMode
	Size       int64
	ModifiedNS int64
	LinkTarget string
	Digest     string
}

func snapshotTree(t *testing.T, root string) map[string]treeSnapshotEntry {
	t.Helper()
	snapshot := make(map[string]treeSnapshotEntry)
	err := filepath.WalkDir(root, func(pathValue string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(pathValue)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, pathValue)
		if err != nil {
			return err
		}
		item := treeSnapshotEntry{Mode: info.Mode(), Size: info.Size(), ModifiedNS: info.ModTime().UnixNano()}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			item.LinkTarget, err = os.Readlink(pathValue)
		case info.Mode().IsRegular():
			raw, readErr := os.ReadFile(pathValue)
			if readErr != nil {
				return readErr
			}
			digest := sha256.Sum256(raw)
			item.Digest = hex.EncodeToString(digest[:])
		}
		if err != nil {
			return err
		}
		snapshot[filepath.ToSlash(relative)] = item
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return snapshot
}

func TestInventoryRejectsInvalidRemoteAccounting(t *testing.T) {
	root := t.TempDir()
	_, err := (Service{Remote: staticRemoteSource{inventory: RemoteInventory{
		RepositoryID: "repository-1",
		Accounting:   ByteAccounting{AllocatedBytes: 10, UniqueBytes: 9, SharedBytes: 9},
	}}}).Inventory(context.Background(), Config{LocalRoot: root})
	if !errors.Is(err, ErrInvalidRemoteInventory) || !strings.Contains(err.Error(), "partition") {
		t.Fatalf("invalid remote accounting error = %v", err)
	}
}
