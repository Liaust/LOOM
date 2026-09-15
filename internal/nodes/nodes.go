package nodes

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Node struct {
	NodeID           string          `json:"node_id"`
	NodeKey          string          `json:"node_key"`
	DisplayName      string          `json:"display_name"`
	NodeKind         string          `json:"node_kind"`
	NodeRole         string          `json:"node_role"`
	RuntimeClass     string          `json:"runtime_class"`
	Status           string          `json:"status"`
	PresenceState    string          `json:"presence_state"`
	LastHeartbeatAt  *time.Time      `json:"last_heartbeat_at,omitempty"`
	LastSeenAt       *time.Time      `json:"last_seen_at,omitempty"`
	RuntimeVersion   *string         `json:"runtime_version,omitempty"`
	EnrollmentStatus string          `json:"enrollment_status"`
	CredentialStatus string          `json:"credential_status"`
	OwnerActorID     *string         `json:"owner_actor_id,omitempty"`
	HomeScopeID      *string         `json:"home_scope_id,omitempty"`
	Metadata         json.RawMessage `json:"metadata"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
	RetiredAt        *time.Time      `json:"retired_at,omitempty"`
}

type Service struct {
	DB *sql.DB
}

func NewService(db *sql.DB) Service {
	return Service{DB: db}
}

func (s Service) GetNode(ctx context.Context, ref string) (Node, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Node{}, fmt.Errorf("node ref is required")
	}

	row := s.DB.QueryRowContext(ctx, `
		SELECT node_id, node_key, display_name, node_kind, node_role,
		       runtime_class, status, presence_state, last_heartbeat_at,
		       last_seen_at, runtime_version, enrollment_status,
		       credential_status, owner_actor_id, home_scope_id, metadata,
		       created_at, updated_at, retired_at
		FROM nodes.nodes
		WHERE node_id = $1 OR node_key = $1
	`, ref)

	return scanNode(row)
}

func (s Service) ResolveNodeRef(ctx context.Context, ref string) (string, error) {
	node, err := s.GetNode(ctx, ref)
	if err != nil {
		return "", err
	}
	return node.NodeID, nil
}

func (s Service) ListNodes(ctx context.Context, limit int) ([]Node, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT node_id, node_key, display_name, node_kind, node_role,
		       runtime_class, status, presence_state, last_heartbeat_at,
		       last_seen_at, runtime_version, enrollment_status,
		       credential_status, owner_actor_id, home_scope_id, metadata,
		       created_at, updated_at, retired_at
		FROM nodes.nodes
		ORDER BY created_at ASC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	nodes := []Node{}
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, rows.Err()
}

type nodeScanner interface {
	Scan(dest ...any) error
}

func scanNode(scanner nodeScanner) (Node, error) {
	var node Node
	var metadata []byte
	var ownerActorID, homeScopeID, runtimeVersion sql.NullString
	var retiredAt, lastHeartbeatAt, lastSeenAt sql.NullTime
	if err := scanner.Scan(
		&node.NodeID,
		&node.NodeKey,
		&node.DisplayName,
		&node.NodeKind,
		&node.NodeRole,
		&node.RuntimeClass,
		&node.Status,
		&node.PresenceState,
		&lastHeartbeatAt,
		&lastSeenAt,
		&runtimeVersion,
		&node.EnrollmentStatus,
		&node.CredentialStatus,
		&ownerActorID,
		&homeScopeID,
		&metadata,
		&node.CreatedAt,
		&node.UpdatedAt,
		&retiredAt,
	); err != nil {
		return Node{}, err
	}
	node.OwnerActorID = stringPtr(ownerActorID)
	node.HomeScopeID = stringPtr(homeScopeID)
	node.RuntimeVersion = stringPtr(runtimeVersion)
	node.LastHeartbeatAt = timePtr(lastHeartbeatAt)
	node.LastSeenAt = timePtr(lastSeenAt)
	node.RetiredAt = timePtr(retiredAt)
	node.Metadata = json.RawMessage(metadata)
	if len(node.Metadata) == 0 {
		node.Metadata = json.RawMessage(`{}`)
	}
	return node, nil
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
