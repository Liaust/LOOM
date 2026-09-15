package notesprojection

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestStatusReadsExistingManifest(t *testing.T) {
	root := filepath.Join(t.TempDir(), DefaultDirectoryName)
	defer makeProjectionWritableForCleanup(t, root)
	source := writeProjectionSource(t, "daily.md", "box note")
	svc := NewService(fakeProjectionSources{sources: []SourceObject{{
		KnowledgeObjectID: "knowledge_object_box",
		NotesSourceRootID: "notes_source_root_box",
		RootKind:          RootKindBoxNotes,
		SourceNodeKey:     "main",
		SourcePath:        source,
		RelativePath:      "daily.md",
	}}}, root)
	svc.Now = fixedProjectionNow
	if _, err := svc.Rebuild(context.Background(), RebuildInput{}); err != nil {
		t.Fatalf("Rebuild returned error: %v", err)
	}

	status, err := svc.Status(context.Background())
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if !status.Exists || !status.ReadOnly || status.RawWritesSupported {
		t.Fatalf("unexpected status flags: %#v", status)
	}
	if status.LastRebuildAt == nil || status.Counts.Materialized != 1 {
		t.Fatalf("status did not read manifest: %#v", status)
	}
}

func TestStatusForMissingProjectionRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), DefaultDirectoryName)
	svc := NewService(nil, root)
	status, err := svc.Status(context.Background())
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if status.Exists || !status.ReadOnly || status.ManifestPath != filepath.Join(root, ".loom", "manifest.json") {
		t.Fatalf("missing root status = %#v", status)
	}
}

func TestRebuildRequiresSourceProvider(t *testing.T) {
	root := filepath.Join(t.TempDir(), DefaultDirectoryName)
	svc := NewService(nil, root)
	if _, err := svc.Rebuild(context.Background(), RebuildInput{}); err == nil {
		t.Fatal("Rebuild returned nil error without source provider")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("projection root stat error = %v, want not exist", err)
	}
}
