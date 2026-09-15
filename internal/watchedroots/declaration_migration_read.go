package watchedroots

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
	Roots        []WatchedRoot
}

func ReadDeclarationMigrationWatchedrootsTx(ctx context.Context, tx *sql.Tx, projectID, nodeID string) (out DeclarationMigrationRead, err error) {
	out.Completeness = "unavailable"
	out.Roots = []WatchedRoot{}
	if tx == nil || projectID == "" || nodeID == "" {
		return out, errors.New("migration_scope_transaction_required")
	}
	rows, err := tx.QueryContext(ctx, rootSelectSQL(false)+` WHERE node_id=$1 ORDER BY root_key LIMIT 4097`, nodeID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		v, e := scanRoot(rows)
		if e != nil {
			return out, e
		}
		out.Roots = append(out.Roots, v)
		if len(out.Roots) > 4096 {
			out.Completeness = "truncated"
			return out, errors.New("migration_owner_bound")
		}
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	raw, err := json.Marshal(out.Roots)
	if err != nil {
		return out, err
	}
	out.Revision = fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	out.Completeness = "complete"
	return out, nil
}
