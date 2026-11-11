package projects

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"loom.local/loom/internal/boxregistry"
)

var ErrInvalidOverviewFilter = errors.New("invalid project overview filter")

// OverviewFilter selects a live registry page. Empty WorkState and Status mean
// all; Query is a literal, case-insensitive substring of name, slug or description.
type OverviewFilter struct {
	Limit     int
	WorkState string
	Status    string
	Query     string
	Cursor    string
}

type ProjectOverviewCounts struct {
	Working      int64 `json:"working"`
	Passive      int64 `json:"passive"`
	Archived     int64 `json:"archived"`
	Unclassified int64 `json:"unclassified"`
}

// ProjectOverviewItem contains no policy/configuration fields. WorkState is the
// presentation group, not an inferred or persisted declaration. Name and
// description are compact previews (at most 160 and 512 characters respectively).
type ProjectOverviewItem struct {
	ProjectID      string    `json:"project_id"`
	ProjectScopeID string    `json:"project_scope_id"`
	Name           string    `json:"name"`
	Slug           string    `json:"slug"`
	Description    string    `json:"description"`
	Status         string    `json:"status"`
	WorkState      string    `json:"work_state"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type ProjectOverview struct {
	selectionDigest string
	ObservedAt      time.Time             `json:"observed_at"`
	Counts          ProjectOverviewCounts `json:"counts"`
	Total           int64                 `json:"total"`
	FilteredTotal   int64                 `json:"filtered_total"`
	Items           []ProjectOverviewItem `json:"items"`
	NextCursor      string                `json:"next_cursor"`
	Complete        bool                  `json:"complete"`
}

type projectOverviewCursor struct {
	SelectionDigest string    `json:"selection_digest,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
	ProjectID       string    `json:"project_id"`
}

// Overview returns global counts and the total matching the filters before
// pagination, from one statement snapshot and the same visibility as ListProjects.
// Pages are live, ordered by updated_at DESC, project_id DESC. Concurrent changes
// can move rows between pages; Complete only means this read saw no later row.
// Counts are recomputed per call, not a retained snapshot or a loaded-page count.
func (s Service) Overview(ctx context.Context, filter OverviewFilter) (ProjectOverview, error) {
	filter, cursor, err := normalizeOverviewFilter(filter)
	if err != nil {
		return ProjectOverview{}, err
	}
	var after any
	if !cursor.UpdatedAt.IsZero() {
		after = cursor.UpdatedAt
	}
	var result ProjectOverview
	if selection, ok := boxregistry.FromContext(ctx); ok {
		result.selectionDigest = selection.Digest
		if result.selectionDigest == "" {
			raw, _ := json.Marshal(selection)
			result.selectionDigest = boxregistry.Digest("selection", string(raw), "")
		}
	}
	if filter.Cursor != "" && cursor.SelectionDigest != result.selectionDigest {
		return ProjectOverview{}, fmt.Errorf("%w: cursor belongs to a different Box selection", ErrInvalidOverviewFilter)
	}
	var items []byte
	args := []any{filter.WorkState, filter.Query, filter.Status, after, cursor.ProjectID, filter.Limit + 1}
	selection := projectBoxSelectionSQL(ctx, "p", &args)
	query := projectOverviewSQL
	if selection != "true" {
		query = strings.Replace(query, "/* box_selection */", "WHERE "+selection, 1)
	}
	if err := s.DB.QueryRowContext(ctx, query, args...).Scan(&result.ObservedAt, &result.Total, &result.Counts.Working,
		&result.Counts.Passive, &result.Counts.Archived, &result.Counts.Unclassified,
		&result.FilteredTotal, &items); err != nil {
		return ProjectOverview{}, err
	}
	if err := json.Unmarshal(items, &result.Items); err != nil {
		return ProjectOverview{}, fmt.Errorf("decode project overview items: %w", err)
	}
	return finishProjectOverview(result, filter.Limit)
}

