package storagearchive

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"time"

	"golang.org/x/sys/unix"
	"loom.local/loom/internal/storagecatalog"
)

// The DB lease alone cannot cover a root CLI's filesystem phase. All supported
// executors share this lock; delayed external execution rechecks its journal
// state while holding it, before touching the payload.
func lockRetainedExecutor(roots TrustedWorkspaceRoots) (func(), error) {
	chain, err := openHeldDirectoryChain(roots.StorageRoot, ".loom", nil)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Openat(chain.ParentFD(), "retained-relocation.lock", unix.O_CREAT|unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0660)
	if err != nil {
		chain.Close()
		return nil, err
	}
	close := func() { _ = unix.Close(fd); chain.Close() }
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 {
		close()
		return nil, fmt.Errorf("retained executor lock must be a private regular file: %v", err)
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		close()
		return nil, fmt.Errorf("another retained executor is active; inspect this exact request after it finishes: %w", err)
	}
	if err = chain.verifyNamed(); err != nil {
		close()
		return nil, err
	}
	return func() { _ = unix.Flock(fd, unix.LOCK_UN); close() }, nil
}

func (s RetainedRelocationService) savedPlan(ctx context.Context, id, digest string) (storagecatalog.RetainedRelocation, RetainedRelocationPlan, error) {
	r, err := s.Catalog.RetainedRelocation(ctx, id)
	var p RetainedRelocationPlan
	if err != nil {
		return r, p, err
	}
	if json.Unmarshal(r.Plan, &p) != nil || p.Schema != RetainedRelocationSchema || p.ID != r.ID || p.CustodyID != r.CustodyID || p.Original != r.Original || p.Source.AbsolutePath != r.Source || p.Destination.AbsolutePath != r.Destination || p.Digest != digest || digest != r.Digest || relocationDigest(p) != digest || validateRetainedInventory(p.Inventory) != nil {
		return r, p, fmt.Errorf("saved relocation plan does not match the reviewed digest")
	}
	if err := validateWorkspaceBoxBinding(p.BoxID, s.Roots); err != nil {
		return r, p, err
	}
	for _, ref := range []WorkspacePathRef{p.Source, p.Destination} {
		want, err := retainedRef(s.Roots, ref.RelativePath)
		if err != nil || ref != want {
			return r, p, fmt.Errorf("trusted roots changed since review")
		}
	}
	return r, p, nil
}

func (s RetainedRelocationService) Cancel(ctx context.Context, id, digest string) (storagecatalog.RetainedRelocation, error) {
	r, p, err := s.savedPlan(ctx, id, digest)
	if err != nil || r.State == "cancelled" {
		return r, err
	}
	release, err := lockRetainedExecutor(s.Roots)
	if err != nil {
		return r, err
	}
	defer release()
	return s.Catalog.CancelRetainedRelocation(ctx, id, digest, func(storagecatalog.RetainedRelocation) (json.RawMessage, error) {
		return proveRetainedUnmoved(ctx, s.Roots, p)
	})
}

func proveRetainedUnmoved(ctx context.Context, roots TrustedWorkspaceRoots, p RetainedRelocationPlan) (json.RawMessage, error) {
	if err := validateWorkspaceBoxBinding(p.BoxID, roots); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source, err := openHeldDirectoryChain(pathOperationRoot(roots, p.Source.Root), path.Dir(p.Source.RelativePath), p.SourceAncestors)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	destination, err := openHeldDirectoryChain(pathOperationRoot(roots, p.Destination.Root), path.Dir(p.Destination.RelativePath), p.DestinationAncestors)
	if err != nil {
		return nil, err
	}
	defer destination.Close()
	present, matches, err := inspectNamedDirectory(source.ParentFD(), path.Base(p.Source.RelativePath), p.Inventory.RootIdentity)
	if err != nil || !present || !matches {
		return nil, fmt.Errorf("cancellation needs the unchanged source root inode; recover or inspect the exact move: %v", err)
	}
	if present, err := namePresentNoFollow(destination.ParentFD(), path.Base(p.Destination.RelativePath)); err != nil || present {
		return nil, fmt.Errorf("cancellation requires an absent destination; no data was changed: %v", err)
	}
	if err = source.verifyNamed(); err != nil {
		return nil, err
	}
	if err = destination.verifyNamed(); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"schema": "storage.retained_cancellation.v1", "effect": "none", "source_identity": p.Inventory.RootIdentity, "destination_absent": true, "reviewed_inventory": p.Inventory.Digest, "source_content_revalidated": false, "verification_uid": os.Geteuid(), "at": time.Now().UTC()})
}
