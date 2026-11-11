package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type OverviewFilter struct {
	Hours      int
	ProjectRef string
	Limit      int
	State      string
	Since      *time.Time
	Until      *time.Time
}

type Overview struct {
	ObservedAt     time.Time        `json:"observed_at"`
	Counts         OverviewCounts   `json:"counts"`
	Window         OverviewWindow   `json:"window"`
	Buckets        []OverviewBucket `json:"buckets"`
	Items          []OverviewItem   `json:"items"`
	ItemsWindow    *OverviewWindow  `json:"items_window"`
	ItemsTruncated bool             `json:"items_truncated"`
}

type OverviewCounts struct {
	Queued    int64 `json:"queued"`
	Running   int64 `json:"running"`
	Attention int64 `json:"attention"`
}

type OverviewWindow struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

type OverviewBucket struct {
	At        time.Time `json:"at"`
	Succeeded int64     `json:"succeeded"`
	Failed    int64     `json:"failed"`
	Cancelled int64     `json:"cancelled"`
}

// OverviewItem deliberately excludes job payloads, diagnostics and configuration.
type OverviewItem struct {
	ID                     string     `json:"id"`
	JobID                  string     `json:"job_id"`
	JobType                string     `json:"job_type"`
	DisplayName            string     `json:"display_name"`
	FailureCode            string     `json:"failure_code"`
	Status                 string     `json:"status"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
	QueuedAt               *time.Time `json:"queued_at"`
	StartedAt              *time.Time `json:"started_at"`
	CompletedAt            *time.Time `json:"completed_at"`
	FailedAt               *time.Time `json:"failed_at"`
	CancelledAt            *time.Time `json:"cancelled_at"`
	FailureAttentionStatus string     `json:"failure_attention_status"`
	ManualActionRequired   bool       `json:"manual_action_required"`
	ScopeID                *string    `json:"scope_id"`
}

// Overview reads one consistent snapshot. Counts are current and independent of
// item filters; outcomes cover [start, end). Items have their own explicit range,
// or no age cutoff for running/queued/attention. ItemsTruncated is not an audit
// cursor or a claim that the returned rows exhaust retained job history.
// Outcomes describe the latest retained terminal state, not individual attempts.
func (s Service) Overview(ctx context.Context, filter OverviewFilter) (Overview, error) {
	if filter.Hours != 1 && filter.Hours != 24 {
		return Overview{}, fmt.Errorf("overview hours must be 1 or 24")
	}
	if filter.Limit <= 0 {
		filter.Limit = 30
	} else if filter.Limit > 100 {
		filter.Limit = 100
	}
	filter.State = strings.ToLower(strings.TrimSpace(filter.State))
	if filter.State == "" {
		filter.State = "all"
	}
	switch filter.State {
	case "all", "running", "queued", "attention", "completed":
	default:
		return Overview{}, fmt.Errorf("invalid overview state %q", filter.State)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	itemsWindow, err := overviewItemsWindow(filter, now)
	if err != nil {
		return Overview{}, err
	}
	if s.DB == nil {
		return Overview{}, fmt.Errorf("job overview is not configured")
	}
	scopeID, err := s.resolveOptionalScope(ctx, strings.TrimSpace(filter.ProjectRef), "")
	if err != nil {
		return Overview{}, err
	}

	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return Overview{}, err
	}
	defer tx.Rollback()

	start := now.Add(-time.Duration(filter.Hours) * time.Hour)
	bucketCount := 60
	if filter.Hours == 24 {
		bucketCount = 96
	}
	stride := now.Sub(start) / time.Duration(bucketCount)
	result := Overview{
		ObservedAt:  now,
		Window:      OverviewWindow{Start: start, End: now},
		Buckets:     make([]OverviewBucket, bucketCount),
		Items:       []OverviewItem{},
		ItemsWindow: itemsWindow,
	}
	for i := range result.Buckets {
		result.Buckets[i].At = start.Add(time.Duration(i) * stride)
	}

	itemsSQL, itemArgs := overviewItemsQuery(filter.State, itemsWindow, scopeID, filter.Limit+1, ctx)
	countArgs := []any{}
	outcomeArgs := []any{start, now, stride.Seconds()}
	if scopeID != "" {
		countArgs = append(countArgs, scopeID)
		outcomeArgs = append(outcomeArgs, scopeID)
	}
	countsSQL, outcomesSQL := overviewQueries(scopeID != "", jobBoxPredicate(ctx, &countArgs), jobBoxPredicate(ctx, &outcomeArgs))
	if err := tx.QueryRowContext(ctx, countsSQL, countArgs...).Scan(
		&result.Counts.Queued, &result.Counts.Running, &result.Counts.Attention,
	); err != nil {
		return Overview{}, err
	}

	rows, err := tx.QueryContext(ctx, outcomesSQL, outcomeArgs...)
	if err != nil {
		return Overview{}, err
	}
	for rows.Next() {
		var index int
		var succeeded, failed, cancelled int64
		if err := rows.Scan(&index, &succeeded, &failed, &cancelled); err != nil {
			rows.Close()
			return Overview{}, err
		}
		if index < 0 || index >= len(result.Buckets) {
			rows.Close()
			return Overview{}, fmt.Errorf("job overview bucket outside requested window")
		}
		result.Buckets[index].Succeeded = succeeded
		result.Buckets[index].Failed = failed
		result.Buckets[index].Cancelled = cancelled
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Overview{}, err
	}

	rows, err = tx.QueryContext(ctx, itemsSQL, itemArgs...)
	if err != nil {
		return Overview{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var item OverviewItem
		if err := rows.Scan(&item.JobID, &item.JobType, &item.Status,
			&item.CreatedAt, &item.UpdatedAt, &item.QueuedAt, &item.StartedAt,
			&item.CompletedAt, &item.FailedAt, &item.CancelledAt,
			&item.FailureAttentionStatus, &item.ManualActionRequired, &item.ScopeID, &item.DisplayName, &item.FailureCode); err != nil {
			return Overview{}, err
		}
		item.ID = item.JobID
		result.Items = append(result.Items, item)
	}
	if err := rows.Err(); err != nil {
		return Overview{}, err
	}
	if err := rows.Close(); err != nil {
		return Overview{}, err
	}
	if len(result.Items) > filter.Limit {
		result.ItemsTruncated = true
		result.Items = result.Items[:filter.Limit]
	}
	if err := tx.Commit(); err != nil {
		return Overview{}, err
	}
	return result, nil
}

const overviewAttentionPredicate = `j.failure_attention_status = 'active'
	AND (j.status IN ('failed', 'timed_out') OR j.manual_action_required = true)`

func overviewQueries(scoped bool, boxPredicates ...string) (counts, outcomes string) {
	countScope, rangeScope := "", ""
	if scoped {
		countScope = " AND j.scope_id = $1"
		rangeScope = " AND j.scope_id = $4"
	}
	if len(boxPredicates) == 2 {
		countScope += " AND " + boxPredicates[0]
		rangeScope += " AND " + boxPredicates[1]
	}
	// Separate current-state predicates can use the status and failure-attention
	// indexes. The attention OR is a union of jobs, not a sum of overlapping counts.
	counts = fmt.Sprintf(`SELECT
		(SELECT COUNT(*) FROM jobs.jobs j WHERE j.status = 'queued'%s),
		(SELECT COUNT(*) FROM jobs.jobs j WHERE j.status = 'running'%s),
		(SELECT COUNT(*) FROM jobs.jobs j
		 WHERE %s%s)`, countScope, countScope, overviewAttentionPredicate, countScope)
	// Each disjoint terminal state uses its own outcome timestamp and range
	// predicate, so retained timestamps from earlier attempts cannot contribute.
	outcomes = fmt.Sprintf(`SELECT
		FLOOR(EXTRACT(EPOCH FROM (outcome_at - $1::timestamptz)) / $3)::int AS bucket,
		COUNT(*) FILTER (WHERE status = 'completed'),
		COUNT(*) FILTER (WHERE status IN ('failed', 'timed_out')),
		COUNT(*) FILTER (WHERE status = 'cancelled')
		FROM (
			SELECT j.completed_at AS outcome_at, j.status FROM jobs.jobs j
			WHERE j.status = 'completed' AND j.completed_at >= $1 AND j.completed_at < $2%s
			UNION ALL
			SELECT j.failed_at AS outcome_at, j.status FROM jobs.jobs j
			WHERE j.status IN ('failed', 'timed_out') AND j.failed_at >= $1 AND j.failed_at < $2%s
			UNION ALL
			SELECT j.cancelled_at AS outcome_at, j.status FROM jobs.jobs j
			WHERE j.status = 'cancelled' AND j.cancelled_at >= $1 AND j.cancelled_at < $2%s
		) terminal_outcomes GROUP BY bucket ORDER BY bucket`, rangeScope, rangeScope, rangeScope)
	return counts, outcomes
}

// Explicit bounds are item-only and may refer to an earlier chart observation.
// Cap their span rather than rejecting a valid bucket as the live window moves.
// A missing Until defaults to now; a missing Since to Until minus Hours.
func overviewItemsWindow(filter OverviewFilter, now time.Time) (*OverviewWindow, error) {
	span := time.Duration(filter.Hours) * time.Hour
	if filter.Since == nil && filter.Until == nil {
		switch filter.State {
		case "running", "queued", "attention":
			return nil, nil
		case "all":
			span = 24 * time.Hour
		}
		return &OverviewWindow{Start: now.Add(-span), End: now}, nil
	}
	end := now
	if filter.Until != nil {
		end = filter.Until.UTC()
	}
	start := end.Add(-span)
	if filter.Since != nil {
		start = filter.Since.UTC()
	}
	if start.IsZero() || end.IsZero() || !start.Before(end) || end.Sub(start) > span {
		return nil, fmt.Errorf("overview item range must be nonempty and at most %d hours", filter.Hours)
	}
	return &OverviewWindow{Start: start, End: end}, nil
}

func overviewItemsQuery(state string, window *OverviewWindow, scopeID string, limit int, contexts ...context.Context) (string, []any) {
	query := `SELECT j.job_id, j.job_type, j.status, j.created_at, j.updated_at,
		j.queued_at, j.started_at, j.completed_at, j.failed_at, j.cancelled_at,
		j.failure_attention_status, j.manual_action_required, j.scope_id,
		COALESCE((SELECT s.name FROM packages.scripts s WHERE j.target_kind='script' AND s.script_id=j.target_id),replace(j.job_type,'_',' ')),
		COALESCE(j.failure_code,'')
		FROM jobs.jobs j WHERE true`
	args := []any{}
	order := "j.updated_at DESC, j.job_id DESC"
	switch state {
	case "running":
		query += " AND j.status = 'running'"
	case "queued":
		query += " AND j.status = 'queued'"
	case "attention":
		query += " AND " + overviewAttentionPredicate
	case "completed":
		// Finished graph records include every terminal outcome, not just success.
		// Keep timestamp predicates on their columns so range indexes remain usable.
		query += ` AND (
			(j.status = 'completed' AND j.completed_at >= $1 AND j.completed_at < $2)
			OR (j.status IN ('failed', 'timed_out') AND j.failed_at >= $1 AND j.failed_at < $2)
			OR (j.status = 'cancelled' AND j.cancelled_at >= $1 AND j.cancelled_at < $2))`
		order = `CASE WHEN j.status = 'completed' THEN j.completed_at
			WHEN j.status IN ('failed', 'timed_out') THEN j.failed_at
			WHEN j.status = 'cancelled' THEN j.cancelled_at END DESC, j.job_id DESC`
	}
	if window != nil {
		args = append(args, window.Start, window.End)
		if state != "completed" {
			query += " AND j.updated_at >= $1 AND j.updated_at < $2"
		}
	}
	if scopeID != "" {
		args = append(args, scopeID)
		query += fmt.Sprintf(" AND j.scope_id = $%d", len(args))
	}
	if len(contexts) > 0 {
		query += " AND " + jobBoxPredicate(contexts[0], &args)
	}
	args = append(args, limit)
	query += fmt.Sprintf(" ORDER BY %s LIMIT $%d", order, len(args))
	return query, args
}
