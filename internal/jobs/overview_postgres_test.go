package jobs

import (
	"database/sql"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Use only a transaction-local table on the configured test connection. No
// migrations, bootstrap, real jobs or project state are needed for these reads.
func TestOverviewPredicatesPostgres(t *testing.T) {
	url := os.Getenv("LOOM_TEST_DB_URL")
	if url == "" {
		t.Skip("LOOM_TEST_DB_URL is unset")
	}
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(t.Context(), `CREATE TEMP TABLE overview_jobs (
		job_id text, job_type text DEFAULT 'script_run', status text,
		failure_attention_status text, manual_action_required boolean,
		scope_id text, created_at timestamptz, updated_at timestamptz,
		queued_at timestamptz, started_at timestamptz, completed_at timestamptz,
		failed_at timestamptz, cancelled_at timestamptz
	) ON COMMIT DROP`); err != nil {
		t.Fatal(err)
	}
	for _, hours := range []int{1, 24} {
		t.Run((time.Duration(hours) * time.Hour).String(), func(t *testing.T) {
			if _, err := tx.ExecContext(t.Context(), `TRUNCATE pg_temp.overview_jobs`); err != nil {
				t.Fatal(err)
			}
			end := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
			start := end.Add(-time.Duration(hours) * time.Hour)
			buckets := 60
			if hours == 24 {
				buckets = 96
			}
			stride := end.Sub(start) / time.Duration(buckets)
			first, second, last := start.Add(stride), start.Add(2*stride), end.Add(-time.Microsecond)
			insert := func(id, status, attention string, manual bool, scope any, updated time.Time, completed, failed, cancelled any) {
				t.Helper()
				if _, err := tx.ExecContext(t.Context(), `INSERT INTO pg_temp.overview_jobs
					(job_id,status,failure_attention_status,manual_action_required,scope_id,
					created_at,updated_at,completed_at,failed_at,cancelled_at)
					VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
					id, status, attention, manual, scope, start.Add(-time.Hour), updated, completed, failed, cancelled); err != nil {
					t.Fatal(err)
				}
			}
			insert("job_old_queued", StatusQueued, FailureAttentionStatusActive, false, "scope_a", start.Add(-time.Hour), nil, nil, nil)
			old := start.Add(-48 * time.Hour)
			insert("job_old_unresolved", StatusFailed, FailureAttentionStatusActive, true, "scope_a", old, nil, old, nil)
			insert("job_old_resolved", StatusFailed, FailureAttentionStatusArchived, true, "scope_a", old, nil, old, nil)
			insert("job_running", StatusRunning, FailureAttentionStatusActive, false, "scope_a", first, nil, nil, nil)
			insert("job_failed_manual", StatusFailed, FailureAttentionStatusActive, true, "scope_a", first, nil, start, nil)
			insert("job_timeout", StatusTimedOut, FailureAttentionStatusActive, false, "scope_a", first, nil, first, nil)
			insert("job_manual_queued", StatusQueued, FailureAttentionStatusActive, true, "scope_a", first, nil, nil, nil)
			insert("job_acknowledged", StatusFailed, FailureAttentionStatusAcknowledged, true, "scope_a", first, nil, first, nil)
			insert("job_archived", StatusTimedOut, FailureAttentionStatusArchived, true, "scope_a", first, nil, first, nil)
			insert("job_retry_success", StatusCompleted, FailureAttentionStatusArchived, false, "scope_a", old, second, first, nil)
			insert("job_retry_queued", StatusQueued, FailureAttentionStatusActive, false, "scope_a", second, nil, first, nil)
			insert("job_cancelled", StatusCancelled, FailureAttentionStatusArchived, false, "scope_a", last, first, first, last)
			insert("job_before_start", StatusCompleted, FailureAttentionStatusArchived, false, "scope_a", second, start.Add(-time.Microsecond), first, nil)
			insert("job_at_end", StatusFailed, FailureAttentionStatusActive, false, "scope_a", end, first, end, nil)
			insert("job_null_time", StatusFailed, FailureAttentionStatusActive, false, "scope_a", first, first, nil, nil)
			insert("job_created", StatusCreated, FailureAttentionStatusActive, false, "scope_a", first, first, first, first)
			insert("job_other", StatusFailed, FailureAttentionStatusActive, false, "scope_b", first, nil, start, nil)
			insert("job_unscoped", StatusQueued, FailureAttentionStatusActive, false, nil, first, nil, nil, nil)
			insert("job_tie_a", StatusCompleted, FailureAttentionStatusArchived, false, "scope_a", last, nil, nil, nil)
			insert("job_tie_b", StatusCompleted, FailureAttentionStatusArchived, false, "scope_a", last, nil, nil, nil)

			for _, scoped := range []bool{true, false} {
				counts, outcomes := overviewQueries(scoped)
				counts = strings.ReplaceAll(counts, "jobs.jobs", "pg_temp.overview_jobs")
				outcomes = strings.ReplaceAll(outcomes, "jobs.jobs", "pg_temp.overview_jobs")
				countArgs := []any{}
				outcomeArgs := []any{start, end, stride.Seconds()}
				wantCounts := OverviewCounts{Queued: 4, Running: 1, Attention: 7}
				wantOutcomes := map[int][3]int64{0: {0, 2, 0}, 1: {0, 3, 0}, 2: {1, 0, 0}, buckets - 1: {0, 0, 1}}
				scopeID := ""
				if scoped {
					scopeID = "scope_a"
					countArgs = append(countArgs, "scope_a")
					outcomeArgs = append(outcomeArgs, "scope_a")
					wantCounts = OverviewCounts{Queued: 3, Running: 1, Attention: 6}
					wantOutcomes[0] = [3]int64{0, 1, 0}
				}
				var gotCounts OverviewCounts
				if err := tx.QueryRowContext(t.Context(), counts, countArgs...).Scan(&gotCounts.Queued, &gotCounts.Running, &gotCounts.Attention); err != nil {
					t.Fatal(err)
				}
				if gotCounts != wantCounts {
					t.Fatalf("scoped=%v counts=%+v want=%+v", scoped, gotCounts, wantCounts)
				}
				rows, err := tx.QueryContext(t.Context(), outcomes, outcomeArgs...)
				if err != nil {
					t.Fatal(err)
				}
				gotOutcomes := map[int][3]int64{}
				for rows.Next() {
					var index int
					var values [3]int64
					if err := rows.Scan(&index, &values[0], &values[1], &values[2]); err != nil {
						t.Fatal(err)
					}
					gotOutcomes[index] = values
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				rows.Close()
				if !reflect.DeepEqual(gotOutcomes, wantOutcomes) {
					t.Fatalf("scoped=%v outcomes=%v want=%v", scoped, gotOutcomes, wantOutcomes)
				}
				readItems := func(state string, window *OverviewWindow, limit int) []OverviewItem {
					t.Helper()
					itemsSQL, args := overviewItemsQuery(state, window, scopeID, limit)
					itemsSQL = strings.ReplaceAll(itemsSQL, "jobs.jobs", "pg_temp.overview_jobs")
					rows, err := tx.QueryContext(t.Context(), itemsSQL, args...)
					if err != nil {
						t.Fatal(err)
					}
					defer rows.Close()
					var items []OverviewItem
					for rows.Next() {
						var item OverviewItem
						if err := rows.Scan(&item.JobID, &item.JobType, &item.Status, &item.CreatedAt, &item.UpdatedAt, &item.QueuedAt, &item.StartedAt, &item.CompletedAt, &item.FailedAt, &item.CancelledAt, &item.FailureAttentionStatus, &item.ManualActionRequired, &item.ScopeID); err != nil {
							t.Fatal(err)
						}
						items = append(items, item)
					}
					if err := rows.Err(); err != nil {
						t.Fatal(err)
					}
					return items
				}
				var ids []string
				for _, item := range readItems("all", &OverviewWindow{Start: start, End: end}, 2) {
					ids = append(ids, item.JobID)
				}
				if !reflect.DeepEqual(ids, []string{"job_tie_b", "job_tie_a"}) {
					t.Fatalf("recent stable bounded order: %v", ids)
				}
				attention := readItems("attention", nil, 100)
				if int64(len(attention)) != wantCounts.Attention {
					t.Fatalf("attention items do not match current count: %+v", attention)
				}
				foundOld := false
				for _, item := range attention {
					if item.JobID == "job_old_unresolved" {
						foundOld = item.ManualActionRequired
					}
					if item.FailureAttentionStatus != FailureAttentionStatusActive {
						t.Fatalf("resolved failure returned as blocker: %+v", item)
					}
				}
				if !foundOld {
					t.Fatal("old unresolved manual failure absent from attention source query")
				}
				if int64(len(readItems("queued", nil, 100))) != wantCounts.Queued || int64(len(readItems("running", nil, 100))) != wantCounts.Running {
					t.Fatal("current-state source queries do not match counts")
				}
				finished := readItems("completed", &OverviewWindow{Start: start, End: end}, 100)
				var wantFinished int64
				for _, values := range wantOutcomes {
					wantFinished += values[0] + values[1] + values[2]
				}
				if int64(len(finished)) != wantFinished {
					t.Fatalf("finished items do not match graph outcomes: %+v", finished)
				}
				bucketItems := readItems("completed", &OverviewWindow{Start: second, End: second.Add(stride)}, 100)
				if len(bucketItems) != 1 || bucketItems[0].JobID != "job_retry_success" || !bucketItems[0].UpdatedAt.Before(start) {
					t.Fatalf("bucket used update time or stale failure instead of latest terminal outcome: %+v", bucketItems)
				}
			}
		})
	}
}
