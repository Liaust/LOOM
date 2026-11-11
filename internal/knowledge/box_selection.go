package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"loom.local/loom/internal/boxregistry"
)

func notesBoxSelection(ctx context.Context) *boxregistry.ResolvedSelection {
	selection, ok := boxregistry.FromContext(ctx)
	if !ok {
		return nil
	}
	return &selection
}

// Project/application roots inherit logical ownership, never allocation paths.
// A missing historical project membership stays unknown, not Personal.
func notesRootBoxIDSQL(root string) string {
	return `(CASE WHEN ` + root + `.project_id IS NOT NULL THEN
 (SELECT membership_project.metadata->>'home_box_id' FROM projects.projects membership_project
 WHERE membership_project.project_id=` + root + `.project_id)
 ELSE ` + root + `.metadata->>'box_id' END)`
}

func notesSelectedRootSQL(selection *boxregistry.ResolvedSelection, root string, args *[]any) string {
	if selection == nil || selection.AllBoxes {
		return "true"
	}
	if len(selection.BoxIDs) == 0 {
		return "false"
	}
	ids, _ := json.Marshal(selection.BoxIDs)
	*args = append(*args, string(ids))
	return fmt.Sprintf("%s IN (SELECT jsonb_array_elements_text($%d::jsonb))", notesRootBoxIDSQL(root), len(*args))
}

func notesSelectedObjectSQL(ctx context.Context, object string, args *[]any) string {
	return notesSelectedObjectForSelectionSQL(notesBoxSelection(ctx), object, args)
}

func notesSelectedObjectForSelectionSQL(selection *boxregistry.ResolvedSelection, object string, args *[]any) string {
	if selection == nil || selection.AllBoxes {
		return "true"
	}
	return `EXISTS (SELECT 1 FROM knowledge.notes_source_roots box_root WHERE box_root.notes_source_root_id=` + object + `.notes_source_root_id AND ` + notesSelectedRootSQL(selection, "box_root", args) + `)`
}

func notesSelectedPipelineRunSQL(ctx context.Context, run string, args *[]any) string {
	selection := notesBoxSelection(ctx)
	if selection == nil || selection.AllBoxes {
		return "true"
	}
	return `EXISTS (SELECT 1 FROM knowledge.knowledge_objects box_object WHERE box_object.knowledge_object_id=` + run + `.knowledge_object_id AND ` + notesSelectedObjectForSelectionSQL(selection, "box_object", args) + `)`
}

func notesSelectedPipelineChildSQL(ctx context.Context, child string, args *[]any) string {
	selection := notesBoxSelection(ctx)
	if selection == nil || selection.AllBoxes {
		return "true"
	}
	return `EXISTS (SELECT 1 FROM knowledge.pipeline_runs box_run WHERE box_run.knowledge_pipeline_run_id=` + child + `.knowledge_pipeline_run_id AND ` + notesSelectedPipelineRunSQL(ctx, "box_run", args) + `)`
}

type boxMembershipQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Bounded exact-object enrichment reads current membership, rather than waiting
// for admission to rewrite every object after a logical reassociation.
func bindObjectBoxMembership(ctx context.Context, query boxMembershipQuerier, object *KnowledgeObject) error {
	var id, revision string
	err := query.QueryRowContext(ctx, `SELECT COALESCE(`+notesRootBoxIDSQL("r")+`,''),
 COALESCE(CASE WHEN r.project_id IS NOT NULL THEN
 (SELECT p.metadata->>'box_membership_revision' FROM projects.projects p WHERE p.project_id=r.project_id)
 ELSE r.metadata->>'box_membership_revision' END,'')
 FROM knowledge.notes_source_roots r WHERE r.notes_source_root_id=$1`, object.NotesSourceRootID).Scan(&id, &revision)
	if err != nil {
		return err
	}
	if selection := notesBoxSelection(ctx); selection != nil && !selection.AllBoxes && !selection.Contains(id) {
		return fmt.Errorf("%w: object is outside the selected Box or has unassigned ownership; explicitly select its home Box", ErrConflict)
	}
	object.BoxMembershipDigest = ""
	if id != "" {
		object.BoxMembershipDigest = boxregistry.Digest(id, object.NotesSourceRootID, revision)
	}
	return nil
}
