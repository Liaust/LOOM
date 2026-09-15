package provenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"loom.local/loom/internal/ids"
)

func (store *Store) resolveSearchProject(ctx context.Context, slug string) (string, error) {
	var matches []string
	err := store.q.queryRow(ctx, `
		WITH latest AS (
			SELECT DISTINCT ON (project_id) project_id, projection_json
			FROM provenance.project_projection_snapshots
			ORDER BY project_id, projection_revision DESC, id
		), matching AS (
			SELECT project_id FROM latest
			WHERE lower(projection_json->>'slug') = lower($1::text)
			ORDER BY project_id LIMIT 2
		)
		SELECT coalesce(array_agg(project_id), ARRAY[]::text[]) FROM matching
	`, slug).Scan(&matches)
	if err != nil {
		return "", fmt.Errorf("resolve provenance search project: %w", err)
	}
	if len(matches) != 1 {
		return "", &FoundationValidationError{Err: errors.New("project filter must be an exact project ID or one uniquely observed project slug; use loom project list to find its ID")}
	}
	return matches[0], nil
}

const (
	searchAcceptedRecordPriority   = 0
	searchUnresolvedCasePriority   = 1
	searchPendingCandidatePriority = 2
	// Repository state is appended after the three lifecycle collections so
	// cursors issued for those collections retain their original priorities.
	searchRepositoryStatePriority = 3
	searchProjectStatePriority    = 4
)

type searchQueryCursor struct {
	Enabled            bool
	Score              int
	CollectionPriority int
	SortTime           time.Time
	ID                 string
}

type searchQueryRow struct {
	ID                 string
	Collection         SearchCollection
	CollectionPriority int
	Score              int
	SortTime           time.Time
	SearchableSummary  string
	RepositoryCard     *RepositoryCard
	ProjectContext     *ProjectContextProjection
	ProjectSnapshotID  SemanticID
	ObservedAt         *time.Time
}

func (store *Store) searchAcceptedRecords(ctx context.Context, terms []string, projectID, repositoryID string, cursor searchQueryCursor, limit int) ([]searchQueryRow, bool, error) {
	rows, err := store.q.query(ctx, `
		WITH searchable AS (
			SELECT record.id,
			       record.created_at AS sort_time,
			       lower(concat_ws(' ', record.claim, record.record_context,
			           record.record_kind, record.domain, coalesce(record.anchors_json::text, ''))) AS haystack
			FROM provenance.records AS record
			WHERE ($2::text = '' OR coalesce(record.anchors_json->'projects', '[]'::jsonb) ? $2::text)
			  AND ($3::text = '' OR coalesce(record.anchors_json->'entities', '[]'::jsonb) ? $3::text)
			  AND NOT EXISTS (
				SELECT 1
				FROM provenance.relationships AS relationship
				WHERE relationship.to_record_id = record.id
				  AND lower(btrim(relationship.relationship_type)) IN
				      ('supersedes', 'corrects', 'correcting', 'refines')
				  AND NOT EXISTS (
					SELECT 1 FROM provenance.relationship_events AS relationship_event
					WHERE relationship_event.relationship_id = relationship.id
					  AND relationship_event.event_type = 'ended'
				  )
			  )
			  AND NOT EXISTS (
				SELECT 1
				FROM provenance.case_members AS member
				JOIN provenance.resolution_cases AS resolution_case ON resolution_case.id = member.case_id
				WHERE member.record_id = record.id
				  AND lower(btrim(resolution_case.initial_status)) NOT IN ('closed', 'resolved', 'dismissed')
				  AND NOT EXISTS (
					SELECT 1 FROM provenance.case_events AS case_event
					WHERE case_event.case_id = resolution_case.id
					  AND lower(btrim(case_event.outcome)) IN ('closed', 'resolved', 'dismissed')
				  )
			  )
		), ranked AS (
			SELECT id, sort_time,
			       (SELECT count(*)::int FROM unnest($1::text[]) AS term
			        WHERE position(term IN haystack) > 0) AS score
			FROM searchable
		)
		SELECT id::text, score, sort_time
		FROM ranked
		WHERE score > 0 AND score * 5 >= cardinality($1::text[]) * 3
		  AND (
			NOT $4::boolean OR score < $5::integer OR
			(score = $5::integer AND (
				$6::integer > $7::integer OR
				($6::integer = $7::integer AND (sort_time < $8::timestamptz OR (sort_time = $8::timestamptz AND id > $9::uuid)))
			))
		  )
		ORDER BY score DESC, sort_time DESC, id
		LIMIT $10::integer
		`, terms, projectID, repositoryID, cursor.Enabled, cursor.Score,
		searchAcceptedRecordPriority, cursor.CollectionPriority, nullableSearchCursorTime(cursor),
		nullableSearchCursorID(cursor, searchAcceptedRecordPriority), limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("search accepted provenance records: %w", err)
	}
	return scanSearchRows(rows, SearchCollectionAcceptedRecords, searchAcceptedRecordPriority, limit)
}

