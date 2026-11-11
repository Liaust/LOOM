package provenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"loom.local/loom/internal/boxregistry"
)

var ErrBoxSelectionMismatch = errors.New("provenance Box membership is outside selection or unknown")

const boxMembershipSQL = `WITH latest AS (
 SELECT DISTINCT ON (project_id) project_id,projection_json->>'home_box_id' AS home_box_id
 FROM provenance.project_projection_snapshots ORDER BY project_id,projection_revision DESC,id
)
SELECT md5(COALESCE(string_agg(project_id||':'||COALESCE(home_box_id,''),',' ORDER BY project_id),''))
FROM latest WHERE $1::text[] IS NULL OR home_box_id=ANY($1::text[])`

// Membership, unlike a Box ID, can change. Bind inherited project membership
// to the cursor without changing append-only records or adding another ledger.
func (store *Store) boxSelectionContext(ctx context.Context) (context.Context, error) {
	selection, scoped := boxregistry.FromContext(ctx)
	if !scoped {
		return ctx, nil
	}
	var membership string
	if err := store.q.queryRow(ctx, boxMembershipSQL, selectedBoxes(ctx)).Scan(&membership); err != nil {
		return nil, fmt.Errorf("read provenance Box membership revision: %w", err)
	}
	selection.Digest = selectionDigest(ctx) + ":" + membership
	return boxregistry.WithSelection(ctx, selection), nil
}

func selectionDigest(ctx context.Context) string {
	selection, ok := boxregistry.FromContext(ctx)
	if !ok {
		return ""
	}
	ids := append([]string{}, selection.BoxIDs...)
	sort.Strings(ids)
	raw, _ := json.Marshal(struct {
		IDs    []string
		All    bool
		Digest string
	}{ids, selection.AllBoxes, selection.Digest})
	return digestJSON(raw)
}

func boxMismatch() error {
	return &FoundationConflictError{Err: fmt.Errorf("%w; explicitly select the originating Boxes or all_boxes to inspect qualified history", ErrBoxSelectionMismatch)}
}

// A nil SQL array is the intentional legacy/internal global view. An empty
// selected array matches nothing; it must not broaden to that global view.
func selectedBoxes(ctx context.Context) []string {
	selection, ok := boxregistry.FromContext(ctx)
	if !ok || selection.AllBoxes {
		return nil
	}
	return append([]string{}, selection.BoxIDs...)
}

// Arguments are SQL expressions owned by this package, never caller text.
// Explicit attribution wins over project inheritance. Every attribution must
// fit: mixed or unassigned history is not safe implicit context for either Box.
func anchorsBoxSQL(anchors, parameter string) string {
	boxes := "COALESCE(NULLIF(" + anchors + "->'boxes','null'::jsonb),'[]'::jsonb)"
	projects := "COALESCE(NULLIF(" + anchors + "->'projects','null'::jsonb),'[]'::jsonb)"
	return `(` + parameter + `::text[] IS NULL OR CASE
 WHEN jsonb_typeof(` + boxes + `) <> 'array' THEN false
 WHEN jsonb_array_length(` + boxes + `)>0 THEN NOT EXISTS (
  SELECT 1 FROM jsonb_array_elements_text(` + boxes + `) AS box(value)
  WHERE NOT COALESCE(box.value=ANY(` + parameter + `::text[]),false))
 WHEN jsonb_typeof(` + projects + `) <> 'array' THEN false
 ELSE jsonb_array_length(` + projects + `)>0 AND NOT EXISTS (
  SELECT 1 FROM jsonb_array_elements_text(` + projects + `) AS member(value)
  WHERE NOT COALESCE((SELECT p.projection_json->>'home_box_id'=ANY(` + parameter + `::text[])
   FROM provenance.project_projection_snapshots p WHERE p.project_id=member.value
   ORDER BY p.projection_revision DESC,p.id LIMIT 1),false)) END)`
}

func caseBoxSQL(caseID, parameter string) string {
	return `(` + parameter + `::text[] IS NULL OR (EXISTS (
 SELECT 1 FROM provenance.case_members scoped_member WHERE scoped_member.case_id=` + caseID + `)
 AND NOT EXISTS (
 SELECT 1 FROM provenance.case_members scoped_member
 LEFT JOIN provenance.records scoped_record ON scoped_record.id=scoped_member.record_id
 LEFT JOIN provenance.candidates scoped_candidate ON scoped_candidate.id=scoped_member.candidate_id
 WHERE scoped_member.case_id=` + caseID + ` AND NOT ` + anchorsBoxSQL("COALESCE(scoped_record.anchors_json,scoped_candidate.submitted_json->'anchors')", parameter) + `)))`
}

