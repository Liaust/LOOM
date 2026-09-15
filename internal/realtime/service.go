package realtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/requestctx"
)

type Service struct {
	DB *sql.DB
}

func NewService(db *sql.DB) Service {
	return Service{DB: db}
}

func (s Service) CreateTopic(ctx context.Context, req requestctx.Context, input CreateTopicInput) (Topic, error) {
	input = normalizeCreateTopicInput(input)
	if input.TopicPath == "" {
		return Topic{}, fmt.Errorf("topic_path is required")
	}
	if !ValidRetentionMode(input.RetentionMode) {
		return Topic{}, fmt.Errorf("unsupported retention_mode: %s", input.RetentionMode)
	}
	if !ValidDeliveryClass(input.DeliveryClass) {
		return Topic{}, fmt.Errorf("unsupported delivery_class: %s", input.DeliveryClass)
	}
	if err := ValidateObjectJSON(input.Metadata, "metadata"); err != nil {
		return Topic{}, err
	}
	scopeID := req.ScopeID
	var err error
	if input.ScopeRef != "" {
		scopeID, err = resolveScopeID(ctx, s.DB, input.ScopeRef)
		if err != nil {
			return Topic{}, err
		}
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Topic{}, err
	}
	defer tx.Rollback()

	topicID := ids.NewTopicID()
	topic, err := scanTopic(tx.QueryRowContext(ctx, topicSelectSQL(`
		INSERT INTO realtime.topics (
			topic_id, topic_path, display_name, scope_id, owner_actor_id,
			created_by_actor_id, retention_mode, delivery_class, ordering_mode,
			status, metadata
		)
		VALUES ($1, $2, $3, nullif($4, ''), nullif($5, ''), $6, $7, $8, $9, $10, $11)
	`),
		topicID,
		input.TopicPath,
		input.DisplayName,
		scopeID,
		req.ActorID,
		req.ActorID,
		input.RetentionMode,
		input.DeliveryClass,
		OrderingModeTopicSequence,
		TopicStatusActive,
		objectOrDefault(input.Metadata),
	))
	if err != nil {
		return Topic{}, err
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  events.TypeRealtimeTopicCreated,
		EventLevel: "audit",
		Request:    req,
		ScopeID:    scopeID,
		TargetKind: "topic",
		TargetID:   topic.TopicID,
		Status:     topic.Status,
		Result:     "topic_created",
		Payload: map[string]any{
			"topic_id":       topic.TopicID,
			"topic_path":     topic.TopicPath,
			"retention_mode": topic.RetentionMode,
			"delivery_class": topic.DeliveryClass,
		},
		VisibilityClass: "internal",
	}); err != nil {
		return Topic{}, err
	}
	if err := tx.Commit(); err != nil {
		return Topic{}, err
	}
	return topic, nil
}

func (s Service) ListTopics(ctx context.Context, filter TopicFilter) ([]Topic, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	if status := strings.TrimSpace(filter.Status); status != "" && !ValidTopicStatus(status) {
		return nil, fmt.Errorf("unsupported topic status filter: %s", status)
	}

	query := topicSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}

	if status := strings.TrimSpace(filter.Status); status != "" {
		add("status =", status)
	}
	if ref := strings.TrimSpace(filter.ScopeRef); ref != "" {
		scopeID, err := resolveScopeID(ctx, s.DB, ref)
		if err != nil {
			return nil, err
		}
		add("scope_id =", scopeID)
	}

	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY updated_at DESC LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	topics := []Topic{}
	for rows.Next() {
		topic, err := scanTopic(rows)
		if err != nil {
			return nil, err
		}
		topics = append(topics, topic)
	}
	return topics, rows.Err()
}

func (s Service) GetTopic(ctx context.Context, ref string) (Topic, error) {
	topic, err := getTopic(ctx, s.DB, ref, false)
	if err != nil {
		return Topic{}, err
	}
	return topic, nil
}

func (s Service) CloseTopic(ctx context.Context, req requestctx.Context, ref string) (Topic, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Topic{}, err
	}
	defer tx.Rollback()

	topic, err := getTopic(ctx, tx, ref, true)
	if err != nil {
		return Topic{}, err
	}
	if topic.Status != TopicStatusActive {
		return Topic{}, fmt.Errorf("topic is not active: %s", topic.Status)
	}
	closed, err := scanTopic(tx.QueryRowContext(ctx, topicSelectSQL(`
		UPDATE realtime.topics
		SET status = $2,
		    closed_at = now(),
		    updated_at = now()
		WHERE topic_id = $1
	`), topic.TopicID, TopicStatusClosed))
	if err != nil {
		return Topic{}, err
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  events.TypeRealtimeTopicClosed,
		EventLevel: "audit",
		Request:    req,
		ScopeID:    pointerValue(closed.ScopeID),
		TargetKind: "topic",
		TargetID:   closed.TopicID,
		Status:     closed.Status,
		Result:     "topic_closed",
		Payload: map[string]any{
			"topic_id":   closed.TopicID,
			"topic_path": closed.TopicPath,
		},
		VisibilityClass: "internal",
	}); err != nil {
		return Topic{}, err
	}
	if err := tx.Commit(); err != nil {
		return Topic{}, err
	}
	return closed, nil
}

func (s Service) PublishTopic(ctx context.Context, req requestctx.Context, ref string, input PublishInput) (TopicPublication, error) {
	input = normalizePublishInput(input)
	if strings.TrimSpace(ref) == "" {
		return TopicPublication{}, fmt.Errorf("topic ref is required")
	}
	if input.MessageType == "" {
		return TopicPublication{}, fmt.Errorf("message_type is required")
	}
	if err := ValidateObjectJSON(input.Payload, "payload"); err != nil {
		return TopicPublication{}, err
	}
	if err := ValidateObjectJSON(input.Metadata, "metadata"); err != nil {
		return TopicPublication{}, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return TopicPublication{}, err
	}
	defer tx.Rollback()

	topic, err := getTopic(ctx, tx, ref, true)
	if err != nil {
		return TopicPublication{}, err
	}
	if topic.Status != TopicStatusActive {
		return TopicPublication{}, fmt.Errorf("topic is not active: %s", topic.Status)
	}

	nextSequence := topic.LastSequence + 1
	durabilityMode := durabilityForRetention(topic.RetentionMode)
	if _, err := tx.ExecContext(ctx, `
		UPDATE realtime.topics
		SET last_sequence = $2,
		    latest_payload_json = $3,
		    updated_at = now()
		WHERE topic_id = $1
	`, topic.TopicID, nextSequence, input.Payload); err != nil {
		return TopicPublication{}, err
	}

	publication, err := scanTopicPublication(tx.QueryRowContext(ctx, topicPublicationSelectSQL(`
		INSERT INTO realtime.topic_publications (
			topic_publication_id, topic_id, sequence, publisher_actor_id, publisher_node_id,
			message_type, payload_json, payload_schema_ref, correlation_id, causation_ref,
			authorization_ref, durability_mode, metadata
		)
		VALUES ($1, $2, $3, nullif($4, ''), nullif($5, ''), $6, $7, $8, $9, $10, $11, $12, $13)
	`),
		ids.NewTopicPublicationID(),
		topic.TopicID,
		nextSequence,
		req.ActorID,
		req.OriginNodeID,
		input.MessageType,
		input.Payload,
		input.PayloadSchemaRef,
		req.CorrelationID,
		input.CausationRef,
		input.AuthorizationRef,
		durabilityMode,
		objectOrDefault(input.Metadata),
	))
	if err != nil {
		return TopicPublication{}, err
	}
	if input.MeaningfulEvent {
		if _, err := events.AppendTx(ctx, tx, events.AppendInput{
			EventType:  events.TypeRealtimePublicationCreated,
			EventLevel: "audit",
			Request:    req,
			ScopeID:    pointerValue(topic.ScopeID),
			TargetKind: "topic_publication",
			TargetID:   publication.TopicPublicationID,
			Status:     "published",
			Result:     "publication_created",
			Payload: map[string]any{
				"topic_id":             topic.TopicID,
				"topic_publication_id": publication.TopicPublicationID,
				"sequence":             publication.Sequence,
				"message_type":         publication.MessageType,
			},
			VisibilityClass: "internal",
		}); err != nil {
			return TopicPublication{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return TopicPublication{}, err
	}
	return publication, nil
}

func (s Service) CreateSubscription(ctx context.Context, req requestctx.Context, input CreateSubscriptionInput) (Subscription, error) {
	input = normalizeCreateSubscriptionInput(input)
	if input.TopicRef == "" {
		return Subscription{}, fmt.Errorf("topic_ref is required")
	}
	if input.CursorMode != SubscriptionCursorFromNow && input.CursorMode != SubscriptionCursorFromStart && input.CursorMode != SubscriptionCursorFromBeginning {
		return Subscription{}, fmt.Errorf("unsupported cursor_mode: %s", input.CursorMode)
	}
	if !ValidDeliveryClass(input.DeliveryTargetKind) {
		return Subscription{}, fmt.Errorf("unsupported delivery_target_kind: %s", input.DeliveryTargetKind)
	}
	if !ValidDeliveryMode(input.DeliveryMode) {
		return Subscription{}, fmt.Errorf("unsupported delivery_mode: %s", input.DeliveryMode)
	}
	if !ValidDeliveryClass(input.DeliveryClass) {
		return Subscription{}, fmt.Errorf("unsupported delivery_class: %s", input.DeliveryClass)
	}
	if err := ValidateObjectJSON(input.FilterJSON, "filter_json"); err != nil {
		return Subscription{}, err
	}
	if err := ValidateObjectJSON(input.Metadata, "metadata"); err != nil {
		return Subscription{}, err
	}

	topic, err := getTopic(ctx, s.DB, input.TopicRef, false)
	if err != nil {
		return Subscription{}, err
	}
	if topic.Status != TopicStatusActive {
		return Subscription{}, fmt.Errorf("topic is not active: %s", topic.Status)
	}
	cursor := topic.LastSequence
	if input.CursorMode == SubscriptionCursorFromStart || input.CursorMode == SubscriptionCursorFromBeginning {
		cursor = 0
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Subscription{}, err
	}
	defer tx.Rollback()

	subscription, err := scanSubscription(tx.QueryRowContext(ctx, subscriptionSelectSQL(`
		INSERT INTO realtime.subscriptions (
			subscription_id, topic_id, source_kind, source_ref, subscriber_actor_id,
			subscriber_node_id, scope_id, filter_json, cursor_sequence,
			last_acknowledged_sequence, delivery_target_kind, delivery_target_ref,
			delivery_mode, delivery_class, authorization_ref, status, expires_at, metadata
		)
		VALUES (
			$1, $2, $3, $4, nullif($5, ''), nullif($6, ''), nullif($7, ''),
			$8, $9, $9, $10, $11, $12, $13, $14, $15, $16, $17
		)
	`),
		ids.NewSubscriptionID(),
		topic.TopicID,
		SubscriptionSourceKindTopic,
		topic.TopicID,
		req.ActorID,
		req.OriginNodeID,
		pointerValue(topic.ScopeID),
		objectOrDefault(input.FilterJSON),
		cursor,
		input.DeliveryTargetKind,
		input.DeliveryTargetRef,
		input.DeliveryMode,
		input.DeliveryClass,
		input.AuthorizationRef,
		SubscriptionStatusActive,
		input.ExpiresAt,
		objectOrDefault(input.Metadata),
	))
	if err != nil {
		return Subscription{}, err
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  events.TypeRealtimeSubscriptionCreated,
		EventLevel: "audit",
		Request:    req,
		ScopeID:    pointerValue(topic.ScopeID),
		TargetKind: "subscription",
		TargetID:   subscription.SubscriptionID,
		Status:     subscription.Status,
		Result:     "subscription_created",
		Payload: map[string]any{
			"subscription_id": subscription.SubscriptionID,
			"topic_id":        topic.TopicID,
			"cursor_sequence": cursor,
			"cursor_mode":     input.CursorMode,
		},
		VisibilityClass: "internal",
	}); err != nil {
		return Subscription{}, err
	}
	if err := tx.Commit(); err != nil {
		return Subscription{}, err
	}
	return subscription, nil
}

func (s Service) ListSubscriptions(ctx context.Context, filter SubscriptionFilter) ([]Subscription, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	if status := strings.TrimSpace(filter.Status); status != "" && !ValidSubscriptionStatus(status) {
		return nil, fmt.Errorf("unsupported subscription status filter: %s", status)
	}

	query := subscriptionSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		add("status =", status)
	}
	if ref := strings.TrimSpace(filter.TopicRef); ref != "" {
		topic, err := getTopic(ctx, s.DB, ref, false)
		if err != nil {
			return nil, err
		}
		add("topic_id =", topic.TopicID)
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	subscriptions := []Subscription{}
	for rows.Next() {
		subscription, err := scanSubscription(rows)
		if err != nil {
			return nil, err
		}
		subscriptions = append(subscriptions, subscription)
	}
	return subscriptions, rows.Err()
}

func (s Service) GetSubscription(ctx context.Context, ref string) (Subscription, error) {
	return getSubscription(ctx, s.DB, ref, false)
}

func (s Service) PollSubscription(ctx context.Context, ref string, input PollSubscriptionInput) (SubscriptionPollResult, error) {
	if input.Limit <= 0 || input.Limit > 200 {
		input.Limit = 50
	}
	subscription, err := getSubscription(ctx, s.DB, ref, false)
	if err != nil {
		return SubscriptionPollResult{}, err
	}
	if subscription.Status != SubscriptionStatusActive {
		return SubscriptionPollResult{}, fmt.Errorf("subscription is not active: %s", subscription.Status)
	}
	if subscription.ExpiresAt != nil && time.Now().UTC().After(*subscription.ExpiresAt) {
		return SubscriptionPollResult{}, fmt.Errorf("subscription is expired")
	}
	rows, err := s.DB.QueryContext(ctx, topicPublicationSelectSQL()+`
		WHERE topic_id = $1
		  AND sequence > $2
		ORDER BY sequence ASC
		LIMIT $3
	`, subscription.TopicID, subscription.CursorSequence, input.Limit)
	if err != nil {
		return SubscriptionPollResult{}, err
	}
	defer rows.Close()

	publications := []TopicPublication{}
	for rows.Next() {
		publication, err := scanTopicPublication(rows)
		if err != nil {
			return SubscriptionPollResult{}, err
		}
		publications = append(publications, publication)
	}
	if err := rows.Err(); err != nil {
		return SubscriptionPollResult{}, err
	}
	if len(publications) > 0 {
		now := time.Now().UTC()
		if _, err := s.DB.ExecContext(ctx, `
			UPDATE realtime.subscriptions
			SET last_delivered_at = $2,
			    updated_at = now()
			WHERE subscription_id = $1
		`, subscription.SubscriptionID, now); err != nil {
			return SubscriptionPollResult{}, err
		}
		subscription.LastDeliveredAt = &now
	}
	fromSequence := subscription.CursorSequence + 1
	toSequence := subscription.CursorSequence
	if len(publications) > 0 {
		toSequence = publications[len(publications)-1].Sequence
	}
	return SubscriptionPollResult{
		Subscription: subscription,
		Publications: publications,
		FromSequence: fromSequence,
		ToSequence:   toSequence,
		Replay:       subscription.CursorSequence > subscription.LastAcknowledgedSequence,
	}, nil
}

func (s Service) AcknowledgeSubscription(ctx context.Context, req requestctx.Context, ref string, input AcknowledgeSubscriptionInput) (Subscription, error) {
	if input.Sequence <= 0 {
		return Subscription{}, fmt.Errorf("sequence must be positive")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Subscription{}, err
	}
	defer tx.Rollback()

	subscription, err := getSubscription(ctx, tx, ref, true)
	if err != nil {
		return Subscription{}, err
	}
	if subscription.Status != SubscriptionStatusActive {
		return Subscription{}, fmt.Errorf("subscription is not active: %s", subscription.Status)
	}
	if input.Sequence < subscription.CursorSequence {
		return Subscription{}, fmt.Errorf("sequence cannot move cursor backwards")
	}

	updated, err := scanSubscription(tx.QueryRowContext(ctx, subscriptionSelectSQL(`
		UPDATE realtime.subscriptions
		SET cursor_sequence = $2,
		    last_acknowledged_sequence = $2,
		    last_acknowledged_at = now(),
		    updated_at = now()
		WHERE subscription_id = $1
	`), subscription.SubscriptionID, input.Sequence))
	if err != nil {
		return Subscription{}, err
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  events.TypeRealtimeSubscriptionAcked,
		EventLevel: "audit",
		Request:    req,
		ScopeID:    pointerValue(updated.ScopeID),
		TargetKind: "subscription",
		TargetID:   updated.SubscriptionID,
		Status:     updated.Status,
		Result:     "subscription_acknowledged",
		Payload: map[string]any{
			"subscription_id": updated.SubscriptionID,
			"topic_id":        updated.TopicID,
			"sequence":        updated.CursorSequence,
		},
		VisibilityClass: "internal",
	}); err != nil {
		return Subscription{}, err
	}
	if err := tx.Commit(); err != nil {
		return Subscription{}, err
	}
	return updated, nil
}