func (store *Store) searchUnresolvedCases(ctx context.Context, terms []string, projectID, repositoryID string, cursor searchQueryCursor, limit int) ([]searchQueryRow, bool, error) {
	rows, err := store.q.query(ctx, `
		WITH searchable AS (
			SELECT resolution_case.id,
			       resolution_case.created_at AS sort_time,
			       lower(concat_ws(' ', resolution_case.issue, resolution_case.domain,
			           resolution_case.initial_status)) AS haystack
			FROM provenance.resolution_cases AS resolution_case
			WHERE lower(btrim(resolution_case.initial_status)) NOT IN ('closed', 'resolved', 'dismissed')
			  AND NOT EXISTS (
				SELECT 1 FROM provenance.case_events AS case_event
				WHERE case_event.case_id = resolution_case.id
				  AND lower(btrim(case_event.outcome)) IN ('closed', 'resolved', 'dismissed')
			  )
			  AND ($2::text = '' OR EXISTS (
				SELECT 1
				FROM provenance.case_members AS member
				LEFT JOIN provenance.records AS record ON record.id = member.record_id
				LEFT JOIN provenance.candidates AS candidate ON candidate.id = member.candidate_id
				WHERE member.case_id = resolution_case.id
				  AND (
					coalesce(record.anchors_json->'projects', '[]'::jsonb) ? $2::text OR
					coalesce(candidate.submitted_json->'anchors'->'projects', '[]'::jsonb) ? $2::text
				  )
			  ))
			  AND ($3::text = '' OR EXISTS (
				SELECT 1
				FROM provenance.case_members AS member
				LEFT JOIN provenance.records AS record ON record.id = member.record_id
				LEFT JOIN provenance.candidates AS candidate ON candidate.id = member.candidate_id
				WHERE member.case_id = resolution_case.id
				  AND (
					coalesce(record.anchors_json->'entities', '[]'::jsonb) ? $3::text OR
					coalesce(candidate.submitted_json->'anchors'->'entities', '[]'::jsonb) ? $3::text
				  )
			  ))
		), ranked AS (
			SELECT id, sort_time,
			       (SELECT count(*)::int FROM unnest($1::text[]) AS term
			        WHERE position(term IN haystack) > 0) AS score
			FROM searchable
		)
		SELECT id::text, score, sort_time
		FROM ranked
		WHERE score > 0 AND score * 5 >= cardinality($1::text[]) * 3
		  AND (
			NOT $4::boolean OR score < $5::integer OR
			(score = $5::integer AND (
				$6::integer > $7::integer OR
				($6::integer = $7::integer AND (sort_time < $8::timestamptz OR (sort_time = $8::timestamptz AND id > $9::uuid)))
			))
		  )
		ORDER BY score DESC, sort_time DESC, id
		LIMIT $10::integer
		`, terms, projectID, repositoryID, cursor.Enabled, cursor.Score,
		searchUnresolvedCasePriority, cursor.CollectionPriority, nullableSearchCursorTime(cursor),
		nullableSearchCursorID(cursor, searchUnresolvedCasePriority), limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("search unresolved provenance cases: %w", err)
	}
	return scanSearchRows(rows, SearchCollectionUnresolvedCases, searchUnresolvedCasePriority, limit)
}