func projectBoxSQL(projectID, parameter string) string {
	return `(` + parameter + `::text[] IS NULL OR COALESCE((
 SELECT p.projection_json->>'home_box_id'=ANY(` + parameter + `::text[])
 FROM provenance.project_projection_snapshots p WHERE p.project_id=` + projectID + `
 ORDER BY p.projection_revision DESC,p.id LIMIT 1),false))`
}

func (store *Store) checkAnchors(ctx context.Context, anchors *StructuralAnchors) error {
	boxes := selectedBoxes(ctx)
	if boxes == nil {
		return nil
	}
	if anchors == nil {
		return boxMismatch()
	}
	if len(anchors.Boxes) > 0 {
		selection, _ := boxregistry.FromContext(ctx)
		for _, id := range anchors.Boxes {
			if id == "" || !selection.Contains(id) {
				return boxMismatch()
			}
		}
		return nil
	}
	if len(anchors.Projects) == 0 {
		return boxMismatch()
	}
	raw, err := json.Marshal(anchors)
	if err != nil {
		return err
	}
	var allowed bool
	if err := store.q.queryRow(ctx, `SELECT `+anchorsBoxSQL("$1::jsonb", "$2"), raw, boxes).Scan(&allowed); err != nil {
		return fmt.Errorf("resolve provenance Box attribution: %w", err)
	}
	if !allowed {
		return boxMismatch()
	}
	return nil
}

func (store *Store) checkCase(ctx context.Context, id SemanticID) error {
	if selectedBoxes(ctx) == nil {
		return nil
	}
	var allowed bool
	if err := store.q.queryRow(ctx, `SELECT `+caseBoxSQL("$1::uuid", "$2"), string(id), selectedBoxes(ctx)).Scan(&allowed); err != nil {
		return fmt.Errorf("resolve provenance case Box attribution: %w", err)
	}
	if !allowed {
		return boxMismatch()
	}
	return nil
}

func (store *Store) checkProject(ctx context.Context, id string) error {
	if selectedBoxes(ctx) == nil {
		return nil
	}
	var allowed bool
	if err := store.q.queryRow(ctx, `SELECT `+projectBoxSQL("$1::text", "$2"), id, selectedBoxes(ctx)).Scan(&allowed); err != nil {
		return fmt.Errorf("resolve provenance project Box attribution: %w", err)
	}
	if !allowed {
		return boxMismatch()
	}
	return nil
}

func checkHomeBox(ctx context.Context, id string) error {
	selection, ok := boxregistry.FromContext(ctx)
	if ok && !selection.AllBoxes && (id == "" || !selection.Contains(id)) {
		return boxMismatch()
	}
	return nil
}

// Replays bypass transition validation, not the request's Box boundary.
func (store *Store) checkReplaySelection(ctx context.Context, operation LifecycleOperation) error {
	if selectedBoxes(ctx) == nil {
		return nil
	}
	receipt := receiptForOperation(operation)
	var found bool
	var err error
	switch operation.(type) {
	case AcceptCandidateOperation, ConsolidateCandidateOperation, RejectCandidateOperation, DeferCandidateOperation, LinkCandidateEvidenceOperation:
		_, found, err = store.GetCandidate(ctx, receipt.ObjectID)
	case AppendQualificationOperation, AppendTemporalInterpretationOperation, AppendAssertionPostureOperation:
		_, found, err = store.GetRecord(ctx, receipt.ObjectID)
	case CreateResolutionCaseOperation, UpdateResolutionCaseOperation, CloseResolutionCaseOperation, AttachCaseMemberOperation:
		_, found, err = store.GetResolutionCase(ctx, receipt.ObjectID)
	case AddRelationshipOperation, EndRelationshipOperation:
		_, found, err = store.GetRelationship(ctx, receipt.ObjectID)
	}
	if err != nil {
		return err
	}
	if !found {
		return boxMismatch()
	}
	return nil
}
