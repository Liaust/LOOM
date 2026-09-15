package provenance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrReplayConflict = errors.New("registration replay key has a different payload")

type Store struct {
	pool *pgxpool.Pool
	q    queryAdapter
}

type queryAdapter struct {
	exec     func(context.Context, string, ...any) (int64, error)
	query    func(context.Context, string, ...any) (pgx.Rows, error)
	queryRow func(context.Context, string, ...any) pgx.Row
}

func NewStore(pool *pgxpool.Pool) (*Store, error) {
	if pool == nil {
		return nil, errors.New("provenance store pool is required")
	}
	return &Store{pool: pool, q: adaptPool(pool)}, nil
}

func adaptPool(pool *pgxpool.Pool) queryAdapter {
	return queryAdapter{
		exec: func(ctx context.Context, sql string, arguments ...any) (int64, error) {
			tag, err := pool.Exec(ctx, sql, arguments...)
			return tag.RowsAffected(), err
		},
		query:    pool.Query,
		queryRow: pool.QueryRow,
	}
}

func adaptTx(tx pgx.Tx) queryAdapter {
	return queryAdapter{
		exec: func(ctx context.Context, sql string, arguments ...any) (int64, error) {
			tag, err := tx.Exec(ctx, sql, arguments...)
			return tag.RowsAffected(), err
		},
		query:    tx.Query,
		queryRow: tx.QueryRow,
	}
}

// Transact lets lifecycle code compose bounded primitives atomically without
// giving Store permission to choose lifecycle transitions itself.
func (store *Store) Transact(ctx context.Context, work func(*Store) error) error {
	if store == nil || store.pool == nil {
		return errors.New("transactions require a root provenance store")
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin provenance transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := work(&Store{q: adaptTx(tx)}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit provenance transaction: %w", err)
	}
	return nil
}

func (store *Store) AppendCandidate(ctx context.Context, candidate Candidate) error {
	if err := validateCandidate(candidate); err != nil {
		return err
	}
	_, err := store.q.exec(ctx, `
		INSERT INTO provenance.candidates(
			id, schema_version, state, domain, visibility, record_kind, claim,
			record_context, assertion_posture, producer_id, registered_at,
			submitted_json, payload_json
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`, string(candidate.ID), candidate.SchemaVersion, candidate.State, candidate.Domain,
		candidate.Visibility, candidate.RecordKind, candidate.Claim, candidate.RecordContext,
		candidate.AssertionPosture, candidate.ProducerID, candidate.RegisteredAt,
		[]byte(candidate.Submitted), []byte(candidate.Payload))
	return wrapStoreError("append candidate", err)
}

func (store *Store) GetCandidate(ctx context.Context, id SemanticID) (Candidate, bool, error) {
	if err := id.Validate(); err != nil {
		return Candidate{}, false, err
	}
	var candidate Candidate
	var rawID string
	err := store.q.queryRow(ctx, `
		SELECT id::text, schema_version, state, domain, visibility, record_kind,
		       claim, record_context, assertion_posture, producer_id,
		       registered_at, submitted_json, payload_json
		FROM provenance.candidates WHERE id = $1
	`, string(id)).Scan(&rawID, &candidate.SchemaVersion, &candidate.State, &candidate.Domain,
		&candidate.Visibility, &candidate.RecordKind, &candidate.Claim, &candidate.RecordContext,
		&candidate.AssertionPosture, &candidate.ProducerID, &candidate.RegisteredAt,
		&candidate.Submitted, &candidate.Payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return Candidate{}, false, nil
	}
	if err != nil {
		return Candidate{}, false, fmt.Errorf("get candidate: %w", err)
	}
	candidate.ID = SemanticID(rawID)
	candidate.RegisteredAt = candidate.RegisteredAt.UTC()
	return candidate, true, nil
}

func (store *Store) ListCandidates(ctx context.Context, request PageRequest) (Page[Candidate], error) {
	request, err := request.normalized()
	if err != nil {
		return Page[Candidate]{}, err
	}
	rows, err := store.q.query(ctx, `
		SELECT id::text, schema_version, state, domain, visibility, record_kind,
		       claim, record_context, assertion_posture, producer_id,
		       registered_at, submitted_json, payload_json
		FROM provenance.candidates
		WHERE ($1 = '' OR domain = $1)
		  AND ($2 = '' OR visibility = $2)
		  AND ($3::timestamptz IS NULL OR (registered_at, id) > ($3, $4::uuid))
		ORDER BY registered_at, id
		LIMIT $5
	`, request.Domain, request.Visibility, request.AfterTime, idArgument(request.AfterID), request.Limit+1)
	if err != nil {
		return Page[Candidate]{}, fmt.Errorf("list candidates: %w", err)
	}
	defer rows.Close()
	items := make([]Candidate, 0, request.Limit+1)
	for rows.Next() {
		var item Candidate
		var id string
		if err := rows.Scan(&id, &item.SchemaVersion, &item.State, &item.Domain, &item.Visibility,
			&item.RecordKind, &item.Claim, &item.RecordContext, &item.AssertionPosture,
			&item.ProducerID, &item.RegisteredAt, &item.Submitted, &item.Payload); err != nil {
			return Page[Candidate]{}, fmt.Errorf("scan candidate page: %w", err)
		}
		item.ID = SemanticID(id)
		item.RegisteredAt = item.RegisteredAt.UTC()
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return Page[Candidate]{}, fmt.Errorf("iterate candidate page: %w", err)
	}
	return makePage(items, request.Limit, func(item Candidate) (time.Time, SemanticID) {
		return item.RegisteredAt, item.ID
	}), nil
}

func (store *Store) AppendSourceReference(ctx context.Context, source SourceReference) error {
	if err := validateSource(source); err != nil {
		return err
	}
	_, err := store.q.exec(ctx, `
		INSERT INTO provenance.source_references(
			id, candidate_id, schema_version, source_kind, status,
			verification_posture, resolver_name, resolver_version, resolution_at,
			canonical_locator, version_address, content_digest, source_context,
			gap_reason, submitted_json, payload_json
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
	`, string(source.ID), idArgument(source.CandidateID), source.SchemaVersion,
		source.SourceKind, source.Status, source.VerificationPosture, source.ResolverName,
		source.ResolverVersion, source.ResolutionAt, source.CanonicalLocator,
		source.VersionAddress, source.ContentDigest, source.SourceContext, source.GapReason,
		[]byte(source.Submitted), []byte(source.Payload))
	return wrapStoreError("append source reference", err)
}

func (store *Store) GetSourceReference(ctx context.Context, id SemanticID) (SourceReference, bool, error) {
	if err := id.Validate(); err != nil {
		return SourceReference{}, false, err
	}
	var source SourceReference
	var rawID string
	var candidateID *string
	err := store.q.queryRow(ctx, `
		SELECT id::text, candidate_id::text, schema_version, source_kind, status,
		       verification_posture, resolver_name, resolver_version, resolution_at,
		       canonical_locator, version_address, content_digest, source_context,
		       gap_reason, submitted_json, payload_json
		FROM provenance.source_references WHERE id = $1
	`, string(id)).Scan(&rawID, &candidateID, &source.SchemaVersion, &source.SourceKind,
		&source.Status, &source.VerificationPosture, &source.ResolverName, &source.ResolverVersion,
		&source.ResolutionAt, &source.CanonicalLocator, &source.VersionAddress,
		&source.ContentDigest, &source.SourceContext, &source.GapReason, &source.Submitted,
		&source.Payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return SourceReference{}, false, nil
	}
	if err != nil {
		return SourceReference{}, false, fmt.Errorf("get source reference: %w", err)
	}
	source.ID = SemanticID(rawID)
	source.CandidateID = optionalID(candidateID)
	source.ResolutionAt = source.ResolutionAt.UTC()
	return source, true, nil
}

func (store *Store) AppendRecord(ctx context.Context, record Record) error {
	if err := validateRecord(record); err != nil {
		return err
	}
	ambiguity, err := json.Marshal(record.Ambiguity)
	if err != nil {
		return fmt.Errorf("encode record ambiguity: %w", err)
	}
	temporal, err := json.Marshal(record.Temporal)
	if err != nil {
		return fmt.Errorf("encode record temporal interpretation: %w", err)
	}
	var anchors []byte
	if record.Anchors != nil {
		anchors, err = json.Marshal(record.Anchors)
		if err != nil {
			return fmt.Errorf("encode record anchors: %w", err)
		}
	}
	_, err = store.q.exec(ctx, `
		INSERT INTO provenance.records(
			id, schema_version, claim, record_kind, record_context, ambiguity_json,
			modality, domain, visibility, assertion_posture, source_actor_id,
			artifact_author_id, approving_actor_id, observed_at, valid_from,
			valid_until, anchors_json, temporal_json, created_at, payload_json
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)
	`, string(record.ID), record.SchemaVersion, record.Claim, record.RecordKind,
		record.RecordContext, ambiguity, record.Modality, record.Domain, record.Visibility,
		record.AssertionPosture, record.SourceActorID, record.ArtifactAuthorID,
		record.ApprovingActorID, record.Temporal.ObservedAt, record.Temporal.ValidFrom,
		record.Temporal.ValidUntil, nullableJSON(anchors), temporal, record.CreatedAt,
		[]byte(record.Payload))
	return wrapStoreError("append record", err)
}

func (store *Store) GetRecord(ctx context.Context, id SemanticID) (Record, bool, error) {
	if err := id.Validate(); err != nil {
		return Record{}, false, err
	}
	var record Record
	var rawID string
	var ambiguity, anchors, temporal []byte
	err := store.q.queryRow(ctx, `
		SELECT id::text, schema_version, claim, record_kind, record_context,
		       ambiguity_json, modality, domain, visibility, assertion_posture,
		       source_actor_id, artifact_author_id, approving_actor_id,
		       observed_at, valid_from, valid_until, anchors_json, temporal_json,
		       created_at, payload_json
		FROM provenance.records WHERE id = $1
	`, string(id)).Scan(&rawID, &record.SchemaVersion, &record.Claim, &record.RecordKind,
		&record.RecordContext, &ambiguity, &record.Modality, &record.Domain,
		&record.Visibility, &record.AssertionPosture, &record.SourceActorID,
		&record.ArtifactAuthorID, &record.ApprovingActorID, &record.Temporal.ObservedAt,
		&record.Temporal.ValidFrom, &record.Temporal.ValidUntil, &anchors, &temporal,
		&record.CreatedAt, &record.Payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("get record: %w", err)
	}
	record.ID = SemanticID(rawID)
	if err := json.Unmarshal(ambiguity, &record.Ambiguity); err != nil {
		return Record{}, false, fmt.Errorf("decode record ambiguity: %w", err)
	}
	if err := json.Unmarshal(temporal, &record.Temporal); err != nil {
		return Record{}, false, fmt.Errorf("decode record temporal interpretation: %w", err)
	}
	if len(anchors) > 0 {
		record.Anchors = &StructuralAnchors{}
		if err := json.Unmarshal(anchors, record.Anchors); err != nil {
			return Record{}, false, fmt.Errorf("decode record anchors: %w", err)
		}
	}
	record.CreatedAt = record.CreatedAt.UTC()
	return record, true, nil
}

func (store *Store) ListRecords(ctx context.Context, request PageRequest) (Page[Record], error) {
	request, err := request.normalized()
	if err != nil {
		return Page[Record]{}, err
	}
	rows, err := store.q.query(ctx, `
		SELECT id::text
		FROM provenance.records
		WHERE ($1 = '' OR domain = $1)
		  AND ($2 = '' OR visibility = $2)
		  AND ($3::timestamptz IS NULL OR (created_at, id) > ($3, $4::uuid))
		ORDER BY created_at, id
		LIMIT $5
	`, request.Domain, request.Visibility, request.AfterTime, idArgument(request.AfterID), request.Limit+1)
	if err != nil {
		return Page[Record]{}, fmt.Errorf("list records: %w", err)
	}
	defer rows.Close()
	ids := make([]SemanticID, 0, request.Limit+1)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return Page[Record]{}, fmt.Errorf("scan record page id: %w", err)
		}
		ids = append(ids, SemanticID(id))
	}
	if err := rows.Err(); err != nil {
		return Page[Record]{}, fmt.Errorf("iterate record page: %w", err)
	}
	items := make([]Record, 0, len(ids))
	for _, id := range ids {
		item, found, err := store.GetRecord(ctx, id)
		if err != nil {
			return Page[Record]{}, err
		}
		if !found {
			return Page[Record]{}, fmt.Errorf("record %s disappeared during bounded list", id)
		}
		items = append(items, item)
	}
	return makePage(items, request.Limit, func(item Record) (time.Time, SemanticID) {
		return item.CreatedAt, item.ID
	}), nil
}

