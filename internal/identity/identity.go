package identity

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Actor struct {
	ActorID        string          `json:"actor_id"`
	ActorKey       string          `json:"actor_key"`
	DisplayName    string          `json:"display_name"`
	ActorKind      string          `json:"actor_kind"`
	HomeNodeID     *string         `json:"home_node_id,omitempty"`
	DefaultScopeID *string         `json:"default_scope_id,omitempty"`
	Status         string          `json:"status"`
	Metadata       json.RawMessage `json:"metadata"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	RevokedAt      *time.Time      `json:"revoked_at,omitempty"`
	RevokedReason  *string         `json:"revoked_reason,omitempty"`
}

type Service struct {
	DB *sql.DB
}

func NewService(db *sql.DB) Service {
	return Service{DB: db}
}

func (s Service) GetActor(ctx context.Context, ref string) (Actor, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Actor{}, fmt.Errorf("actor ref is required")
	}

	row := s.DB.QueryRowContext(ctx, `
		SELECT actor_id, actor_key, display_name, actor_kind, home_node_id,
		       default_scope_id, status, metadata, created_at, updated_at,
		       revoked_at, revoked_reason
		FROM identity.actors
		WHERE actor_id = $1 OR actor_key = $1
	`, ref)

	return scanActor(row)
}

func (s Service) ListActors(ctx context.Context, limit int) ([]Actor, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	rows, err := s.DB.QueryContext(ctx, `
		SELECT actor_id, actor_key, display_name, actor_kind, home_node_id,
		       default_scope_id, status, metadata, created_at, updated_at,
		       revoked_at, revoked_reason
		FROM identity.actors
		ORDER BY actor_key
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var actors []Actor
	for rows.Next() {
		actor, err := scanActor(rows)
		if err != nil {
			return nil, err
		}
		actors = append(actors, actor)
	}
	return actors, rows.Err()
}

func (s Service) ResolveActorRef(ctx context.Context, ref string) (string, error) {
	actor, err := s.GetActor(ctx, ref)
	if err != nil {
		return "", err
	}
	return actor.ActorID, nil
}

type actorScanner interface {
	Scan(dest ...any) error
}

func scanActor(scanner actorScanner) (Actor, error) {
	var actor Actor
	var metadata []byte
	var homeNodeID sql.NullString
	var defaultScopeID sql.NullString
	var revokedAt sql.NullTime
	var revokedReason sql.NullString
	if err := scanner.Scan(
		&actor.ActorID,
		&actor.ActorKey,
		&actor.DisplayName,
		&actor.ActorKind,
		&homeNodeID,
		&defaultScopeID,
		&actor.Status,
		&metadata,
		&actor.CreatedAt,
		&actor.UpdatedAt,
		&revokedAt,
		&revokedReason,
	); err != nil {
		return Actor{}, err
	}
	actor.HomeNodeID = stringPtr(homeNodeID)
	actor.DefaultScopeID = stringPtr(defaultScopeID)
	actor.RevokedAt = timePtr(revokedAt)
	actor.RevokedReason = stringPtr(revokedReason)
	actor.Metadata = json.RawMessage(metadata)
	if len(actor.Metadata) == 0 {
		actor.Metadata = json.RawMessage(`{}`)
	}
	return actor, nil
}

func stringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func timePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}
