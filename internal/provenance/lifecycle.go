package provenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
)

type CandidateLifecycleProjection struct {
	SchemaVersion      string          `json:"schema_version"`
	Candidate          Candidate       `json:"candidate"`
	EffectiveState     string          `json:"effective_state"`
	WinningOperationID *SemanticID     `json:"winning_operation_id,omitempty"`
	LatestDeferral     json.RawMessage `json:"latest_deferral,omitempty"`
	SourceReferenceIDs []SemanticID    `json:"source_reference_ids"`
	Lineage            []SemanticID    `json:"lineage"`
	Events             []LedgerEvent   `json:"events"`
	Truncated          bool            `json:"truncated"`
}

type RecordLifecycleProjection struct {
	SchemaVersion      string           `json:"schema_version"`
	Record             Record           `json:"record"`
	SourceReferenceIDs []SemanticID     `json:"source_reference_ids"`
	ProducerHistory    []RecordProducer `json:"producer_history"`
	Events             []LedgerEvent    `json:"representation_events"`
	Truncated          bool             `json:"truncated"`
}

type RelationshipLifecycleProjection struct {
	SchemaVersion     string        `json:"schema_version"`
	Relationship      Relationship  `json:"relationship"`
	EffectiveState    string        `json:"effective_state"`
	EndingOperationID *SemanticID   `json:"ending_operation_id,omitempty"`
	Events            []LedgerEvent `json:"events"`
	Truncated         bool          `json:"truncated"`
}

type ResolutionCaseLifecycleProjection struct {
	SchemaVersion  string         `json:"schema_version"`
	Case           ResolutionCase `json:"case"`
	EffectiveState string         `json:"effective_state"`
	Members        []CaseMember   `json:"members"`
	Events         []CaseEvent    `json:"events"`
	Truncated      bool           `json:"truncated"`
}

func (service *Service) GetCandidateLifecycle(ctx context.Context, id SemanticID, limit int) (CandidateLifecycleProjection, bool, error) {
	limit, err := boundedLimit(limit)
	if err != nil {
		return CandidateLifecycleProjection{}, false, err
	}
	candidate, found, err := service.store.GetCandidate(ctx, id)
	if err != nil || !found {
		return CandidateLifecycleProjection{}, found, err
	}
	projection := CandidateLifecycleProjection{SchemaVersion: SchemaVersion, Candidate: candidate, EffectiveState: "pending"}
	var eventType, winnerID string
	err = service.store.q.queryRow(ctx, `SELECT event_type, operation_id::text FROM provenance.candidate_events WHERE candidate_id=$1 AND event_type IN ('accepted','consolidated','rejected') ORDER BY id LIMIT 1`, string(id)).Scan(&eventType, &winnerID)
	if err == nil {
		winner := SemanticID(winnerID)
		projection.EffectiveState, projection.WinningOperationID = eventType, &winner
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return CandidateLifecycleProjection{}, false, fmt.Errorf("load candidate lifecycle winner: %w", err)
	}
	if projection.EffectiveState == "pending" {
		err = service.store.q.queryRow(ctx, `SELECT payload_json FROM provenance.candidate_events WHERE candidate_id=$1 AND event_type='deferred' ORDER BY id DESC LIMIT 1`, string(id)).Scan(&projection.LatestDeferral)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return CandidateLifecycleProjection{}, false, fmt.Errorf("load candidate latest deferral: %w", err)
		}
	}
	rows, err := service.store.q.query(ctx, `
		SELECT source_reference_id::text FROM (
			SELECT id AS source_reference_id, resolution_at AS linked_at FROM provenance.source_references WHERE candidate_id=$1
			UNION ALL
			SELECT source_reference_id, linked_at FROM provenance.candidate_evidence_links WHERE candidate_id=$1
		) AS sources ORDER BY linked_at, source_reference_id LIMIT $2
	`, string(id), limit+1)
	if err != nil {
		return CandidateLifecycleProjection{}, false, fmt.Errorf("load candidate lifecycle sources: %w", err)
	}
	projection.SourceReferenceIDs, err = scanSemanticIDs(rows, "candidate lifecycle sources")
	rows.Close()
	if err != nil {
		return CandidateLifecycleProjection{}, false, err
	}
	if len(projection.SourceReferenceIDs) > limit {
		projection.SourceReferenceIDs = projection.SourceReferenceIDs[:limit]
		projection.Truncated = true
	}
	rows, err = service.store.q.query(ctx, `SELECT derived_from_candidate_id::text FROM provenance.candidate_lineage WHERE candidate_id=$1 ORDER BY registered_at,id LIMIT $2`, string(id), limit+1)
	if err != nil {
		return CandidateLifecycleProjection{}, false, fmt.Errorf("load candidate lifecycle lineage: %w", err)
	}
	projection.Lineage, err = scanSemanticIDs(rows, "candidate lifecycle lineage")
	rows.Close()
	if err != nil {
		return CandidateLifecycleProjection{}, false, err
	}
	if len(projection.Lineage) > limit {
		projection.Lineage = projection.Lineage[:limit]
		projection.Truncated = true
	}
	projection.Events, found, err = service.listLedgerEventsNewest(ctx, "candidate_events", "candidate_id", id, limit)
	projection.Truncated = projection.Truncated || found
	return projection, true, err
}

