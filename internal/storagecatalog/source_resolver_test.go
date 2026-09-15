package storagecatalog

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestPreferredReadablePhysicalRefsFiltersAndOrdersSources(t *testing.T) {
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	refs := []PhysicalRef{
		{StoragePhysicalRefID: "retention", RefKind: PhysicalRefKindRetentionPayload, URI: "/retained", Status: PhysicalRefStatusAvailable, UpdatedAt: now.Add(3 * time.Minute)},
		{StoragePhysicalRefID: "missing-uri", RefKind: PhysicalRefKindLocalPath, Status: PhysicalRefStatusAvailable, UpdatedAt: now.Add(4 * time.Minute)},
		{StoragePhysicalRefID: "unavailable", RefKind: PhysicalRefKindLocalPath, URI: "/old", Status: "missing", UpdatedAt: now.Add(5 * time.Minute)},
		{StoragePhysicalRefID: "backup", RefKind: PhysicalRefKindBackupArtifact, URI: "/backup.tar", Status: PhysicalRefStatusAvailable, UpdatedAt: now.Add(2 * time.Minute)},
		{StoragePhysicalRefID: "local", RefKind: PhysicalRefKindLocalPath, URI: "/source.md", Status: PhysicalRefStatusAvailable, UpdatedAt: now},
	}
	got := PreferredReadablePhysicalRefs(refs)
	if len(got) != 3 {
		t.Fatalf("refs = %#v, want 3 readable available refs", got)
	}
	for index, want := range []string{"local", "retention", "backup"} {
		if got[index].StoragePhysicalRefID != want {
			t.Fatalf("ref %d = %s, want %s; all=%#v", index, got[index].StoragePhysicalRefID, want, got)
		}
	}
}

func TestBackupArtifactContentMemberDefaultsToContent(t *testing.T) {
	if got := BackupArtifactContentMember(PhysicalRef{}); got != "content" {
		t.Fatalf("default content member = %q", got)
	}
	metadata, _ := json.Marshal(map[string]string{"content_member": "notes/source.md"})
	if got := BackupArtifactContentMember(PhysicalRef{Metadata: metadata}); got != "notes/source.md" {
		t.Fatalf("content member = %q", got)
	}
}

func TestClassifyPhysicalRefCoversEveryKind(t *testing.T) {
	want := map[string]string{
		PhysicalRefKindLocalPath:        PhysicalRefClassCurrentSource,
		PhysicalRefKindExternalURI:      PhysicalRefClassCurrentSource,
		PhysicalRefKindObjectBlob:       PhysicalRefClassRetentionCopy,
		PhysicalRefKindDropzoneFile:     PhysicalRefClassCanonicalCustody,
		PhysicalRefKindLaneFile:         PhysicalRefClassCanonicalCustody,
		PhysicalRefKindRetentionPayload: PhysicalRefClassRetentionCopy,
		PhysicalRefKindArchiveFile:      PhysicalRefClassArchiveCopy,
		PhysicalRefKindBackupArtifact:   PhysicalRefClassBackupArtifact,
		PhysicalRefKindCloudObject:      PhysicalRefClassCloudCopy,
	}
	if len(want) != len(validPhysicalRefKinds) {
		t.Fatalf("classification fixture covers %d kinds, catalog has %d", len(want), len(validPhysicalRefKinds))
	}
	for kind := range validPhysicalRefKinds {
		got, ok := ClassifyPhysicalRef(PhysicalRef{RefKind: kind})
		if !ok || got != want[kind] {
			t.Errorf("ClassifyPhysicalRef(%q) = %q, %t; want %q, true", kind, got, ok, want[kind])
		}
	}
}

