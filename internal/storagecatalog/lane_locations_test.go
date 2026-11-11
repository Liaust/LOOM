package storagecatalog

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

func TestLaneResolutionPreparesRootOnceAndResolvesLocationsLive(t *testing.T) {
	db, store := newStorageCatalogFakeDB(t)
	defer db.Close()
	svc := NewService(db)
	root := "/srv/loom/storage/imports/macbook/2026-11-08/batch"
	for pass := 0; pass < 2; pass++ {
		store.expect("ORDER BY metadata->>'relative_lane_path',storage_entry_id LIMIT 1", nil,
			[]driver.Value{root + "/folder/first.md", "folder/first.md"})
		ctx, err := svc.PrepareLaneCustodyResolution(context.Background(), "macbook", "batch")
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 128; i++ {
			member := fmt.Sprintf("folder/note-%d.md", i)
			current := fmt.Sprintf("/srv/loom/storage/Records/move-%d/%s", pass, member)
			store.expect("SELECT storage.resolve_custody_path($1,$2)", func(args []driver.NamedValue) {
				requireStorageDriverArgs(t, args, LaneCustodyIdentity("macbook", "batch"), filepath.Join(root, member))
			}, []driver.Value{current})
			got, err := svc.ResolveLaneCustodyMember(ctx, "macbook", "batch", member)
			if err != nil || got != current {
				t.Fatalf("member %s: got %q, %v", member, got, err)
			}
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := svc.ResolveLaneCustodyMember(cancelled, "macbook", "batch", "folder/first.md"); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	}
	store.requireDone()
}

func TestLaneResolutionPreparationScopeAndValidation(t *testing.T) {
	db, store := newStorageCatalogFakeDB(t)
	defer db.Close()
	svc := NewService(db)
	store.expect("FROM storage.storage_entries", nil, []driver.Value{"/custody/first/note.md", "note.md"})
	ctx, err := svc.PrepareLaneCustodyResolution(context.Background(), "macbook", "first")
	if err != nil {
		t.Fatal(err)
	}
	for _, identity := range [][2]string{{"another-node", "first"}, {"macbook", "another-batch"}} {
		store.expect("FROM storage.storage_entries", func(args []driver.NamedValue) {
			requireStorageDriverArgs(t, args, identity[0], identity[1])
		}, []driver.Value{"/custody/second/note.md", "note.md"})
		store.expect("SELECT storage.resolve_custody_path", nil, []driver.Value{"/custody/second/note.md"})
		got, err := svc.ResolveLaneCustodyMember(ctx, identity[0], identity[1], "note.md")
		if err != nil || got != "/custody/second/note.md" {
			t.Fatalf("scope leaked: %q %v", got, err)
		}
	}
	otherDB, other := newStorageCatalogFakeDB(t)
	defer otherDB.Close()
	other.expect("FROM storage.storage_entries", nil, []driver.Value{"/another-catalog/note.md", "note.md"})
	other.expect("SELECT storage.resolve_custody_path", nil, []driver.Value{"/another-catalog/note.md"})
	if got, err := NewService(otherDB).ResolveLaneCustodyMember(ctx, "macbook", "first", "note.md"); err != nil || got != "/another-catalog/note.md" {
		t.Fatalf("database scope leaked: %q %v", got, err)
	}
	for _, member := range []string{".", "../note.md", "/absolute.md", "folder//note.md"} {
		if _, err := svc.ResolveLaneCustodyMember(ctx, "macbook", "first", member); err == nil {
			t.Fatalf("invalid member accepted: %q", member)
		}
	}
	if _, err := svc.PrepareLaneCustodyResolution(ctx, "", "first"); err == nil {
		t.Fatal("empty source accepted")
	}
	store.requireDone()
	other.requireDone()
}

func TestLaneResolutionPreparationMissingAndInvalidRoot(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		db, store := newStorageCatalogFakeDB(t)
		svc := NewService(db)
		if invalid {
			store.expect("FROM storage.storage_entries", nil, []driver.Value{"/custody/not-the-member", "note.md"})
		} else {
			store.expect("FROM storage.storage_entries", nil)
		}
		ctx, err := svc.PrepareLaneCustodyResolution(context.Background(), "macbook", "batch")
		if invalid {
			if err == nil || ctx != nil {
				t.Fatal("invalid root was prepared")
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				if got, err := svc.ResolveLaneCustodyMember(ctx, "macbook", "batch", "note.md"); err != nil || got != "" {
					t.Fatalf("missing root: %q %v", got, err)
				}
			}
		}
		store.requireDone()
		db.Close()
	}
}
