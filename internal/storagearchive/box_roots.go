package storagearchive

import (
	"fmt"

	"loom.local/loom/internal/boxregistry"
)

// WorkspaceRootsForBox binds a service instance to one real source/archive pair.
// Callers must compose their project/Notes/dependency adapters with the same
// binding, not change a process-global active root.
func WorkspaceRootsForBox(catalog boxregistry.Catalog, ref string) (TrustedWorkspaceRoots, error) {
	binding, err := catalog.Get(ref)
	if err != nil {
		return TrustedWorkspaceRoots{}, err
	}
	roots := TrustedWorkspaceRoots{BoxID: binding.ID, Primary: binding.Primary, BoxRoot: binding.RootPath, StorageRoot: binding.ArchiveRoot}
	if roots.BoxID == "" || !boxregistry.IsBoxRef("box:"+roots.BoxID) {
		return TrustedWorkspaceRoots{}, fmt.Errorf("workspace lifecycle requires an immutable Box identity")
	}
	if err := validateTrustedWorkspaceRoot(WorkspaceRootBox, roots.BoxRoot); err != nil {
		return TrustedWorkspaceRoots{}, err
	}
	if err := validateTrustedWorkspaceRoot(WorkspaceRootStorage, roots.StorageRoot); err != nil {
		return TrustedWorkspaceRoots{}, err
	}
	return roots, nil
}

// WorkspaceRootsForManifest deliberately ignores current navigation selection.
// Historical identity-free manifests can resolve only to the legacy primary
// Box and must still match its exact signed absolute path evidence.
func WorkspaceRootsForManifest(catalog boxregistry.Catalog, manifest WorkspaceArchiveManifest) (TrustedWorkspaceRoots, error) {
	ref := manifest.BoxID
	if ref == "" {
		for _, binding := range catalog.Boxes {
			if binding.Primary {
				ref = binding.RootRef
				break
			}
		}
		if ref == "" {
			return TrustedWorkspaceRoots{}, fmt.Errorf("historical home Box unavailable; explicit recovery placement required")
		}
	}
	roots, err := WorkspaceRootsForBox(catalog, ref)
	if err != nil {
		return TrustedWorkspaceRoots{}, err
	}
	if err := ValidateWorkspaceArchiveManifestForRoots(manifest, roots); err != nil {
		return TrustedWorkspaceRoots{}, err
	}
	return roots, nil
}

func validateWorkspaceBoxBinding(id string, roots TrustedWorkspaceRoots) error {
	if id != "" && !boxregistry.IsBoxRef("box:"+id) {
		return fmt.Errorf("invalid immutable Box identity")
	}
	if id != roots.BoxID && !(id == "" && roots.Primary) {
		return fmt.Errorf("workspace operation belongs to another Box; explicit transfer or recovery placement required")
	}
	return nil
}