func (store *Store) searchPendingCandidates(ctx context.Context, terms []string, projectID, repositoryID string, cursor searchQueryCursor, limit int) ([]searchQueryRow, bool, error) {
	rows, err := store.q.query(ctx, `
		WITH searchable AS (
			SELECT candidate.id,
			       candidate.registered_at AS sort_time,
			       lower(concat_ws(' ', candidate.claim, candidate.record_context,
			           candidate.record_kind, candidate.domain,
			           coalesce((candidate.submitted_json->'anchors')::text, ''))) AS haystack
			FROM provenance.candidates AS candidate
			WHERE NOT EXISTS (
				SELECT 1 FROM provenance.candidate_events AS candidate_event
				WHERE candidate_event.candidate_id = candidate.id
				  AND candidate_event.event_type IN ('accepted', 'consolidated', 'rejected')
			  )
			  AND ($2::text = '' OR coalesce(candidate.submitted_json->'anchors'->'projects', '[]'::jsonb) ? $2::text)
			  AND ($3::text = '' OR coalesce(candidate.submitted_json->'anchors'->'entities', '[]'::jsonb) ? $3::text)
		), ranked AS (
			SELECT id, sort_time,
			       (SELECT count(*)::int FROM unnest($1::text[]) AS term
			        WHERE position(term IN haystack) > 0) AS score
			FROM searchable
		)
		SELECT id::text, score, sort_time
		FROM ranked
		WHERE score > 0 AND score * 5 >= cardinality($1::text[]) * 3
		  AND (
			NOT $4::boolean OR score < $5::integer OR
			(score = $5::integer AND (
				$6::integer > $7::integer OR
				($6::integer = $7::integer AND (sort_time < $8::timestamptz OR (sort_time = $8::timestamptz AND id > $9::uuid)))
			))
		  )
		ORDER BY score DESC, sort_time DESC, id
		LIMIT $10::integer
		`, terms, projectID, repositoryID, cursor.Enabled, cursor.Score,
		searchPendingCandidatePriority, cursor.CollectionPriority, nullableSearchCursorTime(cursor),
		nullableSearchCursorID(cursor, searchPendingCandidatePriority), limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("search pending provenance candidates: %w", err)
	}
	return scanSearchRows(rows, SearchCollectionPendingCandidates, searchPendingCandidatePriority, limit)
}

func (store *Store) searchRepositoryState(ctx context.Context, terms []string, projectID, repositoryID string, cursor searchQueryCursor, limit int) ([]searchQueryRow, bool, error) {
	rows, err := store.q.query(ctx, `
		WITH latest_projects AS (
			SELECT DISTINCT ON (project_id) id, project_id
			FROM provenance.project_projection_snapshots
			ORDER BY project_id, projection_revision DESC, id
		), latest AS (
			SELECT DISTINCT ON (repository.repository_id)
			       repository.repository_id AS id,
			       repository.project_id,
			       repository.created_at AS sort_time,
			       repository.searchable_summary,
			       repository.card_json
			FROM provenance.repository_projection_snapshots AS repository
			JOIN latest_projects AS project
			  ON project.project_id=repository.project_id
			 AND project.id=repository.project_snapshot_id
			ORDER BY repository.repository_id, repository.source_revision DESC,
			         repository.created_at DESC, repository.id
		), searchable AS (
			SELECT id, project_id, sort_time, searchable_summary, card_json,
			       lower(searchable_summary) AS haystack
			FROM latest
			WHERE ($2::text = '' OR project_id=$2::text)
			  AND ($3::text = '' OR id=$3::text)
		), ranked AS (
			SELECT id, project_id, sort_time, searchable_summary, card_json,
			       (SELECT count(*)::int FROM unnest($1::text[]) AS term
			        WHERE position(term IN haystack) > 0) AS score
			FROM searchable
		)
		SELECT id, score, sort_time, searchable_summary, project_id, card_json
		FROM ranked
		WHERE score > 0 AND score * 5 >= cardinality($1::text[]) * 3
		  AND (
			NOT $4::boolean OR score < $5::integer OR
			(score = $5::integer AND (
				$6::integer > $7::integer OR
				($6::integer = $7::integer AND
				 (sort_time < $8::timestamptz OR
				  (sort_time = $8::timestamptz AND id > $9::text)))
			))
		  )
		ORDER BY score DESC, sort_time DESC, id
		LIMIT $10::integer
	`, terms, projectID, repositoryID, cursor.Enabled, cursor.Score,
		searchRepositoryStatePriority, cursor.CollectionPriority, nullableSearchCursorTime(cursor),
		nullableSearchCursorID(cursor, searchRepositoryStatePriority), limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("search repository provenance state: %w", err)
	}
	return scanRepositorySearchRows(rows, limit)
}