func (s Service) CancelSubscription(ctx context.Context, req requestctx.Context, ref string) (Subscription, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Subscription{}, err
	}
	defer tx.Rollback()

	subscription, err := getSubscription(ctx, tx, ref, true)
	if err != nil {
		return Subscription{}, err
	}
	if subscription.Status != SubscriptionStatusActive {
		return Subscription{}, fmt.Errorf("subscription is not active: %s", subscription.Status)
	}
	updated, err := scanSubscription(tx.QueryRowContext(ctx, subscriptionSelectSQL(`
		UPDATE realtime.subscriptions
		SET status = $2,
		    updated_at = now()
		WHERE subscription_id = $1
	`), subscription.SubscriptionID, SubscriptionStatusCancelled))
	if err != nil {
		return Subscription{}, err
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  events.TypeRealtimeSubscriptionCancel,
		EventLevel: "audit",
		Request:    req,
		ScopeID:    pointerValue(updated.ScopeID),
		TargetKind: "subscription",
		TargetID:   updated.SubscriptionID,
		Status:     updated.Status,
		Result:     "subscription_cancelled",
		Payload: map[string]any{
			"subscription_id": updated.SubscriptionID,
			"topic_id":        updated.TopicID,
		},
		VisibilityClass: "internal",
	}); err != nil {
		return Subscription{}, err
	}
	if err := tx.Commit(); err != nil {
		return Subscription{}, err
	}
	return updated, nil
}

func (s Service) ProjectNodeHeartbeat(ctx context.Context, req requestctx.Context, input NodePresenceInput) (Presence, error) {
	input = normalizeNodePresenceInput(input)
	if input.NodeID == "" {
		return Presence{}, fmt.Errorf("node_id is required")
	}
	if !ValidPresenceState(input.State) {
		return Presence{}, fmt.Errorf("unsupported presence state: %s", input.State)
	}
	if err := ValidateObjectJSON(input.Metadata, "metadata"); err != nil {
		return Presence{}, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Presence{}, err
	}
	defer tx.Rollback()

	existing, err := scanPresence(tx.QueryRowContext(ctx, presenceSelectSQL()+`
		WHERE subject_kind = $1
		  AND subject_ref = $2
		  AND node_id = $2
	`, PresenceSubjectKindNode, input.NodeID))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Presence{}, err
	}
	previousState := ""
	if err == nil {
		previousState = existing.State
		presence, err := scanPresence(tx.QueryRowContext(ctx, presenceSelectSQL(`
			UPDATE realtime.presence
			SET state = $2,
			    source_kind = $3,
			    source_ref = $4,
			    last_seen_at = $5,
			    expires_at = $6,
			    status_detail = $7,
			    metadata = $8,
			    updated_at = now()
			WHERE presence_id = $1
		`),
			existing.PresenceID,
			input.State,
			PresenceSourceKindHeartbeat,
			input.HeartbeatID,
			input.LastSeenAt,
			input.LastSeenAt.Add(time.Duration(input.ExpiresInSeconds)*time.Second),
			input.StatusDetail,
			objectOrDefault(input.Metadata),
		))
		if err != nil {
			return Presence{}, err
		}
		if previousState != presence.State {
			if err := appendPresenceChangedEvent(ctx, tx, req, presence, previousState); err != nil {
				return Presence{}, err
			}
		}
		if err := tx.Commit(); err != nil {
			return Presence{}, err
		}
		return presence, nil
	}

	presence, err := scanPresence(tx.QueryRowContext(ctx, presenceSelectSQL(`
		INSERT INTO realtime.presence (
			presence_id, subject_kind, subject_ref, node_id, state, source_kind,
			source_ref, last_seen_at, expires_at, status_detail, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`),
		ids.NewPresenceID(),
		PresenceSubjectKindNode,
		input.NodeID,
		input.NodeID,
		input.State,
		PresenceSourceKindHeartbeat,
		input.HeartbeatID,
		input.LastSeenAt,
		input.LastSeenAt.Add(time.Duration(input.ExpiresInSeconds)*time.Second),
		input.StatusDetail,
		objectOrDefault(input.Metadata),
	))
	if err != nil {
		return Presence{}, err
	}
	if err := appendPresenceChangedEvent(ctx, tx, req, presence, previousState); err != nil {
		return Presence{}, err
	}
	if err := tx.Commit(); err != nil {
		return Presence{}, err
	}
	return presence, nil
}

func (s Service) ListPresence(ctx context.Context, filter PresenceFilter) ([]Presence, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	if subjectKind := strings.TrimSpace(filter.SubjectKind); subjectKind != "" && !ValidPresenceSubjectKind(subjectKind) {
		return nil, fmt.Errorf("unsupported presence subject_kind filter: %s", subjectKind)
	}
	if state := strings.TrimSpace(filter.State); state != "" && !ValidPresenceState(state) {
		return nil, fmt.Errorf("unsupported presence state filter: %s", state)
	}

	query := presenceSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	if subjectKind := strings.TrimSpace(filter.SubjectKind); subjectKind != "" {
		add("subject_kind =", subjectKind)
	}
	if state := strings.TrimSpace(filter.State); state != "" {
		add("state =", state)
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY updated_at DESC LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	presenceRows := []Presence{}
	for rows.Next() {
		presence, err := scanPresence(rows)
		if err != nil {
			return nil, err
		}
		presenceRows = append(presenceRows, presence)
	}
	return presenceRows, rows.Err()
}

func (s Service) GetPresence(ctx context.Context, ref string) (Presence, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Presence{}, fmt.Errorf("presence ref is required")
	}
	row := s.DB.QueryRowContext(ctx, presenceSelectSQL()+`
		WHERE presence_id = $1 OR subject_ref = $1
		ORDER BY updated_at DESC
		LIMIT 1
	`, ref)
	return scanPresence(row)
}

func (s Service) CreateNotification(ctx context.Context, req requestctx.Context, input CreateNotificationInput) (Notification, error) {
	input = normalizeCreateNotificationInput(input)
	if input.TargetKind == "" || !ValidNotificationTargetKind(input.TargetKind) {
		return Notification{}, fmt.Errorf("unsupported target_kind: %s", input.TargetKind)
	}
	if input.TargetRef == "" {
		return Notification{}, fmt.Errorf("target_ref is required")
	}
	if input.SourceKind == "" || !ValidNotificationSourceKind(input.SourceKind) {
		return Notification{}, fmt.Errorf("unsupported source_kind: %s", input.SourceKind)
	}
	if input.Category == "" || !ValidNotificationCategory(input.Category) {
		return Notification{}, fmt.Errorf("unsupported category: %s", input.Category)
	}
	if !ValidNotificationPriority(input.Priority) {
		return Notification{}, fmt.Errorf("unsupported priority: %s", input.Priority)
	}
	if input.Summary == "" {
		return Notification{}, fmt.Errorf("summary is required")
	}
	if !ValidDeliveryMode(input.DeliveryMode) {
		return Notification{}, fmt.Errorf("unsupported delivery_mode: %s", input.DeliveryMode)
	}
	if err := ValidateObjectJSON(input.Payload, "payload"); err != nil {
		return Notification{}, err
	}
	if err := ValidateObjectJSON(input.Metadata, "metadata"); err != nil {
		return Notification{}, err
	}

	scopeID := ""
	var err error
	if input.ScopeRef != "" {
		scopeID, err = resolveScopeID(ctx, s.DB, input.ScopeRef)
		if err != nil {
			return Notification{}, err
		}
	}
	policyDecisionID := ""
	if input.PolicyDecisionRef != "" {
		policyDecisionID, err = resolvePolicyDecisionID(ctx, s.DB, input.PolicyDecisionRef)
		if err != nil {
			return Notification{}, err
		}
	}
	approvalID := ""
	if input.ApprovalRef != "" {
		approvalID, err = resolveApprovalID(ctx, s.DB, input.ApprovalRef)
		if err != nil {
			return Notification{}, err
		}
	}
	routeID := ""
	if input.RouteRef != "" {
		routeID, err = resolveRouteID(ctx, s.DB, input.RouteRef)
		if err != nil {
			return Notification{}, err
		}
	}
	capabilityCallID := ""
	if input.CapabilityCallRef != "" {
		capabilityCallID, err = resolveCapabilityCallID(ctx, s.DB, input.CapabilityCallRef)
		if err != nil {
			return Notification{}, err
		}
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Notification{}, err
	}
	defer tx.Rollback()

	notification, err := insertNotificationTx(ctx, tx, req, input, scopeID, policyDecisionID, approvalID, routeID, capabilityCallID)
	if err != nil {
		return Notification{}, err
	}
	if err := tx.Commit(); err != nil {
		return Notification{}, err
	}
	return notification, nil
}