func normalizeOverviewFilter(filter OverviewFilter) (OverviewFilter, projectOverviewCursor, error) {
	var cursor projectOverviewCursor
	if filter.Limit == 0 {
		filter.Limit = 24
	}
	if filter.Limit < 1 || filter.Limit > 100 {
		return filter, cursor, fmt.Errorf("%w: limit must be between 1 and 100", ErrInvalidOverviewFilter)
	}
	filter.WorkState = strings.TrimSpace(filter.WorkState)
	if filter.WorkState == "" {
		filter.WorkState = "all"
	}
	switch filter.WorkState {
	case "all", "working", "passive", "archived", "unclassified":
	default:
		return filter, cursor, fmt.Errorf("%w: unsupported work_state", ErrInvalidOverviewFilter)
	}
	filter.Status = strings.TrimSpace(filter.Status)
	if filter.Status == "" {
		filter.Status = "all"
	}
	switch filter.Status {
	case "all", "active", "paused", "completed", "archived", "abandoned", "blocked":
	default:
		return filter, cursor, fmt.Errorf("%w: unsupported status", ErrInvalidOverviewFilter)
	}
	if !utf8.ValidString(filter.Query) || utf8.RuneCountInString(filter.Query) > 160 || strings.ContainsRune(filter.Query, 0) {
		return filter, cursor, fmt.Errorf("%w: query must be valid text of at most 160 characters", ErrInvalidOverviewFilter)
	}
	filter.Query = strings.TrimSpace(filter.Query)
	if filter.Cursor == "" {
		return filter, cursor, nil
	}
	invalidCursor := fmt.Errorf("%w: invalid cursor", ErrInvalidOverviewFilter)
	if len(filter.Cursor) > 1024 {
		return filter, cursor, invalidCursor
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(filter.Cursor)
	if err != nil {
		return filter, cursor, invalidCursor
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return filter, cursor, invalidCursor
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return filter, cursor, invalidCursor
	}
	if cursor.UpdatedAt.IsZero() || cursor.UpdatedAt.Year() < 1 || !strings.HasPrefix(cursor.ProjectID, "project_") ||
		len(cursor.ProjectID) <= len("project_") || len(cursor.ProjectID) > 256 ||
		!utf8.ValidString(cursor.ProjectID) || strings.ContainsRune(cursor.ProjectID, 0) {
		return filter, cursor, invalidCursor
	}
	return filter, cursor, nil
}

func finishProjectOverview(result ProjectOverview, limit int) (ProjectOverview, error) {
	if result.Items == nil {
		result.Items = []ProjectOverviewItem{}
	}
	result.Complete = len(result.Items) <= limit
	if !result.Complete {
		result.Items = result.Items[:limit]
	}
	for {
		result.NextCursor = ""
		if !result.Complete {
			last := result.Items[len(result.Items)-1]
			raw, err := json.Marshal(projectOverviewCursor{UpdatedAt: last.UpdatedAt, ProjectID: last.ProjectID, SelectionDigest: result.selectionDigest})
			if err != nil {
				return ProjectOverview{}, err
			}
			result.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
		}
		raw, err := json.Marshal(result)
		if err != nil {
			return ProjectOverview{}, err
		}
		if len(raw) <= 128*1024 {
			return result, nil
		}
		if len(result.Items) <= 1 {
			return ProjectOverview{}, fmt.Errorf("project overview item exceeds response size limit")
		}
		result.Items = result.Items[:len(result.Items)-1]
		result.Complete = false
	}
}

// Keep the scope join aligned with projectSelectSQL/ListProjects. Classification
// is defined once, before both aggregation and filtering. The lookahead is not
// included in the returned page and cursor predicates never affect the counts.
const projectOverviewSQL = `
	WITH visible AS (
		SELECT p.project_id, p.project_scope_id, p.name, p.slug, p.description,
		       p.status, p.updated_at,
		       CASE WHEN p.status = 'archived' THEN 'archived'
		            WHEN p.metadata->>'work_state' IN ('working', 'passive')
		              THEN p.metadata->>'work_state'
		            ELSE 'unclassified' END AS work_state
		FROM projects.projects p
		JOIN scopes.scopes scope ON scope.scope_id = p.project_scope_id
		/* box_selection */
	), filtered AS (
		SELECT * FROM visible
		WHERE ($1 = 'all' OR work_state = $1)
		  AND ($2 = '' OR strpos(lower(name), lower($2)) > 0
		       OR strpos(lower(slug), lower($2)) > 0
		       OR strpos(lower(description), lower($2)) > 0)
		  AND ($3 = 'all' OR status = $3)
	), counts AS (
		SELECT count(*) AS total,
		       count(*) FILTER (WHERE work_state = 'working') AS working,
		       count(*) FILTER (WHERE work_state = 'passive') AS passive,
		       count(*) FILTER (WHERE work_state = 'archived') AS archived,
		       count(*) FILTER (WHERE work_state = 'unclassified') AS unclassified
		FROM visible
	), page AS (
		SELECT project_id, project_scope_id, left(name, 160) AS name, slug,
		       left(description, 512) AS description, status, work_state, updated_at
		FROM filtered
		WHERE ($4::timestamptz IS NULL OR (updated_at, project_id) < ($4::timestamptz, $5::text))
		ORDER BY updated_at DESC, project_id DESC
		LIMIT $6
	)
	SELECT statement_timestamp(), total, working, passive, archived, unclassified,
	       (SELECT count(*) FROM filtered),
	       COALESCE((SELECT jsonb_agg(page ORDER BY updated_at DESC, project_id DESC) FROM page), '[]'::jsonb)
	FROM counts
`