func (store *Store) searchProjectState(ctx context.Context, terms []string, projectID, repositoryID string, cursor searchQueryCursor, limit int) ([]searchQueryRow, bool, error) {
	rows, err := store.q.query(ctx, `
		WITH latest AS (
			SELECT DISTINCT ON (snapshot.project_id) snapshot.id AS snapshot_id, snapshot.project_id AS id,
			       snapshot.created_at AS sort_time, observed_at, searchable_summary, projection_json
			FROM provenance.project_projection_snapshots AS snapshot
			ORDER BY snapshot.project_id, snapshot.projection_revision DESC, snapshot.id
		), searchable AS (
			SELECT *, lower(searchable_summary) AS haystack
			FROM latest
			WHERE ($2::text = '' OR id=$2::text)
			  AND ($3::text = '' OR EXISTS (
			      SELECT 1 FROM jsonb_array_elements(coalesce(projection_json->'memberships', '[]'::jsonb)) AS binding
			      WHERE binding->>'repository_id'=$3::text
			        AND binding->>'owner_project_id'=latest.id
			        AND binding->>'role' IN ('primary', 'component')
			  ) OR (NOT (projection_json ? 'memberships_captured') AND EXISTS (
			      SELECT 1 FROM provenance.repository_projection_snapshots AS repository
			      WHERE repository.project_id=latest.id
			        AND repository.project_snapshot_id=latest.snapshot_id
			        AND repository.repository_id=$3::text
			  )))
		), ranked AS (
			SELECT *, (SELECT count(*)::int FROM unnest($1::text[]) AS term
			           WHERE position(term IN haystack) > 0) AS score
			FROM searchable
		)
		SELECT id, score, sort_time, searchable_summary, snapshot_id::text, observed_at, projection_json
		FROM ranked
		WHERE score > 0 AND score * 5 >= cardinality($1::text[]) * 3
		  AND (
			NOT $4::boolean OR score < $5::integer OR
			(score = $5::integer AND (
				$6::integer > $7::integer OR
				($6::integer = $7::integer AND
				 (sort_time < $8::timestamptz OR
				  (sort_time = $8::timestamptz AND id > $9::text)))
			))
		  )
		ORDER BY score DESC, sort_time DESC, id
		LIMIT $10::integer
	`, terms, projectID, repositoryID, cursor.Enabled, cursor.Score,
		searchProjectStatePriority, cursor.CollectionPriority, nullableSearchCursorTime(cursor),
		nullableSearchCursorID(cursor, searchProjectStatePriority), limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("search project provenance state: %w", err)
	}
	return scanProjectSearchRows(rows, limit)
}