func (s Service) CreateApprovalNotification(ctx context.Context, req requestctx.Context, input ApprovalNotificationInput) (Notification, error) {
	approval, err := lookupApprovalForNotification(ctx, s.DB, input.ApprovalRef)
	if err != nil {
		return Notification{}, err
	}
	existing, err := scanNotification(s.DB.QueryRowContext(ctx, notificationSelectSQL()+`
		WHERE approval_id = $1
		  AND status IN ('created', 'routed', 'delivered')
		ORDER BY created_at DESC
		LIMIT 1
	`, approval.ApprovalID))
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Notification{}, err
	}

	targetActorID, err := resolveOwnerActorID(ctx, s.DB)
	if err != nil {
		return Notification{}, err
	}
	payload, err := json.Marshal(map[string]any{
		"approval_id":                   approval.ApprovalID,
		"approval_key":                  approval.ApprovalKey,
		"requested_by_actor_id":         approval.RequestedByActorID,
		"operation":                     approval.Operation,
		"risk_level":                    approval.RiskLevel,
		"execution_authorization_level": approval.ExecutionAuthorizationLevel,
		"action_summary":                approval.ActionSummary,
		"expires_at":                    approval.ExpiresAt.UTC().Format(time.RFC3339),
		"route_id":                      strings.TrimSpace(input.RouteRef),
		"capability_call_id":            strings.TrimSpace(input.CapabilityCallRef),
	})
	if err != nil {
		return Notification{}, err
	}
	return s.CreateNotification(ctx, req, CreateNotificationInput{
		TargetKind:        NotificationTargetKindActor,
		TargetRef:         targetActorID,
		SourceKind:        NotificationSourceKindPolicy,
		SourceRef:         approval.ApprovalID,
		ScopeRef:          firstNonEmpty(input.ScopeRef, pointerValue(approval.ScopeID)),
		Category:          NotificationCategoryApproval,
		Priority:          NotificationPriorityHigh,
		Summary:           firstNonEmpty(approval.ActionSummary, "Capability call requires approval."),
		Payload:           payload,
		PolicyDecisionRef: firstNonEmpty(input.PolicyDecisionRef, pointerValue(approval.PolicyDecisionID)),
		ApprovalRef:       approval.ApprovalID,
		RouteRef:          input.RouteRef,
		CapabilityCallRef: input.CapabilityCallRef,
		DeliveryMode:      DeliveryClassPolling,
		ExpiresAt:         &approval.ExpiresAt,
		Metadata: mustJSON(map[string]any{
			"bridge": "approval_notification",
		}),
	})
}

func (s Service) ListNotifications(ctx context.Context, filter NotificationFilter) ([]Notification, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	if status := strings.TrimSpace(filter.Status); status != "" && !ValidNotificationStatus(status) {
		return nil, fmt.Errorf("unsupported notification status filter: %s", status)
	}
	if category := strings.TrimSpace(filter.Category); category != "" && !ValidNotificationCategory(category) {
		return nil, fmt.Errorf("unsupported notification category filter: %s", category)
	}
	if targetKind := strings.TrimSpace(filter.TargetKind); targetKind != "" && !ValidNotificationTargetKind(targetKind) {
		return nil, fmt.Errorf("unsupported notification target_kind filter: %s", targetKind)
	}

	query := notificationSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		add("status =", status)
	}
	if category := strings.TrimSpace(filter.Category); category != "" {
		add("category =", category)
	}
	if targetKind := strings.TrimSpace(filter.TargetKind); targetKind != "" {
		add("target_kind =", targetKind)
	}
	if targetRef := strings.TrimSpace(filter.TargetRef); targetRef != "" {
		add("target_ref =", targetRef)
	}
	if approvalRef := strings.TrimSpace(filter.ApprovalRef); approvalRef != "" {
		approvalID, err := resolveApprovalID(ctx, s.DB, approvalRef)
		if err != nil {
			return nil, err
		}
		add("approval_id =", approvalID)
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	notifications := []Notification{}
	for rows.Next() {
		notification, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		notifications = append(notifications, notification)
	}
	return notifications, rows.Err()
}

func (s Service) GetNotification(ctx context.Context, ref string) (Notification, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Notification{}, fmt.Errorf("notification ref is required")
	}
	row := s.DB.QueryRowContext(ctx, notificationSelectSQL()+`
		WHERE notification_id = $1
	`, ref)
	return scanNotification(row)
}

func (s Service) AcknowledgeNotification(ctx context.Context, req requestctx.Context, ref string) (Notification, error) {
	return s.transitionNotification(ctx, req, ref, NotificationStatusAcknowledged, events.TypeRealtimeNotificationAcked, "notification_acknowledged")
}

func (s Service) DismissNotification(ctx context.Context, req requestctx.Context, ref string) (Notification, error) {
	return s.transitionNotification(ctx, req, ref, NotificationStatusDismissed, events.TypeRealtimeNotificationDismiss, "notification_dismissed")
}

func (s Service) ExpireNotifications(ctx context.Context, req requestctx.Context, now time.Time) (NotificationExpirationResult, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return NotificationExpirationResult{}, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, notificationSelectSQL(`
		UPDATE realtime.notifications
		SET status = $2,
		    updated_at = now()
		WHERE expires_at IS NOT NULL
		  AND expires_at <= $1
		  AND status IN ('created', 'routed', 'delivered')
	`), now, NotificationStatusExpired)
	if err != nil {
		return NotificationExpirationResult{}, err
	}
	defer rows.Close()

	result := NotificationExpirationResult{}
	expired := []Notification{}
	for rows.Next() {
		notification, err := scanNotification(rows)
		if err != nil {
			return NotificationExpirationResult{}, err
		}
		result.NotificationsExpired++
		expired = append(expired, notification)
	}
	if err := rows.Err(); err != nil {
		return NotificationExpirationResult{}, err
	}
	if err := rows.Close(); err != nil {
		return NotificationExpirationResult{}, err
	}
	for _, notification := range expired {
		if _, err := events.AppendTx(ctx, tx, events.AppendInput{
			EventType:  events.TypeRealtimeNotificationExpired,
			EventLevel: "audit",
			Request:    req,
			ScopeID:    pointerValue(notification.ScopeID),
			TargetKind: "notification",
			TargetID:   notification.NotificationID,
			Status:     notification.Status,
			Result:     "notification_expired",
			Payload: map[string]any{
				"notification_id": notification.NotificationID,
				"category":        notification.Category,
				"approval_id":     pointerValue(notification.ApprovalID),
			},
			VisibilityClass: "internal",
		}); err != nil {
			return NotificationExpirationResult{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE realtime.notification_deliveries
		SET status = $2
		WHERE notification_id IN (
			SELECT notification_id
			FROM realtime.notifications
			WHERE expires_at IS NOT NULL
			  AND expires_at <= $1
			  AND status = $3
		)
		  AND status IN ('pending', 'delivered')
	`, now, NotificationDeliveryStatusExpired, NotificationStatusExpired); err != nil {
		return NotificationExpirationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return NotificationExpirationResult{}, err
	}
	return result, nil
}

func (s Service) transitionNotification(ctx context.Context, req requestctx.Context, ref, status, eventType, result string) (Notification, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Notification{}, err
	}
	defer tx.Rollback()

	notification, err := getNotification(ctx, tx, ref, true)
	if err != nil {
		return Notification{}, err
	}
	if notification.Status == NotificationStatusAcknowledged || notification.Status == NotificationStatusDismissed || notification.Status == NotificationStatusExpired || notification.Status == NotificationStatusCancelled {
		return Notification{}, fmt.Errorf("notification is already terminal: %s", notification.Status)
	}
	timestampColumn := "acknowledged_at"
	if status == NotificationStatusDismissed {
		timestampColumn = "dismissed_at"
	}
	updated, err := scanNotification(tx.QueryRowContext(ctx, notificationSelectSQL(fmt.Sprintf(`
		UPDATE realtime.notifications
		SET status = $2,
		    %s = now(),
		    updated_at = now()
		WHERE notification_id = $1
	`, timestampColumn)), notification.NotificationID, status))
	if err != nil {
		return Notification{}, err
	}
	deliveryStatus := NotificationDeliveryStatusAcknowledged
	if status == NotificationStatusDismissed {
		deliveryStatus = NotificationDeliveryStatusCancelled
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE realtime.notification_deliveries
		SET status = $2,
		    acknowledged_at = CASE WHEN $2 = 'acknowledged' THEN now() ELSE acknowledged_at END
		WHERE notification_id = $1
		  AND status IN ('pending', 'delivered')
	`, updated.NotificationID, deliveryStatus); err != nil {
		return Notification{}, err
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  eventType,
		EventLevel: "audit",
		Request:    req,
		ScopeID:    pointerValue(updated.ScopeID),
		TargetKind: "notification",
		TargetID:   updated.NotificationID,
		Status:     updated.Status,
		Result:     result,
		Payload: map[string]any{
			"notification_id": updated.NotificationID,
			"category":        updated.Category,
			"target_kind":     updated.TargetKind,
			"target_ref":      updated.TargetRef,
		},
		VisibilityClass: "internal",
	}); err != nil {
		return Notification{}, err
	}
	if err := tx.Commit(); err != nil {
		return Notification{}, err
	}
	return updated, nil
}

