package automation

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"loom.local/loom/internal/jobs"
)

var ErrOverviewCursor = errors.New("schedule overview cursor is invalid or belongs to different filters or Box selection")

const (
	maxOverviewOccurrences = 500
	maxOverviewBytes       = 127 * 1024 // Leave room for the API envelope's 128 KiB bound.
)

type OverviewFilter struct {
	Days          int
	Limit         int
	Cursor        string
	Status        string
	AutomationRef string
	ProjectRef    string
}

type Overview struct {
	ObservedAt     time.Time            `json:"observed_at"`
	Items          []OverviewSchedule   `json:"items"`
	NextCursor     string               `json:"next_cursor"`
	Complete       bool                 `json:"complete"`
	Counts         OverviewCounts       `json:"counts"`
	Occurrences    []OverviewOccurrence `json:"occurrences"`
	Window         OverviewWindow       `json:"window"`
	PreviewLimited bool                 `json:"preview_limited"`
	Truncated      bool                 `json:"truncated"`
	Coverage       OverviewCoverage     `json:"coverage"`
}

// Counts cover the entire filtered inventory, not just this cursor page.
// Completed one-shots contribute to Total, but not Active/Paused/Disabled.
type OverviewCounts struct {
	Active   int64 `json:"active"`
	Paused   int64 `json:"paused"`
	Disabled int64 `json:"disabled"`
	Total    int64 `json:"total"`
}

type OverviewSchedule struct {
	ScheduleID         string          `json:"schedule_id"`
	DisplayName        string          `json:"display_name"`
	ScheduleKey        string          `json:"schedule_key"`
	Status             string          `json:"status"`
	ScheduleKind       string          `json:"schedule_kind"`
	ScheduleExpr       string          `json:"schedule_expr"`
	Timezone           string          `json:"timezone"`
	NextFireAt         *time.Time      `json:"next_fire_at"`
	LastFireAt         *time.Time      `json:"last_fire_at"`
	LastScheduleFireID *string         `json:"last_schedule_fire_id"`
	LastResult         *OverviewResult `json:"last_result"`
	MetadataLimited    bool            `json:"metadata_limited,omitempty"`
}

// Source qualifies the result: invocation success means dispatched, not proof
// that an asynchronously queued job completed or produced its intended effects.
type OverviewResult struct {
	Source string    `json:"source"`
	Status string    `json:"status"`
	At     time.Time `json:"at"`
}

type OverviewOccurrence struct {
	ScheduleID string    `json:"schedule_id"`
	At         time.Time `json:"at"`
}

type OverviewWindow struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// Preview covers this selected page only, over [start, end). Complete also
// requires that the page is the entire inventory, with no unavailable timing.
// Occurrences are nominal timing, not promises of invocation or catch-up.
type OverviewCoverage struct {
	SelectedSchedules      int      `json:"selected_schedules"`
	TotalSchedules         int64    `json:"total_schedules"`
	Complete               bool     `json:"complete"`
	UnavailableScheduleIDs []string `json:"unavailable_schedule_ids"`
}

type overviewCursor struct {
	CreatedAt       time.Time `json:"created_at"`
	ScheduleID      string    `json:"schedule_id"`
	Status          string    `json:"status,omitempty"`
	AutomationRef   string    `json:"automation_ref,omitempty"`
	ProjectRef      string    `json:"project_ref,omitempty"`
	SelectionDigest string    `json:"selection_digest,omitempty"`
}

type overviewRow struct {
	Item      OverviewSchedule
	Timing    Schedule
	CreatedAt time.Time
}