func scanProjectSearchRows(rows searchRows, limit int) ([]searchQueryRow, bool, error) {
	defer rows.Close()
	result := make([]searchQueryRow, 0, limit+1)
	for rows.Next() {
		var row searchQueryRow
		var rawSnapshotID string
		var observedAt time.Time
		var payload []byte
		if err := rows.Scan(&row.ID, &row.Score, &row.SortTime, &row.SearchableSummary, &rawSnapshotID, &observedAt, &payload); err != nil {
			return nil, false, fmt.Errorf("scan project state search result: %w", err)
		}
		if err := ids.Validate("project", row.ID); err != nil {
			return nil, false, fmt.Errorf("validate project search result: %w", err)
		}
		var err error
		row.ProjectSnapshotID, err = ParseSemanticID(rawSnapshotID)
		if err != nil {
			return nil, false, fmt.Errorf("validate project search snapshot: %w", err)
		}
		project, err := decodeProjectContext(payload, row.ID)
		if err != nil {
			return nil, false, err
		}
		row.ProjectContext = &project
		row.ObservedAt = ptrTime(observedAt)
		row.SortTime = row.SortTime.UTC()
		row.Collection = SearchCollectionProjectState
		row.CollectionPriority = searchProjectStatePriority
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate project search results: %w", err)
	}
	return result, len(result) > limit, nil
}

type searchRows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close()
}

func scanSearchRows(rows searchRows, collection SearchCollection, priority, limit int) ([]searchQueryRow, bool, error) {
	defer rows.Close()
	result := make([]searchQueryRow, 0, limit+1)
	for rows.Next() {
		var rawID string
		var row searchQueryRow
		if err := rows.Scan(&rawID, &row.Score, &row.SortTime); err != nil {
			return nil, false, fmt.Errorf("scan %s search result: %w", collection, err)
		}
		id, err := ParseSemanticID(rawID)
		if err != nil {
			return nil, false, fmt.Errorf("validate %s search result id: %w", collection, err)
		}
		row.ID = string(id)
		row.Collection = collection
		row.CollectionPriority = priority
		row.SortTime = row.SortTime.UTC()
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate %s search results: %w", collection, err)
	}
	truncated := len(result) > limit
	return result, truncated, nil
}

func scanRepositorySearchRows(rows searchRows, limit int) ([]searchQueryRow, bool, error) {
	defer rows.Close()
	result := make([]searchQueryRow, 0, limit+1)
	for rows.Next() {
		var rawID, projectID string
		var payload []byte
		var row searchQueryRow
		if err := rows.Scan(&rawID, &row.Score, &row.SortTime, &row.SearchableSummary, &projectID, &payload); err != nil {
			return nil, false, fmt.Errorf("scan %s search result: %w", SearchCollectionRepositoryState, err)
		}
		if err := ids.Validate("repo", rawID); err != nil {
			return nil, false, fmt.Errorf("validate %s search result id: %w", SearchCollectionRepositoryState, err)
		}
		var card RepositoryCard
		if err := json.Unmarshal(payload, &card); err != nil {
			return nil, false, fmt.Errorf("decode %s search result %s: %w", SearchCollectionRepositoryState, rawID, err)
		}
		if card.RepositoryID != rawID || card.OwningProject.ProjectID != projectID {
			return nil, false, fmt.Errorf("%s search result %s has mismatched projected identity", SearchCollectionRepositoryState, rawID)
		}
		row.ID = rawID
		row.Collection = SearchCollectionRepositoryState
		row.CollectionPriority = searchRepositoryStatePriority
		row.SortTime = row.SortTime.UTC()
		row.RepositoryCard = &card
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate %s search results: %w", SearchCollectionRepositoryState, err)
	}
	return result, len(result) > limit, nil
}

func nullableSearchCursorTime(cursor searchQueryCursor) any {
	if !cursor.Enabled {
		return nil
	}
	return cursor.SortTime.UTC()
}

func nullableSearchCursorID(cursor searchQueryCursor, priority int) any {
	if !cursor.Enabled || cursor.CollectionPriority != priority {
		return nil
	}
	return cursor.ID
}
