package loomdapp

import (
	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/storagearchive"
)

// Additional archive custody is optional to Main availability. Missing or
// unsafe custody leaves that Box's lifecycle unavailable, never Personal-owned.
func additionalBoxArchiveRoots(catalog boxregistry.Catalog, unavailable func(boxregistry.Box, error)) map[string]storagearchive.TrustedWorkspaceRoots {
	result := map[string]storagearchive.TrustedWorkspaceRoots{}
	for _, binding := range catalog.Boxes {
		if binding.Primary || !binding.Available || binding.ID == "" {
			continue
		}
		roots, err := storagearchive.WorkspaceRootsForBox(catalog, binding.ID)
		if err == nil {
			// Custody root provisioning stays with the host. Prepare only its
			// existing root's managed children, as for the primary Storage owner.
			err = storagearchive.PrepareRetainedRoots(roots.StorageRoot)
		}
		if err != nil {
			if unavailable != nil {
				unavailable(binding, err)
			}
			continue
		}
		result[binding.ID] = roots
	}
	return result
}