func TestObjectBlobIsInternalProtectionCopyAndReadableFallback(t *testing.T) {
	entry := Entry{StorageClass: StorageClassMainDocument, SourceArea: SourceAreaMainDocuments}
	object := PhysicalRef{StoragePhysicalRefID: "object", RefKind: PhysicalRefKindObjectBlob, URI: "/var/lib/loom/object-store/sha256/aa", Status: PhysicalRefStatusAvailable}
	if class, ok := ClassifyPhysicalRef(object); !ok || class != PhysicalRefClassRetentionCopy {
		t.Fatalf("object blob class = %q, %t", class, ok)
	}
	if !PhysicalRefRetainsPayload(entry, object) {
		t.Fatal("object blob did not count as a deletion-protection payload")
	}
	if !IsFilesystemPhysicalRefKind(PhysicalRefKindObjectBlob) {
		t.Fatal("object blob payload is not available to internal fallback readers")
	}
	resolved := ResolvePhysicalRefs([]PhysicalRef{
		object,
		{StoragePhysicalRefID: "source", RefKind: PhysicalRefKindLocalPath, URI: "/srv/loom/box/file", Status: PhysicalRefStatusAvailable},
	}, ResolvePhysicalRefOptions{LocalOnly: true})
	if len(resolved) != 2 || resolved[0].StoragePhysicalRefID != "source" || resolved[1].StoragePhysicalRefID != "object" {
		t.Fatalf("resolved object/source refs = %#v", resolved)
	}
	excluded := ResolvePhysicalRefs([]PhysicalRef{object}, ResolvePhysicalRefOptions{LocalOnly: true, ExcludeKinds: []string{PhysicalRefKindObjectBlob}})
	if len(excluded) != 0 {
		t.Fatalf("operation-specific exclusion returned %#v", excluded)
	}
}

func TestResolvePhysicalRefsFiltersAndOrdersWithoutFilesystemAccess(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "does-not-exist")
	refs := []PhysicalRef{
		{StoragePhysicalRefID: "cloud", RefKind: PhysicalRefKindCloudObject, URI: "s3://bucket/key", Status: PhysicalRefStatusAvailable},
		{StoragePhysicalRefID: "failed", RefKind: PhysicalRefKindLocalPath, URI: missingPath, Status: PhysicalRefStatusFailed},
		{StoragePhysicalRefID: "archive", RefKind: PhysicalRefKindArchiveFile, URI: missingPath, Status: PhysicalRefStatusAvailable},
		{StoragePhysicalRefID: "source", RefKind: PhysicalRefKindLocalPath, URI: missingPath, Status: PhysicalRefStatusAvailable},
		{StoragePhysicalRefID: "custody", RefKind: PhysicalRefKindLaneFile, URI: missingPath, Status: PhysicalRefStatusAvailable},
	}
	got := ResolvePhysicalRefs(refs, ResolvePhysicalRefOptions{})
	if len(got) != 4 {
		t.Fatalf("resolved refs = %#v", got)
	}
	for index, id := range []string{"source", "custody", "archive", "cloud"} {
		if got[index].StoragePhysicalRefID != id {
			t.Fatalf("resolved[%d] = %q, want %q", index, got[index].StoragePhysicalRefID, id)
		}
	}
	local := ResolvePhysicalRefs(refs, ResolvePhysicalRefOptions{LocalOnly: true})
	if len(local) != 3 || local[0].StoragePhysicalRefID != "source" {
		t.Fatalf("local refs = %#v", local)
	}
}

func TestPhysicalRefLocalPathRejectsRemoteAndFileHosts(t *testing.T) {
	if _, ok := PhysicalRefLocalPath(PhysicalRef{RefKind: PhysicalRefKindCloudObject, URI: "/tmp/not-cloud"}); ok {
		t.Fatal("cloud ref resolved as local")
	}
	if _, ok := PhysicalRefLocalPath(PhysicalRef{RefKind: PhysicalRefKindLocalPath, URI: "file://remote/tmp/value"}); ok {
		t.Fatal("remote file URI resolved as local")
	}
	got, ok := PhysicalRefLocalPath(PhysicalRef{RefKind: PhysicalRefKindLocalPath, URI: "file:///tmp/value"})
	if !ok || got != "/tmp/value" {
		t.Fatalf("local file URI = %q, %t", got, ok)
	}
	got, ok = PhysicalRefLocalPath(PhysicalRef{RefKind: PhysicalRefKindLocalPath, URI: "/tmp/value "})
	if !ok || got != "/tmp/value " {
		t.Fatalf("path whitespace was not preserved: %q, %t", got, ok)
	}
}
