package knowledge

import (
	"context"
	"database/sql"
	"reflect"

	"loom.local/loom/internal/projectcontracts"
)

// The declaration digest is audit identity, not the source's membership policy.
func sourcePolicyDeclaration(root SourceRoot) any {
	source, ok := sourceDeclaration(root).(map[string]any)
	if !ok {
		return sourceDeclaration(root)
	}
	delete(source, "source_hash")
	return source
}

func sourceAuditRefreshEligible(previous, next SourceRoot) bool {
	if next.RootKind != RootKindProjectMaterial || next.Status != SourceRootStatusActive ||
		previous.NotesSourceRootID != next.NotesSourceRootID || previous.RootKind != next.RootKind ||
		valueOrEmpty(previous.NodeID) != valueOrEmpty(next.NodeID) || previous.NodeKey != next.NodeKey ||
		valueOrEmpty(previous.ProjectID) != valueOrEmpty(next.ProjectID) ||
		previous.BackendRootKey != next.BackendRootKey || previous.SourcePath != next.SourcePath ||
		previous.RootRelativePath != next.RootRelativePath {
		return false
	}
	before, beforeOK := sourceDeclaration(previous).(map[string]any)
	after, afterOK := sourceDeclaration(next).(map[string]any)
	version, _ := before["schema_version"].(string)
	if !beforeOK || !afterOK || !projectcontracts.IsDeclarationSchemaVersion(version) ||
		after["schema_version"] != version {
		return false
	}
	oldHash, _ := before["source_hash"].(string)
	newHash, _ := after["source_hash"].(string)
	return sha256Pattern.MatchString(oldHash) && sha256Pattern.MatchString(newHash) &&
		(oldHash != newHash || previous.Status != SourceRootStatusActive) &&
		reflect.DeepEqual(sourcePolicyDeclaration(previous), sourcePolicyDeclaration(next))
}

func refreshSourceAuditBindingsTx(ctx context.Context, tx *sql.Tx, root SourceRoot) (bool, error) {
	// Current root authority is checked once. Only audit-only differences in
	// already admitted bindings are changed; content and publication stay intact.
	result, err := tx.ExecContext(ctx, `WITH current_root AS MATERIALIZED (
	 SELECT r.* FROM knowledge.notes_source_roots r WHERE r.notes_source_root_id=$1
	 AND r.status='active' AND `+declarationEnrollmentMembershipSQL("r")+`
	) UPDATE knowledge.knowledge_objects o SET
	 metadata=jsonb_set(o.metadata,'{source_root,knowledge_source}',r.metadata#>'{registration_metadata,knowledge_source}'),
	 updated_at=now()
	 FROM current_root r WHERE o.notes_source_root_id=r.notes_source_root_id AND o.deleted_at IS NULL
	 AND o.source_node_id IS NOT DISTINCT FROM r.node_id AND o.source_node_key=r.node_key
	 AND o.project_id IS NOT DISTINCT FROM r.project_id
	 AND o.metadata#>>'{source_root,source_path}'=r.source_path
	 AND o.metadata#>>'{source_root,root_relative_path}'=r.root_relative_path
	 AND o.metadata#>>'{source_root,knowledge_source,source_hash}' ~ '^sha256:[0-9a-f]{64}$'
	 AND (o.metadata#>'{source_root,knowledge_source}')-'source_hash'::text =
	     (r.metadata#>'{registration_metadata,knowledge_source}')-'source_hash'::text
	 AND o.metadata#>'{source_root,knowledge_source}' IS DISTINCT FROM r.metadata#>'{registration_metadata,knowledge_source}'
	 AND `+notesCustodyWriteAllowedSQL("o"), root.NotesSourceRootID)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}
