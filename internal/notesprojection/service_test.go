package notesprojection

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestRefreshCoalescedPreservesExplicitAndIncompleteRefresh(t *testing.T) {
	root := filepath.Join(t.TempDir(), "generated")
	defer makeProjectionWritableForCleanup(t, root)
	calls := 0
	svc := NewService(projectionSourceFunc(func(context.Context, SourceListInput) ([]SourceObject, error) {
		calls++
		return nil, nil
	}), root)
	now := fixedProjectionNow()
	svc.Now = func() time.Time { return now }
	if _, refreshed, err := svc.RefreshCoalesced(t.Context(), now); err != nil || !refreshed {
		t.Fatalf("missing manifest skipped: %t %v", refreshed, err)
	}
	first := calls
	if changed, refreshed, err := svc.RefreshCoalesced(t.Context(), now); err != nil || changed || refreshed || calls != first {
		t.Fatalf("unchanged interval not coalesced: %t %t %v", changed, refreshed, err)
	}
	if _, err := svc.Rebuild(t.Context(), RebuildInput{}); err != nil || calls != first+1 {
		t.Fatalf("explicit rebuild throttled: %v", err)
	}
	now = now.Add(time.Minute)
	if _, refreshed, err := svc.RefreshCoalesced(t.Context(), now.Add(-time.Minute)); err != nil || !refreshed {
		t.Fatalf("periodic refresh skipped: %v", err)
	}
	if err := os.Chmod(filepath.Join(root, ".loom"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".loom", "rebuild-pending"), []byte("pending"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := calls
	if _, refreshed, err := svc.RefreshCoalesced(t.Context(), now); err != nil || !refreshed || calls <= before {
		t.Fatalf("incomplete rebuild skipped: %t %v", refreshed, err)
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

type projectionSourceFunc func(context.Context, SourceListInput) ([]SourceObject, error)

func (f projectionSourceFunc) ListProjectionSources(ctx context.Context, in SourceListInput) ([]SourceObject, error) {
	return f(ctx, in)
}

func TestProjectionCompleteInventoryBeyondOldCeiling(t *testing.T) {
	root := filepath.Join(t.TempDir(), "generated")
	defer makeProjectionWritableForCleanup(t, root)
	size := int64(0)
	sources := make([]SourceObject, 6071)
	for i := range sources {
		sources[i] = SourceObject{KnowledgeObjectID: fmt.Sprintf("object_%05d", i), RootKind: RootKindBoxNotes,
			SourceNodeKey: "main", RelativePath: fmt.Sprintf("%05d.md", i), SizeBytes: &size}
	}
	// Missing payloads keep this inventory test metadata-only.
	svc := NewService(fakeProjectionSources{sources: sources}, root)
	if changed, err := svc.Refresh(t.Context()); err != nil || !changed {
		t.Fatalf("refresh beyond old ceiling: %t %v", changed, err)
	}
	manifest, ok, err := ReadManifest(root)
	if err != nil || !ok || manifest.Counts.Entries != len(sources) || manifest.Counts.Missing != len(sources) {
		t.Fatalf("full inventory not persisted: %+v %v", manifest.Counts, err)
	}
	// Explicit limits reject incomplete inventories, including dry runs, while
	// an exact bound succeeds. Response truncation remains separate and truthful.
	if _, err := svc.Rebuild(t.Context(), RebuildInput{DryRun: true, MaxObjects: 5000}); err == nil {
		t.Fatal("accepted explicit truncation")
	}
	result, err := svc.Rebuild(t.Context(), RebuildInput{DryRun: true, MaxObjects: len(sources)})
	if err != nil || result.Manifest.Counts.Entries != len(sources) || !result.ManifestEntriesTruncated || !result.ChangesTruncated {
		t.Fatalf("exact bound/result accounting: %+v %v", result.Manifest.Counts, err)
	}
}

func TestProjectionFailedInventoryPreservesGeneratedTree(t *testing.T) {
	for _, mode := range []string{"refresh", "rebuild"} {
		for _, failure := range []string{"page_error", "cancel", "duplicate", "empty_identity", "invalid_path", "explicit_limit"} {
			if mode == "refresh" && failure == "explicit_limit" {
				continue
			}
			t.Run(mode+"/"+failure, func(t *testing.T) {
				root := filepath.Join(t.TempDir(), "generated")
				defer makeProjectionWritableForCleanup(t, root)
				source := SourceObject{KnowledgeObjectID: "old", RootKind: RootKindBoxNotes, SourceNodeKey: "main",
					RelativePath: "old.md", SourcePath: writeProjectionSource(t, "old.md", "retain")}
				svc := NewService(fakeProjectionSources{sources: []SourceObject{source}}, root)
				if _, err := svc.Rebuild(t.Context(), RebuildInput{}); err != nil {
					t.Fatal(err)
				}
				before, err := os.ReadFile(manifestPath(root))
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				svc.Sources = projectionSourceFunc(func(context.Context, SourceListInput) ([]SourceObject, error) {
					next := source
					next.KnowledgeObjectID, next.RelativePath = "new", "new.md"
					switch failure {
					case "page_error":
						return []SourceObject{next}, errors.New("later page failed")
					case "cancel":
						cancel()
						return nil, nil
					case "duplicate":
						return []SourceObject{next, next}, nil
					case "empty_identity":
						next.KnowledgeObjectID = ""
						return []SourceObject{next}, nil
					case "invalid_path":
						next.RelativePath = "../outside"
						return []SourceObject{next}, nil
					default:
						return []SourceObject{source, next}, nil
					}
				})
				if mode == "refresh" {
					_, err = svc.Refresh(ctx)
				} else {
					input := RebuildInput{}
					if failure == "explicit_limit" {
						input.MaxObjects = 1
					}
					_, err = svc.Rebuild(ctx, input)
				}
				if err == nil {
					t.Fatal("accepted failed/incomplete inventory")
				}
				assertProjectionFile(t, filepath.Join(root, "nodes/main/Notes/old.md"), "retain")
				after, err := os.ReadFile(manifestPath(root))
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("manifest changed: %v", err)
				}
				if _, err := os.Stat(filepath.Join(root, ".loom", "rebuild-pending")); !os.IsNotExist(err) {
					t.Fatalf("materializer ran before complete inventory: %v", err)
				}
			})
		}
	}
}