func (s Service) EnsureProgressFeed(ctx context.Context, req requestctx.Context, input EnsureProgressFeedInput) (ProgressFeed, error) {
	input = normalizeEnsureProgressFeedInput(input)
	if input.SourceKind == "" || input.SourceRef == "" {
		return ProgressFeed{}, fmt.Errorf("source_kind and source_ref are required")
	}
	if !ValidProgressSourceKind(input.SourceKind) {
		return ProgressFeed{}, fmt.Errorf("unsupported progress source_kind: %s", input.SourceKind)
	}
	if !ValidProgressStatus(input.Status) {
		return ProgressFeed{}, fmt.Errorf("unsupported progress status: %s", input.Status)
	}
	if err := ValidateObjectJSON(input.Metadata, "metadata"); err != nil {
		return ProgressFeed{}, err
	}
	scopeID := req.ScopeID
	var err error
	if input.ScopeRef != "" {
		scopeID, err = resolveScopeID(ctx, s.DB, input.ScopeRef)
		if err != nil {
			return ProgressFeed{}, err
		}
	}
	topicID := ""
	if input.TopicRef != "" {
		topic, err := getTopic(ctx, s.DB, input.TopicRef, false)
		if err != nil {
			return ProgressFeed{}, err
		}
		topicID = topic.TopicID
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ProgressFeed{}, err
	}
	defer tx.Rollback()
	feed, err := ensureProgressFeedTx(ctx, tx, input, scopeID, topicID)
	if err != nil {
		return ProgressFeed{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProgressFeed{}, err
	}
	return feed, nil
}

func (s Service) UpdateProgress(ctx context.Context, req requestctx.Context, input UpdateProgressInput) (ProgressUpdateResult, error) {
	input = normalizeUpdateProgressInput(input)
	if input.SourceKind == "" || input.SourceRef == "" {
		return ProgressUpdateResult{}, fmt.Errorf("source_kind and source_ref are required")
	}
	if !ValidProgressSourceKind(input.SourceKind) {
		return ProgressUpdateResult{}, fmt.Errorf("unsupported progress source_kind: %s", input.SourceKind)
	}
	if !ValidProgressStatus(input.Status) {
		return ProgressUpdateResult{}, fmt.Errorf("unsupported progress status: %s", input.Status)
	}
	if !ValidProgressSeverity(input.Severity) {
		return ProgressUpdateResult{}, fmt.Errorf("unsupported progress severity: %s", input.Severity)
	}
	if err := ValidateObjectJSON(input.Payload, "payload"); err != nil {
		return ProgressUpdateResult{}, err
	}
	if err := ValidateObjectJSON(input.Metadata, "metadata"); err != nil {
		return ProgressUpdateResult{}, err
	}
	scopeID := req.ScopeID
	var err error
	if input.ScopeRef != "" {
		scopeID, err = resolveScopeID(ctx, s.DB, input.ScopeRef)
		if err != nil {
			return ProgressUpdateResult{}, err
		}
	}
	topicID := ""
	if input.TopicRef != "" {
		topic, err := getTopic(ctx, s.DB, input.TopicRef, false)
		if err != nil {
			return ProgressUpdateResult{}, err
		}
		topicID = topic.TopicID
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ProgressUpdateResult{}, err
	}
	defer tx.Rollback()

	feed, err := ensureProgressFeedTx(ctx, tx, EnsureProgressFeedInput{
		SourceKind: input.SourceKind,
		SourceRef:  input.SourceRef,
		ScopeRef:   input.ScopeRef,
		TopicRef:   input.TopicRef,
		Status:     input.Status,
		Stage:      input.Stage,
		Message:    input.Message,
		Metadata:   input.Metadata,
	}, scopeID, topicID)
	if err != nil {
		return ProgressUpdateResult{}, err
	}
	sequence := feed.LastSequence + 1
	closedAtExpr := "closed_at"
	if isTerminalProgressStatus(input.Status) {
		closedAtExpr = "COALESCE(closed_at, now())"
	}
	updatedFeed, err := scanProgressFeed(tx.QueryRowContext(ctx, progressFeedSelectSQL(fmt.Sprintf(`
		UPDATE realtime.progress_feeds
		SET current_status = $2,
		    current_stage = $3,
		    current_message = $4,
		    current_value = $5,
		    total_value = $6,
		    last_sequence = $7,
		    updated_at = now(),
		    closed_at = %s
		WHERE progress_feed_id = $1
	`, closedAtExpr)),
		feed.ProgressFeedID,
		input.Status,
		input.Stage,
		input.Message,
		input.ProgressValue,
		input.TotalValue,
		sequence,
	))
	if err != nil {
		return ProgressUpdateResult{}, err
	}
	update, err := scanProgressUpdate(tx.QueryRowContext(ctx, progressUpdateSelectSQL(`
		INSERT INTO realtime.progress_updates (
			progress_update_id, progress_feed_id, source_kind, source_ref, sequence,
			stage, status, message, payload_json, progress_value, total_value,
			severity, correlation_id, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`),
		ids.NewProgressUpdateID(),
		updatedFeed.ProgressFeedID,
		input.SourceKind,
		input.SourceRef,
		sequence,
		input.Stage,
		input.Status,
		input.Message,
		objectOrDefault(input.Payload),
		input.ProgressValue,
		input.TotalValue,
		input.Severity,
		req.CorrelationID,
		objectOrDefault(input.Metadata),
	))
	if err != nil {
		return ProgressUpdateResult{}, err
	}
	eventType := events.TypeRealtimeProgressUpdated
	result := "progress_updated"
	if isTerminalProgressStatus(input.Status) {
		eventType = events.TypeRealtimeProgressClosed
		result = "progress_closed"
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  eventType,
		EventLevel: "node_activity",
		Request:    req,
		ScopeID:    pointerValue(updatedFeed.ScopeID),
		TargetKind: "progress_feed",
		TargetID:   updatedFeed.ProgressFeedID,
		Status:     updatedFeed.CurrentStatus,
		Result:     result,
		Payload: map[string]any{
			"progress_feed_id":   updatedFeed.ProgressFeedID,
			"progress_update_id": update.ProgressUpdateID,
			"source_kind":        updatedFeed.SourceKind,
			"source_ref":         updatedFeed.SourceRef,
			"sequence":           update.Sequence,
			"stage":              update.Stage,
			"status":             update.Status,
			"message":            update.Message,
		},
		VisibilityClass: "internal",
	}); err != nil {
		return ProgressUpdateResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProgressUpdateResult{}, err
	}
	return ProgressUpdateResult{Feed: updatedFeed, Update: update}, nil
}

func (s Service) GetProgress(ctx context.Context, source string, limit int) (ProgressDetail, error) {
	sourceKind, sourceRef, err := parseColonRef(source)
	if err != nil {
		return ProgressDetail{}, err
	}
	if !ValidProgressSourceKind(sourceKind) {
		return ProgressDetail{}, fmt.Errorf("unsupported progress source_kind: %s", sourceKind)
	}
	feed, err := getProgressFeed(ctx, s.DB, sourceKind, sourceRef, false)
	if err != nil {
		return ProgressDetail{}, err
	}
	updates, err := s.ListProgressUpdates(ctx, source, limit)
	if err != nil {
		return ProgressDetail{}, err
	}
	return ProgressDetail{Feed: feed, Updates: updates}, nil
}

