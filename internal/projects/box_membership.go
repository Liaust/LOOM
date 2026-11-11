package projects

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"loom.local/loom/internal/boxregistry"
)

// WithBoxCatalog enables physical registration validation without changing
// intentional global/internal callers of the project store.
func (s Service) WithBoxCatalog(catalog boxregistry.Catalog) Service {
	s.boxes = &catalog
	return s
}

// BindBoxMembership accepts placement evidence, not a client metadata claim.
// The verified field is deliberately not serialized into portable declarations.
func BindBoxMembership(input RegisterProjectContractInput, catalog boxregistry.Catalog) (RegisterProjectContractInput, error) {
	box, err := catalog.ForPath(input.ProjectRoot)
	if err != nil {
		return input, fmt.Errorf("project Box placement: %w", err)
	}
	if box.Node != "" && input.Project.OwnerNode != "" && box.Node != input.Project.OwnerNode {
		return input, fmt.Errorf("project owner node contradicts Box placement")
	}
	var metadata map[string]json.RawMessage
	if len(input.Metadata) > 0 {
		if err := json.Unmarshal(input.Metadata, &metadata); err != nil {
			return input, err
		}
	}
	if claimed := HomeBoxID(input.Metadata); claimed != "" && claimed != box.ID {
		return input, fmt.Errorf("project home_box_id contradicts physical placement")
	}
	input.verifiedHomeBoxID = box.ID
	return input, nil
}

func HomeBoxID(metadata json.RawMessage) string {
	var values struct {
		HomeBoxID string `json:"home_box_id"`
	}
	_ = json.Unmarshal(metadata, &values)
	return values.HomeBoxID
}

func projectBoxSelectionSQL(ctx context.Context, alias string, args *[]any) string {
	selection, selected := boxregistry.FromContext(ctx)
	if !selected || selection.AllBoxes {
		return "true"
	}
	if len(selection.BoxIDs) == 0 {
		return "false"
	}
	ids, _ := json.Marshal(selection.BoxIDs)
	*args = append(*args, string(ids))
	return fmt.Sprintf("%s.metadata->>'home_box_id' IN (SELECT jsonb_array_elements_text($%d::jsonb))", alias, len(*args))
}

func projectRefNeedsResolution(ctx context.Context, ref string) bool {
	selection, bound := boxregistry.FromContext(ctx)
	return !strings.HasPrefix(ref, "project_") || (bound && !selection.AllBoxes)
}

func ensureProjectBoxSelection(ctx context.Context, project Project) error {
	if selection, ok := boxregistry.FromContext(ctx); ok && !selection.AllBoxes && !selection.Contains(HomeBoxID(project.Metadata)) {
		return fmt.Errorf("project %q is outside the selected Box or has unassigned ownership; explicitly select its home Box (use a Box-qualified machine slug for a new project)", project.Slug)
	}
	return nil
}

type projectBoxCatalogKey struct{}

func (s Service) boxRegistrationContext(ctx context.Context) context.Context {
	if s.boxes != nil {
		return context.WithValue(ctx, projectBoxCatalogKey{}, *s.boxes)
	}
	return ctx
}
