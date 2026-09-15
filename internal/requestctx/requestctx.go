package requestctx

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type Context struct {
	ActorID       string `json:"actor_id"`
	ActorKey      string `json:"actor_key"`
	OriginNodeID  string `json:"origin_node_id"`
	OriginNodeKey string `json:"origin_node_key"`
	ScopeID       string `json:"scope_id"`
	ScopeKey      string `json:"scope_key"`
	CorrelationID string `json:"correlation_id"`
	FreshnessMode string `json:"freshness_mode"`
	Source        string `json:"source"`
}

type SchedulerResolveInput struct {
	ActorRef      string
	OriginNodeRef string
	ScopeRef      string
	Source        string
}

type ExternalIntegrationResolveInput struct {
	ActorRef      string
	OriginNodeRef string
	ScopeRef      string
	Source        string
}

func ResolveBootstrap(ctx context.Context, db *sql.DB, correlationID string) (Context, error) {
	var req Context

	err := db.QueryRowContext(ctx, `
		SELECT
			owner.actor_id,
			owner.actor_key,
			main.node_id,
			main.node_key,
			system_scope.scope_id,
			system_scope.scope_key
		FROM identity.actors owner
		CROSS JOIN nodes.nodes main
		CROSS JOIN scopes.scopes system_scope
		WHERE owner.actor_key = 'owner'
		  AND main.node_key = 'main'
		  AND system_scope.scope_key = 'system'
	`).Scan(
		&req.ActorID,
		&req.ActorKey,
		&req.OriginNodeID,
		&req.OriginNodeKey,
		&req.ScopeID,
		&req.ScopeKey,
	)
	if err != nil {
		return Context{}, fmt.Errorf("resolve bootstrap request context: %w", err)
	}

	req.CorrelationID = strings.TrimSpace(correlationID)
	req.FreshnessMode = "live_required"
	req.Source = "local-bootstrap"

	return req, nil
}

func ResolveScheduler(ctx context.Context, db *sql.DB, correlationID string, input SchedulerResolveInput) (Context, error) {
	actorRef := strings.TrimSpace(input.ActorRef)
	if actorRef == "" {
		actorRef = "scheduler:loom"
	}
	nodeRef := strings.TrimSpace(input.OriginNodeRef)
	if nodeRef == "" {
		nodeRef = "main"
	}
	scopeRef := strings.TrimSpace(input.ScopeRef)
	if scopeRef == "" {
		scopeRef = "system"
	}

	var req Context
	err := db.QueryRowContext(ctx, `
		SELECT
			actor.actor_id,
			actor.actor_key,
			node.node_id,
			node.node_key,
			scope.scope_id,
			scope.scope_key
		FROM identity.actors actor
		CROSS JOIN nodes.nodes node
		CROSS JOIN scopes.scopes scope
		WHERE (actor.actor_id = $1 OR actor.actor_key = $1)
		  AND actor.actor_kind = 'scheduler'
		  AND actor.status = 'active'
		  AND (node.node_id = $2 OR node.node_key = $2)
		  AND node.status = 'active'
		  AND (scope.scope_id = $3 OR scope.scope_key = $3 OR scope.slug = $3)
		  AND scope.status = 'active'
	`, actorRef, nodeRef, scopeRef).Scan(
		&req.ActorID,
		&req.ActorKey,
		&req.OriginNodeID,
		&req.OriginNodeKey,
		&req.ScopeID,
		&req.ScopeKey,
	)
	if err != nil {
		return Context{}, fmt.Errorf("resolve scheduler request context: %w", err)
	}

	req.CorrelationID = strings.TrimSpace(correlationID)
	req.FreshnessMode = "live_required"
	req.Source = strings.TrimSpace(input.Source)
	if req.Source == "" {
		req.Source = "schedule"
	}

	return req, nil
}

func ResolveExternalIntegration(ctx context.Context, db *sql.DB, correlationID string, input ExternalIntegrationResolveInput) (Context, error) {
	actorRef := strings.TrimSpace(input.ActorRef)
	if actorRef == "" {
		return Context{}, fmt.Errorf("external integration actor ref is required")
	}
	nodeRef := strings.TrimSpace(input.OriginNodeRef)
	if nodeRef == "" {
		nodeRef = "main"
	}
	scopeRef := strings.TrimSpace(input.ScopeRef)
	if scopeRef == "" {
		scopeRef = "system"
	}

	var req Context
	err := db.QueryRowContext(ctx, `
		SELECT
			actor.actor_id,
			actor.actor_key,
			node.node_id,
			node.node_key,
			scope.scope_id,
			scope.scope_key
		FROM identity.actors actor
		CROSS JOIN nodes.nodes node
		CROSS JOIN scopes.scopes scope
		WHERE (actor.actor_id = $1 OR actor.actor_key = $1)
		  AND actor.actor_kind = 'external_integration'
		  AND actor.status = 'active'
		  AND (node.node_id = $2 OR node.node_key = $2)
		  AND node.status = 'active'
		  AND (scope.scope_id = $3 OR scope.scope_key = $3 OR scope.slug = $3)
		  AND scope.status = 'active'
	`, actorRef, nodeRef, scopeRef).Scan(
		&req.ActorID,
		&req.ActorKey,
		&req.OriginNodeID,
		&req.OriginNodeKey,
		&req.ScopeID,
		&req.ScopeKey,
	)
	if err != nil {
		return Context{}, fmt.Errorf("resolve external integration request context: %w", err)
	}

	req.CorrelationID = strings.TrimSpace(correlationID)
	req.FreshnessMode = "live_required"
	req.Source = strings.TrimSpace(input.Source)
	if req.Source == "" {
		req.Source = "direct_event"
	}

	return req, nil
}