func (s Service) ListProgressUpdates(ctx context.Context, source string, limit int) ([]ProgressUpdate, error) {
	sourceKind, sourceRef, err := parseColonRef(source)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT *
		FROM (
	`+progressUpdateSelectSQL()+`
			WHERE source_kind = $1 AND source_ref = $2
			ORDER BY sequence DESC
			LIMIT $3
		) recent
		ORDER BY sequence ASC
	`, sourceKind, sourceRef, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	updates := []ProgressUpdate{}
	for rows.Next() {
		update, err := scanProgressUpdate(rows)
		if err != nil {
			return nil, err
		}
		updates = append(updates, update)
	}
	return updates, rows.Err()
}

func (s Service) CloseProgressFeed(ctx context.Context, req requestctx.Context, source string) (ProgressFeed, error) {
	sourceKind, sourceRef, err := parseColonRef(source)
	if err != nil {
		return ProgressFeed{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ProgressFeed{}, err
	}
	defer tx.Rollback()
	feed, err := getProgressFeed(ctx, tx, sourceKind, sourceRef, true)
	if err != nil {
		return ProgressFeed{}, err
	}
	if feed.ClosedAt != nil {
		if err := tx.Commit(); err != nil {
			return ProgressFeed{}, err
		}
		return feed, nil
	}
	closed, err := scanProgressFeed(tx.QueryRowContext(ctx, progressFeedSelectSQL(`
		UPDATE realtime.progress_feeds
		SET current_status = $2,
		    updated_at = now(),
		    closed_at = now()
		WHERE progress_feed_id = $1
	`), feed.ProgressFeedID, ProgressStatusClosed))
	if err != nil {
		return ProgressFeed{}, err
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  events.TypeRealtimeProgressClosed,
		EventLevel: "node_activity",
		Request:    req,
		ScopeID:    pointerValue(closed.ScopeID),
		TargetKind: "progress_feed",
		TargetID:   closed.ProgressFeedID,
		Status:     closed.CurrentStatus,
		Result:     "progress_closed",
		Payload: map[string]any{
			"progress_feed_id": closed.ProgressFeedID,
			"source_kind":      closed.SourceKind,
			"source_ref":       closed.SourceRef,
		},
		VisibilityClass: "internal",
	}); err != nil {
		return ProgressFeed{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProgressFeed{}, err
	}
	return closed, nil
}

func (s Service) RequestLease(ctx context.Context, req requestctx.Context, input RequestLeaseInput) (Lease, error) {
	input = normalizeRequestLeaseInput(req, input)
	if input.ResourceKind == "" || input.ResourceRef == "" {
		return Lease{}, fmt.Errorf("resource is required")
	}
	if !ValidLeaseHolderKind(input.HolderKind) {
		return Lease{}, fmt.Errorf("unsupported lease holder_kind: %s", input.HolderKind)
	}
	if !ValidLeaseMode(input.LeaseMode) {
		return Lease{}, fmt.Errorf("unsupported lease mode: %s", input.LeaseMode)
	}
	if input.ExpiresAt == nil {
		return Lease{}, fmt.Errorf("lease expires_at or duration_seconds is required")
	}
	if !input.ExpiresAt.After(time.Now().UTC()) {
		return Lease{}, fmt.Errorf("lease expiration must be in the future")
	}
	if err := ValidateObjectJSON(input.Metadata, "metadata"); err != nil {
		return Lease{}, err
	}

	scopeID := req.ScopeID
	var err error
	if input.ScopeRef != "" {
		scopeID, err = resolveScopeID(ctx, s.DB, input.ScopeRef)
		if err != nil {
			return Lease{}, err
		}
	}
	policyDecisionID, err := optionalResolve(ctx, s.DB, input.PolicyDecisionRef, resolvePolicyDecisionID)
	if err != nil {
		return Lease{}, err
	}
	approvalID, err := optionalResolve(ctx, s.DB, input.ApprovalRef, resolveApprovalID)
	if err != nil {
		return Lease{}, err
	}
	routeID, err := optionalResolve(ctx, s.DB, input.RouteRef, resolveRouteID)
	if err != nil {
		return Lease{}, err
	}
	capabilityCallID, err := optionalResolve(ctx, s.DB, input.CapabilityCallRef, resolveCapabilityCallID)
	if err != nil {
		return Lease{}, err
	}
	grantID, err := optionalResolve(ctx, s.DB, input.GrantRef, resolveGrantID)
	if err != nil {
		return Lease{}, err
	}
	holderActorID := ""
	nodeID := ""
	providerID := ""
	switch input.HolderKind {
	case LeaseHolderKindActor:
		holderActorID, err = optionalResolve(ctx, s.DB, input.HolderRef, resolveActorID)
	case LeaseHolderKindNode:
		nodeID, err = optionalResolve(ctx, s.DB, input.HolderRef, resolveNodeID)
	case LeaseHolderKindProvider:
		providerID, err = optionalResolve(ctx, s.DB, input.HolderRef, resolveProviderID)
	}
	if err != nil {
		return Lease{}, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Lease{}, err
	}
	defer tx.Rollback()

	conflicts, err := activeLeaseConflictsTx(ctx, tx, input.ResourceKind, input.ResourceRef, input.LeaseMode)
	if err != nil {
		return Lease{}, err
	}
	if len(conflicts) > 0 {
		if _, appendErr := events.AppendTx(ctx, tx, events.AppendInput{
			EventType:  events.TypeRealtimeLeaseConflict,
			EventLevel: "audit",
			Request:    req,
			ScopeID:    scopeID,
			TargetKind: "lease_resource",
			TargetID:   input.ResourceKind + ":" + input.ResourceRef,
			Status:     LeaseStatusFailed,
			Result:     "lease_conflict",
			Payload: map[string]any{
				"resource_kind":      input.ResourceKind,
				"resource_ref":       input.ResourceRef,
				"requested_mode":     input.LeaseMode,
				"conflicting_leases": conflicts,
			},
			VisibilityClass: "internal",
		}); appendErr != nil {
			return Lease{}, appendErr
		}
		if err := tx.Commit(); err != nil {
			return Lease{}, err
		}
		return Lease{}, fmt.Errorf("lease conflict on %s:%s", input.ResourceKind, input.ResourceRef)
	}

	lease, err := scanLease(tx.QueryRowContext(ctx, leaseSelectSQL(`
		INSERT INTO realtime.leases (
			lease_id, resource_kind, resource_ref, holder_kind, holder_ref,
			holder_actor_id, node_id, provider_id, scope_id, route_id,
			capability_call_id, authorization_ref, policy_decision_id,
			approval_id, grant_id, lease_mode, status, starts_at, expires_at,
			metadata
		)
		VALUES (
			$1, $2, $3, $4, $5, nullif($6, ''), nullif($7, ''),
			nullif($8, ''), nullif($9, ''), nullif($10, ''), nullif($11, ''),
			$12, nullif($13, ''), nullif($14, ''), nullif($15, ''),
			$16, $17, now(), $18, $19
		)
	`),
		ids.NewLeaseID(),
		input.ResourceKind,
		input.ResourceRef,
		input.HolderKind,
		input.HolderRef,
		holderActorID,
		nodeID,
		providerID,
		scopeID,
		routeID,
		capabilityCallID,
		input.AuthorizationRef,
		policyDecisionID,
		approvalID,
		grantID,
		input.LeaseMode,
		LeaseStatusGranted,
		input.ExpiresAt,
		objectOrDefault(input.Metadata),
	))
	if err != nil {
		return Lease{}, err
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  events.TypeRealtimeLeaseGranted,
		EventLevel: "audit",
		Request:    req,
		ScopeID:    pointerValue(lease.ScopeID),
		TargetKind: "lease",
		TargetID:   lease.LeaseID,
		Status:     lease.Status,
		Result:     "lease_granted",
		Payload: map[string]any{
			"lease_id":      lease.LeaseID,
			"resource_kind": lease.ResourceKind,
			"resource_ref":  lease.ResourceRef,
			"lease_mode":    lease.LeaseMode,
			"holder_kind":   lease.HolderKind,
			"holder_ref":    lease.HolderRef,
			"expires_at":    lease.ExpiresAt.UTC().Format(time.RFC3339),
		},
		VisibilityClass: "internal",
	}); err != nil {
		return Lease{}, err
	}
	if err := tx.Commit(); err != nil {
		return Lease{}, err
	}
	return lease, nil
}

func (s Service) ListLeases(ctx context.Context, filter LeaseFilter) ([]Lease, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	if status := strings.TrimSpace(filter.Status); status != "" && !ValidLeaseStatus(status) {
		return nil, fmt.Errorf("unsupported lease status filter: %s", status)
	}
	query := leaseSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		add("status =", status)
	}
	if resource := strings.TrimSpace(filter.Resource); resource != "" {
		kind, ref, err := parseColonRef(resource)
		if err != nil {
			return nil, err
		}
		add("resource_kind =", kind)
		add("resource_ref =", ref)
	}
	if holderKind := strings.TrimSpace(filter.HolderKind); holderKind != "" {
		add("holder_kind =", holderKind)
	}
	if holderRef := strings.TrimSpace(filter.HolderRef); holderRef != "" {
		add("holder_ref =", holderRef)
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY updated_at DESC LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	leases := []Lease{}
	for rows.Next() {
		lease, err := scanLease(rows)
		if err != nil {
			return nil, err
		}
		leases = append(leases, lease)
	}
	return leases, rows.Err()
}

func (s Service) GetLease(ctx context.Context, ref string) (Lease, error) {
	return getLease(ctx, s.DB, ref, false)
}

func (s Service) ReleaseLease(ctx context.Context, req requestctx.Context, ref string, input ReleaseLeaseInput) (Lease, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Lease{}, err
	}
	defer tx.Rollback()
	lease, err := getLease(ctx, tx, ref, true)
	if err != nil {
		return Lease{}, err
	}
	if lease.Status != LeaseStatusGranted {
		return Lease{}, fmt.Errorf("lease is not active: %s", lease.Status)
	}
	reason := strings.TrimSpace(input.ReleaseReason)
	updated, err := scanLease(tx.QueryRowContext(ctx, leaseSelectSQL(`
		UPDATE realtime.leases
		SET status = $2,
		    released_at = now(),
		    release_reason = $3,
		    updated_at = now()
		WHERE lease_id = $1
	`), lease.LeaseID, LeaseStatusReleased, reason))
	if err != nil {
		return Lease{}, err
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  events.TypeRealtimeLeaseReleased,
		EventLevel: "audit",
		Request:    req,
		ScopeID:    pointerValue(updated.ScopeID),
		TargetKind: "lease",
		TargetID:   updated.LeaseID,
		Status:     updated.Status,
		Result:     "lease_released",
		Payload: map[string]any{
			"lease_id":       updated.LeaseID,
			"resource_kind":  updated.ResourceKind,
			"resource_ref":   updated.ResourceRef,
			"release_reason": updated.ReleaseReason,
		},
		VisibilityClass: "internal",
	}); err != nil {
		return Lease{}, err
	}
	if err := tx.Commit(); err != nil {
		return Lease{}, err
	}
	return updated, nil
}

func (s Service) ExpireLeases(ctx context.Context, req requestctx.Context, now time.Time) (LeaseExpirationResult, error) {
	now = now.UTC()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return LeaseExpirationResult{}, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, leaseSelectSQL(`
		UPDATE realtime.leases
		SET status = $2,
		    updated_at = now()
		WHERE status = $3
		  AND expires_at <= $1
	`), now, LeaseStatusExpired, LeaseStatusGranted)
	if err != nil {
		return LeaseExpirationResult{}, err
	}
	leases := []Lease{}
	for rows.Next() {
		lease, err := scanLease(rows)
		if err != nil {
			rows.Close()
			return LeaseExpirationResult{}, err
		}
		leases = append(leases, lease)
	}
	if err := rows.Close(); err != nil {
		return LeaseExpirationResult{}, err
	}
	for _, lease := range leases {
		if _, err := events.AppendTx(ctx, tx, events.AppendInput{
			EventType:  events.TypeRealtimeLeaseExpired,
			EventLevel: "audit",
			Request:    req,
			ScopeID:    pointerValue(lease.ScopeID),
			TargetKind: "lease",
			TargetID:   lease.LeaseID,
			Status:     lease.Status,
			Result:     "lease_expired",
			Payload: map[string]any{
				"lease_id":      lease.LeaseID,
				"resource_kind": lease.ResourceKind,
				"resource_ref":  lease.ResourceRef,
				"lease_mode":    lease.LeaseMode,
				"expires_at":    lease.ExpiresAt.UTC().Format(time.RFC3339),
			},
			VisibilityClass: "internal",
		}); err != nil {
			return LeaseExpirationResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return LeaseExpirationResult{}, err
	}
	return LeaseExpirationResult{LeasesExpired: len(leases)}, nil
}

func (s Service) ExpireSubscriptions(ctx context.Context, now time.Time) (SubscriptionExpirationResult, error) {
	now = now.UTC()
	result, err := s.DB.ExecContext(ctx, `
		UPDATE realtime.subscriptions
		SET status = $2,
		    updated_at = now()
		WHERE status = $3
		  AND expires_at IS NOT NULL
		  AND expires_at <= $1
	`, now, SubscriptionStatusExpired, SubscriptionStatusActive)
	if err != nil {
		return SubscriptionExpirationResult{}, err
	}
	count, _ := result.RowsAffected()
	return SubscriptionExpirationResult{SubscriptionsExpired: int(count)}, nil
}

func (s Service) MarkStalePresence(ctx context.Context, req requestctx.Context, now time.Time) (PresenceExpirationResult, error) {
	now = now.UTC()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return PresenceExpirationResult{}, err
	}
	defer tx.Rollback()
	// Hold the original heartbeat binding through expiry and node reconciliation.
	// Concurrent expiry passes acquire presence rows in the same order.
	rows, err := tx.QueryContext(ctx, presenceSelectSQL()+`
		WHERE expires_at <= $1
		  AND state IN ('online', 'recently_seen', 'degraded', 'unknown')
		ORDER BY presence_id
		FOR UPDATE
	`, now)
	if err != nil {
		return PresenceExpirationResult{}, err
	}
	records := []Presence{}
	for rows.Next() {
		presence, err := scanPresence(rows)
		if err != nil {
			rows.Close()
			return PresenceExpirationResult{}, err
		}
		records = append(records, presence)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return PresenceExpirationResult{}, err
	}
	if err := rows.Close(); err != nil {
		return PresenceExpirationResult{}, err
	}
	for _, presence := range records {
		if err := expireNodePresenceTx(ctx, tx, req, presence); err != nil {
			return PresenceExpirationResult{}, err
		}
		expired, err := scanPresence(tx.QueryRowContext(ctx, presenceSelectSQL(`
			UPDATE realtime.presence
			SET state = $2,
			    source_kind = $3,
			    source_ref = $4,
			    status_detail = CASE WHEN status_detail = '' THEN 'presence heartbeat expired' ELSE status_detail END,
			    updated_at = now()
			WHERE presence_id = $1
		`), presence.PresenceID, PresenceStateStale, PresenceSourceKindExpiryWorker, "expiry_worker"))
		if err != nil {
			return PresenceExpirationResult{}, err
		}
		if err := appendPresenceChangedEvent(ctx, tx, req, expired, "expired"); err != nil {
			return PresenceExpirationResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return PresenceExpirationResult{}, err
	}
	return PresenceExpirationResult{PresenceMarkedStale: len(records)}, nil
}

func expireNodePresenceTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, presence Presence) error {
	if presence.SubjectKind != PresenceSubjectKindNode || presence.NodeID == nil ||
		presence.SubjectRef != *presence.NodeID || presence.SourceKind != PresenceSourceKindHeartbeat || presence.SourceRef == "" {
		return nil
	}
	var previous string
	err := tx.QueryRowContext(ctx, `
		SELECT presence_state FROM nodes.nodes
		WHERE node_id = $1 AND last_heartbeat_at = $2
		  AND status = 'active' AND enrollment_status = 'approved' AND credential_status = 'active'
		  AND presence_state IN ('online', 'recently_seen', 'degraded', 'unknown')
		FOR UPDATE
	`, *presence.NodeID, presence.LastSeenAt).Scan(&previous)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	// Ingestion commits the node before projecting realtime presence. Re-read
	// heartbeat identity after acquiring the node lock, including after a wait.
	var current bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM nodes.heartbeats h
			WHERE h.node_id = $1 AND h.node_heartbeat_id = $2 AND h.received_at = $3
			  AND NOT EXISTS (
				SELECT 1 FROM nodes.heartbeats newer
				WHERE newer.node_id = h.node_id AND newer.received_at >= h.received_at
				  AND newer.node_heartbeat_id <> h.node_heartbeat_id
			  )
		)
	`, *presence.NodeID, presence.SourceRef, presence.LastSeenAt).Scan(&current); err != nil {
		return err
	}
	if !current {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE nodes.nodes SET presence_state = $2, updated_at = now() WHERE node_id = $1`,
		*presence.NodeID, nodes.PresenceOffline); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO nodes.node_status_history (
			node_status_history_id, node_id, previous_presence_state, next_presence_state,
			reason_code, heartbeat_id, metadata
		) VALUES ($1, $2, $3, $4, 'heartbeat_expired', $5, jsonb_build_object('presence_id', $6::text))
	`, "node_status_history_"+ids.NewEventID(), *presence.NodeID, previous, nodes.PresenceOffline, presence.SourceRef, presence.PresenceID); err != nil {
		return err
	}
	_, err = events.AppendTx(ctx, tx, events.AppendInput{
		EventType: events.TypeNodePresenceChanged, EventLevel: "audit", Request: req,
		ScopeID: req.ScopeID, TargetKind: "node", TargetID: *presence.NodeID,
		Status: nodes.PresenceOffline, Result: "presence_changed",
		Payload: map[string]any{
			"node_id": *presence.NodeID, "previous_presence_state": previous,
			"next_presence_state": nodes.PresenceOffline, "heartbeat_id": presence.SourceRef,
			"presence_id": presence.PresenceID, "reason_code": "heartbeat_expired",
		},
		VisibilityClass: "internal",
	})
	return err
}

