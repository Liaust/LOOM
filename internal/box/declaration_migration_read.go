package box

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
	Completeness  string
	Revision      string
	Registrations []WatchRootRegistration
}

func ReadDeclarationMigrationBoxTx(ctx context.Context, tx *sql.Tx, projectID, nodeID string) (out DeclarationMigrationRead, err error) {
	out.Completeness = "unavailable"
	out.Registrations = []WatchRootRegistration{}
	if tx == nil || projectID == "" || nodeID == "" {
		return out, errors.New("migration_scope_transaction_required")
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+watchRootRegistrationColumns()+` FROM box.watch_root_registrations WHERE node_id=$1 ORDER BY box_watch_root_registration_id LIMIT 4097`, nodeID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		v, e := scanWatchRootRegistration(rows)
		if e != nil {
			return out, e
		}
		out.Registrations = append(out.Registrations, v)
		if len(out.Registrations) > 4096 {
			out.Completeness = "truncated"
			return out, errors.New("migration_owner_bound")
		}
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	raw, err := json.Marshal(out.Registrations)
	if err != nil {
		return out, err
	}
	out.Revision = fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	out.Completeness = "complete"
	return out, nil
}
