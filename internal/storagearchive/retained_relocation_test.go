package storagearchive

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestRetainedOperatorChecksPreparedStateUnderExecutorLock(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("explicit local root executor check")
	}
	roots, p := retainedMoveFixture(t)
	if err := os.Mkdir(filepath.Join(roots.StorageRoot, ".loom"), 0700); err != nil {
		t.Fatal(err)
	}
	p.Schema, p.ID, p.CustodyID = RetainedRelocationSchema, "retained_check", "legacy-import:check"
	p.Digest = relocationDigest(p)
	called := false
	confirm := func(context.Context) error {
		called = true
		if release, err := lockRetainedExecutor(roots); err == nil {
			release()
			t.Fatal("state check did not hold executor lock")
		}
		return fmt.Errorf("cancelled in journal")
	}
	if err := ExecuteRetainedOperator(context.Background(), roots, p, p.Digest, confirm); err == nil || !called {
		t.Fatal("stale external request was admitted")
	}
	if _, err := os.Stat(filepath.Join(p.Source.AbsolutePath, "paper.md")); err != nil {
		t.Fatal("source lost after cancellation")
	}
	if _, err := os.Stat(p.Destination.AbsolutePath); !os.IsNotExist(err) {
		t.Fatal("stale request moved payload")
	}
}

