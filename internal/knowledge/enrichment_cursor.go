package knowledge

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"loom.local/loom/internal/boxregistry"
)

type enrichmentCursor struct {
	ObjectID   string `json:"object_id"`
	Scope      string `json:"scope"`
	Membership string `json:"membership"`
}

func enrichmentCursorScope(ctx context.Context, input EnrichmentSelection) string {
	selection := notesBoxSelection(ctx)
	if selection == nil {
		return ""
	}
	raw, _ := json.Marshal([]any{selection.BoxIDs, selection.AllBoxes, selection.Digest, input.Ref, input.Kind, input.NodeKey, input.Recursive, input.Stages})
	return boxregistry.Digest("enrichment", string(raw), "")
}

func decodeEnrichmentCursor(ctx context.Context, input EnrichmentSelection) (enrichmentCursor, error) {
	scope := enrichmentCursorScope(ctx, input)
	if input.After == "" || scope == "" {
		return enrichmentCursor{ObjectID: input.After}, nil
	}
	var cursor enrichmentCursor
	raw, err := base64.RawURLEncoding.DecodeString(input.After)
	if err != nil || len(raw) > 4096 || json.Unmarshal(raw, &cursor) != nil || cursor.ObjectID == "" || cursor.Scope != scope {
		return cursor, fmt.Errorf("%w: enrichment cursor belongs to another Box/selection; preview again", ErrConflict)
	}
	return cursor, nil
}

func validateEnrichmentCursor(ctx context.Context, tx *sql.Tx, cursor enrichmentCursor) error {
	if cursor.Scope == "" {
		return nil
	}
	args := []any{cursor.ObjectID}
	selection := notesSelectedObjectSQL(ctx, "knowledge_objects", &args)
	object, err := scanKnowledgeObject(tx.QueryRowContext(ctx, `SELECT `+knowledgeObjectColumns()+` FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1 AND deleted_at IS NULL AND `+selection, args...))
	if err != nil {
		return fmt.Errorf("%w: enrichment cursor source is no longer selected; preview again", ErrConflict)
	}
	if err = bindObjectBoxMembership(ctx, tx, &object); err != nil {
		return err
	}
	if cursor.Membership != object.BoxMembershipDigest {
		return fmt.Errorf("%w: enrichment cursor membership changed; preview again", ErrConflict)
	}
	return nil
}

func encodeEnrichmentCursor(ctx context.Context, input EnrichmentSelection, binding EnrichmentBinding) string {
	scope := enrichmentCursorScope(ctx, input)
	if scope == "" {
		return binding.ObjectID
	}
	raw, _ := json.Marshal(enrichmentCursor{ObjectID: binding.ObjectID, Scope: scope, Membership: binding.MembershipDigest})
	return base64.RawURLEncoding.EncodeToString(raw)
}
