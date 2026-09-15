package routing

import (
	"context"
	"database/sql"
	"fmt"
)

// FindCapabilityCallByToken is read-only. Ambiguous historical delivery cannot
// establish absence or authorize another submission with the same owner token.
func (s Service) FindCapabilityCallByToken(ctx context.Context, actor, token string) (CapabilityCall, error) {
	rows, err := s.DB.QueryContext(ctx, capabilityCallSelectSQL()+` WHERE actor_id=$1 AND idempotency_key=$2 ORDER BY created_at LIMIT 2`, actor, token)
	if err != nil {
		return CapabilityCall{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return CapabilityCall{}, err
		}
		return CapabilityCall{}, sql.ErrNoRows
	}
	call, err := scanCapabilityCall(rows)
	if err != nil {
		return CapabilityCall{}, err
	}
	if rows.Next() {
		return CapabilityCall{}, fmt.Errorf("application.call_token_ambiguous")
	}
	if err := rows.Err(); err != nil {
		return CapabilityCall{}, err
	}
	return call, nil
}