func (service *Service) GetRecordLifecycle(ctx context.Context, id SemanticID, limit int) (RecordLifecycleProjection, bool, error) {
	limit, err := boundedLimit(limit)
	if err != nil {
		return RecordLifecycleProjection{}, false, err
	}
	record, found, err := service.store.GetRecord(ctx, id)
	if err != nil || !found {
		return RecordLifecycleProjection{}, found, err
	}
	projection := RecordLifecycleProjection{SchemaVersion: SchemaVersion, Record: record}
	rows, err := service.store.q.query(ctx, `SELECT source_reference_id::text FROM provenance.record_sources WHERE record_id=$1 ORDER BY linked_at,source_reference_id LIMIT $2`, string(id), limit+1)
	if err != nil {
		return RecordLifecycleProjection{}, false, fmt.Errorf("load record lifecycle sources: %w", err)
	}
	projection.SourceReferenceIDs, err = scanSemanticIDs(rows, "record lifecycle sources")
	rows.Close()
	if err != nil {
		return RecordLifecycleProjection{}, false, err
	}
	if len(projection.SourceReferenceIDs) > limit {
		projection.SourceReferenceIDs = projection.SourceReferenceIDs[:limit]
		projection.Truncated = true
	}
	projection.ProducerHistory, err = service.store.ListRecordProducers(ctx, id, limit)
	if err != nil {
		return RecordLifecycleProjection{}, false, err
	}
	var producerCount int
	if err := service.store.q.queryRow(ctx, `SELECT count(*) FROM provenance.record_producers WHERE record_id=$1`, string(id)).Scan(&producerCount); err != nil {
		return RecordLifecycleProjection{}, false, err
	}
	projection.Truncated = projection.Truncated || producerCount > len(projection.ProducerHistory)
	projection.Events, found, err = service.listLedgerEventsNewest(ctx, "record_events", "record_id", id, limit)
	if err != nil {
		return RecordLifecycleProjection{}, false, err
	}
	projection.Truncated = projection.Truncated || found
	for index := len(projection.Events) - 1; index >= 0; index-- {
		event := projection.Events[index]
		var payload struct {
			Qualification    string                 `json:"qualification"`
			AssertionPosture string                 `json:"assertion_posture"`
			Temporal         TemporalInterpretation `json:"temporal_interpretation"`
		}
		if err := decodeLosslessJSON(event.Payload, &payload); err != nil {
			return RecordLifecycleProjection{}, false, fmt.Errorf("decode record lifecycle event: %w", err)
		}
		switch event.EventType {
		case "ambiguity_appended":
			if payload.Qualification != "" && !slices.Contains(projection.Record.Ambiguity, payload.Qualification) {
				projection.Record.Ambiguity = append(projection.Record.Ambiguity, payload.Qualification)
			}
		case "assertion_posture_appended":
			if payload.AssertionPosture != "" {
				projection.Record.AssertionPosture = payload.AssertionPosture
			}
		case "temporal_interpretation_appended":
			if payload.Temporal.Interpretation != "" {
				projection.Record.Temporal = payload.Temporal
			}
		}
	}
	var latestPosture json.RawMessage
	if err := service.store.q.queryRow(ctx, `SELECT payload_json FROM provenance.record_events WHERE record_id=$1 AND event_type='assertion_posture_appended' ORDER BY id DESC LIMIT 1`, string(id)).Scan(&latestPosture); err == nil {
		var payload struct {
			AssertionPosture string `json:"assertion_posture"`
		}
		if err := decodeLosslessJSON(latestPosture, &payload); err != nil {
			return RecordLifecycleProjection{}, false, err
		}
		if payload.AssertionPosture != "" {
			projection.Record.AssertionPosture = payload.AssertionPosture
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return RecordLifecycleProjection{}, false, err
	}
	var latestTemporal json.RawMessage
	if err := service.store.q.queryRow(ctx, `SELECT payload_json FROM provenance.record_events WHERE record_id=$1 AND event_type='temporal_interpretation_appended' ORDER BY id DESC LIMIT 1`, string(id)).Scan(&latestTemporal); err == nil {
		var payload struct {
			Temporal TemporalInterpretation `json:"temporal_interpretation"`
		}
		if err := decodeLosslessJSON(latestTemporal, &payload); err != nil {
			return RecordLifecycleProjection{}, false, err
		}
		if payload.Temporal.Interpretation != "" {
			projection.Record.Temporal = payload.Temporal
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return RecordLifecycleProjection{}, false, err
	}
	return projection, true, nil
}

func (service *Service) GetRelationshipLifecycle(ctx context.Context, id SemanticID, limit int) (RelationshipLifecycleProjection, bool, error) {
	limit, err := boundedLimit(limit)
	if err != nil {
		return RelationshipLifecycleProjection{}, false, err
	}
	relationship, found, err := service.store.GetRelationship(ctx, id)
	if err != nil || !found {
		return RelationshipLifecycleProjection{}, found, err
	}
	projection := RelationshipLifecycleProjection{SchemaVersion: SchemaVersion, Relationship: relationship, EffectiveState: "active"}
	var operationID string
	err = service.store.q.queryRow(ctx, `SELECT operation_id::text FROM provenance.relationship_events WHERE relationship_id=$1 AND event_type='ended' ORDER BY id LIMIT 1`, string(id)).Scan(&operationID)
	if err == nil {
		ending := SemanticID(operationID)
		projection.EffectiveState, projection.EndingOperationID = "ended", &ending
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return RelationshipLifecycleProjection{}, false, err
	}
	projection.Events, projection.Truncated, err = service.listLedgerEventsNewest(ctx, "relationship_events", "relationship_id", id, limit)
	return projection, true, err
}

func (service *Service) GetResolutionCaseLifecycle(ctx context.Context, id SemanticID, limit int) (ResolutionCaseLifecycleProjection, bool, error) {
	limit, err := boundedLimit(limit)
	if err != nil {
		return ResolutionCaseLifecycleProjection{}, false, err
	}
	resolutionCase, found, err := service.store.GetResolutionCase(ctx, id)
	if err != nil || !found {
		return ResolutionCaseLifecycleProjection{}, found, err
	}
	projection := ResolutionCaseLifecycleProjection{SchemaVersion: SchemaVersion, Case: resolutionCase, EffectiveState: resolutionCase.InitialStatus}
	if !isClosedStatus(projection.EffectiveState) {
		var outcome string
		err = service.store.q.queryRow(ctx, `
			SELECT outcome FROM provenance.case_events
			WHERE case_id=$1 AND lower(btrim(outcome)) IN ('closed','resolved','dismissed')
			ORDER BY occurred_at,
				CASE WHEN jsonb_typeof(payload_json->'append_order')='number' THEN (payload_json->>'append_order')::bigint ELSE 0 END,
				id
			LIMIT 1
		`, string(id)).Scan(&outcome)
		if errors.Is(err, pgx.ErrNoRows) {
			err = service.store.q.queryRow(ctx, `
				SELECT outcome FROM provenance.case_events
				WHERE case_id=$1 AND outcome IS NOT NULL
				ORDER BY occurred_at DESC,
					CASE WHEN jsonb_typeof(payload_json->'append_order')='number' THEN (payload_json->>'append_order')::bigint ELSE 0 END DESC,
					id DESC
				LIMIT 1
			`, string(id)).Scan(&outcome)
		}
		if err == nil {
			projection.EffectiveState = outcome
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return ResolutionCaseLifecycleProjection{}, false, fmt.Errorf("load resolution case effective state: %w", err)
		}
	}
	rows, err := service.store.q.query(ctx, `SELECT member_type,candidate_id::text,record_id::text,role,attached_at,operation_id::text FROM provenance.case_members WHERE case_id=$1 ORDER BY attached_at,id LIMIT $2`, string(id), limit+1)
	if err != nil {
		return ResolutionCaseLifecycleProjection{}, false, err
	}
	for rows.Next() {
		var member CaseMember
		var candidateID, recordID, operationID *string
		if err := rows.Scan(&member.MemberType, &candidateID, &recordID, &member.Role, &member.AttachedAt, &operationID); err != nil {
			rows.Close()
			return ResolutionCaseLifecycleProjection{}, false, err
		}
		member.CaseID = id
		member.CandidateID = optionalID(candidateID)
		member.RecordID = optionalID(recordID)
		member.OperationID = optionalID(operationID)
		member.AttachedAt = member.AttachedAt.UTC()
		projection.Members = append(projection.Members, member)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ResolutionCaseLifecycleProjection{}, false, err
	}
	rows.Close()
	if len(projection.Members) > limit {
		projection.Members = projection.Members[:limit]
		projection.Truncated = true
	}
	rows, err = service.store.q.query(ctx, `
		SELECT id::text,schema_version,event_type,occurred_at,producer_json,summary,outcome,evidence_json,operation_id::text,payload_json
		FROM provenance.case_events
		WHERE case_id=$1
		ORDER BY occurred_at DESC,
			CASE WHEN jsonb_typeof(payload_json->'append_order')='number' THEN (payload_json->>'append_order')::bigint ELSE 0 END DESC,
			id DESC
		LIMIT $2
	`, string(id), limit+1)
	if err != nil {
		return ResolutionCaseLifecycleProjection{}, false, err
	}
	for rows.Next() {
		var event CaseEvent
		var rawID string
		var producer, evidence []byte
		var operationID *string
		if err := rows.Scan(&rawID, &event.SchemaVersion, &event.EventType, &event.OccurredAt, &producer, &event.Summary, &event.Outcome, &evidence, &operationID, &event.Payload); err != nil {
			rows.Close()
			return ResolutionCaseLifecycleProjection{}, false, err
		}
		event.ID = SemanticID(rawID)
		event.CaseID = id
		event.OperationID = optionalID(operationID)
		event.OccurredAt = event.OccurredAt.UTC()
		if err := decodeLosslessJSON(producer, &event.Producer); err != nil {
			rows.Close()
			return ResolutionCaseLifecycleProjection{}, false, err
		}
		if err := json.Unmarshal(evidence, &event.EvidenceSourceIDs); err != nil {
			rows.Close()
			return ResolutionCaseLifecycleProjection{}, false, err
		}
		projection.Events = append(projection.Events, event)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ResolutionCaseLifecycleProjection{}, false, err
	}
	rows.Close()
	if len(projection.Events) > limit {
		projection.Events = projection.Events[:limit]
		projection.Truncated = true
	}
	return projection, true, nil
}

func (service *Service) listLedgerEventsNewest(ctx context.Context, table, idColumn string, id SemanticID, limit int) ([]LedgerEvent, bool, error) {
	query := fmt.Sprintf(`SELECT event_type,operation_id::text,occurred_at,payload_json FROM provenance.%s WHERE %s=$1 ORDER BY id DESC LIMIT $2`, table, idColumn)
	rows, err := service.store.q.query(ctx, query, string(id), limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	events := make([]LedgerEvent, 0, limit+1)
	for rows.Next() {
		var event LedgerEvent
		var operationID string
		if err := rows.Scan(&event.EventType, &operationID, &event.OccurredAt, &event.Payload); err != nil {
			return nil, false, err
		}
		event.ObjectID = id
		event.OperationID = SemanticID(operationID)
		event.OccurredAt = event.OccurredAt.UTC()
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	truncated := len(events) > limit
	if truncated {
		events = events[:limit]
	}
	return events, truncated, nil
}