func retainedMoveFixture(t *testing.T) (TrustedWorkspaceRoots, RetainedRelocationPlan) {
	t.Helper()
	storage := t.TempDir()
	roots := TrustedWorkspaceRoots{StorageRoot: storage}
	for _, relative := range []string{"imports/batch/research ", "Projects"} {
		if err := os.MkdirAll(filepath.Join(storage, relative), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(storage, "imports/batch/research /paper.md"), []byte("preserve exact source"), 0640); err != nil {
		t.Fatal(err)
	}
	source, _ := retainedRef(roots, "imports/batch/research ")
	destination, _ := retainedRef(roots, "Projects/research")
	inventory, err := inventoryRetained(context.Background(), roots, source)
	if err != nil {
		t.Fatal(err)
	}
	sourceChain, err := openHeldDirectoryChain(storage, "imports/batch", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sourceChain.Close()
	destChain, err := openHeldDirectoryChain(storage, "Projects", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer destChain.Close()
	return roots, RetainedRelocationPlan{Source: source, Destination: destination, Inventory: inventory, SourceAncestors: sourceChain.evidence, DestinationAncestors: destChain.evidence}
}

func TestRetainedCancellationProvesNoEffectAndPreservesNewerBytes(t *testing.T) {
	for _, scenario := range []string{"unchanged", "edited", "moved", "collision", "substituted", "ancestor"} {
		t.Run(scenario, func(t *testing.T) {
			roots, p := retainedMoveFixture(t)
			switch scenario {
			case "edited":
				if err := os.WriteFile(filepath.Join(p.Source.AbsolutePath, "paper.md"), []byte("newer user edit"), 0640); err != nil {
					t.Fatal(err)
				}
			case "moved":
				if _, err := moveRetained(context.Background(), roots, p); err != nil {
					t.Fatal(err)
				}
			case "collision":
				if err := os.Mkdir(p.Destination.AbsolutePath, 0700); err != nil {
					t.Fatal(err)
				}
			case "substituted":
				if err := os.Rename(p.Source.AbsolutePath, p.Source.AbsolutePath+"-preserved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(p.Source.AbsolutePath, 0700); err != nil {
					t.Fatal(err)
				}
			case "ancestor":
				parent := filepath.Dir(p.Destination.AbsolutePath)
				if err := os.Rename(parent, parent+"-preserved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(parent, 0700); err != nil {
					t.Fatal(err)
				}
			}
			_, err := proveRetainedUnmoved(context.Background(), roots, p)
			want := scenario == "unchanged" || scenario == "edited"
			if (err == nil) != want {
				t.Fatalf("cancel proof=%v want success=%v", err, want)
			}
			if scenario == "edited" {
				data, err := os.ReadFile(filepath.Join(p.Source.AbsolutePath, "paper.md"))
				if err != nil || string(data) != "newer user edit" {
					t.Fatal("newer content lost")
				}
			}
		})
	}
}

func TestRetainedExecutorLockRefusesOverlapAndUnsafeLock(t *testing.T) {
	roots, _ := retainedMoveFixture(t)
	if err := os.Mkdir(filepath.Join(roots.StorageRoot, ".loom"), 0700); err != nil {
		t.Fatal(err)
	}
	release, err := lockRetainedExecutor(roots)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := lockRetainedExecutor(roots); err == nil {
		other()
		t.Fatal("executor overlap admitted")
	}
	release()
	other, err := lockRetainedExecutor(roots)
	if err != nil {
		t.Fatal(err)
	}
	other()
	lock := filepath.Join(roots.StorageRoot, ".loom/retained-relocation.lock")
	if err := os.Rename(lock, lock+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(lock+"-original", lock); err != nil {
		t.Fatal(err)
	}
	if release, err := lockRetainedExecutor(roots); err == nil {
		release()
		t.Fatal("symlink lock followed")
	}
}
func TestRetainedMovePreservesIdentityAndRecoversSamePlan(t *testing.T) {
	roots, p := retainedMoveFixture(t)
	ctx := context.Background()
	before, err := os.Stat(filepath.Join(p.Source.AbsolutePath, "paper.md"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = moveRetained(ctx, roots, p); err != nil {
		t.Fatal(err)
	}
	if _, err = moveRetained(ctx, roots, p); err != nil {
		t.Fatalf("recover exact already-moved inode: %v", err)
	}
	after, err := os.Stat(filepath.Join(p.Destination.AbsolutePath, "paper.md"))
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("payload identity: %v", err)
	}
	if _, err = os.Stat(p.Source.AbsolutePath); !os.IsNotExist(err) {
		t.Fatal("old source still exists")
	}
}
func TestRetainedMoveRefusesDriftCollisionAndAncestorSubstitution(t *testing.T) {
	for _, scenario := range []string{"drift", "collision", "ancestor"} {
		t.Run(scenario, func(t *testing.T) {
			roots, p := retainedMoveFixture(t)
			switch scenario {
			case "drift":
				if err := os.WriteFile(filepath.Join(p.Source.AbsolutePath, "paper.md"), []byte("newer user bytes"), 0640); err != nil {
					t.Fatal(err)
				}
			case "collision":
				if err := os.Mkdir(p.Destination.AbsolutePath, 0700); err != nil {
					t.Fatal(err)
				}
			case "ancestor":
				parent := filepath.Dir(p.Destination.AbsolutePath)
				if err := os.Rename(parent, parent+"-retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(parent, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := moveRetained(context.Background(), roots, p); err == nil {
				t.Fatal("unsafe move accepted")
			}
			if _, err := os.Stat(filepath.Join(p.Source.AbsolutePath, "paper.md")); err != nil {
				t.Fatalf("source not preserved: %v", err)
			}
		})
	}
}

func TestRetainedMovePreservesOpaqueLinksWithoutWeakeningArchiveInventory(t *testing.T) {
	roots, p := retainedMoveFixture(t)
	target := filepath.Join(t.TempDir(), "not-opened")
	if err := os.Symlink(target, filepath.Join(p.Source.AbsolutePath, "historical-link")); err != nil {
		t.Fatal(err)
	}
	var err error
	p.Inventory, err = inventoryRetained(context.Background(), roots, p.Source)
	if err != nil || validateRetainedInventory(p.Inventory) != nil {
		t.Fatalf("literal inventory: %v", err)
	}
	if ValidateNoFollowInventory(p.Inventory) == nil {
		t.Fatal("retained-link inventory admitted to strict archive execution")
	}
	if _, err = moveRetained(context.Background(), roots, p); err != nil {
		t.Fatal(err)
	}
	actual, err := os.Readlink(filepath.Join(p.Destination.AbsolutePath, "historical-link"))
	if err != nil || actual != target {
		t.Fatalf("link text changed: %q %v", actual, err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatal("opaque target was materialized")
	}
}

func TestRetainedInventoryCanReadAnOlderCompletedMoveWithoutRewritingIt(t *testing.T) {
	roots, p := retainedMoveFixture(t)
	old, err := inventoryRetainedSchema(context.Background(), roots, p.Source, NoFollowInventorySchemaVersion)
	if err != nil || !retainedInventoriesMatch(old, p.Inventory) {
		t.Fatalf("old inventory compatibility: %v", err)
	}
	changed := p.Inventory
	changed.TotalBytes++
	if retainedInventoriesMatch(old, changed) {
		t.Fatal("schema compatibility hid a changed inventory")
	}
}
