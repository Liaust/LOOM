package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type importsLocationsTestResolver struct {
	resolve func(string, string, string) (string, error)
	verify  func(string, string, string, time.Time, time.Time) error
}

func (r *importsLocationsTestResolver) ResolveLaneCustodyMember(_ context.Context, source, batch, relative string) (string, error) {
	return r.resolve(source, batch, relative)
}

func (r *importsLocationsTestResolver) VerifyLaneCustodyDirectoryMutation(_ context.Context, source, batch, relative string, original, observed time.Time) error {
	if r.verify == nil {
		return errors.New("no exact parent mutation evidence")
	}
	return r.verify(source, batch, relative, original, observed)
}

type importsLocationsFixture struct {
	storage, imports, batchRoot, moved string
	prefix, inventoryHash              string
	manifest                           []byte
	markerInfo                         os.FileInfo
	original, observed                 time.Time
}

func newImportsLocationsFixture(t *testing.T) importsLocationsFixture {
	t.Helper()
	storage, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fixture := importsLocationsFixture{
		storage: storage, imports: filepath.Join(storage, "imports"), prefix: "node-a/2026-11-05/batch-a",
		moved: filepath.Join(storage, "Projects", "named-content"), original: time.Date(2018, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	fixture.batchRoot = filepath.Join(fixture.imports, filepath.FromSlash(fixture.prefix))
	for _, relative := range []string{"collection/nested", "collection/empty", "stays"} {
		if err := os.MkdirAll(filepath.Join(fixture.batchRoot, relative), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for relative, content := range map[string]string{"collection/nested/payload.bin": "retained payload", "stays/other.bin": "unmoved"} {
		if err := os.WriteFile(filepath.Join(fixture.batchRoot, relative), []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("nested/payload.bin", filepath.Join(fixture.batchRoot, "collection/link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(fixture.batchRoot, "collection"), fixture.original, fixture.original); err != nil {
		t.Fatal(err)
	}
	physical, err := buildImportsBackupEvidence(context.Background(), fixture.batchRoot, ImportsBackupPolicyCanonical)
	if err != nil {
		t.Fatal(err)
	}
	entries := make([]laneCustodyManifestEntry, 0, len(physical.Entries))
	for _, entry := range physical.Entries {
		entries = append(entries, laneCustodyManifestEntry{
			RelativePath: entry.RelativePath, Kind: entry.Kind, Mode: entry.Mode, ModifiedAt: entry.ModifiedAt,
			SizeBytes: entry.SizeBytes, SHA256: entry.SHA256, LinkTarget: entry.LinkTarget,
		})
	}
	writeLaneCustodyManifestForTest(t, fixture.batchRoot, "node-a", "2026-11-05", "batch-a", entries)
	fixture.inventoryHash, err = hashLaneCustodyManifestEntries(entries)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(fixture.batchRoot, laneCustodyManifestName)
	fixture.manifest, err = os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	fixture.markerInfo, err = os.Lstat(marker)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (f *importsLocationsFixture) splitNested(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(f.moved), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(f.batchRoot, "collection/nested"), f.moved); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(f.batchRoot, "collection"))
	if err != nil {
		t.Fatal(err)
	}
	f.observed = info.ModTime().UTC()
	if f.observed.Equal(f.original) {
		t.Fatal("nested move did not change parent mtime")
	}
}

func (f importsLocationsFixture) resolve(source, batch, relative string) (string, error) {
	if source != "node-a" || batch != "batch-a" {
		return "", errors.New("wrong original batch identity")
	}
	if relative == "collection/nested" {
		return f.moved, nil
	}
	if strings.HasPrefix(relative, "collection/nested/") {
		return filepath.Join(f.moved, filepath.FromSlash(strings.TrimPrefix(relative, "collection/nested/"))), nil
	}
	return "", nil
}

func (f importsLocationsFixture) verify(source, batch, relative string, original, observed time.Time) error {
	if source != "node-a" || batch != "batch-a" || relative != "collection" || !original.Equal(f.original) || !observed.Equal(f.observed) {
		return errors.New("directory mutation has no matching completed relocation")
	}
	return nil
}

func (f importsLocationsFixture) assertUnchanged(t *testing.T) {
	t.Helper()
	marker := filepath.Join(f.batchRoot, laneCustodyManifestName)
	contents, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(marker)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(contents, f.manifest) || !os.SameFile(info, f.markerInfo) || info.Mode() != f.markerInfo.Mode() || !info.ModTime().Equal(f.markerInfo.ModTime()) {
		t.Fatal("location inspection changed original manifest evidence")
	}
}

func TestInspectImportsCustodyLocationsUnsplit(t *testing.T) {
	f := newImportsLocationsFixture(t)
	evidence, err := InspectImportsCustodyLocations(context.Background(), f.imports, f.storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Schema != ImportsCustodyLocationEvidenceSchema || evidence.ImportsInventoryHash == "" || len(evidence.Batches) != 1 {
		t.Fatalf("location evidence = %#v", evidence)
	}
	batch := evidence.Batches[0]
	if batch.SourceNodeKey != "node-a" || batch.BatchID != "batch-a" || batch.AcceptedDate != "2026-11-05" || batch.ManifestInventoryHash != f.inventoryHash || batch.Marker.RelativePath != f.prefix+"/"+laneCustodyManifestName || batch.Marker.SHA256 != sha256HexForTest(f.manifest) || len(batch.Members) != 7 {
		t.Fatalf("batch evidence = %#v", batch)
	}
	for _, member := range batch.Members {
		if !strings.HasPrefix(member.Original.RelativePath, f.prefix+"/") || member.StorageRelativePath != "imports/"+member.Original.RelativePath || member.QualifiedCurrentDirectory != nil {
			t.Fatalf("unsplit member = %#v", member)
		}
	}
	raw, err := json.Marshal(evidence)
	if err != nil || bytes.Contains(raw, []byte(f.storage)) {
		t.Fatalf("location evidence is not portable: %s, %v", raw, err)
	}
	oldEvidencePath := filepath.Join(t.TempDir(), "imports-evidence.json")
	old, err := WriteImportsBackupEvidence(context.Background(), f.imports, oldEvidencePath, ImportsBackupPolicyCanonical, ImportsSnapshotSharedStore, 3)
	if err != nil {
		t.Fatal(err)
	}
	if old.Schema != ImportsBackupEvidenceSchema {
		t.Fatal("old complete-snapshot evidence format changed")
	}
	if err := ValidateImportsBackup(context.Background(), f.imports, oldEvidencePath, ImportsBackupPolicyCanonical, ImportsSnapshotSharedStore); err != nil {
		t.Fatal(err)
	}
	f.assertUnchanged(t)
}

func TestInspectImportsCustodyLocationsSplitNestedQualifiedDirectory(t *testing.T) {
	f := newImportsLocationsFixture(t)
	f.splitNested(t)
	if _, err := InspectImportsCustodyLocations(context.Background(), f.imports, f.storage, nil); err == nil {
		t.Fatal("split layout passed without a resolver")
	}
	resolver := &importsLocationsTestResolver{resolve: f.resolve}
	if _, err := InspectImportsCustodyLocations(context.Background(), f.imports, f.storage, resolver); err == nil {
		t.Fatal("changed parent mtime passed without exact qualification")
	}
	qualifications := 0
	resolver.verify = func(source, batch, relative string, original, observed time.Time) error {
		qualifications++
		return f.verify(source, batch, relative, original, observed)
	}
	for attempt := 0; attempt < 2; attempt++ {
		evidence, err := InspectImportsCustodyLocations(context.Background(), f.imports, f.storage, resolver)
		if err != nil {
			t.Fatal(err)
		}
		members := make(map[string]ImportsCustodyLocationMember)
		for _, member := range evidence.Batches[0].Members {
			members[strings.TrimPrefix(member.Original.RelativePath, f.prefix+"/")] = member
		}
		parent := members["collection"]
		if parent.QualifiedCurrentDirectory == nil || !parent.Original.ModifiedAt.Equal(f.original) || !parent.QualifiedCurrentDirectory.ModifiedAt.Equal(f.observed) || parent.QualifiedCurrentDirectory.Mode != parent.Original.Mode {
			t.Fatalf("qualified parent metadata = %#v", parent)
		}
		payload := members["collection/nested/payload.bin"]
		if payload.StorageRelativePath != "Projects/named-content/payload.bin" || payload.Original.SHA256 != sha256HexForTest([]byte("retained payload")) || payload.QualifiedCurrentDirectory != nil {
			t.Fatalf("moved payload evidence = %#v", payload)
		}
		if members["collection/link"].Original.LinkTarget != "nested/payload.bin" || members["stays/other.bin"].StorageRelativePath != "imports/"+f.prefix+"/stays/other.bin" {
			t.Fatal("split layout lost unmoved/link provenance")
		}
		if evidence.Batches[0].Marker.SHA256 != sha256HexForTest(f.manifest) || evidence.Batches[0].ManifestInventoryHash != f.inventoryHash {
			t.Fatal("split layout rewrote original manifest/marker hashes")
		}
		f.assertUnchanged(t)
	}
	if qualifications != 2 {
		t.Fatalf("expected one exact parent qualification per read, got %d", qualifications)
	}
	if _, err := InspectImportsCustody(context.Background(), f.imports, ImportsBackupPolicyCanonical); err == nil {
		t.Fatal("old whole-Imports reader silently accepted a partial archive using live locations")
	}
}

func TestInspectImportsCustodyLocationsRejectsUnsafeAndTampered(t *testing.T) {
	for _, kind := range []string{"escape", "relative path", "collision", "ancestor symlink", "tampered payload", "extra batch member", "extra moved member", "orphan payload", "uncommitted directory", "wrong directory evidence"} {
		t.Run(kind, func(t *testing.T) {
			f := newImportsLocationsFixture(t)
			f.splitNested(t)
			resolver := &importsLocationsTestResolver{resolve: f.resolve, verify: f.verify}
			var err error
			switch kind {
			case "escape", "relative path", "collision":
				resolver.resolve = func(source, batch, relative string) (string, error) {
					if relative == "collection/nested/payload.bin" {
						switch kind {
						case "escape":
							return filepath.Join(filepath.Dir(f.storage), "escaped-payload.bin"), nil
						case "relative path":
							return "Projects/named-content/payload.bin", nil
						case "collision":
							return f.moved, nil
						}
					}
					return f.resolve(source, batch, relative)
				}
			case "ancestor symlink":
				alias := filepath.Join(f.storage, "alias")
				err = os.Symlink(f.moved, alias)
				resolver.resolve = func(source, batch, relative string) (string, error) {
					if relative == "collection/nested/payload.bin" {
						return filepath.Join(alias, "payload.bin"), nil
					}
					return f.resolve(source, batch, relative)
				}
			case "tampered payload":
				payload := filepath.Join(f.moved, "payload.bin")
				info, statErr := os.Lstat(payload)
				if statErr != nil {
					t.Fatal(statErr)
				}
				err = os.WriteFile(payload, []byte("tampered payload"), info.Mode().Perm())
				if err == nil {
					err = os.Chtimes(payload, info.ModTime(), info.ModTime())
				}
			case "extra batch member":
				err = os.WriteFile(filepath.Join(f.batchRoot, "extra"), []byte("uncommitted"), 0o640)
			case "extra moved member":
				err = os.WriteFile(filepath.Join(f.moved, "extra"), []byte("uncommitted"), 0o640)
			case "orphan payload":
				err = os.WriteFile(filepath.Join(f.imports, "orphan.bin"), []byte("uncommitted"), 0o640)
			case "uncommitted directory":
				err = os.MkdirAll(filepath.Join(f.imports, "node-a/2026-11-05/unfinished"), 0o750)
			case "wrong directory evidence":
				resolver.verify = func(source, batch, relative string, original, observed time.Time) error {
					return f.verify(source, batch, relative, original, observed.Add(time.Nanosecond))
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			evidence, err := InspectImportsCustodyLocations(context.Background(), f.imports, f.storage, resolver)
			if err == nil || evidence.Schema != "" || len(evidence.Batches) != 0 {
				t.Fatalf("unsafe layout returned successful/partial evidence: %#v, %v", evidence, err)
			}
			if kind == "escape" && !strings.Contains(err.Error(), "outside Storage root") || kind == "collision" && !strings.Contains(err.Error(), "collision") {
				t.Fatalf("unsafe path failed for wrong reason: %v", err)
			}
			f.assertUnchanged(t)
		})
	}
}
