package projectapply

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/config"
	pc "loom.local/loom/internal/projectcontracts"
)

func selectedProjectBox(ctx context.Context, cfg config.Config, root string) (boxregistry.Box, string, error) {
	physical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return boxregistry.Box{}, "", fail(pc.DeclarationTargetUnavailable, "project_source_unavailable")
	}
	var binding boxregistry.Box
	if cfg.BoxesFile != "" {
		catalog, err := cfg.BoxCatalog()
		if err != nil {
			return binding, "", fail(pc.DeclarationTargetUnavailable, "configured_box_unavailable")
		}
		binding, err = catalog.ForPath(physical)
		if err != nil || root != physical {
			return binding, "", fail(pc.DeclarationUnauthorized, "source_outside_configured_box")
		}
	} else {
		trusted, err := filepath.EvalSymlinks(cfg.BoxPath)
		if err != nil || cfg.BoxPath == "" {
			return binding, "", fail(pc.DeclarationTargetUnavailable, "configured_box_unavailable")
		}
		binding = boxregistry.Box{RootPath: trusted, RootRef: "box", Primary: true, Available: true}
		if _, scoped := boxregistry.FromContext(ctx); scoped {
			catalog, err := cfg.BoxCatalog()
			if err != nil {
				return binding, "", fail(pc.DeclarationTargetUnavailable, "configured_box_unavailable")
			}
			binding, err = catalog.ForPath(physical)
			if err != nil {
				return binding, "", fail(pc.DeclarationUnauthorized, "source_outside_configured_box")
			}
		}
	}
	rel, err := filepath.Rel(binding.RootPath, physical)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return binding, "", fail(pc.DeclarationUnauthorized, "source_outside_configured_box")
	}
	if selected, scoped := boxregistry.FromContext(ctx); scoped && !selected.Contains(binding.ID) {
		return binding, "", fail(pc.DeclarationUnauthorized, "project_outside_selected_box")
	}
	return binding, physical, nil
}

func validateProjectHomeBox(metadata json.RawMessage, binding boxregistry.Box) error {
	var fields struct {
		HomeBoxID string `json:"home_box_id"`
	}
	if len(metadata) > 0 && json.Unmarshal(metadata, &fields) != nil {
		return fail(pc.DeclarationIdentityConflict, "project_box_membership_invalid")
	}
	if fields.HomeBoxID != "" && binding.ID != "" && fields.HomeBoxID != binding.ID {
		return fail(pc.DeclarationIdentityConflict, "project_box_membership_changed")
	}
	return nil
}

func projectLocationRevision(nodeID string, binding boxregistry.Box, root, physical string) string {
	location := map[string]string{"node_id": nodeID, "box": binding.RootPath, "project_root": root, "physical_root": physical}
	// Keep Personal's pre-Box revision bytes for existing declaration receipts.
	// Catalog and membership checks independently validate its current identity.
	if !binding.Primary {
		location["box_id"], location["root_ref"] = binding.ID, binding.RootRef
	}
	raw, _ := canonicalValue(location)
	return hashBytes(raw)
}
