package capabilities

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// DeclarationMigrationRead contains private domain rows, not public migration facts.
// The caller owns the read-only repeatable-read transaction and authorization.
type DeclarationMigrationRead struct {
	Completeness string
	Revision     string
	Providers    []ProviderListItem
}

func ReadDeclarationMigrationCapabilitiesTx(ctx context.Context, tx *sql.Tx, projectID, nodeID string) (out DeclarationMigrationRead, err error) {
	out.Completeness = "unavailable"
	out.Providers = []ProviderListItem{}
	if tx == nil || projectID == "" || nodeID == "" {
		return out, errors.New("migration_scope_transaction_required")
	}
	rows, err := tx.QueryContext(ctx, providerListSelectSQL()+` WHERE p.scope_id=(SELECT project_scope_id FROM projects.projects WHERE project_id=$1) AND p.provider_type='service' ORDER BY p.compact_address LIMIT 4097`, projectID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		v, e := scanProviderListItem(rows)
		if e != nil {
			return out, e
		}
		out.Providers = append(out.Providers, v)
		if len(out.Providers) > 4096 {
			out.Completeness = "truncated"
			return out, errors.New("migration_owner_bound")
		}
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	raw, err := json.Marshal(out.Providers)
	if err != nil {
		return out, err
	}
	out.Revision = fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	out.Completeness = "complete"
	return out, nil
}