func (s Service) CloseTerminalProgressFeeds(ctx context.Context, req requestctx.Context) (ProgressCloseResult, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ProgressCloseResult{}, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, progressFeedSelectSQL(`
		UPDATE realtime.progress_feeds
		SET closed_at = now(),
		    updated_at = now()
		WHERE closed_at IS NULL
		  AND current_status IN ('succeeded', 'failed', 'cancelled', 'closed')
	`))
	if err != nil {
		return ProgressCloseResult{}, err
	}
	feeds := []ProgressFeed{}
	for rows.Next() {
		feed, err := scanProgressFeed(rows)
		if err != nil {
			rows.Close()
			return ProgressCloseResult{}, err
		}
		feeds = append(feeds, feed)
	}
	if err := rows.Close(); err != nil {
		return ProgressCloseResult{}, err
	}
	for _, feed := range feeds {
		if _, err := events.AppendTx(ctx, tx, events.AppendInput{
			EventType:  events.TypeRealtimeProgressClosed,
			EventLevel: "node_activity",
			Request:    req,
			ScopeID:    pointerValue(feed.ScopeID),
			TargetKind: "progress_feed",
			TargetID:   feed.ProgressFeedID,
			Status:     feed.CurrentStatus,
			Result:     "progress_closed",
			Payload: map[string]any{
				"progress_feed_id": feed.ProgressFeedID,
				"source_kind":      feed.SourceKind,
				"source_ref":       feed.SourceRef,
			},
			VisibilityClass: "internal",
		}); err != nil {
			return ProgressCloseResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ProgressCloseResult{}, err
	}
	return ProgressCloseResult{ProgressFeedsClosed: len(feeds)}, nil
}

func normalizeEnsureProgressFeedInput(input EnsureProgressFeedInput) EnsureProgressFeedInput {
	input.SourceKind = strings.TrimSpace(input.SourceKind)
	input.SourceRef = strings.TrimSpace(input.SourceRef)
	input.ScopeRef = strings.TrimSpace(input.ScopeRef)
	input.TopicRef = strings.TrimSpace(input.TopicRef)
	input.Status = defaultString(input.Status, ProgressStatusPending)
	input.Stage = strings.TrimSpace(input.Stage)
	input.Message = strings.TrimSpace(input.Message)
	input.Metadata = objectOrDefault(input.Metadata)
	return input
}

func normalizeUpdateProgressInput(input UpdateProgressInput) UpdateProgressInput {
	input.SourceKind = strings.TrimSpace(input.SourceKind)
	input.SourceRef = strings.TrimSpace(input.SourceRef)
	input.ScopeRef = strings.TrimSpace(input.ScopeRef)
	input.TopicRef = strings.TrimSpace(input.TopicRef)
	input.Status = defaultString(input.Status, ProgressStatusRunning)
	input.Stage = strings.TrimSpace(input.Stage)
	input.Message = strings.TrimSpace(input.Message)
	if len(strings.TrimSpace(string(input.Payload))) == 0 {
		input.Payload = input.PayloadJSON
	}
	input.Payload = objectOrDefault(input.Payload)
	input.Severity = defaultString(input.Severity, ProgressSeverityNormal)
	input.Metadata = objectOrDefault(input.Metadata)
	return input
}

func normalizeRequestLeaseInput(req requestctx.Context, input RequestLeaseInput) RequestLeaseInput {
	input.Resource = strings.TrimSpace(input.Resource)
	input.ResourceKind = strings.TrimSpace(input.ResourceKind)
	input.ResourceRef = strings.TrimSpace(input.ResourceRef)
	if input.ResourceKind == "" || input.ResourceRef == "" {
		input.ResourceKind, input.ResourceRef, _ = parseColonRef(input.Resource)
	}
	input.HolderKind = defaultString(input.HolderKind, LeaseHolderKindActor)
	input.HolderRef = strings.TrimSpace(input.HolderRef)
	if input.HolderRef == "" {
		switch input.HolderKind {
		case LeaseHolderKindNode:
			input.HolderRef = req.OriginNodeID
		default:
			input.HolderRef = req.ActorID
		}
	}
	input.ScopeRef = strings.TrimSpace(input.ScopeRef)
	input.RouteRef = strings.TrimSpace(input.RouteRef)
	input.CapabilityCallRef = strings.TrimSpace(input.CapabilityCallRef)
	input.PolicyDecisionRef = strings.TrimSpace(input.PolicyDecisionRef)
	input.ApprovalRef = strings.TrimSpace(input.ApprovalRef)
	input.GrantRef = strings.TrimSpace(input.GrantRef)
	input.AuthorizationRef = strings.TrimSpace(input.AuthorizationRef)
	input.LeaseMode = defaultString(input.LeaseMode, LeaseModeExclusive)
	if input.ExpiresAt == nil && input.DurationSeconds > 0 {
		expiresAt := time.Now().UTC().Add(time.Duration(input.DurationSeconds) * time.Second)
		input.ExpiresAt = &expiresAt
	}
	if input.ExpiresAt != nil {
		expiresAt := input.ExpiresAt.UTC()
		input.ExpiresAt = &expiresAt
	}
	input.Metadata = objectOrDefault(input.Metadata)
	return input
}

func ensureProgressFeedTx(ctx context.Context, tx *sql.Tx, input EnsureProgressFeedInput, scopeID, topicID string) (ProgressFeed, error) {
	existing, err := getProgressFeed(ctx, tx, input.SourceKind, input.SourceRef, true)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ProgressFeed{}, err
	}
	return scanProgressFeed(tx.QueryRowContext(ctx, progressFeedSelectSQL(`
		INSERT INTO realtime.progress_feeds (
			progress_feed_id, source_kind, source_ref, scope_id, topic_id,
			current_status, current_stage, current_message, metadata
		)
		VALUES ($1, $2, $3, nullif($4, ''), nullif($5, ''), $6, $7, $8, $9)
	`),
		ids.NewProgressFeedID(),
		input.SourceKind,
		input.SourceRef,
		scopeID,
		topicID,
		input.Status,
		input.Stage,
		input.Message,
		objectOrDefault(input.Metadata),
	))
}

func getProgressFeed(ctx context.Context, q queryer, sourceKind, sourceRef string, forUpdate bool) (ProgressFeed, error) {
	sourceKind = strings.TrimSpace(sourceKind)
	sourceRef = strings.TrimSpace(sourceRef)
	if sourceKind == "" || sourceRef == "" {
		return ProgressFeed{}, fmt.Errorf("progress source is required")
	}
	query := progressFeedSelectSQL() + ` WHERE source_kind = $1 AND source_ref = $2`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	return scanProgressFeed(q.QueryRowContext(ctx, query, sourceKind, sourceRef))
}

func getLease(ctx context.Context, q queryer, ref string, forUpdate bool) (Lease, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Lease{}, fmt.Errorf("lease ref is required")
	}
	query := leaseSelectSQL() + ` WHERE lease_id = $1`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	return scanLease(q.QueryRowContext(ctx, query, ref))
}