// Overview is a read-only snapshot within one request. Pagination is live:
// immutable created_at/schedule_id keys prevent tie loss, but intervening
// insertions, deletions and filter/status changes are not a frozen export.
func (s Service) Overview(ctx context.Context, filter OverviewFilter) (Overview, error) {
	filter, cursor, err := normalizeOverviewFilter(filter)
	if err != nil {
		return Overview{}, err
	}
	selectionDigest := jobs.BoxSelectionDigest(ctx)
	if cursor != nil && cursor.SelectionDigest != selectionDigest {
		return Overview{}, ErrOverviewCursor
	}
	if s.DB == nil {
		return Overview{}, fmt.Errorf("database is required")
	}
	predicate, args, err := s.overviewPredicate(ctx, filter)
	if err != nil {
		return Overview{}, err
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return Overview{}, err
	}
	defer tx.Rollback()
	result := Overview{ObservedAt: time.Now().UTC(), Items: []OverviewSchedule{}}
	result.Window = OverviewWindow{Start: result.ObservedAt, End: result.ObservedAt.Add(time.Duration(filter.Days) * 24 * time.Hour)}
	err = tx.QueryRowContext(ctx, `SELECT
		count(*) FILTER (WHERE schedules.status = 'active'),
		count(*) FILTER (WHERE schedules.status = 'paused'),
		count(*) FILTER (WHERE schedules.status = 'disabled'), count(*)
		FROM automation.schedules WHERE true `+predicate, args...).Scan(
		&result.Counts.Active, &result.Counts.Paused, &result.Counts.Disabled, &result.Counts.Total)
	if err != nil {
		return Overview{}, err
	}
	if cursor != nil {
		args = append(args, cursor.CreatedAt, cursor.ScheduleID)
		predicate += fmt.Sprintf(" AND (schedules.created_at, schedules.schedule_id) < ($%d, $%d)", len(args)-1, len(args))
	}
	args = append(args, filter.Limit+1)
	rows, err := tx.QueryContext(ctx, overviewPageSQL(predicate, len(args)), args...)
	if err != nil {
		return Overview{}, err
	}
	selected := []overviewRow{}
	for rows.Next() {
		row, err := scanOverviewRow(rows)
		if err != nil {
			rows.Close()
			return Overview{}, err
		}
		selected = append(selected, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Overview{}, err
	}
	if err := tx.Commit(); err != nil {
		return Overview{}, err
	}
	result.Complete = len(selected) <= filter.Limit
	if !result.Complete {
		selected = selected[:filter.Limit]
	}
	// Keep exceptional long labels from making the operator response unbounded.
	// A shortened page retains its continuation; it never drops inventory rows.
	for {
		result.Items = make([]OverviewSchedule, 0, len(selected))
		for _, row := range selected {
			result.Items = append(result.Items, row.Item)
		}
		result.NextCursor = ""
		if !result.Complete && len(selected) > 0 {
			last := selected[len(selected)-1]
			encoded, err := json.Marshal(overviewCursor{CreatedAt: last.CreatedAt, ScheduleID: last.Item.ScheduleID,
				Status: filter.Status, AutomationRef: filter.AutomationRef, ProjectRef: filter.ProjectRef, SelectionDigest: selectionDigest})
			if err != nil {
				return Overview{}, err
			}
			result.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
		}
		result.Occurrences, result.Truncated, result.Coverage.UnavailableScheduleIDs, err = overviewOccurrences(ctx, selected, result.Window)
		if err != nil {
			return Overview{}, err
		}
		result.Coverage.SelectedSchedules = len(selected)
		result.Coverage.TotalSchedules = result.Counts.Total
		result.Coverage.Complete = cursor == nil && result.Complete && !result.Truncated && len(result.Coverage.UnavailableScheduleIDs) == 0
		result.PreviewLimited = !result.Coverage.Complete
		encoded, err := json.Marshal(result)
		if err != nil {
			return Overview{}, err
		}
		if len(encoded) <= maxOverviewBytes {
			return result, nil
		}
		if len(selected) <= 1 {
			return Overview{}, fmt.Errorf("schedule overview metadata exceeds response limit")
		}
		selected = selected[:len(selected)-1]
		result.Complete = false
	}
}

func normalizeOverviewFilter(filter OverviewFilter) (OverviewFilter, *overviewCursor, error) {
	if filter.Days == 0 {
		filter.Days = 1
	}
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	if filter.Days < 1 || filter.Days > 7 || filter.Limit < 1 || filter.Limit > 100 {
		return filter, nil, fmt.Errorf("overview days must be 1..7 and limit must be 1..100")
	}
	filter.Status = strings.TrimSpace(filter.Status)
	filter.AutomationRef = strings.TrimSpace(filter.AutomationRef)
	filter.ProjectRef = strings.TrimSpace(filter.ProjectRef)
	switch filter.Status {
	case "", ScheduleStatusActive, ScheduleStatusPaused, ScheduleStatusDisabled, ScheduleStatusCompleted:
	default:
		return filter, nil, fmt.Errorf("invalid schedule overview status")
	}
	if filter.Cursor == "" {
		return filter, nil, nil
	}
	if len(filter.Cursor) > 4096 {
		return filter, nil, ErrOverviewCursor
	}
	raw, err := base64.RawURLEncoding.DecodeString(filter.Cursor)
	var cursor overviewCursor
	if err != nil || json.Unmarshal(raw, &cursor) != nil || cursor.CreatedAt.IsZero() || cursor.ScheduleID == "" ||
		cursor.Status != filter.Status || cursor.AutomationRef != filter.AutomationRef || cursor.ProjectRef != filter.ProjectRef {
		return filter, nil, ErrOverviewCursor
	}
	return filter, &cursor, nil
}

func (s Service) overviewPredicate(ctx context.Context, filter OverviewFilter) (string, []any, error) {
	predicate := ""
	args := []any{}
	add := func(column string, value any) {
		args = append(args, value)
		predicate += fmt.Sprintf(" AND %s = $%d", column, len(args))
	}
	if filter.Status != "" {
		add("schedules.status", filter.Status)
	}
	if filter.AutomationRef != "" {
		automation, err := s.getAutomation(ctx, filter.AutomationRef)
		if err != nil {
			return "", nil, err
		}
		add("schedules.automation_id", automation.AutomationID)
	}
	if filter.ProjectRef != "" {
		projectID, err := resolveProjectIDIncludingArchive(ctx, s.DB, filter.ProjectRef, true)
		if err != nil {
			return "", nil, err
		}
		add("schedules.project_id", projectID)
	}
	if !hasExplicitRuntimeRef(filter.AutomationRef, filter.ProjectRef) {
		predicate += archivedProjectByProjectColumnSQL("schedules.project_id")
	}
	predicate += " AND " + scheduleBoxPredicate(ctx, &args)
	return predicate, args, nil
}

func overviewPageSQL(predicate string, limitParam int) string {
	return `WITH selected AS (
		SELECT schedule_id, left(display_name, 256) AS display_name,
		       left(schedule_key, 256) AS schedule_key, status, schedule_kind,
		       schedule_expr, timezone, start_at, next_fire_at, last_fire_at,
		       last_schedule_fire_id, created_at,
		       (length(display_name) > 256 OR length(schedule_key) > 256) AS metadata_limited
		FROM automation.schedules WHERE true ` + predicate + fmt.Sprintf(`
		ORDER BY schedules.created_at DESC, schedules.schedule_id DESC LIMIT $%d
	)
	SELECT s.schedule_id, s.display_name, s.schedule_key, s.status, s.schedule_kind,
	       s.schedule_expr, s.timezone, s.start_at, s.next_fire_at, s.last_fire_at,
	       s.last_schedule_fire_id, s.created_at, s.metadata_limited,
	       f.status, f.updated_at, i.status, i.updated_at,
	       COALESCE(f.job_id, i.job_id, c.job_id), j.status, j.updated_at
	FROM selected s
	LEFT JOIN automation.schedule_fires f ON f.schedule_fire_id = s.last_schedule_fire_id AND f.schedule_id = s.schedule_id
	LEFT JOIN automation.invocations i ON i.invocation_id = f.invocation_id
	LEFT JOIN routing.capability_calls c ON c.capability_call_id = COALESCE(f.capability_call_id, i.capability_call_id)
	LEFT JOIN jobs.jobs j ON j.job_id = COALESCE(f.job_id, i.job_id, c.job_id)
	ORDER BY s.created_at DESC, s.schedule_id DESC`, limitParam)
}

func scanOverviewRow(scanner rowScanner) (overviewRow, error) {
	var row overviewRow
	var start, next, last, fireAt, invocationAt, jobAt sql.NullTime
	var fireID, fireStatus, invocationStatus, jobID, jobStatus sql.NullString
	item := &row.Item
	err := scanner.Scan(&item.ScheduleID, &item.DisplayName, &item.ScheduleKey, &item.Status, &item.ScheduleKind,
		&item.ScheduleExpr, &item.Timezone, &start, &next, &last, &fireID, &row.CreatedAt, &item.MetadataLimited,
		&fireStatus, &fireAt, &invocationStatus, &invocationAt, &jobID, &jobStatus, &jobAt)
	if err != nil {
		return row, err
	}
	item.NextFireAt, item.LastFireAt, item.LastScheduleFireID = timePtr(next), timePtr(last), stringPtr(fireID)
	row.Timing = Schedule{ScheduleID: item.ScheduleID, Status: item.Status, ScheduleKind: item.ScheduleKind,
		ScheduleExpr: item.ScheduleExpr, Timezone: item.Timezone, StartAt: timePtr(start), NextFireAt: item.NextFireAt}
	if jobID.Valid {
		if jobStatus.Valid {
			item.LastResult = &OverviewResult{Source: "job", Status: overviewEvidenceStatus(jobStatus.String), At: jobAt.Time.UTC()}
		}
		// Missing linked job evidence must not fall back to transport success.
	} else if invocationStatus.Valid {
		item.LastResult = &OverviewResult{Source: "invocation", Status: overviewEvidenceStatus(invocationStatus.String), At: invocationAt.Time.UTC()}
	} else if fireStatus.Valid {
		item.LastResult = &OverviewResult{Source: "schedule_fire", Status: overviewEvidenceStatus(fireStatus.String), At: fireAt.Time.UTC()}
	}
	if item.LastResult != nil && item.LastResult.Source != "job" &&
		(item.LastResult.Status == "succeeded" || item.LastResult.Status == "completed") {
		item.LastResult.Status = "dispatched"
	}
	for _, field := range []*string{&item.DisplayName, &item.ScheduleKey, &item.ScheduleExpr, &item.Timezone} {
		clean := compactOverviewText(*field)
		item.MetadataLimited = item.MetadataLimited || clean != *field
		*field = clean
	}
	return row, nil
}

func compactOverviewText(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	if len(value) > 1024 {
		value = value[:1024]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}

func overviewEvidenceStatus(status string) string {
	switch status {
	case "created", "queued", "running", "completed", "failed", "cancelled", "timed_out",
		"pending", "leased", "calling", "waiting_approval", "succeeded", "requires_manual_action",
		"missed", "skipped", "pending_invocation", "invocation_created":
		return status
	default:
		return "unknown"
	}
}

func overviewOccurrences(ctx context.Context, selected []overviewRow, window OverviewWindow) ([]OverviewOccurrence, bool, []string, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, nil, err
	}
	next := make([]*time.Time, len(selected))
	unavailable := []string{}
	for index, row := range selected {
		if err := ctx.Err(); err != nil {
			return nil, false, nil, err
		}
		candidate, err := firstOverviewOccurrence(row.Timing, window.Start)
		if err != nil {
			unavailable = append(unavailable, row.Item.ScheduleID)
			continue
		}
		next[index] = candidate
	}
	occurrences := []OverviewOccurrence{}
	// Round robin over the selected schedules, then sort for display. Each
	// eligible schedule gets one occurrence before any gets a second.
	for len(occurrences) < maxOverviewOccurrences {
		added := false
		for index, row := range selected {
			if err := ctx.Err(); err != nil {
				return nil, false, nil, err
			}
			at := next[index]
			if at == nil || !at.Before(window.End) {
				continue
			}
			occurrences = append(occurrences, OverviewOccurrence{ScheduleID: row.Item.ScheduleID, At: at.UTC()})
			added = true
			following, err := ComputeNextFire(row.Timing, *at)
			if err != nil || (following != nil && !following.After(*at)) {
				unavailable = append(unavailable, row.Item.ScheduleID)
				next[index] = nil
			} else {
				next[index] = following
			}
			if len(occurrences) == maxOverviewOccurrences {
				break
			}
		}
		if !added {
			break
		}
	}
	truncated := false
	for _, at := range next {
		truncated = truncated || (at != nil && at.Before(window.End))
	}
	sort.Slice(occurrences, func(i, j int) bool {
		if occurrences[i].At.Equal(occurrences[j].At) {
			return occurrences[i].ScheduleID < occurrences[j].ScheduleID
		}
		return occurrences[i].At.Before(occurrences[j].At)
	})
	return occurrences, truncated, unavailable, nil
}

func firstOverviewOccurrence(schedule Schedule, start time.Time) (*time.Time, error) {
	if schedule.Status != ScheduleStatusActive {
		return nil, nil
	}
	if schedule.NextFireAt == nil {
		return nil, fmt.Errorf("active schedule has no next fire")
	}
	validationAfter := *schedule.NextFireAt
	if schedule.ScheduleKind == ScheduleKindCron {
		validationAfter = validationAfter.Add(-time.Nanosecond)
	}
	following, err := ComputeNextFire(schedule, validationAfter)
	if err != nil {
		return nil, err
	}
	lower := start.UTC()
	if schedule.StartAt != nil && schedule.StartAt.After(lower) {
		lower = schedule.StartAt.UTC()
	}
	if !schedule.NextFireAt.Before(lower) {
		at := schedule.NextFireAt.UTC()
		return &at, nil
	}
	switch schedule.ScheduleKind {
	case ScheduleKindOneShot:
		return nil, nil
	case ScheduleKindCron:
		return ComputeNextFire(schedule, lower.Add(-time.Nanosecond))
	case ScheduleKindInterval:
		// Reuse interval validation/duration, jumping over old nominal ticks in
		// O(1) without replaying downtime or shifting the stored phase.
		duration := following.Sub(*schedule.NextFireAt)
		elapsed := lower.Sub(*schedule.NextFireAt)
		at := lower.Add(-elapsed % duration)
		if at.Before(lower) {
			at = at.Add(duration)
		}
		return &at, nil
	default:
		return nil, fmt.Errorf("unsupported schedule kind")
	}
}