func (store *Store) LinkRecordSource(ctx context.Context, link RecordSource) error {
	if err := link.RecordID.Validate(); err != nil {
		return err
	}
	if err := link.SourceReferenceID.Validate(); err != nil {
		return err
	}
	if err := validateTimestamp(link.LinkedAt, "record source linked_at"); err != nil {
		return err
	}
	_, err := store.q.exec(ctx, `
		INSERT INTO provenance.record_sources(record_id, source_reference_id, linked_at)
		VALUES ($1, $2, $3)
	`, string(link.RecordID), string(link.SourceReferenceID), link.LinkedAt)
	return wrapStoreError("link record source", err)
}

func (store *Store) LinkRecordProducer(ctx context.Context, link RecordProducer) error {
	if err := link.RecordID.Validate(); err != nil {
		return err
	}
	if link.CandidateID != nil {
		if err := link.CandidateID.Validate(); err != nil {
			return err
		}
	}
	if err := validateProducer(link.Producer); err != nil {
		return err
	}
	if err := validateTimestamp(link.LinkedAt, "record producer linked_at"); err != nil {
		return err
	}
	producer, err := json.Marshal(link.Producer)
	if err != nil {
		return fmt.Errorf("encode record producer: %w", err)
	}
	_, err = store.q.exec(ctx, `
		INSERT INTO provenance.record_producers(
			record_id, candidate_id, producer_id, producer_json, linked_at
		) VALUES ($1, $2, $3, $4, $5)
	`, string(link.RecordID), idArgument(link.CandidateID), link.Producer.ProducerID,
		producer, link.LinkedAt)
	return wrapStoreError("link record producer", err)
}

func (store *Store) ListRecordSourceIDs(ctx context.Context, recordID SemanticID, limit int) ([]SemanticID, error) {
	if err := recordID.Validate(); err != nil {
		return nil, err
	}
	limit, err := boundedLimit(limit)
	if err != nil {
		return nil, err
	}
	rows, err := store.q.query(ctx, `
		SELECT source_reference_id::text
		FROM provenance.record_sources
		WHERE record_id = $1
		ORDER BY linked_at, source_reference_id
		LIMIT $2
	`, string(recordID), limit)
	if err != nil {
		return nil, fmt.Errorf("list record sources: %w", err)
	}
	defer rows.Close()
	var result []SemanticID
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan record source: %w", err)
		}
		result = append(result, SemanticID(id))
	}
	return result, rows.Err()
}