func activeLeaseConflictsTx(ctx context.Context, tx *sql.Tx, resourceKind, resourceRef, requestedMode string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT lease_id, lease_mode
		FROM realtime.leases
		WHERE resource_kind = $1
		  AND resource_ref = $2
		  AND status = $3
		  AND expires_at > now()
		FOR UPDATE
	`, resourceKind, resourceRef, LeaseStatusGranted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	conflicts := []string{}
	for rows.Next() {
		var leaseID, existingMode string
		if err := rows.Scan(&leaseID, &existingMode); err != nil {
			return nil, err
		}
		if leaseModesConflict(existingMode, requestedMode) {
			conflicts = append(conflicts, leaseID)
		}
	}
	return conflicts, rows.Err()
}

func leaseModesConflict(existingMode, requestedMode string) bool {
	if requestedMode == LeaseModeRead || requestedMode == LeaseModeShared {
		return existingMode == LeaseModeWrite || existingMode == LeaseModeControl || existingMode == LeaseModeExclusive
	}
	return true
}

func isTerminalProgressStatus(status string) bool {
	switch status {
	case ProgressStatusSucceeded, ProgressStatusFailed, ProgressStatusCancelled, ProgressStatusClosed:
		return true
	default:
		return false
	}
}

func parseColonRef(value string) (string, string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", fmt.Errorf("ref is required")
	}
	parts := strings.SplitN(value, ":", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("ref must use kind:ref syntax")
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
}

func optionalResolve(ctx context.Context, q queryer, ref string, resolve func(context.Context, queryer, string) (string, error)) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}
	return resolve(ctx, q, ref)
}

func normalizeCreateTopicInput(input CreateTopicInput) CreateTopicInput {
	input.TopicPath = strings.TrimSpace(input.TopicPath)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if input.DisplayName == "" {
		input.DisplayName = input.TopicPath
	}
	input.ScopeRef = strings.TrimSpace(input.ScopeRef)
	input.RetentionMode = defaultString(input.RetentionMode, RetentionModeRetainBounded)
	input.DeliveryClass = defaultString(input.DeliveryClass, DeliveryClassPolling)
	input.Metadata = objectOrDefault(input.Metadata)
	return input
}

func normalizePublishInput(input PublishInput) PublishInput {
	input.MessageType = strings.TrimSpace(input.MessageType)
	input.PayloadSchemaRef = strings.TrimSpace(input.PayloadSchemaRef)
	input.CausationRef = strings.TrimSpace(input.CausationRef)
	input.AuthorizationRef = strings.TrimSpace(input.AuthorizationRef)
	if len(strings.TrimSpace(string(input.Payload))) == 0 {
		input.Payload = input.PayloadJSON
	}
	input.Payload = objectOrDefault(input.Payload)
	input.Metadata = objectOrDefault(input.Metadata)
	return input
}

func normalizeCreateSubscriptionInput(input CreateSubscriptionInput) CreateSubscriptionInput {
	input.TopicRef = strings.TrimSpace(input.TopicRef)
	input.CursorMode = defaultString(input.CursorMode, SubscriptionCursorFromNow)
	if input.CursorMode == SubscriptionCursorFromLatest {
		input.CursorMode = SubscriptionCursorFromNow
	}
	if len(strings.TrimSpace(string(input.FilterJSON))) == 0 {
		input.FilterJSON = input.Filter
	}
	input.FilterJSON = objectOrDefault(input.FilterJSON)
	input.DeliveryTargetKind = defaultString(input.DeliveryTargetKind, DeliveryClassPolling)
	input.DeliveryTargetRef = strings.TrimSpace(input.DeliveryTargetRef)
	input.DeliveryMode = defaultString(input.DeliveryMode, DeliveryClassPolling)
	input.DeliveryClass = defaultString(input.DeliveryClass, DeliveryClassPolling)
	input.AuthorizationRef = strings.TrimSpace(input.AuthorizationRef)
	input.Metadata = objectOrDefault(input.Metadata)
	return input
}

func normalizeNodePresenceInput(input NodePresenceInput) NodePresenceInput {
	input.NodeID = strings.TrimSpace(input.NodeID)
	input.HeartbeatID = strings.TrimSpace(input.HeartbeatID)
	input.State = defaultString(input.State, PresenceStateOnline)
	if input.LastSeenAt.IsZero() {
		input.LastSeenAt = time.Now().UTC()
	}
	input.LastSeenAt = input.LastSeenAt.UTC()
	if input.ExpiresInSeconds <= 0 {
		input.ExpiresInSeconds = 90
	}
	input.StatusDetail = strings.TrimSpace(input.StatusDetail)
	input.Metadata = objectOrDefault(input.Metadata)
	return input
}

func normalizeCreateNotificationInput(input CreateNotificationInput) CreateNotificationInput {
	input.TargetKind = strings.TrimSpace(input.TargetKind)
	input.TargetRef = strings.TrimSpace(input.TargetRef)
	input.SourceKind = strings.TrimSpace(input.SourceKind)
	input.SourceRef = strings.TrimSpace(input.SourceRef)
	input.ScopeRef = strings.TrimSpace(input.ScopeRef)
	input.Category = strings.TrimSpace(input.Category)
	input.Priority = defaultString(input.Priority, NotificationPriorityNormal)
	input.Summary = strings.TrimSpace(input.Summary)
	if len(strings.TrimSpace(string(input.Payload))) == 0 {
		input.Payload = input.PayloadJSON
	}
	input.Payload = objectOrDefault(input.Payload)
	input.AuthorizationRef = strings.TrimSpace(input.AuthorizationRef)
	input.PolicyDecisionRef = strings.TrimSpace(input.PolicyDecisionRef)
	input.ApprovalRef = strings.TrimSpace(input.ApprovalRef)
	input.RouteRef = strings.TrimSpace(input.RouteRef)
	input.CapabilityCallRef = strings.TrimSpace(input.CapabilityCallRef)
	input.DeliveryMode = defaultString(input.DeliveryMode, DeliveryClassPolling)
	input.Metadata = objectOrDefault(input.Metadata)
	return input
}

func getTopic(ctx context.Context, q queryer, ref string, forUpdate bool) (Topic, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Topic{}, fmt.Errorf("topic ref is required")
	}
	query := topicSelectSQL() + ` WHERE topic_id = $1 OR topic_path = $1`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	return scanTopic(q.QueryRowContext(ctx, query, ref))
}

func getSubscription(ctx context.Context, q queryer, ref string, forUpdate bool) (Subscription, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Subscription{}, fmt.Errorf("subscription ref is required")
	}
	query := subscriptionSelectSQL() + ` WHERE subscription_id = $1`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	return scanSubscription(q.QueryRowContext(ctx, query, ref))
}

func getNotification(ctx context.Context, q queryer, ref string, forUpdate bool) (Notification, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Notification{}, fmt.Errorf("notification ref is required")
	}
	query := notificationSelectSQL() + ` WHERE notification_id = $1`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	return scanNotification(q.QueryRowContext(ctx, query, ref))
}

func insertNotificationTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, input CreateNotificationInput, scopeID, policyDecisionID, approvalID, routeID, capabilityCallID string) (Notification, error) {
	notification, err := scanNotification(tx.QueryRowContext(ctx, notificationSelectSQL(`
		INSERT INTO realtime.notifications (
			notification_id, target_kind, target_ref, source_kind, source_ref,
			scope_id, category, priority, summary, payload_json, authorization_ref,
			policy_decision_id, approval_id, route_id, capability_call_id, status,
			expires_at, metadata
		)
		VALUES (
			$1, $2, $3, $4, $5, nullif($6, ''), $7, $8, $9, $10, $11,
			nullif($12, ''), nullif($13, ''), nullif($14, ''), nullif($15, ''),
			$16, $17, $18
		)
	`),
		ids.NewNotificationID(),
		input.TargetKind,
		input.TargetRef,
		input.SourceKind,
		input.SourceRef,
		scopeID,
		input.Category,
		input.Priority,
		input.Summary,
		input.Payload,
		input.AuthorizationRef,
		policyDecisionID,
		approvalID,
		routeID,
		capabilityCallID,
		NotificationStatusCreated,
		input.ExpiresAt,
		objectOrDefault(input.Metadata),
	))
	if err != nil {
		return Notification{}, err
	}
	deliveryTargetKind := NotificationDeliveryTargetPolling
	if input.TargetKind == NotificationTargetKindActor {
		deliveryTargetKind = NotificationDeliveryTargetActorInbox
	}
	if input.TargetKind == NotificationTargetKindNode {
		deliveryTargetKind = NotificationDeliveryTargetNodeChannel
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO realtime.notification_deliveries (
			notification_delivery_id, notification_id, target_kind, target_ref,
			delivery_mode, status, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, ids.NewNotificationDeliveryID(), notification.NotificationID, deliveryTargetKind, input.TargetRef, input.DeliveryMode, NotificationDeliveryStatusPending, objectOrDefault(input.Metadata)); err != nil {
		return Notification{}, err
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  events.TypeRealtimeNotificationCreated,
		EventLevel: "audit",
		Request:    req,
		ScopeID:    scopeID,
		TargetKind: "notification",
		TargetID:   notification.NotificationID,
		Status:     notification.Status,
		Result:     "notification_created",
		Payload: map[string]any{
			"notification_id": notification.NotificationID,
			"category":        notification.Category,
			"priority":        notification.Priority,
			"target_kind":     notification.TargetKind,
			"target_ref":      notification.TargetRef,
			"approval_id":     pointerValue(notification.ApprovalID),
		},
		VisibilityClass: "internal",
	}); err != nil {
		return Notification{}, err
	}
	return notification, nil
}

func appendPresenceChangedEvent(ctx context.Context, tx *sql.Tx, req requestctx.Context, presence Presence, previousState string) error {
	_, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  events.TypeRealtimePresenceChanged,
		EventLevel: "node_activity",
		Request:    req,
		ScopeID:    req.ScopeID,
		TargetKind: "presence",
		TargetID:   presence.PresenceID,
		Status:     presence.State,
		Result:     "presence_changed",
		Payload: map[string]any{
			"presence_id":    presence.PresenceID,
			"subject_kind":   presence.SubjectKind,
			"subject_ref":    presence.SubjectRef,
			"node_id":        pointerValue(presence.NodeID),
			"previous_state": previousState,
			"next_state":     presence.State,
			"source_ref":     presence.SourceRef,
			"expires_at":     presence.ExpiresAt.UTC().Format(time.RFC3339),
		},
		VisibilityClass: "internal",
	})
	return err
}

func lookupApprovalForNotification(ctx context.Context, q queryer, ref string) (approvalNotificationRecord, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return approvalNotificationRecord{}, fmt.Errorf("approval ref is required")
	}
	var record approvalNotificationRecord
	var scopeID, policyDecisionID sql.NullString
	err := q.QueryRowContext(ctx, `
		SELECT approval_id, approval_key, requested_by_actor_id, scope_id,
		       operation, policy_decision_id, risk_level,
		       execution_authorization_level, action_summary, expires_at
		FROM policy.approvals
		WHERE approval_id = $1 OR approval_key = $1
	`, ref).Scan(
		&record.ApprovalID,
		&record.ApprovalKey,
		&record.RequestedByActorID,
		&scopeID,
		&record.Operation,
		&policyDecisionID,
		&record.RiskLevel,
		&record.ExecutionAuthorizationLevel,
		&record.ActionSummary,
		&record.ExpiresAt,
	)
	if err != nil {
		return approvalNotificationRecord{}, err
	}
	record.ScopeID = nullStringPtr(scopeID)
	record.PolicyDecisionID = nullStringPtr(policyDecisionID)
	return record, nil
}

type approvalNotificationRecord struct {
	ApprovalID                  string
	ApprovalKey                 string
	RequestedByActorID          string
	ScopeID                     *string
	Operation                   string
	PolicyDecisionID            *string
	RiskLevel                   string
	ExecutionAuthorizationLevel int
	ActionSummary               string
	ExpiresAt                   time.Time
}

func resolveOwnerActorID(ctx context.Context, q queryer) (string, error) {
	var id string
	err := q.QueryRowContext(ctx, `
		SELECT actor_id
		FROM identity.actors
		WHERE actor_key = 'owner'
	`).Scan(&id)
	return id, err
}

func resolveScopeID(ctx context.Context, q queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("scope ref is required")
	}
	var id string
	err := q.QueryRowContext(ctx, `
		SELECT scope_id
		FROM scopes.scopes
		WHERE scope_id = $1 OR scope_key = $1 OR slug = $1
	`, ref).Scan(&id)
	return id, err
}

func resolvePolicyDecisionID(ctx context.Context, q queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("policy decision ref is required")
	}
	var id string
	err := q.QueryRowContext(ctx, `
		SELECT policy_decision_id
		FROM policy.decisions
		WHERE policy_decision_id = $1 OR decision_key = $1
	`, ref).Scan(&id)
	return id, err
}

func resolveApprovalID(ctx context.Context, q queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("approval ref is required")
	}
	var id string
	err := q.QueryRowContext(ctx, `
		SELECT approval_id
		FROM policy.approvals
		WHERE approval_id = $1 OR approval_key = $1
	`, ref).Scan(&id)
	return id, err
}

func resolveRouteID(ctx context.Context, q queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("route ref is required")
	}
	var id string
	err := q.QueryRowContext(ctx, `
		SELECT route_id
		FROM routing.routes
		WHERE route_id = $1 OR route_key = $1
	`, ref).Scan(&id)
	return id, err
}

func resolveCapabilityCallID(ctx context.Context, q queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("capability call ref is required")
	}
	var id string
	err := q.QueryRowContext(ctx, `
		SELECT capability_call_id
		FROM routing.capability_calls
		WHERE capability_call_id = $1 OR capability_call_key = $1
	`, ref).Scan(&id)
	return id, err
}

func resolveActorID(ctx context.Context, q queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("actor ref is required")
	}
	var id string
	err := q.QueryRowContext(ctx, `
		SELECT actor_id
		FROM identity.actors
		WHERE actor_id = $1 OR actor_key = $1
	`, ref).Scan(&id)
	return id, err
}

func resolveNodeID(ctx context.Context, q queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("node ref is required")
	}
	var id string
	err := q.QueryRowContext(ctx, `
		SELECT node_id
		FROM nodes.nodes
		WHERE node_id = $1 OR node_key = $1
	`, ref).Scan(&id)
	return id, err
}

func resolveProviderID(ctx context.Context, q queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("provider ref is required")
	}
	var id string
	err := q.QueryRowContext(ctx, `
		SELECT provider_id
		FROM capabilities.providers
		WHERE provider_id = $1 OR provider_key = $1 OR compact_address = $1
	`, ref).Scan(&id)
	return id, err
}

func resolveGrantID(ctx context.Context, q queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("grant ref is required")
	}
	var id string
	err := q.QueryRowContext(ctx, `
		SELECT grant_id
		FROM policy.grants
		WHERE grant_id = $1 OR grant_key = $1
	`, ref).Scan(&id)
	return id, err
}

func topicSelectSQL(prefix ...string) string {
	columns := `
		topic_id, topic_path, display_name, scope_id, owner_actor_id,
		created_by_actor_id, publisher_policy_id, subscriber_policy_id,
		retention_mode, delivery_class, ordering_mode, status, last_sequence,
		latest_payload_json, created_at, updated_at, closed_at, metadata
	`
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + ` RETURNING ` + columns
	}
	return `SELECT ` + columns + ` FROM realtime.topics`
}

func topicPublicationSelectSQL(prefix ...string) string {
	columns := `
		topic_publication_id, topic_id, sequence, publisher_actor_id,
		publisher_node_id, publisher_provider_id, message_type, payload_json,
		payload_schema_ref, correlation_id, causation_ref, authorization_ref,
		policy_decision_id, durability_mode, created_at, metadata
	`
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + ` RETURNING ` + columns
	}
	return `SELECT ` + columns + ` FROM realtime.topic_publications`
}

func subscriptionSelectSQL(prefix ...string) string {
	columns := `
		subscription_id, topic_id, source_kind, source_ref, subscriber_actor_id,
		subscriber_node_id, scope_id, filter_json, cursor_sequence,
		last_acknowledged_sequence, delivery_target_kind, delivery_target_ref,
		delivery_mode, delivery_class, authorization_ref, policy_decision_id,
		status, expires_at, created_at, updated_at, last_delivered_at,
		last_acknowledged_at, metadata
	`
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + ` RETURNING ` + columns
	}
	return `SELECT ` + columns + ` FROM realtime.subscriptions`
}

func presenceSelectSQL(prefix ...string) string {
	columns := `
		presence_id, subject_kind, subject_ref, node_id, state, source_kind,
		source_ref, last_seen_at, expires_at, confidence, status_detail,
		metadata, updated_at
	`
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + ` RETURNING ` + columns
	}
	return `SELECT ` + columns + ` FROM realtime.presence`
}

func notificationSelectSQL(prefix ...string) string {
	columns := `
		notification_id, target_kind, target_ref, source_kind, source_ref,
		scope_id, category, priority, summary, payload_json, authorization_ref,
		policy_decision_id, approval_id, route_id, capability_call_id, status,
		expires_at, acknowledged_at, dismissed_at, created_at, updated_at, metadata
	`
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + ` RETURNING ` + columns
	}
	return `SELECT ` + columns + ` FROM realtime.notifications`
}

func progressFeedSelectSQL(prefix ...string) string {
	columns := `
		progress_feed_id, source_kind, source_ref, scope_id, topic_id,
		current_status, current_stage, current_message, current_value,
		total_value, last_sequence, created_at, updated_at, closed_at, metadata
	`
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + ` RETURNING ` + columns
	}
	return `SELECT ` + columns + ` FROM realtime.progress_feeds`
}

func progressUpdateSelectSQL(prefix ...string) string {
	columns := `
		progress_update_id, progress_feed_id, source_kind, source_ref,
		sequence, stage, status, message, payload_json, progress_value,
		total_value, severity, correlation_id, created_at, metadata
	`
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + ` RETURNING ` + columns
	}
	return `SELECT ` + columns + ` FROM realtime.progress_updates`
}

func leaseSelectSQL(prefix ...string) string {
	columns := `
		lease_id, resource_kind, resource_ref, holder_kind, holder_ref,
		holder_actor_id, node_id, provider_id, scope_id, route_id,
		capability_call_id, authorization_ref, policy_decision_id,
		approval_id, grant_id, lease_mode, status, starts_at, expires_at,
		released_at, release_reason, created_at, updated_at, metadata
	`
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + ` RETURNING ` + columns
	}
	return `SELECT ` + columns + ` FROM realtime.leases`
}

func scanTopic(scanner rowScanner) (Topic, error) {
	var topic Topic
	var scopeID, ownerActorID, publisherPolicyID, subscriberPolicyID sql.NullString
	var latestPayload, metadata []byte
	var closedAt sql.NullTime
	if err := scanner.Scan(
		&topic.TopicID,
		&topic.TopicPath,
		&topic.DisplayName,
		&scopeID,
		&ownerActorID,
		&topic.CreatedByActorID,
		&publisherPolicyID,
		&subscriberPolicyID,
		&topic.RetentionMode,
		&topic.DeliveryClass,
		&topic.OrderingMode,
		&topic.Status,
		&topic.LastSequence,
		&latestPayload,
		&topic.CreatedAt,
		&topic.UpdatedAt,
		&closedAt,
		&metadata,
	); err != nil {
		return Topic{}, err
	}
	topic.ScopeID = nullStringPtr(scopeID)
	topic.OwnerActorID = nullStringPtr(ownerActorID)
	topic.PublisherPolicyID = nullStringPtr(publisherPolicyID)
	topic.SubscriberPolicyID = nullStringPtr(subscriberPolicyID)
	topic.LatestPayloadJSON = jsonOrDefault(latestPayload, `{}`)
	topic.ClosedAt = nullTimePtr(closedAt)
	topic.Metadata = jsonOrDefault(metadata, `{}`)
	return topic, nil
}

func scanTopicPublication(scanner rowScanner) (TopicPublication, error) {
	var publication TopicPublication
	var publisherActorID, publisherNodeID, publisherProviderID, policyDecisionID sql.NullString
	var payload, metadata []byte
	if err := scanner.Scan(
		&publication.TopicPublicationID,
		&publication.TopicID,
		&publication.Sequence,
		&publisherActorID,
		&publisherNodeID,
		&publisherProviderID,
		&publication.MessageType,
		&payload,
		&publication.PayloadSchemaRef,
		&publication.CorrelationID,
		&publication.CausationRef,
		&publication.AuthorizationRef,
		&policyDecisionID,
		&publication.DurabilityMode,
		&publication.CreatedAt,
		&metadata,
	); err != nil {
		return TopicPublication{}, err
	}
	publication.PublisherActorID = nullStringPtr(publisherActorID)
	publication.PublisherNodeID = nullStringPtr(publisherNodeID)
	publication.PublisherProviderID = nullStringPtr(publisherProviderID)
	publication.PolicyDecisionID = nullStringPtr(policyDecisionID)
	publication.PayloadJSON = jsonOrDefault(payload, `{}`)
	publication.Metadata = jsonOrDefault(metadata, `{}`)
	return publication, nil
}

func scanSubscription(scanner rowScanner) (Subscription, error) {
	var subscription Subscription
	var subscriberActorID, subscriberNodeID, scopeID, policyDecisionID sql.NullString
	var expiresAt, lastDeliveredAt, lastAcknowledgedAt sql.NullTime
	var filterJSON, metadata []byte
	if err := scanner.Scan(
		&subscription.SubscriptionID,
		&subscription.TopicID,
		&subscription.SourceKind,
		&subscription.SourceRef,
		&subscriberActorID,
		&subscriberNodeID,
		&scopeID,
		&filterJSON,
		&subscription.CursorSequence,
		&subscription.LastAcknowledgedSequence,
		&subscription.DeliveryTargetKind,
		&subscription.DeliveryTargetRef,
		&subscription.DeliveryMode,
		&subscription.DeliveryClass,
		&subscription.AuthorizationRef,
		&policyDecisionID,
		&subscription.Status,
		&expiresAt,
		&subscription.CreatedAt,
		&subscription.UpdatedAt,
		&lastDeliveredAt,
		&lastAcknowledgedAt,
		&metadata,
	); err != nil {
		return Subscription{}, err
	}
	subscription.SubscriberActorID = nullStringPtr(subscriberActorID)
	subscription.SubscriberNodeID = nullStringPtr(subscriberNodeID)
	subscription.ScopeID = nullStringPtr(scopeID)
	subscription.PolicyDecisionID = nullStringPtr(policyDecisionID)
	subscription.ExpiresAt = nullTimePtr(expiresAt)
	subscription.LastDeliveredAt = nullTimePtr(lastDeliveredAt)
	subscription.LastAcknowledgedAt = nullTimePtr(lastAcknowledgedAt)
	subscription.FilterJSON = jsonOrDefault(filterJSON, `{}`)
	subscription.Metadata = jsonOrDefault(metadata, `{}`)
	return subscription, nil
}

func scanPresence(scanner rowScanner) (Presence, error) {
	var presence Presence
	var nodeID sql.NullString
	var confidence sql.NullFloat64
	var metadata []byte
	if err := scanner.Scan(
		&presence.PresenceID,
		&presence.SubjectKind,
		&presence.SubjectRef,
		&nodeID,
		&presence.State,
		&presence.SourceKind,
		&presence.SourceRef,
		&presence.LastSeenAt,
		&presence.ExpiresAt,
		&confidence,
		&presence.StatusDetail,
		&metadata,
		&presence.UpdatedAt,
	); err != nil {
		return Presence{}, err
	}
	presence.NodeID = nullStringPtr(nodeID)
	presence.Confidence = nullFloatPtr(confidence)
	presence.Metadata = jsonOrDefault(metadata, `{}`)
	return presence, nil
}

func scanNotification(scanner rowScanner) (Notification, error) {
	var notification Notification
	var scopeID, policyDecisionID, approvalID, routeID, capabilityCallID sql.NullString
	var expiresAt, acknowledgedAt, dismissedAt sql.NullTime
	var payload, metadata []byte
	if err := scanner.Scan(
		&notification.NotificationID,
		&notification.TargetKind,
		&notification.TargetRef,
		&notification.SourceKind,
		&notification.SourceRef,
		&scopeID,
		&notification.Category,
		&notification.Priority,
		&notification.Summary,
		&payload,
		&notification.AuthorizationRef,
		&policyDecisionID,
		&approvalID,
		&routeID,
		&capabilityCallID,
		&notification.Status,
		&expiresAt,
		&acknowledgedAt,
		&dismissedAt,
		&notification.CreatedAt,
		&notification.UpdatedAt,
		&metadata,
	); err != nil {
		return Notification{}, err
	}
	notification.ScopeID = nullStringPtr(scopeID)
	notification.PolicyDecisionID = nullStringPtr(policyDecisionID)
	notification.ApprovalID = nullStringPtr(approvalID)
	notification.RouteID = nullStringPtr(routeID)
	notification.CapabilityCallID = nullStringPtr(capabilityCallID)
	notification.ExpiresAt = nullTimePtr(expiresAt)
	notification.AcknowledgedAt = nullTimePtr(acknowledgedAt)
	notification.DismissedAt = nullTimePtr(dismissedAt)
	notification.PayloadJSON = jsonOrDefault(payload, `{}`)
	notification.Metadata = jsonOrDefault(metadata, `{}`)
	return notification, nil
}

func scanProgressFeed(scanner rowScanner) (ProgressFeed, error) {
	var feed ProgressFeed
	var scopeID, topicID sql.NullString
	var currentValue, totalValue sql.NullFloat64
	var closedAt sql.NullTime
	var metadata []byte
	if err := scanner.Scan(
		&feed.ProgressFeedID,
		&feed.SourceKind,
		&feed.SourceRef,
		&scopeID,
		&topicID,
		&feed.CurrentStatus,
		&feed.CurrentStage,
		&feed.CurrentMessage,
		&currentValue,
		&totalValue,
		&feed.LastSequence,
		&feed.CreatedAt,
		&feed.UpdatedAt,
		&closedAt,
		&metadata,
	); err != nil {
		return ProgressFeed{}, err
	}
	feed.ScopeID = nullStringPtr(scopeID)
	feed.TopicID = nullStringPtr(topicID)
	feed.CurrentValue = nullFloatPtr(currentValue)
	feed.TotalValue = nullFloatPtr(totalValue)
	feed.ClosedAt = nullTimePtr(closedAt)
	feed.Metadata = jsonOrDefault(metadata, `{}`)
	return feed, nil
}

func scanProgressUpdate(scanner rowScanner) (ProgressUpdate, error) {
	var update ProgressUpdate
	var payload, metadata []byte
	var progressValue, totalValue sql.NullFloat64
	if err := scanner.Scan(
		&update.ProgressUpdateID,
		&update.ProgressFeedID,
		&update.SourceKind,
		&update.SourceRef,
		&update.Sequence,
		&update.Stage,
		&update.Status,
		&update.Message,
		&payload,
		&progressValue,
		&totalValue,
		&update.Severity,
		&update.CorrelationID,
		&update.CreatedAt,
		&metadata,
	); err != nil {
		return ProgressUpdate{}, err
	}
	update.PayloadJSON = jsonOrDefault(payload, `{}`)
	update.ProgressValue = nullFloatPtr(progressValue)
	update.TotalValue = nullFloatPtr(totalValue)
	update.Metadata = jsonOrDefault(metadata, `{}`)
	return update, nil
}

func scanLease(scanner rowScanner) (Lease, error) {
	var lease Lease
	var holderActorID, nodeID, providerID, scopeID, routeID, capabilityCallID sql.NullString
	var policyDecisionID, approvalID, grantID sql.NullString
	var releasedAt sql.NullTime
	var metadata []byte
	if err := scanner.Scan(
		&lease.LeaseID,
		&lease.ResourceKind,
		&lease.ResourceRef,
		&lease.HolderKind,
		&lease.HolderRef,
		&holderActorID,
		&nodeID,
		&providerID,
		&scopeID,
		&routeID,
		&capabilityCallID,
		&lease.AuthorizationRef,
		&policyDecisionID,
		&approvalID,
		&grantID,
		&lease.LeaseMode,
		&lease.Status,
		&lease.StartsAt,
		&lease.ExpiresAt,
		&releasedAt,
		&lease.ReleaseReason,
		&lease.CreatedAt,
		&lease.UpdatedAt,
		&metadata,
	); err != nil {
		return Lease{}, err
	}
	lease.HolderActorID = nullStringPtr(holderActorID)
	lease.NodeID = nullStringPtr(nodeID)
	lease.ProviderID = nullStringPtr(providerID)
	lease.ScopeID = nullStringPtr(scopeID)
	lease.RouteID = nullStringPtr(routeID)
	lease.CapabilityCallID = nullStringPtr(capabilityCallID)
	lease.PolicyDecisionID = nullStringPtr(policyDecisionID)
	lease.ApprovalID = nullStringPtr(approvalID)
	lease.GrantID = nullStringPtr(grantID)
	lease.ReleasedAt = nullTimePtr(releasedAt)
	lease.Metadata = jsonOrDefault(metadata, `{}`)
	return lease, nil
}

func durabilityForRetention(retention string) string {
	switch retention {
	case RetentionModeRetainLatest:
		return DurabilityModeLatestOnly
	case RetentionModeDurableEventOnly:
		return DurabilityModeDurableEventOnly
	default:
		return DurabilityModeRetained
	}
}

func objectOrDefault(raw json.RawMessage) json.RawMessage {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`{}`)
	}
	return raw
}

func jsonOrDefault(raw []byte, fallback string) json.RawMessage {
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(fallback)
	}
	return json.RawMessage(raw)
}

func nullStringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func nullTimePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}

func nullFloatPtr(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	return &value.Float64
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func defaultString(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return strings.TrimSpace(fallback)
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func mustJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type rowScanner interface {
	Scan(dest ...any) error
}