func (store *Store) ListRecordProducers(ctx context.Context, recordID SemanticID, limit int) ([]RecordProducer, error) {
	if err := recordID.Validate(); err != nil {
		return nil, err
	}
	limit, err := boundedLimit(limit)
	if err != nil {
		return nil, err
	}
	rows, err := store.q.query(ctx, `
		SELECT candidate_id::text, producer_json, linked_at
		FROM provenance.record_producers
		WHERE record_id = $1
		ORDER BY linked_at, id
		LIMIT $2
	`, string(recordID), limit)
	if err != nil {
		return nil, fmt.Errorf("list record producers: %w", err)
	}
	defer rows.Close()
	var result []RecordProducer
	for rows.Next() {
		var item RecordProducer
		var candidateID *string
		var producer []byte
		if err := rows.Scan(&candidateID, &producer, &item.LinkedAt); err != nil {
			return nil, fmt.Errorf("scan record producer: %w", err)
		}
		item.RecordID, item.CandidateID, item.LinkedAt = recordID, optionalID(candidateID), item.LinkedAt.UTC()
		if err := decodeLosslessJSON(producer, &item.Producer); err != nil {
			return nil, fmt.Errorf("decode record producer: %w", err)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (store *Store) AppendResolutionCase(ctx context.Context, resolutionCase ResolutionCase) error {
	if err := validateResolutionCase(resolutionCase); err != nil {
		return err
	}
	_, err := store.q.exec(ctx, `
		INSERT INTO provenance.resolution_cases(
			id, schema_version, issue, domain, visibility, initial_status,
			created_at, created_by_operation_id, payload_json
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, string(resolutionCase.ID), resolutionCase.SchemaVersion, resolutionCase.Issue,
		resolutionCase.Domain, resolutionCase.Visibility, resolutionCase.InitialStatus,
		resolutionCase.CreatedAt, idArgument(resolutionCase.CreatedByOperationID),
		[]byte(resolutionCase.Payload))
	return wrapStoreError("append resolution case", err)
}

func (store *Store) GetResolutionCase(ctx context.Context, id SemanticID) (ResolutionCase, bool, error) {
	if err := id.Validate(); err != nil {
		return ResolutionCase{}, false, err
	}
	var item ResolutionCase
	var rawID string
	var operationID *string
	err := store.q.queryRow(ctx, `
		SELECT id::text, schema_version, issue, domain, visibility, initial_status,
		       created_at, created_by_operation_id::text, payload_json
		FROM provenance.resolution_cases WHERE id = $1
	`, string(id)).Scan(&rawID, &item.SchemaVersion, &item.Issue, &item.Domain,
		&item.Visibility, &item.InitialStatus, &item.CreatedAt, &operationID, &item.Payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return ResolutionCase{}, false, nil
	}
	if err != nil {
		return ResolutionCase{}, false, fmt.Errorf("get resolution case: %w", err)
	}
	item.ID = SemanticID(rawID)
	item.CreatedByOperationID = optionalID(operationID)
	item.CreatedAt = item.CreatedAt.UTC()
	return item, true, nil
}

func (store *Store) ListResolutionCases(ctx context.Context, request PageRequest) (Page[ResolutionCase], error) {
	request, err := request.normalized()
	if err != nil {
		return Page[ResolutionCase]{}, err
	}
	rows, err := store.q.query(ctx, `
		SELECT id::text, schema_version, issue, domain, visibility, initial_status,
		       created_at, created_by_operation_id::text, payload_json
		FROM provenance.resolution_cases
		WHERE ($1 = '' OR domain = $1)
		  AND ($2 = '' OR visibility = $2)
		  AND ($3::timestamptz IS NULL OR (created_at, id) > ($3, $4::uuid))
		ORDER BY created_at, id
		LIMIT $5
	`, request.Domain, request.Visibility, request.AfterTime, idArgument(request.AfterID), request.Limit+1)
	if err != nil {
		return Page[ResolutionCase]{}, fmt.Errorf("list resolution cases: %w", err)
	}
	defer rows.Close()
	items := make([]ResolutionCase, 0, request.Limit+1)
	for rows.Next() {
		var item ResolutionCase
		var id string
		var operationID *string
		if err := rows.Scan(&id, &item.SchemaVersion, &item.Issue, &item.Domain,
			&item.Visibility, &item.InitialStatus, &item.CreatedAt, &operationID,
			&item.Payload); err != nil {
			return Page[ResolutionCase]{}, fmt.Errorf("scan resolution case page: %w", err)
		}
		item.ID = SemanticID(id)
		item.CreatedByOperationID = optionalID(operationID)
		item.CreatedAt = item.CreatedAt.UTC()
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return Page[ResolutionCase]{}, fmt.Errorf("iterate resolution case page: %w", err)
	}
	return makePage(items, request.Limit, func(item ResolutionCase) (time.Time, SemanticID) {
		return item.CreatedAt, item.ID
	}), nil
}

func (store *Store) AppendCaseMember(ctx context.Context, member CaseMember) error {
	if err := validateCaseMember(member); err != nil {
		return err
	}
	_, err := store.q.exec(ctx, `
		INSERT INTO provenance.case_members(
			case_id, member_type, candidate_id, record_id, role, attached_at, operation_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, string(member.CaseID), member.MemberType, idArgument(member.CandidateID),
		idArgument(member.RecordID), member.Role, member.AttachedAt,
		idArgument(member.OperationID))
	return wrapStoreError("append case member", err)
}

func (store *Store) AppendCaseEvent(ctx context.Context, event CaseEvent) error {
	if err := validateCaseEvent(event); err != nil {
		return err
	}
	producer, err := json.Marshal(event.Producer)
	if err != nil {
		return fmt.Errorf("encode case event producer: %w", err)
	}
	evidence, err := json.Marshal(event.EvidenceSourceIDs)
	if err != nil {
		return fmt.Errorf("encode case event evidence: %w", err)
	}
	_, err = store.q.exec(ctx, `
		INSERT INTO provenance.case_events(
			id, case_id, schema_version, event_type, occurred_at, producer_json,
			summary, outcome, evidence_json, operation_id, payload_json
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, string(event.ID), string(event.CaseID), event.SchemaVersion, event.EventType,
		event.OccurredAt, producer, event.Summary, event.Outcome, evidence,
		idArgument(event.OperationID), []byte(event.Payload))
	return wrapStoreError("append case event", err)
}

func (store *Store) ListCaseMembers(ctx context.Context, caseID SemanticID, limit int) ([]CaseMember, error) {
	if err := caseID.Validate(); err != nil {
		return nil, err
	}
	limit, err := boundedLimit(limit)
	if err != nil {
		return nil, err
	}
	rows, err := store.q.query(ctx, `
		SELECT member_type, candidate_id::text, record_id::text, role,
		       attached_at, operation_id::text
		FROM provenance.case_members
		WHERE case_id = $1
		ORDER BY attached_at, id
		LIMIT $2
	`, string(caseID), limit)
	if err != nil {
		return nil, fmt.Errorf("list case members: %w", err)
	}
	defer rows.Close()
	var result []CaseMember
	for rows.Next() {
		var item CaseMember
		var candidateID, recordID, operationID *string
		if err := rows.Scan(&item.MemberType, &candidateID, &recordID, &item.Role,
			&item.AttachedAt, &operationID); err != nil {
			return nil, fmt.Errorf("scan case member: %w", err)
		}
		item.CaseID, item.CandidateID, item.RecordID = caseID, optionalID(candidateID), optionalID(recordID)
		item.OperationID, item.AttachedAt = optionalID(operationID), item.AttachedAt.UTC()
		result = append(result, item)
	}
	return result, rows.Err()
}

func (store *Store) ListCaseEvents(ctx context.Context, caseID SemanticID, limit int) ([]CaseEvent, error) {
	if err := caseID.Validate(); err != nil {
		return nil, err
	}
	limit, err := boundedLimit(limit)
	if err != nil {
		return nil, err
	}
	rows, err := store.q.query(ctx, `
		SELECT id::text, schema_version, event_type, occurred_at, producer_json,
		       summary, outcome, evidence_json, operation_id::text, payload_json
		FROM provenance.case_events
		WHERE case_id = $1
		ORDER BY occurred_at, id
		LIMIT $2
	`, string(caseID), limit)
	if err != nil {
		return nil, fmt.Errorf("list case events: %w", err)
	}
	defer rows.Close()
	var result []CaseEvent
	for rows.Next() {
		var item CaseEvent
		var id string
		var producer, evidence []byte
		var operationID *string
		if err := rows.Scan(&id, &item.SchemaVersion, &item.EventType, &item.OccurredAt,
			&producer, &item.Summary, &item.Outcome, &evidence, &operationID,
			&item.Payload); err != nil {
			return nil, fmt.Errorf("scan case event: %w", err)
		}
		item.ID, item.CaseID, item.OperationID = SemanticID(id), caseID, optionalID(operationID)
		item.OccurredAt = item.OccurredAt.UTC()
		if err := decodeLosslessJSON(producer, &item.Producer); err != nil {
			return nil, fmt.Errorf("decode case event producer: %w", err)
		}
		if err := json.Unmarshal(evidence, &item.EvidenceSourceIDs); err != nil {
			return nil, fmt.Errorf("decode case event evidence: %w", err)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (store *Store) AppendRelationship(ctx context.Context, relationship Relationship) error {
	if err := validateRelationship(relationship); err != nil {
		return err
	}
	createdBy, err := json.Marshal(relationship.CreatedBy)
	if err != nil {
		return fmt.Errorf("encode relationship producer: %w", err)
	}
	evidence, err := json.Marshal(relationship.EvidenceSourceIDs)
	if err != nil {
		return fmt.Errorf("encode relationship evidence: %w", err)
	}
	_, err = store.q.exec(ctx, `
		INSERT INTO provenance.relationships(
			id, schema_version, relationship_type, from_record_id, to_record_id,
			resolution_case_id, context, coverage_json, evidence_json, created_at,
			created_by_json, created_by_operation_id, payload_json
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`, string(relationship.ID), relationship.SchemaVersion, relationship.RelationshipType,
		string(relationship.FromRecordID), string(relationship.ToRecordID),
		idArgument(relationship.ResolutionCaseID), relationship.Context,
		nullableJSON(relationship.Coverage), evidence, relationship.CreatedAt, createdBy,
		idArgument(relationship.CreatedByOperationID), []byte(relationship.Payload))
	return wrapStoreError("append relationship", err)
}

func (store *Store) GetRelationship(ctx context.Context, id SemanticID) (Relationship, bool, error) {
	if err := id.Validate(); err != nil {
		return Relationship{}, false, err
	}
	var item Relationship
	var rawID, fromID, toID string
	var caseID, operationID *string
	var createdBy, evidence []byte
	err := store.q.queryRow(ctx, `
		SELECT id::text, schema_version, relationship_type, from_record_id::text,
		       to_record_id::text, resolution_case_id::text, context, coverage_json,
		       evidence_json, created_at, created_by_json,
		       created_by_operation_id::text, payload_json
		FROM provenance.relationships WHERE id = $1
	`, string(id)).Scan(&rawID, &item.SchemaVersion, &item.RelationshipType, &fromID,
		&toID, &caseID, &item.Context, &item.Coverage, &evidence, &item.CreatedAt,
		&createdBy, &operationID, &item.Payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return Relationship{}, false, nil
	}
	if err != nil {
		return Relationship{}, false, fmt.Errorf("get relationship: %w", err)
	}
	item.ID, item.FromRecordID, item.ToRecordID = SemanticID(rawID), SemanticID(fromID), SemanticID(toID)
	item.ResolutionCaseID, item.CreatedByOperationID = optionalID(caseID), optionalID(operationID)
	item.CreatedAt = item.CreatedAt.UTC()
	if err := decodeLosslessJSON(createdBy, &item.CreatedBy); err != nil {
		return Relationship{}, false, fmt.Errorf("decode relationship producer: %w", err)
	}
	if err := json.Unmarshal(evidence, &item.EvidenceSourceIDs); err != nil {
		return Relationship{}, false, fmt.Errorf("decode relationship evidence: %w", err)
	}
	return item, true, nil
}

func (store *Store) ListRelationships(ctx context.Context, request PageRequest) (Page[Relationship], error) {
	request, err := request.normalized()
	if err != nil {
		return Page[Relationship]{}, err
	}
	rows, err := store.q.query(ctx, `
		SELECT relationship.id::text
		FROM provenance.relationships AS relationship
		JOIN provenance.records AS from_record ON from_record.id = relationship.from_record_id
		JOIN provenance.records AS to_record ON to_record.id = relationship.to_record_id
		WHERE ($1 = '' OR (from_record.domain = $1 AND to_record.domain = $1))
		  AND ($2 = '' OR (from_record.visibility = $2 AND to_record.visibility = $2))
		  AND ($3::timestamptz IS NULL OR (relationship.created_at, relationship.id) > ($3, $4::uuid))
		ORDER BY relationship.created_at, relationship.id
		LIMIT $5
	`, request.Domain, request.Visibility, request.AfterTime, idArgument(request.AfterID), request.Limit+1)
	if err != nil {
		return Page[Relationship]{}, fmt.Errorf("list relationships: %w", err)
	}
	defer rows.Close()
	ids := make([]SemanticID, 0, request.Limit+1)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return Page[Relationship]{}, fmt.Errorf("scan relationship page id: %w", err)
		}
		ids = append(ids, SemanticID(id))
	}
	if err := rows.Err(); err != nil {
		return Page[Relationship]{}, fmt.Errorf("iterate relationship page: %w", err)
	}
	items := make([]Relationship, 0, len(ids))
	for _, id := range ids {
		item, found, err := store.GetRelationship(ctx, id)
		if err != nil {
			return Page[Relationship]{}, err
		}
		if !found {
			return Page[Relationship]{}, fmt.Errorf("relationship %s disappeared during bounded list", id)
		}
		items = append(items, item)
	}
	return makePage(items, request.Limit, func(item Relationship) (time.Time, SemanticID) {
		return item.CreatedAt, item.ID
	}), nil
}

func (store *Store) AppendProcessingRun(ctx context.Context, run ProcessingRun) error {
	if err := validateProcessingRun(run); err != nil {
		return err
	}
	_, err := store.q.exec(ctx, `
		INSERT INTO provenance.processing_runs(
			id, run_kind, schema_version, input_hash, status, actor_json,
			bounds_json, started_at, completed_at, payload_json
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, string(run.ID), run.RunKind, run.SchemaVersion, run.InputHash, run.Status,
		[]byte(run.Actor), nullableJSON(run.Bounds), run.StartedAt, run.CompletedAt,
		[]byte(run.Payload))
	return wrapStoreError("append processing run", err)
}

func (store *Store) GetProcessingRun(ctx context.Context, id SemanticID) (ProcessingRun, bool, error) {
	if err := id.Validate(); err != nil {
		return ProcessingRun{}, false, err
	}
	var item ProcessingRun
	var rawID string
	err := store.q.queryRow(ctx, `
		SELECT id::text, run_kind, schema_version, input_hash, status, actor_json,
		       bounds_json, started_at, completed_at, payload_json
		FROM provenance.processing_runs WHERE id = $1
	`, string(id)).Scan(&rawID, &item.RunKind, &item.SchemaVersion, &item.InputHash,
		&item.Status, &item.Actor, &item.Bounds, &item.StartedAt, &item.CompletedAt,
		&item.Payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProcessingRun{}, false, nil
	}
	if err != nil {
		return ProcessingRun{}, false, fmt.Errorf("get processing run: %w", err)
	}
	item.ID, item.StartedAt = SemanticID(rawID), item.StartedAt.UTC()
	if item.CompletedAt != nil {
		completedAt := item.CompletedAt.UTC()
		item.CompletedAt = &completedAt
	}
	return item, true, nil
}

func (store *Store) AppendOperation(ctx context.Context, operation Operation) error {
	if err := validateOperation(operation); err != nil {
		return err
	}
	_, err := store.q.exec(ctx, `
		INSERT INTO provenance.operation_history(
			id, processing_run_id, pack_id, operation_type, schema_version,
			producer_json, occurred_at, input_hash, result, operation_json,
			result_json, applied_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`, string(operation.ID), idArgument(operation.ProcessingRunID), idArgument(operation.PackID),
		operation.OperationType, operation.SchemaVersion, []byte(operation.Producer),
		operation.OccurredAt, operation.InputHash, operation.Result,
		[]byte(operation.Operation), []byte(operation.ResultPayload), operation.AppliedAt)
	return wrapStoreError("append operation", err)
}

func (store *Store) GetOperation(ctx context.Context, id SemanticID) (Operation, bool, error) {
	if err := id.Validate(); err != nil {
		return Operation{}, false, err
	}
	var item Operation
	var rawID string
	var runID, packID *string
	err := store.q.queryRow(ctx, `
		SELECT id::text, processing_run_id::text, pack_id::text, operation_type,
		       schema_version, producer_json, occurred_at, input_hash, result,
		       operation_json, result_json, applied_at
		FROM provenance.operation_history WHERE id = $1
	`, string(id)).Scan(&rawID, &runID, &packID, &item.OperationType, &item.SchemaVersion,
		&item.Producer, &item.OccurredAt, &item.InputHash, &item.Result, &item.Operation,
		&item.ResultPayload, &item.AppliedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Operation{}, false, nil
	}
	if err != nil {
		return Operation{}, false, fmt.Errorf("get operation: %w", err)
	}
	item.ID, item.ProcessingRunID, item.PackID = SemanticID(rawID), optionalID(runID), optionalID(packID)
	item.OccurredAt, item.AppliedAt = item.OccurredAt.UTC(), item.AppliedAt.UTC()
	return item, true, nil
}

func (store *Store) AppendCandidateEvent(ctx context.Context, event LedgerEvent) error {
	return store.appendLedgerEvent(ctx, "candidate_events", "candidate_id", event)
}

func (store *Store) AppendRecordEvent(ctx context.Context, event LedgerEvent) error {
	return store.appendLedgerEvent(ctx, "record_events", "record_id", event)
}

func (store *Store) AppendRelationshipEvent(ctx context.Context, event LedgerEvent) error {
	return store.appendLedgerEvent(ctx, "relationship_events", "relationship_id", event)
}

func (store *Store) appendLedgerEvent(ctx context.Context, table, idColumn string, event LedgerEvent) error {
	if err := validateLedgerEvent(event); err != nil {
		return err
	}
	query := fmt.Sprintf(`
		INSERT INTO provenance.%s(%s, event_type, operation_id, occurred_at, payload_json)
		VALUES ($1, $2, $3, $4, $5)
	`, table, idColumn)
	_, err := store.q.exec(ctx, query, string(event.ObjectID), event.EventType,
		string(event.OperationID), event.OccurredAt, []byte(event.Payload))
	return wrapStoreError("append "+table, err)
}

func (store *Store) ListCandidateEvents(ctx context.Context, id SemanticID, limit int) ([]LedgerEvent, error) {
	return store.listLedgerEvents(ctx, "candidate_events", "candidate_id", id, limit)
}

func (store *Store) ListRecordEvents(ctx context.Context, id SemanticID, limit int) ([]LedgerEvent, error) {
	return store.listLedgerEvents(ctx, "record_events", "record_id", id, limit)
}

func (store *Store) ListRelationshipEvents(ctx context.Context, id SemanticID, limit int) ([]LedgerEvent, error) {
	return store.listLedgerEvents(ctx, "relationship_events", "relationship_id", id, limit)
}

func (store *Store) listLedgerEvents(ctx context.Context, table, idColumn string, id SemanticID, limit int) ([]LedgerEvent, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	limit, err := boundedLimit(limit)
	if err != nil {
		return nil, err
	}
	query := fmt.Sprintf(`
		SELECT event_type, operation_id::text, occurred_at, payload_json
		FROM provenance.%s WHERE %s = $1
		ORDER BY occurred_at, id LIMIT $2
	`, table, idColumn)
	rows, err := store.q.query(ctx, query, string(id), limit)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", table, err)
	}
	defer rows.Close()
	var result []LedgerEvent
	for rows.Next() {
		var item LedgerEvent
		var operationID string
		if err := rows.Scan(&item.EventType, &operationID, &item.OccurredAt, &item.Payload); err != nil {
			return nil, fmt.Errorf("scan %s: %w", table, err)
		}
		item.ObjectID, item.OperationID = id, SemanticID(operationID)
		item.OccurredAt = item.OccurredAt.UTC()
		result = append(result, item)
	}
	return result, rows.Err()
}

func (store *Store) AppendEvidenceRegistration(ctx context.Context, registration EvidenceRegistration) error {
	if err := validateEvidenceRegistration(registration); err != nil {
		return err
	}
	producer, err := json.Marshal(registration.Producer)
	if err != nil {
		return fmt.Errorf("encode evidence registration producer: %w", err)
	}
	_, err = store.q.exec(ctx, `
		INSERT INTO provenance.evidence_registrations(
			id, reconciliation_protocol_version, source_reference_id, domain,
			visibility, evidence_context, producer_json, registered_at,
			resolution_case_id, clarification_id, payload_json
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, string(registration.ID), registration.ReconciliationProtocolVersion,
		string(registration.SourceReferenceID), registration.Domain, registration.Visibility,
		registration.EvidenceContext, producer, registration.RegisteredAt,
		idArgument(registration.ResolutionCaseID), idArgument(registration.ClarificationID),
		[]byte(registration.Payload))
	return wrapStoreError("append evidence registration", err)
}

func (store *Store) GetEvidenceRegistration(ctx context.Context, sourceReferenceID SemanticID) (EvidenceRegistration, bool, error) {
	if err := sourceReferenceID.Validate(); err != nil {
		return EvidenceRegistration{}, false, err
	}
	var item EvidenceRegistration
	var rawID, rawSourceID string
	var producer []byte
	var caseID, clarificationID *string
	err := store.q.queryRow(ctx, `
		SELECT id::text, reconciliation_protocol_version, source_reference_id::text,
		       domain, visibility, evidence_context, producer_json, registered_at,
		       resolution_case_id::text, clarification_id::text, payload_json
		FROM provenance.evidence_registrations
		WHERE source_reference_id = $1
	`, string(sourceReferenceID)).Scan(&rawID, &item.ReconciliationProtocolVersion,
		&rawSourceID, &item.Domain, &item.Visibility, &item.EvidenceContext, &producer,
		&item.RegisteredAt, &caseID, &clarificationID, &item.Payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return EvidenceRegistration{}, false, nil
	}
	if err != nil {
		return EvidenceRegistration{}, false, fmt.Errorf("get evidence registration: %w", err)
	}
	item.ID, item.SourceReferenceID = SemanticID(rawID), SemanticID(rawSourceID)
	item.ResolutionCaseID, item.ClarificationID = optionalID(caseID), optionalID(clarificationID)
	item.RegisteredAt = item.RegisteredAt.UTC()
	if err := decodeLosslessJSON(producer, &item.Producer); err != nil {
		return EvidenceRegistration{}, false, fmt.Errorf("decode evidence registration producer: %w", err)
	}
	return item, true, nil
}

func (store *Store) AppendCandidateEvidenceLink(ctx context.Context, link CandidateEvidenceLink) error {
	if err := validateCandidateEvidenceLink(link); err != nil {
		return err
	}
	producer, err := json.Marshal(link.Producer)
	if err != nil {
		return fmt.Errorf("encode candidate evidence producer: %w", err)
	}
	_, err = store.q.exec(ctx, `
		INSERT INTO provenance.candidate_evidence_links(
			id, candidate_id, source_reference_id, operation_id, linked_at,
			producer_json, payload_json
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, string(link.ID), string(link.CandidateID), string(link.SourceReferenceID),
		string(link.OperationID), link.LinkedAt, producer, []byte(link.Payload))
	return wrapStoreError("append candidate evidence link", err)
}

func (store *Store) AppendCandidateLineage(ctx context.Context, lineage CandidateLineage) error {
	if err := lineage.CandidateID.Validate(); err != nil {
		return err
	}
	if err := lineage.DerivedFromCandidateID.Validate(); err != nil {
		return err
	}
	if lineage.CandidateID == lineage.DerivedFromCandidateID {
		return errors.New("candidate cannot derive from itself")
	}
	if err := validateTimestamp(lineage.RegisteredAt, "candidate lineage registered_at"); err != nil {
		return err
	}
	_, err := store.q.exec(ctx, `
		INSERT INTO provenance.candidate_lineage(
			candidate_id, derived_from_candidate_id, registered_at
		) VALUES ($1, $2, $3)
	`, string(lineage.CandidateID), string(lineage.DerivedFromCandidateID), lineage.RegisteredAt)
	return wrapStoreError("append candidate lineage", err)
}

func (store *Store) ListCandidateEvidenceSourceIDs(ctx context.Context, candidateID SemanticID, limit int) ([]SemanticID, error) {
	if err := candidateID.Validate(); err != nil {
		return nil, err
	}
	limit, err := boundedLimit(limit)
	if err != nil {
		return nil, err
	}
	rows, err := store.q.query(ctx, `
		SELECT source_reference_id::text
		FROM provenance.candidate_evidence_links
		WHERE candidate_id = $1
		ORDER BY linked_at, id
		LIMIT $2
	`, string(candidateID), limit)
	if err != nil {
		return nil, fmt.Errorf("list candidate evidence: %w", err)
	}
	defer rows.Close()
	return scanSemanticIDs(rows, "candidate evidence")
}

func (store *Store) ListCandidateLineage(ctx context.Context, candidateID SemanticID, limit int) ([]SemanticID, error) {
	if err := candidateID.Validate(); err != nil {
		return nil, err
	}
	limit, err := boundedLimit(limit)
	if err != nil {
		return nil, err
	}
	rows, err := store.q.query(ctx, `
		SELECT derived_from_candidate_id::text
		FROM provenance.candidate_lineage
		WHERE candidate_id = $1
		ORDER BY registered_at, id
		LIMIT $2
	`, string(candidateID), limit)
	if err != nil {
		return nil, fmt.Errorf("list candidate lineage: %w", err)
	}
	defer rows.Close()
	return scanSemanticIDs(rows, "candidate lineage")
}

func (store *Store) PutRegistrationReplay(ctx context.Context, replay RegistrationReplay) (RegistrationReplay, bool, error) {
	if err := validateRegistrationReplay(replay); err != nil {
		return RegistrationReplay{}, false, err
	}
	producer, err := json.Marshal(replay.Producer)
	if err != nil {
		return RegistrationReplay{}, false, fmt.Errorf("encode replay producer: %w", err)
	}
	candidateIDs, err := json.Marshal(replay.CandidateIDs)
	if err != nil {
		return RegistrationReplay{}, false, fmt.Errorf("encode replay candidate ids: %w", err)
	}
	rows, err := store.q.exec(ctx, `
		INSERT INTO provenance.registration_replays(
			replay_key, workflow_key, registration_digest, producer_json,
			candidate_ids_json, receipts_json, registered_at, payload_json
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT DO NOTHING
	`, replay.ReplayKey, replay.WorkflowKey, replay.RegistrationDigest, producer,
		candidateIDs, []byte(replay.Receipts), replay.RegisteredAt, []byte(replay.Payload))
	if err != nil {
		return RegistrationReplay{}, false, wrapStoreError("append registration replay", err)
	}
	if rows == 1 {
		return replay, false, nil
	}

	stored, found, err := store.getRegistrationReplayByKey(ctx, replay.ReplayKey)
	if err != nil {
		return RegistrationReplay{}, false, err
	}
	if found {
		if !sameReplay(stored, replay) {
			return RegistrationReplay{}, false, ErrReplayConflict
		}
		return stored, true, nil
	}
	stored, found, err = store.GetRegistrationReplay(ctx, replay.WorkflowKey, replay.RegistrationDigest)
	if err != nil {
		return RegistrationReplay{}, false, err
	}
	if !found {
		return RegistrationReplay{}, false, errors.New("registration replay conflict had no durable winner")
	}
	if !sameReplayContent(stored, replay) {
		return RegistrationReplay{}, false, ErrReplayConflict
	}
	return stored, true, nil
}

func (store *Store) GetRegistrationReplay(ctx context.Context, workflowKey, digest string) (RegistrationReplay, bool, error) {
	if err := validateRequired(workflowKey, "workflow key"); err != nil {
		return RegistrationReplay{}, false, err
	}
	if err := validateRequired(digest, "registration digest"); err != nil {
		return RegistrationReplay{}, false, err
	}
	return store.scanRegistrationReplay(store.q.queryRow(ctx, `
		SELECT replay_key, workflow_key, registration_digest, producer_json,
		       candidate_ids_json, receipts_json, registered_at, payload_json
		FROM provenance.registration_replays
		WHERE workflow_key = $1 AND registration_digest = $2
	`, workflowKey, digest))
}

func (store *Store) getRegistrationReplayByKey(ctx context.Context, replayKey string) (RegistrationReplay, bool, error) {
	return store.scanRegistrationReplay(store.q.queryRow(ctx, `
		SELECT replay_key, workflow_key, registration_digest, producer_json,
		       candidate_ids_json, receipts_json, registered_at, payload_json
		FROM provenance.registration_replays WHERE replay_key = $1
	`, replayKey))
}

func (store *Store) scanRegistrationReplay(row pgx.Row) (RegistrationReplay, bool, error) {
	var replay RegistrationReplay
	var producer, candidateIDs []byte
	err := row.Scan(&replay.ReplayKey, &replay.WorkflowKey, &replay.RegistrationDigest,
		&producer, &candidateIDs, &replay.Receipts, &replay.RegisteredAt, &replay.Payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return RegistrationReplay{}, false, nil
	}
	if err != nil {
		return RegistrationReplay{}, false, fmt.Errorf("get registration replay: %w", err)
	}
	if err := decodeLosslessJSON(producer, &replay.Producer); err != nil {
		return RegistrationReplay{}, false, fmt.Errorf("decode replay producer: %w", err)
	}
	if err := json.Unmarshal(candidateIDs, &replay.CandidateIDs); err != nil {
		return RegistrationReplay{}, false, fmt.Errorf("decode replay candidate ids: %w", err)
	}
	replay.RegisteredAt = replay.RegisteredAt.UTC()
	return replay, true, nil
}

func validateCandidate(candidate Candidate) error {
	for _, check := range []error{
		candidate.ID.Validate(), validateRequired(candidate.SchemaVersion, "candidate schema version"),
		validateRequired(candidate.State, "candidate state"), validateRequired(candidate.Domain, "candidate domain"),
		validateRequired(candidate.Visibility, "candidate visibility"), validateRequired(candidate.RecordKind, "candidate record kind"),
		validateRequired(candidate.Claim, "candidate claim"), validateRequired(candidate.RecordContext, "candidate record context"),
		validateRequired(candidate.AssertionPosture, "candidate assertion posture"), validateRequired(candidate.ProducerID, "candidate producer id"),
		validateTimestamp(candidate.RegisteredAt, "candidate registered_at"), validateDocument(candidate.Submitted, "candidate submitted", false),
		validateDocument(candidate.Payload, "candidate payload", false),
	} {
		if check != nil {
			return check
		}
	}
	if candidate.SchemaVersion != SchemaVersion || candidate.State != "pending" {
		return errors.New("candidate requires schema_version 1.0 and pending state")
	}
	return nil
}

func validateSource(source SourceReference) error {
	checks := []error{source.ID.Validate(), validateRequired(source.SchemaVersion, "source schema version"),
		validateRequired(source.SourceKind, "source kind"), validateRequired(source.Status, "source status"),
		validateRequired(source.VerificationPosture, "source verification posture"), validateRequired(source.ResolverName, "source resolver name"),
		validateRequired(source.ResolverVersion, "source resolver version"), validateTimestamp(source.ResolutionAt, "source resolution_at"),
		validateDocument(source.Submitted, "source submitted", false), validateDocument(source.Payload, "source payload", false)}
	for _, check := range checks {
		if check != nil {
			return check
		}
	}
	if source.CandidateID != nil {
		if err := source.CandidateID.Validate(); err != nil {
			return err
		}
	}
	if source.SchemaVersion != SchemaVersion {
		return errors.New("source reference requires schema_version 1.0")
	}
	if source.Status == "resolved" {
		if source.CanonicalLocator == nil || source.GapReason != nil {
			return errors.New("resolved source requires a canonical locator and no gap reason")
		}
	} else if source.Status == "unresolved" {
		if source.GapReason == nil {
			return errors.New("unresolved source requires a gap reason")
		}
	} else {
		return errors.New("source status must be resolved or unresolved")
	}
	return nil
}

func validateRecord(record Record) error {
	checks := []error{record.ID.Validate(), validateRequired(record.SchemaVersion, "record schema version"),
		validateRequired(record.Claim, "record claim"), validateRequired(record.RecordKind, "record kind"),
		validateRequired(record.RecordContext, "record context"), validateRequired(record.Domain, "record domain"),
		validateRequired(record.Visibility, "record visibility"), validateRequired(record.AssertionPosture, "record assertion posture"),
		validateRequired(record.Temporal.Interpretation, "record temporal interpretation"), validateTimestamp(record.CreatedAt, "record created_at"),
		validateDocument(record.Payload, "record payload", false)}
	for _, check := range checks {
		if check != nil {
			return check
		}
	}
	if record.SchemaVersion != SchemaVersion {
		return errors.New("record requires schema_version 1.0")
	}
	if record.Temporal.ValidFrom != nil && record.Temporal.ValidUntil != nil && record.Temporal.ValidUntil.Before(*record.Temporal.ValidFrom) {
		return errors.New("record valid_until cannot precede valid_from")
	}
	return nil
}

func validateResolutionCase(item ResolutionCase) error {
	for _, check := range []error{item.ID.Validate(), validateRequired(item.SchemaVersion, "case schema version"),
		validateRequired(item.Issue, "case issue"), validateRequired(item.Domain, "case domain"),
		validateRequired(item.Visibility, "case visibility"), validateRequired(item.InitialStatus, "case initial status"),
		validateTimestamp(item.CreatedAt, "case created_at"), validateDocument(item.Payload, "case payload", false)} {
		if check != nil {
			return check
		}
	}
	if item.CreatedByOperationID != nil {
		return item.CreatedByOperationID.Validate()
	}
	return nil
}

func validateCaseMember(member CaseMember) error {
	if err := member.CaseID.Validate(); err != nil {
		return err
	}
	if err := validateRequired(member.Role, "case member role"); err != nil {
		return err
	}
	if err := validateTimestamp(member.AttachedAt, "case member attached_at"); err != nil {
		return err
	}
	if member.MemberType == "candidate" && member.CandidateID != nil && member.RecordID == nil {
		return member.CandidateID.Validate()
	}
	if member.MemberType == "record" && member.RecordID != nil && member.CandidateID == nil {
		return member.RecordID.Validate()
	}
	return errors.New("case member requires exactly one matching candidate or record id")
}

func validateCaseEvent(event CaseEvent) error {
	for _, check := range []error{event.ID.Validate(), event.CaseID.Validate(),
		validateRequired(event.SchemaVersion, "case event schema version"), validateRequired(event.EventType, "case event type"),
		validateTimestamp(event.OccurredAt, "case event occurred_at"), validateProducer(event.Producer),
		validateRequired(event.Summary, "case event summary"), validateDocument(event.Payload, "case event payload", false)} {
		if check != nil {
			return check
		}
	}
	if event.OperationID != nil {
		return event.OperationID.Validate()
	}
	return nil
}

func validateRelationship(item Relationship) error {
	for _, check := range []error{item.ID.Validate(), item.FromRecordID.Validate(), item.ToRecordID.Validate(),
		validateRequired(item.SchemaVersion, "relationship schema version"), validateRequired(item.RelationshipType, "relationship type"),
		validateTimestamp(item.CreatedAt, "relationship created_at"), validateProducer(item.CreatedBy),
		validateDocument(item.Coverage, "relationship coverage", true), validateDocument(item.Payload, "relationship payload", false)} {
		if check != nil {
			return check
		}
	}
	if item.FromRecordID == item.ToRecordID {
		return errors.New("relationship endpoints must differ")
	}
	return nil
}

func validateProcessingRun(run ProcessingRun) error {
	for _, check := range []error{run.ID.Validate(), validateRequired(run.RunKind, "processing run kind"),
		validateRequired(run.SchemaVersion, "processing run schema version"), validateRequired(run.InputHash, "processing run input hash"),
		validateRequired(run.Status, "processing run status"), validateDocument(run.Actor, "processing run actor", false),
		validateDocument(run.Bounds, "processing run bounds", true), validateTimestamp(run.StartedAt, "processing run started_at"),
		validateDocument(run.Payload, "processing run payload", false)} {
		if check != nil {
			return check
		}
	}
	if run.CompletedAt != nil && run.CompletedAt.Before(run.StartedAt) {
		return errors.New("processing run completed_at cannot precede started_at")
	}
	return nil
}

func validateOperation(operation Operation) error {
	for _, check := range []error{operation.ID.Validate(), validateRequired(operation.OperationType, "operation type"),
		validateRequired(operation.SchemaVersion, "operation schema version"), validateDocument(operation.Producer, "operation producer", false),
		validateTimestamp(operation.OccurredAt, "operation occurred_at"), validateRequired(operation.InputHash, "operation input hash"),
		validateRequired(operation.Result, "operation result"), validateDocument(operation.Operation, "operation payload", false),
		validateDocument(operation.ResultPayload, "operation result payload", false), validateTimestamp(operation.AppliedAt, "operation applied_at")} {
		if check != nil {
			return check
		}
	}
	return nil
}

func validateLedgerEvent(event LedgerEvent) error {
	for _, check := range []error{event.ObjectID.Validate(), event.OperationID.Validate(),
		validateRequired(event.EventType, "ledger event type"), validateTimestamp(event.OccurredAt, "ledger event occurred_at"),
		validateDocument(event.Payload, "ledger event payload", false)} {
		if check != nil {
			return check
		}
	}
	return nil
}

func validateEvidenceRegistration(item EvidenceRegistration) error {
	for _, check := range []error{item.ID.Validate(), item.SourceReferenceID.Validate(),
		validateRequired(item.ReconciliationProtocolVersion, "reconciliation protocol version"),
		validateRequired(item.Domain, "evidence registration domain"), validateRequired(item.Visibility, "evidence registration visibility"),
		validateRequired(item.EvidenceContext, "evidence registration context"), validateProducer(item.Producer),
		validateTimestamp(item.RegisteredAt, "evidence registration registered_at"), validateDocument(item.Payload, "evidence registration payload", false)} {
		if check != nil {
			return check
		}
	}
	if item.ReconciliationProtocolVersion != ReconciliationProtocolVersion {
		return errors.New("evidence registration requires reconciliation protocol 2.0")
	}
	if item.ClarificationID != nil && item.ResolutionCaseID == nil {
		return errors.New("clarification evidence requires a resolution case")
	}
	return nil
}

func validateCandidateEvidenceLink(link CandidateEvidenceLink) error {
	for _, check := range []error{link.ID.Validate(), link.CandidateID.Validate(),
		link.SourceReferenceID.Validate(), link.OperationID.Validate(), validateTimestamp(link.LinkedAt, "candidate evidence linked_at"),
		validateProducer(link.Producer), validateDocument(link.Payload, "candidate evidence payload", false)} {
		if check != nil {
			return check
		}
	}
	return nil
}

func validateRegistrationReplay(replay RegistrationReplay) error {
	for _, check := range []error{validateRequired(replay.ReplayKey, "replay key"),
		validateRequired(replay.WorkflowKey, "workflow key"), validateRequired(replay.RegistrationDigest, "registration digest"),
		validateProducer(replay.Producer), validateDocument(replay.Receipts, "replay receipts", false),
		validateTimestamp(replay.RegisteredAt, "replay registered_at"), validateDocument(replay.Payload, "replay payload", false)} {
		if check != nil {
			return check
		}
	}
	if len(replay.CandidateIDs) == 0 {
		return errors.New("registration replay requires at least one candidate id")
	}
	for _, id := range replay.CandidateIDs {
		if err := id.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func validateProducer(producer ProducerIdentity) error {
	if err := validateRequired(producer.ProducerID, "producer id"); err != nil {
		return err
	}
	if err := validateRequired(producer.ProducerKind, "producer kind"); err != nil {
		return err
	}
	if producer.RunID != nil {
		return producer.RunID.Validate()
	}
	return nil
}

func boundedLimit(limit int) (int, error) {
	if limit == 0 {
		return DefaultPageLimit, nil
	}
	if limit < 1 || limit > MaximumPageLimit {
		return 0, fmt.Errorf("limit must be between 1 and %d", MaximumPageLimit)
	}
	return limit, nil
}

func idArgument(id *SemanticID) any {
	if id == nil {
		return nil
	}
	return string(*id)
}

func optionalID(value *string) *SemanticID {
	if value == nil {
		return nil
	}
	id := SemanticID(*value)
	return &id
}

func nullableJSON(value []byte) any {
	if len(bytes.TrimSpace(value)) == 0 {
		return nil
	}
	return value
}

func sameReplay(left, right RegistrationReplay) bool {
	return left.ReplayKey == right.ReplayKey && sameReplayContent(left, right)
}

func sameReplayContent(left, right RegistrationReplay) bool {
	leftProducer, leftProducerErr := json.Marshal(left.Producer)
	rightProducer, rightProducerErr := json.Marshal(right.Producer)
	if leftProducerErr != nil || rightProducerErr != nil ||
		left.WorkflowKey != right.WorkflowKey || left.RegistrationDigest != right.RegistrationDigest ||
		!left.RegisteredAt.UTC().Truncate(time.Microsecond).Equal(right.RegisteredAt.UTC().Truncate(time.Microsecond)) ||
		!jsonEqual(leftProducer, rightProducer) || len(left.CandidateIDs) != len(right.CandidateIDs) ||
		!jsonEqual(left.Receipts, right.Receipts) || !jsonEqual(left.Payload, right.Payload) {
		return false
	}
	for index := range left.CandidateIDs {
		if left.CandidateIDs[index] != right.CandidateIDs[index] {
			return false
		}
	}
	return true
}

func jsonEqual(left, right json.RawMessage) bool {
	leftCanonical, leftOK := canonicalJSON(left)
	rightCanonical, rightOK := canonicalJSON(right)
	if !leftOK || !rightOK {
		return bytes.Equal(bytes.TrimSpace(left), bytes.TrimSpace(right))
	}
	return bytes.Equal(leftCanonical, rightCanonical)
}

func canonicalJSON(value json.RawMessage) ([]byte, bool) {
	if !json.Valid(value) {
		return nil, false
	}
	var decoded any
	if err := decodeLosslessJSON(value, &decoded); err != nil {
		return nil, false
	}
	canonical, err := json.Marshal(decoded)
	return canonical, err == nil
}

func decodeLosslessJSON(value []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	return decoder.Decode(destination)
}

func scanSemanticIDs(rows pgx.Rows, kind string) ([]SemanticID, error) {
	var result []SemanticID
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan %s id: %w", kind, err)
		}
		result = append(result, SemanticID(id))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s ids: %w", kind, err)
	}
	return result, nil
}

func makePage[T any](items []T, limit int, cursor func(T) (time.Time, SemanticID)) Page[T] {
	page := Page[T]{Items: items, Truncated: len(items) > limit}
	if page.Truncated {
		page.Items = page.Items[:limit]
	}
	if len(page.Items) > 0 {
		cursorTime, cursorID := cursor(page.Items[len(page.Items)-1])
		page.NextTime, page.NextID = &cursorTime, &cursorID
	}
	return page
}

func wrapStoreError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
